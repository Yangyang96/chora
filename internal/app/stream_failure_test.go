package app_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Yangyang96/chora/internal/agent/pi"
	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func TestPiTruncatedStreamSettlesAfterExitOrRestart(t *testing.T) {
	for _, restart := range []bool{false, true} {
		for _, code := range []int{0, 143} {
			t.Run(fmt.Sprintf("restart=%t/exit=%d", restart, code), func(t *testing.T) {
				f := newFixture(t)
				started := f.started(t)
				f.adapter.decodeEvent = (&pi.Adapter{}).DecodeEvent
				data := []byte(`{"type":"tool_execution_update","partialResult":`)
				f.supervisor.drainOutcome = execution.DrainOutcome{
					Chunks:        map[execution.StreamKind][]byte{execution.StreamStdout: data},
					Offsets:       execution.StreamOffsets{execution.StreamStdout: int64(len(data)), execution.StreamStderr: 0},
					EOF:           map[execution.StreamKind]bool{execution.StreamStdout: true, execution.StreamStderr: true},
					TerminalFiles: execution.TerminalFiles{ExitCode: code},
				}
				f.signalExit()
				if restart {
					if err := f.service.RecoverStartup(context.Background()); err != nil {
						t.Fatal(err)
					}
				} else {
					result, err := f.service.HandleExit(context.Background(), app.ExitRequest{CommandMeta: meta("truncated-exit", "truncated-exit"), SessionID: started.Session.ID, ExpectedVersion: started.Run.Version()})
					if err != nil {
						t.Fatal(err)
					}
					if result.Attempt.State() != domain.AttemptStateFailed || result.Run.State() != domain.RunStateRecoveryRequired {
						t.Fatalf("result=%#v", result)
					}
				}
				session, err := f.db.Reader().GetRuntimeSession(context.Background(), started.Session.ID)
				if err != nil || session.State != "stopped" || session.FinalizedAt.IsZero() {
					t.Fatalf("session=%#v err=%v", session, err)
				}
				events, err := f.db.Reader().ListRunEvents(context.Background(), started.Run.ID())
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, e := range events {
					if bytes.Contains(e.NormalizedJSON(), []byte("runtime_stream_invalid")) {
						found = true
					}
				}
				if !found || f.adapter.terminalDecodeCalls != 0 {
					t.Fatal("missing persisted failure or untrusted success decoded")
				}
				offset, err := f.db.Reader().GetRuntimeStreamOffset(context.Background(), started.Session.ID, storecontract.RuntimeStreamStdout)
				if err != nil || !offset.EOF || offset.Offset != int64(len(data)) {
					t.Fatalf("offset=%#v err=%v", offset, err)
				}
			})
		}
	}
}
func TestStreamInfrastructureErrorStillBlocksFinalization(t *testing.T) {
	f := newFixture(t)
	started := f.started(t)
	f.adapter.decodeEvent = func(execution.EventChunk) (execution.DecodedChunk, error) {
		return execution.DecodedChunk{}, errors.New("temporary adapter infrastructure error")
	}
	f.supervisor.drainOutcome = execution.DrainOutcome{Chunks: map[execution.StreamKind][]byte{}, Offsets: execution.StreamOffsets{execution.StreamStdout: 0, execution.StreamStderr: 0}, EOF: map[execution.StreamKind]bool{execution.StreamStdout: true, execution.StreamStderr: true}}
	f.signalExit()
	if _, err := f.service.HandleExit(context.Background(), app.ExitRequest{CommandMeta: meta("transient-exit", "transient-exit"), SessionID: started.Session.ID, ExpectedVersion: started.Run.Version()}); err == nil {
		t.Fatal("infrastructure error ignored")
	}
	if f.supervisor.finalizeCalls != 0 {
		t.Fatal("finalized without draining")
	}
}
