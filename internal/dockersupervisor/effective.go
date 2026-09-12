package dockersupervisor

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

type effectiveContainer struct {
	ID     string `json:"Id"`
	Name   string `json:"Name"`
	Image  string `json:"Image"`
	Config struct {
		User   string            `json:"User"`
		Env    []string          `json:"Env"`
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	HostConfig struct {
		NetworkMode    string `json:"NetworkMode"`
		ReadonlyRootfs bool   `json:"ReadonlyRootfs"`
		NanoCPUs       int64  `json:"NanoCpus"`
		Memory         int64  `json:"Memory"`
		MemorySwap     int64  `json:"MemorySwap"`
		PidsLimit      int64  `json:"PidsLimit"`
		Privileged     bool   `json:"Privileged"`
		PidMode        string `json:"PidMode"`
		IpcMode        string `json:"IpcMode"`
		LogConfig      struct {
			Type string `json:"Type"`
		} `json:"LogConfig"`
		CapAdd         []string                   `json:"CapAdd"`
		CapDrop        []string                   `json:"CapDrop"`
		SecurityOpt    []string                   `json:"SecurityOpt"`
		Devices        []json.RawMessage          `json:"Devices"`
		DeviceRequests []json.RawMessage          `json:"DeviceRequests"`
		PortBindings   map[string]json.RawMessage `json:"PortBindings"`
		Tmpfs          map[string]string          `json:"Tmpfs"`
		Ulimits        []struct {
			Name       string `json:"Name"`
			Soft, Hard int64
		} `json:"Ulimits"`
	} `json:"HostConfig"`
	Mounts []struct {
		Type, Name, Source, Destination string
		RW                              bool
	} `json:"Mounts"`
	NetworkSettings struct {
		Networks map[string]json.RawMessage `json:"Networks"`
	} `json:"NetworkSettings"`
}

type workbenchReadOnlyMount struct {
	source        string
	destination   string
	originalModes map[string]os.FileMode
}

func resolveReadOnlyWorkspacePaths(repository string, relativePaths []string) ([]workbenchReadOnlyMount, error) {
	if len(relativePaths) == 0 {
		return nil, errors.New("Workbench read-only workspace paths are empty")
	}
	root, err := filepath.EvalSymlinks(repository)
	if err != nil {
		return nil, fmt.Errorf("resolve Workbench repository: %w", err)
	}
	seen := make(map[string]struct{}, len(relativePaths))
	mounts := make([]workbenchReadOnlyMount, 0, len(relativePaths))
	for _, relative := range relativePaths {
		if relative == "" || filepath.IsAbs(relative) || filepath.Clean(relative) != relative || relative == "." || strings.Contains(relative, `\`) {
			return nil, fmt.Errorf("invalid Workbench read-only path %q", relative)
		}
		if _, exists := seen[relative]; exists {
			return nil, fmt.Errorf("duplicate Workbench read-only path %q", relative)
		}
		seen[relative] = struct{}{}
		source := filepath.Join(repository, filepath.FromSlash(relative))
		info, statErr := os.Lstat(source)
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("Workbench read-only path %q is missing or a symlink", relative)
		}
		resolved, evalErr := filepath.EvalSymlinks(source)
		if evalErr != nil || resolved != filepath.Join(root, filepath.FromSlash(relative)) || !pathWithin(root, resolved) {
			return nil, fmt.Errorf("Workbench read-only path %q escapes repository", relative)
		}
		modes := map[string]os.FileMode{}
		if err := filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			info, err := os.Lstat(path)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(source, path)
			if err != nil {
				return err
			}
			modes[rel] = info.Mode()
			return nil
		}); err != nil {
			return nil, fmt.Errorf("record Workbench mount modes: %w", err)
		}
		mounts = append(mounts, workbenchReadOnlyMount{source: source, destination: "/workspace/repository/" + filepath.ToSlash(relative), originalModes: modes})
	}
	return mounts, nil
}

func pathWithin(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func makeReadOnlyMountTreeReadable(root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		var mode os.FileMode
		switch {
		case info.IsDir():
			mode = 0o555
		case info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0:
			mode = 0o555
		case info.Mode().IsRegular():
			mode = 0o444
		default:
			return fmt.Errorf("Workbench read-only mount contains unsupported file %q", path)
		}
		if err := os.Chmod(path, mode); err != nil {
			return fmt.Errorf("make Workbench mount readable: %w", err)
		}
		return nil
	})
}

func (supervisor *Supervisor) verifyEffectiveContainers(ctx context.Context, record *attemptRecord, metadata invocationMetadata) error {
	agent, err := supervisor.inspectEffectiveContainer(ctx, record.container)
	if err != nil {
		return fmt.Errorf("inspect effective Agent container: %w", err)
	}
	if supervisor.config.Workbench != nil {
		return supervisor.verifyEffectiveWorkbench(agent, record, metadata)
	}
	boundary, err := supervisor.inspectEffectiveContainer(ctx, record.boundary)
	if err != nil {
		return fmt.Errorf("inspect effective boundary container: %w", err)
	}
	commonLabels := map[string]string{
		"chora.owner": "dockersupervisor", "chora.runtime_scope": supervisor.recoveryScope,
		"chora.run_id": metadata.runID, "chora.attempt_id": metadata.attemptID, "chora.task_id": metadata.taskID,
		"chora.policy_digest": supervisor.config.PolicyDigest,
	}
	if err := verifyCommonContainer(agent, record.container, supervisor.config.AttemptImageID, commonLabels); err != nil {
		return fmt.Errorf("effective Agent container identity: %w", err)
	}
	if err := verifyCommonContainer(boundary, record.boundary, supervisor.config.BoundaryImageID, commonLabels); err != nil {
		return fmt.Errorf("effective boundary container identity: %w", err)
	}
	if agent.Config.User != "1000:1000" || agent.HostConfig.NetworkMode != record.internalNetwork ||
		agent.HostConfig.NanoCPUs != 2_000_000_000 || agent.HostConfig.Memory != 4096<<20 || agent.HostConfig.MemorySwap != 4096<<20 || agent.HostConfig.PidsLimit != 256 ||
		!exactNetworkMembership(agent, []string{record.internalNetwork}) || !exactMounts(agent, map[string]effectiveMount{
		"/workspace":     {Source: filepath.Join(record.root, "workspace"), RW: true},
		"/input/context": {Source: record.contextDir, RW: false},
	}) || !hasNofileLimit(agent, 1024) ||
		!tmpfsMatches(agent.HostConfig.Tmpfs["/tmp"], []string{"rw", "nosuid", "nodev", "noexec", "uid=1000", "gid=1000", "size=64m"}) ||
		!tmpfsMatches(agent.HostConfig.Tmpfs["/run/chora/pi"], []string{"rw", "nosuid", "nodev", "noexec", "uid=1000", "gid=1000", "size=16m"}) ||
		!tmpfsMatches(agent.HostConfig.Tmpfs["/run/chora/trust"], []string{"rw", "nosuid", "nodev", "noexec", "uid=0", "gid=0", "mode=0755", "size=1m"}) ||
		!agentEnvironmentMatches(agent.Config.Env, metadata.environment) {
		return errors.New("effective Agent limits, mounts, network, tmpfs, user, or environment drift")
	}
	if !nonRootUser(boundary.Config.User) || boundary.HostConfig.NetworkMode != record.upstreamNetwork ||
		boundary.HostConfig.NanoCPUs != 250_000_000 || boundary.HostConfig.Memory != 64<<20 || boundary.HostConfig.MemorySwap != 64<<20 || boundary.HostConfig.PidsLimit != 32 ||
		!exactNetworkMembership(boundary, []string{record.internalNetwork, record.upstreamNetwork}) || len(boundary.Mounts) != 0 {
		return errors.New("effective boundary limits, mounts, network, or user drift")
	}
	return nil
}

func workbenchEnvironmentMatches(environment []string, metadata map[string]string) bool {
	expected := map[string]string{"HOME": "/run/chora/pi", "PI_CODING_AGENT_DIR": "/run/chora/pi", "PI_SKIP_VERSION_CHECK": "1", "PI_TELEMETRY": "0"}
	for key, value := range metadata {
		expected[key] = value
	}
	values := make(map[string]string, len(environment))
	for _, assignment := range environment {
		key, value, ok := strings.Cut(assignment, "=")
		if !ok || key == "" {
			return false
		}
		values[key] = value
	}
	for key, value := range expected {
		if values[key] != value {
			return false
		}
	}
	for key := range values {
		upper := strings.ToUpper(key)
		if key == "DOCKER_HOST" || key == "HTTP_PROXY" || key == "HTTPS_PROXY" || key == "ALL_PROXY" || strings.Contains(upper, "TOKEN") || strings.Contains(upper, "SECRET") || strings.Contains(upper, "PASSWORD") || strings.Contains(upper, "CREDENTIAL") || strings.Contains(upper, "API_KEY") {
			if _, explicitlyBound := expected[key]; !explicitlyBound {
				return false
			}
		}
	}
	return true
}

func (supervisor *Supervisor) verifyEffectiveWorkbench(agent effectiveContainer, record *attemptRecord, metadata invocationMetadata) error {
	labels := map[string]string{"chora.owner": "dockersupervisor", "chora.runtime_scope": supervisor.recoveryScope,
		"chora.run_id": metadata.runID, "chora.attempt_id": metadata.attemptID, "chora.task_id": metadata.taskID, "chora.policy_digest": WorkbenchPolicyDigest}
	if err := verifyCommonContainer(agent, record.container, supervisor.config.AttemptImageID, labels); err != nil {
		return err
	}
	wantMounts := map[string]effectiveMount{
		"/input/context": {Source: record.contextDir, RW: false},
		"/workspace":     {Type: "volume", Source: record.workspaceVolume, RW: true},
	}
	for _, mount := range record.readOnlyWorkspaceMounts {
		wantMounts[mount.destination] = effectiveMount{Source: mount.source, RW: false}
	}
	if agent.Config.User != "1000:1000" || agent.HostConfig.NetworkMode != "bridge" || agent.HostConfig.NanoCPUs != 2_000_000_000 ||
		agent.HostConfig.Memory != 4096<<20 || agent.HostConfig.MemorySwap != 4096<<20 || agent.HostConfig.PidsLimit != 256 || !hasNofileLimit(agent, 1024) {
		return errors.New("effective Workbench resource limits or user drift")
	}
	if !exactNetworkMembership(agent, []string{"bridge"}) {
		return errors.New("effective Workbench network membership drift")
	}
	if !exactMounts(agent, wantMounts) {
		mounts := make([]string, 0, len(agent.Mounts))
		for _, mount := range agent.Mounts {
			identity := mount.Name
			if mount.Type == "bind" {
				identity = "<bind>"
			}
			mounts = append(mounts, mount.Type+":"+mount.Destination+":"+identity+":"+strconv.FormatBool(mount.RW))
		}
		return fmt.Errorf("effective Workbench mounts drift: %v", mounts)
	}
	if !tmpfsMatches(agent.HostConfig.Tmpfs["/tmp"], []string{"rw", "nosuid", "nodev", "noexec", "uid=1000", "gid=1000", "size=64m"}) ||
		!tmpfsMatches(agent.HostConfig.Tmpfs["/run/chora/pi"], []string{"rw", "nosuid", "nodev", "noexec", "uid=1000", "gid=1000", "size=16m"}) {
		return errors.New("effective Workbench private tmpfs drift")
	}
	if !workbenchEnvironmentMatches(agent.Config.Env, metadata.environment) {
		return errors.New("effective Workbench environment drift")
	}
	return nil
}

func (supervisor *Supervisor) inspectEffectiveContainer(ctx context.Context, name string) (effectiveContainer, error) {
	result, err := supervisor.config.Runner.Run(ctx, Command{Args: []string{"container", "inspect", "--format", "{{json .}}", name}})
	if err != nil || result.ExitCode != 0 {
		return effectiveContainer{}, fmt.Errorf("docker inspect: exit=%d error=%v stderr=%s", result.ExitCode, err, boundedDiagnostic(result.Stderr))
	}
	var document effectiveContainer
	if json.Unmarshal(result.Stdout, &document) != nil {
		return effectiveContainer{}, errors.New("docker inspect returned malformed JSON")
	}
	return document, nil
}

func verifyCommonContainer(container effectiveContainer, name, imageID string, labels map[string]string) error {
	decodedID, err := hex.DecodeString(container.ID)
	if err != nil || len(decodedID) != 32 || container.Name != "/"+name || container.Image != imageID || container.Config.Labels["chora.image_digest"] != imageID {
		return errors.New("name, ID, or image drift")
	}
	for key, value := range labels {
		if container.Config.Labels[key] != value {
			return fmt.Errorf("label %s drift", key)
		}
	}
	host := container.HostConfig
	if !host.ReadonlyRootfs || host.Privileged || host.PidMode == "host" || host.IpcMode == "host" || host.LogConfig.Type != "none" ||
		!slices.Equal(host.CapDrop, []string{"ALL"}) || len(host.CapAdd) != 0 || !slices.Equal(host.SecurityOpt, []string{"no-new-privileges:true"}) ||
		len(host.Devices) != 0 || len(host.DeviceRequests) != 0 || len(host.PortBindings) != 0 {
		return errors.New("privilege, namespace, capability, device, port, rootfs, or log boundary drift")
	}
	return nil
}

type effectiveMount struct {
	Type   string
	Source string
	RW     bool
}

func exactMounts(container effectiveContainer, expected map[string]effectiveMount) bool {
	if len(container.Mounts) != len(expected) {
		return false
	}
	for _, mount := range container.Mounts {
		want, ok := expected[mount.Destination]
		wantType := want.Type
		if wantType == "" {
			wantType = "bind"
		}
		actualSource := mount.Source
		if mount.Type == "volume" {
			actualSource = mount.Name
		}
		if !ok || mount.Type != wantType || filepath.Clean(actualSource) != want.Source || mount.RW != want.RW {
			return false
		}
	}
	return true
}

func exactNetworkMembership(container effectiveContainer, expected []string) bool {
	if len(container.NetworkSettings.Networks) != len(expected) {
		return false
	}
	for _, network := range expected {
		if _, ok := container.NetworkSettings.Networks[network]; !ok {
			return false
		}
	}
	return true
}

func hasNofileLimit(container effectiveContainer, limit int64) bool {
	return len(container.HostConfig.Ulimits) == 1 && container.HostConfig.Ulimits[0].Name == "nofile" &&
		container.HostConfig.Ulimits[0].Soft == limit && container.HostConfig.Ulimits[0].Hard == limit
}

func tmpfsMatches(actual string, expected []string) bool {
	parts := strings.Split(actual, ",")
	if len(parts) != len(expected) {
		return false
	}
	for _, value := range expected {
		if !slices.Contains(parts, value) {
			return false
		}
	}
	return true
}

func agentEnvironmentMatches(environment []string, metadata map[string]string) bool {
	values := make(map[string]string, len(environment))
	for _, assignment := range environment {
		key, value, ok := strings.Cut(assignment, "=")
		if !ok || key == "" {
			return false
		}
		values[key] = value
	}
	expected := map[string]string{
		"HOME": "/run/chora/pi", "GOCACHE": toolCachePath, "GOTMPDIR": toolTempPath,
		"PI_CODING_AGENT_DIR": "/run/chora/pi", "PI_OFFLINE": "1", "PI_SKIP_VERSION_CHECK": "1", "PI_TELEMETRY": "0",
		"HTTP_PROXY": "http://codex-boundary:8080", "HTTPS_PROXY": "http://codex-boundary:8080", "NO_PROXY": "localhost,127.0.0.1",
		"NODE_EXTRA_CA_CERTS": "/run/chora/trust/starpoint-root-ca-2048-g2.pem",
	}
	for key, value := range metadata {
		expected[key] = value
	}
	for key, value := range expected {
		if values[key] != value {
			return false
		}
	}
	for key := range values {
		upper := strings.ToUpper(key)
		if (strings.Contains(upper, "TOKEN") || strings.Contains(upper, "SECRET") || strings.Contains(upper, "PASSWORD") || strings.Contains(upper, "CREDENTIAL") || strings.Contains(upper, "API_KEY")) && expected[key] == "" {
			return false
		}
	}
	return true
}

func nonRootUser(user string) bool {
	uid := strings.SplitN(strings.TrimSpace(user), ":", 2)[0]
	if uid == "" || uid == "root" {
		return false
	}
	parsed, err := strconv.Atoi(uid)
	return err == nil && parsed > 0
}
