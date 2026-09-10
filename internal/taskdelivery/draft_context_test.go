package taskdelivery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDraftContextUsesImmutableConventionsWithoutExecutingOrFollowingSymlinks(t *testing.T) {
	_, task, b := testRepository(t)
	marker := filepath.Join(t.TempDir(), "executed")
	os.WriteFile(filepath.Join(task, "commitlint.config.js"), []byte("require('fs').writeFileSync("+"`"+marker+"`"+", 'bad')"), 0600)
	os.WriteFile(filepath.Join(task, "AGENTS.md"), []byte("Use a component prefix."), 0600)
	os.MkdirAll(filepath.Join(task, ".github/PULL_REQUEST_TEMPLATE"), 0700)
	os.WriteFile(filepath.Join(task, ".github/PULL_REQUEST_TEMPLATE/change.md"), []byte("## Change\n\n## Verification\n- [ ] Human review"), 0600)
	external := filepath.Join(t.TempDir(), "private")
	os.WriteFile(external, []byte("must not read"), 0600)
	os.Symlink(external, filepath.Join(task, "CONTRIBUTING.md"))
	git(t, task, "add", ".")
	git(t, task, "commit", "-m", "docs: record contribution policy")
	b.BaseCommit = git(t, task, "rev-parse", "HEAD")
	b.BaseTree = git(t, task, "rev-parse", "HEAD^{tree}")
	os.WriteFile(filepath.Join(task, "AGENTS.md"), []byte("unreviewed different rule"), 0600)
	before := git(t, task, "status", "--porcelain")
	ctx, err := (Git{}).ReadDraftContext(context.Background(), b, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Conventions["AGENTS.md"] != "Use a component prefix." || strings.Contains(strings.Join(ctx.Warnings, " "), "must not read") {
		t.Fatalf("wrong sources: %#v", ctx)
	}
	if _, ok := ctx.Conventions["CONTRIBUTING.md"]; ok {
		t.Fatal("followed convention symlink")
	}
	if len(ctx.Templates) != 1 || !strings.Contains(ctx.RecentCommits, "record contribution policy") {
		t.Fatalf("missing template/history: %#v", ctx)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("executed repository configuration")
	}
	if git(t, task, "status", "--porcelain") != before {
		t.Fatal("modified worktree/index")
	}
}
func TestDraftContextPRCoversAllCommitsAndRejectsUnreviewedRange(t *testing.T) {
	_, task, b := testRepository(t)
	os.WriteFile(filepath.Join(task, "a.txt"), []byte("first change\n"), 0600)
	git(t, task, "add", "a.txt")
	git(t, task, "commit", "-m", "fix: first change")
	first := git(t, task, "rev-parse", "HEAD")
	os.WriteFile(filepath.Join(task, "b.txt"), []byte("second change\n"), 0600)
	git(t, task, "add", "b.txt")
	git(t, task, "commit", "-m", "feat: second change")
	head, tree := git(t, task, "rev-parse", "HEAD"), git(t, task, "rev-parse", "HEAD^{tree}")
	v, err := (Git{}).ReadDraftContext(context.Background(), b, head, b.BaseCommit, tree)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(v.Diff, "+first change") || !strings.Contains(v.Diff, "+second change") || v.MergeBase != b.BaseCommit {
		t.Fatalf("incomplete PR range: %#v", v)
	}
	b.BaseCommit = first
	b.BaseTree = git(t, task, "rev-parse", first+"^{tree}")
	if _, err := (Git{}).ReadDraftContext(context.Background(), b, head, v.Base, tree); !errors.Is(err, ErrConflict) {
		t.Fatalf("accepted extra changes: %v", err)
	}
	if _, err := (Git{}).ReadDraftContext(context.Background(), b, first, first, b.BaseTree); !errors.Is(err, ErrConflict) {
		t.Fatalf("accepted stale head: %v", err)
	}
}
