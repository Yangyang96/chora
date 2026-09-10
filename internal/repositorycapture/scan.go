package repositorycapture

import (
	"bytes"
	"crypto/sha1" // Git object IDs are defined by the repository format.
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const (
	RepositoryScanSchema           = "chora.m1-o4-repository-scan-manifest.v1"
	RepositoryReadFilesSchema      = "chora.m1-o4-repository-read-files-response.v1"
	RepositoryProtocolMaxOutput    = int64(32 << 20)
	repositoryScanDigestSize       = 64
	repositoryReadResponseOverhead = int64(512)
)

type RepositoryScanEntry struct {
	Path        string `json:"path"`
	Kind        string `json:"kind"`
	Mode        int    `json:"mode"`
	UID         int    `json:"uid"`
	GID         int    `json:"gid"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256,omitempty"`
	GitBlobSHA1 string `json:"gitBlobSha1,omitempty"`
}

type RepositoryScanManifest struct {
	SchemaVersion  string                `json:"schemaVersion"`
	Status         string                `json:"status"`
	CaptureRoot    string                `json:"captureRoot"`
	RootIdentity   OwnedPathIdentity     `json:"rootIdentity"`
	Entries        []RepositoryScanEntry `json:"entries"`
	EntryCount     int64                 `json:"entryCount"`
	TotalBytes     int64                 `json:"totalBytes"`
	ManifestDigest string                `json:"manifestDigest"`
}

type RepositoryReadFile struct {
	Path        string `json:"path"`
	BytesBase64 string `json:"bytesBase64"`
	SHA256      string `json:"sha256"`
}

type RepositoryReadFilesResponse struct {
	SchemaVersion  string               `json:"schemaVersion"`
	Status         string               `json:"status"`
	CaptureRoot    string               `json:"captureRoot"`
	RootIdentity   OwnedPathIdentity    `json:"rootIdentity"`
	Files          []RepositoryReadFile `json:"files"`
	FileCount      int64                `json:"fileCount"`
	TotalBytes     int64                `json:"totalBytes"`
	ResponseDigest string               `json:"responseDigest"`
}

type repositoryScanLimits struct {
	maxDepth         int
	maxEntries       int64
	maxPathBytes     int64
	maxFileBytes     int64
	maxTotalBytes    int64
	maxManifestBytes int64
}

var defaultRepositoryScanLimits = repositoryScanLimits{
	maxDepth: 64, maxEntries: 50_000, maxPathBytes: 4 << 20,
	maxFileBytes: 128 << 20, maxTotalBytes: 512 << 20,
	maxManifestBytes: RepositoryProtocolMaxOutput,
}

type repositoryReadLimits struct {
	maxFiles       int64
	maxPathBytes   int64
	maxFileBytes   int64
	maxTotalBytes  int64
	maxOutputBytes int64
}

var defaultRepositoryReadLimits = repositoryReadLimits{
	maxFiles: 256, maxPathBytes: 1 << 20, maxFileBytes: 8 << 20,
	maxTotalBytes: 23 << 20, maxOutputBytes: RepositoryProtocolMaxOutput,
}

// repositoryScanHooks are test-only deterministic mutation points.
type repositoryScanHooks struct {
	afterRootOpen       func()
	beforeEntryStat     func(relative string)
	beforeDirectoryOpen func(relative string)
	afterDirectoryOpen  func(relative string)
	beforeFileOpen      func(relative string)
	beforeFileRead      func(relative string)
	afterFileRead       func(relative string)
}

type repositoryScanConfig struct {
	limits repositoryScanLimits
	hooks  *repositoryScanHooks
}

type repositoryReadConfig struct {
	limits repositoryReadLimits
	hooks  *repositoryScanHooks
}

type pinnedRepositoryRoot struct {
	bindings   []repositoryPathBinding
	parent     *os.File
	base       string
	root       *os.File
	rootBefore nodeStat
}

type repositoryPathBinding struct {
	file   *os.File
	before nodeStat
	parent *os.File
	name   string
}

// ScanRepository produces a bounded manifest without returning file contents.
func ScanRepository(captureRoot string, expectedRoot OwnedPathIdentity) (RepositoryScanManifest, error) {
	return scanRepositoryWithConfig(captureRoot, expectedRoot,
		repositoryScanConfig{limits: defaultRepositoryScanLimits})
}

func scanRepositoryWithConfig(captureRoot string, expectedRoot OwnedPathIdentity, cfg repositoryScanConfig) (
	result RepositoryScanManifest, resultErr error,
) {
	if err := validateRepositoryScanLimits(cfg.limits); err != nil {
		return RepositoryScanManifest{}, err
	}
	pinned, err := openPinnedRepositoryRoot(captureRoot, expectedRoot)
	if err != nil {
		return RepositoryScanManifest{}, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, pinned.close())
		if resultErr != nil {
			result = RepositoryScanManifest{}
		}
	}()
	if cfg.hooks != nil && cfg.hooks.afterRootOpen != nil {
		cfg.hooks.afterRootOpen()
	}
	state := repositoryScanState{
		config: cfg,
		manifest: RepositoryScanManifest{
			SchemaVersion: RepositoryScanSchema, Status: "passed", CaptureRoot: captureRoot,
			RootIdentity: ownedPathIdentity(pinned.rootBefore), Entries: []RepositoryScanEntry{},
		},
	}
	if err := state.initializeOutputBudget(); err != nil {
		return RepositoryScanManifest{}, err
	}
	if err := state.scanDirectory(pinned.root, pinned.rootBefore, "", 0); err != nil {
		return RepositoryScanManifest{}, err
	}
	if err := pinned.verify(); err != nil {
		return RepositoryScanManifest{}, err
	}
	sort.Slice(state.manifest.Entries, func(i, j int) bool {
		return state.manifest.Entries[i].Path < state.manifest.Entries[j].Path
	})
	state.manifest.EntryCount = state.entryCount
	state.manifest.TotalBytes = state.totalBytes
	digest, err := repositoryScanDigest(state.manifest)
	if err != nil {
		return RepositoryScanManifest{}, err
	}
	state.manifest.ManifestDigest = digest
	encoded, err := marshalCanonicalJSON(repositoryScanValue(state.manifest, true))
	if err != nil || int64(len(encoded)+1) > cfg.limits.maxManifestBytes {
		return RepositoryScanManifest{}, errors.Join(errors.New("repository scan manifest exceeds output bound"), err)
	}
	return state.manifest, nil
}

// ReadRepositoryFiles returns only the exact, sorted relative paths requested
// by the caller. It is intended for small repository-control metadata selected
// from a previously authenticated scan manifest.
func ReadRepositoryFiles(captureRoot string, expectedRoot OwnedPathIdentity, relativePaths []string) (RepositoryReadFilesResponse, error) {
	return readRepositoryFilesWithConfig(captureRoot, expectedRoot, relativePaths,
		repositoryReadConfig{limits: defaultRepositoryReadLimits})
}

func readRepositoryFilesWithConfig(captureRoot string, expectedRoot OwnedPathIdentity, relativePaths []string,
	cfg repositoryReadConfig,
) (result RepositoryReadFilesResponse, resultErr error) {
	if err := validateRepositoryReadLimits(cfg.limits); err != nil {
		return RepositoryReadFilesResponse{}, err
	}
	if err := validateRequestedRelativePaths(relativePaths, cfg.limits); err != nil {
		return RepositoryReadFilesResponse{}, err
	}
	pinned, err := openPinnedRepositoryRoot(captureRoot, expectedRoot)
	if err != nil {
		return RepositoryReadFilesResponse{}, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, pinned.close())
		if resultErr != nil {
			result = RepositoryReadFilesResponse{}
		}
	}()
	if cfg.hooks != nil && cfg.hooks.afterRootOpen != nil {
		cfg.hooks.afterRootOpen()
	}
	state := repositoryReadState{
		config: cfg,
		response: RepositoryReadFilesResponse{
			SchemaVersion: RepositoryReadFilesSchema, Status: "passed", CaptureRoot: captureRoot,
			RootIdentity: ownedPathIdentity(pinned.rootBefore), Files: []RepositoryReadFile{},
		},
	}
	if err := state.initializeOutputBudget(); err != nil {
		return RepositoryReadFilesResponse{}, err
	}
	for _, relative := range relativePaths {
		entry, err := state.readOne(pinned.root, pinned.rootBefore, relative)
		if err != nil {
			return RepositoryReadFilesResponse{}, err
		}
		state.response.Files = append(state.response.Files, entry)
	}
	if err := pinned.verify(); err != nil {
		return RepositoryReadFilesResponse{}, err
	}
	state.response.FileCount = int64(len(state.response.Files))
	state.response.TotalBytes = state.totalBytes
	digest, err := repositoryReadDigest(state.response)
	if err != nil {
		return RepositoryReadFilesResponse{}, err
	}
	state.response.ResponseDigest = digest
	encoded, err := marshalCanonicalJSON(repositoryReadValue(state.response, true))
	if err != nil || int64(len(encoded)+1) > cfg.limits.maxOutputBytes {
		return RepositoryReadFilesResponse{}, errors.Join(errors.New("repository read response exceeds output bound"), err)
	}
	return state.response, nil
}

func MarshalRepositoryScanManifest(manifest RepositoryScanManifest) ([]byte, error) {
	digest, err := repositoryScanDigest(manifest)
	if err != nil || manifest.ManifestDigest != digest {
		return nil, errors.Join(errors.New("repository scan manifest digest drifted"), err)
	}
	encoded, err := marshalCanonicalJSON(repositoryScanValue(manifest, true))
	if err != nil || int64(len(encoded)+1) > RepositoryProtocolMaxOutput {
		return nil, errors.Join(errors.New("repository scan manifest exceeds protocol output bound"), err)
	}
	return append(encoded, '\n'), nil
}

func MarshalRepositoryReadFilesResponse(response RepositoryReadFilesResponse) ([]byte, error) {
	digest, err := repositoryReadDigest(response)
	if err != nil || response.ResponseDigest != digest {
		return nil, errors.Join(errors.New("repository read response digest drifted"), err)
	}
	encoded, err := marshalCanonicalJSON(repositoryReadValue(response, true))
	if err != nil || int64(len(encoded)+1) > RepositoryProtocolMaxOutput {
		return nil, errors.Join(errors.New("repository read response exceeds protocol output bound"), err)
	}
	return append(encoded, '\n'), nil
}

type repositoryScanState struct {
	config       repositoryScanConfig
	manifest     RepositoryScanManifest
	entryCount   int64
	pathBytes    int64
	totalBytes   int64
	outputBudget int64
}

func (state *repositoryScanState) initializeOutputBudget() error {
	shell := state.manifest
	shell.ManifestDigest = strings.Repeat("0", repositoryScanDigestSize)
	encoded, err := marshalCanonicalJSON(repositoryScanValue(shell, true))
	if err != nil {
		return err
	}
	state.outputBudget = int64(len(encoded)) + 128
	if state.outputBudget+1 > state.config.limits.maxManifestBytes {
		return errors.New("repository scan manifest header exceeds output bound")
	}
	return nil
}

func (state *repositoryScanState) scanDirectory(directory *os.File, before nodeStat, relative string, depth int) error {
	if depth > state.config.limits.maxDepth {
		return errors.New("repository scan depth limit exceeded")
	}
	openedInfo, err := directory.Stat()
	if err != nil {
		return err
	}
	opened, err := nodeFromFileInfo(openedInfo)
	if err != nil || !sameNode(before, opened) || validateDirectory(opened, false) != nil {
		return errors.Join(fmt.Errorf("repository scan directory %q identity drifted", relative), err)
	}
	seen := map[string]struct{}{}
	for {
		items, readErr := directory.ReadDir(1)
		if len(items) == 0 && errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil || len(items) != 1 {
			return errors.Join(fmt.Errorf("stream repository scan directory %q", relative), readErr)
		}
		name := items[0].Name()
		if !validRepositoryRelativeName(name) {
			return errors.New("repository scan contains an unsafe entry name")
		}
		if _, duplicate := seen[name]; duplicate {
			return errors.New("repository scan contains a duplicate entry name")
		}
		seen[name] = struct{}{}
		childRelative := name
		if relative != "" {
			childRelative = relative + "/" + name
		}
		if err := state.accountPath(childRelative); err != nil {
			return err
		}
		if state.config.hooks != nil && state.config.hooks.beforeEntryStat != nil {
			state.config.hooks.beforeEntryStat(childRelative)
		}
		entryBefore, err := statAt(int(directory.Fd()), name)
		if err != nil {
			return fmt.Errorf("stat repository scan entry %q: %w", childRelative, err)
		}
		switch nodeType(entryBefore.mode) {
		case unix.S_IFDIR:
			if err := validateDirectory(entryBefore, false); err != nil {
				return fmt.Errorf("repository scan directory %q: %w", childRelative, err)
			}
			entry := scanEntryFromNode(childRelative, "directory", entryBefore, "", "")
			if err := state.reserveOutput(entry); err != nil {
				return err
			}
			if state.config.hooks != nil && state.config.hooks.beforeDirectoryOpen != nil {
				state.config.hooks.beforeDirectoryOpen(childRelative)
			}
			fd, err := openDirectoryAt(int(directory.Fd()), name, entryBefore)
			if err != nil {
				return fmt.Errorf("open repository scan directory %q: %w", childRelative, err)
			}
			child := os.NewFile(uintptr(fd), childRelative)
			if child == nil {
				_ = unix.Close(fd)
				return errors.New("construct repository scan directory handle")
			}
			if state.config.hooks != nil && state.config.hooks.afterDirectoryOpen != nil {
				state.config.hooks.afterDirectoryOpen(childRelative)
			}
			scanErr := state.scanDirectory(child, entryBefore, childRelative, depth+1)
			closeErr := child.Close()
			if scanErr != nil || closeErr != nil {
				return errors.Join(scanErr, closeErr)
			}
			entryAfter, err := statAt(int(directory.Fd()), name)
			if err != nil || !sameNode(entryBefore, entryAfter) {
				return errors.Join(fmt.Errorf("repository scan directory entry %q changed", childRelative), err)
			}
			state.manifest.Entries = append(state.manifest.Entries, entry)
		case unix.S_IFREG:
			entry, err := state.scanRegularFile(directory, name, childRelative, entryBefore)
			if err != nil {
				return err
			}
			state.manifest.Entries = append(state.manifest.Entries, entry)
		default:
			return fmt.Errorf("repository scan entry %q is a symlink or special file", childRelative)
		}
	}
	closedInfo, err := directory.Stat()
	if err != nil {
		return err
	}
	closed, err := nodeFromFileInfo(closedInfo)
	if err != nil || !sameNode(before, closed) {
		return errors.Join(fmt.Errorf("repository scan directory %q changed while enumerating", relative), err)
	}
	return nil
}

func (state *repositoryScanState) scanRegularFile(parent *os.File, name, relative string, before nodeStat) (RepositoryScanEntry, error) {
	if err := validateRegular(before); err != nil {
		return RepositoryScanEntry{}, fmt.Errorf("repository scan file %q: %w", relative, err)
	}
	if before.size > state.config.limits.maxFileBytes {
		return RepositoryScanEntry{}, fmt.Errorf("repository scan file %q exceeds file limit", relative)
	}
	if before.size > state.config.limits.maxTotalBytes-state.totalBytes {
		return RepositoryScanEntry{}, errors.New("repository scan total-byte limit exceeded before read")
	}
	placeholder := scanEntryFromNode(relative, "regular_file", before,
		strings.Repeat("0", 64), strings.Repeat("0", 40))
	if err := state.reserveOutput(placeholder); err != nil {
		return RepositoryScanEntry{}, err
	}
	if state.config.hooks != nil && state.config.hooks.beforeFileOpen != nil {
		state.config.hooks.beforeFileOpen(relative)
	}
	fd, err := unix.Openat(int(parent.Fd()), name,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_NOCTTY, 0)
	if err != nil {
		return RepositoryScanEntry{}, fmt.Errorf("open repository scan file %q: %w", relative, err)
	}
	file := os.NewFile(uintptr(fd), relative)
	if file == nil {
		_ = unix.Close(fd)
		return RepositoryScanEntry{}, errors.New("construct repository scan file handle")
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return RepositoryScanEntry{}, err
	}
	opened, err := nodeFromFileInfo(openedInfo)
	if err != nil || !sameNode(before, opened) || validateRegular(opened) != nil {
		return RepositoryScanEntry{}, errors.Join(fmt.Errorf("repository scan file %q changed before read", relative), err)
	}
	if state.config.hooks != nil && state.config.hooks.beforeFileRead != nil {
		state.config.hooks.beforeFileRead(relative)
	}
	sha256Hash := sha256.New()
	gitHash := sha1.New() // Git's blob object format mandates SHA-1 here.
	_, _ = io.WriteString(gitHash, fmt.Sprintf("blob %d\x00", before.size))
	buffer := make([]byte, 64<<10)
	remaining := before.size
	for remaining > 0 {
		chunk := int64(len(buffer))
		if remaining < chunk {
			chunk = remaining
		}
		count, readErr := io.ReadFull(file, buffer[:int(chunk)])
		if readErr != nil || int64(count) != chunk {
			return RepositoryScanEntry{}, errors.Join(fmt.Errorf("read repository scan file %q exactly", relative), readErr)
		}
		_, _ = sha256Hash.Write(buffer[:count])
		_, _ = gitHash.Write(buffer[:count])
		remaining -= int64(count)
	}
	var extra [1]byte
	count, readErr := file.Read(extra[:])
	if count != 0 || (readErr != nil && !errors.Is(readErr, io.EOF)) {
		return RepositoryScanEntry{}, fmt.Errorf("repository scan file %q grew while reading", relative)
	}
	if state.config.hooks != nil && state.config.hooks.afterFileRead != nil {
		state.config.hooks.afterFileRead(relative)
	}
	afterInfo, err := file.Stat()
	if err != nil {
		return RepositoryScanEntry{}, err
	}
	after, err := nodeFromFileInfo(afterInfo)
	if err != nil || !sameNode(opened, after) {
		return RepositoryScanEntry{}, errors.Join(fmt.Errorf("repository scan file %q changed while reading", relative), err)
	}
	entryAfter, err := statAt(int(parent.Fd()), name)
	if err != nil || !sameNode(before, entryAfter) {
		return RepositoryScanEntry{}, errors.Join(fmt.Errorf("repository scan file entry %q changed", relative), err)
	}
	state.totalBytes += before.size
	return scanEntryFromNode(relative, "regular_file", before,
		hex.EncodeToString(sha256Hash.Sum(nil)), hex.EncodeToString(gitHash.Sum(nil))), nil
}

func (state *repositoryScanState) accountPath(relative string) error {
	state.entryCount++
	if state.entryCount > state.config.limits.maxEntries || state.entryCount > maxSafeJSON {
		return errors.New("repository scan entry-count limit exceeded")
	}
	if int64(len(relative)) > state.config.limits.maxPathBytes-state.pathBytes {
		return errors.New("repository scan path-byte limit exceeded")
	}
	state.pathBytes += int64(len(relative))
	return nil
}

func (state *repositoryScanState) reserveOutput(entry RepositoryScanEntry) error {
	encoded, err := marshalCanonicalJSON(repositoryScanEntryValue(entry))
	if err != nil {
		return err
	}
	delta := int64(len(encoded))
	if state.entryCount > 1 {
		delta++
	}
	if delta > state.config.limits.maxManifestBytes-state.outputBudget-1 {
		return errors.New("repository scan manifest budget exceeded before read")
	}
	state.outputBudget += delta
	return nil
}

type repositoryReadState struct {
	config       repositoryReadConfig
	response     RepositoryReadFilesResponse
	totalBytes   int64
	outputBudget int64
}

func (state *repositoryReadState) initializeOutputBudget() error {
	shell := state.response
	shell.ResponseDigest = strings.Repeat("0", repositoryScanDigestSize)
	encoded, err := marshalCanonicalJSON(repositoryReadValue(shell, true))
	if err != nil {
		return err
	}
	state.outputBudget = int64(len(encoded)) + repositoryReadResponseOverhead
	if state.outputBudget+1 > state.config.limits.maxOutputBytes {
		return errors.New("repository read response header exceeds output bound")
	}
	return nil
}

type readDirectoryBinding struct {
	file   *os.File
	before nodeStat
	parent *os.File
	name   string
}

func (state *repositoryReadState) readOne(root *os.File, rootBefore nodeStat, relative string) (result RepositoryReadFile, resultErr error) {
	components := strings.Split(relative, "/")
	fd, err := unix.Dup(int(root.Fd()))
	if err != nil {
		return RepositoryReadFile{}, err
	}
	unix.CloseOnExec(fd)
	rootCopy := os.NewFile(uintptr(fd), "repository-read-root")
	if rootCopy == nil {
		_ = unix.Close(fd)
		return RepositoryReadFile{}, errors.New("construct repository read root handle")
	}
	bindings := []readDirectoryBinding{{file: rootCopy, before: rootBefore}}
	defer func() {
		resultErr = errors.Join(resultErr, verifyReadDirectoryBindings(bindings))
		for index := len(bindings) - 1; index >= 0; index-- {
			resultErr = errors.Join(resultErr, bindings[index].file.Close())
		}
	}()
	current := rootCopy
	directoryRelative := ""
	for _, component := range components[:len(components)-1] {
		if directoryRelative == "" {
			directoryRelative = component
		} else {
			directoryRelative += "/" + component
		}
		before, err := statAt(int(current.Fd()), component)
		if err != nil || validateDirectory(before, false) != nil {
			return RepositoryReadFile{}, errors.Join(fmt.Errorf("repository read directory %q is invalid", directoryRelative), err)
		}
		if state.config.hooks != nil && state.config.hooks.beforeDirectoryOpen != nil {
			state.config.hooks.beforeDirectoryOpen(directoryRelative)
		}
		childFD, err := openDirectoryAt(int(current.Fd()), component, before)
		if err != nil {
			return RepositoryReadFile{}, err
		}
		child := os.NewFile(uintptr(childFD), directoryRelative)
		if child == nil {
			_ = unix.Close(childFD)
			return RepositoryReadFile{}, errors.New("construct repository read directory handle")
		}
		openedInfo, err := child.Stat()
		if err != nil {
			_ = child.Close()
			return RepositoryReadFile{}, err
		}
		opened, err := nodeFromFileInfo(openedInfo)
		if err != nil || !sameNode(before, opened) {
			_ = child.Close()
			return RepositoryReadFile{}, errors.Join(fmt.Errorf("repository read directory %q changed before open", directoryRelative), err)
		}
		bindings = append(bindings, readDirectoryBinding{file: child, before: before, parent: current, name: component})
		current = child
		if state.config.hooks != nil && state.config.hooks.afterDirectoryOpen != nil {
			state.config.hooks.afterDirectoryOpen(directoryRelative)
		}
	}
	name := components[len(components)-1]
	before, err := statAt(int(current.Fd()), name)
	if err != nil || validateRegular(before) != nil {
		return RepositoryReadFile{}, errors.Join(fmt.Errorf("repository read file %q is invalid", relative), err)
	}
	if err := state.reserveRead(relative, before.size); err != nil {
		return RepositoryReadFile{}, err
	}
	if state.config.hooks != nil && state.config.hooks.beforeFileOpen != nil {
		state.config.hooks.beforeFileOpen(relative)
	}
	fileFD, err := unix.Openat(int(current.Fd()), name,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_NOCTTY, 0)
	if err != nil {
		return RepositoryReadFile{}, err
	}
	file := os.NewFile(uintptr(fileFD), relative)
	if file == nil {
		_ = unix.Close(fileFD)
		return RepositoryReadFile{}, errors.New("construct repository read file handle")
	}
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	openedInfo, err := file.Stat()
	if err != nil {
		return RepositoryReadFile{}, err
	}
	opened, err := nodeFromFileInfo(openedInfo)
	if err != nil || !sameNode(before, opened) || validateRegular(opened) != nil {
		return RepositoryReadFile{}, errors.Join(fmt.Errorf("repository read file %q changed before read", relative), err)
	}
	if state.config.hooks != nil && state.config.hooks.beforeFileRead != nil {
		state.config.hooks.beforeFileRead(relative)
	}
	bytes := make([]byte, int(before.size))
	if _, err := io.ReadFull(file, bytes); err != nil {
		return RepositoryReadFile{}, err
	}
	var extra [1]byte
	count, readErr := file.Read(extra[:])
	if count != 0 || (readErr != nil && !errors.Is(readErr, io.EOF)) {
		return RepositoryReadFile{}, errors.New("repository read file grew while reading")
	}
	if state.config.hooks != nil && state.config.hooks.afterFileRead != nil {
		state.config.hooks.afterFileRead(relative)
	}
	afterInfo, err := file.Stat()
	if err != nil {
		return RepositoryReadFile{}, err
	}
	after, err := nodeFromFileInfo(afterInfo)
	if err != nil || !sameNode(opened, after) {
		return RepositoryReadFile{}, errors.Join(fmt.Errorf("repository read file %q changed while reading", relative), err)
	}
	entryAfter, err := statAt(int(current.Fd()), name)
	if err != nil || !sameNode(before, entryAfter) {
		return RepositoryReadFile{}, errors.Join(fmt.Errorf("repository read file entry %q changed", relative), err)
	}
	digest := sha256.Sum256(bytes)
	state.totalBytes += before.size
	return RepositoryReadFile{Path: relative, BytesBase64: base64.StdEncoding.EncodeToString(bytes),
		SHA256: hex.EncodeToString(digest[:])}, nil
}

func (state *repositoryReadState) reserveRead(relative string, size int64) error {
	if size < 0 || size > state.config.limits.maxFileBytes {
		return fmt.Errorf("repository read file %q exceeds file limit", relative)
	}
	if size > state.config.limits.maxTotalBytes-state.totalBytes {
		return errors.New("repository read total-byte limit exceeded before read")
	}
	base64Bytes := int64(base64.StdEncoding.EncodedLen(int(size)))
	pathJSON, err := json.Marshal(relative)
	if err != nil {
		return err
	}
	itemBudget := int64(len(pathJSON)) + base64Bytes + 160
	if len(state.response.Files) > 0 {
		itemBudget++
	}
	if itemBudget > state.config.limits.maxOutputBytes-state.outputBudget-1 {
		return errors.New("repository read output budget exceeded before read")
	}
	state.outputBudget += itemBudget
	return nil
}

func verifyReadDirectoryBindings(bindings []readDirectoryBinding) error {
	var result error
	for index := len(bindings) - 1; index >= 0; index-- {
		binding := bindings[index]
		info, err := binding.file.Stat()
		if err != nil {
			result = errors.Join(result, err)
			continue
		}
		current, err := nodeFromFileInfo(info)
		if err != nil || !sameNode(binding.before, current) {
			result = errors.Join(result, errors.New("repository read directory handle changed"), err)
		}
		if binding.parent != nil {
			entry, err := statAt(int(binding.parent.Fd()), binding.name)
			if err != nil || !sameNode(binding.before, entry) {
				result = errors.Join(result, errors.New("repository read directory entry changed"), err)
			}
		}
	}
	return result
}

func openPinnedRepositoryRoot(path string, expected OwnedPathIdentity) (*pinnedRepositoryRoot, error) {
	if !utf8.ValidString(path) || strings.ContainsAny(path, "\u2028\u2029") {
		return nil, errors.New("repository root is not canonical UTF-8 JSON text")
	}
	expectedNode, err := validateOwnedPathRequest(path, RecursiveDirectory, expected)
	if err != nil {
		return nil, err
	}
	filesystemPath, err := ownedFilesystemPath(path)
	if err != nil {
		return nil, err
	}
	components := strings.Split(strings.TrimPrefix(filesystemPath, string(filepath.Separator)), string(filepath.Separator))
	if len(components) == 0 || len(components) > 256 {
		return nil, errors.New("repository root ancestor depth is invalid")
	}
	rootFD, err := unix.Open(string(filepath.Separator),
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	filesystemRoot := os.NewFile(uintptr(rootFD), string(filepath.Separator))
	if filesystemRoot == nil {
		_ = unix.Close(rootFD)
		return nil, errors.New("construct repository filesystem-root handle")
	}
	filesystemRootInfo, err := filesystemRoot.Stat()
	if err != nil {
		_ = filesystemRoot.Close()
		return nil, err
	}
	filesystemRootBefore, err := nodeFromFileInfo(filesystemRootInfo)
	if err != nil {
		_ = filesystemRoot.Close()
		return nil, err
	}
	bindings := []repositoryPathBinding{{file: filesystemRoot, before: filesystemRootBefore}}
	current := filesystemRoot
	for index, component := range components {
		if !validName(component) {
			_ = closeRepositoryPathBindings(bindings)
			return nil, errors.New("repository root contains an unsafe ancestor name")
		}
		before, err := statAt(int(current.Fd()), component)
		if err != nil || nodeType(before.mode) != unix.S_IFDIR {
			_ = closeRepositoryPathBindings(bindings)
			return nil, errors.Join(errors.New("repository root ancestor is absent, a symlink, or not a directory"), err)
		}
		isFinal := index == len(components)-1
		if isFinal && (!matchesOwnedIdentity(expectedNode, before) || validateDirectory(before, false) != nil) {
			_ = closeRepositoryPathBindings(bindings)
			return nil, errors.New("repository root identity drifted")
		}
		childFD, err := openDirectoryAt(int(current.Fd()), component, before)
		if err != nil {
			_ = closeRepositoryPathBindings(bindings)
			return nil, err
		}
		child := os.NewFile(uintptr(childFD), component)
		if child == nil {
			_ = unix.Close(childFD)
			_ = closeRepositoryPathBindings(bindings)
			return nil, errors.New("construct repository-root ancestor handle")
		}
		childInfo, err := child.Stat()
		if err != nil {
			_ = child.Close()
			_ = closeRepositoryPathBindings(bindings)
			return nil, err
		}
		opened, err := nodeFromFileInfo(childInfo)
		if err != nil || !samePinnedDirectoryIdentity(before, opened) {
			_ = child.Close()
			_ = closeRepositoryPathBindings(bindings)
			return nil, errors.Join(errors.New("repository-root ancestor changed during open"), err)
		}
		bindings = append(bindings, repositoryPathBinding{
			file: child, before: before, parent: current, name: component,
		})
		current = child
	}
	if len(bindings) < 2 {
		_ = closeRepositoryPathBindings(bindings)
		return nil, errors.New("repository root binding is incomplete")
	}
	rootBinding := bindings[len(bindings)-1]
	return &pinnedRepositoryRoot{bindings: bindings, parent: rootBinding.parent,
		base: rootBinding.name, root: rootBinding.file, rootBefore: rootBinding.before}, nil
}

func (root *pinnedRepositoryRoot) verify() error {
	rootInfo, err := root.root.Stat()
	if err != nil {
		return err
	}
	rootCurrent, err := nodeFromFileInfo(rootInfo)
	if err != nil || !sameNode(root.rootBefore, rootCurrent) {
		return errors.Join(errors.New("repository root handle changed"), err)
	}
	entry, err := statAt(int(root.parent.Fd()), root.base)
	if err != nil || !sameNode(root.rootBefore, entry) {
		return errors.Join(errors.New("repository root parent entry changed"), err)
	}
	return verifyRepositoryPathBindings(root.bindings)
}

func (root *pinnedRepositoryRoot) close() error {
	return closeRepositoryPathBindings(root.bindings)
}

func verifyRepositoryPathBindings(bindings []repositoryPathBinding) error {
	var result error
	for index := len(bindings) - 1; index >= 0; index-- {
		binding := bindings[index]
		info, err := binding.file.Stat()
		if err != nil {
			result = errors.Join(result, err)
			continue
		}
		current, err := nodeFromFileInfo(info)
		if err != nil || !samePinnedDirectoryIdentity(binding.before, current) {
			result = errors.Join(result, errors.New("repository-root ancestor handle changed"), err)
		}
		if binding.parent != nil {
			entry, err := statAt(int(binding.parent.Fd()), binding.name)
			if err != nil || !samePinnedDirectoryIdentity(binding.before, entry) {
				result = errors.Join(result, errors.New("repository-root ancestor entry changed"), err)
			}
		}
	}
	return result
}

func closeRepositoryPathBindings(bindings []repositoryPathBinding) error {
	var result error
	for index := len(bindings) - 1; index >= 0; index-- {
		result = errors.Join(result, bindings[index].file.Close())
	}
	return result
}

func samePinnedDirectoryIdentity(left, right nodeStat) bool {
	return left.mode == right.mode && left.uid == right.uid && left.gid == right.gid &&
		left.dev == right.dev && left.ino == right.ino
}

func scanEntryFromNode(path, kind string, stat nodeStat, sha256Value, gitBlobSHA1 string) RepositoryScanEntry {
	return RepositoryScanEntry{Path: path, Kind: kind, Mode: int(stat.mode), UID: int(stat.uid), GID: int(stat.gid),
		Size: stat.size, SHA256: sha256Value, GitBlobSHA1: gitBlobSHA1}
}

func repositoryScanDigest(manifest RepositoryScanManifest) (string, error) {
	encoded, err := marshalCanonicalJSON(repositoryScanValue(manifest, false))
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func repositoryReadDigest(response RepositoryReadFilesResponse) (string, error) {
	encoded, err := marshalCanonicalJSON(repositoryReadValue(response, false))
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func repositoryScanValue(manifest RepositoryScanManifest, includeDigest bool) map[string]any {
	entries := make([]any, len(manifest.Entries))
	for index, entry := range manifest.Entries {
		entries[index] = repositoryScanEntryValue(entry)
	}
	value := map[string]any{
		"schemaVersion": manifest.SchemaVersion,
		"status":        manifest.Status,
		"captureRoot":   manifest.CaptureRoot,
		"rootIdentity":  repositoryIdentityValue(manifest.RootIdentity),
		"entries":       entries,
		"entryCount":    manifest.EntryCount,
		"totalBytes":    manifest.TotalBytes,
	}
	if includeDigest {
		value["manifestDigest"] = manifest.ManifestDigest
	}
	return value
}

func repositoryScanEntryValue(entry RepositoryScanEntry) map[string]any {
	value := map[string]any{
		"path": entry.Path, "kind": entry.Kind, "mode": entry.Mode,
		"uid": entry.UID, "gid": entry.GID, "size": entry.Size,
	}
	if entry.SHA256 != "" {
		value["sha256"] = entry.SHA256
	}
	if entry.GitBlobSHA1 != "" {
		value["gitBlobSha1"] = entry.GitBlobSHA1
	}
	return value
}

func repositoryReadValue(response RepositoryReadFilesResponse, includeDigest bool) map[string]any {
	files := make([]any, len(response.Files))
	for index, file := range response.Files {
		files[index] = map[string]any{
			"path": file.Path, "bytesBase64": file.BytesBase64, "sha256": file.SHA256,
		}
	}
	value := map[string]any{
		"schemaVersion": response.SchemaVersion,
		"status":        response.Status,
		"captureRoot":   response.CaptureRoot,
		"rootIdentity":  repositoryIdentityValue(response.RootIdentity),
		"files":         files,
		"fileCount":     response.FileCount,
		"totalBytes":    response.TotalBytes,
	}
	if includeDigest {
		value["responseDigest"] = response.ResponseDigest
	}
	return value
}

func repositoryIdentityValue(identity OwnedPathIdentity) map[string]any {
	return map[string]any{
		"mode": identity.Mode, "uid": identity.UID, "gid": identity.GID,
		"dev": identity.Dev, "ino": identity.Ino,
	}
}

// marshalCanonicalJSON matches the repository's JavaScript
// canonicalJSONStringify contract: object keys are recursively sorted,
// arrays retain their order, and HTML characters are not needlessly escaped.
func marshalCanonicalJSON(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte{'\n'}), nil
}

func validateRepositoryScanLimits(value repositoryScanLimits) error {
	if value.maxDepth < 0 || value.maxEntries < 1 || value.maxPathBytes < 1 || value.maxFileBytes < 0 ||
		value.maxTotalBytes < 0 || value.maxManifestBytes < 1 || value.maxEntries > maxSafeJSON ||
		value.maxPathBytes > maxSafeJSON || value.maxFileBytes > maxSafeJSON || value.maxTotalBytes > maxSafeJSON ||
		value.maxManifestBytes > RepositoryProtocolMaxOutput {
		return errors.New("invalid repository scan limits")
	}
	return nil
}

func validateRepositoryReadLimits(value repositoryReadLimits) error {
	if value.maxFiles < 1 || value.maxPathBytes < 1 || value.maxFileBytes < 0 || value.maxTotalBytes < 0 ||
		value.maxOutputBytes < 1 || value.maxFiles > maxSafeJSON || value.maxPathBytes > maxSafeJSON ||
		value.maxFileBytes > maxSafeJSON || value.maxTotalBytes > maxSafeJSON ||
		value.maxOutputBytes > RepositoryProtocolMaxOutput {
		return errors.New("invalid repository read limits")
	}
	return nil
}

func validateRequestedRelativePaths(paths []string, limits repositoryReadLimits) error {
	if len(paths) == 0 || int64(len(paths)) > limits.maxFiles {
		return errors.New("repository read path count is invalid")
	}
	var pathBytes int64
	previous := ""
	for _, path := range paths {
		if !validRepositoryRelativePath(path) || (previous != "" && previous >= path) {
			return errors.New("repository read paths are not exact sorted unique relative paths")
		}
		if int64(len(path)) > limits.maxPathBytes-pathBytes {
			return errors.New("repository read path-byte limit exceeded")
		}
		pathBytes += int64(len(path))
		previous = path
	}
	return nil
}

func validRepositoryRelativePath(path string) bool {
	if path == "" || filepath.IsAbs(path) || filepath.Clean(path) != path || path == "." ||
		strings.IndexByte(path, 0) >= 0 || strings.Contains(path, "\\") || !utf8.ValidString(path) {
		return false
	}
	for _, component := range strings.Split(path, "/") {
		if !validRepositoryRelativeName(component) {
			return false
		}
	}
	return true
}

func validRepositoryRelativeName(name string) bool {
	return validName(name) && utf8.ValidString(name) && !strings.Contains(name, "\\") &&
		!strings.ContainsAny(name, "\u2028\u2029")
}
