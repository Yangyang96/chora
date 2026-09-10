package domain

import (
	"testing"
	"time"
)

func TestRestoreAgentRunPreservesPersistedState(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Nanosecond)
	run, err := RestoreAgentRun(AgentRunRecord{
		ID: NewRunID(), TaskID: NewTaskID(), CharterID: NewCharterID(), State: RunStateRunning,
		Version: 3, CurrentAttemptNumber: 1, CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
		StartedAt: now.Add(-time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.State() != RunStateRunning || run.Version() != 3 || run.CurrentAttemptNumber() != 1 {
		t.Fatalf("unexpected restored run: %#v", run)
	}
}

func TestRestoreAttemptAndReviewRejectInvalidRecords(t *testing.T) {
	now := time.Now().UTC()
	_, err := RestoreAttempt(AttemptRecord{ID: NewAttemptID(), RunID: NewRunID(), Sequence: 1, ContextSnapshotID: NewContextSnapshotID(), ContextDigest: [32]byte{1}, AdapterID: "codex", State: AttemptStateRunning, CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	_, err = RestoreReviewDecision(ReviewDecisionRecord{ID: NewReviewDecisionID(), RunID: NewRunID(), Kind: ReviewDecisionAccept, ExpectedRunVersion: 2, DecidedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	_, err = RestoreAgentRun(AgentRunRecord{ID: NewRunID(), TaskID: NewTaskID(), CharterID: NewCharterID(), State: "bogus", CreatedAt: now, UpdatedAt: now})
	if err == nil {
		t.Fatal("expected invalid state rejection")
	}
}

func TestAdditionalTypedIDsAreUUIDv7(t *testing.T) {
	tests := []struct {
		name, value string
		parse       func(string) error
	}{
		{"observation", NewObservationID().String(), func(v string) error { _, e := ParseObservationID(v); return e }},
		{"check", NewCheckID().String(), func(v string) error { _, e := ParseCheckID(v); return e }},
		{"intervention", NewInterventionID().String(), func(v string) error { _, e := ParseInterventionID(v); return e }},
		{"handoff", NewHandoffID().String(), func(v string) error { _, e := ParseHandoffID(v); return e }},
		{"runtime session", NewRuntimeSessionID().String(), func(v string) error { _, e := ParseRuntimeSessionID(v); return e }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.value == "" {
				t.Fatal("empty ID")
			}
			if err := tt.parse(tt.value); err != nil {
				t.Fatal(err)
			}
		})
	}
}
