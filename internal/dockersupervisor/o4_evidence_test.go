package dockersupervisor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPublishO4BoundaryPublishesCompatibleCAndDResources(t *testing.T) {
	supervisor, ledger, runner, paths := newO4EvidenceTestSupervisor(t)
	defer ledger.Close()

	for sequence := 1; sequence <= 5; sequence++ {
		if sequence <= 4 {
			recordO4TestOperation(t, ledger, OperationPhaseAttempt)
		}
		recordO4TestOperation(t, ledger, OperationPhaseVerifier)
		resetO4ResidueRunner(runner)
		request := o4TestRequest(t, paths, O4BoundaryPhaseC, sequence)
		result, err := supervisor.PublishO4Boundary(context.Background(), request)
		if err != nil {
			t.Fatalf("publish C/%d: %v", sequence, err)
		}
		if result.Phase != O4BoundaryPhaseC || result.Sequence != sequence || result.ResourcePath != filepath.Join(paths.resource, o4CContracts[sequence-1].name) ||
			!validCanonicalDigest(result.A3LedgerSHA256) || !validCanonicalDigest(result.VerifierLedgerSHA256) || !validCanonicalDigest(result.ResourceSHA256) {
			t.Fatalf("unexpected C/%d result: %#v", sequence, result)
		}
		assertO4FileMode(t, result.ResourcePath, 0o400)
		var resource map[string]any
		readO4TestJSON(t, result.ResourcePath, &resource)
		if resource["schemaVersion"] != o4ProfileResourceSchema || resource["status"] != "terminal" || resource["profile"] != request.AgentExecutionProfile {
			t.Fatalf("incompatible C/%d resource: %#v", sequence, resource)
		}
	}

	resetO4ResidueRunner(runner)
	dRequest := o4TestRequest(t, paths, O4BoundaryPhaseD, 1)
	dResult, err := supervisor.PublishO4Boundary(context.Background(), dRequest)
	if err != nil {
		t.Fatal(err)
	}
	var dResource struct {
		SchemaVersion string         `json:"schemaVersion"`
		Status        string         `json:"status"`
		Sequence      int            `json:"sequence"`
		Scenario      string         `json:"scenario"`
		LedgerDigest  string         `json:"ledgerDigest"`
		Residue       map[string]int `json:"terminalResidue"`
	}
	readO4TestJSON(t, dResult.ResourcePath, &dResource)
	if dResource.SchemaVersion != o4RecoveryResourceSchema || dResource.Status != "terminal" || dResource.Sequence != 1 || dResource.Scenario != "failure" || !validCanonicalDigest(dResource.LedgerDigest) {
		t.Fatalf("incompatible D resource: %#v", dResource)
	}
	for field, count := range dResource.Residue {
		if count != 0 {
			t.Fatalf("D resource %s residue = %d", field, count)
		}
	}
	assertO4FileMode(t, paths.a3, 0o600)
	assertO4FileMode(t, paths.verifier, 0o600)
}

func TestPublishO4BoundaryRejectsReplayGapIdentityAndProfileWithoutMutation(t *testing.T) {
	supervisor, ledger, runner, paths := newO4EvidenceTestSupervisor(t)
	defer ledger.Close()
	recordO4TestOperation(t, ledger, OperationPhaseAttempt)
	recordO4TestOperation(t, ledger, OperationPhaseVerifier)
	resetO4ResidueRunner(runner)
	request := o4TestRequest(t, paths, O4BoundaryPhaseC, 1)
	if _, err := supervisor.PublishO4Boundary(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	wantA3, _ := os.ReadFile(paths.a3)
	wantVerifier, _ := os.ReadFile(paths.verifier)

	tests := []struct {
		name   string
		mutate func(*O4BoundaryRequest)
	}{
		{"replay", func(value *O4BoundaryRequest) {}},
		{"gap", func(value *O4BoundaryRequest) { value.Sequence = 3; value.AgentExecutionProfile = "standard" }},
		{"tuple", func(value *O4BoundaryRequest) {
			value.Sequence = 2
			value.AgentExecutionProfile = "standard"
			value.TupleIdentity = strings.Repeat("9", 64)
		}},
		{"profile", func(value *O4BoundaryRequest) { value.Sequence = 2; value.AgentExecutionProfile = "minimal" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := request
			test.mutate(&candidate)
			if _, err := supervisor.PublishO4Boundary(context.Background(), candidate); err == nil {
				t.Fatal("invalid boundary was published")
			}
			gotA3, _ := os.ReadFile(paths.a3)
			gotVerifier, _ := os.ReadFile(paths.verifier)
			if string(gotA3) != string(wantA3) || string(gotVerifier) != string(wantVerifier) {
				t.Fatal("rejected boundary mutated a live ledger")
			}
		})
	}
}

func TestPublishO4BoundaryRejectsUnsafeAndPartialOutputs(t *testing.T) {
	t.Run("symlink directory", func(t *testing.T) {
		supervisor, ledger, _, paths := newO4EvidenceTestSupervisor(t)
		defer ledger.Close()
		target := filepath.Join(filepath.Dir(paths.resource), "resource-target")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(filepath.Dir(paths.resource), "resource-link")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		request := o4TestRequest(t, paths, O4BoundaryPhaseC, 1)
		request.ResourceDirectory = link
		if _, err := supervisor.PublishO4Boundary(context.Background(), request); err == nil || !strings.Contains(err.Error(), "unsafe") {
			t.Fatalf("symlink output error = %v", err)
		}
	})

	t.Run("partial", func(t *testing.T) {
		supervisor, ledger, _, paths := newO4EvidenceTestSupervisor(t)
		defer ledger.Close()
		if err := os.WriteFile(paths.a3, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		request := o4TestRequest(t, paths, O4BoundaryPhaseC, 1)
		if _, err := supervisor.PublishO4Boundary(context.Background(), request); err == nil || !strings.Contains(err.Error(), "partial") {
			t.Fatalf("partial output error = %v", err)
		}
		if _, err := os.Lstat(filepath.Join(paths.resource, o4CContracts[0].name)); !os.IsNotExist(err) {
			t.Fatal("partial input produced a resource")
		}
	})

	t.Run("unsafe mode", func(t *testing.T) {
		supervisor, ledger, runner, paths := newO4EvidenceTestSupervisor(t)
		defer ledger.Close()
		recordO4TestOperation(t, ledger, OperationPhaseAttempt)
		recordO4TestOperation(t, ledger, OperationPhaseVerifier)
		resetO4ResidueRunner(runner)
		if _, err := supervisor.PublishO4Boundary(context.Background(), o4TestRequest(t, paths, O4BoundaryPhaseC, 1)); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(paths.a3, 0o644); err != nil {
			t.Fatal(err)
		}
		request := o4TestRequest(t, paths, O4BoundaryPhaseC, 2)
		if _, err := supervisor.PublishO4Boundary(context.Background(), request); err == nil || !strings.Contains(err.Error(), "unsafe") {
			t.Fatalf("unsafe-mode error = %v", err)
		}
	})

	t.Run("hard link", func(t *testing.T) {
		supervisor, ledger, runner, paths := newO4EvidenceTestSupervisor(t)
		defer ledger.Close()
		recordO4TestOperation(t, ledger, OperationPhaseAttempt)
		recordO4TestOperation(t, ledger, OperationPhaseVerifier)
		resetO4ResidueRunner(runner)
		if _, err := supervisor.PublishO4Boundary(context.Background(), o4TestRequest(t, paths, O4BoundaryPhaseC, 1)); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(paths.a3, filepath.Join(filepath.Dir(paths.a3), "a3-hard-link.json")); err != nil {
			t.Fatal(err)
		}
		request := o4TestRequest(t, paths, O4BoundaryPhaseC, 2)
		if _, err := supervisor.PublishO4Boundary(context.Background(), request); err == nil || !strings.Contains(err.Error(), "links") {
			t.Fatalf("hard-link error = %v", err)
		}
	})

	t.Run("caller JSON cannot supply product facts", func(t *testing.T) {
		var request O4BoundaryRequest
		input := `{"phase":"C","sequence":1,"taskId":"task_00000001-0000-7000-8000-000000000001","runId":"run_00000001-0000-7000-8000-000000000001","attemptId":"attempt_00000001-0000-7000-8000-000000000001","agentExecutionProfile":"minimal","observationDigest":"` + strings.Repeat("1", 64) + `","tupleIdentity":"` + strings.Repeat("2", 64) + `","generationId":"gen_aaaaaaaaaaaaaaaaaaaaaaaa","productObservation":{"environmentId":"env_forged"}}`
		if err := json.Unmarshal([]byte(input), &request); err != nil {
			t.Fatal(err)
		}
		if request.ProductObservation.EnvironmentID != "" {
			t.Fatal("untrusted JSON populated ProductObservation")
		}
	})
}

type o4TestPaths struct {
	a3, verifier, resource string
}

func newO4EvidenceTestSupervisor(t *testing.T) (*Supervisor, *OperationLedger, *fakeRunner, o4TestPaths) {
	t.Helper()
	runner := &fakeRunner{}
	supervisor, ledger := newResidueTestSupervisor(t, runner)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	paths := o4TestPaths{
		a3: filepath.Join(root, "a3", "live.json"), verifier: filepath.Join(root, "verifier", "live.json"),
		resource: filepath.Join(root, "resources"),
	}
	for _, directory := range []string{filepath.Dir(paths.a3), filepath.Dir(paths.verifier), paths.resource} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return supervisor, ledger, runner, paths
}

func recordO4TestOperation(t *testing.T, ledger *OperationLedger, phase OperationPhase) {
	t.Helper()
	if _, err := RunnerForOperationPhase(ledger, phase).Run(context.Background(), Command{Args: []string{"inspect", "o4-test"}}); err != nil {
		t.Fatal(err)
	}
}

func resetO4ResidueRunner(runner *fakeRunner) {
	runner.mu.Lock()
	runner.runResults = []CommandResult{
		{ExitCode: 0}, {ExitCode: 0}, {ExitCode: 0}, {ExitCode: 0},
		{ExitCode: 0, Stdout: []byte(`"inactive"|false|""` + "\n")},
	}
	runner.mu.Unlock()
}

func o4TestRequest(t *testing.T, paths o4TestPaths, phase O4BoundaryPhase, sequence int) O4BoundaryRequest {
	t.Helper()
	profile := "standard"
	if phase == O4BoundaryPhaseC {
		profile = o4CContracts[sequence-1].profile
	}
	suffix := string(rune('a' + sequence))
	request := O4BoundaryRequest{
		Phase: phase, Sequence: sequence,
		TaskID:                "task_00000001-0000-7000-8000-00000000000" + string(rune('0'+sequence)),
		RunID:                 "run_00000002-0000-7000-8000-00000000000" + string(rune('0'+sequence)),
		AttemptID:             "attempt_00000003-0000-7000-8000-00000000000" + string(rune('0'+sequence)),
		AgentExecutionProfile: profile, ObservationDigest: strings.Repeat(suffix, 64),
		TupleIdentity: strings.Repeat("d", 64), GenerationID: "gen_" + strings.Repeat("g", 24),
		A3Path: paths.a3, VerifierPath: paths.verifier, ResourceDirectory: paths.resource,
		ProductObservation: O4ProductObservation{
			EnvironmentID: "env_" + strings.Repeat("e", 24), InstallID: "ins_" + strings.Repeat("i", 24),
			BindingDigest: strings.Repeat("b", 64), Platform: O4PlatformObservation{OS: "darwin", Architecture: "arm64"},
			RoleImages: map[string]O4RoleImageObservation{
				"managed_pi_runtime": {ArtifactID: "managed-pi-runtime", ArchiveSHA256: strings.Repeat("1", 64), ArchiveSize: 101, DockerConfigImageID: testAttemptImageID},
				"network_boundary":   {ArtifactID: "network-boundary", ArchiveSHA256: strings.Repeat("2", 64), ArchiveSize: 103, DockerConfigImageID: testBoundaryImageID},
			},
			WorkspaceIdentity: "sha256:" + strings.Repeat(suffix, 64), WorkspaceID: "wsp_" + strings.Repeat(suffix, 24),
			RuntimeIdentitySHA256: strings.Repeat("c", 64),
		},
	}
	request.ProductObservation.ExecutionIdentityDigest, _ = o4CanonicalDigest(map[string]any{
		"tupleIdentity": request.TupleIdentity, "taskId": request.TaskID, "runId": request.RunID,
		"attemptId": request.AttemptID, "workspaceIdentity": request.ProductObservation.WorkspaceIdentity,
	})
	if profile == "trusted_local" {
		trusted := &O4TrustedHostObservation{
			SelectedPiPath: "/private/o4/pi", SelectedPiSourceRoot: "/private/o4/source", SelectedSource: "path",
			PiExecutableSHA256: strings.Repeat("3", 64), PiSourceProvenanceSHA256: strings.Repeat("4", 64),
			RuntimeFingerprint: strings.Repeat("5", 64), ProcessGroupIdentitySHA256: strings.Repeat("6", 64),
			SessionIdentitySHA256: strings.Repeat("7", 64), ProcessStartedAt: time.Date(2026, 8, 30, 1, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
			ProcessExitedAt:        time.Date(2026, 8, 30, 1, 1, 0, 0, time.UTC).Format(time.RFC3339Nano),
			ProcessGroupTerminated: true, SessionClosed: true,
		}
		request.ProductObservation.TrustedHost = trusted
		trusted.TerminalCleanupDigest, _ = o4CanonicalDigest(o4EmptyInventory())
		request.ProductObservation.RuntimeIdentitySHA256, _ = o4CanonicalDigest(map[string]any{
			"runtimeFingerprint": trusted.RuntimeFingerprint, "processGroupIdentitySha256": trusted.ProcessGroupIdentitySHA256,
			"sessionIdentitySha256": trusted.SessionIdentitySHA256,
		})
	}
	return request
}

func readO4TestJSON(t *testing.T, path string, output any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, output); err != nil {
		t.Fatal(err)
	}
}

func assertO4FileMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != mode || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("%s mode = %v", path, info.Mode())
	}
}
