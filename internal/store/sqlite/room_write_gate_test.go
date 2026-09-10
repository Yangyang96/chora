package sqlite

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func TestArchivedRoomRejectsTechnicalPlanMutationWithoutPersistingRow(t *testing.T) {
	db, task, draft := openTechnicalPlanStore(t)
	defer db.Close()
	ctx := context.Background()
	archived := archiveRoomForWriteGateTest(t, ctx, db, task.RoomID())

	err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		return tx.InsertTechnicalPlanDraft(ctx, draft)
	})
	if !errors.Is(err, storecontract.ErrRoomStateForbidden) {
		t.Fatalf("archived InsertTechnicalPlanDraft err=%v", err)
	}
	if _, err := db.Reader().GetTechnicalPlanDraft(ctx, draft.ID()); !errors.Is(err, storecontract.ErrNotFound) {
		t.Fatalf("blocked TechnicalPlanDraft persisted err=%v", err)
	}
	assertRejectedWriteKeptArchivedRoom(t, ctx, db, archived)
}

func TestArchivedRoomRejectsRunCreationWithoutPersistingRow(t *testing.T) {
	db, task, _ := openTechnicalPlanStore(t)
	defer db.Close()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	revision, err := domain.NewRoomContextRevision(domain.RoomContextRevisionParams{
		EntryID: domain.NewContextEntryID(), RevisionID: domain.NewContextRevisionID(), RoomID: task.RoomID(),
		Kind: domain.ContextKindBrief, RevisionNumber: 1, Title: "run context", CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	room, err := db.Reader().GetRoom(ctx, task.RoomID())
	if err != nil {
		t.Fatal(err)
	}
	charter, err := domain.NewRunCharter(domain.RunCharterParams{
		ID: domain.NewCharterID(), TaskID: task.ID(), TaskGoal: task.Goal(), Criteria: task.Criteria(),
		ContextRevisionIDs: []domain.ContextRevisionID{revision.RevisionID()}, WorkspaceRoot: room.WorkspaceRoot(),
		AdapterID: "codex", SandboxMode: "workspace-write", ExpectedOutput: "patch", ResponsibleHuman: "owner",
		CapabilityEnvelope: domain.CapabilityEnvelope{"shell": true}, Initiator: "test", CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.InsertRevision(ctx, revision); err != nil {
			return err
		}
		return tx.InsertCharter(ctx, charter)
	}); err != nil {
		t.Fatal(err)
	}
	archived := archiveRoomForWriteGateTest(t, ctx, db, task.RoomID())
	run, err := domain.NewAgentRun(domain.NewRunID(), task.ID(), charter.ID(), now)
	if err != nil {
		t.Fatal(err)
	}

	err = db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		return tx.InsertRun(ctx, run)
	})
	if !errors.Is(err, storecontract.ErrRoomStateForbidden) {
		t.Fatalf("archived InsertRun err=%v", err)
	}
	if _, err := db.Reader().GetRun(ctx, run.ID()); !errors.Is(err, storecontract.ErrNotFound) {
		t.Fatalf("blocked Run persisted err=%v", err)
	}
	assertRejectedWriteKeptArchivedRoom(t, ctx, db, archived)
}

func archiveRoomForWriteGateTest(t *testing.T, ctx context.Context, db *Store, roomID domain.RoomID) domain.Room {
	t.Helper()
	current, err := db.Reader().GetRoom(ctx, roomID)
	if err != nil {
		t.Fatal(err)
	}
	at := current.UpdatedAt().Add(time.Minute)
	archived, err := current.Archive(current.Version(), at)
	if err != nil {
		t.Fatal(err)
	}
	event := storecontract.RoomLifecycleEvent{
		RoomID: archived.ID(), FromState: current.State(), ToState: archived.State(), Version: archived.Version(),
		ActorID: "owner", SessionID: "write-gate-test",
		IdempotencyKeyHash: sha256.Sum256([]byte("archive:" + archived.ID().String())),
		RequestDigest:      sha256.Sum256([]byte("archive-request:" + archived.ID().String())),
		OccurredAt:         at,
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		return tx.SaveRoomLifecycleCAS(ctx, current.Version(), archived, event)
	}); err != nil {
		t.Fatal(err)
	}
	return archived
}

func assertRejectedWriteKeptArchivedRoom(t *testing.T, ctx context.Context, db *Store, archived domain.Room) {
	t.Helper()
	stored, err := db.Reader().GetRoom(ctx, archived.ID())
	if err != nil {
		t.Fatal(err)
	}
	if stored.State() != domain.RoomStateArchived || stored.Version() != archived.Version() {
		t.Fatalf("rejected write mutated Room state=%q version=%d, want archived version=%d", stored.State(), stored.Version(), archived.Version())
	}
}
