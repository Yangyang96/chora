package agent_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Yangyang96/chora/internal/agent"
	"github.com/Yangyang96/chora/internal/agent/fake"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
)

type testProvider struct {
	execution.AgentAdapter
	descriptor execution.ProviderDescriptor
}

func (p testProvider) Describe() execution.ProviderDescriptor { return p.descriptor.Clone() }
func (p testProvider) Complete(r execution.CompletionRequest) (execution.TerminalResult, error) {
	return p.DecodeTerminal(r.Files)
}
func (p testProvider) Recover(execution.RecoveryRequest) execution.RecoveryPolicy {
	return execution.RecoveryPolicy{FreshRetry: true}
}
func TestRegistryRejectsUnknownDuplicateAndCapabilityMismatch(t *testing.T) {
	p := testProvider{AgentAdapter: fake.NewAdapter(fake.Plan{AdapterID: "synthetic", ResumeMode: execution.ResumeUnsupported}), descriptor: execution.ProviderDescriptor{ID: "synthetic", Name: "Synthetic", ProtocolVersion: "test.v1", Environments: []string{domain.TrustedHostExecutionProvider}, TaskKinds: []string{"code"}, ResumeMode: execution.ResumeUnsupported, ShutdownMode: "exit_after_input", ApprovalMode: "native_noninteractive", ModelMode: "native_default", CheckEvidenceMode: "none"}}
	r := agent.NewRegistry()
	if _, err := r.Get("unknown"); !errors.Is(err, execution.ErrUnknownRuntime) {
		t.Fatalf("unknown runtime: %v", err)
	}
	if err := r.Register(p); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(p); !errors.Is(err, execution.ErrInvalidRuntimeConfig) {
		t.Fatalf("duplicate: %v", err)
	}
	descriptor, _ := r.Describe(p.ID())
	descriptor.Environments[0] = "docker"
	descriptor, _ = r.Describe(p.ID())
	if descriptor.Environments[0] != "trusted_host" {
		t.Fatal("descriptor was mutable")
	}
	hash := fmt.Sprintf("%x", [32]byte{1})
	binding := domain.RuntimeBindingV1{SchemaVersion: "chora.runtime-binding.v1", RuntimeID: p.ID(), ProtocolVersion: descriptor.ProtocolVersion, SourceIdentity: hash, ConfigurationFingerprint: hash, ExecutionProvider: domain.TrustedHostExecutionProvider, SecurityFingerprint: hash, CapabilityDigest: fmt.Sprintf("%x", descriptor.Digest()), ModelProvenance: "native_default"}
	if _, err := r.Bind(p.ID(), binding, "code"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Bind(p.ID(), binding, "code", "native_resources"); !errors.Is(err, execution.ErrUnsupportedCapability) {
		t.Fatalf("unsupported feature: %v", err)
	}
	binding.ExecutionProvider = "docker"
	if _, err := r.Bind(p.ID(), binding, "code"); !errors.Is(err, execution.ErrUnsupportedCapability) {
		t.Fatalf("unsupported environment: %v", err)
	}
	binding.ExecutionProvider = "trusted_host"
	binding.ConfigurationFingerprint = "invalid"
	if _, err := r.Bind(p.ID(), binding, "code"); err == nil {
		t.Fatal("invalid config accepted")
	}
	if got := execution.Inspect(context.Background(), p); got.RuntimeID != p.ID() {
		t.Fatal("inspection changed runtime")
	}
}
