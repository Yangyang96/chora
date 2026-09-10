package taskdelivery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResultCleanupRetainsUnexpectedFilesAndBranches(t *testing.T) {
	original, root, b := testRepository(t)
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("unwanted result\n"), 0600); err != nil {
		t.Fatal(err)
	}
	patch := gitBytes(t, root, "diff", "--full-index", "--no-ext-diff", "--no-textconv", "--no-renames", b.BaseCommit, "--", ":(literal)a.txt")
	preview, err := (Git{}).PreviewResultCleanup(ctx, b, patch, []string{"a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	for _, extra := range []string{"extra.txt", "ignored.out"} {
		if extra == "ignored.out" {
			ignore := filepath.Join(t.TempDir(), "ignore")
			if err := os.WriteFile(ignore, []byte("*.out\n"), 0600); err != nil {
				t.Fatal(err)
			}
			git(t, original, "config", "core.excludesFile", ignore)
		}
		path := filepath.Join(root, extra)
		if err := os.WriteFile(path, []byte("keep\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := (Git{}).CleanupClosedResult(ctx, preview); !errors.Is(err, ErrConflict) {
			t.Fatalf("extra file cleanup=%v", err)
		}
		if string(mustRead(t, path)) != "keep\n" {
			t.Fatal("extra file lost")
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("later user edit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := (Git{}).CleanupClosedResult(ctx, preview); !errors.Is(err, ErrConflict) {
		t.Fatalf("later edit cleanup=%v", err)
	}
	if string(mustRead(t, filepath.Join(root, "a.txt"))) != "later user edit\n" {
		t.Fatal("later edit lost")
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("unwanted result\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, root, "update-index", "--assume-unchanged", "a.txt")
	if _, err := (Git{}).PreviewResultCleanup(ctx, b, patch, []string{"a.txt"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("hidden file cleanup=%v", err)
	}
	git(t, root, "update-index", "--no-assume-unchanged", "a.txt")
	// A later main commit is irrelevant to the frozen, uncommitted Task result.
	git(t, original, "commit", "--allow-empty", "-m", "advance main")
	fresh, err := (Git{}).PreviewResultCleanup(ctx, b, patch, []string{"a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	main := git(t, original, "rev-parse", "HEAD")
	if err = (Git{}).CleanupClosedResult(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	if err = (Git{}).ReconcileResultCleanup(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("Task directory remains")
	}
	if string(mustRead(t, filepath.Join(original, "a.txt"))) != "base\n" || git(t, original, "rev-parse", "HEAD") != main {
		t.Fatal("original changed")
	}
	if git(t, original, "rev-parse", "refs/heads/"+b.Branch) != b.BaseCommit {
		t.Fatal("Task branch changed")
	}
}
