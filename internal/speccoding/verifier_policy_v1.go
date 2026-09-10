package speccoding

import (
	"crypto/sha256"
	"encoding/hex"
)

const (
	VerifierPolicySchemaVersionV1 = "chora.m1-verifier-policy.v1"
	VerifierPolicyModeFullV1      = "full_verification"
	VerifierPolicyModeUnknownV1   = "acceptance_only_evidence_unavailable"

	VerifierPolicySHA256V1               = "93e905405abfbd25f7899ac089d9b49f93312b3f5ff85aec985f05a4259fe12b"
	VerifierPolicyAcceptanceOnlySHA256V1 = "fdc86de1d745e989630bc7c5a40aceb476c587f34b2d4fd08adcf9b8e66447b5"
)

type VerifierPolicyV1 struct {
	SchemaVersion   string                    `json:"schema_version"`
	PolicyMode      string                    `json:"policy_mode"`
	Provider        VerifierProviderV1        `json:"provider"`
	Image           VerifierImageV1           `json:"image"`
	Workspace       VerifierWorkspaceV1       `json:"workspace"`
	Command         VerifierCommandPolicyV1   `json:"command"`
	Isolation       VerifierIsolationV1       `json:"isolation"`
	Network         VerifierNetworkV1         `json:"network"`
	Credentials     VerifierCredentialsV1     `json:"credentials"`
	Resources       VerifierResourcesV1       `json:"resources"`
	Evidence        VerifierEvidencePolicyV1  `json:"evidence"`
	Termination     VerifierTerminationV1     `json:"termination"`
	Identity        VerifierIdentityPolicyV1  `json:"identity"`
	Cleanup         VerifierCleanupPolicyV1   `json:"cleanup"`
	ResultAuthority VerifierResultAuthorityV1 `json:"result_authority"`
}

type VerifierProviderV1 struct {
	Name          string `json:"name"`
	ColimaVersion string `json:"colima_version"`
	Engine        string `json:"engine"`
	EngineVersion string `json:"engine_version"`
}

type VerifierImageV1 struct {
	Reference           string `json:"reference"`
	ID                  string `json:"id"`
	AgentRuntimeInvoked bool   `json:"agent_runtime_invoked"`
}

type VerifierWorkspaceV1 struct {
	Lifecycle                          string   `json:"lifecycle"`
	SourceMaterialization              string   `json:"source_materialization"`
	Mount                              string   `json:"mount"`
	RepositoryPath                     string   `json:"repository_path"`
	WorkingDirectory                   string   `json:"working_directory"`
	User                               string   `json:"user"`
	AllowedWriteRoots                  []string `json:"allowed_write_roots"`
	ReuseAgentWorkspace                bool     `json:"reuse_agent_workspace"`
	ReusePreviousVerificationWorkspace bool     `json:"reuse_previous_verification_workspace"`
	LoadPiSession                      bool     `json:"load_pi_session"`
	LoadModelCredentials               bool     `json:"load_model_credentials"`
	LoadHostAgentState                 bool     `json:"load_host_agent_state"`
	LoadImplicitContext                bool     `json:"load_implicit_context"`
	LoadAgentPackages                  bool     `json:"load_agent_packages"`
	LoadAgentExtensions                bool     `json:"load_agent_extensions"`
	LoadAgentSkills                    bool     `json:"load_agent_skills"`
	LoadAgentPrompts                   bool     `json:"load_agent_prompts"`
	PatchDigestRequired                bool     `json:"patch_digest_required"`
	PatchMustBeNonempty                bool     `json:"patch_must_be_nonempty"`
	PatchMustApply                     bool     `json:"patch_must_apply"`
	PatchTargetBoundaryRequired        bool     `json:"patch_target_boundary_required"`
	FailClosed                         bool     `json:"fail_closed"`
}

type VerifierCommandPolicyV1 struct {
	Authority             string            `json:"authority"`
	Execution             string            `json:"execution"`
	Shell                 bool              `json:"shell"`
	DisplayTextAuthority  bool              `json:"display_text_authority"`
	AgentCommandAuthority bool              `json:"agent_command_authority"`
	AllCriteriaBound      bool              `json:"all_criteria_bound"`
	EvidenceAvailability  string            `json:"evidence_availability"`
	Environment           map[string]string `json:"environment"`
}

type VerifierIsolationV1 struct {
	ReadOnlyRoot              bool     `json:"read_only_root"`
	Tmpfs                     []string `json:"tmpfs"`
	DropAllCapabilities       bool     `json:"drop_all_capabilities"`
	NoNewPrivileges           bool     `json:"no_new_privileges"`
	SeccompProfile            string   `json:"seccomp_profile"`
	Privileged                bool     `json:"privileged"`
	DeviceMounts              []string `json:"device_mounts"`
	DockerSocketMounted       bool     `json:"docker_socket_mounted"`
	UnrelatedHostPathsMounted bool     `json:"unrelated_host_paths_mounted"`
	FailClosed                bool     `json:"fail_closed"`
}

type VerifierNetworkV1 struct {
	Mode                 string `json:"mode"`
	GeneralEgress        bool   `json:"general_egress"`
	HostNetwork          bool   `json:"host_network"`
	PublishedPorts       bool   `json:"published_ports"`
	NormalBridgeFallback bool   `json:"normal_bridge_fallback"`
}

type VerifierCredentialsV1 struct {
	Injection              string   `json:"injection"`
	DeclaredNames          []string `json:"declared_names"`
	InheritHostEnvironment bool     `json:"inherit_host_environment"`
	MountSourceFiles       bool     `json:"mount_source_files"`
	Persist                bool     `json:"persist"`
	LogValues              bool     `json:"log_values"`
}

type VerifierResourcesV1 struct {
	CPUs                  int `json:"cpus"`
	MemoryMiB             int `json:"memory_mib"`
	MemorySwapMiB         int `json:"memory_swap_mib"`
	PIDsLimit             int `json:"pids_limit"`
	FileDescriptors       int `json:"file_descriptors"`
	CommandTimeoutSeconds int `json:"command_timeout_seconds"`
	AttemptTimeoutSeconds int `json:"attempt_timeout_seconds"`
}

type VerifierEvidencePolicyV1 struct {
	PersistedLogBytes             int64    `json:"persisted_log_bytes"`
	ArtifactBytes                 int64    `json:"artifact_bytes"`
	RedactionPolicyVersion        string   `json:"redaction_policy_version"`
	FullStreamSHA256Required      bool     `json:"full_stream_sha256_required"`
	TotalStreamBytesRequired      bool     `json:"total_stream_bytes_required"`
	RetainedRedactedBodyRequired  bool     `json:"retained_redacted_body_required"`
	TruncationFlagRequired        bool     `json:"truncation_flag_required"`
	TruncationBoundaryRequired    bool     `json:"truncation_boundary_required"`
	AllowedCheckStatuses          []string `json:"allowed_check_statuses"`
	AllowedCommandClassifications []string `json:"allowed_command_classifications"`
}

type VerifierTerminationV1 struct {
	DeathConfirmationSeconds int    `json:"death_confirmation_seconds"`
	ForceContainerRemoval    bool   `json:"force_container_removal"`
	RequireProcessTreeDeath  bool   `json:"require_process_tree_death"`
	Uncertainty              string `json:"uncertainty"`
}

type VerifierIdentityPolicyV1 struct {
	RequiredLabels            []string `json:"required_labels"`
	PolicyDigestRequired      bool     `json:"policy_digest_required"`
	WorkspaceIdentityRequired bool     `json:"workspace_identity_required"`
}

type VerifierCleanupPolicyV1 struct {
	RemoveContainer                        bool `json:"remove_container"`
	RemoveWorkspace                        bool `json:"remove_workspace"`
	RemoveAttemptTempAndCache              bool `json:"remove_attempt_temp_and_cache"`
	AllTerminalPaths                       bool `json:"all_terminal_paths"`
	ProveBeforeResult                      bool `json:"prove_before_result"`
	RecoveryEnumeratesLabeledResourcesOnly bool `json:"recovery_enumerates_labeled_resources_only"`
}

type VerifierResultAuthorityV1 struct {
	Owner                                string `json:"owner"`
	UniquePerRun                         bool   `json:"unique_per_run"`
	AtomicCommit                         bool   `json:"atomic_commit"`
	RequiresCompleteEvidence             bool   `json:"requires_complete_evidence"`
	RequiresCleanupProof                 bool   `json:"requires_cleanup_proof"`
	CancelCreatesResult                  bool   `json:"cancel_creates_result"`
	ProcessUncertaintyCreatesResult      bool   `json:"process_uncertainty_creates_result"`
	TrustedUnknownMayCreateNeedsRevision bool   `json:"trusted_unknown_may_create_needs_revision"`
}

func DecodeVerifierPolicyV1(data []byte) (VerifierPolicyV1, error) {
	var policy VerifierPolicyV1
	if err := decodeCandidatePolicy(data, &policy); err != nil {
		return VerifierPolicyV1{}, err
	}
	if err := validateVerifierPolicyV1(policy); err != nil {
		return VerifierPolicyV1{}, err
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != verifierPolicyDigestV1(policy.PolicyMode) {
		return VerifierPolicyV1{}, invalidCandidatePolicy("verifier policy digest")
	}
	return policy, nil
}

func verifierPolicyDigestV1(mode string) string {
	if mode == VerifierPolicyModeFullV1 {
		return VerifierPolicySHA256V1
	}
	if mode == VerifierPolicyModeUnknownV1 {
		return VerifierPolicyAcceptanceOnlySHA256V1
	}
	return ""
}

func validateVerifierPolicyV1(policy VerifierPolicyV1) error {
	if policy.SchemaVersion != VerifierPolicySchemaVersionV1 || verifierPolicyDigestV1(policy.PolicyMode) == "" {
		return invalidCandidatePolicy("verifier policy identity")
	}
	if policy.Provider != (VerifierProviderV1{Name: "colima-docker", ColimaVersion: "0.10.3", Engine: "docker", EngineVersion: "29.6.1"}) {
		return invalidCandidatePolicy("verifier provider")
	}
	if policy.Image.Reference != "chora/g2-m2e-pi-codex:0.84.2-v5" || policy.Image.ID != "sha256:91698efead5641a633519f5f229373e08a59264045ca27f6d01fc06505deeea7" || policy.Image.AgentRuntimeInvoked {
		return invalidCandidatePolicy("verifier image")
	}
	w := policy.Workspace
	if w.Lifecycle != "fresh_per_verification_attempt_removed_after_cleanup" || w.SourceMaterialization != "frozen_baseline_plus_final_patch" || w.Mount != "/workspace" || w.RepositoryPath != "/workspace" || w.WorkingDirectory != "/workspace" || w.User != "1000:1000" || !slicesEqual(w.AllowedWriteRoots, []string{"/workspace", "/tmp", "/workspace/.chora-cache", "/workspace/.chora-tmp"}) || w.ReuseAgentWorkspace || w.ReusePreviousVerificationWorkspace || w.LoadPiSession || w.LoadModelCredentials || w.LoadHostAgentState || w.LoadImplicitContext || w.LoadAgentPackages || w.LoadAgentExtensions || w.LoadAgentSkills || w.LoadAgentPrompts || !w.PatchDigestRequired || !w.PatchMustBeNonempty || !w.PatchMustApply || !w.PatchTargetBoundaryRequired || !w.FailClosed {
		return invalidCandidatePolicy("verifier workspace")
	}
	c := policy.Command
	availability := "execute_frozen_commands"
	if policy.PolicyMode == VerifierPolicyModeUnknownV1 {
		availability = "deterministically_unavailable_by_accepted_policy"
	}
	if c.Authority != "frozen_acceptance_verification_commands" || c.Execution != "direct_argv" || c.Shell || c.DisplayTextAuthority || c.AgentCommandAuthority || !c.AllCriteriaBound || c.EvidenceAvailability != availability {
		return invalidCandidatePolicy("verifier command authority")
	}
	wantEnvironment := map[string]string{"HOME": "/tmp", "GOCACHE": "/workspace/.chora-cache/go-build", "GOTMPDIR": "/workspace/.chora-tmp", "GOPROXY": "off", "GOSUMDB": "off", "GOTOOLCHAIN": "local"}
	if !equalStringMap(c.Environment, wantEnvironment) {
		return invalidCandidatePolicy("verifier command environment")
	}
	i := policy.Isolation
	if !i.ReadOnlyRoot || !slicesEqual(i.Tmpfs, []string{"/tmp"}) || !i.DropAllCapabilities || !i.NoNewPrivileges || i.SeccompProfile != "docker_default" || i.Privileged || len(i.DeviceMounts) != 0 || i.DockerSocketMounted || i.UnrelatedHostPathsMounted || !i.FailClosed {
		return invalidCandidatePolicy("verifier isolation")
	}
	n := policy.Network
	if n.Mode != "none" || n.GeneralEgress || n.HostNetwork || n.PublishedPorts || n.NormalBridgeFallback {
		return invalidCandidatePolicy("verifier network")
	}
	credentials := policy.Credentials
	if credentials.Injection != "none" || len(credentials.DeclaredNames) != 0 || credentials.InheritHostEnvironment || credentials.MountSourceFiles || credentials.Persist || credentials.LogValues {
		return invalidCandidatePolicy("verifier credentials")
	}
	r := policy.Resources
	if r.CPUs != 2 || r.MemoryMiB != 4096 || r.MemorySwapMiB != 4096 || r.PIDsLimit != 256 || r.FileDescriptors != 1024 || r.CommandTimeoutSeconds != 600 || r.AttemptTimeoutSeconds != 1200 {
		return invalidCandidatePolicy("verifier resources")
	}
	e := policy.Evidence
	if e.PersistedLogBytes != 10*1024*1024 || e.ArtifactBytes != 100*1024*1024 || e.RedactionPolicyVersion != "chora.verifier-redaction.v1" || !e.FullStreamSHA256Required || !e.TotalStreamBytesRequired || !e.RetainedRedactedBodyRequired || !e.TruncationFlagRequired || !e.TruncationBoundaryRequired || !slicesEqual(e.AllowedCheckStatuses, []string{"passed", "failed", "unknown"}) || !slicesEqual(e.AllowedCommandClassifications, []string{"exited", "timed_out", "cancelled", "launch_failed", "unavailable"}) {
		return invalidCandidatePolicy("verifier evidence")
	}
	t := policy.Termination
	if t.DeathConfirmationSeconds != 5 || !t.ForceContainerRemoval || !t.RequireProcessTreeDeath || t.Uncertainty != "verification_recovery_required" {
		return invalidCandidatePolicy("verifier termination")
	}
	wantLabels := []string{"chora.run_id", "chora.verification_run_id", "chora.verification_attempt_id", "chora.task_id", "chora.image_digest", "chora.verifier_policy_digest", "chora.baseline_digest", "chora.patch_digest", "chora.acceptance_contract_digest", "chora.workspace_id"}
	if !slicesEqual(policy.Identity.RequiredLabels, wantLabels) || !policy.Identity.PolicyDigestRequired || !policy.Identity.WorkspaceIdentityRequired {
		return invalidCandidatePolicy("verifier identity labels")
	}
	cleanup := policy.Cleanup
	if !cleanup.RemoveContainer || !cleanup.RemoveWorkspace || !cleanup.RemoveAttemptTempAndCache || !cleanup.AllTerminalPaths || !cleanup.ProveBeforeResult || !cleanup.RecoveryEnumeratesLabeledResourcesOnly {
		return invalidCandidatePolicy("verifier cleanup")
	}
	a := policy.ResultAuthority
	if a.Owner != "chora" || !a.UniquePerRun || !a.AtomicCommit || !a.RequiresCompleteEvidence || !a.RequiresCleanupProof || a.CancelCreatesResult || a.ProcessUncertaintyCreatesResult || !a.TrustedUnknownMayCreateNeedsRevision {
		return invalidCandidatePolicy("verifier result authority")
	}
	return nil
}
