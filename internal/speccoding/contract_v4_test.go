package speccoding

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestV4CoreContractPinsEnterpriseTrustAmendment(t *testing.T) {
	contract, err := DecodeCoreContract(readV4ContractFixture(t, "chora-m1-real-task.v4.json"))
	if err != nil {
		t.Fatal(err)
	}
	document := contract.Document()
	if document.SchemaVersion != CoreContractSchemaVersionV4 || document.Revision != 4 {
		t.Fatalf("v4 identity = %q revision %d", document.SchemaVersion, document.Revision)
	}
	if document.Candidate.Runtime.Config.Path != "contracts/g2-m1a/pi-runtime-config.v4.json" || document.Candidate.Policy.Path != "contracts/g2-m1a/sandbox-policy.v4.json" {
		t.Fatalf("v4 references = %#v %#v", document.Candidate.Runtime.Config, document.Candidate.Policy)
	}
	for _, capability := range []string{"sandbox.fixed_enterprise_proxy_upstream", "sandbox.explicit_enterprise_ca_projection", "sandbox.tls_verification_required"} {
		if !containsV2String(document.Execution.RequiredCapabilities, capability) {
			t.Fatalf("missing v4 capability %q", capability)
		}
	}
}

func TestV4PiRuntimeConfigPinsReadOnlyExplicitCA(t *testing.T) {
	config, err := DecodePiRuntimeConfigV4(readV4ContractFixture(t, "pi-runtime-config.v4.json"))
	if err != nil {
		t.Fatal(err)
	}
	trust := config.Trust
	if trust.Source.Path != "contracts/g2-m1a/starpoint-root-ca-2048-g2.pem" || trust.Source.SHA256 != starpointRootCASHA256 || trust.ContainerPath != "/run/chora/trust/starpoint-root-ca-2048-g2.pem" || trust.Projection != "task_scoped_stdin_projection" || trust.DirectoryPath != "/run/chora/trust" || trust.DirectoryOwner != "0:0" || trust.DirectoryModeBeforeProjection != "0755" || trust.DirectoryModeAfterProjection != "0555" || trust.FileOwner != "0:0" || trust.FileMode != "0444" || !trust.ProjectionBeforeRuntimeStart || !trust.RuntimeWriteProbeRequired || trust.SourceMounted || trust.Persist || trust.AgentWritable || trust.SystemStoreModified || !trust.TLSVerificationRequired {
		t.Fatalf("unsafe v4 trust = %#v", trust)
	}
	if config.Environment["NODE_EXTRA_CA_CERTS"] != trust.ContainerPath || config.Environment["HTTPS_PROXY"] != "http://codex-boundary:8080" {
		t.Fatalf("v4 environment = %#v", config.Environment)
	}
}

func TestV4SandboxPolicyPinsEnterpriseUpstream(t *testing.T) {
	policy, err := DecodeSandboxPolicyV4(readV4ContractFixture(t, "sandbox-policy.v4.json"))
	if err != nil {
		t.Fatal(err)
	}
	proxy := policy.Network.Proxy
	if proxy.ResolveDNSAtBoundary || proxy.DNSResolutionOwner != "fixed_enterprise_upstream" || proxy.Upstream.Kind != "fixed_enterprise_http_connect_proxy" || proxy.Upstream.Endpoint != "http://host.docker.internal:9981" || proxy.Upstream.AccessibleFromAttempt || !proxy.Upstream.TaskScoped || !proxy.Upstream.Traceable {
		t.Fatalf("unsafe v4 upstream = %#v", proxy)
	}
	if proxy.TLS.CodexBoundaryTerminates || !proxy.TLS.EnterpriseUpstreamTerminates || !proxy.TLS.VerificationRequired || proxy.TLS.TrustAnchorSHA256 != starpointRootCASHA256 || proxy.TLS.TrustAnchorPath != "/run/chora/trust/starpoint-root-ca-2048-g2.pem" {
		t.Fatalf("unsafe v4 TLS = %#v", proxy.TLS)
	}
	if !equalV3Strings(policy.Isolation.Tmpfs, []string{"/tmp", "/run/chora/pi", "/run/chora/trust"}) || policy.Trust.DirectoryOwner != "0:0" || policy.Trust.DirectoryModeBeforeProjection != "0755" || policy.Trust.DirectoryModeAfterProjection != "0555" || policy.Trust.FileOwner != "0:0" || !policy.Trust.ProjectionBeforeRuntimeStart || !policy.Trust.RuntimeWriteProbeRequired || policy.Trust.AgentWritable || policy.Trust.SourceMounted || !policy.Trust.RemoveAttemptCopy {
		t.Fatalf("unsafe v4 trust projection = %#v %#v", policy.Isolation.Tmpfs, policy.Trust)
	}
}

func TestV4ContractsRejectTrustAndEgressRelaxation(t *testing.T) {
	piFixture := readV4ContractFixture(t, "pi-runtime-config.v4.json")
	for _, test := range []struct {
		name string
		path []string
		set  any
	}{
		{"disabled TLS", []string{"trust", "tls_verification_required"}, false},
		{"writable CA", []string{"trust", "agent_writable"}, true},
		{"runtime-owned trust dir", []string{"trust", "directory_owner"}, "1000:1000"},
		{"writable trust dir", []string{"trust", "directory_mode_after_projection"}, "0755"},
		{"late projection", []string{"trust", "projection_before_runtime_start"}, false},
		{"runtime-owned CA", []string{"trust", "file_owner"}, "1000:1000"},
		{"mounted CA source", []string{"trust", "source_mounted"}, true},
		{"wrong CA", []string{"trust", "source", "sha256"}, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{"missing Node CA", []string{"environment", "NODE_EXTRA_CA_CERTS"}, ""},
	} {
		t.Run("pi "+test.name, func(t *testing.T) {
			if _, err := DecodePiRuntimeConfigV4(mutateV4JSON(t, piFixture, test.path, test.set)); err == nil {
				t.Fatal("unsafe v4 Pi config accepted")
			}
		})
	}

	policyFixture := readV4ContractFixture(t, "sandbox-policy.v4.json")
	for _, test := range []struct {
		name string
		path []string
		set  any
	}{
		{"attempt reaches upstream", []string{"network", "proxy", "upstream", "accessible_from_attempt"}, true},
		{"upstream drift", []string{"network", "proxy", "upstream", "endpoint"}, "http://host.docker.internal:9999"},
		{"hidden termination", []string{"network", "proxy", "tls", "enterprise_upstream_terminates"}, false},
		{"disabled verification", []string{"network", "proxy", "tls", "verification_required"}, false},
		{"writable trust directory", []string{"trust", "directory_mode_after_projection"}, "0755"},
		{"missing write probe", []string{"trust", "runtime_write_probe_required"}, false},
		{"general egress", []string{"network", "general_egress"}, true},
	} {
		t.Run("sandbox "+test.name, func(t *testing.T) {
			if _, err := DecodeSandboxPolicyV4(mutateV4JSON(t, policyFixture, test.path, test.set)); err == nil {
				t.Fatal("unsafe v4 Sandbox policy accepted")
			}
		})
	}
}

func TestV4TrustAnchorIsExactSelfSignedCA(t *testing.T) {
	data := readV4ContractFixture(t, "starpoint-root-ca-2048-g2.pem")
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != starpointRootCASHA256 {
		t.Fatal("v4 CA digest drift")
	}
	block, rest := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		t.Fatal("v4 CA is not one PEM certificate")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if certificate.Subject.String() != certificate.Issuer.String() || certificate.Subject.CommonName != "StarPoint Root CA 2048 - G2" || !certificate.IsCA || time.Now().After(certificate.NotAfter) {
		t.Fatalf("unexpected v4 CA identity = %#v", certificate.Subject)
	}
}

func TestV4CoreReferencesExactCandidateFiles(t *testing.T) {
	contract, err := DecodeCoreContract(readV4ContractFixture(t, "chora-m1-real-task.v4.json"))
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

func readV4ContractFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "contracts", "g2-m1a", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func mutateV4JSON(t *testing.T, data []byte, path []string, value any) []byte {
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
