package pidistribution

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

type closure struct {
	root       string
	files      []ManifestFile
	digest     [32]byte
	executable executableIdentity
}

type executableIdentity struct {
	path     string
	resolved string
	digest   [32]byte
}

func inspectPATHPackage(pathValue string) (closure, error) {
	absolute, err := filepath.Abs(pathValue)
	if err != nil {
		return closure{}, fmt.Errorf("absolute PATH executable: %w", err)
	}
	absolute = filepath.Clean(absolute)
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return closure{}, fmt.Errorf("resolve PATH executable: %w", err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil || !canonicalAbsolute(filepath.Clean(resolved)) {
		return closure{}, errors.New("resolved PATH executable is not canonical and absolute")
	}
	resolved = filepath.Clean(resolved)
	root, err := findPackageRoot(resolved)
	if err != nil {
		return closure{}, err
	}
	observed, err := inspectClosure(root, nil, "")
	if err != nil {
		return closure{}, err
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil || !validRelativePath(filepath.ToSlash(relative)) {
		return closure{}, errors.New("resolved executable is outside package root")
	}
	manifestPath := filepath.ToSlash(relative)
	for _, file := range observed.files {
		if file.Path == manifestPath {
			if file.Mode&0o100 == 0 {
				return closure{}, errors.New("resolved Pi launcher is not owner-executable")
			}
			digest, _ := hex.DecodeString(file.SHA256)
			copy(observed.executable.digest[:], digest)
			observed.executable.path = absolute
			observed.executable.resolved = resolved
			return observed, nil
		}
	}
	return closure{}, errors.New("resolved executable is not in inspected closure")
}

func findPackageRoot(resolvedExecutable string) (string, error) {
	for directory := filepath.Dir(resolvedExecutable); ; directory = filepath.Dir(directory) {
		packagePath := filepath.Join(directory, "package.json")
		info, err := os.Lstat(packagePath)
		if err == nil {
			if !info.Mode().IsRegular() {
				return "", errors.New("package.json is not a regular file")
			}
			identity, err := readPackageIdentity(packagePath)
			if err != nil {
				return "", err
			}
			if identity.Name == ExpectedPackageName && identity.Version == SupportedVersion {
				canonical, err := filepath.EvalSymlinks(directory)
				if err != nil || filepath.Clean(canonical) != directory {
					return "", errors.New("package root is not canonical")
				}
				return directory, nil
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("inspect package identity: %w", err)
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			break
		}
	}
	return "", errors.New("supported Pi package root not found")
}

type packageIdentity struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

func readPackageIdentity(path string) (packageIdentity, error) {
	document, err := os.ReadFile(path)
	if err != nil {
		return packageIdentity{}, err
	}
	if len(document) > maxManifestBytes {
		return packageIdentity{}, errors.New("package.json is too large")
	}
	if err := rejectDuplicateKeys(document); err != nil {
		return packageIdentity{}, fmt.Errorf("strict package.json: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(document))
	var raw map[string]json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return packageIdentity{}, fmt.Errorf("decode package.json: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return packageIdentity{}, fmt.Errorf("decode package.json: %w", err)
	}
	if raw["name"] == nil || raw["version"] == nil {
		return packageIdentity{}, errors.New("package.json lacks name or version")
	}
	var identity packageIdentity
	if err := json.Unmarshal(raw["name"], &identity.Name); err != nil {
		return packageIdentity{}, errors.New("package.json name is not a string")
	}
	if err := json.Unmarshal(raw["version"], &identity.Version); err != nil {
		return packageIdentity{}, errors.New("package.json version is not a string")
	}
	return identity, nil
}

// inspectClosure inventories root. When expected is non-nil it enforces an
// exact manifest (plus an optional private .complete marker) rather than merely
// observing the tree.
func inspectClosure(root string, expected []ManifestFile, permittedExtra string) (closure, error) {
	if !canonicalAbsolute(root) {
		return closure{}, fmt.Errorf("%w: root is not canonical and absolute", ErrUnsafeClosure)
	}
	rootStart, err := os.Lstat(root)
	if err != nil || !rootStart.IsDir() || rootStart.Mode()&os.ModeSymlink != 0 {
		return closure{}, fmt.Errorf("%w: root is not a real directory", ErrUnsafeClosure)
	}
	if err := validateOwnerAndMode(rootStart, true); err != nil {
		return closure{}, err
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil || canonical != root {
		return closure{}, fmt.Errorf("%w: root traverses a symlink", ErrUnsafeClosure)
	}

	var files []ManifestFile
	var directories []string
	err = filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		if relative == "." {
			directories = append(directories, current)
			return nil
		}
		normalized := filepath.ToSlash(relative)
		if !validRelativePath(normalized) {
			return fmt.Errorf("%w: invalid path %q", ErrUnsafeClosure, normalized)
		}
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: symlink %q", ErrUnsafeClosure, normalized)
		}
		if info.IsDir() {
			if err := validateOwnerAndMode(info, true); err != nil {
				return fmt.Errorf("%q: %w", normalized, err)
			}
			directories = append(directories, current)
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%w: special file %q", ErrUnsafeClosure, normalized)
		}
		if normalized == permittedExtra {
			if err := validateOwnerAndMode(info, false); err != nil {
				return fmt.Errorf("%q: %w", normalized, err)
			}
			return nil
		}
		observed, err := inspectRegularFile(current, normalized, info)
		if err != nil {
			return err
		}
		files = append(files, observed)
		return nil
	})
	if err != nil {
		return closure{}, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	if expected != nil && !sameManifestFiles(files, expected) {
		return closure{}, fmt.Errorf("%w: missing, extra, duplicated, or modified file", ErrUnsafeClosure)
	}
	rootEnd, err := os.Lstat(root)
	if err != nil || !os.SameFile(rootStart, rootEnd) || rootStart.Mode() != rootEnd.Mode() || rootStart.ModTime() != rootEnd.ModTime() {
		return closure{}, fmt.Errorf("%w: root drifted during inspection", ErrUnsafeClosure)
	}
	// A second name-only traversal detects entry replacement after an individual
	// file was read and makes transient mutation visible to fixture tests.
	second, err := listClosureNames(root, permittedExtra)
	if err != nil || len(second) != len(files) {
		return closure{}, fmt.Errorf("%w: closure drifted during inspection", ErrUnsafeClosure)
	}
	for index := range files {
		if second[index] != files[index].Path {
			return closure{}, fmt.Errorf("%w: closure names drifted", ErrUnsafeClosure)
		}
	}
	_ = directories
	return closure{root: root, files: files, digest: ClosureDigest(files)}, nil
}

func inspectRegularFile(fullPath, relative string, before fs.FileInfo) (ManifestFile, error) {
	if err := validateOwnerAndMode(before, false); err != nil {
		return ManifestFile{}, fmt.Errorf("%q: %w", relative, err)
	}
	fd, err := unix.Open(fullPath, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return ManifestFile{}, fmt.Errorf("open %q without following links: %w", relative, err)
	}
	file := os.NewFile(uintptr(fd), fullPath)
	if file == nil {
		_ = unix.Close(fd)
		return ManifestFile{}, errors.New("construct file handle")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) || !opened.Mode().IsRegular() {
		return ManifestFile{}, fmt.Errorf("%w: %q changed before open", ErrUnsafeClosure, relative)
	}
	if err := validateOwnerAndMode(opened, false); err != nil {
		return ManifestFile{}, fmt.Errorf("%q: %w", relative, err)
	}
	hash := sha256.New()
	read, err := io.Copy(hash, file)
	if err != nil || read != opened.Size() {
		return ManifestFile{}, fmt.Errorf("read %q exactly: %w", relative, err)
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(opened, after) || opened.Size() != after.Size() || opened.Mode() != after.Mode() || opened.ModTime() != after.ModTime() {
		return ManifestFile{}, fmt.Errorf("%w: %q changed while hashing", ErrUnsafeClosure, relative)
	}
	return ManifestFile{Path: relative, Mode: uint32(opened.Mode().Perm()), Size: opened.Size(), SHA256: hex.EncodeToString(hash.Sum(nil))}, nil
}

func validateOwnerAndMode(info fs.FileInfo, directory bool) error {
	uid, links, err := unixMetadata(info)
	if err != nil || uid != uint32(os.Geteuid()) {
		return fmt.Errorf("%w: wrong owner", ErrUnsafeClosure)
	}
	if !directory && links != 1 {
		return fmt.Errorf("%w: hard-linked regular file", ErrUnsafeClosure)
	}
	mode := info.Mode()
	if mode&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || mode.Perm()&0o022 != 0 || mode.Perm()&0o700 == 0 {
		return fmt.Errorf("%w: unsafe mode %04o", ErrUnsafeClosure, mode.Perm())
	}
	if directory && mode.Perm()&0o100 == 0 {
		return fmt.Errorf("%w: directory is not owner-searchable", ErrUnsafeClosure)
	}
	return nil
}

func listClosureNames(root, permittedExtra string) ([]string, error) {
	var names []string
	err := filepath.WalkDir(root, func(current string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if current == root {
			return nil
		}
		relative, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		normalized := filepath.ToSlash(relative)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return ErrUnsafeClosure
		}
		if info.Mode().IsRegular() && normalized != permittedExtra {
			names = append(names, normalized)
		}
		return nil
	})
	sort.Strings(names)
	return names, err
}

func sameManifestFiles(left, right []ManifestFile) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func sameClosure(left, right closure) bool {
	return left.root == right.root && left.digest == right.digest && left.executable == right.executable && sameManifestFiles(left.files, right.files)
}

func isCrossDevice(err error) bool { return errors.Is(err, syscall.EXDEV) }

func cleanProbeOutput(output []byte) bool {
	return bytes.Equal(output, []byte(SupportedVersion)) || bytes.Equal(output, []byte(SupportedVersion+"\n"))
}

func normalizedExecutable(root, relative string) string {
	return filepath.Join(root, filepath.FromSlash(strings.TrimSpace(relative)))
}
