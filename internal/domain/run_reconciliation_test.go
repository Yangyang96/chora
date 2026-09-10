package domain

import (
	"testing"
	"time"
)

func TestStartReconciliationRequiredDoesNotFailStartingAttempt(t *testing.T) {
	now := time.Now().UTC()
	run, err := RestoreAgentRun(AgentRunRecord{ID: NewRunID(), TaskID: NewTaskID(), CharterID: NewCharterID(), State: RunStateRunning, Version: 2, CurrentAttemptNumber: 1, CreatedAt: now, UpdatedAt: now, StartedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := RestoreAttempt(AttemptRecord{ID: NewAttemptID(), RunID: run.ID(), Sequence: 1, ContextSnapshotID: NewContextSnapshotID(), ContextDigest: [32]byte{1}, AdapterID: "codex", State: AttemptStateStarting, CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}

	nextRun, err := run.Transition(CommandStartReconciliationRequired, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if nextRun.State() != RunStateRecoveryRequired {
		t.Fatalf("run state = %q", nextRun.State())
	}
	if attempt.State() != AttemptStateStarting {
		t.Fatalf("attempt state = %q", attempt.State())
	}
}
