package opencodediscovery

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

func observeFixtureProcess(pid int) fixtureProcessObservation {
	r := fixtureProcessObservation{PID: pid, Presence: "unknown"}
	if pid <= 1 {
		r.QueryError = "invalid fixture PID"
		return r
	}
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if errors.Is(err, unix.ESRCH) {
		r.Presence = "absent"
		return r
	}
	if err != nil {
		r.QueryError = err.Error()
		return r
	}
	if info == nil || info.Proc.P_pid != int32(pid) || info.Proc.P_starttime.Sec <= 0 {
		r.QueryError = "missing or inconsistent process identity"
		return r
	}
	r.Presence = "present"
	r.PPID, r.PGID = int(info.Eproc.Ppid), int(info.Eproc.Pgid)
	r.Start = fmt.Sprintf("%d:%d", info.Proc.P_starttime.Sec, info.Proc.P_starttime.Usec)
	// Darwin sys/proc.h: 5 is SZOMB. The test still requires ESRCH.
	r.State = fmt.Sprintf("darwin:%d", info.Proc.P_stat)
	return r
}
