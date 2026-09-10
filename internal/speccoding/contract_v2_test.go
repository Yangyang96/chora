package speccoding

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestV2CoreContractPinsAcceptedCandidatePolicy(t *testing.T) {
	contract, err := DecodeCoreContract(readV2ContractFixture(t, "chora-m1-real-task.v2.json"))
	if err != nil {
		t.Fatal(err)
	}
	document := contract.Document()
	if document.SchemaVersion != CoreContractSchemaVersionV2 || document.Revision != 2 {
		t.Fatalf("v2 identity = %q revision %d", document.SchemaVersion, document.Revision)
	}
	if document.Candidate.Runtime.Config.Path != "contracts/g2-m1a/pi-runtime-config.v2.json" || document.Candidate.Policy.Path != "contracts/g2-m1a/sandbox-policy.v2.json" {
		t.Fatalf("v2 references = %#v %#v", document.Candidate.Runtime.Config, document.Candidate.Policy)
	}
	if document.Candidate.Model.ModelID != "minimax-m3:cloud" || document.Candidate.Model.DependencyScope != "replaceable_task_dependency" || document.Candidate.Model.ProductInterfaceExposure != "none" {
		t.Fatalf("model leaked into product contract: %#v", document.Candidate.Model)
	}
	assertReferenceDigest(t, document.Candidate.Runtime.Config)
	assertReferenceDigest(t, document.Candidate.Policy)
	if !containsV2String(document.EntryProbe.FailureClasses, "ENVIRONMENT_FAILURE") || containsV2String(document.EntryProbe.FailureClasses, "CANDIDATE_OR_ENV_FAILURE") {
		t.Fatalf("failure classes conflate candidate and environment: %#v", document.EntryProbe.FailureClasses)
	}
}

func TestV2ContractsRejectValidDigestSubstitution(t *testing.T) {
	const validReplacement = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	core := readV2ContractFixture(t, "chora-m1-real-task.v2.json")
	for _, test := range []struct {
		name string
		from string
		to   string
	}{
		{"runtime config", "bd9aa96442eecb57b7b1fa32c4153ebe5e30f9a9541bab0964320b7b82e93aee", validReplacement},
		{"sandbox policy", "dbec78436e8700dd457cd1c5f0cfd4041123ed229562ea495cc1c0028a270ccb", validReplacement},
		{"colima binary", "55278419bd4288e4ab11eb30be13f5e655f3886c0979b68e435df910dbc9bbdf", validReplacement},
		{"docker client", "e8a1e5351c4d12337a4ee2b54523bc0107b4d13f795c9d6e791b9e4cf835f385", validReplacement},
		{"node image", "sha256:4a4884e8a44826194dff92ba316264f392056cbe243dcc9fd3551e71cea02b90", "sha256:" + validReplacement},
		{"go image", "sha256:1ecb7edf62a0408027bd5729dfd6b1b8766e578e8df93995b225dfd0944eb651", "sha256:" + validReplacement},
	} {
		t.Run("core "+test.name, func(t *testing.T) {
			mutated := bytes.Replace(core, []byte(test.from), []byte(test.to), 1)
			if bytes.Equal(mutated, core) {
				t.Fatal("fixture pin not found")
			}
			if _, err := DecodeCoreContract(mutated); err == nil {
				t.Fatal("valid but unaccepted digest substitution was accepted")
			}
		})
	}

	policy := readV2ContractFixture(t, "sandbox-policy.v2.json")
	for _, pin := range []string{
		"55278419bd4288e4ab11eb30be13f5e655f3886c0979b68e435df910dbc9bbdf",
		"e8a1e5351c4d12337a4ee2b54523bc0107b4d13f795c9d6e791b9e4cf835f385",
	} {
		mutated := bytes.Replace(policy, []byte(pin), []byte(validReplacement), 1)
		if _, err := DecodeSandboxPolicyV2(mutated); err == nil {
			t.Fatal("valid but unaccepted provider digest substitution was accepted")
		}
	}
}

func TestV2PiRuntimeConfigHasZeroImplicitInputAndChoraOwnedResult(t *testing.T) {
	config, err := DecodePiRuntimeConfigV2(readV2ContractFixture(t, "pi-runtime-config.v2.json"))
	if err != nil {
		t.Fatal(err)
	}
	if config.Runtime.Version != "0.84.1" || !equalStrings(config.Runtime.Transport[:4], []string{"pi", "--mode", "rpc", "--no-session"}) {
		t.Fatalf("runtime = %#v", config.Runtime)
	}
	if config.Context.ExplicitSource != "digest_bound_context_snapshot_only" || config.Context.RepositoryChild != "/workspace/repository" || config.Context.InheritHostPiState || config.Context.LoadProjectInstructions {
		t.Fatalf("implicit context enabled: %#v", config.Context)
	}
	for name, enabled := range config.Resources {
		if enabled {
			t.Fatalf("undeclared Pi resource %q enabled", name)
		}
	}
	wantEvents := []string{"agent_start", "agent_end", "tool_start", "tool_end", "file_path", "command_summary", "exit_code", "error", "model_identity", "usage"}
	if !equalStrings(config.EventProjection.AllowedEvents, wantEvents) || config.EventProjection.PersistThinking || config.EventProjection.PersistRawPrompt || config.EventProjection.PersistCredentialValues {
		t.Fatalf("unsafe event projection: %#v", config.EventProjection)
	}
	if config.ResultAuthority.Owner != "chora" || config.ResultAuthority.TerminalResultCount != 1 || config.ResultAuthority.PiAgentEndMeansSuccess || config.ResultAuthority.PiFinalTextRole != "agent_summary_only" || !config.ResultAuthority.RequiresSandboxReconciliation {
		t.Fatalf("unsafe result authority: %#v", config.ResultAuthority)
	}
	if config.ModelDependency.Scope != "replaceable_task_dependency" || config.ModelDependency.ProductInterfaceExposure != "none" || config.ModelDependency.ModelID != "minimax-m3:cloud" {
		t.Fatalf("unsafe model dependency: %#v", config.ModelDependency)
	}
}

func TestV2SandboxPolicyEnforcesNetworkLifecycleAndResourceLimits(t *testing.T) {
	policy, err := DecodeSandboxPolicyV2(readV2ContractFixture(t, "sandbox-policy.v2.json"))
	if err != nil {
		t.Fatal(err)
	}
	if policy.Network.Mode != "attempt_private_internal_with_fixed_ollama_upstream" || !policy.Network.AttemptNetworkInternal || policy.Network.GeneralEgress || policy.Network.HostNetwork || policy.Network.PublishedPorts || policy.Network.DockerSocketMounted || policy.Network.NormalBridgeFallback {
		t.Fatalf("unsafe network policy: %#v", policy.Network)
	}
	if policy.Network.Upstream.Kind != "task_scoped_fixed_upstream_boundary" || policy.Network.Upstream.AllowedHost != "host.docker.internal" || policy.Network.Upstream.AllowedPort != 11434 || policy.Network.Upstream.Endpoint != "http://ollama-boundary:11434/v1" || !policy.Network.Upstream.Traceable {
		t.Fatalf("unsafe upstream boundary: %#v", policy.Network.Upstream)
	}
	if policy.Resources.CommandTimeoutSeconds != 600 || policy.Resources.AttemptTimeoutSeconds != 1200 || policy.Resources.CPUs != 2 || policy.Resources.MemoryMiB != 4096 || policy.Resources.MemorySwapMiB != 4096 || policy.Resources.PIDsLimit != 256 || policy.Resources.FileDescriptors != 1024 {
		t.Fatalf("resource limits = %#v", policy.Resources)
	}
	if policy.Termination.CooperativeAbortWaitSeconds != 2 || policy.Termination.DeathConfirmationSeconds != 5 || !policy.Termination.ForceContainerRemoval || !policy.Termination.RequireProcessTreeDeath || policy.Termination.Uncertainty != "recovery_required_fail_closed" {
		t.Fatalf("termination = %#v", policy.Termination)
	}
	if policy.Limits.PersistedLogBytes != 10*1024*1024 || policy.Limits.ArtifactBytes != 100*1024*1024 {
		t.Fatalf("output limits = %#v", policy.Limits)
	}
	if policy.Attempt.OriginalRepositoryWritableMounted || policy.Attempt.ReusePreviousAttempt || policy.Attempt.SourceMaterialization != "fresh_copy_from_frozen_revision_plus_selected_dirty_input" || policy.Attempt.RepositoryPath != "/workspace/repository" || !policy.Attempt.ContextReadOnly {
		t.Fatalf("unsafe workspace policy: %#v", policy.Attempt)
	}
	if policy.Isolation.HostNetwork || policy.Isolation.Privileged || policy.Isolation.DockerSocketMounted || policy.Isolation.PublishPorts || policy.Isolation.NormalBridgeFallback || !policy.Isolation.ReadOnlyRoot || !policy.Isolation.DropAllCapabilities || !policy.Isolation.NoNewPrivileges || policy.Isolation.SeccompProfile != "docker_default" {
		t.Fatalf("unsafe isolation policy: %#v", policy.Isolation)
	}
}

func TestV2ContractsRejectEveryAcceptedHardBoundaryRelaxation(t *testing.T) {
	piFixture := readV2ContractFixture(t, "pi-runtime-config.v2.json")
	for _, test := range []struct {
		name string
		path []string
		set  any
	}{
		{"host pi state", []string{"context", "inherit_host_pi_state"}, true},
		{"project instructions", []string{"context", "load_project_instructions"}, true},
		{"third party package", []string{"resources", "third_party_packages"}, true},
		{"thinking persistence", []string{"event_projection", "persist_thinking"}, true},
		{"pi result authority", []string{"result_authority", "owner"}, "pi"},
		{"multiple results", []string{"result_authority", "terminal_result_count"}, float64(2)},
		{"model in product interface", []string{"model_dependency", "product_interface_exposure"}, "runtime_status"},
	} {
		t.Run("pi "+test.name, func(t *testing.T) {
			if _, err := DecodePiRuntimeConfigV2(mutateV2JSON(t, piFixture, test.path, test.set)); err == nil {
				t.Fatal("unsafe Pi configuration accepted")
			}
		})
	}

	policyFixture := readV2ContractFixture(t, "sandbox-policy.v2.json")
	for _, test := range []struct {
		name string
		path []string
		set  any
	}{
		{"general egress", []string{"network", "general_egress"}, true},
		{"non-internal attempt network", []string{"network", "attempt_network_internal"}, false},
		{"host network", []string{"network", "host_network"}, true},
		{"published port", []string{"network", "published_ports"}, true},
		{"docker socket", []string{"network", "docker_socket_mounted"}, true},
		{"bridge fallback", []string{"network", "normal_bridge_fallback"}, true},
		{"wrong upstream port", []string{"network", "upstream", "allowed_port"}, float64(11435)},
		{"command timeout", []string{"resources", "command_timeout_seconds"}, float64(601)},
		{"attempt timeout", []string{"resources", "attempt_timeout_seconds"}, float64(1201)},
		{"extra swap", []string{"resources", "memory_swap_mib"}, float64(8192)},
		{"abort wait", []string{"termination", "cooperative_abort_wait_seconds"}, float64(3)},
		{"death wait", []string{"termination", "death_confirmation_seconds"}, float64(6)},
		{"writable original", []string{"attempt", "original_repository_writable_mounted"}, true},
		{"reused attempt", []string{"attempt", "reuse_previous_attempt"}, true},
		{"oversize log", []string{"limits", "persisted_log_bytes"}, float64(10*1024*1024 + 1)},
		{"oversize artifact", []string{"limits", "artifact_bytes"}, float64(100*1024*1024 + 1)},
	} {
		t.Run("sandbox "+test.name, func(t *testing.T) {
			if _, err := DecodeSandboxPolicyV2(mutateV2JSON(t, policyFixture, test.path, test.set)); err == nil {
				t.Fatal("unsafe Sandbox policy accepted")
			}
		})
	}
}

func readV2ContractFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "contracts", "g2-m1a", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func mutateV2JSON(t *testing.T, data []byte, path []string, value any) []byte {
	t.Helper()
	var document map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		t.Fatal(err)
	}
	current := document
	for _, key := range path[:len(path)-1] {
		next, ok := current[key].(map[string]any)
		if !ok {
			t.Fatalf("%v is not an object", path)
		}
		current = next
	}
	current[path[len(path)-1]] = value
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func containsV2String(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
