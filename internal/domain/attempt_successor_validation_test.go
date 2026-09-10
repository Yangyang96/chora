package domain

import (
	"testing"
	"time"
)

func TestValidateAttemptSuccessorRejectsIdentityMutation(t *testing.T) {
	now := time.Now().UTC()
	current, err := RestoreAttempt(AttemptRecord{ID: NewAttemptID(), RunID: NewRunID(), Sequence: 1, ContextSnapshotID: NewContextSnapshotID(), ContextDigest: [32]byte{1}, AdapterID: "codex", State: AttemptStateStarting, CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	next, err := RestoreAttempt(AttemptRecord{ID: current.ID(), RunID: NewRunID(), Sequence: 1, ContextSnapshotID: current.ContextSnapshotID(), ContextDigest: current.ContextDigest(), AdapterID: current.AdapterID(), State: AttemptStateRunning, CreatedAt: current.CreatedAt()})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateAttemptSuccessor(current, next); err == nil {
		t.Fatal("identity mutation accepted")
	}
}
