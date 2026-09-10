package verifier

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
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
)

var ErrDockerBoundary = errors.New("invalid Docker verifier boundary")

type DockerCommand struct {
	Args   []string
	Stdout io.Writer
	Stderr io.Writer
}

type DockerCommandResult struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
}

type DockerRunner interface {
	Run(context.Context, DockerCommand) (DockerCommandResult, error)
}

// InstalledDockerAuthority is the installed composition's immutable execution
// authority. The implementation must validate the live Engine against the
// persisted qualification using the same underlying Runner that executes every
// Verifier lifecycle command. The factory deliberately does not reconstruct or
// discover this authority from ambient Docker state.
type InstalledDockerAuthority interface {
	Verify(context.Context) error
	VerifierImageID() string
}

type ExecDockerRunner struct {
	Binary string
}

func (runner ExecDockerRunner) Run(ctx context.Context, request DockerCommand) (DockerCommandResult, error) {
	binary := runner.Binary
	if strings.TrimSpace(binary) == "" {
		binary = "docker"
	}
	command := exec.CommandContext(ctx, binary, request.Args...)
	stdoutBuffer, stderrBuffer := newBoundedBuffer(1<<20), newBoundedBuffer(1<<20)
	if request.Stdout == nil {
		command.Stdout = stdoutBuffer
	} else {
		command.Stdout = request.Stdout
	}
	if request.Stderr == nil {
		command.Stderr = stderrBuffer
	} else {
		command.Stderr = request.Stderr
	}
	err := command.Run()
	result := DockerCommandResult{Stdout: slices.Clone(stdoutBuffer.Bytes()), Stderr: slices.Clone(stderrBuffer.Bytes())}
	if err == nil {
		return result, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		result.ExitCode = exitError.ExitCode()
		return result, nil
	}
	return DockerCommandResult{}, err
}

type boundedBuffer struct {
	limit int
	bytes.Buffer
}

func newBoundedBuffer(limit int) *boundedBuffer { return &boundedBuffer{limit: limit} }

func (buffer *boundedBuffer) Write(body []byte) (int, error) {
	remaining := buffer.limit - buffer.Len()
	if remaining > 0 {
		keep := len(body)
		if keep > remaining {
			keep = remaining
		}
		_, _ = buffer.Buffer.Write(body[:keep])
	}
	return len(body), nil
}

type DockerFactoryConfig struct {
	Runner             DockerRunner
	ColimaVersion      func(context.Context) (string, error)
	RecoveryScope      string
	InstalledAuthority InstalledDockerAuthority
}

type DockerSandboxFactory struct {
	runner             DockerRunner
	colimaVersion      func(context.Context) (string, error)
	recoveryScope      string
	installedAuthority InstalledDockerAuthority
}

func NewDockerSandboxFactory(config DockerFactoryConfig) (*DockerSandboxFactory, error) {
	if config.InstalledAuthority != nil && (!installedDockerAuthorityAvailable(config.InstalledAuthority) || config.Runner == nil || !validSHA256Identity(config.InstalledAuthority.VerifierImageID())) {
		return nil, ErrDockerBoundary
	}
	if config.Runner == nil {
		config.Runner = ExecDockerRunner{}
	}
	if config.ColimaVersion == nil {
		config.ColimaVersion = installedVerifierColimaVersion
	}
	if config.RecoveryScope != "" && !validRecoveryScope(config.RecoveryScope) {
		return nil, ErrDockerBoundary
	}
	return &DockerSandboxFactory{
		runner: config.Runner, colimaVersion: config.ColimaVersion, recoveryScope: config.RecoveryScope,
		installedAuthority: config.InstalledAuthority,
	}, nil
}

func installedDockerAuthorityAvailable(authority InstalledDockerAuthority) bool {
	if authority == nil {
		return false
	}
	value := reflect.ValueOf(authority)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return !value.IsNil()
	default:
		return true
	}
}

func (factory *DockerSandboxFactory) Create(ctx context.Context, spec SandboxSpec) (Sandbox, error) {
	workspace := filepath.Clean(spec.WorkspaceRoot)
	policy := spec.Policy.document
	if err := validatePolicy(policy); err != nil || spec.Policy.digest == ([32]byte{}) || !spec.Identity.valid() || spec.Identity.VerifierPolicyDigest != spec.Policy.digest ||
		spec.Identity.VerifierImage != policy.Image || spec.Identity.VerifierPolicyVersion != policy.Version || spec.Identity.WorkspaceIdentity != workspaceIdentity(workspace) ||
		!filepath.IsAbs(workspace) || workspace == string(filepath.Separator) || strings.ContainsAny(workspace, ",\r\n") || spec.ArtifactLimit != policy.ArtifactBytes {
		return nil, ErrDockerBoundary
	}
	info, err := os.Stat(workspace)
	if err != nil || !info.IsDir() {
		return nil, ErrDockerBoundary
	}
	if err := factory.verifyAuthority(ctx, policy); err != nil {
		return nil, err
	}
	imageResult, err := factory.runner.Run(ctx, DockerCommand{Args: []string{"image", "inspect", "--format", "{{.Id}}", policy.Image}})
	if err != nil || imageResult.ExitCode != 0 || strings.TrimSpace(string(imageResult.Stdout)) != policy.Image {
		return nil, fmt.Errorf("%w: image identity", ErrDockerBoundary)
	}
	nameDigest := sha256.Sum256([]byte(spec.Identity.AttemptID))
	name := "chora-verifier-" + hex.EncodeToString(nameDigest[:10])
	labels := identityLabels(spec.Identity, policy)
	if factory.recoveryScope != "" {
		labels = append(labels, "chora.verifier_runtime_id="+factory.recoveryScope)
	}
	args := []string{"create", "--pull=never", "--name", name}
	for _, label := range labels {
		args = append(args, "--label", label)
	}
	args = append(args,
		"--network", "none",
		"--user", strconv.Itoa(policy.NonRootUID)+":"+strconv.Itoa(policy.NonRootGID),
		"--read-only",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges:true",
		"--cpus", strconv.Itoa(policy.CPUs),
		"--memory", strconv.Itoa(policy.MemoryMiB)+"m",
		"--memory-swap", strconv.Itoa(policy.MemorySwapMiB)+"m",
		"--pids-limit", strconv.Itoa(policy.PIDs),
		"--ulimit", "nofile="+strconv.Itoa(policy.FileDescriptors)+":"+strconv.Itoa(policy.FileDescriptors),
		"--stop-timeout", strconv.Itoa(policy.DeathConfirmationSeconds),
		"--log-driver", "none",
		"--mount", "type=bind,source="+workspace+",target=/workspace",
		"--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=64m",
		"--workdir", "/workspace",
	)
	for _, environment := range policy.Environment {
		args = append(args, "--env", environment)
	}
	args = append(args,
		"--entrypoint", "/usr/bin/tail", policy.Image, "-f", "/dev/null",
	)
	createResult, err := factory.runner.Run(ctx, DockerCommand{Args: args})
	if err != nil || createResult.ExitCode != 0 {
		return nil, fmt.Errorf("%w: create container: exit=%d error=%v stderr=%s", ErrDockerBoundary, createResult.ExitCode, err, dockerDiagnostic(createResult.Stderr))
	}
	containerID := strings.TrimSpace(string(createResult.Stdout))
	if !validContainerID(containerID) {
		return nil, fmt.Errorf("%w: container identity", ErrDockerBoundary)
	}
	sandbox := &dockerSandbox{runner: factory.runner, id: containerID, image: policy.Image, uid: policy.NonRootUID, gid: policy.NonRootGID}
	inspectFormat := "{{.Id}}|{{.Image}}|{{.HostConfig.NetworkMode}}|{{.Config.User}}|{{.HostConfig.ReadonlyRootfs}}|{{.HostConfig.NanoCpus}}|{{.HostConfig.Memory}}|{{.HostConfig.MemorySwap}}|{{.HostConfig.PidsLimit}}|{{.HostConfig.Privileged}}|{{.HostConfig.LogConfig.Type}}|{{json .HostConfig.CapDrop}}|{{json .HostConfig.SecurityOpt}}|{{json .HostConfig.Devices}}|{{json .HostConfig.Ulimits}}|{{range .Mounts}}{{.Source}}>{{.Destination}}>{{.RW}}{{end}}|{{index .HostConfig.Tmpfs \"/tmp\"}}"
	inspectResult, inspectErr := factory.runner.Run(ctx, DockerCommand{Args: []string{"container", "inspect", "--format", inspectFormat, containerID}})
	inspectFields := strings.Split(strings.TrimSpace(string(inspectResult.Stdout)), "|")
	wantInspect := []string{
		containerID, policy.Image, "none", strconv.Itoa(policy.NonRootUID) + ":" + strconv.Itoa(policy.NonRootGID), "true",
		strconv.FormatInt(int64(policy.CPUs)*1_000_000_000, 10), strconv.FormatInt(int64(policy.MemoryMiB)<<20, 10), strconv.FormatInt(int64(policy.MemorySwapMiB)<<20, 10), strconv.Itoa(policy.PIDs),
		"false", "none", `["ALL"]`, `["no-new-privileges:true"]`, "", "", workspace + ">/workspace>true", "rw,nosuid,nodev,noexec,size=64m",
	}
	validInspect := inspectErr == nil && inspectResult.ExitCode == 0 && len(inspectFields) == len(wantInspect)
	if validInspect {
		for index := range inspectFields {
			if index == 13 {
				validInspect = inspectFields[index] == "[]" || inspectFields[index] == "null"
				continue
			}
			if index == 14 {
				var ulimits []struct {
					Hard int64  `json:"Hard"`
					Name string `json:"Name"`
					Soft int64  `json:"Soft"`
				}
				validInspect = json.Unmarshal([]byte(inspectFields[index]), &ulimits) == nil && len(ulimits) == 1 && ulimits[0].Name == "nofile" && ulimits[0].Hard == int64(policy.FileDescriptors) && ulimits[0].Soft == int64(policy.FileDescriptors)
				continue
			}
			validInspect = validInspect && inspectFields[index] == wantInspect[index]
		}
	}
	if !validInspect {
		cleanupErr := sandbox.removeAndProve(context.Background())
		return nil, errors.Join(fmt.Errorf("%w: effective container identity: exit=%d error=%v output=%s", ErrDockerBoundary, inspectResult.ExitCode, inspectErr, dockerDiagnostic(inspectResult.Stdout)), cleanupErr)
	}
	startResult, startErr := factory.runner.Run(ctx, DockerCommand{Args: []string{"start", containerID}})
	if startErr != nil || startResult.ExitCode != 0 {
		cleanupErr := sandbox.removeAndProve(context.Background())
		return nil, errors.Join(fmt.Errorf("%w: start container", ErrDockerBoundary), cleanupErr)
	}
	return sandbox, nil
}

// Recover removes and proves removal of only verifier containers carrying this
// runtime's digest-bound recovery label. It never enumerates or touches
// unrelated containers.
func (factory *DockerSandboxFactory) Recover(ctx context.Context) error {
	if factory == nil || !validRecoveryScope(factory.recoveryScope) {
		return ErrDockerBoundary
	}
	if factory.installedAuthority != nil {
		if err := factory.installedAuthority.Verify(ctx); err != nil {
			return fmt.Errorf("%w: installed Docker authority", ErrDockerBoundary)
		}
	}
	filter := "label=chora.verifier_runtime_id=" + factory.recoveryScope
	list := func() (DockerCommandResult, error) {
		return factory.runner.Run(ctx, DockerCommand{Args: []string{"container", "ls", "--all", "--quiet", "--no-trunc", "--filter", filter}})
	}
	result, err := list()
	if err != nil || result.ExitCode != 0 {
		return errors.Join(ErrDockerBoundary, err)
	}
	for _, id := range strings.Fields(string(result.Stdout)) {
		if !validContainerID(id) {
			return ErrDockerBoundary
		}
		removed, removeErr := factory.runner.Run(ctx, DockerCommand{Args: []string{"rm", "--force", id}})
		if removeErr != nil || removed.ExitCode != 0 {
			return errors.Join(ErrDockerBoundary, removeErr)
		}
	}
	remaining, err := list()
	if err != nil || remaining.ExitCode != 0 || strings.TrimSpace(string(remaining.Stdout)) != "" {
		return errors.Join(ErrDockerBoundary, err)
	}
	return nil
}

func (factory *DockerSandboxFactory) verifyAuthority(ctx context.Context, policy PolicyDocument) error {
	if factory.installedAuthority == nil {
		return factory.verifyProvider(ctx, policy)
	}
	if policy.Image != factory.installedAuthority.VerifierImageID() {
		return fmt.Errorf("%w: activated Verifier image identity", ErrDockerBoundary)
	}
	if err := factory.installedAuthority.Verify(ctx); err != nil {
		return fmt.Errorf("%w: installed Docker authority", ErrDockerBoundary)
	}
	return nil
}

func validRecoveryScope(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func dockerDiagnostic(body []byte) string {
	if len(body) > 4096 {
		body = body[:4096]
	}
	return strings.TrimSpace(string(body))
}

func (factory *DockerSandboxFactory) verifyProvider(ctx context.Context, policy PolicyDocument) error {
	version, err := factory.runner.Run(ctx, DockerCommand{Args: []string{"version", "--format", "{{.Client.Version}}|{{.Server.Version}}"}})
	if err != nil || version.ExitCode != 0 || strings.TrimSpace(string(version.Stdout)) != policy.DockerVersion+"|"+policy.DockerVersion {
		return fmt.Errorf("%w: Docker version identity", ErrDockerBoundary)
	}
	contextResult, err := factory.runner.Run(ctx, DockerCommand{Args: []string{"context", "show"}})
	if err != nil || contextResult.ExitCode != 0 || strings.TrimSpace(string(contextResult.Stdout)) != policy.DockerContext {
		return fmt.Errorf("%w: Docker context identity", ErrDockerBoundary)
	}
	colimaVersion, err := factory.colimaVersion(ctx)
	if err != nil || colimaVersion != policy.ColimaVersion {
		return fmt.Errorf("%w: Colima version identity", ErrDockerBoundary)
	}
	return nil
}

func installedVerifierColimaVersion(ctx context.Context) (string, error) {
	output, err := exec.CommandContext(ctx, "colima", "version").Output()
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(output))
	for index, field := range fields {
		if strings.EqualFold(field, "version") && index+1 < len(fields) {
			return strings.TrimSpace(fields[index+1]), nil
		}
	}
	return "", errors.New("Colima version output is malformed")
}

func identityLabels(identity EvidenceIdentity, policy PolicyDocument) []string {
	return []string{
		"chora.run_id=" + identity.RunID,
		"chora.verification_run_id=" + identity.VerificationRunID,
		"chora.verification_attempt_id=" + identity.AttemptID,
		"chora.task_id=" + identity.TaskID,
		"chora.image_digest=" + identity.VerifierImage,
		"chora.verifier_policy_digest=" + hex.EncodeToString(identity.VerifierPolicyDigest[:]),
		"chora.baseline_digest=" + hex.EncodeToString(identity.BaselineDigest[:]),
		"chora.patch_digest=" + hex.EncodeToString(identity.PatchDigest[:]),
		"chora.acceptance_contract_digest=" + hex.EncodeToString(identity.AcceptanceContractDigest[:]),
		"chora.workspace_id=" + identity.WorkspaceIdentity,
	}
}

type dockerSandbox struct {
	mu      sync.Mutex
	runner  DockerRunner
	id      string
	image   string
	uid     int
	gid     int
	removed bool
}

func (sandbox *dockerSandbox) Identity() string {
	return "docker:" + sandbox.id + "@" + sandbox.image
}

func (sandbox *dockerSandbox) Execute(ctx context.Context, request CommandRequest, stdout, stderr io.Writer) CommandOutcome {
	sandbox.mu.Lock()
	removed := sandbox.removed
	sandbox.mu.Unlock()
	if removed || strings.TrimSpace(request.ID) == "" || len(request.Argv) == 0 {
		return CommandOutcome{Kind: CommandLaunchFailed, Reason: "invalid or removed verifier container"}
	}
	for _, arg := range request.Argv {
		if strings.TrimSpace(arg) == "" || strings.ContainsRune(arg, '\x00') {
			return CommandOutcome{Kind: CommandLaunchFailed, Reason: "invalid direct argv"}
		}
	}
	args := []string{"exec", "--workdir", "/workspace", "--user", strconv.Itoa(sandbox.uid) + ":" + strconv.Itoa(sandbox.gid), sandbox.id}
	args = append(args, slices.Clone(request.Argv)...)
	result, err := sandbox.runner.Run(ctx, DockerCommand{Args: args, Stdout: stdout, Stderr: stderr})
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return CommandOutcome{Kind: CommandTimedOut, Reason: context.DeadlineExceeded.Error()}
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return CommandOutcome{Kind: CommandCancelled, Reason: context.Canceled.Error()}
	}
	if err != nil {
		return CommandOutcome{Kind: CommandLaunchFailed, Reason: err.Error()}
	}
	exitCode := result.ExitCode
	if exitCode != 0 {
		state, stateErr := sandbox.runner.Run(ctx, DockerCommand{Args: []string{"container", "inspect", "--format", "{{.State.Running}}", sandbox.id}})
		if stateErr != nil || state.ExitCode != 0 || strings.TrimSpace(string(state.Stdout)) != "true" {
			return CommandOutcome{Kind: CommandLaunchFailed, Reason: "verifier container process died during command"}
		}
	}
	return CommandOutcome{Kind: CommandExited, ExitCode: &exitCode}
}

func (sandbox *dockerSandbox) Terminate(ctx context.Context) error {
	return sandbox.removeAndProve(ctx)
}
func (sandbox *dockerSandbox) Cleanup(ctx context.Context) error { return sandbox.removeAndProve(ctx) }

func (sandbox *dockerSandbox) removeAndProve(ctx context.Context) error {
	sandbox.mu.Lock()
	defer sandbox.mu.Unlock()
	if sandbox.removed {
		return nil
	}
	_, removeErr := sandbox.runner.Run(ctx, DockerCommand{Args: []string{"rm", "--force", sandbox.id}})
	listResult, listErr := sandbox.runner.Run(ctx, DockerCommand{Args: []string{"container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "id=" + sandbox.id}})
	if listErr != nil || listResult.ExitCode != 0 || strings.TrimSpace(string(listResult.Stdout)) != "" {
		return errors.Join(ErrIntegrity, removeErr, listErr)
	}
	sandbox.removed = true
	return nil
}

func validContainerID(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
