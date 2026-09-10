// Package baselinebundle reconstructs an exact frozen baseline from an
// immutable installed source tree and a sparse historical delta.
package baselinebundle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	SchemaVersionV4 = "chora.source-baseline.v4"
	SchemaVersionV5 = "chora.source-baseline.v5"
	SchemaVersionV6 = "chora.source-baseline.v6"
	SchemaVersion   = SchemaVersionV6
)

var (
	ErrInvalidRequest  = errors.New("invalid baseline reconstruction request")
	ErrInvalidManifest = errors.New("invalid frozen baseline manifest")
	ErrSourceIdentity  = errors.New("installed source identity mismatch")
	ErrDeltaIdentity   = errors.New("historical delta identity mismatch")
	ErrTargetIdentity  = errors.New("reconstructed baseline identity mismatch")
)

type Entry struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Mode   string `json:"mode"`
}

type Manifest struct {
	SchemaVersion         string   `json:"schema_version"`
	SourceRevision        string   `json:"source_revision"`
	TaskID                string   `json:"task_id"`
	ContextSnapshotID     string   `json:"context_snapshot_id"`
	ContextSnapshotDigest string   `json:"context_snapshot_digest"`
	InclusionRules        []string `json:"inclusion_rules"`
	ExclusionRules        []string `json:"exclusion_rules"`
	EntryCount            int      `json:"entry_count"`
	AggregateSHA256       string   `json:"aggregate_sha256"`
	Entries               []Entry  `json:"entries"`
}

type Request struct {
	InstalledRoot string
	DeltaRoot     string
	TargetRoot    string
	Manifest      []byte
}

type Result struct {
	SchemaVersion   string
	SourceRevision  string
	AggregateSHA256 string
	EntryCount      int
	DeltaEntries    int
}

// Reconstruct atomically publishes TargetRoot only after the selected files and
// the completed tree have both passed the manifest identity checks.
func Reconstruct(ctx context.Context, request Request) (_ Result, returnErr error) {
	if ctx == nil {
		return Result{}, fmt.Errorf("%w: nil context", ErrInvalidRequest)
	}
	manifest, err := decodeManifest(request.Manifest)
	if err != nil {
		return Result{}, err
	}
	installed, err := existingRoot(request.InstalledRoot)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrSourceIdentity, err)
	}
	delta, err := existingRoot(request.DeltaRoot)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrDeltaIdentity, err)
	}
	target, err := freshRoot(request.TargetRoot)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	parent := filepath.Dir(target)
	if info, err := os.Lstat(parent); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Result{}, fmt.Errorf("%w: target parent must be an existing real directory", ErrInvalidRequest)
	}
	installedIdentity, installedErr := filepath.EvalSymlinks(installed)
	deltaIdentity, deltaErr := filepath.EvalSymlinks(delta)
	parentIdentity, parentErr := filepath.EvalSymlinks(parent)
	if installedErr != nil || deltaErr != nil || parentErr != nil {
		return Result{}, fmt.Errorf("%w: roots cannot be resolved", ErrInvalidRequest)
	}
	targetIdentity := filepath.Join(parentIdentity, filepath.Base(target))
	// The sparse delta is shipped inside the immutable installed source tree.
	// The publication target must remain disjoint from both inputs.
	if installedIdentity == deltaIdentity || rootsOverlap(installedIdentity, targetIdentity) || rootsOverlap(deltaIdentity, targetIdentity) {
		return Result{}, fmt.Errorf("%w: target must be disjoint from both inputs", ErrInvalidRequest)
	}

	declared := make(map[string]Entry, len(manifest.Entries))
	allowedDirectories := map[string]struct{}{".": {}}
	for _, entry := range manifest.Entries {
		declared[entry.Path] = entry
		for directory := path.Dir(entry.Path); directory != "."; directory = path.Dir(directory) {
			allowedDirectories[directory] = struct{}{}
		}
	}
	deltaFiles, err := inspectDelta(delta, declared, allowedDirectories)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrDeltaIdentity, err)
	}

	stage, err := os.MkdirTemp(parent, "."+filepath.Base(target)+".partial-")
	if err != nil {
		return Result{}, fmt.Errorf("%w: create staging root: %v", ErrTargetIdentity, err)
	}
	if err := os.Chmod(stage, 0o700); err != nil {
		_ = os.RemoveAll(stage)
		return Result{}, fmt.Errorf("%w: secure staging root: %v", ErrTargetIdentity, err)
	}
	published := false
	defer func() {
		_ = os.RemoveAll(stage)
		if returnErr != nil && published {
			_ = os.RemoveAll(target)
		}
	}()

	for _, entry := range manifest.Entries {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		sourceRoot := installed
		sourceError := ErrSourceIdentity
		requireSourceMode := false
		if _, ok := deltaFiles[entry.Path]; ok {
			sourceRoot = delta
			sourceError = ErrDeltaIdentity
			requireSourceMode = true
		}
		if err := copyEntry(ctx, sourceRoot, stage, entry, requireSourceMode); err != nil {
			return Result{}, fmt.Errorf("%w: %s: %v", sourceError, entry.Path, err)
		}
	}
	if _, err := Verify(stage, request.Manifest); err != nil {
		return Result{}, err
	}
	if err := os.Rename(stage, target); err != nil {
		return Result{}, fmt.Errorf("%w: publish: %v", ErrTargetIdentity, err)
	}
	published = true
	result, err := Verify(target, request.Manifest)
	if err != nil {
		return Result{}, err
	}
	result.DeltaEntries = len(deltaFiles)
	return result, nil
}

// Verify proves exact file and derived-directory coverage of a reconstructed
// owner-only root against a supported manifest.
func Verify(root string, manifestBytes []byte) (Result, error) {
	manifest, err := decodeManifest(manifestBytes)
	if err != nil {
		return Result{}, err
	}
	root, err = existingRoot(root)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrTargetIdentity, err)
	}
	rootInfo, err := os.Lstat(root)
	if err != nil || rootInfo.Mode().Perm() != 0o700 {
		return Result{}, fmt.Errorf("%w: root is not owner-only", ErrTargetIdentity)
	}

	declared := make(map[string]Entry, len(manifest.Entries))
	directories := map[string]struct{}{".": {}}
	for _, entry := range manifest.Entries {
		declared[entry.Path] = entry
		for directory := path.Dir(entry.Path); directory != "."; directory = path.Dir(directory) {
			directories[directory] = struct{}{}
		}
	}
	seen := make(map[string]struct{}, len(declared))
	err = filepath.WalkDir(root, func(name string, directoryEntry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative == "." {
			return nil
		}
		info, err := directoryEntry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("unsupported symlink: %s", relative)
		}
		if directoryEntry.IsDir() {
			if _, ok := directories[relative]; !ok || info.Mode().Perm() != 0o700 {
				return fmt.Errorf("extra or unsafe directory: %s", relative)
			}
			return nil
		}
		entry, ok := declared[relative]
		if !ok || !info.Mode().IsRegular() {
			return fmt.Errorf("extra or unsupported file: %s", relative)
		}
		if _, duplicate := seen[relative]; duplicate {
			return fmt.Errorf("duplicate file: %s", relative)
		}
		if fmt.Sprintf("%04o", info.Mode().Perm()) != entry.Mode {
			return fmt.Errorf("mode mismatch: %s", relative)
		}
		digest, err := digestFile(name)
		if err != nil || digest != entry.SHA256 {
			return fmt.Errorf("SHA-256 mismatch: %s", relative)
		}
		seen[relative] = struct{}{}
		return nil
	})
	if err != nil || len(seen) != manifest.EntryCount {
		if err == nil {
			err = errors.New("file coverage mismatch")
		}
		return Result{}, fmt.Errorf("%w: %v", ErrTargetIdentity, err)
	}
	return resultFor(manifest), nil
}

func decodeManifest(encoded []byte) (Manifest, error) {
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if len(encoded) == 0 || decoder.Decode(&manifest) != nil {
		return Manifest{}, ErrInvalidManifest
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Manifest{}, fmt.Errorf("%w: trailing data", ErrInvalidManifest)
	}
	if !supportedSchemaVersion(manifest.SchemaVersion) || strings.TrimSpace(manifest.SourceRevision) == "" || strings.TrimSpace(manifest.TaskID) == "" ||
		strings.TrimSpace(manifest.ContextSnapshotID) == "" || !validDigest(manifest.ContextSnapshotDigest) || !validDigest(manifest.AggregateSHA256) ||
		manifest.EntryCount <= 0 || manifest.EntryCount != len(manifest.Entries) {
		return Manifest{}, ErrInvalidManifest
	}
	seen := make(map[string]struct{}, len(manifest.Entries))
	for _, entry := range manifest.Entries {
		if !validPath(entry.Path) || !validDigest(entry.SHA256) || entry.Mode != "0444" && entry.Mode != "0555" {
			return Manifest{}, fmt.Errorf("%w: invalid entry", ErrInvalidManifest)
		}
		if _, duplicate := seen[entry.Path]; duplicate {
			return Manifest{}, fmt.Errorf("%w: duplicate path %q", ErrInvalidManifest, entry.Path)
		}
		seen[entry.Path] = struct{}{}
	}
	for _, entry := range manifest.Entries {
		for parent := path.Dir(entry.Path); parent != "."; parent = path.Dir(parent) {
			if _, isFile := seen[parent]; isFile {
				return Manifest{}, fmt.Errorf("%w: file is also a directory %q", ErrInvalidManifest, parent)
			}
		}
	}
	if aggregateEntries(manifest.Entries) != manifest.AggregateSHA256 {
		return Manifest{}, fmt.Errorf("%w: aggregate mismatch", ErrInvalidManifest)
	}
	return manifest, nil
}

func supportedSchemaVersion(version string) bool {
	return version == SchemaVersionV4 || version == SchemaVersionV5 || version == SchemaVersionV6
}

func inspectDelta(root string, declared map[string]Entry, directories map[string]struct{}) (map[string]struct{}, error) {
	files := make(map[string]struct{})
	err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative == "." {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("unsupported symlink: %s", relative)
		}
		if entry.IsDir() {
			if _, ok := directories[relative]; !ok {
				return fmt.Errorf("extra directory: %s", relative)
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported entry: %s", relative)
		}
		if _, ok := declared[relative]; !ok {
			return fmt.Errorf("extra file: %s", relative)
		}
		files[relative] = struct{}{}
		return nil
	})
	return files, err
}

func copyEntry(ctx context.Context, sourceRoot, targetRoot string, entry Entry, requireSourceMode bool) error {
	input, info, err := openRegular(sourceRoot, entry.Path)
	if err != nil {
		return err
	}
	defer input.Close()
	// The installed product source has its own immutable source-manifest modes.
	// For unchanged content, the frozen v4 manifest is the authority for the
	// reconstructed mode. Historical delta files must already have that mode.
	if requireSourceMode && fmt.Sprintf("%04o", info.Mode().Perm()) != entry.Mode {
		return errors.New("source mode mismatch")
	}
	destination := filepath.Join(targetRoot, filepath.FromSlash(entry.Path))
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	modeValue, _ := strconv.ParseUint(entry.Mode, 8, 32)
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fs.FileMode(modeValue))
	if err != nil {
		return err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(output, hash), contextReader{ctx: ctx, reader: input})
	closeErr := output.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != entry.SHA256 {
		return errors.New("source SHA-256 mismatch")
	}
	return os.Chmod(destination, fs.FileMode(modeValue))
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader contextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}

func openRegular(root, relative string) (*os.File, os.FileInfo, error) {
	current := root
	components := strings.Split(relative, "/")
	for index, component := range components {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return nil, nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, nil, errors.New("source path contains a symlink")
		}
		if index < len(components)-1 && !info.IsDir() {
			return nil, nil, errors.New("source path component is not a directory")
		}
		if index == len(components)-1 && !info.Mode().IsRegular() {
			return nil, nil, errors.New("source is not a regular file")
		}
	}
	file, err := os.Open(current)
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, nil, errors.New("source identity changed while opening")
	}
	lstat, err := os.Lstat(current)
	if err != nil || !os.SameFile(info, lstat) {
		file.Close()
		return nil, nil, errors.New("source identity changed while opening")
	}
	return file, info, nil
}

func existingRoot(value string) (string, error) {
	clean, err := cleanAbsolute(value)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(clean)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("root must be an existing real directory")
	}
	return clean, nil
}

func freshRoot(value string) (string, error) {
	clean, err := cleanAbsolute(value)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(clean); err == nil {
		return "", errors.New("target root already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return clean, nil
}

func cleanAbsolute(value string) (string, error) {
	if !filepath.IsAbs(value) || filepath.Clean(value) != value || value == string(filepath.Separator) {
		return "", errors.New("root must be absolute, clean, and specific")
	}
	return value, nil
}

func rootsOverlap(left, right string) bool {
	return pathWithin(left, right) || pathWithin(right, left)
}

func pathWithin(candidate, parent string) bool {
	relative, err := filepath.Rel(parent, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func validPath(value string) bool {
	return value != "" && value != "." && !strings.Contains(value, "\\") && !strings.ContainsRune(value, 0) &&
		!strings.HasPrefix(value, "/") && path.Clean(value) == value && value != ".." && !strings.HasPrefix(value, "../")
}

func validDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && strings.ToLower(value) == value
}

func digestFile(name string) (string, error) {
	file, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func aggregateEntries(entries []Entry) string {
	hash := sha256.New()
	for _, entry := range entries {
		_, _ = fmt.Fprintf(hash, "%s\x00%s\x00%s\n", entry.Path, entry.SHA256, entry.Mode)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func resultFor(manifest Manifest) Result {
	return Result{SchemaVersion: manifest.SchemaVersion, SourceRevision: manifest.SourceRevision, AggregateSHA256: manifest.AggregateSHA256, EntryCount: manifest.EntryCount}
}
