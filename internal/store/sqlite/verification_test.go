package sqlite_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
	"github.com/Yangyang96/chora/internal/store/sqlite"
)

type verificationFixture struct {
	db           *sqlite.Store
	seeded       seed
	criterionID  domain.CriterionID
	bindings     domain.VerificationBindings
	run          domain.VerificationRun
	attempt      domain.VerificationAttempt
	agentAttempt domain.Attempt
}

func newVerificationFixture(t *testing.T) verificationFixture {
	t.Helper()
	ctx := context.Background()
	db, seeded := openSeeded(t)
	snapshot := assembleSeedSnapshot(t, db, seeded)
	agentAttempt, err := domain.NewAttempt(domain.AttemptParams{ID: domain.NewAttemptID(), RunID: seeded.run.ID(), Sequence: 1, ContextSnapshotID: snapshot.ID(), ContextDigest: snapshot.Digest(), AdapterID: "fake", CreatedAt: seeded.now})
	if err != nil {
		t.Fatal(err)
	}
	criterionID := seeded.task.Criteria()[0].ID()
	report, err := domain.NewAgentReport(domain.AgentReportParams{ID: domain.NewAgentReportID(), RunID: seeded.run.ID(), AttemptID: agentAttempt.ID(), Summary: "agent summary", FinalText: "agent final", ClaimedChecks: []domain.AgentClaimedCheck{{CriterionID: criterionID, Status: "passed", Evidence: "agent claim only"}}, CompletedAt: seeded.now.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	bindings := domain.VerificationBindings{
		BaselineDigest:           sha256.Sum256([]byte("baseline")),
		PatchDigest:              sha256.Sum256([]byte("patch")),
		ContextSnapshotDigest:    snapshot.Digest(),
		AcceptanceContractDigest: sha256.Sum256([]byte("contract")),
		VerifierPolicyVersion:    "m1-v1",
		VerifierPolicyDigest:     sha256.Sum256([]byte("policy")),
	}
	verificationRun, err := domain.NewVerificationRun(domain.NewVerificationRunID(), seeded.run.ID(), agentAttempt.ID(), bindings, seeded.now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	verificationAttempt, err := domain.NewVerificationAttempt(domain.VerificationAttemptParams{ID: domain.NewVerificationAttemptID(), VerificationRunID: verificationRun.ID(), Sequence: 1, Bindings: bindings, CreatedAt: seeded.now.Add(2 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	err = db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.InsertAttempt(ctx, agentAttempt); err != nil {
			return err
		}
		if err := tx.InsertAgentReport(ctx, report); err != nil {
			return err
		}
		if err := tx.InsertVerificationRun(ctx, verificationRun); err != nil {
			return err
		}
		return tx.InsertVerificationAttempt(ctx, verificationAttempt, verificationAttempt.CreatedAt())
	})
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	return verificationFixture{db: db, seeded: seeded, criterionID: criterionID, bindings: bindings, run: verificationRun, attempt: verificationAttempt, agentAttempt: agentAttempt}
}

func verificationEvidence(t *testing.T, fixture verificationFixture, classification domain.VerificationCommandClassification, exitCode *int) domain.VerificationCommandEvidence {
	t.Helper()
	stdoutBody, stderrBody := "stdout body", "stderr body"
	evidence, err := domain.NewVerificationCommandEvidence(domain.VerificationCommandEvidenceParams{
		ID:                    domain.NewVerificationCommandEvidenceID(),
		VerificationAttemptID: fixture.attempt.ID(),
		CommandID:             "domain-tests",
		CriterionIDs:          []domain.CriterionID{fixture.criterionID},
		Argv:                  []string{"go", "test", "./internal/domain"},
		StartedAt:             fixture.seeded.now.Add(4 * time.Second),
		EndedAt:               fixture.seeded.now.Add(5 * time.Second),
		Classification:        classification,
		ExitCode:              exitCode,
		Stdout:                domain.VerificationStreamLog{FullSHA256: sha256.Sum256([]byte(stdoutBody)), TotalBytes: int64(len(stdoutBody)), RetainedBody: stdoutBody, RedactionPolicyVersion: "redact-v1"},
		Stderr:                domain.VerificationStreamLog{FullSHA256: sha256.Sum256([]byte(stderrBody)), TotalBytes: int64(len(stderrBody)), RetainedBody: stderrBody, RedactionPolicyVersion: "redact-v1"},
		VerifierIdentity:      "docker:image@sha256:test",
		WorkspaceIdentity:     fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("verification-workspace"))),
	})
	if err != nil {
		t.Fatal(err)
	}
	return evidence
}

func completeVerification(t *testing.T, fixture verificationFixture, status domain.AcceptanceCheckStatus, classification domain.VerificationCommandClassification, exitCode *int) (domain.VerificationCommandEvidence, domain.AcceptanceCheck, domain.VerificationResult) {
	t.Helper()
	ctx := context.Background()
	runningRun, err := fixture.run.Transition(domain.VerificationRunStart, fixture.seeded.now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	runningAttempt, err := fixture.attempt.Transition(domain.VerificationAttemptStart)
	if err != nil {
		t.Fatal(err)
	}
	evidence := verificationEvidence(t, fixture, classification, exitCode)
	check, err := domain.NewAcceptanceCheck(domain.NewCheckID(), fixture.criterionID, status, domain.VerificationEvidenceTrusted, []domain.VerificationCommandEvidenceID{evidence.ID()})
	if err != nil {
		t.Fatal(err)
	}
	completedAttempt, err := runningAttempt.Transition(domain.VerificationAttemptEvidenceCompleted)
	if err != nil {
		t.Fatal(err)
	}
	completedRun, err := runningRun.Transition(domain.VerificationRunEvidenceCompleted, fixture.seeded.now.Add(7*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	result, err := domain.NewVerificationResult(domain.NewResultID(), completedRun, completedAttempt, []domain.AcceptanceCheck{check}, domain.VerificationCompletionProof{EvidenceComplete: true, CleanupProven: true}, fixture.seeded.now.Add(8*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	err = fixture.db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.SaveVerificationRunCAS(ctx, domain.VerificationRunAwaiting, runningRun); err != nil {
			return err
		}
		if err := tx.SaveVerificationAttemptCAS(ctx, domain.VerificationAttemptPending, storecontract.VerificationAttemptRecord{Attempt: runningAttempt, UpdatedAt: fixture.seeded.now.Add(3 * time.Second)}); err != nil {
			return err
		}
		if err := tx.InsertVerificationCommandEvidence(ctx, evidence); err != nil {
			return err
		}
		if err := tx.InsertVerificationAcceptanceCheck(ctx, fixture.attempt.ID(), check); err != nil {
			return err
		}
		if err := tx.SaveVerificationAttemptCAS(ctx, domain.VerificationAttemptRunning, storecontract.VerificationAttemptRecord{Attempt: completedAttempt, EvidenceComplete: true, CleanupProven: true, WorkspaceIdentity: evidence.WorkspaceIdentity(), UpdatedAt: fixture.seeded.now.Add(7 * time.Second)}); err != nil {
			return err
		}
		if err := tx.SaveVerificationRunCAS(ctx, domain.VerificationRunVerifying, completedRun); err != nil {
			return err
		}
		return tx.InsertVerificationResult(ctx, result)
	})
	if err != nil {
		t.Fatal(err)
	}
	return evidence, check, result
}

func TestVerificationPassingFailedUnknownRoundTrip(t *testing.T) {
	zero, one := 0, 1
	cases := []struct {
		name           string
		status         domain.AcceptanceCheckStatus
		classification domain.VerificationCommandClassification
		exitCode       *int
		outcome        domain.ResultOutcome
	}{
		{"passing", domain.AcceptanceCheckPassed, domain.VerificationCommandExited, &zero, domain.ResultReviewReady},
		{"failed", domain.AcceptanceCheckFailed, domain.VerificationCommandExited, &one, domain.ResultNeedsRevision},
		{"unknown", domain.AcceptanceCheckUnknown, domain.VerificationCommandTimedOut, nil, domain.ResultNeedsRevision},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newVerificationFixture(t)
			evidence, check, expectedResult := completeVerification(t, fixture, tc.status, tc.classification, tc.exitCode)
			if err := fixture.db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err := sqlite.Open(context.Background(), fixture.seeded.path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			report, err := db.Reader().GetAgentReportForRun(context.Background(), fixture.seeded.run.ID())
			if err != nil || report.Summary() != "agent summary" || len(report.ClaimedChecks()) != 1 {
				t.Fatalf("report=%#v err=%v", report, err)
			}
			attempts, err := db.Reader().ListVerificationAttempts(context.Background(), fixture.run.ID())
			if err != nil || len(attempts) != 1 || attempts[0].Attempt.State() != domain.VerificationAttemptCompleted || !attempts[0].EvidenceComplete || !attempts[0].CleanupProven || attempts[0].WorkspaceIdentity != evidence.WorkspaceIdentity() {
				t.Fatalf("attempts=%#v err=%v", attempts, err)
			}
			commands, err := db.Reader().ListVerificationCommandEvidence(context.Background(), fixture.attempt.ID())
			if err != nil || len(commands) != 1 || commands[0].ID() != evidence.ID() || commands[0].WorkspaceIdentity() != evidence.WorkspaceIdentity() || commands[0].Stdout().RetainedBody != "stdout body" || commands[0].Stderr().RetainedBody != "stderr body" {
				t.Fatalf("commands=%#v err=%v", commands, err)
			}
			checks, err := db.Reader().ListVerificationAcceptanceChecks(context.Background(), fixture.attempt.ID())
			if err != nil || len(checks) != 1 || checks[0].ID() != check.ID() || checks[0].EvidenceIDs()[0] != evidence.ID() {
				t.Fatalf("checks=%#v err=%v", checks, err)
			}
			result, err := db.Reader().GetVerificationResultForRun(context.Background(), fixture.seeded.run.ID())
			if err != nil || result.ID() != expectedResult.ID() || result.Outcome() != tc.outcome || result.Bindings() != fixture.bindings {
				t.Fatalf("result=%#v err=%v", result, err)
			}
		})
	}
}

func TestVerifiedHumanReviewIsAtomicImmutableAndSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	fixture := newVerificationFixture(t)
	zero := 0
	_, _, result := completeVerification(t, fixture, domain.AcceptanceCheckPassed, domain.VerificationCommandExited, &zero)
	now := fixture.seeded.now

	ready, _ := fixture.seeded.run.Transition(domain.CommandPrepareRun, now.Add(time.Second))
	running, _ := ready.Transition(domain.CommandStartAttempt, now.Add(2*time.Second))
	awaitingVerification, _ := running.Transition(domain.CommandSubmitAgentReport, now.Add(3*time.Second))
	verifying, _ := awaitingVerification.Transition(domain.CommandStartVerification, now.Add(4*time.Second))
	awaitingReview, _ := verifying.Transition(domain.CommandVerificationPassed, now.Add(9*time.Second))
	startingAttempt, _ := fixture.agentAttempt.Transition(domain.AttemptEventStartAttempt)
	runningAttempt, _ := startingAttempt.Transition(domain.AttemptEventAttemptStarted)
	outputAttempt, _ := runningAttempt.Transition(domain.AttemptEventOutputSubmitted)
	patchArtifactID := domain.NewArtifactID()

	err := fixture.db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		current := fixture.seeded.run
		for _, next := range []domain.AgentRun{ready, running, awaitingVerification, verifying, awaitingReview} {
			if err := tx.SaveRunCAS(ctx, current.Version(), next); err != nil {
				return err
			}
			current = next
		}
		attempt := fixture.agentAttempt
		for _, next := range []domain.Attempt{startingAttempt, runningAttempt, outputAttempt} {
			if err := tx.SaveAttemptCAS(ctx, attempt.State(), next); err != nil {
				return err
			}
			attempt = next
		}
		return tx.InsertArtifact(ctx, storecontract.Artifact{ID: patchArtifactID, RunID: awaitingReview.ID(), AttemptID: func() *domain.AttemptID { id := outputAttempt.ID(); return &id }(), Kind: "patch", Locator: "attempt/patch.diff", MediaType: "text/x-diff", Description: "verified Patch", Role: "output", Digest: &fixture.bindings.PatchDigest, CreatedAt: now.Add(3 * time.Second)})
	})
	if err != nil {
		t.Fatal(err)
	}
	authorization, _ := domain.MintHumanReviewAuthorization(awaitingReview.ID(), domain.ReviewDecisionAccept, awaitingReview.Version(), true)
	decision, err := domain.NewVerifiedReviewDecision(domain.VerifiedReviewDecisionParams{
		ID: domain.NewReviewDecisionID(), RunID: awaitingReview.ID(), ExpectedRunVersion: awaitingReview.Version(), Kind: domain.ReviewDecisionAccept,
		Reason: "The immutable Patch and every trusted Check satisfy the frozen contract.", ActorID: "local-human", SessionID: "local-browser",
		Binding: domain.VerifiedReviewBinding{ResultID: result.ID(), AgentAttemptID: outputAttempt.ID(), VerificationAttemptID: result.VerificationAttemptID(), PatchArtifactID: patchArtifactID,
			PatchDigest: fixture.bindings.PatchDigest, BaselineDigest: fixture.bindings.BaselineDigest, DeclaredFilesDigest: sha256.Sum256([]byte("declared files"))},
		DecidedAt: now.Add(10 * time.Second),
	}, authorization)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := awaitingReview.ApplyVerifiedReview(decision)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.InsertVerifiedReview(ctx, decision); err != nil {
			return err
		}
		return tx.SaveRunCAS(ctx, awaitingReview.Version(), accepted)
	}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqlite.Open(ctx, fixture.seeded.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	persisted, err := reopened.Reader().GetVerifiedReviewForResult(ctx, result.ID())
	if err != nil || persisted.ID() != decision.ID() || persisted.Binding() != decision.Binding() || persisted.ActorID() != "local-human" || persisted.SessionID() != "local-browser" {
		t.Fatalf("persisted decision = %#v, %v", persisted, err)
	}
	if err := reopened.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.InsertVerifiedReview(ctx, decision) }); !errors.Is(err, storecontract.ErrVerificationConflict) {
		t.Fatalf("duplicate decision error = %v", err)
	}
	raw, err := sql.Open("sqlite", fixture.seeded.path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`UPDATE verified_review_decisions SET reason='rewritten'`); err == nil {
		t.Fatal("verified review mutation bypassed immutability trigger")
	}
}

func TestVerificationRetryPredecessorAndRecoveryHaveNoResult(t *testing.T) {
	ctx := context.Background()
	fixture := newVerificationFixture(t)
	defer fixture.db.Close()
	runningRun, err := fixture.run.Transition(domain.VerificationRunStart, fixture.seeded.now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	runningAttempt, err := fixture.attempt.Transition(domain.VerificationAttemptStart)
	if err != nil {
		t.Fatal(err)
	}
	cancelledAttempt, err := runningAttempt.Transition(domain.VerificationAttemptCancelConfirmed)
	if err != nil {
		t.Fatal(err)
	}
	awaitingRun, err := runningRun.Transition(domain.VerificationRunCancelConfirmed, fixture.seeded.now.Add(4*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	predecessor := fixture.attempt.ID()
	successor, err := domain.NewVerificationAttempt(domain.VerificationAttemptParams{ID: domain.NewVerificationAttemptID(), VerificationRunID: fixture.run.ID(), Sequence: 2, Predecessor: &predecessor, Bindings: fixture.bindings, CreatedAt: fixture.seeded.now.Add(5 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	err = fixture.db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.SaveVerificationRunCAS(ctx, domain.VerificationRunAwaiting, runningRun); err != nil {
			return err
		}
		if err := tx.SaveVerificationAttemptCAS(ctx, domain.VerificationAttemptPending, storecontract.VerificationAttemptRecord{Attempt: runningAttempt, UpdatedAt: fixture.seeded.now.Add(3 * time.Second)}); err != nil {
			return err
		}
		if err := tx.SaveVerificationAttemptCAS(ctx, domain.VerificationAttemptRunning, storecontract.VerificationAttemptRecord{Attempt: cancelledAttempt, CleanupProven: true, WorkspaceIdentity: fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("cancelled-workspace"))), Reason: "user cancelled", UpdatedAt: fixture.seeded.now.Add(4 * time.Second)}); err != nil {
			return err
		}
		if err := tx.SaveVerificationRunCAS(ctx, domain.VerificationRunVerifying, awaitingRun); err != nil {
			return err
		}
		return tx.InsertVerificationAttempt(ctx, successor, successor.CreatedAt())
	})
	if err != nil {
		t.Fatal(err)
	}
	retryingRun, err := awaitingRun.Transition(domain.VerificationRunRetry, fixture.seeded.now.Add(6*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	runningSuccessor, err := successor.Transition(domain.VerificationAttemptStart)
	if err != nil {
		t.Fatal(err)
	}
	recoveryAttempt, err := runningSuccessor.Transition(domain.VerificationAttemptIntegrityUncertain)
	if err != nil {
		t.Fatal(err)
	}
	recoveryRun, err := retryingRun.Transition(domain.VerificationRunIntegrityUncertain, fixture.seeded.now.Add(7*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	err = fixture.db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.SaveVerificationRunCAS(ctx, domain.VerificationRunAwaiting, retryingRun); err != nil {
			return err
		}
		if err := tx.SaveVerificationAttemptCAS(ctx, domain.VerificationAttemptPending, storecontract.VerificationAttemptRecord{Attempt: runningSuccessor, UpdatedAt: fixture.seeded.now.Add(6 * time.Second)}); err != nil {
			return err
		}
		if err := tx.SaveVerificationAttemptCAS(ctx, domain.VerificationAttemptRunning, storecontract.VerificationAttemptRecord{Attempt: recoveryAttempt, Reason: "continuity uncertain", UpdatedAt: fixture.seeded.now.Add(7 * time.Second)}); err != nil {
			return err
		}
		return tx.SaveVerificationRunCAS(ctx, domain.VerificationRunVerifying, recoveryRun)
	})
	if err != nil {
		t.Fatal(err)
	}
	attempts, err := fixture.db.Reader().ListVerificationAttempts(ctx, fixture.run.ID())
	if err != nil || len(attempts) != 2 {
		t.Fatalf("attempts=%#v err=%v", attempts, err)
	}
	gotPredecessor, ok := attempts[1].Attempt.Predecessor()
	if !ok || gotPredecessor != fixture.attempt.ID() || attempts[0].Attempt.State() != domain.VerificationAttemptCancelled || attempts[1].Attempt.State() != domain.VerificationAttemptRecoveryRequired {
		t.Fatalf("lineage=%#v", attempts)
	}
	if _, err := fixture.db.Reader().GetVerificationResultForRun(ctx, fixture.seeded.run.ID()); !errors.Is(err, storecontract.ErrNotFound) {
		t.Fatalf("cancel/recovery result err=%v", err)
	}
	candidates, err := fixture.db.Reader().ListVerificationStartupCandidates(ctx)
	if err != nil || len(candidates) != 1 || candidates[0].Attempt.ID() != successor.ID() {
		t.Fatalf("candidates=%#v err=%v", candidates, err)
	}
}

func TestAgentReportImmutableAndVerificationCASConflict(t *testing.T) {
	ctx := context.Background()
	fixture := newVerificationFixture(t)
	defer fixture.db.Close()
	report, err := fixture.db.Reader().GetAgentReportForRun(ctx, fixture.seeded.run.ID())
	if err != nil {
		t.Fatal(err)
	}
	err = fixture.db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.InsertAgentReport(ctx, report) })
	if !errors.Is(err, storecontract.ErrVerificationConflict) {
		t.Fatalf("duplicate immutable report err=%v", err)
	}
	raw, err := sql.Open("sqlite", fixture.seeded.path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`UPDATE agent_reports SET summary='mutated'`); err == nil {
		t.Fatal("agent report update bypassed immutability trigger")
	}
	running, err := fixture.run.Transition(domain.VerificationRunStart, fixture.seeded.now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	err = fixture.db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		return tx.SaveVerificationRunCAS(ctx, domain.VerificationRunVerifying, running)
	})
	if !errors.Is(err, storecontract.ErrVerificationConflict) {
		t.Fatalf("stale run CAS err=%v", err)
	}
	orphan := verificationEvidence(t, fixture, domain.VerificationCommandTimedOut, nil)
	orphanFixture := fixture
	orphanFixture.attempt, err = domain.NewVerificationAttempt(domain.VerificationAttemptParams{ID: domain.NewVerificationAttemptID(), VerificationRunID: fixture.run.ID(), Sequence: 2, Predecessor: func() *domain.VerificationAttemptID { id := fixture.attempt.ID(); return &id }(), Bindings: fixture.bindings, CreatedAt: fixture.seeded.now.Add(4 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	orphan = verificationEvidence(t, orphanFixture, domain.VerificationCommandTimedOut, nil)
	err = fixture.db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.InsertVerificationCommandEvidence(ctx, orphan) })
	if !errors.Is(err, storecontract.ErrVerificationConflict) {
		t.Fatalf("orphan evidence err=%v", err)
	}
}

func TestVerificationLateResultErrorRollsBackEvidence(t *testing.T) {
	ctx := context.Background()
	fixture := newVerificationFixture(t)
	defer fixture.db.Close()
	runningRun, _ := fixture.run.Transition(domain.VerificationRunStart, fixture.seeded.now.Add(3*time.Second))
	runningAttempt, _ := fixture.attempt.Transition(domain.VerificationAttemptStart)
	evidence := verificationEvidence(t, fixture, domain.VerificationCommandExited, func() *int { value := 0; return &value }())
	check, err := domain.NewAcceptanceCheck(domain.NewCheckID(), fixture.criterionID, domain.AcceptanceCheckPassed, domain.VerificationEvidenceTrusted, []domain.VerificationCommandEvidenceID{evidence.ID()})
	if err != nil {
		t.Fatal(err)
	}
	completedAttempt, _ := runningAttempt.Transition(domain.VerificationAttemptEvidenceCompleted)
	completedRun, _ := runningRun.Transition(domain.VerificationRunEvidenceCompleted, fixture.seeded.now.Add(7*time.Second))
	result, err := domain.NewVerificationResult(domain.NewResultID(), completedRun, completedAttempt, []domain.AcceptanceCheck{check}, domain.VerificationCompletionProof{EvidenceComplete: true, CleanupProven: true}, fixture.seeded.now.Add(8*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	err = fixture.db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.SaveVerificationRunCAS(ctx, domain.VerificationRunAwaiting, runningRun); err != nil {
			return err
		}
		if err := tx.SaveVerificationAttemptCAS(ctx, domain.VerificationAttemptPending, storecontract.VerificationAttemptRecord{Attempt: runningAttempt, UpdatedAt: fixture.seeded.now.Add(3 * time.Second)}); err != nil {
			return err
		}
		if err := tx.InsertVerificationCommandEvidence(ctx, evidence); err != nil {
			return err
		}
		if err := tx.SaveVerificationAttemptCAS(ctx, domain.VerificationAttemptRunning, storecontract.VerificationAttemptRecord{Attempt: completedAttempt, EvidenceComplete: true, CleanupProven: true, WorkspaceIdentity: evidence.WorkspaceIdentity(), UpdatedAt: fixture.seeded.now.Add(7 * time.Second)}); err != nil {
			return err
		}
		if err := tx.SaveVerificationRunCAS(ctx, domain.VerificationRunVerifying, completedRun); err != nil {
			return err
		}
		return tx.InsertVerificationResult(ctx, result) // Missing persisted Check: deliberately late failure.
	})
	if !errors.Is(err, storecontract.ErrVerificationConflict) {
		t.Fatalf("late result err=%v", err)
	}
	commands, err := fixture.db.Reader().ListVerificationCommandEvidence(ctx, fixture.attempt.ID())
	if err != nil || len(commands) != 0 {
		t.Fatalf("partial evidence=%#v err=%v", commands, err)
	}
	attempt, err := fixture.db.Reader().GetVerificationAttempt(ctx, fixture.attempt.ID())
	if err != nil || attempt.Attempt.State() != domain.VerificationAttemptPending {
		t.Fatalf("partial attempt=%#v err=%v", attempt, err)
	}
}

func TestVerificationReadersRejectCorruptJSONDigestAndTime(t *testing.T) {
	tests := []struct {
		name   string
		mutate string
		read   func(context.Context, *sqlite.Store, verificationFixture) error
	}{
		{"agent JSON", `DROP TRIGGER agent_reports_immutable_update; UPDATE agent_reports SET claimed_checks_json='{}'`, func(ctx context.Context, db *sqlite.Store, f verificationFixture) error {
			_, err := db.Reader().GetAgentReportForRun(ctx, f.seeded.run.ID())
			return err
		}},
		{"run digest", `DROP TRIGGER verification_runs_identity_immutable; DROP TRIGGER verification_runs_state_transition; UPDATE verification_runs SET baseline_digest=X'00'`, func(ctx context.Context, db *sqlite.Store, f verificationFixture) error {
			_, err := db.Reader().GetVerificationRun(ctx, f.run.ID())
			return err
		}},
		{"run time", `DROP TRIGGER verification_runs_identity_immutable; DROP TRIGGER verification_runs_state_transition; UPDATE verification_runs SET created_at='invalid-time'`, func(ctx context.Context, db *sqlite.Store, f verificationFixture) error {
			_, err := db.Reader().GetVerificationRun(ctx, f.run.ID())
			return err
		}},
		{"run identity", `DROP TRIGGER verification_runs_identity_immutable; DROP TRIGGER verification_runs_state_transition; UPDATE verification_runs SET id='bad'`, func(ctx context.Context, db *sqlite.Store, f verificationFixture) error {
			_, err := db.Reader().GetVerificationRunForRun(ctx, f.seeded.run.ID())
			return err
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newVerificationFixture(t)
			defer fixture.db.Close()
			raw, err := sql.Open("sqlite", fixture.seeded.path)
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			if _, err := raw.Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
				t.Fatal(err)
			}
			if _, err := raw.Exec(tc.mutate); err != nil {
				t.Fatal(err)
			}
			if err := tc.read(context.Background(), fixture.db, fixture); err == nil {
				t.Fatal("corrupt persisted record accepted")
			}
		})
	}
}
