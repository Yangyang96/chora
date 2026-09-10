package domain

import (
	"testing"
	"time"
)

func TestExecutionDecisionGateResolvesOnePersistableBoundedChoice(t *testing.T) {
	requested := time.Date(2026, 8, 10, 1, 2, 3, 0, time.UTC)
	gate, err := NewExecutionDecisionGate(ExecutionDecisionGateParams{
		ID: NewDecisionGateID(), RunID: NewRunID(), AttemptID: NewAttemptID(),
		Question: "Continue with the frozen evidence?", Context: "The Agent needs one bounded choice.",
		Options: []DecisionGateOption{
			{ID: "continue", Label: "Continue", Impact: "Complete the structured result."},
			{ID: "stop", Label: "Stop", Impact: "Return a revision-required result."},
		},
		Recommendation: "Continue with the frozen evidence.", Impact: "The answer controls execution.", RequestedAt: requested,
	})
	if err != nil {
		t.Fatal(err)
	}
	options := gate.Options()
	options[0].Label = "mutated"
	if gate.Options()[0].Label != "Continue" {
		t.Fatal("gate options alias caller-owned state")
	}
	resolved, err := gate.Resolve("continue", "Use only the selected context.", "local-human", "local-browser", requested.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Status() != DecisionGateResolved || resolved.SelectedOptionID() != "continue" || resolved.ActorID() != "local-human" || resolved.SessionID() != "local-browser" {
		t.Fatalf("resolved gate = %#v", resolved)
	}
	if _, err := resolved.Resolve("stop", "change", "local-human", "local-browser", requested.Add(2*time.Second)); err == nil {
		t.Fatal("resolved gate accepted a second answer")
	}
	restored, err := RestoreExecutionDecisionGate(ExecutionDecisionGateParams{
		ID: resolved.ID(), RunID: resolved.RunID(), AttemptID: resolved.AttemptID(), Question: resolved.Question(), Context: resolved.Context(), Options: resolved.Options(), Recommendation: resolved.Recommendation(), Impact: resolved.Impact(), RequestedAt: resolved.RequestedAt(),
	}, resolved.Status(), resolved.SelectedOptionID(), resolved.Note(), resolved.ActorID(), resolved.SessionID(), resolved.ResolvedAt())
	if err != nil || restored.SelectedOptionID() != resolved.SelectedOptionID() || !restored.ResolvedAt().Equal(resolved.ResolvedAt()) {
		t.Fatalf("restored=%#v err=%v", restored, err)
	}
}

func TestExecutionDecisionGateRejectsUnknownDuplicateAndInvalidResolution(t *testing.T) {
	now := time.Now().UTC()
	base := ExecutionDecisionGateParams{
		ID: NewDecisionGateID(), RunID: NewRunID(), AttemptID: NewAttemptID(), Question: "Choose", Context: "Bounded context",
		Options: []DecisionGateOption{{ID: "same", Label: "One", Impact: "A"}, {ID: "same", Label: "Two", Impact: "B"}}, Recommendation: "One", Impact: "Persisted", RequestedAt: now,
	}
	if _, err := NewExecutionDecisionGate(base); err == nil {
		t.Fatal("duplicate option accepted")
	}
	base.Options[1].ID = "other"
	gate, err := NewExecutionDecisionGate(base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gate.Resolve("missing", "", "owner", "session", now.Add(time.Second)); err == nil {
		t.Fatal("unknown option accepted")
	}
	if _, err := gate.Resolve("same", "", "owner", "session", now.Add(-time.Second)); err == nil {
		t.Fatal("resolution before request accepted")
	}
}
