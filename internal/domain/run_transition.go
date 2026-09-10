package domain

import (
	"fmt"
	"time"
)

type RunState string

const (
	RunStateNone                         RunState = ""
	RunStateDraft                        RunState = "draft"
	RunStateReady                        RunState = "ready"
	RunStateRunning                      RunState = "running"
	RunStateStopping                     RunState = "stopping"
	RunStateAwaitingVerification         RunState = "awaiting_verification"
	RunStateVerifying                    RunState = "verifying"
	RunStateAwaitingReview               RunState = "awaiting_review"
	RunStateRevisionRequired             RunState = "revision_required"
	RunStateRecoveryRequired             RunState = "recovery_required"
	RunStateVerificationRecoveryRequired RunState = "verification_recovery_required"
	RunStateAccepted                     RunState = "accepted"
	RunStateCompleted                    RunState = "completed"
	RunStateCancelled                    RunState = "cancelled"
)

type CommandKind string

const (
	CommandCreateRun                     CommandKind = "create_run"
	CommandPrepareRun                    CommandKind = "prepare_run"
	CommandStartAttempt                  CommandKind = "start_attempt"
	CommandAttemptStarted                CommandKind = "attempt_started"
	CommandStartFailed                   CommandKind = "start_failed"
	CommandStartReconciliationRequired   CommandKind = "start_reconciliation_required"
	CommandRecoveryDetected              CommandKind = "recovery_detected"
	CommandSubmitReviewReadyOutput       CommandKind = "submit_review_ready_output"
	CommandSubmitRevisionOutput          CommandKind = "submit_revision_output"
	CommandRequestIntervention           CommandKind = "request_intervention"
	CommandRequestHandoff                CommandKind = "request_handoff"
	CommandRequestCancel                 CommandKind = "request_cancel"
	CommandStopConfirmedForRevision      CommandKind = "stop_confirmed_for_revision"
	CommandStopConfirmedForCancel        CommandKind = "stop_confirmed_for_cancel"
	CommandStopUncertain                 CommandKind = "stop_uncertain"
	CommandAttemptFailed                 CommandKind = "attempt_failed"
	CommandSubmitAgentReport             CommandKind = "submit_agent_report"
	CommandSubmitAgentReportDirectReview CommandKind = "submit_agent_report_direct_review"
	CommandCompleteNoChange              CommandKind = "complete_no_change"
	CommandStartVerification             CommandKind = "start_verification"
	CommandVerificationPassed            CommandKind = "verification_passed"
	CommandVerificationNeedsRevision     CommandKind = "verification_needs_revision"
	CommandVerificationRecoveryRequired  CommandKind = "verification_recovery_required"
	CommandVerificationCancelConfirmed   CommandKind = "verification_cancel_confirmed"
	CommandRetryVerification             CommandKind = "retry_verification"
	CommandReviewAccept                  CommandKind = "review_accept"
	CommandReviewReject                  CommandKind = "review_reject"
	CommandPrepareRetry                  CommandKind = "prepare_retry"
	CommandCancelInactiveRun             CommandKind = "cancel_inactive_run"
)

func TransitionRun(current RunState, command CommandKind) (RunState, error) {
	if next, ok := runTransitions[runTransition{current, command}]; ok {
		return next, nil
	}
	return current, fmt.Errorf("%w: state %q command %q", ErrInvalidRunTransition, current, command)
}

func CanTransitionRunState(from, to RunState) bool {
	for transition, next := range runTransitions {
		if transition.state == from && next == to {
			return true
		}
	}
	return false
}

func ValidateRunSuccessor(current, next AgentRun) error {
	if current.ID() != next.ID() || current.TaskID() != next.TaskID() || current.CharterID() != next.CharterID() || !current.CreatedAt().Equal(next.CreatedAt()) {
		return fmt.Errorf("%w: run identity changed", ErrInvalidRunTransition)
	}
	if next.Version() != current.Version()+1 || next.UpdatedAt().Before(current.UpdatedAt()) || !CanTransitionRunState(current.State(), next.State()) {
		return fmt.Errorf("%w: invalid run state/version successor", ErrInvalidRunTransition)
	}
	expectedAttempt := current.CurrentAttemptNumber()
	if next.State() == RunStateReady && (current.State() == RunStateDraft || current.State() == RunStateRevisionRequired || current.State() == RunStateRecoveryRequired || current.State() == RunStateCancelled) {
		expectedAttempt++
	}
	if next.CurrentAttemptNumber() != expectedAttempt {
		return fmt.Errorf("%w: invalid run attempt successor", ErrInvalidRunTransition)
	}
	expectedStarted := current.StartedAt()
	if current.State() == RunStateReady && next.State() == RunStateRunning && expectedStarted.IsZero() {
		expectedStarted = next.UpdatedAt()
	}
	if !expectedStarted.Equal(next.StartedAt()) {
		return fmt.Errorf("%w: invalid run started timestamp successor", ErrInvalidRunTransition)
	}
	expectedReviewRequested := current.ReviewRequestedAt()
	if next.State() == RunStateAwaitingReview && (current.State() == RunStateVerifying || current.State() == RunStateRunning) {
		expectedReviewRequested = next.UpdatedAt()
	}
	if !expectedReviewRequested.Equal(next.ReviewRequestedAt()) {
		return fmt.Errorf("%w: invalid run review timestamp successor", ErrInvalidRunTransition)
	}
	expectedTerminal := current.TerminalAt()
	if current.State() == RunStateCancelled && next.State() == RunStateReady {
		expectedTerminal = time.Time{}
	}
	if next.State() == RunStateAccepted || next.State() == RunStateCancelled || next.State() == RunStateCompleted {
		expectedTerminal = next.UpdatedAt()
	}
	if next.State() == RunStateRevisionRequired && !next.TerminalAt().IsZero() {
		expectedTerminal = next.UpdatedAt()
	}
	if !expectedTerminal.Equal(next.TerminalAt()) {
		return fmt.Errorf("%w: invalid run terminal timestamp successor", ErrInvalidRunTransition)
	}
	return nil
}

type runTransition struct {
	state   RunState
	command CommandKind
}

var runTransitions = map[runTransition]RunState{
	{RunStateRunning, CommandCompleteNoChange}:                       RunStateCompleted,
	{RunStateNone, CommandCreateRun}:                                 RunStateDraft,
	{RunStateDraft, CommandPrepareRun}:                               RunStateReady,
	{RunStateReady, CommandStartAttempt}:                             RunStateRunning,
	{RunStateRunning, CommandAttemptStarted}:                         RunStateRunning,
	{RunStateRunning, CommandStartFailed}:                            RunStateRecoveryRequired,
	{RunStateRunning, CommandStartReconciliationRequired}:            RunStateRecoveryRequired,
	{RunStateRunning, CommandRecoveryDetected}:                       RunStateRecoveryRequired,
	{RunStateStopping, CommandRecoveryDetected}:                      RunStateRecoveryRequired,
	{RunStateRunning, CommandSubmitAgentReport}:                      RunStateAwaitingVerification,
	{RunStateRunning, CommandSubmitAgentReportDirectReview}:          RunStateAwaitingReview,
	{RunStateAwaitingVerification, CommandStartVerification}:         RunStateVerifying,
	{RunStateVerifying, CommandVerificationPassed}:                   RunStateAwaitingReview,
	{RunStateVerifying, CommandVerificationNeedsRevision}:            RunStateRevisionRequired,
	{RunStateVerifying, CommandVerificationRecoveryRequired}:         RunStateVerificationRecoveryRequired,
	{RunStateVerifying, CommandVerificationCancelConfirmed}:          RunStateAwaitingVerification,
	{RunStateVerificationRecoveryRequired, CommandRetryVerification}: RunStateVerifying,
	{RunStateAwaitingVerification, CommandRetryVerification}:         RunStateVerifying,
	{RunStateRunning, CommandRequestIntervention}:                    RunStateStopping,
	{RunStateRunning, CommandRequestHandoff}:                         RunStateStopping,
	{RunStateRunning, CommandRequestCancel}:                          RunStateStopping,
	{RunStateStopping, CommandStopConfirmedForRevision}:              RunStateRevisionRequired,
	{RunStateStopping, CommandStopConfirmedForCancel}:                RunStateCancelled,
	{RunStateStopping, CommandStopUncertain}:                         RunStateRecoveryRequired,
	{RunStateRunning, CommandAttemptFailed}:                          RunStateRecoveryRequired,
	{RunStateAwaitingReview, CommandReviewAccept}:                    RunStateAccepted,
	{RunStateAwaitingReview, CommandReviewReject}:                    RunStateRevisionRequired,
	{RunStateRevisionRequired, CommandPrepareRetry}:                  RunStateReady,
	{RunStateRecoveryRequired, CommandPrepareRetry}:                  RunStateReady,
	{RunStateCancelled, CommandPrepareRetry}:                         RunStateReady,
	{RunStateDraft, CommandCancelInactiveRun}:                        RunStateCancelled,
	{RunStateReady, CommandCancelInactiveRun}:                        RunStateCancelled,
	{RunStateAwaitingReview, CommandCancelInactiveRun}:               RunStateCancelled,
	{RunStateRevisionRequired, CommandCancelInactiveRun}:             RunStateCancelled,
	{RunStateRecoveryRequired, CommandCancelInactiveRun}:             RunStateCancelled,
	{RunStateAwaitingVerification, CommandCancelInactiveRun}:         RunStateCancelled,
	{RunStateVerificationRecoveryRequired, CommandCancelInactiveRun}: RunStateCancelled,
}

type AttemptEventKind string

const (
	AttemptEventStartAttempt             AttemptEventKind = "start_attempt"
	AttemptEventAttemptStarted           AttemptEventKind = "attempt_started"
	AttemptEventStartFailed              AttemptEventKind = "start_failed"
	AttemptEventOutputSubmitted          AttemptEventKind = "output_submitted"
	AttemptEventAttemptFailed            AttemptEventKind = "attempt_failed"
	AttemptEventStopConfirmedForRevision AttemptEventKind = "stop_confirmed_for_revision"
	AttemptEventStopConfirmedForCancel   AttemptEventKind = "stop_confirmed_for_cancel"
)

func TransitionAttempt(current AttemptState, event AttemptEventKind) (AttemptState, error) {
	if next, ok := attemptTransitions[attemptTransition{current, event}]; ok {
		return next, nil
	}
	return current, fmt.Errorf("%w: state %q event %q", ErrInvalidAttemptTransition, current, event)
}

func ValidateAttemptSuccessor(current, next Attempt) error {
	currentPredecessor, currentHasPredecessor := current.Predecessor()
	nextPredecessor, nextHasPredecessor := next.Predecessor()
	if current.ID() != next.ID() || current.RunID() != next.RunID() || current.Sequence() != next.Sequence() || currentPredecessor != nextPredecessor || currentHasPredecessor != nextHasPredecessor || current.ContextSnapshotID() != next.ContextSnapshotID() || current.ContextDigest() != next.ContextDigest() || current.AdapterID() != next.AdapterID() || current.AgentExecutionProfileBinding() != next.AgentExecutionProfileBinding() || current.ExternalSession() != next.ExternalSession() || current.RetryReason() != next.RetryReason() || current.InterventionReason() != next.InterventionReason() || current.ContextDelta() != next.ContextDelta() || !current.CreatedAt().Equal(next.CreatedAt()) {
		return fmt.Errorf("%w: attempt identity changed", ErrInvalidAttemptTransition)
	}
	for transition, state := range attemptTransitions {
		if transition.state == current.State() && state == next.State() {
			return nil
		}
	}
	return fmt.Errorf("%w: invalid attempt state successor", ErrInvalidAttemptTransition)
}

type attemptTransition struct {
	state AttemptState
	event AttemptEventKind
}

var attemptTransitions = map[attemptTransition]AttemptState{
	{AttemptStateCreated, AttemptEventStartAttempt}:              AttemptStateStarting,
	{AttemptStateStarting, AttemptEventAttemptStarted}:           AttemptStateRunning,
	{AttemptStateStarting, AttemptEventStartFailed}:              AttemptStateFailed,
	{AttemptStateRunning, AttemptEventOutputSubmitted}:           AttemptStateOutputSubmitted,
	{AttemptStateStarting, AttemptEventAttemptFailed}:            AttemptStateFailed,
	{AttemptStateRunning, AttemptEventAttemptFailed}:             AttemptStateFailed,
	{AttemptStateStarting, AttemptEventStopConfirmedForRevision}: AttemptStateInterrupted,
	{AttemptStateRunning, AttemptEventStopConfirmedForRevision}:  AttemptStateInterrupted,
	{AttemptStateCreated, AttemptEventStopConfirmedForCancel}:    AttemptStateCancelled,
	{AttemptStateStarting, AttemptEventStopConfirmedForCancel}:   AttemptStateCancelled,
	{AttemptStateRunning, AttemptEventStopConfirmedForCancel}:    AttemptStateCancelled,
}
