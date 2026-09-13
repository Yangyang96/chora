package localweb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	agentfake "github.com/Yangyang96/chora/internal/agent/fake"
	agentpi "github.com/Yangyang96/chora/internal/agent/pi"
	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/preflight"
	"github.com/Yangyang96/chora/internal/speccoding"
)

func TestProfileSwitchAPIRejectsStaleVersionAndReplaysSuccessorStart(t *testing.T) {
	server, trustedRuntime, dockerRuntime, taskID, prepared := newProfileSwitchAPIServer(t)
	handler := server.Handler()
	path := "/api/runs/" + prepared.Run.ID().String() + "/agent-execution-profile"

	before, err := server.runView(context.Background(), prepared.Run.ID())
	if err != nil {
		t.Fatal(err)
	}
	preferenceBefore, err := server.store.Reader().GetTaskAgentExecutionProfilePreference(context.Background(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	requestJSONWithHeaders(t, handler, http.MethodPost, path, map[string]any{
		"profile": string(domain.AgentExecutionProfileMinimal), "reason": "stale request", "expectedVersion": prepared.Run.Version() + 1,
	}, map[string]string{"Idempotency-Key": "stale-profile-switch"}, http.StatusConflict, nil)
	afterStale, err := server.runView(context.Background(), prepared.Run.ID())
	if err != nil {
		t.Fatal(err)
	}
	preferenceAfterStale, err := server.store.Reader().GetTaskAgentExecutionProfilePreference(context.Background(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	if afterStale.Attempt != before.Attempt || afterStale.AttemptDetail == nil || before.AttemptDetail == nil ||
		afterStale.AttemptDetail.ID != before.AttemptDetail.ID || len(afterStale.AttemptHistory) != len(before.AttemptHistory) {
		t.Fatalf("stale switch changed Attempt: before=%#v after=%#v", before.AttemptDetail, afterStale.AttemptDetail)
	}
	if preferenceAfterStale.Profile() != preferenceBefore.Profile() || preferenceAfterStale.Version() != preferenceBefore.Version() {
		t.Fatalf("stale switch changed preference: before=%q/v%d after=%q/v%d", preferenceBefore.Profile(), preferenceBefore.Version(), preferenceAfterStale.Profile(), preferenceAfterStale.Version())
	}
	if trustedRuntime.startCount() != 0 || dockerRuntime.startCount() != 0 {
		t.Fatalf("stale switch started a runtime: trusted=%d docker=%d", trustedRuntime.startCount(), dockerRuntime.startCount())
	}

	body := map[string]any{
		"profile": string(domain.AgentExecutionProfileMinimal), "reason": "use the selected sandbox provider", "expectedVersion": prepared.Run.Version(),
	}
	headers := map[string]string{"Idempotency-Key": "switch-to-minimal"}
	var switched, replay runView
	requestJSONWithHeaders(t, handler, http.MethodPost, path, body, headers, http.StatusOK, &switched)
	requestJSONWithHeaders(t, handler, http.MethodPost, path, body, headers, http.StatusOK, &replay)

	if switched.Attempt != 2 || switched.AttemptDetail == nil || switched.AgentExecution == nil || switched.AttemptDetail.AgentExecution == nil ||
		switched.AgentExecution.Profile != domain.AgentExecutionProfileMinimal || switched.AttemptDetail.AgentExecution.Profile != domain.AgentExecutionProfileMinimal {
		t.Fatalf("switch did not return the selected successor: %#v", switched)
	}
	if len(switched.AttemptHistory) != 2 || switched.AttemptHistory[0].AgentExecution == nil || switched.AttemptHistory[1].AgentExecution == nil ||
		switched.AttemptHistory[0].AgentExecution.Profile != domain.AgentExecutionProfileTrustedLocal ||
		switched.AttemptHistory[1].AgentExecution.Profile != domain.AgentExecutionProfileMinimal {
		t.Fatalf("switch history=%#v", switched.AttemptHistory)
	}
	starts, invocation := dockerRuntime.snapshot()
	if starts != 1 || invocation.Target().ProviderID() != domain.DockerExecutionProvider || trustedRuntime.startCount() != 0 {
		t.Fatalf("selected provider routing: trusted starts=%d docker starts=%d target=%q", trustedRuntime.startCount(), starts, invocation.Target().ProviderID())
	}
	if replay.Attempt != switched.Attempt || replay.AttemptDetail == nil || replay.AttemptDetail.ID != switched.AttemptDetail.ID || dockerRuntime.startCount() != 1 {
		t.Fatalf("replay created or started another successor: switch=%#v replay=%#v starts=%d", switched.AttemptDetail, replay.AttemptDetail, dockerRuntime.startCount())
	}
	preferenceAfterReplay, err := server.store.Reader().GetTaskAgentExecutionProfilePreference(context.Background(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	if preferenceAfterReplay.Profile() != domain.AgentExecutionProfileMinimal || preferenceAfterReplay.Version() != preferenceBefore.Version()+1 {
		t.Fatalf("replay changed successor preference=%q/v%d", preferenceAfterReplay.Profile(), preferenceAfterReplay.Version())
	}
}

func newProfileSwitchAPIServer(t *testing.T) (*Server, *profileSwitchSupervisor, *profileSwitchSupervisor, domain.TaskID, app.PrepareRunResult) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	repositoryRoot := filepath.Join(root, "repository")
	if err := os.Mkdir(repositoryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repositoryRoot, "README.md"), []byte("profile switch fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	envelope, err := speccoding.NewBoundEnvelope(repositoryRoot, speccoding.RepositoryIdentity{
		Name: "profile-switch-api", SourceRevision: "0123456789abcdef0123456789abcdef01234567",
	}, "0.84.2")
	if err != nil {
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

	terminal, err := fakeTerminalResult(nil, false)
	if err != nil {
		t.Fatal(err)
	}
	server.registry.Set(agentfake.NewAdapter(agentfake.Plan{
		AdapterID: agentpi.AdapterID, Executable: "/fake/pi", Arguments: []string{"run"}, TerminalResult: terminal,
	}))
	trustedTarget, err := execution.NewExecutionTarget(agentpi.AdapterID, domain.TrustedHostExecutionProvider)
	if err != nil {
		t.Fatal(err)
	}
	dockerTarget, err := execution.NewExecutionTarget(agentpi.AdapterID, domain.DockerExecutionProvider)
	if err != nil {
		t.Fatal(err)
	}
	trustedRuntime := newProfileSwitchSupervisor()
	dockerRuntime := newProfileSwitchSupervisor()
	router, err := newTargetRoutingSupervisor(map[execution.ExecutionTarget]execution.ProcessSupervisor{
		trustedTarget: trustedRuntime,
		dockerTarget:  dockerRuntime,
	})
	if err != nil {
		t.Fatal(err)
	}
	server.supervisor = router
	server.taskWorktreesReady = true
	server.piPreflight = func(context.Context) preflight.Report { return preflight.Report{Status: preflight.StatusPassed} }
	server.service = app.NewService(app.Dependencies{
		Lifecycle: context.Background(), Store: server.store, Context: app.ContextAssembler{}, Agents: server.registry, Supervisor: router,
		Presence: localPresence{}, Authorizer: localAuthorizer{}, IDs: app.RandomIDs{},
		SpecCodingEnvelopeResolver: profileSwitchEnvelopeResolver{envelope: envelope},
		TaskWorktrees:              profileSwitchTaskWorktrees{repository: envelope.Repository()},
	})

	ctx := context.Background()
	room, err := server.service.CreateRoom(ctx, app.CreateRoomRequest{
		CommandMeta: profileSwitchMeta("create-room"), Name: "Profile switch API", Description: "Exercise current HTTP profile switching.", WorkspaceRoot: repositoryRoot,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.service.AcknowledgeTrustedLocal(ctx, app.AcknowledgeTrustedLocalRequest{
		CommandMeta: profileSwitchMeta("ack-trusted-local"), PolicyVersion: domain.TrustedLocalDisclosurePolicy,
	}); err != nil {
		t.Fatal(err)
	}
	created, err := server.service.CreateTask(ctx, app.CreateTaskRequest{
		CommandMeta: profileSwitchMeta("create-task"), RoomID: room.Room.ID(), Title: "Switch the execution profile",
		ExecutionProfile: app.TaskExecutionProfileRealSpecCoding, AgentExecutionProfile: domain.AgentExecutionProfileTrustedLocal,
		RevisionIDs: []domain.ContextRevisionID{room.InitialRevision.ID()},
		RealSpecCoding: &app.RealSpecCodingInput{
			Requirement: "Keep profile switching atomic and idempotent.", Constraints: []string{"Preserve the predecessor."},
			OutOfScope: []string{"Release fixtures."}, Criteria: []app.RealSpecCodingCriterion{{Title: "Switch succeeds", Description: "The selected successor starts once."}},
			WritableFiles: []string{"README.md"}, NoChecks: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	submitted, err := server.service.SubmitTechnicalPlanDraft(ctx, app.SubmitTechnicalPlanDraftRequest{
		CommandMeta: profileSwitchMeta("submit-plan"), DraftID: created.Draft.ID(), ExpectedEditVersion: created.Draft.EditVersion(),
	})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := server.service.ReviewTechnicalPlanRevision(ctx, app.ReviewTechnicalPlanRevisionRequest{
		CommandMeta: profileSwitchMeta("accept-plan"), RevisionID: submitted.Revision.ID(), Kind: domain.TechnicalPlanReviewAccept, Note: "Accept the profile switch fixture.",
	})
	if err != nil {
		t.Fatal(err)
	}
	createdRun, err := server.service.CreateRun(ctx, app.CreateRunRequest{
		CommandMeta: profileSwitchMeta("create-run"), TaskID: created.Task.ID(), RevisionID: accepted.Review.RevisionID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := server.service.PrepareRun(ctx, app.PrepareRunRequest{
		CommandMeta: profileSwitchMeta("prepare-run"), RunID: createdRun.Run.ID(), ExpectedVersion: createdRun.Run.Version(),
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	if _, err := raw.ExecContext(ctx, `UPDATE runs SET state='revision_required' WHERE id=?`, createdRun.Run.ID().String()); err != nil {
		t.Fatal(err)
	}
	return server, trustedRuntime, dockerRuntime, created.Task.ID(), prepared
}

type profileSwitchEnvelopeResolver struct{ envelope speccoding.BoundEnvelope }

func (resolver profileSwitchEnvelopeResolver) ResolveSpecCodingEnvelope(_ context.Context, room domain.Room) (app.SpecCodingEnvelope, error) {
	if !resolver.envelope.MatchesRepositoryRoot(room.WorkspaceRoot()) {
		return nil, speccoding.ErrInvalidInstalledEnvelope
	}
	return resolver.envelope, nil
}

type profileSwitchTaskWorktrees struct{ repository speccoding.RepositoryIdentity }

func (manager profileSwitchTaskWorktrees) Plan(_ context.Context, task domain.Task, createdAt time.Time) (domain.TaskWorktreeBinding, error) {
	return domain.NewTaskWorktreeBinding(domain.TaskWorktreeBindingParams{
		TaskID: task.ID().String(), RepositoryIdentity: manager.repository.Name, PinnedBaseRevision: manager.repository.SourceRevision,
		RelativeLocator: manager.repository.Name + "-" + task.ID().String() + "-api-test", ConfiguredRootFingerprint: sha256.Sum256([]byte("profile-switch-api")), CreatedAt: createdAt,
	})
}

func (profileSwitchTaskWorktrees) Ensure(context.Context, domain.TaskWorktreeBinding) error {
	return nil
}

func (profileSwitchTaskWorktrees) ResolveExecutionRoot(context.Context, domain.Task) (string, error) {
	return "", nil
}

type profileSwitchSupervisor struct {
	*fakeSupervisor
	mu     sync.Mutex
	starts int
}

func newProfileSwitchSupervisor() *profileSwitchSupervisor {
	return &profileSwitchSupervisor{fakeSupervisor: newFakeSupervisor()}
}

func (supervisor *profileSwitchSupervisor) Start(ctx context.Context, invocation execution.Invocation, sink execution.RuntimeSink) execution.StartOutcome {
	supervisor.mu.Lock()
	supervisor.starts++
	supervisor.mu.Unlock()
	return supervisor.fakeSupervisor.Start(ctx, invocation, sink)
}

func (supervisor *profileSwitchSupervisor) startCount() int {
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	return supervisor.starts
}

func (supervisor *profileSwitchSupervisor) snapshot() (int, execution.Invocation) {
	supervisor.mu.Lock()
	starts := supervisor.starts
	supervisor.mu.Unlock()
	supervisor.fakeSupervisor.mu.Lock()
	defer supervisor.fakeSupervisor.mu.Unlock()
	return starts, supervisor.fakeSupervisor.invocation
}

func profileSwitchMeta(key string) app.CommandMeta {
	return app.CommandMeta{ActorID: localActor, SessionID: localSession, IdempotencyKey: key, RequestDigest: sha256.Sum256([]byte(key))}
}
