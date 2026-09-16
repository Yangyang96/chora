package apppreview

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/dockersupervisor"
)

func TestHostLifecycleLogsRestartAndPortOwnership(t *testing.T) {
	manager, root := testManager(t, 1024)
	port := 43191
	target := Target{Key: "task-repo", Root: root, AttemptID: "attempt-1", Profile: ProfileLocalConnected}
	config := Config{Command: `printf 'ready\n'; while :; do sleep 1; done`, WorkingDirectory: ".", Port: port}
	view, err := manager.Start(context.Background(), target, config)
	if err != nil || view.State != StateRunning || view.URL != "http://127.0.0.1:"+strconv.Itoa(port) {
		t.Fatalf("Start() = %#v, %v", view, err)
	}
	waitForLog(t, manager, target.Key, "ready")
	view, err = manager.Stop(context.Background(), target.Key)
	if err != nil || view.State != StateStopped || view.URL != "" {
		t.Fatalf("Stop() = %#v, %v", view, err)
	}
	time.Sleep(100 * time.Millisecond)
	view, err = manager.Inspect(context.Background(), target.Key)
	if err != nil || view.State != StateStopped {
		t.Fatalf("Inspect() after stop = %#v, %v", view, err)
	}
	if _, err := manager.Start(context.Background(), target, config); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if _, err := manager.Stop(context.Background(), target.Key); err != nil {
		t.Fatalf("stop restart: %v", err)
	}

	manager.checkPort = func(int) error { return errors.New("occupied") }
	config.Port = 43192
	view, err = manager.Start(context.Background(), Target{Key: "occupied", Root: root, AttemptID: "attempt-1", Profile: ProfileLocalConnected}, config)
	if err == nil || view.State != StateFailed || view.URL != "" {
		t.Fatalf("occupied Start() = %#v, %v", view, err)
	}
}

func TestHostBoundedLogAndForeignOwnership(t *testing.T) {
	manager, root := testManager(t, 1024)
	target := Target{Key: "bounded", Root: root, AttemptID: "attempt", Profile: ProfileLocalConnected}
	config := Config{Command: `i=0; while [ $i -lt 2000 ]; do printf x; i=$((i+1)); done`, WorkingDirectory: ".", Port: 43193}
	if _, err := manager.Start(context.Background(), target, config); err != nil {
		t.Fatal(err)
	}
	var view View
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		view, _ = manager.Inspect(context.Background(), target.Key)
		if view.State != StateRunning {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	logs, err := manager.Logs(target.Key, 0, 1024)
	if err != nil || logs.Size != 1024 || !logs.Truncated {
		t.Fatalf("Logs() = %#v, %v, view=%#v", logs, err, view)
	}

	config.Command = `while :; do sleep 1; done`
	target.Key = "foreign"
	if _, err := manager.Start(context.Background(), target, config); err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	record, _ := manager.load(target.Key)
	record.Birth = "foreign-birth"
	_ = manager.persist(record)
	manager.mu.Unlock()
	view, err = manager.Stop(context.Background(), target.Key)
	if err == nil || view.State != StateRecoveryRequired || !view.CleanupRequired {
		t.Fatalf("foreign Stop() = %#v, %v", view, err)
	}
	// Test owns the real child and must not leak it after proving the test PID.
	_ = stopGroup(record.PGID, time.Second)
}

func TestConfigurationTraversalAndDiscovery(t *testing.T) {
	manager, root := testManager(t, 1024)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	target := Target{Key: "config", Root: root, AttemptID: "attempt", Profile: ProfileLocalConnected}
	for _, directory := range []string{"../outside", "escape"} {
		if _, err := manager.Save(context.Background(), target, Config{Command: "npm run dev", WorkingDirectory: directory, Port: 5173}); err == nil {
			t.Fatalf("Save accepted working directory %q", directory)
		}
	}
	packageJSON := []byte(`{"scripts":{"dev":"vite --port 4173","start":"node server.js"}}`)
	if err := os.WriteFile(filepath.Join(root, "package.json"), packageJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	suggestions, err := Discover(root)
	if err != nil || len(suggestions) != 2 || suggestions[0].Config.Command != "npm run dev" || suggestions[0].Config.Port != 4173 || suggestions[1].Config.Port != 3000 {
		t.Fatalf("Discover() = %#v, %v", suggestions, err)
	}
}

func TestIsolatedDockerPolicyAndOwnership(t *testing.T) {
	root := t.TempDir()
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	runner := &fakeDockerRunner{image: "sha256:" + strings.Repeat("a", 64), id: strings.Repeat("b", 64), running: true}
	manager, err := New(Options{RuntimeRoot: runtimeRoot, LogCapacity: 1024, ResolveIsolated: func(context.Context) (IsolatedRuntime, error) {
		return IsolatedRuntime{Runner: runner, ImageID: runner.image, EngineIdentity: strings.Repeat("c", 64)}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	target := Target{Key: "isolated", Root: root, AttemptID: "attempt", Profile: ProfileIsolatedLocal}
	view, err := manager.Start(context.Background(), target, Config{Command: "npm run dev", WorkingDirectory: ".", Port: 5173})
	if err != nil || view.State != StateRunning || view.URL != "http://127.0.0.1:49152" {
		t.Fatalf("Start() = %#v, %v", view, err)
	}
	run := runner.command("run")
	required := []string{"--pull=never", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--pids-limit=256", "--memory=1g", "--cpus=2", "--user", "1000:1000", "--publish", "127.0.0.1::5173"}
	for _, argument := range required {
		if !slices.Contains(run, argument) {
			t.Errorf("docker run lacks %q: %q", argument, run)
		}
	}
	mount := run[slices.Index(run, "--mount")+1]
	if !strings.Contains(mount, filepath.Join(runtimeRoot, manager.directory(target.Key)[len(runtimeRoot)+1:], "source")) || !strings.HasSuffix(mount, ",readonly") || strings.Contains(mount, root) {
		t.Fatalf("unsafe source mount: %q", mount)
	}
	if strings.Contains(strings.Join(run, " "), "TOKEN") || strings.Contains(strings.Join(run, " "), "credential") {
		t.Fatalf("docker args contain credentials: %q", run)
	}
	// Recovery may begin with only the pre-run name if Docker's successful run
	// response was lost. Inspect must adopt the verified exact container ID.
	manager.mu.Lock()
	record, _ := manager.load(target.Key)
	record.ContainerID = ""
	_ = manager.persist(record)
	manager.mu.Unlock()
	if _, err := manager.Inspect(context.Background(), target.Key); err != nil {
		t.Fatalf("Inspect name-owned container: %v", err)
	}
	manager.mu.Lock()
	record, _ = manager.load(target.Key)
	manager.mu.Unlock()
	if record.ContainerID != runner.id {
		t.Fatalf("recovered container ID = %q", record.ContainerID)
	}
	view, err = manager.Stop(context.Background(), target.Key)
	if err != nil || view.State != StateStopped {
		t.Fatalf("Stop() = %#v, %v", view, err)
	}

	natural := Target{Key: "isolated-natural", Root: root, AttemptID: "attempt", Profile: ProfileIsolatedLocal}
	runner.running = true
	if _, err := manager.Start(context.Background(), natural, Config{Command: "npm run dev", WorkingDirectory: ".", Port: 5173}); err != nil {
		t.Fatal(err)
	}
	runner.running = false
	view, err = manager.Inspect(context.Background(), natural.Key)
	if err != nil || view.State != StateStopped || !view.CleanupRequired {
		t.Fatalf("natural Inspect() = %#v, %v", view, err)
	}
	view, err = manager.Cleanup(context.Background(), natural.Key)
	if err != nil || view.State != StateIdle || view.CleanupRequired {
		t.Fatalf("natural Cleanup() = %#v, %v", view, err)
	}
}

type fakeDockerRunner struct {
	mu                    sync.Mutex
	commands              [][]string
	image, id, nonce, key string
	running               bool
}

func (runner *fakeDockerRunner) Run(_ context.Context, command dockersupervisor.Command) (dockersupervisor.CommandResult, error) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	runner.commands = append(runner.commands, append([]string(nil), command.Args...))
	switch command.Args[0] {
	case "run":
		for index, value := range command.Args {
			if value == "--label" && index+1 < len(command.Args) {
				if strings.HasPrefix(command.Args[index+1], "chora.preview.nonce=") {
					runner.nonce = strings.TrimPrefix(command.Args[index+1], "chora.preview.nonce=")
				}
				if strings.HasPrefix(command.Args[index+1], "chora.preview.key=") {
					runner.key = strings.TrimPrefix(command.Args[index+1], "chora.preview.key=")
				}
			}
		}
		return dockersupervisor.CommandResult{Stdout: []byte(runner.id + "\n"), ExitCode: 0}, nil
	case "port":
		return dockersupervisor.CommandResult{Stdout: []byte("127.0.0.1:49152\n"), ExitCode: 0}, nil
	case "inspect":
		state := "false"
		if runner.running {
			state = "true"
		}
		output := strings.Join([]string{runner.id, "apppreview", runner.key, runner.nonce, runner.image, state, "0"}, "|")
		return dockersupervisor.CommandResult{Stdout: []byte(output + "\n"), ExitCode: 0}, nil
	case "logs":
		return dockersupervisor.CommandResult{Stdout: []byte("ready\n"), ExitCode: 0}, nil
	case "stop":
		runner.running = false
		return dockersupervisor.CommandResult{ExitCode: 0}, nil
	case "rm":
		return dockersupervisor.CommandResult{ExitCode: 0}, nil
	default:
		return dockersupervisor.CommandResult{ExitCode: 1}, errors.New("unexpected Docker command")
	}
}

func (runner *fakeDockerRunner) Start(context.Context, dockersupervisor.Command) (dockersupervisor.Process, error) {
	return nil, errors.New("unexpected Docker process")
}

func (runner *fakeDockerRunner) command(name string) []string {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	for _, command := range runner.commands {
		if command[0] == name {
			return command
		}
	}
	return nil
}

func testManager(t *testing.T, capacity int64) (*Manager, string) {
	t.Helper()
	root := t.TempDir()
	manager, err := New(Options{RuntimeRoot: filepath.Join(t.TempDir(), "runtime"), LogCapacity: capacity})
	if err != nil {
		t.Fatal(err)
	}
	manager.checkPort = func(int) error { return nil }
	return manager, root
}

func waitForLog(t *testing.T, manager *Manager, key, expected string) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		logs, _ := manager.Logs(key, 0, 1024)
		if strings.Contains(string(logs.Data), expected) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out waiting for preview log")
}
