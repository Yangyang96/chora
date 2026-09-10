package domain

import (
	"errors"
	"testing"
	"time"
)

func testCriterion(t *testing.T) AcceptanceCriterion {
	t.Helper()
	criterion, err := NewAcceptanceCriterion(NewCriterionID(), "tests", "tests pass")
	if err != nil {
		t.Fatal(err)
	}
	return criterion
}

func TestRunCharterPinsCriteriaContextAndCapabilities(t *testing.T) {
	criterion := testCriterion(t)
	criteria := []AcceptanceCriterion{criterion}
	revisions := []ContextRevisionID{NewContextRevisionID()}
	confirmed := []ContextRevisionID{revisions[0], revisions[0]}
	exclusions := []ContextEntryID{NewContextEntryID()}
	capabilities := CapabilityEnvelope{"read_workspace": true, "network": false}
	charter, err := NewRunCharter(RunCharterParams{
		ID: NewCharterID(), TaskID: NewTaskID(), TaskGoal: "Implement domain", Criteria: criteria,
		ContextRevisionIDs: revisions, WorkspaceRoot: "/tmp/chora", AdapterID: "fake", SandboxMode: "workspace-write",
		ConfirmedSensitiveRevisionIDs: confirmed, SensitiveExclusions: exclusions, ExpectedOutput: "verified code", ResponsibleHuman: "Yang Yang",
		CapabilityEnvelope: capabilities, Initiator: "human", CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("NewRunCharter() error = %v", err)
	}

	criteria[0] = testCriterion(t)
	revisions[0] = NewContextRevisionID()
	confirmed[0] = NewContextRevisionID()
	exclusions[0] = NewContextEntryID()
	capabilities["read_workspace"] = false
	if charter.Criteria()[0].ID() != criterion.ID() || charter.CapabilityEnvelope()["read_workspace"] != true {
		t.Fatal("charter aliases caller-owned criteria or capabilities")
	}
	confirmedCopy := charter.ConfirmedSensitiveRevisionIDs()
	if len(confirmedCopy) != 1 {
		t.Fatalf("confirmed sensitive revisions = %v, want one deduplicated ID", confirmedCopy)
	}
	confirmedCopy[0] = NewContextRevisionID()
	if charter.ConfirmedSensitiveRevisionIDs()[0] == confirmedCopy[0] {
		t.Fatal("ConfirmedSensitiveRevisionIDs returns mutable internal slice")
	}
	copyCaps := charter.CapabilityEnvelope()
	copyCaps["read_workspace"] = false
	if !charter.CapabilityEnvelope()["read_workspace"] {
		t.Fatal("CapabilityEnvelope returns mutable internal map")
	}
}

func TestRunCharterRejectsMissingRequiredFields(t *testing.T) {
	params := RunCharterParams{
		ID: NewCharterID(), TaskID: NewTaskID(), TaskGoal: "goal", Criteria: []AcceptanceCriterion{testCriterion(t)},
		ContextRevisionIDs: []ContextRevisionID{NewContextRevisionID()}, WorkspaceRoot: "/tmp/chora", AdapterID: "fake",
		SandboxMode: "workspace-write", ExpectedOutput: "output", ResponsibleHuman: "Yang Yang",
		CapabilityEnvelope: CapabilityEnvelope{"read": true}, Initiator: "human", CreatedAt: time.Now().UTC(),
	}
	tests := []struct {
		name   string
		mutate func(*RunCharterParams)
	}{
		{"goal", func(params *RunCharterParams) { params.TaskGoal = "" }},
		{"criteria", func(params *RunCharterParams) { params.Criteria = nil }},
		{"context", func(params *RunCharterParams) { params.ContextRevisionIDs = nil }},
		{"workspace", func(params *RunCharterParams) { params.WorkspaceRoot = "relative" }},
		{"adapter", func(params *RunCharterParams) { params.AdapterID = "" }},
		{"sandbox", func(params *RunCharterParams) { params.SandboxMode = "" }},
		{"expected output", func(params *RunCharterParams) { params.ExpectedOutput = "" }},
		{"responsible human", func(params *RunCharterParams) { params.ResponsibleHuman = "" }},
		{"capabilities", func(params *RunCharterParams) { params.CapabilityEnvelope = nil }},
		{"initiator", func(params *RunCharterParams) { params.Initiator = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := params
			test.mutate(&candidate)
			if _, err := NewRunCharter(candidate); !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("error = %v, want ErrInvalidArgument", err)
			}
		})
	}
}

func TestPiRunCharterRequiresExactProfileBindingAndNonPiRejectsOne(t *testing.T) {
	params := RunCharterParams{
		ID: NewCharterID(), TaskID: NewTaskID(), TaskGoal: "goal", Criteria: []AcceptanceCriterion{testCriterion(t)},
		ContextRevisionIDs: []ContextRevisionID{NewContextRevisionID()}, WorkspaceRoot: "/tmp/chora", AdapterID: "pi",
		SandboxMode: "docker", ExpectedOutput: "output", ResponsibleHuman: "Yang Yang",
		CapabilityEnvelope: CapabilityEnvelope{"read": true}, Initiator: "human", CreatedAt: time.Now().UTC(),
	}
	if _, err := NewRunCharter(params); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("unbound Pi Charter error = %v", err)
	}
	binding, err := NewAgentExecutionProfileBinding(AgentExecutionProfileMinimal)
	if err != nil {
		t.Fatal(err)
	}
	params.AgentExecutionProfileBinding = binding
	charter, err := NewRunCharter(params)
	if err != nil || charter.AgentExecutionProfileBinding() != binding {
		t.Fatalf("bound Pi Charter = %#v, %v", charter, err)
	}
	params.AdapterID = "fake"
	if _, err := NewRunCharter(params); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("bound Fake Charter error = %v", err)
	}
}

func TestPiAttemptRequiresExactProfileBindingAndKeepsItAcrossTransition(t *testing.T) {
	binding, err := NewAgentExecutionProfileBinding(AgentExecutionProfileTrustedLocal)
	if err != nil {
		t.Fatal(err)
	}
	params := AttemptParams{ID: NewAttemptID(), RunID: NewRunID(), Sequence: 1, ContextSnapshotID: NewContextSnapshotID(), ContextDigest: [32]byte{1}, AdapterID: "pi", CreatedAt: time.Now().UTC()}
	if _, err := NewAttempt(params); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("unbound Pi Attempt error = %v", err)
	}
	params.AgentExecutionProfileBinding = binding
	attempt, err := NewAttempt(params)
	if err != nil {
		t.Fatal(err)
	}
	starting, err := attempt.Transition(AttemptEventStartAttempt)
	if err != nil || starting.AgentExecutionProfileBinding() != binding {
		t.Fatalf("transition binding = %#v, %v", starting.AgentExecutionProfileBinding(), err)
	}

	other, err := NewAgentExecutionProfileBinding(AgentExecutionProfileStandard)
	if err != nil {
		t.Fatal(err)
	}
	mutated, err := RestoreAttempt(AttemptRecord{ID: attempt.ID(), RunID: attempt.RunID(), Sequence: attempt.Sequence(), ContextSnapshotID: attempt.ContextSnapshotID(), ContextDigest: attempt.ContextDigest(), AdapterID: attempt.AdapterID(), AgentExecutionProfileBinding: other, State: AttemptStateStarting, CreatedAt: attempt.CreatedAt()})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateAttemptSuccessor(attempt, mutated); !errors.Is(err, ErrInvalidAttemptTransition) {
		t.Fatalf("binding mutation error = %v", err)
	}
}

func TestAgentRunTracksLifecycleAndRejectsTerminalMutation(t *testing.T) {
	now := time.Now().UTC()
	run, err := NewAgentRun(NewRunID(), NewTaskID(), NewCharterID(), now)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		command CommandKind
		at      time.Time
	}{
		{CommandPrepareRun, now.Add(time.Second)},
		{CommandStartAttempt, now.Add(2 * time.Second)},
		{CommandAttemptStarted, now.Add(3 * time.Second)},
		{CommandSubmitAgentReport, now.Add(4 * time.Second)},
		{CommandStartVerification, now.Add(5 * time.Second)},
		{CommandVerificationPassed, now.Add(6 * time.Second)},
	} {
		run, err = run.Transition(step.command, step.at)
		if err != nil {
			t.Fatalf("Transition(%q) error = %v", step.command, err)
		}
	}
	if run.State() != RunStateAwaitingReview || run.Version() != 6 || run.UpdatedAt() != now.Add(6*time.Second) {
		t.Fatalf("run before review = %#v", run)
	}
	for _, command := range []CommandKind{CommandReviewAccept, CommandReviewReject} {
		unchanged, err := run.Transition(command, now.Add(7*time.Second))
		if !errors.Is(err, ErrInvalidRunTransition) {
			t.Fatalf("Transition(%q) error = %v, want ErrInvalidRunTransition", command, err)
		}
		if unchanged.State() != run.State() || unchanged.Version() != run.Version() || unchanged.UpdatedAt() != run.UpdatedAt() {
			t.Fatalf("rejected review bypass mutated run: before=%#v after=%#v", run, unchanged)
		}
	}
	decisionParams := ReviewDecisionParams{ID: NewReviewDecisionID(), RunID: run.ID(), ExpectedRunVersion: run.Version(), Comment: "accepted", DecidedAt: now.Add(7 * time.Second)}
	decision, err := NewAcceptedReviewDecision(decisionParams, reviewAuthorizationForTest(run.ID(), ReviewDecisionAccept, run.Version(), true))
	if err != nil {
		t.Fatal(err)
	}
	run, err = run.ApplyReview(decision)
	if err != nil {
		t.Fatalf("ApplyReview() error = %v", err)
	}
	if run.State() != RunStateAccepted || run.Version() != 7 || run.CurrentAttemptNumber() != 1 || run.StartedAt().IsZero() || run.ReviewRequestedAt().IsZero() || run.TerminalAt().IsZero() || run.UpdatedAt() != decision.DecidedAt() {
		t.Fatalf("unexpected lifecycle: %#v", run)
	}
	unchanged, err := run.Transition(CommandPrepareRetry, now.Add(8*time.Second))
	if !errors.Is(err, ErrInvalidRunTransition) {
		t.Fatalf("terminal transition error = %v, want ErrInvalidRunTransition", err)
	}
	if unchanged.State() != run.State() || unchanged.Version() != run.Version() || unchanged.UpdatedAt() != run.UpdatedAt() {
		t.Fatal("failed terminal transition mutated run")
	}
}

func TestAgentRunRejectsOutOfOrderTransitionsWithoutMutation(t *testing.T) {
	now := time.Now().UTC()
	run, err := NewAgentRun(NewRunID(), NewTaskID(), NewCharterID(), now)
	if err != nil {
		t.Fatal(err)
	}
	run, err = run.Transition(CommandPrepareRun, now.Add(10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	unchanged, err := run.Transition(CommandStartAttempt, now.Add(time.Second))
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("out-of-order start error = %v, want ErrInvalidArgument", err)
	}
	if unchanged.State() != run.State() || unchanged.Version() != run.Version() || unchanged.UpdatedAt() != run.UpdatedAt() {
		t.Fatal("out-of-order start mutated run")
	}

	run, err = run.Transition(CommandStartAttempt, now.Add(20*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	run, err = run.Transition(CommandAttemptFailed, now.Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	unchanged, err = run.Transition(CommandPrepareRetry, now.Add(25*time.Second))
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("out-of-order retry error = %v, want ErrInvalidArgument", err)
	}
	if unchanged.State() != run.State() || unchanged.Version() != run.Version() || unchanged.UpdatedAt() != run.UpdatedAt() {
		t.Fatal("out-of-order retry mutated run")
	}
}

func TestAgentRunCancelledRetryCreatesNonTerminalSuccessor(t *testing.T) {
	now := time.Now().UTC()
	run, err := NewAgentRun(NewRunID(), NewTaskID(), NewCharterID(), now)
	if err != nil {
		t.Fatal(err)
	}
	for index, command := range []CommandKind{CommandPrepareRun, CommandStartAttempt, CommandRequestCancel, CommandStopConfirmedForCancel} {
		run, err = run.Transition(command, now.Add(time.Duration(index+1)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
	}
	if run.State() != RunStateCancelled || run.TerminalAt().IsZero() {
		t.Fatalf("cancelled run = %#v", run)
	}

	retry, err := run.Transition(CommandPrepareRetry, now.Add(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if retry.State() != RunStateReady || retry.CurrentAttemptNumber() != 2 || !retry.TerminalAt().IsZero() {
		t.Fatalf("cancelled retry = %#v", retry)
	}
}

func TestAttemptRequiresExactlyOneEffectiveContextSnapshot(t *testing.T) {
	digest := [32]byte{1}
	attempt, err := NewAttempt(AttemptParams{
		ID: NewAttemptID(), RunID: NewRunID(), Sequence: 1, ContextSnapshotID: NewContextSnapshotID(), ContextDigest: digest,
		AdapterID: "fake", CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("NewAttempt() error = %v", err)
	}
	if attempt.State() != AttemptStateCreated || attempt.ContextDigest() != digest {
		t.Fatalf("attempt = %#v", attempt)
	}

	params := AttemptParams{ID: NewAttemptID(), RunID: NewRunID(), Sequence: 1, AdapterID: "fake", CreatedAt: time.Now().UTC()}
	if _, err := NewAttempt(params); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("missing snapshot error = %v, want ErrInvalidArgument", err)
	}
	params.ContextSnapshotID = NewContextSnapshotID()
	if _, err := NewAttempt(params); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("missing digest error = %v, want ErrInvalidArgument", err)
	}
}

func reviewAuthorizationForTest(runID RunID, kind ReviewDecisionKind, expectedVersion uint64, deviceOwnerPresent bool) HumanReviewAuthorization {
	return HumanReviewAuthorization{
		humanIssued: true, deviceOwnerPresent: deviceOwnerPresent,
		runID: runID, decisionKind: kind, expectedRunVersion: expectedVersion,
	}
}

func TestAcceptedReviewRequiresDeviceOwnerAuthorization(t *testing.T) {
	runID := NewRunID()
	params := ReviewDecisionParams{ID: NewReviewDecisionID(), RunID: runID, ExpectedRunVersion: 4, Comment: "accepted", Checks: []ReviewedCheck{{Name: "tests", Status: "passed"}}, LinkedArtifacts: []ArtifactLink{"artifact://result"}, DecidedAt: time.Now().UTC()}
	for _, authorization := range []HumanReviewAuthorization{
		{},
		reviewAuthorizationForTest(runID, ReviewDecisionAccept, 4, false),
		reviewAuthorizationForTest(NewRunID(), ReviewDecisionAccept, 4, true),
		reviewAuthorizationForTest(runID, ReviewDecisionReject, 4, true),
		reviewAuthorizationForTest(runID, ReviewDecisionAccept, 3, true),
	} {
		if _, err := NewAcceptedReviewDecision(params, authorization); !errors.Is(err, ErrUnauthorizedReview) {
			t.Fatalf("authorization %#v error = %v, want ErrUnauthorizedReview", authorization, err)
		}
	}
	decision, err := NewAcceptedReviewDecision(params, reviewAuthorizationForTest(runID, ReviewDecisionAccept, 4, true))
	if err != nil {
		t.Fatalf("authorized acceptance error = %v", err)
	}
	if decision.Kind() != ReviewDecisionAccept {
		t.Fatalf("decision kind = %q, want accept", decision.Kind())
	}
	if decision.ExpectedRunVersion() != 4 {
		t.Fatalf("decision expected version = %d, want 4", decision.ExpectedRunVersion())
	}
	missingID := params
	missingID.ID = ReviewDecisionID{}
	if _, err := NewAcceptedReviewDecision(missingID, reviewAuthorizationForTest(runID, ReviewDecisionAccept, 4, true)); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("missing decision ID error = %v, want ErrInvalidArgument", err)
	}
	checks := decision.Checks()
	checks[0].Name = "mutated"
	if decision.Checks()[0].Name != "tests" {
		t.Fatal("review checks are not defensively copied")
	}
}

func TestRejectedReviewRequiresComment(t *testing.T) {
	runID := NewRunID()
	params := ReviewDecisionParams{ID: NewReviewDecisionID(), RunID: runID, ExpectedRunVersion: 4, Checks: []ReviewedCheck{{Name: "tests", Status: "failed"}}, DecidedAt: time.Now().UTC()}
	humanCommand := reviewAuthorizationForTest(runID, ReviewDecisionReject, 4, false)
	if _, err := NewRejectedReviewDecision(params, humanCommand); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("missing note error = %v, want ErrInvalidArgument", err)
	}
	params.Comment = "criterion is not met"
	if _, err := NewRejectedReviewDecision(params, HumanReviewAuthorization{}); !errors.Is(err, ErrUnauthorizedReview) {
		t.Fatalf("missing human command error = %v, want ErrUnauthorizedReview", err)
	}
	decision, err := NewRejectedReviewDecision(params, humanCommand)
	if err != nil {
		t.Fatalf("NewRejectedReviewDecision() error = %v", err)
	}
	if decision.Kind() != ReviewDecisionReject {
		t.Fatalf("decision kind = %q, want reject", decision.Kind())
	}
}

func TestAgentRunApplyReviewRejectsWrongRunStaleVersionAndReuse(t *testing.T) {
	now := time.Now().UTC()
	awaiting := func(t *testing.T) AgentRun {
		t.Helper()
		run, err := NewAgentRun(NewRunID(), NewTaskID(), NewCharterID(), now)
		if err != nil {
			t.Fatal(err)
		}
		for index, command := range []CommandKind{CommandPrepareRun, CommandStartAttempt, CommandAttemptStarted, CommandSubmitAgentReport, CommandStartVerification, CommandVerificationPassed} {
			run, err = run.Transition(command, now.Add(time.Duration(index+1)*time.Second))
			if err != nil {
				t.Fatal(err)
			}
		}
		return run
	}

	run := awaiting(t)
	other := awaiting(t)
	wrongRunParams := ReviewDecisionParams{ID: NewReviewDecisionID(), RunID: other.ID(), ExpectedRunVersion: other.Version(), Comment: "accept", DecidedAt: now.Add(7 * time.Second)}
	wrongRunDecision, err := NewAcceptedReviewDecision(wrongRunParams, reviewAuthorizationForTest(other.ID(), ReviewDecisionAccept, other.Version(), true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run.ApplyReview(wrongRunDecision); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("wrong-run decision error = %v, want ErrInvalidArgument", err)
	}

	staleParams := ReviewDecisionParams{ID: NewReviewDecisionID(), RunID: run.ID(), ExpectedRunVersion: run.Version() - 1, Comment: "accept", DecidedAt: now.Add(7 * time.Second)}
	staleDecision, err := NewAcceptedReviewDecision(staleParams, reviewAuthorizationForTest(run.ID(), ReviewDecisionAccept, staleParams.ExpectedRunVersion, true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run.ApplyReview(staleDecision); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("stale decision error = %v, want ErrInvalidArgument", err)
	}

	acceptParams := ReviewDecisionParams{ID: NewReviewDecisionID(), RunID: run.ID(), ExpectedRunVersion: run.Version(), Comment: "accept", DecidedAt: now.Add(7 * time.Second)}
	acceptDecision, err := NewAcceptedReviewDecision(acceptParams, reviewAuthorizationForTest(run.ID(), ReviewDecisionAccept, run.Version(), true))
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := run.ApplyReview(acceptDecision)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.State() != RunStateAccepted || accepted.Version() != run.Version()+1 {
		t.Fatalf("accepted run = %#v", accepted)
	}
	if _, err := accepted.ApplyReview(acceptDecision); err == nil {
		t.Fatal("reused decision was accepted")
	}

	rejectRun := awaiting(t)
	rejectParams := ReviewDecisionParams{ID: NewReviewDecisionID(), RunID: rejectRun.ID(), ExpectedRunVersion: rejectRun.Version(), Comment: "revise", DecidedAt: now.Add(7 * time.Second)}
	rejectDecision, err := NewRejectedReviewDecision(rejectParams, reviewAuthorizationForTest(rejectRun.ID(), ReviewDecisionReject, rejectRun.Version(), false))
	if err != nil {
		t.Fatal(err)
	}
	rejected, err := rejectRun.ApplyReview(rejectDecision)
	if err != nil {
		t.Fatal(err)
	}
	if rejected.State() != RunStateRevisionRequired || rejected.Version() != rejectRun.Version()+1 {
		t.Fatalf("rejected run = %#v", rejected)
	}
}

func TestAgentRunApplyReviewRejectsDecisionBeforeUpdatedAt(t *testing.T) {
	now := time.Now().UTC()
	run, err := NewAgentRun(NewRunID(), NewTaskID(), NewCharterID(), now)
	if err != nil {
		t.Fatal(err)
	}
	for index, command := range []CommandKind{CommandPrepareRun, CommandStartAttempt, CommandAttemptStarted, CommandSubmitAgentReport, CommandStartVerification, CommandVerificationPassed} {
		run, err = run.Transition(command, now.Add(time.Duration(index+10)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
	}
	params := ReviewDecisionParams{ID: NewReviewDecisionID(), RunID: run.ID(), ExpectedRunVersion: run.Version(), Comment: "accept", DecidedAt: now.Add(time.Second)}
	decision, err := NewAcceptedReviewDecision(params, reviewAuthorizationForTest(run.ID(), ReviewDecisionAccept, run.Version(), true))
	if err != nil {
		t.Fatal(err)
	}
	unchanged, err := run.ApplyReview(decision)
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("out-of-order review error = %v, want ErrInvalidArgument", err)
	}
	if unchanged.State() != run.State() || unchanged.Version() != run.Version() || unchanged.UpdatedAt() != run.UpdatedAt() {
		t.Fatal("out-of-order review mutated run")
	}
}
