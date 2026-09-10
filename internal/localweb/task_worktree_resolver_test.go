package localweb

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
	"github.com/Yangyang96/chora/internal/store/sqlite"
)

func TestTaskWorktreeRepositoryNameSanitizesToSafeIdentity(t *testing.T) {
	tests := map[string]string{
		"My Project!":  "my-project",
		"  Chora  ":    "chora",
		"my.repo.git":  "my-repo-git",
		"../traversal": "traversal",
		"---":          "repository",
		"":             "repository",
	}
	for input, want := range tests {
		if got := taskWorktreeRepositoryName(input); got != want {
			t.Fatalf("taskWorktreeRepositoryName(%q)=%q want %q", input, got, want)
		}
	}
}

func TestTaskWorktreeResolverPlansAndEnsuresPerRoomRepository(t *testing.T) {
	ctx := context.Background()
	db := openTaskWorktreeResolverStore(t)
	defer db.Close()

	anchor, _, revision := newTaskWorktreeTestRepository(t)
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	room, task := insertResolverRoomAndTask(t, db, anchor, revision, "My Project!", now)

	resolver := newTaskWorktreeResolver(db.Reader(), nil)

	planned, err := resolver.Plan(ctx, task, now)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if planned.RepositoryIdentity() != "my-project" {
		t.Fatalf("repository identity=%q want %q", planned.RepositoryIdentity(), "my-project")
	}
	if planned.PinnedBaseRevision() != revision {
		t.Fatalf("pinned revision=%q want %q", planned.PinnedBaseRevision(), revision)
	}
	if planned.RelativeLocator() == "" || planned.ConfiguredRootFingerprint() == ([32]byte{}) {
		t.Fatalf("incomplete binding: %#v", planned)
	}

	if err := resolver.Ensure(ctx, planned); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	wantPath := filepath.Join(filepath.Dir(anchor), filepath.FromSlash(planned.RelativeLocator()))
	if _, err := os.Lstat(wantPath); err != nil {
		t.Fatalf("worktree was not provisioned at %s: %v", wantPath, err)
	}

	// A second room bound to the same anchor shares one cached manager.
	secondTask, err := domain.NewTask(domain.NewTaskID(), room.ID(), "Second", "goal", mustCriteria(t))
	if err != nil {
		t.Fatal(err)
	}
	if again, err := resolver.Plan(ctx, secondTask, now); err != nil || again.RepositoryIdentity() != "my-project" {
		t.Fatalf("second Plan identity=%q err=%v", again.RepositoryIdentity(), err)
	}
}

func TestTaskWorktreeResolverResolvesExecutionRootForBoundRoom(t *testing.T) {
	ctx := context.Background()
	db := openTaskWorktreeResolverStore(t)
	defer db.Close()

	anchor, _, revision := newTaskWorktreeTestRepository(t)
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	room, task := insertResolverRoomAndTask(t, db, anchor, revision, "My Project!", now)

	resolver := newTaskWorktreeResolver(db.Reader(), nil)
	planned, err := resolver.Plan(ctx, task, now)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		return tx.InsertTaskWorktreeBinding(ctx, planned)
	}); err != nil {
		t.Fatalf("persist binding: %v", err)
	}
	if err := resolver.Ensure(ctx, planned); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	ready, err := planned.Ready(now)
	if err != nil {
		t.Fatalf("Ready: %v", err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		return tx.SaveTaskWorktreeBindingCAS(ctx, planned.Version(), ready)
	}); err != nil {
		t.Fatalf("persist ready binding: %v", err)
	}
	root, err := resolver.ResolveExecutionRoot(ctx, task)
	if err != nil {
		t.Fatalf("ResolveExecutionRoot: %v", err)
	}
	want := filepath.Join(filepath.Dir(anchor), filepath.FromSlash(planned.RelativeLocator()))
	if root != want {
		t.Fatalf("execution root=%q want %q", root, want)
	}
	// An unbound Room keeps the charter's logical root (empty result).
	_, unboundTask := insertResolverRoomWithoutBinding(t, db, now)
	empty, err := resolver.ResolveExecutionRoot(ctx, unboundTask)
	if err != nil {
		t.Fatalf("ResolveExecutionRoot (unbound): %v", err)
	}
	if empty != "" {
		t.Fatalf("unbound execution root=%q want empty", empty)
	}
	_ = room
}

func TestTaskWorktreeResolverFallsBackToGlobalManagerWithoutBinding(t *testing.T) {
	ctx := context.Background()
	db := openTaskWorktreeResolverStore(t)
	defer db.Close()

	fallback, _, _, _ := newTaskWorktreeTestManager(t)
	resolver := newTaskWorktreeResolver(db.Reader(), fallback)

	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	_, task := insertResolverRoomWithoutBinding(t, db, now)

	planned, err := resolver.Plan(ctx, task, now)
	if err != nil {
		t.Fatalf("Plan (fallback): %v", err)
	}
	if planned.RepositoryIdentity() != "chora" {
		t.Fatalf("fallback repository identity=%q want %q", planned.RepositoryIdentity(), "chora")
	}
	if err := resolver.Ensure(ctx, planned); err != nil {
		t.Fatalf("Ensure (fallback): %v", err)
	}
	// Missing ownership is not proof of legacy, even with a ready fallback.
	_, unknown := insertResolverRoomWithOwnership(t, db, now, domain.RoomOwnershipUnclassified)
	if _, err := resolver.Plan(ctx, unknown, now); !errors.Is(err, app.ErrTaskWorktreeUnavailable) {
		t.Fatalf("unclassified fallback: %v", err)
	}
	if _, err := resolver.ResolveExecutionRoot(ctx, unknown); !errors.Is(err, app.ErrTaskWorktreeUnavailable) {
		t.Fatalf("unclassified execution root: %v", err)
	}
}

func TestTaskWorktreeResolverFailsClosedWithoutAnyManager(t *testing.T) {
	ctx := context.Background()
	db := openTaskWorktreeResolverStore(t)
	defer db.Close()

	resolver := newTaskWorktreeResolver(db.Reader(), nil)
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	_, task := insertResolverRoomWithoutBinding(t, db, now)

	if _, err := resolver.Plan(ctx, task, now); !errors.Is(err, app.ErrTaskWorktreeUnavailable) {
		t.Fatalf("Plan err=%v, want %v", err, app.ErrTaskWorktreeUnavailable)
	}
}

func TestTaskWorktreeResolverRejectsNonRepositoryAnchor(t *testing.T) {
	ctx := context.Background()
	db := openTaskWorktreeResolverStore(t)
	defer db.Close()

	notGit := t.TempDir()
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	_, task := insertResolverRoomAndTask(t, db, notGit, "0123456789abcdef0123456789abcdef01234567", "Not Git", now)

	resolver := newTaskWorktreeResolver(db.Reader(), nil)
	if _, err := resolver.Plan(ctx, task, now); err == nil {
		t.Fatal("Plan accepted a non-repository anchor")
	}
}

func openTaskWorktreeResolverStore(t *testing.T) *sqlite.Store {
	t.Helper()
	dbRoot := t.TempDir()
	if err := os.Chmod(dbRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sqlite.Open(context.Background(), filepath.Join(dbRoot, "chora.db"))
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func insertResolverRoomAndTask(t *testing.T, db *sqlite.Store, anchor, revision, name string, now time.Time) (domain.Room, domain.Task) {
	t.Helper()
	room, task := insertResolverRoomWithOwnership(t, db, now, domain.RoomOwnershipProject)
	tree := revision // Invalid-anchor fixtures deliberately have no Git tree.
	if raw, err := taskWorktreeGitOutput(context.Background(), anchor, "rev-parse", revision+"^{tree}"); err == nil {
		tree = strings.TrimSpace(string(raw))
	}
	binding, err := domain.NewRepositoryBinding(domain.RepositoryBindingParams{
		RoomID: room.ID(), Name: name, LocalLocator: anchor, SourceKind: domain.RepositorySourceKindOpen,
		AdmittedBase: revision, BaseIdentity: "sha:" + revision + ":tree:" + tree,
		TargetWorktree: anchor, DirtyAdmitted: false, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(context.Background(), func(tx storecontract.WriteTx) error {
		return tx.InsertRepositoryBinding(context.Background(), binding)
	}); err != nil {
		t.Fatal(err)
	}
	room, err = db.Reader().GetRoom(context.Background(), room.ID())
	if err != nil {
		t.Fatal(err)
	}
	return room, task
}

func insertResolverRoomWithoutBinding(t *testing.T, db *sqlite.Store, now time.Time) (domain.Room, domain.Task) {
	return insertResolverRoomWithOwnership(t, db, now, domain.RoomOwnershipLegacyStandalone)
}

func insertResolverRoomWithOwnership(t *testing.T, db *sqlite.Store, now time.Time, ownership domain.RoomOwnershipKind) (domain.Room, domain.Task) {
	t.Helper()
	roomID := domain.NewRoomID()
	var project domain.Project
	if ownership == domain.RoomOwnershipProject {
		var err error
		project, err = domain.NewProject(domain.ProjectParams{ID: domain.NewProjectID(), Name: "Resolver project", DefaultRoomID: roomID, CreatedAt: now, UpdatedAt: now})
		if err != nil {
			t.Fatal(err)
		}
	}
	room, err := domain.NewRoom(domain.RoomParams{ID: roomID, ProjectID: project.ID(), OwnershipKind: ownership, Name: "room", Description: "test", WorkspaceRoot: t.TempDir(), CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	task, err := domain.NewTask(domain.NewTaskID(), room.ID(), "Task", "goal", mustCriteria(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(context.Background(), func(tx storecontract.WriteTx) error {
		if project.ID().Valid() {
			if err := tx.InsertProject(context.Background(), project); err != nil {
				return err
			}
		}
		if err := tx.InsertRoom(context.Background(), room); err != nil {
			return err
		}
		return tx.InsertTask(context.Background(), task)
	}); err != nil {
		t.Fatal(err)
	}
	return room, task
}

func mustCriteria(t *testing.T) []domain.AcceptanceCriterion {
	t.Helper()
	criterion, err := domain.NewAcceptanceCriterion(domain.NewCriterionID(), "works", "")
	if err != nil {
		t.Fatal(err)
	}
	return []domain.AcceptanceCriterion{criterion}
}
