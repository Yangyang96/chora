package dockersupervisor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// Command is one Docker CLI operation. Environment is intentionally absent:
// credentials and inherited host state must never reach Docker by accident.
type Command struct {
	Args   []string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

type CommandResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

type Process interface {
	io.WriteCloser
	Kill() error
	Wait() (int, error)
}

type CommandRunner interface {
	Run(context.Context, Command) (CommandResult, error)
	Start(context.Context, Command) (Process, error)
}

// DockerCommandAuthority is the explicit preselected Docker endpoint authority
// shared by Engine observation, capability Probe, execution, and recovery. The
// endpoint remains non-secret and digest-only, matching EngineIdentity.
type DockerCommandAuthority struct {
	ContextName           string
	ContextEndpointDigest string
}

func (authority DockerCommandAuthority) valid() bool {
	return validText(authority.ContextName, 256) && validCanonicalDigest(authority.ContextEndpointDigest)
}

type dockerRunner struct {
	executable         string
	executableInfo     os.FileInfo
	executableHash     [sha256.Size]byte
	authorityRoot      string
	authorityRootInfo  os.FileInfo
	authorityInputHash [sha256.Size]byte
	endpointDigest     string
	environment        []string
	contextName        string
}

// NewDockerCommandRunner returns the production Docker CLI authority. Setup,
// Probe, server composition, Start, and recovery can share this exact Runner
// instead of accidentally qualifying one context and executing against another.
// The selected executable, Docker configuration root, context, and endpoint
// digest are frozen at construction. Context selection is mandatory: this API
// never consults Docker's current/default context and has no provider fallback.
func NewDockerCommandRunner(authority DockerCommandAuthority) (CommandRunner, error) {
	if !authority.valid() {
		return nil, errors.New("invalid explicit Docker command authority")
	}
	path, err := exec.LookPath("docker")
	if err != nil {
		return nil, fmt.Errorf("resolve Docker CLI: %w", err)
	}
	configRoot := strings.TrimSpace(os.Getenv("DOCKER_CONFIG"))
	if configRoot == "" {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return nil, fmt.Errorf("resolve Docker configuration root: %w", homeErr)
		}
		configRoot = filepath.Join(home, ".docker")
	}
	runner, err := newDockerRunner(path, configRoot, authority)
	if err != nil {
		return nil, err
	}
	return runner, nil
}

func newDockerRunner(executable, configRoot string, authority DockerCommandAuthority) (*dockerRunner, error) {
	if strings.TrimSpace(configRoot) == "" || !authority.valid() {
		return nil, errors.New("invalid Docker CLI environment authority")
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return nil, fmt.Errorf("resolve exact Docker CLI executable: %w", err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return nil, fmt.Errorf("make Docker CLI executable absolute: %w", err)
	}
	configRoot, err = filepath.EvalSymlinks(configRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve Docker endpoint authority root: %w", err)
	}
	configRoot, err = filepath.Abs(filepath.Clean(configRoot))
	if err != nil {
		return nil, errors.New("invalid Docker CLI environment authority")
	}
	info, digest, err := executableIdentity(resolved)
	if err != nil {
		return nil, err
	}
	authorityInfo, authorityDigest, endpointDigest, err := dockerEndpointAuthorityIdentity(configRoot, authority.ContextName)
	if err != nil {
		return nil, err
	}
	if endpointDigest != authority.ContextEndpointDigest {
		return nil, errors.New("Docker context endpoint does not match the explicit command authority")
	}
	return &dockerRunner{
		executable: resolved, executableInfo: info, executableHash: digest, authorityRoot: configRoot,
		authorityRootInfo: authorityInfo, authorityInputHash: authorityDigest, endpointDigest: endpointDigest,
		environment: []string{"DOCKER_CONFIG=" + configRoot}, contextName: authority.ContextName,
	}, nil
}

func executableIdentity(path string) (os.FileInfo, [sha256.Size]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, [sha256.Size]byte{}, fmt.Errorf("open exact Docker CLI executable: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, [sha256.Size]byte{}, fmt.Errorf("stat exact Docker CLI executable: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return nil, [sha256.Size]byte{}, errors.New("exact Docker CLI executable is not an executable regular file")
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return nil, [sha256.Size]byte{}, fmt.Errorf("hash exact Docker CLI executable: %w", err)
	}
	var identity [sha256.Size]byte
	copy(identity[:], digest.Sum(nil))
	return info, identity, nil
}

func dockerEndpointAuthorityIdentity(configRoot, contextName string) (os.FileInfo, [sha256.Size]byte, string, error) {
	rootInfo, err := os.Lstat(configRoot)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, [sha256.Size]byte{}, "", fmt.Errorf("inspect Docker endpoint authority root: %w", errors.Join(err, errors.New("root is not a non-symlink directory")))
	}
	contextDigest := fmt.Sprintf("%x", sha256.Sum256([]byte(contextName)))
	inputs := []struct {
		path     string
		required bool
	}{
		{path: filepath.Join(configRoot, "config.json")},
		{path: filepath.Join(configRoot, "contexts", "meta", contextDigest, "meta.json"), required: true},
		{path: filepath.Join(configRoot, "contexts", "tls", contextDigest, "docker")},
	}
	hash := sha256.New()
	_, _ = io.WriteString(hash, "docker-endpoint-authority-v1\x00"+contextName+"\x00")
	for _, input := range inputs {
		if err := hashDockerAuthorityInput(hash, configRoot, input.path, input.required); err != nil {
			return nil, [sha256.Size]byte{}, "", err
		}
	}
	endpointDigest, err := dockerContextEndpointDigest(inputs[1].path, contextName)
	if err != nil {
		return nil, [sha256.Size]byte{}, "", err
	}
	var identity [sha256.Size]byte
	copy(identity[:], hash.Sum(nil))
	return rootInfo, identity, endpointDigest, nil
}

func dockerContextEndpointDigest(metadataPath, contextName string) (string, error) {
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		return "", fmt.Errorf("read explicit Docker context metadata: %w", err)
	}
	var metadata struct {
		Name      string `json:"Name"`
		Endpoints struct {
			Docker struct {
				Host string `json:"Host"`
			} `json:"docker"`
		} `json:"Endpoints"`
	}
	if err := json.Unmarshal(data, &metadata); err != nil || metadata.Name != contextName {
		return "", errors.New("explicit Docker context metadata name is missing or mismatched")
	}
	digest, err := DigestContextEndpoint(metadata.Endpoints.Docker.Host)
	if err != nil {
		return "", fmt.Errorf("validate explicit Docker context endpoint: %w", err)
	}
	return digest, nil
}

func hashDockerAuthorityInput(hash io.Writer, root, path string, required bool) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) && !required {
		relative, _ := filepath.Rel(root, path)
		_, _ = io.WriteString(hash, "absent\x00"+filepath.ToSlash(relative)+"\x00")
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect Docker endpoint authority input %q: %w", path, err)
	}
	if err := validateDockerAuthorityAncestors(root, path); err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("inspect Docker endpoint authority input %q: symbolic links are forbidden", path)
	}
	if info.IsDir() {
		return filepath.WalkDir(path, func(entryPath string, _ os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			return hashDockerAuthorityEntry(hash, root, entryPath)
		})
	}
	return hashDockerAuthorityEntry(hash, root, path)
}

func validateDockerAuthorityAncestors(root, path string) error {
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("inspect Docker endpoint authority input %q: path escaped authority root", path)
	}
	current := root
	parts := strings.Split(filepath.Clean(relative), string(filepath.Separator))
	for _, part := range parts[:len(parts)-1] {
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("inspect Docker endpoint authority ancestor %q: %w", current, errors.Join(statErr, errors.New("ancestor is not a non-symlink directory")))
		}
	}
	return nil
}

func hashDockerAuthorityEntry(hash io.Writer, root, path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect Docker endpoint authority entry %q: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
		return fmt.Errorf("inspect Docker endpoint authority entry %q: only non-symlink directories and regular files are allowed", path)
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("inspect Docker endpoint authority entry %q: path escaped authority root", path)
	}
	kind := "file"
	if info.IsDir() {
		kind = "directory"
	}
	_, _ = io.WriteString(hash, kind+"\x00"+filepath.ToSlash(relative)+"\x00"+info.Mode().String()+"\x00")
	if info.IsDir() {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("read Docker endpoint authority entry %q: %w", path, err)
	}
	defer file.Close()
	if _, err := io.Copy(hash, file); err != nil {
		return fmt.Errorf("hash Docker endpoint authority entry %q: %w", path, err)
	}
	_, _ = io.WriteString(hash, "\x00")
	return nil
}

func (runner *dockerRunner) command(ctx context.Context, command Command) (*exec.Cmd, error) {
	if runner == nil {
		return nil, errors.New("Docker CLI authority is unavailable")
	}
	info, digest, err := executableIdentity(runner.executable)
	if err != nil || !os.SameFile(runner.executableInfo, info) || digest != runner.executableHash {
		return nil, fmt.Errorf("Docker CLI executable identity drift: %w", errors.Join(err, errors.New("pinned executable no longer matches")))
	}
	authorityInfo, authorityDigest, endpointDigest, err := dockerEndpointAuthorityIdentity(runner.authorityRoot, runner.contextName)
	if err != nil || !os.SameFile(runner.authorityRootInfo, authorityInfo) || authorityDigest != runner.authorityInputHash || endpointDigest != runner.endpointDigest {
		return nil, fmt.Errorf("Docker endpoint authority drift: %w", errors.Join(err, errors.New("pinned context, endpoint, config, or TLS input no longer matches")))
	}
	args := append([]string{"--context", runner.contextName}, command.Args...)
	cmd := exec.CommandContext(ctx, runner.executable, args...)
	cmd.Env = slices.Clone(runner.environment)
	return cmd, nil
}

func (runner *dockerRunner) Run(ctx context.Context, command Command) (CommandResult, error) {
	cmd, err := runner.command(ctx, command)
	if err != nil {
		return CommandResult{ExitCode: -1}, err
	}
	cmd.Stdin = command.Stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	result := CommandResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	} else if err != nil {
		result.ExitCode = -1
	}
	return result, err
}

func (runner *dockerRunner) Start(ctx context.Context, command Command) (Process, error) {
	cmd, err := runner.command(ctx, command)
	if err != nil {
		return nil, err
	}
	cmd.Stdout, cmd.Stderr = command.Stdout, command.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, err
	}
	return &dockerProcess{cmd: cmd, stdin: stdin}, nil
}

type dockerProcess struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
}

func (process *dockerProcess) Write(data []byte) (int, error) { return process.stdin.Write(data) }
func (process *dockerProcess) Close() error                   { return process.stdin.Close() }
func (process *dockerProcess) Kill() error                    { return process.cmd.Process.Kill() }
func (process *dockerProcess) Wait() (int, error) {
	err := process.cmd.Wait()
	if process.cmd.ProcessState == nil {
		return -1, err
	}
	return process.cmd.ProcessState.ExitCode(), err
}
