package app_test

import (
	"context"
	"testing"

	"github.com/Yangyang96/chora/internal/agent/hermes"
	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
)

// A test-only synthetic launcher exercises Hermes projection through persisted
// Run/Attempt stream, terminal and cancel paths. Production Hermes refuses all
// launch/fingerprint requests. This is not native execution or custody evidence.
func TestHermesProtocolPersistentLifecycle(t *testing.T) {
	for _, mode := range []string{"complete", "cancel", "protocol_invalid", "model_failure"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			p := &syntheticProvider{fakeAdapter: &fakeAdapter{id: hermes.AdapterID, resumeMode: execution.ResumeUnsupported}}
			p.providerOverride = &hermesProtocolFixture{syntheticProvider: p, protocol: &hermes.Adapter{}}
			sup := &fakeSupervisor{outcome: execution.StartOutcome{Kind: execution.Started, Handle: execution.RuntimeHandle{Value: "hermes-handle"}, Identity: execution.ProcessIdentity{Value: "hermes-process"}}}
			service, db, run := newSyntheticFixture(t, p, sup)
			prepared, err := service.PrepareRun(ctx, app.PrepareRunRequest{CommandMeta: meta("hermes-prepare", "hermes-prepare"), RunID: run.ID(), ExpectedVersion: run.Version()})
			if err != nil {
				t.Fatal(err)
			}
			stored, err := db.Reader().GetAttempt(ctx, prepared.Attempt.ID())
			if err != nil || stored.AgentExecutionProfileBinding().RuntimeID() != hermes.AdapterID {
				t.Fatal("binding not persisted", err)
			}
			started, err := service.StartAttempt(ctx, app.StartAttemptRequest{CommandMeta: meta("hermes-start", "hermes-start"), RunID: run.ID(), ExpectedVersion: prepared.Run.Version(), Mode: app.StartFresh})
			if err != nil {
				t.Fatal(err)
			}
			if sup.startCalls != 1 {
				t.Fatalf("no launch: %+v", started)
			}
			if mode == "cancel" {
				_, err := service.RequestCancel(ctx, app.StopRequest{CommandMeta: meta("hermes-cancel", "hermes-cancel"), RunID: run.ID(), ExpectedVersion: started.Run.Version(), Reason: "fixture cancellation"})
				if err != nil || sup.stopCalls != 1 || sup.finalizeCalls != 1 {
					t.Fatal("cancel not finalized", err)
				}
				return
			}
			stream := "{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"\"}\n{\"type\":\"text\",\"text\":\"Synthetic bounded result\"}\n{\"type\":\"result\",\"session_id\":\"fixture-session\",\"exit_code\":0,\"text\":\"Synthetic bounded result\"}\n"
			if mode == "model_failure" {
				stream = "{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"\"}\n{\"type\":\"result\",\"session_id\":\"fixture-session\",\"exit_code\":1,\"error\":\"private failure\"}\n"
			}
			if mode == "protocol_invalid" {
				stream = "{\"type\":\"result\",\"session_id\":\"fixture-session\",\"exit_code\":0,\"text\":\"Synthetic bounded result\"}\n"
			}

			sup.drainOutcome = execution.DrainOutcome{Chunks: map[execution.StreamKind][]byte{execution.StreamStdout: []byte(stream)}, Offsets: execution.StreamOffsets{execution.StreamStdout: int64(len(stream)), execution.StreamStderr: 0}, EOF: map[execution.StreamKind]bool{execution.StreamStdout: true, execution.StreamStderr: true}}
			sup.reconcileOutcome = execution.ReconcileOutcome{Kind: execution.ReconcileDead, Handle: execution.RuntimeHandle{Value: "hermes-handle"}, LaunchToken: sup.invocation.LaunchToken()}
			sup.sink.Exited()
			result, err := service.HandleExit(ctx, app.ExitRequest{CommandMeta: meta("hermes-exit", "hermes-exit"), SessionID: started.Session.ID, ExpectedVersion: started.Run.Version()})
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

// The wrapper exists only in this test binary. It cannot authorize native Hermes.
type hermesProtocolFixture struct {
	*syntheticProvider
	protocol *hermes.Adapter
}

func (p *hermesProtocolFixture) DecodeEvent(c execution.EventChunk) (execution.DecodedChunk, error) {
	return p.protocol.DecodeEvent(c)
}
func (p *hermesProtocolFixture) Complete(r execution.CompletionRequest) (execution.TerminalResult, error) {
	return p.protocol.Complete(r)
}
