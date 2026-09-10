package taskdelivery

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Cleanup never removes branches or uses force. The app separately proves merged
// hosting state and the persisted Task workspace ownership before each call.
type CleanupPreview struct {
	Binding      Binding
	Head, Digest string
}

func cleanupDigest(p CleanupPreview) string {
	p.Digest = ""
	raw, _ := json.Marshal(p)
	return hash(raw)
}
func (Git) PreviewCleanup(ctx context.Context, b Binding, head string) (CleanupPreview, error) {
	if !isOID(head) {
		return CleanupPreview{}, conflict("invalid cleanup commit")
	}
	if err := validateBinding(ctx, b, false, false); err != nil {
		return CleanupPreview{}, err
	}
	marker, err := os.Lstat(filepath.Join(b.Root, ".git"))
	if err != nil || !marker.Mode().IsRegular() {
		return CleanupPreview{}, conflict("cleanup requires a linked Task worktree")
	}
	actual, err := output(ctx, b.Root, nil, "rev-parse", "HEAD")
	if err != nil || actual != head {
		return CleanupPreview{}, conflict("Task branch changed since delivery")
	}
	// Git status intentionally trusts skip-worktree/assume-unchanged flags; do
	// not delete a directory when those flags can hide independent file contents.
	entries, err := outputBytes(ctx, b.Root, nil, "ls-files", "-v", "-z")
	if err != nil {
		return CleanupPreview{}, err
	}
	for _, entry := range strings.Split(string(entries), "\x00") {
		if entry != "" && !strings.HasPrefix(entry, "H ") {
			return CleanupPreview{}, conflict("Task index has hidden or unresolved file states; inspect before cleanup")
		}
	}
	status, err := output(ctx, b.Root, nil, "status", "--porcelain=v1", "--untracked-files=all", "--ignored", "--ignore-submodules=none")
	if err != nil {
		return CleanupPreview{}, err
	}
	if status != "" {
		return CleanupPreview{}, conflict("Task worktree contains modified, staged, untracked or ignored files; preserve them before cleanup")
	}
	p := CleanupPreview{Binding: b, Head: head}
	p.Digest = cleanupDigest(p)
	return p, nil
}
func (Git) Cleanup(ctx context.Context, p CleanupPreview) error {
	if p.Digest == "" || p.Digest != cleanupDigest(p) {
		return conflict("invalid cleanup preview")
	}
	fresh, err := (Git{}).PreviewCleanup(ctx, p.Binding, p.Head)
	if err != nil {
		return err
	}
	if fresh.Digest != p.Digest {
		return conflict("cleanup preview changed")
	}
	// Run from the shared repository, not the directory being removed.
	_, err = output(ctx, p.Binding.CommonGitDir, nil, "worktree", "remove", "--", p.Binding.Root)
	if err != nil {
		return recovery("worktree removal requires read-only reconciliation")
	}
	return (Git{}).ReconcileCleanup(ctx, p)
}
func (Git) ReconcileCleanup(ctx context.Context, p CleanupPreview) error {
	if p.Digest == "" || p.Digest != cleanupDigest(p) {
		return conflict("invalid cleanup preview")
	}
	if err := validateBindingStatic(p.Binding); err != nil {
		return err
	}
	if _, err := os.Lstat(p.Binding.Root); !os.IsNotExist(err) {
		return recovery("Task directory remains; cleanup is not proven")
	}
	branch, err := output(ctx, p.Binding.CommonGitDir, nil, "rev-parse", "refs/heads/"+p.Binding.Branch)
	if err != nil || branch != p.Head {
		return recovery("Task branch retention is not proven")
	}
	listing, err := output(ctx, p.Binding.CommonGitDir, nil, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return err
	}
	for _, field := range strings.Split(listing, "\x00") {
		if field == "worktree "+p.Binding.Root {
			return recovery("Task worktree registration remains")
		}
	}
	return nil
}
