package execution

import (
	"context"

	"github.com/Yangyang96/chora/internal/domain"
)

type AgentAdapter interface {
	ID() string
	Capabilities() AdapterCapabilities
	PrepareStart(context.Context, StartRequest) (Invocation, error)
	PrepareResume(context.Context, ResumeRequest) (Invocation, error)
	DecodeEvent(EventChunk) (DecodedChunk, error)
	DecodeTerminal(TerminalFiles) (TerminalResult, error)
	Fingerprint(context.Context) (RuntimeFingerprint, error)
}

// BindingFingerprinter is an optional extension for adapters whose exact
// Runtime identity depends on an immutable Attempt execution-profile binding.
// Adapters without source/profile variants continue to use Fingerprint.
type BindingFingerprinter interface {
	FingerprintForBinding(context.Context, domain.AgentExecutionProfileBinding) (RuntimeFingerprint, error)
}

// BoundStartPreparer is an optional extension for adapters that must select an
// exact Runtime source and prepare its Invocation in one atomic operation.
// AgentAdapter intentionally keeps its existing required methods.
type BoundStartPreparer interface {
	PrepareBoundStart(context.Context, StartRequest) (StartPreparation, error)
}

type ProcessSupervisor interface {
	Start(context.Context, Invocation, RuntimeSink) StartOutcome
	Stop(context.Context, RuntimeHandle, StopIntent) (StopOutcome, error)
	Reconcile(context.Context, ProcessIdentity) (ReconcileOutcome, error)
	ReconcileLaunch(context.Context, LaunchToken) (ReconcileOutcome, error)
	Read(context.Context, RuntimeHandle, StreamKind, int64, int) (StreamChunk, error)
	Drain(context.Context, RuntimeHandle, StreamOffsets, int) (DrainOutcome, error)
	Finalize(context.Context, RuntimeHandle, RetentionPolicy) error
}

// StdinCloser is an optional supervisor extension for runtimes that must hold
// stdin open until their terminal signal is observed and then close it so the
// process shuts down cleanly (e.g. the real Pi `--mode rpc` loop exits on stdin
// EOF). Supervisors that deliver stdin up front may omit it.
type StdinCloser interface {
	CloseStdin(context.Context, RuntimeHandle) error
}

// ContractFingerprinter binds additive native evidence observers only for contracts that use them.
type ContractFingerprinter interface {
	FingerprintForContract(context.Context, domain.AgentExecutionProfileBinding, []byte) (RuntimeFingerprint, error)
}
