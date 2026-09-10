package localweb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	agentpi "github.com/Yangyang96/chora/internal/agent/pi"
	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/preflight"
	"github.com/Yangyang96/chora/internal/speccoding"
	"github.com/Yangyang96/chora/internal/verifier"
)

func TestU3RealStartPreflightAndVerifierGatesAreZeroMutation(t *testing.T) {
	for _, test := range []struct {
		name            string
		failPreflight   bool
		disableVerifier bool
	}{
		{name: "live preflight fails after cached green", failPreflight: true},
		{name: "live preflight passes but verifier becomes unavailable after cached green", disableVerifier: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newU3PreflightFixture(t, "pass")
			fixture.cacheGreen(t)
			callsBefore := fixture.preflight.callCount()
			fixture.preflight.setFailed(test.failPreflight)
			if test.disableVerifier {
				fixture.server.verifierStatus = verifierRuntimeStatus{Reason: "test verifier unavailable"}
				fixture.server.verifier = nil
			}
			stateBefore := snapshotU3PersistentState(t, fixture.databasePath)

			var report preflight.Report
			requestJSONWithHeaders(t, fixture.handler, http.MethodPost, "/api/tasks/"+fixture.taskID+"/runs", nil,
				map[string]string{"Idempotency-Key": "u3-blocked-initial-start"}, http.StatusServiceUnavailable, &report)
			assertTableCounts(t, fixture.databasePath, map[string]int{
				"runs": 0, "attempts": 0, "runtime_sessions": 0, "run_events": 0,
			})
			if stateAfter := snapshotU3PersistentState(t, fixture.databasePath); stateAfter != stateBefore {
				t.Fatalf("blocked Start changed exact Room/Task/Plan/Run/resource/event/runtime state: before=%s after=%s", stateBefore, stateAfter)
			}
			if got := fixture.supervisor.snapshot(); got.starts != 0 || got.adapterID != "" || got.launchToken != "" {
				t.Fatalf("blocked Start invoked runtime: %#v", got)
			}
			if test.failPreflight && (report.Failure == nil || report.Failure.Boundary != "u3.live") {
				t.Fatalf("live Preflight report = %#v", report)
			}
			if got := fixture.preflight.callCount(); got != callsBefore+1 {
				t.Fatalf("Preflight calls = %d, want %d", got, callsBefore+1)
			}
		})
	}
}

func TestU3ImplementationRetryPreflightGatePreservesEveryPredecessor(t *testing.T) {
	for _, test := range []struct {
		name string
		mode string
	}{
		{name: "trusted needs_revision", mode: "fail"},
		{name: "human implementation_gap", mode: "pass"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newU3PreflightFixture(t, test.mode)
			run := fixture.completeAgent(t)
			requestJSONWithHeaders(t, fixture.handler, http.MethodPost, "/api/runs/"+run.ID+"/verification", map[string]any{},
				map[string]string{"Idempotency-Key": "u3-setup-verification"}, http.StatusAccepted, nil)

			if test.mode == "fail" {
				run = waitForRunState(t, fixture.handler, run.ID, string(domain.RunStateRevisionRequired))
			} else {
				run = waitForRunState(t, fixture.handler, run.ID, string(domain.RunStateAwaitingReview))
				var rejected runView
				requestJSONWithHeaders(t, fixture.handler, http.MethodPost, "/api/runs/"+run.ID+"/review", map[string]any{
					"expectedVersion": run.Version, "kind": "reject", "note": "Repair only the bounded implementation gap.", "rejectionClass": "implementation_gap",
				}, map[string]string{"Idempotency-Key": "u3-human-implementation-gap"}, http.StatusOK, &rejected)
				if rejected.Status != domain.RunStateRevisionRequired || rejected.VerifiedReview == nil || rejected.VerifiedReview.RejectionClass != "implementation_gap" {
					t.Fatalf("human implementation-gap predecessor = %#v", rejected)
				}
			}

			before := snapshotU3Run(t, fixture.databasePath, run.ID)
			if test.mode == "fail" && before.verificationResultOutcome != string(domain.ResultNeedsRevision) {
				t.Fatalf("trusted needs_revision predecessor = %#v", before)
			}
			if test.mode == "pass" && before.verificationResultOutcome != string(domain.ResultReviewReady) {
				t.Fatalf("human implementation-gap predecessor = %#v", before)
			}
			invocationBefore := fixture.supervisor.snapshot()
			fixture.cacheGreen(t)
			callsBefore := fixture.preflight.callCount()
			fixture.preflight.setFailed(true)
			var report preflight.Report
			requestJSONWithHeaders(t, fixture.handler, http.MethodPost, "/api/runs/"+run.ID+"/retry", map[string]any{
				"expectedVersion": before.runVersion,
				"instructions":    "Repair only the declared implementation gap and preserve every frozen binding.",
			}, map[string]string{"Idempotency-Key": "u3-blocked-implementation-retry"}, http.StatusServiceUnavailable, &report)

			after := snapshotU3Run(t, fixture.databasePath, run.ID)
			if after != before {
				t.Fatalf("blocked implementation Retry mutated state:\n before=%#v\n after=%#v", before, after)
			}
			if got := fixture.supervisor.snapshot(); got != invocationBefore {
				t.Fatalf("blocked implementation Retry invoked runtime: before=%#v after=%#v", invocationBefore, got)
			}
			if report.Failure == nil || report.Failure.Boundary != "u3.live" || fixture.preflight.callCount() != callsBefore+1 {
				t.Fatalf("Retry did not use one live Preflight: calls=%d report=%#v", fixture.preflight.callCount(), report)
			}
		})
	}
}

func TestU3VerificationPreflightGatesPreserveRunAttemptResultAndEventLineage(t *testing.T) {
	t.Run("start", func(t *testing.T) {
		fixture := newU3PreflightFixture(t, "pass")
		run := fixture.completeAgent(t)
		before := snapshotU3Run(t, fixture.databasePath, run.ID)
		callsBeforeMissingKey := fixture.preflight.callCount()
		requestJSON(t, fixture.handler, http.MethodPost, "/api/runs/"+run.ID+"/verification", map[string]any{}, http.StatusBadRequest, nil)
		if afterMissingKey := snapshotU3Run(t, fixture.databasePath, run.ID); afterMissingKey != before || fixture.preflight.callCount() != callsBeforeMissingKey {
			t.Fatalf("missing caller key changed Verification state or invoked Preflight: before=%#v after=%#v calls=%d/%d", before, afterMissingKey, callsBeforeMissingKey, fixture.preflight.callCount())
		}
		fixture.cacheGreen(t)
		callsBefore := fixture.preflight.callCount()
		fixture.preflight.setFailed(true)
		var report preflight.Report
		requestJSONWithHeaders(t, fixture.handler, http.MethodPost, "/api/runs/"+run.ID+"/verification", map[string]any{},
			map[string]string{"Idempotency-Key": "u3-blocked-verification-start"}, http.StatusServiceUnavailable, &report)
		after := snapshotU3Run(t, fixture.databasePath, run.ID)
		if after != before || after.verificationRuns != 0 || after.verificationAttempts != 0 || after.verificationResults != 0 {
			t.Fatalf("blocked Verification Start mutated lineage:\n before=%#v\n after=%#v", before, after)
		}
		if report.Failure == nil || report.Failure.Boundary != "u3.live" || fixture.preflight.callCount() != callsBefore+1 {
			t.Fatalf("Verification Start did not use one live Preflight: calls=%d report=%#v", fixture.preflight.callCount(), report)
		}
	})

	t.Run("retry", func(t *testing.T) {
		fixture := newU3PreflightFixture(t, "block")
		run := fixture.completeAgent(t)
		requestJSONWithHeaders(t, fixture.handler, http.MethodPost, "/api/runs/"+run.ID+"/verification", map[string]any{},
			map[string]string{"Idempotency-Key": "u3-start-cancelled-verification"}, http.StatusAccepted, nil)
		select {
		case <-fixture.verifier.started:
		case <-time.After(2 * time.Second):
			t.Fatal("Verifier did not start")
		}
		run = waitForRunState(t, fixture.handler, run.ID, string(domain.RunStateVerifying))
		requestJSONWithHeaders(t, fixture.handler, http.MethodPost, "/api/runs/"+run.ID+"/verification/cancel", map[string]any{},
			map[string]string{"Idempotency-Key": "u3-cancel-verification"}, http.StatusAccepted, nil)
		run = waitForRunState(t, fixture.handler, run.ID, string(domain.RunStateAwaitingVerification))

		before := snapshotU3Run(t, fixture.databasePath, run.ID)
		if before.verificationRuns != 1 || before.verificationAttempts != 1 || before.verificationResults != 0 || before.verificationAttemptState != string(domain.VerificationAttemptCancelled) {
			t.Fatalf("retry predecessor is not one cancelled Verification Attempt: %#v", before)
		}
		fixture.cacheGreen(t)
		callsBefore := fixture.preflight.callCount()
		fixture.preflight.setFailed(true)
		var report preflight.Report
		requestJSONWithHeaders(t, fixture.handler, http.MethodPost, "/api/runs/"+run.ID+"/verification/retry", map[string]any{},
			map[string]string{"Idempotency-Key": "u3-blocked-verification-retry"}, http.StatusServiceUnavailable, &report)
		after := snapshotU3Run(t, fixture.databasePath, run.ID)
		if after != before {
			t.Fatalf("blocked Verification Retry changed predecessor:\n before=%#v\n after=%#v", before, after)
		}
		if report.Failure == nil || report.Failure.Boundary != "u3.live" || fixture.preflight.callCount() != callsBefore+1 {
			t.Fatalf("Verification Retry did not use one live Preflight: calls=%d report=%#v", fixture.preflight.callCount(), report)
		}
	})
}

type u3PreflightFixture struct {
	server       *Server
	handler      http.Handler
	databasePath string
	taskID       string
	preflight    *u3PreflightSwitch
	supervisor   *u3Supervisor
	verifier     *u3Verifier
}

func newU3PreflightFixture(t *testing.T, verifierMode string) *u3PreflightFixture {
	t.Helper()
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	repositoryRoot = filepath.Clean(repositoryRoot)
	envelope, err := speccoding.LoadInstalledEnvelope(repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	webRoot := filepath.Join(root, "web")
	if err := os.Mkdir(webRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(webRoot, "index.html"), []byte("<!doctype html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(root, "chora.db")
	server, err := newTestServer(context.Background(), databasePath, webRoot, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	enablePiTestRuntime(t, server)
	preflightGate := &u3PreflightSwitch{}
	server.piPreflight = preflightGate.run
	server.piBaseline = nil
	server.repoRoot = repositoryRoot
	server.installedEnvelope = &envelope
	server.product = true
	server.installationGenerationID = "test-generation"
	server.generationConfigErr = nil
	server.generationPublisher = &recordingGenerationPublisher{activeGenerationID: "test-generation"}
	server.taskWorktreesReady = true
	server.verifierStatus = verifierRuntimeStatus{Enabled: true, Reason: "test verifier enabled", BaselineDigest: verifier.M1FrozenBaselineDigestHex}
	server.verifier = &productVerifier{}

	patch := []byte("diff --git a/internal/domain/task.go b/internal/domain/task.go\n--- a/internal/domain/task.go\n+++ b/internal/domain/task.go\n@@ -1 +1 @@\n-package domain\n+package domain\n")
	verification := newU3Verifier(verifierMode, patch)
	supervisor := &u3Supervisor{}
	target, err := execution.NewExecutionTarget(agentpi.AdapterID, domain.DockerExecutionProvider)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.supervisor.registerTarget(target, supervisor); err != nil {
		t.Fatal(err)
	}
	server.supervisor.mu.Lock()
	server.supervisor.byAdapter[agentpi.AdapterID] = supervisor
	server.supervisor.mu.Unlock()
	server.service = app.NewService(app.Dependencies{
		Lifecycle: server.ctx, Store: server.store, Context: app.ContextAssembler{}, Agents: server.registry, Supervisor: server.supervisor,
		Presence: localPresence{}, Authorizer: localAuthorizer{}, IDs: app.RandomIDs{}, Verifier: verification, PatchSource: verification,
		InstalledSpecCodingEnvelope: &envelope,
	})
	handler := server.Handler()

	var room struct {
		ID              string           `json:"id"`
		InitialRevision roomRevisionView `json:"initialRevision"`
	}
	requestJSONWithHeaders(t, handler, http.MethodPost, "/api/rooms", map[string]any{
		"name": "U3 live Preflight", "description": "Exercise every Real execution gate.",
	}, map[string]string{"Idempotency-Key": "u3-create-room"}, http.StatusCreated, &room)
	var task taskRefView
	requestJSONWithHeaders(t, handler, http.MethodPost, "/api/rooms/"+room.ID+"/tasks", realTaskAPIBody(room.InitialRevision.ID, "Prove every live Preflight gate"),
		map[string]string{"Idempotency-Key": "u3-create-real-task"}, http.StatusCreated, &task)
	acceptTaskPlan(t, handler, task.ID)
	assertU3PiCharter(t, server, task.ID)

	return &u3PreflightFixture{server: server, handler: handler, databasePath: databasePath, taskID: task.ID, preflight: preflightGate, supervisor: supervisor, verifier: verification}
}

func (fixture *u3PreflightFixture) cacheGreen(t *testing.T) {
	t.Helper()
	fixture.preflight.setFailed(false)
	var readiness readinessView
	requestJSON(t, fixture.handler, http.MethodGet, "/api/readiness", nil, http.StatusOK, &readiness)
	for _, item := range readiness.Items {
		if item.State != readinessReady {
			t.Fatalf("cached readiness is not green: %#v", readiness)
		}
	}
}

func (fixture *u3PreflightFixture) completeAgent(t *testing.T) runView {
	t.Helper()
	taskID, err := domain.ParseTaskID(fixture.taskID)
	if err != nil {
		t.Fatal(err)
	}
	task, err := fixture.server.store.Reader().GetTask(context.Background(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := fakeTerminalResult(task.Criteria(), true)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(result, &document); err != nil {
		t.Fatal(err)
	}
	patchDigest := sha256.Sum256(fixture.verifier.patch)
	document["outputs"] = []map[string]string{{
		"locator": "patch.diff", "description": "Bounded U3 implementation Patch", "sha256": hex.EncodeToString(patchDigest[:]), "media_type": "text/x-diff",
	}}
	document["artifact_candidates"] = []map[string]string{}
	document["unknowns"] = []string{}
	result, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(filepath.Dir(fixture.databasePath), "u3-result.json")
	if err := os.WriteFile(resultPath, result, 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.supervisor.setResultPath(resultPath)
	fixture.preflight.setFailed(false)
	var run runView
	requestJSONWithHeaders(t, fixture.handler, http.MethodPost, "/api/tasks/"+fixture.taskID+"/runs", nil,
		map[string]string{"Idempotency-Key": "u3-start-agent"}, http.StatusCreated, &run)
	if run.Adapter != agentpi.AdapterID || run.Room.WorkspaceRoot != "" {
		t.Fatalf("production Run adapter = %q, want Pi", run.Adapter)
	}
	fixture.supervisor.exit(t)
	return waitForRunState(t, fixture.handler, run.ID, string(domain.RunStateAwaitingVerification))
}

func assertU3PiCharter(t *testing.T, server *Server, taskIDText string) {
	t.Helper()
	taskID, err := domain.ParseTaskID(taskIDText)
	if err != nil {
		t.Fatal(err)
	}
	acceptance, err := server.store.Reader().GetCurrentTechnicalPlanAcceptance(context.Background(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := server.store.Reader().GetSnapshot(context.Background(), acceptance.SnapshotID())
	if err != nil {
		t.Fatal(err)
	}
	charterID, err := snapshotCharterID(snapshot.CanonicalJSON())
	if err != nil {
		t.Fatal(err)
	}
	charter, err := server.store.Reader().GetCharter(context.Background(), charterID)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := server.registry.Get(agentpi.AdapterID)
	if err != nil {
		t.Fatal(err)
	}
	if !server.product || charter.AdapterID() != agentpi.AdapterID {
		t.Fatalf("handler/Charter are not production Pi: product=%v charter=%q", server.product, charter.AdapterID())
	}
	if _, ok := adapter.(*agentpi.Adapter); !ok {
		t.Fatalf("Pi Charter substituted production Agent adapter with %T", adapter)
	}
}

type u3PreflightSwitch struct {
	mu     sync.Mutex
	failed bool
	calls  int
}

func (gate *u3PreflightSwitch) run(context.Context) preflight.Report {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	gate.calls++
	if gate.failed {
		return preflight.Report{Status: preflight.StatusFailed, ResourcesCreated: false, InputFingerprint: hex.EncodeToString(make([]byte, sha256.Size)), Failure: &preflight.Failure{
			Boundary: "u3.live", Observed: "live dependency unavailable", Required: "same-process green Preflight", Action: "repair the live dependency and retry",
		}}
	}
	return preflight.Report{Status: preflight.StatusPassed, ResourcesCreated: false, InputFingerprint: hex.EncodeToString(make([]byte, sha256.Size))}
}

func (gate *u3PreflightSwitch) setFailed(failed bool) {
	gate.mu.Lock()
	gate.failed = failed
	gate.mu.Unlock()
}

func (gate *u3PreflightSwitch) callCount() int {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	return gate.calls
}

type u3SupervisorSnapshot struct {
	starts      int
	adapterID   string
	launchToken string
}

type u3Supervisor struct {
	mu         sync.Mutex
	starts     int
	invocation execution.Invocation
	sink       execution.RuntimeSink
	dead       bool
	resultPath string
}

func (supervisor *u3Supervisor) Start(_ context.Context, invocation execution.Invocation, sink execution.RuntimeSink) execution.StartOutcome {
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	supervisor.starts++
	supervisor.invocation = invocation
	supervisor.sink = sink
	supervisor.dead = false
	return execution.StartOutcome{Kind: execution.Started, Handle: execution.RuntimeHandle{Value: "u3-handle"}, Identity: execution.ProcessIdentity{Value: "u3-process"}, LaunchToken: invocation.LaunchToken()}
}

func (supervisor *u3Supervisor) Stop(context.Context, execution.RuntimeHandle, execution.StopIntent) (execution.StopOutcome, error) {
	supervisor.mu.Lock()
	supervisor.dead = true
	sink := supervisor.sink
	supervisor.mu.Unlock()
	if sink != nil {
		sink.Exited()
	}
	return execution.StopOutcome{Kind: execution.StopConfirmed}, nil
}

func (supervisor *u3Supervisor) Reconcile(context.Context, execution.ProcessIdentity) (execution.ReconcileOutcome, error) {
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	kind := execution.ReconcileAlive
	if supervisor.dead {
		kind = execution.ReconcileDead
	}
	return execution.ReconcileOutcome{Kind: kind, Handle: execution.RuntimeHandle{Value: "u3-handle"}, LaunchToken: supervisor.invocation.LaunchToken()}, nil
}

func (supervisor *u3Supervisor) ReconcileLaunch(context.Context, execution.LaunchToken) (execution.ReconcileOutcome, error) {
	return supervisor.Reconcile(context.Background(), execution.ProcessIdentity{})
}

func (*u3Supervisor) Read(context.Context, execution.RuntimeHandle, execution.StreamKind, int64, int) (execution.StreamChunk, error) {
	return execution.StreamChunk{EOF: true}, nil
}

func (supervisor *u3Supervisor) Drain(_ context.Context, _ execution.RuntimeHandle, _ execution.StreamOffsets, _ int) (execution.DrainOutcome, error) {
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	return execution.DrainOutcome{
		Chunks:        map[execution.StreamKind][]byte{execution.StreamStdout: {}, execution.StreamStderr: {}},
		Offsets:       execution.StreamOffsets{execution.StreamStdout: 0, execution.StreamStderr: 0},
		EOF:           map[execution.StreamKind]bool{execution.StreamStdout: true, execution.StreamStderr: true},
		TerminalFiles: execution.TerminalFiles{ExitCode: 0, Paths: map[string]string{"result": supervisor.resultPath}},
	}, nil
}

func (*u3Supervisor) Finalize(context.Context, execution.RuntimeHandle, execution.RetentionPolicy) error {
	return nil
}

func (supervisor *u3Supervisor) setResultPath(path string) {
	supervisor.mu.Lock()
	supervisor.resultPath = path
	supervisor.mu.Unlock()
}

func (supervisor *u3Supervisor) exit(t *testing.T) {
	t.Helper()
	supervisor.mu.Lock()
	supervisor.dead = true
	sink := supervisor.sink
	supervisor.mu.Unlock()
	if sink == nil {
		t.Fatal("runtime was not started")
	}
	sink.Exited()
}

func (supervisor *u3Supervisor) snapshot() u3SupervisorSnapshot {
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	return u3SupervisorSnapshot{starts: supervisor.starts, adapterID: supervisor.invocation.AdapterID(), launchToken: supervisor.invocation.LaunchToken().Value}
}

type u3Verifier struct {
	mu      sync.Mutex
	mode    string
	patch   []byte
	started chan struct{}
	auth    app.VerificationAuthority
}

func newU3Verifier(mode string, patch []byte) *u3Verifier {
	baseline := sha256.Sum256([]byte("u3-frozen-baseline"))
	policy := sha256.Sum256([]byte("u3-verifier-policy"))
	return &u3Verifier{mode: mode, patch: append([]byte(nil), patch...), started: make(chan struct{}, 1), auth: app.VerificationAuthority{
		BaselineDigest: baseline, VerifierPolicyVersion: "chora.u3-verifier-policy.v1", VerifierPolicyDigest: policy,
	}}
}

func (service *u3Verifier) Authority() app.VerificationAuthority { return service.auth }

func (service *u3Verifier) Verify(ctx context.Context, request app.VerificationExecutionRequest) (verifier.AttemptEvidence, error) {
	select {
	case service.started <- struct{}{}:
	default:
	}
	service.mu.Lock()
	mode := service.mode
	service.mu.Unlock()
	if mode == "block" {
		<-ctx.Done()
		mode = "cancelled"
	}
	document := request.Contract.Document()
	command := document.Acceptance.VerificationCommands[0]
	criterionIDs := make([]string, 0, len(document.Acceptance.Criteria))
	for _, criterion := range document.Acceptance.Criteria {
		criterionIDs = append(criterionIDs, criterion.ID)
	}
	zero, one := 0, 1
	exitCode := &zero
	commandKind := verifier.CommandExited
	classification := verifier.AttemptCompleted
	if mode == "fail" {
		exitCode = &one
	} else if mode == "cancelled" {
		exitCode = nil
		commandKind = verifier.CommandCancelled
		classification = verifier.AttemptCancelled
	}
	now := time.Now().UTC()
	workspaceDigest := sha256.Sum256([]byte(request.AttemptID.String()))
	imageDigest := sha256.Sum256([]byte("u3-verifier-image"))
	identity := verifier.EvidenceIdentity{
		RunID: request.RunID.String(), VerificationRunID: request.VerificationRunID.String(), AttemptID: request.AttemptID.String(), TaskID: request.TaskID.String(),
		WorkspaceIdentity: "sha256:" + hex.EncodeToString(workspaceDigest[:]), VerifierImage: "sha256:" + hex.EncodeToString(imageDigest[:]),
		VerifierPolicyVersion: request.Bindings.VerifierPolicyVersion, BaselineDigest: request.Bindings.BaselineDigest, PatchDigest: request.Bindings.PatchDigest,
		ContextSnapshotDigest: request.Bindings.ContextSnapshotDigest, AcceptanceContractDigest: request.Bindings.AcceptanceContractDigest, VerifierPolicyDigest: request.Bindings.VerifierPolicyDigest,
	}
	empty := verifier.StreamLog{FullSHA256: sha256.Sum256(nil), RedactionPolicyVersion: "chora.verifier-redaction.v1"}
	return verifier.AttemptEvidence{Classification: classification, CleanupProven: true, Commands: []verifier.CommandEvidence{{
		CommandID: command.ID, CriterionIDs: criterionIDs, Argv: command.Argv, StartedAt: now, EndedAt: now.Add(time.Second), Kind: commandKind, ExitCode: exitCode,
		Stdout: empty, Stderr: empty, VerifierIdentity: "docker:test@" + identity.VerifierImage, Identity: identity,
	}}}, nil
}

func (service *u3Verifier) ReadReviewPatch(_ context.Context, locator string) ([]byte, error) {
	if locator != "patch.diff" {
		return nil, errors.New("unexpected Patch locator")
	}
	return append([]byte(nil), service.patch...), nil
}

type u3RunSnapshot struct {
	runVersion                uint64
	currentAttempt            int
	currentAttemptID          string
	currentAttemptState       string
	attempts                  int
	sessions                  int
	events                    int
	verificationRuns          int
	verificationAttempts      int
	verificationResults       int
	verificationRunID         string
	verificationRunState      string
	verificationRunUpdatedAt  string
	verificationAttemptID     string
	verificationAttemptState  string
	verificationPredecessor   string
	verificationResultID      string
	verificationResultOutcome string
}

func snapshotU3Run(t *testing.T, databasePath, runID string) u3RunSnapshot {
	t.Helper()
	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var snapshot u3RunSnapshot
	if err := database.QueryRow(`SELECT version,current_attempt_number FROM runs WHERE id=?`, runID).Scan(&snapshot.runVersion, &snapshot.currentAttempt); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT id,state FROM attempts WHERE run_id=? AND sequence=?`, runID, snapshot.currentAttempt).Scan(&snapshot.currentAttemptID, &snapshot.currentAttemptState); err != nil {
		t.Fatal(err)
	}
	queries := []struct {
		query  string
		target *int
	}{
		{`SELECT COUNT(*) FROM attempts WHERE run_id=?`, &snapshot.attempts},
		{`SELECT COUNT(*) FROM runtime_sessions session JOIN attempts attempt ON attempt.id=session.attempt_id WHERE attempt.run_id=?`, &snapshot.sessions},
		{`SELECT COUNT(*) FROM run_events WHERE run_id=?`, &snapshot.events},
		{`SELECT COUNT(*) FROM verification_runs WHERE run_id=?`, &snapshot.verificationRuns},
		{`SELECT COUNT(*) FROM verification_attempts attempt JOIN verification_runs verification ON verification.id=attempt.verification_run_id WHERE verification.run_id=?`, &snapshot.verificationAttempts},
		{`SELECT COUNT(*) FROM verification_results result JOIN verification_runs verification ON verification.id=result.verification_run_id WHERE verification.run_id=?`, &snapshot.verificationResults},
	}
	for _, query := range queries {
		if err := database.QueryRow(query.query, runID).Scan(query.target); err != nil {
			t.Fatal(err)
		}
	}
	if snapshot.verificationRuns > 0 {
		if err := database.QueryRow(`SELECT id,state,updated_at FROM verification_runs WHERE run_id=? ORDER BY created_at DESC LIMIT 1`, runID).
			Scan(&snapshot.verificationRunID, &snapshot.verificationRunState, &snapshot.verificationRunUpdatedAt); err != nil {
			t.Fatal(err)
		}
		var predecessor sql.NullString
		if err := database.QueryRow(`SELECT id,state,predecessor_id FROM verification_attempts WHERE verification_run_id=? ORDER BY sequence DESC LIMIT 1`, snapshot.verificationRunID).
			Scan(&snapshot.verificationAttemptID, &snapshot.verificationAttemptState, &predecessor); err != nil {
			t.Fatal(err)
		}
		snapshot.verificationPredecessor = predecessor.String
	}
	if snapshot.verificationResults > 0 {
		if err := database.QueryRow(`SELECT result.id,result.outcome FROM verification_results result JOIN verification_runs verification ON verification.id=result.verification_run_id WHERE verification.run_id=? ORDER BY result.created_at DESC LIMIT 1`, runID).
			Scan(&snapshot.verificationResultID, &snapshot.verificationResultOutcome); err != nil {
			t.Fatal(err)
		}
	}
	return snapshot
}

func snapshotU3PersistentState(t *testing.T, databasePath string) string {
	t.Helper()
	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	tableRows, err := database.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for tableRows.Next() {
		var table string
		if err := tableRows.Scan(&table); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, table)
	}
	if err := tableRows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := tableRows.Close(); err != nil {
		t.Fatal(err)
	}

	digest := sha256.New()
	for _, table := range tables {
		identifier := u3QuotedIdentifier(table)
		probe, err := database.Query("SELECT * FROM " + identifier + " LIMIT 0")
		if err != nil {
			t.Fatal(err)
		}
		columns, err := probe.Columns()
		if err != nil {
			probe.Close()
			t.Fatal(err)
		}
		if err := probe.Close(); err != nil {
			t.Fatal(err)
		}
		order := make([]string, len(columns))
		for index, column := range columns {
			order[index] = u3QuotedIdentifier(column)
		}
		query := "SELECT * FROM " + identifier
		if len(order) > 0 {
			query += " ORDER BY " + strings.Join(order, ",")
		}
		rows, err := database.Query(query)
		if err != nil {
			t.Fatal(err)
		}
		io.WriteString(digest, table)
		io.WriteString(digest, "\n")
		for rows.Next() {
			values := make([]any, len(columns))
			targets := make([]any, len(columns))
			for index := range values {
				targets[index] = &values[index]
			}
			if err := rows.Scan(targets...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			encoded, err := json.Marshal(values)
			if err != nil {
				rows.Close()
				t.Fatal(err)
			}
			digest.Write(encoded)
			io.WriteString(digest, "\n")
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func u3QuotedIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}
