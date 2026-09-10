package contextcore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
)

type fakePort struct {
	revisions       []domain.RoomContextRevision
	saved           []Snapshot
	promotions      []PromotionRecord
	lookupErr       error
	saveErr         error
	lookupCalls     int
	saveCalls       int
	promoteFn       func(PromoteReviewedRequest) (PromoteReviewedResult, error)
	promoteCalls    int
	promoteRequests []PromoteReviewedRequest
}

func (port *fakePort) LookupRevisions(context.Context, domain.RoomID, []domain.ContextRevisionID) ([]domain.RoomContextRevision, error) {
	port.lookupCalls++
	if port.lookupErr != nil {
		return nil, port.lookupErr
	}
	return append([]domain.RoomContextRevision(nil), port.revisions...), nil
}

func (port *fakePort) SaveSnapshot(_ context.Context, snapshot Snapshot) error {
	port.saveCalls++
	if port.saveErr != nil {
		return port.saveErr
	}
	port.saved = append(port.saved, snapshot)
	return nil
}

func (port *fakePort) PromoteReviewed(_ context.Context, request PromoteReviewedRequest) (PromoteReviewedResult, error) {
	port.promoteCalls++
	port.promoteRequests = append(port.promoteRequests, request)
	if port.promoteFn != nil {
		result, err := port.promoteFn(request)
		if err == nil && (result.Outcome == PromotionOutcomeApplied || result.Outcome == PromotionOutcomeAlreadyApplied) {
			port.promotions = append(port.promotions, result.Record)
		}
		return result, err
	}
	return PromoteReviewedResult{}, nil
}

func TestAssembleInitialSnapshotMatchesGoldenAndCanonicalJSON(t *testing.T) {
	fixture := newAssemblyFixture(t, false)
	port := &fakePort{revisions: []domain.RoomContextRevision{fixture.unknown, fixture.brief, fixture.reference, fixture.constraint, fixture.decision}}
	assembler := NewAssembler(port)

	snapshot, err := assembler.Assemble(context.Background(), AssembleRequest{
		SnapshotID: fixture.snapshotID,
		Task:       fixture.task,
		Charter:    fixture.charter,
	})
	if err != nil {
		t.Fatalf("Assemble() error = %v", err)
	}

	wantMarkdown := readGolden(t, "initial.golden.md")
	if got := snapshot.Markdown(); !bytes.Equal(got, wantMarkdown) {
		t.Fatalf("Markdown mismatch\n--- got ---\n%s--- want ---\n%s", got, wantMarkdown)
	}
	assertRunCharterMarkdown(t, snapshot.Markdown())
	wantJSON := `{"schema_version":"chora.context.snapshot/v1","task":{"id":"task_018f0000-0000-7002-8000-000000000002","room_id":"room_018f0000-0000-7001-8000-000000000001","title":"Build deterministic context","goal":"Assemble a frozen context snapshot","briefs":[{"entry_id":"context_entry_018f0000-0000-7010-8000-000000000010","revision_id":"context_revision_018f0000-0000-7020-8000-000000000020","revision_number":1,"title":"Project brief","body":"Chora is a human-agent workspace.","locator":"docs/brief.md","sensitive":false}]},"charter":{"id":"charter_018f0000-0000-7005-8000-000000000005","task_id":"task_018f0000-0000-7002-8000-000000000002","task_goal":"Assemble a frozen context snapshot","workspace_root":"/tmp/chora","adapter_id":"codex","sandbox_mode":"workspace-write","expected_output":"verified context snapshot","responsible_human":"Yang Yang","capabilities":[{"name":"network","allowed":false},{"name":"read_workspace","allowed":true}],"initiator":"human","created_at":"2026-07-29T00:00:00Z","confirmed_sensitive_revision_ids":[],"sensitive_exclusions":[]},"acceptance_criteria":[{"id":"criterion_018f0000-0000-7003-8000-000000000003","title":"deterministic","description":"same input has the same bytes"},{"id":"criterion_018f0000-0000-7004-8000-000000000004","title":"reviewed","description":"promotion requires review"}],"decisions":[{"entry_id":"context_entry_018f0000-0000-7011-8000-000000000011","revision_id":"context_revision_018f0000-0000-7021-8000-000000000021","revision_number":1,"title":"Storage","body":"Use canonical JSON.","locator":"docs/design.md","sensitive":false}],"constraints":[{"entry_id":"context_entry_018f0000-0000-7012-8000-000000000012","revision_id":"context_revision_018f0000-0000-7022-8000-000000000022","revision_number":1,"title":"Boundary","body":"Do not add a fact system.","locator":"","sensitive":false}],"references":[{"entry_id":"context_entry_018f0000-0000-7013-8000-000000000013","revision_id":"context_revision_018f0000-0000-7023-8000-000000000023","revision_number":1,"title":"Blueprint","body":"Architecture source.","locator":"BLUEPRINT.md","sensitive":false}],"unknowns":[{"entry_id":"context_entry_018f0000-0000-7014-8000-000000000014","revision_id":"context_revision_018f0000-0000-7024-8000-000000000024","revision_number":1,"title":"Future adapter","body":"SQLite arrives in Task 5.","locator":"","sensitive":false}],"excluded":[],"delta":null}`
	if got := string(snapshot.CanonicalJSON()); got != wantJSON {
		t.Fatalf("CanonicalJSON mismatch\n got: %s\nwant: %s", got, wantJSON)
	}
	if want := sha256.Sum256([]byte(wantJSON)); snapshot.Digest() != want {
		t.Fatalf("Digest() = %x, want %x", snapshot.Digest(), want)
	}
	if got := snapshot.DomainSnapshot(); got.ID() != fixture.snapshotID || got.Digest() != snapshot.Digest() {
		t.Fatalf("DomainSnapshot() = %#v", got)
	}
	if len(port.saved) != 1 || !bytes.Equal(port.saved[0].CanonicalJSON(), snapshot.CanonicalJSON()) || !bytes.Equal(port.saved[0].Markdown(), snapshot.Markdown()) {
		t.Fatalf("saved snapshots = %#v", port.saved)
	}
}

func TestAssembleIsByteDeterministicAndDefensivelyCopies(t *testing.T) {
	first := newAssemblyFixture(t, false)
	second := newAssemblyFixture(t, true)
	second.snapshotID = mustSnapshotID(t, 0x08)
	portA := &fakePort{revisions: []domain.RoomContextRevision{first.decision, first.constraint, first.reference, first.unknown, first.brief}}
	portB := &fakePort{revisions: []domain.RoomContextRevision{second.reference, second.brief, second.unknown, second.decision, second.constraint}}

	a, err := NewAssembler(portA).Assemble(context.Background(), AssembleRequest{SnapshotID: first.snapshotID, Task: first.task, Charter: first.charter})
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewAssembler(portB).Assemble(context.Background(), AssembleRequest{SnapshotID: second.snapshotID, Task: second.task, Charter: second.charter})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.CanonicalJSON(), b.CanonicalJSON()) || !bytes.Equal(a.Markdown(), b.Markdown()) || a.Digest() != b.Digest() {
		t.Fatal("same logical input produced different renderings")
	}
	assertRunCharterMarkdown(t, a.Markdown())
	assertRunCharterMarkdown(t, b.Markdown())

	jsonCopy := a.CanonicalJSON()
	markdownCopy := a.Markdown()
	included := a.IncludedRevisionIDs()
	jsonCopy[0] = 'X'
	markdownCopy[0] = 'X'
	included[0] = domain.NewContextRevisionID()
	if a.CanonicalJSON()[0] == 'X' || a.Markdown()[0] == 'X' || a.IncludedRevisionIDs()[0] != first.brief.RevisionID() {
		t.Fatal("Snapshot aliases caller-owned bytes or slices")
	}
}

func TestAssembleRetrySnapshotReferencesPredecessorAndDelta(t *testing.T) {
	fixture := newAssemblyFixture(t, false)
	port := &fakePort{revisions: fixture.revisions()}
	predecessorDigest := [32]byte{}
	for index := range predecessorDigest {
		predecessorDigest[index] = 0x20
	}
	snapshot, err := NewAssembler(port).Assemble(context.Background(), AssembleRequest{
		SnapshotID: fixture.snapshotID,
		Task:       fixture.task,
		Charter:    fixture.charter,
		Delta: &RetryDelta{
			PredecessorSnapshotID: fixture.predecessorID,
			PredecessorDigest:     predecessorDigest,
			Reason:                "Tests failed",
			Instructions:          "Fix the deterministic renderer and rerun tests.",
		},
	})
	if err != nil {
		t.Fatalf("Assemble() error = %v", err)
	}
	if got, want := snapshot.Markdown(), readGolden(t, "retry.golden.md"); !bytes.Equal(got, want) {
		t.Fatalf("retry Markdown mismatch\n--- got ---\n%s--- want ---\n%s", got, want)
	}
	assertRunCharterMarkdown(t, snapshot.Markdown())
	json := string(snapshot.CanonicalJSON())
	for _, required := range []string{fixture.predecessorID.String(), strings.Repeat("20", 32), `"reason":"Tests failed"`, `"instructions":"Fix the deterministic renderer and rerun tests."`} {
		if !strings.Contains(json, required) {
			t.Fatalf("retry JSON missing %q: %s", required, json)
		}
	}

	invalid := []RetryDelta{
		{},
		{PredecessorSnapshotID: fixture.predecessorID, PredecessorDigest: predecessorDigest, Instructions: "fix"},
		{PredecessorSnapshotID: fixture.predecessorID, PredecessorDigest: predecessorDigest, Reason: "failed"},
	}
	for _, delta := range invalid {
		if _, err := NewAssembler(port).Assemble(context.Background(), AssembleRequest{SnapshotID: fixture.snapshotID, Task: fixture.task, Charter: fixture.charter, Delta: &delta}); !errors.Is(err, ErrInvalidAssembly) {
			t.Fatalf("invalid retry error = %v, want ErrInvalidAssembly", err)
		}
	}
}

func TestAssembleRejectsRetrySnapshotAsItsOwnPredecessor(t *testing.T) {
	fixture := newAssemblyFixture(t, false)
	port := &fakePort{revisions: fixture.revisions()}
	digest := [32]byte{1}
	snapshot, err := NewAssembler(port).Assemble(context.Background(), AssembleRequest{
		SnapshotID: fixture.snapshotID,
		Task:       fixture.task,
		Charter:    fixture.charter,
		Delta: &RetryDelta{
			PredecessorSnapshotID: fixture.snapshotID,
			PredecessorDigest:     digest,
			Reason:                "retry",
			Instructions:          "try again",
		},
	})
	if !errors.Is(err, ErrInvalidAssembly) {
		t.Fatalf("error = %v, want ErrInvalidAssembly", err)
	}
	if snapshot.ID().Valid() || len(snapshot.CanonicalJSON()) != 0 || port.lookupCalls != 0 || port.saveCalls != 0 {
		t.Fatalf("self-predecessor returned partial success: snapshot=%#v lookup=%d save=%d", snapshot, port.lookupCalls, port.saveCalls)
	}
}

func TestAssembleRejectsTaskAndCharterCriteriaDrift(t *testing.T) {
	fixture := newAssemblyFixture(t, false)
	criteria := fixture.task.Criteria()
	changedTitle := mustCriterion(t, 0x03, "changed title", criteria[0].Description())
	changedDescription := mustCriterion(t, 0x03, criteria[0].Title(), "changed description")
	tests := []struct {
		name     string
		criteria []domain.AcceptanceCriterion
	}{
		{"different ID", []domain.AcceptanceCriterion{mustCriterion(t, 0x30, criteria[0].Title(), criteria[0].Description()), criteria[1]}},
		{"different order", []domain.AcceptanceCriterion{criteria[1], criteria[0]}},
		{"different title", []domain.AcceptanceCriterion{changedTitle, criteria[1]}},
		{"different description", []domain.AcceptanceCriterion{changedDescription, criteria[1]}},
		{"different length", []domain.AcceptanceCriterion{criteria[0]}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			task, err := domain.NewTask(fixture.task.ID(), fixture.task.RoomID(), fixture.task.Title(), fixture.task.Goal(), test.criteria)
			if err != nil {
				t.Fatal(err)
			}
			port := &fakePort{revisions: fixture.revisions()}
			snapshot, err := NewAssembler(port).Assemble(context.Background(), AssembleRequest{SnapshotID: fixture.snapshotID, Task: task, Charter: fixture.charter})
			if !errors.Is(err, ErrInvalidAssembly) {
				t.Fatalf("error = %v, want ErrInvalidAssembly", err)
			}
			if snapshot.ID().Valid() || len(snapshot.Markdown()) != 0 || port.lookupCalls != 0 || port.saveCalls != 0 {
				t.Fatalf("criteria drift returned partial success: snapshot=%#v lookup=%d save=%d", snapshot, port.lookupCalls, port.saveCalls)
			}
		})
	}
}

func TestAssemblePropagatesPortErrorsWithoutPartialSuccess(t *testing.T) {
	fixture := newAssemblyFixture(t, false)
	t.Run("lookup cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		port := &fakePort{lookupErr: ctx.Err()}
		snapshot, err := NewAssembler(port).Assemble(ctx, AssembleRequest{SnapshotID: fixture.snapshotID, Task: fixture.task, Charter: fixture.charter})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
		if snapshot.ID().Valid() || len(snapshot.CanonicalJSON()) != 0 || port.lookupCalls != 1 || port.saveCalls != 0 || len(port.saved) != 0 {
			t.Fatalf("lookup error returned partial success: snapshot=%#v port=%#v", snapshot, port)
		}
	})
	t.Run("save failure", func(t *testing.T) {
		saveErr := errors.New("save failed")
		port := &fakePort{revisions: fixture.revisions(), saveErr: saveErr}
		snapshot, err := NewAssembler(port).Assemble(context.Background(), AssembleRequest{SnapshotID: fixture.snapshotID, Task: fixture.task, Charter: fixture.charter})
		if !errors.Is(err, saveErr) {
			t.Fatalf("error = %v, want wrapped save error", err)
		}
		if snapshot.ID().Valid() || len(snapshot.Markdown()) != 0 || port.lookupCalls != 1 || port.saveCalls != 1 || len(port.saved) != 0 {
			t.Fatalf("save error returned partial success: snapshot=%#v port=%#v", snapshot, port)
		}
	})
}

func TestCharterBindingFieldsAffectCanonicalOutputsInStableOrder(t *testing.T) {
	fixture := newAssemblyFixture(t, false)
	base, err := NewAssembler(&fakePort{revisions: fixture.revisions()}).Assemble(context.Background(), AssembleRequest{SnapshotID: fixture.snapshotID, Task: fixture.task, Charter: fixture.charter})
	if err != nil {
		t.Fatal(err)
	}
	createdAt := fixture.charter.CreatedAt().Add(123 * time.Nanosecond)
	changedTime := mustCharterCustom(t, fixture, fixture.task.Criteria(), fixture.charter.ContextRevisionIDs(), nil, nil, false, createdAt)
	changed, err := NewAssembler(&fakePort{revisions: fixture.revisions()}).Assemble(context.Background(), AssembleRequest{SnapshotID: fixture.snapshotID, Task: fixture.task, Charter: changedTime})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(base.CanonicalJSON(), changed.CanonicalJSON()) || bytes.Equal(base.Markdown(), changed.Markdown()) || base.Digest() == changed.Digest() {
		t.Fatal("CreatedAt change did not affect all canonical outputs")
	}
	if !bytes.Contains(changed.CanonicalJSON(), []byte(`"created_at":"2026-07-29T00:00:00.000000123Z"`)) || !bytes.Contains(changed.Markdown(), []byte("- Created At: `2026-07-29T00:00:00.000000123Z`")) {
		t.Fatalf("CreatedAt is not rendered with UTC RFC3339Nano: json=%s markdown=%s", changed.CanonicalJSON(), changed.Markdown())
	}

	sensitiveA := mustRevision(t, revisionSpec{entry: 0x15, revision: 0x25, room: 0x01, kind: domain.ContextKindSourceRef, title: "Sensitive A", body: "A", sensitive: true})
	sensitiveB := mustRevision(t, revisionSpec{entry: 0x16, revision: 0x26, room: 0x01, kind: domain.ContextKindUnknown, title: "Sensitive B", body: "B", sensitive: true})
	revisions := []domain.RoomContextRevision{fixture.brief, fixture.decision, sensitiveA, sensitiveB}
	pinned := []domain.ContextRevisionID{fixture.brief.RevisionID(), fixture.decision.RevisionID(), sensitiveA.RevisionID(), sensitiveB.RevisionID()}
	confirmed := []domain.ContextRevisionID{sensitiveB.RevisionID(), sensitiveA.RevisionID()}
	exclusions := []domain.ContextEntryID{fixture.decision.EntryID(), fixture.brief.EntryID()}
	charter := mustCharterCustom(t, fixture, fixture.task.Criteria(), pinned, confirmed, exclusions, false, fixture.charter.CreatedAt())
	snapshot, err := NewAssembler(&fakePort{revisions: revisions}).Assemble(context.Background(), AssembleRequest{SnapshotID: fixture.snapshotID, Task: fixture.task, Charter: charter})
	if err != nil {
		t.Fatal(err)
	}
	wantConfirmedJSON := `"confirmed_sensitive_revision_ids":["` + sensitiveB.RevisionID().String() + `","` + sensitiveA.RevisionID().String() + `"]`
	wantExclusionsJSON := `"sensitive_exclusions":["` + fixture.decision.EntryID().String() + `","` + fixture.brief.EntryID().String() + `"]`
	if !strings.Contains(string(snapshot.CanonicalJSON()), wantConfirmedJSON) || !strings.Contains(string(snapshot.CanonicalJSON()), wantExclusionsJSON) {
		t.Fatalf("charter ID slices lost frozen order: %s", snapshot.CanonicalJSON())
	}
	wantMarkdown := "- Confirmed Sensitive Revision IDs:\n  - `" + sensitiveB.RevisionID().String() + "`\n  - `" + sensitiveA.RevisionID().String() + "`\n- Sensitive Exclusions:\n  - `" + fixture.decision.EntryID().String() + "`\n  - `" + fixture.brief.EntryID().String() + "`"
	if !strings.Contains(string(snapshot.Markdown()), wantMarkdown) {
		t.Fatalf("charter ID slices lost frozen Markdown order: %s", snapshot.Markdown())
	}

	unconfirmedCharter := mustCharterCustom(t, fixture, fixture.task.Criteria(), pinned, nil, exclusions, false, fixture.charter.CreatedAt())
	unconfirmed, err := NewAssembler(&fakePort{revisions: revisions}).Assemble(context.Background(), AssembleRequest{SnapshotID: fixture.snapshotID, Task: fixture.task, Charter: unconfirmedCharter})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(unconfirmed.CanonicalJSON(), snapshot.CanonicalJSON()) || bytes.Equal(unconfirmed.Markdown(), snapshot.Markdown()) || unconfirmed.Digest() == snapshot.Digest() {
		t.Fatal("ConfirmedSensitiveRevisionIDs change did not affect all canonical outputs")
	}

	excludedCharter := mustCharterCustom(t, fixture, fixture.task.Criteria(), fixture.charter.ContextRevisionIDs(), nil, []domain.ContextEntryID{fixture.brief.EntryID()}, false, fixture.charter.CreatedAt())
	excluded, err := NewAssembler(&fakePort{revisions: fixture.revisions()}).Assemble(context.Background(), AssembleRequest{SnapshotID: fixture.snapshotID, Task: fixture.task, Charter: excludedCharter})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(base.CanonicalJSON(), excluded.CanonicalJSON()) || bytes.Equal(base.Markdown(), excluded.Markdown()) || base.Digest() == excluded.Digest() {
		t.Fatal("SensitiveExclusions change did not affect all canonical outputs")
	}
}

func TestAssembleUsesPinnedOrderAndFailsClosedOnRevisionAmbiguity(t *testing.T) {
	fixture := newAssemblyFixture(t, false)
	assembler := NewAssembler(&fakePort{revisions: []domain.RoomContextRevision{fixture.unknown, fixture.decision, fixture.brief, fixture.reference, fixture.constraint}})
	snapshot, err := assembler.Assemble(context.Background(), AssembleRequest{SnapshotID: fixture.snapshotID, Task: fixture.task, Charter: fixture.charter})
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshot.IncludedRevisionIDs(); len(got) != 5 || got[0] != fixture.brief.RevisionID() || got[4] != fixture.unknown.RevisionID() {
		t.Fatalf("included order = %v", got)
	}

	wrongRoom := mustRevision(t, revisionSpec{entry: 0x11, revision: 0x21, room: 0x99, kind: domain.ContextKindDecision, title: "Storage", body: "Use canonical JSON.", locator: "docs/design.md"})
	newerSameEntry := mustRevision(t, revisionSpec{entry: 0x11, revision: 0x31, room: 0x01, kind: domain.ContextKindDecision, number: 2, title: "Storage v2", body: "Unpinned", supersedes: fixture.decision.RevisionID()})
	tests := []struct {
		name      string
		revisions []domain.RoomContextRevision
	}{
		{"missing", fixture.revisions()[1:]},
		{"duplicate", append(fixture.revisions(), fixture.decision)},
		{"wrong room", []domain.RoomContextRevision{fixture.brief, wrongRoom, fixture.constraint, fixture.reference, fixture.unknown}},
		{"unpinned newer revision", []domain.RoomContextRevision{fixture.brief, newerSameEntry, fixture.constraint, fixture.reference, fixture.unknown}},
		{"extra", append(fixture.revisions(), newerSameEntry)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewAssembler(&fakePort{revisions: test.revisions}).Assemble(context.Background(), AssembleRequest{SnapshotID: fixture.snapshotID, Task: fixture.task, Charter: fixture.charter})
			if !errors.Is(err, ErrRevisionSetMismatch) {
				t.Fatalf("error = %v, want ErrRevisionSetMismatch", err)
			}
		})
	}
}

func TestSensitiveRevisionIsExcludedUnlessPreciselyConfirmed(t *testing.T) {
	fixture := newAssemblyFixture(t, false)
	sensitive := mustRevision(t, revisionSpec{entry: 0x15, revision: 0x25, room: 0x01, kind: domain.ContextKindSourceRef, title: "Secret", body: "do-not-leak-body", locator: "do-not-leak-locator", sensitive: true})

	selected := []domain.ContextRevisionID{fixture.brief.RevisionID(), sensitive.RevisionID()}
	revisions := []domain.RoomContextRevision{sensitive, fixture.brief}
	excludedCharter := mustCharter(t, fixture, selected, nil, nil, false)
	excluded, err := NewAssembler(&fakePort{revisions: revisions}).Assemble(context.Background(), AssembleRequest{SnapshotID: fixture.snapshotID, Task: fixture.task, Charter: excludedCharter})
	if err != nil {
		t.Fatal(err)
	}
	for _, rendered := range [][]byte{excluded.CanonicalJSON(), excluded.Markdown()} {
		if bytes.Contains(rendered, []byte("do-not-leak-body")) || bytes.Contains(rendered, []byte("do-not-leak-locator")) || bytes.Contains(rendered, []byte("Secret")) {
			t.Fatalf("excluded sensitive content leaked: %s", rendered)
		}
	}
	if got := excluded.Excluded(); len(got) != 1 || got[0].EntryID() != sensitive.EntryID() || got[0].RevisionID() != sensitive.RevisionID() {
		t.Fatalf("Excluded() = %#v", got)
	}

	confirmedCharter := mustCharter(t, fixture, selected, []domain.ContextRevisionID{sensitive.RevisionID()}, nil, false)
	confirmed, err := NewAssembler(&fakePort{revisions: revisions}).Assemble(context.Background(), AssembleRequest{SnapshotID: fixture.snapshotID, Task: fixture.task, Charter: confirmedCharter})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(confirmed.CanonicalJSON(), []byte("do-not-leak-body")) {
		t.Fatal("precisely confirmed sensitive revision was not included")
	}

	conflictCharter := mustCharter(t, fixture, selected, []domain.ContextRevisionID{sensitive.RevisionID()}, []domain.ContextEntryID{sensitive.EntryID()}, false)
	if _, err := NewAssembler(&fakePort{revisions: revisions}).Assemble(context.Background(), AssembleRequest{SnapshotID: fixture.snapshotID, Task: fixture.task, Charter: conflictCharter}); !errors.Is(err, ErrSensitiveSelectionConflict) {
		t.Fatalf("conflict error = %v, want ErrSensitiveSelectionConflict", err)
	}
}

func TestAssembleAllowsAllPinnedRevisionsToBeSafelyExcluded(t *testing.T) {
	fixture := newAssemblyFixture(t, false)
	sensitive := mustRevision(t, revisionSpec{entry: 0x15, revision: 0x25, room: 0x01, kind: domain.ContextKindSourceRef, title: "Secret", body: "do-not-leak-body", locator: "do-not-leak-locator", sensitive: true})
	charter := mustCharter(t, fixture, []domain.ContextRevisionID{sensitive.RevisionID()}, nil, nil, false)

	snapshot, err := NewAssembler(&fakePort{revisions: []domain.RoomContextRevision{sensitive}}).Assemble(context.Background(), AssembleRequest{SnapshotID: fixture.snapshotID, Task: fixture.task, Charter: charter})
	if err != nil {
		t.Fatalf("Assemble() error = %v", err)
	}
	if len(snapshot.IncludedRevisionIDs()) != 0 || len(snapshot.DomainSnapshot().IncludedRevisionIDs()) != 0 {
		t.Fatalf("included revisions = %v / %v, want empty", snapshot.IncludedRevisionIDs(), snapshot.DomainSnapshot().IncludedRevisionIDs())
	}
	if got := snapshot.DomainSnapshot().ExcludedEntryIDs(); len(got) != 1 || got[0] != sensitive.EntryID() {
		t.Fatalf("excluded entry IDs = %v", got)
	}
	for _, rendered := range [][]byte{snapshot.CanonicalJSON(), snapshot.Markdown()} {
		if bytes.Contains(rendered, []byte("Secret")) || bytes.Contains(rendered, []byte("do-not-leak-body")) || bytes.Contains(rendered, []byte("do-not-leak-locator")) {
			t.Fatalf("excluded sensitive content leaked: %s", rendered)
		}
	}
}

type assemblyFixture struct {
	roomID, snapshotRoomID                          domain.RoomID
	snapshotID, predecessorID                       domain.ContextSnapshotID
	task                                            domain.Task
	charter                                         domain.RunCharter
	brief, decision, constraint, reference, unknown domain.RoomContextRevision
}

func newAssemblyFixture(t *testing.T, reverseCapabilities bool) assemblyFixture {
	t.Helper()
	roomID := mustRoomID(t, 0x01)
	criteria := []domain.AcceptanceCriterion{
		mustCriterion(t, 0x03, "deterministic", "same input has the same bytes"),
		mustCriterion(t, 0x04, "reviewed", "promotion requires review"),
	}
	task, err := domain.NewTask(mustTaskID(t, 0x02), roomID, "Build deterministic context", "Assemble a frozen context snapshot", criteria)
	if err != nil {
		t.Fatal(err)
	}
	fixture := assemblyFixture{
		roomID: roomID, snapshotID: mustSnapshotID(t, 0x06), predecessorID: mustSnapshotID(t, 0x07), task: task,
		brief:      mustRevision(t, revisionSpec{entry: 0x10, revision: 0x20, room: 0x01, kind: domain.ContextKindBrief, title: "Project brief", body: "Chora is a human-agent workspace.", locator: "docs/brief.md"}),
		decision:   mustRevision(t, revisionSpec{entry: 0x11, revision: 0x21, room: 0x01, kind: domain.ContextKindDecision, title: "Storage", body: "Use canonical JSON.", locator: "docs/design.md"}),
		constraint: mustRevision(t, revisionSpec{entry: 0x12, revision: 0x22, room: 0x01, kind: domain.ContextKindConstraint, title: "Boundary", body: "Do not add a fact system."}),
		reference:  mustRevision(t, revisionSpec{entry: 0x13, revision: 0x23, room: 0x01, kind: domain.ContextKindSourceRef, title: "Blueprint", body: "Architecture source.", locator: "BLUEPRINT.md"}),
		unknown:    mustRevision(t, revisionSpec{entry: 0x14, revision: 0x24, room: 0x01, kind: domain.ContextKindUnknown, title: "Future adapter", body: "SQLite arrives in Task 5."}),
	}
	fixture.charter = mustCharter(t, fixture, []domain.ContextRevisionID{fixture.brief.RevisionID(), fixture.decision.RevisionID(), fixture.constraint.RevisionID(), fixture.reference.RevisionID(), fixture.unknown.RevisionID()}, nil, nil, reverseCapabilities)
	return fixture
}

func (fixture assemblyFixture) revisions() []domain.RoomContextRevision {
	return []domain.RoomContextRevision{fixture.brief, fixture.decision, fixture.constraint, fixture.reference, fixture.unknown}
}

func mustCharter(t *testing.T, fixture assemblyFixture, revisionIDs, confirmed []domain.ContextRevisionID, exclusions []domain.ContextEntryID, reverseCapabilities bool) domain.RunCharter {
	return mustCharterCustom(t, fixture, fixture.task.Criteria(), revisionIDs, confirmed, exclusions, reverseCapabilities, time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC))
}

func mustCharterCustom(t *testing.T, fixture assemblyFixture, criteria []domain.AcceptanceCriterion, revisionIDs, confirmed []domain.ContextRevisionID, exclusions []domain.ContextEntryID, reverseCapabilities bool, createdAt time.Time) domain.RunCharter {
	t.Helper()
	capabilities := domain.CapabilityEnvelope{"network": false, "read_workspace": true}
	if reverseCapabilities {
		capabilities = make(domain.CapabilityEnvelope)
		capabilities["read_workspace"] = true
		capabilities["network"] = false
	}
	charter, err := domain.NewRunCharter(domain.RunCharterParams{
		ID: mustCharterID(t, 0x05), TaskID: fixture.task.ID(), TaskGoal: fixture.task.Goal(), Criteria: criteria,
		ContextRevisionIDs: revisionIDs, ConfirmedSensitiveRevisionIDs: confirmed, WorkspaceRoot: "/tmp/chora", AdapterID: "codex", SandboxMode: "workspace-write",
		SensitiveExclusions: exclusions, ExpectedOutput: "verified context snapshot", ResponsibleHuman: "Yang Yang", CapabilityEnvelope: capabilities,
		Initiator: "human", CreatedAt: createdAt,
	})
	if err != nil {
		t.Fatalf("NewRunCharter() error = %v", err)
	}
	return charter
}

type revisionSpec struct {
	entry, revision, room byte
	kind                  domain.ContextKind
	number                int
	title, body, locator  string
	sensitive             bool
	supersedes            domain.ContextRevisionID
}

func mustRevision(t *testing.T, spec revisionSpec) domain.RoomContextRevision {
	t.Helper()
	number := spec.number
	if number == 0 {
		number = 1
	}
	var supersedes *domain.ContextRevisionID
	if spec.supersedes.Valid() {
		value := spec.supersedes
		supersedes = &value
	}
	revision, err := domain.NewRoomContextRevision(domain.RoomContextRevisionParams{
		EntryID: mustContextEntryID(t, spec.entry), RevisionID: mustContextRevisionID(t, spec.revision), RoomID: mustRoomID(t, spec.room),
		Kind: spec.kind, RevisionNumber: number, Title: spec.title, Body: spec.body, Locator: spec.locator, Sensitive: spec.sensitive,
		Supersedes: supersedes, CreatedAt: time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	return revision
}

func mustCriterion(t *testing.T, suffix byte, title, description string) domain.AcceptanceCriterion {
	t.Helper()
	criterion, err := domain.NewAcceptanceCriterion(mustCriterionID(t, suffix), title, description)
	if err != nil {
		t.Fatal(err)
	}
	return criterion
}

func readGolden(t *testing.T, name string) []byte {
	t.Helper()
	content, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func assertRunCharterMarkdown(t *testing.T, markdown []byte) {
	t.Helper()
	rendered := string(markdown)
	briefs := strings.Index(rendered, "### Briefs")
	charter := strings.Index(rendered, "## Run Charter")
	criteria := strings.Index(rendered, "## Acceptance Criteria")
	if briefs < 0 || charter <= briefs || criteria <= charter {
		t.Fatalf("Run Charter section order is invalid: briefs=%d charter=%d criteria=%d\n%s", briefs, charter, criteria, rendered)
	}
	for _, required := range []string{
		"- ID: `charter_018f0000-0000-7005-8000-000000000005`",
		"- Task ID: `task_018f0000-0000-7002-8000-000000000002`",
		"- Task Goal: Assemble a frozen context snapshot",
		"- Workspace Root: `/tmp/chora`",
		"- Adapter ID: `codex`",
		"- Sandbox Mode: `workspace-write`",
		"- Expected Output: verified context snapshot",
		"- Responsible Human: Yang Yang",
		"- Initiator: human",
		"- Created At: `2026-07-29T00:00:00Z`",
		"- Confirmed Sensitive Revision IDs:\n  - None.",
		"- Sensitive Exclusions:\n  - None.",
		"- Capabilities:\n  - `network`: denied\n  - `read_workspace`: allowed",
	} {
		if !strings.Contains(rendered, required) {
			t.Fatalf("Run Charter Markdown missing %q:\n%s", required, rendered)
		}
	}
	if !strings.HasSuffix(rendered, "\n") || strings.HasSuffix(rendered, "\n\n") || strings.Contains(rendered, "\r") {
		t.Fatalf("Markdown must use LF and exactly one trailing newline: %q", rendered)
	}
}

func fixedID(prefix string, suffix byte) string {
	return prefix + "018f0000-0000-70" + strings.ToLower("0123456789abcdef"[suffix>>4:suffix>>4+1]) + strings.ToLower("0123456789abcdef"[suffix&15:suffix&15+1]) + "-8000-0000000000" + strings.ToLower("0123456789abcdef"[suffix>>4:suffix>>4+1]) + strings.ToLower("0123456789abcdef"[suffix&15:suffix&15+1])
}

func mustRoomID(t *testing.T, suffix byte) domain.RoomID {
	t.Helper()
	value, err := domain.ParseRoomID(fixedID("room_", suffix))
	if err != nil {
		t.Fatal(err)
	}
	return value
}
func mustTaskID(t *testing.T, suffix byte) domain.TaskID {
	t.Helper()
	value, err := domain.ParseTaskID(fixedID("task_", suffix))
	if err != nil {
		t.Fatal(err)
	}
	return value
}
func mustCriterionID(t *testing.T, suffix byte) domain.CriterionID {
	t.Helper()
	value, err := domain.ParseCriterionID(fixedID("criterion_", suffix))
	if err != nil {
		t.Fatal(err)
	}
	return value
}
func mustCharterID(t *testing.T, suffix byte) domain.CharterID {
	t.Helper()
	value, err := domain.ParseCharterID(fixedID("charter_", suffix))
	if err != nil {
		t.Fatal(err)
	}
	return value
}
func mustContextEntryID(t *testing.T, suffix byte) domain.ContextEntryID {
	t.Helper()
	value, err := domain.ParseContextEntryID(fixedID("context_entry_", suffix))
	if err != nil {
		t.Fatal(err)
	}
	return value
}
func mustContextRevisionID(t *testing.T, suffix byte) domain.ContextRevisionID {
	t.Helper()
	value, err := domain.ParseContextRevisionID(fixedID("context_revision_", suffix))
	if err != nil {
		t.Fatal(err)
	}
	return value
}
func mustSnapshotID(t *testing.T, suffix byte) domain.ContextSnapshotID {
	t.Helper()
	value, err := domain.ParseContextSnapshotID(fixedID("context_snapshot_", suffix))
	if err != nil {
		t.Fatal(err)
	}
	return value
}
