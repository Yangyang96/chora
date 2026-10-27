package localweb

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

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
