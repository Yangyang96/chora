package sqlite_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
	"github.com/Yangyang96/chora/internal/store/sqlite"
)

func TestRoomLifecycleCASPersistsAuditAndSurvivesReopen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, seeded := openSeeded(t)

	archivedAt := seeded.now.Add(time.Minute)
	archived, err := seeded.room.Archive(seeded.room.Version(), archivedAt)
	if err != nil {
		t.Fatal(err)
	}
	event := roomLifecycleEvent(seeded.room, archived, "owner", "session-a", archivedAt)
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		return tx.SaveRoomLifecycleCAS(ctx, seeded.room.Version(), archived, event)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		return tx.SaveRoomLifecycleCAS(ctx, seeded.room.Version(), archived, event)
	}); !errors.Is(err, storecontract.ErrVersionConflict) {
		t.Fatalf("stale lifecycle save err=%v", err)
	}

	stored, err := db.Reader().GetRoom(ctx, seeded.room.ID())
	if err != nil {
		t.Fatal(err)
	}
	if stored.State() != domain.RoomStateArchived || stored.Version() != archived.Version() || !stored.ArchivedAt().Equal(archivedAt) {
		t.Fatalf("stored Room state=%q version=%d archivedAt=%s", stored.State(), stored.Version(), stored.ArchivedAt())
	}
	events, err := db.Reader().ListRoomLifecycleEvents(ctx, seeded.room.ID())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].FromState != domain.RoomStateActive || events[0].ToState != domain.RoomStateArchived || events[0].Version != archived.Version() || events[0].ActorID != "owner" || events[0].SessionID != "session-a" {
		t.Fatalf("lifecycle events=%#v", events)
	}

	path := seeded.path
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = sqlite.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	reopened, err := db.Reader().GetRoom(ctx, seeded.room.ID())
	if err != nil {
		t.Fatal(err)
	}
	if reopened.State() != stored.State() || reopened.Version() != stored.Version() || !reopened.ArchivedAt().Equal(stored.ArchivedAt()) {
		t.Fatalf("reopened Room=%#v want=%#v", reopened, stored)
	}
}

func TestArchivedRoomTransactionallyRejectsTaskCreation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, seeded := openSeeded(t)
	defer db.Close()

	at := seeded.now.Add(time.Minute)
	archived, err := seeded.room.Archive(seeded.room.Version(), at)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		return tx.SaveRoomLifecycleCAS(ctx, seeded.room.Version(), archived, roomLifecycleEvent(seeded.room, archived, "owner", "session-a", at))
	}); err != nil {
		t.Fatal(err)
	}
	criterion, err := domain.NewAcceptanceCriterion(domain.NewCriterionID(), "blocked", "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := domain.NewTask(domain.NewTaskID(), seeded.room.ID(), "must not exist", "archived Room is frozen", []domain.AcceptanceCriterion{criterion})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		return tx.InsertTask(ctx, task)
	}); !errors.Is(err, storecontract.ErrRoomStateForbidden) {
		t.Fatalf("archived InsertTask err=%v", err)
	}
	if _, err := db.Reader().GetTask(ctx, task.ID()); !errors.Is(err, storecontract.ErrNotFound) {
		t.Fatalf("blocked Task persisted err=%v", err)
	}
}

func TestArchiveRefusesRunActivityWithoutMutatingRun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, seeded := openSeeded(t)
	defer db.Close()

	ready, err := seeded.run.Transition(domain.CommandPrepareRun, seeded.now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	running, err := ready.Transition(domain.CommandStartAttempt, seeded.now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.SaveRunCAS(ctx, seeded.run.Version(), ready); err != nil {
			return err
		}
		return tx.SaveRunCAS(ctx, ready.Version(), running)
	}); err != nil {
		t.Fatal(err)
	}
	currentRoom, err := db.Reader().GetRoom(ctx, seeded.room.ID())
	if err != nil {
		t.Fatal(err)
	}
	archivedAt := currentRoom.UpdatedAt().Add(time.Minute)
	archived, err := currentRoom.Archive(currentRoom.Version(), archivedAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		return tx.SaveRoomLifecycleCAS(ctx, currentRoom.Version(), archived, roomLifecycleEvent(currentRoom, archived, "owner", "session-a", archivedAt))
	}); !errors.Is(err, storecontract.ErrRoomStateForbidden) {
		t.Fatalf("archive busy Room err=%v", err)
	}
	storedRoom, err := db.Reader().GetRoom(ctx, seeded.room.ID())
	if err != nil {
		t.Fatal(err)
	}
	storedRun, err := db.Reader().GetRun(ctx, seeded.run.ID())
	if err != nil {
		t.Fatal(err)
	}
	if storedRoom.State() != domain.RoomStateActive || storedRoom.Version() != currentRoom.Version() || storedRun.State() != domain.RunStateRunning || storedRun.Version() != running.Version() {
		t.Fatalf("archive refusal mutated Room/Run: Room=%q/%d Run=%q/%d", storedRoom.State(), storedRoom.Version(), storedRun.State(), storedRun.Version())
	}
}

func TestRoomDirectoryUsesStableStateSpecificOrderingAndFullCounts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, seeded := openSeeded(t)
	defer db.Close()

	olderAt := seeded.now.Add(-time.Hour)
	older, err := domain.NewRoom(domain.RoomParams{ID: domain.NewRoomID(), Name: "older", WorkspaceRoot: filepath.Join(t.TempDir(), "older"), CreatedAt: olderAt, UpdatedAt: olderAt})
	if err != nil {
		t.Fatal(err)
	}
	archivedAt := seeded.now.Add(2 * time.Hour)
	archived, err := older.Archive(older.Version(), archivedAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.InsertRoom(ctx, older); err != nil {
			return err
		}
		return tx.SaveRoomLifecycleCAS(ctx, older.Version(), archived, roomLifecycleEvent(older, archived, "owner", "session-b", archivedAt))
	}); err != nil {
		t.Fatal(err)
	}

	active, err := db.Reader().ListRoomDirectory(ctx, domain.RoomStateActive)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].Room.ID() != seeded.room.ID() || active[0].TaskCounts.Total != 1 || active[0].TaskCounts.Open != 1 || active[0].TaskCounts.Terminal != 0 || active[0].LastActivityAt.Before(seeded.now) || len(active[0].Tasks) != 1 || active[0].Tasks[0].Task.ID() != seeded.task.ID() {
		t.Fatalf("active directory=%#v", active)
	}
	archivedRows, err := db.Reader().ListRoomDirectory(ctx, domain.RoomStateArchived)
	if err != nil {
		t.Fatal(err)
	}
	if len(archivedRows) != 1 || archivedRows[0].Room.ID() != older.ID() || !archivedRows[0].LastActivityAt.Equal(archivedAt) || len(archivedRows[0].Tasks) != 0 {
		t.Fatalf("archived directory=%#v", archivedRows)
	}
}

func TestArchiveRaceWithTaskCreationHasAtMostOneWinner(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, seeded := openSeeded(t)
	defer db.Close()
	otherDB, err := sqlite.Open(ctx, seeded.path)
	if err != nil {
		t.Fatal(err)
	}
	defer otherDB.Close()

	criterion, err := domain.NewAcceptanceCriterion(domain.NewCriterionID(), "race", "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := domain.NewTask(domain.NewTaskID(), seeded.room.ID(), "racing Task", "only one command wins", []domain.AcceptanceCriterion{criterion})
	if err != nil {
		t.Fatal(err)
	}
	archivedAt := seeded.room.UpdatedAt().Add(time.Minute)
	archived, err := seeded.room.Archive(seeded.room.Version(), archivedAt)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() {
		<-start
		results <- db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
			return tx.SaveRoomLifecycleCAS(ctx, seeded.room.Version(), archived, roomLifecycleEvent(seeded.room, archived, "owner", "archive-tab", archivedAt))
		})
	}()
	go func() {
		<-start
		results <- otherDB.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
			return tx.InsertTask(ctx, task)
		})
	}()
	close(start)

	var succeeded int
	for range 2 {
		err := <-results
		if err == nil {
			succeeded++
			continue
		}
		if !errors.Is(err, storecontract.ErrVersionConflict) && !errors.Is(err, storecontract.ErrRoomStateForbidden) {
			t.Fatalf("unexpected race error=%v", err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("archive/task race successes=%d, want exactly 1", succeeded)
	}
	storedRoom, err := db.Reader().GetRoom(ctx, seeded.room.ID())
	if err != nil {
		t.Fatal(err)
	}
	_, taskErr := db.Reader().GetTask(ctx, task.ID())
	if storedRoom.State() == domain.RoomStateArchived && !errors.Is(taskErr, storecontract.ErrNotFound) {
		t.Fatalf("archived race winner retained Task err=%v", taskErr)
	}
	if storedRoom.State() == domain.RoomStateActive && taskErr != nil {
		t.Fatalf("Task race winner missing Task err=%v", taskErr)
	}
}

func TestArchiveRaceWithPlanMutationHasAtMostOneWinner(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, seeded := openSeeded(t)
	defer db.Close()
	otherDB, err := sqlite.Open(ctx, seeded.path)
	if err != nil {
		t.Fatal(err)
	}
	defer otherDB.Close()
	draft, err := domain.NewTechnicalPlanDraft(domain.NewTechnicalPlanDraftParams{
		ID: domain.NewTechnicalPlanDraftID(), TaskID: seeded.task.ID(), SelectionDigest: sha256.Sum256([]byte("archive-plan-race-selection")),
		Content: domain.TechnicalPlanContent{TechnicalSteps: []string{"before"}, Decisions: []string{"bounded"}, Risks: []string{"race"}, Unknowns: []string{"none"}}, CreatedAt: seeded.now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.InsertTechnicalPlanDraft(ctx, draft) }); err != nil {
		t.Fatal(err)
	}
	edited, err := draft.Edit(draft.EditVersion(), domain.TechnicalPlanContent{TechnicalSteps: []string{"after"}, Decisions: []string{"bounded"}, Risks: []string{"race"}, Unknowns: []string{"none"}}, seeded.now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	currentRoom, err := db.Reader().GetRoom(ctx, seeded.room.ID())
	if err != nil {
		t.Fatal(err)
	}
	archivedAt := currentRoom.UpdatedAt().Add(time.Minute)
	archived, err := currentRoom.Archive(currentRoom.Version(), archivedAt)
	if err != nil {
		t.Fatal(err)
	}
	archiveErr, mutationErr := raceRoomCommands(
		func() error {
			return db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
				return tx.SaveRoomLifecycleCAS(ctx, currentRoom.Version(), archived, roomLifecycleEvent(currentRoom, archived, "owner", "archive-plan-tab", archivedAt))
			})
		},
		func() error {
			return otherDB.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
				return tx.SaveTechnicalPlanDraftCAS(ctx, draft.EditVersion(), edited)
			})
		},
	)
	assertExactlyOneRoomRaceWinner(t, archiveErr, mutationErr)
	storedRoom, err := db.Reader().GetRoom(ctx, currentRoom.ID())
	if err != nil {
		t.Fatal(err)
	}
	storedDraft, err := db.Reader().GetTechnicalPlanDraft(ctx, draft.ID())
	if err != nil {
		t.Fatal(err)
	}
	if storedRoom.State() == domain.RoomStateArchived && storedDraft.EditVersion() != draft.EditVersion() {
		t.Fatalf("archive winner retained Plan edit version=%d", storedDraft.EditVersion())
	}
	if storedRoom.State() == domain.RoomStateActive && storedDraft.EditVersion() != edited.EditVersion() {
		t.Fatalf("Plan winner missing edit version=%d", storedDraft.EditVersion())
	}
}

func TestArchiveRaceWithRunCreationHasAtMostOneWinner(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, seeded := openSeeded(t)
	defer db.Close()
	otherDB, err := sqlite.Open(ctx, seeded.path)
	if err != nil {
		t.Fatal(err)
	}
	defer otherDB.Close()
	criterion, err := domain.NewAcceptanceCriterion(domain.NewCriterionID(), "run race", "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := domain.NewTask(domain.NewTaskID(), seeded.room.ID(), "Run race Task", "Create exactly once", []domain.AcceptanceCriterion{criterion})
	if err != nil {
		t.Fatal(err)
	}
	charter, err := domain.NewRunCharter(domain.RunCharterParams{
		ID: domain.NewCharterID(), TaskID: task.ID(), TaskGoal: task.Goal(), Criteria: task.Criteria(), ContextRevisionIDs: []domain.ContextRevisionID{seeded.revision.RevisionID()},
		WorkspaceRoot: seeded.room.WorkspaceRoot(), AdapterID: "fake", SandboxMode: "workspace", ExpectedOutput: "patch", ResponsibleHuman: "owner",
		CapabilityEnvelope: domain.CapabilityEnvelope{"write": true}, Initiator: "test", CreatedAt: seeded.now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.InsertTask(ctx, task); err != nil {
			return err
		}
		return tx.InsertCharter(ctx, charter)
	}); err != nil {
		t.Fatal(err)
	}
	run, err := domain.NewAgentRun(domain.NewRunID(), task.ID(), charter.ID(), seeded.now)
	if err != nil {
		t.Fatal(err)
	}
	currentRoom, err := db.Reader().GetRoom(ctx, seeded.room.ID())
	if err != nil {
		t.Fatal(err)
	}
	archivedAt := currentRoom.UpdatedAt().Add(time.Minute)
	archived, err := currentRoom.Archive(currentRoom.Version(), archivedAt)
	if err != nil {
		t.Fatal(err)
	}
	archiveErr, mutationErr := raceRoomCommands(
		func() error {
			return db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
				return tx.SaveRoomLifecycleCAS(ctx, currentRoom.Version(), archived, roomLifecycleEvent(currentRoom, archived, "owner", "archive-run-tab", archivedAt))
			})
		},
		func() error {
			return otherDB.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.InsertRun(ctx, run) })
		},
	)
	assertExactlyOneRoomRaceWinner(t, archiveErr, mutationErr)
	storedRoom, err := db.Reader().GetRoom(ctx, currentRoom.ID())
	if err != nil {
		t.Fatal(err)
	}
	_, runErr := db.Reader().GetRun(ctx, run.ID())
	if storedRoom.State() == domain.RoomStateArchived && !errors.Is(runErr, storecontract.ErrNotFound) {
		t.Fatalf("archive winner retained Run err=%v", runErr)
	}
	if storedRoom.State() == domain.RoomStateActive && runErr != nil {
		t.Fatalf("Run winner missing Run err=%v", runErr)
	}
}

func raceRoomCommands(first, second func() error) (error, error) {
	start := make(chan struct{})
	firstResult := make(chan error, 1)
	secondResult := make(chan error, 1)
	go func() { <-start; firstResult <- first() }()
	go func() { <-start; secondResult <- second() }()
	close(start)
	return <-firstResult, <-secondResult
}

func assertExactlyOneRoomRaceWinner(t *testing.T, archiveErr, mutationErr error) {
	t.Helper()
	if (archiveErr == nil) == (mutationErr == nil) {
		t.Fatalf("race archive err=%v mutation err=%v; want exactly one success", archiveErr, mutationErr)
	}
	loser := archiveErr
	if loser == nil {
		loser = mutationErr
	}
	if !errors.Is(loser, storecontract.ErrVersionConflict) && !errors.Is(loser, storecontract.ErrRoomStateForbidden) {
		t.Fatalf("unexpected race loser error=%v", loser)
	}
}

func roomLifecycleEvent(before, after domain.Room, actor, session string, at time.Time) storecontract.RoomLifecycleEvent {
	return storecontract.RoomLifecycleEvent{
		RoomID:             after.ID(),
		FromState:          before.State(),
		ToState:            after.State(),
		Version:            after.Version(),
		ActorID:            actor,
		SessionID:          session,
		IdempotencyKeyHash: sha256.Sum256([]byte("room-lifecycle-key:" + after.ID().String() + ":" + string(after.State()))),
		RequestDigest:      sha256.Sum256([]byte("room-lifecycle-request:" + after.ID().String() + ":" + string(after.State()))),
		OccurredAt:         at,
	}
}
