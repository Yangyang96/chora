package localweb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/speccoding"
	_ "modernc.org/sqlite"
)

func TestPublicRoomAPIRealSpecCodingLifecycleAndBoundaries(t *testing.T) {
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
	server.repoRoot = repositoryRoot
	server.installedEnvelope = &envelope
	server.product = true
	server.taskWorktreesReady = true
	taskWorktrees := publicAPITaskWorktrees{repository: envelope.Repository()}
	server.service = app.NewService(app.Dependencies{
		Lifecycle: context.Background(), Store: server.store, Context: app.ContextAssembler{},
		Authorizer: localAuthorizer{}, IDs: app.RandomIDs{}, InstalledSpecCodingEnvelope: &envelope, TaskWorktrees: taskWorktrees,
	})

	var createdRoom struct {
		ID              string           `json:"id"`
		WorkspaceRoot   string           `json:"workspaceRoot"`
		InitialRevision roomRevisionView `json:"initialRevision"`
	}
	requestJSONWithHeaders(t, server.Handler(), http.MethodPost, "/api/rooms", map[string]any{
		"name": "Installed repository", "description": "Create governed Real Tasks.",
	}, map[string]string{"Idempotency-Key": "create-installed-room"}, http.StatusCreated, &createdRoom)
	if createdRoom.WorkspaceRoot != "" || createdRoom.InitialRevision.ID == "" {
		t.Fatalf("created installed Room = %#v", createdRoom)
	}
	requestJSONWithHeaders(t, server.Handler(), http.MethodPost, "/api/rooms", map[string]any{
		"name": "Changed installed repository", "description": "Changed key reuse must conflict.",
	}, map[string]string{"Idempotency-Key": "create-installed-room"}, http.StatusConflict, nil)
	assertTableCounts(t, databasePath, map[string]int{"rooms": 1, "room_revision_records": 1})
	var room roomDetailView
	requestJSON(t, server.Handler(), http.MethodGet, "/api/rooms/"+createdRoom.ID, nil, http.StatusOK, &room)
	if room.WorkspaceRoot != "" || len(room.Revisions) != 1 {
		t.Fatalf("installed Room = %#v", room)
	}
	var directory roomDirectoryView
	requestJSON(t, server.Handler(), http.MethodGet, "/api/rooms", nil, http.StatusOK, &directory)
	if len(directory.ActiveRooms) != 1 || directory.ActiveRooms[0].WorkspaceRoot != "" {
		t.Fatalf("product Directory exposed installed workspace root: %#v", directory)
	}
	var workspace roomWorkspaceView
	requestJSON(t, server.Handler(), http.MethodGet, "/api/rooms/"+createdRoom.ID+"/workspace", nil, http.StatusOK, &workspace)
	if workspace.Room.WorkspaceRoot != "" {
		t.Fatalf("product workspace exposed installed workspace root: %#v", workspace.Room)
	}
	initialRevisionID := room.Revisions[0].ID
	var implicit taskRefView
	requestJSONWithHeaders(t, server.Handler(), http.MethodPost, "/api/rooms/"+room.ID+"/tasks", map[string]any{
		"title": "Implicit profile", "goal": "An omitted product profile must not silently select Fake.",
		"criteria": []string{"explicit selection is required"}, "revisionIds": []string{initialRevisionID},
	}, map[string]string{"Idempotency-Key": "implicit-profile"}, http.StatusCreated, &implicit)
	if implicit.ExecutionProfile != string(app.TaskExecutionProfileRealSpecCoding) || implicit.AgentExecutionProfile != string(domain.AgentExecutionProfileStandard) || implicit.Repository == nil || implicit.Planning.Draft == nil || len(implicit.Criteria) != 1 {
		t.Fatalf("implicit product Task did not derive the real intent-first boundary: %#v", implicit)
	}
	requestJSON(t, server.Handler(), http.MethodPost, "/api/rooms/"+room.ID+"/tasks", map[string]any{
		"title": "Diagnostic key gate", "goal": "Prove product mutations require caller idempotency.",
		"executionProfile": "diagnostic_fake", "criteria": []string{"caller key is required"}, "revisionIds": []string{initialRevisionID},
	}, http.StatusBadRequest, nil)
	body := realTaskAPIBody(initialRevisionID, "Reject blank Task descriptions")
	var created taskRefView
	createdBody := planningRawRequest(t, server.Handler(), http.MethodPost, "/api/rooms/"+room.ID+"/tasks", body, "create-real-task-one", http.StatusCreated)
	if err := json.Unmarshal(createdBody, &created); err != nil {
		t.Fatal(err)
	}
	if created.ExecutionProfile != string(app.TaskExecutionProfileRealSpecCoding) || created.AgentExecutionProfile != string(domain.AgentExecutionProfileStandard) || created.Repository == nil ||
		created.Repository.Name != envelope.Repository().Name || created.Repository.SourceRevision != envelope.Repository().SourceRevision ||
		created.Repository.BaselineDigest != envelope.Repository().BaselineDigest || created.Planning.Draft == nil {
		t.Fatalf("created Real Task = %#v", created)
	}
	assertTableCounts(t, databasePath, map[string]int{"tasks": 2, "user_spec_coding_intents": 2, "run_charters": 0, "context_snapshots": 0, "spec_coding_bindings": 0})

	var replayed taskRefView
	requestJSONWithHeaders(t, server.Handler(), http.MethodPost, "/api/rooms/"+room.ID+"/tasks", body,
		map[string]string{"Idempotency-Key": "create-real-task-one"}, http.StatusCreated, &replayed)
	if replayed.ID != created.ID || replayed.Planning.Draft == nil || replayed.Planning.Draft.ID != created.Planning.Draft.ID {
		t.Fatalf("replayed Real Task = %#v, want Task/Draft %s/%s", replayed, created.ID, created.Planning.Draft.ID)
	}

	changed := realTaskAPIBody(initialRevisionID, "Changed requirement")
	requestJSONWithHeaders(t, server.Handler(), http.MethodPost, "/api/rooms/"+room.ID+"/tasks", changed,
		map[string]string{"Idempotency-Key": "create-real-task-one"}, http.StatusConflict, nil)
	unsupported := realTaskAPIBody(initialRevisionID, "Escape the installed envelope")
	unsupported["realSpecCoding"].(map[string]any)["writableFiles"] = []string{"../escape.go"}
	requestJSONWithHeaders(t, server.Handler(), http.MethodPost, "/api/rooms/"+room.ID+"/tasks", unsupported,
		map[string]string{"Idempotency-Key": "unsupported-real-task"}, http.StatusUnprocessableEntity, nil)
	requestJSON(t, server.Handler(), http.MethodPost, "/api/rooms/"+room.ID+"/tasks", realTaskAPIBody(initialRevisionID, "Missing caller key"), http.StatusBadRequest, nil)
	assertTableCounts(t, databasePath, map[string]int{"tasks": 2, "user_spec_coding_intents": 2, "run_charters": 0, "context_snapshots": 0, "spec_coding_bindings": 0})

	draft := created.Planning.Draft
	var submitted submitTechnicalPlanDraftCommandView
	requestJSONWithHeaders(t, server.Handler(), http.MethodPost, "/api/tasks/"+created.ID+"/plan/drafts/"+draft.ID+"/submit",
		map[string]any{"expectedEditVersion": draft.EditVersion, "confirmUnchanged": false},
		map[string]string{"Idempotency-Key": "submit-real-task-one"}, http.StatusOK, &submitted)
	var accepted reviewTechnicalPlanRevisionCommandView
	requestJSONWithHeaders(t, server.Handler(), http.MethodPost, "/api/tasks/"+created.ID+"/plan/revisions/"+submitted.Revision.ID+"/activate",
		map[string]any{}, map[string]string{"Idempotency-Key": "activate-real-task-one"}, http.StatusOK, &accepted)
	if accepted.Acceptance == nil || accepted.Acceptance.AdapterID != "pi" || accepted.Acceptance.SnapshotID == "" || accepted.Acceptance.CharterID == "" {
		t.Fatalf("accepted Real Task = %#v", accepted)
	}
	if accepted.Review.Reviewer != systemActor {
		t.Fatalf("automatic Plan activation reviewer = %q, want %q", accepted.Review.Reviewer, systemActor)
	}
	assertTableCounts(t, databasePath, map[string]int{"tasks": 2, "user_spec_coding_intents": 2, "run_charters": 1, "context_snapshots": 1, "spec_coding_bindings": 1})
	lateReplayBody := planningRawRequest(t, server.Handler(), http.MethodPost, "/api/rooms/"+room.ID+"/tasks", body, "create-real-task-one", http.StatusCreated)
	if !bytes.Equal(createdBody, lateReplayBody) {
		t.Fatalf("Real Task replay changed after Plan acceptance:\nfirst=%s\nreplay=%s", createdBody, lateReplayBody)
	}
	requestJSON(t, server.Handler(), http.MethodPost, "/api/tasks/"+created.ID+"/runs",
		map[string]any{"revisionId": submitted.Revision.ID}, http.StatusBadRequest, nil)
	assertTableCounts(t, databasePath, map[string]int{"runs": 0, "attempts": 0, "runtime_sessions": 0})
	requestJSONWithHeaders(t, server.Handler(), http.MethodPost, "/api/tasks/"+created.ID+"/runs",
		map[string]any{"revisionId": submitted.Revision.ID}, map[string]string{"Idempotency-Key": "blocked-real-run"}, http.StatusServiceUnavailable, nil)
	assertTableCounts(t, databasePath, map[string]int{"runs": 0, "attempts": 0, "runtime_sessions": 0})
}

type publicAPITaskWorktrees struct{ repository speccoding.RepositoryIdentity }

func (manager publicAPITaskWorktrees) Plan(_ context.Context, task domain.Task, createdAt time.Time) (domain.TaskWorktreeBinding, error) {
	return domain.NewTaskWorktreeBinding(domain.TaskWorktreeBindingParams{
		TaskID: task.ID().String(), RepositoryIdentity: manager.repository.Name, PinnedBaseRevision: manager.repository.SourceRevision,
		RelativeLocator: "chora-" + task.ID().String() + "-api-test", ConfiguredRootFingerprint: sha256.Sum256([]byte("api-test-worktrees")), CreatedAt: createdAt,
	})
}

func (publicAPITaskWorktrees) Ensure(context.Context, domain.TaskWorktreeBinding) error { return nil }

func (publicAPITaskWorktrees) ResolveExecutionRoot(context.Context, domain.Task) (string, error) { return "", nil }

func realTaskAPIBody(revisionID, requirement string) map[string]any {
	return map[string]any{
		"title": "Validate Task descriptions", "executionProfile": "real_spec_coding", "revisionIds": []string{revisionID},
		"realSpecCoding": map[string]any{
			"requirement": requirement, "constraints": []string{"Keep the change bounded."}, "outOfScope": []string{"No unrelated refactor."},
			"criteria":             []map[string]any{{"title": "Descriptions are validated", "description": "Blank descriptions are rejected.", "verificationCommandIndexes": []int{0}}},
			"writableFiles":        []string{"internal/domain/task.go", "internal/domain/task_test.go"},
			"verificationCommands": []map[string]any{{"argv": []string{"go", "test", "./internal/domain"}}},
		},
	}
}

func assertTableCounts(t *testing.T, databasePath string, expected map[string]int) {
	t.Helper()
	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for table, want := range expected {
		var got int
		if err := database.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&got); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if got != want {
			t.Fatalf("%s count = %d, want %d", table, got, want)
		}
	}
}
