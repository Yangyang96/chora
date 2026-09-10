package pidistribution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

// PrivateBinding is the path-free immutable identity used by product install
// journals. It can represent only an authenticated private asset, never PATH Pi.
type PrivateBinding struct {
	ManifestSHA256 string
	ClosureSHA256  string
}

func BindPrivateAsset(asset *PrivateAsset, goos, goarch string) (PrivateBinding, error) {
	manifest, err := authenticateManifest(asset, goos, goarch)
	if err != nil {
		return PrivateBinding{}, err
	}
	return PrivateBinding{ManifestSHA256: digestText(manifest.digest), ClosureSHA256: digestText(manifest.closure)}, nil
}

// InstallPrivate bypasses PATH selection and installs only the authenticated
// private closure configured on this Resolver.
func (resolver *Resolver) InstallPrivate(ctx context.Context) (Selection, error) {
	if resolver == nil {
		return Selection{}, ErrPrivateAssetUnavailable
	}
	return resolver.resolvePrivate(ctx)
}

// InspectPrivate validates the exact authenticated private closure without
// installing, selecting PATH Pi, or changing any file.
func (resolver *Resolver) InspectPrivate(ctx context.Context) (Selection, bool, error) {
	if resolver == nil {
		return Selection{}, false, ErrPrivateAssetUnavailable
	}
	manifest, err := authenticateManifest(resolver.privateAsset, resolver.goos, resolver.goarch)
	if err != nil {
		return Selection{}, false, err
	}
	available, err := inspectExistingPrivateRoot(resolver.privateRoot)
	if err != nil || !available {
		return Selection{}, false, err
	}
	root := filepath.Join(resolver.privateRoot, digestText(manifest.closure))
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		return Selection{}, false, nil
	} else if err != nil {
		return Selection{}, false, err
	}
	selection, err := resolver.validateInstalled(ctx, manifest, root, true)
	if err != nil {
		return Selection{}, false, err
	}
	return selection, true, nil
}

// RemovalPending reports only a valid identity-bound journal for this exact
// manifest/closure. It lets the outer installation journal resume tombstone
// cleanup instead of mistaking a renamed closure for completed removal.
func (resolver *Resolver) RemovalPending(ctx context.Context) (bool, error) {
	if resolver == nil {
		return false, ErrPrivateAssetUnavailable
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	manifest, err := authenticateManifest(resolver.privateAsset, resolver.goos, resolver.goarch)
	if err != nil {
		return false, err
	}
	available, err := inspectExistingPrivateRoot(resolver.privateRoot)
	if err != nil || !available {
		return false, err
	}
	closure := digestText(manifest.closure)
	journal, present, err := readRemovalJournal(filepath.Join(resolver.privateRoot, ".remove-"+closure+".json"))
	if err != nil || !present {
		return present, err
	}
	if journal.ManifestSHA256 != digestText(manifest.digest) || journal.ClosureSHA256 != closure || journal.Tombstone != ".remove-"+closure {
		return false, errors.New("private Pi removal journal identity mismatch")
	}
	return true, nil
}

// RemovePrivate atomically moves the exact closure behind an identity-bound
// tombstone before deletion. A crash can therefore resume without trusting a
// partially deleted package tree or broadening removal to siblings.
func (resolver *Resolver) RemovePrivate(ctx context.Context) (returnErr error) {
	if resolver == nil {
		return ErrPrivateAssetUnavailable
	}
	manifest, err := authenticateManifest(resolver.privateAsset, resolver.goos, resolver.goarch)
	if err != nil {
		return err
	}
	available, err := inspectExistingPrivateRoot(resolver.privateRoot)
	if err != nil || !available {
		return err
	}
	lock, err := acquireInstallLock(resolver.privateRoot)
	if err != nil {
		return err
	}
	defer func() {
		unlockErr := unix.Flock(int(lock.Fd()), unix.LOCK_UN)
		closeErr := lock.Close()
		if returnErr == nil && (unlockErr != nil || closeErr != nil) {
			returnErr = errors.New("release private Pi removal lock")
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	closure := digestText(manifest.closure)
	root := filepath.Join(resolver.privateRoot, closure)
	tombstone := filepath.Join(resolver.privateRoot, ".remove-"+closure)
	journalPath := filepath.Join(resolver.privateRoot, ".remove-"+closure+".json")
	journal, journalPresent, err := readRemovalJournal(journalPath)
	if err != nil {
		return err
	}
	if journalPresent {
		if journal.ManifestSHA256 != digestText(manifest.digest) || journal.ClosureSHA256 != closure || journal.Tombstone != filepath.Base(tombstone) {
			return errors.New("private Pi removal journal identity mismatch")
		}
	} else {
		if present, err := pathExists(tombstone); err != nil || present {
			return errors.New("unowned private Pi removal tombstone")
		}
		present, err := pathExists(root)
		if err != nil || !present {
			return err
		}
		if _, err := resolver.validateInstalled(ctx, manifest, root, true); err != nil {
			return err
		}
		device, inode, err := exactRemovalDirectoryIdentity(root)
		if err != nil {
			return err
		}
		journal = privateRemovalJournal{
			SchemaVersion: "chora.pi-private-removal/v1", ManifestSHA256: digestText(manifest.digest),
			ClosureSHA256: closure, Tombstone: filepath.Base(tombstone), Device: device, Inode: inode,
		}
		if err := writeRemovalJournal(resolver.privateRoot, journalPath, journal); err != nil {
			return err
		}
	}
	rootPresent, err := pathExists(root)
	if err != nil {
		return err
	}
	tombstonePresent, err := pathExists(tombstone)
	if err != nil || rootPresent && tombstonePresent {
		return errors.New("private Pi removal paths conflict")
	}
	if rootPresent {
		if _, err := resolver.validateInstalled(ctx, manifest, root, true); err != nil {
			return err
		}
		if err := requireRemovalDirectoryIdentity(root, journal); err != nil {
			return err
		}
		if err := os.Rename(root, tombstone); err != nil {
			return errors.New("create exact private Pi removal tombstone failed")
		}
		if err := syncDirectory(resolver.privateRoot); err != nil {
			return err
		}
		tombstonePresent = true
	}
	if tombstonePresent {
		if err := requireRemovalDirectoryIdentity(tombstone, journal); err != nil {
			return err
		}
		if err := resolver.removeTree(tombstone); err != nil {
			return errors.Join(ErrRemovalInterrupted, errors.New("remove exact private Pi tombstone failed"))
		}
		if present, err := pathExists(tombstone); err != nil || present {
			return errors.Join(ErrRemovalInterrupted, errors.New("exact private Pi tombstone removal is unproven"))
		}
		if err := syncDirectory(resolver.privateRoot); err != nil {
			return err
		}
	}
	if err := os.Remove(journalPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.Join(ErrRemovalInterrupted, errors.New("remove private Pi removal journal failed"))
	}
	return syncDirectory(resolver.privateRoot)
}

type privateRemovalJournal struct {
	SchemaVersion  string `json:"schema_version"`
	ManifestSHA256 string `json:"manifest_sha256"`
	ClosureSHA256  string `json:"closure_sha256"`
	Tombstone      string `json:"tombstone"`
	Device         uint64 `json:"device"`
	Inode          uint64 `json:"inode"`
}

func readRemovalJournal(path string) (privateRemovalJournal, bool, error) {
	pathInfo, statErr := os.Lstat(path)
	if errors.Is(statErr, os.ErrNotExist) {
		return privateRemovalJournal{}, false, nil
	}
	if statErr != nil || !pathInfo.Mode().IsRegular() || pathInfo.Mode()&os.ModeSymlink != 0 || pathInfo.Mode().Perm() != 0o600 || !ownedRemovalPath(pathInfo) {
		return privateRemovalJournal{}, false, errors.New("private Pi removal journal is unsafe")
	}
	file, err := os.Open(path)
	if err != nil {
		return privateRemovalJournal{}, false, errors.New("open private Pi removal journal failed")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || !ownedRemovalPath(info) {
		return privateRemovalJournal{}, false, errors.New("private Pi removal journal is unsafe")
	}
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(data) > 4096 {
		return privateRemovalJournal{}, false, errors.New("read private Pi removal journal failed")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var journal privateRemovalJournal
	if err := decoder.Decode(&journal); err != nil {
		return privateRemovalJournal{}, false, errors.New("decode private Pi removal journal failed")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) || journal.SchemaVersion != "chora.pi-private-removal/v1" ||
		!validDigest(journal.ManifestSHA256) || !validDigest(journal.ClosureSHA256) || journal.Tombstone != ".remove-"+journal.ClosureSHA256 || journal.Device == 0 || journal.Inode == 0 {
		return privateRemovalJournal{}, false, errors.New("private Pi removal journal is invalid")
	}
	return journal, true, nil
}

func writeRemovalJournal(root, path string, journal privateRemovalJournal) error {
	data, err := json.Marshal(journal)
	if err != nil {
		return errors.New("encode private Pi removal journal failed")
	}
	temporary, err := os.CreateTemp(root, ".remove-journal-*")
	if err != nil {
		return errors.New("create private Pi removal journal failed")
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return errors.New("secure private Pi removal journal failed")
	}
	if _, err := temporary.Write(append(data, '\n')); err != nil {
		_ = temporary.Close()
		return errors.New("persist private Pi removal journal failed")
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return errors.New("persist private Pi removal journal failed")
	}
	if err := temporary.Close(); err != nil {
		return errors.New("persist private Pi removal journal failed")
	}
	if err := atomicRenameNoReplace(temporaryPath, path); err != nil {
		return errors.New("activate private Pi removal journal failed")
	}
	return syncDirectory(root)
}

func exactRemovalDirectoryIdentity(path string) (uint64, uint64, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 || !ownedRemovalPath(info) {
		return 0, 0, errors.New("private Pi removal directory is unsafe")
	}
	metadata, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, errors.New("private Pi removal directory identity unavailable")
	}
	return uint64(metadata.Dev), uint64(metadata.Ino), nil
}

func requireRemovalDirectoryIdentity(path string, journal privateRemovalJournal) error {
	device, inode, err := exactRemovalDirectoryIdentity(path)
	if err != nil || device != journal.Device || inode != journal.Inode {
		return errors.New("private Pi removal directory identity changed")
	}
	return nil
}

func ownedRemovalPath(info os.FileInfo) bool {
	metadata, ok := info.Sys().(*syscall.Stat_t)
	return ok && metadata.Uid == uint32(os.Geteuid())
}

func pathExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func inspectExistingPrivateRoot(root string) (bool, error) {
	if !canonicalAbsolute(root) {
		return false, errors.New("private Pi root must be canonical and absolute")
	}
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return false, errors.New("private Pi root must be a real 0700 directory")
	}
	uid, _, err := unixMetadata(info)
	if err != nil || uid != uint32(os.Geteuid()) {
		return false, errors.New("private Pi root has wrong owner")
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil || canonical != root {
		return false, errors.New("private Pi root is not canonical")
	}
	return true, nil
}
