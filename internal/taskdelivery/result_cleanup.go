package taskdelivery

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// ResultCleanupPreview binds only the exact, explicitly closed Result contents.
// The app must separately prove the durable closure and Task ownership. This
// operation never stands in for ordinary post-merge cleanup.
type ResultCleanupPreview struct {
	Binding             Binding
	Patch               []byte
	Paths               []string
	IndexDigest, Digest string
}

func resultCleanupDigest(p ResultCleanupPreview) string {
	p.Digest = ""
	raw, _ := json.Marshal(p)
	return hash(raw)
}

func (Git) PreviewResultCleanup(ctx context.Context, b Binding, patch []byte, paths []string) (ResultCleanupPreview, error) {
	if err := (Git{}).VerifyReviewedChanges(ctx, b, patch, paths); err != nil {
		return ResultCleanupPreview{}, err
	}
	marker, err := os.Lstat(filepath.Join(b.Root, ".git"))
	if err != nil || !marker.Mode().IsRegular() {
		return ResultCleanupPreview{}, conflict("cleanup requires a linked Task worktree")
	}
	entries, err := outputBytes(ctx, b.Root, nil, "ls-files", "-v", "-z")
	if err != nil {
		return ResultCleanupPreview{}, err
	}
	for _, entry := range strings.Split(string(entries), "\x00") {
		if entry != "" && !strings.HasPrefix(entry, "H ") {
			return ResultCleanupPreview{}, conflict("Task index hides file states")
		}
	}
	ignored, err := outputBytes(ctx, b.Root, nil, "ls-files", "--others", "--ignored", "--exclude-standard", "-z")
	if err != nil {
		return ResultCleanupPreview{}, err
	}
	if len(ignored) != 0 {
		return ResultCleanupPreview{}, conflict("Task worktree contains additional ignored files")
	}
	stages, err := outputBytes(ctx, b.Root, nil, "ls-files", "--stage", "-z")
	if err != nil {
		return ResultCleanupPreview{}, err
	}
	for _, entry := range strings.Split(string(stages), "\x00") {
		if strings.HasPrefix(entry, "160000 ") {
			return ResultCleanupPreview{}, unsupported("preserve submodule worktrees before cleanup")
		}
	}
	tree, _, err := previewTree(ctx, b, patch)
	if err != nil {
		return ResultCleanupPreview{}, err
	}
	if err = reviewedStaging(ctx, b, tree, paths); err != nil {
		return ResultCleanupPreview{}, err
	}
	index, err := taskIndexDigest(ctx, b)
	if err != nil {
		return ResultCleanupPreview{}, err
	}
	p := ResultCleanupPreview{Binding: b, Patch: append([]byte(nil), patch...), Paths: append([]string(nil), paths...), IndexDigest: index}
	p.Digest = resultCleanupDigest(p)
	return p, nil
}

func (Git) CleanupClosedResult(ctx context.Context, p ResultCleanupPreview) error {
	if p.Digest == "" || p.Digest != resultCleanupDigest(p) {
		return conflict("invalid closed Result cleanup preview")
	}
	fresh, err := (Git{}).PreviewResultCleanup(ctx, p.Binding, p.Patch, p.Paths)
	if err != nil {
		return err
	}
	if fresh.Digest != p.Digest {
		return conflict("closed Result cleanup preview changed")
	}
	// Force is restricted to the exact unwanted Result proven above, including
	// its original index. Untracked, ignored and later edits refuse beforehand.
	if _, err = output(ctx, p.Binding.CommonGitDir, nil, "worktree", "remove", "--force", "--", p.Binding.Root); err != nil {
		return recovery("closed Result cleanup requires read-only reconciliation")
	}
	return (Git{}).ReconcileResultCleanup(ctx, p)
}

func (Git) ReconcileResultCleanup(ctx context.Context, p ResultCleanupPreview) error {
	if p.Digest == "" || p.Digest != resultCleanupDigest(p) {
		return conflict("invalid closed Result cleanup preview")
	}
	clean := CleanupPreview{Binding: p.Binding, Head: p.Binding.BaseCommit}
	clean.Digest = cleanupDigest(clean)
	return (Git{}).ReconcileCleanup(ctx, clean)
}
