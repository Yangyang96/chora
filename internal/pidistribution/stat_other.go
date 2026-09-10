//go:build !darwin && !linux

package pidistribution

import (
	"errors"
	"io/fs"
)

func unixMetadata(fs.FileInfo) (uint32, uint64, error) {
	return 0, 0, errors.New("Pi distribution supports only darwin and linux")
}
