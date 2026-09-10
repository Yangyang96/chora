package speccoding

const (
	PiRuntimeConfigSchemaVersionV3 = "chora.pi-runtime-config.v3"
	SandboxPolicySchemaVersionV3   = "chora.m1-local-sandbox-policy.v3"
)

type PiRuntimeConfigV3 struct {
	SchemaVersion   string              `json:"schema_version"`
	Runtime         PiRuntimeV2         `json:"runtime"`
	Environment     map[string]string   `json:"environment"`
	AgentDirectory  PiAgentDirectoryV3  `json:"agent_directory"`
	Resources       map[string]bool     `json:"resources"`
	Model           PiModelV3           `json:"model"`
	Authentication  PiAuthenticationV3  `json:"authentication"`
	Context         PiContextBoundaryV2 `json:"context"`
	EventProjection PiEventProjectionV2 `json:"event_projection"`
	ResultAuthority PiResultAuthorityV2 `json:"result_authority"`
	ModelDependency PiModelDependencyV2 `json:"model_dependency"`
}

type PiAgentDirectoryV3 struct {
	Lifecycle             string   `json:"lifecycle"`
	AllowedGeneratedFiles []string `json:"allowed_generated_files"`
	InheritHostState      bool     `json:"inherit_host_state"`
	RetainAfterAttempt    bool     `json:"retain_after_attempt"`
}

type PiModelV3 struct {
	Provider            string `json:"provider"`
	API                 string `json:"api"`
	BaseURL             string `json:"base_url"`
	ID                  string `json:"id"`
	CatalogSHA256       string `json:"catalog_sha256"`
	CatalogIdentityKind string `json:"catalog_identity_kind"`
}

type PiAuthenticationV3 struct {
	Type                    string `json:"type"`
	SourceEnvironmentName   string `json:"source_environment_name"`
	SourceDefaultForbidden  bool   `json:"source_default_forbidden"`
	TaskProjection          string `json:"task_projection"`
	CredentialFile          string `json:"credential_file"`
	CredentialFileMode      string `json:"credential_file_mode"`
	PersistCredentialValues bool   `json:"persist_credential_values"`
	LogCredentialValues     bool   `json:"log_credential_values"`
	CopyBackToHost          bool   `json:"copy_back_to_host"`
}

type SandboxPolicyV3 struct {
	SchemaVersion string                `json:"schema_version"`
	Provider      SandboxProviderV3     `json:"provider"`
	Attempt       SandboxAttemptV2      `json:"attempt"`
	Isolation     SandboxIsolationV2    `json:"isolation"`
	Network       SandboxNetworkV3      `json:"network"`
	Resources     SandboxResourcesV2    `json:"resources"`
	Credentials   SandboxCredentialsV3  `json:"credentials"`
	Termination   SandboxTerminationV2  `json:"termination"`
	Limits        SandboxOutputLimitsV2 `json:"limits"`
	Identity      SandboxIdentityV3     `json:"identity"`
	Cleanup       SandboxCleanupV3      `json:"cleanup"`
}

type SandboxProviderV3 struct {
	Name                string `json:"name"`
	ColimaVersion       string `json:"colima_version"`
	ColimaSHA256        string `json:"colima_sha256"`
	Engine              string `json:"engine"`
	EngineClientVersion string `json:"engine_client_version"`
	EngineClientSHA256  string `json:"engine_client_sha256"`
}

type SandboxNetworkV3 struct {
	Mode                   string              `json:"mode"`
	AttemptNetworkInternal bool                `json:"attempt_network_internal"`
	GeneralEgress          bool                `json:"general_egress"`
	HostNetwork            bool                `json:"host_network"`
	PublishedPorts         bool                `json:"published_ports"`
	DockerSocketMounted    bool                `json:"docker_socket_mounted"`
	NormalBridgeFallback   bool                `json:"normal_bridge_fallback"`
	Proxy                  SandboxCodexProxyV3 `json:"proxy"`
}

type SandboxCodexProxyV3 struct {
	Endpoint             string   `json:"endpoint"`
	Protocol             string   `json:"protocol"`
	AllowedHosts         []string `json:"allowed_hosts"`
	AllowedPorts         []int    `json:"allowed_ports"`
	DenyIPLiteral        bool     `json:"deny_ip_literal"`
	ResolveDNSAtBoundary bool     `json:"resolve_dns_at_boundary"`
	TLSTermination       bool     `json:"tls_termination"`
	TaskScoped           bool     `json:"task_scoped"`
	Traceable            bool     `json:"traceable"`
}

type SandboxCredentialsV3 struct {
	Injection         string   `json:"injection"`
	DeclaredNames     []string `json:"declared_names"`
	Persist           bool     `json:"persist"`
	LogValues         bool     `json:"log_values"`
	MountSourceFile   bool     `json:"mount_source_file"`
	RemoveAttemptCopy bool     `json:"remove_attempt_copy"`
	RefreshFailure    string   `json:"refresh_failure"`
}

type SandboxIdentityV3 struct {
	RequiredLabels       []string `json:"required_labels"`
	PolicyDigestRequired bool     `json:"policy_digest_required"`
}

type SandboxCleanupV3 struct {
	RemoveContainer                        bool `json:"remove_container"`
	RemoveInternalNetwork                  bool `json:"remove_internal_network"`
	RemoveUpstreamBoundary                 bool `json:"remove_upstream_boundary"`
	RemoveWorkspace                        bool `json:"remove_workspace"`
	AllTerminalPaths                       bool `json:"all_terminal_paths"`
	RecoveryEnumeratesLabeledResourcesOnly bool `json:"recovery_enumerates_labeled_resources_only"`
}

func DecodePiRuntimeConfigV3(data []byte) (PiRuntimeConfigV3, error) {
	var config PiRuntimeConfigV3
	if err := decodeCandidatePolicy(data, &config); err != nil {
		return PiRuntimeConfigV3{}, err
	}
	if err := validatePiRuntimeConfigV3(config); err != nil {
		return PiRuntimeConfigV3{}, err
	}
	return config, nil
}

func DecodeSandboxPolicyV3(data []byte) (SandboxPolicyV3, error) {
	var policy SandboxPolicyV3
	if err := decodeCandidatePolicy(data, &policy); err != nil {
		return SandboxPolicyV3{}, err
	}
	if err := validateSandboxPolicyV3(policy); err != nil {
		return SandboxPolicyV3{}, err
	}
	return policy, nil
}

func validatePiRuntimeConfigV3(config PiRuntimeConfigV3) error {
	if config.SchemaVersion != PiRuntimeConfigSchemaVersionV3 || config.Runtime.Package != "@earendil-works/pi-coding-agent" || config.Runtime.Version != "0.84.1" || config.Runtime.NPMIntegrity != "sha512-ncAqFrG+iybuPGOhMiZoEHkEzTpJgz3guYD32pD+M7ucc0WeHmauP6wa7qwP8V/KWvsZDVNa5XGsdZ7fkC7w7A==" || !slicesEqual(config.Runtime.Transport, append([]string{"pi"}, requiredPiArgumentsV3...)) {
		return invalidCandidatePolicy("v3 runtime identity or transport")
	}
	wantEnvironment := map[string]string{
		"PI_CODING_AGENT_DIR":   "/run/chora/pi",
		"PI_OFFLINE":            "1",
		"PI_SKIP_VERSION_CHECK": "1",
		"PI_TELEMETRY":          "0",
		"HTTP_PROXY":            "http://codex-boundary:8080",
		"HTTPS_PROXY":           "http://codex-boundary:8080",
		"NO_PROXY":              "localhost,127.0.0.1",
	}
	if !equalStringMap(config.Environment, wantEnvironment) || config.AgentDirectory.Lifecycle != "new_empty_directory_per_attempt" || !slicesEqual(config.AgentDirectory.AllowedGeneratedFiles, []string{"auth.json"}) || config.AgentDirectory.InheritHostState || config.AgentDirectory.RetainAfterAttempt {
		return invalidCandidatePolicy("v3 agent state boundary")
	}
	wantResources := []string{"third_party_packages", "extensions", "skills", "prompt_templates", "themes", "project_context_files", "undeclared_project_resources", "host_pi_state"}
	if !allFalseKeys(config.Resources, wantResources) {
		return invalidCandidatePolicy("v3 implicit resources")
	}
	model := config.Model
	if model.Provider != "openai-codex" || model.API != "openai-codex-responses" || model.BaseURL != "https://chatgpt.com/backend-api" || model.ID != "gpt-5.6-sol" || model.CatalogSHA256 != piOpenAICodexCatalogSHA256 || model.CatalogIdentityKind != "pi_builtin_provider_catalog_sha256" {
		return invalidCandidatePolicy("v3 model identity")
	}
	auth := config.Authentication
	if auth.Type != "openai_codex_oauth" || auth.SourceEnvironmentName != "CHORA_PI_CODEX_AUTH_FILE" || !auth.SourceDefaultForbidden || auth.TaskProjection != "openai-codex_entry_only" || auth.CredentialFile != "/run/chora/pi/auth.json" || auth.CredentialFileMode != "0600" || auth.PersistCredentialValues || auth.LogCredentialValues || auth.CopyBackToHost {
		return invalidCandidatePolicy("v3 authentication boundary")
	}
	if err := validatePiSharedBoundaries(config.Context, config.EventProjection, config.ResultAuthority); err != nil {
		return err
	}
	dependency := config.ModelDependency
	if dependency.Provider != "openai-codex" || dependency.ModelID != "gpt-5.6-sol" || dependency.ModelDigest != "sha256:"+piOpenAICodexCatalogSHA256 || dependency.Scope != "replaceable_task_dependency" || dependency.ProductInterfaceExposure != "none" || dependency.ReplacementAuthority != "explicit_user_new_capsule" || dependency.SemanticFailureLimit != 2 {
		return invalidCandidatePolicy("v3 task model dependency")
	}
	return nil
}

func validatePiSharedBoundaries(context PiContextBoundaryV2, projection PiEventProjectionV2, result PiResultAuthorityV2) error {
	if context.ExplicitSource != "digest_bound_context_snapshot_only" || context.WorkingDirectory != "/workspace" || context.RepositoryChild != "/workspace/repository" || context.InheritHostPiState || context.LoadProjectInstructions || context.LoadPackages || context.LoadExtensions || context.LoadSkills || context.LoadPrompts || context.LoadContextFiles {
		return invalidCandidatePolicy("v3 implicit context")
	}
	wantEvents := []string{"agent_start", "agent_end", "tool_start", "tool_end", "file_path", "command_summary", "exit_code", "error", "model_identity", "usage"}
	wantFields := []string{"event_type", "timestamp", "tool_name", "workspace_relative_path", "redacted_command_summary", "exit_code", "error_class", "model_id", "model_digest", "usage"}
	if !slicesEqual(projection.AllowedEvents, wantEvents) || !slicesEqual(projection.AllowedFields, wantFields) || projection.PersistThinking || projection.PersistRawPrompt || projection.PersistCredentialValues || projection.CanonicalEventOwner != "chora" {
		return invalidCandidatePolicy("v3 event projection")
	}
	if result.Owner != "chora" || result.TerminalResultCount != 1 || result.PiAgentEndMeansSuccess || result.PiFinalTextRole != "agent_summary_only" || !result.RequiresBaselineDiff || !result.RequiresCommandBoundChecks || !result.RequiresSandboxReconciliation || result.MissingEvidencePolicy != "explicit_unknown_or_failure" {
		return invalidCandidatePolicy("v3 result authority")
	}
	return nil
}

func validateSandboxPolicyV3(policy SandboxPolicyV3) error {
	provider := policy.Provider
	if policy.SchemaVersion != SandboxPolicySchemaVersionV3 || provider.Name != "colima-docker" || provider.ColimaVersion != "0.10.3" || provider.ColimaSHA256 != v2ColimaSHA256 || provider.Engine != "docker" || provider.EngineClientVersion != "29.6.1" || provider.EngineClientSHA256 != v2DockerClientSHA256 {
		return invalidCandidatePolicy("v3 provider identity")
	}
	attempt := policy.Attempt
	if attempt.ContainerLifecycle != "one_disposable_container_per_attempt" || attempt.WorkspaceLifecycle != "fresh_copy_per_attempt_removed_on_terminal" || attempt.WorkspaceMount != "/workspace" || attempt.RepositoryPath != "/workspace/repository" || attempt.ContextMount != "/input/context" || !attempt.ContextReadOnly || attempt.OriginalRepositoryWritableMounted || attempt.SourceMaterialization != "fresh_copy_from_frozen_revision_plus_selected_dirty_input" || attempt.ReusePreviousAttempt || len(attempt.OtherHostMounts) != 0 || attempt.PiAgentDirectory != "/run/chora/pi" || attempt.WorkingDirectory != "/workspace" || attempt.User != "1000:1000" {
		return invalidCandidatePolicy("v3 attempt workspace")
	}
	isolation := policy.Isolation
	if !isolation.ReadOnlyRoot || !slicesEqual(isolation.Tmpfs, []string{"/tmp", "/run/chora/pi"}) || !isolation.DropAllCapabilities || !isolation.NoNewPrivileges || isolation.SeccompProfile != "docker_default" || isolation.Privileged || len(isolation.DeviceMounts) != 0 || isolation.HostNetwork || isolation.PublishPorts || isolation.DockerSocketMounted || isolation.NormalBridgeFallback || isolation.UnrelatedHostPathsMounted || !isolation.FailClosed {
		return invalidCandidatePolicy("v3 container isolation")
	}
	network := policy.Network
	if network.Mode != "attempt_private_internal_with_fixed_codex_proxy" || !network.AttemptNetworkInternal || network.GeneralEgress || network.HostNetwork || network.PublishedPorts || network.DockerSocketMounted || network.NormalBridgeFallback || network.Proxy.Endpoint != "http://codex-boundary:8080" || network.Proxy.Protocol != "http_connect" || !slicesEqual(network.Proxy.AllowedHosts, []string{"auth.openai.com", "chatgpt.com"}) || !slicesEqualInt(network.Proxy.AllowedPorts, []int{443}) || !network.Proxy.DenyIPLiteral || !network.Proxy.ResolveDNSAtBoundary || network.Proxy.TLSTermination || !network.Proxy.TaskScoped || !network.Proxy.Traceable {
		return invalidCandidatePolicy("v3 network boundary")
	}
	resources := policy.Resources
	if resources.CPUs != 2 || resources.MemoryMiB != 4096 || resources.MemorySwapMiB != 4096 || resources.PIDsLimit != 256 || resources.FileDescriptors != 1024 || resources.CommandTimeoutSeconds != 600 || resources.AttemptTimeoutSeconds != 1200 {
		return invalidCandidatePolicy("v3 resource limits")
	}
	credentials := policy.Credentials
	if credentials.Injection != "task_scoped_explicit_file_projection" || !slicesEqual(credentials.DeclaredNames, []string{"CHORA_PI_CODEX_AUTH_FILE"}) || credentials.Persist || credentials.LogValues || credentials.MountSourceFile || !credentials.RemoveAttemptCopy || credentials.RefreshFailure != "AUTH_EXPIRED_FAIL_CLOSED" {
		return invalidCandidatePolicy("v3 credentials")
	}
	termination := policy.Termination
	if !termination.RPCAbortIsCooperativeOnly || termination.CooperativeAbortWaitSeconds != 2 || termination.DeathConfirmationSeconds != 5 || !termination.ForceContainerRemoval || !termination.RequireProcessTreeDeath || !termination.RevokeNetworkBeforeKill || termination.Uncertainty != "recovery_required_fail_closed" {
		return invalidCandidatePolicy("v3 termination")
	}
	if policy.Limits.PersistedLogBytes != 10*1024*1024 || policy.Limits.ArtifactBytes != 100*1024*1024 {
		return invalidCandidatePolicy("v3 output limits")
	}
	wantLabels := []string{"chora.run_id", "chora.attempt_id", "chora.task_id", "chora.image_digest", "chora.policy_digest"}
	if !slicesEqual(policy.Identity.RequiredLabels, wantLabels) || !policy.Identity.PolicyDigestRequired {
		return invalidCandidatePolicy("v3 identity labels")
	}
	cleanup := policy.Cleanup
	if !cleanup.RemoveContainer || !cleanup.RemoveInternalNetwork || !cleanup.RemoveUpstreamBoundary || !cleanup.RemoveWorkspace || !cleanup.AllTerminalPaths || !cleanup.RecoveryEnumeratesLabeledResourcesOnly {
		return invalidCandidatePolicy("v3 cleanup")
	}
	return nil
}

func slicesEqualInt(left, right []int) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
