package acceptanceauthority

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

func canonicalAbsolute(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && path != string(filepath.Separator)
}

func readOwnerOnlyCanonicalFile(path string, limit int64) ([]byte, error) {
	if !canonicalAbsolute(path) {
		return nil, errors.New("path must be canonical and absolute")
	}
	if err := proveNoSymlinkAncestors(path); err != nil {
		return nil, err
	}
	return readBoundedRegular(path, limit, true)
}

func readBoundedRegular(path string, limit int64, ownerOnly bool) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("open bounded file")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || stat.Nlink != 1 || info.Size() < 1 || info.Size() > limit {
		return nil, errors.New("file must be one bounded regular nlink-1 file")
	}
	if ownerOnly && (int(stat.Uid) != os.Geteuid() || info.Mode().Perm()&0o077 != 0) {
		return nil, errors.New("file must be owner-only")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("read bounded file")
	}
	return data, nil
}

func validateOwnerRegularPath(path string) error {
	if !canonicalAbsolute(path) {
		return errors.New("file path is not canonical")
	}
	if err := proveNoSymlinkAncestors(path); err != nil {
		return err
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return errors.New("open owner file")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || stat.Nlink != 1 || int(stat.Uid) != os.Geteuid() {
		return errors.New("file must be an owner-only regular nlink-1 file")
	}
	return nil
}

func openExclusiveOwnerFile(path string, mode os.FileMode) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, uint32(mode.Perm()))
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("open exclusive owner file")
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || stat.Nlink != 1 || int(stat.Uid) != os.Geteuid() || info.Mode().Perm()&0o077 != 0 {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, errors.New("exclusive file is not an owner-only regular nlink-1 file")
	}
	return file, nil
}

func durableSyncDirectory(path string) error {
	if !canonicalAbsolute(path) {
		return errors.New("directory path is not canonical")
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	directory := os.NewFile(uintptr(fd), path)
	if directory == nil {
		_ = unix.Close(fd)
		return errors.New("open directory for sync")
	}
	defer directory.Close()
	info, err := directory.Stat()
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm()&0o077 != 0 || int(stat.Uid) != os.Geteuid() {
		return errors.New("directory sync authority is unsafe")
	}
	return directory.Sync()
}

func proveNoSymlinkAncestors(path string) error {
	path = filepath.Clean(path)
	volume := filepath.VolumeName(path)
	current := volume + string(filepath.Separator)
	relative := path[len(current):]
	for _, component := range splitPath(relative) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("inspect path component: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("symbolic-link ancestor is forbidden")
		}
	}
	return nil
}

func splitPath(path string) []string {
	var result []string
	for path != "" && path != "." {
		directory, base := filepath.Split(path)
		if base != "" {
			result = append([]string{base}, result...)
		}
		path = filepath.Clean(directory)
		if path == string(filepath.Separator) {
			break
		}
	}
	return result
}

func validateOwnerDirectory(path string) error {
	if !canonicalAbsolute(path) {
		return errors.New("directory path is not canonical")
	}
	if err := proveNoSymlinkAncestors(path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || int(stat.Uid) != os.Geteuid() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("directory must be a real owner-only directory")
	}
	return nil
}
