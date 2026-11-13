// Package hermes contains the Hermes Agent stream-json candidate. Native launch
// remains unavailable until configuration, retry and gateway custody are proved.
package hermes

import (
	"context"
	"errors"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
)

const AdapterID = "hermes"
const protocolVersion = "hermes.chat.stream-json.v1"

var (
	ErrInstallation = errors.New("hermes_installation_unavailable")
	ErrIdentity     = errors.New("hermes_identity_drift")
	ErrSafety       = errors.New("hermes_safe_start_unverifiable: configuration_sources,retry_control,gateway_custody,executable_closure")
)

// Config explicitly selects the installed launcher chain, source and existing
// authentication scope. No path is discovered from ambient HOME or PATH.
// Inspection never runs these programs or reads credential/configuration contents.
type Config struct {
	Executable, NativeLauncher, Interpreter, SourceRoot, AuthHome string
}

// Adapter's zero value supports pure protocol projection only. It deliberately
// cannot produce an invocation, fingerprint or runnable profile binding.
type Adapter struct{}

var _ execution.Provider = (*Adapter)(nil)
var _ execution.BoundStartPreparer = (*Adapter)(nil)

type QualificationError struct {
	Report execution.QualificationReport
}

func (e *QualificationError) Error() string { return ErrSafety.Error() }
func (e *QualificationError) Unwrap() error { return ErrSafety }

func New(ctx context.Context, config Config) (*Adapter, error) {
	return NewReviewed(ctx, config, nil)
}

// NewReviewed consumes a host's semantic review without granting native startup
// authority. Even a valid signature leaves the independent safety gates pending.
func NewReviewed(ctx context.Context, config Config, reviews *ProtocolReviews) (*Adapter, error) {
	report, err := Inspect(ctx, config, reviews)
	if err != nil {
		return nil, err
	}
	return nil, &QualificationError{Report: report.Qualification}
}

func (*Adapter) ID() string { return AdapterID }
func (*Adapter) Capabilities() execution.AdapterCapabilities {
	return execution.AdapterCapabilities{ResumeMode: execution.ResumeUnsupported}
}
func (*Adapter) Describe() execution.ProviderDescriptor {
	return execution.ProviderDescriptor{
		ID: AdapterID, Name: "Hermes Agent (launch unavailable)", ProtocolVersion: protocolVersion,
		Environments: []string{domain.TrustedHostExecutionProvider}, TaskKinds: []string{"code", "document", "research"},
		Features:   []string{"runtime.progress_events", "runtime.terminal_result", "runtime.allowlisted_event_projection"},
		ResumeMode: execution.ResumeUnsupported, ShutdownMode: "exit_after_input", ApprovalMode: "unsupported",
		ModelMode: "native_default", CheckEvidenceMode: "none",
	}
}
func (*Adapter) Fingerprint(context.Context) (execution.RuntimeFingerprint, error) {
	return execution.RuntimeFingerprint{}, ErrSafety
}
func (*Adapter) PrepareStart(context.Context, execution.StartRequest) (execution.Invocation, error) {
	return execution.Invocation{}, ErrSafety
}
func (*Adapter) PrepareBoundStart(context.Context, execution.StartRequest) (execution.StartPreparation, error) {
	return execution.StartPreparation{}, ErrSafety
}
func (*Adapter) PrepareResume(context.Context, execution.ResumeRequest) (execution.Invocation, error) {
	return execution.Invocation{}, execution.ErrUnsupportedCapability
}
func (*Adapter) Recover(execution.RecoveryRequest) execution.RecoveryPolicy {
	// A missing gateway route or CLI death is not proof that its worker stopped.
	return execution.RecoveryPolicy{StopOnRestart: true, MissingRouteUncertain: true}
}
