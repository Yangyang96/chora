package localweb

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Yangyang96/chora/internal/app"
)

func TestGitPatchTargetAppliesWithoutTouchingIndexOrUnrelatedDirtyFiles(t *testing.T) {
	root := newPatchTargetRepository(t)
	if err := os.WriteFile(filepath.Join(root, "unrelated.txt"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTargetTest(t, root, "add", "unrelated.txt")
	indexBefore := runGitTargetTest(t, root, "diff", "--cached", "--binary")
	patch := []byte("diff --git a/internal/value.go b/internal/value.go\n--- a/internal/value.go\n+++ b/internal/value.go\n@@ -1,3 +1,3 @@\n package internal\n \n-func Value() int { return 1 }\n+func Value() int { return 2 }\n")
	target, err := newGitPatchTarget(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := app.PatchTargetRequest{Raw: patch, PatchDigest: sha256.Sum256(patch), AffectedPaths: []string{"internal/value.go"}}
	inspection, err := target.Inspect(context.Background(), request)
	if err != nil || !inspection.Applicable || inspection.AlreadyApplied || inspection.StateDigest == ([32]byte{}) {
		t.Fatalf("inspection=%#v err=%v", inspection, err)
	}
	evidence, err := target.Apply(context.Background(), request, inspection)
	if err != nil || evidence.PostStateDigest == ([32]byte{}) || evidence.PostStateDigest == inspection.StateDigest {
		t.Fatalf("evidence=%#v err=%v", evidence, err)
	}
	if got := string(mustReadFile(t, filepath.Join(root, "internal", "value.go"))); got != "package internal\n\nfunc Value() int { return 2 }\n" {
		t.Fatalf("applied file=%q", got)
	}
	if got := runGitTargetTest(t, root, "diff", "--cached", "--binary"); got != indexBefore {
		t.Fatalf("Git index changed\nbefore=%q\nafter=%q", indexBefore, got)
	}
	if got := string(mustReadFile(t, filepath.Join(root, "unrelated.txt"))); got != "dirty\n" {
		t.Fatalf("unrelated file=%q", got)
	}
	reconciled, err := target.Inspect(context.Background(), app.PatchTargetRequest{Raw: patch, PatchDigest: sha256.Sum256(patch), AffectedPaths: []string{"internal/value.go"}, PriorPreStateDigest: inspection.StateDigest})
	if err != nil || !reconciled.AlreadyApplied {
		t.Fatalf("reconciled=%#v err=%v", reconciled, err)
	}
}

func TestGitPatchTargetConflictAndSymlinkFailWithoutModification(t *testing.T) {
	root := newPatchTargetRepository(t)
	path := filepath.Join(root, "internal", "value.go")
	if err := os.WriteFile(path, []byte("package internal\n\nfunc Value() int { return 9 }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	patch := []byte("diff --git a/internal/value.go b/internal/value.go\n--- a/internal/value.go\n+++ b/internal/value.go\n@@ -1,3 +1,3 @@\n package internal\n \n-func Value() int { return 1 }\n+func Value() int { return 2 }\n")
	target, err := newGitPatchTarget(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := app.PatchTargetRequest{Raw: patch, PatchDigest: sha256.Sum256(patch), AffectedPaths: []string{"internal/value.go"}}
	inspection, err := target.Inspect(context.Background(), request)
	if err != nil || inspection.Applicable || inspection.AlreadyApplied || inspection.Reason == "" {
		t.Fatalf("inspection=%#v err=%v", inspection, err)
	}
	if got := string(mustReadFile(t, path)); got != "package internal\n\nfunc Value() int { return 9 }\n" {
		t.Fatalf("conflict modified target=%q", got)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "unrelated.txt"), path); err != nil {
		t.Fatal(err)
	}
	unsafeInspection, err := target.Inspect(context.Background(), request)
	if !errors.Is(err, app.ErrPatchTargetConflict) || unsafeInspection.Applicable || unsafeInspection.AlreadyApplied || unsafeInspection.Reason == "" || unsafeInspection.TargetIdentity == "" || unsafeInspection.BaseRevision != "" || unsafeInspection.StateDigest != ([32]byte{}) {
		t.Fatalf("unsafe inspection=%#v err=%v", unsafeInspection, err)
	}
}

func TestGitPatchTargetAppliesAddAndDeleteAndDetectsDestinationCollision(t *testing.T) {
	root := newPatchTargetRepository(t)
	patch := []byte("diff --git a/docs/new note.txt b/docs/new note.txt\n" +
		"new file mode 100644\n" +
		"index 0000000000000000000000000000000000000000..ce013625030ba8dba906f756967f9e9ca394464a\n" +
		"--- /dev/null\n" +
		"+++ b/docs/new note.txt\n" +
		"@@ -0,0 +1 @@\n" +
		"+hello\n" +
		"diff --git a/unrelated.txt b/unrelated.txt\n" +
		"deleted file mode 100644\n" +
		"index 5626abf0f72e58d7a153368ba57db4c673c0e171..0000000000000000000000000000000000000000\n" +
		"--- a/unrelated.txt\n" +
		"+++ /dev/null\n" +
		"@@ -1 +0,0 @@\n" +
		"-clean\n")
	target, err := newGitPatchTarget(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := app.PatchTargetRequest{Raw: patch, PatchDigest: sha256.Sum256(patch), AffectedPaths: []string{"docs/new note.txt", "unrelated.txt"}}
	inspection, err := target.Inspect(context.Background(), request)
	if err != nil || !inspection.Applicable {
		t.Fatalf("inspection = %+v, err = %v", inspection, err)
	}
	if _, err := target.Apply(context.Background(), request, inspection); err != nil {
		t.Fatal(err)
	}
	if got := string(mustReadFile(t, filepath.Join(root, "docs", "new note.txt"))); got != "hello\n" {
		t.Fatalf("added file = %q", got)
	}
	if _, err := os.Lstat(filepath.Join(root, "unrelated.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted file exists: %v", err)
	}
	reconciled, err := target.Inspect(context.Background(), app.PatchTargetRequest{Raw: patch, PatchDigest: sha256.Sum256(patch), AffectedPaths: request.AffectedPaths, PriorPreStateDigest: inspection.StateDigest})
	if err != nil || !reconciled.AlreadyApplied {
		t.Fatalf("reconciled = %+v, err = %v", reconciled, err)
	}

	collisionRoot := newPatchTargetRepository(t)
	collisionTarget, err := newGitPatchTarget(collisionRoot, nil)
	if err != nil {
		t.Fatal(err)
	}
	reviewedInspection, err := collisionTarget.Inspect(context.Background(), request)
	if err != nil || !reviewedInspection.Applicable {
		t.Fatalf("pre-edit inspection = %+v, err = %v", reviewedInspection, err)
	}
	if err := os.Mkdir(filepath.Join(collisionRoot, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(collisionRoot, "docs", "new note.txt"), []byte("human edit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	collision, err := collisionTarget.Inspect(context.Background(), request)
	if err != nil || collision.Applicable || collision.AlreadyApplied || collision.Reason == "" {
		t.Fatalf("collision = %+v, err = %v", collision, err)
	}
	if _, err := collisionTarget.Apply(context.Background(), request, reviewedInspection); !errors.Is(err, app.ErrPatchTargetConflict) {
		t.Fatalf("post-review human edit was applied over: %v", err)
	}
	if got := string(mustReadFile(t, filepath.Join(collisionRoot, "docs", "new note.txt"))); got != "human edit\n" {
		t.Fatalf("collision changed human file = %q", got)
	}
}

func TestGitPatchTargetRejectsOversizedExternalEditAfterReview(t *testing.T) {
	root := newPatchTargetRepository(t)
	patch := []byte("diff --git a/internal/value.go b/internal/value.go\n--- a/internal/value.go\n+++ b/internal/value.go\n@@ -1,3 +1,3 @@\n package internal\n \n-func Value() int { return 1 }\n+func Value() int { return 2 }\n")
	target, err := newGitPatchTarget(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := app.PatchTargetRequest{Raw: patch, PatchDigest: sha256.Sum256(patch), AffectedPaths: []string{"internal/value.go"}}
	reviewed, err := target.Inspect(context.Background(), request)
	if err != nil || !reviewed.Applicable {
		t.Fatalf("reviewed inspection = %+v, err = %v", reviewed, err)
	}
	path := filepath.Join(root, "internal", "value.go")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(gitPatchTargetFileByteLimit + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := target.Apply(context.Background(), request, reviewed); !errors.Is(err, app.ErrPatchTargetConflict) {
		t.Fatalf("oversized post-review edit was not a conflict: %v", err)
	}
	if info, err := os.Stat(path); err != nil || info.Size() != gitPatchTargetFileByteLimit+1 {
		t.Fatalf("oversized human edit was modified: info=%+v err=%v", info, err)
	}
}

func newPatchTargetRepository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "internal"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "internal", "value.go"), []byte("package internal\n\nfunc Value() int { return 1 }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "unrelated.txt"), []byte("clean\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTargetTest(t, root, "init")
	runGitTargetTest(t, root, "add", ".")
	runGitTargetTest(t, root, "-c", "user.name=Chora Test", "-c", "user.email=chora@example.test", "commit", "-m", "initial")
	return root
}

func runGitTargetTest(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
