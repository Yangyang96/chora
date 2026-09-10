package domain

import (
	"errors"
	"testing"
	"time"
)

func TestRunTransitions(t *testing.T) {
	tests := []struct {
		name    string
		from    RunState
		command CommandKind
		want    RunState
	}{
		{"create", RunStateNone, CommandCreateRun, RunStateDraft},
		{"prepare", RunStateDraft, CommandPrepareRun, RunStateReady},
		{"start attempt", RunStateReady, CommandStartAttempt, RunStateRunning},
		{"attempt started", RunStateRunning, CommandAttemptStarted, RunStateRunning},
		{"start failed", RunStateRunning, CommandStartFailed, RunStateRecoveryRequired},
		{"submit agent report", RunStateRunning, CommandSubmitAgentReport, RunStateAwaitingVerification},
		{"submit agent report direct review", RunStateRunning, CommandSubmitAgentReportDirectReview, RunStateAwaitingReview},
		{"start verification", RunStateAwaitingVerification, CommandStartVerification, RunStateVerifying},
		{"verification passed", RunStateVerifying, CommandVerificationPassed, RunStateAwaitingReview},
		{"verification needs revision", RunStateVerifying, CommandVerificationNeedsRevision, RunStateRevisionRequired},
		{"verification recovery required", RunStateVerifying, CommandVerificationRecoveryRequired, RunStateVerificationRecoveryRequired},
		{"verification cancelled", RunStateVerifying, CommandVerificationCancelConfirmed, RunStateAwaitingVerification},
		{"retry verification", RunStateVerificationRecoveryRequired, CommandRetryVerification, RunStateVerifying},
		{"retry verification after cancel", RunStateAwaitingVerification, CommandRetryVerification, RunStateVerifying},
		{"request intervention", RunStateRunning, CommandRequestIntervention, RunStateStopping},
		{"request handoff", RunStateRunning, CommandRequestHandoff, RunStateStopping},
		{"request cancel", RunStateRunning, CommandRequestCancel, RunStateStopping},
		{"stop confirmed for revision", RunStateStopping, CommandStopConfirmedForRevision, RunStateRevisionRequired},
		{"stop confirmed for cancel", RunStateStopping, CommandStopConfirmedForCancel, RunStateCancelled},
		{"stop uncertain", RunStateStopping, CommandStopUncertain, RunStateRecoveryRequired},
		{"attempt failed", RunStateRunning, CommandAttemptFailed, RunStateRecoveryRequired},
		{"review accept", RunStateAwaitingReview, CommandReviewAccept, RunStateAccepted},
		{"review reject", RunStateAwaitingReview, CommandReviewReject, RunStateRevisionRequired},
		{"prepare revision retry", RunStateRevisionRequired, CommandPrepareRetry, RunStateReady},
		{"prepare recovery retry", RunStateRecoveryRequired, CommandPrepareRetry, RunStateReady},
		{"prepare cancelled retry", RunStateCancelled, CommandPrepareRetry, RunStateReady},
		{"cancel draft", RunStateDraft, CommandCancelInactiveRun, RunStateCancelled},
		{"cancel ready", RunStateReady, CommandCancelInactiveRun, RunStateCancelled},
		{"cancel awaiting review", RunStateAwaitingReview, CommandCancelInactiveRun, RunStateCancelled},
		{"cancel awaiting verification", RunStateAwaitingVerification, CommandCancelInactiveRun, RunStateCancelled},
		{"cancel revision required", RunStateRevisionRequired, CommandCancelInactiveRun, RunStateCancelled},
		{"cancel reconciled recovery required", RunStateRecoveryRequired, CommandCancelInactiveRun, RunStateCancelled},
		{"cancel verification recovery required", RunStateVerificationRecoveryRequired, CommandCancelInactiveRun, RunStateCancelled},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := TransitionRun(test.from, test.command)
			if err != nil {
				t.Fatalf("TransitionRun(%q, %q) error = %v", test.from, test.command, err)
			}
			if got != test.want {
				t.Fatalf("TransitionRun(%q, %q) = %q, want %q", test.from, test.command, got, test.want)
			}
		})
	}
}

func TestRunTransitionsRejectUnspecifiedAndTerminalMutations(t *testing.T) {
	tests := []struct {
		name    string
		from    RunState
		command CommandKind
	}{
		{"unspecified", RunStateDraft, CommandStartAttempt},
		{"accepted terminal", RunStateAccepted, CommandPrepareRetry},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := TransitionRun(test.from, test.command)
			if !errors.Is(err, ErrInvalidRunTransition) {
				t.Fatalf("TransitionRun(%q, %q) error = %v, want ErrInvalidRunTransition", test.from, test.command, err)
			}
			if got != test.from {
				t.Fatalf("TransitionRun(%q, %q) = %q on rejection, want unchanged %q", test.from, test.command, got, test.from)
			}
		})
	}
}

func TestDirectReviewTransitionStampsReviewRequestedAt(t *testing.T) {
	now := time.Date(2026, 9, 4, 17, 0, 0, 0, time.UTC)
	run, err := RestoreAgentRun(AgentRunRecord{
		ID: NewRunID(), TaskID: NewTaskID(), CharterID: NewCharterID(), State: RunStateRunning,
		Version: 3, CurrentAttemptNumber: 1, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Minute), StartedAt: now.Add(-time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	next, err := run.Transition(CommandSubmitAgentReportDirectReview, now)
	if err != nil {
		t.Fatal(err)
	}
	if next.State() != RunStateAwaitingReview || !next.ReviewRequestedAt().Equal(now) {
		t.Fatalf("direct review successor=%#v reviewRequestedAt=%v", next, next.ReviewRequestedAt())
	}
	if err := ValidateRunSuccessor(run, next); err != nil {
		t.Fatalf("direct review successor rejected: %v", err)
	}
}

func TestNoChangeCompletionIsTerminalWithoutHumanAcceptance(t *testing.T) {
	next, err := TransitionRun(RunStateRunning, CommandCompleteNoChange)
	if err != nil || next != RunStateCompleted {
		t.Fatalf("completion = %q, %v", next, err)
	}
	for _, command := range []CommandKind{CommandReviewAccept, CommandReviewReject, CommandPrepareRetry, CommandStartAttempt} {
		if _, err := TransitionRun(RunStateCompleted, command); err == nil {
			t.Fatalf("completed Run allowed %q", command)
		}
	}
}
