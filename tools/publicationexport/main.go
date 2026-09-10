// publicationexport copies an explicit set of repository files into a newly
// published directory without following writable pathnames during the copy.
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

type entry struct {
	Path   string `json:"path"`
	Mode   string `json:"mode"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type result struct {
	Target  string  `json:"target"`
	Entries []entry `json:"entries"`
}

type hooks struct {
	afterFirstRead        func(string)
	beforeDestinationOpen func(string, int, string)
	beforePublish         func(int, string, string)
	afterPublish          func(int, string)
}

func main() {
	var source, target, pathsFile string
	flag.StringVar(&source, "source", "", "canonical source root")
	flag.StringVar(&target, "target", "", "new target directory")
	flag.StringVar(&pathsFile, "paths", "", "JSON file containing candidate paths")
	flag.Parse()
	encoded, err := os.ReadFile(pathsFile)
	if err != nil {
		fatal(err)
	}
	var paths []string
	if err := json.Unmarshal(encoded, &paths); err != nil {
		fatal(err)
	}
	got, err := export(source, target, paths, nil)
	if err != nil {
		fatal(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(got); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func export(sourceRoot, target string, paths []string, testHooks *hooks) (got result, resultErr error) {
	sourceRoot, err := filepath.EvalSymlinks(filepath.Clean(sourceRoot))
	if err != nil {
		return got, fmt.Errorf("resolve source root: %w", err)
	}
	target = filepath.Clean(target)
	parentPath, err := filepath.EvalSymlinks(filepath.Dir(target))
	if err != nil {
		return got, fmt.Errorf("resolve target parent: %w", err)
	}
	target = filepath.Join(parentPath, filepath.Base(target))
	if inside(sourceRoot, target) {
		return got, errors.New("publication export target must be outside the source repository")
	}

	source, err := openDirectory(sourceRoot)
	if err != nil {
		return got, fmt.Errorf("open source root: %w", err)
	}
	defer source.Close()
	parent, err := openDirectory(parentPath)
	if err != nil {
		return got, fmt.Errorf("open target parent: %w", err)
	}
	defer parent.Close()
	parentIdentity, err := parent.Stat()
	if err != nil {
		return got, fmt.Errorf("stat target parent: %w", err)
	}
	targetName := filepath.Base(target)
	if err := requireAbsent(int(parent.Fd()), targetName); err != nil {
		return got, err
	}

	stagingName, err := createStaging(int(parent.Fd()))
	if err != nil {
		return got, err
	}
	published := false
	defer func() {
		if !published {
			resultErr = errors.Join(resultErr, removeDirectoryAt(int(parent.Fd()), stagingName))
		}
	}()
	stagingFD, err := unix.Openat(int(parent.Fd()), stagingName, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return got, fmt.Errorf("open staging directory: %w", err)
	}
	staging := os.NewFile(uintptr(stagingFD), stagingName)
	if staging == nil {
		_ = unix.Close(stagingFD)
		return got, errors.New("construct staging handle")
	}
	defer staging.Close()
	stagingIdentity, err := staging.Stat()
	if err != nil {
		return got, fmt.Errorf("stat staging directory: %w", err)
	}

	ordered := append([]string(nil), paths...)
	sort.Strings(ordered)
	for index, relative := range ordered {
		if index > 0 && ordered[index-1] == relative {
			return got, fmt.Errorf("duplicate candidate path: %s", relative)
		}
		if err := validateRelative(relative); err != nil {
			return got, err
		}
		item, err := copyOne(int(source.Fd()), int(staging.Fd()), relative, testHooks)
		if err != nil {
			return got, err
		}
		got.Entries = append(got.Entries, item)
	}
	if testHooks != nil && testHooks.beforePublish != nil {
		testHooks.beforePublish(int(parent.Fd()), stagingName, targetName)
	}
	if err := requireSameDirectoryPath(parentPath, parentIdentity); err != nil {
		return got, err
	}
	if err := renameNoReplace(int(parent.Fd()), stagingName, int(parent.Fd()), targetName); err != nil {
		return got, fmt.Errorf("publish target exclusively: %w", err)
	}
	published = true
	if err := requireSameDirectoryAt(int(parent.Fd()), targetName, stagingIdentity); err != nil {
		return got, errors.Join(err, removeDirectoryAt(int(parent.Fd()), targetName))
	}
	if testHooks != nil && testHooks.afterPublish != nil {
		testHooks.afterPublish(int(parent.Fd()), targetName)
	}
	if err := requireSameDirectoryPath(parentPath, parentIdentity); err != nil {
		return got, errors.Join(err, removeDirectoryAt(int(parent.Fd()), targetName))
	}
	got.Target = target
	return got, nil
}

func copyOne(sourceRootFD, destinationRootFD int, relative string, testHooks *hooks) (entry, error) {
	parts := strings.Split(relative, "/")
	sourceParent, err := openPathDirectories(sourceRootFD, parts[:len(parts)-1], false)
	if err != nil {
		return entry{}, fmt.Errorf("open source parent for %s: %w", relative, err)
	}
	defer unix.Close(sourceParent)
	sourceFD, err := unix.Openat(sourceParent, parts[len(parts)-1], unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return entry{}, fmt.Errorf("open source file %s: %w", relative, err)
	}
	source := os.NewFile(uintptr(sourceFD), relative)
	if source == nil {
		_ = unix.Close(sourceFD)
		return entry{}, errors.New("construct source handle")
	}
	defer source.Close()
	before, err := source.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return entry{}, fmt.Errorf("candidate path is not a regular file: %s", relative)
	}

	destinationParent, err := openPathDirectories(destinationRootFD, parts[:len(parts)-1], true)
	if err != nil {
		return entry{}, fmt.Errorf("create destination parent for %s: %w", relative, err)
	}
	defer unix.Close(destinationParent)
	base := parts[len(parts)-1]
	if testHooks != nil && testHooks.beforeDestinationOpen != nil {
		testHooks.beforeDestinationOpen(relative, destinationParent, base)
	}
	destinationFD, err := unix.Openat(destinationParent, base, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, uint32(before.Mode().Perm()))
	if err != nil {
		return entry{}, fmt.Errorf("create destination file %s: %w", relative, err)
	}
	destination := os.NewFile(uintptr(destinationFD), relative)
	if destination == nil {
		_ = unix.Close(destinationFD)
		return entry{}, errors.New("construct destination handle")
	}
	hash := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(destination, hash), source)
	if copyErr == nil {
		copyErr = unix.Fchmod(destinationFD, uint32(before.Mode().Perm()))
	}
	if closeErr := destination.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		return entry{}, fmt.Errorf("copy candidate file %s: %w", relative, copyErr)
	}
	if testHooks != nil && testHooks.afterFirstRead != nil {
		testHooks.afterFirstRead(relative)
	}
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return entry{}, err
	}
	second := sha256.New()
	secondSize, err := io.Copy(second, source)
	if err != nil {
		return entry{}, err
	}
	after, err := source.Stat()
	if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) || secondSize != size || !strings.EqualFold(hex.EncodeToString(second.Sum(nil)), hex.EncodeToString(hash.Sum(nil))) {
		return entry{}, fmt.Errorf("candidate source changed during export: %s", relative)
	}
	return entry{Path: relative, Mode: fmt.Sprintf("%03o", before.Mode().Perm()), Size: size, SHA256: hex.EncodeToString(hash.Sum(nil))}, nil
}

func openPathDirectories(rootFD int, parts []string, create bool) (int, error) {
	current, err := unix.Dup(rootFD)
	if err != nil {
		return -1, err
	}
	for _, part := range parts {
		if create {
			if err := unix.Mkdirat(current, part, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
				unix.Close(current)
				return -1, err
			}
		}
		next, err := unix.Openat(current, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		unix.Close(current)
		if err != nil {
			return -1, err
		}
		current = next
	}
	return current, nil
}

func validateRelative(path string) error {
	if path == "" || strings.Contains(path, "\\") || filepath.IsAbs(path) || filepath.Clean(path) != filepath.FromSlash(path) {
		return fmt.Errorf("invalid candidate path: %s", path)
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("invalid candidate path: %s", path)
		}
	}
	return nil
}

func openDirectory(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("construct directory handle")
	}
	return file, nil
}

func requireSameDirectoryPath(path string, before os.FileInfo) error {
	after, err := os.Stat(path)
	if err != nil || !after.IsDir() || !os.SameFile(before, after) {
		return errors.New("publication export target parent identity changed")
	}
	return nil
}

func requireSameDirectoryAt(parentFD int, name string, before os.FileInfo) error {
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return errors.New("published target identity changed")
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return errors.New("construct published target handle")
	}
	after, statErr := file.Stat()
	closeErr := file.Close()
	if statErr != nil || closeErr != nil || !after.IsDir() || !os.SameFile(before, after) {
		return errors.New("published target identity changed")
	}
	return nil
}

func requireAbsent(parentFD int, name string) error {
	var stat unix.Stat_t
	err := unix.Fstatat(parentFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if err == nil {
		return errors.New("publication export target already exists")
	}
	if !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("inspect publication export target: %w", err)
	}
	return nil
}

func createStaging(parentFD int) (string, error) {
	for attempt := 0; attempt < 32; attempt++ {
		bytes := make([]byte, 12)
		if _, err := rand.Read(bytes); err != nil {
			return "", err
		}
		name := ".chora-public-export-" + hex.EncodeToString(bytes)
		if err := unix.Mkdirat(parentFD, name, 0o700); err == nil {
			return name, nil
		} else if !errors.Is(err, unix.EEXIST) {
			return "", err
		}
	}
	return "", errors.New("allocate publication export staging directory")
}

func removeDirectoryAt(parentFD int, name string) error {
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	directory := os.NewFile(uintptr(fd), name)
	if directory == nil {
		_ = unix.Close(fd)
		return errors.New("construct cleanup handle")
	}
	names, readErr := directory.Readdirnames(-1)
	for _, child := range names {
		var stat unix.Stat_t
		if err := unix.Fstatat(fd, child, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			readErr = errors.Join(readErr, err)
			continue
		}
		if stat.Mode&unix.S_IFMT == unix.S_IFDIR {
			readErr = errors.Join(readErr, removeDirectoryAt(fd, child))
		} else {
			readErr = errors.Join(readErr, unix.Unlinkat(fd, child, 0))
		}
	}
	readErr = errors.Join(readErr, directory.Close())
	if readErr == nil {
		readErr = unix.Unlinkat(parentFD, name, unix.AT_REMOVEDIR)
	}
	return readErr
}

func inside(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && (relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))))
}
