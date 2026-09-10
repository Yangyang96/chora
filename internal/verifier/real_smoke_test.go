package verifier

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/speccoding"
)

func TestRealDockerVerifierSmoke(t *testing.T) {
	if os.Getenv("CHORA_REAL_VERIFIER_SMOKE") != "1" {
		t.Skip("set CHORA_REAL_VERIFIER_SMOKE=1 for the adopted Docker/Colima verifier smoke")
	}
	baselineRoot := os.Getenv("CHORA_VERIFIER_BASELINE_ROOT")
	patchPath := os.Getenv("CHORA_VERIFIER_PATCH_PATH")
	sharedTempRoot := os.Getenv("CHORA_VERIFIER_SHARED_TEMP_ROOT")
	if !filepath.IsAbs(baselineRoot) || !filepath.IsAbs(patchPath) || !filepath.IsAbs(sharedTempRoot) {
		t.Fatal("real verifier smoke requires absolute baseline, Patch, and Docker-shared temp roots")
	}
	contractBytes := readSmokeFile(t, filepath.Join("..", "..", "contracts", "g2-m1a", "chora-m1-real-task.v8.json"))
	contract, err := speccoding.DecodeCoreContract(contractBytes)
	if err != nil {
		t.Fatal(err)
	}
	policyBytes := readSmokeFile(t, filepath.Join("..", "..", "contracts", "g2-m4", "verifier-policy.v1.json"))
	policy, err := DecodePolicyAuthority(policyBytes)
	if err != nil {
		t.Fatal(err)
	}
	patch := readSmokeFile(t, patchPath)
	baselineManifest := readSmokeFile(t, filepath.Join(filepath.Dir(baselineRoot), "manifest.json"))
	baselineDigest, err := digestHex(M1FrozenBaselineDigestHex)
	if err != nil {
		t.Fatal(err)
	}
	patchDigest := sha256.Sum256(patch)
	contractDigest, err := digestHex(contract.DigestHex())
	if err != nil {
		t.Fatal(err)
	}
	snapshotDigest, err := digestHex(contract.Document().Execution.Input.ContextSnapshotDigest)
	if err != nil {
		t.Fatal(err)
	}
	hostAttemptRoot, err := os.MkdirTemp(sharedTempRoot, "g2m4b-verifier-smoke-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(hostAttemptRoot) })
	runtimeRoot := filepath.Join(hostAttemptRoot, "verification-workspaces")
	workspaceFactory, err := NewFilesystemWorkspaceFactory(runtimeRoot, "git")
	if err != nil {
		t.Fatal(err)
	}
	dockerFactory, err := NewDockerSandboxFactory(DockerFactoryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(Config{Policy: policy, WorkspaceFactory: workspaceFactory, SandboxFactory: dockerFactory, Redactor: smokeRedactor{}})
	if err != nil {
		t.Fatal(err)
	}
	bindings := domain.VerificationBindings{
		BaselineDigest: baselineDigest, PatchDigest: patchDigest, ContextSnapshotDigest: snapshotDigest,
		AcceptanceContractDigest: contractDigest, VerifierPolicyVersion: policy.Document().Version, VerifierPolicyDigest: policy.Digest(),
	}
	evidence, err := service.Verify(context.Background(), VerifyRequest{
		Contract: contract, Bindings: bindings, RunID: "run-g2-m4b-smoke", VerificationRunID: "verification-run-g2-m4b-smoke",
		AttemptID: "verification-attempt-g2-m4b-smoke", BaselineRoot: baselineRoot,
		BaselineManifest: baselineManifest, AgentWorkspace: filepath.Join(hostAttemptRoot, "agent-workspace-never-reused"), Patch: patch,
	})
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Classification != AttemptCompleted || !evidence.CleanupProven || len(evidence.Commands) != 1 {
		t.Fatalf("untrusted smoke evidence: %#v", evidence)
	}
	command := evidence.Commands[0]
	if command.Kind != CommandExited || command.ExitCode == nil || *command.ExitCode != 0 || command.CommandID != "domain-tests" ||
		!equalSmokeStrings(command.Argv, []string{"go", "test", "./internal/domain"}) || len(command.CriterionIDs) != 2 {
		t.Fatalf("unexpected smoke command evidence: %#v", command)
	}
	entries, err := os.ReadDir(runtimeRoot)
	if err != nil || len(entries) != 0 {
		t.Fatalf("verification workspace residue: %v %#v", err, entries)
	}
	runner := ExecDockerRunner{}
	residue, err := runner.Run(context.Background(), DockerCommand{Args: []string{"container", "ls", "--all", "--quiet", "--filter", "label=chora.verification_attempt_id=verification-attempt-g2-m4b-smoke"}})
	if err != nil || residue.ExitCode != 0 || strings.TrimSpace(string(residue.Stdout)) != "" {
		t.Fatalf("verification container residue: %v %#v", err, residue)
	}
	t.Logf("PASS patch=%x policy=%x verifier=%s stdout_sha256=%x stdout_bytes=%d stderr_sha256=%x stderr_bytes=%d cleanup=%t",
		patchDigest, policy.Digest(), command.VerifierIdentity, command.Stdout.FullSHA256, command.Stdout.TotalBytes,
		command.Stderr.FullSHA256, command.Stderr.TotalBytes, evidence.CleanupProven)
}

type smokeRedactor struct{}

func (smokeRedactor) Version() string { return "chora.verifier-redaction.v1" }
func (smokeRedactor) Redact(body []byte) ([]byte, error) {
	return append([]byte(nil), body...), nil
}

func readSmokeFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func equalSmokeStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
