package domain

import (
	"testing"
	"time"
)

func TestValidateRunSuccessorAcceptsRealTransitionsAndReview(t *testing.T) {
	now := time.Now().UTC()
	run, err := NewAgentRun(NewRunID(), NewTaskID(), NewCharterID(), now)
	if err != nil {
		t.Fatal(err)
	}
	for index, command := range []CommandKind{CommandPrepareRun, CommandStartAttempt, CommandAttemptStarted, CommandSubmitAgentReport, CommandStartVerification, CommandVerificationPassed} {
		next, err := run.Transition(command, now.Add(time.Duration(index+1)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidateRunSuccessor(run, next); err != nil {
			t.Fatalf("ValidateRunSuccessor(%s -> %s): %v", run.State(), next.State(), err)
		}
		run = next
	}
	review, err := RestoreReviewDecision(ReviewDecisionRecord{ID: NewReviewDecisionID(), RunID: run.ID(), Kind: ReviewDecisionAccept, ExpectedRunVersion: run.Version(), DecidedAt: now.Add(7 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := run.ApplyReview(review)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateRunSuccessor(run, accepted); err != nil {
		t.Fatalf("ValidateRunSuccessor(awaiting_review -> accepted): %v", err)
	}
}
