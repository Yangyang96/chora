package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
)

type terminalClosureReader struct {
	Reader
	binding SpecCodingBinding
	report  domain.AgentReport
	events  []domain.RunEvent
}

func (r terminalClosureReader) GetSpecCodingBinding(context.Context, domain.TaskID) (SpecCodingBinding, error) {
	return r.binding, nil
}
func (r terminalClosureReader) GetAgentReportForAttempt(context.Context, domain.AttemptID) (domain.AgentReport, error) {
	return r.report, nil
}
func (r terminalClosureReader) ListRunEvents(context.Context, domain.RunID) ([]domain.RunEvent, error) {
	return r.events, nil
}

func TestScalarTerminalClosureRejectsOtherAttemptAndContract(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	run, err := domain.RestoreAgentRun(domain.AgentRunRecord{ID: domain.NewRunID(), TaskID: domain.NewTaskID(), CharterID: domain.NewCharterID(), State: domain.RunStateRecoveryRequired, Version: 3, CurrentAttemptNumber: 1, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	contextDigest := sha256.Sum256([]byte("snapshot"))
	profile, err := domain.NewAgentExecutionProfileBinding(domain.AgentExecutionProfileTrustedLocal)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := domain.RestoreAttempt(domain.AttemptRecord{ID: domain.NewAttemptID(), RunID: run.ID(), Sequence: 1, ContextSnapshotID: domain.NewContextSnapshotID(), ContextDigest: contextDigest, AdapterID: "pi", AgentExecutionProfileBinding: profile, State: domain.AttemptStateFailed, CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	report, err := domain.NewAgentReport(domain.AgentReportParams{ID: domain.NewAgentReportID(), RunID: run.ID(), AttemptID: attempt.ID(), Summary: "Checks were incomplete", FinalText: "No changes", CompletedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	contract := []byte(`{"frozen":"contract"}`)
	r := terminalClosureReader{binding: SpecCodingBinding{TaskID: run.TaskID(), Status: SpecCodingRegistered, SnapshotDigest: contextDigest, ActiveContractJSON: contract, ActiveContractDigest: sha256.Sum256(contract)}, report: report}
	result := domain.NewResultID()
	body := map[string]string{"result_id": result.String(), "attempt_id": attempt.ID().String(), "agent_report_id": report.ID().String(), "outcome": "checks_incomplete", "contract_digest": hex.EncodeToString(r.binding.ActiveContractDigest[:]), "context_snapshot_digest": hex.EncodeToString(contextDigest[:])}
	event := func() domain.RunEvent {
		raw, _ := json.Marshal(body)
		e, err := domain.NewRunEvent(domain.RunEventParams{ID: domain.NewEventID(), RunID: run.ID(), Sequence: 1, Type: "attempt.failed", Source: "adapter", OccurredAt: now, RecordedAt: now, NormalizedJSON: raw})
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	r.events = []domain.RunEvent{event()}
	id, digest, err := TerminalResultForClosure(ctx, r, run, attempt)
	if err != nil || id != result || digest != sha256.Sum256(r.events[0].NormalizedJSON()) {
		t.Fatalf("terminal closure=%s %x %v", id, digest, err)
	}
	body["attempt_id"] = domain.NewAttemptID().String()
	r.events = []domain.RunEvent{event()}
	if _, _, err = TerminalResultForClosure(ctx, r, run, attempt); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other Attempt accepted: %v", err)
	}
	body["attempt_id"] = attempt.ID().String()
	body["contract_digest"] = hex.EncodeToString(contextDigest[:])
	r.events = []domain.RunEvent{event()}
	if _, _, err = TerminalResultForClosure(ctx, r, run, attempt); !errors.Is(err, ErrVerificationConflict) {
		t.Fatalf("other contract accepted: %v", err)
	}
	body["contract_digest"] = hex.EncodeToString(r.binding.ActiveContractDigest[:])
	r.events = []domain.RunEvent{event(), event()}
	if _, _, err = TerminalResultForClosure(ctx, r, run, attempt); !errors.Is(err, ErrVerificationConflict) {
		t.Fatalf("ambiguous Result accepted: %v", err)
	}
}
