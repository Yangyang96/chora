package piinstall

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

type fileLock struct{ file *os.File }

func (l *fileLock) close() {
	if l == nil || l.file == nil {
		return
	}
	_ = unix.Flock(int(l.file.Fd()), unix.LOCK_UN)
	_ = l.file.Close()
}

func (c *Core) ensurePrivateRoot() error {
	if err := validateTrustedDataRoot(c.dataRoot); err != nil {
		return err
	}
	if err := ensurePrivateDir(c.piRoot); err != nil {
		return err
	}
	if err := ensurePrivateDir(filepath.Join(c.piRoot, ".operations")); err != nil {
		return err
	}
	return nil
}

func validateTrustedDataRoot(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect trusted data root: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("trusted data root is not a real directory")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && int(stat.Uid) != os.Getuid() {
		return errors.New("trusted data root has wrong owner")
	}
	return nil
}

func ensurePrivateDir(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil && !os.IsExist(err) {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("private directory is unsafe: %s", filepath.Base(path))
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && int(stat.Uid) != os.Getuid() {
		return errors.New("private directory has wrong owner")
	}
	return nil
}

func ensureEmptyFile(path string) error {
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	var file *os.File
	if err == nil {
		file = os.NewFile(uintptr(fd), path)
	}
	if err == nil {
		return file.Close()
	}
	if !os.IsExist(err) {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 || info.Size() != 0 {
		return errors.New("npm user configuration is unsafe")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && (int(stat.Uid) != os.Getuid() || stat.Nlink != 1) {
		return errors.New("npm user configuration identity is unsafe")
	}
	return nil
}

func writeExclusive(path string, data []byte, mode fs.FileMode) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		file.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}

func (c *Core) acquireLock() (*fileLock, error) {
	path := filepath.Join(c.piRoot, ".install.lock")
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		file.Close()
		return nil, errors.New("install lock is unsafe")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && (int(stat.Uid) != os.Getuid() || stat.Nlink != 1) {
		file.Close()
		return nil, errors.New("install lock identity is unsafe")
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		return nil, errors.New("install lock is held")
	}
	return &fileLock{file: file}, nil
}

func (c *Core) createStage(stage, operationID string) error {
	if err := os.Mkdir(stage, 0o700); err != nil {
		return err
	}
	marker, _ := json.Marshal(map[string]string{"operationId": operationID, "contractDigest": c.contract.Digest})
	return writeExclusive(filepath.Join(stage, ".operation.json"), append(marker, '\n'), 0o600)
}

func (c *Core) saveState(record stateRecord) error {
	record.DestinationPath = c.piRoot
	return atomicJSON(filepath.Join(c.piRoot, "state.json"), record, 0o600)
}

func (c *Core) loadState() (stateRecord, error) {
	var record stateRecord
	data, err := readRegularNoFollow(filepath.Join(c.piRoot, "state.json"), 64<<10)
	if err != nil {
		return record, err
	}
	if err := json.Unmarshal(data, &record); err != nil {
		return stateRecord{}, err
	}
	return record, nil
}

func (c *Core) saveOperation(record operationRecord) error {
	return atomicJSON(filepath.Join(c.piRoot, ".operations", record.OperationID+".json"), record, 0o600)
}

func atomicJSON(path string, value any, mode fs.FileMode) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	file, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := file.Name()
	if err := file.Chmod(mode); err != nil {
		file.Close()
		_ = os.Remove(tmp)
		return err
	}
	ok := false
	defer func() {
		file.Close()
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if err := syncDir(filepath.Dir(path)); err != nil {
		return err
	}
	ok = true
	return nil
}

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func syncTree(root string) error {
	var directories []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			directories = append(directories, path)
			return nil
		}
		if entry.Type().IsRegular() {
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			err = file.Sync()
			closeErr := file.Close()
			if err != nil {
				return err
			}
			return closeErr
		}
		return nil
	})
	if err != nil {
		return err
	}
	for i := len(directories) - 1; i >= 0; i-- {
		if err := syncDir(directories[i]); err != nil {
			return err
		}
	}
	return nil
}

func (c *Core) publishStage(stage, finalRoot string, expected completion) error {
	if _, err := os.Lstat(finalRoot); err == nil {
		if err := validateCompletion(finalRoot, expected); err != nil {
			return err
		}
		return c.removeOwnedStage(stage, expected.OperationID)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(stage, finalRoot); err != nil {
		return err
	}
	return syncDir(c.piRoot)
}

func validateCompletion(root string, expected completion) error {
	data, err := readRegularNoFollow(filepath.Join(root, ".complete"), 64<<10)
	if err != nil {
		return err
	}
	var got completion
	if json.Unmarshal(data, &got) != nil || got.ContractDigest != expected.ContractDigest || got.LockSHA256 != expected.LockSHA256 || got.ClosureDigest != expected.ClosureDigest || got.ExecutableSHA256 != expected.ExecutableSHA256 || got.PiVersion != expected.PiVersion {
		return errors.New("existing content root differs from validated installation")
	}
	return nil
}

func (c *Core) removeOwnedStage(stage, operationID string) error {
	if filepath.Dir(stage) != c.piRoot || filepath.Base(stage) != ".stage-"+operationID {
		return errors.New("stage cleanup target is unsafe")
	}
	info, err := os.Lstat(stage)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return errors.New("stage cleanup identity is unsafe")
	}
	data, err := readRegularNoFollow(filepath.Join(stage, ".operation.json"), 64<<10)
	if err != nil {
		return err
	}
	var marker map[string]string
	if json.Unmarshal(data, &marker) != nil || marker["operationId"] != operationID || marker["contractDigest"] != c.contract.Digest {
		return errors.New("stage cleanup marker differs")
	}
	return os.RemoveAll(stage)
}

func readRegularNoFollow(path string, maximum int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maximum {
		return nil, errors.New("file identity is unsafe")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && (int(stat.Uid) != os.Getuid() || stat.Nlink != 1) {
		return nil, errors.New("file ownership is unsafe")
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, opened) || !opened.Mode().IsRegular() || opened.Size() > maximum {
		return nil, errors.New("file identity changed while opening")
	}
	if stat, ok := opened.Sys().(*syscall.Stat_t); ok && (int(stat.Uid) != os.Getuid() || stat.Nlink != 1) {
		return nil, errors.New("opened file ownership is unsafe")
	}
	data := make([]byte, opened.Size())
	if _, err := io.ReadFull(file, data); err != nil {
		return nil, err
	}
	return data, nil
}
