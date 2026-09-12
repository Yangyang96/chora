package pi

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
)

func TestManagedImageSourceRejectsVersionImageAndIdentityDrift(t *testing.T) {
	source := DefaultManagedImageSource()
	restored, err := RestoreManagedImageSource(source.Record())
	if err != nil || restored != source {
		t.Fatalf("restore managed source = %#v, %v", restored, err)
	}
	recordType := reflect.TypeOf(source.Record())
	for _, forbidden := range []string{"PolicySHA256", "BoundaryImageID"} {
		if _, found := recordType.FieldByName(forbidden); found {
			t.Fatalf("managed Runtime source leaked Sandbox field %q", forbidden)
		}
	}

	invalid := source.Record()
	invalid.RuntimeVersion = "0.84.3"
	if _, err := RestoreManagedImageSource(invalid); err == nil {
		t.Fatal("accepted managed Runtime version drift")
	}
	invalid = source.Record()
	invalid.RuntimeImageID = "sha256:" + strings.Repeat("f", 64)
	if _, err := RestoreManagedImageSource(invalid); err == nil {
		t.Fatal("accepted managed image drift")
	}
	invalid = source.Record()
	invalid.SourceIdentity = sha256.Sum256([]byte("other source"))
	if _, err := RestoreManagedImageSource(invalid); err == nil {
		t.Fatal("accepted managed source identity drift")
	}
}

func TestManagedImageSourceBindsCompleteActivatedReleaseProvenance(t *testing.T) {
	provenance := testManagedReleaseProvenance()
	source, err := NewManagedImageSource(ManagedImageSourceParams{
		RuntimeVersion: RuntimeVersion, Executable: Executable, RuntimeConfigSHA256: RuntimeConfigSHA256,
		RuntimeImageID: AttemptImageID, ReleaseProvenance: provenance,
	})
	if err != nil {
		t.Fatal(err)
	}
	if source.ReleaseProvenance() != provenance || source.Record().ReleaseProvenance != provenance {
		t.Fatal("activated release provenance was not emitted as an immutable value")
	}
	if restored, err := RestoreManagedImageSource(source.Record()); err != nil || restored != source {
		t.Fatalf("restore activated managed source = %#v, %v", restored, err)
	}

	tests := map[string]func(*ManagedReleaseProvenance){
		"kind":                  func(value *ManagedReleaseProvenance) { value.Kind = "other" },
		"release":               func(value *ManagedReleaseProvenance) { value.ReleaseID = "other" },
		"spec":                  func(value *ManagedReleaseProvenance) { value.SpecSHA256 = strings.Repeat("1", 64) },
		"artifact":              func(value *ManagedReleaseProvenance) { value.ArtifactID = "other" },
		"archive format":        func(value *ManagedReleaseProvenance) { value.ArchiveFormat = "oci-layout" },
		"archive digest":        func(value *ManagedReleaseProvenance) { value.ArchiveSHA256 = strings.Repeat("2", 64) },
		"archive size":          func(value *ManagedReleaseProvenance) { value.ArchiveSize++ },
		"Docker config ID":      func(value *ManagedReleaseProvenance) { value.DockerConfigImageID = "sha256:" + strings.Repeat("2", 64) },
		"policy id":             func(value *ManagedReleaseProvenance) { value.PolicyID = "other" },
		"policy path":           func(value *ManagedReleaseProvenance) { value.PolicyPath = "other.json" },
		"policy digest":         func(value *ManagedReleaseProvenance) { value.PolicySHA256 = strings.Repeat("3", 64) },
		"platform os":           func(value *ManagedReleaseProvenance) { value.PlatformOS = "other" },
		"platform architecture": func(value *ManagedReleaseProvenance) { value.PlatformArchitecture = "other" },
		"Pi name":               func(value *ManagedReleaseProvenance) { value.RuntimePiName = "other" },
		"Pi version":            func(value *ManagedReleaseProvenance) { value.RuntimePiVersion = "0.84.3" },
		"Pi integrity":          func(value *ManagedReleaseProvenance) { value.RuntimePiNPMIntegrity = "other" },
		"Node version":          func(value *ManagedReleaseProvenance) { value.RuntimeNodeVersion = "23.0.0" },
		"Node executable":       func(value *ManagedReleaseProvenance) { value.RuntimeNodeExecutable = "/other/node" },
		"Node output":           func(value *ManagedReleaseProvenance) { value.RuntimeNodeVersionOutput = "v23.0.0" },
		"entrypoint":            func(value *ManagedReleaseProvenance) { value.EntrypointJSON = `["other"]` },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			record := source.Record()
			mutate(&record.ReleaseProvenance)
			if _, err := RestoreManagedImageSource(record); err == nil {
				t.Fatal("accepted activated release provenance drift")
			}
		})
	}
}

func testManagedReleaseProvenance() ManagedReleaseProvenance {
	return ManagedReleaseProvenance{
		Kind: ManagedReleaseProvenanceActivatedRelease, ReleaseID: "chora-m1-alpha",
		SpecSHA256: strings.Repeat("a", 64), ArtifactID: "managed-pi-runtime",
		ArchiveFormat: "docker-archive", ArchiveSHA256: strings.Repeat("b", 64), ArchiveSize: 10240,
		DockerConfigImageID: AttemptImageID,
		PolicyID:            "sandbox-policy", PolicyPath: "distribution/policy.json", PolicySHA256: strings.Repeat("c", 64),
		PlatformOS: "linux", PlatformArchitecture: "arm64",
		RuntimePiName: "@earendil-works/pi-coding-agent", RuntimePiVersion: RuntimeVersion,
		RuntimePiNPMIntegrity: "sha512-test", RuntimeNodeVersion: "22.19.0",
		RuntimeNodeExecutable: "/usr/local/bin/node", RuntimeNodeVersionOutput: "v22.19.0", EntrypointJSON: `["pi"]`,
	}
}

func TestLocalPiSourceRejectsPathVersionAndIdentityDrift(t *testing.T) {
	digest := sha256.Sum256([]byte("local-pi"))
	closure := sha256.Sum256([]byte("local-pi-closure"))
	if _, err := NewLocalPiSource(LocalPiSourceParams{ExecutablePath: "pi", ResolvedExecutablePath: "/opt/pi", PackageRoot: "/opt", RuntimeVersion: RuntimeVersion, ExecutableSHA256: digest, ClosureSHA256: closure}); err == nil {
		t.Fatal("accepted relative local Pi path")
	}
	if _, err := NewLocalPiSource(LocalPiSourceParams{ExecutablePath: "/opt/pi", ResolvedExecutablePath: "/opt/pi", PackageRoot: "/opt", RuntimeVersion: "0.84.3", ExecutableSHA256: digest, ClosureSHA256: closure}); err == nil {
		t.Fatal("accepted unsupported local Pi version")
	}
	if _, err := NewLocalPiSource(LocalPiSourceParams{ExecutablePath: "/opt/pi", ResolvedExecutablePath: "/outside/pi", PackageRoot: "/opt", RuntimeVersion: RuntimeVersion, ExecutableSHA256: digest, ClosureSHA256: closure}); err == nil {
		t.Fatal("accepted resolved executable outside package closure")
	}
	source, err := NewLocalPiSource(LocalPiSourceParams{ExecutablePath: "/opt/pi", ResolvedExecutablePath: "/opt/pi", PackageRoot: "/opt", RuntimeVersion: RuntimeVersion, ExecutableSHA256: digest, ClosureSHA256: closure})
	if err != nil {
		t.Fatal(err)
	}
	record := source.Record()
	record.ExecutablePath = "/opt/other-pi"
	if _, err := RestoreLocalPiSource(record); err == nil {
		t.Fatal("accepted local Pi identity drift")
	}
}

func TestAttemptBindingSelectsManagedOrNativeLocalSource(t *testing.T) {
	localBytes := []byte("local-pi-executable")
	localPath := filepath.Join(t.TempDir(), "pi")
	if err := os.WriteFile(localPath, localBytes, 0o700); err != nil {
		t.Fatal(err)
	}
	local, err := NewLocalPiSource(LocalPiSourceParams{
		ExecutablePath: localPath, ResolvedExecutablePath: localPath, PackageRoot: filepath.Dir(localPath),
		RuntimeVersion: RuntimeVersion, ExecutableSHA256: sha256.Sum256(localBytes), ClosureSHA256: sha256.Sum256([]byte("local-closure")),
	})
	if err != nil {
		t.Fatal(err)
	}
	config := testConfig(t, nil)
	config.LocalPiSource = local
	config.ValidateLocalSource = func(context.Context, LocalPiSource) error { return nil }
	adapter, err := New(config)
	if err != nil {
		t.Fatal(err)
	}

	contract := []byte(`{"schema_version":"chora.spec-coding-core.v8"}`)
	for _, profile := range []domain.AgentExecutionProfile{domain.AgentExecutionProfileMinimal, domain.AgentExecutionProfileStandard} {
		run, attempt, snapshot := testRunAttemptForProfile(t, profile)
		invocation, err := adapter.PrepareStart(context.Background(), execution.StartRequest{
			Run: run, Attempt: attempt, SnapshotDocument: snapshot, ExecutionContractDocument: contract,
			WorkspaceRoot: filepath.Join(t.TempDir(), "managed"), LaunchToken: execution.LaunchToken{Value: "managed"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if invocation.Executable() != Executable || !reflect.DeepEqual(invocation.Arguments(), exactRPCArguments) || invocation.Environment()["CHORA_RUNTIME_SOURCE"] != domain.ManagedPiRuntimeSource || invocation.Target().ProviderID() != domain.DockerExecutionProvider {
			t.Fatalf("managed invocation for %q = %q %#v %#v", profile, invocation.Executable(), invocation.Arguments(), invocation.Environment())
		}
	}

	run, attempt, snapshot := testRunAttemptForProfile(t, domain.AgentExecutionProfileTrustedLocal)
	invocation, err := adapter.PrepareStart(context.Background(), execution.StartRequest{
		Run: run, Attempt: attempt, SnapshotDocument: snapshot, ExecutionContractDocument: contract,
		WorkspaceRoot: filepath.Join(t.TempDir(), "local"), LaunchToken: execution.LaunchToken{Value: "local"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if invocation.Executable() != localPath || !reflect.DeepEqual(invocation.Arguments(), nativeRPCArguments) || invocation.Target().ProviderID() != domain.TrustedHostExecutionProvider {
		t.Fatalf("local invocation = %q %#v", invocation.Executable(), invocation.Arguments())
	}
	for _, forbidden := range []string{"--no-skills", "--no-extensions", "--no-context-files", "--provider", "--model"} {
		if contains(invocation.Arguments(), forbidden) {
			t.Fatalf("Trusted Local inherited managed suppression %q", forbidden)
		}
	}
	environment := invocation.Environment()
	if environment["CHORA_AGENT_EXECUTION_PROFILE"] != string(domain.AgentExecutionProfileTrustedLocal) ||
		environment["CHORA_RUNTIME_SOURCE"] != domain.LocalPiRuntimeSource ||
		environment["CHORA_EXECUTION_PROVIDER"] != domain.TrustedHostExecutionProvider ||
		environment["CHORA_CAPABILITY_POLICY"] != domain.NativeCapabilityPolicy ||
		environment["CHORA_TRUST_DISCLOSURE_POLICY"] != domain.TrustedLocalDisclosurePolicy ||
		environment["CHORA_ATTEMPT_IMAGE_ID"] != "" || environment["CHORA_BOUNDARY_IMAGE_ID"] != "" {
		t.Fatalf("local environment = %#v", environment)
	}
}

func TestPrepareBoundStartValidatesOneLocalClosureAndBindsFingerprintToInvocation(t *testing.T) {
	localBytes := []byte("local-pi-executable")
	localPath := filepath.Join(t.TempDir(), "package", "bin", "pi")
	if err := os.MkdirAll(filepath.Dir(localPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(localPath, localBytes, 0o700); err != nil {
		t.Fatal(err)
	}
	local, err := NewLocalPiSource(LocalPiSourceParams{
		ExecutablePath: localPath, ResolvedExecutablePath: localPath, PackageRoot: filepath.Dir(filepath.Dir(localPath)),
		RuntimeVersion: RuntimeVersion, ExecutableSHA256: sha256.Sum256(localBytes), ClosureSHA256: sha256.Sum256([]byte("closure")),
	})
	if err != nil {
		t.Fatal(err)
	}
	closureCalls, executableReads := 0, 0
	config := testConfig(t, nil)
	config.LocalPiSource = local
	config.ValidateLocalSource = func(_ context.Context, got LocalPiSource) error {
		closureCalls++
		if got != local {
			return errors.New("local source drift")
		}
		return nil
	}
	config.ReadSourceFile = func(path string) ([]byte, error) {
		executableReads++
		return os.ReadFile(path)
	}
	adapter, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	run, attempt, snapshot := testRunAttemptForProfile(t, domain.AgentExecutionProfileTrustedLocal)
	preparation, err := adapter.PrepareBoundStart(context.Background(), execution.StartRequest{
		Run: run, Attempt: attempt, SnapshotDocument: snapshot,
		ExecutionContractDocument: []byte(`{"schema_version":"chora.spec-coding-core.v8"}`),
		WorkspaceRoot:             filepath.Join(t.TempDir(), "workspace"), LaunchToken: execution.LaunchToken{Value: "atomic-local"},
	})
	if err != nil || !preparation.Valid() {
		t.Fatalf("bound preparation = %#v, %v", preparation, err)
	}
	if closureCalls != 1 || executableReads != 1 || preparation.Invocation.Target().ProviderID() != domain.TrustedHostExecutionProvider {
		t.Fatalf("closureCalls=%d executableReads=%d target=%q", closureCalls, executableReads, preparation.Invocation.Target().ProviderID())
	}
}

func TestRuntimeFingerprintsDependOnlyOnSelectedRuntimeSourceAndFailClosedOnLocalDrift(t *testing.T) {
	localBytes := []byte("local-pi-executable")
	localPath := filepath.Join(t.TempDir(), "pi")
	if err := os.WriteFile(localPath, localBytes, 0o700); err != nil {
		t.Fatal(err)
	}
	local, err := NewLocalPiSource(LocalPiSourceParams{
		ExecutablePath: localPath, ResolvedExecutablePath: localPath, PackageRoot: filepath.Dir(localPath), RuntimeVersion: RuntimeVersion,
		ExecutableSHA256: sha256.Sum256(localBytes), ClosureSHA256: sha256.Sum256([]byte("local-closure")),
	})
	if err != nil {
		t.Fatal(err)
	}
	config := testConfig(t, nil)
	config.IsolatedSource, err = NewIsolatedSource(IsolatedSourceParams{ImageID: "sha256:" + strings.Repeat("a", 64), HelperSHA256: strings.Repeat("b", 64), PolicySHA256: strings.Repeat("c", 64)})
	if err != nil {
		t.Fatal(err)
	}
	config.LocalPiSource = local
	config.ValidateLocalSource = func(context.Context, LocalPiSource) error { return nil }
	adapter, err := New(config)
	if err != nil {
		t.Fatal(err)
	}

	fingerprints := make(map[domain.AgentExecutionProfile]execution.RuntimeFingerprint)
	for _, profile := range domain.SupportedAgentExecutionProfiles() {
		binding, err := domain.NewAgentExecutionProfileBinding(profile)
		if err != nil {
			t.Fatal(err)
		}
		first, err := adapter.FingerprintForBinding(context.Background(), binding)
		if err != nil || !first.Valid() {
			t.Fatalf("fingerprint %q = %#v, %v", profile, first, err)
		}
		second, err := adapter.FingerprintForBinding(context.Background(), binding)
		if err != nil || second != first {
			t.Fatalf("unstable fingerprint %q = %#v / %#v, %v", profile, first, second, err)
		}
		fingerprints[profile] = first
	}
	if fingerprints[domain.AgentExecutionProfileMinimal] != fingerprints[domain.AgentExecutionProfileStandard] {
		t.Fatalf("shared managed Pi Runtime changed across profile/capability policy: %#v", fingerprints)
	}
	if fingerprints[domain.AgentExecutionProfileStandard].Digest == fingerprints[domain.AgentExecutionProfileTrustedLocal].Digest {
		t.Fatalf("managed and local Runtime sources shared a fingerprint: %#v", fingerprints)
	}

	standardBeforeSandboxMutation := fingerprints[domain.AgentExecutionProfileStandard]
	adapter.config.Policy = []byte(`{"different_sandbox_policy":true}`)
	adapter.config.BoundaryImageID = "sha256:" + strings.Repeat("e", 64)
	standardBinding, _ := domain.NewAgentExecutionProfileBinding(domain.AgentExecutionProfileStandard)
	standardAfterSandboxMutation, err := adapter.FingerprintForBinding(context.Background(), standardBinding)
	if err != nil || standardAfterSandboxMutation != standardBeforeSandboxMutation {
		t.Fatalf("Sandbox policy changed Runtime fingerprint: before=%#v after=%#v err=%v", standardBeforeSandboxMutation, standardAfterSandboxMutation, err)
	}

	if err := os.WriteFile(localPath, []byte("drifted-local-pi"), 0o700); err != nil {
		t.Fatal(err)
	}
	trusted, _ := domain.NewAgentExecutionProfileBinding(domain.AgentExecutionProfileTrustedLocal)
	if _, err := adapter.FingerprintForBinding(context.Background(), trusted); err == nil {
		t.Fatal("accepted local executable identity drift")
	}
}

func TestTrustedLocalRequiresConfiguredSource(t *testing.T) {
	adapter := testAdapter(t, nil)
	binding, err := domain.NewAgentExecutionProfileBinding(domain.AgentExecutionProfileTrustedLocal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.FingerprintForBinding(context.Background(), binding); err == nil {
		t.Fatal("Trusted Local fingerprint passed without a local source")
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
