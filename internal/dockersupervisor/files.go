package dockersupervisor

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/Yangyang96/chora/internal/linediff"
)

var privateHomePathPattern = regexp.MustCompile(`/(?:Users|home)/[^/\s\x00]+/`)

var redactedHomePath = []byte("/chora-redacted-home/")

func copyTree(source, destination string) error {
	info, err := os.Stat(source)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("source repository: %w", err)
	}
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return err
	}
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported repository entry %s", relative)
		}
		return copyFile(path, target, info.Mode().Perm())
	})
}

func copyFile(source, destination string, mode fs.FileMode) error {
	input, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	redacted := privateHomePathPattern.ReplaceAll(input, redactedHomePath)
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, bytes.NewReader(redacted))
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func makeContainerWorkspaceWritable(root string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			return os.Chmod(path, 0o777)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported workspace entry %s", path)
		}
		return os.Chmod(path, info.Mode().Perm()|0o666)
	})
}

func derivePatch(baseline, repository string, writableFiles []string, limit int64) ([]byte, error) {
	before, err := treeDigests(baseline)
	if err != nil {
		return nil, err
	}
	after, err := treeDigests(repository)
	if err != nil {
		return nil, err
	}
	paths := make(map[string]struct{}, len(before)+len(after))
	for path := range before {
		paths[path] = struct{}{}
	}
	for path := range after {
		paths[path] = struct{}{}
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)
	allowed, err := normalizedWritableFiles(writableFiles)
	if err != nil {
		return nil, err
	}
	var patch strings.Builder
	changed := 0
	for _, path := range ordered {
		if before[path] == after[path] {
			continue
		}
		if _, ok := allowed[path]; !ok {
			return nil, fmt.Errorf("workspace change outside writable boundary: %s", path)
		}
		oldData, err := readPatchFile(baseline, path)
		if err != nil {
			return nil, fmt.Errorf("read baseline patch file %s: %w", path, err)
		}
		newData, err := readPatchFile(repository, path)
		if err != nil {
			return nil, fmt.Errorf("read repository patch file %s: %w", path, err)
		}
		if strings.IndexByte(string(oldData), 0) >= 0 || strings.IndexByte(string(newData), 0) >= 0 {
			return nil, fmt.Errorf("binary patch file is forbidden: %s", path)
		}
		writeUnifiedDiff(&patch, path, oldData, newData)
		changed++
		if int64(patch.Len()) > limit {
			return nil, fmt.Errorf("patch exceeds artifact limit")
		}
	}
	if changed == 0 {
		return nil, errors.New("no workspace changes")
	}
	return []byte(patch.String()), nil
}

func normalizedWritableFiles(paths []string) (map[string]struct{}, error) {
	if len(paths) == 0 {
		return nil, errors.New("writable file boundary is empty")
	}
	allowed := make(map[string]struct{}, len(paths))
	for _, value := range paths {
		cleaned := filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
		if value == "" || value != cleaned || filepath.IsAbs(value) || cleaned == "." || strings.HasPrefix(cleaned, "../") {
			return nil, fmt.Errorf("invalid writable file path %q", value)
		}
		for _, part := range strings.Split(cleaned, "/") {
			if part == "" || strings.ContainsAny(part, "\\\n\r\t") {
				return nil, fmt.Errorf("invalid writable file path %q", value)
			}
		}
		if _, exists := allowed[cleaned]; exists {
			return nil, fmt.Errorf("duplicate writable file path %q", value)
		}
		allowed[cleaned] = struct{}{}
	}
	return allowed, nil
}

func readPatchFile(root, relative string) ([]byte, error) {
	path := filepath.Join(root, filepath.FromSlash(relative))
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("patch target is not a regular file")
	}
	return os.ReadFile(path)
}

const patchContextLines = 3

func writeUnifiedDiff(patch *strings.Builder, relative string, before, after []byte) {
	oldLines := splitPatchLines(before)
	newLines := splitPatchLines(after)
	edits := linediff.ShortestEdits(oldLines, newLines)
	fmt.Fprintf(patch, "diff --git a/%s b/%s\n", relative, relative)
	fmt.Fprintf(patch, "--- a/%s\n", relative)
	fmt.Fprintf(patch, "+++ b/%s\n", relative)
	oldConsumed, newConsumed := 0, 0
	position := 0
	for _, hunk := range linediff.ContextRanges(edits, patchContextLines) {
		for position < hunk.Start {
			if edits[position].Kind != linediff.Insert {
				oldConsumed++
			}
			if edits[position].Kind != linediff.Delete {
				newConsumed++
			}
			position++
		}
		oldCount, newCount := 0, 0
		for index := hunk.Start; index < hunk.End; index++ {
			if edits[index].Kind != linediff.Insert {
				oldCount++
			}
			if edits[index].Kind != linediff.Delete {
				newCount++
			}
		}
		oldStart := oldConsumed + 1
		newStart := newConsumed + 1
		if oldCount == 0 {
			oldStart = oldConsumed
		}
		if newCount == 0 {
			newStart = newConsumed
		}
		fmt.Fprintf(patch, "@@ -%s +%s @@\n", patchRange(oldStart, oldCount), patchRange(newStart, newCount))
		for position < hunk.End {
			edit := edits[position]
			prefix := byte(' ')
			switch edit.Kind {
			case linediff.Delete:
				prefix = '-'
			case linediff.Insert:
				prefix = '+'
			}
			writePatchLine(patch, prefix, edit.Line)
			if edit.Kind != linediff.Insert {
				oldConsumed++
			}
			if edit.Kind != linediff.Delete {
				newConsumed++
			}
			position++
		}
	}
}

func splitPatchLines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	lines := strings.SplitAfter(string(data), "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func patchRange(start, count int) string {
	if count == 1 {
		return fmt.Sprintf("%d", start)
	}
	return fmt.Sprintf("%d,%d", start, count)
}

func writePatchLine(patch *strings.Builder, prefix byte, line string) {
	patch.WriteByte(prefix)
	patch.WriteString(line)
	if !strings.HasSuffix(line, "\n") {
		patch.WriteString("\n\\ No newline at end of file\n")
	}
}

func treeDigests(root string) (map[string]string, error) {
	digests := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			digest := sha256.Sum256([]byte("symlink\x00" + link))
			digests[filepath.ToSlash(relative)] = hex.EncodeToString(digest[:])
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(data)
		digests[filepath.ToSlash(relative)] = hex.EncodeToString(digest[:])
		return nil
	})
	return digests, err
}
