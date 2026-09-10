package localweb

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTaskBranchSameBaseCollisionIsNotAdopted(t *testing.T) {
	repository := newTaskResourceTestRepository(t, false)
	fixture := newTaskResourceWorkspaceFixtureMode(t, true, repository)
	r := fixture.snapshot.Resources[0]
	runTaskWorktreeGitTest(t, repository, "branch", r.TaskBranch, r.BaseCommit)
	before := runTaskWorktreeGitTest(t, repository, "reflog", "show", r.TaskBranch)
	resolver, e := newTaskResourceWorkspaceManager(fixture.db, fixture.dataRoot)
	if e != nil {
		t.Fatal(e)
	}
	if e = resolver.Ensure(context.Background(), fixture.task.ID()); e == nil {
		t.Fatal("adopted unrelated branch at identical base")
	}
	if after := runTaskWorktreeGitTest(t, repository, "reflog", "show", r.TaskBranch); after != before {
		t.Fatal("changed unrelated ref or reflog")
	}
	if _, e = os.Stat(filepath.Join(fixture.dataRoot, "task-workspaces", r.TaskWorkspaceDirectory(fixture.task.ID()), r.WorkspaceDirectory())); !os.IsNotExist(e) {
		t.Fatalf("created worktree for unrelated ref: %v", e)
	}
}
func TestTaskBranchCreationWitnessSurvivesCrashBeforeWorktree(t *testing.T) {
	repository := newTaskResourceTestRepository(t, false)
	fixture := newTaskResourceWorkspaceFixtureMode(t, true, repository)
	resolver, e := newTaskResourceWorkspaceManager(fixture.db, fixture.dataRoot)
	if e != nil {
		t.Fatal(e)
	}
	manager := resolver.(*taskResourceWorkspaceManager)
	rows, e := manager.planRows(context.Background(), fixture.task.ID(), fixture.snapshot)
	if e != nil {
		t.Fatal(e)
	}
	r := fixture.snapshot.Resources[0]
	if e = manager.claimTaskResourceBranch(context.Background(), r, rows[0]); e != nil {
		t.Fatal(e)
	}
	// No worktree exists yet: replace the manager to simulate the crash boundary.
	restarted, e := newTaskResourceWorkspaceManager(fixture.db, fixture.dataRoot)
	if e != nil {
		t.Fatal(e)
	}
	if e = restarted.Ensure(context.Background(), fixture.task.ID()); e != nil {
		t.Fatal(e)
	}
	root, e := restarted.ResolveExecutionRoot(context.Background(), fixture.task.ID())
	if e != nil {
		t.Fatal(e)
	}
	if got := strings.TrimSpace(runTaskWorktreeGitTest(t, filepath.Join(root, r.WorkspaceDirectory()), "symbolic-ref", "--short", "HEAD")); got != r.TaskBranch {
		t.Fatalf("branch=%s", got)
	}
}

func TestTaskBranchPreparationCleanupRetainsUncommittedContent(t *testing.T) {
	repository := newTaskResourceTestRepository(t, false)
	fixture := newTaskResourceWorkspaceFixtureMode(t, true, repository)
	resolver, e := newTaskResourceWorkspaceManager(fixture.db, fixture.dataRoot)
	if e != nil {
		t.Fatal(e)
	}
	if e = resolver.Ensure(context.Background(), fixture.task.ID()); e != nil {
		t.Fatal(e)
	}
	root, e := resolver.ResolveExecutionRoot(context.Background(), fixture.task.ID())
	if e != nil {
		t.Fatal(e)
	}
	resource := fixture.snapshot.Resources[0]
	file := filepath.Join(root, resource.WorkspaceDirectory(), "independent.txt")
	if e = os.WriteFile(file, []byte("retain independent unsaved contents\n"), 0600); e != nil {
		t.Fatal(e)
	}
	rows, e := fixture.db.Reader().ListTaskRepositoryWorktrees(context.Background(), fixture.task.ID())
	if e != nil {
		t.Fatal(e)
	}
	manager := resolver.(*taskResourceWorkspaceManager)
	if e = manager.cleanupUnstartedResources(context.Background(), fixture.snapshot.Resources, rows, []bool{true}); e == nil {
		t.Fatal("cleanup removed independent contents")
	}
	if b, e := os.ReadFile(file); e != nil || string(b) != "retain independent unsaved contents\n" {
		t.Fatalf("lost uncommitted contents %q %v", b, e)
	}
}

func TestTargetRefFreezeUsesTreeOfObservedCommitWhenBranchAdvances(t *testing.T) {
	repository := newTaskResourceTestRepository(t, false)
	ref := strings.TrimSpace(runTaskWorktreeGitTest(t, repository, "symbolic-ref", "HEAD"))
	base := strings.TrimSpace(runTaskWorktreeGitTest(t, repository, "rev-parse", "HEAD"))
	baseTree := strings.TrimSpace(runTaskWorktreeGitTest(t, repository, "rev-parse", "HEAD^{tree}"))
	reads := 0
	commit, tree, e := resolveLocalTargetRefWithOutput(context.Background(), repository, ref, func(ctx context.Context, root string, limit int64, args ...string) ([]byte, error) {
		raw, e := gitTargetOutputBounded(ctx, root, limit, args...)
		if e != nil {
			return raw, e
		}
		reads++
		if reads == 1 {
			if e = os.WriteFile(filepath.Join(repository, "moved.txt"), []byte("different tree\n"), 0600); e != nil {
				t.Fatal(e)
			}
			runTaskWorktreeGitTest(t, repository, "add", "moved.txt")
			runTaskWorktreeGitTest(t, repository, "-c", "user.name=Chora Test", "-c", "user.email=test@chora.invalid", "commit", "-m", "advance between commit and tree reads")
		}
		return raw, e
	})
	if e != nil || commit != base || tree != baseTree {
		t.Fatalf("mixed target snapshot commit=%s tree=%s error=%v", commit, tree, e)
	}
	if current := strings.TrimSpace(runTaskWorktreeGitTest(t, repository, "rev-parse", "HEAD")); current == base {
		t.Fatal("test failed to advance branch")
	}
}
