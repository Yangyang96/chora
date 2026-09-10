package verifier

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/speccoding"
)

func TestPolicyProjectionRequiresVerifiedDigestAndExactM1Boundary(t *testing.T) {
	document := validPolicyDocument()
	digest := sha256.Sum256([]byte("formally-verified-policy"))
	policy, err := newPolicyProjection(document, digest)
	if err != nil {
		t.Fatalf("decode valid policy: %v", err)
	}
	if policy.Digest() != digest || policy.Document() != document {
		t.Fatal("policy did not freeze document and digest")
	}

	if _, err := newPolicyProjection(document, [32]byte{}); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("zero verified digest error = %v", err)
	}
	document.NetworkDisabled = false
	if _, err := newPolicyProjection(document, digest); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("network-enabled error = %v", err)
	}
}

func TestDecodePolicyAuthorityMapsOnlyDigestVerifiedFormalPolicy(t *testing.T) {
	for _, fixture := range []struct {
		name string
		mode string
	}{
		{"verifier-policy.v1.json", PolicyModeFull},
		{"verifier-policy.acceptance-only.v1.json", PolicyModeAcceptanceOnly},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("..", "..", "contracts", "g2-m4", fixture.name))
			if err != nil {
				t.Fatal(err)
			}
			policy, err := DecodePolicyAuthority(data)
			if err != nil {
				t.Fatal(err)
			}
			if policy.Digest() != sha256.Sum256(data) || policy.Document().Mode != fixture.mode || policy.Document().Image != "sha256:91698efead5641a633519f5f229373e08a59264045ca27f6d01fc06505deeea7" {
				t.Fatalf("projection = %#v", policy.Document())
			}
			if _, err := DecodePolicyAuthority(append(append([]byte{}, data...), ' ')); err == nil {
				t.Fatal("byte-drifted formal policy accepted")
			}
		})
	}
}

func TestFreezeCommandsUsesAcceptanceV8ArgvAndBindingsOnly(t *testing.T) {
	acceptance := speccoding.AcceptanceContract{
		VerificationCommands: []speccoding.BoundedCommand{
			{ID: "domain-tests", Argv: []string{"go", "test", "./internal/domain", "-count=1"}},
			{ID: "lint", Argv: []string{"go", "vet", "./internal/domain"}},
		},
		Criteria: []speccoding.AcceptanceCriterionContract{
			{ID: "criterion-a", VerificationCommandIDs: []string{"domain-tests"}},
			{ID: "criterion-b", VerificationCommandIDs: []string{"domain-tests", "lint"}},
		},
	}
	commands, err := freezeCommands(speccoding.CoreContractSchemaVersionV8, acceptance)
	if err != nil {
		t.Fatal(err)
	}
	want := []frozenCommand{
		{ID: "domain-tests", Argv: []string{"go", "test", "./internal/domain", "-count=1"}, CriterionIDs: []string{"criterion-a", "criterion-b"}},
		{ID: "lint", Argv: []string{"go", "vet", "./internal/domain"}, CriterionIDs: []string{"criterion-b"}},
	}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("commands = %#v, want %#v", commands, want)
	}
	acceptance.VerificationCommands[0].Argv[0] = "agent-command"
	if commands[0].Argv[0] != "go" {
		t.Fatal("frozen argv aliases contract storage")
	}
	if _, err := freezeCommands(speccoding.CoreContractSchemaVersionV7, acceptance); !errors.Is(err, ErrInvalidAuthority) {
		t.Fatalf("legacy authority error = %v", err)
	}
}

func TestFilesystemWorkspaceIsFreshDigestBoundAndBoundaryChecked(t *testing.T) {
	root := t.TempDir()
	if output, err := exec.Command("git", "init", "--quiet", root).CombinedOutput(); err != nil {
		t.Fatalf("init enclosing Git repository: %v: %s", err, output)
	}
	mustWrite(t, filepath.Join(root, "internal", "domain", "value.go"), "package domain\n\nconst Value = 1\n")
	baseline := filepath.Join(root, "baseline")
	workspaces := filepath.Join(root, "attempts")
	mustWrite(t, filepath.Join(baseline, "internal", "domain", "value.go"), "package domain\n\nconst Value = 1\n")
	baselineDigest, err := DigestTree(baseline)
	if err != nil {
		t.Fatal(err)
	}
	patch := []byte("diff --git a/internal/domain/value.go b/internal/domain/value.go\n--- a/internal/domain/value.go\n+++ b/internal/domain/value.go\n@@ -1,3 +1,3 @@\n package domain\n \n-const Value = 1\n+const Value = 2\n")
	factory, err := NewFilesystemWorkspaceFactory(workspaces, "git")
	if err != nil {
		t.Fatal(err)
	}
	poison := t.TempDir()
	t.Setenv("HOME", poison)
	t.Setenv("XDG_CONFIG_HOME", poison)
	t.Setenv("GIT_DIR", filepath.Join(poison, "foreign.git"))
	t.Setenv("GIT_WORK_TREE", poison)
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.hooksPath")
	t.Setenv("GIT_CONFIG_VALUE_0", "/forbidden/hooks")
	t.Setenv("GIT_EXEC_PATH", poison)
	lease, err := factory.Prepare(context.Background(), WorkspaceRequest{
		BaselineRoot: baseline, AgentWorkspace: filepath.Join(root, "agent"), ExpectedBaselineDigest: baselineDigest,
		Patch: patch, ExpectedPatchDigest: sha256.Sum256(patch), WritableFiles: []string{"internal/domain/value.go"},
		VerificationAttemptID: "verification-attempt-1", PolicyDigest: sha256.Sum256([]byte("policy")),
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if lease.Root() == baseline || !reflect.DeepEqual(lease.TouchedFiles(), []string{"internal/domain/value.go"}) {
		t.Fatalf("workspace not fresh/bound: %s %#v", lease.Root(), lease.TouchedFiles())
	}
	markerPath := filepath.Join(lease.Root(), verificationWorkspaceMarkerName)
	markerBytes, markerErr := os.ReadFile(markerPath)
	markerInfo, markerStatErr := os.Stat(markerPath)
	if markerErr != nil || markerStatErr != nil || markerInfo.Mode().Perm() != 0o400 ||
		!bytes.Contains(markerBytes, []byte(`"verification_attempt_id":"verification-attempt-1"`)) ||
		!bytes.Contains(markerBytes, []byte(`"workspace_identity":"sha256:`)) ||
		!bytes.Contains(markerBytes, []byte(`"policy_digest":"`)) {
		t.Fatalf("immutable workspace marker = %q info=%#v readErr=%v statErr=%v", markerBytes, markerInfo, markerErr, markerStatErr)
	}
	observed, err := factory.ObserveWorkspaces()
	if err != nil || observed.Count != 1 || len(observed.AggregateSHA256) != 64 {
		t.Fatalf("workspace residue observation = %#v, %v", observed, err)
	}
	for _, relative := range []string{".chora-cache/go-build", ".chora-tmp"} {
		info, err := os.Stat(filepath.Join(lease.Root(), relative))
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0o777 {
			t.Fatalf("attempt-owned tooling path %s = %#v, %v", relative, info, err)
		}
	}
	got, err := os.ReadFile(filepath.Join(lease.Root(), "internal", "domain", "value.go"))
	if err != nil || !bytes.Contains(got, []byte("Value = 2")) {
		t.Fatalf("patch result %q, %v", got, err)
	}
	parent, err := os.ReadFile(filepath.Join(root, "internal", "domain", "value.go"))
	if err != nil || !bytes.Contains(parent, []byte("Value = 1")) {
		t.Fatalf("Patch escaped into enclosing Git repository: %q, %v", parent, err)
	}
	if err := lease.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lease.Root()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace survived cleanup: %v", err)
	}

	badPatch := bytes.ReplaceAll(patch, []byte("internal/domain/value.go"), []byte("outside.go"))
	if _, err := factory.Prepare(context.Background(), WorkspaceRequest{
		BaselineRoot: baseline, AgentWorkspace: filepath.Join(root, "agent"), ExpectedBaselineDigest: baselineDigest,
		Patch: badPatch, ExpectedPatchDigest: sha256.Sum256(badPatch), WritableFiles: []string{"internal/domain/value.go"},
		VerificationAttemptID: "verification-attempt-2", PolicyDigest: sha256.Sum256([]byte("policy")),
	}); !errors.Is(err, ErrPatchBoundary) {
		t.Fatalf("boundary error = %v", err)
	}
	if _, err := factory.Prepare(context.Background(), WorkspaceRequest{
		BaselineRoot: baseline, AgentWorkspace: filepath.Join(root, "agent"), ExpectedBaselineDigest: baselineDigest,
		Patch: nil, ExpectedPatchDigest: sha256.Sum256(nil), WritableFiles: []string{"internal/domain/value.go"},
		VerificationAttemptID: "verification-attempt-3", PolicyDigest: sha256.Sum256([]byte("policy")),
	}); !errors.Is(err, ErrEmptyPatch) {
		t.Fatalf("empty error = %v", err)
	}
}

func TestDigestTreeUsesFreezeV4CanonicalAggregate(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "a.txt"), "A\n")
	mustWrite(t, filepath.Join(root, "dir", "b.sh"), "B\n")
	if err := os.Chmod(filepath.Join(root, "dir", "b.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	aDigest := sha256.Sum256([]byte("A\n"))
	bDigest := sha256.Sum256([]byte("B\n"))
	canonical := "a.txt\x00" + fmt.Sprintf("%x", aDigest) + "\x000644\n" +
		"dir/b.sh\x00" + fmt.Sprintf("%x", bDigest) + "\x000755\n"
	want := sha256.Sum256([]byte(canonical))
	got, err := DigestTree(root)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("freeze-v4 digest = %x, want %x for %q", got, want, canonical)
	}
	if M1FrozenBaselineDigestHex != "b28cb1624124736e745f2b217e841957330f763b6b41fab87b2a88b809b8a0cd" {
		t.Fatal("M1 baseline identity drift")
	}
}

func TestVerifyBaselineAuthorityAcceptsV6Manifest(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "value.txt")
	mustWrite(t, path, "value\n")
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("value\n"))
	canonical := fmt.Sprintf("value.txt\x00%x\x000444\n", digest)
	aggregate := sha256.Sum256([]byte(canonical))
	manifest := fmt.Sprintf(`{"schema_version":"chora.source-baseline.v6","source_revision":"revision","task_id":"task","context_snapshot_id":"snapshot","context_snapshot_digest":"%s","inclusion_rules":[],"exclusion_rules":[],"entry_count":1,"aggregate_sha256":"%x","entries":[{"path":"value.txt","sha256":"%x","mode":"0444"}]}`,
		strings.Repeat("a", 64), aggregate, digest)
	got, err := verifyBaselineAuthority(root, []byte(manifest), aggregate)
	if err != nil {
		t.Fatalf("verify v6 baseline authority: %v", err)
	}
	if got != aggregate {
		t.Fatalf("aggregate = %x, want %x", got, aggregate)
	}
}

func TestCaptureRetainsBoundedRedactedBodyAndHashesFullStream(t *testing.T) {
	redactor := fixedRedactor{version: "redaction-v1", secret: []byte("TOKEN"), mask: []byte("*****")}
	capture := newStreamCapture(8, redactor)
	full := []byte("TOKEN-0123456789")
	if _, err := io.Copy(capture, bytes.NewReader(full)); err != nil {
		t.Fatal(err)
	}
	log, err := capture.finish()
	if err != nil {
		t.Fatal(err)
	}
	if log.TotalBytes != int64(len(full)) || log.FullSHA256 != sha256.Sum256(full) || log.RetainedBody != "*****-01" || !log.Truncated || log.TruncationBoundary != 8 || log.RedactionPolicyVersion != "redaction-v1" {
		t.Fatalf("log = %#v", log)
	}
}

func TestExecuteCommandsClassifiesTrustedNonzeroTimeoutCancelAndCleanup(t *testing.T) {
	policy := mustPolicy(t)
	commands := []frozenCommand{{ID: "test", Argv: []string{"go", "test"}, CriterionIDs: []string{"criterion-a"}}}
	for _, test := range []struct {
		name    string
		outcome CommandOutcome
		cancel  bool
		want    AttemptClassification
	}{
		{name: "nonzero", outcome: CommandOutcome{Kind: CommandExited, ExitCode: intPtr(1)}, want: AttemptCompleted},
		{name: "timeout", outcome: CommandOutcome{Kind: CommandTimedOut}, want: AttemptCompleted},
		{name: "cancel", outcome: CommandOutcome{Kind: CommandCancelled}, cancel: true, want: AttemptCancelled},
	} {
		t.Run(test.name, func(t *testing.T) {
			sandbox := &fakeSandbox{outcome: test.outcome}
			ctx := context.Background()
			if test.cancel {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			evidence := executeCommands(ctx, sandbox, policy, commands, identitySet(), fixedRedactor{version: "r1"}, time.Now)
			if evidence.Classification != test.want {
				t.Fatalf("classification = %s, want %s (%s)", evidence.Classification, test.want, evidence.Reason)
			}
			if (test.outcome.Kind == CommandTimedOut || test.cancel) && sandbox.terminateCalls != 1 {
				t.Fatalf("terminate calls = %d", sandbox.terminateCalls)
			}
			if len(evidence.Commands) != 1 || evidence.Commands[0].Argv[0] != "go" {
				t.Fatalf("evidence = %#v", evidence.Commands)
			}
		})
	}

	sandbox := &fakeSandbox{outcome: CommandOutcome{Kind: CommandTimedOut}, terminateErr: errors.New("death unproven")}
	evidence := executeCommands(context.Background(), sandbox, policy, commands, identitySet(), fixedRedactor{version: "r1"}, time.Now)
	if evidence.Classification != AttemptRecoveryRequired {
		t.Fatalf("termination uncertainty = %s", evidence.Classification)
	}
}

func TestAcceptanceOnlyPolicyProducesTrustedUnavailableEvidenceWithoutExecuting(t *testing.T) {
	document := validPolicyDocument()
	document.Mode = PolicyModeAcceptanceOnly
	policy, err := newPolicyProjection(document, sha256.Sum256([]byte("acceptance-only-policy")))
	if err != nil {
		t.Fatal(err)
	}
	if policy.Document().Mode != PolicyModeAcceptanceOnly {
		t.Fatal("acceptance-only projection drift")
	}
	sandbox := &fakeSandbox{}
	commands := []frozenCommand{{ID: "domain-tests", Argv: []string{"go", "test", "./internal/domain"}, CriterionIDs: []string{"criterion-a", "criterion-b"}}}
	evidence := unavailableCommandEvidence(sandbox, commands, identitySet(), fixedRedactor{version: "chora.verifier-redaction.v1"}, time.Now)
	if evidence.Classification != AttemptCompleted || len(evidence.Commands) != 1 || evidence.Commands[0].Kind != CommandUnavailable || !strings.Contains(evidence.Reason, "deterministically unavailable") {
		t.Fatalf("evidence = %#v", evidence)
	}
	if sandbox.executeCalls != 0 || evidence.Commands[0].Stdout.FullSHA256 != sha256.Sum256(nil) || evidence.Commands[0].Stdout.TotalBytes != 0 {
		t.Fatalf("acceptance-only executed or fabricated output: calls=%d evidence=%#v", sandbox.executeCalls, evidence.Commands[0])
	}
}

func TestVerifierOrchestratesFormalPolicyFreshWorkspaceEvidenceAndCleanup(t *testing.T) {
	contractBytes, err := os.ReadFile(filepath.Join("..", "..", "contracts", "g2-m1a", "chora-m1-real-task.v8.json"))
	if err != nil {
		t.Fatal(err)
	}
	contract, err := speccoding.DecodeCoreContract(contractBytes)
	if err != nil {
		t.Fatal(err)
	}
	contractDigest, _ := digestHex(contract.DigestHex())
	retrySnapshotDigest := sha256.Sum256([]byte("lineage-validated-retry-snapshot"))
	patch := []byte("authoritative-patch")
	baselineDigest := sha256.Sum256([]byte("frozen-baseline"))
	patchDigest := sha256.Sum256(patch)
	for _, fixture := range []struct {
		name      string
		wantKind  CommandKind
		wantCalls int
	}{
		{"verifier-policy.v1.json", CommandExited, 1},
		{"verifier-policy.acceptance-only.v1.json", CommandUnavailable, 0},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			policyBytes, err := os.ReadFile(filepath.Join("..", "..", "contracts", "g2-m4", fixture.name))
			if err != nil {
				t.Fatal(err)
			}
			policy, err := DecodePolicyAuthority(policyBytes)
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			lease := &fakeWorkspaceLease{root: root, baseline: baselineDigest, patch: patchDigest}
			workspaceFactory := &fakeWorkspaceFactory{lease: lease}
			exitCode := 0
			sandbox := &fakeSandbox{outcome: CommandOutcome{Kind: CommandExited, ExitCode: &exitCode}}
			sandboxFactory := &fakeSandboxFactory{sandbox: sandbox}
			service, err := New(Config{Policy: policy, WorkspaceFactory: workspaceFactory, SandboxFactory: sandboxFactory, Redactor: fixedRedactor{version: "chora.verifier-redaction.v1"}})
			if err != nil {
				t.Fatal(err)
			}
			bindings := domain.VerificationBindings{BaselineDigest: baselineDigest, PatchDigest: patchDigest, ContextSnapshotDigest: retrySnapshotDigest,
				AcceptanceContractDigest: contractDigest, VerifierPolicyVersion: policy.Document().Version, VerifierPolicyDigest: policy.Digest()}
			evidence, err := service.Verify(context.Background(), VerifyRequest{Contract: contract, Bindings: bindings, RunID: "run-1", VerificationRunID: "verification-run-1",
				AttemptID: "verification-attempt-1", BaselineRoot: filepath.Join(root, "baseline-authority"), BaselineManifest: []byte("frozen-baseline-manifest-authority"),
				AgentWorkspace: filepath.Join(root, "agent-workspace"), Patch: patch})
			if err != nil {
				t.Fatal(err)
			}
			if evidence.Classification != AttemptCompleted || !evidence.CleanupProven || len(evidence.Commands) != 1 || evidence.Commands[0].Kind != fixture.wantKind || sandbox.executeCalls != fixture.wantCalls {
				t.Fatalf("evidence = %#v; execute calls = %d", evidence, sandbox.executeCalls)
			}
			if evidence.Commands[0].Identity.ContextSnapshotDigest != retrySnapshotDigest {
				t.Fatalf("Verifier evidence digest = %x, want retry digest %x", evidence.Commands[0].Identity.ContextSnapshotDigest, retrySnapshotDigest)
			}
			if lease.cleanupCalls != 1 || sandbox.cleanupCalls != 1 || workspaceFactory.request.AgentWorkspace == root || sandboxFactory.spec.Identity.WorkspaceIdentity != workspaceIdentity(root) {
				t.Fatalf("workspace/sandbox lifecycle = %#v %#v", workspaceFactory, sandboxFactory)
			}
		})
	}
}

func TestVerifierRejectsMissingFrozenBaselineManifestAuthority(t *testing.T) {
	contractBytes, err := os.ReadFile(filepath.Join("..", "..", "contracts", "g2-m1a", "chora-m1-real-task.v8.json"))
	if err != nil {
		t.Fatal(err)
	}
	contract, err := speccoding.DecodeCoreContract(contractBytes)
	if err != nil {
		t.Fatal(err)
	}
	contractDigest, _ := digestHex(contract.DigestHex())
	snapshotDigest, _ := digestHex(contract.Document().Execution.Input.ContextSnapshotDigest)
	patch := []byte("authoritative-patch")
	baselineDigest := sha256.Sum256([]byte("frozen-baseline"))
	policy := mustPolicy(t)
	bindings := domain.VerificationBindings{BaselineDigest: baselineDigest, PatchDigest: sha256.Sum256(patch), ContextSnapshotDigest: snapshotDigest,
		AcceptanceContractDigest: contractDigest, VerifierPolicyVersion: policy.Document().Version, VerifierPolicyDigest: policy.Digest()}
	service, err := New(Config{Policy: policy, WorkspaceFactory: &fakeWorkspaceFactory{}, SandboxFactory: &fakeSandboxFactory{}, Redactor: fixedRedactor{version: "r1"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Verify(context.Background(), VerifyRequest{Contract: contract, Bindings: bindings, RunID: "run-1", VerificationRunID: "verification-run-1",
		AttemptID: "verification-attempt-1", BaselineRoot: t.TempDir(), AgentWorkspace: filepath.Join(t.TempDir(), "agent"), Patch: patch})
	if !errors.Is(err, ErrInvalidAuthority) {
		t.Fatalf("missing baseline manifest error = %v", err)
	}
}

func TestDockerSandboxUsesPinnedIsolationDirectArgvAndProvesRemoval(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	runner := &fakeDockerRunner{image: validPolicyDocument().Image, containerID: strings.Repeat("c", 64), workspace: workspace}
	recoveryScope := "sha256:" + strings.Repeat("d", 64)
	factory, err := NewDockerSandboxFactory(DockerFactoryConfig{Runner: runner, ColimaVersion: func(context.Context) (string, error) { return "0.10.3", nil }, RecoveryScope: recoveryScope})
	if err != nil {
		t.Fatal(err)
	}
	policy := mustPolicy(t)
	identity := identitySet()
	identity.WorkspaceIdentity = workspaceIdentity(workspace)
	identity.VerifierImage = policy.Document().Image
	identity.VerifierPolicyVersion = policy.Document().Version
	identity.VerifierPolicyDigest = policy.Digest()
	sandbox, err := factory.Create(context.Background(), SandboxSpec{WorkspaceRoot: workspace, Policy: policy, Identity: identity, ArtifactLimit: 100 << 20})
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	create := runner.commandWith("create")
	imageInspect := []string{"image", "inspect", "--format", "{{.Id}}", policy.Document().Image}
	inspectIndex, createIndex := commandIndex(runner.commands, imageInspect), commandIndex(runner.commands, []string{"create"})
	if inspectIndex < 0 || createIndex < 0 || inspectIndex >= createIndex {
		t.Fatalf("exact verifier image was not inspected before create: %#v", runner.commands)
	}
	if !slices.Equal(runner.commands[inspectIndex], imageInspect) {
		t.Fatalf("image inspection was not exact: %#v", runner.commands)
	}
	if len(create) < 2 || !slices.Equal(create[:2], []string{"create", "--pull=never"}) {
		t.Fatalf("create may resolve a missing image remotely: %#v", create)
	}
	for _, pair := range [][]string{
		{"--network", "none"}, {"--read-only"}, {"--cap-drop", "ALL"}, {"--security-opt", "no-new-privileges:true"},
		{"--cpus", "2"}, {"--memory", "4096m"}, {"--memory-swap", "4096m"}, {"--pids-limit", "256"},
		{"--ulimit", "nofile=1024:1024"}, {"--user", "1000:1000"}, {"--workdir", "/workspace"},
		{"--mount", "type=bind,source=" + workspace + ",target=/workspace"}, {"--entrypoint", "/usr/bin/tail"}, {policy.Document().Image, "-f", "/dev/null"},
	} {
		if !containsSequence(create, pair) {
			t.Fatalf("create missing %q: %#v", pair, create)
		}
	}
	for _, label := range []string{
		"chora.run_id=" + identity.RunID,
		"chora.verification_run_id=" + identity.VerificationRunID,
		"chora.verification_attempt_id=" + identity.AttemptID,
		"chora.task_id=" + identity.TaskID,
		"chora.image_digest=" + identity.VerifierImage,
		"chora.verifier_policy_digest=" + fmt.Sprintf("%x", identity.VerifierPolicyDigest),
		"chora.baseline_digest=" + fmt.Sprintf("%x", identity.BaselineDigest),
		"chora.patch_digest=" + fmt.Sprintf("%x", identity.PatchDigest),
		"chora.acceptance_contract_digest=" + fmt.Sprintf("%x", identity.AcceptanceContractDigest),
		"chora.workspace_id=" + identity.WorkspaceIdentity,
		"chora.verifier_runtime_id=" + recoveryScope,
	} {
		if !containsSequence(create, []string{"--label", label}) {
			t.Fatalf("create missing identity label %q: %#v", label, create)
		}
	}
	for _, environment := range policy.Document().Environment {
		if !containsSequence(create, []string{"--env", environment}) {
			t.Fatalf("create missing fixed environment %q: %#v", environment, create)
		}
	}
	if countArg(create, "--mount") != 1 || countArg(create, "--env") != len(policy.Document().Environment) || strings.Contains(strings.Join(create, " "), "credential") || strings.Contains(strings.Join(create, " "), "context_snapshot") {
		t.Fatalf("create leaked mount/environment/context authority: %#v", create)
	}
	if strings.Contains(strings.Join(create, " "), "seccomp=") {
		t.Fatalf("Docker default seccomp was overridden: %#v", create)
	}
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	outcome := sandbox.Execute(context.Background(), CommandRequest{ID: "domain-tests", Argv: []string{"go", "test", "./internal/domain"}}, stdout, stderr)
	if outcome.Kind != CommandExited || outcome.ExitCode == nil || *outcome.ExitCode != 7 {
		t.Fatalf("outcome = %#v", outcome)
	}
	execArgs := runner.commandWith("exec")
	wantTail := []string{runner.containerID, "go", "test", "./internal/domain"}
	if len(execArgs) < len(wantTail) || !slices.Equal(execArgs[len(execArgs)-len(wantTail):], wantTail) || slices.Contains(execArgs, "sh") || slices.Contains(execArgs, "-c") {
		t.Fatalf("acceptance argv reconstructed: %#v", execArgs)
	}
	if err := sandbox.Terminate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !containsSequence(runner.commandWith("rm"), []string{"rm", "--force", runner.containerID}) || strings.TrimSpace(runner.lastListOutput) != "" {
		t.Fatalf("removal not proven: %#v", runner.commands)
	}
	assertNoDockerImageMutationCommands(t, runner.commands)
}

func TestDockerSandboxMissingPinnedImageFailsClosedBeforeCreate(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	runner := &fakeDockerRunner{imageMissing: true, workspace: workspace}
	factory, err := NewDockerSandboxFactory(DockerFactoryConfig{Runner: runner, ColimaVersion: func(context.Context) (string, error) { return "0.10.3", nil }})
	if err != nil {
		t.Fatal(err)
	}
	policy := mustPolicy(t)
	identity := identitySet()
	identity.WorkspaceIdentity = workspaceIdentity(workspace)
	identity.VerifierImage = policy.Document().Image
	identity.VerifierPolicyVersion = policy.Document().Version
	identity.VerifierPolicyDigest = policy.Digest()
	_, err = factory.Create(context.Background(), SandboxSpec{WorkspaceRoot: workspace, Policy: policy, Identity: identity, ArtifactLimit: 100 << 20})
	if !errors.Is(err, ErrDockerBoundary) {
		t.Fatalf("missing verifier image error = %v", err)
	}
	imageInspect := []string{"image", "inspect", "--format", "{{.Id}}", policy.Document().Image}
	if len(runner.commands) == 0 || !slices.Equal(runner.commands[len(runner.commands)-1], imageInspect) || runner.commandWith("create") != nil {
		t.Fatalf("missing image did not fail closed before create: %#v", runner.commands)
	}
	assertNoDockerImageMutationCommands(t, runner.commands)
}

func TestDockerSandboxClassifiesContainerDeathAsLaunchFailure(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	runner := &fakeDockerRunner{
		image: validPolicyDocument().Image, containerID: strings.Repeat("c", 64), workspace: workspace,
		execContainerMissing: true,
	}
	factory, err := NewDockerSandboxFactory(DockerFactoryConfig{Runner: runner, ColimaVersion: func(context.Context) (string, error) { return "0.10.3", nil }})
	if err != nil {
		t.Fatal(err)
	}
	policy := mustPolicy(t)
	identity := identitySet()
	identity.WorkspaceIdentity = workspaceIdentity(workspace)
	identity.VerifierImage = policy.Document().Image
	identity.VerifierPolicyVersion = policy.Document().Version
	identity.VerifierPolicyDigest = policy.Digest()
	sandbox, err := factory.Create(context.Background(), SandboxSpec{WorkspaceRoot: workspace, Policy: policy, Identity: identity, ArtifactLimit: 100 << 20})
	if err != nil {
		t.Fatal(err)
	}
	outcome := sandbox.Execute(context.Background(), CommandRequest{ID: "domain-tests", Argv: []string{"go", "test", "./internal/domain"}}, &bytes.Buffer{}, &bytes.Buffer{})
	if outcome.Kind != CommandLaunchFailed || !strings.Contains(outcome.Reason, "container process died") {
		t.Fatalf("container death outcome = %#v", outcome)
	}
}

func TestVerifierStartupRecoveryIsDigestScopedAndProvesContainerAndWorkspaceRemoval(t *testing.T) {
	scope := "sha256:" + strings.Repeat("d", 64)
	containerID := strings.Repeat("e", 64)
	runner := &recoveryDockerRunner{containerIDs: []string{containerID}}
	dockerFactory, err := NewDockerSandboxFactory(DockerFactoryConfig{Runner: runner, RecoveryScope: scope})
	if err != nil {
		t.Fatal(err)
	}
	if err := dockerFactory.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runner.containerIDs) != 0 || !containsSequence(runner.commands[0], []string{"--filter", "label=chora.verifier_runtime_id=" + scope}) ||
		!containsSequence(runner.commands[1], []string{"rm", "--force", containerID}) {
		t.Fatalf("unscoped or incomplete Docker recovery: %#v", runner.commands)
	}

	workspaceRoot := filepath.Join(t.TempDir(), "verification", "workspaces")
	workspaceFactory, err := NewFilesystemWorkspaceFactory(workspaceRoot, "git")
	if err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(workspaceRoot, "verification-stale000")
	if err := os.Mkdir(stale, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := workspaceFactory.Recover(context.Background()); !errors.Is(err, ErrInvalidWorkspace) {
		t.Fatalf("prefix-only workspace recovery error = %v", err)
	}
	if err := workspaceFactory.writeWorkspaceMarker(stale, "verification-attempt-stale", sha256.Sum256([]byte("policy"))); err != nil {
		t.Fatal(err)
	}
	if err := workspaceFactory.Recover(context.Background()); err != nil {
		t.Fatalf("authenticated workspace recovery: %v", err)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale Verification Workspace survived recovery: %v", err)
	}
}

func TestInstalledDockerFactoryRejectsAmbientRunnerAndBlocksRecoveryOnAuthorityFailure(t *testing.T) {
	imageID := validPolicyDocument().Image
	authority := &fakeInstalledDockerAuthority{imageID: imageID}
	if _, err := NewDockerSandboxFactory(DockerFactoryConfig{InstalledAuthority: authority}); !errors.Is(err, ErrDockerBoundary) {
		t.Fatalf("ambient installed Runner error = %v", err)
	}

	runner := &recoveryDockerRunner{containerIDs: []string{strings.Repeat("e", 64)}}
	authority.err = errors.New("qualified Engine identity drift")
	factory, err := NewDockerSandboxFactory(DockerFactoryConfig{
		Runner: runner, InstalledAuthority: authority, RecoveryScope: "sha256:" + strings.Repeat("d", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := factory.Recover(context.Background()); !errors.Is(err, ErrDockerBoundary) {
		t.Fatalf("installed authority recovery error = %v", err)
	}
	if authority.calls != 1 || len(runner.commands) != 0 || len(runner.containerIDs) != 1 {
		t.Fatalf("failed authority touched recovery scope: calls=%d commands=%#v containers=%#v", authority.calls, runner.commands, runner.containerIDs)
	}
}

type fixedRedactor struct {
	version string
	secret  []byte
	mask    []byte
}

func (redactor fixedRedactor) Version() string { return redactor.version }
func (redactor fixedRedactor) Redact(body []byte) ([]byte, error) {
	if len(redactor.secret) == 0 {
		return append([]byte(nil), body...), nil
	}
	return bytes.ReplaceAll(body, redactor.secret, redactor.mask), nil
}

type fakeSandbox struct {
	outcome        CommandOutcome
	terminateErr   error
	terminateCalls int
	executeCalls   int
	cleanupCalls   int
}

type fakeSandboxFactory struct {
	sandbox Sandbox
	spec    SandboxSpec
}

func (factory *fakeSandboxFactory) Create(_ context.Context, spec SandboxSpec) (Sandbox, error) {
	factory.spec = spec
	return factory.sandbox, nil
}

type fakeWorkspaceFactory struct {
	lease   WorkspaceLease
	request WorkspaceRequest
}

func (factory *fakeWorkspaceFactory) Prepare(_ context.Context, request WorkspaceRequest) (WorkspaceLease, error) {
	factory.request = request
	return factory.lease, nil
}

type fakeWorkspaceLease struct {
	root         string
	baseline     [32]byte
	patch        [32]byte
	cleanupCalls int
}

func (lease *fakeWorkspaceLease) Root() string             { return lease.root }
func (lease *fakeWorkspaceLease) BaselineDigest() [32]byte { return lease.baseline }
func (lease *fakeWorkspaceLease) PatchDigest() [32]byte    { return lease.patch }
func (lease *fakeWorkspaceLease) TouchedFiles() []string {
	return []string{"internal/domain/task.go", "internal/domain/task_test.go"}
}
func (lease *fakeWorkspaceLease) Cleanup(context.Context) error {
	lease.cleanupCalls++
	return nil
}

type fakeDockerRunner struct {
	image                string
	imageMissing         bool
	containerID          string
	workspace            string
	commands             [][]string
	lastListOutput       string
	execContainerMissing bool
}

type recoveryDockerRunner struct {
	containerIDs []string
	commands     [][]string
}

type fakeInstalledDockerAuthority struct {
	imageID string
	err     error
	calls   int
}

func (authority *fakeInstalledDockerAuthority) Verify(context.Context) error {
	authority.calls++
	return authority.err
}

func (authority *fakeInstalledDockerAuthority) VerifierImageID() string { return authority.imageID }

func (runner *recoveryDockerRunner) Run(_ context.Context, command DockerCommand) (DockerCommandResult, error) {
	runner.commands = append(runner.commands, slices.Clone(command.Args))
	if containsSequence(command.Args, []string{"container", "ls"}) {
		body := strings.Join(runner.containerIDs, "\n")
		if body != "" {
			body += "\n"
		}
		return DockerCommandResult{Stdout: []byte(body)}, nil
	}
	if len(command.Args) == 3 && command.Args[0] == "rm" && command.Args[1] == "--force" {
		for index, id := range runner.containerIDs {
			if id == command.Args[2] {
				runner.containerIDs = append(runner.containerIDs[:index], runner.containerIDs[index+1:]...)
				return DockerCommandResult{}, nil
			}
		}
		return DockerCommandResult{ExitCode: 1}, nil
	}
	return DockerCommandResult{ExitCode: 1}, nil
}

func (runner *fakeDockerRunner) Run(_ context.Context, command DockerCommand) (DockerCommandResult, error) {
	runner.commands = append(runner.commands, slices.Clone(command.Args))
	if len(command.Args) > 0 && command.Args[0] == "version" {
		return DockerCommandResult{ExitCode: 0, Stdout: []byte("29.6.1|29.6.1\n")}, nil
	}
	if containsSequence(command.Args, []string{"context", "show"}) {
		return DockerCommandResult{ExitCode: 0, Stdout: []byte("colima\n")}, nil
	}
	if containsSequence(command.Args, []string{"image", "inspect"}) {
		if runner.imageMissing {
			return DockerCommandResult{ExitCode: 1, Stderr: []byte("No such image")}, nil
		}
		return DockerCommandResult{ExitCode: 0, Stdout: []byte(runner.image + "\n")}, nil
	}
	if len(command.Args) > 0 && command.Args[0] == "create" {
		return DockerCommandResult{ExitCode: 0, Stdout: []byte(runner.containerID + "\n")}, nil
	}
	if containsSequence(command.Args, []string{"container", "inspect", "--format", "{{.State.Running}}"}) {
		if runner.execContainerMissing {
			return DockerCommandResult{ExitCode: 1}, nil
		}
		return DockerCommandResult{ExitCode: 0, Stdout: []byte("true\n")}, nil
	}
	if containsSequence(command.Args, []string{"container", "inspect"}) {
		identity := runner.containerID + "|" + runner.image + "|none|1000:1000|true|2000000000|4294967296|4294967296|256|false|none|[\"ALL\"]|[\"no-new-privileges:true\"]|[]|[{\"Name\":\"nofile\",\"Hard\":1024,\"Soft\":1024}]|" + runner.workspace + ">/workspace>true|rw,nosuid,nodev,noexec,size=64m\n"
		return DockerCommandResult{ExitCode: 0, Stdout: []byte(identity)}, nil
	}
	if len(command.Args) > 0 && command.Args[0] == "exec" {
		_, _ = command.Stdout.Write([]byte("test output"))
		return DockerCommandResult{ExitCode: 7}, nil
	}
	if containsSequence(command.Args, []string{"container", "ls"}) {
		runner.lastListOutput = ""
		return DockerCommandResult{ExitCode: 0}, nil
	}
	return DockerCommandResult{ExitCode: 0}, nil
}

func (runner *fakeDockerRunner) commandWith(first string) []string {
	for index := len(runner.commands) - 1; index >= 0; index-- {
		if len(runner.commands[index]) > 0 && (runner.commands[index][0] == first || slices.Contains(runner.commands[index], first)) {
			return runner.commands[index]
		}
	}
	return nil
}

func containsSequence(haystack, needle []string) bool {
	for index := 0; index+len(needle) <= len(haystack); index++ {
		if slices.Equal(haystack[index:index+len(needle)], needle) {
			return true
		}
	}
	return false
}

func commandIndex(commands [][]string, prefix []string) int {
	for index, command := range commands {
		if len(command) >= len(prefix) && slices.Equal(command[:len(prefix)], prefix) {
			return index
		}
	}
	return -1
}

func assertNoDockerImageMutationCommands(t *testing.T, commands [][]string) {
	t.Helper()
	for _, command := range commands {
		if len(command) == 0 {
			continue
		}
		if slices.Contains([]string{"build", "buildx", "pull", "load"}, command[0]) ||
			(command[0] == "image" && len(command) > 1 && slices.Contains([]string{"build", "pull", "load"}, command[1])) {
			t.Fatalf("verifier lifecycle mutated image availability: %#v", command)
		}
	}
}

func countArg(args []string, target string) int {
	count := 0
	for _, arg := range args {
		if arg == target {
			count++
		}
	}
	return count
}

func (sandbox *fakeSandbox) Identity() string { return "sandbox@sha256:test" }
func (sandbox *fakeSandbox) Execute(_ context.Context, request CommandRequest, stdout, stderr io.Writer) CommandOutcome {
	sandbox.executeCalls++
	_, _ = stdout.Write([]byte("stdout"))
	_, _ = stderr.Write([]byte("stderr"))
	request.Argv[0] = "mutated-by-backend"
	return sandbox.outcome
}
func (sandbox *fakeSandbox) Terminate(context.Context) error {
	sandbox.terminateCalls++
	return sandbox.terminateErr
}
func (sandbox *fakeSandbox) Cleanup(context.Context) error {
	sandbox.cleanupCalls++
	return nil
}

func validPolicyDocument() PolicyDocument {
	return PolicyDocument{
		SchemaVersion: PolicyProjectionSchemaVersion, Version: "m1-v1", Mode: PolicyModeFull,
		DockerVersion: "29.6.1", DockerContext: "colima", ColimaVersion: "0.10.3", Image: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		NetworkDisabled: true, CredentialsDisabled: true, NonRootUID: 1000, NonRootGID: 1000, ReadOnlyRoot: true,
		DropCapabilities: [1]string{"ALL"}, NoNewPrivileges: true, SeccompProfile: "docker_default",
		Environment: [6]string{"GOCACHE=/workspace/.chora-cache/go-build", "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", "GOTMPDIR=/workspace/.chora-tmp", "HOME=/tmp"},
		CPUs:        2, MemoryMiB: 4096, MemorySwapMiB: 4096, PIDs: 256, FileDescriptors: 1024,
		CommandTimeoutSeconds: 600, AttemptTimeoutSeconds: 1200, PersistedLogBytes: 10 << 20, ArtifactBytes: 100 << 20, DeathConfirmationSeconds: 5,
	}
}

func mustPolicy(t *testing.T) Policy {
	t.Helper()
	policy, err := newPolicyProjection(validPolicyDocument(), sha256.Sum256([]byte("formally-verified-policy")))
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func identitySet() EvidenceIdentity {
	digest := sha256.Sum256([]byte("identity"))
	return EvidenceIdentity{RunID: "run-1", VerificationRunID: "verification-run-1", AttemptID: "verification-attempt-1", TaskID: "task-1",
		WorkspaceIdentity: "sha256:" + strings.Repeat("b", 64), VerifierImage: validPolicyDocument().Image,
		VerifierPolicyVersion: "m1-v1", BaselineDigest: digest, PatchDigest: digest, ContextSnapshotDigest: digest, AcceptanceContractDigest: digest, VerifierPolicyDigest: digest}
}

func intPtr(value int) *int { return &value }

func mustWrite(t *testing.T, name, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
