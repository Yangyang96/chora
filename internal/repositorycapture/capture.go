// Package repositorycapture creates a durable, read-only copy of a repository
// without following names out of the source tree.
package repositorycapture

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

const (
	ReceiptSchema = "chora.m1-o4-repository-capture-receipt.v1"
	maxSafeJSON   = int64(1<<53 - 1)
)

type limits struct {
	MaxDepth     int
	MaxEntries   int64
	MaxPathBytes int64
	MaxFileBytes int64
	MaxTotal     int64
}

var defaultLimits = limits{
	MaxDepth:     64,
	MaxEntries:   50_000,
	MaxPathBytes: 4 << 20,
	MaxFileBytes: 128 << 20,
	MaxTotal:     512 << 20,
}

type Identity struct {
	Mode int    `json:"mode"`
	UID  int    `json:"uid"`
	GID  int    `json:"gid"`
	Dev  string `json:"dev"`
	Ino  string `json:"ino"`
}

// OwnedPathIdentity is the exact lstat identity accepted by the trusted
// owned-path cleanup and publication protocols. Mode includes both the Unix
// node-type and permission bits; Dev and Ino are canonical decimal strings so
// JavaScript callers do not lose precision.
type OwnedPathIdentity struct {
	Mode int    `json:"mode"`
	UID  int    `json:"uid"`
	GID  int    `json:"gid"`
	Dev  string `json:"dev"`
	Ino  string `json:"ino"`
}

type OwnedPathDisposition string

const (
	RegularFile        OwnedPathDisposition = "regular_file"
	EmptyDirectory     OwnedPathDisposition = "empty_directory"
	RecursiveDirectory OwnedPathDisposition = "recursive_directory"
	UnixSocket         OwnedPathDisposition = "unix_socket"
)

type Receipt struct {
	SchemaVersion         string   `json:"schemaVersion"`
	Status                string   `json:"status"`
	SourceRoot            string   `json:"sourceRoot"`
	CaptureRoot           string   `json:"captureRoot"`
	SourceRootIdentity    Identity `json:"sourceRootIdentity"`
	SourceGitRootIdentity Identity `json:"sourceGitRootIdentity"`
	EntryCount            int64    `json:"entryCount"`
	TotalBytes            int64    `json:"totalBytes"`
	NetworkUsed           bool     `json:"networkUsed"`
}

// hooks deliberately expose mutation points only to in-package tests. They
// make race rejection deterministic without weakening the production API.
type hooks struct {
	beforeSourceEntryOpen       func(relative string)
	afterSourceEntryOpen        func(relative string)
	afterSourceFileRead         func(relative string)
	afterSourceDirectoryRead    func(relative string)
	afterCaptureRootCreated     func()
	afterDestinationCreate      func(relative string)
	beforeReceiptCreate         func()
	afterReceiptCreated         func()
	beforeCleanup               func()
	beforeCleanupQuarantine     func(relative string)
	beforeOwnedPathQuarantine   func(relative string)
	beforeOwnedPathPublish      func()
	afterOwnedPathReplaceChecks func()
	afterOwnedPathReplaceMove   func()
	beforeReadonlyPublish       func(parentFD int, stagingName, targetName string)
	afterReadonlyPublishMove    func(parentFD int, targetName string)
	beforeReadonlyFinalVerify   func(parentFD int, targetName string)
	beforeOwnedDirectoryCreate  func()
	afterOwnedDirectoryCreate   func()
}

type config struct {
	limits limits
	hooks  *hooks
}

type nodeStat struct {
	mode      uint32
	uid       uint32
	gid       uint32
	dev       uint64
	ino       uint64
	nlink     uint64
	size      int64
	ctimeSec  int64
	ctimeNsec int64
	hasCTime  bool
}

type captureState struct {
	config     config
	entryCount int64
	pathBytes  int64
	totalBytes int64
}

// Capture copies sourceRoot to a newly-created captureRoot and publishes an
// exclusive owner-read-only receipt after all capture data is durable.
func Capture(sourceRoot, captureRoot, receiptPath string) (Receipt, error) {
	return captureWithConfig(sourceRoot, captureRoot, receiptPath, config{limits: defaultLimits})
}

func captureWithConfig(sourceRoot, captureRoot, receiptPath string, cfg config) (receipt Receipt, resultErr error) {
	if err := validateInputs(sourceRoot, captureRoot, receiptPath, cfg.limits); err != nil {
		return Receipt{}, err
	}

	sourceParent, sourceBase, err := openCanonicalParent(sourceRoot)
	if err != nil {
		return Receipt{}, fmt.Errorf("open source parent: %w", err)
	}
	defer sourceParent.Close()
	sourceEntry, err := statAt(int(sourceParent.Fd()), sourceBase)
	if err != nil {
		return Receipt{}, fmt.Errorf("stat source root: %w", err)
	}
	if err := validateDirectory(sourceEntry, false); err != nil {
		return Receipt{}, fmt.Errorf("source root: %w", err)
	}
	sourceFD, err := openDirectoryAt(int(sourceParent.Fd()), sourceBase, sourceEntry)
	if err != nil {
		return Receipt{}, fmt.Errorf("open source root: %w", err)
	}
	source := os.NewFile(uintptr(sourceFD), sourceRoot)
	if source == nil {
		_ = unix.Close(sourceFD)
		return Receipt{}, errors.New("construct source root handle")
	}
	defer source.Close()
	sourceStartInfo, err := source.Stat()
	if err != nil {
		return Receipt{}, err
	}

	gitEntry, err := statAt(int(source.Fd()), ".git")
	if err != nil {
		return Receipt{}, errors.New("source .git is absent")
	}
	if nodeType(gitEntry.mode) == unix.S_IFDIR {
		if err := validateDirectory(gitEntry, false); err != nil {
			return Receipt{}, fmt.Errorf("source .git: %w", err)
		}
	} else if nodeType(gitEntry.mode) == unix.S_IFREG {
		if err := validateRegular(gitEntry); err != nil {
			return Receipt{}, fmt.Errorf("source .git: %w", err)
		}
	} else {
		return Receipt{}, errors.New("source .git must be an owner-safe directory or regular file")
	}

	captureParent, captureBase, err := openCanonicalParent(captureRoot)
	if err != nil {
		return Receipt{}, fmt.Errorf("open capture parent: %w", err)
	}
	defer captureParent.Close()
	if err := validateControlledParent(captureParent); err != nil {
		return Receipt{}, fmt.Errorf("capture parent: %w", err)
	}
	if err := requireAbsentAt(int(captureParent.Fd()), captureBase); err != nil {
		return Receipt{}, fmt.Errorf("capture root: %w", err)
	}
	if err := unix.Mkdirat(int(captureParent.Fd()), captureBase, 0o700); err != nil {
		return Receipt{}, fmt.Errorf("create capture root: %w", err)
	}
	createdEntry, err := statAt(int(captureParent.Fd()), captureBase)
	if err != nil {
		return Receipt{}, errors.Join(fmt.Errorf("stat created capture root: %w", err), removeEmptyCreatedRoot(captureParent, captureBase, nodeStat{}))
	}
	if err := validateCreatedDirectory(createdEntry); err != nil {
		return Receipt{}, errors.Join(err, removeEmptyCreatedRoot(captureParent, captureBase, createdEntry))
	}
	captureFD, err := openDirectoryAt(int(captureParent.Fd()), captureBase, createdEntry)
	if err != nil {
		return Receipt{}, errors.Join(fmt.Errorf("open capture root: %w", err), removeEmptyCreatedRoot(captureParent, captureBase, createdEntry))
	}
	capture := os.NewFile(uintptr(captureFD), captureRoot)
	if capture == nil {
		_ = unix.Close(captureFD)
		return Receipt{}, errors.New("construct capture root handle")
	}
	defer capture.Close()
	cleanup := true
	defer func() {
		if !cleanup {
			return
		}
		if cfg.hooks != nil && cfg.hooks.beforeCleanup != nil {
			cfg.hooks.beforeCleanup()
		}
		cleanupErr := cleanupCapture(capture, captureParent, captureBase, createdEntry, cfg.hooks)
		if cleanupErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("cleanup partial capture: %w", cleanupErr))
		}
	}()
	if cfg.hooks != nil && cfg.hooks.afterCaptureRootCreated != nil {
		cfg.hooks.afterCaptureRootCreated()
	}

	state := &captureState{config: cfg}
	if err := state.copyDirectory(source, capture, "", 0, false); err != nil {
		return Receipt{}, err
	}
	if err := verifyEntryStable(int(source.Fd()), ".git", gitEntry); err != nil {
		return Receipt{}, fmt.Errorf("source .git changed during capture: %w", err)
	}
	if err := verifyFileInfoStable(sourceStartInfo, source); err != nil {
		return Receipt{}, fmt.Errorf("source root changed during capture: %w", err)
	}
	if err := verifyEntryStable(int(sourceParent.Fd()), sourceBase, sourceEntry); err != nil {
		return Receipt{}, fmt.Errorf("source root parent entry changed: %w", err)
	}
	if err := verifyObjectStable(int(captureParent.Fd()), captureBase, createdEntry); err != nil {
		return Receipt{}, fmt.Errorf("capture root was replaced: %w", err)
	}
	if err := capture.Sync(); err != nil {
		return Receipt{}, fmt.Errorf("sync capture root: %w", err)
	}
	if err := captureParent.Sync(); err != nil {
		return Receipt{}, fmt.Errorf("sync capture parent: %w", err)
	}

	receipt = Receipt{
		SchemaVersion:         ReceiptSchema,
		Status:                "passed",
		SourceRoot:            sourceRoot,
		CaptureRoot:           captureRoot,
		SourceRootIdentity:    receiptIdentity(sourceEntry),
		SourceGitRootIdentity: receiptIdentity(gitEntry),
		EntryCount:            state.entryCount,
		TotalBytes:            state.totalBytes,
		NetworkUsed:           false,
	}
	if cfg.hooks != nil && cfg.hooks.beforeReceiptCreate != nil {
		cfg.hooks.beforeReceiptCreate()
	}
	if err := writeReceipt(receiptPath, receipt, cfg.hooks); err != nil {
		return Receipt{}, err
	}
	cleanup = false
	return receipt, nil
}

func (state *captureState) copyDirectory(source, destination *os.File, relative string, depth int, preserveMode bool) error {
	if depth > state.config.limits.MaxDepth {
		return errors.New("repository depth limit exceeded")
	}
	before, err := source.Stat()
	if err != nil || !before.IsDir() {
		return errors.New("source directory identity is invalid")
	}
	beforeStat, err := nodeFromFileInfo(before)
	if err != nil {
		return err
	}
	if err := validateDirectory(beforeStat, false); err != nil {
		return err
	}
	entries, err := source.ReadDir(-1)
	if err != nil {
		return fmt.Errorf("enumerate source directory: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	previous := ""
	for _, entry := range entries {
		name := entry.Name()
		if !validName(name) || (previous != "" && name == previous) {
			return errors.New("source directory contains an unsafe or duplicate name")
		}
		previous = name
		childRelative := name
		if relative != "" {
			childRelative = relative + "/" + name
		}
		if err := state.accountPath(childRelative); err != nil {
			return err
		}
		if state.config.hooks != nil && state.config.hooks.beforeSourceEntryOpen != nil {
			state.config.hooks.beforeSourceEntryOpen(childRelative)
		}
		entryBefore, err := statAt(int(source.Fd()), name)
		if err != nil {
			return fmt.Errorf("stat source entry %q: %w", childRelative, err)
		}
		switch nodeType(entryBefore.mode) {
		case unix.S_IFDIR:
			if err := state.copySubdirectory(source, destination, name, childRelative, depth+1, entryBefore); err != nil {
				return err
			}
		case unix.S_IFREG:
			if err := state.copyFile(source, destination, name, childRelative, entryBefore); err != nil {
				return err
			}
		default:
			return fmt.Errorf("source entry %q is a symlink or special file", childRelative)
		}
	}
	if state.config.hooks != nil && state.config.hooks.afterSourceDirectoryRead != nil {
		state.config.hooks.afterSourceDirectoryRead(relative)
	}
	if err := verifyFileInfoStable(before, source); err != nil {
		return fmt.Errorf("source directory %q changed: %w", relative, err)
	}
	if preserveMode {
		if err := unix.Fchmod(int(destination.Fd()), beforeStat.mode&0o7777); err != nil {
			return fmt.Errorf("preserve directory mode %q: %w", relative, err)
		}
	}
	if err := destination.Sync(); err != nil {
		return fmt.Errorf("sync destination directory %q: %w", relative, err)
	}
	return nil
}

func (state *captureState) copySubdirectory(sourceParent, destinationParent *os.File, name, relative string, depth int, before nodeStat) error {
	if err := validateDirectory(before, false); err != nil {
		return fmt.Errorf("source directory %q: %w", relative, err)
	}
	sourceFD, err := openDirectoryAt(int(sourceParent.Fd()), name, before)
	if err != nil {
		return fmt.Errorf("open source directory %q: %w", relative, err)
	}
	source := os.NewFile(uintptr(sourceFD), relative)
	if source == nil {
		_ = unix.Close(sourceFD)
		return errors.New("construct source directory handle")
	}
	defer source.Close()
	if state.config.hooks != nil && state.config.hooks.afterSourceEntryOpen != nil {
		state.config.hooks.afterSourceEntryOpen(relative)
	}
	if err := unix.Mkdirat(int(destinationParent.Fd()), name, 0o700); err != nil {
		return fmt.Errorf("create destination directory %q: %w", relative, err)
	}
	destinationBefore, err := statAt(int(destinationParent.Fd()), name)
	if err != nil || validateCreatedDirectory(destinationBefore) != nil {
		return fmt.Errorf("created destination directory %q is unsafe", relative)
	}
	destinationFD, err := openDirectoryAt(int(destinationParent.Fd()), name, destinationBefore)
	if err != nil {
		return err
	}
	destination := os.NewFile(uintptr(destinationFD), relative)
	if destination == nil {
		_ = unix.Close(destinationFD)
		return errors.New("construct destination directory handle")
	}
	defer destination.Close()
	if state.config.hooks != nil && state.config.hooks.afterDestinationCreate != nil {
		state.config.hooks.afterDestinationCreate(relative)
	}
	if err := state.copyDirectory(source, destination, relative, depth, true); err != nil {
		return err
	}
	if err := verifyEntryStable(int(sourceParent.Fd()), name, before); err != nil {
		return fmt.Errorf("source directory entry %q changed: %w", relative, err)
	}
	if err := verifyObjectStable(int(destinationParent.Fd()), name, destinationBefore); err != nil {
		return fmt.Errorf("destination directory entry %q changed: %w", relative, err)
	}
	return nil
}

func (state *captureState) copyFile(sourceParent, destinationParent *os.File, name, relative string, before nodeStat) error {
	if err := validateRegular(before); err != nil {
		return fmt.Errorf("source file %q: %w", relative, err)
	}
	if before.size > state.config.limits.MaxFileBytes {
		return fmt.Errorf("source file %q exceeds file limit", relative)
	}
	if before.size > state.config.limits.MaxTotal-state.totalBytes {
		return errors.New("repository total-byte limit exceeded")
	}
	sourceFD, err := unix.Openat(int(sourceParent.Fd()), name,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_NOCTTY, 0)
	if err != nil {
		return fmt.Errorf("open source file %q: %w", relative, err)
	}
	source := os.NewFile(uintptr(sourceFD), relative)
	if source == nil {
		_ = unix.Close(sourceFD)
		return errors.New("construct source file handle")
	}
	defer source.Close()
	opened, err := source.Stat()
	if err != nil {
		return err
	}
	openedStat, err := nodeFromFileInfo(opened)
	if err != nil || !sameNode(before, openedStat) || validateRegular(openedStat) != nil {
		return fmt.Errorf("source file %q changed before open", relative)
	}
	if state.config.hooks != nil && state.config.hooks.afterSourceEntryOpen != nil {
		state.config.hooks.afterSourceEntryOpen(relative)
	}
	destinationFD, err := unix.Openat(int(destinationParent.Fd()), name,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("create destination file %q: %w", relative, err)
	}
	destination := os.NewFile(uintptr(destinationFD), relative)
	if destination == nil {
		_ = unix.Close(destinationFD)
		return errors.New("construct destination file handle")
	}
	defer destination.Close()
	destinationEntry, err := statAt(int(destinationParent.Fd()), name)
	if err != nil {
		return fmt.Errorf("stat destination file %q: %w", relative, err)
	}
	destinationOpened, err := destination.Stat()
	if err != nil {
		return err
	}
	destinationOpenedStat, err := nodeFromFileInfo(destinationOpened)
	if err != nil || !sameNode(destinationEntry, destinationOpenedStat) || validateRegular(destinationOpenedStat) != nil {
		return fmt.Errorf("created destination file %q is unsafe", relative)
	}
	if state.config.hooks != nil && state.config.hooks.afterDestinationCreate != nil {
		state.config.hooks.afterDestinationCreate(relative)
	}
	if err := copyExact(destination, source, before.size); err != nil {
		return fmt.Errorf("copy source file %q exactly: %w", relative, err)
	}
	if state.config.hooks != nil && state.config.hooks.afterSourceFileRead != nil {
		state.config.hooks.afterSourceFileRead(relative)
	}
	if err := verifyFileInfoStable(opened, source); err != nil {
		return fmt.Errorf("source file %q changed while copying: %w", relative, err)
	}
	if err := verifyEntryStable(int(sourceParent.Fd()), name, before); err != nil {
		return fmt.Errorf("source file entry %q changed: %w", relative, err)
	}
	if err := verifyObjectStable(int(destinationParent.Fd()), name, destinationEntry); err != nil {
		return fmt.Errorf("destination file entry %q changed: %w", relative, err)
	}
	if err := destination.Sync(); err != nil {
		return fmt.Errorf("sync destination file %q: %w", relative, err)
	}
	if err := unix.Fchmod(int(destination.Fd()), before.mode&0o7777); err != nil {
		return fmt.Errorf("preserve file mode %q: %w", relative, err)
	}
	if err := destination.Sync(); err != nil {
		return fmt.Errorf("sync destination file mode %q: %w", relative, err)
	}
	if err := verifyObjectStable(int(destinationParent.Fd()), name, destinationEntry); err != nil {
		return fmt.Errorf("destination file entry %q changed after mode preservation: %w", relative, err)
	}
	destinationInfo, err := destination.Stat()
	if err != nil || !destinationInfo.Mode().IsRegular() || destinationInfo.Size() != before.size || destinationInfo.Mode().Perm() != os.FileMode(before.mode&0o777) {
		return fmt.Errorf("destination file %q verification failed", relative)
	}
	state.totalBytes += before.size
	return nil
}

func copyExact(destination io.Writer, source io.Reader, size int64) error {
	written, err := io.CopyN(destination, source, size)
	if err != nil || written != size {
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return err
	}
	var extra [1]byte
	count, err := source.Read(extra[:])
	if count != 0 || (err != nil && !errors.Is(err, io.EOF)) {
		return errors.New("source grew beyond its bounded size")
	}
	return nil
}

func (state *captureState) accountPath(relative string) error {
	state.entryCount++
	if state.entryCount > state.config.limits.MaxEntries || state.entryCount > maxSafeJSON {
		return errors.New("repository entry-count limit exceeded")
	}
	if int64(len(relative)) > state.config.limits.MaxPathBytes-state.pathBytes {
		return errors.New("repository path-byte limit exceeded")
	}
	state.pathBytes += int64(len(relative))
	return nil
}

func validateInputs(sourceRoot, captureRoot, receiptPath string, limits limits) error {
	for _, path := range []string{sourceRoot, captureRoot, receiptPath} {
		if !canonicalAbsolute(path) {
			return errors.New("all paths must be exact canonical absolute paths")
		}
	}
	if sourceRoot == captureRoot || sourceRoot == receiptPath || captureRoot == receiptPath ||
		pathContains(sourceRoot, captureRoot) || pathContains(sourceRoot, receiptPath) || pathContains(captureRoot, receiptPath) {
		return errors.New("source, capture, and receipt paths must be disjoint")
	}
	if limits.MaxDepth < 0 || limits.MaxEntries < 1 || limits.MaxPathBytes < 1 || limits.MaxFileBytes < 0 ||
		limits.MaxTotal < 0 || limits.MaxEntries > maxSafeJSON || limits.MaxPathBytes > maxSafeJSON ||
		limits.MaxFileBytes > maxSafeJSON || limits.MaxTotal > maxSafeJSON {
		return errors.New("invalid capture limits")
	}
	return nil
}

func canonicalAbsolute(path string) bool {
	return path != "" && strings.IndexByte(path, 0) < 0 && filepath.IsAbs(path) && filepath.Clean(path) == path && path != string(filepath.Separator)
}

func pathContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func validName(name string) bool {
	return name != "" && name != "." && name != ".." &&
		!strings.ContainsRune(name, filepath.Separator) && strings.IndexByte(name, 0) < 0
}

func openCanonicalParent(path string) (*os.File, string, error) {
	return openCanonicalDirectory(filepath.Dir(path), filepath.Base(path))
}

// openOwnedCanonicalParent accepts only one platform-owned path alias: the
// fixed first component /var on macOS. The alias itself must still be the
// root-owned system symlink to private/var. Every caller-controlled suffix is
// then walked from / with openat(O_NOFOLLOW), exactly like an ordinary
// canonical path; arbitrary or nested symlink ancestors remain forbidden.
func openOwnedCanonicalParent(path string) (*os.File, string, error) {
	filesystemPath, err := ownedFilesystemPath(path)
	if err != nil {
		return nil, "", err
	}
	return openCanonicalDirectory(filepath.Dir(filesystemPath), filepath.Base(filesystemPath))
}

func ownedFilesystemPath(path string) (string, error) {
	if !canonicalAbsolute(path) {
		return "", errors.New("owned path is not canonical absolute")
	}
	if runtime.GOOS != "darwin" || (path != "/var" && !strings.HasPrefix(path, "/var/")) {
		return path, nil
	}
	info, err := os.Lstat("/var")
	if err != nil {
		return "", errors.New("macOS /var system alias is unavailable")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode()&os.ModeSymlink == 0 || stat.Uid != 0 || stat.Gid != 0 || stat.Nlink != 1 {
		return "", errors.New("macOS /var system alias identity drifted")
	}
	target, err := os.Readlink("/var")
	if err != nil || target != "private/var" {
		return "", errors.New("macOS /var system alias target drifted")
	}
	filesystemPath := "/private" + path
	if !canonicalAbsolute(filesystemPath) || filepath.Base(filesystemPath) != filepath.Base(path) {
		return "", errors.New("macOS /var system alias mapping drifted")
	}
	return filesystemPath, nil
}

func openCanonicalDirectory(path, finalName string) (*os.File, string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, "", errors.New("directory path is not canonical absolute")
	}
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, "", err
	}
	current := os.NewFile(uintptr(fd), string(filepath.Separator))
	if current == nil {
		_ = unix.Close(fd)
		return nil, "", errors.New("construct root handle")
	}
	components := strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator))
	for _, component := range components {
		if component == "" {
			continue
		}
		before, statErr := statAt(int(current.Fd()), component)
		if statErr != nil || nodeType(before.mode) != unix.S_IFDIR {
			_ = current.Close()
			return nil, "", errors.New("path ancestor is absent, a symlink, or not a directory")
		}
		nextFD, openErr := openDirectoryAt(int(current.Fd()), component, before)
		if openErr != nil {
			_ = current.Close()
			return nil, "", openErr
		}
		next := os.NewFile(uintptr(nextFD), component)
		if next == nil {
			_ = unix.Close(nextFD)
			_ = current.Close()
			return nil, "", errors.New("construct ancestor handle")
		}
		_ = current.Close()
		current = next
	}
	return current, finalName, nil
}

func openDirectoryAt(parentFD int, name string, before nodeStat) (int, error) {
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, err
	}
	var opened unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	if !sameObject(before, nodeFromUnix(opened)) {
		_ = unix.Close(fd)
		return -1, errors.New("directory changed during open")
	}
	return fd, nil
}

func statAt(parentFD int, name string) (nodeStat, error) {
	var stat unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nodeStat{}, err
	}
	return nodeFromUnix(stat), nil
}

func nodeFromUnix(stat unix.Stat_t) nodeStat {
	ctimeSec, ctimeNsec, hasCTime := statTimestamp(stat, "Ctim", "Ctimespec")
	return nodeStat{mode: uint32(stat.Mode), uid: stat.Uid, gid: stat.Gid, dev: uint64(stat.Dev), ino: uint64(stat.Ino), nlink: uint64(stat.Nlink), size: stat.Size,
		ctimeSec: ctimeSec, ctimeNsec: ctimeNsec, hasCTime: hasCTime}
}

func nodeFromFileInfo(info os.FileInfo) (nodeStat, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nodeStat{}, errors.New("filesystem identity unavailable")
	}
	ctimeSec, ctimeNsec, hasCTime := statTimestamp(stat, "Ctim", "Ctimespec")
	return nodeStat{mode: uint32(stat.Mode), uid: stat.Uid, gid: stat.Gid, dev: uint64(stat.Dev), ino: uint64(stat.Ino), nlink: uint64(stat.Nlink), size: stat.Size,
		ctimeSec: ctimeSec, ctimeNsec: ctimeNsec, hasCTime: hasCTime}, nil
}

func statTimestamp(stat any, names ...string) (int64, int64, bool) {
	value := reflect.ValueOf(stat)
	if value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return 0, 0, false
	}
	for _, name := range names {
		field := value.FieldByName(name)
		if !field.IsValid() || field.Kind() != reflect.Struct {
			continue
		}
		seconds := field.FieldByName("Sec")
		nanoseconds := field.FieldByName("Nsec")
		if seconds.IsValid() && nanoseconds.IsValid() && seconds.CanInt() && nanoseconds.CanInt() {
			return seconds.Int(), nanoseconds.Int(), true
		}
	}
	return 0, 0, false
}

func nodeType(mode uint32) uint32 { return mode & unix.S_IFMT }

func sameNode(left, right nodeStat) bool {
	return left.mode == right.mode && left.uid == right.uid && left.gid == right.gid && left.dev == right.dev &&
		left.ino == right.ino && left.nlink == right.nlink && left.size == right.size &&
		left.hasCTime == right.hasCTime && (!left.hasCTime || (left.ctimeSec == right.ctimeSec && left.ctimeNsec == right.ctimeNsec))
}

func sameObject(left, right nodeStat) bool {
	return left.dev == right.dev && left.ino == right.ino && left.uid == right.uid && nodeType(left.mode) == nodeType(right.mode)
}

func validateDirectory(stat nodeStat, created bool) error {
	if nodeType(stat.mode) != unix.S_IFDIR {
		return errors.New("node is not a directory")
	}
	if stat.uid != uint32(os.Geteuid()) {
		return errors.New("directory has foreign ownership")
	}
	if stat.mode&0o022 != 0 || stat.mode&uint32(unix.S_ISUID|unix.S_ISGID) != 0 {
		return errors.New("directory is group/world writable or has unsafe special mode bits")
	}
	if created && stat.mode&0o777 != 0o700 {
		return errors.New("created directory is not mode 0700")
	}
	return nil
}

func validateCreatedDirectory(stat nodeStat) error { return validateDirectory(stat, true) }

func validateRegular(stat nodeStat) error {
	if nodeType(stat.mode) != unix.S_IFREG {
		return errors.New("node is not a regular file")
	}
	if stat.uid != uint32(os.Geteuid()) {
		return errors.New("file has foreign ownership")
	}
	if stat.nlink != 1 {
		return errors.New("hard-linked regular file is forbidden")
	}
	if stat.size < 0 {
		return errors.New("regular file has invalid size")
	}
	if stat.mode&0o022 != 0 || stat.mode&uint32(unix.S_ISUID|unix.S_ISGID|unix.S_ISVTX) != 0 {
		return errors.New("file is group/world writable or has unsafe special mode bits")
	}
	return nil
}

func validateControlledParent(directory *os.File) error {
	info, err := directory.Stat()
	if err != nil {
		return err
	}
	stat, err := nodeFromFileInfo(info)
	if err != nil {
		return err
	}
	return validateDirectory(stat, false)
}

func requireAbsentAt(parentFD int, name string) error {
	_, err := statAt(parentFD, name)
	if err == nil {
		return errors.New("path already exists")
	}
	if !errors.Is(err, unix.ENOENT) {
		return err
	}
	return nil
}

func verifyEntryStable(parentFD int, name string, before nodeStat) error {
	after, err := statAt(parentFD, name)
	if err != nil || !sameNode(before, after) {
		return errors.New("parent entry identity drifted")
	}
	return nil
}

func verifyObjectStable(parentFD int, name string, before nodeStat) error {
	after, err := statAt(parentFD, name)
	if err != nil || !sameObject(before, after) {
		return errors.New("parent entry object identity drifted")
	}
	return nil
}

func verifyFileInfoStable(before os.FileInfo, file *os.File) error {
	after, err := file.Stat()
	if err != nil {
		return err
	}
	beforeStat, err := nodeFromFileInfo(before)
	if err != nil {
		return err
	}
	afterStat, err := nodeFromFileInfo(after)
	if err != nil {
		return err
	}
	if !sameNode(beforeStat, afterStat) || !before.ModTime().Equal(after.ModTime()) {
		return errors.New("open node identity or modification time drifted")
	}
	return nil
}

func receiptIdentity(stat nodeStat) Identity {
	return Identity{
		Mode: int(stat.mode & 0o7777), UID: int(stat.uid), GID: int(stat.gid),
		Dev: strconv.FormatUint(stat.dev, 10), Ino: strconv.FormatUint(stat.ino, 10),
	}
}

func writeReceipt(path string, receipt Receipt, testHooks *hooks) (resultErr error) {
	parent, base, err := openCanonicalParent(path)
	if err != nil {
		return fmt.Errorf("open receipt parent: %w", err)
	}
	defer parent.Close()
	if err := validateControlledParent(parent); err != nil {
		return fmt.Errorf("receipt parent: %w", err)
	}
	if err := requireAbsentAt(int(parent.Fd()), base); err != nil {
		return fmt.Errorf("receipt: %w", err)
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	fd, err := unix.Openat(int(parent.Fd()), base,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o400)
	if err != nil {
		return fmt.Errorf("create receipt: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return errors.New("construct receipt handle")
	}
	createdInfo, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return errors.Join(err, errors.New("cannot safely identify receipt for cleanup"))
	}
	created, err := nodeFromFileInfo(createdInfo)
	if err != nil {
		_ = file.Close()
		return errors.Join(err, errors.New("cannot safely identify receipt for cleanup"))
	}
	remove := true
	defer func() {
		closeErr := file.Close()
		if closeErr != nil {
			resultErr = errors.Join(resultErr, closeErr)
		}
		if remove {
			resultErr = errors.Join(resultErr, removeBoundEntry(parent, base, created, 0, testHooks, "receipt"))
		}
	}()
	if validateRegular(created) != nil {
		return errors.New("created receipt is unsafe")
	}
	if err := unix.Fchmod(int(file.Fd()), 0o400); err != nil {
		return err
	}
	createdInfo, err = file.Stat()
	if err != nil {
		return err
	}
	created, err = nodeFromFileInfo(createdInfo)
	if err != nil || created.mode&0o777 != 0o400 {
		return errors.New("created receipt mode drifted")
	}
	if testHooks != nil && testHooks.afterReceiptCreated != nil {
		testHooks.afterReceiptCreated()
	}
	if written, err := file.Write(encoded); err != nil || written != len(encoded) {
		if err == nil {
			err = io.ErrShortWrite
		}
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	finalInfo, err := file.Stat()
	if err != nil {
		return errors.New("receipt mode, type, or size drifted")
	}
	finalStat, statErr := nodeFromFileInfo(finalInfo)
	if statErr != nil || validateRegular(finalStat) != nil || finalInfo.Mode().Perm() != 0o400 || finalInfo.Size() != int64(len(encoded)) {
		return errors.New("receipt mode, type, or size drifted")
	}
	if err := verifyObjectStable(int(parent.Fd()), base, created); err != nil {
		return errors.New("receipt path was replaced")
	}
	if err := parent.Sync(); err != nil {
		return err
	}
	remove = false
	return nil
}

func cleanupCapture(root, parent *os.File, base string, created nodeStat, testHooks *hooks) error {
	var cleanupErr error
	if err := removeContents(root, testHooks, ""); err != nil {
		cleanupErr = errors.Join(cleanupErr, err)
		if syncErr := root.Sync(); syncErr != nil {
			cleanupErr = errors.Join(cleanupErr, syncErr)
		}
		return cleanupErr
	}
	cleanupErr = errors.Join(cleanupErr, removeBoundEntry(parent, base, created, unix.AT_REMOVEDIR, testHooks, "."))
	if err := parent.Sync(); err != nil {
		cleanupErr = errors.Join(cleanupErr, err)
	}
	return cleanupErr
}

func removeEmptyCreatedRoot(parent *os.File, base string, expected nodeStat) error {
	if expected.ino == 0 {
		return errors.New("refused to remove unidentified capture-root path")
	}
	return removeBoundEntry(parent, base, expected, unix.AT_REMOVEDIR, nil, ".")
}

func removeContents(directory *os.File, testHooks *hooks, relative string) error {
	dupFD, err := unix.Dup(int(directory.Fd()))
	if err != nil {
		return err
	}
	reader := os.NewFile(uintptr(dupFD), "cleanup")
	if reader == nil {
		_ = unix.Close(dupFD)
		return errors.New("construct cleanup directory handle")
	}
	entries, readErr := reader.ReadDir(-1)
	_ = reader.Close()
	if readErr != nil {
		return readErr
	}
	var result error
	for _, entry := range entries {
		name := entry.Name()
		childRelative := name
		if relative != "" {
			childRelative = relative + "/" + name
		}
		stat, statErr := statAt(int(directory.Fd()), name)
		if statErr != nil {
			result = errors.Join(result, statErr)
			continue
		}
		if stat.uid != uint32(os.Geteuid()) {
			result = errors.Join(result, errors.New("refused to remove foreign-owned capture entry"))
			continue
		}
		if nodeType(stat.mode) == unix.S_IFDIR {
			fd, openErr := openDirectoryAt(int(directory.Fd()), name, stat)
			if openErr != nil {
				result = errors.Join(result, openErr)
				continue
			}
			child := os.NewFile(uintptr(fd), name)
			if child == nil {
				_ = unix.Close(fd)
				result = errors.Join(result, errors.New("construct cleanup child handle"))
				continue
			}
			if chmodErr := unix.Fchmod(fd, 0o700); chmodErr != nil {
				result = errors.Join(result, chmodErr)
			}
			childErr := removeContents(child, testHooks, childRelative)
			if childErr != nil {
				result = errors.Join(result, childErr)
				_ = child.Close()
				continue
			}
			result = errors.Join(result, removeBoundEntry(directory, name, stat, unix.AT_REMOVEDIR, testHooks, childRelative))
			_ = child.Close()
			continue
		}
		result = errors.Join(result, removeBoundEntry(directory, name, stat, 0, testHooks, childRelative))
	}
	return result
}

// removeBoundEntry closes the fstatat-to-unlink race by atomically moving the
// current name into a fresh private quarantine before deciding whether it is
// the object we own. A mismatched object is retained in quarantine and turns
// cleanup into a reported failure; it is never unlinked.
func removeBoundEntry(parent *os.File, name string, expected nodeStat, flags int, testHooks *hooks, relative string) error {
	current, err := statAt(int(parent.Fd()), name)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return err
	}
	if !sameObject(expected, current) {
		return errors.New("refused to remove replaced cleanup path")
	}
	if testHooks != nil && testHooks.beforeCleanupQuarantine != nil {
		testHooks.beforeCleanupQuarantine(relative)
	}
	quarantine, quarantineName, quarantineIdentity, err := createCleanupQuarantine(parent)
	if err != nil {
		return err
	}
	closeQuarantine := true
	defer func() {
		if closeQuarantine {
			_ = quarantine.Close()
		}
	}()
	if err := unix.Renameat(int(parent.Fd()), name, int(quarantine.Fd()), "entry"); err != nil {
		_ = quarantine.Close()
		closeQuarantine = false
		return errors.Join(err, removeCleanupQuarantine(parent, quarantineName, quarantineIdentity))
	}
	moved, err := statAt(int(quarantine.Fd()), "entry")
	if err != nil || !sameObject(expected, moved) {
		return errors.Join(errors.New("preserved quarantined replacement with mismatched identity"), err)
	}
	if err := unix.Unlinkat(int(quarantine.Fd()), "entry", flags); err != nil {
		return err
	}
	if err := quarantine.Sync(); err != nil {
		return err
	}
	if err := quarantine.Close(); err != nil {
		closeQuarantine = false
		return err
	}
	closeQuarantine = false
	return removeCleanupQuarantine(parent, quarantineName, quarantineIdentity)
}

func removeCleanupQuarantine(parent *os.File, name string, expected nodeStat) error {
	current, err := statAt(int(parent.Fd()), name)
	if err != nil {
		return err
	}
	if !sameObject(expected, current) {
		return errors.New("refused to remove replaced cleanup quarantine")
	}
	return unix.Unlinkat(int(parent.Fd()), name, unix.AT_REMOVEDIR)
}

func createCleanupQuarantine(parent *os.File) (*os.File, string, nodeStat, error) {
	for attempt := 0; attempt < 8; attempt++ {
		nonce := make([]byte, 16)
		if _, err := rand.Read(nonce); err != nil {
			return nil, "", nodeStat{}, err
		}
		name := ".chora-cleanup-" + hex.EncodeToString(nonce)
		if err := unix.Mkdirat(int(parent.Fd()), name, 0o700); err != nil {
			if errors.Is(err, unix.EEXIST) {
				continue
			}
			return nil, "", nodeStat{}, err
		}
		identity, err := statAt(int(parent.Fd()), name)
		if err != nil || validateCreatedDirectory(identity) != nil {
			return nil, "", nodeStat{}, errors.Join(errors.New("created cleanup quarantine is unsafe"), err)
		}
		fd, err := openDirectoryAt(int(parent.Fd()), name, identity)
		if err != nil {
			return nil, "", nodeStat{}, err
		}
		quarantine := os.NewFile(uintptr(fd), name)
		if quarantine == nil {
			_ = unix.Close(fd)
			return nil, "", nodeStat{}, errors.New("construct cleanup quarantine handle")
		}
		return quarantine, name, identity, nil
	}
	return nil, "", nodeStat{}, errors.New("allocate cleanup quarantine")
}

// WriteReadonlyRegularNoReplace durably writes value to a fresh regular file
// and publishes it at target with no-replace semantics. Creation, identity
// checks, cleanup, and publication are all relative to one pinned parent
// directory descriptor. The canonical parent path is re-opened before and
// after publication, so replacing an ancestor cannot redirect a successful
// result. Any exact partial entry is removed fd-relatively on failure; a
// replacement inode is never unlinked.
func WriteReadonlyRegularNoReplace(target string, value []byte) (OwnedPathIdentity, error) {
	return writeReadonlyRegularNoReplaceWithHooks(target, value, nil)
}

func writeReadonlyRegularNoReplaceWithHooks(target string, value []byte, testHooks *hooks) (
	result OwnedPathIdentity, resultErr error,
) {
	if !canonicalAbsolute(target) || len(value) == 0 {
		return OwnedPathIdentity{}, errors.New("readonly output target/value is invalid")
	}
	parent, targetName, err := openOwnedCanonicalParent(target)
	if err != nil {
		return OwnedPathIdentity{}, fmt.Errorf("open readonly output parent: %w", err)
	}
	defer func() {
		resultErr = errors.Join(resultErr, parent.Close())
		if resultErr != nil {
			result = OwnedPathIdentity{}
		}
	}()
	if err := validateOwnedPathParent(parent, filepath.Dir(target)); err != nil {
		return OwnedPathIdentity{}, fmt.Errorf("readonly output parent: %w", err)
	}
	parentInfo, err := parent.Stat()
	if err != nil {
		return OwnedPathIdentity{}, err
	}
	parentIdentity, err := nodeFromFileInfo(parentInfo)
	if err != nil {
		return OwnedPathIdentity{}, err
	}
	if err := requireAbsentAt(int(parent.Fd()), targetName); err != nil {
		return OwnedPathIdentity{}, fmt.Errorf("readonly output target: %w", err)
	}

	stagingName, handle, initial, err := createReadonlyStaging(parent)
	if err != nil {
		return OwnedPathIdentity{}, err
	}
	ready := initial
	publishAttempted := false
	succeeded := false
	defer func() {
		if handle != nil {
			if info, statErr := handle.Stat(); statErr == nil {
				if observed, identityErr := nodeFromFileInfo(info); identityErr == nil && sameObject(initial, observed) {
					ready = observed
				}
			}
			resultErr = errors.Join(resultErr, handle.Close())
		}
		if succeeded {
			return
		}
		if publishAttempted {
			resultErr = errors.Join(resultErr,
				removeExactReadonlyEntry(parent, targetName, ready, "readonly output target"))
		}
		resultErr = errors.Join(resultErr,
			removeExactReadonlyEntry(parent, stagingName, ready, "readonly output staging"))
	}()

	for remaining := value; len(remaining) > 0; {
		written, writeErr := handle.Write(remaining)
		if writeErr != nil {
			return OwnedPathIdentity{}, writeErr
		}
		if written <= 0 {
			return OwnedPathIdentity{}, io.ErrShortWrite
		}
		remaining = remaining[written:]
	}
	if err := handle.Sync(); err != nil {
		return OwnedPathIdentity{}, err
	}
	if err := handle.Chmod(0o400); err != nil {
		return OwnedPathIdentity{}, err
	}
	if err := handle.Sync(); err != nil {
		return OwnedPathIdentity{}, err
	}
	readyInfo, err := handle.Stat()
	if err != nil {
		return OwnedPathIdentity{}, err
	}
	ready, err = nodeFromFileInfo(readyInfo)
	if err != nil || validateRegular(ready) != nil || ready.mode&0o777 != 0o400 || ready.size != int64(len(value)) ||
		!sameObject(initial, ready) {
		return OwnedPathIdentity{}, errors.Join(errors.New("readonly staging identity drifted"), err)
	}
	if err := handle.Close(); err != nil {
		handle = nil
		return OwnedPathIdentity{}, err
	}
	handle = nil

	if testHooks != nil && testHooks.beforeReadonlyPublish != nil {
		testHooks.beforeReadonlyPublish(int(parent.Fd()), stagingName, targetName)
	}
	if err := requireCanonicalParentIdentity(target, parentIdentity); err != nil {
		return OwnedPathIdentity{}, err
	}
	current, err := statAt(int(parent.Fd()), stagingName)
	if err != nil || !sameOwnedEntry(ready, current) || validateRegular(current) != nil {
		if err == nil && current.ino != 0 && !sameOwnedEntry(ready, current) {
			return OwnedPathIdentity{}, errors.Join(errors.New("readonly staging was replaced before publish"),
				quarantinePublishedReplacement(parent, stagingName, current))
		}
		return OwnedPathIdentity{}, errors.Join(errors.New("readonly staging changed before publish"), err)
	}
	publishAttempted = true
	if err := renameNoReplace(int(parent.Fd()), stagingName, int(parent.Fd()), targetName); err != nil {
		return OwnedPathIdentity{}, fmt.Errorf("readonly output no-replace rename: %w", err)
	}
	if testHooks != nil && testHooks.afterReadonlyPublishMove != nil {
		testHooks.afterReadonlyPublishMove(int(parent.Fd()), targetName)
	}
	moved, err := statAt(int(parent.Fd()), targetName)
	if err != nil || !sameOwnedEntry(ready, moved) || validateRegular(moved) != nil {
		if err == nil && moved.ino != 0 && !sameOwnedEntry(ready, moved) {
			return OwnedPathIdentity{}, errors.Join(errors.New("readonly published entry was replaced"),
				quarantinePublishedReplacement(parent, targetName, moved))
		}
		return OwnedPathIdentity{}, errors.Join(errors.New("readonly published entry changed"), err)
	}
	if _, err := statAt(int(parent.Fd()), stagingName); !errors.Is(err, unix.ENOENT) {
		if err == nil {
			err = errors.New("readonly staging path was recreated")
		}
		return OwnedPathIdentity{}, err
	}
	if err := parent.Sync(); err != nil {
		return OwnedPathIdentity{}, err
	}
	if err := requireCanonicalParentIdentity(target, parentIdentity); err != nil {
		return OwnedPathIdentity{}, err
	}
	final, err := statAt(int(parent.Fd()), targetName)
	if err != nil || !sameNode(moved, final) {
		return OwnedPathIdentity{}, errors.Join(errors.New("readonly output changed after publication"), err)
	}
	if testHooks != nil && testHooks.beforeReadonlyFinalVerify != nil {
		testHooks.beforeReadonlyFinalVerify(int(parent.Fd()), targetName)
	}
	verified, err := verifyReadonlyPublishedValue(parent, targetName, final, value)
	if err != nil {
		current, statErr := statAt(int(parent.Fd()), targetName)
		if statErr == nil && current.ino != 0 && !sameOwnedEntry(ready, current) {
			return OwnedPathIdentity{}, errors.Join(err,
				quarantinePublishedReplacement(parent, targetName, current))
		}
		return OwnedPathIdentity{}, err
	}
	if err := requireCanonicalParentIdentity(target, parentIdentity); err != nil {
		return OwnedPathIdentity{}, err
	}
	final, err = statAt(int(parent.Fd()), targetName)
	if err != nil || !sameNode(verified, final) {
		return OwnedPathIdentity{}, errors.Join(errors.New("readonly output changed after final verification"), err)
	}
	succeeded = true
	return ownedPathIdentity(final), nil
}

func verifyReadonlyPublishedValue(parent *os.File, targetName string, expected nodeStat, value []byte) (
	result nodeStat, resultErr error,
) {
	before, err := statAt(int(parent.Fd()), targetName)
	if err != nil || !sameNode(expected, before) || validateRegular(before) != nil ||
		before.mode&0o777 != 0o400 || before.size != int64(len(value)) {
		return nodeStat{}, errors.Join(errors.New("readonly output path changed before final verification"), err)
	}
	fd, err := unix.Openat(int(parent.Fd()), targetName,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_NOCTTY, 0)
	if err != nil {
		return nodeStat{}, err
	}
	handle := os.NewFile(uintptr(fd), targetName)
	if handle == nil {
		_ = unix.Close(fd)
		return nodeStat{}, errors.New("construct readonly final-verification handle")
	}
	defer func() { resultErr = errors.Join(resultErr, handle.Close()) }()
	openedInfo, err := handle.Stat()
	if err != nil {
		return nodeStat{}, err
	}
	opened, err := nodeFromFileInfo(openedInfo)
	if err != nil || !sameNode(before, opened) {
		return nodeStat{}, errors.Join(errors.New("readonly output changed during final open"), err)
	}
	buffer := make([]byte, 64<<10)
	for offset := 0; offset < len(value); {
		length := len(buffer)
		if remaining := len(value) - offset; remaining < length {
			length = remaining
		}
		count, readErr := io.ReadFull(handle, buffer[:length])
		if readErr != nil || count != length || !bytes.Equal(buffer[:count], value[offset:offset+count]) {
			return nodeStat{}, errors.Join(errors.New("readonly output value drifted during final verification"), readErr)
		}
		offset += count
	}
	var extra [1]byte
	count, readErr := handle.Read(extra[:])
	if count != 0 || (readErr != nil && !errors.Is(readErr, io.EOF)) {
		return nodeStat{}, errors.New("readonly output grew during final verification")
	}
	afterInfo, err := handle.Stat()
	if err != nil {
		return nodeStat{}, err
	}
	after, err := nodeFromFileInfo(afterInfo)
	if err != nil || !sameNode(opened, after) {
		return nodeStat{}, errors.Join(errors.New("readonly output changed during final verification"), err)
	}
	entryAfter, err := statAt(int(parent.Fd()), targetName)
	if err != nil || !sameNode(before, entryAfter) {
		return nodeStat{}, errors.Join(errors.New("readonly output path changed during final verification"), err)
	}
	return entryAfter, nil
}

func createReadonlyStaging(parent *os.File) (string, *os.File, nodeStat, error) {
	for attempt := 0; attempt < 8; attempt++ {
		nonce := make([]byte, 16)
		if _, err := rand.Read(nonce); err != nil {
			return "", nil, nodeStat{}, err
		}
		name := ".chora-readonly-" + hex.EncodeToString(nonce)
		fd, err := unix.Openat(int(parent.Fd()), name,
			unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return "", nil, nodeStat{}, err
		}
		var opened unix.Stat_t
		if err := unix.Fstat(fd, &opened); err != nil {
			_ = unix.Close(fd)
			observed, statErr := statAt(int(parent.Fd()), name)
			if statErr == nil {
				statErr = errors.Join(errors.New("unidentified readonly staging retained in quarantine"),
					quarantinePublishedReplacement(parent, name, observed))
			}
			return "", nil, nodeStat{}, errors.Join(errors.New("created readonly staging without recoverable identity"), err, statErr)
		}
		identity := nodeFromUnix(opened)
		if err := validateRegular(identity); err != nil || identity.mode&0o777 != 0o600 || identity.size != 0 {
			_ = unix.Close(fd)
			cleanupErr := removeExactReadonlyEntry(parent, name, identity, "unsafe readonly staging")
			return "", nil, nodeStat{}, errors.Join(errors.New("created readonly staging is unsafe"), err, cleanupErr)
		}
		handle := os.NewFile(uintptr(fd), name)
		if handle == nil {
			_ = unix.Close(fd)
			return "", nil, nodeStat{}, errors.Join(errors.New("construct readonly staging handle"),
				removeExactReadonlyEntry(parent, name, identity, "unopened readonly staging"))
		}
		return name, handle, identity, nil
	}
	return "", nil, nodeStat{}, errors.New("allocate readonly staging")
}

func requireCanonicalParentIdentity(target string, expected nodeStat) error {
	current, _, err := openOwnedCanonicalParent(target)
	if err != nil {
		return fmt.Errorf("re-open readonly output parent: %w", err)
	}
	defer current.Close()
	if err := validateOwnedPathParent(current, filepath.Dir(target)); err != nil {
		return err
	}
	info, err := current.Stat()
	if err != nil {
		return err
	}
	identity, err := nodeFromFileInfo(info)
	if err != nil || !sameObject(expected, identity) {
		return errors.Join(errors.New("readonly output parent identity drifted"), err)
	}
	return nil
}

func removeExactReadonlyEntry(parent *os.File, name string, expected nodeStat, label string) error {
	current, err := statAt(int(parent.Fd()), name)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	if !sameOwnedEntry(expected, current) {
		return fmt.Errorf("%s replacement retained", label)
	}
	return removeOwnedBoundEntry(parent, name, current, RegularFile, nil, label)
}

func ownedPathIdentity(stat nodeStat) OwnedPathIdentity {
	return OwnedPathIdentity{
		Mode: int(stat.mode), UID: int(stat.uid), GID: int(stat.gid),
		Dev: strconv.FormatUint(stat.dev, 10), Ino: strconv.FormatUint(stat.ino, 10),
	}
}

// CreateOwnedDirectoryNoReplace creates one mode-0700 directory beneath an
// exactly identified owner-controlled parent. The parent is held by descriptor
// across mkdirat and validation, so a pathname swap cannot redirect creation.
// If creation succeeds but the new inode cannot be identity-bound, it is
// deliberately retained rather than risking removal of an unknown object.
func CreateOwnedDirectoryNoReplace(path string, expectedParent OwnedPathIdentity) (OwnedPathIdentity, error) {
	return createOwnedDirectoryNoReplaceWithHooks(path, expectedParent, nil)
}

func createOwnedDirectoryNoReplaceWithHooks(path string, expectedParent OwnedPathIdentity, testHooks *hooks) (OwnedPathIdentity, error) {
	var zero OwnedPathIdentity
	if !canonicalAbsolute(path) {
		return zero, errors.New("owned directory path must be canonical absolute and non-root")
	}
	parentPath := filepath.Dir(path)
	expectedParentNode, err := validateOwnedPathRequest(parentPath, EmptyDirectory, expectedParent)
	if err != nil {
		return zero, fmt.Errorf("owned directory parent identity: %w", err)
	}
	parent, base, err := openOwnedCanonicalParent(path)
	if err != nil {
		return zero, fmt.Errorf("open owned directory parent: %w", err)
	}
	defer parent.Close()
	if err := validateOwnedPathParent(parent, parentPath); err != nil {
		return zero, fmt.Errorf("owned directory parent: %w", err)
	}
	parentInfo, err := parent.Stat()
	if err != nil {
		return zero, err
	}
	parentNode, err := nodeFromFileInfo(parentInfo)
	if err != nil || !matchesOwnedIdentity(expectedParentNode, parentNode) {
		return zero, errors.Join(errors.New("owned directory parent identity drifted"), err)
	}
	if _, err := statAt(int(parent.Fd()), base); !errors.Is(err, unix.ENOENT) {
		if err == nil {
			return zero, errors.New("owned directory target already exists")
		}
		return zero, err
	}
	if testHooks != nil && testHooks.beforeOwnedDirectoryCreate != nil {
		testHooks.beforeOwnedDirectoryCreate()
	}
	if err := unix.Mkdirat(int(parent.Fd()), base, 0o700); err != nil {
		return zero, err
	}
	if testHooks != nil && testHooks.afterOwnedDirectoryCreate != nil {
		testHooks.afterOwnedDirectoryCreate()
	}

	// Do not attempt cleanup until the exact created inode has been bound.
	created, err := statAt(int(parent.Fd()), base)
	if err != nil {
		return zero, errors.Join(errors.New("created owned directory retained without identity"), err)
	}
	if err := validateDirectory(created, true); err != nil {
		return zero, errors.Join(errors.New("created owned directory retained after unsafe identity"), err)
	}
	createdIdentity := ownedPathIdentity(created)
	cleanupAfter := func(operationErr error) (OwnedPathIdentity, error) {
		current, statErr := statAt(int(parent.Fd()), base)
		if statErr != nil || !sameOwnedEntry(created, current) {
			return zero, errors.Join(operationErr,
				errors.New("created owned directory replacement retained"), statErr)
		}
		removeErr := removeOwnedBoundEntry(parent, base, current, EmptyDirectory, nil, ".")
		return zero, errors.Join(operationErr, removeErr, parent.Sync())
	}
	if err := parent.Sync(); err != nil {
		return cleanupAfter(err)
	}

	// Reopen through the canonical pathname after creation. This proves the
	// manifest path still names the same parent and exact child identity.
	reopened, reopenedBase, err := openOwnedCanonicalParent(path)
	if err != nil {
		return cleanupAfter(err)
	}
	defer reopened.Close()
	reopenedInfo, statErr := reopened.Stat()
	reopenedParent, identityErr := nodeFromFileInfo(reopenedInfo)
	observed, childErr := statAt(int(reopened.Fd()), reopenedBase)
	if statErr != nil || identityErr != nil ||
		!matchesOwnedIdentity(expectedParentNode, reopenedParent) || childErr != nil ||
		!sameOwnedEntry(created, observed) {
		return cleanupAfter(errors.Join(errors.New("owned directory path binding drifted"),
			statErr, identityErr, childErr))
	}
	return createdIdentity, nil
}

// RemoveOwnedPath removes exactly one caller-identified, owner-controlled path.
// An absent final path is an idempotent success. Existing paths are always
// moved into a fresh private quarantine before deletion, closing the check to
// unlink race without ever deleting a replacement object.
func RemoveOwnedPath(path string, disposition OwnedPathDisposition, expected OwnedPathIdentity) error {
	return removeOwnedPathWithHooks(path, disposition, expected, nil)
}

func removeOwnedPathWithHooks(path string, disposition OwnedPathDisposition, expected OwnedPathIdentity, testHooks *hooks) error {
	expectedNode, err := validateOwnedPathRequest(path, disposition, expected)
	if err != nil {
		return err
	}
	parent, base, err := openOwnedCanonicalParent(path)
	if err != nil {
		return fmt.Errorf("open owned-path parent: %w", err)
	}
	defer parent.Close()
	if err := validateOwnedPathParent(parent, filepath.Dir(path)); err != nil {
		return fmt.Errorf("owned-path parent: %w", err)
	}
	current, err := statAt(int(parent.Fd()), base)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	if !matchesOwnedIdentity(expectedNode, current) {
		return errors.New("owned-path identity drifted")
	}
	if err := validateOwnedNode(current, disposition); err != nil {
		return err
	}

	if disposition == EmptyDirectory || disposition == RecursiveDirectory {
		fd, openErr := openDirectoryAt(int(parent.Fd()), base, current)
		if openErr != nil {
			return openErr
		}
		directory := os.NewFile(uintptr(fd), base)
		if directory == nil {
			_ = unix.Close(fd)
			return errors.New("construct owned directory handle")
		}
		if disposition == EmptyDirectory {
			err = requireEmptyDirectory(directory)
		} else {
			err = removeOwnedContents(directory, testHooks, "")
		}
		closeErr := directory.Close()
		if err != nil || closeErr != nil {
			return errors.Join(err, closeErr)
		}
		// Recursive removal changes directory ctime and link count. Rebind the
		// final removal to its new full identity while retaining the request's
		// exact stable identity fields.
		current, err = statAt(int(parent.Fd()), base)
		if err != nil || !matchesOwnedIdentity(expectedNode, current) {
			return errors.Join(errors.New("owned directory changed during cleanup"), err)
		}
	}
	if err := removeOwnedBoundEntry(parent, base, current, disposition, testHooks, "."); err != nil {
		return err
	}
	return parent.Sync()
}

// PublishOwnedPath atomically moves an exactly identified source to an absent
// sibling target. The platform primitive is required to provide no-replace
// semantics. If the source was swapped after validation and the foreign object
// reaches the target, it is atomically moved into private quarantine and kept;
// the requested target is restored to absence and the operation fails.
func PublishOwnedPath(sourcePath, targetPath string, disposition OwnedPathDisposition, expected OwnedPathIdentity) error {
	return publishOwnedPathWithHooks(sourcePath, targetPath, disposition, expected, nil)
}

func publishOwnedPathWithHooks(sourcePath, targetPath string, disposition OwnedPathDisposition, expected OwnedPathIdentity, testHooks *hooks) error {
	expectedNode, err := validateOwnedPathRequest(sourcePath, disposition, expected)
	if err != nil {
		return err
	}
	if !canonicalAbsolute(targetPath) || filepath.Dir(sourcePath) != filepath.Dir(targetPath) || sourcePath == targetPath {
		return errors.New("publish paths must be distinct canonical siblings")
	}
	parent, sourceBase, err := openOwnedCanonicalParent(sourcePath)
	if err != nil {
		return fmt.Errorf("open publish parent: %w", err)
	}
	defer parent.Close()
	if err := validateOwnedPathParent(parent, filepath.Dir(sourcePath)); err != nil {
		return fmt.Errorf("publish parent: %w", err)
	}
	targetBase := filepath.Base(targetPath)
	if err := requireAbsentAt(int(parent.Fd()), targetBase); err != nil {
		return fmt.Errorf("publish target: %w", err)
	}
	before, err := statAt(int(parent.Fd()), sourceBase)
	if err != nil {
		return fmt.Errorf("publish source: %w", err)
	}
	if !matchesOwnedIdentity(expectedNode, before) {
		return errors.New("publish source identity drifted")
	}
	if err := validateOwnedNode(before, disposition); err != nil {
		return err
	}
	if disposition == EmptyDirectory {
		fd, openErr := openDirectoryAt(int(parent.Fd()), sourceBase, before)
		if openErr != nil {
			return openErr
		}
		directory := os.NewFile(uintptr(fd), sourceBase)
		if directory == nil {
			_ = unix.Close(fd)
			return errors.New("construct publish directory handle")
		}
		err = requireEmptyDirectory(directory)
		closeErr := directory.Close()
		if err != nil || closeErr != nil {
			return errors.Join(err, closeErr)
		}
	}
	if testHooks != nil && testHooks.beforeOwnedPathPublish != nil {
		testHooks.beforeOwnedPathPublish()
	}
	if err := renameNoReplace(int(parent.Fd()), sourceBase, int(parent.Fd()), targetBase); err != nil {
		return fmt.Errorf("publish no-replace rename: %w", err)
	}
	moved, err := statAt(int(parent.Fd()), targetBase)
	if err != nil || !sameOwnedEntry(before, moved) || !matchesOwnedIdentity(expectedNode, moved) || validateOwnedNode(moved, disposition) != nil {
		return errors.Join(errors.New("publish source changed after validation"), err,
			quarantinePublishedReplacement(parent, targetBase, moved))
	}
	if disposition == EmptyDirectory {
		fd, openErr := openDirectoryAt(int(parent.Fd()), targetBase, moved)
		if openErr == nil {
			directory := os.NewFile(uintptr(fd), targetBase)
			if directory == nil {
				_ = unix.Close(fd)
				openErr = errors.New("construct published directory handle")
			} else {
				openErr = errors.Join(requireEmptyDirectory(directory), directory.Close())
			}
		}
		if openErr != nil {
			return errors.Join(errors.New("published empty directory changed after validation"), openErr,
				quarantinePublishedReplacement(parent, targetBase, moved))
		}
	}
	if _, err := statAt(int(parent.Fd()), sourceBase); !errors.Is(err, unix.ENOENT) {
		if err == nil {
			err = errors.New("publish source still exists")
		}
		return err
	}
	if err := parent.Sync(); err != nil {
		return err
	}
	final, err := statAt(int(parent.Fd()), targetBase)
	if err != nil || !sameNode(moved, final) {
		return errors.Join(errors.New("publish target changed after rename"), err)
	}
	return nil
}

// ReplaceOwnedPath atomically publishes one exact regular-file source over one
// exact regular-file target. The old target is pinned in private quarantine
// before the source-to-target race window opens. On any detected swap, foreign
// public entries are preserved in that quarantine and the exact old target is
// restored before failure is returned.
func ReplaceOwnedPath(sourcePath, targetPath string,
	sourceDisposition, targetDisposition OwnedPathDisposition,
	sourceExpected, targetExpected OwnedPathIdentity,
) error {
	return replaceOwnedPathWithHooks(sourcePath, targetPath, sourceDisposition, targetDisposition,
		sourceExpected, targetExpected, nil)
}

func replaceOwnedPathWithHooks(sourcePath, targetPath string,
	sourceDisposition, targetDisposition OwnedPathDisposition,
	sourceExpected, targetExpected OwnedPathIdentity, testHooks *hooks,
) error {
	sourceExpectedNode, err := validateOwnedPathRequest(sourcePath, sourceDisposition, sourceExpected)
	if err != nil {
		return err
	}
	targetExpectedNode, err := validateOwnedPathRequest(targetPath, targetDisposition, targetExpected)
	if err != nil {
		return err
	}
	if sourceDisposition != RegularFile || targetDisposition != RegularFile {
		return errors.New("owned-path replace v1 requires two regular files")
	}
	if filepath.Dir(sourcePath) != filepath.Dir(targetPath) || sourcePath == targetPath {
		return errors.New("replace paths must be distinct canonical siblings")
	}
	parent, sourceBase, err := openOwnedCanonicalParent(sourcePath)
	if err != nil {
		return fmt.Errorf("open replace parent: %w", err)
	}
	defer parent.Close()
	if err := validateOwnedPathParent(parent, filepath.Dir(sourcePath)); err != nil {
		return fmt.Errorf("replace parent: %w", err)
	}
	targetBase := filepath.Base(targetPath)
	sourceBefore, err := statAt(int(parent.Fd()), sourceBase)
	if err != nil || !matchesOwnedIdentity(sourceExpectedNode, sourceBefore) || validateRegular(sourceBefore) != nil {
		return errors.Join(errors.New("replace source identity drifted"), err)
	}
	targetBefore, err := statAt(int(parent.Fd()), targetBase)
	if err != nil || !matchesOwnedIdentity(targetExpectedNode, targetBefore) || validateRegular(targetBefore) != nil {
		return errors.Join(errors.New("replace target identity drifted"), err)
	}

	quarantine, quarantineName, quarantineIdentity, err := createCleanupQuarantine(parent)
	if err != nil {
		return err
	}
	closeQuarantine := true
	defer func() {
		if closeQuarantine {
			_ = quarantine.Close()
		}
	}()
	if err := unix.Renameat(int(parent.Fd()), targetBase, int(quarantine.Fd()), "old-target"); err != nil {
		_ = quarantine.Close()
		closeQuarantine = false
		return errors.Join(err, removeCleanupQuarantine(parent, quarantineName, quarantineIdentity))
	}
	oldTarget, err := statAt(int(quarantine.Fd()), "old-target")
	if err != nil || !sameOwnedEntry(targetBefore, oldTarget) || !matchesOwnedIdentity(targetExpectedNode, oldTarget) || validateRegular(oldTarget) != nil {
		return errors.Join(errors.New("preserved quarantined target replacement with mismatched identity"), err)
	}
	if err := quarantine.Sync(); err != nil {
		return err
	}
	if testHooks != nil && testHooks.afterOwnedPathReplaceChecks != nil {
		testHooks.afterOwnedPathReplaceChecks()
	}
	if err := renameNoReplace(int(parent.Fd()), sourceBase, int(parent.Fd()), targetBase); err != nil {
		rollbackErr := rollbackOwnedPathReplace(parent, sourceBase, targetBase, quarantine,
			quarantineName, quarantineIdentity, sourceBefore, targetBefore)
		_ = quarantine.Close()
		closeQuarantine = false
		return errors.Join(fmt.Errorf("replace no-replace rename: %w", err), rollbackErr)
	}
	if testHooks != nil && testHooks.afterOwnedPathReplaceMove != nil {
		testHooks.afterOwnedPathReplaceMove()
	}
	newTarget, err := statAt(int(parent.Fd()), targetBase)
	if err != nil || !sameOwnedEntry(sourceBefore, newTarget) || !matchesOwnedIdentity(sourceExpectedNode, newTarget) || validateRegular(newTarget) != nil {
		rollbackErr := rollbackOwnedPathReplace(parent, sourceBase, targetBase, quarantine,
			quarantineName, quarantineIdentity, sourceBefore, targetBefore)
		_ = quarantine.Close()
		closeQuarantine = false
		return errors.Join(errors.New("replace source changed after validation"), err, rollbackErr)
	}
	if _, err := statAt(int(parent.Fd()), sourceBase); !errors.Is(err, unix.ENOENT) {
		rollbackErr := rollbackOwnedPathReplace(parent, sourceBase, targetBase, quarantine,
			quarantineName, quarantineIdentity, sourceBefore, targetBefore)
		_ = quarantine.Close()
		closeQuarantine = false
		return errors.Join(errors.New("replace source path was recreated"), err, rollbackErr)
	}
	if err := parent.Sync(); err != nil {
		return err
	}
	finalTarget, err := statAt(int(parent.Fd()), targetBase)
	if err != nil || !sameNode(newTarget, finalTarget) {
		rollbackErr := rollbackOwnedPathReplace(parent, sourceBase, targetBase, quarantine,
			quarantineName, quarantineIdentity, sourceBefore, targetBefore)
		_ = quarantine.Close()
		closeQuarantine = false
		return errors.Join(errors.New("replace target changed after publish"), err, rollbackErr)
	}
	if err := removeOwnedBoundEntry(quarantine, "old-target", oldTarget, RegularFile, nil, "old-target"); err != nil {
		return err
	}
	if err := quarantine.Sync(); err != nil {
		return err
	}
	if err := quarantine.Close(); err != nil {
		closeQuarantine = false
		return err
	}
	closeQuarantine = false
	if err := removeCleanupQuarantine(parent, quarantineName, quarantineIdentity); err != nil {
		return err
	}
	return parent.Sync()
}

func rollbackOwnedPathReplace(parent *os.File, sourceBase, targetBase string, quarantine *os.File,
	quarantineName string, quarantineIdentity, sourceBefore, targetBefore nodeStat,
) error {
	var result error
	foreignRetained := false
	if current, err := statAt(int(parent.Fd()), targetBase); err == nil {
		if moveErr := moveObservedIntoQuarantine(parent, targetBase, quarantine, "foreign-target", current); moveErr != nil {
			result = errors.Join(result, moveErr)
		} else {
			foreignRetained = true
		}
	} else if !errors.Is(err, unix.ENOENT) {
		result = errors.Join(result, err)
	}
	if current, err := statAt(int(parent.Fd()), sourceBase); err == nil {
		if !sameOwnedEntry(sourceBefore, current) {
			if moveErr := moveObservedIntoQuarantine(parent, sourceBase, quarantine, "foreign-source", current); moveErr != nil {
				result = errors.Join(result, moveErr)
			} else {
				foreignRetained = true
			}
		}
	} else if !errors.Is(err, unix.ENOENT) {
		result = errors.Join(result, err)
	}
	if _, err := statAt(int(parent.Fd()), targetBase); !errors.Is(err, unix.ENOENT) {
		if err == nil {
			err = errors.New("replace rollback target is occupied")
		}
		return errors.Join(result, err)
	}
	if err := renameNoReplace(int(quarantine.Fd()), "old-target", int(parent.Fd()), targetBase); err != nil {
		return errors.Join(result, fmt.Errorf("restore exact old target: %w", err))
	}
	restored, err := statAt(int(parent.Fd()), targetBase)
	if err != nil || !sameOwnedEntry(targetBefore, restored) || validateRegular(restored) != nil {
		return errors.Join(result, errors.New("restored old target identity drifted"), err)
	}
	result = errors.Join(result, parent.Sync(), quarantine.Sync())
	if foreignRetained {
		return result
	}
	if err := quarantine.Close(); err != nil {
		return errors.Join(result, err)
	}
	return errors.Join(result, removeCleanupQuarantine(parent, quarantineName, quarantineIdentity), parent.Sync())
}

func moveObservedIntoQuarantine(parent *os.File, name string, quarantine *os.File, quarantineEntry string, observed nodeStat) error {
	if observed.ino == 0 {
		return errors.New("cannot identify replacement for quarantine")
	}
	if err := unix.Renameat(int(parent.Fd()), name, int(quarantine.Fd()), quarantineEntry); err != nil {
		return err
	}
	moved, err := statAt(int(quarantine.Fd()), quarantineEntry)
	if err != nil || !sameOwnedEntry(observed, moved) {
		return errors.Join(errors.New("replacement quarantine identity drifted"), err)
	}
	return quarantine.Sync()
}

func validateOwnedPathRequest(path string, disposition OwnedPathDisposition, expected OwnedPathIdentity) (nodeStat, error) {
	if !canonicalAbsolute(path) {
		return nodeStat{}, errors.New("owned path must be canonical absolute and non-root")
	}
	if expected.Mode < 0 || uint64(expected.Mode) > uint64(^uint32(0)) || expected.UID < 0 ||
		uint64(expected.UID) > uint64(^uint32(0)) || expected.GID < 0 || uint64(expected.GID) > uint64(^uint32(0)) {
		return nodeStat{}, errors.New("owned-path identity integer is invalid")
	}
	dev, err := parseCanonicalUint(expected.Dev)
	if err != nil {
		return nodeStat{}, errors.New("owned-path device identity is invalid")
	}
	ino, err := parseCanonicalUint(expected.Ino)
	if err != nil || ino == 0 {
		return nodeStat{}, errors.New("owned-path inode identity is invalid")
	}
	result := nodeStat{mode: uint32(expected.Mode), uid: uint32(expected.UID), gid: uint32(expected.GID), dev: dev, ino: ino}
	switch disposition {
	case RegularFile:
		if nodeType(result.mode) != unix.S_IFREG {
			return nodeStat{}, errors.New("regular-file disposition/type mismatch")
		}
	case UnixSocket:
		if nodeType(result.mode) != unix.S_IFSOCK {
			return nodeStat{}, errors.New("unix-socket disposition/type mismatch")
		}
	case EmptyDirectory, RecursiveDirectory:
		if nodeType(result.mode) != unix.S_IFDIR {
			return nodeStat{}, errors.New("directory disposition/type mismatch")
		}
	default:
		return nodeStat{}, errors.New("owned-path disposition is invalid")
	}
	return result, nil
}

func parseCanonicalUint(value string) (uint64, error) {
	if value == "" || strings.HasPrefix(value, "+") || strings.HasPrefix(value, "-") {
		return 0, errors.New("not canonical decimal")
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil || strconv.FormatUint(parsed, 10) != value {
		return 0, errors.New("not canonical decimal")
	}
	return parsed, nil
}

func matchesOwnedIdentity(expected, current nodeStat) bool {
	return expected.mode == current.mode && expected.uid == current.uid && expected.gid == current.gid &&
		expected.dev == current.dev && expected.ino == current.ino
}

// A rename may advance ctime on supported filesystems. All caller-bound
// identity fields plus link count and size must remain exact across it.
func sameOwnedEntry(left, right nodeStat) bool {
	return left.mode == right.mode && left.uid == right.uid && left.gid == right.gid && left.dev == right.dev &&
		left.ino == right.ino && left.nlink == right.nlink && left.size == right.size
}

func validateOwnedNode(stat nodeStat, disposition OwnedPathDisposition) error {
	switch disposition {
	case RegularFile:
		return validateRegular(stat)
	case UnixSocket:
		return validateUnixSocket(stat)
	case EmptyDirectory, RecursiveDirectory:
		return validateDirectory(stat, false)
	default:
		return errors.New("owned-path disposition is invalid")
	}
}

func validateUnixSocket(stat nodeStat) error {
	if nodeType(stat.mode) != unix.S_IFSOCK {
		return errors.New("node is not a Unix socket")
	}
	if stat.uid != uint32(os.Geteuid()) {
		return errors.New("Unix socket has foreign ownership")
	}
	if stat.nlink != 1 {
		return errors.New("hard-linked Unix socket is forbidden")
	}
	if stat.mode&0o022 != 0 || stat.mode&uint32(unix.S_ISUID|unix.S_ISGID|unix.S_ISVTX) != 0 {
		return errors.New("Unix socket is group/world writable or has unsafe special mode bits")
	}
	return nil
}

func validateOwnedPathParent(directory *os.File, canonicalPath string) error {
	if err := validateControlledParent(directory); err == nil {
		return nil
	}
	// O4 creates direct, randomly named owned roots under the canonical system
	// temporary directory. Permit only the conventional root-owned sticky
	// directories; the sticky bit prevents other users from renaming or
	// unlinking our entries despite the shared writable parent.
	if canonicalPath != "/private/tmp" && canonicalPath != "/tmp" {
		return errors.New("owned-path parent is not owner-controlled")
	}
	info, err := directory.Stat()
	if err != nil {
		return err
	}
	stat, err := nodeFromFileInfo(info)
	if err != nil {
		return err
	}
	if nodeType(stat.mode) != unix.S_IFDIR || stat.uid != 0 || stat.gid != 0 ||
		stat.mode&0o777 != 0o777 || stat.mode&uint32(unix.S_ISVTX) == 0 ||
		stat.mode&uint32(unix.S_ISUID|unix.S_ISGID) != 0 {
		return errors.New("system temporary parent is not canonical root-owned mode 01777")
	}
	return nil
}

func requireEmptyDirectory(directory *os.File) error {
	dupFD, err := unix.Dup(int(directory.Fd()))
	if err != nil {
		return err
	}
	reader := os.NewFile(uintptr(dupFD), "empty-directory-check")
	if reader == nil {
		_ = unix.Close(dupFD)
		return errors.New("construct directory reader")
	}
	entries, readErr := reader.ReadDir(1)
	closeErr := reader.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return errors.Join(readErr, closeErr)
	}
	if len(entries) != 0 {
		return errors.New("owned directory is not empty")
	}
	return closeErr
}

func removeOwnedContents(directory *os.File, testHooks *hooks, relative string) error {
	dupFD, err := unix.Dup(int(directory.Fd()))
	if err != nil {
		return err
	}
	reader := os.NewFile(uintptr(dupFD), "owned-path-cleanup")
	if reader == nil {
		_ = unix.Close(dupFD)
		return errors.New("construct owned-path directory reader")
	}
	entries, readErr := reader.ReadDir(-1)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil {
		return errors.Join(readErr, closeErr)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		name := entry.Name()
		if !validName(name) {
			return errors.New("owned directory contains an unsafe name")
		}
		childRelative := name
		if relative != "" {
			childRelative = relative + "/" + name
		}
		before, err := statAt(int(directory.Fd()), name)
		if err != nil {
			return err
		}
		flags := 0
		switch nodeType(before.mode) {
		case unix.S_IFREG:
			if err := validateRegular(before); err != nil {
				return err
			}
		case unix.S_IFDIR:
			if err := validateDirectory(before, false); err != nil {
				return err
			}
			openedIdentity := before
			fd, err := openDirectoryAt(int(directory.Fd()), name, before)
			if err != nil {
				return err
			}
			child := os.NewFile(uintptr(fd), childRelative)
			if child == nil {
				_ = unix.Close(fd)
				return errors.New("construct owned cleanup child handle")
			}
			err = removeOwnedContents(child, testHooks, childRelative)
			closeErr := child.Close()
			if err != nil || closeErr != nil {
				return errors.Join(err, closeErr)
			}
			before, err = statAt(int(directory.Fd()), name)
			if err != nil || !matchesOwnedIdentity(openedIdentity, before) {
				return errors.Join(errors.New("owned cleanup directory changed during traversal"), err)
			}
			if err := validateDirectory(before, false); err != nil {
				return err
			}
			flags = unix.AT_REMOVEDIR
		default:
			return errors.New("owned directory contains a symlink or special file")
		}
		disposition := RegularFile
		if flags != 0 {
			disposition = RecursiveDirectory
		}
		if err := removeOwnedBoundEntry(directory, name, before, disposition, testHooks, childRelative); err != nil {
			return err
		}
	}
	return directory.Sync()
}

func removeOwnedBoundEntry(parent *os.File, name string, expected nodeStat, disposition OwnedPathDisposition, testHooks *hooks, relative string) error {
	flags := 0
	if disposition == EmptyDirectory || disposition == RecursiveDirectory {
		flags = unix.AT_REMOVEDIR
	}
	current, err := statAt(int(parent.Fd()), name)
	if err != nil || !sameNode(expected, current) {
		return errors.Join(errors.New("owned cleanup entry changed before quarantine"), err)
	}
	if testHooks != nil && testHooks.beforeOwnedPathQuarantine != nil {
		testHooks.beforeOwnedPathQuarantine(relative)
	}
	quarantine, quarantineName, quarantineIdentity, err := createCleanupQuarantine(parent)
	if err != nil {
		return err
	}
	closeQuarantine := true
	defer func() {
		if closeQuarantine {
			_ = quarantine.Close()
		}
	}()
	if err := unix.Renameat(int(parent.Fd()), name, int(quarantine.Fd()), "entry"); err != nil {
		_ = quarantine.Close()
		closeQuarantine = false
		return errors.Join(err, removeCleanupQuarantine(parent, quarantineName, quarantineIdentity))
	}
	moved, err := statAt(int(quarantine.Fd()), "entry")
	if err != nil || !sameOwnedEntry(expected, moved) {
		return errors.Join(errors.New("preserved quarantined replacement with mismatched identity"), err)
	}
	if err := validateOwnedNode(moved, disposition); err != nil {
		return errors.Join(errors.New("preserved unsafe quarantined owned path"), err)
	}
	if err := unix.Unlinkat(int(quarantine.Fd()), "entry", flags); err != nil {
		return err
	}
	if err := quarantine.Sync(); err != nil {
		return err
	}
	if err := quarantine.Close(); err != nil {
		closeQuarantine = false
		return err
	}
	closeQuarantine = false
	return removeCleanupQuarantine(parent, quarantineName, quarantineIdentity)
}

func quarantinePublishedReplacement(parent *os.File, target string, observed nodeStat) error {
	if observed.ino == 0 {
		return errors.New("cannot identify publish replacement for quarantine")
	}
	quarantine, _, _, err := createCleanupQuarantine(parent)
	if err != nil {
		return err
	}
	defer quarantine.Close()
	if err := unix.Renameat(int(parent.Fd()), target, int(quarantine.Fd()), "entry"); err != nil {
		return err
	}
	moved, err := statAt(int(quarantine.Fd()), "entry")
	if err != nil || !sameOwnedEntry(observed, moved) {
		return errors.Join(errors.New("publish replacement quarantine identity drifted"), err)
	}
	return errors.Join(quarantine.Sync(), parent.Sync())
}
