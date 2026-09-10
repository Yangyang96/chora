package domain

import (
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"
)

func candidateFixture(t *testing.T) Candidate {
	t.Helper()
	candidate, err := NewCandidate(CandidateParams{
		ID: NewCandidateID(), RoomID: NewRoomID(), SourceRunID: NewRunID(), SourceReviewID: NewReviewDecisionID(), SourceArtifactID: NewArtifactID(),
		Title: "Agent finding", Body: "The accepted Run established a durable result.", CreatedAt: time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("NewCandidate() error = %v", err)
	}
	return candidate
}

func TestCandidateContentLimitsApplyToProposalAndEdit(t *testing.T) {
	base := candidateFixture(t)
	params := CandidateParams{ID: NewCandidateID(), RoomID: NewRoomID(), SourceRunID: NewRunID(), SourceReviewID: NewReviewDecisionID(), SourceArtifactID: NewArtifactID(), Title: strings.Repeat("t", MaxCandidateTitleRunes+1), Body: "body", CreatedAt: base.CreatedAt()}
	if _, err := NewCandidate(params); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("oversized Candidate title accepted: %v", err)
	}
	if _, err := base.Edit(base.Version(), "title", strings.Repeat("b", MaxCandidateBodyRunes+1), base.CreatedAt().Add(time.Minute)); !errors.Is(err, ErrInvalidCandidateTransition) {
		t.Fatalf("oversized Candidate body edit accepted: %v", err)
	}
}

func TestTrustedContextTextValidationUsesUnicodeRunesTrimSpaceAndRejectsNUL(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		maxRunes int
		required bool
		want     bool
	}{
		{name: "required text", value: " trusted ", maxRunes: 7, required: true, want: true},
		{name: "optional empty", value: "", maxRunes: 1, required: false, want: true},
		{name: "ascii whitespace required", value: " \t\n", maxRunes: 3, required: true, want: false},
		{name: "nbsp required", value: "\u00a0\u00a0", maxRunes: 2, required: true, want: false},
		{name: "fullwidth space required", value: "\u3000\u3000", maxRunes: 2, required: true, want: false},
		{name: "unicode exact limit", value: "界界", maxRunes: 2, required: true, want: true},
		{name: "unicode over limit", value: "界界界", maxRunes: 2, required: true, want: false},
		{name: "nul", value: "a\x00b", maxRunes: 3, required: true, want: false},
		{name: "nul before oversized suffix", value: "a\x00" + strings.Repeat("界", 3), maxRunes: 2, required: true, want: false},
		{name: "invalid UTF-8", value: string([]byte{0xff}), maxRunes: 1, required: true, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidTrustedContextText(tt.value, tt.maxRunes, tt.required); got != tt.want {
				t.Fatalf("ValidTrustedContextText(%q, %d, %t) = %t, want %t", tt.value, tt.maxRunes, tt.required, got, tt.want)
			}
		})
	}
}

func TestCandidateEditAndConfirmAreExplicitFailClosedTransitions(t *testing.T) {
	candidate := candidateFixture(t)
	editedAt := candidate.CreatedAt().Add(time.Minute)
	edited, err := candidate.Edit(candidate.Version(), "Human-edited title", "Human-edited trusted content.", editedAt)
	if err != nil {
		t.Fatalf("Edit() error = %v", err)
	}
	if candidate.Title() == edited.Title() || candidate.Version() != 1 || edited.Version() != 2 {
		t.Fatalf("edit mutated history or did not advance version: before=%#v after=%#v", candidate, edited)
	}
	if _, err := edited.Edit(1, "stale", "stale", editedAt); !errors.Is(err, ErrInvalidCandidateTransition) {
		t.Fatalf("stale Edit() error = %v, want ErrInvalidCandidateTransition", err)
	}

	decision, confirmed, err := NewCandidateDecision(NewCandidateDecisionID(), edited, CandidateDecisionConfirm, edited.Version(), "confirmed by human", "owner", "browser", editedAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("NewCandidateDecision(confirm) error = %v", err)
	}
	if decision.Kind() != CandidateDecisionConfirm || confirmed.State() != CandidateStateConfirmed || edited.State() != CandidateStatePending {
		t.Fatalf("confirm transition = decision %#v candidate %#v original %#v", decision, confirmed, edited)
	}
	if _, _, err := NewCandidateDecision(NewCandidateDecisionID(), confirmed, CandidateDecisionConfirm, confirmed.Version(), "again", "owner", "browser", editedAt.Add(2*time.Minute)); !errors.Is(err, ErrInvalidCandidateTransition) {
		t.Fatalf("duplicate confirm error = %v, want ErrInvalidCandidateTransition", err)
	}
}

func TestCandidateDismissPreservesAuditStateWithoutRevision(t *testing.T) {
	candidate := candidateFixture(t)
	decision, dismissed, err := NewCandidateDecision(NewCandidateDecisionID(), candidate, CandidateDecisionDismiss, candidate.Version(), "not useful for this Room", "owner", "browser", candidate.CreatedAt().Add(time.Minute))
	if err != nil {
		t.Fatalf("NewCandidateDecision(dismiss) error = %v", err)
	}
	if decision.Kind() != CandidateDecisionDismiss || dismissed.State() != CandidateStateDismissed || decision.Note() == "" {
		t.Fatalf("dismissal not preserved: decision=%#v candidate=%#v", decision, dismissed)
	}
}

func TestCandidateDecisionNoteUnicodeBoundaryAppliesToConstructAndRestore(t *testing.T) {
	limit := strings.Repeat("界", MaxCandidateDecisionNoteRunes)
	overLimit := limit + "界"
	for _, kind := range []CandidateDecisionKind{CandidateDecisionConfirm, CandidateDecisionDismiss} {
		candidate := candidateFixture(t)
		decision, _, err := NewCandidateDecision(NewCandidateDecisionID(), candidate, kind, candidate.Version(), limit, "owner", "browser", candidate.CreatedAt().Add(time.Minute))
		if err != nil {
			t.Fatalf("kind %q rejected exact-limit note: %v", kind, err)
		}
		if _, err := RestoreCandidateDecision(decision.ID(), candidate.ID(), kind, candidate.Version(), limit, "owner", "browser", decision.DecidedAt()); err != nil {
			t.Fatalf("kind %q restore rejected exact-limit note: %v", kind, err)
		}

		candidate = candidateFixture(t)
		if _, _, err := NewCandidateDecision(NewCandidateDecisionID(), candidate, kind, candidate.Version(), overLimit, "owner", "browser", candidate.CreatedAt().Add(time.Minute)); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("kind %q accepted over-limit note: %v", kind, err)
		}
		if _, err := RestoreCandidateDecision(NewCandidateDecisionID(), candidate.ID(), kind, candidate.Version(), overLimit, "owner", "browser", candidate.CreatedAt().Add(time.Minute)); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("kind %q restore accepted over-limit note: %v", kind, err)
		}
	}
}

func TestRoomRevisionFreezesCandidateContentProvenanceAndDigest(t *testing.T) {
	candidate := candidateFixture(t)
	decision, confirmed, err := NewCandidateDecision(NewCandidateDecisionID(), candidate, CandidateDecisionConfirm, candidate.Version(), "confirm", "owner", "browser", candidate.CreatedAt().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	confirmedAt := decision.DecidedAt()
	contextRevision, err := NewRoomContextRevision(RoomContextRevisionParams{
		EntryID: NewContextEntryID(), RevisionID: NewContextRevisionID(), RoomID: confirmed.RoomID(), Kind: ContextKindDecision, RevisionNumber: 1,
		Title: confirmed.Title(), Body: confirmed.Body(), CreatedAt: confirmedAt, UpdatedAt: confirmedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	revision, err := NewRoomRevision(contextRevision, CandidateProvenance(confirmed, decision, "local-human"), confirmedAt)
	if err != nil {
		t.Fatalf("NewRoomRevision() error = %v", err)
	}
	if revision.Digest() == ([32]byte{}) || revision.Provenance().CandidateID != candidate.ID().String() || revision.Provenance().SourceRunID != candidate.SourceRunID().String() {
		t.Fatalf("revision does not freeze digest/provenance: %#v", revision)
	}
	if _, err := RestoreRoomRevision(contextRevision, revision.Provenance(), [32]byte{1}, confirmedAt); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("tampered digest error = %v, want ErrInvalidArgument", err)
	}
}

func TestTaskRevisionSelectionRequiresExplicitPinsAndCopiesManifest(t *testing.T) {
	item := RevisionSelectionItem{RevisionID: NewContextRevisionID(), Digest: [32]byte{1}, Provenance: HumanRoomProvenance("local-human")}
	if _, err := NewTaskRevisionSelection(NewTaskID(), NewRoomID(), nil, nil, time.Now().UTC()); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("empty selection error = %v, want ErrInvalidArgument", err)
	}
	selected := []RevisionSelectionItem{item}
	selection, err := NewTaskRevisionSelection(NewTaskID(), NewRoomID(), selected, nil, time.Now().UTC())
	if err != nil {
		t.Fatalf("NewTaskRevisionSelection() error = %v", err)
	}
	selected[0].Digest = [32]byte{9}
	returned := selection.Selected()
	returned[0].Digest = [32]byte{8}
	if selection.Selected()[0].Digest != item.Digest {
		t.Fatal("selection exposes mutable manifest state")
	}
	digest := selection.Digest()
	if digest == ([32]byte{}) || digest != sha256.Sum256(selection.CanonicalJSON()) {
		t.Fatalf("selection digest = %x", digest)
	}
	restored, err := NewTaskRevisionSelection(selection.TaskID(), selection.RoomID(), selection.Selected(), selection.Excluded(), selection.CreatedAt())
	if err != nil || restored.Digest() != digest {
		t.Fatalf("restored selection digest = %x, err=%v", restored.Digest(), err)
	}
}
