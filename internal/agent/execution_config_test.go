package agent_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/Yangyang96/chora/internal/agent"
	"github.com/Yangyang96/chora/internal/agent/fake"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
)

func configurationFixture(t *testing.T) (*agent.Registry, []agent.ExecutionConfiguration) {
	t.Helper()
	registry := agent.NewRegistry()
	var configs []agent.ExecutionConfiguration
	for i, id := range []string{"runtime-a", "runtime-b"} {
		descriptor := execution.ProviderDescriptor{ID: id, Name: id, ProtocolVersion: "test.v1", Environments: []string{"trusted_host"}, TaskKinds: []string{"code"}, ResumeMode: execution.ResumeUnsupported, ShutdownMode: "exit_after_input", ApprovalMode: "native_noninteractive", ModelMode: "native_default", CheckEvidenceMode: "none"}
		p := testProvider{AgentAdapter: fake.NewAdapter(fake.Plan{AdapterID: id, ResumeMode: execution.ResumeUnsupported}), descriptor: descriptor}
		if err := registry.Register(p); err != nil {
			t.Fatal(err)
		}
		hash := fmt.Sprintf("%x", [32]byte{1})
		binding, err := registry.Bind(id, domain.RuntimeBindingV1{SchemaVersion: "chora.runtime-binding.v1", RuntimeID: id, ProtocolVersion: "test.v1", SourceIdentity: hash, ConfigurationFingerprint: hash, ExecutionProvider: "trusted_host", SecurityFingerprint: hash, CapabilityDigest: fmt.Sprintf("%x", descriptor.Digest()), ModelProvenance: "native_default"}, "code")
		if err != nil {
			t.Fatal(err)
		}
		configs = append(configs, agent.ExecutionConfiguration{ID: "default", Version: uint64(i + 1), Binding: binding})
	}
	return registry, configs
}
func TestReusableExecutionConfigurationFreezesEachTask(t *testing.T) {
	registry, configs := configurationFixture(t)
	selection := agent.ExecutionSelection{ProjectRevision: 1, ProjectDefault: agent.ConfigurationRef{ID: "default", Version: 1}}
	requirements := []agent.ExecutorRequirement{{Key: "author", Role: "author", AgentID: "same-agent", TaskKind: "code"}}
	first, err := registry.ResolveExecution(configs, selection, requirements)
	if err != nil {
		t.Fatal(err)
	}
	second, err := registry.ResolveExecution(configs, selection, requirements)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := first.Executor("author")
	b, _ := second.Executor("author")
	if a != b || a.Configuration.Binding.RuntimeID() != "runtime-a" {
		t.Fatal("repeated task required a new selection")
	}
	selection.ProjectRevision = 2
	selection.ProjectDefault.Version = 2
	third, err := registry.ResolveExecution(configs, selection, requirements)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := third.Executor("author")
	if c.Configuration.Binding.RuntimeID() != "runtime-b" || first.ProjectRevision() != 1 {
		t.Fatal("defaults did not affect only new tasks")
	}
	// Neither catalog changes nor a caller's returned slice can change a child binding.
	configs[0] = configs[1]
	copy := first.Executors()
	copy[0] = c
	child, _ := first.Executor("author")
	if child != a {
		t.Fatal("frozen child mapping mutated")
	}
	if _, err := first.Executor("unmapped-child"); !errors.Is(err, execution.ErrInvalidRuntimeConfig) {
		t.Fatal(err)
	}
}
func TestExecutionTemplateRoleAgentAndOverridePrecedence(t *testing.T) {
	registry, configs := configurationFixture(t)
	one := agent.ConfigurationRef{ID: "default", Version: 1}
	two := agent.ConfigurationRef{ID: "default", Version: 2}
	selection := agent.ExecutionSelection{ProjectRevision: 1, ProjectDefault: one, TemplateID: "workflow", TemplateVersion: 3, Roles: map[string]agent.ConfigurationRef{"review": two}, Agents: map[string]agent.ConfigurationRef{"specialist": one}, Overrides: map[string]agent.ConfigurationRef{"override": two}}
	required := []agent.ExecutorRequirement{{Key: "author", Role: "author", TaskKind: "code"}, {Key: "reviewer", Role: "review", TaskKind: "code"}, {Key: "agent", Role: "review", AgentID: "specialist", TaskKind: "code"}, {Key: "override", Role: "review", AgentID: "specialist", TaskKind: "code"}}
	snapshot, err := registry.ResolveExecution(configs, selection, required)
	if err != nil {
		t.Fatal(err)
	}
	for i, source := range []string{"project_default", "template_role", "template_agent", "task_override"} {
		entry := snapshot.Executors()[i]
		if entry.Source != source || entry.Configuration.Version != []uint64{1, 2, 1, 2}[i] {
			t.Fatalf("wrong precedence: %+v", entry)
		}
	}
	if id, version := snapshot.Template(); id != "workflow" || version != 3 {
		t.Fatal("template provenance lost")
	}
	selection.Roles["review"] = one
	reviewer, _ := snapshot.Executor("reviewer")
	if reviewer.Configuration.Version != 2 {
		t.Fatal("template changed existing task")
	}
}
func TestExecutionConfigurationPreflightFailsWithoutPartialSnapshot(t *testing.T) {
	for _, name := range []string{"missing_mapping", "invalid_selected_reference", "unknown_runtime", "capability", "duplicate_revision", "missing_template_revision", "unknown_override", "descriptor_drift"} {
		t.Run(name, func(t *testing.T) {
			registry, configs := configurationFixture(t)
			selection := agent.ExecutionSelection{ProjectRevision: 1, ProjectDefault: agent.ConfigurationRef{ID: "default", Version: 1}}
			required := []agent.ExecutorRequirement{{Key: "valid", TaskKind: "code"}, {Key: "invalid", TaskKind: "code"}}
			want := execution.ErrInvalidRuntimeConfig
			switch name {
			case "missing_mapping":
				selection.ProjectDefault = agent.ConfigurationRef{}
			case "invalid_selected_reference":
				selection.Overrides = map[string]agent.ConfigurationRef{"invalid": {ID: "missing", Version: 1}}
			case "unknown_runtime":
				registry = agent.NewRegistry()
				want = execution.ErrUnknownRuntime
			case "capability":
				required[1].Capabilities = []string{"unsupported"}
				want = execution.ErrUnsupportedCapability
			case "duplicate_revision":
				configs = append(configs, configs[0])
			case "missing_template_revision":
				selection.TemplateID = "template"
			case "unknown_override":
				selection.Overrides = map[string]agent.ConfigurationRef{"unknown": selection.ProjectDefault}
			case "descriptor_drift":
				binding, _ := domain.ParseRuntimeBinding(configs[0].Binding.RuntimeBindingJSON())
				binding.CapabilityDigest = fmt.Sprintf("%x", [32]byte{9})
				configs[0].Binding, _ = domain.NewRuntimeProfileBinding(binding)
			}
			snapshot, err := registry.ResolveExecution(configs, selection, required)
			if !errors.Is(err, want) || len(snapshot.Executors()) != 0 {
				t.Fatalf("partial or wrong rejection: %+v %v", snapshot, err)
			}
		})
	}
}

func TestReusableConfigurationPreservesLegacyPiBinding(t *testing.T) {
	registry := agent.NewRegistry()
	descriptor := execution.ProviderDescriptor{ID: "pi", Name: "Pi fixture", ProtocolVersion: "pi.rpc.v1", Environments: []string{"trusted_host"}, TaskKinds: []string{"code"}, ResumeMode: execution.ResumeUnsupported, ShutdownMode: "exit_after_input", ApprovalMode: "native_noninteractive", ModelMode: "native_default", CheckEvidenceMode: "none"}
	if err := registry.Register(testProvider{AgentAdapter: fake.NewAdapter(fake.Plan{AdapterID: "pi", ResumeMode: execution.ResumeUnsupported}), descriptor: descriptor}); err != nil {
		t.Fatal(err)
	}
	binding, err := domain.NewAgentExecutionProfileBinding(domain.AgentExecutionProfileTrustedLocal)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := registry.ResolveExecution([]agent.ExecutionConfiguration{{ID: "existing", Version: 1, Binding: binding}}, agent.ExecutionSelection{ProjectRevision: 1, ProjectDefault: agent.ConfigurationRef{ID: "existing", Version: 1}}, []agent.ExecutorRequirement{{Key: "author", TaskKind: "code"}})
	if err != nil {
		t.Fatal(err)
	}
	entry, _ := snapshot.Executor("author")
	if entry.Configuration.Binding.Record() != binding.Record() || entry.Configuration.Binding.RuntimeBindingJSON() != "" {
		t.Fatal("legacy binding representation rewritten")
	}
}
