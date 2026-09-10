package taskdelivery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCleanupPreservesDirtyFilesAndRetainsBranch(t *testing.T) {
	original, root, b := testRepository(t)
	ctx := context.Background()
	head := git(t, root, "rev-parse", "HEAD")
	for _, name := range []string{"a.txt", "untracked.txt", "ignored.out"} {
		t.Run(name, func(t *testing.T) {
			git(t, original, "config", "core.excludesFile", filepath.Join(t.TempDir(), "ignore"))
			if name == "ignored.out" {
				ignore := git(t, original, "config", "core.excludesFile")
				os.WriteFile(ignore, []byte("*.out\n"), 0600)
			}
			path := filepath.Join(root, name)
			old, err := os.ReadFile(path)
			os.WriteFile(path, []byte("preserve\n"), 0600)
			if _, e := (Git{}).PreviewCleanup(ctx, b, head); !errors.Is(e, ErrConflict) {
				t.Fatalf("dirty allowed: %v", e)
			}
			if got := string(mustRead(t, path)); got != "preserve\n" {
				t.Fatal("dirty file changed")
			}
			if err == nil {
				os.WriteFile(path, old, 0600)
			} else {
				os.Remove(path)
			}
		})
	}
	for _, flag := range []string{"assume-unchanged", "skip-worktree"} {
		git(t, root, "update-index", "--"+flag, "a.txt")
		os.WriteFile(filepath.Join(root, "a.txt"), []byte("hidden modification\n"), 0600)
		if _, err := (Git{}).PreviewCleanup(ctx, b, head); !errors.Is(err, ErrConflict) {
			t.Fatalf("hidden %s allowed: %v", flag, err)
		}
		git(t, root, "update-index", "--no-"+flag, "a.txt")
		os.WriteFile(filepath.Join(root, "a.txt"), []byte("base\n"), 0600)
	}
	preview, err := (Git{}).PreviewCleanup(ctx, b, head)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "late.txt"), []byte("late\n"), 0600)
	if err := (Git{}).Cleanup(ctx, preview); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale cleanup: %v", err)
	}
	os.Remove(filepath.Join(root, "late.txt"))
	if err := (Git{}).Cleanup(ctx, preview); err != nil {
		t.Fatal(err)
	}
	if err := (Git{}).ReconcileCleanup(ctx, preview); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("worktree remains")
	}
	if got := git(t, original, "rev-parse", "refs/heads/"+b.Branch); got != head {
		t.Fatal("branch was removed")
	}
}
func TestCommitReviewedStagingWithoutDiscardingIntermediateIndex(t *testing.T) {
	_, root, b := testRepository(t)
	ctx := context.Background()
	path := filepath.Join(root, "a.txt")
	os.WriteFile(path, []byte("intermediate\n"), 0600)
	git(t, root, "add", "a.txt")
	os.WriteFile(path, []byte("reviewed\n"), 0600)
	patch := gitBytes(t, root, "diff", "--full-index", "--no-ext-diff", "--no-textconv", "--no-renames", b.BaseCommit, "--", ":(literal)a.txt")
	if _, err := (Git{}).PreviewCommit(ctx, b, patch, []string{"a.txt"}, "reviewed"); !errors.Is(err, ErrConflict) {
		t.Fatalf("intermediate staging: %v", err)
	}
	git(t, root, "add", "a.txt")
	p, err := (Git{}).PreviewCommit(ctx, b, patch, []string{"a.txt"}, "reviewed")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = (Git{}).Commit(ctx, p); err != nil {
		t.Fatal(err)
	}
	if got := git(t, root, "status", "--porcelain"); got != "" {
		t.Fatalf("index not updated: %s", got)
	}
}
