package desktop

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"

	"golang.org/x/sys/unix"
)

var ErrInUse = errors.New("Chora data root is in use")

// OwnerLock serializes every process which may read or mutate a data root.
// The lock lives beside the root so it also protects restore while the root is
// temporarily absent.
type OwnerLock struct{ file *os.File }

func AcquireOwner(dataRoot string) (*OwnerLock, error) {
	root, err := exactAbsolute(dataRoot)
	if err != nil {
		return nil, err
	}
	parent := filepath.Dir(root)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return nil, fmt.Errorf("create lock parent: %w", err)
	}
	if err := rejectSymlink(parent); err != nil {
		return nil, err
	}
	path := filepath.Join(parent, "."+filepath.Base(root)+".chora-owner.lock")
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, fmt.Errorf("open owner lock: %w", err)
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Getuid()) {
		_ = f.Close()
		return nil, errors.New("owner lock must be an owner-private regular file")
	}
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrInUse
		}
		return nil, fmt.Errorf("acquire owner lock: %w", err)
	}
	return &OwnerLock{file: f}, nil
}

func (l *OwnerLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := unix.Flock(int(l.file.Fd()), unix.LOCK_UN)
	closeErr := l.file.Close()
	l.file = nil
	if err != nil {
		return err
	}
	return closeErr
}

func exactAbsolute(path string) (string, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("path must be clean and absolute")
	}
	if path == string(os.PathSeparator) {
		return "", errors.New("filesystem root is not a valid Chora path")
	}
	if err := rejectSymlinkAncestors(filepath.Dir(path)); err != nil {
		return "", err
	}
	return path, nil
}

func rejectSymlinkAncestors(path string) error {
	for {
		info, err := os.Lstat(path)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return errors.New("path ancestor must not be a symlink")
		}
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return nil
		}
		path = parent
	}
}

func ensureStopped(dataRoot string) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	paths := []string{filepath.Join(dataRoot, "chora.db"), filepath.Join(dataRoot, "chora.db-wal"), filepath.Join(dataRoot, "chora.db-shm")}
	args := append([]string{"-Fpc", "--"}, paths...)
	cmd := exec.Command("/usr/sbin/lsof", args...)
	out, err := cmd.Output()
	if err == nil && len(out) > 0 {
		return errors.New("data root database is open by another process")
	}
	if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
		return nil
	}
	if err != nil {
		return fmt.Errorf("cannot prove data root is stopped: %w", err)
	}
	return nil
}

func rejectSymlink(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("path must be a real directory")
	}
	return nil
}
