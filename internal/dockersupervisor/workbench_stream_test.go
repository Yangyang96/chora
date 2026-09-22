package dockersupervisor

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/execution"
)

func TestWorkbenchProgressDoesNotTruncateResult(t *testing.T) {
	process := newFakeProcess()
	runner := &fakeRunner{processes: []*fakeProcess{process}, boundaryStopped: true}
	s := newWorkbenchTestSupervisor(t, runner, func(context.Context, execution.Invocation, string) error { return nil })
	invocation := testWorkbenchInvocation(t, testSource(t), "progress-budget", s.config)
	out := s.Start(context.Background(), invocation, &testSink{binding: invocation.LaunchToken()})
	if out.Kind != execution.Started {
		t.Fatalf("start: %+v", out)
	}
	record := s.record(out.Handle)
	defer func() { process.exit(0); waitDone(t, record.done) }()
	frame := []byte(`{"type":"message_update","delta":"` + strings.Repeat("x", 32000) + `"}` + "\n")
	for i := 0; i < 400; i++ {
		process.stdout.Write(frame[:17])
		process.stdout.Write(frame[17:])
	}
	terminal := []byte("{\"type\":\"tool_execution_end\"}\n{\"type\":\"agent_end\"}\n")
	process.stdout.Write(terminal)
	got, _, _ := record.stdout.read(0, persistedLogBytes)
	if !bytes.Equal(got, terminal) {
		t.Fatalf("transient output displaced final evidence: retained=%d want=%d", len(got), len(terminal))
	}
}

func TestWorkbenchAuthorityOutputLimitPreventsImport(t *testing.T) {
	process := newFakeProcess()
	runner := &fakeRunner{processes: []*fakeProcess{process}, boundaryStopped: true}
	collected := false
	s := newWorkbenchTestSupervisor(t, runner, func(context.Context, execution.Invocation, string) error { collected = true; return nil })
	invocation := testWorkbenchInvocation(t, testSource(t), "authority-budget", s.config)
	out := s.Start(context.Background(), invocation, &testSink{binding: invocation.LaunchToken()})
	if out.Kind != execution.Started {
		t.Fatalf("start: %+v", out)
	}
	record := s.record(out.Handle)
	process.stdout.Write([]byte(strings.Repeat("x", persistedLogBytes+1)))
	process.exit(0)
	waitDone(t, record.done)
	if collected {
		t.Fatal("imported result after output evidence was truncated")
	}
	if record.terminationCause != execution.TerminationOutputLimit {
		t.Fatalf("termination=%s", record.terminationCause)
	}
}

func TestWorkbenchFlushesIncompleteFinalFrame(t *testing.T) {
	process := newFakeProcess()
	runner := &fakeRunner{processes: []*fakeProcess{process}, boundaryStopped: true}
	s := newWorkbenchTestSupervisor(t, runner, func(context.Context, execution.Invocation, string) error { return nil })
	invocation := testWorkbenchInvocation(t, testSource(t), "partial-frame", s.config)
	out := s.Start(context.Background(), invocation, &testSink{binding: invocation.LaunchToken()})
	if out.Kind != execution.Started {
		t.Fatalf("start: %+v", out)
	}
	record := s.record(out.Handle)
	partial := []byte(`{"type":"message_update","unfinished":`)
	process.stdout.Write(partial)
	process.exit(0)
	waitDone(t, record.done)
	got, _, _ := record.stdout.read(0, persistedLogBytes)
	if !bytes.Equal(got, partial) {
		t.Fatalf("incomplete frame disappeared: got %q", got)
	}
}

func TestWorkbenchOutputLimitPreservesStopAndTimeout(t *testing.T) {
	for _, timedOut := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "timeout"}[timedOut], func(t *testing.T) {
			process := newFakeProcess()
			runner := &fakeRunner{processes: []*fakeProcess{process}, boundaryStopped: true}
			collected := false
			s := newWorkbenchTestSupervisor(t, runner, func(context.Context, execution.Invocation, string) error { collected = true; return nil })
			invocation := testWorkbenchInvocation(t, testSource(t), "stopped-overflow", s.config)
			out := s.Start(context.Background(), invocation, &testSink{binding: invocation.LaunchToken()})
			if out.Kind != execution.Started {
				t.Fatalf("start: %+v", out)
			}
			record := s.record(out.Handle)
			process.stdout.Write([]byte(strings.Repeat("x", persistedLogBytes+1)))
			record.mu.Lock()
			record.stopRequested = true
			record.timedOut = timedOut
			if timedOut {
				record.terminationCause = execution.TerminationDeadlineExceeded
			}
			record.mu.Unlock()
			process.exit(0)
			waitDone(t, record.done)
			if collected || record.terminationCause == execution.TerminationOutputLimit {
				t.Fatalf("stop overwritten or imported: cause=%s collected=%v", record.terminationCause, collected)
			}
			if timedOut && record.terminationCause != execution.TerminationDeadlineExceeded {
				t.Fatalf("lost timeout cause: %s", record.terminationCause)
			}
		})
	}
}
