package codexexec

import (
	"context"
	"crypto/sha256"
	"encoding/json"
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

const successStream = `{"type":"thread.started","thread_id":"thread-1"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"Complete synthetic finding."}}
{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"output_tokens":1}}
`

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
	executable := filepath.Join(root, "codex-fixture")
	script := "#!/bin/sh\ncase \"$1\" in\n--version) echo 'codex-cli 0.155.1'; exit;;\nlogin) echo 'Logged in using ChatGPT'; exit;;\nesac\n" + body
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
func TestProtocolCompletionAndFailurePaths(t *testing.T) {
	a := &Adapter{}
	cases := []struct {
		name, stream, want string
		exit               int
	}{
		{"complete", successStream, "", 0},
		{"nonzero", successStream, "process_failure", 2},
		{"empty", "", "protocol_incomplete", 0},
		{"no final", `{"type":"thread.started","thread_id":"t"}` + "\n" + `{"type":"turn.started"}` + "\n" + `{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"output_tokens":1}}` + "\n", "protocol_incomplete", 0},
		{"auth", successStream + `{"type":"turn.failed","error":{"message":"401 unauthorized bearer private-value"}}` + "\n", "authentication_rejected", 1},
		{"failed turn", successStream + `{"type":"turn.failed","error":{"message":"network unavailable"}}` + "\n", "provider_failure", 0},
		{"model", successStream + `{"type":"error","message":"model_not_found"}` + "\n", "model_unavailable", 1},
		{"duplicate terminal", successStream + `{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"output_tokens":1}}` + "\n", "protocol_invalid", 0},
		{"reordered", `{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"output_tokens":1}}` + "\n" + successStream, "protocol_invalid", 0},
		{"duplicate item", strings.Replace(successStream, `{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"output_tokens":1}}`, `{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"duplicate"}}`+"\n"+`{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"output_tokens":1}}`, 1), "protocol_invalid", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, err := a.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: []byte(tc.stream), EOF: true})
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(d)
			if strings.Contains(string(raw), "private-value") {
				t.Fatal("diagnostic leaked")
			}
			r, err := a.Complete(execution.CompletionRequest{Events: persisted(t, d.Events), Files: execution.TerminalFiles{ExitCode: tc.exit}})
			if err != nil || r.FailureReason != tc.want {
				t.Fatalf("result=%+v error=%v", r, err)
			}
		})
	}
	d, _ := a.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: []byte(successStream), EOF: true})
	r, _ := a.Complete(execution.CompletionRequest{Events: persisted(t, d.Events), Files: execution.TerminalFiles{TerminationCause: execution.TerminationDeadlineExceeded}})
	if r.Kind != execution.TerminalFailed {
		t.Fatal("cancel lost to success")
	}
}
func TestFramingRedactionAndIndependentSessions(t *testing.T) {
	a := &Adapter{}
	for _, s := range []string{"{\n", "{}\n", `{"type":"turn.unknown"}` + "\n", strings.Repeat("x", maxFrame+1), strings.TrimSuffix(successStream, "\n"), `{"type":"item.completed","item":{"id":"x","type":"mcp_tool_call"}}` + "\n"} {
		if _, err := a.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: []byte(s), EOF: true}); !errors.Is(err, execution.ErrEventStreamInvalid) {
			t.Fatalf("accepted invalid stream %.40q", s)
		}
	}
	for split := 0; split < len(successStream); split++ {
		first, err := a.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: []byte(successStream[:split])})
		if err != nil {
			t.Fatal(err)
		}
		second, err := a.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: []byte(successStream[first.ConsumedBytes:]), EOF: true})
		if err != nil {
			t.Fatal(err)
		}
		r, _ := a.Complete(execution.CompletionRequest{Events: persisted(t, append(first.Events, second.Events...)), Files: execution.TerminalFiles{}})
		if r.Kind != execution.TerminalReviewReady {
			t.Fatal("split failed", split, r)
		}
	}
	d, err := a.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: []byte(`{"type":"item.completed","item":{"id":"x","type":"reasoning","text":"private"}}` + "\n"), EOF: true})
	if err != nil || len(d.Events) != 0 {
		t.Fatal("reasoning persisted")
	}
	// A second incomplete session cannot inherit the first session's settlement.
	d, _ = a.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: []byte(successStream), EOF: true})
	_, _ = a.Complete(execution.CompletionRequest{Events: persisted(t, d.Events)})
	r, _ := a.Complete(execution.CompletionRequest{Events: persisted(t, nil)})
	if r.Kind != execution.TerminalFailed {
		t.Fatal("cross-session state")
	}
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
	if err := os.Mkdir(filepath.Join(req.WorkspaceRoot, ".codex"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := a.PrepareStart(context.Background(), req); !errors.Is(err, ErrPolicy) {
		t.Fatal("project config accepted", err)
	}
	if err := os.WriteFile(filepath.Join(a.config.AuthHome, "extra.config.toml"), []byte("untrusted"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Fingerprint(context.Background()); !errors.Is(err, ErrPolicy) {
		t.Fatal("profile accepted", err)
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

func TestCodexReusableConfigurationAndUnsupportedChecks(t *testing.T) {
	a := fixture(t, "exit 0\n")
	registry := agent.NewRegistry()
	if err := registry.Register(a); err != nil {
		t.Fatal(err)
	}
	binding, err := a.Binding(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	configs := []agent.ExecutionConfiguration{{ID: "codex-local", Version: 1, Binding: binding}}
	selection := agent.ExecutionSelection{ProjectRevision: 1, ProjectDefault: agent.ConfigurationRef{ID: "codex-local", Version: 1}}
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
