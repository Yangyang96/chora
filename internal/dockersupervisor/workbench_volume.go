package dockersupervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// VerifyWorkbenchWorkspaceVolume proves that the selected Docker Engine can
// create, use, archive, and remove the bounded local-driver tmpfs volume used
// by public Workbench Attempts. It reads no credential source.
func VerifyWorkbenchWorkspaceVolume(ctx context.Context, runner CommandRunner, imageID, scopeRoot string) (returnErr error) {
	if runner == nil || !validImageID(imageID) || !filepath.IsAbs(scopeRoot) {
		return errors.New("invalid Workbench workspace volume probe config")
	}
	suffix, err := randomSuffix()
	if err != nil {
		return err
	}
	prefix := "chora-" + suffix + "-workspace-probe"
	container, volume := prefix+"-container", prefix+"-volume"
	scopeDigest := digestText([]byte(filepath.Clean(scopeRoot)))
	scope := "sha256:" + scopeDigest
	labels := []string{"--label", "chora.owner=dockersupervisor-workspace-probe", "--label", "chora.runtime_scope=" + scope}
	if err := reconcileWorkbenchWorkspaceProbes(runner, scope); err != nil {
		return err
	}
	run := func(args []string) error {
		result, runErr := runner.Run(ctx, Command{Args: args})
		if runErr != nil || result.ExitCode != 0 {
			return fmt.Errorf("docker %v: exit=%d error=%v stderr=%s", args, result.ExitCode, runErr, boundedDiagnostic(result.Stderr))
		}
		return nil
	}
	removed := false
	cleanup := func() error {
		if removed {
			return nil
		}
		cleanupContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		containerResult, containerErr := runner.Run(cleanupContext, Command{Args: []string{"rm", "-f", container}})
		if containerErr != nil || containerResult.ExitCode != 0 && !strings.Contains(strings.ToLower(string(containerResult.Stderr)), "no such container") {
			containerErr = fmt.Errorf("remove probe container: exit=%d error=%v stderr=%s", containerResult.ExitCode, containerErr, boundedDiagnostic(containerResult.Stderr))
		} else {
			containerErr = nil
		}
		volumeResult, volumeErr := runner.Run(cleanupContext, Command{Args: []string{"volume", "rm", volume}})
		if volumeErr != nil || volumeResult.ExitCode != 0 && !strings.Contains(strings.ToLower(string(volumeResult.Stderr)), "no such volume") {
			volumeErr = fmt.Errorf("remove probe volume: exit=%d error=%v stderr=%s", volumeResult.ExitCode, volumeErr, boundedDiagnostic(volumeResult.Stderr))
		} else {
			volumeErr = nil
		}
		cleanupErr := errors.Join(containerErr, volumeErr)
		if cleanupErr == nil {
			removed = true
		}
		return cleanupErr
	}
	defer func() { returnErr = errors.Join(returnErr, cleanup()) }()
	volumeArgs := []string{"volume", "create", "--driver", "local", "--opt", "type=tmpfs", "--opt", "device=tmpfs", "--opt", "o=size=1m,uid=1000,gid=1000,nosuid,nodev"}
	volumeArgs = append(volumeArgs, labels...)
	volumeArgs = append(volumeArgs, volume)
	if err := run(volumeArgs); err != nil {
		return err
	}
	volumeText, err := runnerText(ctx, runner, []string{"volume", "inspect", "--format", "{{json .}}", volume})
	var effective struct {
		Name    string            `json:"Name"`
		Driver  string            `json:"Driver"`
		Options map[string]string `json:"Options"`
	}
	if err != nil || json.Unmarshal([]byte(volumeText), &effective) != nil || effective.Name != volume || effective.Driver != "local" ||
		len(effective.Options) != 3 || effective.Options["type"] != "tmpfs" || effective.Options["device"] != "tmpfs" ||
		effective.Options["o"] != "size=1m,uid=1000,gid=1000,nosuid,nodev" {
		return fmt.Errorf("effective Workbench workspace volume options drift: %s", boundedDiagnostic([]byte(volumeText)))
	}
	create := []string{"create", "--pull=never", "--name", container}
	create = append(create, labels...)
	create = append(create, "--network", "none", "--user", "1000:1000", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true",
		"--mount", "type=volume,src="+volume+",dst=/workspace", "--entrypoint", "/bin/sh", imageID, "-c", "exec sleep infinity")
	if err := run(create); err != nil {
		return err
	}
	if err := run([]string{"start", container}); err != nil {
		return err
	}
	if err := run([]string{"exec", "--user", "1000:1000", container, "/bin/sh", "-c", "mkdir -p /workspace/repository && printf probe > /workspace/repository/probe.txt"}); err != nil {
		return err
	}
	if err := run([]string{"pause", container}); err != nil {
		return err
	}
	archive := boundedArchiveBuffer{limit: 2 << 20}
	var diagnostic bytes.Buffer
	process, err := runner.Start(ctx, Command{Args: []string{"cp", container + ":/workspace/repository/.", "-"}, Stdout: &archive, Stderr: &diagnostic})
	if err != nil || process == nil {
		return fmt.Errorf("start Workbench workspace export probe: %w", err)
	}
	_ = process.Close()
	exitCode, waitErr := process.Wait()
	if waitErr != nil || exitCode != 0 {
		return fmt.Errorf("Workbench workspace export probe exit=%d error=%v stderr=%s", exitCode, waitErr, boundedDiagnostic(diagnostic.Bytes()))
	}
	destination, err := os.MkdirTemp(scopeRoot, ".workbench-volume-probe-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(destination)
	if err := extractWorkbenchArchive(archive.Bytes(), destination); err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(destination, "probe.txt"))
	if err != nil || string(data) != "probe" {
		return errors.New("Workbench workspace volume probe export mismatch")
	}
	if err := cleanup(); err != nil {
		return err
	}
	containerResult, containerErr := runner.Run(ctx, Command{Args: []string{"ps", "-aq", "--filter", "name=^/(" + container + ")$"}})
	volumeResult, volumeErr := runner.Run(ctx, Command{Args: []string{"volume", "ls", "-q", "--filter", "name=^(" + volume + ")$"}})
	if containerErr != nil || volumeErr != nil || containerResult.ExitCode != 0 || volumeResult.ExitCode != 0 || len(bytes.TrimSpace(containerResult.Stdout)) != 0 || len(bytes.TrimSpace(volumeResult.Stdout)) != 0 {
		return errors.New("Workbench workspace volume probe residue remains")
	}
	return nil
}

func reconcileWorkbenchWorkspaceProbes(runner CommandRunner, scope string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	filters := []string{"label=chora.owner=dockersupervisor-workspace-probe", "label=chora.runtime_scope=" + scope}
	containerText, containerErr := runnerText(ctx, runner, appendDockerFilters([]string{"ps", "-aq"}, filters))
	volumeText, volumeErr := runnerText(ctx, runner, appendDockerFilters([]string{"volume", "ls", "-q"}, filters))
	if err := errors.Join(containerErr, volumeErr); err != nil {
		return fmt.Errorf("inventory Workbench workspace probe residue: %w", err)
	}
	containers, volumes := strings.Fields(containerText), strings.Fields(volumeText)
	for _, container := range containers {
		observation, err := observeProbeOwnership(ctx, runner, "container", container)
		if err != nil || !validWorkspaceProbeName(observation.Name, "-container") || observation.Labels["chora.owner"] != "dockersupervisor-workspace-probe" || observation.Labels["chora.runtime_scope"] != scope {
			return fmt.Errorf("Workbench workspace probe container ownership is unproven: %w", err)
		}
	}
	for _, volume := range volumes {
		observation, err := observeProbeOwnership(ctx, runner, "volume", volume)
		if err != nil || !validWorkspaceProbeName(observation.Name, "-volume") || observation.Labels["chora.owner"] != "dockersupervisor-workspace-probe" || observation.Labels["chora.runtime_scope"] != scope {
			return fmt.Errorf("Workbench workspace probe volume ownership is unproven: %w", err)
		}
	}
	for _, container := range containers {
		if result, err := runner.Run(ctx, Command{Args: []string{"rm", "-f", container}}); err != nil || result.ExitCode != 0 {
			return fmt.Errorf("remove prior Workbench workspace probe container: exit=%d error=%v", result.ExitCode, err)
		}
	}
	for _, volume := range volumes {
		if result, err := runner.Run(ctx, Command{Args: []string{"volume", "rm", volume}}); err != nil || result.ExitCode != 0 {
			return fmt.Errorf("remove prior Workbench workspace probe volume: exit=%d error=%v", result.ExitCode, err)
		}
	}
	return nil
}

func observeProbeOwnership(ctx context.Context, runner CommandRunner, kind, identity string) (resourceOwnershipObservation, error) {
	text, err := runnerText(ctx, runner, []string{kind, "inspect", "--format", networkOwnershipObservationFormat, identity})
	if err != nil {
		return resourceOwnershipObservation{}, err
	}
	var observation resourceOwnershipObservation
	if err := strictJSON([]byte(text), &observation); err != nil {
		return resourceOwnershipObservation{}, err
	}
	return observation, nil
}

func validWorkspaceProbeName(name, suffix string) bool {
	name = strings.TrimPrefix(name, "/")
	const prefix = "chora-"
	const middle = "-workspace-probe"
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, middle+suffix) {
		return false
	}
	hexPart := strings.TrimSuffix(strings.TrimPrefix(name, prefix), middle+suffix)
	return len(hexPart) == 24 && isLowerHex(hexPart)
}
