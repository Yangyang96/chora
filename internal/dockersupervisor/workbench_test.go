package dockersupervisor

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/execution"
)

func TestWorkbenchConfigIsExplicitAndPinned(t *testing.T) {
	root := t.TempDir()
	config := Config{
		Runner: &fakeRunner{}, RuntimeRoot: filepath.Join(root, "runtime"),
		ArtifactRoot: filepath.Join(root, "artifacts"), PolicyDigest: WorkbenchPolicyDigest,
		AttemptImageID: testAttemptImageID, RuntimeSourceIdentity: strings.Repeat("a", 64),
		CredentialSource: testCredentialSource(t), CapabilityContract: testCapabilityContractFor(t, testAttemptImageID, WorkbenchPolicyDigest),
		EngineQualification: testEngineQualificationFor(t, testAttemptImageID, WorkbenchPolicyDigest),
		Workbench: &WorkbenchConfig{
			Arguments: workbenchArguments(), RuntimeVersion: "0.85.1",
			ObserverSHA256: strings.Repeat("b", 64), HelperSHA256: strings.Repeat("c", 64), RuntimeFingerprint: strings.Repeat("d", 64),
			PrepareWorkspace:       func(context.Context, execution.Invocation, string) error { return nil },
			CollectWorkspace:       func(context.Context, execution.Invocation, string) error { return nil },
			ReadOnlyWorkspacePaths: func(execution.Invocation) ([]string, error) { return []string{"repo-a/.git", "reference"}, nil },
		},
	}
	if _, err := New(config); err != nil {
		t.Fatalf("New(workbench) error = %v", err)
	}
	config.PolicyDigest = pinnedPolicyDigest
	if _, err := New(config); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("New(workbench drift) error = %v", err)
	}
}

func TestValidateWorkbenchPromptAcceptsIsolatedV12SuccessorCorrection(t *testing.T) {
	snapshot := []byte(`{"task":{"id":"task-successor"}}`)
	contract := []byte(`{"schema_version":"chora.spec-coding-core.v12","task":{"id":"task-successor"}}`)
	message, err := execution.WrapIsolatedExecutionPrompt(snapshot, contract, nil)
	if err != nil {
		t.Fatal(err)
	}
	message += "\n\nHuman Review correction for this successor Attempt. Apply these instructions within the frozen execution contract; preserve its authority and boundaries:\nfix the reviewer finding"
	prompt, _ := json.Marshal(map[string]string{"id": "chora-prompt", "type": "prompt", "message": message})
	stdin := append([]byte("{\"id\":\"chora-get-state\",\"type\":\"get_state\"}\n"), append(prompt, '\n')...)
	snapshotDigest, contractDigest := sha256.Sum256(snapshot), sha256.Sum256(contract)
	_, taskID, err := validateWorkbenchPrompt(stdin, hex.EncodeToString(snapshotDigest[:]), hex.EncodeToString(contractDigest[:]))
	if err != nil || taskID != "task-successor" {
		t.Fatalf("validateWorkbenchPrompt() = %q, %v", taskID, err)
	}
}

func TestValidateWorkbenchPromptTreatsDeclaredHomeLocatorAsContextOnly(t *testing.T) {
	snapshot := []byte(`{"task":{"id":"task-home","selectedPath":"/Users/alice/project"}}`)
	contract := []byte(`{"schema_version":"chora.spec-coding-core.v12","task":{"id":"task-home"}}`)
	message, err := execution.WrapIsolatedExecutionPrompt(snapshot, contract, nil)
	if err != nil {
		t.Fatal(err)
	}
	prompt, _ := json.Marshal(map[string]string{"id": "chora-prompt", "type": "prompt", "message": message})
	stdin := append([]byte("{\"id\":\"chora-get-state\",\"type\":\"get_state\"}\n"), append(prompt, '\n')...)
	sd, cd := sha256.Sum256(snapshot), sha256.Sum256(contract)
	if _, _, err := validateWorkbenchPrompt(stdin, hex.EncodeToString(sd[:]), hex.EncodeToString(cd[:])); err != nil {
		t.Fatal(err)
	}
}

func TestWorkbenchRejectsCapabilityBundleIdentityDrift(t *testing.T) {
	runner := &fakeRunner{}
	supervisor := newWorkbenchTestSupervisor(t, runner, func(context.Context, execution.Invocation, string) error { return nil })
	invocation := testWorkbenchInvocation(t, testSource(t), "workbench-bundle-drift", supervisor.config)
	environment := invocation.Environment()
	environment[EnvCapabilityBundleID] = "other"
	invocation = remakeInvocation(t, invocation, environment)
	outcome := supervisor.Start(context.Background(), invocation, &testSink{binding: invocation.LaunchToken()})
	if outcome.Kind != execution.StartProvenNoChild || len(runner.allCommands()) != 0 {
		t.Fatalf("Start() = %#v commands=%#v", outcome, runner.allCommands())
	}
}

func TestWorkbenchDeepSeekProjectionRequiresLiteralAPIKeyAndProjectsOnlyDeepSeek(t *testing.T) {
	valid := `{"deepseek":{"type":"api_key","key":"literal-secret"},"openai-codex":{"type":"oauth","access":"excluded-secret"}}`
	path := writeWorkbenchCredential(t, valid)
	projection, err := loadWorkbenchAPIKeyProjection(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]map[string]any
	if err := json.Unmarshal(projection.auth, &document); err != nil || len(document) != 1 || document["deepseek"]["type"] != "api_key" || document["deepseek"]["key"] != "literal-secret" {
		t.Fatalf("projected auth = %s", projection.auth)
	}
	if bytes.Contains(projection.auth, []byte("openai-codex")) || bytes.Contains(projection.auth, []byte("excluded-secret")) {
		t.Fatalf("projected unrelated provider: %s", projection.auth)
	}
	if !slices.ContainsFunc(projection.secrets, func(value []byte) bool { return string(value) == "literal-secret" }) {
		t.Fatal("DeepSeek key was not retained for stream redaction")
	}
	for name, document := range map[string]string{
		"missing provider": `{"openai-codex":{"type":"oauth","access":"excluded-secret"}}`,
		"wrong type":       `{"deepseek":{"type":"oauth","key":"literal-secret"}}`,
		"missing key":      `{"deepseek":{"type":"api_key"}}`,
		"empty key":        `{"deepseek":{"type":"api_key","key":""}}`,
		"command key":      `{"deepseek":{"type":"api_key","key":"!security find-generic-password"}}`,
		"environment key":  `{"deepseek":{"type":"api_key","key":"$DEEPSEEK_API_KEY"}}`,
		"braced env key":   `{"deepseek":{"type":"api_key","key":"${DEEPSEEK_API_KEY}"}}`,
		"bare env key":     `{"deepseek":{"type":"api_key","key":"DEEPSEEK_API_KEY"}}`,
		"multiline key":    "{\"deepseek\":{\"type\":\"api_key\",\"key\":\"literal\\nsecret\"}}",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := loadWorkbenchAPIKeyProjection(writeWorkbenchCredential(t, document)); err == nil {
				t.Fatal("accepted invalid Workbench DeepSeek API key")
			}
		})
	}
}

func TestWorkbenchDeepSeekInjectionVerifiesExactEntryAndOwnerOnlyMode(t *testing.T) {
	runner := &fakeRunner{}
	supervisor := &Supervisor{config: Config{Runner: runner}}
	projection := taskProjection{auth: []byte(`{"deepseek":{"type":"api_key","key":"literal-secret"}}` + "\n")}
	if err := supervisor.injectWorkbenchAPIKeyProjection(context.Background(), "attempt", projection); err != nil {
		t.Fatal(err)
	}
	commands := runner.allCommands()
	if len(commands) != 2 {
		t.Fatalf("injection commands = %#v", commands)
	}
	projected, err := io.ReadAll(commands[0].Stdin)
	if err != nil || !bytes.Equal(projected, projection.auth) {
		t.Fatalf("projected auth = %q, %v", projected, err)
	}
	probe := strings.Join(commands[1].Args, " ")
	for _, required := range []string{"Object.keys(v).length!==1", "Object.keys(e).length!==2", "e.type!=='api_key'", "typeof e.key!=='string'", "mode&511)!==384"} {
		if !strings.Contains(probe, required) {
			t.Fatalf("projection probe missing %q: %s", required, probe)
		}
	}
}

func TestPrepareWorkbenchMountParentsMakesOnlyPrivateAncestorsWritable(t *testing.T) {
	runner := &fakeRunner{}
	supervisor := &Supervisor{config: Config{Runner: runner}}
	record := &attemptRecord{container: "attempt", readOnlyWorkspaceMounts: []workbenchReadOnlyMount{
		{destination: "/workspace/repository/repo/.git"},
		{destination: "/workspace/repository/reference"},
		{destination: "/workspace/repository/reference/.git"},
	}}
	if err := supervisor.prepareWorkbenchMountParents(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	commands := runner.allCommands()
	if len(commands) != 1 {
		t.Fatalf("parent preparation commands = %#v", commands)
	}
	wantPaths := []string{"/workspace/repository", "/workspace/repository/repo"}
	if !slices.Equal(commands[0].Args, append([]string{"exec", "--user", "0:0", "attempt", "chmod", "0777"}, wantPaths...)) {
		t.Fatalf("parent preparation commands = %#v", commands)
	}
}

func TestConfigureWorkbenchGitTrustsOnlyRepositoriesWithReadOnlyGitMetadata(t *testing.T) {
	runner := &fakeRunner{}
	supervisor := &Supervisor{config: Config{Runner: runner}}
	record := &attemptRecord{container: "attempt", readOnlyWorkspaceMounts: []workbenchReadOnlyMount{
		{destination: "/workspace/repository/repo/.git"},
		{destination: "/workspace/repository/reference"},
	}}
	if err := supervisor.configureWorkbenchGitSafeDirectories(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	commands := runner.allCommands()
	want := []string{"exec", "--user", "1000:1000", "attempt", "git", "config", "--global", "--add", "safe.directory", "/workspace/repository/repo"}
	if len(commands) != 1 || !slices.Equal(commands[0].Args, want) {
		t.Fatalf("Git safe.directory commands = %#v", commands)
	}
}

func TestWorkbenchRejectsExistingWorkspaceVolumeWithWrongOptions(t *testing.T) {
	runner := &fakeRunner{runResults: []CommandResult{{Stdout: []byte(`{"Name":"chora-aaaaaaaaaaaaaaaaaaaaaaaa-workspace","Driver":"local","Options":{"type":"tmpfs","device":"tmpfs","o":"size=1g"},"Labels":{"chora.owner":"dockersupervisor","chora.runtime_scope":"scope","chora.run_id":"run","chora.attempt_id":"attempt","chora.task_id":"task","chora.image_digest":"image","chora.policy_digest":"policy"}}`)}}}
	supervisor := &Supervisor{config: Config{Runner: runner}}
	record := &attemptRecord{workspaceVolume: "chora-aaaaaaaaaaaaaaaaaaaaaaaa-workspace"}
	labels := map[string]string{"chora.owner": "dockersupervisor", "chora.runtime_scope": "scope", "chora.run_id": "run", "chora.attempt_id": "attempt", "chora.task_id": "task", "chora.image_digest": "image", "chora.policy_digest": "policy"}
	if err := supervisor.verifyWorkbenchWorkspaceVolume(context.Background(), record, labels); err == nil {
		t.Fatal("accepted pre-existing Workbench volume with wrong options")
	}
}

func TestWorkbenchLifecycleUsesOneContainerAndCollectsOnlyAfterDeathProof(t *testing.T) {
	process := newFakeProcess()
	runner := &fakeRunner{processes: []*fakeProcess{process}, boundaryStopped: true}
	collected := false
	supervisor := newWorkbenchTestSupervisor(t, runner, func(_ context.Context, _ execution.Invocation, repository string) error {
		if !commandsContain(runner.allCommands(), "{{.State.Running}}") {
			return errors.New("collection preceded container death proof")
		}
		collected = true
		return os.WriteFile(filepath.Join(repository, "collected.txt"), []byte("ok"), 0o600)
	})
	invocation := testWorkbenchInvocation(t, testSource(t), "workbench-success", supervisor.config)
	outcome := supervisor.Start(context.Background(), invocation, &testSink{binding: invocation.LaunchToken()})
	if outcome.Kind != execution.Started {
		t.Fatalf("Start() = %#v", outcome)
	}
	if _, err := process.stdout.Write([]byte("provider failed with synthetic-deepseek-")); err != nil {
		t.Fatal(err)
	}
	if _, err := process.stdout.Write([]byte("secret\n")); err != nil {
		t.Fatal(err)
	}
	output, err := supervisor.Read(context.Background(), outcome.Handle, execution.StreamStdout, 0, 4096)
	if err != nil || bytes.Contains(output.Data, []byte("synthetic-deepseek-secret")) || !bytes.Contains(output.Data, []byte(strings.Repeat("*", len("synthetic-deepseek-secret")))) {
		t.Fatalf("DeepSeek stream redaction = %q, %v", output.Data, err)
	}
	process.exit(0)
	record := supervisor.record(outcome.Handle)
	waitDone(t, record.done)
	if !collected {
		t.Fatal("successful Workbench was not collected")
	}
	data, err := os.ReadFile(record.terminal.Paths["result"])
	if err != nil || !strings.Contains(string(data), `"review_ready":true`) {
		t.Fatalf("result = %q, %v", data, err)
	}
	commands := runner.allCommands()
	if commandsContain(commands, "codex-boundary") || commandsContain(commands, "network create") {
		t.Fatalf("Workbench created boundary resources: %#v", commands)
	}
	create := slices.IndexFunc(commands, func(command Command) bool { return len(command.Args) > 0 && command.Args[0] == "create" })
	if create < 0 {
		t.Fatal("missing Workbench create command")
	}
	joined := strings.Join(commands[create].Args, " ")
	for _, required := range []string{"--network bridge", "--read-only", "--cap-drop ALL", "no-new-privileges:true", "--pids-limit 256", "--cpus 2", "--memory 4096m", "type=volume,src=" + record.workspaceVolume + ",dst=/workspace", "dst=/workspace/repository/repo/.git,readonly"} {
		if !strings.Contains(joined, required) {
			t.Errorf("create command missing %q: %s", required, joined)
		}
	}
	if strings.Contains(joined, "type=bind,src="+record.root+",dst=/workspace") {
		t.Fatalf("Workbench exposed writable host workspace: %s", joined)
	}
	if !commandsContain(commands, "volume create --driver local --opt type=tmpfs --opt device=tmpfs --opt o=size=256m,uid=1000,gid=1000,nosuid,nodev") {
		t.Fatalf("Workbench omitted bounded local tmpfs volume: %#v", commands)
	}
	if !commandsContain(commands, record.container+" "+allowedExecutable+" --mode rpc") {
		t.Fatalf("Workbench did not execute %s: %#v", allowedExecutable, commands)
	}
	assertCommandOrder(t, commands, []string{"pause ", "cp ", "unpause ", "kill ", "{{.State.Running}}"})
	drainAll(t, supervisor, outcome.Handle)
	if err := supervisor.Finalize(context.Background(), outcome.Handle, execution.RetentionPolicy{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(record.root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runtime root remains: %v", err)
	}
}

func TestWorkbenchDoesNotCollectCancelledOrFailedExecution(t *testing.T) {
	for _, exitCode := range []int{1, 137} {
		t.Run(string(rune(exitCode)), func(t *testing.T) {
			process := newFakeProcess()
			runner := &fakeRunner{processes: []*fakeProcess{process}, boundaryStopped: true}
			calls := 0
			supervisor := newWorkbenchTestSupervisor(t, runner, func(context.Context, execution.Invocation, string) error { calls++; return nil })
			invocation := testWorkbenchInvocation(t, testSource(t), "workbench-failure-"+string(rune(exitCode)), supervisor.config)
			outcome := supervisor.Start(context.Background(), invocation, &testSink{binding: invocation.LaunchToken()})
			if outcome.Kind != execution.Started {
				t.Fatalf("Start() = %#v", outcome)
			}
			process.exit(exitCode)
			waitDone(t, supervisor.record(outcome.Handle).done)
			if calls != 0 {
				t.Fatalf("CollectWorkspace calls = %d", calls)
			}
		})
	}
}

func TestWorkbenchFailsClosedWhenContainerDeathIsUnproven(t *testing.T) {
	process := newFakeProcess()
	runner := &fakeRunner{processes: []*fakeProcess{process}, workbenchKillDrift: true}
	calls := 0
	supervisor := newWorkbenchTestSupervisor(t, runner, func(context.Context, execution.Invocation, string) error { calls++; return nil })
	invocation := testWorkbenchInvocation(t, testSource(t), "workbench-unproven-death", supervisor.config)
	outcome := supervisor.Start(context.Background(), invocation, &testSink{binding: invocation.LaunchToken()})
	if outcome.Kind != execution.Started {
		t.Fatalf("Start() = %#v", outcome)
	}
	process.exit(0)
	record := supervisor.record(outcome.Handle)
	waitDone(t, record.done)
	if calls != 0 {
		t.Fatalf("CollectWorkspace calls = %d", calls)
	}
	data, err := os.ReadFile(record.terminal.Paths["result"])
	if err != nil || !strings.Contains(string(data), `"review_ready":false`) {
		t.Fatalf("result = %q, %v", data, err)
	}
	if record.exitCode == 0 {
		t.Fatal("unproven death retained successful exit")
	}
}

func TestResolveReadOnlyWorkspacePathsRejectsEscapeSymlinkAndMissingPath(t *testing.T) {
	repository := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repository, "repo", ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(repository, "escape")); err != nil {
		t.Fatal(err)
	}
	if got, err := resolveReadOnlyWorkspacePaths(repository, []string{"repo/.git"}); err != nil || len(got) != 1 || got[0].destination != "/workspace/repository/repo/.git" {
		t.Fatalf("valid paths = %#v, %v", got, err)
	}
	for _, paths := range [][]string{{"../outside"}, {"/absolute"}, {"repo/../repo/.git"}, {"escape"}, {"missing"}, {"repo/.git", "repo/.git"}} {
		if _, err := resolveReadOnlyWorkspacePaths(repository, paths); err == nil {
			t.Fatalf("accepted unsafe paths %#v", paths)
		}
	}
}

func TestMakeReadOnlyMountTreeReadablePreservesOnlyExecutableClass(t *testing.T) {
	root := t.TempDir()
	t.Cleanup(func() {
		_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err == nil {
				_ = os.Chmod(path, 0o700)
			}
			return nil
		})
	})
	directory := filepath.Join(root, "nested")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	plain, executable := filepath.Join(directory, "plain"), filepath.Join(directory, "tool")
	if err := os.WriteFile(plain, []byte("plain"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("tool"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := makeReadOnlyMountTreeReadable(root); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{root: 0o555, directory: 0o555, plain: 0o444, executable: 0o555} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Fatalf("%s mode = %v; want %v", path, info.Mode().Perm(), want)
		}
	}
}

func TestRestoreExportedMountModesPreventsReferenceModeDrift(t *testing.T) {
	staging := t.TempDir()
	reference := filepath.Join(staging, "reference")
	if err := os.Mkdir(reference, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(reference, 0o700) })
	source := filepath.Join(reference, "source.txt")
	if err := os.WriteFile(source, []byte("same"), 0o644); err != nil {
		t.Fatal(err)
	}
	mounts, err := resolveReadOnlyWorkspacePaths(staging, []string{"reference"})
	if err != nil {
		t.Fatal(err)
	}
	if err := makeReadOnlyMountTreeReadable(reference); err != nil {
		t.Fatal(err)
	}
	exported := t.TempDir()
	exportedReference := filepath.Join(exported, "reference")
	if err := os.Mkdir(exportedReference, 0o700); err != nil {
		t.Fatal(err)
	}
	exportedSource := filepath.Join(exportedReference, "source.txt")
	if err := os.WriteFile(exportedSource, []byte("same"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := restoreExportedMountModes(exported, mounts); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(exportedSource)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("exported mode=%o", info.Mode().Perm())
	}
}

func TestExtractWorkbenchArchiveRejectsLinksAndEscapesBeforeHostWrite(t *testing.T) {
	for name, header := range map[string]*tar.Header{
		"symlink":  {Name: "link", Typeflag: tar.TypeSymlink, Linkname: "../sentinel"},
		"hardlink": {Name: "link", Typeflag: tar.TypeLink, Linkname: "../sentinel"},
		"escape":   {Name: "../sentinel", Typeflag: tar.TypeReg, Size: 1},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			destination := filepath.Join(root, "export")
			if err := os.Mkdir(destination, 0o700); err != nil {
				t.Fatal(err)
			}
			sentinel := filepath.Join(root, "sentinel")
			if err := os.WriteFile(sentinel, []byte("safe"), 0o600); err != nil {
				t.Fatal(err)
			}
			var data bytes.Buffer
			writer := tar.NewWriter(&data)
			if err := writer.WriteHeader(header); err != nil {
				t.Fatal(err)
			}
			if header.Size > 0 {
				_, _ = writer.Write([]byte("x"))
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			if err := extractWorkbenchArchive(data.Bytes(), destination); err == nil {
				t.Fatal("accepted unsafe archive")
			}
			got, err := os.ReadFile(sentinel)
			if err != nil || string(got) != "safe" {
				t.Fatalf("sentinel = %q, %v", got, err)
			}
		})
	}
}

func TestExtractWorkbenchArchiveAcceptsDockerDirectoryHeaders(t *testing.T) {
	var data bytes.Buffer
	w := tar.NewWriter(&data)
	if err := w.WriteHeader(&tar.Header{Name: "repo/", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
		t.Fatal(err)
	}
	content := []byte("ok\n")
	if err := w.WriteHeader(&tar.Header{Name: "repo/file.txt", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	if err := extractWorkbenchArchive(data.Bytes(), destination); err != nil {
		t.Fatalf("extract Docker-compatible archive: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(destination, "repo", "file.txt"))
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("extracted file = %q, %v", got, err)
	}
}

func workbenchArguments() []string {
	return []string{"--mode", "rpc", "--no-session", "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-themes", "--no-context-files", "--provider", "deepseek", "--model", "deepseek-v4-flash", "--extension", "/opt/chora/resource_check_observer.mjs"}
}

func newWorkbenchTestSupervisor(t *testing.T, runner *fakeRunner, collect func(context.Context, execution.Invocation, string) error) *Supervisor {
	t.Helper()
	root := t.TempDir()
	config := Config{Runner: runner, RuntimeRoot: filepath.Join(root, "runtime"), ArtifactRoot: filepath.Join(root, "artifacts"),
		PolicyDigest: WorkbenchPolicyDigest, AttemptImageID: testAttemptImageID, RuntimeSourceIdentity: strings.Repeat("a", 64),
		CredentialSource: writeWorkbenchCredential(t, `{"deepseek":{"type":"api_key","key":"synthetic-deepseek-secret"}}`), CapabilityContract: testCapabilityContractFor(t, testAttemptImageID, WorkbenchPolicyDigest),
		EngineQualification: testEngineQualificationFor(t, testAttemptImageID, WorkbenchPolicyDigest), CooperativeWait: time.Millisecond, DeathWait: 5 * time.Millisecond,
	}
	config.Workbench = &WorkbenchConfig{Arguments: workbenchArguments(), RuntimeVersion: "0.85.1", ObserverSHA256: strings.Repeat("b", 64), HelperSHA256: strings.Repeat("c", 64), RuntimeFingerprint: strings.Repeat("d", 64),
		PrepareWorkspace: func(_ context.Context, _ execution.Invocation, repository string) error {
			if err := os.MkdirAll(filepath.Join(repository, "repo", ".git"), 0o700); err != nil {
				return err
			}
			contextDir := filepath.Join(filepath.Dir(filepath.Dir(repository)), "context")
			return os.WriteFile(filepath.Join(contextDir, "observer.json"), []byte("{}\n"), 0o600)
		}, CollectWorkspace: collect, ReadOnlyWorkspacePaths: func(execution.Invocation) ([]string, error) { return []string{"repo/.git"}, nil }}
	supervisor, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	return supervisor
}

func writeWorkbenchCredential(t *testing.T, document string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, append([]byte(document), '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func testWorkbenchInvocation(t *testing.T, source, launch string, config Config) execution.Invocation {
	t.Helper()
	base := testInvocation(t, source, launch)
	environment := base.Environment()
	environment[EnvPolicyDigest] = WorkbenchPolicyDigest
	environment[EnvAttemptImageID] = config.AttemptImageID
	environment[EnvExecutionProfile] = "isolated_local"
	environment[EnvRuntimeSource] = "public_pi_image"
	environment[EnvRuntimeSourceID] = config.RuntimeSourceIdentity
	environment[EnvRuntimeVersion] = "0.85.1"
	environment[EnvCapabilityPolicy] = "chora.isolated-local.v1"
	environment[EnvCapabilityBundleID] = "chora.public-pi-observer.v1"
	environment[EnvCapabilityBundleSHA] = config.Workbench.ObserverSHA256
	environment[EnvBoundaryImageID] = ""
	environment[EnvObserverConfigPath] = "/input/context/observer.json"
	environment[EnvObserverConfigSHA] = digestText([]byte("{}\n"))
	environment[EnvObserverHelperPath] = "/usr/local/bin/chora"
	environment[EnvObserverHelperSHA] = config.Workbench.HelperSHA256
	environment[EnvObserverRuntimeSHA] = config.Workbench.RuntimeFingerprint
	environment[EnvObserverSHA] = config.Workbench.ObserverSHA256
	stdin := append([]byte("{\"id\":\"chora-get-state\",\"type\":\"get_state\"}\n"), base.Stdin()...)
	invocation, err := execution.NewInvocation(execution.InvocationParams{AdapterID: base.AdapterID(), Executable: base.Executable(), Arguments: workbenchArguments(), Environment: environment, WorkingRoot: source, Stdin: stdin, LaunchToken: base.LaunchToken()})
	if err != nil {
		t.Fatal(err)
	}
	return invocation
}
