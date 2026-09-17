package isolatedenv

import (
	"debug/elf"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func validatePackagedHelper(root, helper string) error {
	if helper != filepath.Join(root, "isolated-helper") {
		return errors.New("packaged helper must be inside the application resources")
	}
	info, err := os.Lstat(helper)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("packaged helper is unavailable")
	}
	expected, err := os.ReadFile(helper + ".sha256")
	if err != nil {
		return errors.New("packaged helper checksum is unavailable")
	}
	actual, err := digestRegular(helper, 512<<20)
	if err != nil || actual != strings.TrimSpace(string(expected)) {
		return errors.New("packaged helper checksum mismatch")
	}
	binary, err := elf.Open(helper)
	if err != nil {
		return errors.New("packaged helper is not a Linux executable")
	}
	defer binary.Close()
	if binary.Machine != elf.EM_AARCH64 || binary.Class != elf.ELFCLASS64 {
		return errors.New("packaged helper requires Linux arm64")
	}
	return nil
}
