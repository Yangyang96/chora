//go:build linux

package apppreview

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func processBirthIdentity(pid int) (string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", os.ErrProcessDone
		}
		return "", err
	}
	close := strings.LastIndexByte(string(data), ')')
	if close < 0 {
		return "", errors.New("malformed process stat")
	}
	fields := strings.Fields(string(data[close+1:]))
	if len(fields) <= 19 {
		return "", errors.New("short process stat")
	}
	start, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil || start == 0 {
		return "", errors.New("invalid process start time")
	}
	return fmt.Sprintf("linux-proc-start:%d", start), nil
}
