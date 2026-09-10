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

func TestImmutableTechnicalPlanRoundTripCASAndLineage(t *testing.T) {
	db, task, draft := openTechnicalPlanStore(t)
	defer db.Close()
	ctx := context.Background()
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.InsertTechnicalPlanDraft(ctx, draft) }); err != nil {
		t.Fatal(err)
	}
	duplicate, _ := domain.NewTechnicalPlanDraft(domain.NewTechnicalPlanDraftParams{ID: domain.NewTechnicalPlanDraftID(), TaskID: task.ID(), SelectionDigest: draft.SelectionDigest(), Content: draft.Content(), CreatedAt: draft.CreatedAt()})
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.InsertTechnicalPlanDraft(ctx, duplicate) }); !errors.Is(err, storecontract.ErrPlanningConflict) {
		t.Fatalf("second open draft err=%v", err)
	}
	edited, err := draft.Edit(1, domain.TechnicalPlanContent{TechnicalSteps: []string{"inspect", "build", "verify"}, Decisions: []string{"bounded"}, Risks: []string{"risk"}, Unknowns: []string{"unknown"}}, draft.UpdatedAt().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.SaveTechnicalPlanDraftCAS(ctx, 1, edited) }); err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.SaveTechnicalPlanDraftCAS(ctx, 1, edited) }); !errors.Is(err, storecontract.ErrVersionConflict) {
		t.Fatalf("stale save err=%v", err)
	}
	closed, revision1, err := edited.Submit(2, domain.NewTechnicalPlanRevisionID(), false, edited.UpdatedAt().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.SubmitTechnicalPlanDraftCAS(ctx, 2, closed, revision1) }); err != nil {
		t.Fatal(err)
	}
	loaded, err := db.Reader().GetTechnicalPlanRevision(ctx, revision1.ID())
	if err != nil || loaded.ContentDigest() != revision1.ContentDigest() || loaded.RevisionNumber() != 1 {
		t.Fatalf("loaded=%#v err=%v", loaded, err)
	}
	successor, err := domain.NewTechnicalPlanSuccessorDraft(domain.NewTechnicalPlanDraftID(), revision1, revision1.SubmittedAt().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	requestReview, err := domain.NewTechnicalPlanReview(domain.NewTechnicalPlanReviewParams{ID: domain.NewTechnicalPlanReviewID(), RevisionID: revision1.ID(), TaskID: task.ID(), Kind: domain.TechnicalPlanReviewRequestRevision, Reviewer: "owner", Note: "revise", DecidedAt: successor.CreatedAt()})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.InsertTechnicalPlanReviewCAS(ctx, requestReview); err != nil {
			return err
		}
		return tx.InsertTechnicalPlanDraft(ctx, successor)
	}); err != nil {
		t.Fatal(err)
	}
	closed2, revision2, err := successor.Submit(1, domain.NewTechnicalPlanRevisionID(), true, successor.UpdatedAt().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		return tx.SubmitTechnicalPlanDraftCAS(ctx, 1, closed2, revision2)
	}); err != nil {
		t.Fatal(err)
	}
	revisions, err := db.Reader().ListTechnicalPlanRevisions(ctx, task.ID())
	if err != nil || len(revisions) != 2 || revisions[1].PredecessorRevisionID() != revision1.ID() || !revisions[1].Unchanged() {
		t.Fatalf("revisions=%#v err=%v", revisions, err)
	}
	if _, err := db.db.ExecContext(ctx, `UPDATE technical_plan_revisions SET revision_number=9 WHERE id=?`, revision1.ID().String()); err == nil {
		t.Fatal("immutable revision updated")
	}
}

func TestTechnicalPlanSubmissionRejectsForgedDraftContent(t *testing.T) {
	db, task, draft := openTechnicalPlanStore(t)
	defer db.Close()
	ctx := context.Background()
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.InsertTechnicalPlanDraft(ctx, draft) }); err != nil {
		t.Fatal(err)
	}
	forgedContent := domain.TechnicalPlanContent{
		TechnicalSteps: []string{"forged step"}, Decisions: []string{"forged decision"},
		Risks: []string{"forged risk"}, Unknowns: []string{"forged unknown"},
	}
	revisionID := domain.NewTechnicalPlanRevisionID()
	submittedAt := draft.UpdatedAt().Add(time.Second)
	closed, err := domain.RestoreTechnicalPlanDraft(domain.TechnicalPlanDraftRecord{
		ID: draft.ID(), TaskID: task.ID(), EditVersion: 2, NextRevisionNumber: 1,
		SelectionDigest: draft.SelectionDigest(), Content: forgedContent,
		CreatedAt: draft.CreatedAt(), UpdatedAt: submittedAt, ClosedAt: submittedAt, SubmittedRevisionID: revisionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	revision, err := domain.RestoreTechnicalPlanRevision(domain.TechnicalPlanRevisionRecord{
		ID: revisionID, TaskID: task.ID(), SourceDraftID: draft.ID(), RevisionNumber: 1,
		Content: forgedContent, ContentDigest: domain.CanonicalTechnicalPlanContentDigest(forgedContent),
		SelectionDigest: draft.SelectionDigest(), SubmittedAt: submittedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		return tx.SubmitTechnicalPlanDraftCAS(ctx, 1, closed, revision)
	}); !errors.Is(err, storecontract.ErrVersionConflict) {
		t.Fatalf("forged Draft content submission err=%v", err)
	}
}

func TestTechnicalPlanRevisionSchemaRejectsFalseChangedLabel(t *testing.T) {
	db, _, draft := openTechnicalPlanStore(t)
	defer db.Close()
	ctx := context.Background()
	closed, revision1, err := draft.Submit(1, domain.NewTechnicalPlanRevisionID(), false, draft.UpdatedAt().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.InsertTechnicalPlanDraft(ctx, draft); err != nil {
			return err
		}
		return tx.SubmitTechnicalPlanDraftCAS(ctx, 1, closed, revision1)
	}); err != nil {
		t.Fatal(err)
	}
	successor, err := domain.NewTechnicalPlanSuccessorDraft(domain.NewTechnicalPlanDraftID(), revision1, revision1.SubmittedAt().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.InsertTechnicalPlanDraft(ctx, successor) }); err != nil {
		t.Fatal(err)
	}
	_, revision2, err := successor.Submit(1, domain.NewTechnicalPlanRevisionID(), true, successor.UpdatedAt().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	content, err := encodePlanContent(revision2.Content())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `INSERT INTO technical_plan_revisions(id,task_id,source_draft_id,revision_number,predecessor_revision_id,content_json,content_digest,selection_digest,unchanged,submitted_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		revision2.ID().String(), revision2.TaskID().String(), revision2.SourceDraftID().String(), revision2.RevisionNumber(), revision2.PredecessorRevisionID().String(), content,
		digestBytes(revision2.ContentDigest()), digestBytes(revision2.SelectionDigest()), false, timeText(revision2.SubmittedAt())); err == nil {
		t.Fatal("schema accepted unchanged content labeled as changed")
	}
}

func TestTechnicalPlanReviewAcceptanceAndRunBindingAreAppendOnlyExact(t *testing.T) {
	db, task, draft := openTechnicalPlanStore(t)
	defer db.Close()
	ctx := context.Background()
	closed, revision, _ := draft.Submit(1, domain.NewTechnicalPlanRevisionID(), false, draft.UpdatedAt().Add(time.Second))
	review, _ := domain.NewTechnicalPlanReview(domain.NewTechnicalPlanReviewParams{ID: domain.NewTechnicalPlanReviewID(), RevisionID: revision.ID(), TaskID: task.ID(), Kind: domain.TechnicalPlanReviewAccept, Reviewer: "owner", Note: "ready", DecidedAt: closed.ClosedAt().Add(time.Second)})
	snapshotID := domain.NewContextSnapshotID()
	snapshotDigest := sha256.Sum256([]byte("snapshot"))
	if _, err := db.db.ExecContext(ctx, `INSERT INTO context_snapshots(id,digest,canonical_json,markdown,created_at) VALUES(?,?,?,?,?)`, snapshotID.String(), snapshotDigest[:], []byte(`{"snapshot":true}`), []byte("snapshot"), timeText(review.DecidedAt())); err != nil {
		t.Fatal(err)
	}
	acceptance, _ := domain.NewTechnicalPlanAcceptanceBinding(domain.TechnicalPlanAcceptanceBindingRecord{TaskID: task.ID(), RevisionID: revision.ID(), ReviewID: review.ID(), SnapshotID: snapshotID, SnapshotDigest: snapshotDigest, BoundAt: review.DecidedAt().Add(time.Second)})
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.InsertTechnicalPlanDraft(ctx, draft); err != nil {
			return err
		}
		if err := tx.SubmitTechnicalPlanDraftCAS(ctx, 1, closed, revision); err != nil {
			return err
		}
		if err := tx.InsertTechnicalPlanReviewCAS(ctx, review); err != nil {
			return err
		}
		return tx.InsertTechnicalPlanAcceptance(ctx, acceptance)
	}); err != nil {
		t.Fatal(err)
	}
	current, err := db.Reader().GetCurrentTechnicalPlanAcceptance(ctx, task.ID())
	if err != nil || current.RevisionID() != revision.ID() || current.SnapshotDigest() != snapshotDigest {
		t.Fatalf("current=%#v err=%v", current, err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.InsertTechnicalPlanReviewCAS(ctx, review) }); !errors.Is(err, storecontract.ErrVersionConflict) {
		t.Fatalf("duplicate review err=%v", err)
	}
	if _, err := db.db.ExecContext(ctx, `UPDATE technical_plan_reviews SET note='changed' WHERE id=?`, review.ID().String()); err == nil {
		t.Fatal("append-only review updated")
	}
	contextRevision, err := domain.NewRoomContextRevision(domain.RoomContextRevisionParams{EntryID: domain.NewContextEntryID(), RevisionID: domain.NewContextRevisionID(), RoomID: task.RoomID(), Kind: domain.ContextKindBrief, RevisionNumber: 1, Title: "Bound context", Body: "Frozen", CreatedAt: acceptance.BoundAt(), UpdatedAt: acceptance.BoundAt()})
	if err != nil {
		t.Fatal(err)
	}
	charter, err := domain.NewRunCharter(domain.RunCharterParams{ID: domain.NewCharterID(), TaskID: task.ID(), TaskGoal: task.Goal(), Criteria: task.Criteria(), ContextRevisionIDs: []domain.ContextRevisionID{contextRevision.RevisionID()}, WorkspaceRoot: t.TempDir(), AdapterID: "fake", SandboxMode: "workspace-write", ExpectedOutput: "patch", ResponsibleHuman: "owner", CapabilityEnvelope: domain.CapabilityEnvelope{"read": true}, Initiator: "test", CreatedAt: acceptance.BoundAt()})
	if err != nil {
		t.Fatal(err)
	}
	run, err := domain.NewAgentRun(domain.NewRunID(), task.ID(), charter.ID(), acceptance.BoundAt())
	if err != nil {
		t.Fatal(err)
	}
	binding, _ := domain.NewTechnicalPlanRunBinding(domain.TechnicalPlanRunBindingRecord{RunID: run.ID(), TaskID: task.ID(), RevisionID: revision.ID(), CharterID: charter.ID(), SnapshotID: snapshotID, SnapshotDigest: snapshotDigest, BoundAt: acceptance.BoundAt().Add(time.Second)})
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.InsertRevision(ctx, contextRevision); err != nil {
			return err
		}
		if err := tx.InsertCharter(ctx, charter); err != nil {
			return err
		}
		if err := tx.InsertRun(ctx, run); err != nil {
			return err
		}
		return tx.InsertTechnicalPlanRunBinding(ctx, binding)
	}); err != nil {
		t.Fatal(err)
	}
	loadedBinding, err := db.Reader().GetTechnicalPlanRunBinding(ctx, run.ID())
	if err != nil || loadedBinding.RevisionID() != revision.ID() || loadedBinding.CharterID() != charter.ID() {
		t.Fatalf("binding=%#v err=%v", loadedBinding, err)
	}
	if _, err := db.db.ExecContext(ctx, `UPDATE technical_plan_run_bindings SET snapshot_digest=zeroblob(32) WHERE run_id=?`, run.ID().String()); err == nil {
		t.Fatal("immutable run binding updated")
	}
}

func TestTechnicalPlanSubmissionCASRollsBackRevision(t *testing.T) {
	db, _, draft := openTechnicalPlanStore(t)
	defer db.Close()
	ctx := context.Background()
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.InsertTechnicalPlanDraft(ctx, draft) }); err != nil {
		t.Fatal(err)
	}
	closed, revision, _ := draft.Submit(1, domain.NewTechnicalPlanRevisionID(), false, draft.UpdatedAt().Add(time.Second))
	err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.SubmitTechnicalPlanDraftCAS(ctx, 2, closed, revision) })
	if !errors.Is(err, storecontract.ErrVersionConflict) {
		t.Fatalf("stale submit err=%v", err)
	}
	if _, err := db.Reader().GetTechnicalPlanRevision(ctx, revision.ID()); !errors.Is(err, storecontract.ErrNotFound) {
		t.Fatalf("stale submit persisted revision: %v", err)
	}
}

func openTechnicalPlanStore(t *testing.T) (*Store, domain.Task, domain.TechnicalPlanDraft) {
	t.Helper()
	ctx := context.Background()
	db, err := openLatestInternalSQLiteTestStore(ctx, t.TempDir()+"/state/chora.db", nil)
	if err != nil {
		t.Fatal(err)
	}
	criterion, _ := domain.NewAcceptanceCriterion(domain.NewCriterionID(), "Plan is reviewable", "Review survives restart")
	now := time.Date(2026, 8, 17, 1, 2, 3, 0, time.UTC)
	room, err := domain.NewRoom(domain.RoomParams{ID: domain.NewRoomID(), Name: "Room", Description: "Description", WorkspaceRoot: t.TempDir(), CreatedAt: now, UpdatedAt: now})
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	task, err := domain.NewTask(domain.NewTaskID(), room.ID(), "Build planning", "Expose immutable planning", []domain.AcceptanceCriterion{criterion})
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	selection := sha256.Sum256([]byte("frozen selection"))
	draft, err := domain.NewTechnicalPlanDraft(domain.NewTechnicalPlanDraftParams{ID: domain.NewTechnicalPlanDraftID(), TaskID: task.ID(), SelectionDigest: selection, Content: domain.TechnicalPlanContent{TechnicalSteps: []string{"inspect", "build"}, Decisions: []string{"bounded"}, Risks: []string{"risk"}, Unknowns: []string{"unknown"}}, CreatedAt: now})
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.InsertRoom(ctx, room); err != nil {
			return err
		}
		return tx.InsertTask(ctx, task)
	}); err != nil {
		db.Close()
		t.Fatal(err)
	}
	return db, task, draft
}
