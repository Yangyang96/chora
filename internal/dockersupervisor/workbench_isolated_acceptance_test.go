//go:build isolated_acceptance

package dockersupervisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	agentpi "github.com/Yangyang96/chora/internal/agent/pi"
	"github.com/Yangyang96/chora/internal/execution"
)

func TestRealWorkbenchCancelIsolationAndZeroResidue(t *testing.T) {
	metadataPath := strings.TrimSpace(os.Getenv("CHORA_ISOLATED_METADATA"))
	if metadataPath == "" || !filepath.IsAbs(metadataPath) {
		t.Fatal("CHORA_ISOLATED_METADATA absolute path is required")
	}
	var metadata struct {
		Source               struct{ ImageID, HelperSHA256 string } `json:"source"`
		DockerContext        string                                 `json:"dockerContext"`
		DockerEndpointDigest string                                 `json:"dockerEndpointDigest"`
		Capability           CapabilityProbeContractRecord          `json:"capability"`
		Qualification        EngineQualificationRecord              `json:"qualification"`
	}
	body, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &metadata); err != nil {
		t.Fatal(err)
	}
	contract, err := RestoreCapabilityProbeContract(metadata.Capability)
	if err != nil {
		t.Fatal(err)
	}
	qualification, err := RestoreEngineQualification(metadata.Qualification)
	if err != nil {
		t.Fatal(err)
	}
	configRoot := filepath.Join(filepath.Dir(metadataPath), "docker-authority")
	runner, err := NewDockerCommandRunnerWithConfigRoot(DockerCommandAuthority{ContextName: metadata.DockerContext, ContextEndpointDigest: metadata.DockerEndpointDigest}, configRoot)
	if err != nil {
		t.Fatal(err)
	}
	runtimeRoot := filepath.Join(os.Getenv("TMPDIR"), "chora-supervisor-real-"+strings.ReplaceAll(t.Name(), "/", "-"))
	if err := os.MkdirAll(runtimeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeRoot) })
	credential := filepath.Join(runtimeRoot, "auth.json")
	auth := `{"deepseek":{"type":"api_key","key":"synthetic-deepseek-acceptance-key"}}`
	if err := os.WriteFile(credential, []byte(auth), 0o600); err != nil {
		t.Fatal(err)
	}
	observerSHA := agentpi.ResourceObserverSHA256()
	collectCalls := 0
	config := Config{Runner: runner, RuntimeRoot: filepath.Join(runtimeRoot, "runtime"), ArtifactRoot: filepath.Join(runtimeRoot, "artifacts"), PolicyDigest: WorkbenchPolicyDigest,
		AttemptImageID: metadata.Source.ImageID, RuntimeSourceIdentity: strings.Repeat("a", 64), CredentialSource: credential,
		CapabilityContract: contract, EngineQualification: qualification, CooperativeWait: 50 * time.Millisecond, DeathWait: 10 * time.Second}
	config.Workbench = &WorkbenchConfig{Arguments: workbenchArguments(), RuntimeVersion: "0.85.1", ObserverSHA256: observerSHA, HelperSHA256: metadata.Source.HelperSHA256, RuntimeFingerprint: strings.Repeat("d", 64),
		PrepareWorkspace: func(_ context.Context, _ execution.Invocation, repository string) error {
			repo := filepath.Join(repository, "repo")
			if err := os.MkdirAll(repo, 0o700); err != nil {
				return err
			}
			if output, err := exec.Command("git", "init", "--quiet", repo).CombinedOutput(); err != nil {
				return fmt.Errorf("git init: %v: %s", err, output)
			}
			if err := os.WriteFile(filepath.Join(repo, "ordinary.txt"), []byte("ordinary\n"), 0o644); err != nil {
				return err
			}
			for _, args := range [][]string{{"-C", repo, "config", "user.name", "Chora Acceptance"}, {"-C", repo, "config", "user.email", "acceptance@example.invalid"}, {"-C", repo, "add", "ordinary.txt"}, {"-C", repo, "commit", "--quiet", "-m", "fixture"}} {
				if output, err := exec.Command("git", args...).CombinedOutput(); err != nil {
					return fmt.Errorf("git fixture %v: %v: %s", args, err, output)
				}
			}
			if err := os.MkdirAll(filepath.Join(repository, "reference"), 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(repository, "reference", "readonly.txt"), []byte("reference\n"), 0o600); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Join(repository, "reference", ".git"), 0o700); err != nil {
				return err
			}
			contextDir := filepath.Join(filepath.Dir(filepath.Dir(repository)), "context")
			return os.WriteFile(filepath.Join(contextDir, "observer.json"), []byte("{}\n"), 0o600)
		}, CollectWorkspace: func(context.Context, execution.Invocation, string) error {
			collectCalls++
			return errors.New("cancel must not collect")
		},
		ReadOnlyWorkspacePaths: func(execution.Invocation) ([]string, error) {
			return []string{"repo/.git", "reference", "reference/.git"}, nil
		}}
	supervisor, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	invocation := testWorkbenchInvocation(t, testSource(t), "real-workbench-cancel", config)
	outcome := supervisor.Start(context.Background(), invocation, &testSink{binding: invocation.LaunchToken()})
	if outcome.Kind != execution.Started {
		t.Fatalf("Start = %#v", outcome)
	}
	t.Cleanup(func() {
		_, _ = supervisor.Stop(context.Background(), outcome.Handle, execution.StopIntent{Kind: execution.StopForCancel, Reason: "acceptance cleanup"})
		_, _ = supervisor.Drain(context.Background(), outcome.Handle, execution.StreamOffsets{}, persistedLogBytes*2)
		_ = supervisor.Finalize(context.Background(), outcome.Handle, execution.RetentionPolicy{})
	})
	record := supervisor.record(outcome.Handle)
	effective, err := supervisor.inspectEffectiveContainer(context.Background(), record.container)
	if err != nil {
		t.Fatal(err)
	}
	workspaceVolumeMounted := false
	for _, mount := range effective.Mounts {
		workspaceVolumeMounted = workspaceVolumeMounted || mount.Type == "volume" && mount.Name == record.workspaceVolume && mount.Destination == "/workspace" && mount.RW
	}
	if effective.Config.User != "1000:1000" || effective.HostConfig.NetworkMode != "bridge" || !effective.HostConfig.ReadonlyRootfs ||
		effective.HostConfig.NanoCPUs != 2_000_000_000 || effective.HostConfig.Memory != 4096<<20 || effective.HostConfig.MemorySwap != 4096<<20 || effective.HostConfig.PidsLimit != 256 ||
		!slices.Equal(effective.HostConfig.CapDrop, []string{"ALL"}) || len(effective.HostConfig.CapAdd) != 0 || !slices.Equal(effective.HostConfig.SecurityOpt, []string{"no-new-privileges:true"}) ||
		!workspaceVolumeMounted {
		t.Fatalf("effective limits drift: %#v", effective.HostConfig)
	}
	for _, mount := range effective.Mounts {
		if mount.Type == "volume" && mount.Name == record.workspaceVolume && mount.Destination == "/workspace" && mount.RW {
			continue
		}
		if mount.Type != "bind" || mount.RW || !pathWithin(record.root, mount.Source) {
			t.Fatalf("unsafe mount: %#v", mount)
		}
	}
	positive := func(script string) []byte {
		result, err := runner.Run(context.Background(), Command{Args: []string{"exec", record.container, "/bin/sh", "-c", script}})
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("command failed: %s: %#v %v", script, result, err)
		}
		return result.Stdout
	}
	if uid := strings.TrimSpace(string(positive("stat -c %u /workspace/repository/repo/ordinary.txt"))); uid != "1000" {
		t.Fatalf("ordinary file uid=%q", uid)
	}
	positive("echo changed >> /workspace/repository/repo/ordinary.txt")
	positive("touch /workspace/repository/repo/created.txt && mv /workspace/repository/repo/created.txt /workspace/repository/repo/renamed.txt && rm /workspace/repository/repo/renamed.txt")
	if status := string(positive("git -C /workspace/repository/repo status --porcelain")); !strings.Contains(status, "ordinary.txt") {
		t.Fatalf("git status=%q", status)
	}
	negative := func(script string) {
		result, err := runner.Run(context.Background(), Command{Args: []string{"exec", record.container, "/bin/sh", "-c", script}})
		if err == nil && result.ExitCode == 0 {
			t.Fatalf("write unexpectedly succeeded: %s", script)
		}
	}
	negative("touch /rootfs-write")
	negative("echo x > /workspace/repository/repo/.git/HEAD")
	negative("echo x > /workspace/repository/reference/readonly.txt")
	positive(`node -e 'const f=require("fs"),p="/run/chora/pi/auth.json",v=JSON.parse(f.readFileSync(p));if(Object.keys(v).length!==1||v.deepseek.type!=="api_key"||v.deepseek.key!=="synthetic-deepseek-acceptance-key"||(f.statSync(p).mode&511)!==384)process.exit(1)'`)
	stop, err := supervisor.Stop(context.Background(), outcome.Handle, execution.StopIntent{Kind: execution.StopForCancel, Reason: "acceptance cancel"})
	if err != nil || stop.Kind != execution.StopConfirmed {
		t.Fatalf("Stop = %#v, %v", stop, err)
	}
	drainAll(t, supervisor, outcome.Handle)
	if err := supervisor.Finalize(context.Background(), outcome.Handle, execution.RetentionPolicy{}); err != nil {
		t.Fatal(err)
	}
	if collectCalls != 0 {
		t.Fatalf("cancel imported workspace: CollectWorkspace calls=%d", collectCalls)
	}
	if result, inspectErr := runner.Run(context.Background(), Command{Args: []string{"inspect", record.container}}); inspectErr == nil && result.ExitCode == 0 {
		t.Fatalf("container and projected credential remain after cancel: %#v", result)
	}
	restarted, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(config.RuntimeRoot)
	if err != nil || len(entries) != 0 {
		t.Fatalf("restart Recover retained runtime roots: entries=%v error=%v", entries, err)
	}
	filters := restarted.ownedResourceFilters("")
	residue, err := restarted.inventoryResources(context.Background(), filters)
	if err != nil || len(residue.containers) != 0 || len(residue.networks) != 0 {
		t.Fatalf("restart Recover retained Docker resources: %#v error=%v", residue, err)
	}
}

func TestRealWorkbenchDockerExportAndPermissionRestore(t *testing.T) {
	metadataPath := strings.TrimSpace(os.Getenv("CHORA_ISOLATED_METADATA"))
	if metadataPath == "" || !filepath.IsAbs(metadataPath) {
		t.Fatal("CHORA_ISOLATED_METADATA absolute path is required")
	}
	var metadata struct {
		Source               struct{ ImageID string } `json:"source"`
		DockerContext        string                   `json:"dockerContext"`
		DockerEndpointDigest string                   `json:"dockerEndpointDigest"`
	}
	body, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &metadata); err != nil {
		t.Fatal(err)
	}
	runner, err := NewDockerCommandRunnerWithConfigRoot(
		DockerCommandAuthority{ContextName: metadata.DockerContext, ContextEndpointDigest: metadata.DockerEndpointDigest},
		filepath.Join(filepath.Dir(metadataPath), "docker-authority"),
	)
	if err != nil {
		t.Fatal(err)
	}
	container := "chora-workbench-export-" + strconv.Itoa(os.Getpid())
	volume := container + "-workspace"
	runOK := func(args ...string) {
		t.Helper()
		result, runErr := runner.Run(context.Background(), Command{Args: args})
		if runErr != nil || result.ExitCode != 0 {
			t.Fatalf("docker %v: %#v %v", args, result, runErr)
		}
	}
	_, _ = runner.Run(context.Background(), Command{Args: []string{"rm", "-f", container}})
	_, _ = runner.Run(context.Background(), Command{Args: []string{"volume", "rm", volume}})
	t.Cleanup(func() {
		_, _ = runner.Run(context.Background(), Command{Args: []string{"rm", "-f", container}})
		_, _ = runner.Run(context.Background(), Command{Args: []string{"volume", "rm", volume}})
	})
	runOK("volume", "create", "--driver", "local", "--opt", "type=tmpfs", "--opt", "device=tmpfs", "--opt", "o=size=16m,uid=1000,gid=1000,nosuid,nodev", volume)
	runOK("create", "--pull=never", "--name", container, "--network", "none", "--read-only", "--cap-drop", "ALL",
		"--mount", "type=volume,src="+volume+",dst=/workspace", "--entrypoint", "/bin/sh", metadata.Source.ImageID, "-c", "exec sleep infinity")
	runOK("start", container)
	runOK("exec", "--user", "1000:1000", container, "/bin/sh", "-c",
		"mkdir -p /workspace/repository/reference/.git; printf 'changed\\n' > /workspace/repository/ordinary.txt; printf 'reference\\n' > /workspace/repository/reference/readonly.txt")
	probe, probeErr := runner.Run(context.Background(), Command{Args: []string{"exec", container, "stat", "-c", "%F", "/workspace/repository"}})
	if probeErr != nil || probe.ExitCode != 0 || strings.TrimSpace(string(probe.Stdout)) != "directory" {
		t.Fatalf("export source missing before docker cp: %#v %v", probe, probeErr)
	}
	destination := t.TempDir()
	mounts := []workbenchReadOnlyMount{{destination: "/workspace/repository/reference", originalModes: map[string]os.FileMode{
		".": 0o750, ".git": 0o700, "readonly.txt": 0o640,
	}}}
	supervisor := &Supervisor{config: Config{Runner: runner}}
	if err := supervisor.exportWorkbenchArchive(context.Background(), container, destination, mounts); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(destination, "ordinary.txt"))
	if err != nil || string(content) != "changed\n" {
		t.Fatalf("exported ordinary file = %q, %v", content, err)
	}
	for relative, want := range mounts[0].originalModes {
		info, statErr := os.Stat(filepath.Join(destination, "reference", relative))
		if statErr != nil || info.Mode().Perm() != want.Perm() {
			t.Fatalf("restored mode %q = %v, %v; want %v", relative, info, statErr, want.Perm())
		}
	}
}

func TestRealWorkbenchWorkspaceVolumeQualification(t *testing.T) {
	metadataPath := strings.TrimSpace(os.Getenv("CHORA_ISOLATED_METADATA"))
	if metadataPath == "" || !filepath.IsAbs(metadataPath) {
		t.Fatal("CHORA_ISOLATED_METADATA absolute path is required")
	}
	var metadata struct {
		Source               struct{ ImageID string } `json:"source"`
		DockerContext        string                   `json:"dockerContext"`
		DockerEndpointDigest string                   `json:"dockerEndpointDigest"`
	}
	body, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &metadata); err != nil {
		t.Fatal(err)
	}
	runner, err := NewDockerCommandRunnerWithConfigRoot(
		DockerCommandAuthority{ContextName: metadata.DockerContext, ContextEndpointDigest: metadata.DockerEndpointDigest},
		filepath.Join(filepath.Dir(metadataPath), "docker-authority"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyWorkbenchWorkspaceVolume(context.Background(), runner, metadata.Source.ImageID, t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

func TestRealWorkbenchRejectsWrongExistingWorkspaceVolume(t *testing.T) {
	metadataPath := strings.TrimSpace(os.Getenv("CHORA_ISOLATED_METADATA"))
	if metadataPath == "" || !filepath.IsAbs(metadataPath) {
		t.Fatal("CHORA_ISOLATED_METADATA absolute path is required")
	}
	var metadata struct {
		DockerContext        string `json:"dockerContext"`
		DockerEndpointDigest string `json:"dockerEndpointDigest"`
	}
	body, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &metadata); err != nil {
		t.Fatal(err)
	}
	runner, err := NewDockerCommandRunnerWithConfigRoot(
		DockerCommandAuthority{ContextName: metadata.DockerContext, ContextEndpointDigest: metadata.DockerEndpointDigest},
		filepath.Join(filepath.Dir(metadataPath), "docker-authority"),
	)
	if err != nil {
		t.Fatal(err)
	}
	volume := "chora-" + strings.Repeat("a", 24) + "-workspace"
	labels := map[string]string{"chora.owner": "dockersupervisor", "chora.runtime_scope": "scope", "chora.run_id": "run", "chora.attempt_id": "attempt", "chora.task_id": "task", "chora.image_digest": "image", "chora.policy_digest": "policy"}
	_, _ = runner.Run(context.Background(), Command{Args: []string{"volume", "rm", volume}})
	t.Cleanup(func() { _, _ = runner.Run(context.Background(), Command{Args: []string{"volume", "rm", volume}}) })
	args := []string{"volume", "create", "--driver", "local", "--opt", "type=tmpfs", "--opt", "device=tmpfs", "--opt", "o=size=1m,uid=1000,gid=1000,nosuid,nodev"}
	for _, key := range []string{"chora.owner", "chora.runtime_scope", "chora.run_id", "chora.attempt_id", "chora.task_id", "chora.image_digest", "chora.policy_digest"} {
		args = append(args, "--label", key+"="+labels[key])
	}
	args = append(args, volume)
	result, err := runner.Run(context.Background(), Command{Args: args})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("create wrong-options fixture: %#v %v", result, err)
	}
	supervisor := &Supervisor{config: Config{Runner: runner}}
	if err := supervisor.verifyWorkbenchWorkspaceVolume(context.Background(), &attemptRecord{workspaceVolume: volume}, labels); err == nil {
		t.Fatal("accepted real pre-existing workspace volume with wrong options")
	}
}
