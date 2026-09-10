package speccoding

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestV3CoreContractPinsOpenAICodexAmendment(t *testing.T) {
	contract, err := DecodeCoreContract(readV3ContractFixture(t, "chora-m1-real-task.v3.json"))
	if err != nil {
		t.Fatal(err)
	}
	document := contract.Document()
	if document.SchemaVersion != CoreContractSchemaVersionV3 || document.Revision != 3 {
		t.Fatalf("v3 identity = %q revision %d", document.SchemaVersion, document.Revision)
	}
	if document.Candidate.Runtime.Config.Path != "contracts/g2-m1a/pi-runtime-config.v3.json" || document.Candidate.Policy.Path != "contracts/g2-m1a/sandbox-policy.v3.json" {
		t.Fatalf("v3 references = %#v %#v", document.Candidate.Runtime.Config, document.Candidate.Policy)
	}
	model := document.Candidate.Model
	if model.Provider != "openai-codex" || model.ModelID != "gpt-5.6-sol" || model.Endpoint != "https://chatgpt.com/backend-api/codex/responses" || model.AuthenticationType != "openai_codex_oauth" || model.DependencyScope != "replaceable_task_dependency" || model.ProductInterfaceExposure != "none" {
		t.Fatalf("v3 model boundary = %#v", model)
	}
	for _, capability := range []string{"sandbox.openai_codex_only_network", "sandbox.explicit_oauth_projection", "sandbox.no_host_pi_state"} {
		if !containsV2String(document.Execution.RequiredCapabilities, capability) {
			t.Fatalf("missing v3 capability %q", capability)
		}
	}
}

func TestV3PiRuntimeConfigProjectsOnlyTaskScopedOAuth(t *testing.T) {
	config, err := DecodePiRuntimeConfigV3(readV3ContractFixture(t, "pi-runtime-config.v3.json"))
	if err != nil {
		t.Fatal(err)
	}
	wantTransport := []string{"pi", "--mode", "rpc", "--no-session", "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-themes", "--no-context-files", "--no-approve", "--provider", "openai-codex", "--model", "gpt-5.6-sol"}
	if !equalV3Strings(config.Runtime.Transport, wantTransport) {
		t.Fatalf("v3 transport = %#v", config.Runtime.Transport)
	}
	if config.Model.Provider != "openai-codex" || config.Model.API != "openai-codex-responses" || config.Model.BaseURL != "https://chatgpt.com/backend-api" || config.Model.ID != "gpt-5.6-sol" || config.Model.CatalogSHA256 != "4a73818291987693fcb53e9e61b4be3429ffd78b3f64ea877a34c5f9928c89e1" {
		t.Fatalf("v3 model identity = %#v", config.Model)
	}
	auth := config.Authentication
	if auth.Type != "openai_codex_oauth" || auth.SourceEnvironmentName != "CHORA_PI_CODEX_AUTH_FILE" || !auth.SourceDefaultForbidden || auth.TaskProjection != "openai-codex_entry_only" || auth.CredentialFile != "/run/chora/pi/auth.json" || auth.CredentialFileMode != "0600" || auth.PersistCredentialValues || auth.LogCredentialValues || auth.CopyBackToHost {
		t.Fatalf("unsafe v3 auth = %#v", auth)
	}
	if !equalV3Strings(config.AgentDirectory.AllowedGeneratedFiles, []string{"auth.json"}) || config.AgentDirectory.InheritHostState || config.AgentDirectory.RetainAfterAttempt {
		t.Fatalf("unsafe v3 agent directory = %#v", config.AgentDirectory)
	}
	for key, value := range config.Environment {
		if key == "OPENAI_API_KEY" || key == "CODEX_HOME" || bytes.Contains(bytes.ToLower([]byte(value)), []byte("bearer")) {
			t.Fatalf("credential leaked into environment %s", key)
		}
	}
	if config.Environment["HTTPS_PROXY"] != "http://codex-boundary:8080" || config.Environment["HTTP_PROXY"] != "http://codex-boundary:8080" {
		t.Fatalf("proxy environment = %#v", config.Environment)
	}
	if config.EventProjection.PersistThinking || config.EventProjection.PersistRawPrompt || config.EventProjection.PersistCredentialValues || config.ResultAuthority.Owner != "chora" || config.ResultAuthority.TerminalResultCount != 1 {
		t.Fatalf("unsafe v3 projection/result = %#v %#v", config.EventProjection, config.ResultAuthority)
	}
}

func TestV3SandboxPolicyHasFixedCodexProxyAndCredentialCleanup(t *testing.T) {
	policy, err := DecodeSandboxPolicyV3(readV3ContractFixture(t, "sandbox-policy.v3.json"))
	if err != nil {
		t.Fatal(err)
	}
	network := policy.Network
	if network.Mode != "attempt_private_internal_with_fixed_codex_proxy" || !network.AttemptNetworkInternal || network.GeneralEgress || network.HostNetwork || network.PublishedPorts || network.DockerSocketMounted || network.NormalBridgeFallback || network.Proxy.Endpoint != "http://codex-boundary:8080" || network.Proxy.Protocol != "http_connect" || !equalV3Strings(network.Proxy.AllowedHosts, []string{"auth.openai.com", "chatgpt.com"}) || !equalV3Ints(network.Proxy.AllowedPorts, []int{443}) || !network.Proxy.DenyIPLiteral || !network.Proxy.ResolveDNSAtBoundary || network.Proxy.TLSTermination || !network.Proxy.TaskScoped || !network.Proxy.Traceable {
		t.Fatalf("unsafe v3 network = %#v", network)
	}
	credentials := policy.Credentials
	if credentials.Injection != "task_scoped_explicit_file_projection" || !equalV3Strings(credentials.DeclaredNames, []string{"CHORA_PI_CODEX_AUTH_FILE"}) || credentials.Persist || credentials.LogValues || credentials.MountSourceFile || !credentials.RemoveAttemptCopy || credentials.RefreshFailure != "AUTH_EXPIRED_FAIL_CLOSED" {
		t.Fatalf("unsafe v3 credentials = %#v", credentials)
	}
	if policy.Attempt.OtherHostMounts == nil || len(policy.Attempt.OtherHostMounts) != 0 || policy.Attempt.ReusePreviousAttempt || policy.Attempt.OriginalRepositoryWritableMounted {
		t.Fatalf("unsafe v3 Attempt = %#v", policy.Attempt)
	}
}

func TestV3ContractsRejectCredentialAndEgressRelaxation(t *testing.T) {
	piFixture := readV3ContractFixture(t, "pi-runtime-config.v3.json")
	for _, test := range []struct {
		name string
		path []string
		set  any
	}{
		{"host Pi state", []string{"agent_directory", "inherit_host_state"}, true},
		{"credential copyback", []string{"authentication", "copy_back_to_host"}, true},
		{"credential persistence", []string{"authentication", "persist_credential_values"}, true},
		{"wrong provider", []string{"model", "provider"}, "openai"},
		{"wrong model", []string{"model", "id"}, "gpt-5.6-luna"},
		{"direct model endpoint", []string{"environment", "HTTPS_PROXY"}, ""},
	} {
		t.Run("pi "+test.name, func(t *testing.T) {
			if _, err := DecodePiRuntimeConfigV3(mutateV3JSON(t, piFixture, test.path, test.set)); err == nil {
				t.Fatal("unsafe v3 Pi config accepted")
			}
		})
	}

	policyFixture := readV3ContractFixture(t, "sandbox-policy.v3.json")
	for _, test := range []struct {
		name string
		path []string
		set  any
	}{
		{"general egress", []string{"network", "general_egress"}, true},
		{"extra host", []string{"network", "proxy", "allowed_hosts"}, []string{"auth.openai.com", "chatgpt.com", "example.com"}},
		{"IP literals", []string{"network", "proxy", "deny_ip_literal"}, false},
		{"TLS termination", []string{"network", "proxy", "tls_termination"}, true},
		{"source mount", []string{"credentials", "mount_source_file"}, true},
		{"retained credential", []string{"credentials", "remove_attempt_copy"}, false},
	} {
		t.Run("sandbox "+test.name, func(t *testing.T) {
			if _, err := DecodeSandboxPolicyV3(mutateV3JSON(t, policyFixture, test.path, test.set)); err == nil {
				t.Fatal("unsafe v3 Sandbox policy accepted")
			}
		})
	}
}

func TestV3CoreReferencesExactCandidateFiles(t *testing.T) {
	contract, err := DecodeCoreContract(readV3ContractFixture(t, "chora-m1-real-task.v3.json"))
	if err != nil {
		t.Fatal(err)
	}
	document := contract.Document()
	for _, reference := range []ConfigReference{document.Candidate.Runtime.Config, document.Candidate.Policy} {
		data, err := os.ReadFile(filepath.Join("..", "..", reference.Path))
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(data)
		if hex.EncodeToString(digest[:]) != reference.SHA256 {
			t.Fatalf("reference digest drift for %s", reference.Path)
		}
	}
}

func readV3ContractFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "contracts", "g2-m1a", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func mutateV3JSON(t *testing.T, data []byte, path []string, value any) []byte {
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

func equalV3Strings(left, right []string) bool {
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

func equalV3Ints(left, right []int) bool {
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
