package claudeprint

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/agent"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/trustedhost"
)

const initStream = `{"type":"system","subtype":"init","session_id":"session-1","tools":["Read","Glob","Grep","Edit","Write","Bash"],"mcp_servers":[],"skills":[],"plugins":[],"slash_commands":[],"permissionMode":"dontAsk","claude_code_version":"2.1.251"}` + "\n"
const messageStream = `{"type":"assistant","session_id":"session-1","message":{"id":"message-1","content":[{"type":"text","text":"Complete synthetic finding."}]}}` + "\n"
const resultStream = `{"type":"result","session_id":"session-1","subtype":"success","is_error":false,"result":"Complete synthetic finding.","permission_denials":[]}` + "\n"
const successStream = initStream + messageStream + resultStream

func fixture(t *testing.T, body string) *Adapter {
	t.Helper()
	root, _ := filepath.EvalSymlinks(t.TempDir())
	home := filepath.Join(root, "home")
	auth := filepath.Join(root, "auth")
	for _, dir := range []string{home, auth} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	executable := filepath.Join(root, "claude-fixture")
	script := "#!/bin/sh\ncase \"$1\" in\n--version) echo '2.1.251 (Claude Code)'; exit;;\n--safe-mode) case \"$*\" in *status) echo '{\"loggedIn\":true,\"authMethod\":\"claude.ai\",\"apiProvider\":\"firstParty\"}'; exit;; esac;;\nesac\n" + body
	if err := os.WriteFile(executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	a, err := New(context.Background(), Config{Executable: executable, AuthHome: auth, Home: home, Model: "fixture-model"})
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func request(t *testing.T, a *Adapter) execution.StartRequest {
	t.Helper()
	now := time.Now().UTC()
	run, err := domain.NewAgentRun(domain.NewRunID(), domain.NewTaskID(), domain.NewCharterID(), now)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := []byte(`{"context":"synthetic bounded task"}`)
	binding, err := a.Binding(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := domain.NewAttempt(domain.AttemptParams{ID: domain.NewAttemptID(), RunID: run.ID(), Sequence: 1, ContextSnapshotID: domain.NewContextSnapshotID(), ContextDigest: sha256.Sum256(snapshot), AdapterID: AdapterID, AgentExecutionProfileBinding: binding, CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	return execution.StartRequest{Run: run, Attempt: attempt, WorkspaceRoot: root, SnapshotDocument: snapshot, ExecutionContractDocument: []byte(`{"schema_version":"chora.spec-coding-core.v12","task":{"outcome_kind":"document","requirement":"Return a short synthetic finding; do not use tools."}}`), LaunchToken: execution.LaunchToken{Value: attempt.ID().String()}}
}
func persisted(t *testing.T, events []execution.NormalizedEvent) []domain.RunEvent {
	t.Helper()
	now := time.Now().UTC()
	run := domain.NewRunID()
	result := []domain.RunEvent{}
	for i, e := range append([]execution.NormalizedEvent{{Type: "attempt.started", NormalizedJSON: []byte(`{}`)}}, events...) {
		source := "adapter"
		if i == 0 {
			source = "app"
		}
		r, err := domain.NewRunEvent(domain.RunEventParams{ID: domain.NewEventID(), RunID: run, Sequence: int64(i + 1), Type: e.Type, Source: source, OccurredAt: now, RecordedAt: now, NormalizedJSON: e.NormalizedJSON})
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, r)
	}
	return result
}
func TestPreparationIdentityAndCapabilities(t *testing.T) {
	a := fixture(t, "exit 0\n")
	ctx := context.Background()
	if err := agent.CheckConformance(ctx, a); err != nil {
		t.Fatal(err)
	}
	registry := agent.NewRegistry()
	if err := registry.Register(a); err != nil {
		t.Fatal(err)
	}
	for _, feature := range []string{"resume", "native_resources", "agent_checks", "interactive_approval"} {
		if a.Describe().Require("trusted_host", "code", feature) == nil {
			t.Fatal("unsupported feature accepted")
		}
	}
	if a.Describe().Require("docker", "code") == nil {
		t.Fatal("Docker accepted")
	}
	if _, err := a.PrepareResume(ctx, execution.ResumeRequest{}); !errors.Is(err, execution.ErrUnsupportedCapability) {
		t.Fatal(err)
	}
	req := request(t, a)
	p, err := a.PrepareBoundStart(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if p.RuntimeFingerprint.SourceIdentity == ([32]byte{}) || p.RuntimeFingerprint.SecurityFingerprint == ([32]byte{}) {
		t.Fatal("missing independent proof")
	}
	if _, ok := p.Invocation.Environment()["OPENAI_API_KEY"]; ok {
		t.Fatal("ambient credentials inherited")
	}
	// Frozen requested fields cannot substitute for changed executable bytes.
	if err := os.WriteFile(a.config.Executable, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := a.PrepareBoundStart(ctx, req); !errors.Is(err, ErrIdentity) {
		t.Fatal("source drift accepted", err)
	}
}
func TestRejectNativeConfigAndMissingAuthentication(t *testing.T) {
	a := fixture(t, "exit 0\n")
	req := request(t, a)
	if err := os.Mkdir(filepath.Join(req.WorkspaceRoot, ".claude"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := a.PrepareStart(context.Background(), req); !errors.Is(err, ErrPolicy) {
		t.Fatal("project config accepted", err)
	}
	if err := os.WriteFile(filepath.Join(a.config.AuthHome, "managed-settings.json"), []byte("untrusted"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Fingerprint(context.Background()); !errors.Is(err, ErrPolicy) {
		t.Fatal("managed policy accepted", err)
	}
	b := fixture(t, "exit 0\n")
	if err := os.WriteFile(b.config.Executable, []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(b.Authentication(context.Background()), ErrAuthentication) {
		t.Fatal("missing auth accepted")
	}
}

type sink struct {
	token execution.LaunchToken
	done  chan struct{}
	once  sync.Once
}

func (s *sink) Binding() execution.LaunchToken     { return s.token }
func (s *sink) Notify(execution.StreamKind, int64) {}
func (s *sink) Exited()                            { s.once.Do(func() { close(s.done) }) }
func TestSupervisorEOFAndBoundLaunch(t *testing.T) {
	a := fixture(t, "cat >/dev/null\nprintf '%s\\n' '"+strings.ReplaceAll(strings.TrimSuffix(successStream, "\n"), "\n", "' '")+"'\n")
	req := request(t, a)
	p, err := a.PrepareBoundStart(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	supervisor, err := trustedhost.New(a.SupervisorConfig(filepath.Join(root, "runtime")))
	if err != nil {
		t.Fatal(err)
	}
	s := &sink{token: req.LaunchToken, done: make(chan struct{})}
	started := supervisor.Start(context.Background(), p.Invocation, s)
	if started.Kind != execution.Started {
		t.Fatal(started)
	}
	defer supervisor.Stop(context.Background(), started.Handle, execution.StopIntent{Kind: execution.StopForCancel})
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
		t.Fatal("stdin EOF not delivered")
	}
	d, err := supervisor.Drain(context.Background(), started.Handle, execution.StreamOffsets{}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := a.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: d.Chunks[execution.StreamStdout], EOF: true})
	if err != nil {
		t.Fatal(err)
	}
	result, err := a.Complete(execution.CompletionRequest{Files: d.TerminalFiles, Events: persisted(t, decoded.Events)})
	if err != nil || result.Kind != execution.TerminalReviewReady {
		t.Fatalf("result=%+v err=%v drain=%+v", result, err, d)
	}
	if err := supervisor.Finalize(context.Background(), started.Handle, execution.RetentionPolicy{}); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorCancelsDescendantAndRejectsEnvironmentDrift(t *testing.T) {
	a := fixture(t, "cat >/dev/null\nsleep 60 &\necho $! > child.pid\nwait\n")
	req := request(t, a)
	p, err := a.PrepareBoundStart(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	s, err := trustedhost.New(a.SupervisorConfig(filepath.Join(root, "runtime")))
	if err != nil {
		t.Fatal(err)
	}
	env := p.Invocation.Environment()
	env["CONTROL_PLANE_TOKEN"] = "synthetic"
	modified, err := execution.NewInvocation(execution.InvocationParams{AdapterID: AdapterID, Executable: a.config.Executable, Arguments: a.arguments(), Environment: env, WorkingRoot: req.WorkspaceRoot, LaunchToken: req.LaunchToken})
	if err != nil {
		t.Fatal(err)
	}
	observer := &sink{token: req.LaunchToken, done: make(chan struct{})}
	if outcome := s.Start(context.Background(), modified, observer); outcome.Kind != execution.StartProvenNoChild {
		t.Fatal("environment drift launched")
	}
	outcome := s.Start(context.Background(), p.Invocation, observer)
	if outcome.Kind != execution.Started {
		t.Fatal(outcome)
	}
	defer s.Stop(context.Background(), outcome.Handle, execution.StopIntent{Kind: execution.StopForCancel})
	var childPID int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		raw, e := os.ReadFile(filepath.Join(req.WorkspaceRoot, "child.pid"))
		if e == nil {
			childPID, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
			if childPID > 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if childPID <= 0 {
		t.Fatal("no descendant created")
	}
	stopped, err := s.Stop(context.Background(), outcome.Handle, execution.StopIntent{Kind: execution.StopForCancel})
	if err != nil || stopped.Kind != execution.StopConfirmed {
		t.Fatal(stopped, err)
	}
	if observed, err := s.Reconcile(context.Background(), outcome.Identity); err != nil || observed.Kind != execution.ReconcileDead {
		t.Fatal("process tree death not proven", err)
	}
	if _, err := s.Drain(context.Background(), outcome.Handle, execution.StreamOffsets{}, 1<<20); err != nil {
		t.Fatal(err)
	}
	if err := s.Finalize(context.Background(), outcome.Handle, execution.RetentionPolicy{}); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeReusableConfigurationAndUnsupportedChecks(t *testing.T) {
	a := fixture(t, "exit 0\n")
	registry := agent.NewRegistry()
	if err := registry.Register(a); err != nil {
		t.Fatal(err)
	}
	binding, err := a.Binding(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	configs := []agent.ExecutionConfiguration{{ID: "claude-local", Version: 1, Binding: binding}}
	selection := agent.ExecutionSelection{ProjectRevision: 1, ProjectDefault: agent.ConfigurationRef{ID: "claude-local", Version: 1}}
	for i := 0; i < 2; i++ {
		snapshot, err := registry.ResolveExecution(configs, selection, []agent.ExecutorRequirement{{Key: "writer", TaskKind: "document"}})
		if err != nil {
			t.Fatal(err)
		}
		executor, err := snapshot.Executor("writer")
		if err != nil || executor.Configuration.Binding != binding {
			t.Fatal("selection not inherited", err)
		}
	}
	if _, err := registry.ResolveExecution(configs, selection, []agent.ExecutorRequirement{{Key: "writer", TaskKind: "code", Capabilities: []string{"agent_checks"}}}); !errors.Is(err, execution.ErrUnsupportedCapability) {
		t.Fatal("checks accepted", err)
	}
	req := request(t, a)
	req.ExecutionContractDocument = []byte(`{"task":{"resources":[{"checks":{"mode":"auto"}}]}}`)
	if _, err := a.PrepareStart(context.Background(), req); !errors.Is(err, execution.ErrUnsupportedCapability) {
		t.Fatal("automatic checks launched", err)
	}
}

func TestPreparationRejectsBindingAndPolicyDrift(t *testing.T) {
	a := fixture(t, "exit 0\n")
	req := request(t, a)
	req.WorkspaceRoot = filepath.Join(req.WorkspaceRoot, "missing")
	if _, err := a.PrepareBoundStart(context.Background(), req); !errors.Is(err, ErrPolicy) {
		t.Fatal(err)
	}
	for _, name := range []string{".claude", ".mcp.json", "CLAUDE.md", "CLAUDE.local.md", ".agents", ".env"} {
		req = request(t, a)
		if err := os.WriteFile(filepath.Join(req.WorkspaceRoot, name), []byte("untrusted"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := a.PrepareBoundStart(context.Background(), req); !errors.Is(err, ErrPolicy) {
			t.Fatal(name, err)
		}
	}
	req = request(t, a)
	req.SnapshotDocument = []byte(`{"changed":true}`)
	if _, err := a.PrepareBoundStart(context.Background(), req); !errors.Is(err, ErrIdentity) {
		t.Fatal("snapshot drift", err)
	}
	req = request(t, a)

	config := a.config
	config.Executable = filepath.Join(t.TempDir(), "missing")
	if _, err := New(context.Background(), config); !errors.Is(err, ErrInstallation) {
		t.Fatal("missing executable", err)
	}
	config = a.config
	config.NetworkEnvironment = map[string]string{"ANTHROPIC_API_KEY": "synthetic"}
	if _, err := New(context.Background(), config); !errors.Is(err, ErrPolicy) {
		t.Fatal("ambient auth admitted", err)
	}
	if err := os.WriteFile(a.config.Executable, []byte("#!/bin/sh\necho '9.0.0 (Claude Code)'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := New(context.Background(), a.config); !errors.Is(err, ErrVersion) {
		t.Fatal("unqualified version admitted", err)
	}
}
