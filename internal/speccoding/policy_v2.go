package speccoding

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	PiRuntimeConfigSchemaVersionV2 = "chora.pi-runtime-config.v2"
	SandboxPolicySchemaVersionV2   = "chora.m1-local-sandbox-policy.v2"
)

var ErrInvalidCandidatePolicy = errors.New("invalid candidate policy")

type PiRuntimeConfigV2 struct {
	SchemaVersion  string            `json:"schema_version"`
	Runtime        PiRuntimeV2       `json:"runtime"`
	Environment    map[string]string `json:"environment"`
	AgentDirectory struct {
		Lifecycle             string   `json:"lifecycle"`
		AllowedGeneratedFiles []string `json:"allowed_generated_files"`
		InheritHostState      bool     `json:"inherit_host_state"`
		RetainAfterAttempt    bool     `json:"retain_after_attempt"`
	} `json:"agent_directory"`
	Resources       map[string]bool     `json:"resources"`
	ModelConfig     PiModelConfigV2     `json:"model_config"`
	Authentication  PiAuthenticationV2  `json:"authentication"`
	Context         PiContextBoundaryV2 `json:"context"`
	EventProjection PiEventProjectionV2 `json:"event_projection"`
	ResultAuthority PiResultAuthorityV2 `json:"result_authority"`
	ModelDependency PiModelDependencyV2 `json:"model_dependency"`
}

type PiRuntimeV2 struct {
	Package      string   `json:"package"`
	Version      string   `json:"version"`
	NPMIntegrity string   `json:"npm_integrity"`
	Transport    []string `json:"transport"`
}

type PiModelConfigV2 struct {
	Providers map[string]PiProviderV2 `json:"providers"`
}

type PiProviderV2 struct {
	BaseURL string `json:"baseUrl"`
	API     string `json:"api"`
	APIKey  string `json:"apiKey"`
	Compat  struct {
		SupportsDeveloperRole   bool `json:"supportsDeveloperRole"`
		SupportsReasoningEffort bool `json:"supportsReasoningEffort"`
	} `json:"compat"`
	Models []PiModelV2 `json:"models"`
}

type PiModelV2 struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Reasoning     bool   `json:"reasoning"`
	ContextWindow int    `json:"contextWindow"`
	MaxTokens     int    `json:"maxTokens"`
}

type PiAuthenticationV2 struct {
	Type                             string   `json:"type"`
	CredentialEnvironmentNames       []string `json:"credential_environment_names"`
	DummyProviderKeyIsSecret         bool     `json:"dummy_provider_key_is_secret"`
	ContainerReceivesCloudCredential bool     `json:"container_receives_cloud_credential"`
	HostDaemonOwnsCloudSession       bool     `json:"host_daemon_owns_cloud_session"`
}

type PiContextBoundaryV2 struct {
	ExplicitSource          string `json:"explicit_source"`
	WorkingDirectory        string `json:"working_directory"`
	RepositoryChild         string `json:"repository_child"`
	InheritHostPiState      bool   `json:"inherit_host_pi_state"`
	LoadProjectInstructions bool   `json:"load_project_instructions"`
	LoadPackages            bool   `json:"load_packages"`
	LoadExtensions          bool   `json:"load_extensions"`
	LoadSkills              bool   `json:"load_skills"`
	LoadPrompts             bool   `json:"load_prompts"`
	LoadContextFiles        bool   `json:"load_context_files"`
}

type PiEventProjectionV2 struct {
	AllowedEvents           []string `json:"allowed_events"`
	AllowedFields           []string `json:"allowed_fields"`
	PersistThinking         bool     `json:"persist_thinking"`
	PersistRawPrompt        bool     `json:"persist_raw_prompt"`
	PersistCredentialValues bool     `json:"persist_credential_values"`
	CanonicalEventOwner     string   `json:"canonical_event_owner"`
}

type PiResultAuthorityV2 struct {
	Owner                         string `json:"owner"`
	TerminalResultCount           int    `json:"terminal_result_count"`
	PiAgentEndMeansSuccess        bool   `json:"pi_agent_end_means_success"`
	PiFinalTextRole               string `json:"pi_final_text_role"`
	RequiresBaselineDiff          bool   `json:"requires_baseline_diff"`
	RequiresCommandBoundChecks    bool   `json:"requires_command_bound_checks"`
	RequiresSandboxReconciliation bool   `json:"requires_sandbox_reconciliation"`
	MissingEvidencePolicy         string `json:"missing_evidence_policy"`
}

type PiModelDependencyV2 struct {
	Provider                 string `json:"provider"`
	ModelID                  string `json:"model_id"`
	ModelDigest              string `json:"model_digest"`
	Scope                    string `json:"scope"`
	ProductInterfaceExposure string `json:"product_interface_exposure"`
	ReplacementAuthority     string `json:"replacement_authority"`
	SemanticFailureLimit     int    `json:"semantic_failure_limit"`
}

type SandboxPolicyV2 struct {
	SchemaVersion string `json:"schema_version"`
	Provider      struct {
		Name                string `json:"name"`
		ColimaVersion       string `json:"colima_version"`
		ColimaSHA256        string `json:"colima_sha256"`
		Engine              string `json:"engine"`
		EngineClientVersion string `json:"engine_client_version"`
		EngineClientSHA256  string `json:"engine_client_sha256"`
	} `json:"provider"`
	Attempt     SandboxAttemptV2   `json:"attempt"`
	Isolation   SandboxIsolationV2 `json:"isolation"`
	Network     SandboxNetworkV2   `json:"network"`
	Resources   SandboxResourcesV2 `json:"resources"`
	Credentials struct {
		Injection     string   `json:"injection"`
		DeclaredNames []string `json:"declared_names"`
		Persist       bool     `json:"persist"`
		LogValues     bool     `json:"log_values"`
	} `json:"credentials"`
	Termination SandboxTerminationV2  `json:"termination"`
	Limits      SandboxOutputLimitsV2 `json:"limits"`
	Identity    struct {
		RequiredLabels       []string `json:"required_labels"`
		PolicyDigestRequired bool     `json:"policy_digest_required"`
	} `json:"identity"`
	Cleanup struct {
		RemoveContainer                        bool `json:"remove_container"`
		RemoveInternalNetwork                  bool `json:"remove_internal_network"`
		RemoveUpstreamBoundary                 bool `json:"remove_upstream_boundary"`
		RemoveWorkspace                        bool `json:"remove_workspace"`
		AllTerminalPaths                       bool `json:"all_terminal_paths"`
		RecoveryEnumeratesLabeledResourcesOnly bool `json:"recovery_enumerates_labeled_resources_only"`
	} `json:"cleanup"`
}

type SandboxAttemptV2 struct {
	ContainerLifecycle                string   `json:"container_lifecycle"`
	WorkspaceLifecycle                string   `json:"workspace_lifecycle"`
	WorkspaceMount                    string   `json:"workspace_mount"`
	RepositoryPath                    string   `json:"repository_path"`
	ContextMount                      string   `json:"context_mount"`
	ContextReadOnly                   bool     `json:"context_read_only"`
	OriginalRepositoryWritableMounted bool     `json:"original_repository_writable_mounted"`
	SourceMaterialization             string   `json:"source_materialization"`
	ReusePreviousAttempt              bool     `json:"reuse_previous_attempt"`
	OtherHostMounts                   []string `json:"other_host_mounts"`
	PiAgentDirectory                  string   `json:"pi_agent_directory"`
	WorkingDirectory                  string   `json:"working_directory"`
	User                              string   `json:"user"`
}

type SandboxIsolationV2 struct {
	ReadOnlyRoot              bool     `json:"read_only_root"`
	Tmpfs                     []string `json:"tmpfs"`
	DropAllCapabilities       bool     `json:"drop_all_capabilities"`
	NoNewPrivileges           bool     `json:"no_new_privileges"`
	SeccompProfile            string   `json:"seccomp_profile"`
	Privileged                bool     `json:"privileged"`
	DeviceMounts              []string `json:"device_mounts"`
	HostNetwork               bool     `json:"host_network"`
	PublishPorts              bool     `json:"publish_ports"`
	DockerSocketMounted       bool     `json:"docker_socket_mounted"`
	NormalBridgeFallback      bool     `json:"normal_bridge_fallback"`
	UnrelatedHostPathsMounted bool     `json:"unrelated_host_paths_mounted"`
	FailClosed                bool     `json:"fail_closed"`
}

type SandboxNetworkV2 struct {
	Mode                   string                    `json:"mode"`
	AttemptNetworkInternal bool                      `json:"attempt_network_internal"`
	GeneralEgress          bool                      `json:"general_egress"`
	HostNetwork            bool                      `json:"host_network"`
	PublishedPorts         bool                      `json:"published_ports"`
	DockerSocketMounted    bool                      `json:"docker_socket_mounted"`
	NormalBridgeFallback   bool                      `json:"normal_bridge_fallback"`
	Upstream               SandboxUpstreamBoundaryV2 `json:"upstream"`
}

type SandboxUpstreamBoundaryV2 struct {
	Kind        string `json:"kind"`
	Endpoint    string `json:"endpoint"`
	AllowedHost string `json:"allowed_host"`
	AllowedPort int    `json:"allowed_port"`
	TaskScoped  bool   `json:"task_scoped"`
	Traceable   bool   `json:"traceable"`
}

type SandboxResourcesV2 struct {
	CPUs                  int `json:"cpus"`
	MemoryMiB             int `json:"memory_mib"`
	MemorySwapMiB         int `json:"memory_swap_mib"`
	PIDsLimit             int `json:"pids_limit"`
	FileDescriptors       int `json:"file_descriptors"`
	CommandTimeoutSeconds int `json:"command_timeout_seconds"`
	AttemptTimeoutSeconds int `json:"attempt_timeout_seconds"`
}

type SandboxTerminationV2 struct {
	RPCAbortIsCooperativeOnly   bool   `json:"rpc_abort_is_cooperative_only"`
	CooperativeAbortWaitSeconds int    `json:"cooperative_abort_wait_seconds"`
	DeathConfirmationSeconds    int    `json:"death_confirmation_seconds"`
	ForceContainerRemoval       bool   `json:"force_container_removal"`
	RequireProcessTreeDeath     bool   `json:"require_process_tree_death"`
	RevokeNetworkBeforeKill     bool   `json:"revoke_network_before_kill"`
	Uncertainty                 string `json:"uncertainty"`
}

type SandboxOutputLimitsV2 struct {
	PersistedLogBytes int `json:"persisted_log_bytes"`
	ArtifactBytes     int `json:"artifact_bytes"`
}

func DecodePiRuntimeConfigV2(data []byte) (PiRuntimeConfigV2, error) {
	var config PiRuntimeConfigV2
	if err := decodeCandidatePolicy(data, &config); err != nil {
		return PiRuntimeConfigV2{}, err
	}
	if err := validatePiRuntimeConfigV2(config); err != nil {
		return PiRuntimeConfigV2{}, err
	}
	return config, nil
}

func DecodeSandboxPolicyV2(data []byte) (SandboxPolicyV2, error) {
	var policy SandboxPolicyV2
	if err := decodeCandidatePolicy(data, &policy); err != nil {
		return SandboxPolicyV2{}, err
	}
	if err := validateSandboxPolicyV2(policy); err != nil {
		return SandboxPolicyV2{}, err
	}
	return policy, nil
}

func decodeCandidatePolicy(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("%w: decode: %v", ErrInvalidCandidatePolicy, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing data", ErrInvalidCandidatePolicy)
	}
	return nil
}

func validatePiRuntimeConfigV2(config PiRuntimeConfigV2) error {
	if config.SchemaVersion != PiRuntimeConfigSchemaVersionV2 || config.Runtime.Package != "@earendil-works/pi-coding-agent" || config.Runtime.Version != "0.84.1" || config.Runtime.NPMIntegrity != "sha512-ncAqFrG+iybuPGOhMiZoEHkEzTpJgz3guYD32pD+M7ucc0WeHmauP6wa7qwP8V/KWvsZDVNa5XGsdZ7fkC7w7A==" || !slicesEqual(config.Runtime.Transport, append([]string{"pi"}, requiredPiArguments...)) {
		return invalidCandidatePolicy("runtime identity or transport")
	}
	wantEnvironment := map[string]string{"PI_CODING_AGENT_DIR": "/run/chora/pi", "PI_OFFLINE": "1", "PI_SKIP_VERSION_CHECK": "1", "PI_TELEMETRY": "0"}
	if !equalStringMap(config.Environment, wantEnvironment) || config.AgentDirectory.Lifecycle != "new_empty_directory_per_attempt" || !slicesEqual(config.AgentDirectory.AllowedGeneratedFiles, []string{"models.json"}) || config.AgentDirectory.InheritHostState || config.AgentDirectory.RetainAfterAttempt {
		return invalidCandidatePolicy("agent state boundary")
	}
	wantResources := []string{"third_party_packages", "extensions", "skills", "prompt_templates", "themes", "project_context_files", "undeclared_project_resources", "host_pi_state"}
	if !allFalseKeys(config.Resources, wantResources) {
		return invalidCandidatePolicy("implicit resources")
	}
	provider, ok := config.ModelConfig.Providers["ollama"]
	if !ok || len(config.ModelConfig.Providers) != 1 || provider.BaseURL != "http://ollama-boundary:11434/v1" || provider.API != "openai-completions" || provider.APIKey != "ollama" || provider.Compat.SupportsDeveloperRole || provider.Compat.SupportsReasoningEffort || len(provider.Models) != 1 || provider.Models[0].ID != "minimax-m3:cloud" || provider.Models[0].ContextWindow != 524288 || provider.Models[0].MaxTokens != 8192 {
		return invalidCandidatePolicy("model configuration")
	}
	if config.Authentication.Type != "host_ollama_cloud_session" || len(config.Authentication.CredentialEnvironmentNames) != 0 || config.Authentication.DummyProviderKeyIsSecret || config.Authentication.ContainerReceivesCloudCredential || !config.Authentication.HostDaemonOwnsCloudSession {
		return invalidCandidatePolicy("model authentication")
	}
	context := config.Context
	if context.ExplicitSource != "digest_bound_context_snapshot_only" || context.WorkingDirectory != "/workspace" || context.RepositoryChild != "/workspace/repository" || context.InheritHostPiState || context.LoadProjectInstructions || context.LoadPackages || context.LoadExtensions || context.LoadSkills || context.LoadPrompts || context.LoadContextFiles {
		return invalidCandidatePolicy("implicit context")
	}
	wantEvents := []string{"agent_start", "agent_end", "tool_start", "tool_end", "file_path", "command_summary", "exit_code", "error", "model_identity", "usage"}
	wantFields := []string{"event_type", "timestamp", "tool_name", "workspace_relative_path", "redacted_command_summary", "exit_code", "error_class", "model_id", "model_digest", "usage"}
	projection := config.EventProjection
	if !slicesEqual(projection.AllowedEvents, wantEvents) || !slicesEqual(projection.AllowedFields, wantFields) || projection.PersistThinking || projection.PersistRawPrompt || projection.PersistCredentialValues || projection.CanonicalEventOwner != "chora" {
		return invalidCandidatePolicy("event projection")
	}
	result := config.ResultAuthority
	if result.Owner != "chora" || result.TerminalResultCount != 1 || result.PiAgentEndMeansSuccess || result.PiFinalTextRole != "agent_summary_only" || !result.RequiresBaselineDiff || !result.RequiresCommandBoundChecks || !result.RequiresSandboxReconciliation || result.MissingEvidencePolicy != "explicit_unknown_or_failure" {
		return invalidCandidatePolicy("result authority")
	}
	model := config.ModelDependency
	if model.Provider != "ollama" || model.ModelID != "minimax-m3:cloud" || model.ModelDigest != "sha256:8cd948b96f47afd232cef7d49faf65791d22bd9dbbef74add3b9355d9d75f765" || model.Scope != "replaceable_task_dependency" || model.ProductInterfaceExposure != "none" || model.ReplacementAuthority != "explicit_user_new_capsule" || model.SemanticFailureLimit != 2 {
		return invalidCandidatePolicy("task model dependency")
	}
	return nil
}

func validateSandboxPolicyV2(policy SandboxPolicyV2) error {
	provider := policy.Provider
	if policy.SchemaVersion != SandboxPolicySchemaVersionV2 || provider.Name != "colima-docker" || provider.ColimaVersion != "0.10.3" || provider.ColimaSHA256 != v2ColimaSHA256 || provider.Engine != "docker" || provider.EngineClientVersion != "29.6.1" || provider.EngineClientSHA256 != v2DockerClientSHA256 {
		return invalidCandidatePolicy("provider identity")
	}
	attempt := policy.Attempt
	if attempt.ContainerLifecycle != "one_disposable_container_per_attempt" || attempt.WorkspaceLifecycle != "fresh_copy_per_attempt_removed_on_terminal" || attempt.WorkspaceMount != "/workspace" || attempt.RepositoryPath != "/workspace/repository" || attempt.ContextMount != "/input/context" || !attempt.ContextReadOnly || attempt.OriginalRepositoryWritableMounted || attempt.SourceMaterialization != "fresh_copy_from_frozen_revision_plus_selected_dirty_input" || attempt.ReusePreviousAttempt || len(attempt.OtherHostMounts) != 0 || attempt.PiAgentDirectory != "/run/chora/pi" || attempt.WorkingDirectory != "/workspace" || attempt.User != "1000:1000" {
		return invalidCandidatePolicy("attempt workspace")
	}
	isolation := policy.Isolation
	if !isolation.ReadOnlyRoot || !slicesEqual(isolation.Tmpfs, []string{"/tmp", "/run/chora/pi"}) || !isolation.DropAllCapabilities || !isolation.NoNewPrivileges || isolation.SeccompProfile != "docker_default" || isolation.Privileged || len(isolation.DeviceMounts) != 0 || isolation.HostNetwork || isolation.PublishPorts || isolation.DockerSocketMounted || isolation.NormalBridgeFallback || isolation.UnrelatedHostPathsMounted || !isolation.FailClosed {
		return invalidCandidatePolicy("container isolation")
	}
	network := policy.Network
	if network.Mode != "attempt_private_internal_with_fixed_ollama_upstream" || !network.AttemptNetworkInternal || network.GeneralEgress || network.HostNetwork || network.PublishedPorts || network.DockerSocketMounted || network.NormalBridgeFallback || network.Upstream.Kind != "task_scoped_fixed_upstream_boundary" || network.Upstream.Endpoint != "http://ollama-boundary:11434/v1" || network.Upstream.AllowedHost != "host.docker.internal" || network.Upstream.AllowedPort != 11434 || !network.Upstream.TaskScoped || !network.Upstream.Traceable {
		return invalidCandidatePolicy("network boundary")
	}
	resources := policy.Resources
	if resources.CPUs != 2 || resources.MemoryMiB != 4096 || resources.MemorySwapMiB != 4096 || resources.PIDsLimit != 256 || resources.FileDescriptors != 1024 || resources.CommandTimeoutSeconds != 600 || resources.AttemptTimeoutSeconds != 1200 {
		return invalidCandidatePolicy("resource limits")
	}
	if policy.Credentials.Injection != "task_scoped_explicit_only" || len(policy.Credentials.DeclaredNames) != 0 || policy.Credentials.Persist || policy.Credentials.LogValues {
		return invalidCandidatePolicy("credentials")
	}
	termination := policy.Termination
	if !termination.RPCAbortIsCooperativeOnly || termination.CooperativeAbortWaitSeconds != 2 || termination.DeathConfirmationSeconds != 5 || !termination.ForceContainerRemoval || !termination.RequireProcessTreeDeath || !termination.RevokeNetworkBeforeKill || termination.Uncertainty != "recovery_required_fail_closed" {
		return invalidCandidatePolicy("termination")
	}
	if policy.Limits.PersistedLogBytes != 10*1024*1024 || policy.Limits.ArtifactBytes != 100*1024*1024 {
		return invalidCandidatePolicy("output limits")
	}
	wantLabels := []string{"chora.run_id", "chora.attempt_id", "chora.task_id", "chora.image_digest", "chora.policy_digest"}
	if !slicesEqual(policy.Identity.RequiredLabels, wantLabels) || !policy.Identity.PolicyDigestRequired {
		return invalidCandidatePolicy("identity labels")
	}
	cleanup := policy.Cleanup
	if !cleanup.RemoveContainer || !cleanup.RemoveInternalNetwork || !cleanup.RemoveUpstreamBoundary || !cleanup.RemoveWorkspace || !cleanup.AllTerminalPaths || !cleanup.RecoveryEnumeratesLabeledResourcesOnly {
		return invalidCandidatePolicy("cleanup")
	}
	return nil
}

func invalidCandidatePolicy(field string) error {
	return fmt.Errorf("%w: %s", ErrInvalidCandidatePolicy, field)
}

func equalStringMap(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range right {
		if left[key] != value {
			return false
		}
	}
	return true
}

func allFalseKeys(values map[string]bool, expected []string) bool {
	if len(values) != len(expected) {
		return false
	}
	for _, key := range expected {
		value, ok := values[key]
		if !ok || value {
			return false
		}
	}
	return true
}
