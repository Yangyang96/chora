//go:build !darwin && !linux

package repositorycapture

import "errors"

func renameNoReplace(oldDirFD int, oldName string, newDirFD int, newName string) error {
	return errors.New("atomic no-replace rename is unsupported on this platform")
}
