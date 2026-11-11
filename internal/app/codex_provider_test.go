package app_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Yangyang96/chora/internal/agent/codexexec"
	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
)

// Protocol fixtures exercise the real provider through persisted application
// admission/stream/terminal/cancel paths; they do not qualify model access.
func TestCodexProviderPersistentLifecycle(t *testing.T) {
	for _, mode := range []string{"complete", "cancel", "protocol_invalid", "model_failure"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			root, _ := filepath.EvalSymlinks(t.TempDir())
			for _, n := range []string{"home", "auth"} {
				if err := os.Mkdir(filepath.Join(root, n), 0700); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(root, "codex-fixture")
			if err := os.WriteFile(path, []byte("#!/bin/sh\ncase \"$1\" in --version) echo 'codex-cli 0.155.1';; login) echo 'Logged in using ChatGPT';; esac\n"), 0700); err != nil {
				t.Fatal(err)
			}
			provider, err := codexexec.New(ctx, codexexec.Config{Executable: path, AuthHome: filepath.Join(root, "auth"), Home: filepath.Join(root, "home")})
			if err != nil {
				t.Fatal(err)
			}
			p := &syntheticProvider{providerOverride: provider}
			sup := &fakeSupervisor{outcome: execution.StartOutcome{Kind: execution.Started, Handle: execution.RuntimeHandle{Value: "codex-handle"}, Identity: execution.ProcessIdentity{Value: "codex-process"}}}
			service, db, run := newSyntheticFixture(t, p, sup)
			prepared, err := service.PrepareRun(ctx, app.PrepareRunRequest{CommandMeta: meta("codex-prepare", "codex-prepare"), RunID: run.ID(), ExpectedVersion: run.Version()})
			if err != nil {
				t.Fatal(err)
			}
			stored, err := db.Reader().GetAttempt(ctx, prepared.Attempt.ID())
			if err != nil || stored.AgentExecutionProfileBinding().RuntimeID() != codexexec.AdapterID {
				t.Fatal("binding not persisted", err)
			}
			started, err := service.StartAttempt(ctx, app.StartAttemptRequest{CommandMeta: meta("codex-start", "codex-start"), RunID: run.ID(), ExpectedVersion: prepared.Run.Version(), Mode: app.StartFresh})
			if err != nil {
				t.Fatal(err)
			}
			if sup.startCalls != 1 {
				t.Fatalf("no launch: %+v", started)
			}
			if mode == "cancel" {
				_, err := service.RequestCancel(ctx, app.StopRequest{CommandMeta: meta("codex-cancel", "codex-cancel"), RunID: run.ID(), ExpectedVersion: started.Run.Version(), Reason: "fixture cancellation"})
				if err != nil || sup.stopCalls != 1 || sup.finalizeCalls != 1 {
					t.Fatal("cancel not finalized", err)
				}
				return
			}
			stream := "{\"type\":\"thread.started\",\"thread_id\":\"fixture-thread\"}\n{\"type\":\"turn.started\"}\n{\"type\":\"item.completed\",\"item\":{\"id\":\"item_0\",\"type\":\"agent_message\",\"text\":\"Synthetic bounded result\"}}\n{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":1,\"cached_input_tokens\":0,\"output_tokens\":1}}\n"
			if mode == "model_failure" {
				stream = "{\"type\":\"turn.failed\",\"error\":{\"message\":\"model_not_found\"}}\n"
			}
			if mode == "protocol_invalid" {
				stream = "{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":1,\"cached_input_tokens\":0,\"output_tokens\":1}}\n"
			}
			sup.drainOutcome = execution.DrainOutcome{Chunks: map[execution.StreamKind][]byte{execution.StreamStdout: []byte(stream)}, Offsets: execution.StreamOffsets{execution.StreamStdout: int64(len(stream)), execution.StreamStderr: 0}, EOF: map[execution.StreamKind]bool{execution.StreamStdout: true, execution.StreamStderr: true}}
			sup.reconcileOutcome = execution.ReconcileOutcome{Kind: execution.ReconcileDead, Handle: execution.RuntimeHandle{Value: "codex-handle"}, LaunchToken: sup.invocation.LaunchToken()}
			sup.sink.Exited()
			result, err := service.HandleExit(ctx, app.ExitRequest{CommandMeta: meta("codex-exit", "codex-exit"), SessionID: started.Session.ID, ExpectedVersion: started.Run.Version()})
			expected := domain.RunStateRecoveryRequired
			if mode == "complete" {
				expected = domain.RunStateAwaitingVerification
			}
			if err != nil || result.Run.State() != expected || sup.finalizeCalls != 1 {
				t.Fatalf("state=%s expected=%s finalized=%d err=%v", result.Run.State(), expected, sup.finalizeCalls, err)
			}
		})
	}
}
