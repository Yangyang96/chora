//go:build !linux

package guardian

import "errors"

func BootClock() (Sample, error) {
	return Sample{}, errors.New("guardian requires Linux CLOCK_BOOTTIME and private PID namespace")
}
