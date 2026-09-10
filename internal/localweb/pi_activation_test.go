package localweb

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	agentpi "github.com/Yangyang96/chora/internal/agent/pi"
	"github.com/Yangyang96/chora/internal/dockersupervisor"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/pidistribution"
)

func TestComposePiRuntimeActivatesOnlyExactTrustedLocalSelection(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	packageRoot := filepath.Join(base, "pi-package")
	bin := filepath.Join(packageRoot, "bin")
	executable := filepath.Join(bin, "pi")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	packageJSON := filepath.Join(packageRoot, "package.json")
	if err := os.WriteFile(packageJSON, []byte(`{"name":"@earendil-works/pi-coding-agent","version":"0.84.2"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("trusted Pi fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	resolver, err := pidistribution.NewResolver(pidistribution.Config{
		LookPath: func(string) (string, error) { return executable, nil },
		RunVersion: func(_ context.Context, observed string, arguments ...string) (pidistribution.VersionResult, error) {
			if filepath.Base(observed) != "pi" || len(arguments) != 1 || arguments[0] != "--version" {
				return pidistribution.VersionResult{}, errors.New("unexpected Pi version probe")
			}
			return pidistribution.VersionResult{Stdout: []byte(pidistribution.SupportedVersion + "\n")}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	selection, err := resolver.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	runtimeConfig, err := os.ReadFile(filepath.Join("..", "..", "contracts", "g2-m1a", "pi-runtime-config.v6.json"))
	if err != nil {
		t.Fatal(err)
	}
	policy, err := os.ReadFile(filepath.Join("..", "..", "contracts", "g2-m1a", "sandbox-policy.v4.json"))
	if err != nil {
		t.Fatal(err)
	}
	timeoutPolicy := acceptanceTimeoutPolicyFor(nil)
	runtimeRoot := filepath.Join(base, "runtime")
	if err := os.Mkdir(runtimeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	composition, err := composePiRuntime(context.Background(), runtimeRoot, filepath.Join(base, "artifacts"), base, runtimeConfig, policy, ProductOptions{
		LocalPiResolver: resolver, LocalPiSelection: selection,
	}, true, timeoutPolicy)
	if err != nil {
		t.Fatal(err)
	}
	if composition.dockerSupervisor != nil || composition.trustedSupervisor == nil || composition.adapter == nil {
		t.Fatalf("unexpected composition: %#v", composition)
	}
	if !composition.localSnapshot.Selection().Configured() || composition.localSnapshot.Selection().PackageRoot() == packageRoot {
		t.Fatalf("local Runtime did not execute from an immutable Chora snapshot: %#v", composition.localSnapshot.Selection().Record())
	}
	status := piCompositionStatus(composition)
	if !status.Enabled || status.Provider != domain.TrustedHostExecutionProvider || status.Image != "" || status.HostReadIsolation != "not claimed" {
		t.Fatalf("Trusted Local status = %#v", status)
	}
	fingerprinter, ok := composition.adapter.(execution.BindingFingerprinter)
	if !ok {
		t.Fatal("Pi adapter lost profile-bound fingerprinting")
	}
	trusted, _ := domain.NewAgentExecutionProfileBinding(domain.AgentExecutionProfileTrustedLocal)
	if fingerprint, err := fingerprinter.FingerprintForBinding(context.Background(), trusted); err != nil || !fingerprint.Valid() || fingerprint.Version != agentpi.RuntimeVersion {
		t.Fatalf("Trusted Local fingerprint = %#v, %v", fingerprint, err)
	}
	standard, _ := domain.NewAgentExecutionProfileBinding(domain.AgentExecutionProfileStandard)
	if _, err := fingerprinter.FingerprintForBinding(context.Background(), standard); err == nil {
		t.Fatal("local-only activation silently supplied a managed profile")
	}
	if err := validatePiCompositionFingerprints(context.Background(), composition); err != nil {
		t.Fatalf("installed Trusted Local binding did not validate: %v", err)
	}

	if err := os.WriteFile(packageJSON, []byte(`{"name":"@earendil-works/pi-coding-agent","version":"0.84.2","drift":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fingerprinter.FingerprintForBinding(context.Background(), trusted); err != nil {
		t.Fatalf("mutable discovery source drift invalidated the immutable Runtime snapshot: %v", err)
	}
	snapshotPackageJSON := filepath.Join(composition.localSnapshot.Selection().PackageRoot(), "package.json")
	if err := os.WriteFile(snapshotPackageJSON, []byte(`{"name":"@earendil-works/pi-coding-agent","version":"0.84.2","snapshot_drift":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fingerprinter.FingerprintForBinding(context.Background(), trusted); err == nil {
		t.Fatal("immutable Trusted Local snapshot drift remained executable")
	}
}

func TestComposePiRuntimeRejectsPartialLocalActivation(t *testing.T) {
	runtimeConfig, err := os.ReadFile(filepath.Join("..", "..", "contracts", "g2-m1a", "pi-runtime-config.v6.json"))
	if err != nil {
		t.Fatal(err)
	}
	policy, err := os.ReadFile(filepath.Join("..", "..", "contracts", "g2-m1a", "sandbox-policy.v4.json"))
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	timeoutPolicy := acceptanceTimeoutPolicyFor(nil)
	resolver, _ := pidistribution.NewResolver(pidistribution.Config{})
	if _, err := composePiRuntime(context.Background(), filepath.Join(root, "runtime"), filepath.Join(root, "artifacts"), root, runtimeConfig, policy, ProductOptions{LocalPiResolver: resolver}, true, timeoutPolicy); err == nil {
		t.Fatal("partial local activation was accepted")
	}
}

func TestComposeSourceCheckoutJoinsQualifiedIdentityToDockerWithoutHostFallback(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runtimeConfig, err := os.ReadFile(filepath.Join("..", "..", "contracts", "g2-m1a", "pi-runtime-config.v6.json"))
	if err != nil {
		t.Fatal(err)
	}
	policy, err := os.ReadFile(filepath.Join("..", "..", "contracts", "g2-m1a", "sandbox-policy.v4.json"))
	if err != nil {
		t.Fatal(err)
	}
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	identity := sourceCheckoutTestEngineIdentity(t, "source-checkout-daemon")
	contract, err := dockersupervisor.NewCapabilityProbeContract(agentpi.AttemptImageID, agentpi.PolicySHA256)
	if err != nil {
		t.Fatal(err)
	}
	qualification := testEngineQualification(t, identity, contract)
	resolver, selection := sourceCheckoutTestLocalSelection(t, base)
	newOptions := func(runner *sourceCheckoutJoinedRunner) ProductOptions {
		return ProductOptions{
			SourceCheckout: true, DockerRunner: runner, DockerEngineIdentity: identity,
			DockerCapability: contract, DockerQualification: qualification,
			PiAuthFile: filepath.Join(base, "auth.json"), LocalPiResolver: resolver, LocalPiSelection: selection,
		}
	}
	compose := func(name string, options ProductOptions) (piComposition, error) {
		return composePiRuntime(context.Background(), filepath.Join(base, name, "runtime"), filepath.Join(base, name, "artifacts"),
			repoRoot, runtimeConfig, policy, options, true, acceptanceTimeoutPolicyFor(nil))
	}

	runner := &sourceCheckoutJoinedRunner{identity: identity}
	composition, err := compose("valid", newOptions(runner))
	if err != nil {
		t.Fatal(err)
	}
	if composition.dockerSupervisor == nil || composition.trustedSupervisor != nil || composition.adapter == nil {
		t.Fatalf("SourceCheckout composition selected a non-Docker route: %#v", composition)
	}
	status, err := sourceCheckoutPiRuntimeStatus(piCompositionStatus(composition), newOptions(runner))
	if err != nil {
		t.Fatal(err)
	}
	if status.EngineIdentityDigest != identity.Digest() || status.DockerContext != identity.ContextName() ||
		status.ContextEndpointDigest != identity.ContextEndpointDigest() || status.DockerServerVersion != identity.EngineVersion() ||
		status.DockerVersion != identity.EngineVersion() {
		t.Fatalf("SourceCheckout status did not derive from the qualified identity: %#v", status)
	}

	// Exercise the composed seam, not an independently paired Adapter and
	// Supervisor. The deliberately incomplete contract must be rejected only
	// after the exact profile and managed Runtime-source identities agree.
	run, attempt, snapshot := piWorkspaceTestRun(t)
	launch := execution.LaunchToken{Value: "source-checkout-composed-runtime-source"}
	invocation, err := composition.adapter.PrepareStart(context.Background(), execution.StartRequest{
		Run: run, Attempt: attempt, SnapshotDocument: snapshot,
		ExecutionContractDocument: []byte(`{"frozen":true}`),
		WorkspaceRoot:             base, LaunchToken: launch,
	})
	if err != nil {
		t.Fatal(err)
	}
	outcome := composition.dockerSupervisor.Start(context.Background(), invocation, routingTestSink{launch: launch})
	if outcome.Kind != execution.StartProvenNoChild || outcome.Diagnostic != "stdin prompt does not contain matching frozen Snapshot and Active Contract authority" {
		t.Fatalf("composed SourceCheckout Runtime identity did not reach the post-identity boundary: %#v", outcome)
	}

	t.Run("qualification mismatch", func(t *testing.T) {
		mismatched := newOptions(&sourceCheckoutJoinedRunner{identity: identity})
		other := sourceCheckoutTestEngineIdentity(t, "other-daemon")
		mismatched.DockerQualification = testEngineQualification(t, other, contract)
		failed, err := compose("qualification-mismatch", mismatched)
		if err == nil || failed.dockerSupervisor != nil || failed.trustedSupervisor != nil {
			t.Fatalf("qualification mismatch selected an execution route: composition=%#v err=%v", failed, err)
		}
	})

	t.Run("Engine observation failure", func(t *testing.T) {
		failed, err := compose("engine-failure", newOptions(&sourceCheckoutJoinedRunner{identity: identity, failEngine: true}))
		if err == nil || failed.dockerSupervisor != nil || failed.trustedSupervisor != nil {
			t.Fatalf("Engine failure selected an execution route: composition=%#v err=%v", failed, err)
		}
	})
}

func sourceCheckoutTestEngineIdentity(t *testing.T, daemonID string) dockersupervisor.EngineIdentity {
	t.Helper()
	endpointDigest, err := dockersupervisor.DigestContextEndpoint("unix:///tmp/chora-source-checkout-test.sock")
	if err != nil {
		t.Fatal(err)
	}
	identity, err := dockersupervisor.NewEngineIdentity(dockersupervisor.EngineIdentityInput{
		DaemonID: daemonID, APIVersion: "1.47", OperatingSystem: "linux", Architecture: "arm64",
		ContextEndpointDigest: endpointDigest, ProviderName: "source-checkout-test", EngineVersion: "29.6.1", ContextName: "source-checkout-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func sourceCheckoutTestLocalSelection(t *testing.T, base string) (*pidistribution.Resolver, pidistribution.Selection) {
	t.Helper()
	packageRoot := filepath.Join(base, "local-pi-package")
	executable := filepath.Join(packageRoot, "bin", "pi")
	if err := os.MkdirAll(filepath.Dir(executable), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packageRoot, "package.json"), []byte(`{"name":"@earendil-works/pi-coding-agent","version":"0.84.2"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("source-checkout ignored host fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	resolver, err := pidistribution.NewResolver(pidistribution.Config{
		LookPath: func(string) (string, error) { return executable, nil },
		RunVersion: func(context.Context, string, ...string) (pidistribution.VersionResult, error) {
			return pidistribution.VersionResult{Stdout: []byte(pidistribution.SupportedVersion + "\n")}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	selection, err := resolver.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return resolver, selection
}

type sourceCheckoutJoinedRunner struct {
	identity   dockersupervisor.EngineIdentity
	failEngine bool
	commands   [][]string
}

func (runner *sourceCheckoutJoinedRunner) Run(_ context.Context, command dockersupervisor.Command) (dockersupervisor.CommandResult, error) {
	runner.commands = append(runner.commands, slices.Clone(command.Args))
	switch {
	case len(command.Args) > 0 && command.Args[0] == "version":
		if runner.failEngine {
			return dockersupervisor.CommandResult{ExitCode: 1, Stderr: []byte("injected Engine observation failure")}, nil
		}
		return dockersupervisor.CommandResult{Stdout: []byte(fmt.Sprintf(`{"api_version":%q,"server_version":%q,"operating_system":%q,"architecture":%q}`,
			runner.identity.APIVersion(), runner.identity.EngineVersion(), runner.identity.OperatingSystem(), runner.identity.Architecture()))}, nil
	case len(command.Args) > 0 && command.Args[0] == "info":
		return dockersupervisor.CommandResult{Stdout: []byte(fmt.Sprintf(`{"daemon_id":%q,"provider_name":%q}`, runner.identity.DaemonID(), runner.identity.ProviderName()))}, nil
	case slices.Equal(command.Args, []string{"context", "show"}):
		return dockersupervisor.CommandResult{Stdout: []byte(runner.identity.ContextName() + "\n")}, nil
	case len(command.Args) > 1 && command.Args[0] == "context" && command.Args[1] == "inspect":
		return dockersupervisor.CommandResult{Stdout: []byte(`"unix:///tmp/chora-source-checkout-test.sock"`)}, nil
	case len(command.Args) == 5 && slices.Equal(command.Args[:4], []string{"image", "inspect", "--format", "{{.Id}}"}):
		if !strings.HasPrefix(command.Args[4], "sha256:") {
			return dockersupervisor.CommandResult{ExitCode: 1}, nil
		}
		return dockersupervisor.CommandResult{Stdout: []byte(command.Args[4] + "\n")}, nil
	default:
		return dockersupervisor.CommandResult{}, nil
	}
}

func (*sourceCheckoutJoinedRunner) Start(context.Context, dockersupervisor.Command) (dockersupervisor.Process, error) {
	return nil, errors.New("unexpected direct process start")
}
