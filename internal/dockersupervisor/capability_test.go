package dockersupervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testProbeImage = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testPolicy     = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testProbeID    = "0123456789abcdef0123456789abcdef"
)

func TestEngineIdentityStableDigestRoundTripAndProviderAgnosticQualification(t *testing.T) {
	endpointDigest, err := DigestContextEndpoint("unix:///tmp/docker.sock")
	if err != nil {
		t.Fatal(err)
	}
	base := EngineIdentityInput{
		DaemonID: "daemon-id", APIVersion: "1.53", OperatingSystem: "linux", Architecture: "arm64",
		ContextEndpointDigest: endpointDigest, ProviderName: "Colima", EngineVersion: "29.6.1", ContextName: "colima",
	}
	colima, err := NewEngineIdentity(base)
	if err != nil {
		t.Fatal(err)
	}
	otherInput := base
	otherInput.ProviderName, otherInput.EngineVersion, otherInput.ContextName = "Docker Desktop", "31.0.0", "desktop-linux"
	other, err := NewEngineIdentity(otherInput)
	if err != nil {
		t.Fatal(err)
	}
	if colima.Digest() != other.Digest() {
		t.Fatalf("informational provider/version/context changed Engine digest: %s != %s", colima.Digest(), other.Digest())
	}
	encoded, err := json.Marshal(colima)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip EngineIdentity
	if err := json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if roundTrip.Digest() != colima.Digest() || !bytes.Equal(roundTrip.CanonicalJSON(), colima.CanonicalJSON()) {
		t.Fatal("Engine identity round trip was not canonical")
	}
	copy := colima.CanonicalJSON()
	copy[0] = '!'
	if bytes.Equal(copy, colima.CanonicalJSON()) {
		t.Fatal("Engine identity canonical bytes were aliased")
	}
	contract, err := NewCapabilityProbeContract(testProbeImage, testPolicy)
	if err != nil {
		t.Fatal(err)
	}
	runner := newCapabilityFakeRunner(contract)
	probe := mustCapabilityProbe(t, runner, t.TempDir(), colima, contract)
	qualification, err := probe.Probe(context.Background(), AuthorizeCapabilityProbe())
	if err != nil {
		t.Fatal(err)
	}
	if !qualification.ValidFor(colima, contract) || !qualification.ValidFor(other, contract) {
		t.Fatal("provider-agnostic Engine identities did not share qualification")
	}
}

func TestEngineIdentityRejectsInvalidAPIPlatformEndpointAndRecords(t *testing.T) {
	if _, err := DigestContextEndpoint("/var/run/docker.sock"); err == nil {
		t.Fatal("accepted endpoint without a scheme")
	}
	if _, err := DigestContextEndpoint("unix://relative"); err == nil {
		t.Fatal("accepted non-absolute unix endpoint")
	}
	endpoint := strings.Repeat("c", 64)
	valid := EngineIdentityInput{DaemonID: "daemon", APIVersion: "1.44", OperatingSystem: "linux", Architecture: "arm64",
		ContextEndpointDigest: endpoint, ProviderName: "provider", EngineVersion: "version", ContextName: "context"}
	cases := []EngineIdentityInput{
		withEngineInput(valid, func(value *EngineIdentityInput) { value.APIVersion = "1.43" }),
		withEngineInput(valid, func(value *EngineIdentityInput) { value.APIVersion = "01.53" }),
		withEngineInput(valid, func(value *EngineIdentityInput) { value.OperatingSystem = "darwin" }),
		withEngineInput(valid, func(value *EngineIdentityInput) { value.Architecture = "amd64" }),
		withEngineInput(valid, func(value *EngineIdentityInput) { value.ContextEndpointDigest = strings.ToUpper(endpoint) }),
		withEngineInput(valid, func(value *EngineIdentityInput) { value.ProviderName = "" }),
	}
	for index, input := range cases {
		if _, err := NewEngineIdentity(input); err == nil {
			t.Fatalf("accepted invalid Engine identity case %d", index)
		}
	}
	identity, err := NewEngineIdentity(valid)
	if err != nil {
		t.Fatal(err)
	}
	drifted := identity.Record()
	drifted.DaemonID = "different"
	if _, err := RestoreEngineIdentity(drifted); err == nil {
		t.Fatal("restored digest-drifted Engine record")
	}
	unknown := append(identity.CanonicalJSON()[:len(identity.CanonicalJSON())-1], []byte(`,"unknown":true}`)...)
	if err := json.Unmarshal(unknown, &identity); err == nil {
		t.Fatal("accepted unknown Engine identity JSON field")
	}
	if err := json.Unmarshal([]byte(`{"schema_version":"chora.docker-engine-identity/v1"}`), &identity); err == nil {
		t.Fatal("accepted partial Engine identity JSON")
	}
}

func TestCapabilityProbeContractStableDigestRoundTripAndRejectsDrift(t *testing.T) {
	contract, err := NewCapabilityProbeContract(testProbeImage, testPolicy)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewCapabilityProbeContract(testProbeImage, testPolicy)
	if err != nil || contract.Digest() != second.Digest() || !bytes.Equal(contract.CanonicalJSON(), second.CanonicalJSON()) {
		t.Fatal("capability contract is not deterministic")
	}
	encoded, err := json.Marshal(contract)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip CapabilityProbeContract
	if err := json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if roundTrip.Digest() != contract.Digest() || !slices.Equal(roundTrip.Capabilities(), contract.Capabilities()) {
		t.Fatal("capability contract round trip drifted")
	}
	record := contract.Record()
	record.Capabilities[0] = "pull-image"
	if _, err := RestoreCapabilityProbeContract(record); err == nil {
		t.Fatal("accepted changed capability set")
	}
	record = contract.Record()
	record.ProbeImageID = "sha256:" + strings.Repeat("d", 64)
	if _, err := RestoreCapabilityProbeContract(record); err == nil {
		t.Fatal("accepted record with drifted exact image")
	}
	unknown := append(contract.CanonicalJSON()[:len(contract.CanonicalJSON())-1], []byte(`,"future":1}`)...)
	if err := json.Unmarshal(unknown, &roundTrip); err == nil {
		t.Fatal("accepted unknown capability contract JSON field")
	}
	if _, err := NewCapabilityProbeContract("chora/probe:latest", testPolicy); err == nil {
		t.Fatal("accepted mutable Probe image reference")
	}
}

func TestEngineQualificationStableRoundTripAndRejectsPartialOrDriftedRecords(t *testing.T) {
	identity, contract := testCapabilityIdentityContract(t)
	completedAt := time.Date(2026, 8, 26, 1, 2, 3, 456, time.UTC)
	qualification, err := newEngineQualification(identity, contract, completedAt)
	if err != nil {
		t.Fatal(err)
	}
	second, err := newEngineQualification(identity, contract, completedAt)
	if err != nil || qualification.Digest() != second.Digest() {
		t.Fatal("qualification digest is not deterministic")
	}
	encoded, err := json.Marshal(qualification)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip EngineQualification
	if err := json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if !roundTrip.ValidFor(identity, contract) || !roundTrip.CompletedAt().Equal(completedAt) {
		t.Fatal("qualification round trip lost its binding")
	}
	drifted := qualification.Record()
	drifted.SandboxPolicyDigest = strings.Repeat("e", 64)
	if _, err := RestoreEngineQualification(drifted); err == nil {
		t.Fatal("restored drifted qualification")
	}
	if err := json.Unmarshal([]byte(`{"schema_version":"chora.docker-engine-qualification/v1"}`), &roundTrip); err == nil {
		t.Fatal("accepted partial qualification")
	}
	unknown := append(qualification.CanonicalJSON()[:len(qualification.CanonicalJSON())-1], []byte(`,"unknown":true}`)...)
	if err := json.Unmarshal(unknown, &roundTrip); err == nil {
		t.Fatal("accepted unknown qualification JSON field")
	}
}

func TestCapabilityProbeMissingAuthorizationRunsZeroCommands(t *testing.T) {
	identity, contract := testCapabilityIdentityContract(t)
	root := t.TempDir()
	runner := newCapabilityFakeRunner(contract)
	probe := mustCapabilityProbe(t, runner, root, identity, contract)
	qualification, err := probe.Probe(context.Background(), ProbeAuthorization{})
	if !errors.Is(err, ErrProbeAuthorizationRequired) || qualification.Digest() != "" {
		t.Fatalf("missing authorization result = %#v, %v", qualification, err)
	}
	if len(runner.Commands()) != 0 || runner.StartCalls() != 0 {
		t.Fatal("missing authorization executed Docker commands")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("missing authorization created host resources: %v, %v", entries, err)
	}
}

func TestCapabilityProbeRejectsStaleExpectedEngineBeforeCreatingResources(t *testing.T) {
	identity, contract := testCapabilityIdentityContract(t)
	staleRecord := identity.Record()
	staleRecord.DaemonID = "stale-daemon-id"
	stale, err := NewEngineIdentity(EngineIdentityInput{
		DaemonID: staleRecord.DaemonID, APIVersion: staleRecord.APIVersion, OperatingSystem: staleRecord.OperatingSystem,
		Architecture: staleRecord.Architecture, ContextEndpointDigest: staleRecord.ContextEndpointDigest,
		ProviderName: staleRecord.ProviderName, EngineVersion: staleRecord.EngineVersion, ContextName: staleRecord.ContextName,
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := newCapabilityFakeRunner(contract)
	root := t.TempDir()
	probe := mustCapabilityProbe(t, runner, root, stale, contract)
	qualification, err := probe.Probe(context.Background(), AuthorizeCapabilityProbe())
	if err == nil || qualification.Digest() != "" {
		t.Fatalf("stale expected Engine result = %#v, %v", qualification, err)
	}
	want := expectedObservationCommands("test")
	if got := observationCommands(runner.Commands()); !slices.EqualFunc(got, want, slices.Equal[[]string]) {
		t.Fatalf("pre-observation commands = %v, want %v", got, want)
	}
	if runner.StartCalls() != 0 || hasProbeResourceCommand(runner.Commands()) {
		t.Fatalf("pre-mismatch created Probe resources: starts=%d commands=%v", runner.StartCalls(), runner.Commands())
	}
	entries, statErr := os.ReadDir(root)
	if statErr != nil || len(entries) != 0 {
		t.Fatalf("pre-mismatch left host resources: entries=%v error=%v", entries, statErr)
	}
}

func TestCapabilityProbeRejectsPostCleanupEngineEndpointOrContextDrift(t *testing.T) {
	for _, drift := range []string{"daemon", "endpoint", "context"} {
		t.Run(drift, func(t *testing.T) {
			identity, contract := testCapabilityIdentityContract(t)
			runner := newCapabilityFakeRunner(contract)
			after := runner.observations[1]
			if drift == "daemon" {
				after.daemonID = "replacement-daemon"
			} else if drift == "endpoint" {
				after.endpoint = "unix:///tmp/replacement-docker.sock"
			} else {
				after.contextName = "replacement-context"
			}
			runner.observations[1] = after
			root := t.TempDir()
			probe := mustCapabilityProbe(t, runner, root, identity, contract)
			qualification, err := probe.Probe(context.Background(), AuthorizeCapabilityProbe())
			if err == nil || qualification.Digest() != "" {
				t.Fatalf("post-cleanup %s drift result = %#v, %v", drift, qualification, err)
			}
			want := append(expectedObservationCommands("test"), expectedObservationCommands(after.contextName)...)
			if got := observationCommands(runner.Commands()); !slices.EqualFunc(got, want, slices.Equal[[]string]) {
				t.Fatalf("pre/post observation commands = %v, want %v", got, want)
			}
			if !runner.InventoryAttempted() || runner.ContainerPresent() || runner.NetworkPresent() {
				t.Fatalf("post-cleanup drift residue = inventory:%t container:%t network:%t",
					runner.InventoryAttempted(), runner.ContainerPresent(), runner.NetworkPresent())
			}
			entries, statErr := os.ReadDir(root)
			if statErr != nil || len(entries) != 0 {
				t.Fatalf("post-cleanup drift left host resources: entries=%v error=%v", entries, statErr)
			}
		})
	}
}

func TestCapabilityProbeRejectsUnsafeRuntimeRootsWithoutCommandsOrResources(t *testing.T) {
	identity, contract := testCapabilityIdentityContract(t)
	realRoot := t.TempDir()
	symlinkRoot := filepath.Join(t.TempDir(), "runtime-link")
	if err := os.Symlink(realRoot, symlinkRoot); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		root string
	}{
		{name: "filesystem-root", root: string(filepath.Separator)},
		{name: "non-canonical", root: realRoot + string(filepath.Separator) + "."},
		{name: "symlink", root: symlinkRoot},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := newCapabilityFakeRunner(contract)
			before, err := os.ReadDir(realRoot)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := NewCapabilityProbe(CapabilityProbeConfig{Runner: runner, RuntimeRoot: test.root,
				Identity: identity, Contract: contract}); !errors.Is(err, ErrInvalidCapabilityProbe) {
				t.Fatalf("unsafe RuntimeRoot %q error = %v", test.root, err)
			}
			after, err := os.ReadDir(realRoot)
			if err != nil {
				t.Fatal(err)
			}
			if len(runner.Commands()) != 0 || runner.StartCalls() != 0 || len(before) != len(after) {
				t.Fatalf("unsafe RuntimeRoot caused commands/resources: commands=%v starts=%d before=%v after=%v",
					runner.Commands(), runner.StartCalls(), before, after)
			}
		})
	}
}

func TestCapabilityProbeSuccessUsesExactPresentImageAndProvesZeroResidue(t *testing.T) {
	identity, contract := testCapabilityIdentityContract(t)
	runner := newCapabilityFakeRunner(contract)
	root := t.TempDir()
	probe := mustCapabilityProbe(t, runner, root, identity, contract)
	qualification, err := probe.Probe(context.Background(), AuthorizeCapabilityProbe())
	if err != nil {
		t.Fatal(err)
	}
	if !qualification.ValidFor(identity, contract) {
		t.Fatal("successful Probe returned an unbound qualification")
	}
	commands := runner.Commands()
	var run Command
	for _, command := range commands {
		if len(command.Args) > 0 && command.Args[0] == "run" {
			run = command
		}
		joined := strings.ToLower(strings.Join(command.Args, " "))
		for _, forbidden := range []string{" build ", " pull ", " load "} {
			if strings.Contains(" "+joined+" ", forbidden) {
				t.Fatalf("Probe invoked forbidden image operation: %v", command.Args)
			}
		}
	}
	joined := strings.Join(run.Args, " ")
	for _, required := range []string{"run --pull=never", testProbeImage, "--read-only", "--cap-drop ALL", "--network chora-capability-probe-", "readonly"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("Probe run argv missing %q: %s", required, joined)
		}
	}
	labels := labelValues(run.Args)
	if len(labels) != 7 {
		t.Fatalf("Probe run labels = %v", labels)
	}
	for _, command := range commands {
		if isLabelInventory(command.Args) {
			filters := labelFilters(command.Args)
			want := make([]string, len(labels))
			for index, label := range labels {
				want[index] = "label=" + label
			}
			if !slices.Equal(filters, want) {
				t.Fatalf("inventory used non-exact labels: got %v want %v", filters, want)
			}
		}
	}
	if runner.ContainerPresent() || runner.NetworkPresent() {
		t.Fatal("successful Probe left fake Docker residue")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("successful Probe left host residue: %v, %v", entries, err)
	}
}

func TestCapabilityProbeAcceptsAlreadyClosedProcessAfterForcedTermination(t *testing.T) {
	identity, contract := testCapabilityIdentityContract(t)
	runner := newCapabilityFakeRunner(contract)
	runner.process.closeErr = os.ErrClosed
	root := t.TempDir()
	probe := mustCapabilityProbe(t, runner, root, identity, contract)

	qualification, err := probe.Probe(context.Background(), AuthorizeCapabilityProbe())
	if err != nil {
		t.Fatal(err)
	}
	if !qualification.ValidFor(identity, contract) {
		t.Fatal("already-closed Probe process returned an invalid qualification")
	}
	if runner.process.CloseCalls() != 1 || !runner.process.CloseAfterExit() {
		t.Fatalf("Probe process close calls/after-exit = %d/%t, want 1/true",
			runner.process.CloseCalls(), runner.process.CloseAfterExit())
	}
	if !runner.InventoryAttempted() || runner.ContainerPresent() || runner.NetworkPresent() {
		t.Fatalf("already-closed cleanup = inventory:%t container:%t network:%t",
			runner.InventoryAttempted(), runner.ContainerPresent(), runner.NetworkPresent())
	}
	entries, statErr := os.ReadDir(root)
	if statErr != nil || len(entries) != 0 {
		t.Fatalf("already-closed Probe left host resources: entries=%v error=%v", entries, statErr)
	}
}

func TestCapabilityProbeRejectsOtherProcessCloseErrors(t *testing.T) {
	identity, contract := testCapabilityIdentityContract(t)
	runner := newCapabilityFakeRunner(contract)
	closeErr := errors.New("injected close failure")
	runner.process.closeErr = closeErr
	root := t.TempDir()
	probe := mustCapabilityProbe(t, runner, root, identity, contract)

	qualification, err := probe.Probe(context.Background(), AuthorizeCapabilityProbe())
	if !errors.Is(err, closeErr) || qualification.Digest() != "" {
		t.Fatalf("process close failure result = %#v, %v", qualification, err)
	}
	if runner.process.CloseCalls() != 1 {
		t.Fatalf("Probe process close calls = %d, want 1", runner.process.CloseCalls())
	}
	if !runner.InventoryAttempted() || runner.ContainerPresent() || runner.NetworkPresent() {
		t.Fatalf("process close failure cleanup = inventory:%t container:%t network:%t",
			runner.InventoryAttempted(), runner.ContainerPresent(), runner.NetworkPresent())
	}
	entries, statErr := os.ReadDir(root)
	if statErr != nil || len(entries) != 0 {
		t.Fatalf("process close failure left host resources: entries=%v error=%v", entries, statErr)
	}
}

func TestCapabilityProbeWaitsForStartedContainerToBecomeInspectable(t *testing.T) {
	identity, contract := testCapabilityIdentityContract(t)
	runner := newCapabilityFakeRunner(contract)
	runner.containerInspectNotVisible = 2
	root := t.TempDir()
	probe := mustCapabilityProbe(t, runner, root, identity, contract)
	qualification, err := probe.Probe(context.Background(), AuthorizeCapabilityProbe())
	if err != nil {
		t.Fatal(err)
	}
	if !qualification.ValidFor(identity, contract) || runner.containerInspectAttempts != 3 {
		t.Fatalf("readiness result = valid:%t inspect attempts:%d, want true/3",
			qualification.ValidFor(identity, contract), runner.containerInspectAttempts)
	}
	if !runner.InventoryAttempted() || runner.ContainerPresent() || runner.NetworkPresent() {
		t.Fatalf("readiness cleanup = inventory:%t container:%t network:%t",
			runner.InventoryAttempted(), runner.ContainerPresent(), runner.NetworkPresent())
	}
}

func TestCapabilityProbeWaitsForVisibleContainerToBecomeRunning(t *testing.T) {
	identity, contract := testCapabilityIdentityContract(t)
	runner := newCapabilityFakeRunner(contract)
	runner.containerInspectNotRunning = 2
	root := t.TempDir()
	probe := mustCapabilityProbe(t, runner, root, identity, contract)
	qualification, err := probe.Probe(context.Background(), AuthorizeCapabilityProbe())
	if err != nil {
		t.Fatal(err)
	}
	if !qualification.ValidFor(identity, contract) || runner.containerInspectAttempts != 3 {
		t.Fatalf("running readiness result = valid:%t inspect attempts:%d, want true/3",
			qualification.ValidFor(identity, contract), runner.containerInspectAttempts)
	}
	if !runner.InventoryAttempted() || runner.ContainerPresent() || runner.NetworkPresent() {
		t.Fatalf("running readiness cleanup = inventory:%t container:%t network:%t",
			runner.InventoryAttempted(), runner.ContainerPresent(), runner.NetworkPresent())
	}
	entries, statErr := os.ReadDir(root)
	if statErr != nil || len(entries) != 0 {
		t.Fatalf("running readiness left host resources: entries=%v error=%v", entries, statErr)
	}
}

func TestCapabilityProbeReadinessAbortsWhenStartedProcessExits(t *testing.T) {
	identity, contract := testCapabilityIdentityContract(t)
	runner := newCapabilityFakeRunner(contract)
	runner.containerInspectNotVisible = 1000
	runner.processExitAfterStart = true
	probe := mustCapabilityProbe(t, runner, t.TempDir(), identity, contract)
	startedAt := time.Now()
	qualification, err := probe.Probe(context.Background(), AuthorizeCapabilityProbe())
	if err == nil || !strings.Contains(err.Error(), "process exited before it became inspectable") || qualification.Digest() != "" {
		t.Fatalf("early process exit result = %#v, %v", qualification, err)
	}
	if elapsed := time.Since(startedAt); elapsed >= time.Second {
		t.Fatalf("early process exit took %s, want less than 1s", elapsed)
	}
	if !runner.InventoryAttempted() || runner.ContainerPresent() || runner.NetworkPresent() {
		t.Fatalf("early process exit cleanup = inventory:%t container:%t network:%t",
			runner.InventoryAttempted(), runner.ContainerPresent(), runner.NetworkPresent())
	}
}

func TestCapabilityProbeFailuresReturnNoQualification(t *testing.T) {
	for _, stage := range []string{"start", "inspect", "cleanup", "inventory"} {
		t.Run(stage, func(t *testing.T) {
			identity, contract := testCapabilityIdentityContract(t)
			runner := newCapabilityFakeRunner(contract)
			runner.failStage = stage
			probe := mustCapabilityProbe(t, runner, t.TempDir(), identity, contract)
			qualification, err := probe.Probe(context.Background(), AuthorizeCapabilityProbe())
			if err == nil || qualification.Digest() != "" {
				t.Fatalf("%s failure returned qualification %#v, error %v", stage, qualification, err)
			}
			if !runner.InventoryAttempted() {
				t.Fatalf("%s failure skipped post-cleanup inventory", stage)
			}
		})
	}
}

func TestCapabilityProbeCancellationWaitsExactlyOnceAndCleansAllResidue(t *testing.T) {
	identity, contract := testCapabilityIdentityContract(t)
	runner := newCapabilityFakeRunner(contract)
	runner.dockerKillDoesNotExit = true
	root := t.TempDir()
	probe := mustCapabilityProbe(t, runner, root, identity, contract)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	qualification, err := probe.Probe(ctx, AuthorizeCapabilityProbe())
	if !errors.Is(err, context.Canceled) || qualification.Digest() != "" {
		t.Fatalf("canceled Probe result = %#v, %v", qualification, err)
	}
	if runner.process.WaitCalls() != 1 || runner.process.KillCalls() != 1 {
		t.Fatalf("canceled Probe Wait/Kill calls = %d/%d, want 1/1", runner.process.WaitCalls(), runner.process.KillCalls())
	}
	if !runner.InventoryAttempted() || runner.ContainerPresent() || runner.NetworkPresent() {
		t.Fatalf("canceled Probe cleanup = inventory:%t container:%t network:%t",
			runner.InventoryAttempted(), runner.ContainerPresent(), runner.NetworkPresent())
	}
	entries, statErr := os.ReadDir(root)
	if statErr != nil || len(entries) != 0 {
		t.Fatalf("canceled Probe left host residue: entries=%v error=%v", entries, statErr)
	}
}

func TestCapabilityProbeRejectsSelfCrashAndInvalidDaemonTerminalState(t *testing.T) {
	for _, test := range []struct {
		name            string
		cliExit         int
		daemonExit      int
		daemonOOMKilled bool
	}{
		{name: "self-crash-cli-exit-1", cliExit: 1, daemonExit: 137},
		{name: "daemon-exit-not-137", cliExit: 137, daemonExit: 1},
		{name: "daemon-oom-killed", cliExit: 137, daemonExit: 137, daemonOOMKilled: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			identity, contract := testCapabilityIdentityContract(t)
			runner := newCapabilityFakeRunner(contract)
			runner.dockerRunExitCode = test.cliExit
			runner.daemonExitCode = test.daemonExit
			runner.daemonOOMKilled = test.daemonOOMKilled
			root := t.TempDir()
			probe := mustCapabilityProbe(t, runner, root, identity, contract)
			qualification, err := probe.Probe(context.Background(), AuthorizeCapabilityProbe())
			if err == nil || qualification.Digest() != "" {
				t.Fatalf("invalid forced termination result = %#v, %v", qualification, err)
			}
			if !runner.InventoryAttempted() || runner.ContainerPresent() || runner.NetworkPresent() {
				t.Fatalf("invalid termination residue = inventory:%t container:%t network:%t",
					runner.InventoryAttempted(), runner.ContainerPresent(), runner.NetworkPresent())
			}
			entries, statErr := os.ReadDir(root)
			if statErr != nil || len(entries) != 0 {
				t.Fatalf("invalid termination left host resources: entries=%v error=%v", entries, statErr)
			}
		})
	}
}

func withEngineInput(input EngineIdentityInput, change func(*EngineIdentityInput)) EngineIdentityInput {
	change(&input)
	return input
}

func testCapabilityIdentityContract(t *testing.T) (EngineIdentity, CapabilityProbeContract) {
	t.Helper()
	endpoint, err := DigestContextEndpoint("unix:///tmp/docker.sock")
	if err != nil {
		t.Fatal(err)
	}
	identity, err := NewEngineIdentity(EngineIdentityInput{DaemonID: "daemon-id", APIVersion: "1.53", OperatingSystem: "linux",
		Architecture: "arm64", ContextEndpointDigest: endpoint, ProviderName: "test-provider", EngineVersion: "99.7.3", ContextName: "test"})
	if err != nil {
		t.Fatal(err)
	}
	contract, err := NewCapabilityProbeContract(testProbeImage, testPolicy)
	if err != nil {
		t.Fatal(err)
	}
	return identity, contract
}

func mustCapabilityProbe(t *testing.T, runner CommandRunner, root string, identity EngineIdentity, contract CapabilityProbeContract) *CapabilityProbe {
	t.Helper()
	probe, err := NewCapabilityProbe(CapabilityProbeConfig{Runner: runner, RuntimeRoot: root, Identity: identity, Contract: contract,
		Clock:    func() time.Time { return time.Date(2026, 8, 26, 2, 3, 4, 5, time.UTC) },
		IDSource: func() (string, error) { return testProbeID, nil }})
	if err != nil {
		t.Fatal(err)
	}
	return probe
}

type capabilityFakeRunner struct {
	mu                         sync.Mutex
	contract                   CapabilityProbeContract
	commands                   []Command
	startArgs                  []string
	startCalls                 int
	failStage                  string
	process                    *capabilityFakeProcess
	containerPresent           bool
	networkPresent             bool
	inventoryAttempt           bool
	dockerKillDoesNotExit      bool
	dockerRunExitCode          int
	containerInspectNotVisible int
	containerInspectNotRunning int
	containerInspectAttempts   int
	processExitAfterStart      bool
	daemonExitCode             int
	daemonOOMKilled            bool
	daemonDead                 bool
	daemonError                string
	observations               []fakeEngineObservation
	observationIndex           int
}

func newCapabilityFakeRunner(contract CapabilityProbeContract) *capabilityFakeRunner {
	observation := fakeEngineObservation{
		daemonID: "daemon-id", apiVersion: "1.53", operatingSystem: "linux", architecture: "arm64",
		serverVersion: "99.7.3", providerName: "test-provider", contextName: "test", endpoint: "unix:///tmp/docker.sock",
	}
	return &capabilityFakeRunner{contract: contract, process: newCapabilityFakeProcess(), dockerRunExitCode: 137,
		daemonExitCode: 137, observations: []fakeEngineObservation{observation, observation}}
}

type fakeEngineObservation struct {
	daemonID, apiVersion, operatingSystem, architecture string
	serverVersion, providerName, contextName, endpoint  string
}

func (runner *capabilityFakeRunner) Run(_ context.Context, command Command) (CommandResult, error) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	command.Args = slices.Clone(command.Args)
	runner.commands = append(runner.commands, command)
	args := command.Args
	observation := runner.observations[min(runner.observationIndex, len(runner.observations)-1)]
	if slices.Equal(args, []string{"version", "--format", engineVersionObservationFormat}) {
		document, err := json.Marshal(engineVersionObservation{APIVersion: observation.apiVersion, ServerVersion: observation.serverVersion,
			OperatingSystem: observation.operatingSystem, Architecture: observation.architecture})
		return CommandResult{Stdout: document}, err
	}
	if slices.Equal(args, []string{"info", "--format", engineInfoObservationFormat}) {
		document, err := json.Marshal(engineInfoObservation{DaemonID: observation.daemonID, ProviderName: observation.providerName})
		return CommandResult{Stdout: document}, err
	}
	if slices.Equal(args, []string{"context", "show"}) {
		return CommandResult{Stdout: []byte(observation.contextName + "\n")}, nil
	}
	if slices.Equal(args, []string{"context", "inspect", "--format", engineEndpointObservationFormat, observation.contextName}) {
		document, err := json.Marshal(observation.endpoint)
		runner.observationIndex++
		return CommandResult{Stdout: document}, err
	}
	if slices.Equal(args, []string{"image", "inspect", "--format", "{{.Id}}", runner.contract.ProbeImageID()}) {
		return CommandResult{Stdout: []byte(runner.contract.ProbeImageID() + "\n")}, nil
	}
	if len(args) >= 2 && args[0] == "network" && args[1] == "create" {
		runner.networkPresent = true
		return CommandResult{}, nil
	}
	if len(args) == 5 && args[0] == "network" && args[1] == "inspect" && args[3] == "{{json .}}" {
		if runner.failStage == "inspect" {
			return CommandResult{ExitCode: 1, Stderr: []byte("injected inspect failure")}, nil
		}
		var createArgs []string
		for _, call := range runner.commands {
			if len(call.Args) >= 2 && call.Args[0] == "network" && call.Args[1] == "create" {
				createArgs = call.Args
			}
		}
		document, err := json.Marshal(map[string]any{
			"Name": args[4], "Id": strings.Repeat("e", 64), "Internal": true,
			"Labels": labelsMap(createArgs),
		})
		return CommandResult{Stdout: document}, err
	}
	if len(args) == 5 && args[0] == "container" && args[1] == "inspect" && args[3] == "{{json .}}" {
		runner.containerInspectAttempts++
		if runner.containerInspectNotVisible > 0 {
			runner.containerInspectNotVisible--
			return CommandResult{ExitCode: 1, Stderr: []byte("Error response from daemon: No such container: " + args[4])}, nil
		}
		if runner.failStage == "inspect" {
			return CommandResult{ExitCode: 1, Stderr: []byte("injected inspect failure")}, nil
		}
		running := !runner.process.done()
		if runner.containerInspectNotRunning > 0 {
			runner.containerInspectNotRunning--
			running = false
		}
		document, err := fakeProbeInspect(runner.startArgs, running)
		if err != nil {
			return CommandResult{}, err
		}
		return CommandResult{Stdout: document}, nil
	}
	if slices.Equal(args, []string{"container", "inspect", "--format", probeTerminalStateFormat, "chora-capability-probe-" + testProbeID}) {
		document, err := json.Marshal(map[string]any{"running": false, "exit_code": runner.daemonExitCode,
			"oom_killed": runner.daemonOOMKilled, "dead": runner.daemonDead, "error": runner.daemonError})
		return CommandResult{Stdout: document}, err
	}
	if len(args) > 0 && args[0] == "kill" {
		if !runner.dockerKillDoesNotExit {
			runner.process.exit(runner.dockerRunExitCode)
		}
		return CommandResult{}, nil
	}
	if len(args) > 1 && args[0] == "rm" && args[1] == "-f" {
		if runner.failStage == "cleanup" {
			return CommandResult{ExitCode: 1, Stderr: []byte("injected cleanup failure")}, nil
		}
		runner.containerPresent = false
		return CommandResult{}, nil
	}
	if len(args) > 1 && args[0] == "network" && args[1] == "rm" {
		runner.networkPresent = false
		return CommandResult{}, nil
	}
	if isLabelInventory(args) {
		runner.inventoryAttempt = true
		if runner.failStage == "inventory" {
			return CommandResult{ExitCode: 1, Stderr: []byte("injected inventory failure")}, nil
		}
		return CommandResult{}, nil
	}
	return CommandResult{}, nil
}

func (runner *capabilityFakeRunner) Start(_ context.Context, command Command) (Process, error) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	command.Args = slices.Clone(command.Args)
	runner.commands = append(runner.commands, command)
	runner.startCalls++
	runner.startArgs = command.Args
	if runner.failStage == "start" {
		return nil, errors.New("injected start failure")
	}
	runner.containerPresent = true
	if runner.processExitAfterStart {
		runner.process.exit(1)
	}
	return runner.process, nil
}

func (runner *capabilityFakeRunner) Commands() []Command {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	result := make([]Command, len(runner.commands))
	copy(result, runner.commands)
	return result
}

func (runner *capabilityFakeRunner) StartCalls() int {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return runner.startCalls
}

func (runner *capabilityFakeRunner) ContainerPresent() bool {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return runner.containerPresent
}

func (runner *capabilityFakeRunner) NetworkPresent() bool {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return runner.networkPresent
}

func (runner *capabilityFakeRunner) InventoryAttempted() bool {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return runner.inventoryAttempt
}

type capabilityFakeProcess struct {
	mu             sync.Mutex
	exitCode       int
	exited         chan struct{}
	once           sync.Once
	waitCalls      int
	killCalls      int
	closeCalls     int
	closeAfterExit bool
	closeErr       error
}

func newCapabilityFakeProcess() *capabilityFakeProcess {
	return &capabilityFakeProcess{exited: make(chan struct{})}
}

func (process *capabilityFakeProcess) Write(data []byte) (int, error) { return len(data), nil }
func (process *capabilityFakeProcess) Close() error {
	process.mu.Lock()
	defer process.mu.Unlock()
	process.closeCalls++
	process.closeAfterExit = process.done()
	return process.closeErr
}
func (process *capabilityFakeProcess) Kill() error {
	process.mu.Lock()
	process.killCalls++
	process.mu.Unlock()
	if process.done() {
		return os.ErrProcessDone
	}
	process.exit(137)
	return nil
}
func (process *capabilityFakeProcess) Wait() (int, error) {
	process.mu.Lock()
	process.waitCalls++
	process.mu.Unlock()
	<-process.exited
	process.mu.Lock()
	defer process.mu.Unlock()
	return process.exitCode, errors.New("signal: killed")
}

func (process *capabilityFakeProcess) WaitCalls() int {
	process.mu.Lock()
	defer process.mu.Unlock()
	return process.waitCalls
}

func (process *capabilityFakeProcess) KillCalls() int {
	process.mu.Lock()
	defer process.mu.Unlock()
	return process.killCalls
}
func (process *capabilityFakeProcess) CloseCalls() int {
	process.mu.Lock()
	defer process.mu.Unlock()
	return process.closeCalls
}
func (process *capabilityFakeProcess) CloseAfterExit() bool {
	process.mu.Lock()
	defer process.mu.Unlock()
	return process.closeAfterExit
}
func (process *capabilityFakeProcess) exit(code int) {
	process.once.Do(func() {
		process.mu.Lock()
		process.exitCode = code
		process.mu.Unlock()
		close(process.exited)
	})
}
func (process *capabilityFakeProcess) done() bool {
	select {
	case <-process.exited:
		return true
	default:
		return false
	}
}

func fakeProbeInspect(args []string, running bool) ([]byte, error) {
	var container effectiveContainer
	container.ID = strings.Repeat("d", 64)
	container.Config.Labels = map[string]string{}
	container.HostConfig.PortBindings = map[string]json.RawMessage{}
	container.HostConfig.Tmpfs = map[string]string{}
	container.NetworkSettings.Networks = map[string]json.RawMessage{}
	for index := 0; index < len(args); index++ {
		next := func() string {
			if index+1 < len(args) {
				return args[index+1]
			}
			return ""
		}
		switch args[index] {
		case "--name":
			container.Name = "/" + next()
		case "--network":
			container.HostConfig.NetworkMode = next()
			container.NetworkSettings.Networks[next()] = json.RawMessage(`{}`)
		case "--user":
			container.Config.User = next()
		case "--read-only":
			container.HostConfig.ReadonlyRootfs = true
		case "--cap-drop":
			container.HostConfig.CapDrop = append(container.HostConfig.CapDrop, next())
		case "--security-opt":
			container.HostConfig.SecurityOpt = append(container.HostConfig.SecurityOpt, next())
		case "--pids-limit":
			container.HostConfig.PidsLimit = 16
		case "--cpus":
			container.HostConfig.NanoCPUs = 100_000_000
		case "--memory":
			container.HostConfig.Memory = 32 << 20
		case "--memory-swap":
			container.HostConfig.MemorySwap = 32 << 20
		case "--ulimit":
			container.HostConfig.Ulimits = append(container.HostConfig.Ulimits, struct {
				Name       string `json:"Name"`
				Soft, Hard int64
			}{Name: "nofile", Soft: 64, Hard: 64})
		case "--log-driver":
			container.HostConfig.LogConfig.Type = next()
		case "--tmpfs":
			path, options, _ := strings.Cut(next(), ":")
			container.HostConfig.Tmpfs[path] = options
		case "--mount":
			parts := strings.Split(next(), ",")
			mount := struct {
				Type, Name, Source, Destination string
				RW                              bool
			}{Type: "bind"}
			for _, part := range parts {
				key, value, ok := strings.Cut(part, "=")
				if !ok {
					continue
				}
				switch key {
				case "src":
					mount.Source = filepath.Clean(value)
				case "dst":
					mount.Destination = value
				}
			}
			container.Mounts = append(container.Mounts, mount)
		case "--label":
			key, value, ok := strings.Cut(next(), "=")
			if ok {
				container.Config.Labels[key] = value
			}
		}
	}
	for index := len(args) - 1; index >= 0; index-- {
		if strings.HasPrefix(args[index], "sha256:") {
			container.Image = args[index]
			break
		}
	}
	encoded, err := json.Marshal(container)
	if err != nil {
		return nil, err
	}
	var document map[string]any
	if err := json.Unmarshal(encoded, &document); err != nil {
		return nil, err
	}
	document["State"] = map[string]any{"Running": running}
	return json.Marshal(document)
}

func labelValues(args []string) []string {
	var result []string
	for index, value := range args {
		if value == "--label" && index+1 < len(args) {
			result = append(result, args[index+1])
		}
	}
	return result
}

func labelsMap(args []string) map[string]string {
	result := map[string]string{}
	for _, label := range labelValues(args) {
		key, value, ok := strings.Cut(label, "=")
		if ok {
			result[key] = value
		}
	}
	return result
}

func labelFilters(args []string) []string {
	var result []string
	for index, value := range args {
		if value == "--filter" && index+1 < len(args) && strings.HasPrefix(args[index+1], "label=") {
			result = append(result, args[index+1])
		}
	}
	return result
}

func isLabelInventory(args []string) bool {
	return (len(args) >= 2 && args[0] == "ps" && args[1] == "-aq" ||
		len(args) >= 3 && args[0] == "network" && args[1] == "ls" && args[2] == "-q") && len(labelFilters(args)) > 0
}

func expectedObservationCommands(contextName string) [][]string {
	return [][]string{
		{"version", "--format", engineVersionObservationFormat},
		{"info", "--format", engineInfoObservationFormat},
		{"context", "show"},
		{"context", "inspect", "--format", engineEndpointObservationFormat, contextName},
	}
}

func observationCommands(commands []Command) [][]string {
	var result [][]string
	for _, command := range commands {
		if slices.Equal(command.Args, []string{"version", "--format", engineVersionObservationFormat}) ||
			slices.Equal(command.Args, []string{"info", "--format", engineInfoObservationFormat}) ||
			slices.Equal(command.Args, []string{"context", "show"}) ||
			len(command.Args) == 5 && slices.Equal(command.Args[:4], []string{"context", "inspect", "--format", engineEndpointObservationFormat}) {
			result = append(result, slices.Clone(command.Args))
		}
	}
	return result
}

func hasProbeResourceCommand(commands []Command) bool {
	for _, command := range commands {
		if len(command.Args) == 0 {
			continue
		}
		if command.Args[0] == "run" || command.Args[0] == "image" || command.Args[0] == "network" ||
			command.Args[0] == "exec" || command.Args[0] == "kill" || command.Args[0] == "rm" || command.Args[0] == "ps" ||
			command.Args[0] == "container" {
			return true
		}
	}
	return false
}

var _ CommandRunner = (*capabilityFakeRunner)(nil)
var _ Process = (*capabilityFakeProcess)(nil)
var _ io.WriteCloser = (*capabilityFakeProcess)(nil)
