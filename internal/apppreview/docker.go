package apppreview

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Yangyang96/chora/internal/dockersupervisor"
)

const dockerOwnerLabel = "chora.owner=apppreview"

func (manager *Manager) isolatedRuntime(ctx context.Context) (IsolatedRuntime, error) {
	if manager.resolve == nil {
		return IsolatedRuntime{}, errors.New("isolated app preview runtime is unavailable")
	}
	runtime, err := manager.resolve(ctx)
	if err != nil {
		return IsolatedRuntime{}, fmt.Errorf("resolve qualified isolated app preview runtime: %w", err)
	}
	if runtime.Runner == nil || !imagePattern.MatchString(runtime.ImageID) || !validDigest(runtime.EngineIdentity) {
		return IsolatedRuntime{}, errors.New("qualified isolated app preview runtime is invalid")
	}
	return runtime, nil
}

func (manager *Manager) startIsolated(ctx context.Context, record manifest) (View, error) {
	runtime, err := manager.isolatedRuntime(ctx)
	if err != nil {
		return manager.failStart(record, err)
	}
	snapshot := filepath.Join(manager.directory(record.View.Key), "source")
	if strings.Contains(snapshot, ",") {
		return manager.failStart(record, errors.New("isolated preview runtime path cannot be represented safely"))
	}
	if err := os.RemoveAll(snapshot); err != nil {
		return manager.failStart(record, err)
	}
	if err := copySnapshot(record.View.Target.Root, snapshot); err != nil {
		return manager.failStart(record, fmt.Errorf("copy isolated preview source: %w", err))
	}
	record.SourceSnapshot = snapshot
	keyHash := fmt.Sprintf("%x", sha256Bytes([]byte(record.View.Key)))[:16]
	name := "chora-preview-" + keyHash + "-" + record.Nonce[:12]
	record.ContainerName = name
	record.ImageID, record.EngineIdentity = runtime.ImageID, runtime.EngineIdentity
	record.View.CleanupRequired = true
	if err := manager.persist(record); err != nil {
		return View{}, err
	}
	working := record.View.Config.WorkingDirectory
	if working == "" {
		working = "."
	}
	args := []string{
		"run", "--detach", "--pull=never", "--name", name,
		"--label", dockerOwnerLabel, "--label", "chora.preview.key=" + keyHash, "--label", "chora.preview.nonce=" + record.Nonce,
		"--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--pids-limit=256", "--memory=1g", "--cpus=2",
		"--user", "1000:1000", "--publish", "127.0.0.1::" + strconv.Itoa(record.View.Config.Port),
		"--mount", "type=bind,src=" + snapshot + ",dst=/source,readonly",
		"--tmpfs", "/workspace:rw,nosuid,nodev,mode=1777,size=2g", "--tmpfs", "/tmp:rw,nosuid,nodev,noexec,mode=1777,size=256m",
		"--env", "HOST=0.0.0.0", "--env", "PORT=" + strconv.Itoa(record.View.Config.Port),
		"--entrypoint", "/bin/sh", runtime.ImageID, "-lc",
		`cp -R /source/. /workspace && cd "/workspace/$1" && exec /bin/sh -lc "$2"`, "preview", working, record.View.Config.Command,
	}
	result, runErr := runtime.Runner.Run(ctx, dockersupervisor.Command{Args: args})
	containerID := strings.TrimSpace(string(result.Stdout))
	if runErr != nil || result.ExitCode != 0 || !validContainerID(containerID) {
		if observed, inspectErr := inspectContainer(ctx, runtime.Runner, name); inspectErr == nil && observed.owned(record) {
			record.ContainerID = observed.id
		}
		record = requireRecovery(record, fmt.Sprintf("isolated app preview start result is uncertain: %v: %s", runErr, boundedText(result.Stderr)))
		if err := manager.persist(record); err != nil {
			return View{}, errors.Join(runErr, err)
		}
		return record.View, errors.New(record.View.Reason)
	}
	record.ContainerID = containerID
	if err := manager.persist(record); err != nil {
		return View{}, err
	}
	portResult, portErr := runtime.Runner.Run(ctx, dockersupervisor.Command{Args: []string{"port", containerID, strconv.Itoa(record.View.Config.Port) + "/tcp"}})
	hostPort, parseErr := parsePublishedPort(portResult.Stdout)
	if portErr != nil || portResult.ExitCode != 0 || parseErr != nil {
		record = requireRecovery(record, "isolated app preview port result is uncertain")
		if err := manager.persist(record); err != nil {
			return View{}, err
		}
		return record.View, errors.New(record.View.Reason)
	}
	record.View.State = StateRunning
	record.View.CleanupRequired = false
	record.View.URL = "http://127.0.0.1:" + strconv.Itoa(hostPort)
	if err := manager.persist(record); err != nil {
		_, _ = runtime.Runner.Run(context.Background(), dockersupervisor.Command{Args: []string{"rm", "--force", containerID}})
		return View{}, err
	}
	return manager.withLogSize(record.View), nil
}

func (manager *Manager) inspectIsolated(ctx context.Context, record manifest) manifest {
	runtime, err := manager.isolatedRuntime(ctx)
	if err != nil || runtime.ImageID != record.ImageID || runtime.EngineIdentity != record.EngineIdentity {
		return requireRecovery(record, "isolated preview runtime authority or image identity drifted")
	}
	identifier := record.ContainerID
	if identifier == "" {
		identifier = record.ContainerName
	}
	observation, err := inspectContainer(ctx, runtime.Runner, identifier)
	if err != nil {
		return requireRecovery(record, "isolated preview container ownership cannot be verified")
	}
	if !observation.owned(record) {
		return requireRecovery(record, "isolated preview container identity does not match durable ownership")
	}
	record.ContainerID = observation.id
	manager.syncDockerLogs(ctx, runtime.Runner, &record)
	if observation.running {
		record.View.State = StateRunning
		return record
	}
	record.View.ExitCode = &observation.exitCode
	// A stopped container is still owned residue. Preserve its identity until
	// Stop or Cleanup verifies and removes that exact container.
	record.View.CleanupRequired = true
	if observation.exitCode == 0 {
		record.View.State = StateStopped
		record.View.Reason = ""
	} else {
		record.View.State = StateFailed
		record.View.Reason = fmt.Sprintf("preview command exited with status %d", observation.exitCode)
	}
	return record
}

func (manager *Manager) stopIsolated(ctx context.Context, record *manifest) error {
	runtime, err := manager.isolatedRuntime(ctx)
	if err != nil || runtime.ImageID != record.ImageID || runtime.EngineIdentity != record.EngineIdentity {
		return errors.New("isolated preview runtime authority or image identity drifted")
	}
	identifier := record.ContainerID
	if identifier == "" {
		identifier = record.ContainerName
	}
	observation, err := inspectContainer(ctx, runtime.Runner, identifier)
	if errors.Is(err, os.ErrNotExist) {
		record.View.State, record.View.CleanupRequired, record.View.Reason, record.View.URL = StateStopped, false, "", ""
		return nil
	}
	if err != nil {
		return errors.New("isolated preview container ownership cannot be verified")
	}
	if !observation.owned(*record) {
		return errors.New("isolated preview container identity does not match durable ownership")
	}
	record.ContainerID = observation.id
	manager.syncDockerLogs(ctx, runtime.Runner, record)
	if observation.running {
		result, stopErr := runtime.Runner.Run(ctx, dockersupervisor.Command{Args: []string{"stop", "--time", "2", record.ContainerID}})
		if stopErr != nil || result.ExitCode != 0 {
			return fmt.Errorf("stop isolated preview container: %w", stopErr)
		}
	}
	result, removeErr := runtime.Runner.Run(ctx, dockersupervisor.Command{Args: []string{"rm", record.ContainerID}})
	if removeErr != nil || result.ExitCode != 0 {
		return fmt.Errorf("remove isolated preview container: %w", removeErr)
	}
	record.View.State, record.View.CleanupRequired, record.View.Reason, record.View.URL = StateStopped, false, "", ""
	return nil
}

type containerObservation struct {
	id, owner, key, nonce, image string
	running                      bool
	exitCode                     int
}

func (observation containerObservation) owned(record manifest) bool {
	keyHash := fmt.Sprintf("%x", sha256Bytes([]byte(record.View.Key)))[:16]
	return observation.owner == "apppreview" && observation.key == keyHash && observation.nonce == record.Nonce && observation.image == record.ImageID && (record.ContainerID == "" || observation.id == record.ContainerID)
}

func inspectContainer(ctx context.Context, runner dockersupervisor.CommandRunner, id string) (containerObservation, error) {
	if !validContainerID(id) && !containerNamePattern.MatchString(id) {
		return containerObservation{}, errors.New("invalid container identity")
	}
	format := `{{.Id}}|{{index .Config.Labels "chora.owner"}}|{{index .Config.Labels "chora.preview.key"}}|{{index .Config.Labels "chora.preview.nonce"}}|{{.Image}}|{{.State.Running}}|{{.State.ExitCode}}`
	result, err := runner.Run(ctx, dockersupervisor.Command{Args: []string{"inspect", "--format", format, id}})
	if err == nil && result.ExitCode != 0 {
		return containerObservation{}, os.ErrNotExist
	}
	if err != nil {
		return containerObservation{}, errors.New("inspect container")
	}
	parts := strings.Split(strings.TrimSpace(string(result.Stdout)), "|")
	if len(parts) != 7 || !validContainerID(parts[0]) {
		return containerObservation{}, errors.New("invalid container observation")
	}
	exitCode, err := strconv.Atoi(parts[6])
	if err != nil || parts[5] != "true" && parts[5] != "false" {
		return containerObservation{}, errors.New("invalid container state")
	}
	return containerObservation{id: parts[0], owner: parts[1], key: parts[2], nonce: parts[3], image: parts[4], running: parts[5] == "true", exitCode: exitCode}, nil
}

func (manager *Manager) syncDockerLogs(ctx context.Context, runner dockersupervisor.CommandRunner, record *manifest) {
	result, err := runner.Run(ctx, dockersupervisor.Command{Args: []string{"logs", "--tail", "10000", record.ContainerID}})
	if err != nil && len(result.Stdout) == 0 && len(result.Stderr) == 0 {
		return
	}
	data := append(append([]byte(nil), result.Stdout...), result.Stderr...)
	if int64(len(data)) > manager.logCap {
		data = data[len(data)-int(manager.logCap):]
		record.View.LogTruncated = true
	}
	if writeErr := os.WriteFile(manager.logPath(record.View.Key), data, 0o600); writeErr != nil {
		record.View.Reason = "persist isolated preview logs: " + writeErr.Error()
	}
}

func copySnapshot(source, destination string) error {
	if err := os.Mkdir(destination, 0o755); err != nil {
		return err
	}
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			if relative == "." {
				return nil
			}
			return os.Mkdir(target, 0o755)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("isolated preview source contains an unsupported entry")
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm()|0o444)
		if err == nil {
			_, err = output.ReadFrom(input)
		}
		err = errors.Join(err, input.Close())
		if output != nil {
			err = errors.Join(err, output.Close())
		}
		return err
	})
}

func parsePublishedPort(data []byte) (int, error) {
	line := strings.TrimSpace(string(data))
	if strings.Contains(line, "\n") || !strings.HasPrefix(line, "127.0.0.1:") {
		return 0, errors.New("published preview port is not loopback-only")
	}
	port, err := strconv.Atoi(strings.TrimPrefix(line, "127.0.0.1:"))
	if err != nil || port < 1 || port > 65535 {
		return 0, errors.New("published preview port is invalid")
	}
	return port, nil
}

func requireRecovery(record manifest, reason string) manifest {
	record.View.State, record.View.CleanupRequired, record.View.Reason = StateRecoveryRequired, true, reason
	return record
}

func validContainerID(value string) bool {
	if len(value) < 12 || len(value) > 64 {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' && char < 'a' || char > 'f' {
			return false
		}
	}
	return true
}

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' && char < 'a' || char > 'f' {
			return false
		}
	}
	return true
}

func sha256Bytes(data []byte) [32]byte {
	return sha256.Sum256(data)
}

func boundedText(data []byte) string {
	if len(data) > 4096 {
		data = data[:4096]
	}
	return strings.TrimSpace(string(data))
}
