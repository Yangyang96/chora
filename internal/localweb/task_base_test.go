package localweb

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/pidiscovery"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func TestSuccessiveTaskBasesPreserveHistoryAndPendingPatchConflicts(t *testing.T) {
	t.Run("same Room P2", func(t *testing.T) { verifySuccessiveTaskBases(t, false) })
	t.Run("shared Project across Rooms P2A", func(t *testing.T) { verifySuccessiveTaskBases(t, true) })
}

func verifySuccessiveTaskBases(t *testing.T, crossRoom bool) {
	ctx := context.Background()
	db := openTaskWorktreeResolverStore(t)
	defer db.Close()
	root, _, firstRevision := newTaskWorktreeTestRepository(t)
	now := time.Now().UTC()
	room, first := insertResolverRoomAndTask(t, db, root, firstRevision, "Successive", now)
	nextRoom := room
	if crossRoom {
		service := app.NewService(app.Dependencies{Store: db, Authorizer: localAuthorizer{}})
		project, err := service.GetProject(ctx, room.ID())
		if err != nil {
			t.Fatal(err)
		}
		result, err := service.CreateProjectRoom(ctx, app.CreateProjectRoomRequest{CommandMeta: app.CommandMeta{ActorID: "human:local", SessionID: "p2a-test", IdempotencyKey: "second-topic"}, ProjectID: project.Project.ID(), Name: "Separate topic", Description: "Independent Room context"})
		if err != nil {
			t.Fatal(err)
		}
		nextRoom = result.Room
		if nextRoom.ID() == room.ID() || nextRoom.ProjectID() != project.Project.ID() {
			t.Fatal("invalid topic ownership")
		}
	}

	resolver := newTaskWorktreeResolver(db.Reader(), nil)
	registerPatchScope(t, db, first, root, firstRevision, []string{"README.md"}, nil, now)
	firstRoot := persistReadyResolverWorktree(t, db, resolver, first, now)
	firstBase, err := db.Reader().GetTaskWorktreeBinding(ctx, first.ID())
	if err != nil {
		t.Fatal(err)
	}
	if firstBase.PinnedBaseTree() == "" || firstBase.StartPolicy() != domain.TaskStartPolicyCurrentHEAD {
		t.Fatal("missing frozen base")
	}
	newTask := func(title string) domain.Task {
		task, err := domain.NewTask(domain.NewTaskID(), nextRoom.ID(), title, "goal", mustCriteria(t))
		if err != nil {
			t.Fatal(err)
		}
		if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.InsertTask(ctx, task) }); err != nil {
			t.Fatal(err)
		}
		return task
	}
	// Two pending results start at the same base; one Apply invalidates the other.
	pending := newTask("Pending old base")
	registerPatchScope(t, db, pending, root, firstRevision, []string{"README.md"}, nil, now)
	pendingRoot := persistReadyResolverWorktree(t, db, resolver, pending, now)
	patchStore, err := newLocalConnectedPatchStore(filepath.Join(t.TempDir(), "patches"), db.Reader(), resolver)
	if err != nil {
		t.Fatal(err)
	}
	patch := func(task domain.Task, worktree, text string) app.PatchTargetRequest {
		if err := os.WriteFile(filepath.Join(worktree, "README.md"), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		artifact, err := patchStore.MaterializeReviewPatch(ctx, app.ReviewPatchMaterializationRequest{RunID: domain.NewRunID(), TaskID: task.ID(), AttemptID: domain.NewAttemptID(), WorkingRoot: worktree})
		if err != nil {
			t.Fatal(err)
		}
		raw, err := patchStore.ReadReviewPatch(ctx, artifact.Locator)
		if err != nil {
			t.Fatal(err)
		}
		return app.PatchTargetRequest{TaskID: task.ID(), Raw: raw, PatchDigest: sha256.Sum256(raw), AffectedPaths: []string{"README.md"}}
	}
	firstPatch := patch(first, firstRoot, "first applied\n")
	pendingPatch := patch(pending, pendingRoot, "competing change\n")
	target := &repositoryBoundGitPatchTarget{reader: db.Reader()}
	pendingInspection, err := target.Inspect(ctx, pendingPatch)
	if err != nil || !pendingInspection.Applicable {
		t.Fatal(err)
	}
	inspection, err := target.Inspect(ctx, firstPatch)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := target.Apply(ctx, firstPatch, inspection); err != nil {
		t.Fatal(err)
	}
	if _, err := target.Apply(ctx, pendingPatch, pendingInspection); !errors.Is(err, app.ErrPatchTargetConflict) {
		t.Fatalf("stale pending patch accepted: %v", err)
	}
	// Crash-after-write reconciliation recognizes the exact earlier patch.
	firstPatch.PriorPreStateDigest = inspection.StateDigest
	replay, err := target.Inspect(ctx, firstPatch)
	if err != nil || !replay.AlreadyApplied {
		t.Fatalf("reconcile: %+v %v", replay, err)
	}
	runTaskWorktreeGitTest(t, root, "add", "README.md")
	runTaskWorktreeGitTest(t, root, "-c", "user.name=Chora Test", "-c", "user.email=chora@example.test", "commit", "-m", "first applied")
	secondRevision, _, _, err := currentTaskBase(ctx, root)
	if err != nil || secondRevision == firstRevision {
		t.Fatal(err)
	}
	// Dirty original contents and untracked files are preserved, not copied.
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("user dirty\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "private-untracked.txt"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	second := newTask("New committed base")
	registerPatchScope(t, db, second, root, secondRevision, []string{"README.md"}, nil, now)
	secondRoot := persistReadyResolverWorktree(t, db, resolver, second, now)
	secondBase, err := db.Reader().GetTaskWorktreeBinding(ctx, second.ID())
	if err != nil || secondBase.PinnedBaseRevision() != secondRevision {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(secondRoot, "README.md")); string(got) != "first applied\n" {
		t.Fatalf("dirty copied: %s", got)
	}
	if _, err := os.Stat(filepath.Join(secondRoot, "private-untracked.txt")); !os.IsNotExist(err) {
		t.Fatal("untracked copied")
	}
	secondPatch := patch(second, secondRoot, "second applied\n")
	if i, e := target.Inspect(ctx, secondPatch); e == nil && i.Applicable {
		t.Fatal("dirty target accepted")
	}
	if got, _ := os.ReadFile(filepath.Join(root, "README.md")); string(got) != "user dirty\n" {
		t.Fatal("dirty user file changed")
	}
	// Restore only test-owned contents to the selected committed base.
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("first applied\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// A different branch at the same commit is still drift.
	runTaskWorktreeGitTest(t, root, "switch", "-c", "another-branch")
	if i, e := target.Inspect(ctx, secondPatch); !errors.Is(e, app.ErrPatchTargetConflict) || !strings.Contains(i.Reason, "branch drifted") {
		t.Fatalf("branch drift accepted: %+v %v", i, e)
	}
	branch := strings.TrimPrefix(secondBase.BaseRef(), "refs/heads/")
	runTaskWorktreeGitTest(t, root, "switch", branch)
	inspection, err = target.Inspect(ctx, secondPatch)
	if err != nil {
		t.Fatal(err)
	}
	target.applyMu.Lock()
	if _, err := target.Apply(ctx, secondPatch, inspection); !errors.Is(err, app.ErrPatchTargetConflict) {
		t.Fatal("concurrent Apply admitted")
	}
	target.applyMu.Unlock()
	if _, err := target.Apply(ctx, secondPatch, inspection); err != nil {
		t.Fatal(err)
	}
	// Fresh resolver after restart uses task authority, never admission/current HEAD.
	restarted := newTaskWorktreeResolver(db.Reader(), nil)
	if _, err := restarted.ResolveExecutionRoot(ctx, first); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.ResolveExecutionRoot(ctx, second); err != nil {
		t.Fatal(err)
	}
	persisted, _ := db.Reader().GetTaskWorktreeBinding(ctx, first.ID())
	if persisted != firstBase {
		t.Fatal("old base changed")
	}
	project, _ := db.Reader().GetRepositoryBinding(ctx, room.ID())
	if project.AdmittedBase() != firstRevision {
		t.Fatal("project admission rewritten")
	}
	envelopes := newBoundSpecCodingEnvelopeResolver(db.Reader(), nil, pidiscovery.Result{State: pidiscovery.StateReady, Version: "0.84.2"})
	oldEnvelope, err := envelopes.ResolveTaskSpecCodingEnvelope(ctx, room, firstBase)
	if err != nil || oldEnvelope.Repository().SourceRevision != firstRevision {
		t.Fatal(err)
	}
	newEnvelope, err := envelopes.ResolveTaskSpecCodingEnvelope(ctx, nextRoom, secondBase)
	if err != nil || newEnvelope.Repository().SourceRevision != secondRevision {
		t.Fatal(err)
	}
	if len(restarted.byAnchor) != 2 {
		t.Fatalf("base cache conflated: %d", len(restarted.byAnchor))
	}
	if got, _ := os.ReadFile(filepath.Join(root, "private-untracked.txt")); string(got) != "keep" {
		t.Fatal("unrelated file changed")
	}
}

func TestTaskBaseDeclarationDriftFailsWithoutWorktree(t *testing.T) {
	ctx := context.Background()
	db := openTaskWorktreeResolverStore(t)
	defer db.Close()
	root, _, revision := newTaskWorktreeTestRepository(t)
	now := time.Now().UTC()
	_, task := insertResolverRoomAndTask(t, db, root, revision, "Drift", now)
	runTaskWorktreeGitTest(t, root, "-c", "user.name=Chora Test", "-c", "user.email=chora@example.test", "commit", "--allow-empty", "-m", "external")
	resolver := newTaskWorktreeResolver(db.Reader(), nil)
	if _, err := resolver.PlanForRevision(ctx, task, revision, now); !errors.Is(err, app.ErrInvalidCommand) {
		t.Fatalf("declaration drift accepted: %v", err)
	}
	if _, err := db.Reader().GetTaskWorktreeBinding(ctx, task.ID()); !errors.Is(err, storecontract.ErrNotFound) {
		t.Fatalf("drift persisted: %v", err)
	}
}

func TestLegacyTaskBaseNeverInheritsCurrentHead(t *testing.T) {
	ctx := context.Background()
	db := openTaskWorktreeResolverStore(t)
	defer db.Close()
	root, _, revision := newTaskWorktreeTestRepository(t)
	now := time.Now().UTC()
	room, task := insertResolverRoomAndTask(t, db, root, revision, "Legacy", now)
	project, err := db.Reader().GetRepositoryBinding(ctx, room.ID())
	if err != nil {
		t.Fatal(err)
	}
	manager, err := newTaskWorktreeResolver(db.Reader(), nil).managerForBinding(project)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := manager.Plan(task.ID(), task.Title(), now)
	if err != nil {
		t.Fatal(err)
	}
	if legacy.StartPolicy() != domain.TaskStartPolicyLegacy {
		t.Fatal("fixture is not historical")
	}
	runTaskWorktreeGitTest(t, root, "-c", "user.name=Chora Test", "-c", "user.email=chora@example.test", "commit", "--allow-empty", "-m", "new HEAD")
	effective, err := repositoryForTaskBase(ctx, project, legacy)
	if err != nil || effective.AdmittedBase() != revision || effective.BaseIdentity() != project.BaseIdentity() {
		t.Fatalf("legacy drift: %+v %v", effective, err)
	}
	// A legacy row that points elsewhere cannot silently be treated as a new Task.
	current, _, _, err := currentTaskBase(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	forged, err := domain.NewTaskWorktreeBinding(domain.TaskWorktreeBindingParams{TaskID: task.ID().String(), RepositoryIdentity: legacy.RepositoryIdentity(), PinnedBaseRevision: current, RelativeLocator: legacy.RelativeLocator(), ConfiguredRootFingerprint: legacy.ConfiguredRootFingerprint(), CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repositoryForTaskBase(ctx, project, forged); err == nil {
		t.Fatal("inconsistent historical authority accepted")
	}
}
