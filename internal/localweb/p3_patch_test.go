package localweb

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
)

func TestP3PatchMaterializesAndAppliesScopedTextAddModifyDeleteAndRename(t *testing.T) {
	ctx := context.Background()
	anchor := newP3PatchRepository(t)
	revision := strings.TrimSpace(runTaskWorktreeGitTest(t, anchor, "rev-parse", "HEAD"))
	db := openTaskWorktreeResolverStore(t)
	defer db.Close()
	now := time.Date(2026, 9, 7, 8, 0, 0, 0, time.UTC)
	_, task := insertResolverRoomAndTask(t, db, anchor, revision, "P3", now)
	contract := registerPatchScope(t, db, task, anchor, revision, []string{"README.md", "delete.txt"}, []string{"src", "docs", "a b"}, now)
	resolver := newTaskWorktreeResolver(db.Reader(), nil)
	worktree := persistReadyResolverWorktree(t, db, resolver, task, now)
	store, err := newLocalConnectedPatchStore(filepath.Join(t.TempDir(), "review-patches"), db.Reader(), resolver)
	if err != nil {
		t.Fatal(err)
	}
	request := app.ReviewPatchMaterializationRequest{RunID: domain.NewRunID(), TaskID: task.ID(), AttemptID: domain.NewAttemptID(), WorkingRoot: worktree}
	if _, err := store.MaterializeReviewPatch(ctx, request); !errors.Is(err, app.ErrNoReviewableChanges) {
		t.Fatalf("clean worktree error = %v, want ErrNoReviewableChanges", err)
	}

	mustWriteP3File(t, filepath.Join(worktree, "README.md"), "changed ✓\n")
	mustWriteP3File(t, filepath.Join(worktree, "a b", "tracked.txt"), "tracked changed\n")
	mustWriteP3File(t, filepath.Join(worktree, "a b", "c.txt"), "new ambiguous header path\n")
	if err := os.Remove(filepath.Join(worktree, "delete.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(worktree, "src", "old name.txt"), filepath.Join(worktree, "src", "new name.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(worktree, "docs", "旧 文档.txt")); err != nil {
		t.Fatal(err)
	}
	mustWriteP3File(t, filepath.Join(worktree, "docs", "设计 note.txt"), "新增内容\n")
	if err := os.Mkdir(filepath.Join(worktree, "build"), 0o700); err != nil {
		t.Fatal(err)
	}
	mustWriteP3File(t, filepath.Join(worktree, "build", "ignored.bin"), "ignored\n")

	artifact, err := store.MaterializeReviewPatch(ctx, request)
	if err != nil {
		t.Fatalf("MaterializeReviewPatch: %v", err)
	}
	raw, err := store.ReadReviewPatch(ctx, artifact.Locator)
	if err != nil {
		t.Fatal(err)
	}
	review, err := app.BuildReviewablePatch(contract, raw, sha256.Sum256(raw))
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(review.Files))
	for _, file := range review.Files {
		got = append(got, string(file.Kind)+":"+file.Path)
	}
	sort.Strings(got)
	want := []string{
		"added:a b/c.txt",
		"added:docs/设计 note.txt",
		"added:src/new name.txt",
		"deleted:delete.txt",
		"deleted:docs/旧 文档.txt",
		"deleted:src/old name.txt",
		"modified:README.md",
		"modified:a b/tracked.txt",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") || strings.Contains(string(raw), "build/ignored.bin") {
		t.Fatalf("review files = %q\nPatch:\n%s", got, raw)
	}

	paths := make([]string, 0, len(review.Files))
	for _, file := range review.Files {
		paths = append(paths, file.Path)
	}
	target := &repositoryBoundGitPatchTarget{reader: db.Reader()}
	applyRequest := app.PatchTargetRequest{TaskID: task.ID(), Raw: raw, PatchDigest: sha256.Sum256(raw), AffectedPaths: paths}
	inspection, err := target.Inspect(ctx, applyRequest)
	if err != nil || !inspection.Applicable {
		t.Fatalf("Inspect = %+v, err = %v", inspection, err)
	}
	if _, err := target.Apply(ctx, applyRequest, inspection); err != nil {
		t.Fatal(err)
	}
	if got := string(mustReadFile(t, filepath.Join(anchor, "docs", "设计 note.txt"))); got != "新增内容\n" {
		t.Fatalf("added content = %q", got)
	}
	if got := string(mustReadFile(t, filepath.Join(anchor, "a b", "tracked.txt"))); got != "tracked changed\n" {
		t.Fatalf("ambiguous-header tracked content = %q", got)
	}
	if got := string(mustReadFile(t, filepath.Join(anchor, "a b", "c.txt"))); got != "new ambiguous header path\n" {
		t.Fatalf("ambiguous-header added content = %q", got)
	}
	if _, err := os.Lstat(filepath.Join(anchor, "delete.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted file still exists: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(anchor, "docs", "旧 文档.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Unicode deleted file still exists: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(anchor, "src", "old name.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("renamed source still exists: %v", err)
	}
	if got := string(mustReadFile(t, filepath.Join(anchor, "src", "new name.txt"))); got != "old\n" {
		t.Fatalf("renamed destination = %q", got)
	}
	reconciled, err := target.Inspect(ctx, app.PatchTargetRequest{TaskID: task.ID(), Raw: raw, PatchDigest: sha256.Sum256(raw), AffectedPaths: paths, PriorPreStateDigest: inspection.StateDigest})
	if err != nil || !reconciled.AlreadyApplied || reconciled.Applicable {
		t.Fatalf("duplicate Apply reconciliation = %+v, err = %v", reconciled, err)
	}
}

func TestP3PatchRejectsOutOfScopeAndUnsupportedOutputs(t *testing.T) {
	tests := []struct {
		name       string
		make       func(*testing.T, string)
		want       string
		noArtifact bool
	}{
		{name: "out of scope", make: func(t *testing.T, root string) { mustWriteP3File(t, filepath.Join(root, "outside.txt"), "no\n") }, want: "outside its frozen writable scope"},
		{name: "binary", make: func(t *testing.T, root string) {
			mustWriteP3Bytes(t, filepath.Join(root, "docs", "bad.bin"), []byte{'a', 0, 'b'})
		}, want: "binary or non-UTF-8"},
		{name: "symlink", make: func(t *testing.T, root string) {
			if err := os.Symlink(filepath.Join(root, "README.md"), filepath.Join(root, "docs", "link.txt")); err != nil {
				t.Fatal(err)
			}
		}, want: "symlink"},
		{name: "lfs", make: func(t *testing.T, root string) {
			mustWriteP3File(t, filepath.Join(root, "docs", "pointer.txt"), "version https://git-lfs.github.com/spec/v1\noid sha256:0123\nsize 4\n")
		}, want: "Git LFS"},
		{name: "new executable", make: func(t *testing.T, root string) {
			path := filepath.Join(root, "docs", "run.sh")
			mustWriteP3File(t, path, "#!/bin/sh\n")
			if err := os.Chmod(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}, want: "new executable"},
		{name: "oversized scoped file", make: func(t *testing.T, root string) {
			file, err := os.Create(filepath.Join(root, "docs", "oversized.txt"))
			if err != nil {
				t.Fatal(err)
			}
			if err := file.Truncate(localConnectedRepositoryFileByteLimit + 1); err != nil {
				file.Close()
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
		}, want: "exceeds the supported review size", noArtifact: true},
		{name: "oversized scoped modification", make: func(t *testing.T, root string) {
			file, err := os.OpenFile(filepath.Join(root, "README.md"), os.O_WRONLY|os.O_TRUNC, 0)
			if err != nil {
				t.Fatal(err)
			}
			if err := file.Truncate(localConnectedRepositoryFileByteLimit + 1); err != nil {
				file.Close()
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
		}, want: "exceeds the supported review size", noArtifact: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			anchor := newP3PatchRepository(t)
			revision := strings.TrimSpace(runTaskWorktreeGitTest(t, anchor, "rev-parse", "HEAD"))
			db := openTaskWorktreeResolverStore(t)
			defer db.Close()
			now := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
			_, task := insertResolverRoomAndTask(t, db, anchor, revision, "P3 reject", now)
			registerPatchScope(t, db, task, anchor, revision, []string{"README.md"}, []string{"docs"}, now)
			resolver := newTaskWorktreeResolver(db.Reader(), nil)
			worktree := persistReadyResolverWorktree(t, db, resolver, task, now)
			if err := os.MkdirAll(filepath.Join(worktree, "docs"), 0o700); err != nil {
				t.Fatal(err)
			}
			test.make(t, worktree)
			patchRoot := filepath.Join(t.TempDir(), "patches")
			store, err := newLocalConnectedPatchStore(patchRoot, db.Reader(), resolver)
			if err != nil {
				t.Fatal(err)
			}
			_, err = store.MaterializeReviewPatch(ctx, app.ReviewPatchMaterializationRequest{RunID: domain.NewRunID(), TaskID: task.ID(), AttemptID: domain.NewAttemptID(), WorkingRoot: worktree})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
			if test.noArtifact {
				entries, readErr := os.ReadDir(patchRoot)
				if readErr != nil || len(entries) != 0 {
					t.Fatalf("oversized output created review artifact entries %v: %v", entries, readErr)
				}
			}
		})
	}
}

func TestBuildScopedReviewPatchBoundsAggregateOutput(t *testing.T) {
	root := newP3PatchRepository(t)
	base := strings.TrimSpace(runTaskWorktreeGitTest(t, root, "rev-parse", "HEAD"))
	mustWriteP3File(t, filepath.Join(root, "docs", "first-new.txt"), "first\n")
	mustWriteP3File(t, filepath.Join(root, "docs", "second-new.txt"), "second\n")
	firstPath := "docs/first-new.txt"
	secondPath := "docs/second-new.txt"
	firstOnly, err := buildScopedReviewPatchBounded(context.Background(), root, base, []string{firstPath}, map[string]struct{}{firstPath: {}}, 4096)
	if err != nil || len(firstOnly) == 0 {
		t.Fatalf("first Patch = %d bytes, err = %v", len(firstOnly), err)
	}
	limit := len(firstOnly) + 8
	if patch, err := buildScopedReviewPatchBounded(context.Background(), root, base, []string{firstPath, secondPath}, map[string]struct{}{firstPath: {}, secondPath: {}}, limit); err == nil || len(patch) != 0 {
		t.Fatalf("aggregate Patch exceeded %d bytes without a bounded failure: bytes=%d err=%v", limit, len(patch), err)
	}
}

func newP3PatchRepository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "a b"), 0o700); err != nil {
		t.Fatal(err)
	}
	mustWriteP3File(t, filepath.Join(root, "README.md"), "initial\n")
	mustWriteP3File(t, filepath.Join(root, "delete.txt"), "delete me\n")
	mustWriteP3File(t, filepath.Join(root, "src", "old name.txt"), "old\n")
	mustWriteP3File(t, filepath.Join(root, "docs", "旧 文档.txt"), "旧内容\n")
	mustWriteP3File(t, filepath.Join(root, "a b", "tracked.txt"), "tracked initial\n")
	mustWriteP3File(t, filepath.Join(root, ".gitignore"), "build/\n")
	runTaskWorktreeGitTest(t, root, "init")
	runTaskWorktreeGitTest(t, root, "add", ".")
	runTaskWorktreeGitTest(t, root, "-c", "user.name=Chora Test", "-c", "user.email=chora@example.test", "commit", "-m", "initial")
	return root
}

func mustWriteP3File(t *testing.T, path, content string) {
	t.Helper()
	mustWriteP3Bytes(t, path, []byte(content))
}

func mustWriteP3Bytes(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}
