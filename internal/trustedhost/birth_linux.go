//go:build linux

package trustedhost

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
	// The comm field is parenthesized and may contain spaces or ')'. Fields
	// after its final ')' begin at field 3; starttime is field 22.
	close := strings.LastIndexByte(string(data), ')')
	if close < 0 || close+2 > len(data) {
		return "", errors.New("malformed /proc process stat")
	}
	fields := strings.Fields(string(data[close+1:]))
	if len(fields) <= 19 {
		return "", errors.New("short /proc process stat")
	}
	start, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil || start == 0 {
		return "", errors.New("invalid /proc process start time")
	}
	return fmt.Sprintf("linux-proc-start:%d", start), nil
}
