package trustedhost

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/execution"
)

func TestOutputLimitCauseAndExactBudgetDrainSurviveRestore(t *testing.T) {
	platform := newFakePlatform()
	platform.stdout = []byte("0123456789")
	platform.stderr = []byte("error")
	supervisor, config, source := newTestSupervisor(t, platform, realTimerFactory{}, 8)
	launch := execution.LaunchToken{Value: "stream-limit-restore"}
	sink := newRecordingSink(launch)
	out := supervisor.Start(context.Background(), invocationFor(t, config, source, launch, nil, trustedEnvironment()), sink)
	if out.Kind != execution.Started {
		t.Fatalf("start=%#v", out)
	}
	select {
	case <-sink.exited:
	case <-time.After(2 * time.Second):
		t.Fatal("overflow did not terminate")
	}
	record, _ := supervisor.recordForHandle(out.Handle)
	// Observe terminal persistence, not merely leader exit.
	deadline := time.Now().Add(time.Second)
	for {
		record.mu.Lock()
		ready := record.terminalPersisted
		record.mu.Unlock()
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("terminal not persisted")
		}
		time.Sleep(time.Millisecond)
	}
	config.AmbientEnvironment = append(config.AmbientEnvironment, "NEW_RESTART_VARIABLE=changed")
	restored, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	first, err := restored.Drain(context.Background(), out.Handle, execution.StreamOffsets{}, 8)
	if err != nil || len(first.Chunks[execution.StreamStdout]) != 8 || first.EOF[execution.StreamStderr] || first.TerminalFiles.TerminationCause != execution.TerminationOutputLimit {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	second, err := restored.Drain(context.Background(), out.Handle, first.Offsets, 8)
	if err != nil || string(second.Chunks[execution.StreamStderr]) != "error" || !second.EOF[execution.StreamStderr] {
		t.Fatalf("second=%#v err=%v", second, err)
	}
	if err := restored.Finalize(context.Background(), out.Handle, execution.RetentionPolicy{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("source removed: %v", err)
	}
}
