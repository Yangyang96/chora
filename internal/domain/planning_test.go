package domain

import (
	"crypto/sha256"
	"errors"
	"testing"
	"time"
)

func TestTechnicalPlanDraftEditCASAndImmutableSubmission(t *testing.T) {
	now := time.Date(2026, 8, 17, 1, 2, 3, 0, time.UTC)
	selection := sha256.Sum256([]byte("frozen room selection"))
	draft, err := NewTechnicalPlanDraft(NewTechnicalPlanDraftParams{
		ID: NewTechnicalPlanDraftID(), TaskID: NewTaskID(), SelectionDigest: selection,
		Content:   TechnicalPlanContent{TechnicalSteps: []string{"inspect", "build"}, Decisions: []string{"bounded"}, Risks: []string{"risk"}, Unknowns: []string{"unknown"}},
		CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	edited, err := draft.Edit(1, TechnicalPlanContent{TechnicalSteps: []string{"inspect", "build", "verify"}, Decisions: draft.Content().Decisions, Risks: draft.Content().Risks, Unknowns: draft.Content().Unknowns}, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if edited.EditVersion() != 2 || edited.Content().TechnicalSteps[2] != "verify" || !edited.Open() {
		t.Fatalf("edited=%#v", edited)
	}
	if _, err := draft.Edit(2, draft.Content(), now.Add(time.Second)); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("stale edit err=%v", err)
	}
	closed, revision, err := edited.Submit(2, NewTechnicalPlanRevisionID(), false, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if closed.Open() || closed.SubmittedRevisionID() != revision.ID() || revision.RevisionNumber() != 1 || revision.PredecessorRevisionID().Valid() || revision.Unchanged() || revision.ContentDigest() == ([32]byte{}) || revision.SelectionDigest() != selection {
		t.Fatalf("closed=%#v revision=%#v", closed, revision)
	}
	copy := revision.Content()
	copy.TechnicalSteps[0] = "mutated"
	if revision.Content().TechnicalSteps[0] != "inspect" {
		t.Fatal("revision content leaked mutable storage")
	}
}

func TestSuccessorDraftRequiresExplicitUnchangedConfirmation(t *testing.T) {
	now := time.Date(2026, 8, 17, 1, 2, 3, 0, time.UTC)
	selection := sha256.Sum256([]byte("selection"))
	first, _ := NewTechnicalPlanDraft(NewTechnicalPlanDraftParams{ID: NewTechnicalPlanDraftID(), TaskID: NewTaskID(), SelectionDigest: selection, Content: samplePlanContent(), CreatedAt: now})
	_, revision1, _ := first.Submit(1, NewTechnicalPlanRevisionID(), false, now.Add(time.Second))
	successor, err := NewTechnicalPlanSuccessorDraft(NewTechnicalPlanDraftID(), revision1, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := successor.Submit(1, NewTechnicalPlanRevisionID(), false, now.Add(3*time.Second)); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("unchanged without confirmation err=%v", err)
	}
	closed, revision2, err := successor.Submit(1, NewTechnicalPlanRevisionID(), true, now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if closed.Open() || !revision2.Unchanged() || revision2.RevisionNumber() != 2 || revision2.PredecessorRevisionID() != revision1.ID() || revision2.ContentDigest() != revision1.ContentDigest() {
		t.Fatalf("closed=%#v revision2=%#v", closed, revision2)
	}
}

func TestTechnicalPlanReviewAndBindingsAreExact(t *testing.T) {
	now := time.Date(2026, 8, 17, 1, 2, 3, 0, time.UTC)
	selection := sha256.Sum256([]byte("selection"))
	draft, _ := NewTechnicalPlanDraft(NewTechnicalPlanDraftParams{ID: NewTechnicalPlanDraftID(), TaskID: NewTaskID(), SelectionDigest: selection, Content: samplePlanContent(), CreatedAt: now})
	_, revision, _ := draft.Submit(1, NewTechnicalPlanRevisionID(), false, now.Add(time.Second))
	review, err := NewTechnicalPlanReview(NewTechnicalPlanReviewParams{ID: NewTechnicalPlanReviewID(), RevisionID: revision.ID(), TaskID: revision.TaskID(), Kind: TechnicalPlanReviewAccept, Reviewer: " owner ", Note: " exact revision ", DecidedAt: now.Add(2 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	snapshotDigest := sha256.Sum256([]byte("snapshot"))
	acceptance, err := NewTechnicalPlanAcceptanceBinding(TechnicalPlanAcceptanceBindingRecord{TaskID: revision.TaskID(), RevisionID: revision.ID(), ReviewID: review.ID(), SnapshotID: NewContextSnapshotID(), SnapshotDigest: snapshotDigest, BoundAt: now.Add(3 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	run, err := NewTechnicalPlanRunBinding(TechnicalPlanRunBindingRecord{RunID: NewRunID(), TaskID: revision.TaskID(), RevisionID: revision.ID(), CharterID: NewCharterID(), SnapshotID: acceptance.SnapshotID(), SnapshotDigest: acceptance.SnapshotDigest(), BoundAt: now.Add(4 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if review.Reviewer() != "owner" || review.Note() != "exact revision" || acceptance.RevisionID() != revision.ID() || run.RevisionID() != revision.ID() || run.SnapshotDigest() != snapshotDigest {
		t.Fatalf("review=%#v acceptance=%#v run=%#v", review, acceptance, run)
	}
}

func samplePlanContent() TechnicalPlanContent {
	return TechnicalPlanContent{TechnicalSteps: []string{"build"}, Decisions: []string{"choose"}, Risks: []string{"risk"}, Unknowns: []string{"unknown"}}
}
