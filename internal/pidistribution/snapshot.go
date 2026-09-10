package pidistribution

import (
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
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

const snapshotSchema = "chora.pi-runtime-snapshot.v1"

var ErrSnapshotDrift = errors.New("Pi Runtime snapshot identity drift")

// SnapshotConfig identifies Chora's owner-private, content-addressed snapshot
// store. RunVersion is the same exact-version injection seam used by Resolver.
type SnapshotConfig struct {
	Root           string
	RunVersion     VersionRunner
	VersionTimeout time.Duration
}

type snapshotRecord struct {
	Schema           string `json:"schema_version"`
	ContentDigest    string `json:"content_sha256"`
	ClosureSHA256    string `json:"closure_sha256"`
	ExecutablePath   string `json:"executable"`
	ExecutableSHA256 string `json:"executable_sha256"`
	RuntimeVersion   string `json:"runtime_version"`
}

// LocalPiSnapshot is an immutable binding to a published Runtime package.
// Its paths always name the snapshot package, never the mutable source.
type LocalPiSnapshot struct {
	root           string
	content        [32]byte
	selection      Selection
	runVersion     VersionRunner
	versionTimeout time.Duration
}

func (snapshot LocalPiSnapshot) Root() string            { return snapshot.root }
func (snapshot LocalPiSnapshot) ContentSHA256() [32]byte { return snapshot.content }
func (snapshot LocalPiSnapshot) Selection() Selection    { return snapshot.selection }
func (snapshot LocalPiSnapshot) LocalPiSourceData() LocalPiSourceData {
	return snapshot.selection.LocalPiSourceData()
}

// Snapshot uses the Resolver's already-configured exact version runner.
func (resolver *Resolver) Snapshot(ctx context.Context, root string, selection Selection) (LocalPiSnapshot, error) {
	return createLocalPiSnapshot(ctx, selection, SnapshotConfig{
		Root: root, RunVersion: resolver.runVersion, VersionTimeout: resolver.versionTimeout,
	})
}

// CreateLocalPiSnapshot copies a validated Selection into Chora-owned storage.
// Existing targets are never overwritten and are reused only after an exact,
// no-mutation validation.
func CreateLocalPiSnapshot(ctx context.Context, selection Selection, config SnapshotConfig) (LocalPiSnapshot, error) {
	if config.RunVersion == nil {
		config.RunVersion = defaultVersionRunner
	}
	if config.VersionTimeout == 0 {
		config.VersionTimeout = defaultVersionTimeout
	}
	return createLocalPiSnapshot(ctx, selection, config)
}

func createLocalPiSnapshot(ctx context.Context, source Selection, config SnapshotConfig) (snapshot LocalPiSnapshot, returnErr error) {
	if config.VersionTimeout <= 0 || config.VersionTimeout > 30*time.Second {
		return LocalPiSnapshot{}, errors.New("Pi snapshot version timeout must be positive and no more than 30 seconds")
	}
	restored, err := RestoreSelection(source.Record())
	if err != nil || restored.Record() != source.Record() {
		return LocalPiSnapshot{}, ErrSelectionDrift
	}
	relativeExecutable, err := filepath.Rel(source.PackageRoot(), source.ResolvedPath())
	if err != nil || !validRelativePath(filepath.ToSlash(relativeExecutable)) {
		return LocalPiSnapshot{}, fmt.Errorf("%w: executable escaped source package", ErrSelectionDrift)
	}
	relativeExecutable = filepath.ToSlash(relativeExecutable)
	content := snapshotContentIdentity(source, relativeExecutable)
	if !canonicalAbsolute(config.Root) {
		return LocalPiSnapshot{}, errors.New("Pi snapshot root must be canonical and absolute")
	}
	if config.Root == source.PackageRoot() || pathWithin(source.PackageRoot(), config.Root) {
		return LocalPiSnapshot{}, errors.New("Pi snapshot root must not be inside the source package")
	}
	if err := ensureSnapshotRoot(config.Root); err != nil {
		return LocalPiSnapshot{}, err
	}
	lock, err := acquireSnapshotLock(config.Root)
	if err != nil {
		return LocalPiSnapshot{}, err
	}
	defer func() {
		unlockErr := unix.Flock(int(lock.Fd()), unix.LOCK_UN)
		closeErr := lock.Close()
		if returnErr == nil && (unlockErr != nil || closeErr != nil) {
			returnErr = errors.New("release Pi snapshot lock")
		}
	}()

	finalRoot := filepath.Join(config.Root, digestText(content))
	snapshot, err = newLocalPiSnapshot(finalRoot, content, source, relativeExecutable, config)
	if err != nil {
		return LocalPiSnapshot{}, err
	}
	if _, err := os.Lstat(finalRoot); err == nil {
		if err := snapshot.revalidateWithVersion(ctx); err != nil {
			return LocalPiSnapshot{}, fmt.Errorf("existing Pi snapshot target is partial or unknown: %w", err)
		}
		return snapshot, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return LocalPiSnapshot{}, fmt.Errorf("inspect Pi snapshot target: %w", err)
	}

	validator := &Resolver{runVersion: config.RunVersion, versionTimeout: config.VersionTimeout}
	if err := validator.Revalidate(ctx, source); err != nil {
		return LocalPiSnapshot{}, fmt.Errorf("revalidate Pi snapshot source before copy: %w", err)
	}
	before, directories, err := inspectSnapshotSource(source)
	if err != nil {
		return LocalPiSnapshot{}, err
	}

	stage, err := os.MkdirTemp(config.Root, ".snapshot-stage-")
	if err != nil {
		return LocalPiSnapshot{}, fmt.Errorf("create Pi snapshot stage: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(stage) // exclusively created stage; published/unknown targets are never removed
		}
	}()
	if err := os.Chmod(stage, 0o700); err != nil {
		return LocalPiSnapshot{}, err
	}
	stagePackage := filepath.Join(stage, "package")
	if err := os.Mkdir(stagePackage, 0o700); err != nil {
		return LocalPiSnapshot{}, err
	}
	for _, declared := range before.files {
		sourcePath := filepath.Join(source.PackageRoot(), filepath.FromSlash(declared.Path))
		targetPath := filepath.Join(stagePackage, filepath.FromSlash(declared.Path))
		if !pathWithin(source.PackageRoot(), sourcePath) || !pathWithin(stagePackage, targetPath) {
			return LocalPiSnapshot{}, errors.New("Pi snapshot copy path escaped its package root")
		}
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o700); err != nil {
			return LocalPiSnapshot{}, err
		}
		if err := enforceStageDirectories(stagePackage, filepath.Dir(targetPath)); err != nil {
			return LocalPiSnapshot{}, err
		}
		if err := copySnapshotFile(sourcePath, targetPath, declared); err != nil {
			return LocalPiSnapshot{}, err
		}
	}
	if err := validator.Revalidate(ctx, source); err != nil {
		return LocalPiSnapshot{}, fmt.Errorf("revalidate Pi snapshot source after copy: %w", err)
	}
	after, afterDirectories, err := inspectSnapshotSource(source)
	if err != nil || !sameClosure(before, after) || !sameDirectoryState(directories, afterDirectories) {
		return LocalPiSnapshot{}, fmt.Errorf("%w: source changed during snapshot copy", ErrSelectionDrift)
	}
	marker := snapshotMarker(content, source, relativeExecutable)
	if err := writeSnapshotMarker(stage, marker); err != nil {
		return LocalPiSnapshot{}, err
	}
	stageSnapshot, err := newLocalPiSnapshot(stage, content, source, relativeExecutable, config)
	if err != nil {
		return LocalPiSnapshot{}, err
	}
	if err := stageSnapshot.revalidateWithVersion(ctx); err != nil {
		return LocalPiSnapshot{}, fmt.Errorf("validate Pi snapshot stage: %w", err)
	}
	if err := syncDirectoryTree(stage); err != nil {
		return LocalPiSnapshot{}, err
	}
	if err := atomicRenameNoReplace(stage, finalRoot); err != nil {
		if isCrossDevice(err) {
			return LocalPiSnapshot{}, errors.New("Pi snapshot stage and target are not on one filesystem")
		}
		return LocalPiSnapshot{}, fmt.Errorf("publish Pi snapshot without replacement: %w", err)
	}
	cleanup = false
	if err := syncDirectory(config.Root); err != nil {
		return LocalPiSnapshot{}, err
	}
	if err := snapshot.revalidateWithVersion(ctx); err != nil {
		return LocalPiSnapshot{}, fmt.Errorf("validate published Pi snapshot: %w", err)
	}
	return snapshot, nil
}

func newLocalPiSnapshot(root string, content [32]byte, source Selection, relativeExecutable string, config SnapshotConfig) (LocalPiSnapshot, error) {
	packageRoot := filepath.Join(root, "package")
	executable := filepath.Join(packageRoot, filepath.FromSlash(relativeExecutable))
	selection, err := newSelection(SelectionRecord{
		Kind: SelectionPATH, Path: executable, ResolvedPath: executable, PackageRoot: packageRoot,
		Version: source.Version(), ExecutableSHA256: source.ExecutableSHA256(), ClosureSHA256: source.ClosureSHA256(),
	})
	if err != nil {
		return LocalPiSnapshot{}, err
	}
	return LocalPiSnapshot{root: root, content: content, selection: selection, runVersion: config.RunVersion, versionTimeout: config.VersionTimeout}, nil
}

// Revalidate is the process-free final closure gate. It only reads the
// snapshot's container, marker, package identity, file bytes, and metadata.
// On success it returns the same content digest bound during publication.
func (snapshot LocalPiSnapshot) Revalidate() ([32]byte, error) {
	restored, err := RestoreSelection(snapshot.selection.Record())
	if err != nil || restored.Record() != snapshot.selection.Record() {
		return [32]byte{}, ErrSnapshotDrift
	}
	relativeExecutable, err := filepath.Rel(snapshot.selection.PackageRoot(), snapshot.selection.ResolvedPath())
	if err != nil || !validRelativePath(filepath.ToSlash(relativeExecutable)) {
		return [32]byte{}, ErrSnapshotDrift
	}
	relativeExecutable = filepath.ToSlash(relativeExecutable)
	if snapshotContentIdentity(snapshot.selection, relativeExecutable) != snapshot.content {
		return [32]byte{}, fmt.Errorf("%w: content digest mismatch", ErrSnapshotDrift)
	}
	marker := snapshotMarker(snapshot.content, snapshot.selection, relativeExecutable)
	if err := validateSnapshotContainer(snapshot.root, marker); err != nil {
		return [32]byte{}, fmt.Errorf("%w: %v", ErrSnapshotDrift, err)
	}
	before, err := inspectClosure(snapshot.selection.PackageRoot(), nil, "")
	if err != nil || !closureMatchesSelection(before, snapshot.selection) {
		return [32]byte{}, fmt.Errorf("%w: package closure mismatch", ErrSnapshotDrift)
	}
	if err := verifyPackageIdentityAtRoot(snapshot.selection.PackageRoot()); err != nil {
		return [32]byte{}, fmt.Errorf("%w: %v", ErrSnapshotDrift, err)
	}
	after, err := inspectClosure(snapshot.selection.PackageRoot(), nil, "")
	if err != nil || !sameClosure(before, after) || !closureMatchesSelection(after, snapshot.selection) {
		return [32]byte{}, fmt.Errorf("%w: package changed during revalidation", ErrSnapshotDrift)
	}
	if err := validateSnapshotContainer(snapshot.root, marker); err != nil {
		return [32]byte{}, fmt.Errorf("%w: marker or container changed: %v", ErrSnapshotDrift, err)
	}
	return snapshot.content, nil
}

// revalidateWithVersion is intentionally unexported: exact version execution
// is permitted while creating/reusing a Chora-owned snapshot, never at the
// trusted-host final closure gate.
func (snapshot LocalPiSnapshot) revalidateWithVersion(ctx context.Context) error {
	if snapshot.runVersion == nil || snapshot.versionTimeout <= 0 || snapshot.versionTimeout > 30*time.Second {
		return ErrSnapshotDrift
	}
	before, err := snapshot.Revalidate()
	if err != nil {
		return err
	}
	probeContext, cancel := context.WithTimeout(ctx, snapshot.versionTimeout)
	err = verifyVersion(probeContext, snapshot.runVersion, snapshot.selection.ResolvedPath())
	cancel()
	if err != nil {
		return err
	}
	after, err := snapshot.Revalidate()
	if err != nil {
		return err
	}
	if before != after || after != snapshot.content {
		return fmt.Errorf("%w: content changed during version proof", ErrSnapshotDrift)
	}
	return nil
}

func snapshotContentIdentity(selection Selection, relativeExecutable string) [32]byte {
	hash := sha256.New()
	closureDigest := selection.ClosureSHA256()
	executableDigest := selection.ExecutableSHA256()
	writeFrame(hash, []byte(snapshotSchema))
	writeFrame(hash, []byte(selection.Version()))
	writeFrame(hash, closureDigest[:])
	writeFrame(hash, []byte(relativeExecutable))
	writeFrame(hash, executableDigest[:])
	var digest [32]byte
	copy(digest[:], hash.Sum(nil))
	return digest
}

func snapshotMarker(content [32]byte, selection Selection, relativeExecutable string) snapshotRecord {
	return snapshotRecord{
		Schema: snapshotSchema, ContentDigest: digestText(content),
		ClosureSHA256: digestText(selection.ClosureSHA256()), ExecutablePath: relativeExecutable,
		ExecutableSHA256: digestText(selection.ExecutableSHA256()), RuntimeVersion: selection.Version(),
	}
}

func ensureSnapshotRoot(root string) error {
	if err := os.Mkdir(root, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("create Pi snapshot root: %w", err)
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return errors.New("Pi snapshot root must be a real 0700 directory")
	}
	uid, _, err := unixMetadata(info)
	if err != nil || uid != uint32(os.Geteuid()) {
		return errors.New("Pi snapshot root has wrong owner")
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil || canonical != root {
		return errors.New("Pi snapshot root is not canonical")
	}
	return nil
}

func acquireSnapshotLock(root string) (*os.File, error) {
	path := filepath.Join(root, ".snapshot.lock")
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open Pi snapshot lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("construct Pi snapshot lock")
	}
	valid := false
	defer func() {
		if !valid {
			_ = file.Close()
		}
	}()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return nil, errors.New("Pi snapshot lock must be a 0600 regular file")
	}
	uid, links, err := unixMetadata(info)
	if err != nil || uid != uint32(os.Geteuid()) || links != 1 {
		return nil, errors.New("Pi snapshot lock identity is unsafe")
	}
	if err := unix.Flock(fd, unix.LOCK_EX); err != nil {
		return nil, fmt.Errorf("lock Pi snapshot store: %w", err)
	}
	named, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, named) || named.Mode().Perm() != 0o600 {
		_ = unix.Flock(fd, unix.LOCK_UN)
		return nil, errors.New("Pi snapshot lock drifted")
	}
	valid = true
	return file, nil
}

type snapshotDirectory struct {
	Path string
	Mode fs.FileMode
	UID  uint32
}

func inspectSnapshotSource(selection Selection) (closure, []snapshotDirectory, error) {
	permitted := ""
	if selection.Kind() == SelectionPrivate {
		permitted = ".complete"
	}
	observed, err := inspectClosure(selection.PackageRoot(), nil, permitted)
	if err != nil || !closureMatchesSelection(observed, selection) {
		return closure{}, nil, fmt.Errorf("%w: source closure mismatch", ErrSelectionDrift)
	}
	var directories []snapshotDirectory
	err = filepath.WalkDir(selection.PackageRoot(), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return ErrUnsafeClosure
		}
		uid, _, err := unixMetadata(info)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(selection.PackageRoot(), path)
		if err != nil {
			return err
		}
		directories = append(directories, snapshotDirectory{Path: filepath.ToSlash(relative), Mode: info.Mode(), UID: uid})
		return nil
	})
	return observed, directories, err
}

func sameDirectoryState(left, right []snapshotDirectory) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func copySnapshotFile(source, target string, declared ManifestFile) error {
	before, err := os.Lstat(source)
	if err != nil || !before.Mode().IsRegular() {
		return fmt.Errorf("snapshot source %q is unavailable", declared.Path)
	}
	if err := validateOwnerAndMode(before, false); err != nil {
		return err
	}
	inputFD, err := unix.Open(source, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	input := os.NewFile(uintptr(inputFD), source)
	if input == nil {
		_ = unix.Close(inputFD)
		return errors.New("construct Pi snapshot source handle")
	}
	defer input.Close()
	opened, err := input.Stat()
	if err != nil || !os.SameFile(before, opened) || opened.Mode().Perm() != fs.FileMode(declared.Mode) {
		return fmt.Errorf("%w: source %q changed before copy", ErrSelectionDrift, declared.Path)
	}
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fs.FileMode(declared.Mode))
	if err != nil {
		return fmt.Errorf("create snapshot file %q: %w", declared.Path, err)
	}
	remove := true
	defer func() {
		_ = output.Close()
		if remove {
			_ = os.Remove(target)
		}
	}()
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(output, hash), input)
	if copyErr != nil || written != declared.Size || hex.EncodeToString(hash.Sum(nil)) != declared.SHA256 {
		return fmt.Errorf("copy snapshot file %q exactly", declared.Path)
	}
	after, err := input.Stat()
	if err != nil || !os.SameFile(opened, after) || opened.Size() != after.Size() || opened.Mode() != after.Mode() || opened.ModTime() != after.ModTime() {
		return fmt.Errorf("%w: source %q changed while copying", ErrSelectionDrift, declared.Path)
	}
	if err := output.Chmod(fs.FileMode(declared.Mode)); err != nil {
		return err
	}
	if err := output.Sync(); err != nil {
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	remove = false
	return nil
}

func writeSnapshotMarker(root string, record snapshotRecord) error {
	document, err := canonicalSnapshotMarker(record)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(root, ".snapshot"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	remove := true
	defer func() {
		_ = file.Close()
		if remove {
			_ = os.Remove(filepath.Join(root, ".snapshot"))
		}
	}()
	if _, err := file.Write(document); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	remove = false
	return nil
}

func canonicalSnapshotMarker(record snapshotRecord) ([]byte, error) {
	document, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	return append(document, '\n'), nil
}

func validateSnapshotContainer(root string, expected snapshotRecord) error {
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return errors.New("snapshot container is not a real 0700 directory")
	}
	uid, _, err := unixMetadata(info)
	if err != nil || uid != uint32(os.Geteuid()) {
		return errors.New("snapshot container has wrong owner")
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil || canonical != root {
		return errors.New("snapshot container is not canonical")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 2 || entries[0].Name() != ".snapshot" || entries[1].Name() != "package" {
		return errors.New("snapshot container has missing or unknown entries")
	}
	packageRoot := filepath.Join(root, "package")
	if err := validateSnapshotDirectories(packageRoot); err != nil {
		return err
	}
	markerPath := filepath.Join(root, ".snapshot")
	markerInfo, err := os.Lstat(markerPath)
	if err != nil || !markerInfo.Mode().IsRegular() || markerInfo.Mode().Perm() != 0o600 || markerInfo.Size() > 4096 {
		return errors.New("snapshot marker is absent or unsafe")
	}
	if err := validateOwnerAndMode(markerInfo, false); err != nil {
		return err
	}
	document, err := readSnapshotMarker(markerPath, markerInfo)
	if err != nil || rejectDuplicateKeys(document) != nil {
		return errors.New("snapshot marker is malformed")
	}
	var observed snapshotRecord
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&observed) != nil || requireJSONEOF(decoder) != nil {
		return errors.New("snapshot marker is not strict JSON")
	}
	canonicalDocument, err := canonicalSnapshotMarker(expected)
	if err != nil || observed != expected || !bytes.Equal(document, canonicalDocument) {
		return errors.New("snapshot marker identity mismatch")
	}
	return nil
}

func readSnapshotMarker(path string, before fs.FileInfo) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("construct Pi snapshot marker handle")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) || !opened.Mode().IsRegular() || opened.Size() > 4096 {
		return nil, ErrSnapshotDrift
	}
	if err := validateOwnerAndMode(opened, false); err != nil {
		return nil, err
	}
	document, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(document) > 4096 || int64(len(document)) != opened.Size() {
		return nil, ErrSnapshotDrift
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(opened, after) || opened.Size() != after.Size() || opened.Mode() != after.Mode() || opened.ModTime() != after.ModTime() {
		return nil, ErrSnapshotDrift
	}
	return document, nil
}

func validateSnapshotDirectories(root string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
			return errors.New("snapshot package directory mode or identity drifted")
		}
		uid, _, err := unixMetadata(info)
		if err != nil || uid != uint32(os.Geteuid()) {
			return errors.New("snapshot package directory owner drifted")
		}
		return nil
	})
}
