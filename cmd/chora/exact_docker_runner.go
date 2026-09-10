package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Yangyang96/chora/internal/dockersupervisor"
	"github.com/Yangyang96/chora/internal/productinstall"
)

type exactDockerCLIRunner struct {
	executable     string
	executableInfo os.FileInfo
	executableHash [sha256.Size]byte
	contextName    string
	configRoot     string
	configRootInfo os.FileInfo
	configHash     [sha256.Size]byte
	endpoint       string
	endpointDigest string
}

func newExactDockerCLIRunner(target productinstall.EngineTarget) (*exactDockerCLIRunner, error) {
	if !canonicalAbsolute(target.CLIPath) || !productIdentifier.MatchString(target.ContextName) || !validFingerprint(target.EndpointDigest) {
		return nil, productinstall.ErrInvalidRequest
	}
	resolved, err := filepath.EvalSymlinks(target.CLIPath)
	if err != nil || resolved != target.CLIPath {
		return nil, productinstall.ErrInvalidRequest
	}
	info, digest, err := exactExecutableIdentity(target.CLIPath)
	if err != nil {
		return nil, errors.New("exact Docker CLI unavailable")
	}
	configRoot := os.Getenv("DOCKER_CONFIG")
	if configRoot == "" {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return nil, errors.New("explicit Docker configuration unavailable")
		}
		configRoot = filepath.Join(home, ".docker")
	}
	configRoot, err = filepath.EvalSymlinks(configRoot)
	if err != nil || !canonicalAbsolute(configRoot) {
		return nil, errors.New("explicit Docker configuration unavailable")
	}
	configRootInfo, configHash, endpoint, endpointDigest, err := exactDockerAuthorityIdentity(configRoot, target.ContextName)
	if err != nil || endpointDigest != target.EndpointDigest {
		return nil, errors.New("explicit Docker endpoint unavailable")
	}
	return &exactDockerCLIRunner{
		executable: target.CLIPath, executableInfo: info, executableHash: digest,
		contextName: target.ContextName, configRoot: configRoot, configRootInfo: configRootInfo,
		configHash: configHash, endpoint: endpoint, endpointDigest: endpointDigest,
	}, nil
}

func exactExecutableIdentity(path string) (os.FileInfo, [sha256.Size]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, [sha256.Size]byte{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return nil, [sha256.Size]byte{}, errors.New("not executable")
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return nil, [sha256.Size]byte{}, err
	}
	var result [sha256.Size]byte
	copy(result[:], digest.Sum(nil))
	return info, result, nil
}

// exactDockerAuthorityIdentity binds the non-process inputs Docker uses to
// select an explicit context. Avoiding a Docker CLI observation here is
// deliberate: callers place the Runner behind OperationLedger, so every
// process start remains inside that one hash-chained boundary.
func exactDockerAuthorityIdentity(configRoot, contextName string) (os.FileInfo, [sha256.Size]byte, string, string, error) {
	var identity [sha256.Size]byte
	rootInfo, err := os.Lstat(configRoot)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, identity, "", "", errors.New("explicit Docker configuration root unavailable")
	}
	contextID := sha256.Sum256([]byte(contextName))
	metadataPath := filepath.Join(configRoot, "contexts", "meta", hex.EncodeToString(contextID[:]), "meta.json")
	inputs := []struct {
		path     string
		required bool
	}{
		{path: filepath.Join(configRoot, "config.json")},
		{path: metadataPath, required: true},
		{path: filepath.Join(configRoot, "contexts", "tls", hex.EncodeToString(contextID[:]), "docker")},
	}
	hash := sha256.New()
	_, _ = io.WriteString(hash, "chora-exact-docker-authority-v1\x00"+contextName+"\x00")
	for _, input := range inputs {
		if err := hashExactDockerAuthorityInput(hash, configRoot, input.path, input.required); err != nil {
			return nil, identity, "", "", err
		}
	}
	endpoint, endpointDigest, err := readExactDockerContextEndpoint(metadataPath, contextName)
	if err != nil {
		return nil, identity, "", "", err
	}
	copy(identity[:], hash.Sum(nil))
	return rootInfo, identity, endpoint, endpointDigest, nil
}

func hashExactDockerAuthorityInput(hash io.Writer, root, path string, required bool) error {
	if err := validateExactDockerAuthorityAncestors(root, path); err != nil {
		if errors.Is(err, os.ErrNotExist) && !required {
			relative, _ := filepath.Rel(root, path)
			_, _ = io.WriteString(hash, "absent\x00"+filepath.ToSlash(relative)+"\x00")
			return nil
		}
		return err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) && !required {
		relative, _ := filepath.Rel(root, path)
		_, _ = io.WriteString(hash, "absent\x00"+filepath.ToSlash(relative)+"\x00")
		return nil
	}
	if err != nil {
		return errors.New("explicit Docker configuration input unavailable")
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("explicit Docker configuration symbolic links are forbidden")
	}
	if info.IsDir() {
		return filepath.WalkDir(path, func(entryPath string, _ os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return errors.New("walk explicit Docker configuration failed")
			}
			return hashExactDockerAuthorityEntry(hash, root, entryPath)
		})
	}
	return hashExactDockerAuthorityEntry(hash, root, path)
}

func validateExactDockerAuthorityAncestors(root, path string) error {
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("explicit Docker configuration input escaped its root")
	}
	current := root
	parts := strings.Split(filepath.Clean(relative), string(filepath.Separator))
	for _, part := range parts[:len(parts)-1] {
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) {
			return os.ErrNotExist
		}
		if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("explicit Docker configuration ancestor unavailable")
		}
	}
	return nil
}

func hashExactDockerAuthorityEntry(hash io.Writer, root, path string) error {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
		return errors.New("explicit Docker configuration entry is unsafe")
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("explicit Docker configuration entry escaped its root")
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
		return errors.New("read explicit Docker configuration entry failed")
	}
	defer file.Close()
	if _, err := io.Copy(hash, file); err != nil {
		return errors.New("hash explicit Docker configuration entry failed")
	}
	_, _ = io.WriteString(hash, "\x00")
	return nil
}

func readExactDockerContextEndpoint(metadataPath, contextName string) (string, string, error) {
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		return "", "", errors.New("read explicit Docker context metadata failed")
	}
	var metadata struct {
		Name      string `json:"Name"`
		Endpoints struct {
			Docker struct {
				Host string `json:"Host"`
			} `json:"docker"`
		} `json:"Endpoints"`
	}
	if json.Unmarshal(data, &metadata) != nil || metadata.Name != contextName {
		return "", "", errors.New("explicit Docker context metadata is invalid")
	}
	endpoint := strings.TrimSpace(metadata.Endpoints.Docker.Host)
	digest, err := dockersupervisor.DigestContextEndpoint(endpoint)
	if err != nil {
		return "", "", errors.New("explicit Docker context endpoint is invalid")
	}
	return endpoint, digest, nil
}

func (runner *exactDockerCLIRunner) command(ctx context.Context, command dockersupervisor.Command) (*exec.Cmd, error) {
	if runner == nil {
		return nil, errors.New("exact Docker CLI unavailable")
	}
	info, digest, err := exactExecutableIdentity(runner.executable)
	if err != nil || !os.SameFile(info, runner.executableInfo) || digest != runner.executableHash {
		return nil, errors.New("exact Docker CLI identity changed")
	}
	rootInfo, configHash, endpoint, endpointDigest, err := exactDockerAuthorityIdentity(runner.configRoot, runner.contextName)
	if err != nil || !os.SameFile(rootInfo, runner.configRootInfo) || configHash != runner.configHash ||
		endpoint != runner.endpoint || endpointDigest != runner.endpointDigest {
		return nil, errors.New("explicit Docker context endpoint changed")
	}
	global := []string{"--host", runner.endpoint}
	if len(command.Args) > 0 && command.Args[0] == "context" {
		global = []string{"--context", runner.contextName}
	}
	args := append(global, command.Args...)
	process := exec.CommandContext(ctx, runner.executable, args...)
	process.Env = []string{"DOCKER_CONFIG=" + runner.configRoot}
	return process, nil
}

func (runner *exactDockerCLIRunner) Run(ctx context.Context, command dockersupervisor.Command) (dockersupervisor.CommandResult, error) {
	process, err := runner.command(ctx, command)
	if err != nil {
		return dockersupervisor.CommandResult{ExitCode: -1}, err
	}
	process.Stdin = command.Stdin
	var stdout, stderr bytes.Buffer
	process.Stdout, process.Stderr = &stdout, &stderr
	err = process.Run()
	result := dockersupervisor.CommandResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), ExitCode: -1}
	if process.ProcessState != nil {
		result.ExitCode = process.ProcessState.ExitCode()
	}
	return result, err
}

func (runner *exactDockerCLIRunner) Start(ctx context.Context, command dockersupervisor.Command) (dockersupervisor.Process, error) {
	process, err := runner.command(ctx, command)
	if err != nil {
		return nil, err
	}
	process.Stdout, process.Stderr = command.Stdout, command.Stderr
	stdin, err := process.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := process.Start(); err != nil {
		_ = stdin.Close()
		return nil, err
	}
	return &exactDockerProcess{process: process, stdin: stdin}, nil
}

type exactDockerProcess struct {
	process *exec.Cmd
	stdin   io.WriteCloser
}

func (process *exactDockerProcess) Write(data []byte) (int, error) { return process.stdin.Write(data) }
func (process *exactDockerProcess) Close() error                   { return process.stdin.Close() }
func (process *exactDockerProcess) Kill() error                    { return process.process.Process.Kill() }
func (process *exactDockerProcess) Wait() (int, error) {
	err := process.process.Wait()
	if process.process.ProcessState == nil {
		return -1, err
	}
	return process.process.ProcessState.ExitCode(), err
}
