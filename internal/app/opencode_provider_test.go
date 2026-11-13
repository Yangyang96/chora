package app_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/agent/opencoderun"
	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
)

// This fixture delegates event decoding and completion to the real protocol
// implementation, while admission/launch identity is explicitly synthetic. It
// exercises persisted lifecycle behavior without granting native qualification.
type openCodeProtocolFixture struct{ *syntheticProvider }

func (p *openCodeProtocolFixture) Describe() execution.ProviderDescriptor {
	return (&opencoderun.Adapter{}).Describe()
}
func (p *openCodeProtocolFixture) DecodeEvent(c execution.EventChunk) (execution.DecodedChunk, error) {
	return (&opencoderun.Adapter{}).DecodeEvent(c)
}
func (p *openCodeProtocolFixture) Complete(r execution.CompletionRequest) (execution.TerminalResult, error) {
	return (&opencoderun.Adapter{}).Complete(r)
}

func TestOpenCodeProtocolPersistentLifecycleFixture(t *testing.T) {
	for _, mode := range []string{"complete", "cancel", "protocol_invalid", "model_failure", "case_alias"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			p := &syntheticProvider{fakeAdapter: &fakeAdapter{id: opencoderun.AdapterID, resumeMode: execution.ResumeUnsupported}}
			p.providerOverride = &openCodeProtocolFixture{syntheticProvider: p}
			sup := &fakeSupervisor{outcome: execution.StartOutcome{Kind: execution.Started, Handle: execution.RuntimeHandle{Value: "opencode-handle"}, Identity: execution.ProcessIdentity{Value: "opencode-process"}}}
			service, db, run := newSyntheticFixture(t, p, sup)
			prepared, err := service.PrepareRun(ctx, app.PrepareRunRequest{CommandMeta: meta("opencode-prepare", "opencode-prepare"), RunID: run.ID(), ExpectedVersion: run.Version()})
			if err != nil {
				t.Fatal(err)
			}
			stored, err := db.Reader().GetAttempt(ctx, prepared.Attempt.ID())
			if err != nil || stored.AgentExecutionProfileBinding().RuntimeID() != opencoderun.AdapterID {
				t.Fatal("binding not persisted", err)
			}
			started, err := service.StartAttempt(ctx, app.StartAttemptRequest{CommandMeta: meta("opencode-start", "opencode-start"), RunID: run.ID(), ExpectedVersion: prepared.Run.Version(), Mode: app.StartFresh})
			if err != nil {
				t.Fatal(err)
			}
			if sup.startCalls != 1 {
				t.Fatalf("no launch: %+v", started)
			}
			if mode == "cancel" {
				_, err := service.RequestCancel(ctx, app.StopRequest{CommandMeta: meta("opencode-cancel", "opencode-cancel"), RunID: run.ID(), ExpectedVersion: started.Run.Version(), Reason: "fixture cancellation"})
				if err != nil || sup.stopCalls != 1 || sup.finalizeCalls != 1 {
					t.Fatal("cancel not finalized", err)
				}
				return
			}
			initial := `{"type":"step_start","timestamp":1,"sessionID":"fixture-session","part":{"id":"part-1","sessionID":"fixture-session","messageID":"message-1","type":"step-start"}}` + "\n"
			text := `{"type":"text","timestamp":2,"sessionID":"fixture-session","part":{"id":"part-2","sessionID":"fixture-session","messageID":"message-1","type":"text","text":"Synthetic bounded result","time":{"start":1,"end":2}}}` + "\n"
			finish := `{"type":"step_finish","timestamp":3,"sessionID":"fixture-session","part":{"id":"part-3","sessionID":"fixture-session","messageID":"message-1","type":"step-finish","reason":"stop","cost":0,"tokens":{"input":1,"output":1,"reasoning":0,"cache":{"read":0,"write":0}}}}` + "\n"
			stream := initial + text + finish
			if mode == "case_alias" {
				stream = initial + text + strings.Replace(finish, `"reason":"stop"`, `"reason":"error","Reason":"stop"`, 1)
			}
			if mode == "model_failure" {
				stream = initial + `{"type":"error","timestamp":2,"sessionID":"fixture-session","error":{"name":"APIError"}}` + "\n"
			}
			if mode == "protocol_invalid" {
				stream = finish
			}
			sup.drainOutcome = execution.DrainOutcome{Chunks: map[execution.StreamKind][]byte{execution.StreamStdout: []byte(stream)}, Offsets: execution.StreamOffsets{execution.StreamStdout: int64(len(stream)), execution.StreamStderr: 0}, EOF: map[execution.StreamKind]bool{execution.StreamStdout: true, execution.StreamStderr: true}}
			sup.reconcileOutcome = execution.ReconcileOutcome{Kind: execution.ReconcileDead, Handle: execution.RuntimeHandle{Value: "opencode-handle"}, LaunchToken: sup.invocation.LaunchToken()}
			sup.sink.Exited()
			result, err := service.HandleExit(ctx, app.ExitRequest{CommandMeta: meta("opencode-exit", "opencode-exit"), SessionID: started.Session.ID, ExpectedVersion: started.Run.Version()})
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
