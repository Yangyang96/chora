package app_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/speccoding"
	storecontract "github.com/Yangyang96/chora/internal/store"
	"github.com/Yangyang96/chora/internal/store/sqlite"
	"github.com/Yangyang96/chora/internal/verifier"
)

type verificationExecutorFake struct {
	mu        sync.Mutex
	mode      string
	authority app.VerificationAuthority
	started   chan app.VerificationExecutionRequest
}

type verificationPiAdapter struct{ *fakeAdapter }

func (verificationPiAdapter) ID() string { return "pi" }

func (executor *verificationExecutorFake) Authority() app.VerificationAuthority {
	return executor.authority
}
func (executor *verificationExecutorFake) setMode(mode string) {
	executor.mu.Lock()
	executor.mode = mode
	executor.mu.Unlock()
}
func (executor *verificationExecutorFake) Verify(ctx context.Context, request app.VerificationExecutionRequest) (verifier.AttemptEvidence, error) {
	select {
	case executor.started <- request:
	default:
	}
	executor.mu.Lock()
	mode := executor.mode
	executor.mu.Unlock()
	if mode == "block" {
		<-ctx.Done()
		return fakeVerificationEvidence(request, "cancelled"), nil
	}
	if mode == "late-complete" {
		<-ctx.Done()
		return fakeVerificationEvidence(request, "pass"), nil
	}
	if mode == "recovery" {
		return verifier.AttemptEvidence{Classification: verifier.AttemptRecoveryRequired, Reason: "identity uncertain"}, nil
	}
	return fakeVerificationEvidence(request, mode), nil
}

func fakeVerificationEvidence(request app.VerificationExecutionRequest, mode string) verifier.AttemptEvidence {
	document := request.Contract.Document()
	command := document.Acceptance.VerificationCommands[0]
	criteria := make([]string, 0, len(document.Acceptance.Criteria))
	for _, criterion := range document.Acceptance.Criteria {
		if len(criterion.VerificationCommandIDs) == 1 && criterion.VerificationCommandIDs[0] == command.ID {
			criteria = append(criteria, criterion.ID)
		}
	}
	empty := verifier.StreamLog{FullSHA256: sha256.Sum256(nil), RedactionPolicyVersion: "chora.verifier-redaction.v1"}
	zero, one := 0, 1
	kind, code, classification, cleanupProven := verifier.CommandExited, &zero, verifier.AttemptCompleted, true
	switch mode {
	case "fail":
		code = &one
	case "unknown":
		kind, code = verifier.CommandUnavailable, nil
	case "cancelled":
		kind, code, classification = verifier.CommandCancelled, nil, verifier.AttemptCancelled
	case "recovery-evidence":
		kind, code, classification, cleanupProven = verifier.CommandLaunchFailed, nil, verifier.AttemptRecoveryRequired, false
	}
	now := time.Date(2026, 8, 12, 8, 0, 0, 0, time.UTC)
	workspaceDigest := sha256.Sum256([]byte(request.AttemptID.String()))
	imageDigest := sha256.Sum256([]byte("image"))
	identity := verifier.EvidenceIdentity{RunID: request.RunID.String(), VerificationRunID: request.VerificationRunID.String(), AttemptID: request.AttemptID.String(),
		TaskID: request.TaskID.String(), WorkspaceIdentity: "sha256:" + hex.EncodeToString(workspaceDigest[:]),
		VerifierImage: "sha256:" + hex.EncodeToString(imageDigest[:]), VerifierPolicyVersion: request.Bindings.VerifierPolicyVersion,
		BaselineDigest: request.Bindings.BaselineDigest, PatchDigest: request.Bindings.PatchDigest, ContextSnapshotDigest: request.Bindings.ContextSnapshotDigest,
		AcceptanceContractDigest: request.Bindings.AcceptanceContractDigest, VerifierPolicyDigest: request.Bindings.VerifierPolicyDigest}
	return verifier.AttemptEvidence{Classification: classification, CleanupProven: cleanupProven, Reason: mode, Commands: []verifier.CommandEvidence{{CommandID: command.ID, CriterionIDs: criteria,
		Argv: command.Argv, StartedAt: now, EndedAt: now.Add(time.Second), Kind: kind, ExitCode: code, Stdout: empty, Stderr: empty,
		VerifierIdentity: "docker:test@" + identity.VerifierImage, Identity: identity}}}
}

type verificationFlowFixture struct {
	db         *sqlite.Store
	service    *app.Service
	executor   *verificationExecutorFake
	adapter    *fakeAdapter
	supervisor *fakeSupervisor
	run        domain.AgentRun
	started    app.StartAttemptResult
	lifecycle  context.CancelFunc
	patch      []byte
}

type reviewPatchSourceFake struct {
	locator string
	patch   []byte
}

type patchTargetFake struct {
	inspection app.PatchTargetInspection
	post       [32]byte
	err        error
}

type recoveringPatchTarget struct {
	calls               int
	firstTarget, target string
	before, after       [32]byte
}

func (target *recoveringPatchTarget) Inspect(context.Context, app.PatchTargetRequest) (app.PatchTargetInspection, error) {
	target.calls++
	if target.calls == 1 {
		return app.PatchTargetInspection{TargetIdentity: target.firstTarget, Reason: "The target file is temporarily unavailable."}, app.ErrPatchTargetConflict
	}
	return app.PatchTargetInspection{TargetIdentity: target.target, BaseRevision: "67b83d9", StateDigest: target.before, Applicable: true}, nil
}

func (target *recoveringPatchTarget) Apply(context.Context, app.PatchTargetRequest, app.PatchTargetInspection) (app.PatchTargetEvidence, error) {
	return app.PatchTargetEvidence{PostStateDigest: target.after}, nil
}

func (target patchTargetFake) Inspect(context.Context, app.PatchTargetRequest) (app.PatchTargetInspection, error) {
	return target.inspection, target.err
}
func (target patchTargetFake) Apply(context.Context, app.PatchTargetRequest, app.PatchTargetInspection) (app.PatchTargetEvidence, error) {
	return app.PatchTargetEvidence{PostStateDigest: target.post}, target.err
}

func (source reviewPatchSourceFake) ReadReviewPatch(_ context.Context, locator string) ([]byte, error) {
	if locator != source.locator {
		return nil, errors.New("unknown Patch locator")
	}
	return append([]byte(nil), source.patch...), nil
}

func newVerificationFlowFixture(t *testing.T, mode string) *verificationFlowFixture {
	return newVerificationFlowFixtureOpts(t, mode, false)
}

func newVerificationFlowFixtureOpts(t *testing.T, mode string, autoStart bool) *verificationFlowFixture {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := openLatestSQLiteTestStore(ctx, filepath.Join(root, "chora.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	setup := app.NewService(app.Dependencies{Store: db, Context: app.ContextAssembler{}, Authorizer: allowAuthorizer{}, Clock: &fixedClock{now: now}, IDs: app.RandomIDs{}})
	v4 := readSpecCodingContract(t, "chora-m1-real-task.v4.json")
	materialized, err := setup.MaterializeSpecCodingContract(ctx, app.MaterializeSpecCodingContractRequest{CommandMeta: meta("materialize-verification", "materialize-verification"), Contract: v4,
		WorkspaceRoot: filepath.Join(root, "source-baseline-v4", "repo"), ContextEntryID: mustContextEntryID(t, "context_entry_018f0c4a-1a30-7c3d-8e4f-1234567890ab"),
		ContextRevisionID: mustContextRevisionID(t, "context_revision_018f0c4a-1a31-7c3d-8e4f-1234567890ab"), CharterID: mustCharterID(t, "charter_018f0c4a-1a32-7c3d-8e4f-1234567890ab"), FrozenAt: now})
	if err != nil {
		t.Fatal(err)
	}
	document := v5Document(v4, materialized.Snapshot.Digest())
	document.SchemaVersion, document.Revision = speccoding.CoreContractSchemaVersionV8, 8
	document.Acceptance.VerificationCommands = []speccoding.BoundedCommand{{ID: "domain-tests", Argv: []string{"go", "test", "./internal/domain"}}}
	for index := range document.Acceptance.Criteria {
		document.Acceptance.Criteria[index].VerificationCommandIDs = []string{"domain-tests"}
	}
	contract, err := speccoding.NewCoreContract(document)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := setup.RegisterSpecCodingContract(ctx, app.RegisterSpecCodingContractRequest{CommandMeta: meta("register-verification", "register-verification"), Contract: contract}); err != nil {
		t.Fatal(err)
	}
	submitted, err := setup.SubmitTechnicalPlanDraft(ctx, app.SubmitTechnicalPlanDraftRequest{CommandMeta: meta("submit-verification-plan", "submit-verification-plan"), DraftID: materialized.Draft.ID(), ExpectedEditVersion: materialized.Draft.EditVersion()})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := setup.ReviewTechnicalPlanRevision(ctx, app.ReviewTechnicalPlanRevisionRequest{CommandMeta: meta("accept-verification-plan", "accept-verification-plan"), RevisionID: submitted.Revision.ID(), Kind: domain.TechnicalPlanReviewAccept, Note: "Verification contract plan accepted."})
	if err != nil || accepted.Charter == nil {
		t.Fatalf("accepted=%#v err=%v", accepted, err)
	}
	createdRun, err := setup.CreateRun(ctx, app.CreateRunRequest{CommandMeta: meta("create-verification-run", "create-verification-run"), TaskID: materialized.Task.ID(), RevisionID: submitted.Revision.ID()})
	if err != nil {
		t.Fatal(err)
	}
	run := createdRun.Run
	adapter := &fakeAdapter{id: "pi"}
	piAdapter := verificationPiAdapter{fakeAdapter: adapter}
	supervisor := &fakeSupervisor{outcome: execution.StartOutcome{Kind: execution.Started, Handle: execution.RuntimeHandle{Value: "handle"}, Identity: execution.ProcessIdentity{Value: "pid:verification"}}}
	baselineDigest, policyDigest := sha256.Sum256([]byte("frozen-baseline")), sha256.Sum256([]byte("verifier-policy"))
	executor := &verificationExecutorFake{mode: mode, authority: app.VerificationAuthority{BaselineDigest: baselineDigest, VerifierPolicyVersion: "chora.m1-verifier-policy.v1", VerifierPolicyDigest: policyDigest}, started: make(chan app.VerificationExecutionRequest, 4)}
	lifecycle, cancel := context.WithCancel(context.Background())
	service := app.NewService(app.Dependencies{Lifecycle: lifecycle, Store: db, Context: app.ContextAssembler{}, Agents: staticRegistry{adapter: piAdapter}, Supervisor: supervisor,
		Authorizer: allowAuthorizer{}, Clock: &fixedClock{now: now.Add(time.Hour)}, IDs: app.RandomIDs{}, Verifier: executor, AutoStartVerification: autoStart})
	prepared, err := service.PrepareRun(ctx, app.PrepareRunRequest{CommandMeta: meta("prepare-verification", "prepare-verification"), RunID: run.ID(), ExpectedVersion: run.Version()})
	if err != nil {
		t.Fatal(err)
	}
	started, err := service.StartAttempt(ctx, app.StartAttemptRequest{CommandMeta: meta("start-verification-agent", "start-verification-agent"), RunID: run.ID(), ExpectedVersion: prepared.Run.Version(), Mode: app.StartFresh})
	if err != nil {
		t.Fatal(err)
	}
	patch := []byte("diff --git a/internal/domain/task.go b/internal/domain/task.go\n--- a/internal/domain/task.go\n+++ b/internal/domain/task.go\n@@ -1 +1 @@\n-package domain\n+package domain\n")
	patchDigest := sha256.Sum256(patch)
	adapter.terminal = execution.TerminalResult{Kind: execution.TerminalReviewReady, Summary: "Agent final text", Outputs: []execution.WorkspaceArtifact{{Locator: filepath.Join(root, "patch.diff"), SHA256: hex.EncodeToString(patchDigest[:]), MediaType: "text/x-diff"}}}
	supervisor.drainOutcome = execution.DrainOutcome{Chunks: map[execution.StreamKind][]byte{}, Offsets: execution.StreamOffsets{execution.StreamStdout: 0, execution.StreamStderr: 0}, EOF: map[execution.StreamKind]bool{execution.StreamStdout: true, execution.StreamStderr: true}}
	supervisor.reconcileOutcome = execution.ReconcileOutcome{Kind: execution.ReconcileDead, Handle: execution.RuntimeHandle{Value: "handle"}, LaunchToken: supervisor.invocation.LaunchToken()}
	supervisor.sink.Exited()
	terminal, err := service.HandleExit(ctx, app.ExitRequest{CommandMeta: meta("terminal-verification", "terminal-verification"), SessionID: started.Session.ID, ExpectedVersion: started.Run.Version()})
	if err != nil || terminal.Run.State() != domain.RunStateAwaitingVerification {
		t.Fatalf("terminal = %#v, %v", terminal, err)
	}
	return &verificationFlowFixture{db: db, service: service, executor: executor, adapter: adapter, supervisor: supervisor, run: terminal.Run, started: started, lifecycle: cancel, patch: patch}
}

func (fixture *verificationFlowFixture) waitRunState(t *testing.T, state domain.RunState) domain.AgentRun {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		run, err := fixture.db.Reader().GetRun(context.Background(), fixture.run.ID())
		if err == nil && run.State() == state {
			return run
		}
		time.Sleep(10 * time.Millisecond)
	}
	run, err := fixture.db.Reader().GetRun(context.Background(), fixture.run.ID())
	t.Fatalf("run state = %q, want %q, err=%v", run.State(), state, err)
	return domain.AgentRun{}
}

func TestAgentCompletionAutoStartsIndependentVerification(t *testing.T) {
	fixture := newVerificationFlowFixtureOpts(t, "pass", true)
	defer fixture.lifecycle()
	// No explicit StartVerification command is issued: the Agent completion
	// itself auto-starts independent verification for the registered contract.
	fixture.waitRunState(t, domain.RunStateAwaitingReview)
	result, err := fixture.db.Reader().GetVerificationResultForRun(context.Background(), fixture.run.ID())
	if err != nil || result.Outcome() != domain.ResultReviewReady {
		t.Fatalf("auto-started verification result = %#v, %v", result, err)
	}
	verificationRun, err := fixture.db.Reader().GetVerificationRunForRun(context.Background(), fixture.run.ID())
	if err != nil {
		t.Fatalf("auto-started verification run = %v", err)
	}
	attempt, err := fixture.db.Reader().GetCurrentVerificationAttempt(context.Background(), verificationRun.ID())
	if err != nil || attempt.Attempt.State() != domain.VerificationAttemptCompleted || !attempt.EvidenceComplete || !attempt.CleanupProven {
		t.Fatalf("auto-started verification attempt = %#v, %v", attempt, err)
	}
}

func TestIndependentVerificationPersistsPassingFailingAndUnknownResults(t *testing.T) {
	for _, test := range []struct {
		mode      string
		wantState domain.RunState
		want      domain.ResultOutcome
		check     domain.AcceptanceCheckStatus
	}{{"pass", domain.RunStateAwaitingReview, domain.ResultReviewReady, domain.AcceptanceCheckPassed},
		{"fail", domain.RunStateRevisionRequired, domain.ResultNeedsRevision, domain.AcceptanceCheckFailed},
		{"unknown", domain.RunStateRevisionRequired, domain.ResultNeedsRevision, domain.AcceptanceCheckUnknown}} {
		t.Run(test.mode, func(t *testing.T) {
			fixture := newVerificationFlowFixture(t, test.mode)
			defer fixture.lifecycle()
			started, err := fixture.service.StartVerification(context.Background(), app.StartVerificationRequest{CommandMeta: meta("start-independent-"+test.mode, test.mode), RunID: fixture.run.ID(), ExpectedVersion: fixture.run.Version()})
			if err != nil || started.Run.State() != domain.RunStateVerifying {
				t.Fatalf("start = %#v, %v", started, err)
			}
			fixture.waitRunState(t, test.wantState)
			result, err := fixture.db.Reader().GetVerificationResultForRun(context.Background(), fixture.run.ID())
			if err != nil || result.Outcome() != test.want {
				t.Fatalf("result = %#v, %v", result, err)
			}
			checks, err := fixture.db.Reader().ListVerificationAcceptanceChecks(context.Background(), started.Attempt.ID())
			if err != nil || len(checks) != 2 || checks[0].Status() != test.check || checks[1].Status() != test.check {
				t.Fatalf("checks = %#v, %v", checks, err)
			}
			if _, err := fixture.db.Reader().GetAgentReportForRun(context.Background(), fixture.run.ID()); err != nil {
				t.Fatalf("AgentReport unavailable: %v", err)
			}
		})
	}
}

func TestReviewableChangeRevalidatesPatchAndVerificationBindingsOnEveryRead(t *testing.T) {
	fixture := newVerificationFlowFixture(t, "pass")
	defer fixture.lifecycle()
	if _, err := fixture.service.StartVerification(context.Background(), app.StartVerificationRequest{CommandMeta: meta("start-reviewable-change", "start"), RunID: fixture.run.ID(), ExpectedVersion: fixture.run.Version()}); err != nil {
		t.Fatal(err)
	}
	fixture.waitRunState(t, domain.RunStateAwaitingReview)
	projection, err := fixture.db.Reader().GetTerminalProjection(context.Background(), fixture.started.Attempt.ID())
	if err != nil || len(projection.Artifacts) != 1 {
		t.Fatalf("artifacts = %#v, %v", projection.Artifacts, err)
	}
	source := reviewPatchSourceFake{locator: projection.Artifacts[0].Locator, patch: fixture.patch}
	service := app.NewService(app.Dependencies{Store: fixture.db, PatchSource: source})
	change, err := service.LoadReviewableChange(context.Background(), fixture.run.ID())
	if err != nil {
		t.Fatal(err)
	}
	if change.Outcome != domain.ResultReviewReady || change.Binding.AgentAttemptID != fixture.started.Attempt.ID() || change.Binding.PatchArtifactID != projection.Artifacts[0].ID ||
		change.Binding.PatchDigest != sha256.Sum256(fixture.patch) || len(change.Patch.Files) != 1 || !change.EvidenceComplete || !change.CleanupProven {
		t.Fatalf("reviewable change = %#v", change)
	}

	drifted := append([]byte(nil), fixture.patch...)
	drifted[len(drifted)-2] = 'x'
	service = app.NewService(app.Dependencies{Store: fixture.db, PatchSource: reviewPatchSourceFake{locator: source.locator, patch: drifted}})
	if _, err := service.LoadReviewableChange(context.Background(), fixture.run.ID()); !errors.Is(err, app.ErrReviewEvidenceUnavailable) {
		t.Fatalf("drift error = %v", err)
	}
}

func TestVerifiedReviewAcceptRejectIdempotencyAndPersistence(t *testing.T) {
	for _, test := range []struct {
		name      string
		kind      domain.ReviewDecisionKind
		class     domain.ReviewRejectionClass
		wantState domain.RunState
	}{
		{name: "accept", kind: domain.ReviewDecisionAccept, wantState: domain.RunStateAccepted},
		{name: "reject implementation gap", kind: domain.ReviewDecisionReject, class: domain.ReviewRejectionImplementationGap, wantState: domain.RunStateRevisionRequired},
		{name: "reject planning gap", kind: domain.ReviewDecisionReject, class: domain.ReviewRejectionPlanningGap, wantState: domain.RunStateRevisionRequired},
		{name: "reject contract change", kind: domain.ReviewDecisionReject, class: domain.ReviewRejectionContractChangeRequired, wantState: domain.RunStateRevisionRequired},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newVerificationFlowFixture(t, "pass")
			defer fixture.lifecycle()
			if _, err := fixture.service.StartVerification(context.Background(), app.StartVerificationRequest{CommandMeta: meta("start-review-"+test.name, "start"), RunID: fixture.run.ID(), ExpectedVersion: fixture.run.Version()}); err != nil {
				t.Fatal(err)
			}
			awaiting := fixture.waitRunState(t, domain.RunStateAwaitingReview)
			projection, err := fixture.db.Reader().GetTerminalProjection(context.Background(), fixture.started.Attempt.ID())
			if err != nil || len(projection.Artifacts) != 1 {
				t.Fatalf("artifacts = %#v, %v", projection.Artifacts, err)
			}
			beforeDigest, afterDigest := sha256.Sum256([]byte("target-before")), sha256.Sum256([]byte("target-after"))
			service := app.NewService(app.Dependencies{
				Store: fixture.db, PatchSource: reviewPatchSourceFake{locator: projection.Artifacts[0].Locator, patch: fixture.patch},
				PatchTarget: patchTargetFake{inspection: app.PatchTargetInspection{TargetIdentity: "sha256:target", BaseRevision: "67b83d9", StateDigest: beforeDigest, Applicable: true}, post: afterDigest},
				Presence:    fakePresence{}, Authorizer: allowAuthorizer{}, Clock: &fixedClock{now: time.Date(2026, 8, 13, 1, 0, 0, 0, time.UTC)}, IDs: app.RandomIDs{},
			})
			request := app.VerifiedReviewRequest{
				CommandMeta: meta("verified-review-"+test.name, "same"), RunID: fixture.run.ID(), ExpectedVersion: awaiting.Version(), Kind: test.kind,
				Reason: "The device owner reviewed the immutable Patch and bound evidence.", RejectionClass: test.class, ActorID: "local-human", SessionID: "local-browser",
			}
			first, err := service.ReviewVerifiedResult(context.Background(), request)
			if err != nil || first.Run.State() != test.wantState || first.Decision.Binding().ResultID == (domain.ResultID{}) {
				t.Fatalf("first review = %#v, %v", first, err)
			}
			if test.class == domain.ReviewRejectionPlanningGap && (first.PlanningDraft == nil || first.RelatedTask != nil) {
				t.Fatalf("planning-gap route = %#v", first)
			}
			if test.class == domain.ReviewRejectionContractChangeRequired && (first.RelatedTask == nil || first.RelatedTask.PredecessorTaskID() != fixture.run.TaskID() || first.PlanningDraft != nil) {
				t.Fatalf("contract-change route = %#v", first)
			}
			if (test.kind == domain.ReviewDecisionAccept || test.class == domain.ReviewRejectionImplementationGap) && (first.PlanningDraft != nil || first.RelatedTask != nil) {
				t.Fatalf("unexpected rejection route = %#v", first)
			}
			replayed, err := service.ReviewVerifiedResult(context.Background(), request)
			if err != nil || !replayed.Replayed || replayed.Decision.ID() != first.Decision.ID() {
				t.Fatalf("replayed review = %#v, %v", replayed, err)
			}
			if first.PlanningDraft != nil && (replayed.PlanningDraft == nil || replayed.PlanningDraft.ID() != first.PlanningDraft.ID()) {
				t.Fatalf("replayed planning route = %#v", replayed)
			}
			if first.RelatedTask != nil && (replayed.RelatedTask == nil || replayed.RelatedTask.ID() != first.RelatedTask.ID()) {
				t.Fatalf("replayed contract route = %#v", replayed)
			}
			persisted, err := fixture.db.Reader().GetVerifiedReviewForResult(context.Background(), first.Decision.Binding().ResultID)
			if err != nil || persisted.ID() != first.Decision.ID() || persisted.Binding() != first.Decision.Binding() || persisted.Reason() != request.Reason {
				t.Fatalf("persisted review = %#v, %v", persisted, err)
			}
			if test.class == domain.ReviewRejectionPlanningGap || test.class == domain.ReviewRejectionContractChangeRequired {
				route, err := fixture.db.Reader().GetVerifiedReviewRoute(context.Background(), first.Decision.ID())
				if err != nil || route.RejectionClass != test.class || route.SourceRunID != fixture.run.ID() || route.SourceTaskID != fixture.run.TaskID() {
					t.Fatalf("persisted route = %#v, %v", route, err)
				}
			}
			if test.kind == domain.ReviewDecisionAccept {
				pendingTask, err := fixture.db.Reader().GetTask(context.Background(), fixture.run.TaskID())
				if err != nil || pendingTask.State() != domain.TaskStateOpen {
					t.Fatalf("accepted-not-applied Task=%#v err=%v", pendingTask, err)
				}
				acceptance, err := fixture.db.Reader().GetCurrentTechnicalPlanAcceptance(context.Background(), fixture.run.TaskID())
				if err != nil {
					t.Fatal(err)
				}
				_, err = service.CreateRun(context.Background(), app.CreateRunRequest{CommandMeta: meta("stale-closed-task-start", "stale-closed-task-start"), TaskID: fixture.run.TaskID(), RevisionID: acceptance.RevisionID()})
				if !errors.Is(err, storecontract.ErrVersionConflict) {
					t.Fatalf("CreateRun while Apply pending err=%v", err)
				}
				application, err := service.ApplyAcceptedPatch(context.Background(), app.ApplyAcceptedPatchRequest{CommandMeta: meta("apply-accepted", "apply-accepted"), RunID: fixture.run.ID(), ExpectedVersion: first.Run.Version()})
				if err != nil || application.Application.State() != domain.PatchApplicationApplied || application.Application.PostStateDigest() != afterDigest {
					t.Fatalf("application=%#v err=%v", application, err)
				}
				closedTask, err := fixture.db.Reader().GetTask(context.Background(), fixture.run.TaskID())
				if err != nil || closedTask.State() != domain.TaskStateClosed {
					t.Fatalf("applied Task=%#v err=%v", closedTask, err)
				}
				replayed, err := service.ApplyAcceptedPatch(context.Background(), app.ApplyAcceptedPatchRequest{CommandMeta: meta("apply-replay", "apply-replay"), RunID: fixture.run.ID(), ExpectedVersion: first.Run.Version()})
				if err != nil || !replayed.Replayed || replayed.Application.State() != domain.PatchApplicationApplied {
					t.Fatalf("replayed application=%#v err=%v", replayed, err)
				}
			}
			if test.class == domain.ReviewRejectionImplementationGap {
				retryService := app.NewService(app.Dependencies{
					Lifecycle: context.Background(), Store: fixture.db, Context: app.ContextAssembler{}, Agents: staticRegistry{adapter: verificationPiAdapter{fakeAdapter: fixture.adapter}}, Supervisor: fixture.supervisor,
					Presence: fakePresence{}, Authorizer: allowAuthorizer{}, Clock: &fixedClock{now: time.Date(2026, 8, 13, 3, 0, 0, 0, time.UTC)}, IDs: app.RandomIDs{}, Verifier: fixture.executor,
					PatchSource: reviewPatchSourceFake{locator: projection.Artifacts[0].Locator, patch: fixture.patch},
				})
				prepared, err := retryService.PrepareVerifiedAgentRetry(context.Background(), app.PrepareVerifiedAgentRetryRequest{
					CommandMeta: meta("prepare-reviewed-retry", "reviewed-retry"), RunID: fixture.run.ID(), ExpectedVersion: first.Run.Version(), Instructions: "Repair the reviewed implementation gap.",
				})
				if err != nil {
					t.Fatal(err)
				}
				retryEvents, err := fixture.db.Reader().ListRunEvents(context.Background(), fixture.run.ID())
				if err != nil {
					t.Fatal(err)
				}
				reset := false
				for _, event := range retryEvents {
					reset = reset || event.Type() == "automatic_retry.reset"
				}
				if !reset {
					t.Fatal("verified human Agent Retry did not reset the automatic retry budget")
				}
				started, err := retryService.StartAttempt(context.Background(), app.StartAttemptRequest{CommandMeta: meta("start-reviewed-retry", "reviewed-retry"), RunID: fixture.run.ID(), ExpectedVersion: prepared.Run.Version(), Mode: app.StartFresh})
				if err != nil {
					t.Fatal(err)
				}
				fixture.executor.setMode("fail")
				fixture.supervisor.reconcileOutcome = execution.ReconcileOutcome{Kind: execution.ReconcileDead, Handle: execution.RuntimeHandle{Value: "handle"}, LaunchToken: fixture.supervisor.invocation.LaunchToken()}
				fixture.supervisor.sink.Exited()
				terminal, err := retryService.HandleExit(context.Background(), app.ExitRequest{CommandMeta: meta("terminal-reviewed-retry", "reviewed-retry"), SessionID: started.Session.ID, ExpectedVersion: started.Run.Version()})
				if err != nil || terminal.Run.State() != domain.RunStateAwaitingVerification {
					t.Fatalf("reviewed retry terminal=%#v err=%v", terminal, err)
				}
				secondVerification, err := retryService.StartVerification(context.Background(), app.StartVerificationRequest{CommandMeta: meta("verify-reviewed-retry", "reviewed-retry"), RunID: fixture.run.ID(), ExpectedVersion: terminal.Run.Version()})
				if err != nil {
					t.Fatal(err)
				}
				fixture.service = retryService
				fixture.waitRunState(t, domain.RunStateRevisionRequired)
				currentResult, err := fixture.db.Reader().GetVerificationResultForVerificationRun(context.Background(), secondVerification.VerificationRun.ID())
				if err != nil || currentResult.Outcome() != domain.ResultNeedsRevision {
					t.Fatalf("current Result=%#v err=%v", currentResult, err)
				}
				task, err := fixture.db.Reader().GetTask(context.Background(), fixture.run.TaskID())
				if err != nil {
					t.Fatal(err)
				}
				workspace, err := retryService.GetRoomWorkspace(context.Background(), task.RoomID())
				if err != nil || len(workspace.Tasks) != 1 || workspace.Tasks[0].CurrentAction.Kind != app.CurrentActionRetryImplementation || workspace.Tasks[0].CurrentAction.Target.ResultID != currentResult.ID() || !workspace.Room.HumanActionRequired {
					t.Fatalf("second needs-revision workspace=%#v err=%v", workspace, err)
				}
				directory, err := retryService.ListRoomDirectory(context.Background())
				if err != nil || len(directory.ActiveRooms) != 1 || !directory.ActiveRooms[0].HumanActionRequired {
					t.Fatalf("second needs-revision directory=%#v err=%v", directory, err)
				}
			}
			conflict := request
			conflict.CommandMeta = meta("conflicting-review-"+test.name, "other")
			if test.kind == domain.ReviewDecisionAccept {
				conflict.Kind, conflict.RejectionClass = domain.ReviewDecisionReject, domain.ReviewRejectionImplementationGap
			} else {
				conflict.Kind, conflict.RejectionClass = domain.ReviewDecisionAccept, ""
			}
			if _, err := service.ReviewVerifiedResult(context.Background(), conflict); err == nil {
				t.Fatal("conflicting review succeeded")
			}
		})
	}
}

func TestInspectionFailurePersistsWithoutFakeBindingAndRepairsAfterRestart(t *testing.T) {
	for _, test := range []struct {
		name       string
		nextTarget string
		wantErr    bool
	}{
		{name: "same target repairs and applies", nextTarget: "sha256:target"},
		{name: "changed target stays rejected", nextTarget: "sha256:other", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newVerificationFlowFixture(t, "pass")
			defer fixture.lifecycle()
			if _, err := fixture.service.StartVerification(context.Background(), app.StartVerificationRequest{CommandMeta: meta("start-inspection-"+test.name, "start"), RunID: fixture.run.ID(), ExpectedVersion: fixture.run.Version()}); err != nil {
				t.Fatal(err)
			}
			awaiting := fixture.waitRunState(t, domain.RunStateAwaitingReview)
			projection, err := fixture.db.Reader().GetTerminalProjection(context.Background(), fixture.started.Attempt.ID())
			if err != nil || len(projection.Artifacts) != 1 {
				t.Fatalf("artifacts=%#v err=%v", projection.Artifacts, err)
			}
			target := &recoveringPatchTarget{
				firstTarget: "sha256:target", target: test.nextTarget,
				before: sha256.Sum256([]byte("real-before")), after: sha256.Sum256([]byte("real-after")),
			}
			newService := func() *app.Service {
				return app.NewService(app.Dependencies{
					Store: fixture.db, PatchSource: reviewPatchSourceFake{locator: projection.Artifacts[0].Locator, patch: fixture.patch}, PatchTarget: target,
					Presence: fakePresence{}, Authorizer: allowAuthorizer{}, Clock: &fixedClock{now: time.Date(2026, 8, 24, 20, 0, 0, 0, time.UTC)}, IDs: app.RandomIDs{},
				})
			}
			service := newService()
			reviewed, err := service.ReviewVerifiedResult(context.Background(), app.VerifiedReviewRequest{
				CommandMeta: meta("accept-inspection-"+test.name, "accept"), RunID: fixture.run.ID(), ExpectedVersion: awaiting.Version(), Kind: domain.ReviewDecisionAccept,
				Reason: "Accept the exact verified Patch.", ActorID: "local-human", SessionID: "local-browser",
			})
			if err != nil {
				t.Fatal(err)
			}
			first, err := service.ApplyAcceptedPatch(context.Background(), app.ApplyAcceptedPatchRequest{CommandMeta: meta("inspect-fail-"+test.name, "apply"), RunID: fixture.run.ID(), ExpectedVersion: reviewed.Run.Version()})
			if err != nil || first.InspectionFailure == nil || first.Application.State() != "" {
				t.Fatalf("first=%#v err=%v", first, err)
			}
			if _, err := fixture.db.Reader().GetPatchApplication(context.Background(), fixture.run.ID()); !errors.Is(err, storecontract.ErrNotFound) {
				t.Fatalf("inspection failure created fake application binding: %v", err)
			}
			persistedFailure, err := fixture.db.Reader().GetPatchInspectionFailure(context.Background(), fixture.run.ID())
			if err != nil || persistedFailure.TargetIdentity() != "sha256:target" {
				t.Fatalf("inspection failure=%#v err=%v", persistedFailure, err)
			}

			restarted := newService()
			second, err := restarted.ApplyAcceptedPatch(context.Background(), app.ApplyAcceptedPatchRequest{CommandMeta: meta("inspect-retry-"+test.name, "apply"), RunID: fixture.run.ID(), ExpectedVersion: reviewed.Run.Version()})
			if test.wantErr {
				if !errors.Is(err, app.ErrInvalidCommand) {
					t.Fatalf("binding-change err=%v", err)
				}
				return
			}
			if err != nil || second.Application.State() != domain.PatchApplicationApplied || second.Application.PreStateDigest() != target.before {
				t.Fatalf("second=%#v err=%v", second, err)
			}
			closed, err := fixture.db.Reader().GetTask(context.Background(), fixture.run.TaskID())
			if err != nil || closed.State() != domain.TaskStateClosed {
				t.Fatalf("closed=%#v err=%v", closed, err)
			}
		})
	}
}

func TestVerifiedAgentRetryKeepsFrozenContextAndCreatesFreshVerificationLineage(t *testing.T) {
	fixture := newVerificationFlowFixture(t, "fail")
	defer fixture.lifecycle()
	firstStarted, err := fixture.service.StartVerification(context.Background(), app.StartVerificationRequest{CommandMeta: meta("start-first-failing-verification", "start"), RunID: fixture.run.ID(), ExpectedVersion: fixture.run.Version()})
	if err != nil {
		t.Fatal(err)
	}
	revision := fixture.waitRunState(t, domain.RunStateRevisionRequired)
	firstResult, err := fixture.db.Reader().GetVerificationResultForVerificationRun(context.Background(), firstStarted.VerificationRun.ID())
	if err != nil || firstResult.Outcome() != domain.ResultNeedsRevision {
		t.Fatalf("first Result = %#v, %v", firstResult, err)
	}
	task, err := fixture.db.Reader().GetTask(context.Background(), fixture.run.TaskID())
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := fixture.service.GetRoomWorkspace(context.Background(), task.RoomID())
	if err != nil || len(workspace.Tasks) != 1 || workspace.Tasks[0].CurrentAction.Kind != app.CurrentActionRetryImplementation || workspace.Tasks[0].CurrentAction.Target.ResultID != firstResult.ID() || !workspace.Room.HumanActionRequired {
		t.Fatalf("needs-revision workspace=%#v err=%v", workspace, err)
	}
	directory, err := fixture.service.ListRoomDirectory(context.Background())
	if err != nil || len(directory.ActiveRooms) != 1 || !directory.ActiveRooms[0].HumanActionRequired {
		t.Fatalf("needs-revision directory=%#v err=%v", directory, err)
	}
	projection, err := fixture.db.Reader().GetTerminalProjection(context.Background(), fixture.started.Attempt.ID())
	if err != nil || len(projection.Artifacts) != 1 {
		t.Fatalf("artifacts = %#v, %v", projection.Artifacts, err)
	}
	retryService := app.NewService(app.Dependencies{
		Lifecycle: context.Background(), Store: fixture.db, Context: app.ContextAssembler{}, Agents: staticRegistry{adapter: verificationPiAdapter{fakeAdapter: fixture.adapter}}, Supervisor: fixture.supervisor,
		Presence: fakePresence{}, Authorizer: allowAuthorizer{}, Clock: &fixedClock{now: time.Date(2026, 8, 13, 2, 0, 0, 0, time.UTC)}, IDs: app.RandomIDs{}, Verifier: fixture.executor,
		PatchSource: reviewPatchSourceFake{locator: projection.Artifacts[0].Locator, patch: fixture.patch},
	})
	prepared, err := retryService.PrepareVerifiedAgentRetry(context.Background(), app.PrepareVerifiedAgentRetryRequest{
		CommandMeta: meta("prepare-verified-agent-retry", "retry"), RunID: fixture.run.ID(), ExpectedVersion: revision.Version(), Instructions: "Repair only the failed frozen Acceptance Criterion.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Attempt.Sequence() != 2 || prepared.Attempt.ContextSnapshotID() != fixture.started.Attempt.ContextSnapshotID() || prepared.Attempt.ContextDigest() != fixture.started.Attempt.ContextDigest() || prepared.Attempt.AgentExecutionProfileBinding() != fixture.started.Attempt.AgentExecutionProfileBinding() || prepared.Attempt.ExternalSession() != "" {
		t.Fatalf("successor Attempt = %#v", prepared.Attempt)
	}
	if predecessor, ok := prepared.Attempt.Predecessor(); !ok || predecessor != fixture.started.Attempt.ID() {
		t.Fatalf("predecessor = %s, %v", predecessor, ok)
	}
	started, err := retryService.StartAttempt(context.Background(), app.StartAttemptRequest{CommandMeta: meta("start-verified-agent-retry", "start"), RunID: fixture.run.ID(), ExpectedVersion: prepared.Run.Version(), Mode: app.StartFresh})
	if err != nil || started.Session == nil {
		t.Fatalf("start successor = %#v, %v", started, err)
	}
	fixture.executor.setMode("pass")
	fixture.supervisor.reconcileOutcome = execution.ReconcileOutcome{Kind: execution.ReconcileDead, Handle: execution.RuntimeHandle{Value: "handle"}, LaunchToken: fixture.supervisor.invocation.LaunchToken()}
	fixture.supervisor.sink.Exited()
	terminal, err := retryService.HandleExit(context.Background(), app.ExitRequest{CommandMeta: meta("terminal-verified-agent-retry", "terminal"), SessionID: started.Session.ID, ExpectedVersion: started.Run.Version()})
	if err != nil || terminal.Run.State() != domain.RunStateAwaitingVerification {
		t.Fatalf("successor terminal = %#v, %v", terminal, err)
	}
	secondStarted, err := retryService.StartVerification(context.Background(), app.StartVerificationRequest{CommandMeta: meta("start-successor-verification", "verification"), RunID: fixture.run.ID(), ExpectedVersion: terminal.Run.Version()})
	if err != nil || secondStarted.VerificationRun.AgentAttemptID() != prepared.Attempt.ID() || secondStarted.VerificationRun.ID() == firstStarted.VerificationRun.ID() {
		t.Fatalf("successor verification = %#v, %v", secondStarted, err)
	}
	fixture.service = retryService
	fixture.waitRunState(t, domain.RunStateAwaitingReview)
	workspace, err = retryService.GetRoomWorkspace(context.Background(), task.RoomID())
	if err != nil || len(workspace.Tasks) != 1 || workspace.Tasks[0].CurrentAction.Kind != app.CurrentActionReviewResult || !workspace.Room.HumanActionRequired {
		t.Fatalf("successor workspace=%#v err=%v", workspace, err)
	}
	directory, err = retryService.ListRoomDirectory(context.Background())
	if err != nil || len(directory.ActiveRooms) != 1 || !directory.ActiveRooms[0].HumanActionRequired {
		t.Fatalf("successor directory=%#v err=%v", directory, err)
	}
	agentAttempts, _ := fixture.db.Reader().ListAttemptsForRun(context.Background(), fixture.run.ID())
	reports, _ := fixture.db.Reader().ListAgentReportsForRun(context.Background(), fixture.run.ID())
	verificationRuns, _ := fixture.db.Reader().ListVerificationRunsForRun(context.Background(), fixture.run.ID())
	if len(agentAttempts) != 2 || len(reports) != 2 || len(verificationRuns) != 2 || reports[0].AttemptID() != fixture.started.Attempt.ID() || reports[1].AttemptID() != prepared.Attempt.ID() {
		t.Fatalf("lineage attempts=%d reports=%#v verificationRuns=%#v", len(agentAttempts), reports, verificationRuns)
	}
	preserved, err := fixture.db.Reader().GetVerificationResultForVerificationRun(context.Background(), firstStarted.VerificationRun.ID())
	if err != nil || preserved.ID() != firstResult.ID() || preserved.Outcome() != domain.ResultNeedsRevision {
		t.Fatalf("predecessor Result changed = %#v, %v", preserved, err)
	}
}

func TestIndependentVerificationRetryPreservesAcceptedSnapshotLineage(t *testing.T) {
	fixture := newVerificationFlowFixture(t, "fail")
	defer fixture.lifecycle()
	if _, err := fixture.service.StartVerification(context.Background(), app.StartVerificationRequest{
		CommandMeta: meta("start-predecessor-verification", "start"), RunID: fixture.run.ID(), ExpectedVersion: fixture.run.Version(),
	}); err != nil {
		t.Fatal(err)
	}
	revision := fixture.waitRunState(t, domain.RunStateRevisionRequired)
	<-fixture.executor.started
	prepared, err := fixture.service.PrepareRetry(context.Background(), app.PrepareRetryRequest{
		CommandMeta: meta("prepare-ordinary-retry", "prepare"), RunID: fixture.run.ID(), ExpectedVersion: revision.Version(),
		Reason: "Retry the governed Agent after the failed predecessor.", Instructions: "Preserve the registered contract while producing a fresh patch.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Attempt.ContextSnapshotID() != fixture.started.Attempt.ContextSnapshotID() || prepared.Attempt.ContextDigest() != fixture.started.Attempt.ContextDigest() {
		t.Fatalf("retry substituted the accepted Snapshot: predecessor=%#v retry=%#v", fixture.started.Attempt, prepared.Attempt)
	}
	started, err := fixture.service.StartAttempt(context.Background(), app.StartAttemptRequest{
		CommandMeta: meta("start-ordinary-retry", "start"), RunID: fixture.run.ID(), ExpectedVersion: prepared.Run.Version(), Mode: app.StartFresh,
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.executor.setMode("pass")
	fixture.supervisor.reconcileOutcome = execution.ReconcileOutcome{Kind: execution.ReconcileDead, Handle: execution.RuntimeHandle{Value: "handle"}, LaunchToken: fixture.supervisor.invocation.LaunchToken()}
	fixture.supervisor.sink.Exited()
	terminal, err := fixture.service.HandleExit(context.Background(), app.ExitRequest{
		CommandMeta: meta("terminal-ordinary-retry", "terminal"), SessionID: started.Session.ID, ExpectedVersion: started.Run.Version(),
	})
	if err != nil || terminal.Run.State() != domain.RunStateAwaitingVerification {
		t.Fatalf("ordinary retry terminal = %#v, %v", terminal, err)
	}
	verification, err := fixture.service.StartVerification(context.Background(), app.StartVerificationRequest{
		CommandMeta: meta("verify-ordinary-retry", "verify"), RunID: fixture.run.ID(), ExpectedVersion: terminal.Run.Version(),
	})
	if err != nil || verification.VerificationRun.AgentAttemptID() != prepared.Attempt.ID() {
		t.Fatalf("ordinary retry verification = %#v, %v", verification, err)
	}
	fixture.waitRunState(t, domain.RunStateAwaitingReview)
	request := <-fixture.executor.started
	if request.Bindings.ContextSnapshotDigest != prepared.Attempt.ContextDigest() {
		t.Fatalf("Verifier evidence bound digest = %x, want retry digest %x", request.Bindings.ContextSnapshotDigest, prepared.Attempt.ContextDigest())
	}
	reviewService := app.NewService(app.Dependencies{
		Store: fixture.db, PatchSource: reviewPatchSourceFake{locator: request.PatchLocator, patch: fixture.patch},
	})
	change, err := reviewService.LoadReviewableChange(context.Background(), fixture.run.ID())
	if err != nil || change.Binding.AgentAttemptID != prepared.Attempt.ID() || change.Binding.PatchDigest != request.Bindings.PatchDigest {
		t.Fatalf("ordinary retry reviewable change = %#v, %v", change, err)
	}
}

func TestVerifiedAgentRetryHonorsHumanRejectionClass(t *testing.T) {
	for _, test := range []struct {
		name    string
		class   domain.ReviewRejectionClass
		allowed bool
	}{
		{name: "implementation gap", class: domain.ReviewRejectionImplementationGap, allowed: true},
		{name: "planning gap", class: domain.ReviewRejectionPlanningGap, allowed: false},
		{name: "contract change", class: domain.ReviewRejectionContractChangeRequired, allowed: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newVerificationFlowFixture(t, "pass")
			defer fixture.lifecycle()
			if _, err := fixture.service.StartVerification(context.Background(), app.StartVerificationRequest{CommandMeta: meta("start-class-"+test.name, "start"), RunID: fixture.run.ID(), ExpectedVersion: fixture.run.Version()}); err != nil {
				t.Fatal(err)
			}
			awaiting := fixture.waitRunState(t, domain.RunStateAwaitingReview)
			projection, err := fixture.db.Reader().GetTerminalProjection(context.Background(), fixture.started.Attempt.ID())
			if err != nil || len(projection.Artifacts) != 1 {
				t.Fatalf("artifacts = %#v, %v", projection.Artifacts, err)
			}
			service := app.NewService(app.Dependencies{
				Store: fixture.db, PatchSource: reviewPatchSourceFake{locator: projection.Artifacts[0].Locator, patch: fixture.patch}, Presence: fakePresence{}, Authorizer: allowAuthorizer{},
				Clock: &fixedClock{now: time.Date(2026, 8, 13, 3, 0, 0, 0, time.UTC)}, IDs: app.RandomIDs{},
			})
			reviewed, err := service.ReviewVerifiedResult(context.Background(), app.VerifiedReviewRequest{
				CommandMeta: meta("reject-class-"+test.name, "review"), RunID: fixture.run.ID(), ExpectedVersion: awaiting.Version(), Kind: domain.ReviewDecisionReject,
				Reason: "The reviewed implementation requires a bounded correction.", RejectionClass: test.class, ActorID: "local-human", SessionID: "local-browser",
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = service.PrepareVerifiedAgentRetry(context.Background(), app.PrepareVerifiedAgentRetryRequest{
				CommandMeta: meta("retry-class-"+test.name, "retry"), RunID: fixture.run.ID(), ExpectedVersion: reviewed.Run.Version(), Instructions: "Change only the declared implementation files.",
			})
			if test.allowed && err != nil {
				t.Fatalf("implementation-gap Retry error = %v", err)
			}
			if !test.allowed && (err == nil || !strings.Contains(err.Error(), string(test.class))) {
				t.Fatalf("non-implementation Retry error = %v", err)
			}
		})
	}
}

func TestCancelThenExplicitVerifierRetryPreservesAttemptLineageWithoutAgentRerun(t *testing.T) {
	fixture := newVerificationFlowFixture(t, "block")
	defer fixture.lifecycle()
	started, err := fixture.service.StartVerification(context.Background(), app.StartVerificationRequest{CommandMeta: meta("start-cancel-verifier", "start"), RunID: fixture.run.ID(), ExpectedVersion: fixture.run.Version()})
	if err != nil {
		t.Fatal(err)
	}
	<-fixture.executor.started
	if _, err := fixture.service.CancelVerification(context.Background(), app.CancelVerificationRequest{CommandMeta: meta("cancel-verifier", "cancel"), RunID: fixture.run.ID(), ExpectedVersion: started.Run.Version()}); err != nil {
		t.Fatal(err)
	}
	awaiting := fixture.waitRunState(t, domain.RunStateAwaitingVerification)
	if _, err := fixture.db.Reader().GetVerificationResultForRun(context.Background(), fixture.run.ID()); !errors.Is(err, storecontract.ErrNotFound) {
		t.Fatalf("cancel created Result: %v", err)
	}
	first, err := fixture.db.Reader().GetVerificationAttempt(context.Background(), started.Attempt.ID())
	if err != nil || first.Attempt.State() != domain.VerificationAttemptCancelled || !first.CleanupProven {
		t.Fatalf("cancelled Attempt = %#v, %v", first, err)
	}
	firstEvidence, err := fixture.db.Reader().ListVerificationCommandEvidence(context.Background(), first.Attempt.ID())
	if err != nil || len(firstEvidence) != 1 || firstEvidence[0].Classification() != domain.VerificationCommandCancelled {
		t.Fatalf("cancel evidence = %#v, %v", firstEvidence, err)
	}
	fixture.executor.setMode("pass")
	retried, err := fixture.service.RetryVerification(context.Background(), app.StartVerificationRequest{CommandMeta: meta("retry-verifier", "retry"), RunID: fixture.run.ID(), ExpectedVersion: awaiting.Version()})
	if err != nil || retried.Attempt.Sequence() != 2 {
		t.Fatalf("retry = %#v, %v", retried, err)
	}
	if predecessor, ok := retried.Attempt.Predecessor(); !ok || predecessor != first.Attempt.ID() {
		t.Fatalf("retry predecessor = %s, %v", predecessor, ok)
	}
	fixture.waitRunState(t, domain.RunStateAwaitingReview)
	agentAttempts, _ := fixture.db.Reader().ListAttemptsForRun(context.Background(), fixture.run.ID())
	verificationAttempts, _ := fixture.db.Reader().ListVerificationAttempts(context.Background(), started.VerificationRun.ID())
	if len(agentAttempts) != 1 || len(verificationAttempts) != 2 {
		t.Fatalf("Agent Attempts=%d verification Attempts=%d", len(agentAttempts), len(verificationAttempts))
	}
}

func TestAcceptedCancelDurablyFencesLateCompletedVerifierResult(t *testing.T) {
	fixture := newVerificationFlowFixture(t, "late-complete")
	defer fixture.lifecycle()
	started, err := fixture.service.StartVerification(context.Background(), app.StartVerificationRequest{CommandMeta: meta("start-late-complete", "start"), RunID: fixture.run.ID(), ExpectedVersion: fixture.run.Version()})
	if err != nil {
		t.Fatal(err)
	}
	<-fixture.executor.started
	if _, err := fixture.service.CancelVerification(context.Background(), app.CancelVerificationRequest{CommandMeta: meta("cancel-late-complete", "cancel"), RunID: fixture.run.ID(), ExpectedVersion: started.Run.Version()}); err != nil {
		t.Fatal(err)
	}
	fixture.waitRunState(t, domain.RunStateAwaitingVerification)
	if _, err := fixture.db.Reader().GetVerificationResultForRun(context.Background(), fixture.run.ID()); !errors.Is(err, storecontract.ErrNotFound) {
		t.Fatalf("accepted cancel race created Result: %v", err)
	}
	requested, err := fixture.db.Reader().VerificationCancelRequested(context.Background(), started.Attempt.ID())
	if err != nil || !requested {
		t.Fatalf("durable cancel fence = %v, %v", requested, err)
	}
	attempt, err := fixture.db.Reader().GetVerificationAttempt(context.Background(), started.Attempt.ID())
	if err != nil || attempt.Attempt.State() != domain.VerificationAttemptCancelled || !attempt.CleanupProven || attempt.WorkspaceIdentity == "" {
		t.Fatalf("late completion cancellation = %#v, %v", attempt, err)
	}
	commands, err := fixture.db.Reader().ListVerificationCommandEvidence(context.Background(), started.Attempt.ID())
	if err != nil || len(commands) != 1 || commands[0].Classification() != domain.VerificationCommandExited || commands[0].WorkspaceIdentity() != attempt.WorkspaceIdentity {
		t.Fatalf("late completion evidence = %#v, %v", commands, err)
	}
}

func TestRestartMarksRunningVerificationRecoveryRequiredThenExplicitRetry(t *testing.T) {
	fixture := newVerificationFlowFixture(t, "block")
	started, err := fixture.service.StartVerification(context.Background(), app.StartVerificationRequest{CommandMeta: meta("start-restart-verifier", "start"), RunID: fixture.run.ID(), ExpectedVersion: fixture.run.Version()})
	if err != nil {
		t.Fatal(err)
	}
	<-fixture.executor.started
	fixture.executor.setMode("pass")
	restarted := app.NewService(app.Dependencies{Store: fixture.db, Authorizer: allowAuthorizer{}, Clock: &fixedClock{now: time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)}, IDs: app.RandomIDs{}, Verifier: fixture.executor})
	if err := restarted.RecoverStartup(context.Background()); err != nil {
		t.Fatal(err)
	}
	recovery := fixture.waitRunState(t, domain.RunStateVerificationRecoveryRequired)
	if _, err := fixture.db.Reader().GetVerificationResultForRun(context.Background(), fixture.run.ID()); !errors.Is(err, storecontract.ErrNotFound) {
		t.Fatalf("restart created Result: %v", err)
	}
	current, err := fixture.db.Reader().GetVerificationAttempt(context.Background(), started.Attempt.ID())
	if err != nil || current.Attempt.State() != domain.VerificationAttemptRecoveryRequired {
		t.Fatalf("recovery Attempt = %#v, %v", current, err)
	}
	fixture.lifecycle()
	retried, err := restarted.RetryVerification(context.Background(), app.StartVerificationRequest{CommandMeta: meta("retry-after-restart", "retry"), RunID: fixture.run.ID(), ExpectedVersion: recovery.Version()})
	if err != nil || retried.Attempt.Sequence() != 2 {
		t.Fatalf("retry = %#v, %v", retried, err)
	}
	fixture.service = restarted
	fixture.waitRunState(t, domain.RunStateAwaitingReview)
}

func TestUntrustedOrUnpersistedVerificationNeverCreatesResult(t *testing.T) {
	t.Run("verifier boundary", func(t *testing.T) {
		fixture := newVerificationFlowFixture(t, "recovery")
		defer fixture.lifecycle()
		if _, err := fixture.service.StartVerification(context.Background(), app.StartVerificationRequest{CommandMeta: meta("start-untrusted-verifier", "start"), RunID: fixture.run.ID(), ExpectedVersion: fixture.run.Version()}); err != nil {
			t.Fatal(err)
		}
		fixture.waitRunState(t, domain.RunStateVerificationRecoveryRequired)
		if _, err := fixture.db.Reader().GetVerificationResultForRun(context.Background(), fixture.run.ID()); !errors.Is(err, storecontract.ErrNotFound) {
			t.Fatalf("untrusted boundary created Result: %v", err)
		}
	})

	t.Run("recovery preserves produced command evidence", func(t *testing.T) {
		fixture := newVerificationFlowFixture(t, "recovery-evidence")
		defer fixture.lifecycle()
		started, err := fixture.service.StartVerification(context.Background(), app.StartVerificationRequest{CommandMeta: meta("start-recovery-evidence", "start"), RunID: fixture.run.ID(), ExpectedVersion: fixture.run.Version()})
		if err != nil {
			t.Fatal(err)
		}
		fixture.waitRunState(t, domain.RunStateVerificationRecoveryRequired)
		attempt, err := fixture.db.Reader().GetVerificationAttempt(context.Background(), started.Attempt.ID())
		if err != nil || attempt.WorkspaceIdentity == "" || attempt.CleanupProven {
			t.Fatalf("recovery Attempt identity/proof = %#v, %v", attempt, err)
		}
		commands, err := fixture.db.Reader().ListVerificationCommandEvidence(context.Background(), started.Attempt.ID())
		if err != nil || len(commands) != 1 || commands[0].Classification() != domain.VerificationCommandLaunchFailed || commands[0].WorkspaceIdentity() != attempt.WorkspaceIdentity {
			t.Fatalf("recovery evidence = %#v, %v", commands, err)
		}
		if _, err := fixture.db.Reader().GetVerificationResultForRun(context.Background(), fixture.run.ID()); !errors.Is(err, storecontract.ErrNotFound) {
			t.Fatalf("recovery evidence created Result: %v", err)
		}
	})

	t.Run("persistence rollback", func(t *testing.T) {
		fixture := newVerificationFlowFixture(t, "pass")
		defer fixture.lifecycle()
		wrapped := &nthFailStore{Store: fixture.db, failAt: 2, err: errors.New("precommit persistence failure")}
		service := app.NewService(app.Dependencies{Store: wrapped, Authorizer: allowAuthorizer{}, Clock: &fixedClock{now: time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)}, IDs: app.RandomIDs{}, Verifier: fixture.executor})
		fixture.service = service
		if _, err := service.StartVerification(context.Background(), app.StartVerificationRequest{CommandMeta: meta("start-persistence-failure", "start"), RunID: fixture.run.ID(), ExpectedVersion: fixture.run.Version()}); err != nil {
			t.Fatal(err)
		}
		fixture.waitRunState(t, domain.RunStateVerificationRecoveryRequired)
		if _, err := fixture.db.Reader().GetVerificationResultForRun(context.Background(), fixture.run.ID()); !errors.Is(err, storecontract.ErrNotFound) {
			t.Fatalf("rolled-back completion created Result: %v", err)
		}
	})
}
