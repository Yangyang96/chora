package localweb

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/Yangyang96/chora/internal/domain"
)

func TestShortWorktreePathsRestartAndRejectForeignTaskRoot(t *testing.T) {
	for _, scenario := range []string{"restart", "foreign owner", "unowned directory", "symlink owner"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newTaskResourceWorkspaceFixtureMode(t, true, newTaskResourceTestRepository(t, false), newTaskResourceTestRepository(t, false))
			resolver, err := newTaskResourceWorkspaceManager(fixture.db, fixture.dataRoot)
			if err != nil {
				t.Fatal(err)
			}
			resource := fixture.snapshot.Resources[0]
			root := filepath.Join(fixture.dataRoot, "task-workspaces", resource.TaskWorkspaceDirectory(fixture.task.ID()))
			if !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(filepath.Base(root)) {
				t.Fatalf("long Task root: %s", root)
			}
			if scenario != "restart" {
				if err := os.MkdirAll(root, 0o700); err != nil {
					t.Fatal(err)
				}
				marker := filepath.Join(root, ".chora-task")
				switch scenario {
				case "foreign owner":
					err = os.WriteFile(marker, []byte(domain.NewTaskID().String()), 0o600)
				case "symlink owner":
					target := filepath.Join(fixture.dataRoot, "foreign-marker")
					if err := os.WriteFile(target, []byte(fixture.task.ID().String()), 0o600); err != nil {
						t.Fatal(err)
					}
					err = os.Symlink(target, marker)
				}
				if err != nil {
					t.Fatal(err)
				}
				if err := resolver.Ensure(context.Background(), fixture.task.ID()); err == nil {
					t.Fatal("adopted unproven Task root")
				}
				if _, err := os.Lstat(filepath.Join(root, resource.WorkspaceDirectory())); !os.IsNotExist(err) {
					t.Fatalf("created worktree under foreign root: %v", err)
				}
				return
			}
			if err := resolver.Ensure(context.Background(), fixture.task.ID()); err != nil {
				t.Fatal(err)
			}
			rows, err := fixture.db.Reader().ListTaskRepositoryWorktrees(context.Background(), fixture.task.ID())
			if err != nil || len(rows) != 2 {
				t.Fatalf("rows=%v err=%v", rows, err)
			}
			for _, row := range rows {
				if !regexp.MustCompile(`^task-workspaces/[0-9a-f]{12}/[0-9a-f]{12}$`).MatchString(row.RelativePath) {
					t.Fatalf("long worktree path: %s", row.RelativePath)
				}
				content, err := os.ReadFile(filepath.Join(fixture.dataRoot, row.RelativePath, "README.md"))
				if err != nil || string(content) != "committed\n" {
					t.Fatalf("wrong checkout: %q %v", content, err)
				}
			}
			restarted, err := newTaskResourceWorkspaceManager(fixture.db, fixture.dataRoot)
			if err != nil {
				t.Fatal(err)
			}
			if actual, err := restarted.ResolveExecutionRoot(context.Background(), fixture.task.ID()); err != nil || actual != root {
				t.Fatalf("restart root=%s err=%v", actual, err)
			}
			if err := restarted.Ensure(context.Background(), fixture.task.ID()); err != nil {
				t.Fatal(err)
			}
		})
	}
}
