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
	"sort"

	"golang.org/x/sys/unix"
)

type completeRecord struct {
	Schema         string `json:"schema_version"`
	ManifestSHA256 string `json:"manifest_sha256"`
	ClosureSHA256  string `json:"closure_sha256"`
}

func (resolver *Resolver) install(ctx context.Context, manifest authenticatedManifest) (selection Selection, returnErr error) {
	if err := ensurePrivateRoot(resolver.privateRoot); err != nil {
		return Selection{}, err
	}
	lock, err := acquireInstallLock(resolver.privateRoot)
	if err != nil {
		return Selection{}, err
	}
	defer func() {
		unlockErr := unix.Flock(int(lock.Fd()), unix.LOCK_UN)
		closeErr := lock.Close()
		if returnErr == nil && (unlockErr != nil || closeErr != nil) {
			returnErr = errors.New("release private Pi install lock")
		}
	}()

	finalRoot := filepath.Join(resolver.privateRoot, digestText(manifest.closure))
	if _, err := os.Lstat(finalRoot); err == nil {
		return resolver.reuseInstalled(ctx, manifest, finalRoot)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Selection{}, fmt.Errorf("inspect private Pi target: %w", err)
	}

	stage, err := os.MkdirTemp(resolver.privateRoot, ".stage-")
	if err != nil {
		return Selection{}, fmt.Errorf("create private Pi stage: %w", err)
	}
	if err := os.Chmod(stage, 0o700); err != nil {
		return Selection{}, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(stage) // stage was created and is exclusively owned by this invocation
		}
	}()
	if err := resolver.populateStage(manifest, stage); err != nil {
		return Selection{}, err
	}
	if _, err := resolver.validateInstalled(ctx, manifest, stage, false); err != nil {
		return Selection{}, err
	}
	if err := syncDirectoryTree(stage); err != nil {
		return Selection{}, err
	}
	if err := writeComplete(stage, manifest); err != nil {
		return Selection{}, err
	}
	if err := syncDirectory(stage); err != nil {
		return Selection{}, err
	}
	if err := atomicRenameNoReplace(stage, finalRoot); err != nil {
		if isCrossDevice(err) {
			return Selection{}, errors.New("private Pi stage and target are not on one filesystem")
		}
		// A concurrently-created target is never overwritten. Under the verified
		// advisory lock it is unexpected, so preserve it and fail closed.
		return Selection{}, fmt.Errorf("publish private Pi target without replacement: %w", err)
	}
	cleanup = false
	if err := syncDirectory(resolver.privateRoot); err != nil {
		return Selection{}, err
	}
	return resolver.validateInstalled(ctx, manifest, finalRoot, true)
}

func ensurePrivateRoot(root string) error {
	if err := os.Mkdir(root, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("create private Pi root: %w", err)
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return errors.New("private Pi root must be a real 0700 directory")
	}
	uid, _, err := unixMetadata(info)
	if err != nil || uid != uint32(os.Geteuid()) {
		return errors.New("private Pi root has wrong owner")
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil || canonical != root {
		return errors.New("private Pi root is not canonical")
	}
	return nil
}

func acquireInstallLock(root string) (*os.File, error) {
	path := filepath.Join(root, ".install.lock")
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open private Pi install lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("construct private Pi install lock")
	}
	valid := false
	defer func() {
		if !valid {
			_ = file.Close()
		}
	}()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return nil, errors.New("private Pi install lock must be a 0600 regular file")
	}
	uid, links, err := unixMetadata(info)
	if err != nil || uid != uint32(os.Geteuid()) || links != 1 {
		return nil, errors.New("private Pi install lock identity is unsafe")
	}
	if err := unix.Flock(fd, unix.LOCK_EX); err != nil {
		return nil, fmt.Errorf("lock private Pi install: %w", err)
	}
	// Revalidate the pathname after locking to reject replacement races.
	named, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, named) || named.Mode().Perm() != 0o600 {
		_ = unix.Flock(fd, unix.LOCK_UN)
		return nil, errors.New("private Pi install lock drifted")
	}
	valid = true
	return file, nil
}

func (resolver *Resolver) populateStage(manifest authenticatedManifest, stage string) error {
	for _, declared := range manifest.document.Files {
		source := normalizedExecutable(resolver.privateAsset.SourceRoot, declared.Path)
		target := normalizedExecutable(stage, declared.Path)
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return fmt.Errorf("create private Pi stage directories: %w", err)
		}
		if err := enforceStageDirectories(stage, filepath.Dir(target)); err != nil {
			return err
		}
		if err := copyDeclaredFile(source, target, declared); err != nil {
			return err
		}
	}
	// Re-observe the authenticated source after all reads; source drift can
	// never be hidden by a stable copied result.
	after, err := inspectClosure(resolver.privateAsset.SourceRoot, manifest.document.Files, "")
	if err != nil || after.digest != manifest.closure {
		return fmt.Errorf("%w: source drifted during copy", ErrSelectionDrift)
	}
	return nil
}

func enforceStageDirectories(stage, leaf string) error {
	for current := leaf; ; current = filepath.Dir(current) {
		if err := os.Chmod(current, 0o700); err != nil {
			return err
		}
		if current == stage {
			return nil
		}
		if !pathWithin(stage, current) {
			return errors.New("stage directory escaped private root")
		}
	}
}

func copyDeclaredFile(source, target string, declared ManifestFile) error {
	before, err := os.Lstat(source)
	if err != nil || !before.Mode().IsRegular() {
		return fmt.Errorf("source %q is unavailable", declared.Path)
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
		return errors.New("construct source file")
	}
	defer input.Close()
	opened, err := input.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return ErrSelectionDrift
	}
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fs.FileMode(declared.Mode))
	if err != nil {
		return fmt.Errorf("create staged %q: %w", declared.Path, err)
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
	var copiedDigest [32]byte
	copy(copiedDigest[:], hash.Sum(nil))
	if copyErr != nil || written != declared.Size || digestText(copiedDigest) != declared.SHA256 {
		return fmt.Errorf("copy staged %q exactly", declared.Path)
	}
	after, err := input.Stat()
	if err != nil || !os.SameFile(opened, after) || opened.Size() != after.Size() || opened.Mode() != after.Mode() || opened.ModTime() != after.ModTime() {
		return fmt.Errorf("%w: source %q changed while copying", ErrSelectionDrift, declared.Path)
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

func (resolver *Resolver) validateInstalled(ctx context.Context, manifest authenticatedManifest, root string, requireComplete bool) (Selection, error) {
	permitted := ""
	if requireComplete {
		if err := verifyComplete(root, manifest); err != nil {
			return Selection{}, err
		}
		permitted = ".complete"
	}
	before, err := inspectClosure(root, manifest.document.Files, permitted)
	if err != nil || before.digest != manifest.closure {
		return Selection{}, fmt.Errorf("installed private Pi closure mismatch: %w", err)
	}
	if err := verifyPackageIdentityAtRoot(root); err != nil {
		return Selection{}, err
	}
	executable := normalizedExecutable(root, manifest.document.Executable)
	before.executable = executableIdentity{path: executable, resolved: executable}
	for _, file := range before.files {
		if file.Path == manifest.document.Executable {
			decoded, _ := hex.DecodeString(file.SHA256)
			copy(before.executable.digest[:], decoded)
		}
	}
	probeContext, cancel := context.WithTimeout(ctx, resolver.versionTimeout)
	err = verifyVersion(probeContext, resolver.runVersion, executable)
	cancel()
	if err != nil {
		return Selection{}, err
	}
	after, err := inspectClosure(root, manifest.document.Files, permitted)
	if err != nil || after.digest != before.digest {
		return Selection{}, fmt.Errorf("%w: private Pi changed during validation", ErrSelectionDrift)
	}
	if requireComplete {
		if err := verifyComplete(root, manifest); err != nil {
			return Selection{}, fmt.Errorf("%w: private completion identity changed", ErrSelectionDrift)
		}
	}
	return newSelection(SelectionRecord{
		Kind: SelectionPrivate, Path: executable, ResolvedPath: executable, PackageRoot: root,
		Version: SupportedVersion, ExecutableSHA256: before.executable.digest, ClosureSHA256: before.digest,
	})
}

func (resolver *Resolver) reuseInstalled(ctx context.Context, manifest authenticatedManifest, root string) (Selection, error) {
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return Selection{}, errors.New("existing private Pi target is partial or unknown")
	}
	return resolver.validateInstalled(ctx, manifest, root, true)
}

func writeComplete(root string, manifest authenticatedManifest) error {
	record := completeRecord{Schema: completeSchema, ManifestSHA256: digestText(manifest.digest), ClosureSHA256: digestText(manifest.closure)}
	document, err := canonicalComplete(record)
	if err != nil {
		return err
	}
	path := filepath.Join(root, ".complete")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(document); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func verifyComplete(root string, manifest authenticatedManifest) error {
	path := filepath.Join(root, ".complete")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > 4096 {
		return errors.New("existing private Pi target lacks an exact .complete marker")
	}
	if err := validateOwnerAndMode(info, false); err != nil {
		return err
	}
	var record completeRecord
	document, err := os.ReadFile(path)
	if err != nil || rejectDuplicateKeys(document) != nil {
		return errors.New("read exact private Pi .complete marker")
	}
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	expected := completeRecord{
		Schema: completeSchema, ManifestSHA256: digestText(manifest.digest), ClosureSHA256: digestText(manifest.closure),
	}
	canonical, canonicalErr := canonicalComplete(expected)
	if decoder.Decode(&record) != nil || requireJSONEOF(decoder) != nil || record != expected || canonicalErr != nil || !bytes.Equal(document, canonical) {
		return errors.New("private Pi .complete marker identity mismatch")
	}
	return nil
}

func verifyCompleteForSelection(root string, closure [32]byte) error {
	path := filepath.Join(root, ".complete")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > 4096 {
		return errors.New("private Pi .complete marker is absent or unsafe")
	}
	if err := validateOwnerAndMode(info, false); err != nil {
		return err
	}
	document, err := os.ReadFile(path)
	if err != nil || rejectDuplicateKeys(document) != nil {
		return errors.New("private Pi .complete marker is malformed")
	}
	var record completeRecord
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&record) != nil || requireJSONEOF(decoder) != nil || record.Schema != completeSchema ||
		!validDigest(record.ManifestSHA256) || record.ClosureSHA256 != digestText(closure) {
		return errors.New("private Pi .complete marker does not bind the selection")
	}
	canonical, err := canonicalComplete(record)
	if err != nil || !bytes.Equal(document, canonical) {
		return errors.New("private Pi .complete marker is not canonical")
	}
	return nil
}

func canonicalComplete(record completeRecord) ([]byte, error) {
	document, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	return append(document, '\n'), nil
}

func syncDirectoryTree(root string) error {
	var directories []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err == nil && entry.IsDir() {
			directories = append(directories, path)
		}
		return err
	})
	if err != nil {
		return err
	}
	sort.Slice(directories, func(i, j int) bool { return len(directories[i]) > len(directories[j]) })
	for _, directory := range directories {
		if err := syncDirectory(directory); err != nil {
			return err
		}
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
