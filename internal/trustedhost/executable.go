package trustedhost

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

// ExecutableIdentity is the exact local Pi launcher selected by discovery.
// The resolved target and its bytes are revalidated immediately before every
// spawn and again inside the pre-exec gate.
type ExecutableIdentity struct {
	Path         string
	ResolvedPath string
	SHA256       [32]byte
}

// InspectExecutable resolves and fingerprints one absolute executable.
func InspectExecutable(path string) (ExecutableIdentity, error) {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return ExecutableIdentity{}, errors.New("executable path must be absolute and clean")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || !filepath.IsAbs(resolved) || filepath.Clean(resolved) != resolved {
		return ExecutableIdentity{}, errors.New("resolve executable path")
	}
	file, err := os.Open(resolved)
	if err != nil {
		return ExecutableIdentity{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return ExecutableIdentity{}, errors.New("executable target must be an executable regular file")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return ExecutableIdentity{}, err
	}
	var digest [32]byte
	copy(digest[:], hash.Sum(nil))
	return ExecutableIdentity{Path: path, ResolvedPath: resolved, SHA256: digest}, nil
}

func validateExecutableIdentity(expected ExecutableIdentity) error {
	observed, err := InspectExecutable(expected.Path)
	if err != nil {
		return err
	}
	if observed != expected {
		return fmt.Errorf("executable identity drift: resolved path or SHA-256 changed")
	}
	return nil
}

func validateAllowedArguments(actual, expected []string, trailingRoot string, observerPaths ...string) error {
	if len(observerPaths) == 1 && observerPaths[0] != "" && len(actual) >= 4 && actual[2] == "--extension" && actual[3] == observerPaths[0] {
		copyArgs := append([]string{}, actual[:2]...)
		actual = append(copyArgs, actual[4:]...)
	}
	if err := validateArguments(actual); err != nil {
		return err
	}
	if trailingRoot == "" {
		if !reflect.DeepEqual(actual, expected) {
			return errors.New("invocation arguments do not match the pinned native Pi argv")
		}
		return nil
	}
	if (len(actual) != len(expected)+1 && len(actual) != len(expected)+3) || !reflect.DeepEqual(actual[:len(expected)], expected) {
		return errors.New("invocation arguments do not match the pinned native Pi session argv")
	}
	trailing := actual[len(expected)]
	if !filepath.IsAbs(trailing) || filepath.Clean(trailing) != trailing {
		return errors.New("trailing argument must be an absolute clean path")
	}
	relative, err := filepath.Rel(trailingRoot, trailing)
	if err != nil || relative == "." || relative == ".." || strings.Contains(relative, string(filepath.Separator)) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("trailing argument must be inside the allowed path root")
	}
	if len(actual) == len(expected)+3 && (actual[len(expected)+1] != "--session" || !validSessionUUID(actual[len(expected)+2])) {
		return errors.New("resume arguments require exactly --session plus a UUID")
	}
	return nil
}

func validSessionUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	for index, r := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') && !(r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}

func forbiddenRuntimeEnvironment(key string) bool {
	upper := strings.ToUpper(key)
	if strings.HasPrefix(upper, "DYLD_") || strings.HasPrefix(upper, "LD_") {
		return true
	}
	switch upper {
	case "NODE_OPTIONS", "NODE_PATH",
		"BASH_ENV", "ENV", "SHELLOPTS",
		"PYTHONHOME", "PYTHONPATH", "PYTHONINSPECT", "PYTHONSTARTUP",
		"RUBYOPT", "RUBYLIB", "PERL5OPT", "PERL5LIB",
		"JAVA_TOOL_OPTIONS", "JDK_JAVA_OPTIONS", "_JAVA_OPTIONS", "CLASSPATH",
		"GCONV_PATH", "GLIBC_TUNABLES", "MALLOC_TRACE":
		return true
	default:
		return false
	}
}
