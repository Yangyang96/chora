package domain

import "time"

const AgentHeartbeatTimeout = 30 * time.Second

type AgentPresence string

const (
	AgentPresenceAvailable    AgentPresence = "available"
	AgentPresenceActive       AgentPresence = "active"
	AgentPresenceWaiting      AgentPresence = "waiting"
	AgentPresenceUnresponsive AgentPresence = "unresponsive"
	AgentPresenceOffline      AgentPresence = "offline"
)

type AgentPhase string

const (
	AgentPhasePreparingContext     AgentPhase = "preparing_context"
	AgentPhaseExecuting            AgentPhase = "executing"
	AgentPhaseWaitingForDecision   AgentPhase = "waiting_for_decision"
	AgentPhaseStopping             AgentPhase = "stopping"
	AgentPhaseReviewReady          AgentPhase = "review_ready"
	AgentPhaseWaitingForRetry      AgentPhase = "waiting_for_retry"
	AgentPhaseRecovery             AgentPhase = "recovery"
	AgentPhaseCompleted            AgentPhase = "completed"
	AgentPhaseCancelled            AgentPhase = "cancelled"
	AgentPhaseAwaitingVerification AgentPhase = "awaiting_verification"
	AgentPhaseVerifying            AgentPhase = "verifying"
	AgentPhaseVerificationRecovery AgentPhase = "verification_recovery_required"
)

type AgentVisibility struct {
	Presence AgentPresence
	Phase    AgentPhase
}

func ProjectAgentVisibility(state RunState, latestEventAt, now time.Time) AgentVisibility {
	visibility := AgentVisibility{Presence: AgentPresenceAvailable, Phase: AgentPhasePreparingContext}
	switch state {
	case RunStateRunning:
		visibility = AgentVisibility{Presence: AgentPresenceActive, Phase: AgentPhaseExecuting}
	case RunStateStopping:
		visibility = AgentVisibility{Presence: AgentPresenceActive, Phase: AgentPhaseStopping}
	case RunStateAwaitingVerification:
		visibility = AgentVisibility{Presence: AgentPresenceOffline, Phase: AgentPhaseAwaitingVerification}
	case RunStateVerifying:
		visibility = AgentVisibility{Presence: AgentPresenceOffline, Phase: AgentPhaseVerifying}
	case RunStateAwaitingReview:
		visibility = AgentVisibility{Presence: AgentPresenceWaiting, Phase: AgentPhaseReviewReady}
	case RunStateRevisionRequired:
		visibility = AgentVisibility{Presence: AgentPresenceWaiting, Phase: AgentPhaseWaitingForRetry}
	case RunStateRecoveryRequired:
		visibility = AgentVisibility{Presence: AgentPresenceUnresponsive, Phase: AgentPhaseRecovery}
	case RunStateVerificationRecoveryRequired:
		visibility = AgentVisibility{Presence: AgentPresenceOffline, Phase: AgentPhaseVerificationRecovery}
	case RunStateAccepted, RunStateCompleted:
		visibility = AgentVisibility{Presence: AgentPresenceOffline, Phase: AgentPhaseCompleted}
	case RunStateCancelled:
		visibility = AgentVisibility{Presence: AgentPresenceOffline, Phase: AgentPhaseCancelled}
	}
	if (state == RunStateRunning || state == RunStateStopping) &&
		!latestEventAt.IsZero() && !now.IsZero() && now.Sub(latestEventAt) > AgentHeartbeatTimeout {
		visibility.Presence = AgentPresenceUnresponsive
	}
	return visibility
}
