package dockersupervisor

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
		Type, Source, Destination string
		RW                        bool
	} `json:"Mounts"`
	NetworkSettings struct {
		Networks map[string]json.RawMessage `json:"Networks"`
	} `json:"NetworkSettings"`
}

func (supervisor *Supervisor) verifyEffectiveContainers(ctx context.Context, record *attemptRecord, metadata invocationMetadata) error {
	agent, err := supervisor.inspectEffectiveContainer(ctx, record.container)
	if err != nil {
		return fmt.Errorf("inspect effective Agent container: %w", err)
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
	Source string
	RW     bool
}

func exactMounts(container effectiveContainer, expected map[string]effectiveMount) bool {
	if len(container.Mounts) != len(expected) {
		return false
	}
	for _, mount := range container.Mounts {
		want, ok := expected[mount.Destination]
		if !ok || mount.Type != "bind" || filepath.Clean(mount.Source) != want.Source || mount.RW != want.RW {
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
