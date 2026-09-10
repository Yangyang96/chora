package localweb

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/gitsource"
	storecontract "github.com/Yangyang96/chora/internal/store"
	"github.com/Yangyang96/chora/internal/store/sqlite"
)

func TestTaskResourceWorkspaceManagerPreparesIndependentRepositoriesAndRestarts(t *testing.T) {
	first := newTaskResourceTestRepository(t, true)
	second := newTaskResourceTestRepository(t, false)
	fixture := newTaskResourceWorkspaceFixture(t, first, second)

	// Source checkout dirt is not Task input. The immutable commit is checked out
	// into each private resource root without copying untracked local material.
	if err := os.WriteFile(filepath.Join(first, "README.md"), []byte("dirty source\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(first, "local-secret.txt"), []byte("do not copy\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	resolver, err := newTaskResourceWorkspaceManager(fixture.db, fixture.dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := resolver.Ensure(context.Background(), fixture.task.ID()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	root, err := resolver.ResolveExecutionRoot(context.Background(), fixture.task.ID())
	if err != nil {
		t.Fatalf("ResolveExecutionRoot: %v", err)
	}
	for _, resource := range fixture.snapshot.Resources {
		repositoryRoot := filepath.Join(root, resource.WorkspaceDirectory())
		contents, err := os.ReadFile(filepath.Join(repositoryRoot, "README.md"))
		if err != nil || string(contents) != "committed\n" {
			t.Fatalf("%s README=%q err=%v", resource.RepoID, contents, err)
		}
		if _, err := os.Lstat(filepath.Join(repositoryRoot, "local-secret.txt")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("source-only file entered %s: %v", resource.RepoID, err)
		}
	}
	largeRepositoryID := fixture.repositoryIDForPath(first)
	largeInfo, err := os.Stat(filepath.Join(root, largeRepositoryID, "large.bin"))
	if err != nil || largeInfo.Size() != int64(domain.RepositoryTextBytes+1) {
		t.Fatalf("unchanged large binary size=%v err=%v", largeInfo, err)
	}
	rows, err := fixture.db.Reader().ListTaskRepositoryWorktrees(context.Background(), fixture.task.ID())
	if err != nil || len(rows) != 2 || rows[0].State != "ready" || rows[1].State != "ready" {
		t.Fatalf("worktree rows=%+v err=%v", rows, err)
	}

	// A fresh manager has no memory of preparation and must re-prove durable
	// rows, physical repository identity, and the pinned commit/tree pair.
	restarted, err := newTaskResourceWorkspaceManager(fixture.db, fixture.dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := restarted.ResolveExecutionRoot(context.Background(), fixture.task.ID()); err != nil || again != root {
		t.Fatalf("restart root=%q err=%v", again, err)
	}
}

func TestTaskResourceWorkspaceManagerCreatesStableTaskBranchAndRejectsCollision(t *testing.T) {
	t.Run("stable branch", func(t *testing.T) {
		repository := newTaskResourceTestRepository(t, false)
		fixture := newTaskResourceWorkspaceFixtureMode(t, true, repository)
		resolver, err := newTaskResourceWorkspaceManager(fixture.db, fixture.dataRoot)
		if err != nil {
			t.Fatal(err)
		}
		if err = resolver.Ensure(context.Background(), fixture.task.ID()); err != nil {
			t.Fatal(err)
		}
		resource := fixture.snapshot.Resources[0]
		root, err := resolver.ResolveExecutionRoot(context.Background(), fixture.task.ID())
		if err != nil {
			t.Fatal(err)
		}
		branch := strings.TrimSpace(runTaskWorktreeGitTest(t, filepath.Join(root, resource.WorkspaceDirectory()), "symbolic-ref", "HEAD"))
		if branch != "refs/heads/"+resource.TaskBranch {
			t.Fatalf("branch=%q want=%q", branch, "refs/heads/"+resource.TaskBranch)
		}
	})

	t.Run("unrelated ref", func(t *testing.T) {
		repository := newTaskResourceTestRepository(t, false)
		fixture := newTaskResourceWorkspaceFixtureMode(t, true, repository)
		resource := fixture.snapshot.Resources[0]
		if err := os.WriteFile(filepath.Join(repository, "later.txt"), []byte("later\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		runTaskWorktreeGitTest(t, repository, "add", "later.txt")
		runTaskWorktreeGitTest(t, repository, "-c", "user.name=Chora Test", "-c", "user.email=chora@example.test", "commit", "-m", "later")
		runTaskWorktreeGitTest(t, repository, "branch", resource.TaskBranch, "HEAD")
		before := strings.TrimSpace(runTaskWorktreeGitTest(t, repository, "rev-parse", resource.TaskBranch))
		resolver, err := newTaskResourceWorkspaceManager(fixture.db, fixture.dataRoot)
		if err != nil {
			t.Fatal(err)
		}
		if err = resolver.Ensure(context.Background(), fixture.task.ID()); err == nil {
			t.Fatal("accepted colliding Task branch")
		}
		after := strings.TrimSpace(runTaskWorktreeGitTest(t, repository, "rev-parse", resource.TaskBranch))
		if after != before {
			t.Fatalf("collision ref changed: before=%s after=%s", before, after)
		}
	})
}

func TestTaskResourceWorkspaceManagerPersistsCancellationBeforeReady(t *testing.T) {
	fixture := newTaskResourceWorkspaceFixture(t, newTaskResourceTestRepository(t, false))
	resolver, err := newTaskResourceWorkspaceManager(fixture.db, fixture.dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := resolver.Ensure(cancelled, fixture.task.ID()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Ensure cancellation=%v", err)
	}
	rows, err := fixture.db.Reader().ListTaskRepositoryWorktrees(context.Background(), fixture.task.ID())
	if err != nil || len(rows) != 1 || rows[0].State != "cancelled" || rows[0].Reason == "" {
		t.Fatalf("cancelled rows=%+v err=%v", rows, err)
	}
	if _, err := os.Lstat(filepath.Join(fixture.dataRoot, rows[0].RelativePath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled resource exists: %v", err)
	}
	if _, err := resolver.ResolveExecutionRoot(context.Background(), fixture.task.ID()); err == nil {
		t.Fatal("cancelled workspace resolved")
	}
}

func TestTaskResourceWorkspaceManagerFailsWholeVectorAndRejectsTampering(t *testing.T) {
	t.Run("second repository fails", func(t *testing.T) {
		fixture := newTaskResourceWorkspaceFixture(t, newTaskResourceTestRepository(t, false), newTaskResourceTestRepository(t, false))
		second := fixture.snapshot.Resources[1]
		if err := os.Rename(second.Checkout, second.Checkout+"-missing"); err != nil {
			t.Fatal(err)
		}
		resolver, err := newTaskResourceWorkspaceManager(fixture.db, fixture.dataRoot)
		if err != nil {
			t.Fatal(err)
		}
		if err := resolver.Ensure(context.Background(), fixture.task.ID()); err == nil {
			t.Fatal("Ensure accepted missing second repository")
		}
		rows, err := fixture.db.Reader().ListTaskRepositoryWorktrees(context.Background(), fixture.task.ID())
		if err != nil || len(rows) != 2 || rows[0].State != "failed" || rows[1].State != "failed" || rows[1].Reason == "" {
			t.Fatalf("partial failure rows=%+v err=%v", rows, err)
		}
		if _, err := os.Lstat(filepath.Join(fixture.dataRoot, rows[0].RelativePath)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("new unused first worktree retained after second failed: %v", err)
		}
		if _, err := resolver.ResolveExecutionRoot(context.Background(), fixture.task.ID()); err == nil {
			t.Fatal("partial vector resolved")
		}
	})

	t.Run("symlinked resource root", func(t *testing.T) {
		fixture := newTaskResourceWorkspaceFixture(t, newTaskResourceTestRepository(t, false))
		resolver, err := newTaskResourceWorkspaceManager(fixture.db, fixture.dataRoot)
		if err != nil {
			t.Fatal(err)
		}
		if err := resolver.Ensure(context.Background(), fixture.task.ID()); err != nil {
			t.Fatal(err)
		}
		rows, err := fixture.db.Reader().ListTaskRepositoryWorktrees(context.Background(), fixture.task.ID())
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(fixture.dataRoot, rows[0].RelativePath)
		moved := path + "-moved"
		if err := os.Rename(path, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(moved, path); err != nil {
			t.Fatal(err)
		}
		if _, err := resolver.ResolveExecutionRoot(context.Background(), fixture.task.ID()); err == nil {
			t.Fatal("symlinked resource root resolved")
		}
	})
}

type taskResourceWorkspaceFixture struct {
	db       *sqlite.Store
	dataRoot string
	task     domain.Task
	snapshot domain.TaskResourceSnapshot
}

func newTaskResourceWorkspaceFixture(t *testing.T, repositories ...string) *taskResourceWorkspaceFixture {
	return newTaskResourceWorkspaceFixtureMode(t, false, repositories...)
}

func newTaskResourceWorkspaceFixtureMode(t *testing.T, taskBranches bool, repositories ...string) *taskResourceWorkspaceFixture {
	t.Helper()
	db := openTaskWorktreeResolverStore(t)
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	room, task := insertResolverRoomWithOwnership(t, db, now, domain.RoomOwnershipProject)
	resources := make([]domain.TaskRepositoryResource, 0, len(repositories))
	records := make([]domain.RepositoryRecord, 0, len(repositories))
	for index, repository := range repositories {
		inspection, err := (gitsource.Default{}).Inspect(context.Background(), repository)
		if err != nil {
			t.Fatal(err)
		}
		record := domain.RepositoryRecord{
			ID: domain.NewRepositoryID(), Name: "repository-" + string(rune('a'+index)),
			Checkout: inspection.CanonicalPath, CommonGitDir: inspection.CommonGitDir,
			PhysicalIdentity: inspection.PhysicalIdentity, IdentitySource: "inspected", CreatedAt: now,
		}
		records = append(records, record)
		role := "reference"
		if index == 0 {
			role = "write"
		}
		resources = append(resources, domain.TaskRepositoryResource{
			RepoID: record.ID.String(), Name: record.Name, Checkout: record.Checkout, CommonGitDir: record.CommonGitDir,
			PhysicalIdentity: record.PhysicalIdentity, Role: role, BaseCommit: inspection.HeadCommit, BaseTree: inspection.RootTree, BaseRef: "HEAD",
			AssociationVersion: 1, Scope: domain.TaskRepositoryScope{Mode: "repository", MigrationChoice: "not_needed"},
			Checks: domain.TaskCheckPolicy{Mode: "none"},
		})
	}
	sort.Slice(resources, func(i, j int) bool { return resources[i].RepoID < resources[j].RepoID })
	snapshot := domain.TaskResourceSnapshot{
		SchemaVersion: domain.TaskResourceSchemaV2, ProjectID: room.ProjectID().String(),
		RoomID: room.ID().String(), SelectionSource: "room_explicit", Resources: resources,
	}
	if taskBranches {
		for index := range snapshot.Resources {
			if snapshot.Resources[index].Role == "write" {
				inspection, inspectErr := (gitsource.Default{}).Inspect(context.Background(), snapshot.Resources[index].Checkout)
				if inspectErr != nil || inspection.Branch == "" {
					t.Fatalf("inspect target branch: %v", inspectErr)
				}
				snapshot.Resources[index].BaseRef = "refs/heads/" + inspection.Branch
			}
		}
		var bindErr error
		snapshot, bindErr = domain.BindTaskResourceBranches(snapshot, task.ID(), task.Title())
		if bindErr != nil {
			t.Fatal(bindErr)
		}
	} else {
		snapshot.TaskID = task.ID().String()
	}
	canonical, digest, err := snapshot.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(context.Background(), func(tx storecontract.WriteTx) error {
		for _, record := range records {
			if err := tx.InsertRepository(context.Background(), record); err != nil {
				return err
			}
			association := domain.ProjectRepository{ProjectID: room.ProjectID(), Repository: record, State: "active", Version: 1, AddedAt: now, UpdatedAt: now}
			if err := tx.SaveProjectRepository(context.Background(), 0, association); err != nil {
				return err
			}
		}
		return tx.InsertTaskResourceSnapshot(context.Background(), storecontract.TaskResourceRecord{TaskID: task.ID(), CanonicalJSON: canonical, Digest: digest, CreatedAt: now})
	}); err != nil {
		t.Fatal(err)
	}
	dataRoot := t.TempDir()
	dataRoot, err = filepath.EvalSymlinks(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	return &taskResourceWorkspaceFixture{db: db, dataRoot: dataRoot, task: task, snapshot: snapshot}
}

func (fixture *taskResourceWorkspaceFixture) repositoryIDForPath(path string) string {
	for _, resource := range fixture.snapshot.Resources {
		if resource.Checkout == path {
			return resource.RepoID
		}
	}
	return ""
}

func newTaskResourceTestRepository(t *testing.T, large bool) string {
	t.Helper()
	root, _, _ := newTaskWorktreeTestRepository(t)
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("committed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if large {
		file, err := os.OpenFile(filepath.Join(root, "large.bin"), os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(int64(domain.RepositoryTextBytes + 1)); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	runTaskWorktreeGitTest(t, root, "add", ".")
	runTaskWorktreeGitTest(t, root, "-c", "user.name=Chora Test", "-c", "user.email=chora@example.test", "commit", "-m", "resource fixture")
	return root
}

func taskResourceTestTargetRef(t *testing.T, root string) string {
	t.Helper()
	branch := strings.TrimSpace(runTaskWorktreeGitTest(t, root, "symbolic-ref", "--short", "HEAD"))
	return "refs/heads/" + branch
}

var _ app.TaskResourceWorkspaceResolver = (*taskResourceWorkspaceManager)(nil)

// Cancellation happens after a real Git worktree is persisted ready, while the
// resource vector is still preparing. Existing resources from earlier Ensure
// calls must survive; only this call's new, unstarted resources are removed.
func TestTaskResourceWorkspaceCancellationDuringPreparationCleansOnlyNew(t *testing.T) {
	fixture := newTaskResourceWorkspaceFixture(t, newTaskResourceTestRepository(t, false), newTaskResourceTestRepository(t, false))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wrapped := &cancelAfterReadyResourceStore{Store: fixture.db, cancel: cancel}
	resolver, err := newTaskResourceWorkspaceManager(wrapped, fixture.dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := resolver.Ensure(ctx, fixture.task.ID()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Ensure=%v", err)
	}
	rows, err := fixture.db.Reader().ListTaskRepositoryWorktrees(context.Background(), fixture.task.ID())
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	for _, row := range rows {
		if row.State != "cancelled" {
			t.Fatalf("cancel state=%+v", row)
		}
		if _, err := os.Lstat(filepath.Join(fixture.dataRoot, row.RelativePath)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("cancelled new path retained: %v", err)
		}
	}
	if _, err := resolver.ResolveExecutionRoot(context.Background(), fixture.task.ID()); err == nil {
		t.Fatal("cancelled vector resolved")
	}
	// Retry uses the same frozen vector after successful cleanup.
	if err := resolver.Ensure(context.Background(), fixture.task.ID()); err != nil {
		t.Fatalf("retry after cancellation=%v", err)
	}
	root, err := resolver.ResolveExecutionRoot(context.Background(), fixture.task.ID())
	if err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(root, fixture.snapshot.Resources[0].WorkspaceDirectory(), "user-work.txt")
	if err := os.WriteFile(keep, []byte("preserve existing task work"), 0600); err != nil {
		t.Fatal(err)
	}
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if err := resolver.Ensure(cancelled, fixture.task.ID()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel existing=%v", err)
	}
	if data, err := os.ReadFile(keep); err != nil || string(data) != "preserve existing task work" {
		t.Fatalf("existing resource lost: %q %v", data, err)
	}
}

type cancelAfterReadyResourceStore struct {
	storecontract.Store
	cancel context.CancelFunc
}

func (s *cancelAfterReadyResourceStore) WithinWriteTx(ctx context.Context, fn func(storecontract.WriteTx) error) error {
	ready := false
	err := s.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		return fn(cancelAfterReadyResourceTx{WriteTx: tx, ready: &ready})
	})
	if err == nil && ready && s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	return err
}

type cancelAfterReadyResourceTx struct {
	storecontract.WriteTx
	ready *bool
}

func (tx cancelAfterReadyResourceTx) SaveTaskRepositoryWorktree(ctx context.Context, version uint64, row domain.TaskRepositoryWorktree) error {
	err := tx.WriteTx.SaveTaskRepositoryWorktree(ctx, version, row)
	if err == nil && row.State == "ready" {
		*tx.ready = true
	}
	return err
}

func TestTaskResourceWorkspaceCancellationWhileGitCreatesWorktree(t *testing.T) {
	fixture := newTaskResourceWorkspaceFixture(t, newTaskResourceTestRepository(t, true))
	resolver, err := newTaskResourceWorkspaceManager(fixture.db, fixture.dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- resolver.Ensure(ctx, fixture.task.ID()) }()
	path := filepath.Join(fixture.dataRoot, "task-workspaces", fixture.task.ID().String(), fixture.snapshot.Resources[0].RepoID)
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
waitForCreation:
	for {
		select {
		case err := <-done:
			t.Fatalf("preparation completed before cancellation point: %v", err)
		case <-deadline.C:
			t.Fatal("Git worktree creation did not begin")
		case <-ticker.C:
			if _, err := os.Lstat(filepath.Join(path, ".git")); err == nil {
				break waitForCreation
			}
		}
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("creation cancellation=%v", err)
	}
	rows, err := fixture.db.Reader().ListTaskRepositoryWorktrees(context.Background(), fixture.task.ID())
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	row := rows[0]
	if row.State == "cancelled" {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("cancelled resource not removed: %v", err)
		}
	} else if row.State != "recovery_required" || row.Reason == "" {
		t.Fatalf("uncertain partial creation has no durable recovery record: %+v", row)
	}
	if _, err := resolver.ResolveExecutionRoot(context.Background(), fixture.task.ID()); err == nil {
		t.Fatal("interrupted creation resolved for execution")
	}
	data, err := os.ReadFile(filepath.Join(fixture.snapshot.Resources[0].Checkout, "README.md"))
	if err != nil || string(data) != "committed\n" {
		t.Fatalf("original changed: %q %v", data, err)
	}
}

func TestTaskFeatureWorktreesCanShareFrozenBaseWithoutSharingEdits(t *testing.T) {
	repository := newTaskResourceTestRepository(t, false)
	first := newTaskResourceWorkspaceFixtureMode(t, true, repository)
	second := newTaskResourceWorkspaceFixtureMode(t, true, repository)
	a, b := first.snapshot.Resources[0], second.snapshot.Resources[0]
	if a.BaseCommit != b.BaseCommit || a.TaskBranch == b.TaskBranch {
		t.Fatal("parallel Tasks must share base with distinct branches")
	}
	firstManager, err := newTaskResourceWorkspaceManager(first.db, first.dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	secondManager, err := newTaskResourceWorkspaceManager(second.db, second.dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err = firstManager.Ensure(context.Background(), first.task.ID()); err != nil {
		t.Fatal(err)
	}
	if err = secondManager.Ensure(context.Background(), second.task.ID()); err != nil {
		t.Fatal(err)
	}
	firstRoot, err := firstManager.ResolveExecutionRoot(context.Background(), first.task.ID())
	if err != nil {
		t.Fatal(err)
	}
	secondRoot, err := secondManager.ResolveExecutionRoot(context.Background(), second.task.ID())
	if err != nil {
		t.Fatal(err)
	}
	firstPath, secondPath := filepath.Join(firstRoot, a.WorkspaceDirectory()), filepath.Join(secondRoot, b.WorkspaceDirectory())
	if err = os.WriteFile(filepath.Join(firstPath, "README.md"), []byte("first task pending\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{repository, secondPath} {
		data, err := os.ReadFile(filepath.Join(p, "README.md"))
		if err != nil || string(data) != "committed\n" {
			t.Fatalf("other workspace modified %s: %q %v", p, data, err)
		}
	}
	for _, p := range []string{firstPath, secondPath} {
		if strings.TrimSpace(runTaskWorktreeGitTest(t, p, "rev-parse", "HEAD")) != a.BaseCommit {
			t.Fatal("parallel development moved base")
		}
	}
}
