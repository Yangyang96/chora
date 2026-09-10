package localweb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/baselinebundle"
	"github.com/Yangyang96/chora/internal/dockersupervisor"
	"github.com/Yangyang96/chora/internal/releaseassets"
	storecontract "github.com/Yangyang96/chora/internal/store"
	"github.com/Yangyang96/chora/internal/verifier"
)

func TestInstalledVerifierBindsExactReleaseRunnerQualificationAndLiveEngine(t *testing.T) {
	fixture := newInstalledVerifierBindingFixture(t)
	authority, err := bindInstalledVerifierDockerAuthorityFromImages(context.Background(), fixture.executionPolicyPath, fixture.policyBytes, fixture.rolePolicyBytes, fixture.policy,
		fixture.runner, fixture.capability, fixture.qualification, fixture.managed, fixture.independent, fixture.probe)
	if err != nil {
		t.Fatal(err)
	}
	hostile, err := os.ReadFile(filepath.Join(fixture.repoRoot, filepath.FromSlash(fixture.independent.PolicyPath)))
	if err != nil || bytes.Equal(hostile, fixture.rolePolicyBytes) {
		t.Fatalf("repo-side hostile policy fixture unavailable: %q err=%v", hostile, err)
	}
	if authority.VerifierImageID() != fixture.independent.LocalDockerConfigImageID || len(fixture.runner.commands) != 5 {
		t.Fatalf("installed authority = image %q commands %#v", authority.VerifierImageID(), fixture.runner.commands)
	}
	if _, err := authority.Run(context.Background(), verifier.DockerCommand{Args: []string{"image", "inspect", "--format", "{{.Id}}", authority.VerifierImageID()}}); err != nil {
		t.Fatal(err)
	}
	if got := fixture.runner.commands[len(fixture.runner.commands)-1]; !slices.Equal(got, []string{"image", "inspect", "--format", "{{.Id}}", authority.VerifierImageID()}) {
		t.Fatalf("Verifier did not reuse exact Runner: %#v", fixture.runner.commands)
	}
	stdout := &bytes.Buffer{}
	result, err := authority.Run(context.Background(), verifier.DockerCommand{Args: []string{"exec", "container", "go", "test"}, Stdout: stdout})
	if err != nil || result.ExitCode != 7 || stdout.String() != "bounded output" || len(fixture.runner.startCommands) != 1 {
		t.Fatalf("Verifier streaming command did not reuse exact Runner: result=%#v err=%v output=%q starts=%#v", result, err, stdout.String(), fixture.runner.startCommands)
	}
}

func TestInstalledVerifierRejectsAmbientRunnerWrongRoleImageQualificationAndEngine(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*installedVerifierBindingFixture)
	}{
		{name: "ambient runner", mutate: func(f *installedVerifierBindingFixture) { f.runner = nil }},
		{name: "wrong role", mutate: func(f *installedVerifierBindingFixture) { f.independent.Role = releaseassets.RoleCapabilityProbe }},
		{name: "wrong image", mutate: func(f *installedVerifierBindingFixture) {
			wrong := "sha256:" + strings.Repeat("f", 64)
			f.managed.LocalDockerConfigImageID, f.independent.LocalDockerConfigImageID, f.probe.LocalDockerConfigImageID = wrong, wrong, wrong
		}},
		{name: "archive digest alias drift", mutate: func(f *installedVerifierBindingFixture) { f.independent.ArchiveSHA256 = strings.Repeat("a", 64) }},
		{name: "archive size alias drift", mutate: func(f *installedVerifierBindingFixture) { f.probe.ArchiveSize++ }},
		{name: "archive path alias drift", mutate: func(f *installedVerifierBindingFixture) {
			f.independent.ArchivePath = filepath.Join(f.releaseRoot, "other.tar")
		}},
		{name: "local image identity drift", mutate: func(f *installedVerifierBindingFixture) { f.runner.imageID = "sha256:" + strings.Repeat("e", 64) }},
		{name: "wrong qualification", mutate: func(f *installedVerifierBindingFixture) { f.qualification = dockersupervisor.EngineQualification{} }},
		{name: "wrong Engine", mutate: func(f *installedVerifierBindingFixture) { f.runner.daemonID = "different-installed-engine" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newInstalledVerifierBindingFixture(t)
			test.mutate(&fixture)
			if _, err := bindInstalledVerifierDockerAuthorityFromImages(context.Background(), fixture.executionPolicyPath, fixture.policyBytes, fixture.rolePolicyBytes, fixture.policy,
				fixture.runner, fixture.capability, fixture.qualification, fixture.managed, fixture.independent, fixture.probe); err == nil {
				t.Fatal("invalid installed Verifier authority was accepted")
			}
		})
	}
}

func TestInstalledVerifierRejectsEmptyActivatedReleaseRoot(t *testing.T) {
	fixture := newInstalledVerifierBindingFixture(t)
	zeroRelease := &releaseassets.ActivatedRelease{}
	_, err := bindInstalledVerifierDockerAuthority(context.Background(), fixture.executionPolicyPath, fixture.policyBytes, fixture.policy, ProductOptions{
		ActivatedRelease: zeroRelease, ActivatedReleaseRoot: "", DockerRunner: fixture.runner,
		DockerCapability: fixture.capability, DockerQualification: fixture.qualification,
	})
	if err == nil {
		t.Fatal("empty activated release root was accepted")
	}
}

func TestRecoverInstalledVerifierStartupRunsWithoutDatabaseCandidatesAndWithExistingCandidates(t *testing.T) {
	for _, count := range []int{0, 2} {
		t.Run(fmt.Sprintf("candidates-%d", count), func(t *testing.T) {
			recovery := &recordingVerifierRecovery{}
			listed := false
			got, err := recoverInstalledVerifierStartup(context.Background(), recovery, func(context.Context) ([]storecontract.VerificationStartupCandidate, error) {
				listed = true
				if recovery.calls != 1 {
					t.Fatal("DB candidates were consulted before exact-scope recovery")
				}
				return make([]storecontract.VerificationStartupCandidate, count), nil
			})
			if err != nil || got != count || recovery.calls != 1 || !listed {
				t.Fatalf("recovery calls=%d listed=%t count=%d err=%v", recovery.calls, listed, got, err)
			}
		})
	}
}

func TestRecoverInstalledVerifierStartupFailureBlocksCandidateRecovery(t *testing.T) {
	want := errors.New("remove-and-prove failed")
	recovery := &recordingVerifierRecovery{err: want}
	listed := false
	_, err := recoverInstalledVerifierStartup(context.Background(), recovery, func(context.Context) ([]storecontract.VerificationStartupCandidate, error) {
		listed = true
		return nil, nil
	})
	if !errors.Is(err, want) || recovery.calls != 1 || listed {
		t.Fatalf("recovery failure = %v calls=%d listed=%t", err, recovery.calls, listed)
	}
}

type installedVerifierBindingFixture struct {
	repoRoot, releaseRoot        string
	executionPolicyPath          string
	policyBytes, rolePolicyBytes []byte
	policy                       verifier.Policy
	runner                       *installedVerifierTestRunner
	capability                   dockersupervisor.CapabilityProbeContract
	qualification                dockersupervisor.EngineQualification
	managed, independent, probe  releaseassets.ActivatedImage
}

func newInstalledVerifierBindingFixture(t *testing.T) installedVerifierBindingFixture {
	t.Helper()
	policyBytes, err := os.ReadFile(filepath.Join("..", "..", "contracts", "g2-m4", "verifier-policy.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	policy, err := verifier.DecodePolicyAuthority(policyBytes)
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := t.TempDir()
	releaseRoot := t.TempDir()
	executionPolicyPath := "contracts/g2-m4/verifier-policy.v1.json"
	rolePolicyPath := "distribution/v1/policies/independent-verifier.v1.json"
	rolePolicy := []byte(`{"schema_version":"chora.independent-verifier-role-policy.v1","execution_policy":"contracts/g2-m4/verifier-policy.v1.json","agent_runtime_invoked":false,"network":"none","credentials":"none","workspace":"fresh_per_verification_attempt","image_alias_role":"managed_pi_runtime","fail_closed":true}`)
	for _, root := range []string{repoRoot, releaseRoot} {
		if err := os.MkdirAll(filepath.Join(root, filepath.Dir(rolePolicyPath)), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repoRoot, rolePolicyPath), []byte(`{"hostile":"repo policy is not release authority"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(releaseRoot, rolePolicyPath), rolePolicy, 0o600); err != nil {
		t.Fatal(err)
	}
	rolePolicyBytes, err := os.ReadFile(filepath.Join(releaseRoot, rolePolicyPath))
	if err != nil {
		t.Fatal(err)
	}
	roleDigest := sha256.Sum256(rolePolicy)
	base := releaseassets.ActivatedImage{
		Role: releaseassets.RoleManagedPiRuntime, ArtifactID: "managed-pi-runtime",
		LocalDockerConfigImageID: policy.Document().Image,
		ArchiveFormat:            releaseassets.DockerArchiveFormat, ArchivePath: filepath.Join(releaseRoot, "managed.tar"),
		ArchiveSize: 10240, ArchiveSHA256: strings.Repeat("1", 64),
		ReleaseID: "chora-m1-alpha", SpecSHA256: strings.Repeat("2", 64), Platform: releaseassets.Platform{OS: "linux", Architecture: "arm64"},
		RuntimePiName: "@earendil-works/pi-coding-agent", RuntimePiVersion: "0.84.2", RuntimePiNPMIntegrity: "sha512-authority",
		RuntimeNodeVersion: "22.19.0", RuntimeNodeExecutable: "/usr/local/bin/node", RuntimeNodeVersionOutput: "v22.19.0", EntrypointJSON: `["pi"]`,
	}
	independent := base
	independent.Role = releaseassets.RoleIndependentVerifier
	independent.PolicyID = "chora.independent-verifier.v1"
	independent.PolicyPath = rolePolicyPath
	independent.PolicySHA256 = hex.EncodeToString(roleDigest[:])
	probe := base
	probe.Role = releaseassets.RoleCapabilityProbe
	endpoint := "unix:///private/tmp/chora-installed-verifier.sock"
	endpointDigest, err := dockersupervisor.DigestContextEndpoint(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	runner := &installedVerifierTestRunner{daemonID: "installed-engine", endpoint: endpoint, contextName: "installed-context", imageID: policy.Document().Image}
	identity, err := dockersupervisor.NewEngineIdentity(dockersupervisor.EngineIdentityInput{
		DaemonID: runner.daemonID, APIVersion: "1.44", OperatingSystem: "linux", Architecture: "arm64", ContextEndpointDigest: endpointDigest,
		ProviderName: "installed-provider", EngineVersion: "29.6.1", ContextName: runner.contextName,
	})
	if err != nil {
		t.Fatal(err)
	}
	capability, err := dockersupervisor.NewCapabilityProbeContract(policy.Document().Image, strings.Repeat("3", 64))
	if err != nil {
		t.Fatal(err)
	}
	qualification := testEngineQualification(t, identity, capability)
	return installedVerifierBindingFixture{
		repoRoot: repoRoot, releaseRoot: releaseRoot, executionPolicyPath: executionPolicyPath, policyBytes: policyBytes, rolePolicyBytes: rolePolicyBytes, policy: policy,
		runner: runner, capability: capability, qualification: qualification, managed: base, independent: independent, probe: probe,
	}
}

func testEngineQualification(t *testing.T, identity dockersupervisor.EngineIdentity, contract dockersupervisor.CapabilityProbeContract) dockersupervisor.EngineQualification {
	t.Helper()
	record := dockersupervisor.EngineQualificationRecord{
		SchemaVersion: "chora.docker-engine-qualification/v1", EngineIdentityDigest: identity.Digest(), ProbeContractDigest: contract.Digest(),
		ProbeImageID: contract.ProbeImageID(), SandboxPolicyDigest: contract.SandboxPolicyDigest(), CompletedAt: time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
	}
	payload, err := json.Marshal(struct {
		SchemaVersion        string `json:"schema_version"`
		EngineIdentityDigest string `json:"engine_identity_digest"`
		ProbeContractDigest  string `json:"probe_contract_digest"`
		ProbeImageID         string `json:"probe_image_id"`
		SandboxPolicyDigest  string `json:"sandbox_policy_digest"`
		CompletedAt          string `json:"completed_at"`
	}{record.SchemaVersion, record.EngineIdentityDigest, record.ProbeContractDigest, record.ProbeImageID, record.SandboxPolicyDigest, record.CompletedAt})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	record.QualificationDigest = hex.EncodeToString(digest[:])
	qualification, err := dockersupervisor.RestoreEngineQualification(record)
	if err != nil {
		t.Fatal(err)
	}
	return qualification
}

type installedVerifierTestRunner struct {
	daemonID, endpoint, contextName, imageID string
	commands                                 [][]string
	startCommands                            [][]string
}

func (runner *installedVerifierTestRunner) Run(_ context.Context, command dockersupervisor.Command) (dockersupervisor.CommandResult, error) {
	runner.commands = append(runner.commands, slices.Clone(command.Args))
	switch {
	case len(command.Args) > 0 && command.Args[0] == "version":
		return dockersupervisor.CommandResult{Stdout: []byte(`{"api_version":"1.44","server_version":"29.6.1","operating_system":"linux","architecture":"arm64"}`)}, nil
	case len(command.Args) > 0 && command.Args[0] == "info":
		return dockersupervisor.CommandResult{Stdout: []byte(fmt.Sprintf(`{"daemon_id":%q,"provider_name":"installed-provider"}`, runner.daemonID))}, nil
	case slices.Equal(command.Args, []string{"context", "show"}):
		return dockersupervisor.CommandResult{Stdout: []byte(runner.contextName)}, nil
	case len(command.Args) > 1 && command.Args[0] == "context" && command.Args[1] == "inspect":
		body, _ := json.Marshal(runner.endpoint)
		return dockersupervisor.CommandResult{Stdout: body}, nil
	case len(command.Args) > 1 && command.Args[0] == "image" && command.Args[1] == "inspect":
		return dockersupervisor.CommandResult{Stdout: []byte(runner.imageID)}, nil
	default:
		return dockersupervisor.CommandResult{}, nil
	}
}

func (runner *installedVerifierTestRunner) Start(_ context.Context, command dockersupervisor.Command) (dockersupervisor.Process, error) {
	runner.startCommands = append(runner.startCommands, slices.Clone(command.Args))
	if command.Stdout != nil {
		_, _ = command.Stdout.Write([]byte("bounded output"))
	}
	return installedVerifierTestProcess{}, nil
}

type installedVerifierTestProcess struct{}

func (installedVerifierTestProcess) Write(body []byte) (int, error) { return len(body), nil }
func (installedVerifierTestProcess) Close() error                   { return nil }
func (installedVerifierTestProcess) Kill() error                    { return nil }
func (installedVerifierTestProcess) Wait() (int, error)             { return 7, errors.New("exit status 7") }

type recordingVerifierRecovery struct {
	calls int
	err   error
}

func (recovery *recordingVerifierRecovery) Recover(context.Context) error {
	recovery.calls++
	return recovery.err
}

func TestPrepareProductBaselineReconstructsAndReusesBundledIdentity(t *testing.T) {
	if os.Getenv("CHORA_BASELINE_BUNDLE_REAL") != "1" {
		t.Skip("set CHORA_BASELINE_BUNDLE_REAL=1 for the 139 MiB product Baseline v6 gate")
	}
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	if err := os.Mkdir(runtimeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	baseline, manifestPath, err := prepareProductBaseline(context.Background(), repositoryRoot, runtimeRoot, true)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(baseline) != "baseline-v6" || filepath.Base(filepath.Dir(manifestPath)) != "source-baseline-v6" {
		t.Fatalf("product baseline paths = %q, %q", baseline, manifestPath)
	}
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	result, err := baselinebundle.Verify(baseline, manifest)
	if err != nil || result.AggregateSHA256 != "b28cb1624124736e745f2b217e841957330f763b6b41fab87b2a88b809b8a0cd" {
		t.Fatalf("reconstructed baseline = %#v, err = %v", result, err)
	}
	reused, _, err := prepareProductBaseline(context.Background(), repositoryRoot, runtimeRoot, true)
	if err != nil || reused != baseline {
		t.Fatalf("reused baseline = %q, err = %v", reused, err)
	}
}
