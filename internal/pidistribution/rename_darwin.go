//go:build darwin

package pidistribution

import "golang.org/x/sys/unix"

func atomicRenameNoReplace(source, target string) error {
	return unix.RenamexNp(source, target, unix.RENAME_EXCL)
}
