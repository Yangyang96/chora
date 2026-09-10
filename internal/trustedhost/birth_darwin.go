//go:build darwin

package trustedhost

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func processBirthIdentity(pid int) (string, error) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		if errors.Is(err, unix.ESRCH) {
			return "", os.ErrProcessDone
		}
		return "", err
	}
	if info == nil || info.Proc.P_pid != int32(pid) || info.Proc.P_starttime.Sec <= 0 {
		return "", errors.New("invalid Darwin process birth identity")
	}
	return fmt.Sprintf("darwin-start:%d:%d", info.Proc.P_starttime.Sec, info.Proc.P_starttime.Usec), nil
}
