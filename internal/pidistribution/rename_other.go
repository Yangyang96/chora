//go:build !darwin && !linux

package pidistribution

import "errors"

func atomicRenameNoReplace(string, string) error {
	return errors.New("atomic private Pi installation supports only darwin and linux")
}
