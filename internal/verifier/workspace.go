package verifier

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
)

var (
	ErrInvalidWorkspace = errors.New("invalid verification workspace")
	ErrBaselineIdentity = errors.New("baseline identity mismatch")
	ErrPatchIdentity    = errors.New("patch identity mismatch")
	ErrPatchApply       = errors.New("patch is not applicable")
	ErrPatchBoundary    = errors.New("patch target boundary drift")
	ErrEmptyPatch       = errors.New("empty patch")
)

const M1FrozenBaselineDigestHex = "b28cb1624124736e745f2b217e841957330f763b6b41fab87b2a88b809b8a0cd"

const (
	verificationWorkspaceMarkerSchema = "chora.verification-workspace-root.v1"
	verificationWorkspaceMarkerName   = ".chora-verification-workspace.json"
)

type WorkspaceRequest struct {
	BaselineRoot           string
	BaselineManifest       []byte
	AgentWorkspace         string
	ExpectedBaselineDigest [32]byte
	Patch                  []byte
	ExpectedPatchDigest    [32]byte
	WritableFiles          []string
	VerificationAttemptID  string
	PolicyDigest           [32]byte
}

type WorkspaceLease interface {
	Root() string
	BaselineDigest() [32]byte
	PatchDigest() [32]byte
	TouchedFiles() []string
	Cleanup(context.Context) error
}

type WorkspaceFactory interface {
	Prepare(context.Context, WorkspaceRequest) (WorkspaceLease, error)
}

type FilesystemWorkspaceFactory struct {
	root          string
	git           string
	recoveryScope string
}

func NewFilesystemWorkspaceFactory(root, gitExecutable string) (*FilesystemWorkspaceFactory, error) {
	root = filepath.Clean(root)
	if !filepath.IsAbs(root) || root == string(filepath.Separator) || strings.TrimSpace(gitExecutable) == "" {
		return nil, ErrInvalidWorkspace
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("%w: create workspace root: %v", ErrInvalidWorkspace, err)
	}
	scope := sha256.Sum256([]byte(root))
	return &FilesystemWorkspaceFactory{root: root, git: gitExecutable, recoveryScope: "sha256:" + hex.EncodeToString(scope[:])}, nil
}

// Recover removes only attempt directories created beneath the dedicated
// verification workspace root and proves that no attempt workspace remains.
func (factory *FilesystemWorkspaceFactory) Recover(ctx context.Context) error {
	if factory == nil || !filepath.IsAbs(factory.root) || factory.root == string(filepath.Separator) {
		return ErrInvalidWorkspace
	}
	entries, err := factory.validatedWorkspaceEntries()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		path := filepath.Join(factory.root, entry.Name())
		if !pathWithin(path, factory.root) || samePath(factory.root, path) {
			return ErrInvalidWorkspace
		}
		if err := os.RemoveAll(path); err != nil {
			return err
		}
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			return ErrInvalidWorkspace
		}
	}
	remaining, err := os.ReadDir(factory.root)
	if err != nil || len(remaining) != 0 {
		return ErrInvalidWorkspace
	}
	return nil
}

func (factory *FilesystemWorkspaceFactory) Prepare(ctx context.Context, request WorkspaceRequest) (_ WorkspaceLease, returnErr error) {
	baseline := filepath.Clean(request.BaselineRoot)
	agent := filepath.Clean(request.AgentWorkspace)
	if !filepath.IsAbs(baseline) || !filepath.IsAbs(agent) || samePath(baseline, agent) || request.ExpectedBaselineDigest == ([32]byte{}) || request.ExpectedPatchDigest == ([32]byte{}) ||
		strings.TrimSpace(request.VerificationAttemptID) == "" || request.PolicyDigest == ([32]byte{}) {
		return nil, ErrInvalidWorkspace
	}
	if pathWithin(factory.root, baseline) {
		return nil, ErrInvalidWorkspace
	}
	if len(bytes.TrimSpace(request.Patch)) == 0 {
		return nil, ErrEmptyPatch
	}
	if sha256.Sum256(request.Patch) != request.ExpectedPatchDigest {
		return nil, ErrPatchIdentity
	}
	targets, err := patchTargets(request.Patch)
	if err != nil {
		return nil, err
	}
	allowed, err := normalizedPathSet(request.WritableFiles)
	if err != nil {
		return nil, ErrPatchBoundary
	}
	for _, target := range targets {
		if _, ok := allowed[target]; !ok {
			return nil, fmt.Errorf("%w: target %q is outside writable_files", ErrPatchBoundary, target)
		}
	}
	actualBaseline, err := verifyBaselineAuthority(baseline, request.BaselineManifest, request.ExpectedBaselineDigest)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBaselineIdentity, err)
	}
	root, err := os.MkdirTemp(factory.root, "verification-")
	if err != nil {
		return nil, fmt.Errorf("%w: create: %v", ErrInvalidWorkspace, err)
	}
	defer func() {
		if returnErr != nil {
			_ = os.RemoveAll(root)
		}
	}()
	if samePath(root, baseline) || samePath(root, agent) {
		return nil, ErrInvalidWorkspace
	}
	if err := copyTree(ctx, baseline, root); err != nil {
		return nil, fmt.Errorf("%w: materialize: %v", ErrBaselineIdentity, err)
	}
	copiedDigest, err := verifyBaselineAuthority(root, request.BaselineManifest, request.ExpectedBaselineDigest)
	if err != nil || copiedDigest != actualBaseline {
		return nil, ErrBaselineIdentity
	}
	before, err := treeManifest(root)
	if err != nil {
		return nil, ErrBaselineIdentity
	}
	if err := runGitApply(ctx, factory.git, root, request.Patch, true); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPatchApply, err)
	}
	if err := runGitApply(ctx, factory.git, root, request.Patch, false); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPatchApply, err)
	}
	after, err := treeManifest(root)
	if err != nil {
		return nil, fmt.Errorf("%w: inspect result: %v", ErrPatchApply, err)
	}
	changed := changedPaths(before, after)
	if len(changed) == 0 || !slices.Equal(changed, targets) {
		return nil, fmt.Errorf("%w: applied files %v do not match declared Patch targets %v", ErrPatchBoundary, changed, targets)
	}
	if err := prepareWorkspaceForContainer(root); err != nil {
		return nil, fmt.Errorf("%w: prepare container workspace: %v", ErrInvalidWorkspace, err)
	}
	if err := factory.writeWorkspaceMarker(root, request.VerificationAttemptID, request.PolicyDigest); err != nil {
		return nil, fmt.Errorf("%w: write immutable ownership marker: %v", ErrInvalidWorkspace, err)
	}
	return &filesystemLease{root: root, baseline: actualBaseline, patch: request.ExpectedPatchDigest, touched: targets}, nil
}

type verificationWorkspaceMarker struct {
	SchemaVersion         string `json:"schema_version"`
	Owner                 string `json:"owner"`
	RecoveryScope         string `json:"recovery_scope"`
	RootName              string `json:"root_name"`
	VerificationAttemptID string `json:"verification_attempt_id"`
	WorkspaceIdentity     string `json:"workspace_identity"`
	PolicyDigest          string `json:"policy_digest"`
}

type WorkspaceResidueObservation struct {
	Count           int    `json:"count"`
	AggregateSHA256 string `json:"aggregateSha256"`
}

func (factory *FilesystemWorkspaceFactory) ObserveWorkspaces() (WorkspaceResidueObservation, error) {
	entries, err := factory.validatedWorkspaceEntries()
	if err != nil {
		return WorkspaceResidueObservation{}, err
	}
	hash := sha256.New()
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(factory.root, entry.Name(), verificationWorkspaceMarkerName))
		if err != nil {
			return WorkspaceResidueObservation{}, ErrInvalidWorkspace
		}
		_, _ = hash.Write(data)
	}
	return WorkspaceResidueObservation{Count: len(entries), AggregateSHA256: hex.EncodeToString(hash.Sum(nil))}, nil
}

func (factory *FilesystemWorkspaceFactory) validatedWorkspaceEntries() ([]os.DirEntry, error) {
	if factory == nil || !filepath.IsAbs(factory.root) || factory.root == string(filepath.Separator) {
		return nil, ErrInvalidWorkspace
	}
	entries, err := os.ReadDir(factory.root)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "verification-") || factory.validateWorkspaceMarker(entry) != nil {
			return nil, ErrInvalidWorkspace
		}
	}
	return entries, nil
}

func (factory *FilesystemWorkspaceFactory) writeWorkspaceMarker(root, attemptID string, policyDigest [32]byte) error {
	marker := verificationWorkspaceMarker{
		SchemaVersion: verificationWorkspaceMarkerSchema, Owner: "verifier", RecoveryScope: factory.recoveryScope,
		RootName: filepath.Base(root), VerificationAttemptID: attemptID, WorkspaceIdentity: workspaceIdentity(root),
		PolicyDigest: hex.EncodeToString(policyDigest[:]),
	}
	data, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	path := filepath.Join(root, verificationWorkspaceMarkerName)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	created := true
	defer func() {
		if created {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Chmod(0o400); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	created = false
	return nil
}

func (factory *FilesystemWorkspaceFactory) validateWorkspaceMarker(entry os.DirEntry) error {
	path := filepath.Join(factory.root, entry.Name())
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !validVerificationRootName(entry.Name()) {
		return ErrInvalidWorkspace
	}
	markerPath := filepath.Join(path, verificationWorkspaceMarkerName)
	markerInfo, err := os.Lstat(markerPath)
	stat, ok := func() (*syscall.Stat_t, bool) {
		if err != nil {
			return nil, false
		}
		value, ok := markerInfo.Sys().(*syscall.Stat_t)
		return value, ok
	}()
	if err != nil || !ok || !markerInfo.Mode().IsRegular() || markerInfo.Mode()&os.ModeSymlink != 0 || markerInfo.Mode().Perm() != 0o400 || stat.Nlink != 1 || int(stat.Uid) != os.Geteuid() {
		return ErrInvalidWorkspace
	}
	data, err := os.ReadFile(markerPath)
	if err != nil || len(data) == 0 || len(data) > 16<<10 {
		return ErrInvalidWorkspace
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var marker verificationWorkspaceMarker
	if decoder.Decode(&marker) != nil {
		return ErrInvalidWorkspace
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrInvalidWorkspace
	}
	canonical, _ := json.Marshal(marker)
	if !bytes.Equal(data, append(canonical, '\n')) || marker.SchemaVersion != verificationWorkspaceMarkerSchema || marker.Owner != "verifier" ||
		marker.RecoveryScope != factory.recoveryScope || marker.RootName != entry.Name() || strings.TrimSpace(marker.VerificationAttemptID) == "" ||
		marker.WorkspaceIdentity != workspaceIdentity(path) || !validWorkspaceMarkerDigest(marker.PolicyDigest) {
		return ErrInvalidWorkspace
	}
	return nil
}

func validVerificationRootName(name string) bool {
	if !strings.HasPrefix(name, "verification-") || len(name) < len("verification-")+8 || len(name) > len("verification-")+32 {
		return false
	}
	for _, value := range name[len("verification-"):] {
		if value < '0' || value > '9' && value < 'a' || value > 'z' {
			return false
		}
	}
	return true
}

func validWorkspaceMarkerDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && strings.ToLower(value) == value
}

type frozenBaselineManifest struct {
	SchemaVersion         string                `json:"schema_version"`
	SourceRevision        string                `json:"source_revision"`
	TaskID                string                `json:"task_id"`
	ContextSnapshotID     string                `json:"context_snapshot_id"`
	ContextSnapshotDigest string                `json:"context_snapshot_digest"`
	InclusionRules        []string              `json:"inclusion_rules"`
	ExclusionRules        []string              `json:"exclusion_rules"`
	EntryCount            int                   `json:"entry_count"`
	AggregateSHA256       string                `json:"aggregate_sha256"`
	Entries               []frozenBaselineEntry `json:"entries"`
}

type frozenBaselineEntry struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Mode   string `json:"mode"`
}

func verifyBaselineAuthority(root string, manifestBytes []byte, expected [32]byte) ([32]byte, error) {
	if len(manifestBytes) == 0 {
		actual, err := DigestTree(root)
		if err != nil || actual != expected {
			return [32]byte{}, ErrBaselineIdentity
		}
		return actual, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(manifestBytes))
	decoder.DisallowUnknownFields()
	var manifest frozenBaselineManifest
	if err := decoder.Decode(&manifest); err != nil {
		return [32]byte{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return [32]byte{}, errors.New("baseline manifest has trailing data")
	}
	if manifest.SchemaVersion != "chora.source-baseline.v4" && manifest.SchemaVersion != "chora.source-baseline.v5" && manifest.SchemaVersion != "chora.source-baseline.v6" || strings.TrimSpace(manifest.SourceRevision) == "" || strings.TrimSpace(manifest.TaskID) == "" ||
		strings.TrimSpace(manifest.ContextSnapshotID) == "" || len(manifest.ContextSnapshotDigest) != 64 || manifest.EntryCount <= 0 || manifest.EntryCount != len(manifest.Entries) {
		return [32]byte{}, ErrBaselineIdentity
	}
	manifestDigest, err := hex.DecodeString(manifest.AggregateSHA256)
	if err != nil || len(manifestDigest) != sha256.Size {
		return [32]byte{}, ErrBaselineIdentity
	}
	var declared [32]byte
	copy(declared[:], manifestDigest)
	if declared != expected {
		return [32]byte{}, ErrBaselineIdentity
	}
	actualFiles, err := treeManifest(root)
	if err != nil || len(actualFiles) != manifest.EntryCount {
		return [32]byte{}, ErrBaselineIdentity
	}
	canonical := sha256.New()
	seen := make(map[string]struct{}, manifest.EntryCount)
	for _, entry := range manifest.Entries {
		if !validRelativePath(entry.Path) || len(entry.SHA256) != 64 || entry.Mode != "0444" && entry.Mode != "0555" {
			return [32]byte{}, ErrBaselineIdentity
		}
		if _, duplicate := seen[entry.Path]; duplicate {
			return [32]byte{}, ErrBaselineIdentity
		}
		seen[entry.Path] = struct{}{}
		actual, ok := actualFiles[entry.Path]
		if !ok || hex.EncodeToString(actual.digest[:]) != entry.SHA256 || fmt.Sprintf("%04o", actual.mode.Perm()) != entry.Mode {
			return [32]byte{}, ErrBaselineIdentity
		}
		_, _ = fmt.Fprintf(canonical, "%s\x00%s\x00%s\n", entry.Path, entry.SHA256, entry.Mode)
	}
	var aggregate [32]byte
	copy(aggregate[:], canonical.Sum(nil))
	if aggregate != declared {
		return [32]byte{}, ErrBaselineIdentity
	}
	return aggregate, nil
}

func prepareWorkspaceForContainer(root string) error {
	if err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return os.Chmod(name, 0o755)
		}
		return nil
	}); err != nil {
		return err
	}
	for _, relative := range []string{".chora-cache/go-build", ".chora-tmp"} {
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(path, 0o777); err != nil {
			return err
		}
		if err := os.Chmod(path, 0o777); err != nil {
			return err
		}
	}
	return nil
}

type filesystemLease struct {
	mu       sync.Mutex
	root     string
	baseline [32]byte
	patch    [32]byte
	touched  []string
	cleaned  bool
}

func (lease *filesystemLease) Root() string             { return lease.root }
func (lease *filesystemLease) BaselineDigest() [32]byte { return lease.baseline }
func (lease *filesystemLease) PatchDigest() [32]byte    { return lease.patch }
func (lease *filesystemLease) TouchedFiles() []string   { return slices.Clone(lease.touched) }
func (lease *filesystemLease) Cleanup(context.Context) error {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.cleaned {
		return nil
	}
	if err := os.RemoveAll(lease.root); err != nil {
		return err
	}
	if _, err := os.Stat(lease.root); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return errors.New("workspace still exists")
		}
		return err
	}
	lease.cleaned = true
	return nil
}

func DigestTree(root string) ([32]byte, error) {
	manifest, err := treeManifest(root)
	if err != nil {
		return [32]byte{}, err
	}
	paths := make([]string, 0, len(manifest))
	for name := range manifest {
		paths = append(paths, name)
	}
	sort.Strings(paths)
	hash := sha256.New()
	for _, name := range paths {
		entry := manifest[name]
		_, _ = hash.Write([]byte(name))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(hex.EncodeToString(entry.digest[:])))
		_, _ = hash.Write([]byte{0})
		_, _ = fmt.Fprintf(hash, "%04o\n", entry.mode.Perm())
	}
	var digest [32]byte
	copy(digest[:], hash.Sum(nil))
	return digest, nil
}

type manifestEntry struct {
	mode   fs.FileMode
	digest [32]byte
}

func treeManifest(root string) (map[string]manifestEntry, error) {
	root = filepath.Clean(root)
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return nil, ErrInvalidWorkspace
	}
	manifest := map[string]manifestEntry{}
	err = filepath.WalkDir(root, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, name)
		if err != nil || rel == "." {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == ".git" && entry.IsDir() {
			return filepath.SkipDir
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() && !info.IsDir() {
			return fmt.Errorf("unsupported baseline entry %s", rel)
		}
		if info.IsDir() {
			return nil
		}
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		manifest[rel] = manifestEntry{mode: info.Mode(), digest: sha256.Sum256(data)}
		return nil
	})
	return manifest, err
}

func copyTree(ctx context.Context, source, destination string) error {
	return filepath.WalkDir(source, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(source, name)
		if err != nil || rel == "." {
			return err
		}
		if rel == ".git" && entry.IsDir() {
			return filepath.SkipDir
		}
		target := filepath.Join(destination, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() && !info.IsDir() {
			return fmt.Errorf("unsupported entry %s", rel)
		}
		if info.IsDir() {
			// Frozen source directories may be 0555. The owner-controlled
			// materializer needs temporary write authority to populate children;
			// file modes remain frozen and directories are normalized before the
			// verifier container starts.
			return os.MkdirAll(target, 0o755)
		}
		input, err := os.Open(name)
		if err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
		if err == nil {
			_, err = io.Copy(output, input)
		}
		closeOut := error(nil)
		if output != nil {
			closeOut = output.Close()
		}
		closeIn := input.Close()
		return errors.Join(err, closeOut, closeIn)
	})
}

func runGitApply(ctx context.Context, executable, root string, patch []byte, check bool) error {
	applyArguments := []string{"apply", "--whitespace=nowarn", "--recount"}
	if check {
		applyArguments = append(applyArguments, "--check")
	}
	args := []string{
		"--no-replace-objects",
		"-c", "core.hooksPath=/dev/null",
		"-c", "core.fsmonitor=false",
		"-c", "protocol.allow=never",
		"-c", "gc.auto=0",
		"-C", root,
	}
	args = append(args, applyArguments...)
	command := exec.CommandContext(ctx, executable, args...)
	command.Dir = root
	command.Stdin = bytes.NewReader(patch)
	command.Env = isolatedGitApplyEnvironment(root)
	var diagnostic bytes.Buffer
	command.Stderr = &diagnostic
	if err := command.Run(); err != nil {
		if diagnostic.Len() > 4096 {
			diagnostic.Truncate(4096)
		}
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(diagnostic.String()))
	}
	return nil
}

func isolatedGitApplyEnvironment(root string) []string {
	environment := make([]string, 0, len(os.Environ())+11)
	for _, entry := range os.Environ() {
		key := entry
		if index := strings.IndexByte(entry, '='); index >= 0 {
			key = entry[:index]
		}
		if strings.HasPrefix(key, "GIT_") || key == "HOME" ||
			key == "XDG_CONFIG_HOME" || key == "SSH_ASKPASS" {
			continue
		}
		environment = append(environment, entry)
	}
	return append(environment,
		"HOME=/var/empty",
		"XDG_CONFIG_HOME=/var/empty",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_NO_LAZY_FETCH=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=/usr/bin/false",
		"SSH_ASKPASS=/usr/bin/false",
		"GIT_CEILING_DIRECTORIES="+filepath.Dir(filepath.Clean(root)),
	)
}

func patchTargets(patch []byte) ([]string, error) {
	scanner := bufio.NewScanner(bytes.NewReader(patch))
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	var oldName string
	set := map[string]struct{}{}
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "--- ") {
			oldName = patchHeaderPath(strings.TrimPrefix(line, "--- "), "a/")
			if oldName == "" && !strings.HasPrefix(strings.TrimPrefix(line, "--- "), "/dev/null") {
				return nil, ErrPatchBoundary
			}
			continue
		}
		if strings.HasPrefix(line, "+++ ") {
			newName := patchHeaderPath(strings.TrimPrefix(line, "+++ "), "b/")
			if oldName == "" && newName == "" {
				return nil, ErrPatchBoundary
			}
			if oldName != "" && newName != "" && oldName != newName {
				return nil, ErrPatchBoundary
			}
			target := newName
			if target == "" {
				target = oldName
			}
			if !validRelativePath(target) {
				return nil, ErrPatchBoundary
			}
			set[target] = struct{}{}
			oldName = ""
		}
	}
	if err := scanner.Err(); err != nil || len(set) == 0 || oldName != "" {
		return nil, ErrPatchBoundary
	}
	targets := make([]string, 0, len(set))
	for target := range set {
		targets = append(targets, target)
	}
	sort.Strings(targets)
	return targets, nil
}

func patchHeaderPath(value, prefix string) string {
	value, _, _ = strings.Cut(value, "\t")
	if value == "/dev/null" {
		return ""
	}
	if strings.HasPrefix(value, "\"") || !strings.HasPrefix(value, prefix) {
		return ""
	}
	return strings.TrimPrefix(value, prefix)
}

func normalizedPathSet(paths []string) (map[string]struct{}, error) {
	if len(paths) == 0 {
		return nil, ErrPatchBoundary
	}
	set := make(map[string]struct{}, len(paths))
	for _, name := range paths {
		name = filepath.ToSlash(name)
		if !validRelativePath(name) {
			return nil, ErrPatchBoundary
		}
		if _, exists := set[name]; exists {
			return nil, ErrPatchBoundary
		}
		set[name] = struct{}{}
	}
	return set, nil
}

func validRelativePath(name string) bool {
	clean := filepath.ToSlash(filepath.Clean(name))
	return name != "" && clean == name && clean != "." && !strings.HasPrefix(clean, "../") && !filepath.IsAbs(name) && !strings.ContainsRune(name, '\x00')
}

func changedPaths(before, after map[string]manifestEntry) []string {
	set := map[string]struct{}{}
	for name, old := range before {
		if current, ok := after[name]; !ok || current != old {
			set[name] = struct{}{}
		}
	}
	for name, current := range after {
		if old, ok := before[name]; !ok || old != current {
			set[name] = struct{}{}
		}
	}
	paths := make([]string, 0, len(set))
	for name := range set {
		paths = append(paths, name)
	}
	sort.Strings(paths)
	return paths
}

func samePath(left, right string) bool {
	leftResolved, leftErr := filepath.EvalSymlinks(left)
	rightResolved, rightErr := filepath.EvalSymlinks(right)
	if leftErr == nil {
		left = leftResolved
	}
	if rightErr == nil {
		right = rightResolved
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

func workspaceIdentity(root string) string {
	resolved, err := filepath.EvalSymlinks(root)
	if err == nil {
		root = resolved
	}
	digest := sha256.Sum256([]byte(filepath.Clean(root)))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func pathWithin(candidate, parent string) bool {
	rel, err := filepath.Rel(filepath.Clean(parent), filepath.Clean(candidate))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
