//go:build chora_e2e

package localweb

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
	"github.com/Yangyang96/chora/internal/verifier"
)

type passingVerifierCore struct{}

func (passingVerifierCore) Verify(_ context.Context, request verifier.VerifyRequest) (verifier.AttemptEvidence, error) {
	document := request.Contract.Document()
	command := document.Acceptance.VerificationCommands[0]
	criteria := make([]string, 0, len(document.Acceptance.Criteria))
	for _, criterion := range document.Acceptance.Criteria {
		criteria = append(criteria, criterion.ID)
	}
	code := 0
	empty := verifier.StreamLog{FullSHA256: sha256.Sum256(nil), RedactionPolicyVersion: "chora.verifier-redaction.v1"}
	identity := verifier.EvidenceIdentity{RunID: request.RunID, VerificationRunID: request.VerificationRunID, AttemptID: request.AttemptID,
		TaskID: document.Task.ID, VerifierImage: "sha256:91698efead5641a633519f5f229373e08a59264045ca27f6d01fc06505deeea7",
		VerifierPolicyVersion: request.Bindings.VerifierPolicyVersion, BaselineDigest: request.Bindings.BaselineDigest, PatchDigest: request.Bindings.PatchDigest,
		ContextSnapshotDigest: request.Bindings.ContextSnapshotDigest, AcceptanceContractDigest: request.Bindings.AcceptanceContractDigest, VerifierPolicyDigest: request.Bindings.VerifierPolicyDigest}
	identity.WorkspaceIdentity = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	return verifier.AttemptEvidence{Classification: verifier.AttemptCompleted, CleanupProven: true, Commands: []verifier.CommandEvidence{{
		CommandID: command.ID, CriterionIDs: criteria, Argv: command.Argv, StartedAt: time.Now().UTC(), EndedAt: time.Now().UTC(), Kind: verifier.CommandExited,
		ExitCode: &code, Stdout: empty, Stderr: empty, VerifierIdentity: identity.VerifierImage, Identity: identity,
	}}}, nil
}

func TestVerifiedReviewPublicAPIExposesPatchAcceptAndUnconfiguredSCM(t *testing.T) {
	t.Setenv(verifierPolicyModeEnvironment, "full")
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	commandBin := filepath.Join(root, "ambient-command-bin")
	if err := os.Mkdir(commandBin, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"docker", "colima"} {
		if err := os.WriteFile(filepath.Join(commandBin, name), []byte("#!/bin/sh\nexit 97\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", commandBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	webRoot, err := filepath.Abs(filepath.Join("..", "..", "web", "dist"))
	if err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(root, "chora.db")
	if err := prepareLatestSQLiteTestDatabase(databasePath); err != nil {
		t.Fatal(err)
	}
	server, err := NewE2E(context.Background(), databasePath, webRoot, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if server.verifier == nil || !server.verifierStatus.Enabled {
		t.Fatalf("product Verifier unavailable: %#v", server.verifierStatus)
	}
	if _, ok := server.verifier.sandboxLifecycle.(*e2eVerifierSandboxFactory); !ok {
		t.Fatalf("E2E verifier lifecycle = %T", server.verifier.sandboxLifecycle)
	}
	enablePiTestRuntime(t, server)
	handler := server.Handler()
	v4 := readSpecCodingJSON(t, "chora-m1-real-task.v4.json")
	baselineRoot, err := filepath.Abs(filepath.Join("..", "..", "..", ".agent", "evidence", "G2-M3", "source-baseline-v4", "repo"))
	if err != nil {
		t.Fatal(err)
	}
	applyRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"internal/domain/task.go", "internal/domain/task_test.go"} {
		source, readErr := os.ReadFile(filepath.Join(baselineRoot, filepath.FromSlash(relative)))
		if readErr != nil {
			t.Fatal(readErr)
		}
		destination := filepath.Join(applyRoot, filepath.FromSlash(relative))
		if mkdirErr := os.MkdirAll(filepath.Dir(destination), 0o700); mkdirErr != nil {
			t.Fatal(mkdirErr)
		}
		if writeErr := os.WriteFile(destination, source, 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	runGitTargetTest(t, applyRoot, "init")
	runGitTargetTest(t, applyRoot, "add", ".")
	runGitTargetTest(t, applyRoot, "-c", "user.name=Chora Test", "-c", "user.email=chora@example.test", "commit", "-m", "initial")
	patchTarget, err := newGitPatchTarget(applyRoot, nil)
	if err != nil {
		t.Fatal(err)
	}
	server.taskWorktreesReady = true
	server.service = app.NewService(app.Dependencies{
		Lifecycle: server.ctx, Store: server.store, Context: app.ContextAssembler{}, Agents: server.registry, Supervisor: server.supervisor,
		Presence: localPresence{}, Authorizer: localAuthorizer{}, IDs: app.RandomIDs{}, Verifier: server.verifier, PatchSource: server.verifier,
		PatchTarget: patchTarget, AutoStartVerification: true,
	})
	handler = server.Handler()
	var materialized struct {
		TaskID   string `json:"taskId"`
		Snapshot struct {
			ID     string `json:"id"`
			Digest string `json:"digest"`
		} `json:"snapshot"`
	}
	requestJSON(t, handler, http.MethodPost, "/api/spec-coding/materializations", map[string]any{
		"contract": json.RawMessage(v4), "workspaceRoot": baselineRoot,
		"contextEntryId": "context_entry_018f0c4a-1a30-7c3d-8e4f-1234567890ab", "contextRevisionId": "context_revision_018f0c4a-1a31-7c3d-8e4f-1234567890ab",
		"charterId": "charter_018f0c4a-1a32-7c3d-8e4f-1234567890ab", "frozenAt": "2026-08-11T00:00:00Z",
	}, http.StatusCreated, &materialized)
	v8 := versionSpecCodingContractV8(t, v4, materialized.Snapshot.Digest)
	requestJSON(t, handler, http.MethodPost, "/api/spec-coding/contracts", map[string]any{"contract": json.RawMessage(v8)}, http.StatusOK, nil)
	acceptTaskPlan(t, handler, materialized.TaskID)
	var started runView
	requestJSON(t, handler, http.MethodPost, "/api/tasks/"+materialized.TaskID+"/runs", map[string]any{"patchFixture": "g2-m3-authoritative"}, http.StatusCreated, &started)
	// Verification auto-starts once the Agent completes; no explicit command.
	passed := waitForRunState(t, handler, started.ID, "awaiting_review")
	if passed.AgentReport == nil || passed.Verification == nil || passed.Verification.Result == nil || passed.Verification.Result.Outcome != "review_ready" || len(passed.Verification.Attempts) != 1 ||
		len(passed.Verification.Attempts[0].Checks) != 2 || len(passed.Verification.Attempts[0].Commands) != 1 || !passed.Verification.Attempts[0].CleanupProven ||
		passed.Verification.Attempts[0].WorkspaceIdentity == "" || passed.Verification.Attempts[0].Commands[0].WorkspaceIdentity != passed.Verification.Attempts[0].WorkspaceIdentity ||
		passed.Verification.Attempts[0].Commands[0].VerifierIdentity == "" || !passed.Controls.CanReview || passed.Controls.CanRetry || passed.ReviewablePatch == nil ||
		passed.ReviewablePatch.PatchDigest != g2M3AuthoritativePatchSHA256 || len(passed.ReviewablePatch.Files) != 2 || len(passed.Artifacts) != 1 || passed.Artifacts[0].Digest != g2M3AuthoritativePatchSHA256 || passed.SCM.Configured || passed.SCM.CanCreateDraftMR || passed.SCM.Reason != app.UnconfiguredSCMReason {
		t.Fatalf("passing verification view = %#v", passed.Verification)
	}
	patchRequest := newLocalRequest(http.MethodGet, "/api/runs/"+started.ID+"/patch", nil)
	patchResponse := httptest.NewRecorder()
	handler.ServeHTTP(patchResponse, patchRequest)
	if patchResponse.Code != http.StatusOK || patchResponse.Header().Get("X-Chora-Patch-SHA256") != g2M3AuthoritativePatchSHA256 || fmt.Sprintf("%x", sha256.Sum256(patchResponse.Body.Bytes())) != g2M3AuthoritativePatchSHA256 {
		t.Fatalf("raw Patch response status=%d headers=%v digest=%x", patchResponse.Code, patchResponse.Header(), sha256.Sum256(patchResponse.Body.Bytes()))
	}
	var accepted runView
	requestJSONWithHeaders(t, handler, http.MethodPost, "/api/runs/"+started.ID+"/review", map[string]any{
		"expectedVersion": passed.Version, "kind": "accept", "note": "The frozen Patch and trusted evidence satisfy the contract.",
	}, map[string]string{"Idempotency-Key": "accept-authoritative-patch"}, http.StatusOK, &accepted)
	if accepted.Status != "accepted" || accepted.VerifiedReview == nil || accepted.VerifiedReview.Kind != "accept" || accepted.VerifiedReview.ActorID != localActor || accepted.VerifiedReview.SessionID != localSession ||
		accepted.ReviewablePatch == nil || accepted.ReviewablePatch.PatchDigest != g2M3AuthoritativePatchSHA256 || !accepted.Controls.CanApplyPatch || accepted.Controls.CanCreateDraftMR || accepted.SCM.Reason != app.UnconfiguredSCMReason || len(accepted.Candidates) != 0 {
		t.Fatalf("accepted verified Review = %#v", accepted)
	}
	requestJSONWithHeaders(t, handler, http.MethodPost, "/api/runs/"+started.ID+"/review", map[string]any{
		"expectedVersion": passed.Version, "kind": "accept", "note": "The frozen Patch and trusted evidence satisfy the contract.",
	}, map[string]string{"Idempotency-Key": "accept-authoritative-patch"}, http.StatusOK, nil)
	requestJSONWithHeaders(t, handler, http.MethodPost, "/api/runs/"+started.ID+"/review", map[string]any{
		"expectedVersion": passed.Version, "kind": "reject", "note": "stale conflicting decision", "rejectionClass": "implementation_gap",
	}, map[string]string{"Idempotency-Key": "stale-conflicting-review"}, http.StatusConflict, nil)
	indexBefore := runGitTargetTest(t, applyRoot, "diff", "--cached", "--binary", "--no-ext-diff", "--")
	var applied runView
	requestJSON(t, handler, http.MethodPost, "/api/runs/"+started.ID+"/apply", map[string]any{
		"expectedVersion": accepted.Version,
	}, http.StatusOK, &applied)
	if applied.PatchApplication == nil || applied.PatchApplication.State != "applied" || applied.Controls.CanApplyPatch {
		t.Fatalf("applied Patch view = %#v", applied)
	}
	taskID, err := domain.ParseTaskID(materialized.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	appliedTask, err := server.store.Reader().GetTask(context.Background(), taskID)
	if err != nil || appliedTask.State() != domain.TaskStateClosed {
		t.Fatalf("applied Task = %#v err=%v", appliedTask, err)
	}
	if indexAfter := runGitTargetTest(t, applyRoot, "diff", "--cached", "--binary", "--no-ext-diff", "--"); indexAfter != indexBefore {
		t.Fatalf("Patch Apply changed Git index\nbefore=%q\nafter=%q", indexBefore, indexAfter)
	}
	if diff := runGitTargetTest(t, applyRoot, "diff", "--", "internal/domain/task.go", "internal/domain/task_test.go"); diff == "" {
		t.Fatal("Patch Apply did not produce a working-tree diff")
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := newTestServer(context.Background(), filepath.Join(root, "chora.db"), webRoot, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var afterRestart runView
	requestJSON(t, reopened.Handler(), http.MethodGet, "/api/runs/"+started.ID, nil, http.StatusOK, &afterRestart)
	if afterRestart.Status != "accepted" || afterRestart.VerifiedReview == nil || afterRestart.VerifiedReview.ID != accepted.VerifiedReview.ID || afterRestart.ReviewablePatch == nil ||
		afterRestart.ReviewablePatch.PatchDigest != accepted.ReviewablePatch.PatchDigest || afterRestart.PatchApplication == nil || afterRestart.PatchApplication.State != "applied" ||
		afterRestart.SCM.Reason != app.UnconfiguredSCMReason {
		t.Fatalf("restart/reopen verified Review = %#v", afterRestart)
	}
}

func TestE2EVerifierStartupCandidateRecoveryDoesNotUseAmbientDocker(t *testing.T) {
	t.Setenv(verifierPolicyModeEnvironment, "full")
	root := t.TempDir()
	commandBin := filepath.Join(root, "ambient-command-bin")
	if err := os.Mkdir(commandBin, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"docker", "colima"} {
		if err := os.WriteFile(filepath.Join(commandBin, name), []byte("#!/bin/sh\nexit 97\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", commandBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := &e2eVerifierSandboxFactory{}
	service, _, err := newProductVerifier(
		withVerifierSandboxLifecycle(context.Background(), lifecycle),
		repoRoot, filepath.Join(root, "runtime"), false, ProductOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if service.sandboxLifecycle != lifecycle {
		t.Fatalf("E2E lifecycle was replaced by %T", service.sandboxLifecycle)
	}
	listed := false
	count, err := recoverInstalledVerifierStartup(context.Background(), service, func(context.Context) ([]storecontract.VerificationStartupCandidate, error) {
		listed = true
		return make([]storecontract.VerificationStartupCandidate, 1), nil
	})
	if err != nil || count != 1 || !listed {
		t.Fatalf("startup recovery count=%d listed=%t err=%v", count, listed, err)
	}
}

func TestVerifiedRejectAndAgentRetryPreservePredecessorAndReverifySuccessor(t *testing.T) {
	t.Setenv(verifierPolicyModeEnvironment, "full")
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	webRoot, err := filepath.Abs(filepath.Join("..", "..", "web", "dist"))
	if err != nil {
		t.Fatal(err)
	}
	server, err := newTestServer(context.Background(), filepath.Join(root, "chora.db"), webRoot, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	server.verifier.core = passingVerifierCore{}
	enablePiTestRuntime(t, server)
	handler := server.Handler()
	v4 := readSpecCodingJSON(t, "chora-m1-real-task.v4.json")
	baselineRoot, err := filepath.Abs(filepath.Join("..", "..", "..", ".agent", "evidence", "G2-M3", "source-baseline-v4", "repo"))
	if err != nil {
		t.Fatal(err)
	}
	var materialized struct {
		TaskID   string `json:"taskId"`
		Snapshot struct {
			ID     string `json:"id"`
			Digest string `json:"digest"`
		} `json:"snapshot"`
	}
	requestJSON(t, handler, http.MethodPost, "/api/spec-coding/materializations", map[string]any{
		"contract": json.RawMessage(v4), "workspaceRoot": baselineRoot,
		"contextEntryId": "context_entry_018f0c4a-1a30-7c3d-8e4f-1234567890ab", "contextRevisionId": "context_revision_018f0c4a-1a31-7c3d-8e4f-1234567890ab",
		"charterId": "charter_018f0c4a-1a32-7c3d-8e4f-1234567890ab", "frozenAt": "2026-08-11T00:00:00Z",
	}, http.StatusCreated, &materialized)
	v8 := versionSpecCodingContractV8(t, v4, materialized.Snapshot.Digest)
	requestJSON(t, handler, http.MethodPost, "/api/spec-coding/contracts", map[string]any{"contract": json.RawMessage(v8)}, http.StatusOK, nil)
	acceptTaskPlan(t, handler, materialized.TaskID)
	var started runView
	requestJSON(t, handler, http.MethodPost, "/api/tasks/"+materialized.TaskID+"/runs", map[string]any{"patchFixture": "g2-m3-authoritative"}, http.StatusCreated, &started)
	// Verification auto-starts on the first Agent completion.
	first := waitForRunState(t, handler, started.ID, "awaiting_review")
	if first.ReviewablePatch == nil || !first.Controls.CanReview {
		t.Fatalf("first review gate = %#v", first)
	}
	var rejected runView
	requestJSONWithHeaders(t, handler, http.MethodPost, "/api/runs/"+started.ID+"/review", map[string]any{
		"expectedVersion": first.Version, "kind": "reject", "note": "The implementation needs a bounded correction inside the frozen contract.", "rejectionClass": "implementation_gap",
	}, map[string]string{"Idempotency-Key": "reject-for-agent-retry"}, http.StatusOK, &rejected)
	if rejected.Status != "revision_required" || rejected.VerifiedReview == nil || rejected.VerifiedReview.RejectionClass != "implementation_gap" || !rejected.Controls.CanRetry {
		t.Fatalf("rejected view = %#v", rejected)
	}
	requestJSONWithHeaders(t, handler, http.MethodPost, "/api/runs/"+started.ID+"/retry", map[string]any{
		"expectedVersion": rejected.Version,
		"instructions":    "Repair only the declared implementation gap and keep every frozen binding unchanged.",
	}, map[string]string{"Idempotency-Key": "explicit-agent-retry"}, http.StatusOK, nil)
	// Verification auto-starts on the successor Agent completion too.
	second := waitForRunState(t, handler, started.ID, "awaiting_review")
	if second.Attempt != 2 || second.Verification == nil || second.Verification.Result == nil || second.Verification.Result.Outcome != "review_ready" ||
		second.Verification.AgentAttemptID != second.AttemptDetail.ID || len(second.VerificationHistory) != 2 || second.VerificationHistory[0].Result == nil ||
		second.VerificationHistory[0].Result.ID != first.Verification.Result.ID || second.VerifiedReviewHistory[0].ResultID != first.Verification.Result.ID || second.ReviewablePatch == nil ||
		second.Snapshot == nil || first.Snapshot == nil || second.Snapshot.ID != first.Snapshot.ID || second.Snapshot.Digest != first.Snapshot.Digest ||
		len(second.AttemptHistory) != 2 || len(second.VerifiedReviewHistory) != 1 {
		t.Fatalf("successor review lineage = %#v", second)
	}
	if second.Version <= first.Version {
		t.Fatalf("Retry did not advance Run version: first=%d second=%d", first.Version, second.Version)
	}
}

func versionSpecCodingContractV8(t *testing.T, v4 []byte, snapshotDigest string) []byte {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(v4, &document); err != nil {
		t.Fatal(err)
	}
	document["schema_version"] = "chora.spec-coding-core.v8"
	document["revision"] = float64(8)
	document["execution"].(map[string]any)["input"].(map[string]any)["context_snapshot_digest"] = snapshotDigest
	acceptance := document["acceptance"].(map[string]any)
	acceptance["verification_commands"] = []map[string]any{{"id": "domain-tests", "argv": []string{"go", "test", "./internal/domain"}}}
	for _, raw := range acceptance["criteria"].([]any) {
		raw.(map[string]any)["verification_command_ids"] = []string{"domain-tests"}
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
