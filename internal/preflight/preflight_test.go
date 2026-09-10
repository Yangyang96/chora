package preflight

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/dockersupervisor"
	"github.com/Yangyang96/chora/internal/pidistribution"
	"github.com/Yangyang96/chora/internal/sourcebundle"
)

func supportedConfig() Config {
	return Config{
		SourceRoot: "/source", InstallRoot: "/install", DataRoot: "/data",
		AuthFile: "/auth.json", CAFile: "/ca.pem", ProxyURL: FixedProxyURL,
		ModelURL: AllowedModelURL, Port: 8787, SourceManifest: "/source/source-manifest.json",
		BundleAggregate: strings.Repeat("b", 64),
	}
}

func supportedInstalledConfig() Config {
	config := supportedConfig()
	config.SourceRoot = config.InstallRoot
	config.SourceManifest = filepath.Join(config.InstallRoot, sourcebundle.ManifestName)
	return config
}

func supportedProbes(t *testing.T) (Probes, *[][]string) {
	t.Helper()
	var commands [][]string
	outputs := map[string]string{
		"docker version --format {{.Client.Version}}|{{.Server.Version}}": "29.6.1|29.6.1",
		"docker context show": "colima",
		"colima version":      "colima version 0.10.3",
		"colima list --json":  `{"name":"default","status":"Running","arch":"aarch64","cpus":2,"memory":6442450944,"disk":107374182400,"runtime":"docker"}`,
		"docker image inspect sha256:91698efead5641a633519f5f229373e08a59264045ca27f6d01fc06505deeea7 --format {{.Id}}|{{.Os}}/{{.Architecture}}": "sha256:91698efead5641a633519f5f229373e08a59264045ca27f6d01fc06505deeea7|linux/arm64",
		"docker image inspect sha256:4f7746f3cdbe55dc454775ead5958a9ed8a78b93776ea1df598255c1606b25c6 --format {{.Id}}|{{.Os}}/{{.Architecture}}": "sha256:4f7746f3cdbe55dc454775ead5958a9ed8a78b93776ea1df598255c1606b25c6|linux/arm64",
		"go version":     "go version go1.26.0 darwin/arm64",
		"node --version": "v22.12.0",
		"npm --version":  "11.0.0",
		"pi --version":   "0.84.2",
		"docker ps -a --filter label=chora.owner --format {{.ID}}":               "",
		"docker ps -a --filter label=chora.verifier_runtime_id --format {{.ID}}": "",
		"docker network ls --filter label=chora.owner --format {{.ID}}":          "",
	}
	probes := Probes{
		Host: func() (string, string) { return "darwin", "arm64" },
		Command: func(_ context.Context, name string, arguments ...string) CommandResult {
			command := append([]string{name}, arguments...)
			commands = append(commands, command)
			return CommandResult{Stdout: outputs[strings.Join(command, " ")]}
		},
		File: func(path string) FileMetadata {
			switch path {
			case "/source":
				return FileMetadata{Exists: true, Directory: true, Owner: true, Mode: 0o700}
			case "/auth.json":
				return FileMetadata{Exists: true, Owner: true, Mode: 0o600}
			case "/ca.pem":
				return FileMetadata{Exists: true, Owner: true, Mode: 0o600}
			default:
				return FileMetadata{}
			}
		},
		ReadFile: func(path string) ([]byte, error) {
			if path == "/auth.json" {
				return []byte(`{"openai-codex":{"access":"top-secret","accountId":"acct-safe"}}`), nil
			}
			return []byte("test-ca"), nil
		},
		Digest:        func(string) (string, error) { return RequiredCA256, nil },
		Proxy:         func(context.Context, ProxyRequest) CommandResult { return CommandResult{Stdout: "200"} },
		Model:         func(context.Context, ModelRequest) CommandResult { return CommandResult{Stdout: "400"} },
		DiskFree:      func(string) (uint64, error) { return 30 << 30, nil },
		PortAvailable: func(int) bool { return true },
		Bundle: func(_ context.Context, request BundleRequest) BundleResult {
			return BundleResult{Aggregate: request.ExpectedAggregate, Valid: true}
		},
		DataIdentity: func(Config) error { return nil },
		Residue:      func(context.Context, Config) ResidueResult { return ResidueResult{} },
		Now:          time.Now,
	}
	return probes, &commands
}

func TestRunRejectsSourceBundleAggregateMismatch(t *testing.T) {
	probes, _ := supportedProbes(t)
	probes.Bundle = func(context.Context, BundleRequest) BundleResult {
		return BundleResult{Aggregate: strings.Repeat("c", 64)}
	}
	report := Run(context.Background(), supportedConfig(), probes)
	if report.Failure == nil || report.Failure.Boundary != "source.bundle" {
		t.Fatalf("failure = %#v, want source.bundle", report.Failure)
	}
}

func TestRunRejectsWrongPinnedCA(t *testing.T) {
	probes, _ := supportedProbes(t)
	modelCalls := 0
	probes.Model = func(context.Context, ModelRequest) CommandResult {
		modelCalls++
		return CommandResult{Stdout: "200"}
	}
	probes.Digest = func(string) (string, error) { return "wrong", nil }

	report := Run(context.Background(), supportedConfig(), probes)

	if report.Failure == nil || report.Failure.Boundary != "trust.ca" || modelCalls != 0 || report.ModelProbe != nil {
		t.Fatalf("failure = %#v, want trust.ca", report.Failure)
	}
}

func TestRunRejectsUndeclaredModelDestinationBeforeNetwork(t *testing.T) {
	probes, _ := supportedProbes(t)
	called := false
	probes.Model = func(context.Context, ModelRequest) CommandResult {
		called = true
		return CommandResult{Stdout: "200"}
	}
	config := supportedConfig()
	config.ModelURL = "https://example.com/steal"

	report := Run(context.Background(), config, probes)

	if report.Failure == nil || report.Failure.Boundary != "model.destination" || called || report.ModelProbe != nil {
		t.Fatalf("failure/called = %#v/%t, want model.destination/false", report.Failure, called)
	}
}

func TestRunRedactsCredentialsFromWrongProxyURL(t *testing.T) {
	probes, _ := supportedProbes(t)
	config := supportedConfig()
	config.ProxyURL = "http://operator:top-secret@evil.example:9981/path?token=top-secret"
	report := Run(context.Background(), config, probes)
	if report.Failure == nil || report.Failure.Boundary != "proxy.route" {
		t.Fatalf("failure = %#v, want proxy.route", report.Failure)
	}
	encoded, _ := json.Marshal(report)
	if strings.Contains(string(encoded), "top-secret") || strings.Contains(string(encoded), "operator") {
		t.Fatalf("report leaked URL credentials: %s", encoded)
	}
}

func TestRunStopsAtFixedProxyReachability(t *testing.T) {
	probes, _ := supportedProbes(t)
	probes.Proxy = func(context.Context, ProxyRequest) CommandResult { return CommandResult{Err: context.DeadlineExceeded} }
	report := Run(context.Background(), supportedConfig(), probes)
	if report.Failure == nil || report.Failure.Boundary != "proxy.reachability" {
		t.Fatalf("failure = %#v, want proxy.reachability", report.Failure)
	}
}

func TestRunRejectsProxyAuthenticationAndUpstreamFailures(t *testing.T) {
	for _, status := range []string{"407", "500"} {
		t.Run(status, func(t *testing.T) {
			probes, _ := supportedProbes(t)
			probes.Proxy = func(context.Context, ProxyRequest) CommandResult { return CommandResult{Stdout: status} }
			report := Run(context.Background(), supportedConfig(), probes)
			if report.Failure == nil || report.Failure.Boundary != "proxy.reachability" {
				t.Fatalf("failure = %#v, want proxy.reachability", report.Failure)
			}
		})
	}
}

func TestRunRejectsModelAuthenticationFailure(t *testing.T) {
	probes, _ := supportedProbes(t)
	calls := 0
	probes.Model = func(context.Context, ModelRequest) CommandResult {
		calls++
		return CommandResult{Stdout: "401"}
	}
	report := Run(context.Background(), supportedConfig(), probes)
	if report.Failure == nil || report.Failure.Boundary != "model.reachability" || calls != 1 {
		t.Fatalf("failure = %#v, want model.reachability", report.Failure)
	}
	want := []ModelProbeAttempt{{Attempt: 1, Outcome: "http_permanent_failure", StatusCode: 401}}
	if report.ModelProbe == nil || report.ModelProbe.MaxAttempts != 3 || !reflect.DeepEqual(report.ModelProbe.Attempts, want) {
		t.Fatalf("model probe audit = %#v, want %#v", report.ModelProbe, want)
	}
}

func TestRunRejectsNonContractModelStatuses(t *testing.T) {
	for _, status := range []string{"403", "404", "407"} {
		t.Run(status, func(t *testing.T) {
			probes, _ := supportedProbes(t)
			calls := 0
			probes.Model = func(context.Context, ModelRequest) CommandResult {
				calls++
				return CommandResult{Stdout: status}
			}
			report := Run(context.Background(), supportedConfig(), probes)
			if report.Failure == nil || report.Failure.Boundary != "model.reachability" || calls != 1 {
				t.Fatalf("failure = %#v, want model.reachability", report.Failure)
			}
		})
	}
}

func TestRunRetriesOnlyTransientModelFailuresAndAuditsAttempts(t *testing.T) {
	probes, _ := supportedProbes(t)
	results := []CommandResult{
		{Err: context.DeadlineExceeded, RetryableTransport: true, Stderr: "must-not-serialize top-secret"},
		{Stdout: "503", Stderr: "must-not-serialize top-secret"},
		{Stdout: "400"},
	}
	calls := 0
	probes.Model = func(context.Context, ModelRequest) CommandResult {
		result := results[calls]
		calls++
		return result
	}

	report := Run(context.Background(), supportedConfig(), probes)

	if report.Status != StatusPassed || calls != 3 {
		t.Fatalf("report/calls = %#v/%d, want passed/3", report, calls)
	}
	want := []ModelProbeAttempt{
		{Attempt: 1, Outcome: "transport_error", Retryable: true, Retried: true},
		{Attempt: 2, Outcome: "http_5xx", StatusCode: 503, Retryable: true, Retried: true},
		{Attempt: 3, Outcome: "authenticated_schema", StatusCode: 400},
	}
	if report.ModelProbe == nil || report.ModelProbe.MaxAttempts != 3 || !reflect.DeepEqual(report.ModelProbe.Attempts, want) {
		t.Fatalf("model probe audit = %#v, want %#v", report.ModelProbe, want)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "top-secret") || strings.Contains(string(encoded), "must-not-serialize") {
		t.Fatalf("model probe audit leaked diagnostic content: %s", encoded)
	}

	baselineProbes, _ := supportedProbes(t)
	baseline := Run(context.Background(), supportedConfig(), baselineProbes)
	if report.InputFingerprint != baseline.InputFingerprint {
		t.Fatalf("transient retry changed input fingerprint: %q != %q", report.InputFingerprint, baseline.InputFingerprint)
	}
}

func TestRunStopsAfterThreeTransientModelAttempts(t *testing.T) {
	probes, _ := supportedProbes(t)
	calls := 0
	probes.Model = func(context.Context, ModelRequest) CommandResult {
		calls++
		return CommandResult{Stdout: "500"}
	}

	report := Run(context.Background(), supportedConfig(), probes)

	if report.Failure == nil || report.Failure.Boundary != "model.reachability" || calls != 3 {
		t.Fatalf("report/calls = %#v/%d, want model.reachability/3", report, calls)
	}
	want := []ModelProbeAttempt{
		{Attempt: 1, Outcome: "http_5xx", StatusCode: 500, Retryable: true, Retried: true},
		{Attempt: 2, Outcome: "http_5xx", StatusCode: 500, Retryable: true, Retried: true},
		{Attempt: 3, Outcome: "http_5xx", StatusCode: 500, Retryable: true},
	}
	if report.ModelProbe == nil || !reflect.DeepEqual(report.ModelProbe.Attempts, want) {
		t.Fatalf("model probe audit = %#v, want %#v", report.ModelProbe, want)
	}
}

func TestRunCancelsTransientRetryWithinDeadline(t *testing.T) {
	probes, _ := supportedProbes(t)
	calls := 0
	probes.Model = func(context.Context, ModelRequest) CommandResult {
		calls++
		return CommandResult{Err: errors.New("temporary transport failure"), RetryableTransport: true}
	}
	config := supportedConfig()
	config.Deadline = 20 * time.Millisecond
	started := time.Now()

	report := Run(context.Background(), config, probes)

	if report.Failure == nil || report.Failure.Boundary != "model.reachability" || calls != 1 || time.Since(started) >= time.Second {
		t.Fatalf("report/calls/elapsed = %#v/%d/%s", report, calls, time.Since(started))
	}
	want := []ModelProbeAttempt{{Attempt: 1, Outcome: "transport_error", Retryable: true}}
	if report.ModelProbe == nil || !reflect.DeepEqual(report.ModelProbe.Attempts, want) {
		t.Fatalf("model probe audit = %#v, want %#v", report.ModelProbe, want)
	}
}

func TestRunDoesNotRetryPermanentModelProbeErrors(t *testing.T) {
	probes, _ := supportedProbes(t)
	calls := 0
	probes.Model = func(context.Context, ModelRequest) CommandResult {
		calls++
		return CommandResult{Err: errors.New("pinned CA or endpoint identity failure")}
	}

	report := Run(context.Background(), supportedConfig(), probes)

	want := []ModelProbeAttempt{{Attempt: 1, Outcome: "permanent_probe_error"}}
	if report.Failure == nil || report.Failure.Boundary != "model.reachability" || calls != 1 || report.ModelProbe == nil || !reflect.DeepEqual(report.ModelProbe.Attempts, want) {
		t.Fatalf("report/calls/audit = %#v/%d/%#v", report, calls, report.ModelProbe)
	}
}

func TestRunAcceptsAuthenticatedQuotaWithoutRetry(t *testing.T) {
	probes, _ := supportedProbes(t)
	calls := 0
	probes.Model = func(context.Context, ModelRequest) CommandResult {
		calls++
		return CommandResult{Stdout: "429"}
	}

	report := Run(context.Background(), supportedConfig(), probes)

	want := []ModelProbeAttempt{{Attempt: 1, Outcome: "authenticated_quota", StatusCode: 429}}
	if report.Status != StatusPassed || calls != 1 || report.ModelProbe == nil || !reflect.DeepEqual(report.ModelProbe.Attempts, want) {
		t.Fatalf("report/calls/audit = %#v/%d/%#v", report, calls, report.ModelProbe)
	}
}

func TestRetryableHTTPTransportErrorClassification(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "connection", err: errors.New("connection reset"), want: true},
		{name: "request-timeout", err: context.DeadlineExceeded, want: true},
		{name: "canceled", err: context.Canceled},
		{name: "redirect-endpoint", err: errUndeclaredRedirectDestination},
		{name: "certificate", err: &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := retryableHTTPTransportError(context.Background(), test.err); got != test.want {
				t.Fatalf("retryableHTTPTransportError() = %t, want %t", got, test.want)
			}
		})
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if retryableHTTPTransportError(canceled, errors.New("connection reset")) {
		t.Fatal("parent cancellation must not be retryable")
	}
}

func TestRunPreservesContainerProxyIdentityButDialsHostProbeRoute(t *testing.T) {
	probes, _ := supportedProbes(t)
	var got ProxyRequest
	probes.Proxy = func(_ context.Context, request ProxyRequest) CommandResult {
		got = request
		return CommandResult{Stdout: "200"}
	}
	report := Run(context.Background(), supportedConfig(), probes)
	if report.Status != StatusPassed {
		t.Fatalf("report = %#v", report)
	}
	if got.ContractProxyURL != FixedProxyURL || got.DialProxyURL != HostProxyProbeURL {
		t.Fatalf("proxy identities = %#v", got)
	}
}

func TestRunStopsAtToolchainMismatch(t *testing.T) {
	probes, _ := supportedProbes(t)
	original := probes.Command
	probes.Command = func(ctx context.Context, name string, args ...string) CommandResult {
		if name == "node" {
			return CommandResult{Stdout: "v20.0.0"}
		}
		return original(ctx, name, args...)
	}
	report := Run(context.Background(), supportedConfig(), probes)
	if report.Failure == nil || report.Failure.Boundary != "tool.node" {
		t.Fatalf("failure = %#v, want tool.node", report.Failure)
	}
}

func TestRunStopsAtUnsafeInstallRoot(t *testing.T) {
	probes, _ := supportedProbes(t)
	original := probes.File
	probes.File = func(path string) FileMetadata {
		if path == "/install" {
			return FileMetadata{Exists: true, Directory: true, Owner: true, Mode: 0o700}
		}
		return original(path)
	}
	report := Run(context.Background(), supportedConfig(), probes)
	if report.Failure == nil || report.Failure.Boundary != "root.install" {
		t.Fatalf("failure = %#v, want root.install", report.Failure)
	}
}

func TestRunRejectsFreshRootThroughSymlinkAncestor(t *testing.T) {
	probes, _ := supportedProbes(t)
	original := probes.File
	probes.File = func(path string) FileMetadata {
		if path == "/install" {
			return FileMetadata{Symlink: true}
		}
		return original(path)
	}
	report := Run(context.Background(), supportedConfig(), probes)
	if report.Failure == nil || report.Failure.Boundary != "root.install" {
		t.Fatalf("failure = %#v, want root.install", report.Failure)
	}
}

func TestRunRejectsNestedRoots(t *testing.T) {
	probes, _ := supportedProbes(t)
	config := supportedConfig()
	config.InstallRoot = "/source/install"
	report := Run(context.Background(), config, probes)
	if report.Failure == nil || report.Failure.Boundary != "root.inputs" {
		t.Fatalf("failure = %#v, want root.inputs", report.Failure)
	}
}

func TestInstalledDoctorAcceptsExactInstallRootAsInstalledSource(t *testing.T) {
	probes, _ := supportedProbes(t)
	runtime, _ := qualifiedRuntimeFixture(t, "Colima", "29.6.1", "chora-o4")
	probes.QualifiedRuntime = &runtime
	originalFile := probes.File
	probes.File = func(path string) FileMetadata {
		if path == "/install" || path == "/data" {
			return FileMetadata{Exists: true, Directory: true, Owner: true, Mode: 0o700}
		}
		return originalFile(path)
	}
	config := supportedConfig()
	config.Mode = ModeInstalledDoctor
	config.SourceRoot = config.InstallRoot
	config.SourceManifest = filepath.Join(config.InstallRoot, sourcebundle.ManifestName)

	report := Run(context.Background(), config, probes)
	if report.Status != StatusPassed {
		t.Fatalf("installed-root source report = %#v", report)
	}
}

func TestInstalledSourceAliasRemainsModeBoundAndFailClosed(t *testing.T) {
	for _, mode := range []Mode{ModeInstalledDoctor, ModeServiceRecovery, ModeServiceStart, ModePreAttempt} {
		if !validRootInputs(mode, "/install", "/install", "/data", "/auth.json", "/ca.pem") {
			t.Fatalf("installed source alias rejected for mode %q", mode)
		}
		if validRootInputs(mode, "/source", "/install", "/data", "/auth.json", "/ca.pem") {
			t.Fatalf("detached installed source accepted for mode %q", mode)
		}
	}
	for _, mode := range []Mode{ModeFreshInstall, Mode("unknown")} {
		if validRootInputs(mode, "/install", "/install", "/data", "/auth.json", "/ca.pem") {
			t.Fatalf("installed source alias accepted for mode %q", mode)
		}
	}
	for _, dataRoot := range []string{"/install/data", "/"} {
		if validRootInputs(ModeInstalledDoctor, "/install", "/install", dataRoot, "/auth.json", "/ca.pem") {
			t.Fatalf("unsafe installed data root accepted: %q", dataRoot)
		}
	}
	if validRootInputs(ModeInstalledDoctor, "/install", "/install", "/data", "/install/auth.json", "/ca.pem") {
		t.Fatal("credential inside installed root was accepted")
	}
}

func TestRunStopsAtLowDiskCapacity(t *testing.T) {
	probes, _ := supportedProbes(t)
	probes.DiskFree = func(string) (uint64, error) { return 10 << 30, nil }
	report := Run(context.Background(), supportedConfig(), probes)
	if report.Failure == nil || report.Failure.Boundary != "disk.capacity" {
		t.Fatalf("failure = %#v, want disk.capacity", report.Failure)
	}
}

func TestRunStopsAtUnavailablePort(t *testing.T) {
	probes, _ := supportedProbes(t)
	probes.PortAvailable = func(int) bool { return false }
	report := Run(context.Background(), supportedConfig(), probes)
	if report.Failure == nil || report.Failure.Boundary != "port.loopback" {
		t.Fatalf("failure = %#v, want port.loopback", report.Failure)
	}
}

func TestRunStopsAtOwnedResidue(t *testing.T) {
	probes, _ := supportedProbes(t)
	original := probes.Command
	probes.Command = func(ctx context.Context, name string, args ...string) CommandResult {
		if name == "docker" && len(args) > 0 && args[0] == "ps" {
			return CommandResult{Stdout: "owned-container-id"}
		}
		return original(ctx, name, args...)
	}
	report := Run(context.Background(), supportedConfig(), probes)
	if report.Failure == nil || report.Failure.Boundary != "residue.container" {
		t.Fatalf("failure = %#v, want residue.container", report.Failure)
	}
}

func TestRunStopsAtOwnedWorkspaceResidue(t *testing.T) {
	probes, _ := supportedProbes(t)
	probes.Residue = func(context.Context, Config) ResidueResult { return ResidueResult{AgentWorkspaces: 1} }
	report := Run(context.Background(), supportedConfig(), probes)
	if report.Failure == nil || report.Failure.Boundary != "residue.workspace" {
		t.Fatalf("failure = %#v, want residue.workspace", report.Failure)
	}
}

func TestRunSuccessHasStableSecretSafeFingerprint(t *testing.T) {
	probes, _ := supportedProbes(t)
	report := Run(context.Background(), supportedConfig(), probes)
	if report.Status != StatusPassed || report.Failure != nil || report.ResourcesCreated {
		t.Fatalf("report = %#v, want safe pass", report)
	}
	if len(report.InputFingerprint) != 64 || report.Elapsed <= 0 || strings.Contains(report.InputFingerprint, "top-secret") {
		t.Fatalf("fingerprint/elapsed = %q/%s", report.InputFingerprint, report.Elapsed)
	}
	config := supportedConfig()
	config.Port++
	drifted := Run(context.Background(), config, probes)
	if drifted.InputFingerprint == report.InputFingerprint {
		t.Fatal("input drift did not invalidate fingerprint")
	}
}

func TestQualifiedRuntimeIsProviderNeutralAndUsesOnlyExactRunnerEvidence(t *testing.T) {
	probes, baseCommands := supportedProbes(t)
	runtime, runner := qualifiedRuntimeFixture(t, "OrbStack", "30.1.0", "orbstack-chora")
	runtime.ManagedImageID = "sha256:" + strings.Repeat("c", 64)
	runtime.VerifierImageID = "sha256:" + strings.Repeat("e", 64)
	runtime.BoundaryImageID = "sha256:" + strings.Repeat("d", 64)
	probes.QualifiedRuntime = &runtime
	report := Run(context.Background(), supportedConfig(), probes)
	if report.Status != StatusPassed {
		t.Fatalf("qualified provider-neutral report = %#v", report)
	}
	for _, command := range *baseCommands {
		if len(command) > 0 && (command[0] == "docker" || command[0] == "colima" || command[0] == "pi") {
			t.Fatalf("qualified runtime used legacy/synthesized command: %v", command)
		}
	}
	wantImage := []string{"image", "inspect", runtime.ManagedImageID, "--format", "{{.Id}}|{{.Os}}/{{.Architecture}}"}
	wantVerifierImage := []string{"image", "inspect", runtime.VerifierImageID, "--format", "{{.Id}}|{{.Os}}/{{.Architecture}}"}
	wantBoundaryImage := []string{"image", "inspect", runtime.BoundaryImageID, "--format", "{{.Id}}|{{.Os}}/{{.Architecture}}"}
	wantProbeImage := []string{"image", "inspect", runtime.Contract.ProbeImageID(), "--format", "{{.Id}}|{{.Os}}/{{.Architecture}}"}
	wantResidue := []string{"ps", "-a", "--filter", "label=chora.owner", "--format", "{{.ID}}"}
	if !containsExactCommand(runner.commands, wantImage) || !containsExactCommand(runner.commands, wantVerifierImage) || !containsExactCommand(runner.commands, wantBoundaryImage) || !containsExactCommand(runner.commands, wantProbeImage) || !containsExactCommand(runner.commands, wantResidue) {
		t.Fatalf("exact Runner did not inspect pinned images/residue: %v", runner.commands)
	}
	for _, command := range runner.commands {
		if len(command) > 0 && command[0] == "colima" {
			t.Fatalf("qualified runtime synthesized provider call: %v", command)
		}
	}
	runner.failImage = true
	failedReport := Run(context.Background(), supportedConfig(), probes)
	if failedReport.Status != StatusFailed || failedReport.Failure == nil ||
		!strings.Contains(failedReport.Failure.Action, "qualified installed Engine") ||
		strings.Contains(failedReport.Failure.Action, "Colima") {
		t.Fatalf("qualified image remediation = %#v", failedReport.Failure)
	}
}

func containsExactCommand(commands [][]string, want []string) bool {
	for _, command := range commands {
		if slices.Equal(command, want) {
			return true
		}
	}
	return false
}

func qualifiedRuntimeFixture(t *testing.T, provider, serverVersion, contextName string) (QualifiedRuntime, *qualifiedRuntimeRunner) {
	t.Helper()
	endpoint := "unix:///private/runtime/engine.sock"
	endpointDigest, err := dockersupervisor.DigestContextEndpoint(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := dockersupervisor.NewEngineIdentity(dockersupervisor.EngineIdentityInput{
		DaemonID: "daemon-qualified", APIVersion: "1.49", OperatingSystem: "linux", Architecture: "arm64",
		ContextEndpointDigest: endpointDigest, ProviderName: provider, EngineVersion: serverVersion, ContextName: contextName,
	})
	if err != nil {
		t.Fatal(err)
	}
	contract, err := dockersupervisor.NewCapabilityProbeContract(AgentImageID, strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	completed := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	record := dockersupervisor.EngineQualificationRecord{
		SchemaVersion: "chora.docker-engine-qualification/v1", EngineIdentityDigest: identity.Digest(),
		ProbeContractDigest: contract.Digest(), ProbeImageID: contract.ProbeImageID(), SandboxPolicyDigest: contract.SandboxPolicyDigest(), CompletedAt: completed,
	}
	payload, _ := json.Marshal(struct {
		SchemaVersion        string `json:"schema_version"`
		EngineIdentityDigest string `json:"engine_identity_digest"`
		ProbeContractDigest  string `json:"probe_contract_digest"`
		ProbeImageID         string `json:"probe_image_id"`
		SandboxPolicyDigest  string `json:"sandbox_policy_digest"`
		CompletedAt          string `json:"completed_at"`
	}{record.SchemaVersion, record.EngineIdentityDigest, record.ProbeContractDigest, record.ProbeImageID, record.SandboxPolicyDigest, record.CompletedAt})
	record.QualificationDigest = fmt.Sprintf("%x", sha256.Sum256(payload))
	qualification, err := dockersupervisor.RestoreEngineQualification(record)
	if err != nil {
		t.Fatal(err)
	}
	selection := qualifiedPiSelection(t)
	runner := &qualifiedRuntimeRunner{
		endpoint: endpoint, provider: provider, serverVersion: serverVersion, contextName: contextName,
	}
	return QualifiedRuntime{
		Runner: runner, Identity: identity, Contract: contract, Qualification: qualification, PiSelection: selection,
		ManagedImageID: AgentImageID, VerifierImageID: AgentImageID, BoundaryImageID: BoundaryImageID,
		GenerationID: "generation-1", ReleaseID: "release-1", ManifestSHA256: strings.Repeat("f", 64),
	}, runner
}

func qualifiedPiSelection(t *testing.T) pidistribution.Selection {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	packageRoot := filepath.Join(root, "pi-package")
	if err := os.MkdirAll(filepath.Join(packageRoot, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(packageRoot, "lib"), 0o700); err != nil {
		t.Fatal(err)
	}
	packageJSON, _ := json.Marshal(map[string]any{"name": pidistribution.ExpectedPackageName, "version": pidistribution.SupportedVersion, "private": true})
	if err := os.WriteFile(filepath.Join(packageRoot, "package.json"), packageJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(packageRoot, "bin", "pi")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf '%s\\n' '"+pidistribution.SupportedVersion+"'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packageRoot, "lib", "dependency.js"), []byte("exact dependency"), 0o600); err != nil {
		t.Fatal(err)
	}
	selection, err := pidistribution.Resolve(context.Background(), pidistribution.Config{
		LookPath: func(string) (string, error) { return executable, nil },
		RunVersion: func(context.Context, string, ...string) (pidistribution.VersionResult, error) {
			return pidistribution.VersionResult{Stdout: []byte(pidistribution.SupportedVersion)}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return selection
}

type qualifiedRuntimeRunner struct {
	commands      [][]string
	endpoint      string
	provider      string
	serverVersion string
	contextName   string
	failImage     bool
}

func (runner *qualifiedRuntimeRunner) Run(_ context.Context, command dockersupervisor.Command) (dockersupervisor.CommandResult, error) {
	runner.commands = append(runner.commands, slices.Clone(command.Args))
	args := command.Args
	if len(args) == 0 {
		return dockersupervisor.CommandResult{ExitCode: 1}, errors.New("missing command")
	}
	switch args[0] {
	case "version":
		if len(args) >= 3 && strings.Contains(args[2], ".Client.Version") {
			return dockersupervisor.CommandResult{Stdout: []byte("29.6.1|" + runner.serverVersion), ExitCode: 0}, nil
		}
		return dockersupervisor.CommandResult{Stdout: []byte(`{"api_version":"1.49","server_version":"` + runner.serverVersion + `","operating_system":"linux","architecture":"arm64"}`), ExitCode: 0}, nil
	case "info":
		return dockersupervisor.CommandResult{Stdout: []byte(`{"daemon_id":"daemon-qualified","provider_name":"` + runner.provider + `"}`), ExitCode: 0}, nil
	case "context":
		if len(args) == 2 && args[1] == "show" {
			return dockersupervisor.CommandResult{Stdout: []byte(runner.contextName + "\n"), ExitCode: 0}, nil
		}
		encoded, _ := json.Marshal(runner.endpoint)
		return dockersupervisor.CommandResult{Stdout: encoded, ExitCode: 0}, nil
	case "image":
		if runner.failImage {
			return dockersupervisor.CommandResult{ExitCode: 1}, errors.New("qualified image unavailable")
		}
		return dockersupervisor.CommandResult{Stdout: []byte(args[2] + "|linux/arm64"), ExitCode: 0}, nil
	case "ps", "network":
		return dockersupervisor.CommandResult{ExitCode: 0}, nil
	default:
		return dockersupervisor.CommandResult{ExitCode: 1}, errors.New("unexpected exact Runner command")
	}
}

func (*qualifiedRuntimeRunner) Start(context.Context, dockersupervisor.Command) (dockersupervisor.Process, error) {
	return nil, errors.New("unexpected Start")
}

func TestRunFingerprintIsStableAcrossAcceptedModelStatuses(t *testing.T) {
	var fingerprint string
	for _, status := range []string{"200", "400", "409", "422", "429"} {
		probes, _ := supportedProbes(t)
		probes.Model = func(context.Context, ModelRequest) CommandResult { return CommandResult{Stdout: status} }
		report := Run(context.Background(), supportedConfig(), probes)
		if report.Status != StatusPassed {
			t.Fatalf("status %s report = %#v", status, report)
		}
		if fingerprint == "" {
			fingerprint = report.InputFingerprint
		} else if report.InputFingerprint != fingerprint {
			t.Fatalf("status %s changed fingerprint: %q != %q", status, report.InputFingerprint, fingerprint)
		}
	}
}

func TestRunRevalidationAllowsAcceptedModelStatusVariation(t *testing.T) {
	probes, _ := supportedProbes(t)
	probes.Model = func(context.Context, ModelRequest) CommandResult { return CommandResult{Stdout: "200"} }
	originalFile := probes.File
	probes.File = func(path string) FileMetadata {
		if path == "/install" || path == "/data" {
			return FileMetadata{Exists: true, Directory: true, Owner: true, Mode: 0o700}
		}
		return originalFile(path)
	}
	config := supportedInstalledConfig()
	config.Mode = ModeServiceStart
	baseline := run(context.Background(), config, probes)
	if baseline.Status != StatusPassed {
		t.Fatalf("installed baseline = %#v", baseline)
	}
	probes.Model = func(context.Context, ModelRequest) CommandResult { return CommandResult{Stdout: "429"} }
	config.PriorInputFingerprint = baseline.InputFingerprint

	revalidated := Run(context.Background(), config, probes)

	if revalidated.Status != StatusPassed || revalidated.InputFingerprint != baseline.InputFingerprint {
		t.Fatalf("revalidated/baseline = %#v/%#v", revalidated, baseline)
	}
}

func TestRunAllowsPinnedPublicCAInsideImmutableSourceBundle(t *testing.T) {
	probes, _ := supportedProbes(t)
	config := supportedConfig()
	config.CAFile = "/source/source/contracts/g2-m1a/starpoint-root-ca-2048-g2.pem"
	originalFile := probes.File
	probes.File = func(path string) FileMetadata {
		if path == config.CAFile {
			return FileMetadata{Exists: true, Owner: true, Mode: 0o444}
		}
		return originalFile(path)
	}
	report := Run(context.Background(), config, probes)
	if report.Status != StatusPassed {
		t.Fatalf("source-bundled CA report = %#v", report)
	}
}

func TestRunRejectsCredentialInsideSourceBundle(t *testing.T) {
	probes, _ := supportedProbes(t)
	config := supportedConfig()
	config.AuthFile = "/source/source/auth.json"
	originalFile := probes.File
	probes.File = func(path string) FileMetadata {
		if path == config.AuthFile {
			return FileMetadata{Exists: true, Owner: true, Mode: 0o600}
		}
		return originalFile(path)
	}
	originalRead := probes.ReadFile
	probes.ReadFile = func(path string) ([]byte, error) {
		if path == config.AuthFile {
			return []byte(`{"openai-codex":{"access":"top-secret","accountId":"acct-safe"}}`), nil
		}
		return originalRead(path)
	}
	report := Run(context.Background(), config, probes)
	if report.Failure == nil || report.Failure.Boundary != "root.inputs" {
		t.Fatalf("failure = %#v, want root.inputs", report.Failure)
	}
}

func TestRunRevalidatesServiceStartAndPreAttemptStages(t *testing.T) {
	probes, _ := supportedProbes(t)
	originalFile := probes.File
	probes.File = func(path string) FileMetadata {
		if path == "/install" || path == "/data" {
			return FileMetadata{Exists: true, Directory: true, Owner: true, Mode: 0o700}
		}
		return originalFile(path)
	}
	config := supportedInstalledConfig()
	config.Mode = ModeServiceStart
	baseline := run(context.Background(), config, probes)
	if baseline.Status != StatusPassed {
		t.Fatalf("installed baseline = %#v", baseline)
	}
	config.PriorInputFingerprint = baseline.InputFingerprint
	serviceStart := Run(context.Background(), config, probes)
	if serviceStart.Status != StatusPassed {
		t.Fatalf("service-start report = %#v", serviceStart)
	}
	config.Mode = ModePreAttempt
	config.ChoraOwnsPort = true
	probes.PortAvailable = func(int) bool { return false }
	preAttempt := Run(context.Background(), config, probes)
	if preAttempt.Status != StatusPassed {
		t.Fatalf("pre-attempt report = %#v", preAttempt)
	}
}

func TestRunServiceRecoveryAllowsOnlyCurrentInstallationScope(t *testing.T) {
	probes, _ := supportedProbes(t)
	originalFile := probes.File
	probes.File = func(path string) FileMetadata {
		if path == "/install" || path == "/data" {
			return FileMetadata{Exists: true, Directory: true, Owner: true, Mode: 0o700}
		}
		return originalFile(path)
	}
	originalCommand := probes.Command
	agentScope := recoveryScope("/data/runtime/pi")
	probes.Command = func(ctx context.Context, name string, args ...string) CommandResult {
		command := strings.Join(append([]string{name}, args...), " ")
		switch command {
		case "docker ps -a --filter label=chora.owner --format {{.ID}}":
			return CommandResult{Stdout: "owned-agent"}
		case "docker ps -a --filter label=chora.owner=dockersupervisor --filter label=chora.runtime_scope=" + agentScope + " --format {{.ID}}":
			return CommandResult{Stdout: "owned-agent"}
		default:
			return originalCommand(ctx, name, args...)
		}
	}
	probes.Residue = func(context.Context, Config) ResidueResult {
		return ResidueResult{AgentWorkspaces: 1}
	}
	config := supportedInstalledConfig()
	config.Mode = ModeServiceRecovery
	baseline := run(context.Background(), config, probes)
	if baseline.Status != StatusPassed {
		t.Fatalf("installed recovery baseline = %#v", baseline)
	}
	config.PriorInputFingerprint = baseline.InputFingerprint
	if report := Run(context.Background(), config, probes); report.Status != StatusPassed {
		t.Fatalf("scoped recovery report = %#v", report)
	}

	config.Mode = ModeServiceStart
	if report := Run(context.Background(), config, probes); report.Failure == nil || report.Failure.Boundary != "residue.container" {
		t.Fatalf("post-recovery report = %#v, want residue.container", report)
	}

	config.Mode = ModeServiceRecovery
	probes.Command = func(ctx context.Context, name string, args ...string) CommandResult {
		command := strings.Join(append([]string{name}, args...), " ")
		if command == "docker ps -a --filter label=chora.owner --format {{.ID}}" {
			return CommandResult{Stdout: "other-installation"}
		}
		return originalCommand(ctx, name, args...)
	}
	if report := Run(context.Background(), config, probes); report.Failure == nil || report.Failure.Boundary != "residue.container" {
		t.Fatalf("foreign-scope report = %#v, want residue.container", report)
	}
}

func TestInstalledDoctorIssuesNewFingerprintWithoutPriorFingerprint(t *testing.T) {
	probes, _ := supportedProbes(t)
	runtime, _ := qualifiedRuntimeFixture(t, "Colima", "29.6.1", "chora-o4")
	probes.QualifiedRuntime = &runtime
	originalFile := probes.File
	probes.File = func(path string) FileMetadata {
		if path == "/install" || path == "/data" {
			return FileMetadata{Exists: true, Directory: true, Owner: true, Mode: 0o700}
		}
		return originalFile(path)
	}
	config := supportedInstalledConfig()
	config.Mode = ModeInstalledDoctor
	config.SetupReceiptSHA256 = strings.Repeat("1", 64)
	config.ModelAuthoritySHA256 = strings.Repeat("2", 64)
	report := Run(context.Background(), config, probes)
	if report.Status != StatusPassed || report.InputFingerprint == "" || report.ResourcesCreated || report.InputEvidence == nil ||
		!validDigest(report.InputEvidence.AuthFileSHA256) || report.InputEvidence.CAFileSHA256 != RequiredCA256 ||
		!validDigest(report.InputEvidence.ProxyURLSHA256) || !validDigest(report.InputEvidence.ModelURLSHA256) {
		t.Fatalf("installed Doctor report = %#v", report)
	}
	probes.QualifiedRuntime.ReleaseID = "release-2"
	drifted := Run(context.Background(), config, probes)
	if drifted.Status != StatusPassed || drifted.InputFingerprint == report.InputFingerprint {
		t.Fatalf("release binding did not change installed Doctor fingerprint: %#v", drifted)
	}
}

func TestInstalledDoctorFingerprintContinuesIntoServiceModes(t *testing.T) {
	probes, _ := supportedProbes(t)
	runtime, _ := qualifiedRuntimeFixture(t, "Colima", "29.6.1", "chora-o4")
	probes.QualifiedRuntime = &runtime
	originalFile := probes.File
	probes.File = func(path string) FileMetadata {
		if path == "/install" || path == "/data" {
			return FileMetadata{Exists: true, Directory: true, Owner: true, Mode: 0o700}
		}
		return originalFile(path)
	}
	config := supportedInstalledConfig()
	config.Mode = ModeInstalledDoctor
	config.SetupReceiptSHA256 = strings.Repeat("1", 64)
	config.ModelAuthoritySHA256 = strings.Repeat("2", 64)
	doctor := Run(context.Background(), config, probes)
	if doctor.Status != StatusPassed {
		t.Fatalf("installed Doctor report = %#v", doctor)
	}

	// Serve reconstructs the common Preflight config and does not receive the
	// Doctor-only proof authorities. The stable continuation must still match.
	config.SetupReceiptSHA256 = ""
	config.ModelAuthoritySHA256 = ""
	for _, mode := range []Mode{ModeServiceRecovery, ModeServiceStart} {
		continued := Revalidate(context.Background(), config, probes, mode, doctor.InputFingerprint, false)
		if continued.Status != StatusPassed || continued.InputFingerprint != doctor.InputFingerprint {
			t.Fatalf("%s continuation = %#v", mode, continued)
		}
	}
	continued := Revalidate(context.Background(), config, probes, ModePreAttempt, doctor.InputFingerprint, true)
	if continued.Status != StatusPassed || continued.InputFingerprint != doctor.InputFingerprint {
		t.Fatalf("pre-attempt continuation = %#v", continued)
	}
}

func TestInstalledDoctorRejectsUnqualifiedAmbientRuntime(t *testing.T) {
	probes, _ := supportedProbes(t)
	originalFile := probes.File
	probes.File = func(path string) FileMetadata {
		if path == "/install" || path == "/data" {
			return FileMetadata{Exists: true, Directory: true, Owner: true, Mode: 0o700}
		}
		return originalFile(path)
	}
	config := supportedInstalledConfig()
	config.Mode = ModeInstalledDoctor
	report := Run(context.Background(), config, probes)
	if report.Status != StatusFailed || report.Failure == nil || report.Failure.Boundary != "runtime.qualification" {
		t.Fatalf("ambient installed Doctor report = %#v", report)
	}
}

func TestRunServiceRecoveryMatchesEveryRecoverableDockerBoundary(t *testing.T) {
	tests := []struct {
		name, global, scoped, boundary string
		residue                        ResidueResult
	}{
		{
			name: "verifier", boundary: "residue.verifier_container",
			global:  "docker ps -a --filter label=chora.verifier_runtime_id --format {{.ID}}",
			scoped:  "docker ps -a --filter label=chora.verifier_runtime_id=" + recoveryScope("/data/runtime") + " --format {{.ID}}",
			residue: ResidueResult{VerifierWorkspaces: 1},
		},
		{
			name: "agent network", boundary: "residue.network",
			global: "docker network ls --filter label=chora.owner --format {{.ID}}",
			scoped: "docker network ls --filter label=chora.owner=dockersupervisor --filter label=chora.runtime_scope=" + recoveryScope("/data/runtime/pi") + " --format {{.ID}}",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			probes, _ := supportedProbes(t)
			originalFile := probes.File
			probes.File = func(path string) FileMetadata {
				if path == "/install" || path == "/data" {
					return FileMetadata{Exists: true, Directory: true, Owner: true, Mode: 0o700}
				}
				return originalFile(path)
			}
			originalCommand := probes.Command
			probes.Command = func(ctx context.Context, name string, args ...string) CommandResult {
				command := strings.Join(append([]string{name}, args...), " ")
				if command == test.global || command == test.scoped {
					return CommandResult{Stdout: "current-resource"}
				}
				return originalCommand(ctx, name, args...)
			}
			probes.Residue = func(context.Context, Config) ResidueResult { return test.residue }
			config := supportedInstalledConfig()
			config.Mode = ModeServiceRecovery
			baseline := run(context.Background(), config, probes)
			if baseline.Status != StatusPassed {
				t.Fatalf("installed recovery baseline = %#v", baseline)
			}
			config.PriorInputFingerprint = baseline.InputFingerprint
			if report := Run(context.Background(), config, probes); report.Status != StatusPassed {
				t.Fatalf("scoped recovery report = %#v", report)
			}
			probes.Command = func(ctx context.Context, name string, args ...string) CommandResult {
				command := strings.Join(append([]string{name}, args...), " ")
				if command == test.global {
					return CommandResult{Stdout: "current-resource foreign-resource"}
				}
				if command == test.scoped {
					return CommandResult{Stdout: "current-resource"}
				}
				return originalCommand(ctx, name, args...)
			}
			if report := Run(context.Background(), config, probes); report.Failure == nil || report.Failure.Boundary != test.boundary {
				t.Fatalf("mixed-scope report = %#v, want %s", report, test.boundary)
			}
		})
	}
}

func TestRunInvalidatesPriorReportOnMutableInputDrift(t *testing.T) {
	probes, _ := supportedProbes(t)
	originalFile := probes.File
	probes.File = func(path string) FileMetadata {
		if path == "/install" || path == "/data" {
			return FileMetadata{Exists: true, Directory: true, Owner: true, Mode: 0o700}
		}
		return originalFile(path)
	}
	config := supportedInstalledConfig()
	config.Mode = ModeServiceStart
	baseline := run(context.Background(), config, probes)
	if baseline.Status != StatusPassed {
		t.Fatalf("installed baseline = %#v", baseline)
	}
	probes.ReadFile = func(path string) ([]byte, error) {
		if path == "/auth.json" {
			return []byte(`{"openai-codex":{"access":"refreshed-secret","accountId":"acct-safe"}}`), nil
		}
		return []byte("test-ca"), nil
	}
	config.PriorInputFingerprint = baseline.InputFingerprint
	report := Run(context.Background(), config, probes)
	if report.Failure == nil || report.Failure.Boundary != "input.drift" {
		t.Fatalf("failure = %#v, want input.drift", report.Failure)
	}
}

func TestDefaultProbesProvidesEveryProductionBoundary(t *testing.T) {
	probes := DefaultProbes()
	if probes.Host == nil || probes.Command == nil || probes.File == nil || probes.ReadFile == nil ||
		probes.Digest == nil || probes.Proxy == nil || probes.Model == nil || probes.DiskFree == nil || probes.PortAvailable == nil || probes.Bundle == nil || probes.DataIdentity == nil || probes.Residue == nil || probes.Now == nil {
		t.Fatalf("incomplete production probes = %#v", probes)
	}
}

func TestDefaultResidueProbeAllowsEmptyParentsAndFindsAttemptWorkspaces(t *testing.T) {
	dataRoot := t.TempDir()
	agentRoot := filepath.Join(dataRoot, "runtime", "pi")
	verifierRoot := filepath.Join(dataRoot, "runtime", "verification", "workspaces")
	for _, root := range []string{agentRoot, verifierRoot} {
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	probes := DefaultProbes()
	if result := probes.Residue(context.Background(), Config{DataRoot: dataRoot}); result.Err != nil || result.AgentWorkspaces != 0 || result.VerifierWorkspaces != 0 {
		t.Fatalf("empty residue result = %#v", result)
	}
	if err := os.Mkdir(filepath.Join(agentRoot, "chora-attempt"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(verifierRoot, "verification-attempt"), 0o700); err != nil {
		t.Fatal(err)
	}
	result := probes.Residue(context.Background(), Config{DataRoot: dataRoot})
	if result.Err != nil || result.AgentWorkspaces != 1 || result.VerifierWorkspaces != 1 {
		t.Fatalf("residue result = %#v", result)
	}
}

func TestDefaultBundleProbeVerifiesFreshAndInstalledTrees(t *testing.T) {
	root := t.TempDir()
	product := filepath.Join(root, "product")
	if err := os.Mkdir(product, 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		".npmrc":             "engine-strict=true\nregistry=https://registry.npmjs.org/\n",
		"README.md":          "install-only fixture\n",
		"go.mod":             "module github.com/Yangyang96/chora\n",
		"package.json":       "{\"name\":\"chora\",\"private\":true}\n",
		"vendor/modules.txt": "offline fixture\n",
	}
	for relative, body := range files {
		path := filepath.Join(product, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	declared := []string{".npmrc", "README.md", sourcebundle.SourcePathPolicyPath, "go.mod", "package.json", "vendor/modules.txt"}
	slices.Sort(declared)
	installOnly := []string{"README.md", "vendor/modules.txt"}
	modelReadable := []string{".npmrc", sourcebundle.SourcePathPolicyPath, "go.mod", "package.json"}
	slices.Sort(modelReadable)
	policy := struct {
		SchemaVersion            string   `json:"schema_version"`
		Roots                    []string `json:"roots"`
		ExcludedDirectoryNames   []string `json:"excluded_directory_names"`
		ExcludedFileSuffixes     []string `json:"excluded_file_suffixes"`
		DeclaredFiles            int      `json:"declared_files"`
		DeclaredPathsSHA256      string   `json:"declared_paths_sha256"`
		InstallOnlyRoots         []string `json:"install_only_roots"`
		InstallOnlyPaths         []string `json:"install_only_paths"`
		InstallOnlyFiles         int      `json:"install_only_files"`
		InstallOnlyPathsSHA256   string   `json:"install_only_paths_sha256"`
		ModelReadableFiles       int      `json:"model_readable_files"`
		ModelReadablePathsSHA256 string   `json:"model_readable_paths_sha256"`
	}{
		SchemaVersion:          sourcebundle.SourcePathPolicySchema,
		Roots:                  []string{".npmrc", "README.md", "distribution", "go.mod", "package.json", "vendor"},
		ExcludedDirectoryNames: []string{"dist"}, ExcludedFileSuffixes: []string{".tsbuildinfo"},
		DeclaredFiles: len(declared), DeclaredPathsSHA256: sourceBundleFixturePathsDigest(declared),
		InstallOnlyRoots: []string{"vendor"}, InstallOnlyPaths: []string{"README.md"},
		InstallOnlyFiles: len(installOnly), InstallOnlyPathsSHA256: sourceBundleFixturePathsDigest(installOnly),
		ModelReadableFiles: len(modelReadable), ModelReadablePathsSHA256: sourceBundleFixturePathsDigest(modelReadable),
	}
	encoded, err := json.MarshalIndent(policy, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	policyPath := filepath.Join(product, filepath.FromSlash(sourcebundle.SourcePathPolicyPath))
	if err := os.MkdirAll(filepath.Dir(policyPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policyPath, append(encoded, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	bundleRoot := filepath.Join(root, "bundle")
	manifest, err := sourcebundle.Create(product, bundleRoot)
	if err != nil {
		t.Fatal(err)
	}
	installRoot := filepath.Join(root, "install")
	if _, err := sourcebundle.Install(bundleRoot, installRoot); err != nil {
		t.Fatal(err)
	}
	probes := DefaultProbes()
	request := BundleRequest{Mode: ModeFreshInstall, SourceRoot: bundleRoot, InstallRoot: installRoot, SourceManifest: filepath.Join(bundleRoot, sourcebundle.ManifestName), ExpectedAggregate: manifest.AggregateSHA256}
	if result := probes.Bundle(context.Background(), request); !result.Valid || result.Err != nil {
		t.Fatalf("fresh bundle result = %#v", result)
	}
	request.Mode = ModeServiceStart
	request.SourceRoot = installRoot
	request.SourceManifest = filepath.Join(installRoot, sourcebundle.ManifestName)
	if result := probes.Bundle(context.Background(), request); !result.Valid || result.Err != nil {
		t.Fatalf("installed-root source bundle result = %#v", result)
	}
	request.SourceRoot = bundleRoot
	request.SourceManifest = filepath.Join(bundleRoot, sourcebundle.ManifestName)
	if result := probes.Bundle(context.Background(), request); result.Valid || result.Err == nil {
		t.Fatalf("detached installed source bundle passed = %#v", result)
	}
	request.SourceRoot = installRoot
	request.SourceManifest = filepath.Join(installRoot, sourcebundle.ManifestName)
	if err := os.WriteFile(filepath.Join(installRoot, sourcebundle.SourceDirName, "README.md"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if result := probes.Bundle(context.Background(), request); result.Valid || result.Err == nil {
		t.Fatalf("changed installed bundle passed = %#v", result)
	}
}

func sourceBundleFixturePathsDigest(paths []string) string {
	hash := sha256.New()
	for _, path := range paths {
		fmt.Fprintf(hash, "%s\n", path)
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func TestRunRejectsUnsafeOAuthFileWithoutLeakingValue(t *testing.T) {
	probes, _ := supportedProbes(t)
	probes.File = func(path string) FileMetadata {
		if path == "/auth.json" {
			return FileMetadata{Exists: true, Owner: true, Mode: 0o644}
		}
		return FileMetadata{Exists: true, Directory: path == "/source", Owner: true, Mode: 0o700}
	}

	report := Run(context.Background(), supportedConfig(), probes)

	if report.Failure == nil || report.Failure.Boundary != "oauth.file" {
		t.Fatalf("failure = %#v, want oauth.file", report.Failure)
	}
	encoded := report.Failure.Observed + report.Failure.Action
	if strings.Contains(encoded, "top-secret") {
		t.Fatal("report leaked OAuth value")
	}
}

func TestRunReportsDockerVersionMismatchBeforeAnyResource(t *testing.T) {
	var commands [][]string
	probes := Probes{
		Host: func() (string, string) { return "darwin", "arm64" },
		Command: func(_ context.Context, name string, arguments ...string) CommandResult {
			command := append([]string{name}, arguments...)
			commands = append(commands, command)
			return CommandResult{Stdout: "29.6.0|29.6.1"}
		},
	}

	report := Run(context.Background(), Config{}, probes)

	if report.Status != StatusFailed || report.ResourcesCreated {
		t.Fatalf("report status/resources = %q/%t, want failed/false", report.Status, report.ResourcesCreated)
	}
	if report.Failure == nil {
		t.Fatal("failure is nil")
	}
	want := Failure{
		Boundary: "docker.version",
		Observed: "client 29.6.0; server 29.6.1",
		Required: "Docker client and server 29.6.1",
		Action:   "Install Docker 29.6.1 and restart the Colima profile, then rerun chora doctor.",
	}
	if !reflect.DeepEqual(*report.Failure, want) {
		t.Fatalf("failure = %#v, want %#v", *report.Failure, want)
	}
	if got, wantCommands := commands, [][]string{{"docker", "version", "--format", "{{.Client.Version}}|{{.Server.Version}}"}}; !reflect.DeepEqual(got, wantCommands) {
		t.Fatalf("commands = %#v, want %#v", got, wantCommands)
	}
}

func TestRunStopsAtDockerContextMismatch(t *testing.T) {
	probes, commands := supportedProbes(t)
	original := probes.Command
	probes.Command = func(ctx context.Context, name string, args ...string) CommandResult {
		if name == "docker" && reflect.DeepEqual(args, []string{"context", "show"}) {
			return CommandResult{Stdout: "desktop-linux"}
		}
		return original(ctx, name, args...)
	}

	report := Run(context.Background(), Config{}, probes)

	if report.Failure == nil || report.Failure.Boundary != "docker.context" {
		t.Fatalf("failure = %#v, want docker.context", report.Failure)
	}
	if len(*commands) != 1 {
		t.Fatalf("commands before context failure = %#v", *commands)
	}
}

func TestRunStopsAtColimaCapacity(t *testing.T) {
	probes, _ := supportedProbes(t)
	original := probes.Command
	probes.Command = func(ctx context.Context, name string, args ...string) CommandResult {
		if name == "colima" && reflect.DeepEqual(args, []string{"list", "--json"}) {
			return CommandResult{Stdout: `{"name":"default","status":"Running","arch":"aarch64","cpus":1,"memory":2147483648,"disk":10737418240,"runtime":"docker"}`}
		}
		return original(ctx, name, args...)
	}

	report := Run(context.Background(), Config{}, probes)

	if report.Failure == nil || report.Failure.Boundary != "colima.capacity" {
		t.Fatalf("failure = %#v, want colima.capacity", report.Failure)
	}
	if report.ResourcesCreated {
		t.Fatal("preflight reported resources created")
	}
}

func TestRunStopsAtPinnedImageMismatch(t *testing.T) {
	probes, _ := supportedProbes(t)
	original := probes.Command
	probes.Command = func(ctx context.Context, name string, args ...string) CommandResult {
		if name == "docker" && len(args) > 2 && args[0] == "image" && args[2] == AgentImageID {
			return CommandResult{Stdout: "sha256:wrong|linux/arm64"}
		}
		return original(ctx, name, args...)
	}

	report := Run(context.Background(), Config{}, probes)

	if report.Failure == nil || report.Failure.Boundary != "image.agent" {
		t.Fatalf("failure = %#v, want image.agent", report.Failure)
	}
	if report.Failure != nil && strings.Contains(report.Failure.Observed, "wrong secret") {
		t.Fatal("failure leaked secret")
	}
}
