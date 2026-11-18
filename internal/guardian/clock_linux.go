//go:build linux

package guardian

import (
	"golang.org/x/sys/unix"
	"time"
)

func BootClock() (Sample, error) {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_BOOTTIME, &ts); err != nil {
		return Sample{}, err
	}
	return Sample{Wall: time.Now(), Boot: time.Duration(ts.Sec)*time.Second + time.Duration(ts.Nsec)}, nil
}
