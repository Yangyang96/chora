package codexexec

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/trustedhost"
)

// Opt-in model test. The explicitly selected existing credential scope is used
// by Codex only. Nothing copies authentication or prints native diagnostics.
func TestRealCodexQualification(t *testing.T) {
	executable, auth, model := os.Getenv("CHORA_CODEX_EXECUTABLE"), os.Getenv("CHORA_CODEX_AUTH_HOME"), os.Getenv("CHORA_CODEX_MODEL")
	if executable == "" || auth == "" || model == "" {
		t.Skip("requires explicit Codex executable, existing auth home and model")
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	home := filepath.Join(root, "home")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	a, err := New(context.Background(), Config{Executable: executable, AuthHome: auth, Home: home, Model: model})
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"bounded-task", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			req := request(t, a)
			p, err := a.PrepareBoundStart(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			s, err := trustedhost.New(a.SupervisorConfig(filepath.Join(root, mode)))
			if err != nil {
				t.Fatal(err)
			}
			observer := &sink{token: req.LaunchToken, done: make(chan struct{})}
			started := s.Start(context.Background(), p.Invocation, observer)
			if started.Kind != execution.Started {
				t.Fatal("native launch did not start")
			}
			defer s.Stop(context.Background(), started.Handle, execution.StopIntent{Kind: execution.StopForCancel})
			if mode == "cancel" {
				// Wait for observable native output, bounded by ten seconds. No success
				// terminal is required to test the supervisor-owned stop contract.
				deadline := time.Now().Add(10 * time.Second)
				for time.Now().Before(deadline) {
					chunk, _ := s.Read(context.Background(), started.Handle, execution.StreamStdout, 0, 4096)
					if len(chunk.Data) > 0 {
						break
					}
					time.Sleep(20 * time.Millisecond)
				}
				stopped, err := s.Stop(context.Background(), started.Handle, execution.StopIntent{Kind: execution.StopForCancel, Reason: "qualification"})
				if err != nil || stopped.Kind != execution.StopConfirmed {
					t.Fatal("native stop not proven", err)
				}
				observed, err := s.Reconcile(context.Background(), started.Identity)
				if err != nil || observed.Kind != execution.ReconcileDead {
					t.Fatal("native process group death not proven", err)
				}
				t.Log("native cancellation: confirmed process-group death")
			} else {
				select {
				case <-observer.done:
				case <-time.After(90 * time.Second):
					t.Fatal("native bounded task exceeded qualification deadline")
				}
			}
			d, err := s.Drain(context.Background(), started.Handle, execution.StreamOffsets{}, 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "bounded-task" {
				decoded, err := a.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: d.Chunks[execution.StreamStdout], EOF: true})
				if err != nil {
					t.Fatal(err)
				}
				result, err := a.Complete(execution.CompletionRequest{Events: persisted(t, decoded.Events), Files: d.TerminalFiles})
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("native result: kind=%s failure=%s exit=%d projected_events=%d", result.Kind, result.FailureReason, d.TerminalFiles.ExitCode, len(decoded.Events))
				if result.Kind != execution.TerminalReviewReady {
					t.Errorf("real bounded task is not qualified: %s", result.FailureReason)
				}
			}
			if err := s.Finalize(context.Background(), started.Handle, execution.RetentionPolicy{}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
