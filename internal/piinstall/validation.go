package piinstall

import (
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

type lockDocument struct {
	Name            string                 `json:"name"`
	Version         string                 `json:"version"`
	LockfileVersion int                    `json:"lockfileVersion"`
	Packages        map[string]lockPackage `json:"packages"`
}

type lockPackage struct {
	Name      string            `json:"name"`
	Version   string            `json:"version"`
	Resolved  string            `json:"resolved"`
	Integrity string            `json:"integrity"`
	Optional  bool              `json:"optional"`
	OS        []string          `json:"os"`
	CPU       []string          `json:"cpu"`
	Bin       map[string]string `json:"bin"`
}

func validateLockDocument(lock lockDocument, c Contract) error {
	if lock.LockfileVersion != 3 || lock.Name != "chora-pi-install-qualification" || lock.Version != "0.0.0" {
		return errors.New("consumer lock root identity is invalid")
	}
	root, ok := lock.Packages[""]
	if !ok {
		return errors.New("consumer lock root package is missing")
	}
	_ = root
	pi, ok := lock.Packages["node_modules/"+c.Package]
	if !ok || pi.Version != c.Version || pi.Resolved != "file:../qualified-package.tgz" || pi.Integrity != c.TarballSRI || pi.Bin["pi"] != c.ExecutableRelative {
		return errors.New("consumer lock Pi identity is invalid")
	}
	for path, pkg := range lock.Packages {
		if path == "" {
			continue
		}
		if path == "node_modules/"+c.Package {
			continue
		}
		if !strings.HasPrefix(pkg.Resolved, c.Registry+"/") || pkg.Integrity == "" || !strings.HasPrefix(pkg.Integrity, "sha512-") {
			return fmt.Errorf("consumer lock artifact %s is not fixed to registry SHA-512", path)
		}
	}
	return nil
}

func verifySRI(path, sri string, maximum int64) error {
	const prefix = "sha512-"
	if !strings.HasPrefix(sri, prefix) {
		return errors.New("unsupported SRI algorithm")
	}
	want, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(sri, prefix))
	if err != nil || len(want) != sha512.Size {
		return errors.New("invalid SHA-512 SRI")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maximum {
		return errors.New("downloaded tarball identity is unsafe")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && (int(stat.Uid) != os.Getuid() || stat.Nlink != 1) {
		return errors.New("downloaded tarball ownership is unsafe")
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	h := sha512.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if !equalBytes(h.Sum(nil), want) {
		return errors.New("downloaded tarball does not match frozen SRI")
	}
	return nil
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var different byte
	for i := range a {
		different |= a[i] ^ b[i]
	}
	return different == 0
}

func validateInstalledClosure(consumer string, c Contract) (string, string, string, error) {
	var lock lockDocument
	lockBytes, err := readRegularNoFollow(filepath.Join(consumer, "package-lock.json"), 8<<20)
	if err != nil {
		return "", "", "", err
	}
	if hexSHA256(lockBytes) != c.ConsumerLockSHA256 {
		return "", "", "", errors.New("consumer lock changed during installation")
	}
	manifest, err := readRegularNoFollow(filepath.Join(consumer, "package.json"), 1<<20)
	if err != nil {
		return "", "", "", err
	}
	if hexSHA256(manifest) != hexSHA256(c.ConsumerManifest) {
		return "", "", "", errors.New("consumer manifest changed during installation")
	}
	if err := json.Unmarshal(lockBytes, &lock); err != nil {
		return "", "", "", err
	}
	for path, pkg := range lock.Packages {
		if path == "" {
			continue
		}
		full := filepath.Join(consumer, filepath.FromSlash(path))
		applicable := platformAllows(pkg.OS, runtime.GOOS) && platformAllows(pkg.CPU, runtime.GOARCH)
		info, statErr := os.Lstat(full)
		if !applicable && pkg.Optional {
			if statErr == nil {
				return "", "", "", fmt.Errorf("platform-inapplicable optional package installed: %s", path)
			}
			if !os.IsNotExist(statErr) {
				return "", "", "", statErr
			}
			continue
		}
		if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", "", "", fmt.Errorf("required locked package missing: %s", path)
		}
		var metadata struct{ Name, Version string }
		data, err := readRegularNoFollow(filepath.Join(full, "package.json"), 1<<20)
		if err != nil || json.Unmarshal(data, &metadata) != nil || metadata.Name != packageNameFromLockPath(path) || metadata.Version != pkg.Version {
			return "", "", "", fmt.Errorf("installed package identity differs from lock: %s", path)
		}
	}
	known := map[string]bool{}
	for path := range lock.Packages {
		if path != "" {
			known[filepath.Clean(filepath.FromSlash(path))] = true
		}
	}
	err = filepath.WalkDir(filepath.Join(consumer, "node_modules"), func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Name() != "package.json" || entry.IsDir() {
			return nil
		}
		relDir, err := filepath.Rel(consumer, filepath.Dir(path))
		if err != nil {
			return err
		}
		if isPackageRoot(relDir) && !known[filepath.Clean(relDir)] {
			return fmt.Errorf("installed package is absent from frozen lock: %s", filepath.ToSlash(relDir))
		}
		return nil
	})
	if err != nil {
		return "", "", "", err
	}
	piRoot := filepath.Join(consumer, "node_modules", filepath.FromSlash(c.Package))
	var piManifest struct {
		Version string            `json:"version"`
		Bin     map[string]string `json:"bin"`
	}
	piData, err := readRegularNoFollow(filepath.Join(piRoot, "package.json"), 1<<20)
	if err != nil || json.Unmarshal(piData, &piManifest) != nil || piManifest.Version != c.Version || piManifest.Bin["pi"] != c.ExecutableRelative {
		return "", "", "", errors.New("installed Pi manifest identity is invalid")
	}
	execPath := filepath.Join(piRoot, filepath.FromSlash(piManifest.Bin["pi"]))
	resolved, err := filepath.EvalSymlinks(execPath)
	if err != nil {
		return "", "", "", fmt.Errorf("resolve installed Pi executable: %w", err)
	}
	if !inside(consumer, resolved) {
		return "", "", "", errors.New("installed Pi executable escapes consumer root")
	}
	execHash, err := validateExecutable(resolved)
	if err != nil {
		return "", "", "", err
	}
	closure, err := digestTree(consumer)
	if err != nil {
		return "", "", "", err
	}
	relative, _ := filepath.Rel(filepath.Dir(consumer), resolved)
	return closure, filepath.ToSlash(relative), execHash, nil
}

func platformAllows(rules []string, value string) bool {
	if value == "amd64" {
		value = "x64"
	}
	if len(rules) == 0 {
		return true
	}
	positive := false
	matched := false
	for _, rule := range rules {
		if strings.HasPrefix(rule, "!") {
			if strings.TrimPrefix(rule, "!") == value {
				return false
			}
			continue
		}
		positive = true
		if rule == value {
			matched = true
		}
	}
	return !positive || matched
}

func packageNameFromLockPath(path string) string {
	parts := strings.Split(filepath.ToSlash(path), "/")
	last := -1
	for i, part := range parts {
		if part == "node_modules" {
			last = i
		}
	}
	if last < 0 || last+1 >= len(parts) {
		return ""
	}
	if strings.HasPrefix(parts[last+1], "@") && last+2 < len(parts) {
		return parts[last+1] + "/" + parts[last+2]
	}
	return parts[last+1]
}

func isPackageRoot(path string) bool {
	parts := strings.Split(filepath.ToSlash(path), "/")
	last := -1
	for i, part := range parts {
		if part == "node_modules" {
			last = i
		}
	}
	if last < 0 || last+1 >= len(parts) {
		return false
	}
	after := parts[last+1:]
	if strings.HasPrefix(after[0], "@") {
		return len(after) == 2
	}
	return len(after) == 1
}

func validateExecutable(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o111 == 0 {
		return "", errors.New("Pi executable is not an executable regular file")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		if int(stat.Uid) != os.Getuid() {
			return "", errors.New("Pi executable owner differs from current user")
		}
		if stat.Nlink != 1 {
			return "", errors.New("Pi executable has unexpected hard links")
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return hexSHA256(data), nil
}

func digestTree(root string) (string, error) {
	var records []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); ok && int(stat.Uid) != os.Getuid() {
			return errors.New("installed closure has wrong owner")
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		switch {
		case info.Mode().IsRegular():
			if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Nlink != 1 {
				return fmt.Errorf("installed closure contains hard-linked file: %s", rel)
			}
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			h := sha256.New()
			_, copyErr := io.Copy(h, file)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			records = append(records, fmt.Sprintf("f\t%s\t%o\t%d\t%s", rel, info.Mode().Perm(), info.Size(), hex.EncodeToString(h.Sum(nil))))
		case info.IsDir():
			records = append(records, fmt.Sprintf("d\t%s\t%o", rel, info.Mode().Perm()))
		case info.Mode()&os.ModeSymlink != 0:
			target, err := filepath.EvalSymlinks(path)
			if err != nil || !inside(root, target) {
				return fmt.Errorf("installed closure symlink escapes root: %s", rel)
			}
			link, _ := os.Readlink(path)
			records = append(records, fmt.Sprintf("l\t%s\t%s", rel, link))
		default:
			return fmt.Errorf("installed closure contains unsupported file: %s", rel)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(records)
	return hexSHA256([]byte(strings.Join(records, "\n") + "\n")), nil
}

func inside(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && rel != "" && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
