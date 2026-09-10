package processbound

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

const (
	DefaultTimeout     = 2 * time.Minute
	DefaultStdoutLimit = 1 << 20
	DefaultStderrLimit = 1 << 20
	EventLineLimit     = 256 << 10
	EventStreamLimit   = 4 << 20
	ResultLimit        = 1 << 20
)

var ErrOutputLimit = errors.New("subprocess output limit exceeded")

type Spec struct {
	Name        string
	Args        []string
	Dir         string
	Env         []string
	Stdin       io.Reader
	StdoutLimit int
	StderrLimit int
}

type Result struct {
	Stdout []byte
	Stderr []byte
}

func WithTimeout(parent context.Context) (context.Context, context.CancelFunc) {
	return WithTimeoutDuration(parent, DefaultTimeout)
}

func WithTimeoutDuration(parent context.Context, duration time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, duration)
}

func Run(parent context.Context, spec Spec) (Result, error) {
	return RunWithTimeout(parent, spec, DefaultTimeout)
}

func RunWithTimeout(parent context.Context, spec Spec, duration time.Duration) (Result, error) {
	ctx, cancel := WithTimeoutDuration(parent, duration)
	defer cancel()
	stdout := NewBuffer(limitOrDefault(spec.StdoutLimit, DefaultStdoutLimit))
	stderr := NewBuffer(limitOrDefault(spec.StderrLimit, DefaultStderrLimit))
	command := exec.CommandContext(ctx, spec.Name, spec.Args...)
	command.Dir = spec.Dir
	command.Env = spec.Env
	command.Stdin = spec.Stdin
	command.Stdout = stdout
	command.Stderr = stderr
	command.WaitDelay = time.Second
	if err := command.Start(); err != nil {
		return Result{}, fmt.Errorf("start %s: %w", spec.Name, err)
	}
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	var err error
	select {
	case err = <-wait:
	case <-ctx.Done():
		_ = command.Process.Kill()
		<-wait
		return Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}, ctx.Err()
	case <-stdout.Done():
		_ = command.Process.Kill()
		<-wait
		return Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}, ErrOutputLimit
	case <-stderr.Done():
		_ = command.Process.Kill()
		<-wait
		return Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}, ErrOutputLimit
	}
	result := Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if outputErr := errors.Join(stdout.Err(), stderr.Err()); outputErr != nil {
		return result, outputErr
	}
	if err != nil {
		return result, fmt.Errorf("run %s: %w", spec.Name, err)
	}
	return result, nil
}

type Buffer struct {
	mu    sync.Mutex
	data  bytes.Buffer
	limit int
	err   error
	done  chan struct{}
	once  sync.Once
}

func NewBuffer(limit int) *Buffer {
	return &Buffer{limit: limit, done: make(chan struct{})}
}

func (buffer *Buffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	if buffer.err != nil {
		return len(data), nil
	}
	if len(data) > buffer.limit-buffer.data.Len() {
		buffer.err = ErrOutputLimit
		buffer.once.Do(func() { close(buffer.done) })
		return len(data), nil
	}
	return buffer.data.Write(data)
}

func (buffer *Buffer) Done() <-chan struct{} {
	return buffer.done
}

func (buffer *Buffer) Bytes() []byte {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return append([]byte(nil), buffer.data.Bytes()...)
}

func (buffer *Buffer) String() string {
	return string(buffer.Bytes())
}

func (buffer *Buffer) Err() error {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.err
}

func limitOrDefault(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}
