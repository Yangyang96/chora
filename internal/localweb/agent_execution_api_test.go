package localweb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	agentfake "github.com/Yangyang96/chora/internal/agent/fake"
	agentpi "github.com/Yangyang96/chora/internal/agent/pi"
	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/preflight"
	"github.com/Yangyang96/chora/internal/productinstall"
	"github.com/Yangyang96/chora/internal/speccoding"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func TestTrustedLocalAcknowledgementAPIRequiresCurrentPolicyAndReplaysExactly(t *testing.T) {
	server := newReadinessTestServer(t)
	handler := server.Handler()
	var current currentTrustedLocalAcknowledgementView
	requestJSON(t, handler, http.MethodGet, "/api/agent-execution/trusted-local-acknowledgements/current", nil, http.StatusOK, &current)
	if current.Acknowledged || current.PolicyVersion != domain.TrustedLocalDisclosurePolicy || current.AcknowledgedAt != "" {
		t.Fatalf("missing current acknowledgement=%#v", current)
	}
	wrongPolicy, err := domain.NewTrustedLocalAcknowledgement(domain.TrustedLocalAcknowledgementRecord{
		PolicyVersion: "chora.trusted-local-disclosure.v2", ActorID: localActor, SessionID: "wrong-policy", AcknowledgedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.store.WithinWriteTx(context.Background(), func(tx storecontract.WriteTx) error {
		return tx.InsertTrustedLocalAcknowledgement(context.Background(), wrongPolicy)
	}); err != nil {
		t.Fatal(err)
	}
	requestJSON(t, handler, http.MethodGet, "/api/agent-execution/trusted-local-acknowledgements/current", nil, http.StatusOK, &current)
	if current.Acknowledged {
		t.Fatalf("wrong-policy acknowledgement became current: %#v", current)
	}
	body := map[string]any{"policyVersion": domain.TrustedLocalDisclosurePolicy}
	requestJSON(t, handler, http.MethodPost, "/api/agent-execution/trusted-local-acknowledgements", body, http.StatusBadRequest, nil)
	requestJSONWithHeaders(t, handler, http.MethodPost, "/api/agent-execution/trusted-local-acknowledgements",
		map[string]any{"policyVersion": "chora.trusted-local-disclosure.v2"}, map[string]string{"Idempotency-Key": "ack-policy-bump"}, http.StatusUnprocessableEntity, nil)
	if _, err := server.store.Reader().GetTrustedLocalAcknowledgement(context.Background(), localActor, domain.TrustedLocalDisclosurePolicy); err != storecontract.ErrNotFound {
		t.Fatalf("rejected acknowledgement persisted: %v", err)
	}
	var first, replay trustedLocalAcknowledgementView
	headers := map[string]string{"Idempotency-Key": "ack-current-policy"}
	requestJSONWithHeaders(t, handler, http.MethodPost, "/api/agent-execution/trusted-local-acknowledgements", body, headers, http.StatusOK, &first)
	requestJSONWithHeaders(t, handler, http.MethodPost, "/api/agent-execution/trusted-local-acknowledgements", body, headers, http.StatusOK, &replay)
	if first.PolicyVersion != domain.TrustedLocalDisclosurePolicy || first.ActorID != localActor || first.SessionID != localSession || first.AcknowledgedAt == "" || first.Replayed ||
		!replay.Replayed || replay.PolicyVersion != first.PolicyVersion || replay.ActorID != first.ActorID || replay.SessionID != first.SessionID || replay.AcknowledgedAt != first.AcknowledgedAt {
		t.Fatalf("ack first=%#v replay=%#v", first, replay)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, newLocalRequest(http.MethodGet, "/api/agent-execution/trusted-local-acknowledgements/current", nil))
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "actor") || strings.Contains(response.Body.String(), "session") {
		t.Fatalf("current acknowledgement leaked identity: code=%d body=%s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &current); err != nil || !current.Acknowledged || current.PolicyVersion != domain.TrustedLocalDisclosurePolicy || current.AcknowledgedAt != first.AcknowledgedAt {
		t.Fatalf("current acknowledgement=%#v err=%v", current, err)
	}
}

func TestAgentExecutionProjectionFreezesAllThreeProfilesAndTrustedLocalDisclosure(t *testing.T) {
	status := piRuntimeStatus{Provider: "docker-colima", Image: "sha256:managed", PolicyFingerprint: "sha256:sandbox"}
	for _, profile := range domain.SupportedAgentExecutionProfiles() {
		binding, err := domain.NewAgentExecutionProfileBinding(profile)
		if err != nil {
			t.Fatal(err)
		}
		view := agentExecutionViewOf(binding)
		if view == nil || view.Profile != profile || view.RuntimeSource != binding.RuntimeSource() || view.ExecutionProvider != binding.ExecutionProvider() || view.CapabilityPolicy != binding.CapabilityPolicy() || view.TrustDisclosurePolicy != binding.TrustDisclosurePolicy() {
			t.Fatalf("profile %q projection=%#v", profile, view)
		}
		sandbox := sandboxIdentityViewOf(binding, status)
		if profile == domain.AgentExecutionProfileTrustedLocal {
			if view.Sandboxed || view.DisclosureLabel != "Trusted Local · No Sandbox" || sandbox.Status != "not_applicable" || sandbox.Provider != domain.TrustedHostExecutionProvider || sandbox.Mode != "No Sandbox" || sandbox.Image != "not_applicable" || sandbox.PolicyFingerprint != "not_applicable" {
				t.Fatalf("Trusted Local disclosure=%#v sandbox=%#v", view, sandbox)
			}
			encoded, _ := json.Marshal(struct {
				AgentExecution *agentExecutionView `json:"agentExecution"`
				Sandbox        sandboxIdentityView `json:"sandbox"`
			}{view, sandbox})
			if strings.Contains(string(encoded), "sha256:managed") || strings.Contains(string(encoded), "sha256:sandbox") || strings.Contains(string(encoded), "docker-colima") {
				t.Fatalf("Trusted Local leaked managed claims: %s", encoded)
			}
		} else if !view.Sandboxed || sandbox.Status != "adopted" {
			t.Fatalf("managed profile %q disclosure=%#v sandbox=%#v", profile, view, sandbox)
		}
	}
}

func TestExactProfileReadinessDoesNotUseGlobalPiStatusOrFallback(t *testing.T) {
	server := &Server{registry: newAdapterRegistry(), supervisor: newRoutingSupervisor(map[string]execution.ProcessSupervisor{"fake": newFakeSupervisor()})}
	terminal, err := fakeTerminalResult(nil, false)
	if err != nil {
		t.Fatal(err)
	}
	server.registry.Set(agentfake.NewAdapter(agentfake.Plan{AdapterID: agentpi.AdapterID, Executable: "/fake", Arguments: []string{"run"}, TerminalResult: terminal}))
	trustedTarget, _ := execution.NewExecutionTarget(agentpi.AdapterID, domain.TrustedHostExecutionProvider)
	if err := server.supervisor.registerTarget(trustedTarget, newFakeSupervisor()); err != nil {
		t.Fatal(err)
	}
	server.piStatus = piRuntimeStatus{Enabled: false, Reason: "token sk-super-secret-value Docker unavailable"}
	server.product = true
	server.generationConfigErr = errors.New("managed generation unavailable")
	preflightCalls := 0
	server.piPreflight = func(context.Context) preflight.Report {
		preflightCalls++
		return preflight.Report{Status: preflight.StatusPassed}
	}
	trusted, _ := domain.NewAgentExecutionProfileBinding(domain.AgentExecutionProfileTrustedLocal)
	trustedRecorder := httptest.NewRecorder()
	if !server.requireAgentExecutionRoute(trustedRecorder, newLocalRequest(http.MethodPost, "/trusted", nil), trusted) || preflightCalls != 0 {
		t.Fatalf("Trusted Local was blocked by Docker/global Pi status: code=%d body=%s calls=%d", trustedRecorder.Code, trustedRecorder.Body.String(), preflightCalls)
	}
	server.product = false
	managed, _ := domain.NewAgentExecutionProfileBinding(domain.AgentExecutionProfileStandard)
	managedRecorder := httptest.NewRecorder()
	if server.requireAgentExecutionRoute(managedRecorder, newLocalRequest(http.MethodPost, "/managed", nil), managed) || managedRecorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing managed route did not fail closed: code=%d body=%s", managedRecorder.Code, managedRecorder.Body.String())
	}
	publicError := managedRecorder.Body.String()
	for _, forbidden := range []string{"sk-super-secret-value", "trusted_local", "Trusted Local", "fallback"} {
		if strings.Contains(publicError, forbidden) {
			t.Fatalf("managed route error leaked or suggested fallback: %s", publicError)
		}
	}
	if preflightCalls != 0 {
		t.Fatalf("missing exact managed route invoked Preflight: %d", preflightCalls)
	}
	dockerTarget, _ := execution.NewExecutionTarget(agentpi.AdapterID, domain.DockerExecutionProvider)
	if err := server.supervisor.registerTarget(dockerTarget, newFakeSupervisor()); err != nil {
		t.Fatal(err)
	}
	readyRecorder := httptest.NewRecorder()
	if !server.requireAgentExecutionRoute(readyRecorder, newLocalRequest(http.MethodPost, "/managed", nil), managed) || preflightCalls != 1 {
		t.Fatalf("exact managed route readiness failed: code=%d body=%s calls=%d", readyRecorder.Code, readyRecorder.Body.String(), preflightCalls)
	}
}

func TestAgentExecutionPublicAPIProfilesAndSuccessorOnlySwitch(t *testing.T) {
	server, databasePath := newAgentExecutionProductServer(t)
	handler := server.Handler()
	var createdRoom struct {
		ID string `json:"id"`
	}
	requestJSONWithHeaders(t, handler, http.MethodPost, "/api/rooms", map[string]any{
		"name": "Agent execution profiles", "description": "Exercise the public profile contract.",
	}, map[string]string{"Idempotency-Key": "create-profile-room"}, http.StatusCreated, &createdRoom)
	var room roomDetailView
	requestJSON(t, handler, http.MethodGet, "/api/rooms/"+createdRoom.ID, nil, http.StatusOK, &room)
	if len(room.Revisions) != 1 {
		t.Fatalf("Room revisions=%#v", room.Revisions)
	}

	trustedBody := realTaskAPIBody(room.Revisions[0].ID, "Trusted Local requires current disclosure acknowledgement")
	trustedBody["agentExecutionProfile"] = string(domain.AgentExecutionProfileTrustedLocal)
	requestJSONWithHeaders(t, handler, http.MethodPost, "/api/rooms/"+room.ID+"/tasks", trustedBody,
		map[string]string{"Idempotency-Key": "unacknowledged-trusted-task"}, http.StatusForbidden, nil)
	assertTableCounts(t, databasePath, map[string]int{"tasks": 0, "agent_execution_profile_preferences": 0, "user_spec_coding_intents": 0})
	requestJSONWithHeaders(t, handler, http.MethodPost, "/api/agent-execution/trusted-local-acknowledgements",
		map[string]any{"policyVersion": domain.TrustedLocalDisclosurePolicy}, map[string]string{"Idempotency-Key": "ack-profile-api"}, http.StatusOK, nil)

	created := make(map[domain.AgentExecutionProfile]taskRefView)
	for _, profile := range domain.SupportedAgentExecutionProfiles() {
		body := realTaskAPIBody(room.Revisions[0].ID, "Persist the "+string(profile)+" profile exactly")
		body["agentExecutionProfile"] = string(profile)
		var task taskRefView
		requestJSONWithHeaders(t, handler, http.MethodPost, "/api/rooms/"+room.ID+"/tasks", body,
			map[string]string{"Idempotency-Key": "create-profile-" + string(profile)}, http.StatusCreated, &task)
		if task.AgentExecutionProfile != string(profile) {
			t.Fatalf("created %q Task profile=%q", profile, task.AgentExecutionProfile)
		}
		var fetched taskRefView
		requestJSON(t, handler, http.MethodGet, "/api/tasks/"+task.ID, nil, http.StatusOK, &fetched)
		if fetched.AgentExecutionProfile != string(profile) {
			t.Fatalf("fetched %q Task profile=%q", profile, fetched.AgentExecutionProfile)
		}
		created[profile] = task
	}
	var workspace roomWorkspaceView
	requestJSON(t, handler, http.MethodGet, "/api/rooms/"+room.ID+"/workspace", nil, http.StatusOK, &workspace)
	if len(workspace.Tasks) != 3 {
		t.Fatalf("workspace Tasks=%#v", workspace.Tasks)
	}
	for _, task := range workspace.Tasks {
		if task.AgentExecutionProfile == "" {
			t.Fatalf("workspace Task omitted Agent profile: %#v", task)
		}
	}

	standard := created[domain.AgentExecutionProfileStandard]
	acceptTaskPlan(t, handler, standard.ID)
	var accepted taskRefView
	requestJSON(t, handler, http.MethodGet, "/api/tasks/"+standard.ID, nil, http.StatusOK, &accepted)
	if accepted.Planning.Acceptance == nil {
		t.Fatalf("Standard Task has no accepted planning: %#v", accepted.Planning)
	}
	taskID, err := domain.ParseTaskID(standard.ID)
	if err != nil {
		t.Fatal(err)
	}
	revisionID, err := domain.ParseTechnicalPlanRevisionID(accepted.Planning.Acceptance.RevisionID)
	if err != nil {
		t.Fatal(err)
	}
	meta := func(key string) app.CommandMeta {
		return app.CommandMeta{ActorID: localActor, SessionID: localSession, IdempotencyKey: key, RequestDigest: sha256.Sum256([]byte(key))}
	}
	createdRun, err := server.service.CreateRun(context.Background(), app.CreateRunRequest{
		CommandMeta: meta("create-profile-switch-run"), TaskID: taskID, RevisionID: revisionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := server.service.PrepareRun(context.Background(), app.PrepareRunRequest{
		CommandMeta: meta("prepare-profile-switch-run"), RunID: createdRun.Run.ID(), ExpectedVersion: createdRun.Run.Version(),
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`UPDATE runs SET state='revision_required' WHERE id=?`, createdRun.Run.ID().String()); err != nil {
		t.Fatal(err)
	}
	eligible, err := server.runView(context.Background(), createdRun.Run.ID())
	if err != nil {
		t.Fatal(err)
	}
	if !eligible.Controls.CanSwitchAgentExecutionProfile || eligible.AgentExecution == nil || eligible.AgentExecution.Profile != domain.AgentExecutionProfileStandard {
		t.Fatalf("profile switch eligibility=%#v profile=%#v", eligible.Controls, eligible.AgentExecution)
	}

	switchBody := map[string]any{
		"profile": string(domain.AgentExecutionProfileMinimal), "reason": "reduce the successor capability boundary", "expectedVersion": prepared.Run.Version(),
	}
	headers := map[string]string{"Idempotency-Key": "switch-standard-to-minimal"}
	var switched, replay runView
	path := "/api/runs/" + createdRun.Run.ID().String() + "/agent-execution-profile"
	requestJSONWithHeaders(t, handler, http.MethodPost, path, map[string]any{
		"profile": string(domain.AgentExecutionProfileMinimal), "reason": "stale switch must not mutate", "expectedVersion": prepared.Run.Version() + 100,
	}, map[string]string{"Idempotency-Key": "stale-profile-switch"}, http.StatusConflict, nil)
	assertTableCounts(t, databasePath, map[string]int{"attempts": 1, "agent_execution_profile_preferences": 3})
	requestJSONWithHeaders(t, handler, http.MethodPost, path, switchBody, headers, http.StatusOK, &switched)
	requestJSONWithHeaders(t, handler, http.MethodPost, path, switchBody, headers, http.StatusOK, &replay)
	if switched.AgentExecution == nil || switched.AgentExecution.Profile != domain.AgentExecutionProfileMinimal || switched.Attempt != 2 || switched.Controls.CanSwitchAgentExecutionProfile ||
		replay.AgentExecution == nil || replay.AgentExecution.Profile != domain.AgentExecutionProfileMinimal || replay.AttemptDetail == nil || switched.AttemptDetail == nil || replay.AttemptDetail.ID != switched.AttemptDetail.ID {
		t.Fatalf("switch=%#v replay=%#v", switched, replay)
	}
	if len(switched.AttemptHistory) != 2 || switched.AttemptHistory[0].AgentExecution == nil || switched.AttemptHistory[0].AgentExecution.Profile != domain.AgentExecutionProfileStandard ||
		switched.AttemptHistory[1].AgentExecution == nil || switched.AttemptHistory[1].AgentExecution.Profile != domain.AgentExecutionProfileMinimal {
		t.Fatalf("successor-only Attempt history=%#v", switched.AttemptHistory)
	}
	var history taskRunHistoryView
	requestJSON(t, handler, http.MethodGet, "/api/rooms/"+room.ID+"/tasks/"+standard.ID+"/runs", nil, http.StatusOK, &history)
	if len(history.Runs) != 1 || history.Runs[0].AgentExecution == nil || history.Runs[0].AgentExecution.Profile != domain.AgentExecutionProfileMinimal {
		t.Fatalf("Run history profile=%#v", history.Runs)
	}
	var switchedTask taskRefView
	requestJSON(t, handler, http.MethodGet, "/api/tasks/"+standard.ID, nil, http.StatusOK, &switchedTask)
	if switchedTask.AgentExecutionProfile != string(domain.AgentExecutionProfileMinimal) {
		t.Fatalf("Task successor profile=%q", switchedTask.AgentExecutionProfile)
	}
}

func newAgentExecutionProductServer(t *testing.T) (*Server, string) {
	t.Helper()
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := speccoding.LoadInstalledEnvelope(filepath.Clean(repositoryRoot))
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
	server.repoRoot = filepath.Clean(repositoryRoot)
	server.installedEnvelope = &envelope
	server.product = true
	server.installationGenerationID = "test-generation"
	server.generationConfigErr = nil
	server.generationPublisher = &recordingGenerationPublisher{activeGenerationID: "test-generation"}
	server.taskWorktreesReady = true
	enablePiTestRuntime(t, server)
	server.service = app.NewService(app.Dependencies{
		Lifecycle: context.Background(), Store: server.store, Context: app.ContextAssembler{}, Agents: server.registry, Supervisor: server.supervisor,
		Presence: localPresence{}, Authorizer: localAuthorizer{}, IDs: app.RandomIDs{}, InstalledSpecCodingEnvelope: &envelope,
		TaskWorktrees: publicAPITaskWorktrees{repository: envelope.Repository()},
	})
	return server, databasePath
}

type recordingGenerationPublisher struct {
	installationMu     sync.Mutex
	mu                 sync.Mutex
	err                error
	activeGenerationID string
	snapshots          []productinstall.GenerationReferences
}

func (publisher *recordingGenerationPublisher) PublishReferences(_ context.Context, refs productinstall.GenerationReferences) error {
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	if publisher.err != nil {
		return publisher.err
	}
	publisher.snapshots = append(publisher.snapshots, productinstall.GenerationReferences{
		ServingGenerationIDs:            slices.Clone(refs.ServingGenerationIDs),
		ActiveAttemptGenerationIDs:      slices.Clone(refs.ActiveAttemptGenerationIDs),
		RecoverableAttemptGenerationIDs: slices.Clone(refs.RecoverableAttemptGenerationIDs),
	})
	return nil
}

func (publisher *recordingGenerationPublisher) WithInstallationLock(ctx context.Context, fn func(context.Context) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	publisher.installationMu.Lock()
	defer publisher.installationMu.Unlock()
	return fn(ctx)
}

func (publisher *recordingGenerationPublisher) Load(ctx context.Context) (productinstall.State, error) {
	if err := ctx.Err(); err != nil {
		return productinstall.State{}, err
	}
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	state := productinstall.EmptyState()
	state.ActiveGenerationID = publisher.activeGenerationID
	return state, nil
}

func (publisher *recordingGenerationPublisher) setActiveGeneration(generationID string) {
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	publisher.activeGenerationID = generationID
}
