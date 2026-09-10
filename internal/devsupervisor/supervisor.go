package devsupervisor

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Yangyang96/chora/internal/execution"
)

const (
	terminalResultEnvironment = "CHORA_TERMINAL_RESULT_PATH"
	defaultMaxRuntime         = 20 * time.Minute
	defaultStopWait           = 3 * time.Second
	streamCapacity            = 4 << 20
)

var (
	ErrInvalidConfig      = errors.New("invalid dev supervisor config")
	ErrUnknownHandle      = errors.New("unknown runtime handle")
	ErrInvalidStreamRead  = errors.New("invalid stream read")
	ErrInvalidDrain       = errors.New("invalid stream drain")
	ErrFinalizeNotReady   = errors.New("runtime is not ready to finalize")
	applicationActiveSlot = struct {
		sync.Mutex
		active bool
	}{}
)

type Config struct {
	AllowedAdapterID  string
	AllowedExecutable string
	MaxRuntime        time.Duration
	StopWait          time.Duration
}

type Supervisor struct {
	config Config

	mu         sync.Mutex
	byHandle   map[string]*processRecord
	byIdentity map[string]*processRecord
	byLaunch   map[string]*processRecord
}

type processRecord struct {
	mu sync.Mutex

	cmd         *exec.Cmd
	handle      execution.RuntimeHandle
	identity    execution.ProcessIdentity
	launch      execution.LaunchToken
	resultPath  string
	terminalDir string
	sink        execution.RuntimeSink
	stdout      cappedBuffer
	stderr      cappedBuffer
	done        chan struct{}
	exited      bool
	exitCode    int
	drainedEOF  map[execution.StreamKind]bool
}

type cappedBuffer struct {
	mu   sync.RWMutex
	data []byte
}

func New(config Config) (*Supervisor, error) {
	config.AllowedAdapterID = strings.TrimSpace(config.AllowedAdapterID)
	config.AllowedExecutable = strings.TrimSpace(config.AllowedExecutable)
	if config.AllowedAdapterID == "" || config.AllowedExecutable == "" || config.MaxRuntime < 0 || config.StopWait < 0 {
		return nil, ErrInvalidConfig
	}
	if config.MaxRuntime == 0 || config.MaxRuntime > defaultMaxRuntime {
		config.MaxRuntime = defaultMaxRuntime
	}
	if config.StopWait == 0 || config.StopWait > defaultStopWait {
		config.StopWait = defaultStopWait
	}
	return &Supervisor{
		config:     config,
		byHandle:   make(map[string]*processRecord),
		byIdentity: make(map[string]*processRecord),
		byLaunch:   make(map[string]*processRecord),
	}, nil
}

func (s *Supervisor) Start(_ context.Context, invocation execution.Invocation, sink execution.RuntimeSink) execution.StartOutcome {
	launch := invocation.LaunchToken()
	if invocation.AdapterID() != s.config.AllowedAdapterID || invocation.Executable() != s.config.AllowedExecutable {
		return noChild(launch, "invocation is not allowed by supervisor config")
	}
	if sink == nil || sink.Binding() != launch {
		return noChild(launch, "runtime sink binding does not match launch token")
	}
	s.mu.Lock()
	_, launchKnown := s.byLaunch[launch.Value]
	s.mu.Unlock()
	if launchKnown {
		return noChild(launch, "launch token is already known")
	}
	if !acquireApplicationSlot() {
		return noChild(launch, "another child process is active")
	}

	handleValue, err := randomValue("handle")
	if err != nil {
		releaseApplicationSlot()
		return noChild(launch, err.Error())
	}
	identityValue, err := randomValue("identity")
	if err != nil {
		releaseApplicationSlot()
		return noChild(launch, err.Error())
	}

	environment := invocation.Environment()
	resultPath := environment[terminalResultEnvironment]
	delete(environment, terminalResultEnvironment)
	cmd := exec.Command(invocation.Executable(), invocation.Arguments()...)
	cmd.Dir = invocation.WorkingRoot()
	cmd.Env = mergedEnvironment(environment)
	cmd.Stdin = bytes.NewReader(invocation.Stdin())
	record := &processRecord{
		cmd:         cmd,
		handle:      execution.RuntimeHandle{Value: handleValue},
		identity:    execution.ProcessIdentity{Value: identityValue},
		launch:      launch,
		resultPath:  resultPath,
		terminalDir: terminalDirectory(resultPath),
		sink:        sink,
		done:        make(chan struct{}),
		exitCode:    -1,
		drainedEOF:  make(map[execution.StreamKind]bool, 2),
	}
	cmd.Stdout = notifyingWriter{record: record, kind: execution.StreamStdout}
	cmd.Stderr = notifyingWriter{record: record, kind: execution.StreamStderr}
	if err := cmd.Start(); err != nil {
		releaseApplicationSlot()
		return noChild(launch, fmt.Sprintf("start child: %v", err))
	}

	s.mu.Lock()
	s.byHandle[record.handle.Value] = record
	s.byIdentity[record.identity.Value] = record
	s.byLaunch[record.launch.Value] = record
	s.mu.Unlock()

	go s.wait(record)
	go s.interruptAtDeadline(record)
	return execution.StartOutcome{
		Kind:        execution.Started,
		Handle:      record.handle,
		Identity:    record.identity,
		LaunchToken: launch,
	}
}

func (s *Supervisor) Stop(ctx context.Context, handle execution.RuntimeHandle, _ execution.StopIntent) (execution.StopOutcome, error) {
	record, err := s.recordForHandle(handle)
	if err != nil {
		return execution.StopOutcome{}, err
	}
	if record.isExited() {
		return execution.StopOutcome{Kind: execution.StopConfirmed}, nil
	}
	if err := record.cmd.Process.Signal(os.Interrupt); err != nil && !record.isExited() {
		return execution.StopOutcome{}, fmt.Errorf("interrupt child: %w", err)
	}
	timer := time.NewTimer(s.config.StopWait)
	defer timer.Stop()
	select {
	case <-record.done:
		return execution.StopOutcome{Kind: execution.StopConfirmed}, nil
	case <-ctx.Done():
		return execution.StopOutcome{}, ctx.Err()
	case <-timer.C:
		return execution.StopOutcome{Kind: execution.StopUncertain, Diagnostic: "child did not exit before stop deadline"}, nil
	}
}

func (s *Supervisor) Reconcile(_ context.Context, identity execution.ProcessIdentity) (execution.ReconcileOutcome, error) {
	s.mu.Lock()
	record := s.byIdentity[identity.Value]
	s.mu.Unlock()
	return reconcileRecord(record), nil
}

func (s *Supervisor) ReconcileLaunch(_ context.Context, launch execution.LaunchToken) (execution.ReconcileOutcome, error) {
	s.mu.Lock()
	record := s.byLaunch[launch.Value]
	s.mu.Unlock()
	return reconcileRecord(record), nil
}

func (s *Supervisor) Read(_ context.Context, handle execution.RuntimeHandle, kind execution.StreamKind, offset int64, limit int) (execution.StreamChunk, error) {
	record, err := s.recordForHandle(handle)
	if err != nil {
		return execution.StreamChunk{}, err
	}
	buffer, err := record.stream(kind)
	if err != nil || offset < 0 || limit <= 0 {
		return execution.StreamChunk{}, ErrInvalidStreamRead
	}
	data, next, ok := buffer.read(offset, limit)
	if !ok {
		return execution.StreamChunk{}, ErrInvalidStreamRead
	}
	return execution.StreamChunk{Data: data, NextOffset: next, EOF: record.isExited() && next == buffer.length()}, nil
}

func (s *Supervisor) Drain(_ context.Context, handle execution.RuntimeHandle, offsets execution.StreamOffsets, limit int) (execution.DrainOutcome, error) {
	record, err := s.recordForHandle(handle)
	if err != nil {
		return execution.DrainOutcome{}, err
	}
	if limit <= 0 {
		return execution.DrainOutcome{}, ErrInvalidDrain
	}
	for kind := range offsets {
		if kind != execution.StreamStdout && kind != execution.StreamStderr {
			return execution.DrainOutcome{}, ErrInvalidDrain
		}
	}

	outcome := execution.DrainOutcome{
		Chunks:  make(map[execution.StreamKind][]byte, 2),
		Offsets: make(execution.StreamOffsets, 2),
		EOF:     make(map[execution.StreamKind]bool, 2),
		TerminalFiles: execution.TerminalFiles{
			Paths: map[string]string{"result": record.resultPath},
		},
	}
	remaining := limit
	for _, kind := range []execution.StreamKind{execution.StreamStdout, execution.StreamStderr} {
		buffer, _ := record.stream(kind)
		offset := offsets[kind]
		data, next, ok := buffer.read(offset, remaining)
		if !ok {
			return execution.DrainOutcome{}, ErrInvalidDrain
		}
		outcome.Chunks[kind] = data
		outcome.Offsets[kind] = next
		remaining -= len(data)
		eof := record.isExited() && next == buffer.length()
		outcome.EOF[kind] = eof
		if eof {
			record.markDrained(kind)
		}
	}
	if exitCode, exited := record.exitStatus(); exited {
		outcome.TerminalFiles.ExitCode = exitCode
	}
	return outcome, nil
}

func (s *Supervisor) Finalize(_ context.Context, handle execution.RuntimeHandle, _ execution.RetentionPolicy) error {
	record, err := s.recordForHandle(handle)
	if err != nil {
		return err
	}
	if !record.readyToFinalize() {
		return ErrFinalizeNotReady
	}
	if record.terminalDir != "" {
		if err := os.RemoveAll(record.terminalDir); err != nil {
			return fmt.Errorf("remove terminal directory: %w", err)
		}
	}
	s.mu.Lock()
	delete(s.byHandle, record.handle.Value)
	delete(s.byIdentity, record.identity.Value)
	delete(s.byLaunch, record.launch.Value)
	s.mu.Unlock()
	return nil
}

func (s *Supervisor) wait(record *processRecord) {
	err := record.cmd.Wait()
	exitCode := -1
	if record.cmd.ProcessState != nil {
		exitCode = record.cmd.ProcessState.ExitCode()
	} else if err == nil {
		exitCode = 0
	}
	record.mu.Lock()
	record.exited = true
	record.exitCode = exitCode
	close(record.done)
	record.mu.Unlock()
	releaseApplicationSlot()
	safeExited(record.sink)
}

func (s *Supervisor) interruptAtDeadline(record *processRecord) {
	timer := time.NewTimer(s.config.MaxRuntime)
	defer timer.Stop()
	select {
	case <-record.done:
		return
	case <-timer.C:
		_ = record.cmd.Process.Signal(os.Interrupt)
	}
	escalation := time.NewTimer(s.config.StopWait)
	defer escalation.Stop()
	select {
	case <-record.done:
	case <-escalation.C:
		_ = record.cmd.Process.Kill()
	}
}

func (s *Supervisor) recordForHandle(handle execution.RuntimeHandle) (*processRecord, error) {
	s.mu.Lock()
	record := s.byHandle[handle.Value]
	s.mu.Unlock()
	if record == nil {
		return nil, ErrUnknownHandle
	}
	return record, nil
}

func (record *processRecord) stream(kind execution.StreamKind) (*cappedBuffer, error) {
	switch kind {
	case execution.StreamStdout:
		return &record.stdout, nil
	case execution.StreamStderr:
		return &record.stderr, nil
	default:
		return nil, ErrInvalidStreamRead
	}
}

func (record *processRecord) isExited() bool {
	record.mu.Lock()
	defer record.mu.Unlock()
	return record.exited
}

func (record *processRecord) exitStatus() (int, bool) {
	record.mu.Lock()
	defer record.mu.Unlock()
	return record.exitCode, record.exited
}

func (record *processRecord) markDrained(kind execution.StreamKind) {
	record.mu.Lock()
	record.drainedEOF[kind] = true
	record.mu.Unlock()
}

func (record *processRecord) readyToFinalize() bool {
	record.mu.Lock()
	defer record.mu.Unlock()
	return record.exited && record.drainedEOF[execution.StreamStdout] && record.drainedEOF[execution.StreamStderr]
}

type notifyingWriter struct {
	record *processRecord
	kind   execution.StreamKind
}

func (writer notifyingWriter) Write(data []byte) (int, error) {
	buffer, _ := writer.record.stream(writer.kind)
	accepted, offset := buffer.append(data)
	if accepted > 0 {
		safeNotify(writer.record.sink, writer.kind, offset)
	}
	return len(data), nil
}

func (buffer *cappedBuffer) append(data []byte) (int, int64) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	available := streamCapacity - len(buffer.data)
	if available <= 0 {
		return 0, int64(len(buffer.data))
	}
	if len(data) > available {
		data = data[:available]
	}
	buffer.data = append(buffer.data, data...)
	return len(data), int64(len(buffer.data))
}

func (buffer *cappedBuffer) read(offset int64, limit int) ([]byte, int64, bool) {
	buffer.mu.RLock()
	defer buffer.mu.RUnlock()
	if offset < 0 || offset > int64(len(buffer.data)) || limit < 0 {
		return nil, 0, false
	}
	end := int64(len(buffer.data))
	if limit == 0 {
		end = offset
	} else if end-offset > int64(limit) {
		end = offset + int64(limit)
	}
	return append([]byte(nil), buffer.data[offset:end]...), end, true
}

func (buffer *cappedBuffer) length() int64 {
	buffer.mu.RLock()
	defer buffer.mu.RUnlock()
	return int64(len(buffer.data))
}

func reconcileRecord(record *processRecord) execution.ReconcileOutcome {
	if record == nil {
		return execution.ReconcileOutcome{Kind: execution.ReconcileUncertain, Diagnostic: "process is unknown to this supervisor lifecycle"}
	}
	kind := execution.ReconcileAlive
	if record.isExited() {
		kind = execution.ReconcileDead
	}
	return execution.ReconcileOutcome{Kind: kind, Handle: record.handle, LaunchToken: record.launch}
}

func noChild(launch execution.LaunchToken, diagnostic string) execution.StartOutcome {
	return execution.StartOutcome{Kind: execution.StartProvenNoChild, LaunchToken: launch, Diagnostic: diagnostic}
}

func acquireApplicationSlot() bool {
	applicationActiveSlot.Lock()
	defer applicationActiveSlot.Unlock()
	if applicationActiveSlot.active {
		return false
	}
	applicationActiveSlot.active = true
	return true
}

func releaseApplicationSlot() {
	applicationActiveSlot.Lock()
	applicationActiveSlot.active = false
	applicationActiveSlot.Unlock()
}

func randomValue(prefix string) (string, error) {
	value := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, value); err != nil {
		return "", fmt.Errorf("generate runtime identity: %w", err)
	}
	return prefix + ":" + hex.EncodeToString(value), nil
}

func mergedEnvironment(overrides map[string]string) []string {
	values := make(map[string]string)
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok && key != terminalResultEnvironment {
			values[key] = value
		}
	}
	for key, value := range overrides {
		if key != terminalResultEnvironment {
			values[key] = value
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}

func terminalDirectory(resultPath string) string {
	if resultPath == "" {
		return ""
	}
	directory := filepath.Clean(filepath.Dir(resultPath))
	if directory == "." || directory == string(filepath.Separator) {
		return ""
	}
	return directory
}

func safeNotify(sink execution.RuntimeSink, kind execution.StreamKind, offset int64) {
	defer func() { _ = recover() }()
	sink.Notify(kind, offset)
}

func safeExited(sink execution.RuntimeSink) {
	defer func() { _ = recover() }()
	sink.Exited()
}

var _ execution.ProcessSupervisor = (*Supervisor)(nil)
