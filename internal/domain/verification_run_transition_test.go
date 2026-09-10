package domain

import "testing"

func TestAuthoritativeVerificationRunStateFlow(t *testing.T) {
	tests := []struct {
		from RunState
		cmd  CommandKind
		want RunState
	}{
		{RunStateRunning, CommandSubmitAgentReport, RunStateAwaitingVerification},
		{RunStateAwaitingVerification, CommandStartVerification, RunStateVerifying},
		{RunStateVerifying, CommandVerificationPassed, RunStateAwaitingReview},
		{RunStateVerifying, CommandVerificationNeedsRevision, RunStateRevisionRequired},
		{RunStateVerifying, CommandVerificationRecoveryRequired, RunStateVerificationRecoveryRequired},
		{RunStateVerifying, CommandVerificationCancelConfirmed, RunStateAwaitingVerification},
		{RunStateVerificationRecoveryRequired, CommandRetryVerification, RunStateVerifying},
	}
	for _, test := range tests {
		got, err := TransitionRun(test.from, test.cmd)
		if err != nil || got != test.want {
			t.Fatalf("TransitionRun(%q, %q) = %q, %v; want %q", test.from, test.cmd, got, err, test.want)
		}
	}
	for _, command := range []CommandKind{CommandSubmitReviewReadyOutput, CommandSubmitRevisionOutput} {
		if _, err := TransitionRun(RunStateRunning, command); err == nil {
			t.Fatalf("Agent terminal command %q bypassed verification", command)
		}
	}
}
