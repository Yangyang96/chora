package domain

import (
	"errors"
	"testing"
	"time"
)

func verifiedReviewBinding() VerifiedReviewBinding {
	return VerifiedReviewBinding{
		ResultID:              NewResultID(),
		AgentAttemptID:        NewAttemptID(),
		VerificationAttemptID: NewVerificationAttemptID(),
		PatchArtifactID:       NewArtifactID(),
		PatchDigest:           digest(21),
		BaselineDigest:        digest(22),
		DeclaredFilesDigest:   digest(23),
	}
}

func TestVerifiedReviewDecisionRequiresOwnerReasonAndFrozenEvidenceBinding(t *testing.T) {
	now := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	runID := NewRunID()
	authorization, err := MintHumanReviewAuthorization(runID, ReviewDecisionAccept, 7, true)
	if err != nil {
		t.Fatal(err)
	}
	params := VerifiedReviewDecisionParams{
		ID: NewReviewDecisionID(), RunID: runID, ExpectedRunVersion: 7,
		Kind: ReviewDecisionAccept, Reason: "Verified evidence satisfies the frozen acceptance contract.",
		ActorID: "local-human", SessionID: "local-device", Binding: verifiedReviewBinding(), DecidedAt: now,
	}
	decision, err := NewVerifiedReviewDecision(params, authorization)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Kind() != ReviewDecisionAccept || decision.Reason() != params.Reason || decision.Binding() != params.Binding || decision.ActorID() != params.ActorID || decision.SessionID() != params.SessionID {
		t.Fatalf("decision = %#v", decision)
	}

	invalid := []VerifiedReviewDecisionParams{
		func() VerifiedReviewDecisionParams { value := params; value.Reason = " "; return value }(),
		func() VerifiedReviewDecisionParams { value := params; value.ActorID = ""; return value }(),
		func() VerifiedReviewDecisionParams { value := params; value.SessionID = ""; return value }(),
		func() VerifiedReviewDecisionParams {
			value := params
			value.Binding.PatchDigest = [32]byte{}
			return value
		}(),
		func() VerifiedReviewDecisionParams {
			value := params
			value.RejectionClass = ReviewRejectionImplementationGap
			return value
		}(),
	}
	for _, value := range invalid {
		if _, err := NewVerifiedReviewDecision(value, authorization); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("invalid decision accepted: %#v, %v", value, err)
		}
	}

	noOwner, _ := MintHumanReviewAuthorization(runID, ReviewDecisionAccept, 7, false)
	if _, err := NewVerifiedReviewDecision(params, noOwner); !errors.Is(err, ErrUnauthorizedReview) {
		t.Fatalf("non-owner decision error = %v", err)
	}
}

func TestVerifiedRejectClassControlsAgentRetryEligibility(t *testing.T) {
	now := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	runID := NewRunID()
	authorization, _ := MintHumanReviewAuthorization(runID, ReviewDecisionReject, 9, true)
	params := VerifiedReviewDecisionParams{
		ID: NewReviewDecisionID(), RunID: runID, ExpectedRunVersion: 9,
		Kind: ReviewDecisionReject, Reason: "Implementation does not satisfy the verified review expectation.",
		RejectionClass: ReviewRejectionImplementationGap, ActorID: "local-human", SessionID: "local-device",
		Binding: verifiedReviewBinding(), DecidedAt: now,
	}
	decision, err := NewVerifiedReviewDecision(params, authorization)
	if err != nil || !decision.AllowsAgentRetry() {
		t.Fatalf("implementation rejection = %#v, %v", decision, err)
	}
	params.ID = NewReviewDecisionID()
	params.RejectionClass = ReviewRejectionPlanningGap
	decision, err = NewVerifiedReviewDecision(params, authorization)
	if err != nil || decision.AllowsAgentRetry() {
		t.Fatalf("planning rejection = %#v, %v", decision, err)
	}
	params.ID = NewReviewDecisionID()
	params.RejectionClass = ReviewRejectionContractChangeRequired
	decision, err = NewVerifiedReviewDecision(params, authorization)
	if err != nil || decision.AllowsAgentRetry() {
		t.Fatalf("contract rejection = %#v, %v", decision, err)
	}
	params.ID = NewReviewDecisionID()
	params.RejectionClass = "scope_expansion"
	if _, err := NewVerifiedReviewDecision(params, authorization); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("unknown rejection class error = %v", err)
	}
}

func TestVerifiedPlanningAndContractRejectionsTerminateCurrentRun(t *testing.T) {
	now := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	for _, class := range []ReviewRejectionClass{ReviewRejectionPlanningGap, ReviewRejectionContractChangeRequired} {
		run, err := RestoreAgentRun(AgentRunRecord{ID: NewRunID(), TaskID: NewTaskID(), CharterID: NewCharterID(), State: RunStateAwaitingReview, Version: 7, CurrentAttemptNumber: 1, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Minute), ReviewRequestedAt: now.Add(-time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
		authorization, _ := MintHumanReviewAuthorization(run.ID(), ReviewDecisionReject, run.Version(), true)
		decision, err := NewVerifiedReviewDecision(VerifiedReviewDecisionParams{ID: NewReviewDecisionID(), RunID: run.ID(), ExpectedRunVersion: run.Version(), Kind: ReviewDecisionReject, Reason: "The frozen implementation boundary is no longer sufficient.", RejectionClass: class, ActorID: "owner", SessionID: "device", Binding: verifiedReviewBinding(), DecidedAt: now}, authorization)
		if err != nil {
			t.Fatal(err)
		}
		rejected, err := run.ApplyVerifiedReview(decision)
		if err != nil || rejected.State() != RunStateRevisionRequired || !rejected.TerminalAt().Equal(now) {
			t.Fatalf("class %q rejected run = %#v, %v", class, rejected, err)
		}
	}
}

func TestAgentRunAppliesVerifiedReviewOnlyAtAwaitingReview(t *testing.T) {
	now := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	run, err := RestoreAgentRun(AgentRunRecord{
		ID: NewRunID(), TaskID: NewTaskID(), CharterID: NewCharterID(), State: RunStateAwaitingReview,
		Version: 7, CurrentAttemptNumber: 1, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Minute), ReviewRequestedAt: now.Add(-time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	authorization, _ := MintHumanReviewAuthorization(run.ID(), ReviewDecisionAccept, run.Version(), true)
	decision, err := NewVerifiedReviewDecision(VerifiedReviewDecisionParams{
		ID: NewReviewDecisionID(), RunID: run.ID(), ExpectedRunVersion: run.Version(), Kind: ReviewDecisionAccept,
		Reason: "Accept verified Patch.", ActorID: "owner", SessionID: "device", Binding: verifiedReviewBinding(), DecidedAt: now,
	}, authorization)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := run.ApplyVerifiedReview(decision)
	if err != nil || accepted.State() != RunStateAccepted || accepted.Version() != run.Version()+1 || !accepted.TerminalAt().Equal(now) {
		t.Fatalf("accepted = %#v, %v", accepted, err)
	}
	if _, err := accepted.ApplyVerifiedReview(decision); !errors.Is(err, ErrInvalidRunTransition) {
		t.Fatalf("terminal rereview error = %v", err)
	}
}
