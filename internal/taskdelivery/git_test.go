package taskdelivery

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommitUsesReviewedTreeAndLeavesOriginalCheckoutUntouched(t *testing.T) {
	root, task, b := testRepository(t)
	os.WriteFile(filepath.Join(root, "local.txt"), []byte("local\n"), 0600)
	git(t, root, "add", "local.txt")
	os.WriteFile(filepath.Join(root, "local.txt"), []byte("local dirty\n"), 0600)
	os.WriteFile(filepath.Join(task, "a.txt"), []byte("changed\n"), 0600)
	patch := gitBytes(t, task, "-c", "core.quotePath=false", "diff", "--full-index", "--no-ext-diff", "--no-textconv", "--no-renames", b.BaseCommit, "--", ":(literal)a.txt")
	p, err := (Git{}).PreviewCommit(context.Background(), b, patch, []string{"a.txt"}, "reviewed message")
	if err != nil {
		t.Fatal(err)
	}
	r, err := (Git{}).Commit(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if r.Tree != p.Tree || r.Commit == "" {
		t.Fatalf("unexpected result: %+v", r)
	}
	if got := git(t, task, "status", "--porcelain"); got != "" {
		t.Fatalf("task worktree not clean: %q", got)
	}
	if got := string(mustRead(t, filepath.Join(root, "local.txt"))); got != "local dirty\n" {
		t.Fatalf("original changed: %q", got)
	}
	if got := git(t, root, "diff", "--cached", "--name-only"); got != "local.txt" {
		t.Fatalf("original index changed: %q", got)
	}
	if got := git(t, task, "show", "-s", "--format=%B", r.Commit); got != "reviewed message" {
		t.Fatalf("message: %q", got)
	}
}

func TestPreviewRejectsDriftIncompletePathsAndStagedIndex(t *testing.T) {
	_, task, b := testRepository(t)
	os.WriteFile(filepath.Join(task, "a.txt"), []byte("changed\n"), 0600)
	patch := gitBytes(t, task, "-c", "core.quotePath=false", "diff", "--full-index", "--no-ext-diff", "--no-textconv", "--no-renames", b.BaseCommit, "--", ":(literal)a.txt")
	if _, err := (Git{}).PreviewCommit(context.Background(), b, patch, []string{"a.txt"}, "m"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(task, "extra.txt"), []byte("extra\n"), 0600)
	if _, err := (Git{}).PreviewCommit(context.Background(), b, patch, []string{"a.txt"}, "m"); !errors.Is(err, ErrConflict) {
		t.Fatalf("incomplete paths: %v", err)
	}
	os.Remove(filepath.Join(task, "extra.txt"))
	os.WriteFile(filepath.Join(task, "a.txt"), []byte("drift\n"), 0600)
	if _, err := (Git{}).PreviewCommit(context.Background(), b, patch, []string{"a.txt"}, "m"); !errors.Is(err, ErrConflict) {
		t.Fatalf("drift: %v", err)
	}
	git(t, task, "add", "a.txt")
	if _, err := (Git{}).PreviewCommit(context.Background(), b, patch, []string{"a.txt"}, "m"); !errors.Is(err, ErrConflict) {
		t.Fatalf("staged index: %v", err)
	}
}

func TestPreviewIgnoresAmbientRepositoryIndexAndInjectedConfig(t *testing.T) {
	_, task, b := testRepository(t)
	os.WriteFile(filepath.Join(task, "a.txt"), []byte("changed\n"), 0600)
	patch := gitBytes(t, task, "-c", "core.quotePath=false", "diff", "--full-index", "--no-ext-diff", "--no-textconv", "--no-renames", b.BaseCommit, "--", ":(literal)a.txt")
	t.Setenv("GIT_DIR", t.TempDir())
	t.Setenv("GIT_WORK_TREE", t.TempDir())
	t.Setenv("GIT_INDEX_FILE", filepath.Join(t.TempDir(), "hostile-index"))
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.bare")
	t.Setenv("GIT_CONFIG_VALUE_0", "true")
	t.Setenv("GIT_EXTERNAL_DIFF", "false")
	if _, err := (Git{}).PreviewCommit(context.Background(), b, patch, []string{"a.txt"}, "m"); err != nil {
		t.Fatal(err)
	}
}

func TestCommitHonorsIdentityAndHooks(t *testing.T) {
	_, task, b := testRepository(t)
	os.WriteFile(filepath.Join(task, "a.txt"), []byte("changed\n"), 0600)
	patch := gitBytes(t, task, "-c", "core.quotePath=false", "diff", "--full-index", "--no-ext-diff", "--no-textconv", "--no-renames", b.BaseCommit, "--", ":(literal)a.txt")
	p, err := (Git{}).PreviewCommit(context.Background(), b, patch, []string{"a.txt"}, "m")
	if err != nil {
		t.Fatal(err)
	}
	git(t, task, "config", "user.name", "")
	git(t, task, "config", "user.email", "")
	if _, err = (Git{}).Commit(context.Background(), p); !errors.Is(err, ErrRecovery) {
		t.Fatalf("identity failure: %v", err)
	}
	git(t, task, "config", "user.name", "T")
	git(t, task, "config", "user.email", "t@example.test")
	hook := filepath.Join(b.CommonGitDir, "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = (Git{}).Commit(context.Background(), p); !errors.Is(err, ErrRecovery) {
		t.Fatalf("hook failure: %v", err)
	}
}

func TestCommitSupportsReviewedUntrackedFileAndReconcilesDuplicate(t *testing.T) {
	_, task, b := testRepository(t)
	path := "new file.txt"
	os.WriteFile(filepath.Join(task, path), []byte("new\n"), 0600)
	patch := gitDiffExitOne(t, task, "-c", "core.quotePath=false", "diff", "--no-index", "--full-index", "--no-ext-diff", "--no-textconv", "--", "/dev/null", path)
	p, err := (Git{}).PreviewCommit(context.Background(), b, patch, []string{path}, "new file")
	if err != nil {
		t.Fatal(err)
	}
	r, err := (Git{}).Commit(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	again, err := (Git{}).ReconcileCommit(context.Background(), p)
	if err != nil || again != r {
		t.Fatalf("reconcile = %+v, %v", again, err)
	}
	if _, err = (Git{}).Commit(context.Background(), p); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate commit should conflict without mutation: %v", err)
	}
}

func TestPushExactRefAndDetectsAdvancement(t *testing.T) {
	root, task, b := testRepository(t)
	remote := t.TempDir()
	git(t, remote, "init", "--bare")
	git(t, root, "remote", "add", "origin", remote)
	git(t, root, "push", remote, b.TargetRef+":"+b.TargetRef)
	os.WriteFile(filepath.Join(task, "a.txt"), []byte("changed\n"), 0600)
	patch := gitBytes(t, task, "-c", "core.quotePath=false", "diff", "--full-index", "--no-ext-diff", "--no-textconv", "--no-renames", b.BaseCommit, "--", ":(literal)a.txt")
	cp, _ := (Git{}).PreviewCommit(context.Background(), b, patch, []string{"a.txt"}, "m")
	cr, err := (Git{}).Commit(context.Background(), cp)
	if err != nil {
		t.Fatal(err)
	}
	pp, err := (Git{}).PreviewPush(context.Background(), b, "origin")
	if err != nil {
		t.Fatal(err)
	}
	if pp.Head != cr.Commit {
		t.Fatal("wrong push head")
	}
	if err = (Git{}).Push(context.Background(), pp); err != nil {
		t.Fatal(err)
	}
	if got := git(t, remote, "rev-parse", pp.RemoteRef); got != cr.Commit {
		t.Fatalf("remote head %s", got)
	}
	git(t, root, "commit", "--allow-empty", "-m", "advance")
	git(t, root, "push", remote, b.TargetRef+":"+b.TargetRef)
	if _, err = (Git{}).PreviewPush(context.Background(), b, "origin"); !errors.Is(err, ErrConflict) {
		t.Fatalf("target advance: %v", err)
	}
	if reconciled, reconcileErr := (Git{}).ReconcilePush(context.Background(), pp); reconcileErr != nil || !reconciled {
		t.Fatalf("completed push lost after target advance: %v, %v", reconciled, reconcileErr)
	}
	if reconciledCommit, reconcileErr := (Git{}).ReconcileCommit(context.Background(), cp); reconcileErr != nil || reconciledCommit != cr {
		t.Fatalf("completed commit lost after target advance: %+v, %v", reconciledCommit, reconcileErr)
	}
}

func TestPushRejectsMultipleURLsAndDoesNotFollowTags(t *testing.T) {
	root, task, b := testRepository(t)
	remote := t.TempDir()
	other := t.TempDir()
	git(t, remote, "init", "--bare")
	git(t, other, "init", "--bare")
	git(t, root, "remote", "add", "origin", remote)
	git(t, root, "config", "--add", "remote.origin.pushurl", remote)
	git(t, root, "config", "--add", "remote.origin.pushurl", other)
	if _, err := (Git{}).PreviewPush(context.Background(), b, "origin"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("multiple URLs: %v", err)
	}
	git(t, root, "config", "--unset-all", "remote.origin.pushurl")
	git(t, root, "config", "remote.origin.url", remote)
	git(t, root, "push", remote, b.TargetRef+":"+b.TargetRef)
	os.WriteFile(filepath.Join(task, "a.txt"), []byte("changed\n"), 0600)
	patch := gitBytes(t, task, "-c", "core.quotePath=false", "diff", "--full-index", "--no-ext-diff", "--no-textconv", "--no-renames", b.BaseCommit, "--", ":(literal)a.txt")
	cp, _ := (Git{}).PreviewCommit(context.Background(), b, patch, []string{"a.txt"}, "m")
	cr, err := (Git{}).Commit(context.Background(), cp)
	if err != nil {
		t.Fatal(err)
	}
	git(t, task, "tag", "delivery-unrelated", cr.Commit)
	pp, err := (Git{}).PreviewPush(context.Background(), b, "origin")
	if err != nil {
		t.Fatal(err)
	}
	if err = (Git{}).Push(context.Background(), pp); err != nil {
		t.Fatal(err)
	}
	if out := git(t, remote, "for-each-ref", "--format=%(refname)", "refs/tags"); out != "" {
		t.Fatalf("unrelated tag pushed: %q", out)
	}
}

func testRepository(t *testing.T) (string, string, Binding) {
	t.Helper()
	root := t.TempDir()
	git(t, root, "init", "-b", "main")
	git(t, root, "config", "user.name", "T")
	git(t, root, "config", "user.email", "t@example.test")
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("base\n"), 0600)
	git(t, root, "add", "a.txt")
	git(t, root, "commit", "-m", "base")
	base := git(t, root, "rev-parse", "HEAD")
	tree := git(t, root, "show", "-s", "--format=%T", base)
	task := filepath.Join(t.TempDir(), "task")
	git(t, root, "worktree", "add", "-b", "chora/task/repo", task, base)
	common := git(t, task, "rev-parse", "--path-format=absolute", "--git-common-dir")
	return root, task, Binding{Root: task, CommonGitDir: common, BaseCommit: base, BaseTree: tree, Branch: "chora/task/repo", TargetRef: "refs/heads/main"}
}
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return strings.TrimSpace(string(gitBytes(t, dir, args...)))
}
func gitBytes(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "LC_ALL=C")
	var stderr bytes.Buffer
	c.Stderr = &stderr
	b, e := c.Output()
	if e != nil {
		t.Fatalf("git %v: %v: %s", args, e, stderr.String())
	}
	return b
}
func gitDiffExitOne(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "LC_ALL=C")
	var stderr bytes.Buffer
	c.Stderr = &stderr
	b, e := c.Output()
	if e != nil {
		var ee *exec.ExitError
		if !errors.As(e, &ee) || ee.ExitCode() != 1 {
			t.Fatalf("git %v: %v: %s", args, e, stderr.String())
		}
	}
	return b
}
func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	return b
}

func TestPushPreviewRejectsCredentialQueriesAndOpaqueHelpersBeforeDisclosure(t *testing.T) {
	root, _, binding := testRepository(t)
	git(t, root, "remote", "add", "origin", "https://example.invalid/repo.git")
	for _, destination := range []string{"https://example.invalid/repo.git?access_token=fixture-secret", "https://example.invalid/repo.git#fixture-secret", "https://user:fixture-secret@example.invalid/repo.git", "ext::echo fixture-secret", "helper:fixture-secret"} {
		git(t, root, "remote", "set-url", "origin", destination)
		preview, err := (Git{}).PreviewPush(context.Background(), binding, "origin")
		if !errors.Is(err, ErrUnsupported) || preview.URL != "" || strings.Contains(err.Error(), "fixture-secret") {
			t.Fatalf("unsafe destination reached preview/disclosure: %+v %v", preview, err)
		}
	}
}

func TestPushPreviewNeverExecutesExtHelperContainingURLSyntax(t *testing.T) {
	root, _, binding := testRepository(t)
	directory := t.TempDir()
	marker := filepath.Join(directory, "executed")
	script := filepath.Join(directory, "helper.sh")
	t.Setenv("CHORA_DELIVERY_HELPER_MARKER", marker)
	if err := os.WriteFile(script, []byte("#!/bin/sh\n: > \"$CHORA_DELIVERY_HELPER_MARKER\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	git(t, root, "config", "protocol.ext.allow", "always")
	git(t, root, "remote", "add", "origin", "ext::sh "+script+" https://example.invalid")
	if _, err := (Git{}).PreviewPush(context.Background(), binding, "origin"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("helper URL accepted: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("preview executed helper: %v", err)
	}
}

func TestReconcileCommitDoesNotCertifyStaleOrConflictedRealIndex(t *testing.T) {
	_, task, b := testRepository(t)
	os.WriteFile(filepath.Join(task, "a.txt"), []byte("changed\n"), 0600)
	patch := gitBytes(t, task, "-c", "core.quotePath=false", "diff", "--full-index", "--no-ext-diff", "--no-textconv", "--no-renames", b.BaseCommit, "--", ":(literal)a.txt")
	p, err := (Git{}).PreviewCommit(context.Background(), b, patch, []string{"a.txt"}, "reviewed message")
	if err != nil {
		t.Fatal(err)
	}
	result, err := (Git{}).Commit(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	// Reproduce the crash window: ref is published, real index still names base.
	git(t, task, "read-tree", b.BaseCommit)
	before := git(t, task, "ls-files", "--stage")
	if _, err := (Git{}).ReconcileCommit(context.Background(), p); !errors.Is(err, ErrRecovery) {
		t.Fatalf("certified stale index: %v", err)
	}
	if got := git(t, task, "ls-files", "--stage"); got != before {
		t.Fatal("read-only refresh rewrote index")
	}
	if got := git(t, task, "rev-parse", "HEAD"); got != result.Commit {
		t.Fatal("refresh changed ref")
	}
	git(t, task, "read-tree", result.Commit)
	if _, err := (Git{}).ReconcileCommit(context.Background(), p); err != nil {
		t.Fatal(err)
	}
}

func TestReviewedPatchAboveFormerSixteenMiBLimitCanCommit(t *testing.T) {
	_, task, binding := testRepository(t)
	content := bytes.Repeat([]byte("large reviewed line\n"), (17<<20)/20+1)
	if err := os.WriteFile(filepath.Join(task, "a.txt"), content, 0600); err != nil {
		t.Fatal(err)
	}
	patch := gitBytes(t, task, "-c", "core.quotePath=false", "diff", "--full-index", "--no-ext-diff", "--no-textconv", "--no-renames", binding.BaseCommit, "--", ":(literal)a.txt")
	if len(patch) <= 16<<20 {
		t.Fatal("fixture below former limit")
	}
	preview, err := (Git{}).PreviewCommit(context.Background(), binding, patch, []string{"a.txt"}, "large review")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (Git{}).Commit(context.Background(), preview); err != nil {
		t.Fatal(err)
	}
	if got := git(t, task, "status", "--porcelain"); got != "" {
		t.Fatalf("dirty: %s", got)
	}
}

func TestReviewAcceptsExactStagedChangesButRejectsConflictedIndex(t *testing.T) {
	_, root, binding := testRepository(t)
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("reviewed staged change\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", "a.txt")
	patch := gitBytes(t, root, "-c", "core.quotePath=false", "diff", "--full-index", "--no-ext-diff", "--no-textconv", "--no-renames", binding.BaseCommit, "--", ":(literal)a.txt")
	before := git(t, root, "ls-files", "--stage")
	if err := (Git{}).VerifyReviewedChanges(context.Background(), binding, patch, []string{"a.txt"}); err != nil {
		t.Fatalf("exact staged review rejected: %v", err)
	}
	if got := git(t, root, "ls-files", "--stage"); got != before {
		t.Fatal("review rewrote index")
	}
	baseBlob := git(t, root, "rev-parse", binding.BaseCommit+":a.txt")
	changedBlob := git(t, root, "rev-parse", ":a.txt")
	command := exec.Command("git", "update-index", "--index-info")
	command.Dir = root
	command.Stdin = strings.NewReader("0 " + strings.Repeat("0", 40) + "\ta.txt\n100644 " + baseBlob + " 1\ta.txt\n100644 " + baseBlob + " 2\ta.txt\n100644 " + changedBlob + " 3\ta.txt\n")
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("seed conflicted index: %v %s", err, out)
	}
	before = git(t, root, "ls-files", "--stage")
	if err := (Git{}).VerifyReviewedChanges(context.Background(), binding, patch, []string{"a.txt"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicted index accepted: %v", err)
	}
	if got := git(t, root, "ls-files", "--stage"); got != before {
		t.Fatal("review changed conflict stages")
	}
}
