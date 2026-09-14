package domain

import (
	"testing"
	"time"
)

func TestExecutionDecisionGateReopenClearsResolution(t *testing.T) {
	now := time.Now().UTC()
	g, err := NewExecutionDecisionGate(ExecutionDecisionGateParams{ID: NewDecisionGateID(), RunID: NewRunID(), AttemptID: NewAttemptID(), Question: "q", Context: "c", Options: []DecisionGateOption{{ID: "a", Label: "A", Impact: "x"}, {ID: "b", Label: "B", Impact: "y"}}, Recommendation: "a", Impact: "x", RequestedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	g, err = g.Resolve("a", "ok", "actor", "session", now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := g.Reopen(now.Add(2 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Status() != DecisionGateOpen || reopened.SelectedOptionID() != "" || !reopened.ResolvedAt().IsZero() {
		t.Fatalf("%#v", reopened)
	}
}
