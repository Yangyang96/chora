package localweb

import (
	"context"
	"errors"
	"fmt"
	storecontract "github.com/Yangyang96/chora/internal/store"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/agent/fake"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
)

type registrationProvider struct{ execution.AgentAdapter }

func (p registrationProvider) Describe() execution.ProviderDescriptor {
	return execution.ProviderDescriptor{ID: p.ID(), Name: "Synthetic", ProtocolVersion: "v1", Environments: []string{"trusted_host"}, TaskKinds: []string{"code"}, ResumeMode: execution.ResumeUnsupported, ShutdownMode: "exit_after_input", ApprovalMode: "native_noninteractive", ModelMode: "native_default", CheckEvidenceMode: "none"}
}
func (p registrationProvider) Complete(r execution.CompletionRequest) (execution.TerminalResult, error) {
	return p.DecodeTerminal(r.Files)
}
func (p registrationProvider) Recover(execution.RecoveryRequest) execution.RecoveryPolicy {
	return execution.RecoveryPolicy{FreshRetry: true}
}
func TestRuntimeProviderRegistrationRequiresExactLaunchRoute(t *testing.T) {
	p := registrationProvider{fake.NewAdapter(fake.Plan{AdapterID: "synthetic", ResumeMode: execution.ResumeUnsupported})}
	registry := newAdapterRegistry()
	router := newRoutingSupervisor(nil)
	if err := registerRuntimeProviders(registry, router, []RuntimeProviderRegistration{{Provider: p, Supervisors: map[string]execution.ProcessSupervisor{"docker": newFakeSupervisor()}}}); err == nil {
		t.Fatal("unqualified environment accepted")
	}
	if err := registerRuntimeProviders(registry, router, []RuntimeProviderRegistration{{Provider: p, Supervisors: map[string]execution.ProcessSupervisor{"trusted_host": newFakeSupervisor()}}}); err != nil {
		t.Fatal(err)
	}
	if err := registerRuntimeProviders(registry, router, []RuntimeProviderRegistration{{Provider: p, Supervisors: map[string]execution.ProcessSupervisor{"trusted_host": newFakeSupervisor()}}}); err == nil {
		t.Fatal("duplicate runtime accepted")
	}
	hash := fmt.Sprintf("%x", [32]byte{1})
	binding, err := domain.NewRuntimeProfileBinding(domain.RuntimeBindingV1{SchemaVersion: "chora.runtime-binding.v1", RuntimeID: p.ID(), ProtocolVersion: "v1", SourceIdentity: hash, ConfigurationFingerprint: hash, ExecutionProvider: "trusted_host", SecurityFingerprint: hash, CapabilityDigest: fmt.Sprintf("%x", p.Describe().Digest()), ModelProvenance: "native_default"})
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{registry: registry, supervisor: router}
	response := httptest.NewRecorder()
	if !server.requireAgentExecutionRoute(response, newLocalRequest(http.MethodPost, "/runtime", nil), binding) {
		t.Fatalf("registered route rejected: %s", response.Body.String())
	}
	unknown, _ := execution.NewExecutionTarget("unknown", "trusted_host")
	invocation, _ := execution.NewInvocation(execution.InvocationParams{AdapterID: "unknown", Target: unknown, Executable: "/synthetic", WorkingRoot: t.TempDir(), LaunchToken: execution.LaunchToken{Value: "unknown-launch"}})
	if got := router.Start(context.Background(), invocation, nil); got.Kind != execution.StartProvenNoChild {
		t.Fatal("unknown route fell back")
	}
}

type reviewStartupReader struct {
	storecontract.Reader
	candidate storecontract.StartupCandidate
	session   storecontract.RuntimeSession
}

func (r reviewStartupReader) ListStartupCandidates(context.Context) ([]storecontract.StartupCandidate, error) {
	return []storecontract.StartupCandidate{r.candidate}, nil
}
func (r reviewStartupReader) GetRuntimeSessionForAttempt(context.Context, domain.AttemptID) (storecontract.RuntimeSession, error) {
	return r.session, nil
}

type reviewMissingOwnershipSupervisor struct {
	execution.ProcessSupervisor
	outcome *execution.ReconcileOutcome
	err     error
}

func (s reviewMissingOwnershipSupervisor) Reconcile(context.Context, execution.ProcessIdentity) (execution.ReconcileOutcome, error) {
	if s.err != nil {
		return execution.ReconcileOutcome{}, s.err
	}
	if s.outcome != nil {
		return *s.outcome, nil
	}
	return execution.ReconcileOutcome{Kind: execution.ReconcileUncertain, Diagnostic: "process identity is not owned by this trusted-host state"}, nil
}
func TestMissingProviderOwnershipMustPreserveRecoverySurface(t *testing.T) {
	p := registrationProvider{fake.NewAdapter(fake.Plan{AdapterID: "synthetic", ResumeMode: execution.ResumeUnsupported})}
	registry := newAdapterRegistry()
	router := newRoutingSupervisor(nil)
	missing := &reviewMissingOwnershipSupervisor{}
	if err := registerRuntimeProviders(registry, router, []RuntimeProviderRegistration{{Provider: p, Supervisors: map[string]execution.ProcessSupervisor{"trusted_host": missing}}}); err != nil {
		t.Fatal(err)
	}
	hash := fmt.Sprintf("%x", [32]byte{1})
	binding, err := domain.NewRuntimeProfileBinding(domain.RuntimeBindingV1{SchemaVersion: "chora.runtime-binding.v1", RuntimeID: p.ID(), ProtocolVersion: "v1", SourceIdentity: hash, ConfigurationFingerprint: hash, ExecutionProvider: "trusted_host", SecurityFingerprint: hash, CapabilityDigest: fmt.Sprintf("%x", p.Describe().Digest()), ModelProvenance: "native_default"})
	if err != nil {
		t.Fatal(err)
	}
	run, legacy, snapshot := piWorkspaceTestRun(t)
	attempt, err := domain.NewAttempt(domain.AttemptParams{ID: domain.NewAttemptID(), RunID: run.ID(), Sequence: 1, ContextSnapshotID: legacy.ContextSnapshotID(), ContextDigest: legacy.ContextDigest(), AdapterID: p.ID(), AgentExecutionProfileBinding: binding, CreatedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	_ = snapshot
	run, err = run.Transition(domain.CommandPrepareRun, run.CreatedAt())
	if err != nil {
		t.Fatal(err)
	}
	run, err = run.Transition(domain.CommandStartAttempt, run.CreatedAt())
	if err != nil {
		t.Fatal(err)
	}
	attempt, err = attempt.Transition(domain.AttemptEventStartAttempt)
	if err != nil {
		t.Fatal(err)
	}
	reader := reviewStartupReader{candidate: storecontract.StartupCandidate{Run: run, Attempt: &attempt}, session: storecontract.RuntimeSession{ID: domain.NewRuntimeSessionID(), AttemptID: attempt.ID(), AdapterID: p.ID(), ProcessIdentity: "lost-process", LaunchToken: "persisted-launch"}}
	if err = restoreProviderRoutes(context.Background(), reader, registry, router); err != nil {
		t.Fatalf("missing ownership aborts startup before app.RecoverStartup can retain recovery_required: %v", err)
	}
	if len(router.routes) != 0 {
		t.Fatal("unproved ownership recorded a process route")
	}
	missing.err = errors.New("state storage corrupted")
	if err := restoreProviderRoutes(context.Background(), reader, registry, router); !errors.Is(err, missing.err) {
		t.Fatalf("storage error masked: %v", err)
	}
	missing.err = nil
	missing.outcome = &execution.ReconcileOutcome{Kind: execution.ReconcileUncertain, Handle: execution.RuntimeHandle{Value: "unexpected"}, LaunchToken: execution.LaunchToken{Value: "wrong-launch"}}
	if err := restoreProviderRoutes(context.Background(), reader, registry, router); err == nil {
		t.Fatal("malformed ownership masked")
	}
	if len(router.routes) != 0 {
		t.Fatal("malformed ownership recorded a process route")
	}
}
func TestInstalledPiWrapperPreservesProviderPolicy(t *testing.T) {
	delegate := newPiWorkspaceTestDelegate(t)
	if _, ok := any(delegate).(execution.Provider); !ok {
		t.Fatal("Pi delegate is not provider")
	}
	wrapped, err := newPiWorkspaceAdapter(delegate, "/logical/repository", "/physical/baseline")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := wrapped.(execution.Provider); !ok {
		t.Error("installed product wrapper hides Provider: app skips descriptor admission and Complete/Recover")
	}
	if _, ok := wrapped.(execution.AgentCheckReporter); !ok {
		t.Error("installed product wrapper hides AgentCheckReporter")
	}
}
