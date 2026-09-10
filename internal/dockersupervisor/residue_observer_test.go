package dockersupervisor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResidueObserverProvesZeroWithAuthenticatedSwarmInactive(t *testing.T) {
	runner := &fakeRunner{runResults: []CommandResult{
		{ExitCode: 0}, {ExitCode: 0}, {ExitCode: 0}, {ExitCode: 0},
		{ExitCode: 0, Stdout: []byte(`"inactive"|false|""` + "\n")},
	}}
	supervisor, ledger := newResidueTestSupervisor(t, runner)
	defer ledger.Close()

	observation, err := supervisor.ObserveResidue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if observation.SchemaVersion != residueObservationSchema || observation.Status != "proven" ||
		!observation.SwarmInactive || !observation.ServingProcessExcluded || !observation.OperationLedgerSettled ||
		observation.Containers.Count != 0 || observation.VerifierContainers.Count != 0 || observation.Networks.Count != 0 || observation.Volumes.Count != 0 ||
		observation.Configs.Count != 0 || observation.ManagedWorkspaces.Count != 0 || observation.AttemptProcessGroups.Count != 0 ||
		len(observation.Containers.AggregateSHA256) != 64 || observation.OperationLedgerCount == 0 || len(observation.OperationLedgerSHA256) != 64 {
		t.Fatalf("unsafe or incomplete zero observation: %#v", observation)
	}
	if commandsContain(runner.allCommands(), "config ls") {
		t.Fatal("Swarm-inactive proof still attempted a config listing")
	}
}

func TestResidueObserverListsConfigsForAuthenticatedActiveManager(t *testing.T) {
	runner := &fakeRunner{runResults: []CommandResult{
		{ExitCode: 0}, {ExitCode: 0}, {ExitCode: 0}, {ExitCode: 0},
		{ExitCode: 0, Stdout: []byte(`"active"|true|"node-1"` + "\n")},
		{ExitCode: 0},
	}}
	supervisor, ledger := newResidueTestSupervisor(t, runner)
	defer ledger.Close()

	observation, err := supervisor.ObserveResidue(context.Background())
	if err != nil || observation.SwarmInactive || observation.Configs.Count != 0 {
		t.Fatalf("active-manager observation = %#v, %v", observation, err)
	}
	if !commandsContain(runner.allCommands(), "config ls -q") {
		t.Fatal("active manager did not enumerate configs")
	}
}

func TestResidueObserverFailsClosedOnUnknownResourceAndWorkspace(t *testing.T) {
	t.Run("resource", func(t *testing.T) {
		runner := &fakeRunner{
			runResults:      []CommandResult{{ExitCode: 0, Stdout: []byte("candidate-id\n")}},
			ownershipName:   "/untrusted",
			ownershipLabels: map[string]string{"chora.owner": "dockersupervisor"},
		}
		supervisor, ledger := newResidueTestSupervisor(t, runner)
		defer ledger.Close()
		if _, err := supervisor.ObserveResidue(context.Background()); err == nil || !strings.Contains(err.Error(), "ownership is unknown") {
			t.Fatalf("unknown resource error = %v", err)
		}
	})

	t.Run("prefix alone", func(t *testing.T) {
		runner := &fakeRunner{runResults: []CommandResult{
			{ExitCode: 0}, {ExitCode: 0}, {ExitCode: 0}, {ExitCode: 0},
			{ExitCode: 0, Stdout: []byte(`"inactive"|false|""` + "\n")},
		}}
		supervisor, ledger := newResidueTestSupervisor(t, runner)
		defer ledger.Close()
		path := filepath.Join(supervisor.config.RuntimeRoot, "chora-"+strings.Repeat("a", 24))
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := supervisor.ObserveResidue(context.Background()); err == nil || !strings.Contains(err.Error(), "unknown managed workspace") {
			t.Fatalf("prefix-only workspace error = %v", err)
		}
	})
}

func newResidueTestSupervisor(t *testing.T, runner *fakeRunner) (*Supervisor, *OperationLedger) {
	t.Helper()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := OpenOperationLedger(runner, filepath.Join(root, "state"), "gen_residue-test")
	if err != nil {
		t.Fatal(err)
	}
	supervisor, err := New(Config{
		Runner: ledger, RuntimeRoot: filepath.Join(root, "runtime"),
		PolicyDigest: testPolicyDigest, AttemptImageID: testAttemptImageID, BoundaryImageID: testBoundaryImageID, VerifierImageID: testAttemptImageID,
		CapabilityContract: testCapabilityContract(t), EngineQualification: testEngineQualification(t),
		RuntimeSourceIdentity: managedRuntimeSourceIdentity(testAttemptImageID),
		CredentialSource:      testCredentialSource(t), TrustAnchorSource: testTrustAnchorSource(t),
	})
	if err != nil {
		_ = ledger.Close()
		t.Fatal(err)
	}
	return supervisor, ledger
}
