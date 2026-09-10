package domain

import (
	"testing"
	"time"
)

func TestProjectAgentVisibilitySeparatesPresenceFromRunState(t *testing.T) {
	now := time.Date(2026, 8, 7, 6, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		state    RunState
		latest   time.Time
		presence AgentPresence
		phase    AgentPhase
	}{
		{name: "draft", state: RunStateDraft, latest: now, presence: AgentPresenceAvailable, phase: AgentPhasePreparingContext},
		{name: "running", state: RunStateRunning, latest: now.Add(-5 * time.Second), presence: AgentPresenceActive, phase: AgentPhaseExecuting},
		{name: "stale running", state: RunStateRunning, latest: now.Add(-AgentHeartbeatTimeout - time.Second), presence: AgentPresenceUnresponsive, phase: AgentPhaseExecuting},
		{name: "stale stopping", state: RunStateStopping, latest: now.Add(-AgentHeartbeatTimeout - time.Second), presence: AgentPresenceUnresponsive, phase: AgentPhaseStopping},
		{name: "awaiting review", state: RunStateAwaitingReview, latest: now.Add(-time.Hour), presence: AgentPresenceWaiting, phase: AgentPhaseReviewReady},
		{name: "revision required", state: RunStateRevisionRequired, latest: now, presence: AgentPresenceWaiting, phase: AgentPhaseWaitingForRetry},
		{name: "recovery required", state: RunStateRecoveryRequired, latest: now, presence: AgentPresenceUnresponsive, phase: AgentPhaseRecovery},
		{name: "accepted", state: RunStateAccepted, latest: now, presence: AgentPresenceOffline, phase: AgentPhaseCompleted},
		{name: "cancelled", state: RunStateCancelled, latest: now, presence: AgentPresenceOffline, phase: AgentPhaseCancelled},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ProjectAgentVisibility(test.state, test.latest, now)
			if got.Presence != test.presence || got.Phase != test.phase {
				t.Fatalf("ProjectAgentVisibility(%q) = %#v, want presence=%q phase=%q", test.state, got, test.presence, test.phase)
			}
		})
	}
}
