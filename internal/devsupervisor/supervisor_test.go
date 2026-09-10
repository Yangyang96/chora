package devsupervisor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/execution"
)

const helperAdapterID = "test-helper"

type recordingSink struct {
	launch execution.LaunchToken

	mu            sync.Mutex
	notifications map[execution.StreamKind][]int64
	exited        chan struct{}
	exitOnce      sync.Once
}

func newRecordingSink(launch execution.LaunchToken) *recordingSink {
	return &recordingSink{
		launch: launch,
		notifications: map[execution.StreamKind][]int64{
			execution.StreamStdout: nil,
			execution.StreamStderr: nil,
		},
		exited: make(chan struct{}),
	}
}

func (sink *recordingSink) Binding() execution.LaunchToken { return sink.launch }

func (sink *recordingSink) Notify(kind execution.StreamKind, offset int64) {
	sink.mu.Lock()
	sink.notifications[kind] = append(sink.notifications[kind], offset)
	sink.mu.Unlock()
}

func (sink *recordingSink) Exited() { sink.exitOnce.Do(func() { close(sink.exited) }) }

func TestConfigCannotRaiseRuntimeOrStopCeilings(t *testing.T) {
	supervisor, err := New(Config{
		AllowedAdapterID:  helperAdapterID,
		AllowedExecutable: os.Args[0],
		MaxRuntime:        defaultMaxRuntime + time.Second,
		StopWait:          defaultStopWait + time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if supervisor.config.MaxRuntime != defaultMaxRuntime || supervisor.config.StopWait != defaultStopWait {
		t.Fatalf("config ceilings = %s, %s", supervisor.config.MaxRuntime, supervisor.config.StopWait)
	}
}

func TestSingleApplicationSlotAndUnknownReconciliation(t *testing.T) {
	supervisor := newTestSupervisor(t, 5*time.Second, 50*time.Millisecond)
	first, firstSink, _ := startHelper(t, supervisor, "sleep", "250ms")
	secondSupervisor := newTestSupervisor(t, 5*time.Second, 50*time.Millisecond)
	secondLaunch := execution.LaunchToken{Value: "launch-second"}
	secondInvocation := helperInvocation(t, secondLaunch, t.TempDir(), "echo")
	second := secondSupervisor.Start(context.Background(), secondInvocation, newRecordingSink(secondLaunch))
	if second.Kind != execution.StartProvenNoChild {
		t.Fatalf("second start kind = %q, want %q", second.Kind, execution.StartProvenNoChild)
	}

	unknownIdentity, err := supervisor.Reconcile(context.Background(), execution.ProcessIdentity{Value: "unknown"})
	if err != nil || unknownIdentity.Kind != execution.ReconcileUncertain {
		t.Fatalf("unknown identity = %#v, %v", unknownIdentity, err)
	}
	unknownLaunch, err := supervisor.ReconcileLaunch(context.Background(), execution.LaunchToken{Value: "unknown"})
	if err != nil || unknownLaunch.Kind != execution.ReconcileUncertain {
		t.Fatalf("unknown launch = %#v, %v", unknownLaunch, err)
	}
	alive, err := supervisor.Reconcile(context.Background(), first.Identity)
	if err != nil || alive.Kind != execution.ReconcileAlive || alive.Handle != first.Handle || alive.LaunchToken != first.LaunchToken {
		t.Fatalf("alive reconcile = %#v, %v", alive, err)
	}
	waitExited(t, firstSink)
	dead, err := supervisor.ReconcileLaunch(context.Background(), first.LaunchToken)
	if err != nil || dead.Kind != execution.ReconcileDead || dead.Handle != first.Handle || dead.LaunchToken != first.LaunchToken {
		t.Fatalf("dead reconcile = %#v, %v", dead, err)
	}
}

func TestReadDrainTerminalFilesAndFinalizeOrder(t *testing.T) {
	supervisor := newTestSupervisor(t, 5*time.Second, 50*time.Millisecond)
	outcome, sink, resultPath := startHelper(t, supervisor, "echo")

	if err := supervisor.Finalize(context.Background(), outcome.Handle, execution.RetentionPolicy{}); !errors.Is(err, ErrFinalizeNotReady) {
		t.Fatalf("Finalize before exit = %v", err)
	}
	waitExited(t, sink)

	stdout, err := supervisor.Read(context.Background(), outcome.Handle, execution.StreamStdout, 0, 3)
	if err != nil || string(stdout.Data) != "out" || stdout.NextOffset != 3 || stdout.EOF {
		t.Fatalf("partial stdout read = %#v, %v", stdout, err)
	}
	stdout, err = supervisor.Read(context.Background(), outcome.Handle, execution.StreamStdout, stdout.NextOffset, 16)
	if err != nil || string(stdout.Data) != "put" || stdout.NextOffset != 6 || !stdout.EOF {
		t.Fatalf("final stdout read = %#v, %v", stdout, err)
	}
	if _, err := supervisor.Read(context.Background(), outcome.Handle, execution.StreamStdout, 7, 1); !errors.Is(err, ErrInvalidStreamRead) {
		t.Fatalf("invalid read offset = %v", err)
	}

	firstDrain, err := supervisor.Drain(context.Background(), outcome.Handle, execution.StreamOffsets{}, 8)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstDrain.Chunks[execution.StreamStdout]) != "output" || string(firstDrain.Chunks[execution.StreamStderr]) != "er" {
		t.Fatalf("bounded drain chunks = %#v", firstDrain.Chunks)
	}
	if firstDrain.EOF[execution.StreamStdout] != true || firstDrain.EOF[execution.StreamStderr] != false {
		t.Fatalf("bounded drain EOF = %#v", firstDrain.EOF)
	}
	if firstDrain.TerminalFiles.Paths["result"] != resultPath || firstDrain.TerminalFiles.ExitCode != 7 {
		t.Fatalf("terminal files = %#v", firstDrain.TerminalFiles)
	}
	if _, err := supervisor.Drain(context.Background(), outcome.Handle, execution.StreamOffsets{execution.StreamStdout: 7}, 1); !errors.Is(err, ErrInvalidDrain) {
		t.Fatalf("invalid drain offset = %v", err)
	}
	if _, err := supervisor.Drain(context.Background(), outcome.Handle, execution.StreamOffsets{}, 0); !errors.Is(err, ErrInvalidDrain) {
		t.Fatalf("invalid drain limit = %v", err)
	}
	if err := supervisor.Finalize(context.Background(), outcome.Handle, execution.RetentionPolicy{}); !errors.Is(err, ErrFinalizeNotReady) {
		t.Fatalf("Finalize before complete drain = %v", err)
	}
	secondDrain, err := supervisor.Drain(context.Background(), outcome.Handle, firstDrain.Offsets, 8)
	if err != nil || string(secondDrain.Chunks[execution.StreamStderr]) != "ror" || !secondDrain.EOF[execution.StreamStderr] {
		t.Fatalf("final drain = %#v, %v", secondDrain, err)
	}
	terminalDir := filepath.Dir(resultPath)
	if err := supervisor.Finalize(context.Background(), outcome.Handle, execution.RetentionPolicy{KeepTerminal: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(terminalDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("terminal directory still exists: %v", err)
	}
	if _, err := supervisor.Reconcile(context.Background(), outcome.Identity); err != nil {
		t.Fatal(err)
	}
	reconciled, _ := supervisor.Reconcile(context.Background(), outcome.Identity)
	if reconciled.Kind != execution.ReconcileUncertain {
		t.Fatalf("finalized identity reconcile = %#v", reconciled)
	}
}

func TestStopUncertainThenEventuallyDead(t *testing.T) {
	supervisor := newTestSupervisor(t, 5*time.Second, 20*time.Millisecond)
	outcome, sink, _ := startHelper(t, supervisor, "ignore-interrupt", "200ms")
	waitNotification(t, sink, execution.StreamStdout)
	stopped, err := supervisor.Stop(context.Background(), outcome.Handle, execution.StopIntent{Kind: execution.StopForCancel})
	if err != nil || stopped.Kind != execution.StopUncertain {
		t.Fatalf("Stop = %#v, %v", stopped, err)
	}

	blockedSupervisor := newTestSupervisor(t, 5*time.Second, 20*time.Millisecond)
	blockedLaunch := execution.LaunchToken{Value: "launch-blocked"}
	blocked := blockedSupervisor.Start(context.Background(), helperInvocation(t, blockedLaunch, t.TempDir(), "echo"), newRecordingSink(blockedLaunch))
	if blocked.Kind != execution.StartProvenNoChild {
		t.Fatalf("active slot released after uncertain stop: %#v", blocked)
	}
	waitExited(t, sink)
	reconciled, err := supervisor.Reconcile(context.Background(), outcome.Identity)
	if err != nil || reconciled.Kind != execution.ReconcileDead {
		t.Fatalf("eventual reconcile = %#v, %v", reconciled, err)
	}
}

func TestOutputIsCappedAndPrivateTerminalEnvironmentIsRemoved(t *testing.T) {
	supervisor := newTestSupervisor(t, 5*time.Second, 50*time.Millisecond)
	outcome, sink, _ := startHelper(t, supervisor, "cap")
	waitExited(t, sink)

	stdout, err := supervisor.Read(context.Background(), outcome.Handle, execution.StreamStdout, 0, streamCapacity+1)
	if err != nil || len(stdout.Data) != streamCapacity || !stdout.EOF {
		t.Fatalf("stdout cap = %d, EOF %v, err %v", len(stdout.Data), stdout.EOF, err)
	}
	stderr, err := supervisor.Read(context.Background(), outcome.Handle, execution.StreamStderr, 0, streamCapacity+1)
	if err != nil || len(stderr.Data) != streamCapacity || !stderr.EOF {
		t.Fatalf("stderr cap = %d, EOF %v, err %v", len(stderr.Data), stderr.EOF, err)
	}
	if !bytes.Equal(stdout.Data[:16], bytes.Repeat([]byte("o"), 16)) || !bytes.Equal(stderr.Data[:16], bytes.Repeat([]byte("e"), 16)) {
		t.Fatal("unexpected capped output contents")
	}
	sink.mu.Lock()
	stdoutNotifications := append([]int64(nil), sink.notifications[execution.StreamStdout]...)
	stderrNotifications := append([]int64(nil), sink.notifications[execution.StreamStderr]...)
	sink.mu.Unlock()
	if len(stdoutNotifications) == 0 || stdoutNotifications[len(stdoutNotifications)-1] != streamCapacity {
		t.Fatalf("stdout notifications = %#v", stdoutNotifications)
	}
	if len(stderrNotifications) == 0 || stderrNotifications[len(stderrNotifications)-1] != streamCapacity {
		t.Fatalf("stderr notifications = %#v", stderrNotifications)
	}
	if bytes.Contains(stdout.Data, []byte("private-env-leaked")) {
		t.Fatal("terminal result environment reached child")
	}
}

func TestRuntimeDeadlineSendsInterrupt(t *testing.T) {
	supervisor := newTestSupervisor(t, 30*time.Millisecond, 20*time.Millisecond)
	outcome, sink, _ := startHelper(t, supervisor, "wait-interrupt")
	waitExited(t, sink)
	reconciled, err := supervisor.Reconcile(context.Background(), outcome.Identity)
	if err != nil || reconciled.Kind != execution.ReconcileDead {
		t.Fatalf("deadline reconcile = %#v, %v", reconciled, err)
	}
}

func TestRuntimeDeadlineKillsChildThatIgnoresInterrupt(t *testing.T) {
	supervisor := newTestSupervisor(t, 30*time.Millisecond, 20*time.Millisecond)
	outcome, sink, _ := startHelper(t, supervisor, "ignore-interrupt", "5s")
	waitExited(t, sink)
	reconciled, err := supervisor.Reconcile(context.Background(), outcome.Identity)
	if err != nil || reconciled.Kind != execution.ReconcileDead {
		t.Fatalf("deadline escalation reconcile = %#v, %v", reconciled, err)
	}
}

func newTestSupervisor(t *testing.T, maxRuntime, stopWait time.Duration) *Supervisor {
	t.Helper()
	supervisor, err := New(Config{
		AllowedAdapterID:  helperAdapterID,
		AllowedExecutable: os.Args[0],
		MaxRuntime:        maxRuntime,
		StopWait:          stopWait,
	})
	if err != nil {
		t.Fatal(err)
	}
	return supervisor
}

func startHelper(t *testing.T, supervisor *Supervisor, mode string, arguments ...string) (execution.StartOutcome, *recordingSink, string) {
	t.Helper()
	launch := execution.LaunchToken{Value: fmt.Sprintf("launch-%s-%d", mode, time.Now().UnixNano())}
	terminalDir := filepath.Join(t.TempDir(), "attempt-terminal")
	if err := os.MkdirAll(terminalDir, 0o700); err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(terminalDir, "result.json")
	invocation := helperInvocation(t, launch, terminalDir, mode, arguments...)
	sink := newRecordingSink(launch)
	outcome := supervisor.Start(context.Background(), invocation, sink)
	if outcome.Kind != execution.Started || !outcome.Handle.Valid() || !outcome.Identity.Valid() || outcome.LaunchToken != launch {
		t.Fatalf("Start = %#v", outcome)
	}
	return outcome, sink, resultPath
}

func helperInvocation(t *testing.T, launch execution.LaunchToken, terminalDir, mode string, arguments ...string) execution.Invocation {
	t.Helper()
	resultPath := filepath.Join(terminalDir, "result.json")
	args := []string{"-test.run=TestHelperProcess", "--", mode}
	args = append(args, arguments...)
	invocation, err := execution.NewInvocation(execution.InvocationParams{
		AdapterID:  helperAdapterID,
		Executable: os.Args[0],
		Arguments:  args,
		Environment: map[string]string{
			"GO_WANT_DEV_SUPERVISOR_HELPER": "1",
			"HOME":                          os.Getenv("HOME"),
			"CODEX_HOME":                    os.Getenv("CODEX_HOME"),
			terminalResultEnvironment:       resultPath,
		},
		WorkingRoot: terminalDir,
		LaunchToken: launch,
	})
	if err != nil {
		t.Fatal(err)
	}
	return invocation
}

func waitExited(t *testing.T, sink *recordingSink) {
	t.Helper()
	select {
	case <-sink.exited:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for helper exit")
	}
}

func waitNotification(t *testing.T, sink *recordingSink, kind execution.StreamKind) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		sink.mu.Lock()
		notified := len(sink.notifications[kind]) > 0
		sink.mu.Unlock()
		if notified {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for helper output")
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_DEV_SUPERVISOR_HELPER") != "1" {
		return
	}
	separator := 0
	for index, argument := range os.Args {
		if argument == "--" {
			separator = index
			break
		}
	}
	if separator == 0 || separator+1 >= len(os.Args) {
		os.Exit(90)
	}
	mode := os.Args[separator+1]
	arguments := os.Args[separator+2:]
	switch mode {
	case "echo":
		_, _ = os.Stdout.WriteString("output")
		_, _ = os.Stderr.WriteString("error")
		os.Exit(7)
	case "sleep":
		duration, err := time.ParseDuration(arguments[0])
		if err != nil {
			os.Exit(91)
		}
		time.Sleep(duration)
	case "ignore-interrupt":
		signal.Ignore(os.Interrupt)
		_, _ = os.Stdout.WriteString("ready")
		duration, err := time.ParseDuration(arguments[0])
		if err != nil {
			os.Exit(92)
		}
		time.Sleep(duration)
	case "wait-interrupt":
		interrupt := make(chan os.Signal, 1)
		signal.Notify(interrupt, os.Interrupt)
		<-interrupt
	case "cap":
		if os.Getenv(terminalResultEnvironment) != "" {
			_, _ = os.Stdout.WriteString("private-env-leaked")
		}
		chunk := bytes.Repeat([]byte("o"), streamCapacity+1024)
		_, _ = os.Stdout.Write(chunk)
		chunk = bytes.Repeat([]byte("e"), streamCapacity+1024)
		_, _ = os.Stderr.Write(chunk)
	default:
		os.Exit(93)
	}
	os.Exit(0)
}
