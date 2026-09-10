package dockersupervisor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Yangyang96/chora/internal/execution"
)

func TestStartQualificationRejectsEngineAndRoleImageDriftBeforeMutation(t *testing.T) {
	tests := []struct {
		name       string
		configure  func(*fakeRunner)
		diagnostic string
	}{
		{name: "Engine identity", configure: func(runner *fakeRunner) { runner.engineIdentityDrift = true }, diagnostic: "Engine identity or exact capability contract drift"},
		{name: "Attempt image", configure: func(runner *fakeRunner) { runner.attemptImageDrift = true }, diagnostic: "Attempt image identity drift"},
		{name: "Boundary image", configure: func(runner *fakeRunner) { runner.boundaryImageDrift = true }, diagnostic: "Boundary image identity drift"},
		{name: "observation failure", configure: func(runner *fakeRunner) { runner.qualificationReadError = true }, diagnostic: "re-observe Engine"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeRunner{processes: []*fakeProcess{newFakeProcess()}}
			test.configure(runner)
			supervisor := newTestSupervisor(t, runner)
			invocation := testInvocation(t, testSource(t), "launch-qualification-drift")

			outcome := supervisor.Start(context.Background(), invocation, &testSink{binding: invocation.LaunchToken()})
			if outcome.Kind != execution.StartProvenNoChild || !strings.Contains(outcome.Diagnostic, test.diagnostic) {
				t.Fatalf("Start() = %#v", outcome)
			}
			for _, command := range runner.allCommands() {
				if !isExecutionQualificationCommand(command.Args) {
					t.Fatalf("qualification failure mutated Docker state: %v", command.Args)
				}
			}
			entries, err := os.ReadDir(supervisor.config.RuntimeRoot)
			if err != nil || len(entries) != 0 {
				t.Fatalf("qualification failure created runtime roots: entries=%v error=%v", entries, err)
			}
		})
	}
}

func TestStartRejectsTamperedQualificationBeforeDockerObservation(t *testing.T) {
	runner := &fakeRunner{processes: []*fakeProcess{newFakeProcess()}}
	supervisor := newTestSupervisor(t, runner)
	tampered := supervisor.config.EngineQualification
	tampered.record.EngineIdentityDigest = strings.Repeat("f", 64)
	supervisor.config.EngineQualification = tampered
	invocation := testInvocation(t, testSource(t), "launch-tampered-qualification")
	outcome := supervisor.Start(context.Background(), invocation, &testSink{binding: invocation.LaunchToken()})
	if outcome.Kind != execution.StartProvenNoChild || !strings.Contains(outcome.Diagnostic, "qualification or exact capability contract") {
		t.Fatalf("Start() = %#v", outcome)
	}
	if commands := runner.allCommands(); len(commands) != 0 {
		t.Fatalf("tampered qualification reached Docker: %#v", commands)
	}
}

func TestNewRejectsQualificationFromWrongContractAndUnprobedAttemptImage(t *testing.T) {
	otherImage := "sha256:" + strings.Repeat("e", 64)
	otherContract, err := NewCapabilityProbeContract(otherImage, testPolicyDigest)
	if err != nil {
		t.Fatal(err)
	}
	otherQualification, err := newEngineQualification(testEngineIdentity(t), otherContract, testEngineQualification(t).CompletedAt())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name          string
		contract      CapabilityProbeContract
		qualification EngineQualification
	}{
		{name: "wrong same-policy contract", contract: testCapabilityContract(t), qualification: otherQualification},
		{name: "unprobed Attempt image", contract: otherContract, qualification: otherQualification},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			_, err := New(Config{
				Runner: &fakeRunner{}, RuntimeRoot: filepath.Join(root, "runtime"), ArtifactRoot: filepath.Join(root, "artifacts"),
				PolicyDigest: testPolicyDigest, AttemptImageID: testAttemptImageID, BoundaryImageID: testBoundaryImageID,
				RuntimeSourceIdentity: managedRuntimeSourceIdentity(testAttemptImageID),
				CapabilityContract:    test.contract, EngineQualification: test.qualification,
				TrustAnchorSource: testTrustAnchorSource(t),
			})
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("New() error = %v", err)
			}
		})
	}
}

func TestNewRequiresExactContractAndQualification(t *testing.T) {
	root := t.TempDir()
	_, err := New(Config{
		Runner: &fakeRunner{}, RuntimeRoot: filepath.Join(root, "runtime"), ArtifactRoot: filepath.Join(root, "artifacts"),
		PolicyDigest: testPolicyDigest, AttemptImageID: testAttemptImageID, BoundaryImageID: testBoundaryImageID,
		RuntimeSourceIdentity: managedRuntimeSourceIdentity(testAttemptImageID),
		TrustAnchorSource:     testTrustAnchorSource(t),
	})
	if !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), "contract and Engine qualification are required") {
		t.Fatalf("New() error = %v", err)
	}
}

func TestNewRequiresExplicitSetupQualifiedRunner(t *testing.T) {
	root := t.TempDir()
	_, err := New(Config{
		RuntimeRoot: filepath.Join(root, "runtime"), ArtifactRoot: filepath.Join(root, "artifacts"),
		PolicyDigest: testPolicyDigest, AttemptImageID: testAttemptImageID, BoundaryImageID: testBoundaryImageID,
		RuntimeSourceIdentity: managedRuntimeSourceIdentity(testAttemptImageID),
		CapabilityContract:    testCapabilityContract(t), EngineQualification: testEngineQualification(t),
		TrustAnchorSource: testTrustAnchorSource(t),
	})
	if !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), "setup-qualified Docker Runner is required") {
		t.Fatalf("New() error = %v", err)
	}
}

func TestQualificationDriftReadsNoCredentialOrTrustProjection(t *testing.T) {
	runner := &fakeRunner{engineIdentityDrift: true}
	supervisor := newTestSupervisor(t, runner)
	projectionReads := 0
	supervisor.loadProjection = func(_, _ string) (taskProjection, error) {
		projectionReads++
		return taskProjection{}, errors.New("projection read must not occur")
	}
	invocation := testInvocation(t, testSource(t), "launch-no-projection-before-qualification")
	outcome := supervisor.Start(context.Background(), invocation, &testSink{binding: invocation.LaunchToken()})
	if outcome.Kind != execution.StartProvenNoChild || projectionReads != 0 {
		t.Fatalf("Start() = %#v projectionReads=%d", outcome, projectionReads)
	}
}

func TestStartReadsSensitiveProjectionOnlyAfterAllQualificationObservations(t *testing.T) {
	process := newFakeProcess()
	runner := &fakeRunner{processes: []*fakeProcess{process}}
	supervisor := newTestSupervisor(t, runner)
	projectionReadAt := -1
	supervisor.loadProjection = func(credentialSource, trustAnchorSource string) (taskProjection, error) {
		commands := runner.allCommands()
		projectionReadAt = len(commands)
		if projectionReadAt != 6 || countQualificationCommands(commands) != 6 {
			return taskProjection{}, errors.New("sensitive projection read preceded complete qualification")
		}
		entries, err := os.ReadDir(supervisor.config.RuntimeRoot)
		if err != nil || len(entries) != 0 {
			return taskProjection{}, errors.New("Attempt resource existed before sensitive projection read")
		}
		return loadTaskProjection(credentialSource, trustAnchorSource)
	}
	invocation := testInvocation(t, testSource(t), "launch-sensitive-read-order")
	outcome := supervisor.Start(context.Background(), invocation, &testSink{binding: invocation.LaunchToken()})
	if outcome.Kind != execution.Started || projectionReadAt != 6 {
		t.Fatalf("Start() = %#v projectionReadAt=%d commands=%#v", outcome, projectionReadAt, runner.allCommands())
	}
	process.exit(0)
	record := supervisor.record(outcome.Handle)
	waitDone(t, record.done)
	drainAll(t, supervisor, outcome.Handle)
	if err := supervisor.Finalize(context.Background(), outcome.Handle, execution.RetentionPolicy{}); err != nil {
		t.Fatal(err)
	}
}

func TestStartReobservesQualificationBeforeEveryExecution(t *testing.T) {
	process := newFakeProcess()
	runner := &fakeRunner{processes: []*fakeProcess{process}}
	supervisor := newTestSupervisor(t, runner)
	first := testInvocation(t, testSource(t), "launch-qualified-one")
	firstOutcome := supervisor.Start(context.Background(), first, &testSink{binding: first.LaunchToken()})
	if firstOutcome.Kind != execution.Started {
		t.Fatalf("first Start() = %#v", firstOutcome)
	}

	runner.mu.Lock()
	runner.engineIdentityDrift = true
	runner.mu.Unlock()
	second := testInvocation(t, testSource(t), "launch-qualified-two")
	outcome := supervisor.Start(context.Background(), second, &testSink{binding: second.LaunchToken()})
	if outcome.Kind != execution.StartProvenNoChild || !strings.Contains(outcome.Diagnostic, "Engine identity or exact capability contract drift") {
		t.Fatalf("second Start() = %#v", outcome)
	}
	all := runner.allCommands()
	firstMutation := slices.IndexFunc(all, func(command Command) bool { return !isExecutionQualificationCommand(command.Args) })
	if firstMutation != 6 {
		t.Fatalf("first Docker mutation preceded exact qualification observations: index=%d commands=%#v", firstMutation, all)
	}
	if got := countQualificationCommands(all); got != 10 {
		t.Fatalf("qualification commands = %d, want first full observation plus second drift observation", got)
	}

	runner.mu.Lock()
	runner.engineIdentityDrift = false
	runner.mu.Unlock()
	process.exit(0)
	record := supervisor.record(firstOutcome.Handle)
	if record == nil {
		t.Fatal("first record disappeared")
	}
	waitDone(t, record.done)
	drainAll(t, supervisor, firstOutcome.Handle)
	if err := supervisor.Finalize(context.Background(), firstOutcome.Handle, execution.RetentionPolicy{}); err != nil {
		t.Fatal(err)
	}
}

func TestRecoverQualificationFailureLeavesOrphansUntouched(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*Supervisor, *fakeRunner)
	}{
		{name: "missing qualification", configure: func(supervisor *Supervisor, _ *fakeRunner) {
			supervisor.config.EngineQualification = EngineQualification{}
		}},
		{name: "Engine drift", configure: func(_ *Supervisor, runner *fakeRunner) { runner.engineIdentityDrift = true }},
		{name: "Attempt image drift", configure: func(_ *Supervisor, runner *fakeRunner) { runner.attemptImageDrift = true }},
		{name: "Boundary image drift", configure: func(_ *Supervisor, runner *fakeRunner) { runner.boundaryImageDrift = true }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeRunner{}
			supervisor := newTestSupervisor(t, runner)
			test.configure(supervisor, runner)
			orphan := filepath.Join(supervisor.config.RuntimeRoot, "chora-orphan")
			if err := os.MkdirAll(orphan, 0o700); err != nil {
				t.Fatal(err)
			}

			if err := supervisor.Recover(context.Background()); err == nil {
				t.Fatal("Recover accepted missing or drifted qualification")
			}
			if _, err := os.Stat(orphan); err != nil {
				t.Fatalf("Recover mutated orphan root before qualification: %v", err)
			}
			for _, command := range runner.allCommands() {
				if !isExecutionQualificationCommand(command.Args) {
					t.Fatalf("Recover mutated Docker state before qualification: %v", command.Args)
				}
			}
		})
	}
}

func TestFinalizeInventoriesDeclaredCheckResourcesAndProvesRuntimeRootAbsent(t *testing.T) {
	process := newFakeProcess()
	runner := &fakeRunner{processes: []*fakeProcess{process}}
	supervisor := newTestSupervisor(t, runner)
	invocation := testInvocation(t, testSource(t), "launch-finalize-inventory")
	outcome := supervisor.Start(context.Background(), invocation, &testSink{binding: invocation.LaunchToken()})
	if outcome.Kind != execution.Started {
		t.Fatalf("Start() = %#v", outcome)
	}
	record := supervisor.record(outcome.Handle)
	process.exit(0)
	waitDone(t, record.done)
	drainAll(t, supervisor, outcome.Handle)
	if err := supervisor.Finalize(context.Background(), outcome.Handle, execution.RetentionPolicy{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(record.root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Finalize retained runtime root: %v", err)
	}
	commands := runner.commands()
	if !commandsContain(commands, "label=chora.attempt_id=launch-finalize-inventory") ||
		!commandsContain(commands, record.container+"-check-[0-9]+") {
		t.Fatalf("Finalize omitted declared-check inventory proof: %#v", commands)
	}
}

func TestStopConfirmedProvesNoAttemptDockerResourcesOrRuntimeRoot(t *testing.T) {
	process := newFakeProcess()
	runner := &fakeRunner{processes: []*fakeProcess{process}, inspectMissing: true, exitOnRemove: true}
	supervisor := newTestSupervisor(t, runner)
	invocation := testInvocation(t, testSource(t), "launch-cancel-zero-residue")
	outcome := supervisor.Start(context.Background(), invocation, &testSink{binding: invocation.LaunchToken()})
	record := supervisor.record(outcome.Handle)
	stopped, err := supervisor.Stop(context.Background(), outcome.Handle, execution.StopIntent{Kind: execution.StopForCancel})
	if err != nil || stopped.Kind != execution.StopConfirmed {
		t.Fatalf("Stop() = %#v, %v", stopped, err)
	}
	if _, err := os.Stat(record.root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("confirmed Stop retained runtime root: %v", err)
	}
	commands := runner.commands()
	if !commandsContain(commands, "label=chora.attempt_id=launch-cancel-zero-residue") ||
		!commandsContain(commands, record.container+"-check-[0-9]+") {
		t.Fatalf("confirmed Stop omitted zero-residue proof: %#v", commands)
	}
}

func TestCleanupPreservesExactNameCollisionWithMismatchedOwnership(t *testing.T) {
	runner := &fakeRunner{
		exactContainerResidue: "collision-container-id", ownershipName: "/chora-aaaaaaaaaaaaaaaaaaaaaaaa-attempt",
		ownershipLabels: map[string]string{
			"chora.owner": "someone-else", "chora.runtime_scope": "different-scope",
			"chora.attempt_id": "different-attempt", "chora.policy_digest": testPolicyDigest,
		},
	}
	supervisor := newTestSupervisor(t, runner)
	root := filepath.Join(supervisor.config.RuntimeRoot, "chora-aaaaaaaaaaaaaaaaaaaaaaaa")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	record := &attemptRecord{
		root: root, attemptID: "attempt-owned", container: "chora-aaaaaaaaaaaaaaaaaaaaaaaa-attempt",
		boundary:        "chora-aaaaaaaaaaaaaaaaaaaaaaaa-codex-boundary",
		internalNetwork: "chora-aaaaaaaaaaaaaaaaaaaaaaaa-internal", upstreamNetwork: "chora-aaaaaaaaaaaaaaaaaaaaaaaa-upstream",
	}
	if err := supervisor.cleanup(context.Background(), record); err == nil || !strings.Contains(err.Error(), "ownership is unproven") {
		t.Fatalf("cleanup collision error = %v", err)
	}
	if commandsContain(runner.commands(), "rm -f collision-container-id") {
		t.Fatalf("cleanup removed unowned exact-name collision: %#v", runner.commands())
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("cleanup removed runtime root while collision ownership was unproven: %v", err)
	}
}

func TestCleanupInspectsExactNameCollisionEvenWhenLabelInventoryReturnsIt(t *testing.T) {
	runner := &fakeRunner{
		cleanupContainerResidue: "collision-container-id", exactContainerResidue: "collision-container-id",
		ownershipName: "/chora-aaaaaaaaaaaaaaaaaaaaaaaa-attempt",
		ownershipLabels: map[string]string{
			"chora.owner": "someone-else", "chora.runtime_scope": "different-scope",
			"chora.attempt_id": "different-attempt", "chora.policy_digest": testPolicyDigest,
		},
	}
	supervisor := newTestSupervisor(t, runner)
	root := filepath.Join(supervisor.config.RuntimeRoot, "chora-aaaaaaaaaaaaaaaaaaaaaaaa")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	record := &attemptRecord{
		root: root, attemptID: "attempt-owned", container: "chora-aaaaaaaaaaaaaaaaaaaaaaaa-attempt",
		boundary:        "chora-aaaaaaaaaaaaaaaaaaaaaaaa-codex-boundary",
		internalNetwork: "chora-aaaaaaaaaaaaaaaaaaaaaaaa-internal", upstreamNetwork: "chora-aaaaaaaaaaaaaaaaaaaaaaaa-upstream",
	}
	if err := supervisor.cleanup(context.Background(), record); err == nil || !strings.Contains(err.Error(), "ownership is unproven") {
		t.Fatalf("cleanup collision error = %v", err)
	}
	if commandsContain(runner.commands(), "rm -f collision-container-id") {
		t.Fatalf("cleanup removed unowned collision returned by label inventory: %#v", runner.commands())
	}
}

func TestCleanupRemovesExactNameFallbackOnlyAfterOwnershipVerification(t *testing.T) {
	runner := &fakeRunner{exactContainerResidue: "owned-container-id"}
	supervisor := newTestSupervisor(t, runner)
	root := filepath.Join(supervisor.config.RuntimeRoot, "chora-aaaaaaaaaaaaaaaaaaaaaaaa")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	record := &attemptRecord{
		root: root, attemptID: "attempt-owned", container: "chora-aaaaaaaaaaaaaaaaaaaaaaaa-attempt",
		boundary:        "chora-aaaaaaaaaaaaaaaaaaaaaaaa-codex-boundary",
		internalNetwork: "chora-aaaaaaaaaaaaaaaaaaaaaaaa-internal", upstreamNetwork: "chora-aaaaaaaaaaaaaaaaaaaaaaaa-upstream",
	}
	runner.ownershipName = "/" + record.container
	runner.ownershipLabels = map[string]string{
		"chora.owner": "dockersupervisor", "chora.runtime_scope": supervisor.recoveryScope,
		"chora.attempt_id": record.attemptID, "chora.policy_digest": testPolicyDigest,
	}
	if err := supervisor.cleanup(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if !commandsContain(runner.commands(), "rm -f owned-container-id") {
		t.Fatalf("cleanup did not remove verified exact-name resource: %#v", runner.commands())
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cleanup retained owned runtime root: %v", err)
	}
}

func TestRecoverRemovesAttemptAndDeclaredCheckOrphansAndProvesAbsence(t *testing.T) {
	runner := newOrphanLifecycleRunner([]string{"attempt-container", "attempt-container-check-1"}, []string{"attempt-network"})
	supervisor := newOrphanTestSupervisor(t, runner)
	createOwnedAttemptRoot(t, supervisor, "1", "attempt-one")
	createOwnedAttemptRoot(t, supervisor, "2", "attempt-two")
	if err := supervisor.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	containers, networks := runner.resources()
	if len(containers) != 0 || len(networks) != 0 {
		t.Fatalf("Recover retained Docker resources: containers=%v networks=%v", containers, networks)
	}
	if entries, err := os.ReadDir(supervisor.config.RuntimeRoot); err != nil || len(entries) != 0 {
		t.Fatalf("Recover retained runtime roots: entries=%v error=%v", entries, err)
	}
	if !commandsContain(runner.Commands(), "rm -f attempt-container-check-1") {
		t.Fatalf("Recover omitted declared-check orphan: %#v", runner.Commands())
	}
	for _, command := range runner.Commands() {
		joined := strings.Join(command.Args, " ")
		if strings.Contains(joined, "ps -aq") && strings.Contains(joined, "label=chora.policy_digest=") {
			t.Fatalf("Recover left older-policy orphans outside its owned scope: %v", command.Args)
		}
	}
}

func TestRecoverPreservesUnknownAndSymlinkRuntimeEntriesFailClosed(t *testing.T) {
	runner := newOrphanLifecycleRunner(nil, nil)
	supervisor := newOrphanTestSupervisor(t, runner)
	owned := createOwnedAttemptRoot(t, supervisor, "3", "owned-attempt")
	unknown := filepath.Join(supervisor.config.RuntimeRoot, "user-not-an-attempt")
	if err := os.Mkdir(unknown, 0o700); err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	symlink := filepath.Join(supervisor.config.RuntimeRoot, "chora-"+strings.Repeat("4", 24))
	if err := os.Symlink(external, symlink); err != nil {
		t.Fatal(err)
	}

	if err := supervisor.Recover(context.Background()); err == nil || !strings.Contains(err.Error(), "preserve unowned runtime entry") {
		t.Fatalf("Recover() error = %v", err)
	}
	if _, err := os.Stat(owned); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Recover retained valid owned Attempt root: %v", err)
	}
	if _, err := os.Stat(unknown); err != nil {
		t.Fatalf("Recover removed unknown user entry: %v", err)
	}
	if info, err := os.Lstat(symlink); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("Recover removed or followed symlink: info=%v error=%v", info, err)
	}
	if err := supervisor.RegisterRecoveredDead(execution.ProcessIdentity{Value: "chora-unowned-entry"}, execution.LaunchToken{}); err == nil {
		t.Fatal("Recover with preserved unknown entries granted cleanup proof")
	}
}

func TestRecoverReportsRemovalResidueAndWithholdsCleanupProof(t *testing.T) {
	runner := newOrphanLifecycleRunner([]string{"stuck-check"}, []string{"stuck-network"})
	runner.failContainerRemoval = true
	supervisor := newOrphanTestSupervisor(t, runner)
	orphanRoot := filepath.Join(supervisor.config.RuntimeRoot, "chora-stuck-root")
	if err := os.MkdirAll(orphanRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Recover(context.Background()); err == nil || !strings.Contains(err.Error(), "stuck-check") {
		t.Fatalf("Recover failure = %v", err)
	}
	if _, err := os.Stat(orphanRoot); err != nil {
		t.Fatalf("Recover erased runtime root while Docker death was unproven: %v", err)
	}
	if err := supervisor.RegisterRecoveredDead(execution.ProcessIdentity{Value: "chora-stuck"}, execution.LaunchToken{}); err == nil {
		t.Fatal("Recover failure incorrectly granted completed cleanup proof")
	}
}

func countQualificationCommands(commands []Command) int {
	count := 0
	for _, command := range commands {
		if isExecutionQualificationCommand(command.Args) {
			count++
		}
	}
	return count
}

type orphanLifecycleRunner struct {
	mu                   sync.Mutex
	qualification        fakeRunner
	commands             []Command
	containers           []string
	networks             []string
	failContainerRemoval bool
	ownershipScope       string
	ownershipMismatch    bool
}

func newOrphanLifecycleRunner(containers, networks []string) *orphanLifecycleRunner {
	return &orphanLifecycleRunner{containers: slices.Clone(containers), networks: slices.Clone(networks)}
}

func (runner *orphanLifecycleRunner) Run(_ context.Context, command Command) (CommandResult, error) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	runner.commands = append(runner.commands, command)
	if result, handled := runner.qualification.executionQualificationResult(command.Args); handled {
		return result, nil
	}
	args := command.Args
	switch {
	case len(args) >= 2 && args[0] == "ps" && args[1] == "-aq":
		return CommandResult{Stdout: []byte(strings.Join(runner.containers, "\n"))}, nil
	case len(args) >= 3 && args[0] == "network" && args[1] == "ls":
		return CommandResult{Stdout: []byte(strings.Join(runner.networks, "\n"))}, nil
	case len(args) == 5 && args[0] == "container" && args[1] == "inspect" && args[2] == "--format" && args[3] == containerOwnershipObservationFormat:
		labels := map[string]string{"chora.owner": "dockersupervisor", "chora.runtime_scope": runner.ownershipScope}
		if runner.ownershipMismatch {
			labels["chora.owner"] = "someone-else"
		}
		data, err := json.Marshal(resourceOwnershipObservation{Name: "/" + args[4], Labels: labels})
		return CommandResult{Stdout: data}, err
	case len(args) == 5 && args[0] == "network" && args[1] == "inspect" && args[2] == "--format" && args[3] == networkOwnershipObservationFormat:
		labels := map[string]string{"chora.owner": "dockersupervisor", "chora.runtime_scope": runner.ownershipScope}
		if runner.ownershipMismatch {
			labels["chora.owner"] = "someone-else"
		}
		data, err := json.Marshal(resourceOwnershipObservation{Name: args[4], Labels: labels})
		return CommandResult{Stdout: data}, err
	case len(args) == 3 && args[0] == "rm" && args[1] == "-f":
		if runner.failContainerRemoval {
			return CommandResult{ExitCode: 1, Stderr: []byte("injected removal failure")}, nil
		}
		runner.containers = removeString(runner.containers, args[2])
		return CommandResult{}, nil
	case len(args) == 3 && args[0] == "network" && args[1] == "rm":
		runner.networks = removeString(runner.networks, args[2])
		return CommandResult{}, nil
	case len(args) == 5 && args[0] == "network" && args[1] == "disconnect":
		return CommandResult{}, nil
	default:
		return CommandResult{ExitCode: 1, Stderr: []byte("unexpected Docker command")}, nil
	}
}

func (runner *orphanLifecycleRunner) Start(context.Context, Command) (Process, error) {
	return nil, errors.New("unexpected Start")
}

func (runner *orphanLifecycleRunner) Commands() []Command {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return slices.Clone(runner.commands)
}

func (runner *orphanLifecycleRunner) resources() ([]string, []string) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return slices.Clone(runner.containers), slices.Clone(runner.networks)
}

func newOrphanTestSupervisor(t *testing.T, runner CommandRunner) *Supervisor {
	t.Helper()
	root := t.TempDir()
	supervisor, err := New(Config{
		Runner: runner, RuntimeRoot: filepath.Join(root, "runtime"), ArtifactRoot: filepath.Join(root, "artifacts"),
		PolicyDigest: testPolicyDigest, AttemptImageID: testAttemptImageID, BoundaryImageID: testBoundaryImageID,
		RuntimeSourceIdentity: managedRuntimeSourceIdentity(testAttemptImageID),
		CapabilityContract:    testCapabilityContract(t), EngineQualification: testEngineQualification(t), TrustAnchorSource: testTrustAnchorSource(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	if orphanRunner, ok := runner.(*orphanLifecycleRunner); ok {
		orphanRunner.ownershipScope = supervisor.recoveryScope
	}
	return supervisor
}

func createOwnedAttemptRoot(t *testing.T, supervisor *Supervisor, hexDigit, attemptID string) string {
	t.Helper()
	name := "chora-" + strings.Repeat(hexDigit, 24)
	root := filepath.Join(supervisor.config.RuntimeRoot, name)
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.writeAttemptRootMarker(root, attemptID); err != nil {
		t.Fatal(err)
	}
	return root
}

func removeString(values []string, target string) []string {
	result := values[:0]
	for _, value := range values {
		if value != target {
			result = append(result, value)
		}
	}
	return result
}
