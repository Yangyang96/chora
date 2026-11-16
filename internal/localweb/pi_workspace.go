package localweb

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/speccoding"
)

var errInvalidPiWorkspaceMapping = errors.New("invalid installed Pi Workspace mapping")
var errPiWorkspaceResumeUnsupported = errors.New("installed Pi Workspace resume unsupported")

// validatePiRetryWorkspaceAuthority distinguishes the installed product's
// stable logical repository identity from a diagnostic server's host path.
// Product callers have already passed the live physical Baseline check before
// reaching this predicate; piWorkspaceAdapter rebinds this exact logical root
// to that private Baseline during atomic Invocation preparation.
func validatePiRetryWorkspaceAuthority(product bool, workspaceRoot string) error {
	if product {
		if workspaceRoot != speccoding.InstalledWorkspaceRoot {
			return errors.New("installed Pi retry Workspace identity mismatch")
		}
		return nil
	}
	info, err := os.Stat(workspaceRoot)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("Pi retry source repository is not a directory")
	}
	return nil
}

type piWorkspaceAdapter struct {
	delegate execution.AgentAdapter
	logical  string
	physical string
}

// newPiWorkspaceAdapter maps one server-identified installed repository to one
// freshly reconstructed frozen Baseline. The mapping is deliberately lexical:
// it neither discovers host paths nor follows symlinks to widen authority.
func newPiWorkspaceAdapter(delegate execution.AgentAdapter, logical, physical string) (execution.AgentAdapter, error) {
	if delegate == nil || !validPiWorkspaceRoot(logical) || !validPiWorkspaceRoot(physical) {
		return nil, errInvalidPiWorkspaceMapping
	}
	return &piWorkspaceAdapter{delegate: delegate, logical: logical, physical: physical}, nil
}

func validPiWorkspaceRoot(root string) bool {
	return filepath.IsAbs(root) && filepath.Clean(root) == root && root != string(filepath.Separator)
}

func (adapter *piWorkspaceAdapter) ID() string { return adapter.delegate.ID() }
func (adapter *piWorkspaceAdapter) Capabilities() execution.AdapterCapabilities {
	return adapter.delegate.Capabilities()
}
func (adapter *piWorkspaceAdapter) PrepareStart(ctx context.Context, request execution.StartRequest) (execution.Invocation, error) {
	if request.WorkspaceRoot != adapter.logical {
		return execution.Invocation{}, errors.New("Pi Workspace does not match the frozen logical Baseline identity")
	}
	request.WorkspaceRoot = adapter.physical
	return adapter.delegate.PrepareStart(ctx, request)
}
func (adapter *piWorkspaceAdapter) PrepareBoundStart(ctx context.Context, request execution.StartRequest) (execution.StartPreparation, error) {
	if request.WorkspaceRoot != adapter.logical {
		return execution.StartPreparation{}, errors.New("Pi Workspace does not match the frozen logical Baseline identity")
	}
	preparer, ok := adapter.delegate.(execution.BoundStartPreparer)
	if !ok {
		return execution.StartPreparation{}, errors.New("Pi delegate does not support atomic start preparation")
	}
	request.WorkspaceRoot = adapter.physical
	return preparer.PrepareBoundStart(ctx, request)
}
func (*piWorkspaceAdapter) PrepareResume(context.Context, execution.ResumeRequest) (execution.Invocation, error) {
	return execution.Invocation{}, errPiWorkspaceResumeUnsupported
}
func (adapter *piWorkspaceAdapter) DecodeEvent(chunk execution.EventChunk) (execution.DecodedChunk, error) {
	return adapter.delegate.DecodeEvent(chunk)
}
func (adapter *piWorkspaceAdapter) DecodeTerminal(files execution.TerminalFiles) (execution.TerminalResult, error) {
	return adapter.delegate.DecodeTerminal(files)
}
func (adapter *piWorkspaceAdapter) Fingerprint(ctx context.Context) (execution.RuntimeFingerprint, error) {
	return adapter.delegate.Fingerprint(ctx)
}
func (adapter *piWorkspaceAdapter) FingerprintForBinding(ctx context.Context, binding domain.AgentExecutionProfileBinding) (execution.RuntimeFingerprint, error) {
	fingerprinter, ok := adapter.delegate.(execution.BindingFingerprinter)
	if !ok {
		return execution.RuntimeFingerprint{}, errors.New("Pi delegate does not support profile-bound fingerprinting")
	}
	return fingerprinter.FingerprintForBinding(ctx, binding)
}

var _ execution.AgentAdapter = (*piWorkspaceAdapter)(nil)
var _ execution.BindingFingerprinter = (*piWorkspaceAdapter)(nil)
var _ execution.BoundStartPreparer = (*piWorkspaceAdapter)(nil)
