//go:build darwin || linux

package pidistribution

import (
	"errors"
	"io/fs"
	"syscall"
)

func unixMetadata(info fs.FileInfo) (uid uint32, links uint64, err error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, errors.New("file has no Unix stat metadata")
	}
	return stat.Uid, uint64(stat.Nlink), nil
}
