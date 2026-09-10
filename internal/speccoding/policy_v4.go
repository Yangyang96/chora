package speccoding

const (
	PiRuntimeConfigSchemaVersionV4 = "chora.pi-runtime-config.v4"
	SandboxPolicySchemaVersionV4   = "chora.m1-local-sandbox-policy.v4"
)

type PiRuntimeConfigV4 struct {
	SchemaVersion   string              `json:"schema_version"`
	Runtime         PiRuntimeV2         `json:"runtime"`
	Environment     map[string]string   `json:"environment"`
	AgentDirectory  PiAgentDirectoryV3  `json:"agent_directory"`
	Resources       map[string]bool     `json:"resources"`
	Model           PiModelV3           `json:"model"`
	Authentication  PiAuthenticationV3  `json:"authentication"`
	Trust           PiTrustV4           `json:"trust"`
	Context         PiContextBoundaryV2 `json:"context"`
	EventProjection PiEventProjectionV2 `json:"event_projection"`
	ResultAuthority PiResultAuthorityV2 `json:"result_authority"`
	ModelDependency PiModelDependencyV2 `json:"model_dependency"`
}

type PiTrustV4 struct {
	Source                        ConfigReference `json:"source"`
	ContainerPath                 string          `json:"container_path"`
	Projection                    string          `json:"projection"`
	DirectoryPath                 string          `json:"directory_path"`
	DirectoryOwner                string          `json:"directory_owner"`
	DirectoryModeBeforeProjection string          `json:"directory_mode_before_projection"`
	DirectoryModeAfterProjection  string          `json:"directory_mode_after_projection"`
	FileOwner                     string          `json:"file_owner"`
	FileMode                      string          `json:"file_mode"`
	ProjectionBeforeRuntimeStart  bool            `json:"projection_before_runtime_start"`
	RuntimeWriteProbeRequired     bool            `json:"runtime_write_probe_required"`
	SourceMounted                 bool            `json:"source_mounted"`
	Persist                       bool            `json:"persist"`
	AgentWritable                 bool            `json:"agent_writable"`
	SystemStoreModified           bool            `json:"system_store_modified"`
	TLSVerificationRequired       bool            `json:"tls_verification_required"`
}

type SandboxPolicyV4 struct {
	SchemaVersion string                `json:"schema_version"`
	Provider      SandboxProviderV3     `json:"provider"`
	Attempt       SandboxAttemptV2      `json:"attempt"`
	Isolation     SandboxIsolationV2    `json:"isolation"`
	Network       SandboxNetworkV4      `json:"network"`
	Resources     SandboxResourcesV2    `json:"resources"`
	Credentials   SandboxCredentialsV3  `json:"credentials"`
	Trust         SandboxTrustV4        `json:"trust"`
	Termination   SandboxTerminationV2  `json:"termination"`
	Limits        SandboxOutputLimitsV2 `json:"limits"`
	Identity      SandboxIdentityV3     `json:"identity"`
	Cleanup       SandboxCleanupV3      `json:"cleanup"`
}

type SandboxNetworkV4 struct {
	Mode                   string              `json:"mode"`
	AttemptNetworkInternal bool                `json:"attempt_network_internal"`
	GeneralEgress          bool                `json:"general_egress"`
	HostNetwork            bool                `json:"host_network"`
	PublishedPorts         bool                `json:"published_ports"`
	DockerSocketMounted    bool                `json:"docker_socket_mounted"`
	NormalBridgeFallback   bool                `json:"normal_bridge_fallback"`
	Proxy                  SandboxCodexProxyV4 `json:"proxy"`
}

type SandboxCodexProxyV4 struct {
	Endpoint             string                   `json:"endpoint"`
	Protocol             string                   `json:"protocol"`
	AllowedHosts         []string                 `json:"allowed_hosts"`
	AllowedPorts         []int                    `json:"allowed_ports"`
	DenyIPLiteral        bool                     `json:"deny_ip_literal"`
	ResolveDNSAtBoundary bool                     `json:"resolve_dns_at_boundary"`
	DNSResolutionOwner   string                   `json:"dns_resolution_owner"`
	TaskScoped           bool                     `json:"task_scoped"`
	Traceable            bool                     `json:"traceable"`
	Upstream             SandboxEnterpriseProxyV4 `json:"upstream"`
	TLS                  SandboxTLSBoundaryV4     `json:"tls"`
}

type SandboxEnterpriseProxyV4 struct {
	Kind                  string `json:"kind"`
	Endpoint              string `json:"endpoint"`
	AccessibleFromAttempt bool   `json:"accessible_from_attempt"`
	TaskScoped            bool   `json:"task_scoped"`
	Traceable             bool   `json:"traceable"`
}

type SandboxTLSBoundaryV4 struct {
	CodexBoundaryTerminates      bool   `json:"codex_boundary_terminates"`
	EnterpriseUpstreamTerminates bool   `json:"enterprise_upstream_terminates"`
	VerificationRequired         bool   `json:"verification_required"`
	TrustAnchorPath              string `json:"trust_anchor_path"`
	TrustAnchorSHA256            string `json:"trust_anchor_sha256"`
}

type SandboxTrustV4 struct {
	Projection                    string `json:"projection"`
	SourcePath                    string `json:"source_path"`
	SourceSHA256                  string `json:"source_sha256"`
	ContainerPath                 string `json:"container_path"`
	DirectoryPath                 string `json:"directory_path"`
	DirectoryOwner                string `json:"directory_owner"`
	DirectoryModeBeforeProjection string `json:"directory_mode_before_projection"`
	DirectoryModeAfterProjection  string `json:"directory_mode_after_projection"`
	FileOwner                     string `json:"file_owner"`
	FileMode                      string `json:"file_mode"`
	ProjectionBeforeRuntimeStart  bool   `json:"projection_before_runtime_start"`
	RuntimeWriteProbeRequired     bool   `json:"runtime_write_probe_required"`
	SourceMounted                 bool   `json:"source_mounted"`
	AgentWritable                 bool   `json:"agent_writable"`
	Persist                       bool   `json:"persist"`
	RemoveAttemptCopy             bool   `json:"remove_attempt_copy"`
}

func DecodePiRuntimeConfigV4(data []byte) (PiRuntimeConfigV4, error) {
	var config PiRuntimeConfigV4
	if err := decodeCandidatePolicy(data, &config); err != nil {
		return PiRuntimeConfigV4{}, err
	}
	if err := validatePiRuntimeConfigV4(config); err != nil {
		return PiRuntimeConfigV4{}, err
	}
	return config, nil
}

func DecodeSandboxPolicyV4(data []byte) (SandboxPolicyV4, error) {
	var policy SandboxPolicyV4
	if err := decodeCandidatePolicy(data, &policy); err != nil {
		return SandboxPolicyV4{}, err
	}
	if err := validateSandboxPolicyV4(policy); err != nil {
		return SandboxPolicyV4{}, err
	}
	return policy, nil
}

func validatePiRuntimeConfigV4(config PiRuntimeConfigV4) error {
	if config.SchemaVersion != PiRuntimeConfigSchemaVersionV4 || config.Runtime.Package != "@earendil-works/pi-coding-agent" || config.Runtime.Version != "0.84.1" || config.Runtime.NPMIntegrity != piRuntimeNPMIntegrity || !slicesEqual(config.Runtime.Transport, append([]string{"pi"}, requiredPiArgumentsV3...)) {
		return invalidCandidatePolicy("v4 runtime identity or transport")
	}
	wantEnvironment := map[string]string{
		"PI_CODING_AGENT_DIR":   "/run/chora/pi",
		"PI_OFFLINE":            "1",
		"PI_SKIP_VERSION_CHECK": "1",
		"PI_TELEMETRY":          "0",
		"HTTP_PROXY":            "http://codex-boundary:8080",
		"HTTPS_PROXY":           "http://codex-boundary:8080",
		"NO_PROXY":              "localhost,127.0.0.1",
		"NODE_EXTRA_CA_CERTS":   "/run/chora/trust/starpoint-root-ca-2048-g2.pem",
	}
	if !equalStringMap(config.Environment, wantEnvironment) || config.AgentDirectory.Lifecycle != "new_empty_directory_per_attempt" || !slicesEqual(config.AgentDirectory.AllowedGeneratedFiles, []string{"auth.json"}) || config.AgentDirectory.InheritHostState || config.AgentDirectory.RetainAfterAttempt {
		return invalidCandidatePolicy("v4 agent state boundary")
	}
	wantResources := []string{"third_party_packages", "extensions", "skills", "prompt_templates", "themes", "project_context_files", "undeclared_project_resources", "host_pi_state"}
	if !allFalseKeys(config.Resources, wantResources) {
		return invalidCandidatePolicy("v4 implicit resources")
	}
	model := config.Model
	if model.Provider != "openai-codex" || model.API != "openai-codex-responses" || model.BaseURL != "https://chatgpt.com/backend-api" || model.ID != "gpt-5.6-sol" || model.CatalogSHA256 != piOpenAICodexCatalogSHA256 || model.CatalogIdentityKind != "pi_builtin_provider_catalog_sha256" {
		return invalidCandidatePolicy("v4 model identity")
	}
	auth := config.Authentication
	if auth.Type != "openai_codex_oauth" || auth.SourceEnvironmentName != "CHORA_PI_CODEX_AUTH_FILE" || !auth.SourceDefaultForbidden || auth.TaskProjection != "openai-codex_entry_only" || auth.CredentialFile != "/run/chora/pi/auth.json" || auth.CredentialFileMode != "0600" || auth.PersistCredentialValues || auth.LogCredentialValues || auth.CopyBackToHost {
		return invalidCandidatePolicy("v4 authentication boundary")
	}
	trust := config.Trust
	if trust.Source.Path != starpointRootCAPath || trust.Source.SHA256 != starpointRootCASHA256 || trust.ContainerPath != starpointRootCAContainerPath || trust.Projection != "task_scoped_stdin_projection" || trust.DirectoryPath != "/run/chora/trust" || trust.DirectoryOwner != "0:0" || trust.DirectoryModeBeforeProjection != "0755" || trust.DirectoryModeAfterProjection != "0555" || trust.FileOwner != "0:0" || trust.FileMode != "0444" || !trust.ProjectionBeforeRuntimeStart || !trust.RuntimeWriteProbeRequired || trust.SourceMounted || trust.Persist || trust.AgentWritable || trust.SystemStoreModified || !trust.TLSVerificationRequired {
		return invalidCandidatePolicy("v4 trust boundary")
	}
	if err := validatePiSharedBoundaries(config.Context, config.EventProjection, config.ResultAuthority); err != nil {
		return err
	}
	dependency := config.ModelDependency
	if dependency.Provider != "openai-codex" || dependency.ModelID != "gpt-5.6-sol" || dependency.ModelDigest != "sha256:"+piOpenAICodexCatalogSHA256 || dependency.Scope != "replaceable_task_dependency" || dependency.ProductInterfaceExposure != "none" || dependency.ReplacementAuthority != "explicit_user_new_capsule" || dependency.SemanticFailureLimit != 2 {
		return invalidCandidatePolicy("v4 task model dependency")
	}
	return nil
}

func validateSandboxPolicyV4(policy SandboxPolicyV4) error {
	provider := policy.Provider
	if policy.SchemaVersion != SandboxPolicySchemaVersionV4 || provider.Name != "colima-docker" || provider.ColimaVersion != "0.10.3" || provider.ColimaSHA256 != v2ColimaSHA256 || provider.Engine != "docker" || provider.EngineClientVersion != "29.6.1" || provider.EngineClientSHA256 != v2DockerClientSHA256 {
		return invalidCandidatePolicy("v4 provider identity")
	}
	attempt := policy.Attempt
	if attempt.ContainerLifecycle != "one_disposable_container_per_attempt" || attempt.WorkspaceLifecycle != "fresh_copy_per_attempt_removed_on_terminal" || attempt.WorkspaceMount != "/workspace" || attempt.RepositoryPath != "/workspace/repository" || attempt.ContextMount != "/input/context" || !attempt.ContextReadOnly || attempt.OriginalRepositoryWritableMounted || attempt.SourceMaterialization != "fresh_copy_from_frozen_revision_plus_selected_dirty_input" || attempt.ReusePreviousAttempt || len(attempt.OtherHostMounts) != 0 || attempt.PiAgentDirectory != "/run/chora/pi" || attempt.WorkingDirectory != "/workspace" || attempt.User != "1000:1000" {
		return invalidCandidatePolicy("v4 attempt workspace")
	}
	isolation := policy.Isolation
	if !isolation.ReadOnlyRoot || !slicesEqual(isolation.Tmpfs, []string{"/tmp", "/run/chora/pi", "/run/chora/trust"}) || !isolation.DropAllCapabilities || !isolation.NoNewPrivileges || isolation.SeccompProfile != "docker_default" || isolation.Privileged || len(isolation.DeviceMounts) != 0 || isolation.HostNetwork || isolation.PublishPorts || isolation.DockerSocketMounted || isolation.NormalBridgeFallback || isolation.UnrelatedHostPathsMounted || !isolation.FailClosed {
		return invalidCandidatePolicy("v4 container isolation")
	}
	network := policy.Network
	proxy := network.Proxy
	if network.Mode != "attempt_private_internal_with_fixed_codex_proxy_and_enterprise_upstream" || !network.AttemptNetworkInternal || network.GeneralEgress || network.HostNetwork || network.PublishedPorts || network.DockerSocketMounted || network.NormalBridgeFallback || proxy.Endpoint != "http://codex-boundary:8080" || proxy.Protocol != "http_connect" || !slicesEqual(proxy.AllowedHosts, []string{"auth.openai.com", "chatgpt.com"}) || !slicesEqualInt(proxy.AllowedPorts, []int{443}) || !proxy.DenyIPLiteral || proxy.ResolveDNSAtBoundary || proxy.DNSResolutionOwner != "fixed_enterprise_upstream" || !proxy.TaskScoped || !proxy.Traceable {
		return invalidCandidatePolicy("v4 network boundary")
	}
	if proxy.Upstream.Kind != "fixed_enterprise_http_connect_proxy" || proxy.Upstream.Endpoint != "http://host.docker.internal:9981" || proxy.Upstream.AccessibleFromAttempt || !proxy.Upstream.TaskScoped || !proxy.Upstream.Traceable {
		return invalidCandidatePolicy("v4 enterprise upstream")
	}
	if proxy.TLS.CodexBoundaryTerminates || !proxy.TLS.EnterpriseUpstreamTerminates || !proxy.TLS.VerificationRequired || proxy.TLS.TrustAnchorPath != starpointRootCAContainerPath || proxy.TLS.TrustAnchorSHA256 != starpointRootCASHA256 {
		return invalidCandidatePolicy("v4 TLS boundary")
	}
	resources := policy.Resources
	if resources.CPUs != 2 || resources.MemoryMiB != 4096 || resources.MemorySwapMiB != 4096 || resources.PIDsLimit != 256 || resources.FileDescriptors != 1024 || resources.CommandTimeoutSeconds != 600 || resources.AttemptTimeoutSeconds != 1200 {
		return invalidCandidatePolicy("v4 resource limits")
	}
	credentials := policy.Credentials
	if credentials.Injection != "task_scoped_explicit_file_projection" || !slicesEqual(credentials.DeclaredNames, []string{"CHORA_PI_CODEX_AUTH_FILE"}) || credentials.Persist || credentials.LogValues || credentials.MountSourceFile || !credentials.RemoveAttemptCopy || credentials.RefreshFailure != "AUTH_EXPIRED_FAIL_CLOSED" {
		return invalidCandidatePolicy("v4 credentials")
	}
	trust := policy.Trust
	if trust.Projection != "task_scoped_stdin_projection" || trust.SourcePath != starpointRootCAPath || trust.SourceSHA256 != starpointRootCASHA256 || trust.ContainerPath != starpointRootCAContainerPath || trust.DirectoryPath != "/run/chora/trust" || trust.DirectoryOwner != "0:0" || trust.DirectoryModeBeforeProjection != "0755" || trust.DirectoryModeAfterProjection != "0555" || trust.FileOwner != "0:0" || trust.FileMode != "0444" || !trust.ProjectionBeforeRuntimeStart || !trust.RuntimeWriteProbeRequired || trust.SourceMounted || trust.AgentWritable || trust.Persist || !trust.RemoveAttemptCopy {
		return invalidCandidatePolicy("v4 trust projection")
	}
	termination := policy.Termination
	if !termination.RPCAbortIsCooperativeOnly || termination.CooperativeAbortWaitSeconds != 2 || termination.DeathConfirmationSeconds != 5 || !termination.ForceContainerRemoval || !termination.RequireProcessTreeDeath || !termination.RevokeNetworkBeforeKill || termination.Uncertainty != "recovery_required_fail_closed" {
		return invalidCandidatePolicy("v4 termination")
	}
	if policy.Limits.PersistedLogBytes != 10*1024*1024 || policy.Limits.ArtifactBytes != 100*1024*1024 {
		return invalidCandidatePolicy("v4 output limits")
	}
	wantLabels := []string{"chora.run_id", "chora.attempt_id", "chora.task_id", "chora.image_digest", "chora.policy_digest"}
	if !slicesEqual(policy.Identity.RequiredLabels, wantLabels) || !policy.Identity.PolicyDigestRequired {
		return invalidCandidatePolicy("v4 identity labels")
	}
	cleanup := policy.Cleanup
	if !cleanup.RemoveContainer || !cleanup.RemoveInternalNetwork || !cleanup.RemoveUpstreamBoundary || !cleanup.RemoveWorkspace || !cleanup.AllTerminalPaths || !cleanup.RecoveryEnumeratesLabeledResourcesOnly {
		return invalidCandidatePolicy("v4 cleanup")
	}
	return nil
}
