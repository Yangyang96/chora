package domain

import (
	"strings"
	"testing"
	"time"
)

func TestAgentReportIsImmutableNonAuthoritativeOutput(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	claims := []AgentClaimedCheck{{CriterionID: NewCriterionID(), Status: "PASS", Evidence: "agent says tests passed"}}
	report, err := NewAgentReport(AgentReportParams{
		ID: NewAgentReportID(), RunID: NewRunID(), AttemptID: NewAttemptID(),
		Summary: "candidate complete", FinalText: "all checks pass", ClaimedChecks: claims,
		CompletedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	claims[0].Status = "FAIL"
	got := report.ClaimedChecks()
	got[0].Evidence = "mutated"
	if report.FinalText() != "all checks pass" || report.ClaimedChecks()[0].Status != "PASS" || report.ClaimedChecks()[0].Evidence != "agent says tests passed" {
		t.Fatalf("AgentReport was mutable: %#v", report.ClaimedChecks())
	}
}

func TestVerificationRunBindsEveryFrozenIdentity(t *testing.T) {
	bindings := VerificationBindings{
		BaselineDigest: digest(1), PatchDigest: digest(2), ContextSnapshotDigest: digest(3),
		AcceptanceContractDigest: digest(4), VerifierPolicyVersion: "chora.verifier-policy.v1", VerifierPolicyDigest: digest(5),
	}
	run, err := NewVerificationRun(NewVerificationRunID(), NewRunID(), NewAttemptID(), bindings, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if run.State() != VerificationRunAwaiting || run.Bindings() != bindings {
		t.Fatalf("verification run = %#v", run)
	}
}

func TestVerificationRunTransitionsAndRestorePreserveAuthority(t *testing.T) {
	now := time.Now().UTC()
	bindings := VerificationBindings{BaselineDigest: digest(1), PatchDigest: digest(2), ContextSnapshotDigest: digest(3), AcceptanceContractDigest: digest(4), VerifierPolicyVersion: "chora.verifier-policy.v1", VerifierPolicyDigest: digest(5)}
	run, err := NewVerificationRun(NewVerificationRunID(), NewRunID(), NewAttemptID(), bindings, now)
	if err != nil {
		t.Fatal(err)
	}
	verifying, err := run.Transition(VerificationRunStart, now.Add(time.Second))
	if err != nil || verifying.State() != VerificationRunVerifying {
		t.Fatalf("start = %#v, %v", verifying, err)
	}
	recovery, err := verifying.Transition(VerificationRunIntegrityUncertain, now.Add(2*time.Second))
	if err != nil || recovery.State() != VerificationRunRecoveryRequired {
		t.Fatalf("recovery = %#v, %v", recovery, err)
	}
	retried, err := recovery.Transition(VerificationRunRetry, now.Add(3*time.Second))
	if err != nil || retried.State() != VerificationRunVerifying || retried.Bindings() != bindings {
		t.Fatalf("retry = %#v, %v", retried, err)
	}
	restored, err := RestoreVerificationRun(VerificationRunRecord{ID: retried.ID(), RunID: retried.RunID(), AgentAttemptID: retried.AgentAttemptID(), Bindings: bindings, State: retried.State(), CreatedAt: retried.CreatedAt(), UpdatedAt: retried.UpdatedAt()})
	if err != nil || restored.State() != VerificationRunVerifying || restored.Bindings() != bindings {
		t.Fatalf("restore = %#v, %v", restored, err)
	}
	if _, err := restored.Transition(VerificationRunCancelConfirmed, now.Add(4*time.Second)); err != nil {
		t.Fatalf("cancel after restored verifying: %v", err)
	}
	awaiting, _ := restored.Transition(VerificationRunCancelConfirmed, now.Add(4*time.Second))
	if retriedAfterCancel, err := awaiting.Transition(VerificationRunRetry, now.Add(5*time.Second)); err != nil || retriedAfterCancel.State() != VerificationRunVerifying {
		t.Fatalf("explicit retry after cancel = %#v, %v", retriedAfterCancel, err)
	}
}

func TestVerificationAttemptLineagePreservesBindings(t *testing.T) {
	bindings := VerificationBindings{BaselineDigest: digest(1), PatchDigest: digest(2), ContextSnapshotDigest: digest(3), AcceptanceContractDigest: digest(4), VerifierPolicyVersion: "chora.verifier-policy.v1", VerifierPolicyDigest: digest(5)}
	verificationRun, _ := NewVerificationRun(NewVerificationRunID(), NewRunID(), NewAttemptID(), bindings, time.Now())
	first, err := NewVerificationAttempt(VerificationAttemptParams{ID: NewVerificationAttemptID(), VerificationRunID: verificationRun.ID(), Sequence: 1, Bindings: bindings, CreatedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	started, err := first.Transition(VerificationAttemptStart)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := started.Transition(VerificationAttemptCancelConfirmed)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.State() != VerificationAttemptCancelled {
		t.Fatalf("cancelled state = %q", cancelled.State())
	}
	predecessor := first.ID()
	second, err := NewVerificationAttempt(VerificationAttemptParams{ID: NewVerificationAttemptID(), VerificationRunID: verificationRun.ID(), Sequence: 2, Predecessor: &predecessor, Bindings: bindings, CreatedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := second.Predecessor(); !ok || got != first.ID() || second.Bindings() != bindings {
		t.Fatalf("successor lineage = %#v", second)
	}
	restored, err := RestoreVerificationAttempt(VerificationAttemptRecord{ID: second.ID(), VerificationRunID: second.VerificationRunID(), Sequence: second.Sequence(), Predecessor: &predecessor, Bindings: second.Bindings(), State: second.State(), CreatedAt: second.CreatedAt()})
	if err != nil || restored.ID() != second.ID() || restored.State() != VerificationAttemptPending {
		t.Fatalf("restore = %#v, %v", restored, err)
	}
}

func TestAcceptanceCheckStatusAndAggregation(t *testing.T) {
	criterion1, criterion2 := NewCriterionID(), NewCriterionID()
	evidence1, evidence2 := NewVerificationCommandEvidenceID(), NewVerificationCommandEvidenceID()
	passed1, err := NewAcceptanceCheck(NewCheckID(), criterion1, AcceptanceCheckPassed, VerificationEvidenceTrusted, []VerificationCommandEvidenceID{evidence1})
	if err != nil {
		t.Fatal(err)
	}
	passed2, _ := NewAcceptanceCheck(NewCheckID(), criterion2, AcceptanceCheckPassed, VerificationEvidenceTrusted, []VerificationCommandEvidenceID{evidence2})
	failed, _ := NewAcceptanceCheck(NewCheckID(), criterion2, AcceptanceCheckFailed, VerificationEvidenceTrusted, []VerificationCommandEvidenceID{evidence2})
	unknown, _ := NewAcceptanceCheck(NewCheckID(), criterion2, AcceptanceCheckUnknown, VerificationEvidenceTrusted, []VerificationCommandEvidenceID{evidence2})

	for _, test := range []struct {
		name   string
		checks []AcceptanceCheck
		want   ResultOutcome
	}{
		{"all passed", []AcceptanceCheck{passed1, passed2}, ResultReviewReady},
		{"failed", []AcceptanceCheck{passed1, failed}, ResultNeedsRevision},
		{"unknown", []AcceptanceCheck{passed1, unknown}, ResultNeedsRevision},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := AggregateAcceptanceChecks(test.checks)
			if err != nil || got != test.want {
				t.Fatalf("AggregateAcceptanceChecks() = %q, %v; want %q", got, err, test.want)
			}
		})
	}
	for _, status := range []AcceptanceCheckStatus{"PASS", "FAIL", "UNKNOWN", "", "green"} {
		if _, err := NewAcceptanceCheck(NewCheckID(), criterion1, status, VerificationEvidenceTrusted, []VerificationCommandEvidenceID{evidence1}); err == nil {
			t.Fatalf("accepted non-authoritative status %q", status)
		}
	}
	if _, err := NewAcceptanceCheck(NewCheckID(), criterion1, AcceptanceCheckUnknown, VerificationEvidenceUntrusted, []VerificationCommandEvidenceID{evidence1}); err == nil {
		t.Fatal("unknown Check accepted untrustworthy verification evidence")
	}
}

func TestResultRequiresCompletedTrustedEvidenceAndCleanup(t *testing.T) {
	bindings := VerificationBindings{BaselineDigest: digest(1), PatchDigest: digest(2), ContextSnapshotDigest: digest(3), AcceptanceContractDigest: digest(4), VerifierPolicyVersion: "chora.verifier-policy.v1", VerifierPolicyDigest: digest(5)}
	verificationRun, _ := NewVerificationRun(NewVerificationRunID(), NewRunID(), NewAttemptID(), bindings, time.Now())
	attempt, _ := NewVerificationAttempt(VerificationAttemptParams{ID: NewVerificationAttemptID(), VerificationRunID: verificationRun.ID(), Sequence: 1, Bindings: bindings, CreatedAt: time.Now()})
	attempt, _ = attempt.Transition(VerificationAttemptStart)
	attempt, _ = attempt.Transition(VerificationAttemptEvidenceCompleted)
	check, _ := NewAcceptanceCheck(NewCheckID(), NewCriterionID(), AcceptanceCheckPassed, VerificationEvidenceTrusted, []VerificationCommandEvidenceID{NewVerificationCommandEvidenceID()})

	for _, proof := range []VerificationCompletionProof{
		{EvidenceComplete: false, CleanupProven: true},
		{EvidenceComplete: true, CleanupProven: false},
	} {
		if _, err := NewVerificationResult(NewResultID(), verificationRun, attempt, []AcceptanceCheck{check}, proof, time.Now()); err == nil {
			t.Fatalf("created Result with incomplete proof %#v", proof)
		}
	}
	result, err := NewVerificationResult(NewResultID(), verificationRun, attempt, []AcceptanceCheck{check}, VerificationCompletionProof{EvidenceComplete: true, CleanupProven: true}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome() != ResultReviewReady || result.Bindings() != bindings {
		t.Fatalf("Result = %#v", result)
	}
}

func TestVerificationCommandEvidenceFreezesArgvAndBoundedLogs(t *testing.T) {
	now := time.Now().UTC()
	criterion := NewCriterionID()
	argv := []string{"go", "test", "./internal/domain"}
	stdout := VerificationStreamLog{FullSHA256: digest(6), TotalBytes: 12, RetainedBody: "redacted", Truncated: true, TruncationBoundary: 8, RedactionPolicyVersion: "chora.log-redaction.v1"}
	stderr := VerificationStreamLog{FullSHA256: digest(7), TotalBytes: 0, RetainedBody: "", RedactionPolicyVersion: "chora.log-redaction.v1"}
	exitCode := 0
	evidence, err := NewVerificationCommandEvidence(VerificationCommandEvidenceParams{
		ID: NewVerificationCommandEvidenceID(), VerificationAttemptID: NewVerificationAttemptID(),
		CommandID: "domain-tests", CriterionIDs: []CriterionID{criterion}, Argv: argv,
		StartedAt: now, EndedAt: now.Add(time.Second), Classification: VerificationCommandExited,
		ExitCode: &exitCode, Stdout: stdout, Stderr: stderr, VerifierIdentity: "docker@sha256:verified", WorkspaceIdentity: "sha256:" + strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	argv[0] = "sh"
	got := evidence.Argv()
	got[0] = "bash"
	if evidence.Argv()[0] != "go" || evidence.CriterionIDs()[0] != criterion || evidence.Stdout().FullSHA256 != digest(6) || !evidence.Stdout().Truncated {
		t.Fatalf("command evidence mutated: %#v", evidence)
	}
}

func TestVerificationCommandEvidenceAllowsTrustedPolicyUnavailableClassification(t *testing.T) {
	now := time.Now().UTC()
	empty := VerificationStreamLog{FullSHA256: digest(8), RedactionPolicyVersion: "chora.verifier-redaction.v1"}
	evidence, err := NewVerificationCommandEvidence(VerificationCommandEvidenceParams{
		ID: NewVerificationCommandEvidenceID(), VerificationAttemptID: NewVerificationAttemptID(),
		CommandID: "domain-tests", CriterionIDs: []CriterionID{NewCriterionID()}, Argv: []string{"go", "test", "./internal/domain"},
		StartedAt: now, EndedAt: now, Classification: VerificationCommandUnavailable,
		Stdout: empty, Stderr: empty, VerifierIdentity: "docker@sha256:verified", WorkspaceIdentity: "sha256:" + strings.Repeat("b", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Classification() != VerificationCommandUnavailable {
		t.Fatalf("classification = %q", evidence.Classification())
	}
}

func digest(value byte) [32]byte {
	var digest [32]byte
	digest[0] = value
	return digest
}
