package localweb

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/Yangyang96/chora/internal/execution"
)

type adapterRegistry struct {
	mu       sync.RWMutex
	adapters map[string]execution.AgentAdapter
}

func newAdapterRegistry() *adapterRegistry {
	return &adapterRegistry{adapters: make(map[string]execution.AgentAdapter)}
}

func (registry *adapterRegistry) Set(adapter execution.AgentAdapter) {
	registry.mu.Lock()
	registry.adapters[adapter.ID()] = adapter
	registry.mu.Unlock()
}

func (registry *adapterRegistry) Get(id string) (execution.AgentAdapter, error) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	adapter := registry.adapters[id]
	if adapter == nil || adapter.ID() != id {
		return nil, fmt.Errorf("agent adapter %q is disabled", id)
	}
	return adapter, nil
}

type fakeSupervisor struct {
	mu                  sync.Mutex
	invocation          execution.Invocation
	sink                execution.RuntimeSink
	exited              bool
	reconcileOverride   execution.ReconcileOutcomeKind
	reconcileDiagnostic string
	streams             map[execution.StreamKind][]byte
	terminalFiles       execution.TerminalFiles
}

func newFakeSupervisor() *fakeSupervisor {
	return &fakeSupervisor{streams: map[execution.StreamKind][]byte{
		execution.StreamStdout: []byte("{\"type\":\"thread.started\",\"thread_id\":\"session-local-fake\"}\n"),
		execution.StreamStderr: []byte("{\"type\":\"runtime.notice\",\"level\":\"info\"}\n"),
	}}
}

func (supervisor *fakeSupervisor) Start(_ context.Context, invocation execution.Invocation, sink execution.RuntimeSink) execution.StartOutcome {
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	supervisor.invocation = invocation
	supervisor.sink = sink
	supervisor.exited = false
	return execution.StartOutcome{
		Kind:        execution.Started,
		Handle:      execution.RuntimeHandle{Value: "local-fake-handle"},
		Identity:    execution.ProcessIdentity{Value: "local-fake-process"},
		LaunchToken: invocation.LaunchToken(),
	}
}

func (supervisor *fakeSupervisor) Notify(kind execution.StreamKind) error {
	supervisor.mu.Lock()
	sink := supervisor.sink
	data, ok := supervisor.streams[kind]
	supervisor.mu.Unlock()
	if sink == nil || !ok {
		return errors.New("fake runtime is not running")
	}
	sink.Notify(kind, int64(len(data)))
	return nil
}

func (supervisor *fakeSupervisor) Exit() error {
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	if supervisor.sink == nil {
		return errors.New("fake runtime is not running")
	}
	supervisor.exited = true
	supervisor.sink.Exited()
	return nil
}

func (supervisor *fakeSupervisor) Stop(context.Context, execution.RuntimeHandle, execution.StopIntent) (execution.StopOutcome, error) {
	if err := supervisor.Exit(); err != nil {
		return execution.StopOutcome{}, err
	}
	return execution.StopOutcome{Kind: execution.StopConfirmed}, nil
}

func (supervisor *fakeSupervisor) Reconcile(context.Context, execution.ProcessIdentity) (execution.ReconcileOutcome, error) {
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	if supervisor.reconcileOverride != "" {
		return execution.ReconcileOutcome{Kind: supervisor.reconcileOverride, Handle: execution.RuntimeHandle{Value: "local-fake-handle"}, LaunchToken: supervisor.invocation.LaunchToken(), Diagnostic: supervisor.reconcileDiagnostic}, nil
	}
	kind := execution.ReconcileAlive
	if supervisor.exited {
		kind = execution.ReconcileDead
	}
	return execution.ReconcileOutcome{Kind: kind, Handle: execution.RuntimeHandle{Value: "local-fake-handle"}, LaunchToken: supervisor.invocation.LaunchToken()}, nil
}

func (supervisor *fakeSupervisor) ReconcileLaunch(_ context.Context, launch execution.LaunchToken) (execution.ReconcileOutcome, error) {
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	kind := execution.ReconcileAlive
	if supervisor.exited {
		kind = execution.ReconcileDead
	}
	return execution.ReconcileOutcome{Kind: kind, Handle: execution.RuntimeHandle{Value: "local-fake-handle"}, LaunchToken: launch}, nil
}

func (supervisor *fakeSupervisor) Read(_ context.Context, _ execution.RuntimeHandle, kind execution.StreamKind, offset int64, limit int) (execution.StreamChunk, error) {
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	data, ok := supervisor.streams[kind]
	if !ok || offset < 0 || offset > int64(len(data)) || limit <= 0 {
		return execution.StreamChunk{}, errors.New("invalid fake stream read")
	}
	end := len(data)
	if remaining := end - int(offset); remaining > limit {
		end = int(offset) + limit
	}
	return execution.StreamChunk{Data: append([]byte(nil), data[offset:end]...), NextOffset: int64(end)}, nil
}

func (supervisor *fakeSupervisor) Drain(_ context.Context, _ execution.RuntimeHandle, offsets execution.StreamOffsets, limit int) (execution.DrainOutcome, error) {
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	if limit <= 0 {
		return execution.DrainOutcome{}, errors.New("invalid fake drain limit")
	}
	outcome := execution.DrainOutcome{
		Chunks:        make(map[execution.StreamKind][]byte, 2),
		Offsets:       make(execution.StreamOffsets, 2),
		EOF:           make(map[execution.StreamKind]bool, 2),
		TerminalFiles: supervisor.terminalFiles,
	}
	total := 0
	for _, kind := range []execution.StreamKind{execution.StreamStdout, execution.StreamStderr} {
		data := supervisor.streams[kind]
		offset := offsets[kind]
		if offset < 0 || offset > int64(len(data)) {
			return execution.DrainOutcome{}, errors.New("invalid fake drain offset")
		}
		chunk := data[offset:]
		total += len(chunk)
		if total > limit {
			return execution.DrainOutcome{}, errors.New("fake drain exceeds limit")
		}
		outcome.Chunks[kind] = append([]byte(nil), chunk...)
		outcome.Offsets[kind] = int64(len(data))
		outcome.EOF[kind] = true
	}
	return outcome, nil
}

func (supervisor *fakeSupervisor) Finalize(context.Context, execution.RuntimeHandle, execution.RetentionPolicy) error {
	return nil
}

var _ execution.ProcessSupervisor = (*fakeSupervisor)(nil)
