package sqlite_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
	"github.com/Yangyang96/chora/internal/store/sqlite"
)

func TestDelegationProposalSourcePersistsAndCannotBeRebound(t *testing.T) {
	f := newVerificationFixture(t)
	ctx := context.Background()
	report, err := f.db.Reader().GetAgentReportForAttempt(ctx, f.agentAttempt.ID())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", f.seeded.path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var event domain.RunEvent
	if err = f.db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		var e error
		event, e = tx.AppendRunEvent(ctx, f.seeded.run.ID(), storecontract.EventDraft{ID: domain.NewEventID(), Type: "assistant_message", Source: "adapter", OccurredAt: f.seeded.now, RecordedAt: f.seeded.now, NormalizedJSON: []byte(`{"text":"agent final","terminal_complete":true}`)})
		return e
	}); err != nil {
		t.Fatal(err)
	}
	resultID := domain.NewResultID()
	digest := sha256.Sum256([]byte("retained result"))
	// A pre-existing result fixture: this test exercises SQL lineage and source
	// immutability; proposal parsing/final-message validation belongs to app tests.
	if _, err = raw.Exec(`INSERT INTO resource_result_groups(result_id,run_id,task_id,attempt_id,canonical_json,digest,created_at) VALUES(?,?,?,?,?,?,?)`, resultID.String(), f.seeded.run.ID().String(), f.seeded.task.ID().String(), f.agentAttempt.ID().String(), []byte(`{}`), digest[:], f.seeded.now.Format("2006-01-02T15:04:05.999999999Z07:00")); err != nil {
		t.Fatal(err)
	}
	a := []domain.DelegationAssignment{{Role: "Researcher", Title: "Research", Requirement: "Review supplied material."}}
	d := domain.TaskDelegation{ParentTaskID: f.seeded.task.ID(), Version: 1, State: domain.DelegationRunning, Assignments: a, ActorID: "local-human", SessionID: "browser", CreatedAt: f.seeded.now, UpdatedAt: f.seeded.now}
	s := domain.DelegationProposalSource{ParentTaskID: d.ParentTaskID, ProjectDocumentSource: domain.ProjectDocumentSource{RunID: f.seeded.run.ID(), AttemptID: f.agentAttempt.ID(), ResultID: resultID, AgentReportID: report.ID(), EventID: event.ID(), EventSequence: event.Sequence(), ResultDigest: digest, SourceTextDigest: sha256.Sum256([]byte(report.FinalText()))}, PlanDigest: domain.DelegationPlanDigest(a), CreatedAt: d.CreatedAt}
	bad := s
	bad.EventSequence++
	err = f.db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if e := tx.InsertTaskDelegation(ctx, d); e != nil {
			return e
		}
		return tx.InsertDelegationProposalSource(ctx, bad)
	})
	if err == nil {
		t.Fatal("mismatched event source accepted")
	}
	if _, err = f.db.Reader().GetTaskDelegation(ctx, d.ParentTaskID); !errors.Is(err, storecontract.ErrNotFound) {
		t.Fatalf("source failure partially persisted delegation: %v", err)
	}
	if err = f.db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if e := tx.InsertTaskDelegation(ctx, d); e != nil {
			return e
		}
		return tx.InsertDelegationProposalSource(ctx, s)
	}); err != nil {
		t.Fatal(err)
	}
	got, err := f.db.Reader().GetDelegationProposalSource(ctx, d.ParentTaskID)
	if err != nil || !reflect.DeepEqual(got, s) {
		t.Fatalf("source=%#v want=%#v err=%v", got, s, err)
	}
	for _, query := range []string{`UPDATE delegation_proposal_sources SET plan_digest=zeroblob(32) WHERE parent_task_id=?`, `DELETE FROM delegation_proposal_sources WHERE parent_task_id=?`} {
		if _, err = raw.Exec(query, d.ParentTaskID.String()); err == nil {
			t.Fatalf("immutable source changed with %s", query)
		}
	}
	if err = f.db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqlite.Open(ctx, f.seeded.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err = reopened.Reader().GetDelegationProposalSource(ctx, d.ParentTaskID)
	if err != nil || !reflect.DeepEqual(got, s) {
		t.Fatalf("reopened source=%#v err=%v", got, err)
	}
	for _, table := range []string{"project_document_reviews", "project_document_revisions"} {
		var count int
		if err = raw.QueryRow(fmt.Sprintf("SELECT count(*) FROM %s", table)).Scan(&count); err != nil || count != 0 {
			t.Fatalf("source import changed human document history: %s %d %v", table, count, err)
		}
	}
}
