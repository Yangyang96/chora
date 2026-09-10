package dockersupervisor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/acceptanceauthority"
	agentpi "github.com/Yangyang96/chora/internal/agent/pi"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
)

const (
	testPolicyDigest    = pinnedPolicyDigest
	testAttemptImageID  = "sha256:8e946e7dd0a6a92e9b916841f93361b4373844313d3c873e5c515ffd99ad14a6"
	testBoundaryImageID = "sha256:9e386d7456b64f981ed628446c24a8c98f71601360516616d87f95db16fbcbd9"
)

func TestPiAdapterInvocationIsAcceptedWithoutContractTranslation(t *testing.T) {
	runtimeConfig, err := os.ReadFile(filepath.Join("..", "..", "contracts", "g2-m1a", "pi-runtime-config.v6.json"))
	if err != nil {
		t.Fatal(err)
	}
	policy, err := os.ReadFile(filepath.Join("..", "..", "contracts", "g2-m1a", "sandbox-policy.v4.json"))
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := agentpi.New(agentpi.Config{
		RuntimeConfig: runtimeConfig, Policy: policy,
		AttemptImageID: agentpi.AttemptImageID, BoundaryImageID: agentpi.BoundaryImageID,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range []domain.AgentExecutionProfile{
		domain.AgentExecutionProfileMinimal,
		domain.AgentExecutionProfileStandard,
	} {
		t.Run(string(profile), func(t *testing.T) {
			now := time.Now().UTC()
			run, err := domain.NewAgentRun(domain.NewRunID(), domain.NewTaskID(), domain.NewCharterID(), now)
			if err != nil {
				t.Fatal(err)
			}
			snapshot := []byte(fmt.Sprintf(`{"task":{"id":%q}}`, run.TaskID().String()))
			contextDigest := sha256.Sum256(snapshot)
			binding, err := domain.NewAgentExecutionProfileBinding(profile)
			if err != nil {
				t.Fatal(err)
			}
			attempt, err := domain.NewAttempt(domain.AttemptParams{
				ID: domain.NewAttemptID(), RunID: run.ID(), Sequence: 1,
				ContextSnapshotID: domain.NewContextSnapshotID(), ContextDigest: contextDigest,
				AdapterID: agentpi.AdapterID, AgentExecutionProfileBinding: binding, CreatedAt: now,
			})
			if err != nil {
				t.Fatal(err)
			}
			launch := execution.LaunchToken{Value: "launch-pi-contract-" + string(profile)}
			contract := []byte(fmt.Sprintf(`{"schema_version":"chora.spec-coding-core.v10","task":{"id":%q},"acceptance":{"criteria":[{"id":"criterion-1"}]},"execution":{"boundary":{"writable_files":["source.txt"],"commands":[{"id":"test","argv":["go","test","./..."]}],"test_command_ids":["test"]}}}`, run.TaskID().String()))
			invocation, err := adapter.PrepareStart(context.Background(), execution.StartRequest{
				Run: run, Attempt: attempt, SnapshotDocument: snapshot, ExecutionContractDocument: contract,
				WorkspaceRoot: testSource(t), LaunchToken: launch,
			})
			if err != nil {
				t.Fatal(err)
			}
			runner := &fakeRunner{processes: []*fakeProcess{newFakeProcess()}}
			supervisor, err := New(Config{
				Runner: runner, RuntimeRoot: filepath.Join(t.TempDir(), "runtime"),
				PolicyDigest: agentpi.PolicySHA256, AttemptImageID: agentpi.AttemptImageID,
				BoundaryImageID:       agentpi.BoundaryImageID,
				RuntimeSourceIdentity: managedRuntimeSourceIdentity(agentpi.AttemptImageID),
				CapabilityContract:    testCapabilityContractFor(t, agentpi.AttemptImageID, agentpi.PolicySHA256),
				EngineQualification:   testEngineQualificationFor(t, agentpi.AttemptImageID, agentpi.PolicySHA256),
				CredentialSource:      testCredentialSource(t), TrustAnchorSource: testTrustAnchorSource(t),
			})
			if err != nil {
				t.Fatal(err)
			}
			supervisor.providerIdentity = ProviderIdentity{
				DockerClientVersion: DockerEngineVersion, DockerServerVersion: DockerEngineVersion,
				DockerContext: DockerContext, ColimaVersion: RequiredColimaVersion,
			}
			outcome := supervisor.Start(context.Background(), invocation, &testSink{binding: launch})
			if outcome.Kind != execution.Started {
				t.Fatalf("Pi %s invocation rejected by Docker supervisor: %#v", profile, outcome)
			}
			assertNoAttemptImageAcquisition(t, runner.commands(), 2)
		})
	}
}

func TestStartEnforcesExactProfileToolAllowlistAtPiProcessBoundary(t *testing.T) {
	for _, test := range []struct {
		profile domain.AgentExecutionProfile
		tools   []string
	}{
		{profile: domain.AgentExecutionProfileMinimal, tools: []string{"read", "bash", "edit", "write"}},
		{profile: domain.AgentExecutionProfileStandard, tools: []string{"read", "bash", "edit", "write", "grep", "find", "ls"}},
	} {
		t.Run(string(test.profile), func(t *testing.T) {
			runner := &fakeRunner{processes: []*fakeProcess{newFakeProcess()}}
			supervisor := newTestSupervisor(t, runner)
			invocation := testInvocationForProfile(t, testSource(t), "launch-tools-"+string(test.profile), test.profile)
			outcome := supervisor.Start(context.Background(), invocation, &testSink{binding: invocation.LaunchToken()})
			if outcome.Kind != execution.Started {
				t.Fatalf("Start() = %#v", outcome)
			}
			commands := runner.commands()
			if len(commands) < 5 {
				t.Fatalf("commands = %#v", commands)
			}
			attempt := commands[4].Args
			wantAllowlist := strings.Join(test.tools, ",")
			assertAdjacentArgs(t, attempt, "--tools", wantAllowlist)
			if test.profile == domain.AgentExecutionProfileMinimal {
				for _, prohibited := range []string{"grep", "find", "ls"} {
					if slices.Contains(strings.Split(wantAllowlist, ","), prohibited) || slices.Contains(attempt, "--exclude-tools") {
						t.Fatalf("Minimal ambient/prohibited tool exposure %q: %v", prohibited, attempt)
					}
				}
			}
		})
	}
}

func TestStartRejectsMinimalCapabilityEscalationBeforeAnySideEffect(t *testing.T) {
	standard, err := agentpi.ManagedCapabilityBundleForProfile(domain.AgentExecutionProfileStandard)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, execution.Invocation) execution.Invocation
	}{
		{name: "bundle identity", mutate: func(t *testing.T, invocation execution.Invocation) execution.Invocation {
			environment := invocation.Environment()
			environment[EnvCapabilityBundleID] = standard.ID()
			environment[EnvCapabilityBundleSHA] = standard.SHA256()
			return remakeInvocation(t, invocation, environment)
		}},
		{name: "Pi argv prohibited tool", mutate: func(t *testing.T, invocation execution.Invocation) execution.Invocation {
			arguments := append(invocation.Arguments(), "--tools", "read,bash,edit,write,grep")
			return remakeInvocationArguments(t, invocation, arguments)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeRunner{processes: []*fakeProcess{newFakeProcess()}}
			supervisor := newTestSupervisor(t, runner)
			invocation := testInvocationForProfile(t, testSource(t), "launch-minimal-escalation", domain.AgentExecutionProfileMinimal)
			invocation = test.mutate(t, invocation)

			outcome := supervisor.Start(context.Background(), invocation, &testSink{binding: invocation.LaunchToken()})
			if outcome.Kind != execution.StartProvenNoChild {
				t.Fatalf("outcome = %#v", outcome)
			}
			if commands := runner.allCommands(); len(commands) != 0 {
				t.Fatalf("capability escalation reached Docker/model execution: %#v", commands)
			}
			entries, err := os.ReadDir(supervisor.config.RuntimeRoot)
			if err != nil || len(entries) != 0 {
				t.Fatalf("capability escalation changed runtime root: entries=%v err=%v", entries, err)
			}
		})
	}
}

func TestStartRejectsInvalidInvocationBeforeSideEffects(t *testing.T) {
	tests := map[string]func(map[string]string){
		"policy digest": func(environment map[string]string) {
			environment[EnvPolicyDigest] = strings.Repeat("0", 64)
		},
		"minimal profile with Standard policy": func(environment map[string]string) {
			environment[EnvExecutionProfile] = string(domain.AgentExecutionProfileMinimal)
		},
		"Standard profile with Minimal policy": func(environment map[string]string) {
			environment[EnvCapabilityPolicy] = domain.MinimalCapabilityPolicy
		},
		"Trusted Local": func(environment map[string]string) {
			environment[EnvExecutionProfile] = string(domain.AgentExecutionProfileTrustedLocal)
			environment[EnvRuntimeSource] = domain.LocalPiRuntimeSource
			environment[EnvExecutionProvider] = domain.TrustedHostExecutionProvider
			environment[EnvCapabilityPolicy] = domain.NativeCapabilityPolicy
			environment[EnvTrustDisclosure] = domain.TrustedLocalDisclosurePolicy
		},
		"unknown profile": func(environment map[string]string) {
			environment[EnvExecutionProfile] = "unknown"
		},
		"nonempty managed disclosure": func(environment map[string]string) {
			environment[EnvTrustDisclosure] = domain.TrustedLocalDisclosurePolicy
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			runner := &fakeRunner{}
			supervisor := newTestSupervisor(t, runner)
			invocation := testInvocation(t, testSource(t), "launch-invalid")
			environment := invocation.Environment()
			mutate(environment)
			invocation = remakeInvocation(t, invocation, environment)

			outcome := supervisor.Start(context.Background(), invocation, &testSink{binding: invocation.LaunchToken()})
			if outcome.Kind != execution.StartProvenNoChild || len(runner.commands()) != 0 {
				t.Fatalf("outcome = %#v, commands = %#v", outcome, runner.commands())
			}
			entries, err := os.ReadDir(supervisor.config.RuntimeRoot)
			if err != nil || len(entries) != 0 {
				t.Fatalf("runtime root changed before validation: entries=%v err=%v", entries, err)
			}
		})
	}
}

func TestValidatePromptRejectsPrivateHomePathBeforeSideEffects(t *testing.T) {
	baseSnapshot := `{"task":{"id":"task-1"}}`
	baseContract := `{"schema_version":"chora.spec-coding-core.v8","task":{"id":"task-1"},"acceptance":{"criteria":[{"id":"criterion-1"}]},"execution":{"boundary":{"writable_files":["source.txt"],"commands":[{"id":"test","argv":["go","test","./..."]}],"test_command_ids":["test"]}}}`
	for name, documents := range map[string][2]string{
		"snapshot": {`{"task":{"id":"task-1"},"note":"/Users/alice/private.txt"}`, baseContract},
		"contract": {baseSnapshot, strings.Replace(baseContract, `"task-1"`, `"task-1","note":"/home/bob/private.txt"`, 1)},
	} {
		t.Run(name, func(t *testing.T) {
			snapshot, contract := []byte(documents[0]), []byte(documents[1])
			message, err := execution.WrapFrozenExecutionPrompt(snapshot, contract)
			if err != nil {
				t.Fatal(err)
			}
			prompt, err := json.Marshal(map[string]string{"id": "prompt-private-path", "type": "prompt", "message": message})
			if err != nil {
				t.Fatal(err)
			}
			prompt = append(prompt, '\n')
			snapshotDigest := sha256.Sum256(snapshot)
			contractDigest := sha256.Sum256(contract)
			if _, _, err := validatePrompt(prompt, hex.EncodeToString(snapshotDigest[:]), hex.EncodeToString(contractDigest[:])); err == nil || !strings.Contains(err.Error(), "private home path") {
				t.Fatalf("validatePrompt() error = %v", err)
			}
		})
	}
}

func TestStartFailsClosedWithoutEngineQualification(t *testing.T) {
	runner := &fakeRunner{processes: []*fakeProcess{newFakeProcess()}}
	supervisor := newTestSupervisor(t, runner)
	supervisor.config.EngineQualification = EngineQualification{}
	invocation := testInvocation(t, testSource(t), "launch-unverified")
	outcome := supervisor.Start(context.Background(), invocation, &testSink{binding: invocation.LaunchToken()})
	if outcome.Kind != execution.StartProvenNoChild || !strings.Contains(outcome.Diagnostic, "not capability-qualified") || len(runner.allCommands()) != 0 {
		t.Fatalf("unverified start = %#v commands=%#v", outcome, runner.commands())
	}
}

func TestStartFailsClosedWithoutTaskCredentialAfterProviderVerification(t *testing.T) {
	runner := &fakeRunner{}
	supervisor, err := New(Config{
		Runner: runner, RuntimeRoot: filepath.Join(t.TempDir(), "runtime"),
		PolicyDigest: testPolicyDigest, AttemptImageID: testAttemptImageID,
		BoundaryImageID: testBoundaryImageID, CapabilityContract: testCapabilityContract(t), EngineQualification: testEngineQualification(t),
		RuntimeSourceIdentity: managedRuntimeSourceIdentity(testAttemptImageID),
		TrustAnchorSource:     testTrustAnchorSource(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	supervisor.providerIdentity = ProviderIdentity{
		DockerClientVersion: DockerEngineVersion, DockerServerVersion: DockerEngineVersion,
		DockerContext: DockerContext, ColimaVersion: RequiredColimaVersion,
	}
	invocation := testInvocation(t, testSource(t), "launch-missing-credential")
	outcome := supervisor.Start(context.Background(), invocation, &testSink{binding: invocation.LaunchToken()})
	if outcome.Kind != execution.StartProvenNoChild || !strings.Contains(outcome.Diagnostic, "Pi OAuth source") || len(runner.commands()) != 0 {
		t.Fatalf("missing credential start = %#v commands=%#v", outcome, runner.commands())
	}
}

func TestVerifyReobservesQualifiedEngineAndImagesWithoutMutation(t *testing.T) {
	runner := &fakeRunner{}
	supervisor := newTestSupervisor(t, runner)
	identity, err := supervisor.Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if identity.DockerClientVersion != "" || identity.DockerServerVersion != "99.7.3" || identity.DockerContext != "test-context" || identity.ColimaVersion != "" {
		t.Fatalf("provider identity = %#v", identity)
	}
	if !identity.Valid() {
		t.Fatal("provider-neutral Verify projection is not self-consistently valid")
	}
	commands := runner.allCommands()
	if len(commands) != 6 {
		t.Fatalf("verify commands = %#v", commands)
	}
	for _, command := range commands {
		if !isExecutionQualificationCommand(command.Args) {
			t.Fatalf("Verify performed non-observation command: %v", command.Args)
		}
	}
}

func TestStartBuildsFreshHardenedAttempt(t *testing.T) {
	runner := &fakeRunner{processes: []*fakeProcess{newFakeProcess()}}
	supervisor := newTestSupervisor(t, runner)
	source := testSource(t)
	invocation := testInvocation(t, source, "launch-normal")
	sink := &testSink{binding: invocation.LaunchToken()}

	outcome := supervisor.Start(context.Background(), invocation, sink)
	if outcome.Kind != execution.Started || !outcome.Handle.Valid() || !outcome.Identity.Valid() {
		t.Fatalf("Start() = %#v", outcome)
	}
	commands := runner.commands()
	if len(commands) != 13 {
		t.Fatalf("commands = %d, want 13: %#v", len(commands), commands)
	}
	assertNoAttemptImageAcquisition(t, commands, 2)
	assertArgsContain(t, commands[0].Args, "network", "create", "--internal")
	assertArgsContain(t, commands[1].Args, "network", "create")
	assertArgsContain(t, commands[2].Args, "run", "-d", "--read-only", "--log-driver", "none", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--ulimit", "nofile=1024:1024")
	if strings.Contains(strings.Join(commands[2].Args, " "), "chora.fault_seam") {
		t.Fatalf("obsolete boundary fault seam enabled by default: %v", commands[2].Args)
	}
	assertArgsContain(t, commands[3].Args, "network", "connect", "--alias", "codex-boundary")
	runtimeScopeDigest := sha256.Sum256([]byte(filepath.Clean(supervisor.config.RuntimeRoot)))
	runtimeScopeLabel := "chora.runtime_scope=sha256:" + hex.EncodeToString(runtimeScopeDigest[:])
	for _, index := range []int{0, 1, 2, 4} {
		if !strings.Contains(strings.Join(commands[index].Args, " "), runtimeScopeLabel) {
			t.Fatalf("command %d missing installation-scoped recovery label %q: %v", index, runtimeScopeLabel, commands[index].Args)
		}
	}
	attempt := commands[4].Args
	assertArgsContain(t, attempt, "create", "--pull=never")
	assertArgsContain(t, commands[5].Args, "start", "-ai")
	for _, required := range []string{"--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--pids-limit", "256", "--cpus", "2", "--memory", "4096m", "--memory-swap", "4096m", "--ulimit", "nofile=1024:1024", "--network"} {
		if !slices.Contains(attempt, required) {
			t.Errorf("attempt argv missing %q: %v", required, attempt)
		}
	}
	joined := strings.Join(attempt, " ")
	if !strings.Contains(joined, "--log-driver none") || strings.Contains(joined, "--log-opt") {
		t.Fatalf("attempt retained daemon logs: %s", joined)
	}
	if !strings.Contains(joined, "--entrypoint /bin/sh") || !strings.Contains(joined, "NODE_EXTRA_CA_CERTS=/run/chora/trust/starpoint-root-ca-2048-g2.pem") || !strings.Contains(joined, "GOCACHE="+toolCachePath) || !strings.Contains(joined, "GOTMPDIR="+toolTempPath) || !strings.Contains(joined, "exec pi") || !strings.Contains(joined, "--mode rpc --no-session") || !strings.Contains(joined, "--provider openai-codex --model gpt-5.6-sol") {
		t.Fatalf("attempt does not bootstrap exact Pi RPC transport: %s", joined)
	}
	if strings.Contains(joined, source) || strings.Contains(joined, "test-access-token") || strings.Contains(joined, "--network host") || strings.Contains(joined, "-p ") || strings.Contains(joined, "docker.sock") {
		t.Fatalf("unsafe attempt argv: %s", joined)
	}
	assertArgsContain(t, commands[7].Args, "container", "inspect", "{{json .}}")
	assertArgsContain(t, commands[8].Args, "container", "inspect", "{{json .}}")
	assertArgsContain(t, commands[9].Args, "exec", "--user", "0:0", "-i", "chmod 0555 /run/chora/trust")
	assertArgsContain(t, commands[11].Args, "exec", "--user", "1000:1000", "-i", "auth.json")
	if commands[9].Stdin == nil || commands[11].Stdin == nil {
		t.Fatal("trust and OAuth projections must use task-scoped stdin")
	}
	authProjection, err := io.ReadAll(commands[11].Stdin)
	if err != nil || !strings.Contains(string(authProjection), `"openai-codex"`) || strings.Contains(string(authProjection), "unrelated") {
		t.Fatalf("OAuth projection was not provider-only: %q err=%v", authProjection, err)
	}
	record := supervisor.record(outcome.Handle)
	workspaceInfo, err := os.Stat(filepath.Join(record.root, "workspace"))
	if err != nil || workspaceInfo.Mode().Perm() != 0o777 {
		t.Fatalf("container workspace permissions = %v err=%v", workspaceInfo.Mode().Perm(), err)
	}
	repositoryFileInfo, err := os.Stat(filepath.Join(record.repository, "source.txt"))
	if err != nil || repositoryFileInfo.Mode().Perm()&0o006 != 0o006 {
		t.Fatalf("container repository file permissions = %v err=%v", repositoryFileInfo.Mode().Perm(), err)
	}
	contextInfo, err := os.Stat(record.contextDir)
	if err != nil || contextInfo.Mode().Perm() != 0o755 {
		t.Fatalf("read-only context permissions = %v err=%v", contextInfo.Mode().Perm(), err)
	}
	for _, relative := range []string{".chora-cache/go-build", ".chora-tmp"} {
		info, err := os.Stat(filepath.Join(record.root, "workspace", filepath.FromSlash(relative)))
		if err != nil {
			t.Fatalf("tooling directory %s: %v", relative, err)
		}
		if info.Mode().Perm() != 0o777 {
			t.Fatalf("tooling directory %s permissions = %v", relative, info.Mode().Perm())
		}
	}
	for _, path := range []string{
		filepath.Join(record.root, "baseline", "repository", "source.txt"),
		filepath.Join(record.root, "workspace", "repository", "source.txt"),
		filepath.Join(record.root, "context", "snapshot.jsonl"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("attempt materialization %s: %v", path, err)
		}
	}
}

func TestStartRejectsEffectiveContainerIdentityDriftBeforeCredentialProjection(t *testing.T) {
	process := newFakeProcess()
	runner := &fakeRunner{processes: []*fakeProcess{process}, effectiveIdentityDrift: true}
	supervisor := newTestSupervisor(t, runner)
	invocation := testInvocation(t, testSource(t), "launch-effective-drift")
	outcome := supervisor.Start(context.Background(), invocation, &testSink{binding: invocation.LaunchToken()})
	if outcome.Kind != execution.StartProvenNoChild || !strings.Contains(outcome.Diagnostic, "effective Agent container identity") {
		t.Fatalf("effective identity drift outcome=%#v", outcome)
	}
	for _, command := range runner.commands() {
		if len(command.Args) > 0 && command.Args[0] == "exec" {
			t.Fatalf("credential or trust projection ran after effective identity drift: %v", command.Args)
		}
	}
	process.mu.Lock()
	closeCount, killCount, waitCount := process.closeCount, process.killCount, process.waitCount
	process.mu.Unlock()
	if closeCount != 1 || killCount != 1 || waitCount != 1 {
		t.Fatalf("failed setup CLI close/kill/wait = %d/%d/%d, want 1/1/1", closeCount, killCount, waitCount)
	}
}

func TestStartSetupFailureReapsCLIBeforeFinalCleanupCatchesLateResource(t *testing.T) {
	process := newFakeProcess()
	runner := &fakeRunner{processes: []*fakeProcess{process}, effectiveIdentityDrift: true}
	supervisor := newTestSupervisor(t, runner)
	supervisor.config.DeathWait = 100 * time.Millisecond
	invocation := testInvocation(t, testSource(t), "launch-late-resource-after-kill")
	lateResource := ""
	process.afterDoneBeforeReturn = func() {
		// Ensure the old cleanup-before-Wait order would finish its absence
		// check before this dying CLI publishes the late resource.
		time.Sleep(20 * time.Millisecond)
		runner.mu.Lock()
		defer runner.mu.Unlock()
		for _, command := range runner.calls {
			for index, argument := range command.Args {
				if argument == "--name" && index+1 < len(command.Args) && strings.HasSuffix(command.Args[index+1], "-attempt") {
					lateResource = command.Args[index+1]
				}
			}
		}
		if lateResource == "" {
			return
		}
		runner.cleanupContainerResidue = lateResource
		runner.ownershipName = "/" + lateResource
		runner.ownershipLabels = map[string]string{
			"chora.owner": "dockersupervisor", "chora.runtime_scope": supervisor.recoveryScope,
			"chora.attempt_id": invocation.Environment()[EnvAttemptID], "chora.policy_digest": testPolicyDigest,
		}
	}
	outcome := supervisor.Start(context.Background(), invocation, &testSink{binding: invocation.LaunchToken()})
	if outcome.Kind != execution.StartProvenNoChild {
		t.Fatalf("Start() = %#v", outcome)
	}
	runner.mu.Lock()
	created, retained := lateResource, runner.cleanupContainerResidue
	runner.mu.Unlock()
	if created == "" || retained != "" || !commandsContain(runner.commands(), "rm -f "+created) {
		t.Fatalf("post-reap cleanup missed late resource: created=%q retained=%q commands=%#v", created, retained, runner.commands())
	}
}

func TestStartSetupFailureReturnsOwnedReconciliationWhenCLIReapIsUnproven(t *testing.T) {
	process := newFakeProcess()
	process.killErr = errors.New("injected kill failure")
	runner := &fakeRunner{processes: []*fakeProcess{process}, effectiveIdentityDrift: true}
	supervisor := newTestSupervisor(t, runner)
	invocation := testInvocation(t, testSource(t), "launch-effective-drift-unreaped")
	outcome := supervisor.Start(context.Background(), invocation, &testSink{binding: invocation.LaunchToken()})
	if outcome.Kind != execution.StartReconciliationRequired || !outcome.Handle.Valid() || !outcome.Identity.Valid() ||
		!strings.Contains(outcome.Diagnostic, "Docker CLI reap unproven") {
		t.Fatalf("Start() = %#v", outcome)
	}
	reconciled, err := supervisor.Reconcile(context.Background(), outcome.Identity)
	if err != nil || reconciled.Kind != execution.ReconcileAlive || reconciled.Handle != outcome.Handle {
		t.Fatalf("Reconcile() = %#v, %v", reconciled, err)
	}
	process.exit(137)
	waitDone(t, supervisor.record(outcome.Handle).done)
	drainAll(t, supervisor, outcome.Handle)
	if err := supervisor.Finalize(context.Background(), outcome.Handle, execution.RetentionPolicy{}); err != nil {
		t.Fatal(err)
	}
}

func TestStartNeverClaimsNoChildWhenRunnerReturnsNoProcess(t *testing.T) {
	runner := &fakeRunner{returnNilProcess: true}
	supervisor := newTestSupervisor(t, runner)
	invocation := testInvocation(t, testSource(t), "launch-no-process-proof")
	outcome := supervisor.Start(context.Background(), invocation, &testSink{binding: invocation.LaunchToken()})
	if outcome.Kind != execution.StartReconciliationRequired || !outcome.Handle.Valid() || !outcome.Identity.Valid() ||
		!strings.Contains(outcome.Diagnostic, "non-execution is unproven") {
		t.Fatalf("Start() = %#v", outcome)
	}
	stopped, err := supervisor.Stop(context.Background(), outcome.Handle, execution.StopIntent{Kind: execution.StopForCancel})
	if err != nil || stopped.Kind != execution.StopUncertain {
		t.Fatalf("Stop() = %#v, %v", stopped, err)
	}
}

func TestCredentialValuesAreRedactedAcrossRawStreamChunks(t *testing.T) {
	process := newFakeProcess()
	runner := &fakeRunner{processes: []*fakeProcess{process}}
	supervisor := newTestSupervisor(t, runner)
	supervisor.config.DeathWait = 100 * time.Millisecond
	invocation := testInvocation(t, testSource(t), "launch-redaction")
	outcome := supervisor.Start(context.Background(), invocation, &testSink{binding: invocation.LaunchToken()})
	if outcome.Kind != execution.Started {
		t.Fatalf("Start() = %#v", outcome)
	}
	if _, err := process.stdout.Write([]byte(`{"type":"error","message":"test-access-`)); err != nil {
		t.Fatal(err)
	}
	if _, err := process.stdout.Write([]byte("token\"}\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := process.stderr.Write([]byte("diagnostic test-refresh-token\n")); err != nil {
		t.Fatal(err)
	}
	stdout, err := supervisor.Read(context.Background(), outcome.Handle, execution.StreamStdout, 0, 4096)
	if err != nil || bytes.Contains(stdout.Data, []byte("test-access-token")) || !bytes.Contains(stdout.Data, []byte("*****************")) {
		t.Fatalf("stdout credential redaction = %q err=%v", stdout.Data, err)
	}
	stderr, err := supervisor.Read(context.Background(), outcome.Handle, execution.StreamStderr, 0, 4096)
	if err != nil || bytes.Contains(stderr.Data, []byte("test-refresh-token")) || !bytes.Contains(stderr.Data, []byte("******************")) {
		t.Fatalf("stderr credential redaction = %q err=%v", stderr.Data, err)
	}
	process.exit(0)
	waitDone(t, supervisor.record(outcome.Handle).done)
	drainAll(t, supervisor, outcome.Handle)
	if err := supervisor.Finalize(context.Background(), outcome.Handle, execution.RetentionPolicy{}); err != nil {
		t.Fatalf("Finalize() = %v", err)
	}
}

func TestPromptWriteFailureForcesRemovalAndCompletesLifecycle(t *testing.T) {
	process := newFakeProcess()
	process.writeErr = errors.New("injected prompt write failure")
	runner := &fakeRunner{processes: []*fakeProcess{process}, inspectMissing: true, exitOnRemove: true}
	supervisor := newTestSupervisor(t, runner)
	invocation := testInvocation(t, testSource(t), "launch-prompt-write-failure")
	outcome := supervisor.Start(context.Background(), invocation, &testSink{binding: invocation.LaunchToken()})
	if outcome.Kind != execution.StartReconciliationRequired || !outcome.Handle.Valid() || !outcome.Identity.Valid() {
		t.Fatalf("Start() = %#v", outcome)
	}
	if !commandsContain(runner.commands(), "network disconnect -f") || !commandsContain(runner.commands(), "rm -f") {
		t.Fatalf("prompt failure did not force removal: %#v", runner.commands())
	}
	record := supervisor.record(outcome.Handle)
	waitDone(t, record.done)
	drainAll(t, supervisor, outcome.Handle)
	if err := supervisor.Finalize(context.Background(), outcome.Handle, execution.RetentionPolicy{}); err != nil {
		t.Fatalf("Finalize() = %v", err)
	}
	if _, err := os.Stat(record.root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("prompt failure retained workspace: %v", err)
	}
}

func TestAcceptanceFailureStartsOnlyAfterPromptAndRequiresConfirmedExactKill(t *testing.T) {
	process := newFakeProcess()
	runner := &fakeRunner{processes: []*fakeProcess{process}}
	supervisor := newTestSupervisor(t, runner)
	invocation := testAcceptanceInvocation(t, testSource(t), "attempt-failure")
	supervisor.config.AcceptanceAuthority = testAcceptanceController(t, filepath.Join(t.TempDir(), "consumptions"), nil)
	outcome := supervisor.Start(context.Background(), invocation, &testSink{binding: invocation.LaunchToken()})
	if outcome.Kind != execution.Started {
		t.Fatalf("Start() = %#v", outcome)
	}
	waitDone(t, supervisor.record(outcome.Handle).done)
	drained, err := supervisor.Drain(context.Background(), outcome.Handle, execution.StreamOffsets{}, persistedLogBytes*2)
	if err != nil || drained.TerminalFiles.TerminationCause != execution.TerminationExitNonzero || drained.TerminalFiles.ExitCode != 137 {
		t.Fatalf("confirmed injected terminal = %#v, %v", drained.TerminalFiles, err)
	}
	identity, err := os.ReadFile(drained.TerminalFiles.Paths["runtime_identity"])
	if err != nil || !bytes.Contains(identity, []byte("acceptance_authority_digest")) || !bytes.Contains(identity, []byte("acceptance_consumption_digest")) {
		t.Fatalf("injected runtime identity = %q, %v", identity, err)
	}
	if !commandsContain(runner.commands(), "kill --signal KILL "+strings.Repeat("a", 64)) {
		t.Fatalf("exact fixed KILL missing: %#v", runner.commands())
	}
}

func TestAcceptanceFailureNeverDerivesPatchOrChecksFromPartialWorkspace(t *testing.T) {
	process := newFakeProcess()
	runner := &fakeRunner{processes: []*fakeProcess{process}}
	supervisor := newTestSupervisor(t, runner)
	supervisor.config.DeathWait = 100 * time.Millisecond
	runner.beforeForcedKill = func() {
		entries, err := os.ReadDir(supervisor.config.RuntimeRoot)
		if err != nil || len(entries) != 1 {
			return
		}
		_ = os.WriteFile(filepath.Join(supervisor.config.RuntimeRoot, entries[0].Name(), "workspace", "repository", "source.txt"), []byte("partial change\n"), 0o644)
	}
	invocation := testAcceptanceInvocation(t, testSource(t), "attempt-failure")
	supervisor.config.AcceptanceAuthority = testAcceptanceController(t, filepath.Join(t.TempDir(), "consumptions"), nil)
	outcome := supervisor.Start(context.Background(), invocation, &testSink{binding: invocation.LaunchToken()})
	if outcome.Kind != execution.Started {
		t.Fatalf("Start() = %#v", outcome)
	}
	waitDone(t, supervisor.record(outcome.Handle).done)
	drained, err := supervisor.Drain(context.Background(), outcome.Handle, execution.StreamOffsets{}, persistedLogBytes*2)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"patch", "checks"} {
		if _, err := os.Lstat(drained.TerminalFiles.Paths[key]); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s authority exists after injected failure: %v", key, err)
		}
	}
	result, err := os.ReadFile(drained.TerminalFiles.Paths["result"])
	if err != nil || !bytes.Contains(result, []byte(`"review_ready":false`)) || !bytes.Contains(result, []byte(`"outputs":[]`)) {
		t.Fatalf("safe injected result = %q, %v", result, err)
	}
	if commandsContain(runner.commands(), "-check-") {
		t.Fatalf("declared checks executed after injected failure: %#v", runner.commands())
	}
}

func TestAcceptanceFailureDoesNotInjectWhenPromptStartFails(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*fakeProcess)
	}{
		{name: "error", mutate: func(process *fakeProcess) { process.writeErr = errors.New("injected prompt write failure") }},
		{name: "short write", mutate: func(process *fakeProcess) { process.partialWrite = 1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			process := newFakeProcess()
			test.mutate(process)
			runner := &fakeRunner{processes: []*fakeProcess{process}, inspectMissing: true, exitOnRemove: true}
			supervisor := newTestSupervisor(t, runner)
			invocation := testAcceptanceInvocation(t, testSource(t), "attempt-failure")
			supervisor.config.AcceptanceAuthority = testAcceptanceController(t, filepath.Join(t.TempDir(), "consumptions"), nil)
			outcome := supervisor.Start(context.Background(), invocation, &testSink{binding: invocation.LaunchToken()})
			if outcome.Kind != execution.StartReconciliationRequired {
				t.Fatalf("Start() = %#v", outcome)
			}
			if commandsContain(runner.commands(), "kill --signal KILL") {
				t.Fatalf("force action ran without an exact started prompt: %#v", runner.commands())
			}
		})
	}
}

func TestAcceptanceConsumptionSyncFailurePreventsDockerExecution(t *testing.T) {
	runner := &fakeRunner{}
	supervisor := newTestSupervisor(t, runner)
	invocation := testAcceptanceInvocation(t, testSource(t), "attempt-failure")
	supervisor.config.AcceptanceAuthority = testAcceptanceController(t, filepath.Join(t.TempDir(), "consumptions"), func(string) error {
		return errors.New("injected durable sync failure")
	})
	outcome := supervisor.Start(context.Background(), invocation, &testSink{binding: invocation.LaunchToken()})
	if outcome.Kind != execution.StartProvenNoChild || len(runner.commands()) != 0 {
		t.Fatalf("sync failure outcome=%#v commands=%#v", outcome, runner.commands())
	}
}

func TestAcceptanceFailureInjectionFailsClosedOnAmbiguity(t *testing.T) {
	tests := []struct {
		name   string
		runner *fakeRunner
		before func(*attemptRecord)
	}{
		{name: "kill nonzero", runner: &fakeRunner{runResults: []CommandResult{{ExitCode: 1}}}},
		{name: "kill error", runner: &fakeRunner{runErrorAt: 1, runError: errors.New("injected command error")}},
		{name: "self exit before injection", runner: &fakeRunner{}, before: func(record *attemptRecord) { record.exitObserved = true }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			supervisor := newTestSupervisor(t, test.runner)
			record := acceptanceInjectionRecord()
			if test.before != nil {
				test.before(record)
			}
			supervisor.forceManagedNonzero(record)
			if record.acceptanceInjectionState != acceptanceInjectionFailed || record.terminationCause != execution.TerminationNone {
				t.Fatalf("ambiguous injection state=%v cause=%q", record.acceptanceInjectionState, record.terminationCause)
			}
			if test.name == "self exit before injection" && len(test.runner.commands()) != 0 {
				t.Fatalf("self-exited process still received injection: %#v", test.runner.commands())
			}
		})
	}

	record := acceptanceInjectionRecord()
	record.acceptanceInjectionState = acceptanceInjectionConfirmed
	record.classifyObservedExitLocked(9)
	if !record.acceptanceTerminalUnproven || record.terminationCause != execution.TerminationNone {
		t.Fatalf("wrong injected exit was trusted: %#v", record)
	}

	process := newFakeProcess()
	runner := &fakeRunner{processes: []*fakeProcess{process}, forcedKillExitCode: 9}
	supervisor := newTestSupervisor(t, runner)
	supervisor.config.DeathWait = 100 * time.Millisecond
	invocation := testAcceptanceInvocation(t, testSource(t), "attempt-failure")
	supervisor.config.AcceptanceAuthority = testAcceptanceController(t, filepath.Join(t.TempDir(), "consumptions"), nil)
	outcome := supervisor.Start(context.Background(), invocation, &testSink{binding: invocation.LaunchToken()})
	if outcome.Kind != execution.Started {
		t.Fatalf("wrong-exit Start() = %#v", outcome)
	}
	waitDone(t, supervisor.record(outcome.Handle).done)
	drained, err := supervisor.Drain(context.Background(), outcome.Handle, execution.StreamOffsets{}, persistedLogBytes*2)
	if err != nil || drained.TerminalFiles.TerminationCause != execution.TerminationNone || drained.TerminalFiles.ExitCode != 0 || len(drained.TerminalFiles.Paths) != 0 {
		t.Fatalf("wrong injected exit public projection = %#v, %v", drained.TerminalFiles, err)
	}
}

func TestDeadlineCauseCannotOverwriteObservedExit(t *testing.T) {
	record := &attemptRecord{exitObserved: true, exitCode: 0, terminationCause: execution.TerminationNone}
	if record.markDeadlineIfLive() || record.timedOut || record.terminationCause != execution.TerminationNone {
		t.Fatalf("deadline overwrote observed exit: %#v", record)
	}
}

func TestStartFailsClosedBeforeDockerWhenProjectionSourcesDrift(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *Supervisor)
	}{
		{name: "credential permissions", mutate: func(t *testing.T, supervisor *Supervisor) {
			if err := os.Chmod(supervisor.config.CredentialSource, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "trust identity", mutate: func(t *testing.T, supervisor *Supervisor) {
			path := filepath.Join(t.TempDir(), "wrong-ca.pem")
			if err := os.WriteFile(path, []byte("wrong trust\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			supervisor.config.TrustAnchorSource = path
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeRunner{processes: []*fakeProcess{newFakeProcess()}}
			supervisor := newTestSupervisor(t, runner)
			test.mutate(t, supervisor)
			invocation := testInvocation(t, testSource(t), "launch-projection-drift")
			outcome := supervisor.Start(context.Background(), invocation, &testSink{binding: invocation.LaunchToken()})
			if outcome.Kind != execution.StartProvenNoChild || len(runner.commands()) != 0 {
				t.Fatalf("projection drift did not fail closed: outcome=%#v commands=%#v", outcome, runner.commands())
			}
		})
	}
}

func TestStartFailureCleansOwnedResources(t *testing.T) {
	runner := &fakeRunner{failRunAt: 1}
	supervisor := newTestSupervisor(t, runner)
	invocation := testInvocation(t, testSource(t), "launch-failure")
	outcome := supervisor.Start(context.Background(), invocation, &testSink{binding: invocation.LaunchToken()})
	if outcome.Kind != execution.StartProvenNoChild {
		t.Fatalf("Start() = %#v", outcome)
	}
	entries, err := os.ReadDir(supervisor.config.RuntimeRoot)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed start retained attempt: entries=%v err=%v", entries, err)
	}
}

func TestAgentEndClosesStdinAndStreamsAreBounded(t *testing.T) {
	process := newFakeProcess()
	runner := &fakeRunner{processes: []*fakeProcess{process}}
	supervisor := newTestSupervisor(t, runner)
	invocation := testInvocation(t, testSource(t), "launch-stream")
	sink := &testSink{binding: invocation.LaunchToken()}
	outcome := supervisor.Start(context.Background(), invocation, sink)
	if outcome.Kind != execution.Started {
		t.Fatalf("Start() = %#v", outcome)
	}

	process.stdout.Write(bytes.Repeat([]byte("x"), persistedLogBytes+1024))
	process.stdout.Write([]byte("\n{\"type\":\"agent_end\"}\n"))
	process.stderr.Write(bytes.Repeat([]byte("e"), persistedLogBytes+1024))
	process.waitClosed(t)
	if process.closeCount != 1 {
		t.Fatalf("stdin close count = %d", process.closeCount)
	}
	chunk, err := supervisor.Read(context.Background(), outcome.Handle, execution.StreamStdout, 0, persistedLogBytes+1)
	if err != nil || len(chunk.Data) != persistedLogBytes {
		t.Fatalf("bounded stdout = %d, err=%v", len(chunk.Data), err)
	}
	if len(sink.notifications) == 0 {
		t.Fatal("no bounded stream notifications")
	}
}

func TestLargeAgentEndClosesStdin(t *testing.T) {
	process := newFakeProcess()
	runner := &fakeRunner{processes: []*fakeProcess{process}}
	supervisor := newTestSupervisor(t, runner)
	invocation := testInvocation(t, testSource(t), "launch-large-agent-end")
	outcome := supervisor.Start(context.Background(), invocation, &testSink{binding: invocation.LaunchToken()})
	if outcome.Kind != execution.Started {
		t.Fatalf("Start() = %#v", outcome)
	}

	event := append([]byte(`{"type":"agent_end","messages":"`), bytes.Repeat([]byte("x"), 70*1024)...)
	event = append(event, []byte(`"}`+"\n")...)
	process.stdout.Write(event)
	process.waitClosed(t)
	if process.closeCount != 1 {
		t.Fatalf("stdin close count = %d", process.closeCount)
	}
}

func TestStopAbortsRevokesRemovesAndProvesDeath(t *testing.T) {
	process := newFakeProcess()
	runner := &fakeRunner{processes: []*fakeProcess{process}, inspectMissing: true, exitOnRemove: true}
	supervisor := newTestSupervisor(t, runner)
	invocation := testInvocation(t, testSource(t), "launch-stop")
	outcome := supervisor.Start(context.Background(), invocation, &testSink{binding: invocation.LaunchToken()})

	stopped, err := supervisor.Stop(context.Background(), outcome.Handle, execution.StopIntent{Kind: execution.StopForCancel})
	if err != nil || stopped.Kind != execution.StopConfirmed {
		t.Fatalf("Stop() = %#v, %v", stopped, err)
	}
	if process.abortCount != 1 {
		t.Fatalf("abort writes = %d", process.abortCount)
	}
	process.mu.Lock()
	closeCount := process.closeCount
	process.mu.Unlock()
	if closeCount != 1 {
		t.Fatalf("forced stop stdin close count = %d", closeCount)
	}
	if process.killCount != 1 {
		t.Fatalf("forced stop process kill count = %d", process.killCount)
	}
	commands := runner.commands()
	assertCommandOrder(t, commands, []string{"network disconnect", "rm -f", "inspect"})
	record := supervisor.record(outcome.Handle)
	if _, err := os.Stat(record.terminal.Paths["patch"]); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled Attempt derived a Patch: %v", err)
	}
	if _, err := os.Stat(record.terminal.Paths["checks"]); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled Attempt ran declared Checks: %v", err)
	}
}

func TestStopWaitsForProcessTreeAfterContainerDisappears(t *testing.T) {
	process := newFakeProcess()
	process.killDelay = time.Millisecond
	runner := &fakeRunner{processes: []*fakeProcess{process}, inspectMissing: true}
	supervisor := newTestSupervisor(t, runner)
	invocation := testInvocation(t, testSource(t), "launch-delayed-process-death")
	outcome := supervisor.Start(context.Background(), invocation, &testSink{binding: invocation.LaunchToken()})

	stopped, err := supervisor.Stop(context.Background(), outcome.Handle, execution.StopIntent{Kind: execution.StopForCancel})
	if err != nil || stopped.Kind != execution.StopConfirmed {
		t.Fatalf("Stop() = %#v, %v", stopped, err)
	}
	drained, err := supervisor.Drain(context.Background(), outcome.Handle, execution.StreamOffsets{}, persistedLogBytes)
	if err != nil || !drained.EOF[execution.StreamStdout] || !drained.EOF[execution.StreamStderr] {
		t.Fatalf("Drain() after confirmed stop = %#v, %v", drained, err)
	}
}

func TestStopFailsClosedWhenDeathUncertain(t *testing.T) {
	process := newFakeProcess()
	runner := &fakeRunner{processes: []*fakeProcess{process}}
	supervisor := newTestSupervisor(t, runner)
	invocation := testInvocation(t, testSource(t), "launch-uncertain")
	outcome := supervisor.Start(context.Background(), invocation, &testSink{binding: invocation.LaunchToken()})

	stopped, err := supervisor.Stop(context.Background(), outcome.Handle, execution.StopIntent{Kind: execution.StopForCancel})
	if err != nil || stopped.Kind != execution.StopUncertain {
		t.Fatalf("Stop() = %#v, %v", stopped, err)
	}
}

func TestStopForcesRemovalAfterCallerContextCancellation(t *testing.T) {
	process := newFakeProcess()
	runner := &fakeRunner{processes: []*fakeProcess{process}, inspectMissing: true, exitOnRemove: true}
	supervisor := newTestSupervisor(t, runner)
	invocation := testInvocation(t, testSource(t), "launch-cancelled-context")
	outcome := supervisor.Start(context.Background(), invocation, &testSink{binding: invocation.LaunchToken()})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	stopped, err := supervisor.Stop(ctx, outcome.Handle, execution.StopIntent{Kind: execution.StopForCancel})
	if err != nil || stopped.Kind != execution.StopConfirmed || !commandsContain(runner.commands(), "rm -f") {
		t.Fatalf("Stop() = %#v, %v; commands=%#v", stopped, err, runner.commands())
	}
}

func TestAttemptTimeoutForcesRemoval(t *testing.T) {
	process := newFakeProcess()
	runner := &fakeRunner{processes: []*fakeProcess{process}, inspectMissing: true, exitOnRemove: true}
	supervisor := newTestSupervisor(t, runner)
	supervisor.config.AttemptTimeout = 2 * time.Millisecond
	supervisor.config.AcceptanceTimeoutPolicyDigest = strings.Repeat("a", 64)
	invocation := testInvocation(t, testSource(t), "launch-timeout")
	outcome := supervisor.Start(context.Background(), invocation, &testSink{binding: invocation.LaunchToken()})
	if outcome.Kind != execution.Started {
		t.Fatalf("Start() = %#v", outcome)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if commandsContain(runner.commands(), "rm -f") {
			record := supervisor.record(outcome.Handle)
			record.mu.Lock()
			timedOut := record.timedOut
			record.mu.Unlock()
			if !timedOut {
				t.Fatal("forced removal did not retain timeout reason")
			}
			waitDone(t, record.done)
			result, err := os.ReadFile(record.terminal.Paths["result"])
			if err != nil || !bytes.Contains(result, []byte("attempt timeout exceeded limit of 2ms")) {
				t.Fatalf("timeout result = %q err=%v", result, err)
			}
			drained, drainErr := supervisor.Drain(context.Background(), outcome.Handle, execution.StreamOffsets{}, persistedLogBytes*2)
			if drainErr != nil || drained.TerminalFiles.TerminationCause != execution.TerminationDeadlineExceeded {
				t.Fatalf("typed timeout cause = %q, %v", drained.TerminalFiles.TerminationCause, drainErr)
			}
			for _, name := range []string{"patch", "checks"} {
				if _, err := os.Stat(record.terminal.Paths[name]); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("timeout %s must be absent: %v", name, err)
				}
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("attempt timeout did not force removal")
}

func TestRetryUsesFreshResourcesAndFinalizeCleans(t *testing.T) {
	p1, p2 := newFakeProcess(), newFakeProcess()
	runner := &fakeRunner{processes: []*fakeProcess{p1, p2}, inspectMissing: true}
	supervisor := newTestSupervisor(t, runner)
	source := testSource(t)
	one := testInvocation(t, source, "launch-one")
	two := testInvocation(t, source, "launch-two")
	o1 := supervisor.Start(context.Background(), one, &testSink{binding: one.LaunchToken()})
	o2 := supervisor.Start(context.Background(), two, &testSink{binding: two.LaunchToken()})
	r1, r2 := supervisor.record(o1.Handle), supervisor.record(o2.Handle)
	if r1.root == r2.root || r1.container == r2.container || r1.internalNetwork == r2.internalNetwork {
		t.Fatal("retry reused attempt resources")
	}
	p1.exit(0)
	p2.exit(1)
	waitDone(t, r1.done)
	waitDone(t, r2.done)
	drainAll(t, supervisor, o1.Handle)
	if err := supervisor.Finalize(context.Background(), o1.Handle, execution.RetentionPolicy{}); err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	if _, err := os.Stat(r1.root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("attempt root retained: %v", err)
	}
}

func TestFinalizeRetainsOwnershipUntilCleanupIsProven(t *testing.T) {
	process := newFakeProcess()
	runner := &fakeRunner{processes: []*fakeProcess{process}, cleanupContainerResidue: "still-running"}
	supervisor := newTestSupervisor(t, runner)
	invocation := testInvocation(t, testSource(t), "launch-cleanup-proof")
	outcome := supervisor.Start(context.Background(), invocation, &testSink{binding: invocation.LaunchToken()})
	process.exit(0)
	waitDone(t, supervisor.record(outcome.Handle).done)
	drainAll(t, supervisor, outcome.Handle)

	if err := supervisor.Finalize(context.Background(), outcome.Handle, execution.RetentionPolicy{}); err == nil {
		t.Fatal("Finalize accepted resource residue")
	}
	if _, err := os.Stat(supervisor.record(outcome.Handle).root); err != nil {
		t.Fatalf("cleanup uncertainty erased runtime root: %v", err)
	}
	reconciled, err := supervisor.Reconcile(context.Background(), outcome.Identity)
	if err != nil || reconciled.Kind != execution.ReconcileDead || reconciled.Handle != outcome.Handle {
		t.Fatalf("cleanup failure dropped ownership: %#v, %v", reconciled, err)
	}
}

func TestTerminalResultReferencesChoraOwnedPatchChecksAndIdentity(t *testing.T) {
	process := newFakeProcess()
	runner := &fakeRunner{processes: []*fakeProcess{process}}
	supervisor := newTestSupervisor(t, runner)
	source := testSource(t)
	invocation := testInvocation(t, source, "launch-terminal-evidence")
	outcome := supervisor.Start(context.Background(), invocation, &testSink{binding: invocation.LaunchToken()})
	if outcome.Kind != execution.Started {
		t.Fatalf("Start() = %#v", outcome)
	}
	record := supervisor.record(outcome.Handle)
	if err := os.WriteFile(filepath.Join(record.repository, "source.txt"), []byte("after\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	process.exit(0)
	waitDone(t, record.done)
	outcomeDrain, err := supervisor.Drain(context.Background(), outcome.Handle, execution.StreamOffsets{}, persistedLogBytes*2)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(outcomeDrain.TerminalFiles.Paths["result"])
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Outputs            []map[string]string `json:"outputs"`
		ArtifactCandidates []map[string]string `json:"artifact_candidates"`
		Unknowns           []string            `json:"unknowns"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Outputs) != 1 || !strings.HasSuffix(result.Outputs[0]["locator"], "/patch.diff") || result.Outputs[0]["sha256"] == "" || result.Outputs[0]["media_type"] != "text/x-diff" || len(result.ArtifactCandidates) != 2 || !strings.HasSuffix(result.ArtifactCandidates[0]["locator"], "/checks.json") || !strings.HasSuffix(result.ArtifactCandidates[1]["locator"], "/runtime-identity.json") || len(result.Unknowns) != 0 {
		t.Fatalf("terminal Result evidence = %#v", result)
	}
	patchPath := filepath.Join(supervisor.config.ArtifactRoot, filepath.FromSlash(result.Outputs[0]["locator"]))
	patchData, err := os.ReadFile(patchPath)
	if err != nil || !strings.Contains(string(patchData), "source.txt") {
		t.Fatalf("patch evidence = %q err=%v", patchData, err)
	}
	digest := sha256.Sum256(patchData)
	if hex.EncodeToString(digest[:]) != result.Outputs[0]["sha256"] {
		t.Fatalf("patch digest = %q", result.Outputs[0]["sha256"])
	}
	identityData, err := os.ReadFile(outcomeDrain.TerminalFiles.Paths["runtime_identity"])
	if err != nil {
		t.Fatal(err)
	}
	var identity map[string]string
	if err := json.Unmarshal(identityData, &identity); err != nil {
		t.Fatal(err)
	}
	if identity["agent_execution_profile"] != string(domain.AgentExecutionProfileStandard) ||
		identity["capability_policy"] != domain.StandardCapabilityPolicy ||
		identity["capability_bundle_id"] != agentpi.StandardCapabilityBundleID ||
		identity["capability_bundle_sha256"] != agentpi.StandardCapabilityBundleSHA256 {
		t.Fatalf("terminal capability identity = %#v", identity)
	}
	command := exec.Command("git", "apply", "--check", patchPath)
	command.Dir = source
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git apply --check = %v: %s", err, output)
	}
	if err := supervisor.Finalize(context.Background(), outcome.Handle, execution.RetentionPolicy{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(patchPath); err != nil {
		t.Fatalf("durable Patch removed by Finalize: %v", err)
	}
}

func TestRecoverEnumeratesOnlyOwnedLabels(t *testing.T) {
	runner := &fakeRunner{runResults: []CommandResult{{Stdout: []byte("owned-one\nowned-two\n")}, {Stdout: []byte("network-one\nnetwork-two\n")}}}
	supervisor := newTestSupervisor(t, runner)
	runner.ownershipLabels = map[string]string{
		"chora.owner": "dockersupervisor", "chora.runtime_scope": supervisor.recoveryScope,
	}
	createOwnedAttemptRoot(t, supervisor, "1", "restart-attempt")
	if err := supervisor.Recover(context.Background()); err != nil {
		t.Fatalf("Recover() error = %v", err)
	}
	commands := runner.commands()
	runtimeScopeDigest := sha256.Sum256([]byte(filepath.Clean(supervisor.config.RuntimeRoot)))
	runtimeScopeFilter := "label=chora.runtime_scope=sha256:" + hex.EncodeToString(runtimeScopeDigest[:])
	if len(commands) < 2 || !strings.Contains(strings.Join(commands[0].Args, " "), "label=chora.owner=dockersupervisor") ||
		!strings.Contains(strings.Join(commands[0].Args, " "), runtimeScopeFilter) ||
		!strings.Contains(strings.Join(commands[1].Args, " "), runtimeScopeFilter) {
		t.Fatalf("unscoped recovery: %#v", commands)
	}
	assertCommandOrder(t, commands, []string{"ps -aq", "network ls", "network disconnect -f", "rm -f owned-one", "network rm network-one"})
	if entries, err := os.ReadDir(supervisor.config.RuntimeRoot); err != nil || len(entries) != 0 {
		t.Fatalf("recovery retained runtime directories: entries=%v err=%v", entries, err)
	}
}

func TestRegisterRecoveredDeadSupportsDrainAndFinalize(t *testing.T) {
	supervisor := newTestSupervisor(t, &fakeRunner{})
	identity := execution.ProcessIdentity{Value: "chora-recovered-identity"}
	launch := execution.LaunchToken{Value: "runtime_session_recovered"}
	if err := supervisor.RegisterRecoveredDead(identity, launch); err == nil {
		t.Fatal("registered recovered runtime before cleanup proof")
	}
	if err := supervisor.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.RegisterRecoveredDead(identity, launch); err != nil {
		t.Fatal(err)
	}
	reconciled, err := supervisor.Reconcile(context.Background(), identity)
	if err != nil || reconciled.Kind != execution.ReconcileDead || !reconciled.Handle.Valid() || reconciled.LaunchToken != launch {
		t.Fatalf("reconciled tombstone = %#v err=%v", reconciled, err)
	}
	offsets := execution.StreamOffsets{execution.StreamStdout: 42, execution.StreamStderr: 13}
	drained, err := supervisor.Drain(context.Background(), reconciled.Handle, offsets, 1024)
	if err != nil || !drained.EOF[execution.StreamStdout] || !drained.EOF[execution.StreamStderr] || drained.Offsets[execution.StreamStdout] != 42 || drained.Offsets[execution.StreamStderr] != 13 {
		t.Fatalf("drained tombstone = %#v err=%v", drained, err)
	}
	if err := supervisor.Finalize(context.Background(), reconciled.Handle, execution.RetentionPolicy{}); err != nil {
		t.Fatal(err)
	}
	unknown, err := supervisor.Reconcile(context.Background(), identity)
	if err != nil || unknown.Kind != execution.ReconcileUncertain {
		t.Fatalf("finalized tombstone = %#v err=%v", unknown, err)
	}
}

func newTestSupervisor(t *testing.T, runner *fakeRunner) *Supervisor {
	t.Helper()
	root := t.TempDir()
	supervisor, err := New(Config{
		Runner: runner, RuntimeRoot: filepath.Join(root, "runtime"),
		PolicyDigest: testPolicyDigest, AttemptImageID: testAttemptImageID,
		BoundaryImageID: testBoundaryImageID, CapabilityContract: testCapabilityContract(t), EngineQualification: testEngineQualification(t),
		RuntimeSourceIdentity: managedRuntimeSourceIdentity(testAttemptImageID),
		CredentialSource:      testCredentialSource(t), TrustAnchorSource: testTrustAnchorSource(t),
		CooperativeWait: time.Millisecond, DeathWait: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() {
		_ = filepath.WalkDir(supervisor.config.ArtifactRoot, func(path string, entry os.DirEntry, err error) error {
			if err == nil && entry.IsDir() {
				_ = os.Chmod(path, 0o700)
			}
			return nil
		})
	})
	supervisor.providerIdentity = ProviderIdentity{
		DockerClientVersion: DockerEngineVersion, DockerServerVersion: DockerEngineVersion,
		DockerContext: DockerContext, ColimaVersion: RequiredColimaVersion,
	}
	return supervisor
}

func testEngineIdentity(t *testing.T) EngineIdentity {
	t.Helper()
	endpointDigest, err := DigestContextEndpoint("unix:///tmp/chora-test-docker.sock")
	if err != nil {
		t.Fatal(err)
	}
	identity, err := NewEngineIdentity(EngineIdentityInput{
		DaemonID: "chora-test-daemon", APIVersion: "1.53", OperatingSystem: CapabilityProbeOperatingOS,
		Architecture: CapabilityProbeArchitecture, ContextEndpointDigest: endpointDigest,
		ProviderName: "test-provider", EngineVersion: "99.7.3", ContextName: "test-context",
	})
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func testEngineQualification(t *testing.T) EngineQualification {
	t.Helper()
	return testEngineQualificationFor(t, testAttemptImageID, testPolicyDigest)
}

func testEngineQualificationFor(t *testing.T, imageID, policyDigest string) EngineQualification {
	t.Helper()
	contract := testCapabilityContractFor(t, imageID, policyDigest)
	qualification, err := newEngineQualification(testEngineIdentity(t), contract, time.Date(2026, 8, 26, 1, 2, 3, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return qualification
}

func testCapabilityContract(t *testing.T) CapabilityProbeContract {
	t.Helper()
	return testCapabilityContractFor(t, testAttemptImageID, testPolicyDigest)
}

func testCapabilityContractFor(t *testing.T, imageID, policyDigest string) CapabilityProbeContract {
	t.Helper()
	contract, err := NewCapabilityProbeContract(imageID, policyDigest)
	if err != nil {
		t.Fatal(err)
	}
	return contract
}

func testCredentialSource(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, []byte(`{"openai-codex":{"access":"test-access-token","refresh":"test-refresh-token"},"unrelated":{"secret":"excluded"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func testTrustAnchorSource(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", "contracts", "g2-m1a", "starpoint-root-ca-2048-g2.pem"))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func testSource(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func testInvocation(t *testing.T, source, launch string) execution.Invocation {
	t.Helper()
	standardBundle, err := agentpi.ManagedCapabilityBundleForProfile(domain.AgentExecutionProfileStandard)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := []byte(`{"task":{"id":"task-1"}}`)
	contract := []byte(`{"schema_version":"chora.spec-coding-core.v8","task":{"id":"task-1"},"acceptance":{"criteria":[{"id":"criterion-1"}]},"execution":{"boundary":{"writable_files":["source.txt"],"commands":[{"id":"test","argv":["go","test","./..."]}],"test_command_ids":["test"]}}}`)
	message, err := execution.WrapFrozenExecutionPrompt(snapshot, contract)
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := json.Marshal(map[string]string{"id": "prompt-1", "type": "prompt", "message": message})
	if err != nil {
		t.Fatal(err)
	}
	prompt = append(prompt, '\n')
	digest := sha256.Sum256(snapshot)
	contractDigest := sha256.Sum256(contract)
	environment := map[string]string{
		EnvRunID: "run-1", EnvAttemptID: launch,
		EnvContextDigest: hex.EncodeToString(digest[:]), EnvPolicyDigest: testPolicyDigest,
		EnvContractDigest:   hex.EncodeToString(contractDigest[:]),
		EnvExecutionProfile: string(domain.AgentExecutionProfileStandard), EnvRuntimeSource: domain.ManagedPiRuntimeSource,
		EnvRuntimeSourceID: managedRuntimeSourceIdentity(testAttemptImageID), EnvRuntimeVersion: managedRuntimeVersion,
		EnvExecutionProvider: domain.DockerExecutionProvider, EnvCapabilityPolicy: domain.StandardCapabilityPolicy,
		EnvCapabilityBundleID: standardBundle.ID(), EnvCapabilityBundleSHA: standardBundle.SHA256(),
		EnvTrustDisclosure: "",
		EnvAttemptImageID:  testAttemptImageID, EnvBoundaryImageID: testBoundaryImageID,
		EnvCommandTimeout: "600", EnvAttemptTimeout: "1200",
		EnvPersistedLogLimit: strconv.Itoa(persistedLogBytes), EnvArtifactLimit: strconv.Itoa(artifactBytes),
		EnvResultPath: "/output/result.json", EnvPatchPath: "/output/patch.diff",
		EnvChecksPath: "/output/checks.json", EnvRuntimeIdentityPath: "/output/runtime-identity.json",
	}
	invocation, err := execution.NewInvocation(execution.InvocationParams{
		AdapterID: allowedAdapter, Executable: allowedExecutable, Arguments: frozenArguments,
		Environment: environment, WorkingRoot: source, Stdin: prompt,
		LaunchToken: execution.LaunchToken{Value: launch},
	})
	if err != nil {
		t.Fatal(err)
	}
	return invocation
}

func testInvocationForProfile(t *testing.T, source, launch string, profile domain.AgentExecutionProfile) execution.Invocation {
	t.Helper()
	invocation := testInvocation(t, source, launch)
	if profile == domain.AgentExecutionProfileStandard {
		return invocation
	}
	bundle, err := agentpi.ManagedCapabilityBundleForProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	environment := invocation.Environment()
	environment[EnvExecutionProfile] = string(profile)
	environment[EnvCapabilityPolicy] = domain.MinimalCapabilityPolicy
	environment[EnvCapabilityBundleID] = bundle.ID()
	environment[EnvCapabilityBundleSHA] = bundle.SHA256()
	return remakeInvocation(t, invocation, environment)
}

func testAcceptanceInvocation(t *testing.T, source, launch string) execution.Invocation {
	t.Helper()
	standardBundle, err := agentpi.ManagedCapabilityBundleForProfile(domain.AgentExecutionProfileStandard)
	if err != nil {
		t.Fatal(err)
	}
	const taskID = "task_018f0000-0000-7002-8000-000000000002"
	const snapshotID = "context_snapshot_018f0000-0000-7004-8000-000000000004"
	snapshot := []byte(`{"task":{"id":"` + taskID + `"}}`)
	contextDigest := sha256.Sum256(snapshot)
	contract := []byte(fmt.Sprintf(`{"schema_version":"chora.spec-coding-core.v8","task":{"id":%q},"acceptance":{"criteria":[{"id":"criterion-1"}]},"execution":{"input":{"context_snapshot_id":%q,"context_snapshot_digest":%q},"boundary":{"writable_files":["source.txt"],"commands":[{"id":"test","argv":["go","test","./..."]}],"test_command_ids":["test"]}}}`, taskID, snapshotID, hex.EncodeToString(contextDigest[:])))
	message, err := execution.WrapFrozenExecutionPrompt(snapshot, contract)
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := json.Marshal(map[string]string{"id": "prompt-acceptance", "type": "prompt", "message": message})
	if err != nil {
		t.Fatal(err)
	}
	prompt = append(prompt, '\n')
	contractDigest := sha256.Sum256(contract)
	environment := map[string]string{
		EnvRunID: "run-failure", EnvAttemptID: launch, EnvContextDigest: hex.EncodeToString(contextDigest[:]), EnvContractDigest: hex.EncodeToString(contractDigest[:]),
		EnvPolicyDigest: testPolicyDigest, EnvExecutionProfile: string(domain.AgentExecutionProfileStandard), EnvRuntimeSource: domain.ManagedPiRuntimeSource,
		EnvRuntimeSourceID: managedRuntimeSourceIdentity(testAttemptImageID), EnvRuntimeVersion: managedRuntimeVersion, EnvExecutionProvider: domain.DockerExecutionProvider,
		EnvCapabilityPolicy: domain.StandardCapabilityPolicy, EnvCapabilityBundleID: standardBundle.ID(), EnvCapabilityBundleSHA: standardBundle.SHA256(),
		EnvTrustDisclosure: "", EnvAttemptImageID: testAttemptImageID, EnvBoundaryImageID: testBoundaryImageID,
		EnvCommandTimeout: "600", EnvAttemptTimeout: "1200", EnvPersistedLogLimit: strconv.Itoa(persistedLogBytes), EnvArtifactLimit: strconv.Itoa(artifactBytes),
		EnvResultPath: "/output/result.json", EnvPatchPath: "/output/patch.diff", EnvChecksPath: "/output/checks.json", EnvRuntimeIdentityPath: "/output/runtime-identity.json",
	}
	invocation, err := execution.NewInvocation(execution.InvocationParams{
		AdapterID: allowedAdapter, Executable: allowedExecutable, Arguments: frozenArguments, Environment: environment,
		WorkingRoot: source, Stdin: prompt, LaunchToken: execution.LaunchToken{Value: launch},
	})
	if err != nil {
		t.Fatal(err)
	}
	return invocation
}

func testAcceptanceController(t *testing.T, root string, syncDirectory func(string) error) *acceptanceauthority.Controller {
	t.Helper()
	root, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	rootParent, err := filepath.EvalSymlinks(filepath.Dir(root))
	if err != nil {
		t.Fatal(err)
	}
	root = filepath.Join(rootParent, filepath.Base(root))
	if err := os.Chmod(rootParent, 0o700); err != nil {
		t.Fatal(err)
	}
	failureSnapshot := []byte(`{"task":{"id":"task_018f0000-0000-7002-8000-000000000002"}}`)
	timeoutSnapshot := []byte(`{"task":{"id":"task_018f0000-0000-7003-8000-000000000003"}}`)
	failureDigest, timeoutDigest := sha256.Sum256(failureSnapshot), sha256.Sum256(timeoutSnapshot)
	controller, err := acceptanceauthority.NewController(acceptanceauthority.ControllerConfig{
		AuthorityDigest: strings.Repeat("a", 64), TupleIdentity: strings.Repeat("b", 64), ConsumptionRoot: root, DirectorySync: syncDirectory,
		Scenarios: []acceptanceauthority.BoundScenario{
			{Contract: acceptanceauthority.Scenario{Scenario: acceptanceauthority.ScenarioFailure, Action: acceptanceauthority.ActionForceManagedNonzero, TaskID: "task_018f0000-0000-7002-8000-000000000002", SnapshotID: "context_snapshot_018f0000-0000-7004-8000-000000000004", SnapshotDigest: hex.EncodeToString(failureDigest[:]), AttemptSequence: 1, AgentExecutionProfile: acceptanceauthority.AgentExecutionProfileStandard}, RunID: "run-failure", AttemptID: "attempt-failure"},
			{Contract: acceptanceauthority.Scenario{Scenario: acceptanceauthority.ScenarioTimeout, Action: acceptanceauthority.ActionAcceleratedManagedDeadline, TaskID: "task_018f0000-0000-7003-8000-000000000003", SnapshotID: "context_snapshot_018f0000-0000-7005-8000-000000000005", SnapshotDigest: hex.EncodeToString(timeoutDigest[:]), AttemptSequence: 1, AgentExecutionProfile: acceptanceauthority.AgentExecutionProfileStandard}, RunID: "run-timeout", AttemptID: "attempt-timeout"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return controller
}

func acceptanceInjectionRecord() *attemptRecord {
	return &attemptRecord{
		containerDockerID: strings.Repeat("a", 64), acceptanceDirective: acceptanceauthority.Directive{Action: acceptanceauthority.ActionForceManagedNonzero},
		acceptanceInjectionState: acceptanceInjectionPending, acceptanceInjectionDone: make(chan struct{}), terminationCause: execution.TerminationNone,
	}
}

func remakeInvocation(t *testing.T, old execution.Invocation, environment map[string]string) execution.Invocation {
	t.Helper()
	invocation, err := execution.NewInvocation(execution.InvocationParams{
		AdapterID: old.AdapterID(), Executable: old.Executable(), Arguments: old.Arguments(), Environment: environment,
		WorkingRoot: old.WorkingRoot(), Stdin: old.Stdin(), LaunchToken: old.LaunchToken(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return invocation
}

func remakeInvocationArguments(t *testing.T, old execution.Invocation, arguments []string) execution.Invocation {
	t.Helper()
	invocation, err := execution.NewInvocation(execution.InvocationParams{
		AdapterID: old.AdapterID(), Executable: old.Executable(), Arguments: arguments, Environment: old.Environment(),
		WorkingRoot: old.WorkingRoot(), Stdin: old.Stdin(), LaunchToken: old.LaunchToken(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return invocation
}

func assertArgsContain(t *testing.T, got []string, want ...string) {
	t.Helper()
	joined := strings.Join(got, " ")
	for _, value := range want {
		if !strings.Contains(joined, value) {
			t.Errorf("argv missing %q: %s", value, joined)
		}
	}
}

func assertAdjacentArgs(t *testing.T, got []string, first, second string) {
	t.Helper()
	for index := 0; index+1 < len(got); index++ {
		if got[index] == first && got[index+1] == second {
			return
		}
	}
	t.Fatalf("argv lacks adjacent %q %q: %v", first, second, got)
}

func assertNoAttemptImageAcquisition(t *testing.T, commands []Command, wantLaunches int) {
	t.Helper()
	launches := 0
	for index, command := range commands {
		if len(command.Args) == 0 {
			continue
		}
		operation := command.Args[0]
		if operation == "image" && len(command.Args) > 1 {
			operation = command.Args[1]
		}
		switch operation {
		case "build", "pull", "load":
			t.Errorf("command %d performs forbidden Attempt-time image operation %q: %v", index, operation, command.Args)
		}
		if command.Args[0] != "run" && command.Args[0] != "create" {
			continue
		}
		launches++
		pullNeverCount := 0
		for _, argument := range command.Args {
			if argument == "--pull=never" {
				pullNeverCount++
			}
		}
		if len(command.Args) < 2 || command.Args[1] != "--pull=never" || pullNeverCount != 1 {
			t.Errorf("Docker launch %d does not carry exact --pull=never in CLI option position: %v", index, command.Args)
		}
	}
	if launches != wantLaunches {
		t.Errorf("Docker launch commands = %d, want %d: %#v", launches, wantLaunches, commands)
	}
}

func assertCommandOrder(t *testing.T, commands []Command, needles []string) {
	t.Helper()
	position := 0
	for _, command := range commands {
		if position < len(needles) && strings.Contains(strings.Join(command.Args, " "), needles[position]) {
			position++
		}
	}
	if position != len(needles) {
		t.Fatalf("command order missing %q: %#v", needles[position:], commands)
	}
}

func commandsContain(commands []Command, needle string) bool {
	for _, command := range commands {
		if strings.Contains(strings.Join(command.Args, " "), needle) {
			return true
		}
	}
	return false
}

func drainAll(t *testing.T, supervisor *Supervisor, handle execution.RuntimeHandle) {
	t.Helper()
	_, err := supervisor.Drain(context.Background(), handle, execution.StreamOffsets{}, persistedLogBytes*2)
	if err != nil {
		t.Fatal(err)
	}
}

func waitDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for process")
	}
}

type fakeRunner struct {
	mu                      sync.Mutex
	calls                   []Command
	processes               []*fakeProcess
	runResults              []CommandResult
	inspectMissing          bool
	containerRemoved        bool
	failRunAt               int
	runErrorAt              int
	runError                error
	runCalls                int
	cleanupContainerResidue string
	exactContainerResidue   string
	ownershipName           string
	ownershipLabels         map[string]string
	exitOnRemove            bool
	effectiveIdentityDrift  bool
	boundaryStopped         bool
	forcedKillExitCode      int
	beforeForcedKill        func()
	engineIdentityDrift     bool
	attemptImageDrift       bool
	boundaryImageDrift      bool
	qualificationReadError  bool
	returnNilProcess        bool
	active                  []*fakeProcess
}

func (runner *fakeRunner) Run(_ context.Context, command Command) (CommandResult, error) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if command.Stdin != nil {
		input, err := io.ReadAll(command.Stdin)
		if err != nil {
			return CommandResult{}, err
		}
		command.Stdin = bytes.NewReader(input)
	}
	runner.calls = append(runner.calls, command)
	if result, handled := runner.executionQualificationResult(command.Args); handled {
		return result, nil
	}
	runner.runCalls++
	if runner.runErrorAt == runner.runCalls {
		return CommandResult{ExitCode: -1}, runner.runError
	}
	if runner.failRunAt == runner.runCalls {
		return CommandResult{ExitCode: 1, Stderr: []byte("injected failure")}, nil
	}
	if len(command.Args) > 0 && command.Args[0] == "inspect" && runner.inspectMissing && runner.containerRemoved {
		return CommandResult{ExitCode: 1, Stderr: []byte("Error: No such object")}, nil
	}
	if len(command.Args) == 6 && command.Args[0] == "inspect" && command.Args[1] == "--type" && command.Args[2] == "container" && command.Args[3] == "--format" && command.Args[4] == "{{.Id}}" {
		id := strings.Repeat("a", 64)
		if strings.HasSuffix(command.Args[5], "-codex-boundary") {
			id = strings.Repeat("b", 64)
		}
		return CommandResult{Stdout: []byte(id + "\n")}, nil
	}
	if len(command.Args) == 6 && command.Args[0] == "inspect" && command.Args[1] == "--type" && command.Args[2] == "container" && command.Args[3] == "--format" && command.Args[4] == "{{.State.Running}}" {
		return CommandResult{Stdout: []byte(strconv.FormatBool(!runner.boundaryStopped) + "\n")}, nil
	}
	if slices.Equal(command.Args, []string{"kill", "--signal", "KILL", strings.Repeat("a", 64)}) {
		if runner.beforeForcedKill != nil {
			runner.beforeForcedKill()
		}
		if len(runner.active) > 0 {
			exitCode := runner.forcedKillExitCode
			if exitCode == 0 {
				exitCode = 137
			}
			runner.active[0].exit(exitCode)
		}
	}
	if len(command.Args) > 1 && command.Args[0] == "rm" && command.Args[1] == "-f" {
		runner.containerRemoved = true
		if len(command.Args) == 3 && command.Args[2] == runner.cleanupContainerResidue {
			runner.cleanupContainerResidue = ""
		}
		if len(command.Args) == 3 && command.Args[2] == runner.exactContainerResidue {
			runner.exactContainerResidue = ""
		}
		if runner.exitOnRemove {
			for _, process := range runner.active {
				process.exit(137)
			}
		}
	}
	if len(command.Args) > 1 && command.Args[0] == "ps" && command.Args[1] == "-aq" && runner.cleanupContainerResidue != "" {
		return CommandResult{Stdout: []byte(runner.cleanupContainerResidue + "\n")}, nil
	}
	if len(command.Args) > 1 && command.Args[0] == "ps" && command.Args[1] == "-aq" && runner.exactContainerResidue != "" && commandsContain([]Command{command}, "name=^/") {
		return CommandResult{Stdout: []byte(runner.exactContainerResidue + "\n")}, nil
	}
	if len(command.Args) == 5 && command.Args[0] == "container" && command.Args[1] == "inspect" && command.Args[2] == "--format" && command.Args[3] == containerOwnershipObservationFormat {
		name := runner.ownershipName
		if name == "" {
			name = "/" + command.Args[4]
		}
		data, err := json.Marshal(resourceOwnershipObservation{Name: name, Labels: runner.ownershipLabels})
		return CommandResult{Stdout: data}, err
	}
	if len(command.Args) == 5 && command.Args[0] == "network" && command.Args[1] == "inspect" && command.Args[2] == "--format" && command.Args[3] == networkOwnershipObservationFormat {
		data, err := json.Marshal(resourceOwnershipObservation{Name: command.Args[4], Labels: runner.ownershipLabels})
		return CommandResult{Stdout: data}, err
	}
	if len(command.Args) == 5 && command.Args[0] == "container" && command.Args[1] == "inspect" && command.Args[2] == "--format" && command.Args[3] == "{{json .}}" {
		document, err := fakeEffectiveContainer(runner.calls, command.Args[4])
		if err != nil {
			return CommandResult{ExitCode: 1, Stderr: []byte(err.Error())}, nil
		}
		if runner.effectiveIdentityDrift && strings.HasSuffix(command.Args[4], "-attempt") {
			document.HostConfig.ReadonlyRootfs = false
		}
		encoded, err := json.Marshal(document)
		if err != nil {
			return CommandResult{}, err
		}
		return CommandResult{Stdout: encoded}, nil
	}
	if len(runner.runResults) > 0 {
		result := runner.runResults[0]
		runner.runResults = runner.runResults[1:]
		return result, nil
	}
	return CommandResult{}, nil
}

func fakeEffectiveContainer(commands []Command, name string) (effectiveContainer, error) {
	var launch []string
	for _, command := range commands {
		for index, value := range command.Args {
			if value == "--name" && index+1 < len(command.Args) && command.Args[index+1] == name {
				launch = command.Args
			}
		}
	}
	if len(launch) == 0 {
		return effectiveContainer{}, errors.New("fake launch command not found")
	}
	var document effectiveContainer
	document.ID = strings.Repeat("a", 64)
	if strings.HasSuffix(name, "-codex-boundary") {
		document.ID = strings.Repeat("b", 64)
	}
	document.Name = "/" + name
	document.Config.Labels = map[string]string{}
	document.HostConfig.PortBindings = map[string]json.RawMessage{}
	document.HostConfig.Tmpfs = map[string]string{}
	document.NetworkSettings.Networks = map[string]json.RawMessage{}
	for index := 0; index < len(launch); index++ {
		value := launch[index]
		next := func() string {
			if index+1 < len(launch) {
				return launch[index+1]
			}
			return ""
		}
		switch value {
		case "--label":
			key, labelValue, ok := strings.Cut(next(), "=")
			if ok {
				document.Config.Labels[key] = labelValue
			}
		case "--network":
			document.HostConfig.NetworkMode = next()
			document.NetworkSettings.Networks[next()] = json.RawMessage(`{}`)
		case "--user":
			document.Config.User = next()
		case "--read-only":
			document.HostConfig.ReadonlyRootfs = true
		case "--cap-drop":
			document.HostConfig.CapDrop = append(document.HostConfig.CapDrop, next())
		case "--security-opt":
			document.HostConfig.SecurityOpt = append(document.HostConfig.SecurityOpt, next())
		case "--cpus":
			cpus, _ := strconv.ParseFloat(next(), 64)
			document.HostConfig.NanoCPUs = int64(cpus * 1_000_000_000)
		case "--memory":
			document.HostConfig.Memory = fakeMemoryBytes(next())
		case "--memory-swap":
			document.HostConfig.MemorySwap = fakeMemoryBytes(next())
		case "--pids-limit":
			document.HostConfig.PidsLimit, _ = strconv.ParseInt(next(), 10, 64)
		case "--ulimit":
			parts := strings.Split(next(), "=")
			limits := strings.Split(parts[len(parts)-1], ":")
			soft, _ := strconv.ParseInt(limits[0], 10, 64)
			hard, _ := strconv.ParseInt(limits[len(limits)-1], 10, 64)
			document.HostConfig.Ulimits = append(document.HostConfig.Ulimits, struct {
				Name       string `json:"Name"`
				Soft, Hard int64
			}{Name: parts[0], Soft: soft, Hard: hard})
		case "--log-driver":
			document.HostConfig.LogConfig.Type = next()
		case "--tmpfs":
			path, options, ok := strings.Cut(next(), ":")
			if ok {
				document.HostConfig.Tmpfs[path] = options
			}
		case "--env":
			document.Config.Env = append(document.Config.Env, next())
		case "--mount":
			fields := strings.Split(next(), ",")
			mount := struct {
				Type, Source, Destination string
				RW                        bool
			}{RW: true}
			for _, field := range fields {
				key, mountValue, hasValue := strings.Cut(field, "=")
				switch key {
				case "type":
					mount.Type = mountValue
				case "src", "source":
					mount.Source = mountValue
				case "dst", "target":
					mount.Destination = mountValue
				case "readonly":
					if !hasValue {
						mount.RW = false
					}
				}
			}
			document.Mounts = append(document.Mounts, mount)
		case "--entrypoint":
			if index+2 < len(launch) {
				document.Image = launch[index+2]
			}
		}
	}
	if strings.HasSuffix(name, "-codex-boundary") {
		document.Config.User = "65532:65532"
		for _, command := range commands {
			if len(command.Args) >= 6 && command.Args[0] == "network" && command.Args[1] == "connect" && command.Args[len(command.Args)-1] == name {
				document.NetworkSettings.Networks[command.Args[len(command.Args)-2]] = json.RawMessage(`{}`)
			}
		}
		if document.Image == "" {
			document.Image = launch[len(launch)-1]
		}
	}
	return document, nil
}

func fakeMemoryBytes(value string) int64 {
	trimmed := strings.TrimSuffix(value, "m")
	parsed, _ := strconv.ParseInt(trimmed, 10, 64)
	return parsed << 20
}

func (runner *fakeRunner) Start(_ context.Context, command Command) (Process, error) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	runner.calls = append(runner.calls, command)
	if runner.returnNilProcess {
		return nil, nil
	}
	if len(runner.processes) == 0 {
		return nil, errors.New("no fake process")
	}
	process := runner.processes[0]
	runner.processes = runner.processes[1:]
	runner.active = append(runner.active, process)
	process.stdout = command.Stdout
	process.stderr = command.Stderr
	return process, nil
}

func (runner *fakeRunner) commands() []Command {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	commands := make([]Command, 0, len(runner.calls))
	for _, command := range runner.calls {
		if !isExecutionQualificationCommand(command.Args) {
			commands = append(commands, command)
		}
	}
	return commands
}

func (runner *fakeRunner) allCommands() []Command {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return append([]Command(nil), runner.calls...)
}

func (runner *fakeRunner) executionQualificationResult(args []string) (CommandResult, bool) {
	if !isExecutionQualificationCommand(args) {
		return CommandResult{}, false
	}
	if runner.qualificationReadError {
		return CommandResult{ExitCode: 1, Stderr: []byte("injected qualification observation failure")}, true
	}
	identity := EngineIdentityInput{
		DaemonID: "chora-test-daemon", APIVersion: "1.53", OperatingSystem: CapabilityProbeOperatingOS,
		Architecture: CapabilityProbeArchitecture, ProviderName: "test-provider",
		EngineVersion: "99.7.3", ContextName: "test-context",
	}
	if runner.engineIdentityDrift {
		identity.DaemonID = "drifted-daemon"
	}
	switch {
	case slices.Equal(args, []string{"version", "--format", engineVersionObservationFormat}):
		data, _ := json.Marshal(engineVersionObservation{APIVersion: identity.APIVersion, ServerVersion: identity.EngineVersion,
			OperatingSystem: identity.OperatingSystem, Architecture: identity.Architecture})
		return CommandResult{Stdout: data}, true
	case slices.Equal(args, []string{"info", "--format", engineInfoObservationFormat}):
		data, _ := json.Marshal(engineInfoObservation{DaemonID: identity.DaemonID, ProviderName: identity.ProviderName})
		return CommandResult{Stdout: data}, true
	case slices.Equal(args, []string{"context", "show"}):
		return CommandResult{Stdout: []byte(identity.ContextName + "\n")}, true
	case slices.Equal(args, []string{"context", "inspect", "--format", engineEndpointObservationFormat, identity.ContextName}):
		data, _ := json.Marshal("unix:///tmp/chora-test-docker.sock")
		return CommandResult{Stdout: data}, true
	case len(args) == 5 && slices.Equal(args[:4], []string{"image", "inspect", "--format", "{{.Id}}"}):
		imageID := args[4]
		isBoundary := imageID == testBoundaryImageID || imageID == agentpi.BoundaryImageID
		if isBoundary && runner.boundaryImageDrift {
			imageID = "sha256:" + strings.Repeat("d", 64)
		} else if !isBoundary && runner.attemptImageDrift {
			imageID = "sha256:" + strings.Repeat("c", 64)
		}
		return CommandResult{Stdout: []byte(imageID + "\n")}, true
	default:
		return CommandResult{}, false
	}
}

func isExecutionQualificationCommand(args []string) bool {
	return slices.Equal(args, []string{"version", "--format", engineVersionObservationFormat}) ||
		slices.Equal(args, []string{"info", "--format", engineInfoObservationFormat}) ||
		slices.Equal(args, []string{"context", "show"}) ||
		(len(args) == 5 && slices.Equal(args[:4], []string{"context", "inspect", "--format", engineEndpointObservationFormat})) ||
		(len(args) == 5 && slices.Equal(args[:4], []string{"image", "inspect", "--format", "{{.Id}}"}) && validImageID(args[4]))
}

type fakeProcess struct {
	mu                    sync.Mutex
	stdout                io.Writer
	stderr                io.Writer
	done                  chan struct{}
	exitCode              int
	stdin                 bytes.Buffer
	closeCount            int
	abortCount            int
	killCount             int
	waitCount             int
	killDelay             time.Duration
	killErr               error
	writeErr              error
	partialWrite          int
	afterDoneBeforeReturn func()
}

func newFakeProcess() *fakeProcess { return &fakeProcess{done: make(chan struct{})} }

func (process *fakeProcess) Write(data []byte) (int, error) {
	process.mu.Lock()
	defer process.mu.Unlock()
	if bytes.Contains(data, []byte(`"type":"abort"`)) {
		process.abortCount++
	}
	if process.writeErr != nil {
		return 0, process.writeErr
	}
	if process.partialWrite > 0 && process.partialWrite < len(data) {
		_, _ = process.stdin.Write(data[:process.partialWrite])
		return process.partialWrite, nil
	}
	return process.stdin.Write(data)
}

func (process *fakeProcess) Close() error {
	process.mu.Lock()
	process.closeCount++
	process.mu.Unlock()
	return nil
}

func (process *fakeProcess) Kill() error {
	process.mu.Lock()
	process.killCount++
	delay := process.killDelay
	killErr := process.killErr
	process.mu.Unlock()
	if killErr != nil {
		return killErr
	}
	if delay > 0 {
		go func() {
			time.Sleep(delay)
			process.exit(137)
		}()
	} else {
		process.exit(137)
	}
	return nil
}

func (process *fakeProcess) Wait() (int, error) {
	process.mu.Lock()
	process.waitCount++
	afterDoneBeforeReturn := process.afterDoneBeforeReturn
	process.mu.Unlock()
	<-process.done
	if afterDoneBeforeReturn != nil {
		afterDoneBeforeReturn()
	}
	process.mu.Lock()
	defer process.mu.Unlock()
	return process.exitCode, nil
}

func (process *fakeProcess) exit(code int) {
	process.mu.Lock()
	defer process.mu.Unlock()
	process.exitCode = code
	select {
	case <-process.done:
	default:
		close(process.done)
	}
}

func (process *fakeProcess) waitClosed(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		process.mu.Lock()
		closed := process.closeCount > 0
		process.mu.Unlock()
		if closed {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("stdin was not closed")
}

type testSink struct {
	mu            sync.Mutex
	binding       execution.LaunchToken
	notifications []execution.StreamKind
	exited        int
}

func (sink *testSink) Binding() execution.LaunchToken { return sink.binding }
func (sink *testSink) Notify(kind execution.StreamKind, _ int64) {
	sink.mu.Lock()
	sink.notifications = append(sink.notifications, kind)
	sink.mu.Unlock()
}
func (sink *testSink) Exited() { sink.mu.Lock(); sink.exited++; sink.mu.Unlock() }
