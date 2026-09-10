package speccoding

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Yangyang96/chora/internal/agent"
)

func TestCoreContractFixtureFreezesTheM1RepositoryTask(t *testing.T) {
	data := readCoreContractFixture(t)
	contract, err := DecodeCoreContract(data)
	if err != nil {
		t.Fatalf("DecodeCoreContract() error = %v", err)
	}
	document := contract.Document()

	if document.SchemaVersion != CoreContractSchemaVersion || document.ContractID != "chora-m1-real-task-001" || document.Revision != 1 {
		t.Fatalf("unexpected contract identity: %#v", document)
	}
	if document.Task.Repository.Name != "chora" || document.Task.Repository.BaseRevision != "67b83d9ed8c4bdc17fa4d16da53c9943b255c222" {
		t.Fatalf("unexpected repository target: %#v", document.Task.Repository)
	}
	if document.Execution.Input.ContextSnapshotDigest != "80b62373d85c6d6676a935b2ba1487f8466530aa7fc5d506decf4839a27b336d" {
		t.Fatalf("unexpected context digest: %s", document.Execution.Input.ContextSnapshotDigest)
	}
	wantWrites := []string{"internal/domain/task.go", "internal/domain/task_test.go"}
	if !equalStrings(document.Execution.Boundary.WritableFiles, wantWrites) {
		t.Fatalf("writable files = %v, want %v", document.Execution.Boundary.WritableFiles, wantWrites)
	}
	if document.Execution.Output.ResultSchemaVersion != agent.ResultSchemaVersion {
		t.Fatalf("result schema = %q, decoder schema = %q", document.Execution.Output.ResultSchemaVersion, agent.ResultSchemaVersion)
	}
	if document.Candidate.Runtime.Package != "@earendil-works/pi-coding-agent" || document.Candidate.Runtime.Version != "0.84.1" || document.Candidate.Runtime.NPMIntegrity != "sha512-ncAqFrG+iybuPGOhMiZoEHkEzTpJgz3guYD32pD+M7ucc0WeHmauP6wa7qwP8V/KWvsZDVNa5XGsdZ7fkC7w7A==" {
		t.Fatalf("runtime candidate is not pinned: %#v", document.Candidate.Runtime)
	}
	wantTransport := []string{"pi", "--mode", "rpc", "--no-session"}
	if !equalStrings(document.Candidate.Runtime.Transport[:4], wantTransport) {
		t.Fatalf("runtime transport = %v, want prefix %v", document.Candidate.Runtime.Transport, wantTransport)
	}
	if document.Candidate.Sandbox.Version != "0.10.3" || document.Candidate.Sandbox.EngineVersion != "29.6.1" {
		t.Fatalf("sandbox candidate is not pinned: %#v", document.Candidate.Sandbox)
	}
	if len(document.Candidate.BaseImages) != 2 || document.Candidate.BaseImages[0].Digest != "sha256:4a4884e8a44826194dff92ba316264f392056cbe243dcc9fd3551e71cea02b90" || document.Candidate.BaseImages[1].Digest != "sha256:1ecb7edf62a0408027bd5729dfd6b1b8766e578e8df93995b225dfd0944eb651" {
		t.Fatalf("base images are not digest pinned: %#v", document.Candidate.BaseImages)
	}
	if document.Candidate.Model.Provider != "ollama" || document.Candidate.Model.ModelID != "minimax-m3:cloud" || document.Candidate.Model.ModelDigest != "sha256:8cd948b96f47afd232cef7d49faf65791d22bd9dbbef74add3b9355d9d75f765" || document.Candidate.Model.IdentityStatus != "RESOLVED" || document.Candidate.Model.AuthenticationType != "host_ollama_cloud_session" {
		t.Fatalf("model identity is not separate or pinned: %#v", document.Candidate.Model)
	}
	if len(document.Decisions) == 0 || len(document.Risks) == 0 || len(document.Unknowns) == 0 {
		t.Fatal("planning decisions, risks, and unknowns must stay explicit")
	}
	if len(contract.DigestHex()) != 64 || !bytes.Equal(contract.CanonicalJSON(), mustCanonicalJSON(t, document)) {
		t.Fatal("contract digest or canonical representation is unstable")
	}

	document.Requirement.Statement = "mutated"
	if contract.Document().Requirement.Statement == "mutated" {
		t.Fatal("Document() aliases immutable contract state")
	}
}

func TestCoreContractReferencesMatchFiles(t *testing.T) {
	contract, err := DecodeCoreContract(readCoreContractFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	document := contract.Document()
	assertReferenceDigest(t, document.Candidate.Runtime.Config)
	assertReferenceDigest(t, document.Candidate.Policy)
}

func TestPiRuntimeConfigurationDisablesUndeclaredState(t *testing.T) {
	var config piRuntimeConfigFixture
	decodeContractFile(t, "contracts/g2-m1a/pi-runtime-config.v1.json", &config)

	wantTransport := []string{
		"pi", "--mode", "rpc", "--no-session", "--no-extensions", "--no-skills",
		"--no-prompt-templates", "--no-themes", "--no-context-files", "--no-approve",
		"--provider", "ollama", "--model", "minimax-m3:cloud",
	}
	if !equalStrings(config.Runtime.Transport, wantTransport) {
		t.Fatalf("Pi transport = %v, want %v", config.Runtime.Transport, wantTransport)
	}
	if config.Environment["PI_CODING_AGENT_DIR"] != "/run/chora/pi" || config.Environment["PI_OFFLINE"] != "1" || config.Environment["PI_SKIP_VERSION_CHECK"] != "1" || config.Environment["PI_TELEMETRY"] != "0" {
		t.Fatalf("unsafe Pi environment: %#v", config.Environment)
	}
	if config.AgentDirectory.Lifecycle != "new_empty_directory_per_attempt" || config.AgentDirectory.InheritHostState || config.AgentDirectory.RetainAfterAttempt {
		t.Fatalf("unsafe Pi agent directory: %#v", config.AgentDirectory)
	}
	for name, enabled := range config.Resources {
		if enabled {
			t.Fatalf("Pi resource %q must be disabled", name)
		}
	}
	provider := config.ModelConfig.Providers["ollama"]
	if provider.BaseURL != "http://host.docker.internal:11434/v1" || provider.API != "openai-completions" || len(provider.Models) != 1 || provider.Models[0].ID != "minimax-m3:cloud" || !provider.Models[0].Reasoning || provider.Models[0].ContextWindow != 524288 || provider.Models[0].MaxTokens != 8192 {
		t.Fatalf("unexpected Ollama model configuration: %#v", provider)
	}
	if config.Authentication.Type != "host_ollama_cloud_session" || len(config.Authentication.CredentialEnvironmentNames) != 0 || config.Authentication.ContainerReceivesCloudCredential || !config.Authentication.HostDaemonOwnsCloudSession {
		t.Fatalf("unsafe model authentication boundary: %#v", config.Authentication)
	}
}

func TestSandboxPolicyFailsClosedAndOwnsAttemptLifecycle(t *testing.T) {
	var policy sandboxPolicyFixture
	decodeContractFile(t, "contracts/g2-m1a/sandbox-policy.v1.json", &policy)

	if policy.Attempt.ContainerLifecycle != "one_disposable_container_per_attempt" || policy.Attempt.WorkspaceLifecycle != "one_disposable_execution_workspace_per_attempt" || policy.Attempt.WorkspaceMount != "/workspace" || len(policy.Attempt.OtherHostMounts) != 0 || policy.Attempt.PiAgentDirectory != "/run/chora/pi" {
		t.Fatalf("unsafe attempt lifecycle: %#v", policy.Attempt)
	}
	if !policy.Isolation.ReadOnlyRoot || !policy.Isolation.DropAllCapabilities || !policy.Isolation.NoNewPrivileges || policy.Isolation.PublishPorts || policy.Isolation.DockerSocketMounted || policy.Isolation.UnrelatedHostPathsMounted || !policy.Isolation.FailClosed {
		t.Fatalf("unsafe isolation policy: %#v", policy.Isolation)
	}
	if policy.Network.Mode != "outbound_enabled_no_inbound" || policy.Network.DeclaredModelEndpoint != "http://host.docker.internal:11434/v1" {
		t.Fatalf("unexpected network policy: %#v", policy.Network)
	}
	if policy.Resources.CPUs != 2 || policy.Resources.MemoryMB != 4096 || policy.Resources.PIDsLimit != 256 || policy.Resources.TimeoutSeconds != 900 {
		t.Fatalf("unexpected resource limits: %#v", policy.Resources)
	}
	if policy.Credentials.Injection != "task_scoped_environment_only" || len(policy.Credentials.DeclaredNames) != 0 || policy.Credentials.Persist || policy.Credentials.LogValues {
		t.Fatalf("unsafe credential policy: %#v", policy.Credentials)
	}
	if !policy.Termination.RPCAbortIsCooperativeOnly || policy.Termination.StrictCancel != "docker kill then docker rm force" || policy.Termination.Timeout != "docker kill then docker rm force" || !policy.Termination.RequireProcessTreeDeathConfirmation || !policy.Termination.RemoveOwnedWorkspace || policy.Termination.Uncertainty != "fail_closed" {
		t.Fatalf("unsafe termination policy: %#v", policy.Termination)
	}
}

func TestCoreContractRejectsIncompleteOrUnsafeG2M1ABoundaries(t *testing.T) {
	contract, err := DecodeCoreContract(readCoreContractFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*CoreContractDocument)
	}{
		{"requirement", func(document *CoreContractDocument) { document.Requirement.Statement = " " }},
		{"spec", func(document *CoreContractDocument) { document.Spec.DesiredBehavior = nil }},
		{"acceptance", func(document *CoreContractDocument) { document.Acceptance.Criteria = nil }},
		{"technical plan", func(document *CoreContractDocument) { document.TechnicalPlan.Steps = nil }},
		{"decision", func(document *CoreContractDocument) { document.Decisions = nil }},
		{"risk", func(document *CoreContractDocument) { document.Risks = nil }},
		{"unknown", func(document *CoreContractDocument) { document.Unknowns = nil }},
		{"context snapshot", func(document *CoreContractDocument) { document.Execution.Input.ContextSnapshotDigest = "" }},
		{"unsafe write path", func(document *CoreContractDocument) { document.Execution.Boundary.WritableFiles[0] = "../host" }},
		{"command boundary", func(document *CoreContractDocument) { document.Execution.Boundary.Commands = nil }},
		{"test boundary", func(document *CoreContractDocument) { document.Execution.Boundary.TestCommandIDs = nil }},
		{"result schema", func(document *CoreContractDocument) { document.Execution.Output.ResultSchemaVersion = "unknown" }},
		{"capability", func(document *CoreContractDocument) {
			document.Execution.RequiredCapabilities = document.Execution.RequiredCapabilities[1:]
		}},
		{"runtime identity", func(document *CoreContractDocument) { document.Candidate.Runtime.NPMIntegrity = "tag-only" }},
		{"sandbox identity", func(document *CoreContractDocument) { document.Candidate.Sandbox.SHA256 = "" }},
		{"image identity", func(document *CoreContractDocument) { document.Candidate.BaseImages[0].Digest = "tag-only" }},
		{"model identity", func(document *CoreContractDocument) { document.Candidate.Model.ModelID = "" }},
		{"model resolution status", func(document *CoreContractDocument) { document.Candidate.Model.IdentityStatus = "" }},
		{"runtime config", func(document *CoreContractDocument) { document.Candidate.Runtime.Config.SHA256 = "" }},
		{"resource policy", func(document *CoreContractDocument) { document.Candidate.Policy.SHA256 = "" }},
		{"probe pass", func(document *CoreContractDocument) { document.EntryProbe.PassCriteria = nil }},
		{"probe failure", func(document *CoreContractDocument) { document.EntryProbe.FailureClasses = nil }},
		{"probe stop", func(document *CoreContractDocument) { document.EntryProbe.StopCriteria = nil }},
		{"capsule invalidation", func(document *CoreContractDocument) { document.EntryProbe.CapsuleChangeBehavior = "KEEP" }},
		{"fallback authority", func(document *CoreContractDocument) {
			document.EntryProbe.FallbackCandidateRequiresUserApproval = false
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := contract.Document()
			test.mutate(&document)
			if _, err := NewCoreContract(document); err == nil {
				t.Fatal("NewCoreContract() accepted incomplete or unsafe contract")
			}
		})
	}
}

func TestDecodeCoreContractRejectsUnknownFieldsAndTrailingDocuments(t *testing.T) {
	data := readCoreContractFixture(t)
	unknown := bytes.Replace(data, []byte(`"revision": 1,`), []byte(`"revision": 1, "surprise": true,`), 1)
	if _, err := DecodeCoreContract(unknown); err == nil {
		t.Fatal("DecodeCoreContract() accepted unknown field")
	}
	if _, err := DecodeCoreContract(append(data, []byte(`{}`)...)); err == nil {
		t.Fatal("DecodeCoreContract() accepted trailing document")
	}
}

func readCoreContractFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "contracts", "g2-m1a", "chora-m1-real-task.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func mustCanonicalJSON(t *testing.T, document CoreContractDocument) []byte {
	t.Helper()
	contract, err := NewCoreContract(document)
	if err != nil {
		t.Fatal(err)
	}
	return contract.CanonicalJSON()
}

func assertReferenceDigest(t *testing.T, reference ConfigReference) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", reference.Path))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	if got := hex.EncodeToString(digest[:]); got != reference.SHA256 {
		t.Fatalf("%s digest = %s, want %s", reference.Path, got, reference.SHA256)
	}
}

func decodeContractFile(t *testing.T, name string, destination any) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", name))
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		t.Fatal(err)
	}
}

func equalStrings(left, right []string) bool {
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

type piRuntimeConfigFixture struct {
	SchemaVersion string `json:"schema_version"`
	Runtime       struct {
		Package      string   `json:"package"`
		Version      string   `json:"version"`
		NPMIntegrity string   `json:"npm_integrity"`
		Transport    []string `json:"transport"`
	} `json:"runtime"`
	Environment    map[string]string `json:"environment"`
	AgentDirectory struct {
		Lifecycle             string   `json:"lifecycle"`
		AllowedGeneratedFiles []string `json:"allowed_generated_files"`
		InheritHostState      bool     `json:"inherit_host_state"`
		RetainAfterAttempt    bool     `json:"retain_after_attempt"`
	} `json:"agent_directory"`
	Resources   map[string]bool `json:"resources"`
	ModelConfig struct {
		Providers map[string]struct {
			BaseURL string `json:"baseUrl"`
			API     string `json:"api"`
			APIKey  string `json:"apiKey"`
			Compat  struct {
				SupportsDeveloperRole   bool `json:"supportsDeveloperRole"`
				SupportsReasoningEffort bool `json:"supportsReasoningEffort"`
			} `json:"compat"`
			Models []struct {
				ID            string `json:"id"`
				Name          string `json:"name"`
				Reasoning     bool   `json:"reasoning"`
				ContextWindow int    `json:"contextWindow"`
				MaxTokens     int    `json:"maxTokens"`
			} `json:"models"`
		} `json:"providers"`
	} `json:"model_config"`
	Authentication struct {
		Type                             string   `json:"type"`
		CredentialEnvironmentNames       []string `json:"credential_environment_names"`
		DummyProviderKeyIsSecret         bool     `json:"dummy_provider_key_is_secret"`
		ContainerReceivesCloudCredential bool     `json:"container_receives_cloud_credential"`
		HostDaemonOwnsCloudSession       bool     `json:"host_daemon_owns_cloud_session"`
	} `json:"authentication"`
	PreProbeUnknowns struct {
		ModelPresentInOllama       bool   `json:"model_present_in_ollama"`
		ModelDigest                string `json:"model_digest"`
		ContextWindow              int    `json:"context_window"`
		ProviderMaxTokens          *int   `json:"provider_max_tokens"`
		ConfiguredMaxTokens        int    `json:"configured_max_tokens"`
		ContainerEndpointReachable *bool  `json:"container_endpoint_reachable"`
		PiToolCallingCompatible    *bool  `json:"pi_tool_calling_compatible"`
		ResolutionGate             string `json:"resolution_gate"`
	} `json:"pre_probe_unknowns"`
}

type sandboxPolicyFixture struct {
	SchemaVersion string `json:"schema_version"`
	Provider      struct {
		Name                string `json:"name"`
		ColimaVersion       string `json:"colima_version"`
		ColimaSHA256        string `json:"colima_sha256"`
		Engine              string `json:"engine"`
		EngineClientVersion string `json:"engine_client_version"`
		EngineClientSHA256  string `json:"engine_client_sha256"`
	} `json:"provider"`
	Attempt struct {
		ContainerLifecycle string   `json:"container_lifecycle"`
		WorkspaceLifecycle string   `json:"workspace_lifecycle"`
		WorkspaceMount     string   `json:"workspace_mount"`
		OtherHostMounts    []string `json:"other_host_mounts"`
		PiAgentDirectory   string   `json:"pi_agent_directory"`
		WorkingDirectory   string   `json:"working_directory"`
		User               string   `json:"user"`
	} `json:"attempt"`
	Isolation struct {
		ReadOnlyRoot              bool     `json:"read_only_root"`
		Tmpfs                     []string `json:"tmpfs"`
		DropAllCapabilities       bool     `json:"drop_all_capabilities"`
		NoNewPrivileges           bool     `json:"no_new_privileges"`
		PublishPorts              bool     `json:"publish_ports"`
		DockerSocketMounted       bool     `json:"docker_socket_mounted"`
		UnrelatedHostPathsMounted bool     `json:"unrelated_host_paths_mounted"`
		FailClosed                bool     `json:"fail_closed"`
	} `json:"isolation"`
	Network struct {
		Mode                  string `json:"mode"`
		DeclaredModelEndpoint string `json:"declared_model_endpoint"`
		DefaultBehavior       string `json:"default_behavior"`
	} `json:"network"`
	Resources struct {
		CPUs           int `json:"cpus"`
		MemoryMB       int `json:"memory_mb"`
		PIDsLimit      int `json:"pids_limit"`
		TimeoutSeconds int `json:"timeout_seconds"`
	} `json:"resources"`
	Credentials struct {
		Injection     string   `json:"injection"`
		DeclaredNames []string `json:"declared_names"`
		Persist       bool     `json:"persist"`
		LogValues     bool     `json:"log_values"`
	} `json:"credentials"`
	Termination struct {
		RPCAbortIsCooperativeOnly           bool   `json:"rpc_abort_is_cooperative_only"`
		StrictCancel                        string `json:"strict_cancel"`
		Timeout                             string `json:"timeout"`
		AbnormalExit                        string `json:"abnormal_exit"`
		RequireProcessTreeDeathConfirmation bool   `json:"require_process_tree_death_confirmation"`
		RemoveOwnedWorkspace                bool   `json:"remove_owned_workspace"`
		Uncertainty                         string `json:"uncertainty"`
	} `json:"termination"`
}
