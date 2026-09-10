package localweb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	agentfake "github.com/Yangyang96/chora/internal/agent/fake"
	agentpi "github.com/Yangyang96/chora/internal/agent/pi"
	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/dockersupervisor"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/preflight"
	storecontract "github.com/Yangyang96/chora/internal/store"
	storesqlite "github.com/Yangyang96/chora/internal/store/sqlite"
	"github.com/Yangyang96/chora/migrations"
)

var latestLocalwebSQLiteTemplate struct {
	sync.Once
	data []byte
	err  error
}

func TestOptionalVerificationServicesDoesNotBoxTypedNil(t *testing.T) {
	verification, patch := optionalVerificationServices(nil)
	if verification != nil || patch != nil {
		t.Fatalf("typed nil verifier leaked through interfaces: verification=%#v patch=%#v", verification, patch)
	}
	executor := &productVerifier{}
	verification, patch = optionalVerificationServices(executor)
	if verification == nil || patch == nil {
		t.Fatal("configured verifier was dropped")
	}
}

func prepareLatestSQLiteTestDatabase(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	latestLocalwebSQLiteTemplate.Do(func() {
		root, err := os.MkdirTemp("", "chora-localweb-sqlite-template-")
		if err != nil {
			latestLocalwebSQLiteTemplate.err = err
			return
		}
		defer os.RemoveAll(root)
		if err := os.Chmod(root, 0o700); err != nil {
			latestLocalwebSQLiteTemplate.err = err
			return
		}
		templatePath := filepath.Join(root, "chora.db")
		store, err := storesqlite.Open(context.Background(), templatePath)
		if err != nil {
			latestLocalwebSQLiteTemplate.err = err
			return
		}
		if err := store.Close(); err != nil {
			latestLocalwebSQLiteTemplate.err = err
			return
		}
		latestLocalwebSQLiteTemplate.data, latestLocalwebSQLiteTemplate.err = os.ReadFile(templatePath)
	})
	if latestLocalwebSQLiteTemplate.err != nil {
		return fmt.Errorf("prepare latest localweb SQLite test template: %w", latestLocalwebSQLiteTemplate.err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create localweb SQLite test directory: %w", err)
	}
	if err := os.WriteFile(path, latestLocalwebSQLiteTemplate.data, 0o600); err != nil {
		return fmt.Errorf("copy latest localweb SQLite test template: %w", err)
	}
	return nil
}

func newTestServer(ctx context.Context, databasePath, webRoot string, logger *log.Logger) (*Server, error) {
	if err := prepareLatestSQLiteTestDatabase(databasePath); err != nil {
		return nil, err
	}
	server, err := New(ctx, databasePath, webRoot, logger)
	if err != nil {
		return nil, err
	}
	// Unit fixtures replace service/runtime dependencies after construction.
	// Finish that setup before allowing the background dispatcher to read them.
	// Dispatcher integration fixtures explicitly restart it after configuration.
	server.automaticRetryCancel()
	server.automaticRetryWG.Wait()
	return server, nil
}

func TestNewProductFailureClosesOwnedDockerLedgerForSameProcessReopen(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stateRoot := filepath.Join(root, "state")
	ledger, err := dockersupervisor.OpenOperationLedger(ownershipLedgerRunner{}, stateRoot, "g1")
	if err != nil {
		t.Fatal(err)
	}
	options := ProductOptions{DockerRunner: ledger, DockerLedgerOwner: NewDockerOperationLedgerOwnership(ledger)}
	if _, err := NewProduct(context.Background(), "relative.db", "relative-web", log.New(io.Discard, "", 0), options); err == nil {
		t.Fatal("invalid NewProduct unexpectedly succeeded")
	}
	reopened, err := dockersupervisor.OpenOperationLedger(ownershipLedgerRunner{}, stateRoot, "g1")
	if err != nil {
		t.Fatalf("same-process reopen after NewProduct failure: %v", err)
	}
	_ = reopened.Close()
}

func TestNewSourceCheckoutRequiresQualifiedDockerAndNeverFallsBackToHost(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewSourceCheckout(context.Background(), filepath.Join(root, "data", "chora.db"), filepath.Join(root, "web"), log.New(io.Discard, "", 0), ProductOptions{
		RepoRoot: root, RepositoryRoot: root, PiAuthFile: filepath.Join(root, "auth.json"), PiPreflight: func(context.Context) preflight.Report {
			return preflight.Report{Status: preflight.StatusPassed}
		},
	})
	if err == nil || !strings.Contains(err.Error(), "qualified Docker Sandbox") {
		t.Fatalf("missing Sandbox authority did not fail closed: %v", err)
	}
}

func TestNewSourceCheckoutRejectsMismatchedEngineIdentityAndQualification(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	identity := sourceCheckoutTestEngineIdentity(t, "expected-daemon")
	otherIdentity := sourceCheckoutTestEngineIdentity(t, "qualified-other-daemon")
	contract, err := dockersupervisor.NewCapabilityProbeContract(agentpi.AttemptImageID, agentpi.PolicySHA256)
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewSourceCheckout(context.Background(), filepath.Join(root, "data", "chora.db"), filepath.Join(root, "web"), log.New(io.Discard, "", 0), ProductOptions{
		RepoRoot: root, RepositoryRoot: root, PiAuthFile: filepath.Join(root, "auth.json"),
		PiPreflight:  func(context.Context) preflight.Report { return preflight.Report{Status: preflight.StatusPassed} },
		DockerRunner: &sourceCheckoutJoinedRunner{identity: identity}, DockerLedgerOwner: &DockerOperationLedgerOwnership{},
		DockerEngineIdentity: identity, DockerCapability: contract, DockerQualification: testEngineQualification(t, otherIdentity, contract),
	})
	if err == nil || !strings.Contains(err.Error(), "identity is inconsistent") {
		t.Fatalf("mismatched SourceCheckout identity was not rejected: %v", err)
	}
}

func TestNewSourceCheckoutRejectsAbsentEngineIdentityWithOtherwiseQualifiedDocker(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	qualifiedIdentity := sourceCheckoutTestEngineIdentity(t, "qualified-daemon")
	contract, err := dockersupervisor.NewCapabilityProbeContract(agentpi.AttemptImageID, agentpi.PolicySHA256)
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewSourceCheckout(context.Background(), filepath.Join(root, "data", "chora.db"), filepath.Join(root, "web"), log.New(io.Discard, "", 0), ProductOptions{
		RepoRoot: root, RepositoryRoot: root, PiAuthFile: filepath.Join(root, "auth.json"),
		PiPreflight:  func(context.Context) preflight.Report { return preflight.Report{Status: preflight.StatusPassed} },
		DockerRunner: &sourceCheckoutJoinedRunner{identity: qualifiedIdentity}, DockerLedgerOwner: &DockerOperationLedgerOwnership{},
		DockerCapability: contract, DockerQualification: testEngineQualification(t, qualifiedIdentity, contract),
	})
	if err == nil || !strings.Contains(err.Error(), "qualified Docker Sandbox") {
		t.Fatalf("absent SourceCheckout identity was not rejected: %v", err)
	}
}

func TestStatusAPIExposesQualifiedSourceCheckoutEngineIdentity(t *testing.T) {
	identity := sourceCheckoutTestEngineIdentity(t, "status-daemon")
	contract, err := dockersupervisor.NewCapabilityProbeContract(agentpi.AttemptImageID, agentpi.PolicySHA256)
	if err != nil {
		t.Fatal(err)
	}
	options := ProductOptions{
		DockerEngineIdentity: identity, DockerCapability: contract,
		DockerQualification: testEngineQualification(t, identity, contract),
	}
	status, err := sourceCheckoutPiRuntimeStatus(piRuntimeStatus{Enabled: true, Provider: domain.DockerExecutionProvider}, options)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{piStatus: status}
	var response struct {
		Pi piRuntimeStatus `json:"pi"`
	}
	requestJSON(t, server.Handler(), http.MethodGet, "/api/status", nil, http.StatusOK, &response)
	if response.Pi.EngineIdentityDigest != identity.Digest() || response.Pi.DockerContext != identity.ContextName() ||
		response.Pi.ContextEndpointDigest != identity.ContextEndpointDigest() || response.Pi.DockerServerVersion != identity.EngineVersion() {
		t.Fatalf("/api/status.pi = %#v", response.Pi)
	}
}

func TestServerCloseConsumesTransferredDockerLedgerExactlyOnce(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
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
	stateRoot := filepath.Join(root, "state")
	ledger, err := dockersupervisor.OpenOperationLedger(ownershipLedgerRunner{}, stateRoot, "g1")
	if err != nil {
		t.Fatal(err)
	}
	owner := NewDockerOperationLedgerOwnership(ledger)
	transferred, err := owner.transfer(ledger)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.transfer(ledger); err == nil {
		t.Fatal("ledger ownership transferred twice")
	}
	counted := &countingLedgerCloser{closer: transferred}
	server.dockerLedgerCloser = counted
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	if counted.calls != 1 {
		t.Fatalf("ledger close calls = %d, want 1", counted.calls)
	}
	reopened, err := dockersupervisor.OpenOperationLedger(ownershipLedgerRunner{}, stateRoot, "g1")
	if err != nil {
		t.Fatalf("same-process reopen after Server.Close: %v", err)
	}
	_ = reopened.Close()
}

type ownershipLedgerRunner struct{}

func (ownershipLedgerRunner) Run(context.Context, dockersupervisor.Command) (dockersupervisor.CommandResult, error) {
	return dockersupervisor.CommandResult{}, nil
}

func (ownershipLedgerRunner) Start(context.Context, dockersupervisor.Command) (dockersupervisor.Process, error) {
	return nil, errors.New("unexpected Start")
}

type countingLedgerCloser struct {
	closer interface{ Close() error }
	calls  int
}

func (closer *countingLedgerCloser) Close() error {
	closer.calls++
	return closer.closer.Close()
}

func TestRoomDirectoryWorkspaceNestedResourcesAndLifecycleAPI(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	webRoot := filepath.Join(root, "web")
	if err := os.MkdirAll(webRoot, 0o700); err != nil {
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
	handler := server.Handler()

	type createdRoomView struct {
		ID              string           `json:"id"`
		State           string           `json:"state"`
		Version         uint64           `json:"version"`
		ArchivedAt      string           `json:"archivedAt"`
		InitialRevision roomRevisionView `json:"initialRevision"`
	}
	var room, otherRoom createdRoomView
	requestJSON(t, handler, http.MethodPost, "/api/rooms", map[string]any{
		"name": "Directory Room", "description": "Resume exact work.", "workspaceRoot": root,
	}, http.StatusCreated, &room)
	otherRoot := filepath.Join(root, "other")
	if err := os.MkdirAll(otherRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	requestJSON(t, handler, http.MethodPost, "/api/rooms", map[string]any{
		"name": "Other Room", "description": "Prove nested ownership.", "workspaceRoot": otherRoot,
	}, http.StatusCreated, &otherRoom)
	if room.State != string(domain.RoomStateActive) || room.Version == 0 || room.ArchivedAt != "" {
		t.Fatalf("created Room lifecycle = %#v", room)
	}

	createTask := func(title string) taskRefView {
		t.Helper()
		var task taskRefView
		requestJSON(t, handler, http.MethodPost, "/api/rooms/"+room.ID+"/tasks", map[string]any{
			"title": title, "goal": "Exercise U2 public API.", "criteria": []string{"state is durable"}, "revisionIds": []string{room.InitialRevision.ID},
		}, http.StatusCreated, &task)
		return task
	}
	draftTask := createTask("Keep an editable draft")

	var directory roomDirectoryView
	requestJSON(t, handler, http.MethodGet, "/api/rooms", nil, http.StatusOK, &directory)
	if len(directory.ActiveRooms) != 2 || len(directory.ArchivedRooms) != 0 {
		t.Fatalf("initial directory = %#v", directory)
	}
	var workspace roomWorkspaceView
	requestJSON(t, handler, http.MethodGet, "/api/rooms/"+room.ID+"/workspace", nil, http.StatusOK, &workspace)
	if workspace.Room.ID != room.ID || workspace.Room.State != string(domain.RoomStateActive) || workspace.Room.Version < room.Version ||
		workspace.Room.TaskCounts.Total != 1 || !workspace.Room.HumanActionRequired || len(workspace.Tasks) != 1 {
		t.Fatalf("Room workspace summary = %#v", workspace)
	}
	taskSummary := workspace.Tasks[0]
	if taskSummary.ID != draftTask.ID || taskSummary.Status != domain.TaskStateOpen || taskSummary.RunCount != 0 || taskSummary.LatestRun != nil ||
		taskSummary.CurrentPlanRevision != nil || taskSummary.CurrentAction.Kind != "edit_plan" || taskSummary.CurrentAction.Target.RoomID != room.ID ||
		taskSummary.CurrentAction.Target.TaskID != draftTask.ID || taskSummary.CurrentAction.Target.DraftID != draftTask.Planning.Draft.ID ||
		taskSummary.CurrentAction.Target.ExpectedVersion != draftTask.Planning.Draft.EditVersion || taskSummary.CurrentAction.URL == "" || taskSummary.CurrentAction.Reason == "" {
		t.Fatalf("Task workspace summary = %#v", taskSummary)
	}
	var nestedTask taskRefView
	requestJSON(t, handler, http.MethodGet, "/api/rooms/"+room.ID+"/tasks/"+draftTask.ID, nil, http.StatusOK, &nestedTask)
	if nestedTask.ID != draftTask.ID || nestedTask.RoomID != room.ID {
		t.Fatalf("nested Task = %#v", nestedTask)
	}
	requestJSON(t, handler, http.MethodGet, "/api/rooms/"+otherRoom.ID+"/tasks/"+draftTask.ID, nil, http.StatusNotFound, nil)
	requestJSON(t, handler, http.MethodGet, "/api/rooms/"+otherRoom.ID+"/tasks/"+draftTask.ID+"/runs", nil, http.StatusNotFound, nil)

	runTask := createTask("Produce a reviewable Run")
	acceptTaskPlan(t, handler, runTask.ID)
	requestJSON(t, handler, http.MethodGet, "/api/tasks/"+runTask.ID, nil, http.StatusOK, &runTask)
	runTaskID, err := domain.ParseTaskID(runTask.ID)
	if err != nil {
		t.Fatal(err)
	}
	revisionID, err := domain.ParseTechnicalPlanRevisionID(runTask.Planning.Acceptance.RevisionID)
	if err != nil {
		t.Fatal(err)
	}
	createdRun, err := server.service.CreateRun(context.Background(), app.CreateRunRequest{CommandMeta: commandMeta("seed-u2-dormant-run"), TaskID: runTaskID, RevisionID: revisionID})
	if err != nil {
		t.Fatal(err)
	}
	started, err := server.runView(context.Background(), createdRun.Run.ID())
	if err != nil {
		t.Fatal(err)
	}
	startTask := createTask("Remain ready to start")
	acceptTaskPlan(t, handler, startTask.ID)
	closedTask := createTask("Reject a stale terminal start")
	acceptTaskPlan(t, handler, closedTask.ID)
	raw, err := sql.Open("sqlite", filepath.Join(root, "chora.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`UPDATE tasks SET state='closed' WHERE id=?`, closedTask.ID); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	requestJSON(t, handler, http.MethodPost, "/api/tasks/"+closedTask.ID+"/runs", nil, http.StatusConflict, nil)

	var history taskRunHistoryView
	requestJSON(t, handler, http.MethodGet, "/api/rooms/"+room.ID+"/tasks/"+runTask.ID+"/runs", nil, http.StatusOK, &history)
	if history.RoomID != room.ID || history.TaskID != runTask.ID || len(history.Runs) != 1 || history.Runs[0].ID != started.ID ||
		history.Runs[0].Status != domain.RunStateDraft || history.Runs[0].CreatedAt == "" || history.Runs[0].UpdatedAt == "" {
		t.Fatalf("Run history = %#v", history)
	}
	var nestedRun runView
	requestJSON(t, handler, http.MethodGet, "/api/rooms/"+room.ID+"/tasks/"+runTask.ID+"/runs/"+started.ID, nil, http.StatusOK, &nestedRun)
	if nestedRun.ID != started.ID || nestedRun.Task.ID != runTask.ID || nestedRun.Room.ID != room.ID {
		t.Fatalf("nested Run = %#v", nestedRun)
	}
	requestJSON(t, handler, http.MethodGet, "/api/rooms/"+room.ID+"/tasks/"+draftTask.ID+"/runs/"+started.ID, nil, http.StatusNotFound, nil)
	requestJSON(t, handler, http.MethodGet, "/api/rooms/"+otherRoom.ID+"/tasks/"+runTask.ID+"/runs/"+started.ID, nil, http.StatusNotFound, nil)

	requestJSON(t, handler, http.MethodGet, "/api/rooms/"+room.ID+"/workspace", nil, http.StatusOK, &workspace)
	archiveVersion := workspace.Room.Version
	headers := map[string]string{"Idempotency-Key": "archive-directory-room"}
	var archived, replayed roomView
	requestJSONWithHeaders(t, handler, http.MethodPost, "/api/rooms/"+room.ID+"/archive", map[string]any{"expectedVersion": archiveVersion}, headers, http.StatusOK, &archived)
	requestJSONWithHeaders(t, handler, http.MethodPost, "/api/rooms/"+room.ID+"/archive", map[string]any{"expectedVersion": archiveVersion}, headers, http.StatusOK, &replayed)
	if archived != replayed || archived.State != string(domain.RoomStateArchived) || archived.Version != archiveVersion+1 || archived.ArchivedAt == "" {
		t.Fatalf("Archive replay: first=%#v replay=%#v", archived, replayed)
	}
	requestJSON(t, handler, http.MethodPost, "/api/rooms/"+room.ID+"/restore", map[string]any{"expectedVersion": archived.Version}, http.StatusBadRequest, nil)
	requestJSONWithHeaders(t, handler, http.MethodPost, "/api/rooms/"+room.ID+"/restore", map[string]any{"expectedVersion": archiveVersion}, map[string]string{"Idempotency-Key": "stale-restore"}, http.StatusConflict, nil)

	requestJSON(t, handler, http.MethodPost, "/api/rooms/"+room.ID+"/tasks", map[string]any{
		"title": "Forbidden", "goal": "Must remain frozen.", "criteria": []string{"blocked"}, "revisionIds": []string{room.InitialRevision.ID},
	}, http.StatusUnprocessableEntity, nil)
	requestJSONWithHeaders(t, handler, http.MethodPatch, "/api/tasks/"+draftTask.ID+"/plan/drafts/"+draftTask.Planning.Draft.ID, map[string]any{
		"expectedEditVersion": draftTask.Planning.Draft.EditVersion, "technicalSteps": []string{"Blocked step."}, "decisions": []string{"Blocked decision."}, "risks": []string{"Blocked risk."}, "unknowns": []string{"Blocked unknown."},
	}, map[string]string{"Idempotency-Key": "archived-plan-write"}, http.StatusUnprocessableEntity, nil)
	requestJSON(t, handler, http.MethodPost, "/api/tasks/"+startTask.ID+"/runs", nil, http.StatusUnprocessableEntity, nil)
	reviewError := httptest.NewRecorder()
	writeMutationConflictError(reviewError, fmt.Errorf("review blocked: %w", storecontract.ErrRoomStateForbidden))
	if reviewError.Code != http.StatusUnprocessableEntity {
		t.Fatalf("archived Review error status = %d, want %d", reviewError.Code, http.StatusUnprocessableEntity)
	}

	requestJSON(t, handler, http.MethodGet, "/api/rooms", nil, http.StatusOK, &directory)
	if len(directory.ActiveRooms) != 1 || directory.ActiveRooms[0].ID != otherRoom.ID || len(directory.ArchivedRooms) != 1 || directory.ArchivedRooms[0].ID != room.ID {
		t.Fatalf("archived directory = %#v", directory)
	}
	var restored roomView
	requestJSONWithHeaders(t, handler, http.MethodPost, "/api/rooms/"+room.ID+"/restore", map[string]any{"expectedVersion": archived.Version}, map[string]string{"Idempotency-Key": "restore-directory-room"}, http.StatusOK, &restored)
	if restored.State != string(domain.RoomStateActive) || restored.Version != archived.Version+1 || restored.ArchivedAt != "" {
		t.Fatalf("restored Room = %#v", restored)
	}
}

func TestRelatedTaskNestedRouteDoesNotRequireFrozenSelection(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	webRoot := filepath.Join(root, "web")
	if err := os.MkdirAll(webRoot, 0o700); err != nil {
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

	var room struct {
		ID              string `json:"id"`
		InitialRevision struct {
			ID string `json:"id"`
		} `json:"initialRevision"`
	}
	requestJSON(t, server.Handler(), http.MethodPost, "/api/rooms", map[string]any{
		"name": "Contract Change Room", "description": "Keep the successor route usable.", "workspaceRoot": root,
	}, http.StatusCreated, &room)
	var source taskRefView
	requestJSON(t, server.Handler(), http.MethodPost, "/api/rooms/"+room.ID+"/tasks", map[string]any{
		"title": "Original contract", "goal": "Prove an explicit contract-change route.",
		"criteria": []string{"the related Task remains reachable"}, "revisionIds": []string{room.InitialRevision.ID},
	}, http.StatusCreated, &source)
	sourceID, err := domain.ParseTaskID(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	sourceTask, err := server.store.Reader().GetTask(context.Background(), sourceID)
	if err != nil {
		t.Fatal(err)
	}
	criterion, err := domain.NewAcceptanceCriterion(domain.NewCriterionID(), "Approve the contract change", "The related Task remains explicitly reviewable.")
	if err != nil {
		t.Fatal(err)
	}
	related, err := domain.NewRelatedTask(domain.NewTaskID(), sourceTask, "Related contract change", "Review the requested contract change.", []domain.AcceptanceCriterion{criterion})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.store.WithinWriteTx(context.Background(), func(tx storecontract.WriteTx) error {
		return tx.InsertTask(context.Background(), related)
	}); err != nil {
		t.Fatal(err)
	}

	var loaded taskRefView
	requestJSON(t, server.Handler(), http.MethodGet, "/api/rooms/"+room.ID+"/tasks/"+related.ID().String(), nil, http.StatusOK, &loaded)
	if loaded.ID != related.ID().String() || loaded.RoomID != room.ID || loaded.Selection != nil || loaded.Planning.Revisions == nil || len(loaded.Planning.Revisions) != 0 {
		t.Fatalf("related Task route = %#v", loaded)
	}
}

func TestPiProductEntryExposesAdoptedRuntimeAndSandbox(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	webRoot := filepath.Join(root, "web")
	if err := os.MkdirAll(webRoot, 0o700); err != nil {
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

	var status struct {
		Pi piRuntimeStatus `json:"pi"`
	}
	requestJSON(t, server.Handler(), http.MethodGet, "/api/status", nil, http.StatusOK, &status)
	if !status.Pi.Enabled || status.Pi.Provider != "docker-colima" || status.Pi.Image != agentpi.AttemptImageID || status.Pi.PolicyFingerprint != agentpi.PolicySHA256 {
		t.Fatalf("Pi status = %#v", status.Pi)
	}

	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	taskID := createRegisteredPiTask(t, server.Handler(), source)
	var run runView
	requestJSON(t, server.Handler(), http.MethodPost, "/api/tasks/"+taskID+"/runs", nil, http.StatusCreated, &run)
	if run.Adapter != agentpi.AdapterID || run.AttemptDetail == nil || run.AttemptDetail.Runtime == nil || run.AttemptDetail.Runtime.Version != agentpi.RuntimeVersion {
		t.Fatalf("Pi runtime view = %#v", run.AttemptDetail)
	}
	if sandbox := run.AttemptDetail.Sandbox; sandbox.Status != "adopted" || sandbox.Provider != "docker-colima" || sandbox.Mode != "attempt-private-codex-only" || sandbox.Image != agentpi.AttemptImageID || sandbox.PolicyFingerprint != agentpi.PolicySHA256 {
		t.Fatalf("Pi sandbox view = %#v", sandbox)
	}
	if run.AttemptDetail.ExecutionWorkspace != "/workspace/repository" {
		t.Fatalf("Pi execution workspace leaked source path: %q", run.AttemptDetail.ExecutionWorkspace)
	}
	requestJSON(t, server.Handler(), http.MethodPost, "/api/runs/"+run.ID+"/cancel", map[string]any{"reason": "test cleanup"}, http.StatusOK, &run)
}

func TestPiMonitorPersistsUncertainReconcileAsRecoveryRequired(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	webRoot := filepath.Join(root, "web")
	if err := os.MkdirAll(webRoot, 0o700); err != nil {
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

	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	taskID := createRegisteredPiTask(t, server.Handler(), source)
	var run runView
	requestJSON(t, server.Handler(), http.MethodPost, "/api/tasks/"+taskID+"/runs", nil, http.StatusCreated, &run)

	server.supervisor.mu.RLock()
	runtime := server.supervisor.byAdapter[agentpi.AdapterID].(*fakeSupervisor)
	server.supervisor.mu.RUnlock()
	runtime.mu.Lock()
	runtime.reconcileOverride = execution.ReconcileUncertain
	runtime.reconcileDiagnostic = "declared test uncertainty"
	runtime.mu.Unlock()

	run = waitForRunState(t, server.Handler(), run.ID, string(domain.RunStateRecoveryRequired))
	if run.Controls.CanRetry || run.AttemptDetail == nil || run.AttemptDetail.State != domain.AttemptStateRunning {
		t.Fatalf("uncertain runtime did not fail closed: %#v", run)
	}
}

func TestPiUndeclaredPathEventFailsRunWithoutRetry(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	webRoot := filepath.Join(root, "web")
	if err := os.MkdirAll(webRoot, 0o700); err != nil {
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
	server.supervisor.mu.RLock()
	runtime := server.supervisor.byAdapter[agentpi.AdapterID].(*fakeSupervisor)
	server.supervisor.mu.RUnlock()
	runtime.mu.Lock()
	runtime.streams[execution.StreamStdout] = []byte(`{"type":"tool_execution_start","toolName":"bash","args":{"command":"cat /Users/example/private.txt"}}` + "\n")
	runtime.streams[execution.StreamStderr] = nil
	runtime.mu.Unlock()

	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "probe.txt"), []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	taskID := createRegisteredPiTask(t, server.Handler(), source)
	var run runView
	requestJSON(t, server.Handler(), http.MethodPost, "/api/tasks/"+taskID+"/runs", nil, http.StatusCreated, &run)
	if err := runtime.Notify(execution.StreamStdout); err != nil {
		t.Fatal(err)
	}
	run = waitForRunState(t, server.Handler(), run.ID, "cancelled")
	if run.Controls.CanRetry || run.AttemptDetail == nil || run.AttemptDetail.State != domain.AttemptStateCancelled {
		t.Fatalf("policy violation remained retryable: %#v", run)
	}
	var events struct {
		Events []struct {
			Type       string `json:"type"`
			Normalized struct {
				ErrorClass string `json:"error_class"`
			} `json:"normalized"`
		} `json:"events"`
	}
	requestJSON(t, server.Handler(), http.MethodGet, "/api/runs/"+run.ID+"/events?after=0&limit=100", nil, http.StatusOK, &events)
	found := false
	for _, event := range events.Events {
		found = found || event.Type == "error" && event.Normalized.ErrorClass == "undeclared_path"
	}
	if !found {
		t.Fatalf("policy violation event not persisted: %#v", events.Events)
	}
}

func enablePiTestRuntime(t *testing.T, server *Server) {
	t.Helper()
	runtimeConfig, err := os.ReadFile(filepath.Join("..", "..", "contracts", "g2-m1a", "pi-runtime-config.v6.json"))
	if err != nil {
		t.Fatal(err)
	}
	policy, err := os.ReadFile(filepath.Join("..", "..", "contracts", "g2-m1a", "sandbox-policy.v4.json"))
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := agentpi.New(agentpi.Config{RuntimeConfig: runtimeConfig, Policy: policy, AttemptImageID: agentpi.AttemptImageID, BoundaryImageID: agentpi.BoundaryImageID})
	if err != nil {
		t.Fatal(err)
	}
	server.registry.Set(adapter)
	runtime := newFakeSupervisor()
	target, err := execution.NewExecutionTarget(agentpi.AdapterID, domain.DockerExecutionProvider)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.supervisor.registerTarget(target, runtime); err != nil {
		t.Fatal(err)
	}
	server.supervisor.mu.Lock()
	server.supervisor.byAdapter[agentpi.AdapterID] = runtime
	server.supervisor.mu.Unlock()
	server.piStatus = piRuntimeStatus{Enabled: true, Reason: "test identity verified", HostReadIsolation: "denied", Provider: "docker-colima", Image: agentpi.AttemptImageID, PolicyFingerprint: agentpi.PolicySHA256, DockerVersion: dockersupervisor.DockerEngineVersion, ColimaVersion: dockersupervisor.RequiredColimaVersion}
	server.piPreflight = func(context.Context) preflight.Report {
		return preflight.Report{Status: preflight.StatusPassed, ResourcesCreated: false, InputFingerprint: strings.Repeat("a", 64)}
	}
}

func TestPiPreflightFailureCreatesNoRunOrRuntimeResource(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	webRoot := filepath.Join(root, "web")
	if err := os.MkdirAll(webRoot, 0o700); err != nil {
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
	defer server.Close()
	enablePiTestRuntime(t, server)
	server.piPreflight = func(context.Context) preflight.Report {
		return preflight.Report{Status: preflight.StatusFailed, ResourcesCreated: false, InputFingerprint: strings.Repeat("b", 64), Failure: &preflight.Failure{
			Boundary: "input.drift", Observed: "changed", Required: "prior fingerprint", Action: "rerun doctor",
		}}
	}
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	taskID := createRegisteredPiTask(t, server.Handler(), source)
	request := newLocalRequest(http.MethodPost, "/api/tasks/"+taskID+"/runs", nil)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var report preflight.Report
	if err := json.Unmarshal(response.Body.Bytes(), &report); err != nil || report.Failure == nil || report.Failure.Boundary != "input.drift" || report.ResourcesCreated {
		t.Fatalf("report = %#v, err = %v", report, err)
	}
	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var runs int
	if err := database.QueryRow("SELECT COUNT(*) FROM runs").Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 0 {
		t.Fatalf("Preflight failure persisted %d runs", runs)
	}
	server.supervisor.mu.RLock()
	runtime := server.supervisor.byAdapter[agentpi.AdapterID].(*fakeSupervisor)
	server.supervisor.mu.RUnlock()
	runtime.mu.Lock()
	invocation := runtime.invocation
	runtime.mu.Unlock()
	if invocation.AdapterID() != "" {
		t.Fatalf("Preflight failure created runtime invocation %#v", invocation)
	}
}

func TestPiStartReplaysSameKeyWithoutPreflightAndRejectsDistinctStaleKeyAtomically(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	webRoot := filepath.Join(root, "web")
	if err := os.MkdirAll(webRoot, 0o700); err != nil {
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
	defer server.Close()
	enablePiTestRuntime(t, server)
	preflights := 0
	server.piPreflight = func(context.Context) preflight.Report {
		preflights++
		return preflight.Report{Status: preflight.StatusPassed, ResourcesCreated: false, InputFingerprint: strings.Repeat("c", 64)}
	}
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	taskID := createRegisteredPiTask(t, server.Handler(), source)
	path := "/api/tasks/" + taskID + "/runs"
	body := map[string]any{}
	var first, replay runView
	requestJSONWithHeaders(t, server.Handler(), http.MethodPost, path, body, map[string]string{"Idempotency-Key": "start-cas-replay"}, http.StatusCreated, &first)
	before := readMutationCounts(t, databasePath)
	requestJSONWithHeaders(t, server.Handler(), http.MethodPost, path, body, map[string]string{"Idempotency-Key": "start-cas-replay"}, http.StatusCreated, &replay)
	requestJSONWithHeaders(t, server.Handler(), http.MethodPost, path, body, map[string]string{"Idempotency-Key": "start-cas-distinct-stale"}, http.StatusConflict, nil)
	after := readMutationCounts(t, databasePath)
	if preflights != 1 {
		t.Fatalf("Preflight calls = %d, want 1", preflights)
	}
	if first.ID != replay.ID || first.Version != replay.Version || first.Status != replay.Status || first.Attempt != replay.Attempt {
		t.Fatalf("same-key Start replay drifted: first=%#v replay=%#v", first, replay)
	}
	if before != after {
		t.Fatalf("stale Start mutated persisted resources: before=%#v after=%#v", before, after)
	}
}

func TestPiRetryRequiresCurrentExpectedVersionBeforePreflightOrMutation(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	webRoot := filepath.Join(root, "web")
	if err := os.MkdirAll(webRoot, 0o700); err != nil {
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
	defer server.Close()
	enablePiTestRuntime(t, server)
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	taskID := createRegisteredPiTask(t, server.Handler(), source)
	var started, cancelled runView
	requestJSON(t, server.Handler(), http.MethodPost, "/api/tasks/"+taskID+"/runs", nil, http.StatusCreated, &started)
	requestJSON(t, server.Handler(), http.MethodPost, "/api/runs/"+started.ID+"/cancel", map[string]any{"reason": "prepare versioned Retry boundary"}, http.StatusOK, &cancelled)
	preflights := 0
	server.piPreflight = func(context.Context) preflight.Report {
		preflights++
		return preflight.Report{Status: preflight.StatusPassed, ResourcesCreated: false, InputFingerprint: strings.Repeat("d", 64)}
	}
	before := readMutationCounts(t, databasePath)
	path := "/api/runs/" + cancelled.ID + "/retry"
	headers := map[string]string{"Idempotency-Key": "retry-version-boundary"}
	requestJSONWithHeaders(t, server.Handler(), http.MethodPost, path, map[string]any{"instructions": "must not execute without caller CAS"}, headers, http.StatusBadRequest, nil)
	requestJSONWithHeaders(t, server.Handler(), http.MethodPost, path, map[string]any{"expectedVersion": cancelled.Version - 1, "instructions": "must not execute with stale caller CAS"}, map[string]string{"Idempotency-Key": "retry-version-stale"}, http.StatusConflict, nil)
	after := readMutationCounts(t, databasePath)
	var preserved runView
	requestJSON(t, server.Handler(), http.MethodGet, "/api/runs/"+cancelled.ID, nil, http.StatusOK, &preserved)
	if preflights != 0 {
		t.Fatalf("rejected Retry called Preflight %d times", preflights)
	}
	if before != after {
		t.Fatalf("rejected Retry mutated persisted state: before=%#v after=%#v", before, after)
	}
	if preserved.Version != cancelled.Version || preserved.Status != cancelled.Status || preserved.Attempt != cancelled.Attempt || !preserved.Controls.CanRetry {
		t.Fatalf("rejected Retry changed existing Run: before=%#v after=%#v", cancelled, preserved)
	}
}

type mutationCounts struct {
	Runs, Attempts, Sessions, Events int
}

func readMutationCounts(t *testing.T, databasePath string) mutationCounts {
	t.Helper()
	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var counts mutationCounts
	if err := database.QueryRow(`SELECT
		(SELECT COUNT(*) FROM runs),
		(SELECT COUNT(*) FROM attempts),
		(SELECT COUNT(*) FROM runtime_sessions),
		(SELECT COUNT(*) FROM run_events)`).Scan(&counts.Runs, &counts.Attempts, &counts.Sessions, &counts.Events); err != nil {
		t.Fatal(err)
	}
	return counts
}

func TestLegacyMutablePlanningDataFailsWithNewDataDirectoryGuidance(t *testing.T) {
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
	databasePath := filepath.Join(root, "legacy-v6.db")
	seedV6ExistingTask(t, databasePath, filepath.Join(root, "legacy-workspace"))

	server, err := newTestServer(context.Background(), databasePath, webRoot, log.New(io.Discard, "", 0))
	if server != nil || !errors.Is(err, storecontract.ErrLegacyPlanningData) || !strings.Contains(err.Error(), "new data directory") {
		t.Fatalf("legacy planning open = server=%#v err=%v", server, err)
	}
}

func TestPlanningDraftIsReviewableThroughPublicAPIAndSurvivesRestart(t *testing.T) {
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

	var room struct {
		ID              string `json:"id"`
		InitialRevision struct {
			ID string `json:"id"`
		} `json:"initialRevision"`
	}
	requestJSON(t, server.Handler(), http.MethodPost, "/api/rooms", map[string]any{
		"name": "Planning Room", "description": "Keep planning explicit.", "workspaceRoot": root,
	}, http.StatusCreated, &room)
	var created taskRefView
	requestJSON(t, server.Handler(), http.MethodPost, "/api/rooms/"+room.ID+"/tasks", map[string]any{
		"title": "Validate planning", "goal": "Turn one requirement into a reviewable plan.",
		"criteria": []string{"Planning is visible", "Review survives restart"}, "revisionIds": []string{room.InitialRevision.ID},
		"plan": map[string]any{"technicalSteps": []string{"Implement immutable revisions."}, "decisions": []string{"Use explicit submission."}, "risks": []string{"Stale tabs may conflict."}, "unknowns": []string{"Restart behavior must be proven."}},
	}, http.StatusCreated, &created)
	if created.ID == "" || created.RoomID != room.ID || created.Planning.Draft == nil || created.Planning.Draft.TaskID != created.ID || created.Planning.Draft.EditVersion != 1 || len(created.Planning.Revisions) != 0 || created.Planning.Draft.Content.TechnicalSteps[0] != "Implement immutable revisions." {
		t.Fatalf("created planning Task = %#v", created)
	}
	var loaded taskRefView
	requestJSON(t, server.Handler(), http.MethodGet, "/api/tasks/"+created.ID, nil, http.StatusOK, &loaded)
	if loaded.Planning.Draft == nil || loaded.Planning.Draft.ID != created.Planning.Draft.ID || loaded.Planning.Draft.SelectionDigest != created.Selection.Selected[0].Digest && loaded.Planning.Draft.SelectionDigest == "" {
		t.Fatalf("loaded plan drifted: created=%#v loaded=%#v", created.Planning, loaded.Planning)
	}
	draftPath := "/api/tasks/" + created.ID + "/plan/drafts/" + created.Planning.Draft.ID
	var saved saveTechnicalPlanDraftCommandView
	requestJSONWithHeaders(t, server.Handler(), http.MethodPatch, draftPath, map[string]any{"expectedEditVersion": 1, "technicalSteps": []string{"Implement immutable revisions and restart recovery."}, "decisions": []string{"Use explicit submission."}, "risks": []string{"Stale tabs may conflict."}, "unknowns": []string{"No unresolved planning unknowns."}}, map[string]string{"Idempotency-Key": "save-restart-plan"}, http.StatusOK, &saved)
	if saved.Draft.EditVersion != 2 {
		t.Fatalf("saved Draft = %#v", saved.Draft)
	}
	requestJSONWithHeaders(t, server.Handler(), http.MethodPatch, draftPath, map[string]any{"expectedEditVersion": 1, "technicalSteps": []string{"stale"}, "decisions": []string{"stale"}, "risks": []string{"stale"}, "unknowns": []string{"stale"}}, map[string]string{"Idempotency-Key": "stale-restart-plan"}, http.StatusConflict, nil)
	var submitted submitTechnicalPlanDraftCommandView
	requestJSONWithHeaders(t, server.Handler(), http.MethodPost, draftPath+"/submit", map[string]any{"expectedEditVersion": 2, "confirmUnchanged": false}, map[string]string{"Idempotency-Key": "submit-restart-plan"}, http.StatusOK, &submitted)
	if submitted.Revision.RevisionNumber != 1 {
		t.Fatalf("submitted Revision = %#v", submitted.Revision)
	}
	revision1 := submitted.Revision
	var requested reviewTechnicalPlanRevisionCommandView
	requestJSONWithHeaders(t, server.Handler(), http.MethodPost, "/api/tasks/"+created.ID+"/plan/revisions/"+revision1.ID+"/reviews", map[string]any{"kind": "request_revision", "note": "Clarify restart recovery."}, map[string]string{"Idempotency-Key": "request-restart-revision"}, http.StatusOK, &requested)
	if requested.Draft == nil || requested.Draft.PredecessorRevisionID != revision1.ID || requested.Review.Kind != "request_revision" {
		t.Fatalf("revision request = %#v", requested)
	}
	requestJSONWithHeaders(t, server.Handler(), http.MethodPost, "/api/tasks/"+created.ID+"/plan/revisions/"+revision1.ID+"/reviews", map[string]any{"kind": "accept", "note": "Conflicting terminal review."}, map[string]string{"Idempotency-Key": "conflicting-restart-review"}, http.StatusConflict, nil)
	successorPath := "/api/tasks/" + created.ID + "/plan/drafts/" + requested.Draft.ID
	requestJSONWithHeaders(t, server.Handler(), http.MethodPost, successorPath+"/submit", map[string]any{"expectedEditVersion": 1, "confirmUnchanged": false}, map[string]string{"Idempotency-Key": "reject-unconfirmed-unchanged"}, http.StatusBadRequest, nil)
	var successor submitTechnicalPlanDraftCommandView
	requestJSONWithHeaders(t, server.Handler(), http.MethodPost, successorPath+"/submit", map[string]any{"expectedEditVersion": 1, "confirmUnchanged": true}, map[string]string{"Idempotency-Key": "submit-unchanged-successor"}, http.StatusOK, &successor)
	revision2 := successor.Revision
	var accepted reviewTechnicalPlanRevisionCommandView
	requestJSONWithHeaders(t, server.Handler(), http.MethodPost, "/api/tasks/"+created.ID+"/plan/revisions/"+revision2.ID+"/reviews", map[string]any{"kind": "accept", "note": "The successor plan is bounded and reviewable."}, map[string]string{"Idempotency-Key": "accept-successor-plan"}, http.StatusOK, &accepted)
	if accepted.Acceptance == nil || accepted.Acceptance.RevisionID != revision2.ID || accepted.Review.Reviewer != localActor || !revision2.Unchanged {
		t.Fatalf("accepted plan = %#v", accepted)
	}
	requestJSONWithHeaders(t, server.Handler(), http.MethodPost, "/api/tasks/"+created.ID+"/plan/revisions/"+revision2.ID+"/reviews", map[string]any{"kind": "accept", "note": "The successor plan is bounded and reviewable."}, map[string]string{"Idempotency-Key": "accept-successor-plan"}, http.StatusOK, nil)
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := newTestServer(context.Background(), databasePath, webRoot, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var persisted taskRefView
	requestJSON(t, reopened.Handler(), http.MethodGet, "/api/tasks/"+created.ID, nil, http.StatusOK, &persisted)
	if persisted.Planning.Acceptance == nil || persisted.Planning.Acceptance.RevisionID != revision2.ID || len(persisted.Planning.Revisions) != 2 || persisted.Planning.Revisions[0].Content.TechnicalSteps[0] != revision1.Content.TechnicalSteps[0] {
		t.Fatalf("reopened plan = %#v", persisted.Planning)
	}
}

func TestPlanningCommandsRequireCallerIdempotencyAndReplayExactResponse(t *testing.T) {
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
	var room struct {
		ID              string `json:"id"`
		InitialRevision struct {
			ID string `json:"id"`
		} `json:"initialRevision"`
	}
	requestJSON(t, server.Handler(), http.MethodPost, "/api/rooms", map[string]any{"name": "Replay Room", "workspaceRoot": root}, http.StatusCreated, &room)
	var task taskRefView
	requestJSON(t, server.Handler(), http.MethodPost, "/api/rooms/"+room.ID+"/tasks", map[string]any{
		"title": "Replay planning", "goal": "Make ambiguous command responses safe to retry.", "criteria": []string{"Responses replay exactly"}, "revisionIds": []string{room.InitialRevision.ID},
	}, http.StatusCreated, &task)
	draftPath := "/api/tasks/" + task.ID + "/plan/drafts/" + task.Planning.Draft.ID
	firstSave := map[string]any{
		"expectedEditVersion": 1, "technicalSteps": []string{"first saved step"}, "decisions": []string{"exact DTO"},
		"risks": []string{"ambiguous response"}, "unknowns": []string{"none"},
	}
	requestJSON(t, server.Handler(), http.MethodPatch, draftPath, firstSave, http.StatusBadRequest, nil)
	firstSaveBody := planningRawRequest(t, server.Handler(), http.MethodPatch, draftPath, firstSave, "save-first", http.StatusOK)
	secondSave := map[string]any{
		"expectedEditVersion": 2, "technicalSteps": []string{"second saved step"}, "decisions": []string{"exact DTO"},
		"risks": []string{"ambiguous response"}, "unknowns": []string{"none"},
	}
	planningRawRequest(t, server.Handler(), http.MethodPatch, draftPath, secondSave, "save-second", http.StatusOK)
	replayedSaveBody := planningRawRequest(t, server.Handler(), http.MethodPatch, draftPath, firstSave, "save-first", http.StatusOK)
	if !bytes.Equal(firstSaveBody, replayedSaveBody) {
		t.Fatalf("Save replay changed response:\nfirst=%s\nreplay=%s", firstSaveBody, replayedSaveBody)
	}
	changedReuse := map[string]any{
		"expectedEditVersion": 1, "technicalSteps": []string{"changed semantics"}, "decisions": []string{"exact DTO"},
		"risks": []string{"ambiguous response"}, "unknowns": []string{"none"},
	}
	planningRawRequest(t, server.Handler(), http.MethodPatch, draftPath, changedReuse, "save-first", http.StatusConflict)

	submitBody := map[string]any{"expectedEditVersion": 3, "confirmUnchanged": false}
	requestJSON(t, server.Handler(), http.MethodPost, draftPath+"/submit", submitBody, http.StatusBadRequest, nil)
	firstSubmitBody := planningRawRequest(t, server.Handler(), http.MethodPost, draftPath+"/submit", submitBody, "submit-first", http.StatusOK)
	requestJSON(t, server.Handler(), http.MethodGet, "/api/tasks/"+task.ID, nil, http.StatusOK, &task)
	revision := task.Planning.Revisions[0]
	reviewPath := "/api/tasks/" + task.ID + "/plan/revisions/" + revision.ID + "/reviews"
	reviewBody := map[string]any{"kind": "request_revision", "note": "Create the exact successor Draft."}
	requestJSON(t, server.Handler(), http.MethodPost, reviewPath, reviewBody, http.StatusBadRequest, nil)
	firstReviewBody := planningRawRequest(t, server.Handler(), http.MethodPost, reviewPath, reviewBody, "review-first", http.StatusOK)
	requestJSON(t, server.Handler(), http.MethodGet, "/api/tasks/"+task.ID, nil, http.StatusOK, &task)
	successorPath := "/api/tasks/" + task.ID + "/plan/drafts/" + task.Planning.Draft.ID
	planningRawRequest(t, server.Handler(), http.MethodPatch, successorPath, map[string]any{
		"expectedEditVersion": 1, "technicalSteps": []string{"later successor edit"}, "decisions": []string{"exact DTO"},
		"risks": []string{"ambiguous response"}, "unknowns": []string{"none"},
	}, "save-successor", http.StatusOK)
	if replay := planningRawRequest(t, server.Handler(), http.MethodPost, reviewPath, reviewBody, "review-first", http.StatusOK); !bytes.Equal(firstReviewBody, replay) {
		t.Fatalf("Review replay changed response:\nfirst=%s\nreplay=%s", firstReviewBody, replay)
	}
	if replay := planningRawRequest(t, server.Handler(), http.MethodPost, draftPath+"/submit", submitBody, "submit-first", http.StatusOK); !bytes.Equal(firstSubmitBody, replay) {
		t.Fatalf("Submit replay changed response:\nfirst=%s\nreplay=%s", firstSubmitBody, replay)
	}
	planningRawRequest(t, server.Handler(), http.MethodPost, reviewPath, map[string]any{"kind": "request_revision", "note": "changed semantics"}, "review-first", http.StatusConflict)
}

func planningRawRequest(t *testing.T, handler http.Handler, method, path string, body any, idempotencyKey string, wantStatus int) []byte {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if body == nil {
		encoded = nil
	}
	request := newLocalRequest(method, path, bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", idempotencyKey)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != wantStatus {
		t.Fatalf("%s %s status=%d want=%d body=%s", method, path, response.Code, wantStatus, response.Body.Bytes())
	}
	return bytes.Clone(response.Body.Bytes())
}

func requestJSONWithHeaders(t *testing.T, handler http.Handler, method, path string, body any, headers map[string]string, wantStatus int, target any) {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if body == nil {
		encoded = nil
	}
	request := newLocalRequest(method, path, bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	data := recorder.Body.Bytes()
	if recorder.Code != wantStatus {
		t.Fatalf("%s %s status = %d, want %d; body=%s", method, path, recorder.Code, wantStatus, data)
	}
	if target != nil {
		if err := json.Unmarshal(data, target); err != nil {
			t.Fatalf("decode %s %s: %v; body=%s", method, path, err, data)
		}
	}
}

func TestRunRequiresAcceptedTaskPlanThroughPublicAPI(t *testing.T) {
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

	var room struct {
		ID              string `json:"id"`
		InitialRevision struct {
			ID string `json:"id"`
		} `json:"initialRevision"`
	}
	requestJSON(t, server.Handler(), http.MethodPost, "/api/rooms", map[string]any{
		"name": "Planning Gate", "workspaceRoot": root,
	}, http.StatusCreated, &room)
	var task struct {
		ID string `json:"id"`
	}
	requestJSON(t, server.Handler(), http.MethodPost, "/api/rooms/"+room.ID+"/tasks", map[string]any{
		"title": "Gate execution", "goal": "Require accepted planning authority.",
		"criteria": []string{"Draft and revision states are blocked"}, "revisionIds": []string{room.InitialRevision.ID},
	}, http.StatusCreated, &task)

	var failure struct {
		Error string `json:"error"`
	}
	requestJSON(t, server.Handler(), http.MethodPost, "/api/tasks/"+task.ID+"/runs", nil, http.StatusConflict, &failure)
	if failure.Error != "task has no accepted technical plan revision" {
		t.Fatalf("draft gate error = %q", failure.Error)
	}
	var planning taskRefView
	requestJSON(t, server.Handler(), http.MethodGet, "/api/tasks/"+task.ID, nil, http.StatusOK, &planning)
	var submitted submitTechnicalPlanDraftCommandView
	requestJSONWithHeaders(t, server.Handler(), http.MethodPost, "/api/tasks/"+task.ID+"/plan/drafts/"+planning.Planning.Draft.ID+"/submit", map[string]any{"expectedEditVersion": planning.Planning.Draft.EditVersion, "confirmUnchanged": false}, map[string]string{"Idempotency-Key": "submit-gated-plan"}, http.StatusOK, &submitted)
	revision1 := submitted.Revision
	var requested reviewTechnicalPlanRevisionCommandView
	requestJSONWithHeaders(t, server.Handler(), http.MethodPost, "/api/tasks/"+task.ID+"/plan/revisions/"+revision1.ID+"/reviews", map[string]any{
		"kind": "request_revision", "note": "Clarify the execution boundary.",
	}, map[string]string{"Idempotency-Key": "request-gated-revision"}, http.StatusOK, &requested)
	requestJSON(t, server.Handler(), http.MethodPost, "/api/tasks/"+task.ID+"/runs", nil, http.StatusConflict, &failure)
	var successor submitTechnicalPlanDraftCommandView
	requestJSONWithHeaders(t, server.Handler(), http.MethodPost, "/api/tasks/"+task.ID+"/plan/drafts/"+requested.Draft.ID+"/submit", map[string]any{"expectedEditVersion": requested.Draft.EditVersion, "confirmUnchanged": true}, map[string]string{"Idempotency-Key": "submit-gated-successor"}, http.StatusOK, &successor)
	revision2 := successor.Revision
	requestJSONWithHeaders(t, server.Handler(), http.MethodPost, "/api/tasks/"+task.ID+"/plan/revisions/"+revision2.ID+"/reviews", map[string]any{
		"kind": "accept", "note": "The execution boundary is explicit.",
	}, map[string]string{"Idempotency-Key": "accept-gated-successor"}, http.StatusOK, nil)
	var run runView
	requestJSON(t, server.Handler(), http.MethodPost, "/api/tasks/"+task.ID+"/runs", nil, http.StatusCreated, &run)
	if run.ID == "" || run.Task.ID != task.ID {
		t.Fatalf("accepted planning contract did not start Run: %#v", run)
	}
}

func seedV6ExistingTask(t *testing.T, path, workspaceRoot string) (domain.RoomID, domain.TaskID) {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE, checksum BLOB NOT NULL CHECK(length(checksum)=32), applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	entries, err := migrations.Files.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	applied := 0
	for _, entry := range entries {
		if entry.IsDir() || len(entry.Name()) < 5 || entry.Name()[:5] > "0006_" || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		body, err := migrations.Files.ReadFile(entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, string(body)); err != nil {
			t.Fatalf("apply %s: %v", entry.Name(), err)
		}
		version, err := strconv.Atoi(entry.Name()[:4])
		if err != nil {
			t.Fatal(err)
		}
		checksum := sha256.Sum256(body)
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`, version, entry.Name(), checksum[:], time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
		applied++
	}
	if applied != 6 {
		t.Fatalf("applied %d V6 migrations", applied)
	}
	roomID := domain.NewRoomID()
	taskID := domain.NewTaskID()
	criterionID := domain.NewCriterionID()
	now := time.Date(2026, 8, 1, 1, 2, 3, 0, time.UTC).Format(time.RFC3339Nano)
	if _, err := db.ExecContext(ctx, `INSERT INTO rooms(id,name,description,workspace_root,created_at,updated_at) VALUES(?,?,?,?,?,?)`, roomID.String(), "Existing V6 Room", "Existing V6 Room Brief", workspaceRoot, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO tasks(id,room_id,title,goal,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, taskID.String(), roomID.String(), "Existing V6 Task", "Start after migration", "open", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO task_criteria(criterion_id,task_id,position,title,description) VALUES(?,?,?,?,?)`, criterionID.String(), taskID.String(), 0, "Snapshot is valid", "The migrated Task starts with frozen context"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	return roomID, taskID
}

func TestGetRunEventsFiltersAndBoundsCommittedNormalizedEvents(t *testing.T) {
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

	var room, task struct {
		ID              string `json:"id"`
		InitialRevision struct {
			ID string `json:"id"`
		} `json:"initialRevision"`
	}
	requestJSON(t, server.Handler(), http.MethodPost, "/api/rooms", map[string]any{
		"name": "Events", "workspaceRoot": root,
	}, http.StatusCreated, &room)
	requestJSON(t, server.Handler(), http.MethodPost, "/api/rooms/"+room.ID+"/tasks", map[string]any{
		"title": "Events", "goal": "Test committed events", "criteria": []string{"events"}, "revisionIds": []string{room.InitialRevision.ID},
	}, http.StatusCreated, &task)
	acceptTaskPlan(t, server.Handler(), task.ID)
	var started runView
	requestJSON(t, server.Handler(), http.MethodPost, "/api/tasks/"+task.ID+"/runs", nil, http.StatusCreated, &started)
	resolveFakeDecisionGate(t, server.Handler(), started.ID)
	completed := waitForRunState(t, server.Handler(), started.ID, "awaiting_verification")
	if completed.Controls.CanStartVerification {
		t.Fatal("diagnostic Fake Run incorrectly advertised independent verification")
	}
	if completed.VerificationDisposition.State != "not_applicable" || !strings.Contains(completed.VerificationDisposition.Reason, "diagnostic Agent Run") {
		t.Fatalf("unexpected diagnostic verification disposition: %#v", completed.VerificationDisposition)
	}

	runID, err := domain.ParseRunID(started.ID)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := server.store.Reader().ListRunEvents(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	after := committed[len(committed)-1].Sequence()
	now := time.Now().UTC()
	if err := server.store.WithinWriteTx(context.Background(), func(tx storecontract.WriteTx) error {
		for index := 1; index <= 105; index++ {
			normalized := []byte(`{"number":` + strconv.Itoa(index) + `}`)
			if _, err := tx.AppendRunEvent(context.Background(), runID, storecontract.EventDraft{
				ID: domain.NewEventID(), Type: "test.event", Source: "test",
				OccurredAt: now, RecordedAt: now, NormalizedJSON: normalized,
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	type eventResponse struct {
		RunID  string `json:"runId"`
		Events []struct {
			Sequence   int64           `json:"sequence"`
			Type       string          `json:"type"`
			Source     string          `json:"source"`
			Normalized json.RawMessage `json:"normalized"`
		} `json:"events"`
	}
	var filtered eventResponse
	requestJSON(t, server.Handler(), http.MethodGet, fmt.Sprintf("/api/runs/%s/events?after=%d&limit=2", started.ID, after), nil, http.StatusOK, &filtered)
	if filtered.RunID != started.ID || len(filtered.Events) != 2 ||
		filtered.Events[0].Sequence != after+1 || filtered.Events[1].Sequence != after+2 ||
		filtered.Events[0].Type != "test.event" || filtered.Events[0].Source != "test" ||
		string(filtered.Events[0].Normalized) != `{"number":1}` {
		t.Fatalf("filtered events = %#v", filtered)
	}

	var defaulted, capped eventResponse
	requestJSON(t, server.Handler(), http.MethodGet, fmt.Sprintf("/api/runs/%s/events?after=%d", started.ID, after), nil, http.StatusOK, &defaulted)
	requestJSON(t, server.Handler(), http.MethodGet, fmt.Sprintf("/api/runs/%s/events?after=%d&limit=1000", started.ID, after), nil, http.StatusOK, &capped)
	if len(defaulted.Events) != 50 || len(capped.Events) != 100 {
		t.Fatalf("default events=%d capped events=%d", len(defaulted.Events), len(capped.Events))
	}

	for _, query := range []string{"after=-1", "after=nope", "after=", "after=1&after=2", "limit=-1", "limit=nope", "limit=", "limit=1&limit=2"} {
		requestJSON(t, server.Handler(), http.MethodGet, "/api/runs/"+started.ID+"/events?"+query, nil, http.StatusBadRequest, nil)
	}
}

func TestCodexRuntimeFailsClosedWithControlledGateEvidence(t *testing.T) {
	for _, test := range []struct {
		name       string
		writeGate  func(*testing.T, string)
		wantReason string
	}{
		{
			name: "missing gate evidence",
			writeGate: func(t *testing.T, path string) {
				t.Helper()
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatalf("controlled Gate Evidence path must be missing, stat error = %v", err)
				}
			},
			wantReason: "read Codex gate evidence",
		},
		{
			name: "invalid gate evidence",
			writeGate: func(t *testing.T, path string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(`{"schema_version":`), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantReason: "decode Codex gate evidence",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0o700); err != nil {
				t.Fatal(err)
			}
			repoRoot := filepath.Join(root, "repo")
			webRoot := filepath.Join(repoRoot, "web", "dist")
			if err := os.MkdirAll(webRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(webRoot, "index.html"), []byte("<!doctype html><title>test</title>"), 0o600); err != nil {
				t.Fatal(err)
			}
			gatePath := filepath.Join(repoRoot, "docs", "validation", "codex-cli-0.146.0-gate.json")
			test.writeGate(t, gatePath)

			server, err := newTestServer(context.Background(), filepath.Join(root, "chora.db"), webRoot, log.New(io.Discard, "", 0))
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()

			var status struct {
				Codex codexRuntimeStatus `json:"codex"`
			}
			requestJSON(t, server.Handler(), http.MethodGet, "/api/status", nil, http.StatusOK, &status)
			if status.Codex.Enabled || !strings.Contains(status.Codex.Reason, test.wantReason) || status.Codex.HostReadIsolation != "unavailable" {
				t.Fatalf("controlled fail-closed Codex status = %#v", status.Codex)
			}
			assertCodexRuntimeNotStarted(t, server)

			var room, task struct {
				ID              string `json:"id"`
				InitialRevision struct {
					ID string `json:"id"`
				} `json:"initialRevision"`
			}
			requestJSON(t, server.Handler(), http.MethodPost, "/api/rooms", map[string]any{
				"name": "Runtime Disabled", "workspaceRoot": root,
			}, http.StatusCreated, &room)
			requestJSON(t, server.Handler(), http.MethodPost, "/api/rooms/"+room.ID+"/tasks", map[string]any{
				"title": "Fail closed", "goal": "Do not launch Codex", "criteria": []string{"Codex stays disabled"}, "revisionIds": []string{room.InitialRevision.ID},
			}, http.StatusCreated, &task)
			var failure struct {
				Error string `json:"error"`
			}
			requestJSON(t, server.Handler(), http.MethodPost, "/api/tasks/"+task.ID+"/runs", map[string]any{
				"adapter": "codex",
			}, http.StatusBadRequest, &failure)
			if !strings.Contains(failure.Error, `unknown field "adapter"`) {
				t.Fatalf("adapter substitution response = %q", failure.Error)
			}
			assertCodexRuntimeNotStarted(t, server)
		})
	}
}

func assertCodexRuntimeNotStarted(t *testing.T, server *Server) {
	t.Helper()
	if _, err := server.registry.Get("codex"); err == nil {
		t.Fatal("Codex adapter was registered without valid Gate Evidence")
	}
	server.supervisor.mu.RLock()
	defer server.supervisor.mu.RUnlock()
	if _, ok := server.supervisor.byAdapter["codex"]; ok {
		t.Fatal("Codex supervisor was created without valid Gate Evidence")
	}
	if len(server.supervisor.byHandle) != 0 || len(server.supervisor.byIdentity) != 0 || len(server.supervisor.byLaunch) != 0 {
		t.Fatalf("Codex request created process routing state: handles=%d identities=%d launches=%d",
			len(server.supervisor.byHandle), len(server.supervisor.byIdentity), len(server.supervisor.byLaunch))
	}
}

func TestVisibleAgentRunCanBeCancelledAndCleaned(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	webRoot := filepath.Join(root, "web")
	if err := os.Mkdir(webRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(webRoot, "index.html"), []byte("<!doctype html><title>test</title>"), 0o600); err != nil {
		t.Fatal(err)
	}
	server, err := newTestServer(context.Background(), filepath.Join(root, "chora.db"), webRoot, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	handler := server.Handler()

	var room struct {
		ID              string `json:"id"`
		InitialRevision struct {
			ID string `json:"id"`
		} `json:"initialRevision"`
	}
	requestJSON(t, handler, http.MethodPost, "/api/rooms", map[string]any{
		"name": "Visible Agent Room", "description": "Expose bounded execution state.", "workspaceRoot": root,
	}, http.StatusCreated, &room)
	var task struct {
		ID string `json:"id"`
	}
	requestJSON(t, handler, http.MethodPost, "/api/rooms/"+room.ID+"/tasks", map[string]any{
		"title": "Cancel visible Agent", "goal": "Prove strict cancellation remains inspectable.",
		"criteria": []string{"Agent state is visible"}, "revisionIds": []string{room.InitialRevision.ID},
	}, http.StatusCreated, &task)
	acceptTaskPlan(t, handler, task.ID)

	var started runView
	requestJSON(t, handler, http.MethodPost, "/api/tasks/"+task.ID+"/runs", nil, http.StatusCreated, &started)
	if started.Status != domain.RunStateRunning || started.Agent.Profile.Name != "Fake Agent" ||
		started.Agent.Presence != domain.AgentPresenceActive || started.Activity.Phase != domain.AgentPhaseExecuting ||
		!started.Controls.CanCancel || started.AttemptDetail == nil || started.AttemptDetail.Runtime == nil {
		t.Fatalf("visible running projection = %#v", started)
	}
	if started.Agent.RunState != started.Status || started.AttemptDetail.Sequence != 1 ||
		started.AttemptDetail.Runtime.AdapterID != "fake" || started.AttemptDetail.Runtime.Version != "fake-v1" ||
		started.AttemptDetail.ExecutionWorkspace != root || started.AttemptDetail.Sandbox.Status != "unavailable" ||
		started.AttemptDetail.Sandbox.Provider != "not-adopted" || started.AttemptDetail.Sandbox.Mode != "not-reported" ||
		started.AttemptDetail.Sandbox.Image != "not-reported" || started.AttemptDetail.Sandbox.PolicyFingerprint != "not-reported" {
		t.Fatalf("attempt identity projection = %#v", started.AttemptDetail)
	}

	var cancelled runView
	requestJSON(t, handler, http.MethodPost, "/api/runs/"+started.ID+"/cancel", map[string]any{
		"reason": "Operator cancelled the local run.",
	}, http.StatusOK, &cancelled)
	if cancelled.Status != domain.RunStateCancelled || cancelled.Agent.Presence != domain.AgentPresenceOffline ||
		cancelled.Activity.Phase != domain.AgentPhaseCancelled || cancelled.Controls.CanCancel || !cancelled.Controls.CanRetry ||
		cancelled.AttemptDetail == nil || cancelled.AttemptDetail.State != domain.AttemptStateCancelled ||
		cancelled.AttemptDetail.Runtime == nil || cancelled.AttemptDetail.Runtime.State != "stopped" {
		t.Fatalf("cancelled projection = %#v", cancelled)
	}
	if !timelineContains(cancelled.Timeline, "run.stopping") || !timelineContains(cancelled.Timeline, "run.stop_confirmed") {
		t.Fatalf("cancel timeline = %#v", cancelled.Timeline)
	}
	if cancelled.CancelReason != "Operator cancelled the local run." {
		t.Fatalf("cancel reason = %q", cancelled.CancelReason)
	}
	var retrying runView
	requestJSON(t, handler, http.MethodPost, "/api/runs/"+started.ID+"/retry", map[string]any{
		"expectedVersion": cancelled.Version, "instructions": "Continue from a fresh execution workspace.",
	}, http.StatusOK, &retrying)
	if retrying.Status != domain.RunStateRunning || retrying.Attempt != 2 || retrying.Controls.CanRetry {
		t.Fatalf("cancel retry projection = %#v", retrying)
	}
	requestJSON(t, handler, http.MethodPost, "/api/runs/"+started.ID+"/cancel", map[string]any{
		"reason": "",
	}, http.StatusBadRequest, nil)
}

func TestPersistentRoomRejectRetryAcceptSurvivesReopen(t *testing.T) {
	t.Skip("G2-M5 owns human Reject, Agent Retry, and Accept after verified awaiting_review")
	t.Parallel()

	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	webRoot := filepath.Join(root, "web")
	if err := os.Mkdir(webRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(webRoot, "index.html"), []byte("<!doctype html><title>test</title>"), 0o600); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(root, "chora.db")
	server, err := newTestServer(context.Background(), databasePath, webRoot, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}

	const roomBrief = "Keep the Room decision-ready without exposing the raw Agent transcript."
	var room struct {
		ID              string `json:"id"`
		Description     string `json:"description"`
		InitialRevision struct {
			ID string `json:"id"`
		} `json:"initialRevision"`
	}
	requestJSON(t, server.Handler(), http.MethodPost, "/api/rooms", map[string]any{
		"name": "Persistent Room", "description": roomBrief, "workspaceRoot": root,
	}, http.StatusCreated, &room)
	if room.Description != roomBrief {
		t.Fatalf("created room description = %q", room.Description)
	}

	criteria := []string{"Room context remains visible", "Artifacts survive refresh", "Unknown is resolved by retry"}
	var task struct {
		ID string `json:"id"`
	}
	requestJSON(t, server.Handler(), http.MethodPost, "/api/rooms/"+room.ID+"/tasks", map[string]any{
		"title": "Close the persistent Room loop", "goal": "Review structured evidence without a transcript", "criteria": criteria, "revisionIds": []string{room.InitialRevision.ID},
	}, http.StatusCreated, &task)
	acceptTaskPlan(t, server.Handler(), task.ID)
	var status struct {
		Codex codexRuntimeStatus `json:"codex"`
	}
	requestJSON(t, server.Handler(), http.MethodGet, "/api/status", nil, http.StatusOK, &status)
	if status.Codex.Enabled || status.Codex.Reason == "" || status.Codex.HostReadIsolation != "unavailable" {
		t.Fatalf("unexpected fail-closed Codex status: %#v", status.Codex)
	}
	requestJSON(t, server.Handler(), http.MethodPost, "/api/tasks/"+task.ID+"/runs", map[string]any{"adapter": "codex"}, http.StatusBadRequest, nil)

	var started runView
	requestJSON(t, server.Handler(), http.MethodPost, "/api/tasks/"+task.ID+"/runs", nil, http.StatusCreated, &started)
	resolveFakeDecisionGate(t, server.Handler(), started.ID)
	first := waitForRunState(t, server.Handler(), started.ID, "awaiting_review")
	if first.Attempt != 1 || first.Room.Description != roomBrief || !contextContains(first.Context, "brief", "Room Brief", roomBrief) {
		t.Fatalf("first attempt context = %#v", first)
	}
	if len(first.Timeline) < 5 || len(first.Artifacts) != 2 || len(first.Unknowns) != 1 || len(first.Criteria) != len(criteria) {
		t.Fatalf("first projection incomplete: %#v", first)
	}
	if !timelineContains(first.Timeline, "run.awaiting_review") {
		t.Fatalf("timeline does not explain awaiting review: %#v", first.Timeline)
	}
	if first.Agent.Profile.Name != "Fake Agent" || first.Agent.Presence != domain.AgentPresenceWaiting ||
		first.Activity.Phase != domain.AgentPhaseReviewReady || first.Activity.LatestEvent == nil ||
		first.Activity.LatestEvent.Type != "run.awaiting_review" || first.Gate != nil ||
		len(first.DecisionHistory) != 1 || first.DecisionHistory[0].Status != domain.DecisionGateResolved ||
		first.DecisionHistory[0].SelectedOptionID != "continue" || first.Controls.CanCancel || !first.Controls.CanReview {
		t.Fatalf("awaiting review visibility = %#v", first)
	}
	requestJSON(t, server.Handler(), http.MethodGet, "/api/runs/"+started.ID+"/transcript", nil, http.StatusNotFound, nil)

	const rejectionNote = "Resolve the final unknown using only the operator delta."
	var rejected runView
	requestJSON(t, server.Handler(), http.MethodPost, "/api/runs/"+started.ID+"/review", map[string]any{
		"kind": "reject", "comment": rejectionNote,
	}, http.StatusOK, &rejected)
	if rejected.Status != "revision_required" || rejected.Review == nil || rejected.Review.Comment != rejectionNote {
		t.Fatalf("rejected view = %#v", rejected)
	}

	const instructions = "Re-check the unresolved criterion and return a concise corrected result."
	var retrying runView
	requestJSON(t, server.Handler(), http.MethodPost, "/api/runs/"+started.ID+"/retry", map[string]any{
		"expectedVersion": rejected.Version, "instructions": instructions,
	}, http.StatusOK, &retrying)

	wantDelta, err := json.Marshal(retryDeltaEnvelope{
		SchemaVersion: "chora.retry-delta.v1", RejectionNote: rejectionNote,
		UnmetCriterionIDs: []string{first.Criteria[len(first.Criteria)-1].ID}, Instructions: instructions,
	})
	if err != nil {
		t.Fatal(err)
	}
	server.fakeSupervisor.mu.Lock()
	gotDelta := append([]byte(nil), server.fakeSupervisor.invocation.Stdin()...)
	server.fakeSupervisor.mu.Unlock()
	if !bytes.Equal(gotDelta, wantDelta) {
		t.Fatalf("retry stdin = %s, want %s", gotDelta, wantDelta)
	}
	for _, forbidden := range []string{"Product validation first", "Review structured evidence without a transcript", "snapshot", "charter", "transcript"} {
		if bytes.Contains(bytes.ToLower(gotDelta), []byte(strings.ToLower(forbidden))) {
			t.Fatalf("retry stdin contains forbidden context %q: %s", forbidden, gotDelta)
		}
	}

	second := waitForRunState(t, server.Handler(), started.ID, "awaiting_review")
	if second.Attempt != 2 || second.Retry == nil || second.Retry.RejectionNote != rejectionNote || second.Retry.Instructions != instructions {
		t.Fatalf("second attempt retry summary = %#v", second)
	}
	if len(second.Retry.UnmetCriterionIDs) != 1 || second.Retry.UnmetCriterionIDs[0] != first.Criteria[len(first.Criteria)-1].ID {
		t.Fatalf("second attempt unmet criteria = %#v", second.Retry.UnmetCriterionIDs)
	}
	if second.Review != nil {
		t.Fatalf("second awaiting review leaked old decision: %#v", second.Review)
	}
	if len(second.AttemptHistory) != 2 || second.AttemptHistory[0].Sequence != 1 || second.AttemptHistory[1].Sequence != 2 ||
		second.AttemptHistory[1].PredecessorID != second.AttemptHistory[0].ID || len(second.ReviewHistory) != 1 || second.ReviewHistory[0].Kind != "reject" {
		t.Fatalf("retry overwrote attempt or review history: attempts=%#v reviews=%#v", second.AttemptHistory, second.ReviewHistory)
	}
	if len(second.Unknowns) != 0 {
		t.Fatalf("second attempt unknowns = %#v", second.Unknowns)
	}
	for _, criterion := range second.Criteria {
		if criterion.Status != "passed" {
			t.Fatalf("criterion not resolved: %#v", criterion)
		}
	}
	if !timelineContains(second.Timeline, "review.rejected") {
		t.Fatalf("rejection is not preserved as history: %#v", second.Timeline)
	}
	if last := second.Timeline[len(second.Timeline)-1]; last.Detail == rejectionNote || last.Type != "run.awaiting_review" {
		t.Fatalf("latest timeline item polluted by old rejection: %#v", last)
	}

	const acceptanceNote = "The retry resolved the gap and the structured evidence is sufficient."
	var accepted runView
	requestJSON(t, server.Handler(), http.MethodPost, "/api/runs/"+started.ID+"/review", map[string]any{
		"kind": "accept", "note": acceptanceNote,
	}, http.StatusOK, &accepted)
	if accepted.Status != "accepted" || accepted.Review == nil || accepted.Review.Kind != "accept" || accepted.Review.Comment != acceptanceNote {
		t.Fatalf("accepted view = %#v", accepted)
	}

	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := newTestServer(context.Background(), databasePath, webRoot, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var restored runView
	requestJSON(t, reopened.Handler(), http.MethodGet, "/api/runs/"+started.ID, nil, http.StatusOK, &restored)
	if restored.Status != "accepted" || restored.Review == nil || restored.Review.Comment != acceptanceNote {
		t.Fatalf("restored run = %#v", restored)
	}
	if restored.Attempt != 2 || restored.Room.Description != roomBrief || restored.Retry == nil || restored.Retry.RejectionNote != rejectionNote {
		t.Fatalf("restored retry context = %#v", restored)
	}
	if len(restored.Artifacts) != 2 || len(restored.Context) != 1 || !contextContains(restored.Context, "brief", "Room Brief", roomBrief) || !timelineContains(restored.Timeline, "review.rejected") || !timelineContains(restored.Timeline, "review.accepted") || len(restored.Candidates) != 1 || restored.Candidates[0].State != domain.CandidateStatePending {
		t.Fatalf("restored projection incomplete: %#v", restored)
	}
}

func TestExecutionDecisionStopCreatesRetryableSuccessorWithoutResultReview(t *testing.T) {
	t.Skip("G2-M5 owns Agent Retry after a revision_required verification outcome")
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

	var room struct {
		ID              string           `json:"id"`
		InitialRevision roomRevisionView `json:"initialRevision"`
	}
	requestJSON(t, server.Handler(), http.MethodPost, "/api/rooms", map[string]any{"name": "Stop Gate", "description": "Keep the execution choice separate from review.", "workspaceRoot": root}, http.StatusCreated, &room)
	var task taskRefView
	requestJSON(t, server.Handler(), http.MethodPost, "/api/rooms/"+room.ID+"/tasks", map[string]any{"title": "Stop then retry", "goal": "Create a controlled successor Attempt.", "criteria": []string{"retry remains available"}, "revisionIds": []string{room.InitialRevision.ID}}, http.StatusCreated, &task)
	acceptTaskPlan(t, server.Handler(), task.ID)
	var run runView
	requestJSON(t, server.Handler(), http.MethodPost, "/api/tasks/"+task.ID+"/runs", nil, http.StatusCreated, &run)
	const stopNote = "Stop execution and create a bounded successor Attempt."
	resolveFakeDecisionGateOption(t, server.Handler(), run.ID, "stop", stopNote)
	run = waitForRunState(t, server.Handler(), run.ID, "revision_required")
	if run.Review != nil || len(run.ReviewHistory) != 0 || len(run.DecisionHistory) != 1 || run.DecisionHistory[0].SelectedOptionID != "stop" || run.DecisionHistory[0].Note != stopNote || !run.Controls.CanRetry {
		t.Fatalf("execution stop became a Result Review or lost retry authority: %#v", run)
	}
	requestJSON(t, server.Handler(), http.MethodPost, "/api/runs/"+run.ID+"/retry", map[string]any{"expectedVersion": run.Version, "instructions": "Resume with the corrected bounded execution path."}, http.StatusOK, &run)
	run = waitForRunState(t, server.Handler(), run.ID, "awaiting_review")
	if run.Attempt != 2 || run.Retry == nil || run.Retry.RejectionNote != stopNote || len(run.AttemptHistory) != 2 || run.AttemptHistory[1].PredecessorID != run.AttemptHistory[0].ID || len(run.ReviewHistory) != 0 {
		t.Fatalf("Gate stop retry did not preserve a controlled successor Attempt: %#v", run)
	}
}

func TestTrustedContextLoopConfirmSelectSnapshotAndReopen(t *testing.T) {
	t.Skip("G2-M5 owns Accept and candidate promotion after verified awaiting_review")
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

	var room struct {
		ID              string           `json:"id"`
		InitialRevision roomRevisionView `json:"initialRevision"`
	}
	requestJSON(t, server.Handler(), http.MethodPost, "/api/rooms", map[string]any{
		"name": "Trusted Context Room", "description": "Initial human-authored Room Brief.", "workspaceRoot": root,
	}, http.StatusCreated, &room)
	var task1 taskRefView
	requestJSON(t, server.Handler(), http.MethodPost, "/api/rooms/"+room.ID+"/tasks", map[string]any{
		"title": "Produce candidate", "goal": "Create reviewed context", "criteria": []string{"candidate is reviewable"}, "revisionIds": []string{room.InitialRevision.ID},
	}, http.StatusCreated, &task1)
	acceptTaskPlan(t, server.Handler(), task1.ID)
	var run1 runView
	requestJSON(t, server.Handler(), http.MethodPost, "/api/tasks/"+task1.ID+"/runs", nil, http.StatusCreated, &run1)
	resolveFakeDecisionGate(t, server.Handler(), run1.ID)
	run1 = waitForRunState(t, server.Handler(), run1.ID, "awaiting_review")
	requestJSON(t, server.Handler(), http.MethodPost, "/api/runs/"+run1.ID+"/review", map[string]any{
		"kind": "accept", "note": "The Run is accepted; its proposal still requires confirmation.",
	}, http.StatusOK, &run1)
	if len(run1.Artifacts) != 2 || len(run1.Candidates) != 1 || run1.Candidates[0].State != domain.CandidateStatePending || run1.Candidates[0].Revision != nil || run1.Candidates[0].SourceArtifactID != run1.Artifacts[1].ID || run1.Candidates[0].SourceArtifactID == run1.Artifacts[0].ID {
		t.Fatalf("accepted Run trusted candidate automatically: %#v", run1.Candidates)
	}

	candidate := run1.Candidates[0]
	requestJSON(t, server.Handler(), http.MethodPatch, "/api/candidates/"+candidate.ID, map[string]any{
		"expectedVersion": candidate.Version, "title": strings.Repeat("x", domain.MaxCandidateTitleRunes+1), "body": "bounded",
	}, http.StatusBadRequest, nil)
	const editedTitle = "Human-edited Room decision"
	const editedBody = "Only this human-edited content may become trusted context."
	requestJSON(t, server.Handler(), http.MethodPatch, "/api/candidates/"+candidate.ID, map[string]any{
		"expectedVersion": candidate.Version, "title": editedTitle, "body": editedBody,
	}, http.StatusOK, &run1)
	candidate = run1.Candidates[0]
	if candidate.Version != 2 || candidate.Title != editedTitle || candidate.Body != editedBody || candidate.State != domain.CandidateStatePending {
		t.Fatalf("candidate edit not preserved: %#v", candidate)
	}
	requestJSON(t, server.Handler(), http.MethodPost, "/api/candidates/"+candidate.ID+"/decision", map[string]any{
		"expectedVersion": candidate.Version, "kind": "confirm", "note": "Confirmed after human edit.",
	}, http.StatusOK, &run1)
	candidate = run1.Candidates[0]
	if candidate.State != domain.CandidateStateConfirmed || candidate.Decision == nil || candidate.Decision.Kind != "confirm" || candidate.Decision.ActorID != localActor || candidate.Decision.SessionID != localSession || candidate.Revision == nil || candidate.Revision.Title != editedTitle || candidate.Revision.Body != editedBody || candidate.Revision.Digest == "" || candidate.Revision.Provenance.CandidateID != candidate.ID || candidate.Revision.Provenance.SourceRunID != run1.ID {
		t.Fatalf("confirmed revision incomplete: %#v", candidate)
	}
	confirmedRevision := *candidate.Revision
	requestJSON(t, server.Handler(), http.MethodPost, "/api/candidates/"+candidate.ID+"/decision", map[string]any{
		"expectedVersion": candidate.Version, "kind": "confirm", "note": "duplicate must fail",
	}, http.StatusConflict, nil)
	requestJSON(t, server.Handler(), http.MethodPatch, "/api/candidates/"+candidate.ID, map[string]any{
		"expectedVersion": candidate.Version, "title": "mutated", "body": "must not apply",
	}, http.StatusConflict, nil)
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := newTestServer(context.Background(), databasePath, webRoot, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	var reopenedRoom roomDetailView
	requestJSON(t, restarted.Handler(), http.MethodGet, "/api/rooms/"+room.ID, nil, http.StatusOK, &reopenedRoom)
	if reopenedRoom.ID != room.ID || len(reopenedRoom.Revisions) != 2 || reopenedRoom.Revisions[0].ID != room.InitialRevision.ID || reopenedRoom.Revisions[1] != confirmedRevision {
		t.Fatalf("Room did not reopen by persisted ID: %#v", reopenedRoom)
	}

	var task2 taskRefView
	requestJSON(t, restarted.Handler(), http.MethodPost, "/api/rooms/"+room.ID+"/tasks", map[string]any{
		"title": "Use confirmed context", "goal": "Freeze an explicit selection", "criteria": []string{"snapshot is immutable"}, "revisionIds": []string{confirmedRevision.ID},
	}, http.StatusCreated, &task2)
	if len(task2.Selection.Selected) != 1 || task2.Selection.Selected[0].RevisionID != confirmedRevision.ID || len(task2.Selection.Excluded) != 1 || task2.Selection.Excluded[0].RevisionID != room.InitialRevision.ID || task2.Selection.Excluded[0].Reason != "explicitly_not_selected" {
		t.Fatalf("explicit selection not frozen: %#v", task2.Selection)
	}
	acceptTaskPlan(t, restarted.Handler(), task2.ID)
	var run2 runView
	requestJSON(t, restarted.Handler(), http.MethodPost, "/api/tasks/"+task2.ID+"/runs", nil, http.StatusCreated, &run2)
	run2 = waitForRunState(t, restarted.Handler(), run2.ID, "awaiting_review")
	if run2.Snapshot == nil || run2.Snapshot.ID == "" || run2.Snapshot.Digest == "" || len(run2.Snapshot.Selected) != 1 || run2.Snapshot.Selected[0].RevisionID != confirmedRevision.ID || run2.Snapshot.Selected[0].Digest != confirmedRevision.Digest || len(run2.Snapshot.Excluded) != 1 {
		t.Fatalf("snapshot manifest incomplete: %#v", run2.Snapshot)
	}
	if run2.ContextConsumption == nil || run2.ContextConsumption.SnapshotID != run2.Snapshot.ID || run2.ContextConsumption.SnapshotDigest != run2.Snapshot.Digest ||
		run2.ContextConsumption.CandidateRevisionID != confirmedRevision.ID || !reflect.DeepEqual(run2.ContextConsumption.IncludedRevisionIDs, []string{confirmedRevision.ID}) ||
		!reflect.DeepEqual(run2.ContextConsumption.ExcludedRevisionIDs, []string{room.InitialRevision.ID}) || run2.ContextConsumption.ExcludedContentObserved ||
		run2.ContextConsumption.Derivation != execution.DeriveConfirmedContext(confirmedRevision.ID, editedTitle, editedBody) {
		t.Fatalf("Production Fake did not consume the frozen Snapshot: %#v", run2.ContextConsumption)
	}
	snapshotBefore := *run2.Snapshot
	consumptionBefore := *run2.ContextConsumption
	requestJSON(t, restarted.Handler(), http.MethodPost, "/api/runs/"+run2.ID+"/review", map[string]any{
		"kind": "accept", "note": "The Snapshot evidence proves selected-only consumption.",
	}, http.StatusOK, &run2)
	if run2.Status != domain.RunStateAccepted {
		t.Fatalf("Task 2 Run was not accepted: %#v", run2)
	}

	if err := restarted.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`UPDATE context_revisions SET body='tampered' WHERE id=?`, confirmedRevision.ID); err == nil || !strings.Contains(err.Error(), "immutable") {
		raw.Close()
		t.Fatalf("confirmed revision update was not rejected: %v", err)
	}
	if _, err := raw.Exec(`UPDATE context_snapshots SET canonical_json=json('{}') WHERE id=?`, snapshotBefore.ID); err == nil || !strings.Contains(err.Error(), "immutable") {
		raw.Close()
		t.Fatalf("snapshot update was not rejected: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := newTestServer(context.Background(), databasePath, webRoot, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var restored1, restored2 runView
	requestJSON(t, reopened.Handler(), http.MethodGet, "/api/runs/"+run1.ID, nil, http.StatusOK, &restored1)
	requestJSON(t, reopened.Handler(), http.MethodGet, "/api/runs/"+run2.ID, nil, http.StatusOK, &restored2)
	if len(restored1.Candidates) != 1 || restored1.Candidates[0].Revision == nil || *restored1.Candidates[0].Revision != confirmedRevision {
		t.Fatalf("candidate/decision/revision changed after reopen: %#v", restored1.Candidates)
	}
	if restored2.Status != domain.RunStateAccepted || restored2.Selection == nil || restored2.Snapshot == nil || !reflect.DeepEqual(*restored2.Snapshot, snapshotBefore) ||
		restored2.Selection.Selected[0].RevisionID != confirmedRevision.ID || restored2.ContextConsumption == nil || !reflect.DeepEqual(*restored2.ContextConsumption, consumptionBefore) {
		t.Fatalf("selection/snapshot/evidence changed after reopen: selection=%#v snapshot=%#v evidence=%#v", restored2.Selection, restored2.Snapshot, restored2.ContextConsumption)
	}
}

func TestCandidateDecisionNoteUnicodeBoundaryHTTPDirectSQLiteAndReopen(t *testing.T) {
	t.Skip("G2-M5 owns candidate confirmation and dismissal after verified awaiting_review")
	for _, kind := range []domain.CandidateDecisionKind{domain.CandidateDecisionConfirm, domain.CandidateDecisionDismiss} {
		t.Run(string(kind), func(t *testing.T) {
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
			databasePath := filepath.Join(root, "decision-note.db")
			server, err := newTestServer(context.Background(), databasePath, webRoot, log.New(io.Discard, "", 0))
			if err != nil {
				t.Fatal(err)
			}

			var room struct {
				ID              string           `json:"id"`
				InitialRevision roomRevisionView `json:"initialRevision"`
			}
			requestJSON(t, server.Handler(), http.MethodPost, "/api/rooms", map[string]any{"name": "Decision note", "description": "Boundary", "workspaceRoot": root}, http.StatusCreated, &room)
			var task taskRefView
			requestJSON(t, server.Handler(), http.MethodPost, "/api/rooms/"+room.ID+"/tasks", map[string]any{"title": "Candidate", "goal": "Test note boundary", "criteria": []string{"candidate"}, "revisionIds": []string{room.InitialRevision.ID}}, http.StatusCreated, &task)
			acceptTaskPlan(t, server.Handler(), task.ID)
			var run runView
			requestJSON(t, server.Handler(), http.MethodPost, "/api/tasks/"+task.ID+"/runs", nil, http.StatusCreated, &run)
			resolveFakeDecisionGate(t, server.Handler(), run.ID)
			run = waitForRunState(t, server.Handler(), run.ID, "awaiting_review")
			requestJSON(t, server.Handler(), http.MethodPost, "/api/runs/"+run.ID+"/review", map[string]any{"kind": "accept", "note": "Accepted Run only."}, http.StatusOK, &run)
			if len(run.Candidates) != 1 {
				t.Fatalf("candidates=%#v", run.Candidates)
			}
			candidate := run.Candidates[0]
			limit := strings.Repeat("界", domain.MaxCandidateDecisionNoteRunes)
			overLimit := limit + "界"
			requestJSON(t, server.Handler(), http.MethodPost, "/api/candidates/"+candidate.ID+"/decision", map[string]any{"expectedVersion": candidate.Version, "kind": kind, "note": overLimit}, http.StatusBadRequest, nil)

			raw, err := sql.Open("sqlite", databasePath)
			if err != nil {
				t.Fatal(err)
			}
			_, directErr := raw.Exec(`INSERT INTO candidate_decisions(id,candidate_id,kind,expected_candidate_version,note,actor_id,session_id,decided_at) VALUES(?,?,?,?,?,?,?,?)`, domain.NewCandidateDecisionID().String(), candidate.ID, string(kind), candidate.Version, overLimit, "direct-writer", "direct-session", time.Now().UTC().Format(time.RFC3339Nano))
			if err := raw.Close(); err != nil {
				t.Fatal(err)
			}
			if directErr == nil || !strings.Contains(directErr.Error(), "CHECK constraint failed") {
				t.Fatalf("direct SQLite over-limit result=%v", directErr)
			}
			if kind == domain.CandidateDecisionDismiss {
				raw, err := sql.Open("sqlite", databasePath)
				if err != nil {
					t.Fatal(err)
				}
				_, requiredErr := raw.Exec(`INSERT INTO candidate_decisions(id,candidate_id,kind,expected_candidate_version,note,actor_id,session_id,decided_at) VALUES(?,?,?,?,?,?,?,?)`, domain.NewCandidateDecisionID().String(), candidate.ID, string(kind), candidate.Version, "", "direct-writer", "direct-session", time.Now().UTC().Format(time.RFC3339Nano))
				if err := raw.Close(); err != nil {
					t.Fatal(err)
				}
				if requiredErr == nil || !strings.Contains(requiredErr.Error(), "CHECK constraint failed") {
					t.Fatalf("direct SQLite empty dismiss result=%v", requiredErr)
				}
			}

			requestJSON(t, server.Handler(), http.MethodPost, "/api/candidates/"+candidate.ID+"/decision", map[string]any{"expectedVersion": candidate.Version, "kind": kind, "note": limit}, http.StatusOK, &run)
			if len(run.Candidates) != 1 || run.Candidates[0].Decision == nil || run.Candidates[0].Decision.Kind != string(kind) || run.Candidates[0].Decision.Note != limit {
				t.Fatalf("exact-limit decision=%#v", run.Candidates)
			}
			if err := server.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := newTestServer(context.Background(), databasePath, webRoot, log.New(io.Discard, "", 0))
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			var restored runView
			requestJSON(t, reopened.Handler(), http.MethodGet, "/api/runs/"+run.ID, nil, http.StatusOK, &restored)
			if len(restored.Candidates) != 1 || restored.Candidates[0].Decision == nil || restored.Candidates[0].Decision.Note != limit || restored.Candidates[0].Decision.Kind != string(kind) {
				t.Fatalf("restored decision=%#v", restored.Candidates)
			}
		})
	}
}

func TestAcceptedFakePlanCannotBeSubstitutedWithCodexAtRunCreation(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	webRoot := filepath.Join(root, "web")
	if err := os.Mkdir(webRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(webRoot, "index.html"), []byte("<!doctype html><title>test</title>"), 0o600); err != nil {
		t.Fatal(err)
	}
	sourceRoot := filepath.Join(root, "source")
	if err := os.Mkdir(sourceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "README.md"), []byte("dogfood\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitPath, err := runCommand("git", "-C", sourceRoot, "init")
	if err != nil || gitPath == "" {
		t.Fatalf("git init: %q %v", gitPath, err)
	}
	if _, err := runCommand("git", "-C", sourceRoot, "add", "README.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCommand("git", "-C", sourceRoot, "-c", "user.name=Chora Test", "-c", "user.email=chora@example.test", "commit", "-m", "init"); err != nil {
		t.Fatal(err)
	}
	authRoot := filepath.Join(root, "codex-home-source")
	if err := os.Mkdir(authRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(authRoot, "auth.json"), []byte(`{"test":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", authRoot)

	server, err := newTestServer(context.Background(), filepath.Join(root, "chora.db"), webRoot, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	terminal, err := fakeTerminalResult([]domain.AcceptanceCriterion{}, false)
	if err != nil {
		t.Fatal(err)
	}
	server.registry.Set(agentfake.NewAdapter(agentfake.Plan{
		AdapterID: "codex", Executable: "/fake", Arguments: []string{"run", "--json"},
		ResumeMode: execution.ResumeExplicitSession, TerminalResult: terminal,
	}))
	server.supervisor.mu.Lock()
	server.supervisor.byAdapter["codex"] = server.fakeSupervisor
	server.supervisor.mu.Unlock()
	server.codexStatus = codexRuntimeStatus{Enabled: true, Reason: "test gate", HostReadIsolation: "unavailable"}

	var room struct {
		ID              string `json:"id"`
		InitialRevision struct {
			ID string `json:"id"`
		} `json:"initialRevision"`
	}
	requestJSON(t, server.Handler(), http.MethodPost, "/api/rooms", map[string]any{
		"name": "Real Room", "description": "Use the bounded real path.", "workspaceRoot": sourceRoot,
	}, http.StatusCreated, &room)
	var task struct {
		ID string `json:"id"`
	}
	requestJSON(t, server.Handler(), http.MethodPost, "/api/rooms/"+room.ID+"/tasks", map[string]any{
		"title": "Dogfood", "goal": "Change the disposable checkout", "criteria": []string{"Worktree is isolated"}, "revisionIds": []string{room.InitialRevision.ID},
	}, http.StatusCreated, &task)
	acceptTaskPlan(t, server.Handler(), task.ID)
	var failure struct {
		Error string `json:"error"`
	}
	requestJSON(t, server.Handler(), http.MethodPost, "/api/tasks/"+task.ID+"/runs", map[string]any{"adapter": "codex"}, http.StatusBadRequest, &failure)
	if !strings.Contains(failure.Error, `unknown field "adapter"`) {
		t.Fatalf("accepted Fake binding was substitutable: %q", failure.Error)
	}
}

func acceptTaskPlan(t *testing.T, handler http.Handler, taskID string) {
	t.Helper()
	var task taskRefView
	requestJSON(t, handler, http.MethodGet, "/api/tasks/"+taskID, nil, http.StatusOK, &task)
	if task.Planning.Acceptance != nil {
		return
	}
	if task.Planning.Draft == nil {
		t.Fatalf("Task %s has neither accepted planning nor an open Draft: %#v", taskID, task.Planning)
	}
	var submitted submitTechnicalPlanDraftCommandView
	requestJSONWithHeaders(t, handler, http.MethodPost, "/api/tasks/"+taskID+"/plan/drafts/"+task.Planning.Draft.ID+"/submit", map[string]any{
		"expectedEditVersion": task.Planning.Draft.EditVersion, "confirmUnchanged": task.Planning.Draft.PredecessorRevisionID != "",
	}, map[string]string{"Idempotency-Key": "submit-plan-" + task.Planning.Draft.ID}, http.StatusOK, &submitted)
	revision := submitted.Revision
	requestJSONWithHeaders(t, handler, http.MethodPost, "/api/tasks/"+taskID+"/plan/revisions/"+revision.ID+"/reviews", map[string]any{
		"kind": "accept", "note": "The planning contract is accepted for this test.",
	}, map[string]string{"Idempotency-Key": "accept-plan-" + revision.ID}, http.StatusOK, nil)
}

func createRegisteredPiTask(t *testing.T, handler http.Handler, workspaceRoot string) string {
	t.Helper()
	v4 := readSpecCodingJSON(t, "chora-m1-real-task.v4.json")
	var materialized struct {
		TaskID   string `json:"taskId"`
		Snapshot struct {
			Digest string `json:"digest"`
		} `json:"snapshot"`
	}
	requestJSON(t, handler, http.MethodPost, "/api/spec-coding/materializations", map[string]any{
		"contract": json.RawMessage(v4), "workspaceRoot": workspaceRoot,
		"contextEntryId": "context_entry_018f0c4a-1a30-7c3d-8e4f-1234567890ab", "contextRevisionId": "context_revision_018f0c4a-1a31-7c3d-8e4f-1234567890ab",
		"charterId": "charter_018f0c4a-1a32-7c3d-8e4f-1234567890ab", "frozenAt": "2026-08-11T00:00:00Z",
	}, http.StatusCreated, &materialized)
	v5 := versionSpecCodingContract(t, v4, materialized.Snapshot.Digest)
	requestJSON(t, handler, http.MethodPost, "/api/spec-coding/contracts", map[string]any{"contract": json.RawMessage(v5)}, http.StatusOK, nil)
	acceptTaskPlan(t, handler, materialized.TaskID)
	return materialized.TaskID
}

func waitForRunState(t *testing.T, handler http.Handler, runID string, wanted string) runView {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var last runView
	for time.Now().Before(deadline) {
		requestJSON(t, handler, http.MethodGet, "/api/runs/"+runID, nil, http.StatusOK, &last)
		if string(last.Status) == wanted {
			return last
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("run %s did not reach %s; last status=%s verification=%#v blockers=%v",
		runID, wanted, last.Status, last.Verification, last.Blockers)
	return runView{}
}

func resolveFakeDecisionGate(t *testing.T, handler http.Handler, runID string) runView {
	t.Helper()
	return resolveFakeDecisionGateOption(t, handler, runID, "continue", "Continue from the persisted bounded choice.")
}

func resolveFakeDecisionGateOption(t *testing.T, handler http.Handler, runID, optionID, note string) runView {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		var view runView
		requestJSON(t, handler, http.MethodGet, "/api/runs/"+runID, nil, http.StatusOK, &view)
		if view.Gate == nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		if view.Status != domain.RunStateRunning || view.Gate.Kind != "execution_decision" || view.Gate.Status != domain.DecisionGateOpen ||
			len(view.Gate.Options) != 2 || view.Agent.Presence != domain.AgentPresenceWaiting ||
			view.Activity.Phase != domain.AgentPhaseWaitingForDecision || view.Controls.CanReview {
			t.Fatalf("execution Decision Gate is not distinct from Result Review: %#v", view)
		}
		var resolved runView
		requestJSON(t, handler, http.MethodPost, "/api/decision-gates/"+view.Gate.ID+"/resolve", map[string]any{
			"optionId": optionID, "note": note,
		}, http.StatusOK, &resolved)
		if resolved.Gate != nil || len(resolved.DecisionHistory) != 1 || resolved.DecisionHistory[0].Status != domain.DecisionGateResolved ||
			resolved.DecisionHistory[0].SelectedOptionID != optionID || resolved.DecisionHistory[0].Note != note || resolved.DecisionHistory[0].ActorID != localActor ||
			resolved.DecisionHistory[0].SessionID != localSession {
			t.Fatalf("resolved execution Decision Gate = %#v", resolved)
		}
		return resolved
	}
	t.Fatalf("run %s did not expose an execution Decision Gate", runID)
	return runView{}
}

func requestJSON(t *testing.T, handler http.Handler, method, path string, body any, wantStatus int, target any) {
	t.Helper()
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		payload = bytes.NewReader(encoded)
	}
	request := newLocalRequest(method, path, payload)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	response := recorder.Result()
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != wantStatus {
		t.Fatalf("%s %s status = %d, want %d; body=%s", method, path, response.StatusCode, wantStatus, data)
	}
	if target != nil {
		if err := json.Unmarshal(data, target); err != nil {
			t.Fatalf("decode %s %s: %v; body=%s", method, path, err, data)
		}
	}
}

func timelineContains(items []timelineView, eventType string) bool {
	for _, item := range items {
		if item.Type == eventType {
			return true
		}
	}
	return false
}

func contextContains(items []contextView, kind, title, body string) bool {
	for _, item := range items {
		if item.Kind == kind && item.Title == title && item.Body == body {
			return true
		}
	}
	return false
}

func contextHasTitle(items []contextView, title string) bool {
	for _, item := range items {
		if item.Title == title {
			return true
		}
	}
	return false
}

// newLocalRequest models the loopback authority used by the served product.
// Absolute URLs retain their explicit authority for hostile-request tests.
func newLocalRequest(method, target string, body io.Reader) *http.Request {
	request := httptest.NewRequest(method, target, body)
	if strings.HasPrefix(target, "/") {
		request.Host = "localhost"
	}
	return request
}
