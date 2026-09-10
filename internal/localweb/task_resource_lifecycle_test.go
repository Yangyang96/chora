package localweb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"

	agentpi "github.com/Yangyang96/chora/internal/agent/pi"
	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/gitsource"
	"github.com/Yangyang96/chora/internal/pidiscovery"
	"github.com/Yangyang96/chora/internal/speccoding"
)

func TestTaskResourceLifecycleStartsOnePiSessionAtMultiRepositoryRoot(t *testing.T) {
	server, runtime, dataRoot := newTaskResourceLifecycleServer(t)
	handler := server.Handler()

	requestJSONWithHeaders(t, handler, http.MethodPost, "/api/agent-execution/trusted-local-acknowledgements",
		map[string]any{"policyVersion": domain.TrustedLocalDisclosurePolicy},
		map[string]string{"Idempotency-Key": "resource-lifecycle-ack"}, http.StatusOK, nil)

	var project resourceProjectTestView
	requestJSON(t, handler, http.MethodPost, "/api/v2/projects", map[string]any{"name": "Lifecycle multi repository"}, http.StatusCreated, &project)
	if project.DefaultRoomID == "" {
		t.Fatalf("empty Project omitted General Room: %#v", project)
	}

	type admittedRepository struct {
		root string
		view repositoryResourceView
	}
	admitted := make([]admittedRepository, 0, 2)
	for _, name := range []string{"first", "second"} {
		root, _, _ := newTaskWorktreeTestRepository(t)
		if err := os.WriteFile(filepath.Join(root, "same.txt"), []byte(name+" committed\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		runTaskWorktreeGitTest(t, root, "add", "same.txt")
		runTaskWorktreeGitTest(t, root, "-c", "user.name=Chora Test", "-c", "user.email=chora@example.test", "commit", "-m", name+" resource")
		var response struct {
			Repository repositoryResourceView `json:"repository"`
		}
		requestJSON(t, handler, http.MethodPost, "/api/v2/projects/"+project.ID+"/repositories", map[string]any{"locator": root}, http.StatusOK, &response)
		admitted = append(admitted, admittedRepository{root: root, view: response.Repository})
	}

	selections := []taskResourceSelection{
		{RepoID: admitted[0].view.RepoID, AssociationVersion: admitted[0].view.Version, Role: "write", TargetRef: taskResourceTestTargetRef(t, admitted[0].root), Scope: domain.TaskRepositoryScope{Mode: "repository", MigrationChoice: "not_needed"}, Checks: domain.TaskCheckPolicy{Mode: "auto", SelectionSource: "user"}},
		{RepoID: admitted[1].view.RepoID, AssociationVersion: admitted[1].view.Version, Role: "reference", Scope: domain.TaskRepositoryScope{Mode: "restricted", MigrationChoice: "not_needed"}, Checks: domain.TaskCheckPolicy{Mode: "auto", SelectionSource: "user"}},
	}
	projectID, err := domain.ParseProjectID(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, selected := range selections {
		repositoryID, parseErr := domain.ParseRepositoryID(selected.RepoID)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		if _, lookupErr := server.store.Reader().GetProjectRepository(context.Background(), projectID, repositoryID); lookupErr != nil {
			t.Fatalf("selected repository association %s was not persisted: %v", selected.RepoID, lookupErr)
		}
	}
	roomID, err := domain.ParseRoomID(project.DefaultRoomID)
	if err != nil {
		t.Fatal(err)
	}
	revisions, err := server.store.Reader().ListRoomRevisions(context.Background(), roomID)
	if err != nil {
		t.Fatal(err)
	}
	revisionIDs := make([]string, 0, len(revisions))
	for _, revision := range revisions {
		revisionIDs = append(revisionIDs, revision.ID().String())
	}
	var created taskRefView
	requestJSONWithHeaders(t, handler, http.MethodPost, "/api/v2/rooms/"+project.DefaultRoomID+"/tasks", map[string]any{
		"title": "Exercise v12 lifecycle", "requirement": "Read both same-named files and prepare one reviewable result.",
		"agentExecutionProfile": string(domain.AgentExecutionProfileTrustedLocal), "resources": selections, "revisionIds": revisionIDs,
	}, map[string]string{"Idempotency-Key": "create-resource-lifecycle-task"}, http.StatusCreated, &created)
	if created.ResourceSnapshot == nil || len(created.ResourceSnapshot.Resources) != 2 || created.Planning.Draft == nil {
		t.Fatalf("v2 Task creation omitted frozen resources or Draft: %#v", created)
	}

	var preparing struct {
		Ready     bool
		Preparing bool
		Reason    string
		Path      string
	}
	requestJSON(t, handler, http.MethodGet, "/api/projects/"+project.DefaultRoomID+"/continuity?taskId="+created.ID, nil, http.StatusOK, &preparing)
	if preparing.Ready || !preparing.Preparing || preparing.Path != "" {
		t.Fatalf("new Task continuity: %+v", preparing)
	}

	draft := created.Planning.Draft
	requestJSONWithHeaders(t, handler, http.MethodPatch, "/api/tasks/"+created.ID+"/plan/drafts/"+draft.ID, map[string]any{
		"expectedEditVersion": draft.EditVersion,
		"technicalSteps":      []string{"Inspect both repository ID roots without conflating same.txt.", "Report the selected checks and grouped repository changes."},
		"decisions":           []string{"Use one Task workspace containing two stable repository ID children."},
		"risks":               []string{"A relative filename could be attributed to the wrong repository without its repository ID."},
		"unknowns":            []string{"The focused test Pi does not produce a real implementation result."},
	}, map[string]string{"Idempotency-Key": "save-resource-lifecycle-plan"}, http.StatusOK, nil)
	acceptTaskPlan(t, handler, created.ID)

	taskID, err := domain.ParseTaskID(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := server.store.Reader().GetSpecCodingBinding(context.Background(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	contract, err := speccoding.DecodeCoreContract(binding.ActiveContractJSON)
	if err != nil {
		t.Fatal(err)
	}
	document := contract.Document()
	if document.SchemaVersion != speccoding.CoreContractSchemaVersionV12 || document.Revision != 12 || len(document.Task.Resources) != 2 || document.Task.Repository.Locator != "" {
		t.Fatalf("accepted contract is not host-path-free v12 resources: %#v", document.Task)
	}
	for _, repository := range admitted {
		if bytes.Contains(binding.ActiveContractJSON, []byte(repository.root)) {
			t.Fatalf("v12 execution contract leaked original checkout %q", repository.root)
		}
	}

	var started runView
	requestJSONWithHeaders(t, handler, http.MethodPost, "/api/tasks/"+created.ID+"/runs", map[string]any{},
		map[string]string{"Idempotency-Key": "start-resource-lifecycle-run"}, http.StatusCreated, &started)
	if started.Status != domain.RunStateRunning || started.AttemptDetail == nil || started.AttemptDetail.Runtime == nil || started.AttemptDetail.Runtime.AdapterID != agentpi.AdapterID {
		if failedRunID, parseErr := domain.ParseRunID(started.ID); parseErr == nil {
			if failedAttempts, listErr := server.store.Reader().ListAttemptsForRun(context.Background(), failedRunID); listErr == nil && len(failedAttempts) > 0 {
				t.Logf("registered snapshot=%s/%x attempt snapshot=%s/%x", binding.SnapshotID.String(), binding.SnapshotDigest, failedAttempts[0].ContextSnapshotID().String(), failedAttempts[0].ContextDigest())
			}
		}
		t.Fatalf("v2 Task did not start one Pi runtime: %#v", started)
	}
	runID, err := domain.ParseRunID(started.ID)
	if err != nil {
		t.Fatal(err)
	}
	attempts, err := server.store.Reader().ListAttemptsForRun(context.Background(), runID)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts=%#v err=%v", attempts, err)
	}
	session, err := server.store.Reader().GetRuntimeSessionForAttempt(context.Background(), attempts[0].ID())
	if err != nil || session.ID.String() != started.AttemptDetail.Runtime.SessionID || session.AdapterID != agentpi.AdapterID || session.State != "running" {
		t.Fatalf("runtime session=%#v err=%v", session, err)
	}
	wantRoot := filepath.Join(dataRoot, "task-workspaces", created.ResourceSnapshot.Resources[0].TaskWorkspaceDirectory(taskID))
	if session.WorkingRoot != wantRoot {
		t.Fatalf("Pi working root=%q want Task root %q", session.WorkingRoot, wantRoot)
	}
	for _, resource := range created.ResourceSnapshot.Resources {
		info, err := os.Lstat(filepath.Join(wantRoot, resource.WorkspaceDirectory()))
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("repository child %s is not an owned directory: info=%v err=%v", resource.RepoID, info, err)
		}
	}
	rows, err := server.store.Reader().ListTaskRepositoryWorktrees(context.Background(), taskID)
	if err != nil || len(rows) != 2 || rows[0].State != "ready" || rows[1].State != "ready" {
		t.Fatalf("worktree vector=%#v err=%v", rows, err)
	}

	for _, ownerID := range []string{project.ID, project.DefaultRoomID} {
		root, resolveErr := server.continuityDirectory(context.Background(), ownerID, taskID.String())
		if resolveErr != nil || root != wantRoot {
			t.Fatalf("Task continuity root=%q err=%v want=%q", root, resolveErr, wantRoot)
		}
	}
	if _, resolveErr := server.continuityDirectory(context.Background(), "unrelated-project", taskID.String()); resolveErr == nil {
		t.Fatal("Task continuity accepted unrelated ownership")
	}

	starts, invocation := runtime.snapshot()
	if starts != 1 || invocation.AdapterID() != agentpi.AdapterID || invocation.WorkingRoot() != wantRoot || invocation.Target().ProviderID() != domain.TrustedHostExecutionProvider {
		t.Fatalf("Pi invocations=%d invocation=%#v", starts, invocation)
	}
	promptContract := resourceLifecyclePromptContract(t, invocation.Stdin())
	promptDocument, err := speccoding.DecodeCoreContract(promptContract)
	if err != nil || promptDocument.Document().SchemaVersion != speccoding.CoreContractSchemaVersionV12 || len(promptDocument.Document().Task.Resources) != 2 {
		t.Fatalf("Pi prompt contract is not the accepted v12 resource vector: err=%v", err)
	}
	wantContents := map[string]string{
		admitted[0].view.RepoID: "first committed\n",
		admitted[1].view.RepoID: "second committed\n",
	}
	for _, resource := range promptDocument.Document().Task.Resources {
		content, err := os.ReadFile(filepath.Join(invocation.WorkingRoot(), resource.Locator, "same.txt"))
		if err != nil {
			t.Fatalf("prompt locator %q does not resolve below the one Task root: %v", resource.Locator, err)
		}
		if string(content) != wantContents[resource.RepoID] {
			t.Fatalf("repository %s same.txt aliased another resource: got %q", resource.RepoID, content)
		}
	}
}

type taskResourceLifecycleSupervisor struct {
	*fakeSupervisor
	countMu sync.Mutex
	starts  int
}

func (supervisor *taskResourceLifecycleSupervisor) Start(ctx context.Context, invocation execution.Invocation, sink execution.RuntimeSink) execution.StartOutcome {
	supervisor.countMu.Lock()
	supervisor.starts++
	ordinal := supervisor.starts
	supervisor.countMu.Unlock()
	outcome := supervisor.fakeSupervisor.Start(ctx, invocation, sink)
	// Distinct launches must not claim the same live process identity. The
	// single-launch fixture retains its original identity for existing tests.
	if ordinal > 1 {
		outcome.Handle.Value = fmt.Sprintf("local-fake-handle-%d", ordinal)
		outcome.Identity.Value = fmt.Sprintf("local-fake-process-%d", ordinal)
	}
	return outcome
}

func (supervisor *taskResourceLifecycleSupervisor) snapshot() (int, execution.Invocation) {
	supervisor.countMu.Lock()
	defer supervisor.countMu.Unlock()
	supervisor.fakeSupervisor.mu.Lock()
	defer supervisor.fakeSupervisor.mu.Unlock()
	return supervisor.starts, supervisor.fakeSupervisor.invocation
}

func newTaskResourceLifecycleServer(t *testing.T) (*Server, *taskResourceLifecycleSupervisor, string) {
	t.Helper()
	db := openTaskWorktreeResolverStore(t)
	dataRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	resourceWorkspaces, err := newTaskResourceWorkspaceManager(db, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	executableBytes := []byte("#!/bin/sh\necho focused test Pi\n")
	executable := filepath.Join(dataRoot, "pi")
	if err := os.WriteFile(executable, executableBytes, 0o700); err != nil {
		t.Fatal(err)
	}
	executableDigest := sha256.Sum256(executableBytes)
	pathSource, err := agentpi.NewPathPiSource(agentpi.PathPiSourceParams{ExecutablePath: executable, Version: "0.84.2", ExecutableSHA256: executableDigest})
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := agentpi.New(agentpi.Config{PathPiSource: pathSource, SessionRoot: filepath.Join(dataRoot, "pi-sessions")})
	if err != nil {
		t.Fatal(err)
	}
	registry := newAdapterRegistry()
	registry.Set(adapter)
	runtime := &taskResourceLifecycleSupervisor{fakeSupervisor: newFakeSupervisor()}
	target, err := execution.NewExecutionTarget(agentpi.AdapterID, domain.TrustedHostExecutionProvider)
	if err != nil {
		t.Fatal(err)
	}
	router, err := newTargetRoutingSupervisor(map[execution.ExecutionTarget]execution.ProcessSupervisor{target: runtime})
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, cancel := context.WithCancel(context.Background())
	source := gitsource.Default{}
	service := app.NewService(app.Dependencies{
		RoomCreationMode: app.RoomCreationProjectOnly, Lifecycle: lifecycle, Store: db, Context: app.ContextAssembler{},
		Agents: registry, Supervisor: router, Presence: localPresence{}, Authorizer: localAuthorizer{}, IDs: app.RandomIDs{},
		TaskResourceWorkspaces:     resourceWorkspaces,
		SpecCodingEnvelopeResolver: newBoundSpecCodingEnvelopeResolver(db.Reader(), nil, pidiscovery.Result{State: pidiscovery.StateReady, Version: "0.84.2"}),
		RepositorySource:           source, DataRoot: dataRoot,
	})
	server := &Server{
		ctx: lifecycle, cancel: cancel, store: db, service: service, registry: registry, supervisor: router,
		repositorySource: source, pathPiEnabled: true, taskWorktreesReady: true,
		piStatus: piRuntimeStatus{Enabled: true, Provider: domain.TrustedHostExecutionProvider, HostReadIsolation: "not claimed", Reason: "focused test Pi"},
		logger:   log.New(io.Discard, "", 0),
	}
	t.Cleanup(func() {
		cancel()
		_ = db.Close()
	})
	return server, runtime, dataRoot
}

func resourceLifecyclePromptContract(t *testing.T, stdin []byte) []byte {
	t.Helper()
	for _, line := range bytes.Split(bytes.TrimSpace(stdin), []byte{'\n'}) {
		var frame struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(line, &frame) != nil || frame.Message == "" {
			continue
		}
		_, contract, ok := execution.UnwrapFrozenExecutionPrompt(frame.Message)
		if ok {
			return contract
		}
	}
	t.Fatal("Pi invocation omitted the frozen execution prompt")
	return nil
}

var _ execution.ProcessSupervisor = (*taskResourceLifecycleSupervisor)(nil)
