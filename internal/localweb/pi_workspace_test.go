package localweb

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	agentpi "github.com/Yangyang96/chora/internal/agent/pi"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/speccoding"
)

func TestInstalledPiWorkspaceMappingPreservesLogicalSnapshotAndUsesPhysicalBaseline(t *testing.T) {
	delegate := newPiWorkspaceTestDelegate(t)
	logical := filepath.Join(t.TempDir(), "installed", "repository")
	physical := filepath.Join(t.TempDir(), "baseline-v4")
	adapter, err := newPiWorkspaceAdapter(delegate, logical, physical)
	if err != nil {
		t.Fatal(err)
	}
	if adapter.ID() != delegate.ID() || adapter.Capabilities() != delegate.Capabilities() {
		t.Fatal("workspace mapping changed delegate identity or capabilities")
	}
	fingerprinter, ok := adapter.(execution.BindingFingerprinter)
	if !ok {
		t.Fatal("workspace mapping hid profile-bound Runtime fingerprinting")
	}
	binding, err := domain.NewAgentExecutionProfileBinding(domain.AgentExecutionProfileStandard)
	if err != nil {
		t.Fatal(err)
	}
	wantFingerprint, err := delegate.FingerprintForBinding(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	gotFingerprint, err := fingerprinter.FingerprintForBinding(context.Background(), binding)
	if err != nil || gotFingerprint != wantFingerprint {
		t.Fatalf("delegated fingerprint = %#v, want %#v, err=%v", gotFingerprint, wantFingerprint, err)
	}
	boundPreparer, ok := adapter.(execution.BoundStartPreparer)
	if !ok {
		t.Fatal("workspace mapping hid atomic profile-bound preparation")
	}
	run, attempt, snapshot := piWorkspaceTestRun(t)
	contract := []byte(`{"schema_version":"chora.spec-coding-core.v8"}`)
	preparation, err := boundPreparer.PrepareBoundStart(context.Background(), execution.StartRequest{
		Run: run, Attempt: attempt, SnapshotDocument: snapshot, ExecutionContractDocument: contract, WorkspaceRoot: logical, LaunchToken: execution.LaunchToken{Value: "launch-fixed"},
	})
	if err != nil {
		t.Fatal(err)
	}
	invocation := preparation.Invocation
	if invocation.WorkingRoot() != physical {
		t.Fatalf("physical invocation root = %q", invocation.WorkingRoot())
	}
	if preparation.RuntimeFingerprint != wantFingerprint || invocation.Target().ProviderID() != domain.DockerExecutionProvider {
		t.Fatalf("bound workspace preparation = %#v target=%q", preparation, invocation.Target().ProviderID())
	}
	_, err = adapter.PrepareStart(context.Background(), execution.StartRequest{
		Run: run, Attempt: attempt, SnapshotDocument: snapshot, ExecutionContractDocument: contract, WorkspaceRoot: physical, LaunchToken: execution.LaunchToken{Value: "launch-drift"},
	})
	if err == nil {
		t.Fatal("unfrozen logical Workspace identity passed")
	}
}

func TestPiRetryWorkspaceAuthoritySeparatesProductLogicalFromDiagnosticPhysicalRoot(t *testing.T) {
	physical := t.TempDir()
	if err := validatePiRetryWorkspaceAuthority(true, speccoding.InstalledWorkspaceRoot); err != nil {
		t.Fatalf("product logical Workspace rejected after live Baseline verification: %v", err)
	}
	if err := validatePiRetryWorkspaceAuthority(true, physical); err == nil {
		t.Fatal("product Retry accepted a private host Workspace instead of the stable logical identity")
	}
	if err := validatePiRetryWorkspaceAuthority(false, physical); err != nil {
		t.Fatalf("diagnostic physical Workspace rejected: %v", err)
	}
	missing := filepath.Join(physical, "missing")
	if err := validatePiRetryWorkspaceAuthority(false, missing); err == nil {
		t.Fatal("diagnostic Retry accepted an unavailable physical Workspace")
	}
	file := filepath.Join(physical, "not-a-repository")
	if err := os.WriteFile(file, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validatePiRetryWorkspaceAuthority(false, file); err == nil {
		t.Fatal("diagnostic Retry accepted a non-directory Workspace")
	}
}

func TestNewPiWorkspaceAdapterRejectsInvalidMappingAuthority(t *testing.T) {
	delegate := newPiWorkspaceTestDelegate(t)
	logical := filepath.Join(t.TempDir(), "installed", "repository")
	physical := filepath.Join(t.TempDir(), "baseline-v4")
	nonCanonicalLogical := logical + string(filepath.Separator) + ".." + string(filepath.Separator) + filepath.Base(logical)
	nonCanonicalPhysical := physical + string(filepath.Separator) + "."
	tests := []struct {
		name     string
		delegate execution.AgentAdapter
		logical  string
		physical string
	}{
		{name: "nil delegate", logical: logical, physical: physical},
		{name: "relative logical", delegate: delegate, logical: "installed/repository", physical: physical},
		{name: "non-canonical logical", delegate: delegate, logical: nonCanonicalLogical, physical: physical},
		{name: "root logical", delegate: delegate, logical: string(filepath.Separator), physical: physical},
		{name: "relative physical", delegate: delegate, logical: logical, physical: "baseline-v4"},
		{name: "non-canonical physical", delegate: delegate, logical: logical, physical: nonCanonicalPhysical},
		{name: "root physical", delegate: delegate, logical: logical, physical: string(filepath.Separator)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := newPiWorkspaceAdapter(test.delegate, test.logical, test.physical); err == nil {
				t.Fatal("invalid Pi Workspace mapping passed")
			}
		})
	}
}

func TestInstalledPiWorkspaceMappingRejectsEveryNonExactLogicalRoot(t *testing.T) {
	base := t.TempDir()
	logical := filepath.Join(base, "installed", "repository")
	if err := os.MkdirAll(logical, 0o700); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(base, "repository-link")
	if err := os.Symlink(logical, symlink); err != nil {
		t.Fatal(err)
	}
	physical := filepath.Join(base, "reconstructed", "baseline-v4")
	delegate := &recordingWorkspaceAdapter{}
	adapter, err := newPiWorkspaceAdapter(delegate, logical, physical)
	if err != nil {
		t.Fatal(err)
	}

	requests := []struct {
		name string
		root string
	}{
		{name: "other", root: filepath.Join(base, "other")},
		{name: "ancestor", root: filepath.Dir(logical)},
		{name: "physical baseline", root: physical},
		{name: "symlink alias", root: symlink},
		{name: "non-canonical alias", root: logical + string(filepath.Separator) + "."},
		{name: "filesystem root", root: string(filepath.Separator)},
	}
	for _, test := range requests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := adapter.PrepareStart(context.Background(), execution.StartRequest{WorkspaceRoot: test.root}); err == nil {
				t.Fatal("non-exact logical Workspace authority passed")
			}
		})
	}
	if delegate.startCalls != 0 {
		t.Fatalf("rejected starts reached delegate %d times", delegate.startCalls)
	}
}

func TestInstalledPiWorkspaceMappingFailsResumeClosedBeforeDelegate(t *testing.T) {
	delegate := &recordingWorkspaceAdapter{}
	logical := filepath.Join(t.TempDir(), "installed", "repository")
	physical := filepath.Join(t.TempDir(), "baseline-v4")
	adapter, err := newPiWorkspaceAdapter(delegate, logical, physical)
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.PrepareResume(context.Background(), execution.ResumeRequest{
		Binding: execution.ResumeBinding{WorkingRoot: logical},
	})
	if err == nil {
		t.Fatal("Pi Workspace resume passed")
	}
	if delegate.resumeCalls != 0 {
		t.Fatal("Pi Workspace resume bypassed mapping authority through delegate")
	}
}

type recordingWorkspaceAdapter struct {
	execution.AgentAdapter
	startCalls  int
	resumeCalls int
}

func (*recordingWorkspaceAdapter) ID() string { return "recording-pi" }
func (*recordingWorkspaceAdapter) Capabilities() execution.AdapterCapabilities {
	return execution.AdapterCapabilities{ResumeMode: execution.ResumeUnsupported}
}
func (adapter *recordingWorkspaceAdapter) PrepareStart(_ context.Context, request execution.StartRequest) (execution.Invocation, error) {
	adapter.startCalls++
	return execution.NewInvocation(execution.InvocationParams{
		AdapterID: adapter.ID(), Executable: "recording-pi", WorkingRoot: request.WorkspaceRoot,
		LaunchToken: execution.LaunchToken{Value: "recording-launch"},
	})
}
func (adapter *recordingWorkspaceAdapter) PrepareResume(context.Context, execution.ResumeRequest) (execution.Invocation, error) {
	adapter.resumeCalls++
	return execution.Invocation{}, errors.New("recording delegate resume")
}

func newPiWorkspaceTestDelegate(t *testing.T) *agentpi.Adapter {
	t.Helper()
	runtimeConfig, err := os.ReadFile(filepath.Join("..", "..", "contracts", "g2-m1a", "pi-runtime-config.v6.json"))
	if err != nil {
		t.Fatal(err)
	}
	policy, err := os.ReadFile(filepath.Join("..", "..", "contracts", "g2-m1a", "sandbox-policy.v4.json"))
	if err != nil {
		t.Fatal(err)
	}
	delegate, err := agentpi.New(agentpi.Config{RuntimeConfig: runtimeConfig, Policy: policy, AttemptImageID: agentpi.AttemptImageID, BoundaryImageID: agentpi.BoundaryImageID})
	if err != nil {
		t.Fatal(err)
	}
	return delegate
}

func piWorkspaceTestRun(t *testing.T) (domain.AgentRun, domain.Attempt, []byte) {
	t.Helper()
	now := time.Now().UTC()
	run, err := domain.NewAgentRun(domain.NewRunID(), domain.NewTaskID(), domain.NewCharterID(), now)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := []byte(`{"frozen":true}`)
	digest := sha256.Sum256(snapshot)
	binding, err := domain.NewAgentExecutionProfileBinding(domain.AgentExecutionProfileStandard)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := domain.NewAttempt(domain.AttemptParams{
		ID: domain.NewAttemptID(), RunID: run.ID(), Sequence: 1, ContextSnapshotID: domain.NewContextSnapshotID(), ContextDigest: digest,
		AdapterID: agentpi.AdapterID, AgentExecutionProfileBinding: binding, CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return run, attempt, snapshot
}
