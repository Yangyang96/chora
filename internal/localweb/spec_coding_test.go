package localweb

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	agentpi "github.com/Yangyang96/chora/internal/agent/pi"
)

func TestSpecCodingContractMaterializationAndRegistrationUsePublicAPI(t *testing.T) {
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
	server, err := newTestServer(context.Background(), filepath.Join(root, "chora.db"), webRoot, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	enablePiTestRuntime(t, server)

	v4 := readSpecCodingJSON(t, "chora-m1-real-task.v4.json")
	workspaceRoot := filepath.Join(root, "source-baseline-v2", "repo")
	var materialized struct {
		RoomID   string `json:"roomId"`
		TaskID   string `json:"taskId"`
		Status   string `json:"status"`
		Snapshot struct {
			ID     string `json:"id"`
			Digest string `json:"digest"`
		} `json:"snapshot"`
	}
	requestJSON(t, server.Handler(), http.MethodPost, "/api/spec-coding/materializations", map[string]any{
		"contract":          json.RawMessage(v4),
		"workspaceRoot":     workspaceRoot,
		"contextEntryId":    "context_entry_018f0c4a-1a30-7c3d-8e4f-1234567890ab",
		"contextRevisionId": "context_revision_018f0c4a-1a31-7c3d-8e4f-1234567890ab",
		"charterId":         "charter_018f0c4a-1a32-7c3d-8e4f-1234567890ab",
		"frozenAt":          "2026-08-11T00:00:00Z",
	}, http.StatusCreated, &materialized)
	if materialized.TaskID != "task_018f0c4a-1a2c-7c3d-8e4f-1234567890ab" || materialized.Snapshot.ID != "context_snapshot_018f0c4a-1a2f-7c3d-8e4f-1234567890ab" || materialized.Snapshot.Digest == "80b62373d85c6d6676a935b2ba1487f8466530aa7fc5d506decf4839a27b336d" || materialized.Status != "materialized" {
		t.Fatalf("materialization = %#v", materialized)
	}
	requestJSON(t, server.Handler(), http.MethodPost, "/api/tasks/"+materialized.TaskID+"/runs", nil, http.StatusConflict, nil)

	v5 := versionSpecCodingContract(t, v4, materialized.Snapshot.Digest)
	var registered struct {
		TaskID   string `json:"taskId"`
		Status   string `json:"status"`
		Snapshot struct {
			ID     string `json:"id"`
			Digest string `json:"digest"`
		} `json:"snapshot"`
	}
	requestJSON(t, server.Handler(), http.MethodPost, "/api/spec-coding/contracts", map[string]any{
		"contract": json.RawMessage(v5),
	}, http.StatusOK, &registered)
	if registered.Status != "registered" || registered.TaskID != materialized.TaskID || registered.Snapshot.ID != materialized.Snapshot.ID || registered.Snapshot.Digest != materialized.Snapshot.Digest {
		t.Fatalf("registration = %#v", registered)
	}
	acceptTaskPlan(t, server.Handler(), materialized.TaskID)

	var task struct {
		FrozenSnapshot *struct {
			ID     string `json:"id"`
			Digest string `json:"digest"`
			Status string `json:"status"`
		} `json:"frozenSnapshot"`
	}
	requestJSON(t, server.Handler(), http.MethodGet, "/api/tasks/"+materialized.TaskID, nil, http.StatusOK, &task)
	if task.FrozenSnapshot == nil || task.FrozenSnapshot.ID != materialized.Snapshot.ID || task.FrozenSnapshot.Digest != materialized.Snapshot.Digest || task.FrozenSnapshot.Status != "registered" {
		t.Fatalf("Task frozen Snapshot = %#v", task.FrozenSnapshot)
	}

	var run runView
	requestJSON(t, server.Handler(), http.MethodPost, "/api/tasks/"+materialized.TaskID+"/runs", nil, http.StatusCreated, &run)
	if run.Snapshot == nil || run.Snapshot.ID != materialized.Snapshot.ID || run.Snapshot.Digest != materialized.Snapshot.Digest || run.Adapter != agentpi.AdapterID {
		t.Fatalf("Run did not consume registered Snapshot: %#v", run)
	}
	requestJSON(t, server.Handler(), http.MethodPost, "/api/runs/"+run.ID+"/cancel", map[string]any{"reason": "test cleanup"}, http.StatusOK, nil)
}

func readSpecCodingJSON(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "contracts", "g2-m1a", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func versionSpecCodingContract(t *testing.T, v4 []byte, digest string) []byte {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(v4, &document); err != nil {
		t.Fatal(err)
	}
	document["schema_version"] = "chora.spec-coding-core.v5"
	document["revision"] = float64(5)
	document["execution"].(map[string]any)["input"].(map[string]any)["context_snapshot_digest"] = digest
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
