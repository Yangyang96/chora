package apppreview

import (
	"errors"
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func observeProcess(pid int) (int, string, error) {
	if pid <= 0 {
		return 0, "", os.ErrProcessDone
	}
	if err := unix.Kill(pid, 0); err != nil {
		if errors.Is(err, unix.ESRCH) {
			return 0, "", os.ErrProcessDone
		}
		return 0, "", err
	}
	pgid, err := unix.Getpgid(pid)
	if err != nil {
		if errors.Is(err, unix.ESRCH) {
			return 0, "", os.ErrProcessDone
		}
		return 0, "", err
	}
	birth, err := processBirthIdentity(pid)
	return pgid, birth, err
}

func groupAlive(pgid int) (bool, error) {
	if pgid <= 0 {
		return false, errors.New("invalid process group")
	}
	err := unix.Kill(-pgid, 0)
	switch {
	case err == nil, errors.Is(err, unix.EPERM):
		return true, nil
	case errors.Is(err, unix.ESRCH):
		return false, nil
	default:
		return false, err
	}
}

func stopGroup(pgid int, timeout time.Duration) error {
	if pgid <= 0 {
		return errors.New("invalid process group")
	}
	if err := unix.Kill(-pgid, syscall.SIGTERM); err != nil && !errors.Is(err, unix.ESRCH) {
		return err
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		alive, err := groupAlive(pgid)
		if err != nil || !alive {
			return err
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err := unix.Kill(-pgid, syscall.SIGKILL); err != nil && !errors.Is(err, unix.ESRCH) {
		return err
	}
	deadline = time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		alive, err := groupAlive(pgid)
		if err != nil || !alive {
			return err
		}
		time.Sleep(25 * time.Millisecond)
	}
	return errors.New("app preview process group did not terminate")
}
