//go:build isolated_acceptance

package localweb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
)

func TestIsolatedLocalRealWorkbenchTwoRepositoryClosure(t *testing.T) {
	dataRoot := strings.TrimSpace(os.Getenv("CHORA_ISOLATED_ACCEPTANCE_DATA"))
	if dataRoot == "" {
		t.Fatal("CHORA_ISOLATED_ACCEPTANCE_DATA must name an already prepared Isolated Local data root")
	}
	dataRoot, err := filepath.Abs(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	sourceRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	acceptanceRoot, err := os.MkdirTemp(dataRoot, "acceptance-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("preserved failed acceptance data: %s", acceptanceRoot)
			return
		}
		_ = os.RemoveAll(acceptanceRoot)
	})
	server, err := NewWorkbench(ctx, filepath.Join(acceptanceRoot, "acceptance.db"), filepath.Join(sourceRoot, "web", "dist"), log.New(io.Discard, "", 0), WorkbenchOptions{SourceRoot: sourceRoot, DataRoot: dataRoot})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	handler := server.Handler()
	var readiness isolatedLocalView
	requestJSON(t, handler, http.MethodGet, "/api/isolated-local", nil, http.StatusOK, &readiness)
	if readiness.State != "ready" || readiness.ImageID == "" {
		t.Fatalf("prepared Isolated Local is not ready: %#v", readiness)
	}

	writeCheckout, writeHead, writeContent := isolatedAcceptanceRepository(t, "write", map[string]string{
		"package.json":  `{"scripts":{"test":"node --test"},"type":"module"}` + "\n",
		"value.js":      "export const value = 'before'\n",
		"value.test.js": "import test from 'node:test'\nimport assert from 'node:assert/strict'\nimport { value } from './value.js'\ntest('value', () => assert.equal(value, 'after reference'))\n",
	})
	referenceCheckout, referenceHead, referenceContent := isolatedAcceptanceRepository(t, "reference", map[string]string{
		"REFERENCE.md": "Use the exact text: after reference\n",
	})

	var project resourceProjectTestView
	requestJSON(t, handler, http.MethodPost, "/api/v2/projects", map[string]any{"name": "Isolated acceptance"}, http.StatusCreated, &project)
	selections := make([]taskResourceSelection, 0, 2)
	for index, fixture := range []struct{ root, role string }{{writeCheckout, "write"}, {referenceCheckout, "reference"}} {
		var added struct {
			Repository repositoryResourceView `json:"repository"`
		}
		requestJSON(t, handler, http.MethodPost, "/api/v2/projects/"+project.ID+"/repositories", map[string]any{"locator": fixture.root}, http.StatusOK, &added)
		selections = append(selections, taskResourceSelection{RepoID: added.Repository.RepoID, AssociationVersion: added.Repository.Version, Role: fixture.role,
			TargetRef: isolatedAcceptanceGit(t, fixture.root, "symbolic-ref", "HEAD"), Scope: domain.TaskRepositoryScope{Mode: "repository", MigrationChoice: "not_needed"},
			Checks: domain.TaskCheckPolicy{Mode: "auto", SelectionSource: "user"}})
		if index == 1 && fixture.role != "reference" {
			t.Fatal("second repository must remain reference-only")
		}
	}
	roomID, err := domain.ParseRoomID(project.DefaultRoomID)
	if err != nil {
		t.Fatal(err)
	}
	revisions, err := server.store.Reader().ListRoomRevisions(ctx, roomID)
	if err != nil {
		t.Fatal(err)
	}
	revisionIDs := make([]string, 0, len(revisions))
	for _, revision := range revisions {
		revisionIDs = append(revisionIDs, revision.ID().String())
	}
	var task taskRefView
	requestJSONWithHeaders(t, handler, http.MethodPost, "/api/v2/rooms/"+project.DefaultRoomID+"/tasks", map[string]any{
		"title": "Use reference value", "requirement": "Read REFERENCE.md from the reference repository. Change value.js in the write repository so node --test passes. Do not modify the reference repository.",
		"agentExecutionProfile": string(domain.AgentExecutionProfileIsolatedLocal), "resources": selections, "revisionIds": revisionIDs,
	}, map[string]string{"Idempotency-Key": "isolated-acceptance-task"}, http.StatusCreated, &task)
	if task.AgentExecutionProfile != string(domain.AgentExecutionProfileIsolatedLocal) || task.ResourceSnapshot == nil || len(task.ResourceSnapshot.Resources) != 2 {
		t.Fatalf("isolated Task authority = %#v", task)
	}
	acceptTaskPlan(t, handler, task.ID)
	var started runView
	requestJSONWithHeaders(t, handler, http.MethodPost, "/api/tasks/"+task.ID+"/runs", map[string]any{}, map[string]string{"Idempotency-Key": "isolated-acceptance-run"}, http.StatusCreated, &started)
	// Cancel a real admitted attempt before accepting any output, then reopen the
	// same Workbench database and resume under its original frozen authority.
	if started.AttemptDetail == nil || started.AttemptDetail.Runtime == nil {
		t.Fatal("real isolated attempt was not admitted")
	}
	firstAttempt := started.AttemptDetail.ID
	var cancelled runView
	requestJSON(t, handler, http.MethodPost, "/api/runs/"+started.ID+"/cancel", map[string]any{"reason": "Verify isolated cancellation and restart recovery."}, http.StatusOK, &cancelled)
	if cancelled.Status != domain.RunStateCancelled || !cancelled.Controls.CanRetry {
		t.Fatalf("isolated cancellation did not stop the attempt: %#v", cancelled)
	}
	isolatedAcceptanceAssertFiles(t, writeCheckout, writeContent)
	isolatedAcceptanceAssertFiles(t, referenceCheckout, referenceContent)
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	server, err = NewWorkbench(ctx, filepath.Join(acceptanceRoot, "acceptance.db"), filepath.Join(sourceRoot, "web", "dist"), log.New(io.Discard, "", 0), WorkbenchOptions{SourceRoot: sourceRoot, DataRoot: dataRoot})
	if err != nil {
		t.Fatal(err)
	}
	handler = server.Handler()
	var restored runView
	requestJSON(t, handler, http.MethodGet, "/api/runs/"+started.ID, nil, http.StatusOK, &restored)
	var retrying runView
	requestJSONWithHeaders(t, handler, http.MethodPost, "/api/runs/"+started.ID+"/retry", map[string]any{"expectedVersion": restored.Version, "instructions": "Continue the original task in a fresh isolated attempt. Read the reference and make node --test pass."}, map[string]string{"Idempotency-Key": "isolated-acceptance-resume"}, http.StatusOK, &retrying)
	if retrying.Attempt != 2 || retrying.AttemptDetail == nil || retrying.AttemptDetail.ID == firstAttempt || retrying.AgentExecution == nil || retrying.AgentExecution.Profile != domain.AgentExecutionProfileIsolatedLocal {
		t.Fatalf("restart resume did not create a fresh isolated attempt: %#v", retrying)
	}
	finished := waitForIsolatedAcceptanceRun(t, handler, started.ID)
	if finished.Status != domain.RunStateAwaitingReview || finished.AgentExecution == nil || finished.AgentExecution.Profile != domain.AgentExecutionProfileIsolatedLocal {
		t.Fatalf("isolated Run did not reach Review: status=%s execution=%#v blockers=%v", finished.Status, finished.AgentExecution, finished.Blockers)
	}
	var result resourceResultView
	requestJSON(t, handler, http.MethodGet, "/api/v2/runs/"+started.ID+"/result", nil, http.StatusOK, &result)
	firstResultDigest := result.Digest
	var rejected runView
	requestJSONWithHeaders(t, handler, http.MethodPost, "/api/v2/runs/"+started.ID+"/review", map[string]any{"expectedVersion": finished.Version, "kind": "reject", "comment": "Keep value.js correct. Add isolated-result.txt containing exactly reviewed in isolation followed by a newline, then rerun node --test.", "resultDigest": result.Digest}, map[string]string{"Idempotency-Key": "isolated-acceptance-request-repair"}, http.StatusOK, &rejected)
	requestJSONWithHeaders(t, handler, http.MethodPost, "/api/runs/"+started.ID+"/retry", map[string]any{"expectedVersion": rejected.Version, "instructions": "Apply the Human Review correction without losing the existing value.js change. Rerun node --test on final contents."}, map[string]string{"Idempotency-Key": "isolated-acceptance-repair"}, http.StatusOK, &retrying)
	finished = waitForIsolatedAcceptanceRun(t, handler, started.ID)
	requestJSON(t, handler, http.MethodGet, "/api/v2/runs/"+started.ID+"/result", nil, http.StatusOK, &result)
	if finished.Attempt != 3 || result.Digest == firstResultDigest {
		t.Fatal("Human Review did not produce a distinct successor result")
	}
	if result.Digest == "" || len(result.Group.Repositories) != 2 || len(result.Repositories) != 2 {
		t.Fatalf("resource Result evidence = %#v", result)
	}
	for _, repository := range result.Group.Repositories {
		if repository.PatchDigest == "" || len(repository.PatchDigest) != sha256.Size*2 {
			t.Fatalf("invalid repository Patch digest: %#v", repository)
		}
		var checks app.ResourceCheckDerivation
		if err := json.Unmarshal(repository.Checks, &checks); err != nil {
			t.Fatal(err)
		}
		if repository.RepoID == selections[0].RepoID {
			if !slices.Contains(repository.ChangedPaths, "value.js") || checks.Status != execution.CheckPass || !checks.FinalContentVerified {
				t.Fatalf("write repository paths=%v check status=%s finalContentVerified=%t", repository.ChangedPaths, checks.Status, checks.FinalContentVerified)
			}
		} else if len(repository.ChangedPaths) != 0 {
			t.Fatalf("reference repository was changed: %#v", repository)
		}
	}
	if isolatedAcceptanceGit(t, writeCheckout, "rev-parse", "HEAD") != writeHead || isolatedAcceptanceGit(t, referenceCheckout, "rev-parse", "HEAD") != referenceHead {
		t.Fatal("original checkout HEAD changed")
	}
	isolatedAcceptanceAssertFiles(t, writeCheckout, writeContent)
	isolatedAcceptanceAssertFiles(t, referenceCheckout, referenceContent)
	writeRoot := ""
	for _, repository := range result.Repositories {
		if repository.RepoID == selections[0].RepoID {
			writeRoot = repository.WorktreePath
		}
	}
	if writeRoot == "" {
		t.Fatal("write Task worktree path is unavailable")
	}
	changed, err := os.ReadFile(filepath.Join(writeRoot, "value.js"))
	if err != nil || !strings.Contains(string(changed), "after reference") {
		t.Fatalf("Task worktree content = %q err=%v", changed, err)
	}

	newFile, err := os.ReadFile(filepath.Join(writeRoot, "isolated-result.txt"))
	if err != nil || string(newFile) != "reviewed in isolation\n" {
		t.Fatalf("Human Review new-file correction was not imported: %q %v", newFile, err)
	}

	prefix := "/api/v2/runs/" + started.ID
	var accepted runView
	requestJSONWithHeaders(t, handler, http.MethodPost, prefix+"/review", map[string]any{"expectedVersion": finished.Version, "kind": "accept", "comment": "Isolated result and automatic check evidence accepted.", "resultDigest": result.Digest}, map[string]string{"Idempotency-Key": "isolated-acceptance-review"}, http.StatusOK, &accepted)
	writeResource := task.ResourceSnapshot.Resources[0]
	commitBody := map[string]any{"repoId": writeResource.RepoID, "expectedVersion": accepted.Version, "resultDigest": result.Digest, "kind": "commit", "message": "test: verify isolated acceptance"}
	var commitPreview app.DeliveryOperationView
	requestJSONWithHeaders(t, handler, http.MethodPost, prefix+"/delivery/preview", commitBody, map[string]string{"Idempotency-Key": "isolated-acceptance-commit-preview"}, http.StatusOK, &commitPreview)
	var committed app.DeliveryOperationView
	requestJSONWithHeaders(t, handler, http.MethodPost, prefix+"/delivery/confirm", map[string]any{"operationId": commitPreview.ID, "expectedVersion": accepted.Version, "resultDigest": result.Digest}, map[string]string{"Idempotency-Key": "isolated-acceptance-commit-confirm"}, http.StatusOK, &committed)
	if committed.Status != "succeeded" || committed.Commit == "" {
		t.Fatalf("isolated delivery commit = %#v", committed)
	}
	pushBody := map[string]any{"repoId": writeResource.RepoID, "expectedVersion": accepted.Version, "resultDigest": result.Digest, "kind": "push", "remote": "origin"}
	var pushPreview app.DeliveryOperationView
	requestJSONWithHeaders(t, handler, http.MethodPost, prefix+"/delivery/preview", pushBody, map[string]string{"Idempotency-Key": "isolated-acceptance-push-preview"}, http.StatusOK, &pushPreview)
	var pushed app.DeliveryOperationView
	requestJSONWithHeaders(t, handler, http.MethodPost, prefix+"/delivery/confirm", map[string]any{"operationId": pushPreview.ID, "expectedVersion": accepted.Version, "resultDigest": result.Digest}, map[string]string{"Idempotency-Key": "isolated-acceptance-push-confirm"}, http.StatusOK, &pushed)
	remoteHead := strings.Fields(isolatedAcceptanceGit(t, writeCheckout, "ls-remote", "origin", "refs/heads/"+writeResource.TaskBranch))
	if pushed.Status != "succeeded" || len(remoteHead) != 2 || remoteHead[0] != committed.Commit {
		t.Fatalf("isolated delivery push = %#v remote=%v", pushed, remoteHead)
	}
	if os.Getenv("CHORA_ISOLATED_ACCEPTANCE_GITHUB_REPO") != "" {
		for _, kind := range []string{"pr", "merge", "cleanup"} {
			var preview, confirmed app.DeliveryOperationView
			body := map[string]any{"repoId": writeResource.RepoID, "expectedVersion": accepted.Version, "resultDigest": result.Digest, "kind": kind, "remote": "origin", "title": "test: verify isolated local delivery", "body": "Disposable Workbench Isolated Local acceptance fixture. Exercises real Pi/DeepSeek execution, human repair, and reviewed Git delivery."}
			requestJSONWithHeaders(t, handler, http.MethodPost, prefix+"/delivery/preview", body, map[string]string{"Idempotency-Key": "isolated-acceptance-" + kind + "-preview"}, http.StatusOK, &preview)
			requestJSONWithHeaders(t, handler, http.MethodPost, prefix+"/delivery/confirm", map[string]any{"operationId": preview.ID, "expectedVersion": accepted.Version, "resultDigest": result.Digest}, map[string]string{"Idempotency-Key": "isolated-acceptance-" + kind + "-confirm"}, http.StatusOK, &confirmed)
			if confirmed.Status != "succeeded" {
				t.Fatalf("real GitHub %s failed: %#v", kind, confirmed)
			}
			t.Logf("real GitHub %s: PR=%s state=%s commit=%s", kind, confirmed.PRURL, confirmed.PRState, confirmed.Commit)
		}
	} else {
		var closure app.ResultClosureView
		requestJSONWithHeaders(t, handler, http.MethodPost, prefix+"/closure/preview", map[string]any{"expectedVersion": accepted.Version}, map[string]string{"Idempotency-Key": "isolated-acceptance-close-preview"}, http.StatusOK, &closure)
		requestJSONWithHeaders(t, handler, http.MethodPost, prefix+"/closure/confirm", map[string]any{"expectedVersion": accepted.Version, "resultDigest": closure.ResultDigest, "previewDigest": closure.PreviewDigest}, map[string]string{"Idempotency-Key": "isolated-acceptance-close-confirm"}, http.StatusOK, &closure)
		cleanupBody := map[string]any{"expectedVersion": accepted.Version, "resultDigest": closure.ResultDigest, "repoId": writeResource.RepoID}
		var cleanupPreview app.DeliveryOperationView
		requestJSONWithHeaders(t, handler, http.MethodPost, prefix+"/closure/cleanup/preview", cleanupBody, map[string]string{"Idempotency-Key": "isolated-acceptance-cleanup-preview"}, http.StatusOK, &cleanupPreview)
		cleanupBody["operationId"] = cleanupPreview.ID
		var cleaned app.DeliveryOperationView
		requestJSONWithHeaders(t, handler, http.MethodPost, prefix+"/closure/cleanup/confirm", cleanupBody, map[string]string{"Idempotency-Key": "isolated-acceptance-cleanup-confirm"}, http.StatusOK, &cleaned)
		if cleaned.Status != "succeeded" {
			t.Fatalf("isolated delivery cleanup = %#v", cleaned)
		}
	}
	if _, err := os.Stat(writeRoot); !os.IsNotExist(err) {
		t.Fatalf("Task worktree was not cleaned up: %v", err)
	}
	t.Logf("isolated acceptance evidence: image=%s run=%s result=%s patch=%s commit=%s", readiness.ImageID, started.ID, result.Digest, result.Group.Repositories[0].PatchDigest, committed.Commit)
}

func waitForIsolatedAcceptanceRun(t *testing.T, handler http.Handler, runID string) runView {
	t.Helper()
	deadline := time.Now().Add(12 * time.Minute)
	var last runView
	for time.Now().Before(deadline) {
		requestJSON(t, handler, http.MethodGet, "/api/runs/"+runID, nil, http.StatusOK, &last)
		if last.Status == domain.RunStateAwaitingReview {
			return last
		}
		if last.Status == domain.RunStateRecoveryRequired || last.Status == domain.RunStateCancelled {
			t.Fatalf("isolated Run terminated: status=%s reason=%s blockers=%v", last.Status, last.TerminalReason, last.Blockers)
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("isolated Run timed out: status=%s blockers=%v", last.Status, last.Blockers)
	return runView{}
}

func isolatedAcceptanceRepository(t *testing.T, name string, files map[string]string) (string, string, map[string]string) {
	t.Helper()
	root := t.TempDir()
	bare := filepath.Join(root, name+".git")
	checkout := filepath.Join(root, name)
	isolatedAcceptanceGit(t, root, "init", "--bare", bare)
	isolatedAcceptanceGit(t, root, "clone", bare, checkout)
	for path, content := range files {
		if err := os.WriteFile(filepath.Join(checkout, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	isolatedAcceptanceGit(t, checkout, "add", ".")
	isolatedAcceptanceGit(t, checkout, "-c", "user.name=Chora Acceptance", "-c", "user.email=acceptance@example.invalid", "commit", "-m", "fixture")
	isolatedAcceptanceGit(t, checkout, "push", "origin", "HEAD")
	if remote := os.Getenv("CHORA_ISOLATED_ACCEPTANCE_GITHUB_REPO"); name == "write" && remote != "" {
		if !regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`).MatchString(remote) {
			t.Fatal("CHORA_ISOLATED_ACCEPTANCE_GITHUB_REPO must be owner/repository for an owned disposable GitHub repository")
		}
		branch := "chora-isolated-fixture-" + time.Now().UTC().Format("20060102-150405.000000000")
		isolatedAcceptanceGit(t, checkout, "branch", "-m", branch)
		isolatedAcceptanceGit(t, checkout, "remote", "set-url", "origin", "https://github.com/"+remote+".git")
		isolatedAcceptanceGit(t, checkout, "push", "origin", "HEAD:refs/heads/"+branch)
		t.Logf("real GitHub fixture: repository=%s base=%s (retained after acceptance)", remote, branch)
	}
	isolatedAcceptanceGit(t, checkout, "config", "user.name", "Chora Acceptance")
	isolatedAcceptanceGit(t, checkout, "config", "user.email", "acceptance@example.invalid")
	return checkout, isolatedAcceptanceGit(t, checkout, "rev-parse", "HEAD"), files
}

func isolatedAcceptanceGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "git", args...)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func isolatedAcceptanceAssertFiles(t *testing.T, root string, expected map[string]string) {
	t.Helper()
	for path, want := range expected {
		body, err := os.ReadFile(filepath.Join(root, path))
		digest := sha256.Sum256(body)
		if err != nil || string(body) != want {
			t.Fatalf("source checkout %s changed: sha256=%s err=%v", path, hex.EncodeToString(digest[:]), err)
		}
	}
}
