package productinstall

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	fileStateName      = "state.json"
	fileActivationName = "active.json"
	fileReferencesName = "references.json"
	fileLockName       = "install.lock"
	fileCASLockName    = "state-cas.lock"
	fileActiveLockName = "active-cas.lock"
	fileRefsLockName   = "references-cas.lock"
	referenceSchema    = "chora.product-generation-references/v2"
	maxStateFileBytes  = 4 << 20
)

// FileBackend implements the durable installation ports in one owner-private
// directory. Construction is read-only; the directory and lock file are first
// created only when an authorized mutation enters WithInstallationLock.
type FileBackend struct {
	root string
}

func NewFileBackend(root string) (*FileBackend, error) {
	if !canonicalPrivateRootPath(root) {
		return nil, fmt.Errorf("%w: invalid installation state root", ErrInvalidRequest)
	}
	return &FileBackend{root: root}, nil
}

func (backend *FileBackend) Load(ctx context.Context) (State, error) {
	var state State
	found, err := backend.readJSON(ctx, fileStateName, &state)
	if err != nil || !found {
		return state, err
	}
	if err := validateState(state); err != nil {
		return State{}, err
	}
	return state, nil
}

func (backend *FileBackend) SaveCAS(ctx context.Context, expected uint64, state State) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ensurePrivateRoot(backend.root); err != nil {
		return err
	}
	return backend.withFileLock(ctx, fileCASLockName, func(lockCtx context.Context) error {
		current, err := backend.Load(lockCtx)
		if err != nil {
			return err
		}
		if current.SchemaVersion == "" {
			current = EmptyState()
		}
		if current.Revision != expected || state.Revision != expected+1 {
			return ErrConcurrentUpdate
		}
		if err := validateState(state); err != nil {
			return err
		}
		return backend.writeJSON(lockCtx, fileStateName, state)
	})
}

func (backend *FileBackend) WithInstallationLock(ctx context.Context, fn func(context.Context) error) error {
	if fn == nil {
		return ErrInvalidRequest
	}
	return backend.withFileLock(ctx, fileLockName, fn)
}

func (backend *FileBackend) withFileLock(ctx context.Context, name string, fn func(context.Context) error) error {
	if err := ensurePrivateRoot(backend.root); err != nil {
		return err
	}
	path := filepath.Join(backend.root, name)
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return errors.New("open installation lock failed")
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return errors.New("open installation lock failed")
	}
	defer file.Close()
	if err := verifyPrivateFile(file); err != nil {
		return err
	}
	for {
		if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err == nil {
			break
		} else if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return errors.New("acquire installation lock failed")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	callbackErr := fn(ctx)
	unlockErr := unix.Flock(fd, unix.LOCK_UN)
	if unlockErr != nil {
		return errors.Join(callbackErr, errors.New("release installation lock failed"))
	}
	return callbackErr
}

func (backend *FileBackend) Current(ctx context.Context) (string, error) {
	var record struct {
		SchemaVersion string `json:"schema_version"`
		GenerationID  string `json:"generation_id"`
	}
	found, err := backend.readJSON(ctx, fileActivationName, &record)
	if err != nil || !found {
		return "", err
	}
	if record.SchemaVersion != "chora.product-activation/v1" || !identifierPattern.MatchString(record.GenerationID) {
		return "", ErrInvalidState
	}
	return record.GenerationID, nil
}

func (backend *FileBackend) AtomicActivate(ctx context.Context, expected string, generation GenerationSpec) error {
	if validateGeneration(generation) != nil {
		return ErrInvalidRequest
	}
	return backend.withFileLock(ctx, fileActiveLockName, func(lockCtx context.Context) error {
		current, err := backend.Current(lockCtx)
		if err != nil {
			return err
		}
		if current != expected {
			return ErrConcurrentUpdate
		}
		return backend.writeJSON(lockCtx, fileActivationName, struct {
			SchemaVersion string `json:"schema_version"`
			GenerationID  string `json:"generation_id"`
		}{"chora.product-activation/v1", generation.GenerationID})
	})
}

func (backend *FileBackend) AtomicDeactivate(ctx context.Context, expected string) error {
	return backend.withFileLock(ctx, fileActiveLockName, func(lockCtx context.Context) error {
		current, err := backend.Current(lockCtx)
		if err != nil {
			return err
		}
		if current != expected || current == "" {
			return ErrConcurrentUpdate
		}
		path := filepath.Join(backend.root, fileActivationName)
		if err := rejectNonPrivateFile(path); err != nil {
			return err
		}
		if err := os.Remove(path); err != nil {
			return errors.New("deactivate product generation failed")
		}
		return syncDirectory(backend.root)
	})
}

func (backend *FileBackend) References(ctx context.Context) (GenerationReferences, error) {
	var record struct {
		SchemaVersion                   string    `json:"schema_version"`
		ServingGenerationIDs            *[]string `json:"serving_generation_ids"`
		ActiveAttemptGenerationIDs      *[]string `json:"active_attempt_generation_ids"`
		RecoverableAttemptGenerationIDs *[]string `json:"recoverable_attempt_generation_ids"`
	}
	found, err := backend.readJSON(ctx, fileReferencesName, &record)
	if err != nil || !found {
		return GenerationReferences{}, err
	}
	if record.SchemaVersion != referenceSchema || record.ServingGenerationIDs == nil || record.ActiveAttemptGenerationIDs == nil || record.RecoverableAttemptGenerationIDs == nil {
		return GenerationReferences{}, ErrInvalidState
	}
	for _, values := range [][]string{*record.ServingGenerationIDs, *record.ActiveAttemptGenerationIDs, *record.RecoverableAttemptGenerationIDs} {
		if !slices.IsSorted(values) {
			return GenerationReferences{}, ErrInvalidState
		}
		for index, value := range values {
			if !identifierPattern.MatchString(value) || index > 0 && values[index-1] == value {
				return GenerationReferences{}, ErrInvalidState
			}
		}
	}
	return GenerationReferences{
		ServingGenerationIDs:            slices.Clone(*record.ServingGenerationIDs),
		ActiveAttemptGenerationIDs:      slices.Clone(*record.ActiveAttemptGenerationIDs),
		RecoverableAttemptGenerationIDs: slices.Clone(*record.RecoverableAttemptGenerationIDs),
	}, nil
}

// RequireReferenceSnapshot is the production fail-closed gate for destructive
// GC and uninstall commands. A missing snapshot is not evidence of zero live
// Attempts; the command must wait for the owning attempt store to publish one.
func (backend *FileBackend) RequireReferenceSnapshot(ctx context.Context) error {
	var record struct {
		SchemaVersion                   string    `json:"schema_version"`
		ServingGenerationIDs            *[]string `json:"serving_generation_ids"`
		ActiveAttemptGenerationIDs      *[]string `json:"active_attempt_generation_ids"`
		RecoverableAttemptGenerationIDs *[]string `json:"recoverable_attempt_generation_ids"`
	}
	found, err := backend.readJSON(ctx, fileReferencesName, &record)
	if err != nil {
		return err
	}
	if !found {
		return ErrGenerationReferenced
	}
	if record.SchemaVersion != referenceSchema || record.ServingGenerationIDs == nil || record.ActiveAttemptGenerationIDs == nil || record.RecoverableAttemptGenerationIDs == nil {
		return ErrInvalidState
	}
	_, err = backend.References(ctx)
	return err
}

// PublishReferences atomically replaces the complete serving and Attempt
// generation reference snapshot. The publisher must already own lifecycle and
// attempt-store authority; this method never infers, merges, or drops refs.
func (backend *FileBackend) PublishReferences(ctx context.Context, references GenerationReferences) error {
	if err := validateGenerationReferences(references); err != nil {
		return err
	}
	return backend.withFileLock(ctx, fileRefsLockName, func(lockCtx context.Context) error {
		return backend.writeReferences(lockCtx, references)
	})
}

// RetireServingGeneration is the authenticated Uninstall handoff from a live
// process to the durable removal state machine. Its caller must hold the shared
// installation lock, have deactivated the exact active generation, and have
// already rejected every Attempt reference. The dedicated references lock
// keeps the snapshot replacement atomic for readers.
func (backend *FileBackend) RetireServingGeneration(ctx context.Context, generationID string) error {
	if !identifierPattern.MatchString(generationID) {
		return ErrInvalidRequest
	}
	return backend.withFileLock(ctx, fileRefsLockName, func(lockCtx context.Context) error {
		references, err := backend.References(lockCtx)
		if err != nil {
			return err
		}
		index, found := slices.BinarySearch(references.ServingGenerationIDs, generationID)
		if !found {
			return nil
		}
		references.ServingGenerationIDs = slices.Delete(references.ServingGenerationIDs, index, index+1)
		return backend.writeReferences(lockCtx, references)
	})
}

func (backend *FileBackend) writeReferences(ctx context.Context, references GenerationReferences) error {
	return backend.writeJSON(ctx, fileReferencesName, struct {
		SchemaVersion                   string   `json:"schema_version"`
		ServingGenerationIDs            []string `json:"serving_generation_ids"`
		ActiveAttemptGenerationIDs      []string `json:"active_attempt_generation_ids"`
		RecoverableAttemptGenerationIDs []string `json:"recoverable_attempt_generation_ids"`
	}{
		SchemaVersion:                   referenceSchema,
		ServingGenerationIDs:            canonicalReferenceIDs(references.ServingGenerationIDs),
		ActiveAttemptGenerationIDs:      canonicalReferenceIDs(references.ActiveAttemptGenerationIDs),
		RecoverableAttemptGenerationIDs: canonicalReferenceIDs(references.RecoverableAttemptGenerationIDs),
	})
}

func canonicalReferenceIDs(values []string) []string {
	result := make([]string, len(values))
	copy(result, values)
	return result
}

func validateGenerationReferences(references GenerationReferences) error {
	for _, values := range [][]string{references.ServingGenerationIDs, references.ActiveAttemptGenerationIDs, references.RecoverableAttemptGenerationIDs} {
		if !slices.IsSorted(values) {
			return ErrInvalidRequest
		}
		for index, value := range values {
			if !identifierPattern.MatchString(value) || index > 0 && values[index-1] == value {
				return ErrInvalidRequest
			}
		}
	}
	return nil
}

func (backend *FileBackend) readJSON(ctx context.Context, name string, target any) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	found, err := inspectPrivateRoot(backend.root)
	if err != nil || !found {
		if !found && err == nil {
			return false, nil
		}
		return false, err
	}
	info, err := os.Lstat(backend.root)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || !privateDirectory(info) {
		return false, errors.New("installation state root is unavailable or unsafe")
	}
	path := filepath.Join(backend.root, name)
	file, found, err := openPrivateFileForRead(path)
	if !found && err == nil {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxStateFileBytes+1))
	if err != nil || len(data) > maxStateFileBytes {
		return false, errors.New("read installation state failed")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return false, errors.New("decode installation state failed")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return false, errors.New("decode installation state failed")
	}
	return true, nil
}

func (backend *FileBackend) writeJSON(ctx context.Context, name string, value any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ensurePrivateRoot(backend.root); err != nil {
		return err
	}
	path := filepath.Join(backend.root, name)
	if _, err := os.Lstat(path); err == nil {
		if err := rejectNonPrivateFile(path); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("inspect installation state failed")
	}
	data, err := json.Marshal(value)
	if err != nil || len(data) > maxStateFileBytes {
		return errors.New("encode installation state failed")
	}
	data = append(data, '\n')
	temporary, err := os.CreateTemp(backend.root, ".product-state-")
	if err != nil {
		return errors.New("create installation checkpoint failed")
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return errors.New("secure installation checkpoint failed")
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return errors.New("write installation checkpoint failed")
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return errors.New("sync installation checkpoint failed")
	}
	if err := temporary.Close(); err != nil {
		return errors.New("close installation checkpoint failed")
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return errors.New("activate installation checkpoint failed")
	}
	return syncDirectory(backend.root)
}

func canonicalPrivateRootPath(root string) bool {
	return filepath.IsAbs(root) && filepath.Clean(root) == root && root != filepath.VolumeName(root)+string(os.PathSeparator)
}

func ensurePrivateRoot(root string) error {
	if !canonicalPrivateRootPath(root) {
		return ErrInvalidRequest
	}
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		parent := filepath.Dir(root)
		resolved, resolveErr := filepath.EvalSymlinks(parent)
		parentInfo, statErr := os.Lstat(parent)
		if resolveErr != nil || resolved != parent || statErr != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
			return errors.New("installation state parent is unavailable or unsafe")
		}
		if err := os.Mkdir(root, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return errors.New("create installation state root failed")
		}
		info, err = os.Lstat(root)
	}
	if err != nil || !privateDirectory(info) {
		return errors.New("installation state root is unavailable or unsafe")
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || resolved != root {
		return errors.New("installation state root is unavailable or unsafe")
	}
	return nil
}

func inspectPrivateRoot(root string) (bool, error) {
	if !canonicalPrivateRootPath(root) {
		return false, ErrInvalidRequest
	}
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || !privateDirectory(info) {
		return false, errors.New("installation state root is unavailable or unsafe")
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || resolved != root {
		return false, errors.New("installation state root is unavailable or unsafe")
	}
	return true, nil
}

func privateDirectory(info os.FileInfo) bool {
	return info != nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 && info.Mode().Perm() == 0o700 && ownedByCurrentUser(info)
}

func rejectNonPrivateFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !privateFile(info) {
		return errors.New("installation state file is unavailable or unsafe")
	}
	return nil
}

func verifyPrivateFile(file *os.File) error {
	info, err := file.Stat()
	if err != nil || !privateFile(info) {
		return errors.New("installation state file is unavailable or unsafe")
	}
	return nil
}

func openPrivateFileForRead(path string) (*os.File, bool, error) {
	// Never fall back to os.Open: a platform/open failure that cannot enforce
	// O_NOFOLLOW must fail closed rather than follow a durable-state symlink.
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, errors.New("open installation state failed")
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, false, errors.New("open installation state failed")
	}
	openedInfo, statErr := file.Stat()
	pathInfo, pathErr := os.Lstat(path)
	if statErr != nil || pathErr != nil || !privateFile(openedInfo) || !privateFile(pathInfo) || !os.SameFile(openedInfo, pathInfo) {
		_ = file.Close()
		return nil, false, errors.New("installation state file is unavailable or unsafe")
	}
	return file, true, nil
}

func privateFile(info os.FileInfo) bool {
	if info == nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 || !ownedByCurrentUser(info) {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink == 1
}

func ownedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Geteuid()
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return errors.New("open installation state root failed")
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return errors.New("sync installation state root failed")
	}
	return nil
}
