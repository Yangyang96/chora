package processbound

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestDefaultTimeoutRemainsTwoMinutes(t *testing.T) {
	if DefaultTimeout != 2*time.Minute {
		t.Fatalf("DefaultTimeout = %s, want 2m", DefaultTimeout)
	}
}

func TestWithTimeoutDurationUsesRequestedDeadline(t *testing.T) {
	started := time.Now()
	ctx, cancel := WithTimeoutDuration(context.Background(), 250*time.Millisecond)
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("WithTimeoutDuration context has no deadline")
	}
	remaining := deadline.Sub(started)
	if remaining < 200*time.Millisecond || remaining > 300*time.Millisecond {
		t.Fatalf("requested deadline remaining = %s, want about 250ms", remaining)
	}
}

func TestWithTimeoutDurationPropagatesExplicitParentCancellation(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	ctx, cancel := WithTimeoutDuration(parent, time.Hour)
	defer cancel()
	cancelParent()
	<-ctx.Done()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("context error = %v, want canceled", ctx.Err())
	}
}

func TestWithTimeoutDurationPreservesEarlierParentDeadline(t *testing.T) {
	parentDeadline := time.Now().Add(250 * time.Millisecond)
	parent, cancelParent := context.WithDeadline(context.Background(), parentDeadline)
	defer cancelParent()
	ctx, cancel := WithTimeoutDuration(parent, time.Hour)
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("WithTimeoutDuration context has no deadline")
	}
	if !deadline.Equal(parentDeadline) {
		t.Fatalf("deadline = %s, want earlier parent deadline %s", deadline, parentDeadline)
	}
}

func TestRunWithTimeoutUsesRequestedDuration(t *testing.T) {
	if os.Getenv("CHORA_PROCESSBOUND_HELPER") == "duration-hang" {
		for {
			time.Sleep(time.Second)
		}
	}
	started := time.Now()
	_, err := RunWithTimeout(context.Background(), Spec{
		Name: os.Args[0],
		Args: []string{"-test.run=TestRunWithTimeoutUsesRequestedDuration"},
		Env:  append(os.Environ(), "CHORA_PROCESSBOUND_HELPER=duration-hang"),
	}, 50*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RunWithTimeout() error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("RunWithTimeout() elapsed = %s, custom duration was not applied", elapsed)
	}
}

func TestRunKillsAndReapsHungChildAtDeadline(t *testing.T) {
	if os.Getenv("CHORA_PROCESSBOUND_HELPER") == "hang" {
		if err := os.WriteFile(os.Getenv("CHORA_PROCESSBOUND_PID"), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		for {
			time.Sleep(time.Second)
		}
	}
	pidPath := t.TempDir() + "/pid"
	ctx := newTestDeadlineContext()
	defer func() {
		if ctx.Err() == nil {
			ctx.expire()
		}
	}()
	runDone := make(chan error, 1)
	go func() {
		_, err := Run(ctx, Spec{
			Name: os.Args[0],
			Args: []string{"-test.run=TestRunKillsAndReapsHungChildAtDeadline"},
			Env: append(os.Environ(),
				"CHORA_PROCESSBOUND_HELPER=hang",
				"CHORA_PROCESSBOUND_PID="+pidPath,
			),
			StdoutLimit: 64,
			StderrLimit: 64,
		})
		runDone <- err
	}()

	pid := waitForHelperPID(t, pidPath, runDone)
	process, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = process.Kill() }()
	ctx.expire()
	if err := ctx.Err(); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired context error = %v, want deadline exceeded", err)
	}

	select {
	case err = <-runDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after deadline")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run() error = %v, want deadline exceeded", err)
	}
	if err := process.Signal(syscall.Signal(0)); err == nil {
		t.Fatalf("child %d remains alive after Run returned", pid)
	}
}

func waitForHelperPID(t *testing.T, pidPath string, runDone <-chan error) int {
	t.Helper()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case err := <-runDone:
			t.Fatalf("Run returned before helper was ready: %v", err)
		case <-ticker.C:
			data, err := os.ReadFile(pidPath)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasSuffix(string(data), "\n") {
				continue
			}
			pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil {
				t.Fatalf("parse helper PID: %v", err)
			}
			return pid
		case <-timer.C:
			t.Fatal("helper did not publish its PID")
		}
	}
}

type testDeadlineContext struct {
	done chan struct{}
}

func newTestDeadlineContext() *testDeadlineContext {
	return &testDeadlineContext{done: make(chan struct{})}
}

func (ctx *testDeadlineContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (ctx *testDeadlineContext) Done() <-chan struct{}       { return ctx.done }
func (ctx *testDeadlineContext) Value(any) any               { return nil }

func (ctx *testDeadlineContext) Err() error {
	select {
	case <-ctx.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

func (ctx *testDeadlineContext) expire() {
	close(ctx.done)
}

func TestRunRejectsOversizedStderr(t *testing.T) {
	if os.Getenv("CHORA_PROCESSBOUND_HELPER") == "stderr" {
		_, _ = os.Stderr.WriteString(strings.Repeat("x", 1024))
		return
	}
	_, err := Run(context.Background(), Spec{
		Name:        os.Args[0],
		Args:        []string{"-test.run=TestRunRejectsOversizedStderr"},
		Env:         append(os.Environ(), "CHORA_PROCESSBOUND_HELPER=stderr"),
		StdoutLimit: 64,
		StderrLimit: 64,
	})
	if !errors.Is(err, ErrOutputLimit) {
		t.Fatalf("Run() error = %v, want output limit", err)
	}
}
