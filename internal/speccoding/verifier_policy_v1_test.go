package speccoding

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestVerifierPoliciesV1AreDigestBoundAndDifferOnlyByEvidenceMode(t *testing.T) {
	fullBytes := readVerifierPolicyV1(t, "verifier-policy.v1.json")
	unknownBytes := readVerifierPolicyV1(t, "verifier-policy.acceptance-only.v1.json")
	full, err := DecodeVerifierPolicyV1(fullBytes)
	if err != nil {
		t.Fatal(err)
	}
	unknown, err := DecodeVerifierPolicyV1(unknownBytes)
	if err != nil {
		t.Fatal(err)
	}
	if digestBytesV1(fullBytes) != VerifierPolicySHA256V1 || digestBytesV1(unknownBytes) != VerifierPolicyAcceptanceOnlySHA256V1 {
		t.Fatal("verifier policy fixture digest drift")
	}
	if full.PolicyMode != VerifierPolicyModeFullV1 || full.Command.EvidenceAvailability != "execute_frozen_commands" {
		t.Fatalf("full mode = %q / %q", full.PolicyMode, full.Command.EvidenceAvailability)
	}
	if unknown.PolicyMode != VerifierPolicyModeUnknownV1 || unknown.Command.EvidenceAvailability != "deterministically_unavailable_by_accepted_policy" {
		t.Fatalf("unknown mode = %q / %q", unknown.PolicyMode, unknown.Command.EvidenceAvailability)
	}
	full.PolicyMode, unknown.PolicyMode = "", ""
	full.Command.EvidenceAvailability, unknown.Command.EvidenceAvailability = "", ""
	if !reflect.DeepEqual(full, unknown) {
		t.Fatal("acceptance-only policy weakens a boundary other than evidence availability")
	}
}

func TestVerifierPolicyV1PinsIndependentBoundary(t *testing.T) {
	policy, err := DecodeVerifierPolicyV1(readVerifierPolicyV1(t, "verifier-policy.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if policy.Network.Mode != "none" || policy.Network.GeneralEgress || policy.Credentials.Injection != "none" || len(policy.Credentials.DeclaredNames) != 0 {
		t.Fatalf("network or credentials enabled: %#v %#v", policy.Network, policy.Credentials)
	}
	if policy.Workspace.ReuseAgentWorkspace || policy.Workspace.ReusePreviousVerificationWorkspace || policy.Workspace.LoadPiSession || policy.Workspace.LoadModelCredentials || policy.Workspace.LoadImplicitContext || policy.Image.AgentRuntimeInvoked {
		t.Fatalf("agent boundary reused: %#v %#v", policy.Workspace, policy.Image)
	}
	if policy.Command.Authority != "frozen_acceptance_verification_commands" || policy.Command.Execution != "direct_argv" || policy.Command.Shell || policy.Command.AgentCommandAuthority || policy.Command.DisplayTextAuthority {
		t.Fatalf("unsafe command authority: %#v", policy.Command)
	}
	if policy.Resources != (VerifierResourcesV1{CPUs: 2, MemoryMiB: 4096, MemorySwapMiB: 4096, PIDsLimit: 256, FileDescriptors: 1024, CommandTimeoutSeconds: 600, AttemptTimeoutSeconds: 1200}) {
		t.Fatalf("resource drift: %#v", policy.Resources)
	}
	if policy.Evidence.PersistedLogBytes != 10*1024*1024 || policy.Evidence.ArtifactBytes != 100*1024*1024 || policy.Termination.DeathConfirmationSeconds != 5 || !policy.Cleanup.ProveBeforeResult {
		t.Fatal("evidence or cleanup boundary drift")
	}
}

func TestVerifierPolicyV1RejectsAnyByteOrSemanticDrift(t *testing.T) {
	original := readVerifierPolicyV1(t, "verifier-policy.v1.json")
	var document map[string]any
	if err := json.Unmarshal(original, &document); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		path []string
		set  any
	}{
		{"network", []string{"network", "mode"}, "bridge"},
		{"credentials", []string{"credentials", "declared_names"}, []string{"TOKEN"}},
		{"workspace reuse", []string{"workspace", "reuse_agent_workspace"}, true},
		{"shell", []string{"command", "shell"}, true},
		{"cpu", []string{"resources", "cpus"}, 3},
		{"log", []string{"evidence", "persisted_log_bytes"}, 10485761},
		{"cleanup", []string{"cleanup", "prove_before_result"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var mutated map[string]any
			if err := json.Unmarshal(original, &mutated); err != nil {
				t.Fatal(err)
			}
			cursor := mutated
			for _, key := range test.path[:len(test.path)-1] {
				cursor = cursor[key].(map[string]any)
			}
			cursor[test.path[len(test.path)-1]] = test.set
			data, err := json.Marshal(mutated)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeVerifierPolicyV1(data); err == nil {
				t.Fatal("drifted verifier policy accepted")
			}
		})
	}
	if _, err := DecodeVerifierPolicyV1(append(append([]byte{}, original...), ' ')); err == nil {
		t.Fatal("byte-drifted verifier policy accepted")
	}
}

func readVerifierPolicyV1(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "contracts", "g2-m4", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func digestBytesV1(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
