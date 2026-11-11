package claudeprint

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/runtimeenv"
	"github.com/Yangyang96/chora/internal/trustedhost"
)

// Opt-in only. Synthetic protocol coverage never substitutes for this gate.
func TestRealClaudeQualification(t *testing.T) {
	exe, auth, model := os.Getenv("CHORA_CLAUDE_EXECUTABLE"), os.Getenv("CHORA_CLAUDE_AUTH_HOME"), os.Getenv("CHORA_CLAUDE_MODEL")
	if exe == "" || auth == "" || model == "" {
		t.Skip("requires explicit installed executable, existing auth scope and model")
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	home := filepath.Join(root, "home")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	config := Config{Executable: exe, AuthHome: auth, Home: home, Model: model}
	// Explicit caller-selected networking, never discovered from the machine.
	config.NetworkEnvironment = map[string]string{}
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy", "SSL_CERT_FILE", "NODE_EXTRA_CA_CERTS"} {
		if value := os.Getenv("CHORA_CLAUDE_" + key); value != "" {
			config.NetworkEnvironment[key] = value
		}
	}
	a, err := New(context.Background(), config)
	if err != nil && !errors.Is(err, ErrAuthentication) {
		t.Fatal(err)
	}
	t.Run("bounded-task", func(t *testing.T) {
		if errors.Is(err, ErrAuthentication) {
			t.Skip("real success BLOCKED: selected existing Claude login unavailable")
		}
		req := request(t, a)
		p, e := a.PrepareBoundStart(context.Background(), req)
		if e != nil {
			t.Fatal(e)
		}
		result := runNative(t, a, p.Invocation, filepath.Join(root, "success"), false)
		if result.Kind != execution.TerminalReviewReady {
			t.Fatalf("real task failed: %s", result.FailureReason)
		}
		t.Log("real task: trusted exit and native success, projected final message")
	})
	// Run the actual CLI against a disposable, deliberately unauthenticated scope.
	// This test-only path observes native failure and resource controls. Production
	// New/Prepare never bypass authentication and reject this scope before launch.
	emptyAuth := filepath.Join(root, "empty-auth")
	if e := os.Mkdir(emptyAuth, 0700); e != nil {
		t.Fatal(e)
	}
	marker := filepath.Join(root, "UNEXPECTED-HOOK")
	canary := `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"touch ` + marker + `"}]}]},"mcpServers":{"canary":{"command":"touch","args":["` + marker + `"]}}}`
	if e := os.WriteFile(filepath.Join(emptyAuth, "settings.json"), []byte(canary), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(emptyAuth, "CLAUDE.md"), []byte("UNEXPECTED_NATIVE_CONTEXT"), 0600); e != nil {
		t.Fatal(e)
	}
	skill := filepath.Join(emptyAuth, "skills", "canary")
	if e := os.MkdirAll(skill, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("---\nname: canary\ndescription: unexpected\n---\nUNEXPECTED_NATIVE_CONTEXT"), 0600); e != nil {
		t.Fatal(e)
	}
	config.AuthHome = emptyAuth
	identity, e := trustedhost.InspectExecutable(exe)
	if e != nil {
		t.Fatal(e)
	}
	network, e := runtimeenv.New(config.NetworkEnvironment)
	if e != nil {
		t.Fatal(e)
	}
	unauthed := &Adapter{config: config, executable: identity, network: network}
	unauthed.fingerprint = unauthed.deriveFingerprint(identity)
	if e := unauthed.Authentication(context.Background()); !errors.Is(e, ErrAuthentication) {
		t.Fatal("empty native scope unexpectedly authenticated")
	}
	for _, mode := range []string{"native-auth-failure-and-controls", "native-cancel"} {
		t.Run(mode, func(t *testing.T) {
			work, _ := filepath.EvalSymlinks(t.TempDir())
			target, _ := execution.NewExecutionTarget(AdapterID, domain.TrustedHostExecutionProvider)
			invocation, e := execution.NewInvocation(execution.InvocationParams{AdapterID: AdapterID, Executable: exe, Arguments: unauthed.arguments(), Environment: unauthed.environment(), WorkingRoot: work, Stdin: []byte("Return exactly CHORA_RUNTIME_OK. Do not use tools."), LaunchToken: execution.LaunchToken{Value: "native-qualification"}, Target: target})
			if e != nil {
				t.Fatal(e)
			}
			result := runNative(t, unauthed, invocation, filepath.Join(root, mode), mode == "native-cancel")
			if mode != "native-cancel" && (result.Kind != execution.TerminalFailed || result.FailureReason != "authentication_rejected") {
				t.Fatalf("native missing login misclassified: %s %s", result.Kind, result.FailureReason)
			}
			if _, e := os.Stat(marker); !errors.Is(e, os.ErrNotExist) {
				t.Fatal("native hook/MCP canary executed")
			}
		})
	}
}

func runNative(t *testing.T, a *Adapter, invocation execution.Invocation, root string, cancel bool) execution.TerminalResult {
	t.Helper()
	ctx := context.Background()
	s, err := trustedhost.New(a.SupervisorConfig(root))
	if err != nil {
		t.Fatal(err)
	}
	observer := &sink{token: invocation.LaunchToken(), done: make(chan struct{})}
	started := s.Start(ctx, invocation, observer)
	if started.Kind != execution.Started {
		t.Fatal("native launch failed")
	}
	defer s.Stop(ctx, started.Handle, execution.StopIntent{Kind: execution.StopForCancel})
	if cancel {
		live, e := s.Reconcile(ctx, started.Identity)
		if e != nil || live.Kind != execution.ReconcileAlive {
			t.Fatal("native process not alive before cancellation", e)
		}
		stopped, e := s.Stop(ctx, started.Handle, execution.StopIntent{Kind: execution.StopForCancel})
		if e != nil || stopped.Kind != execution.StopConfirmed {
			t.Fatal("native stop unconfirmed", e)
		}
		dead, e := s.Reconcile(ctx, started.Identity)
		if e != nil || dead.Kind != execution.ReconcileDead {
			t.Fatal("native tree death unconfirmed", e)
		}
		t.Log("native cancellation: alive before stop, process-group death confirmed")
	} else {
		select {
		case <-observer.done:
		case <-time.After(60 * time.Second):
			t.Fatal("native task timed out")
		}
	}
	drain, err := s.Drain(ctx, started.Handle, execution.StreamOffsets{}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if e := s.Finalize(ctx, started.Handle, execution.RetentionPolicy{}); e != nil {
			t.Error(e)
		}
	}()
	if cancel {
		return execution.TerminalResult{Kind: execution.TerminalFailed}
	}
	d, err := a.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: drain.Chunks[execution.StreamStdout], EOF: true})
	if err != nil {
		t.Fatal(err)
	}
	result, err := a.Complete(execution.CompletionRequest{Events: persisted(t, d.Events), Files: drain.TerminalFiles})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("native result=%s failure=%s exit=%d projected_events=%d", result.Kind, result.FailureReason, drain.TerminalFiles.ExitCode, len(d.Events))
	return result
}
