package agent

import (
	"fmt"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
)

// ExecutionConfiguration is a reusable, immutable revision. Editing settings
// creates a new revision; it must never mutate a task's resolved snapshot.
// Runtime and environment are in Binding; model selection remains a separate
// contract and must be frozen by the task materializer as well.
type ExecutionConfiguration struct {
	ID      string
	Version uint64
	Binding domain.AgentExecutionProfileBinding
}

type ConfigurationRef struct {
	ID      string
	Version uint64
}

// ExecutionSelection is supplied before task materialization. Agent mappings
// belong to this workflow template, never to the permanent agent identity.
type ExecutionSelection struct {
	ProjectRevision uint64
	ProjectDefault  ConfigurationRef
	TemplateID      string
	TemplateVersion uint64
	Roles           map[string]ConfigurationRef
	Agents          map[string]ConfigurationRef
	Overrides       map[string]ConfigurationRef // optional, keyed by executor key
}

type ExecutorRequirement struct {
	Key          string
	Role         string
	AgentID      string
	TaskKind     string
	Capabilities []string
}

type ResolvedExecutor struct {
	Key           string
	Role          string
	AgentID       string
	Configuration ExecutionConfiguration
	Source        string
}

// ExecutionSnapshot owns its resolved values. Child execution must inherit a
// named entry through Executor, rather than re-read mutable project settings.
type ExecutionSnapshot struct {
	projectRevision uint64
	templateID      string
	templateVersion uint64
	executors       []ResolvedExecutor
}

func (s ExecutionSnapshot) ProjectRevision() uint64    { return s.projectRevision }
func (s ExecutionSnapshot) Template() (string, uint64) { return s.templateID, s.templateVersion }
func (s ExecutionSnapshot) Executors() []ResolvedExecutor {
	return append([]ResolvedExecutor(nil), s.executors...)
}
func (s ExecutionSnapshot) Executor(key string) (ResolvedExecutor, error) {
	for _, e := range s.executors {
		if e.Key == key {
			return e, nil
		}
	}
	return ResolvedExecutor{}, fmt.Errorf("%w: missing executor mapping %s", execution.ErrInvalidRuntimeConfig, key)
}

// ResolveExecution validates every executor before returning any snapshot.
// Precedence: optional task override > template agent > template role > project
// default. An explicitly selected invalid reference fails; it never falls back.
// This pure preflight creates no Attempt, process, or execution authority.
func (r *Registry) ResolveExecution(configs []ExecutionConfiguration, selection ExecutionSelection, requirements []ExecutorRequirement) (ExecutionSnapshot, error) {
	invalid := func(message string) (ExecutionSnapshot, error) {
		return ExecutionSnapshot{}, fmt.Errorf("%w: %s", execution.ErrInvalidRuntimeConfig, message)
	}
	if selection.ProjectRevision == 0 || len(requirements) == 0 {
		return invalid("missing project revision or executors")
	}
	if (selection.TemplateID == "") != (selection.TemplateVersion == 0) || (selection.TemplateID == "" && (len(selection.Roles) != 0 || len(selection.Agents) != 0)) {
		return invalid("invalid template identity")
	}
	catalog := make(map[ConfigurationRef]ExecutionConfiguration)
	for _, config := range configs {
		ref := ConfigurationRef{ID: config.ID, Version: config.Version}
		if ref.ID == "" || ref.Version == 0 {
			return invalid("invalid configuration identity")
		}
		if _, exists := catalog[ref]; exists {
			return invalid("duplicate configuration revision")
		}
		catalog[ref] = config
	}
	snapshot := ExecutionSnapshot{projectRevision: selection.ProjectRevision, templateID: selection.TemplateID, templateVersion: selection.TemplateVersion}
	seen := make(map[string]bool)
	for _, required := range requirements {
		if required.Key == "" || seen[required.Key] {
			return invalid("missing or duplicate executor key")
		}
		seen[required.Key] = true
		ref, source := selection.ProjectDefault, "project_default"
		if v, ok := selection.Roles[required.Role]; ok && required.Role != "" {
			ref, source = v, "template_role"
		}
		if v, ok := selection.Agents[required.AgentID]; ok && required.AgentID != "" {
			ref, source = v, "template_agent"
		}
		if v, ok := selection.Overrides[required.Key]; ok {
			ref, source = v, "task_override"
		}
		config, ok := catalog[ref]
		if !ok {
			return invalid("missing configuration revision for " + required.Key)
		}
		binding, err := domain.RestoreAgentExecutionProfileBinding(config.Binding.Record())
		if err != nil || !binding.Bound() {
			return invalid("invalid configuration binding")
		}
		descriptor, err := r.Describe(binding.RuntimeID())
		if err != nil {
			return ExecutionSnapshot{}, err
		}
		if err := descriptor.Require(binding.ExecutionProvider(), required.TaskKind, required.Capabilities...); err != nil {
			return ExecutionSnapshot{}, err
		}
		if !binding.Legacy() {
			frozen, err := domain.ParseRuntimeBinding(binding.RuntimeBindingJSON())
			if err != nil {
				return invalid("invalid runtime binding")
			}
			if _, err := r.Bind(binding.RuntimeID(), frozen, required.TaskKind, required.Capabilities...); err != nil {
				return ExecutionSnapshot{}, err
			}
		}
		snapshot.executors = append(snapshot.executors, ResolvedExecutor{Key: required.Key, Role: required.Role, AgentID: required.AgentID, Configuration: config, Source: source})
	}
	for key := range selection.Overrides {
		if !seen[key] {
			return invalid("override refers to unknown executor")
		}
	}
	return snapshot, nil
}
