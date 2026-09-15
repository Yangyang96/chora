//go:build isolated_acceptance

package localweb

import (
	"context"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
)

func TestIsolatedLocalRealModelSelectionAndRetry(t *testing.T) {
	dataRoot := strings.TrimSpace(os.Getenv("CHORA_ISOLATED_ACCEPTANCE_DATA"))
	if dataRoot == "" {
		t.Fatal("CHORA_ISOLATED_ACCEPTANCE_DATA must name a prepared Isolated Local data root")
	}
	dataRoot, err := filepath.Abs(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	sourceRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	acceptanceRoot, err := os.MkdirTemp(dataRoot, "model-acceptance-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("preserved acceptance data: %s", acceptanceRoot)
			return
		}
		_ = os.RemoveAll(acceptanceRoot)
	})
	open := func() *Server {
		s, err := NewWorkbench(ctx, filepath.Join(acceptanceRoot, "acceptance.db"), filepath.Join(sourceRoot, "web", "dist"), log.New(io.Discard, "", 0), WorkbenchOptions{SourceRoot: sourceRoot, DataRoot: dataRoot})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	server := open()
	t.Cleanup(func() { _ = server.Close() })
	handler := server.Handler()
	var readiness isolatedLocalView
	requestJSON(t, handler, http.MethodGet, "/api/isolated-local", nil, http.StatusOK, &readiness)
	if readiness.State != "ready" {
		t.Fatalf("Isolated Local state = %s", readiness.State)
	}
	var catalog domain.ModelCatalog
	requestJSON(t, handler, http.MethodGet, "/api/models?agentExecutionProfile=isolated_local", nil, http.StatusOK, &catalog)
	if err := catalog.Validate(); err != nil {
		t.Fatal(err)
	}
	binding := func(modelID string) domain.ModelBinding {
		selected, err := domain.NewModelBinding(catalog, domain.ModelIdentity{Provider: "deepseek", ModelID: modelID}, time.Now())
		if err != nil {
			t.Fatalf("native isolated catalog lacks required model %s: %v", modelID, err)
		}
		return selected
	}
	firstBinding, secondBinding := binding("deepseek-v4-flash"), binding("deepseek-v4-pro")
	checkout, initialHead, initialFiles := isolatedAcceptanceRepository(t, "model-selection", map[string]string{
		"README.md": "before\n",
		"Makefile":  "test:\n\tgrep -q -- '-verified' README.md\n",
	})
	var project resourceProjectTestView
	requestJSON(t, handler, http.MethodPost, "/api/v2/projects", map[string]any{"name": "Isolated model selection"}, http.StatusCreated, &project)
	var added struct {
		Repository repositoryResourceView `json:"repository"`
	}
	requestJSON(t, handler, http.MethodPost, "/api/v2/projects/"+project.ID+"/repositories", map[string]any{"locator": checkout}, http.StatusOK, &added)
	selection := taskResourceSelection{RepoID: added.Repository.RepoID, AssociationVersion: added.Repository.Version, Role: "write", TargetRef: isolatedAcceptanceGit(t, checkout, "symbolic-ref", "HEAD"), Scope: domain.TaskRepositoryScope{Mode: "repository", MigrationChoice: "not_needed"}, Checks: domain.TaskCheckPolicy{Mode: "named", SelectionSource: "user", Commands: []domain.TaskCheckCommand{{ID: "readme", Name: "README marker", Command: "grep -q -- '-verified' README.md", Argv: []string{"grep", "-q", "--", "-verified", "README.md"}, Source: "user"}}}}
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
		"title": "Verify selected model", "requirement": "Change README.md to exactly flash-verified followed by a newline. Leave Makefile unchanged. Check with grep -q -- '-verified' README.md. Do not access any remote repository or install anything.",
		"agentExecutionProfile": string(domain.AgentExecutionProfileIsolatedLocal), "modelBinding": firstBinding, "resources": []taskResourceSelection{selection}, "revisionIds": revisionIDs,
	}, map[string]string{"Idempotency-Key": "isolated-model-task"}, http.StatusCreated, &task)
	if task.ModelBinding.JSON() != firstBinding.JSON() {
		t.Fatal("Task lost selected model binding")
	}
	acceptTaskPlan(t, handler, task.ID)
	var started runView
	requestJSONWithHeaders(t, handler, http.MethodPost, "/api/tasks/"+task.ID+"/runs", map[string]any{}, map[string]string{"Idempotency-Key": "isolated-model-run"}, http.StatusCreated, &started)
	first := waitForIsolatedModelAcceptanceRun(t, handler, started.ID)
	assertIsolatedModelAcceptanceIdentity(t, first, firstBinding)
	firstAttempt := first.AttemptDetail.ID
	var firstResult resourceResultView
	requestJSON(t, handler, http.MethodGet, "/api/v2/runs/"+started.ID+"/result", nil, http.StatusOK, &firstResult)
	assertIsolatedModelAcceptanceContents(t, firstResult, "flash-verified\n")
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	server = open()
	handler = server.Handler()
	var restored runView
	requestJSON(t, handler, http.MethodGet, "/api/runs/"+started.ID, nil, http.StatusOK, &restored)
	assertIsolatedModelAcceptanceIdentity(t, restored, firstBinding)
	if !reflect.DeepEqual(first.AttemptHistory, restored.AttemptHistory) {
		t.Fatal("restart changed attempt history")
	}
	var rejected runView
	requestJSONWithHeaders(t, handler, http.MethodPost, "/api/v2/runs/"+started.ID+"/review", map[string]any{"expectedVersion": restored.Version, "kind": "reject", "comment": "Replace README.md with exactly pro-verified followed by a newline. Keep Makefile unchanged and rerun the README check.", "resultDigest": firstResult.Digest}, map[string]string{"Idempotency-Key": "isolated-model-reject"}, http.StatusOK, &rejected)
	var retrying runView
	requestJSONWithHeaders(t, handler, http.MethodPost, "/api/runs/"+started.ID+"/retry", map[string]any{"expectedVersion": rejected.Version, "instructions": "Apply the Human Review correction to README.md and verify the final contents with grep. Do not access remote repositories or install anything.", "modelBinding": secondBinding}, map[string]string{"Idempotency-Key": "isolated-model-retry"}, http.StatusOK, &retrying)
	second := waitForIsolatedModelAcceptanceRun(t, handler, started.ID)
	assertIsolatedModelAcceptanceIdentity(t, second, secondBinding)
	if second.Attempt != 2 || second.AttemptDetail.ID == firstAttempt {
		t.Fatal("retry did not create exactly one successor attempt")
	}
	found := false
	for _, attempt := range second.AttemptHistory {
		if attempt.ID == firstAttempt {
			found = true
			if attempt.ModelBinding.JSON() != firstBinding.JSON() || !reflect.DeepEqual(attempt.ModelProvenance, first.AttemptDetail.ModelProvenance) {
				t.Fatal("retry changed predecessor model history")
			}
		}
	}
	if !found {
		t.Fatal("predecessor history disappeared")
	}
	var secondResult resourceResultView
	requestJSON(t, handler, http.MethodGet, "/api/v2/runs/"+started.ID+"/result", nil, http.StatusOK, &secondResult)
	assertIsolatedModelAcceptanceContents(t, secondResult, "pro-verified\n")
	if secondResult.Digest == firstResult.Digest {
		t.Fatal("correction did not produce a distinct result")
	}
	isolatedAcceptanceAssertFiles(t, checkout, initialFiles)
	if isolatedAcceptanceGit(t, checkout, "rev-parse", "HEAD") != initialHead || isolatedAcceptanceGit(t, checkout, "status", "--porcelain") != "" {
		t.Fatal("original checkout changed")
	}
	t.Logf("isolated model selection verified: run=%s attempts=%d first=%s second=%s", started.ID, second.Attempt, firstBinding.Record().ModelID, secondBinding.Record().ModelID)
}

func assertIsolatedModelAcceptanceIdentity(t *testing.T, run runView, binding domain.ModelBinding) {
	t.Helper()
	if run.Status != domain.RunStateAwaitingReview || run.AgentExecution == nil || run.AgentExecution.Profile != domain.AgentExecutionProfileIsolatedLocal || run.AttemptDetail == nil {
		t.Fatal("attempt did not complete under Isolated Local")
	}
	if run.AttemptDetail.ModelBinding.JSON() != binding.JSON() {
		t.Fatal("attempt requested model differs from selection")
	}
	observed := run.AttemptDetail.ModelProvenance
	if len(observed.Identities) != 1 || observed.Identities[0].Provider != binding.Record().Provider || observed.Identities[0].ModelID != binding.Record().ModelID {
		t.Fatalf("observed model differs from selection: %#v", observed)
	}
}

func assertIsolatedModelAcceptanceContents(t *testing.T, result resourceResultView, want string) {
	t.Helper()
	if len(result.Repositories) != 1 {
		t.Fatal("unexpected result repository count")
	}
	body, err := os.ReadFile(filepath.Join(result.Repositories[0].WorktreePath, "README.md"))
	if err != nil || string(body) != want {
		t.Fatalf("README result = %q, error = %v", body, err)
	}
}

func waitForIsolatedModelAcceptanceRun(t *testing.T, handler http.Handler, id string) runView {
	t.Helper()
	deadline := time.Now().Add(4 * time.Minute)
	for time.Now().Before(deadline) {
		var run runView
		requestJSON(t, handler, http.MethodGet, "/api/runs/"+id, nil, http.StatusOK, &run)
		if run.Status == domain.RunStateAwaitingReview {
			return run
		}
		if run.Status == domain.RunStateRecoveryRequired || run.Status == domain.RunStateCancelled {
			t.Fatalf("model acceptance terminated: status=%s reason=%s blockers=%v", run.Status, run.TerminalReason, run.Blockers)
		}
		time.Sleep(time.Second)
	}
	t.Fatal("model acceptance exceeded four minutes")
	return runView{}
}
