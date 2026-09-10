package trustedhost

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/Yangyang96/chora/internal/execution"
)

const (
	manifestSchema           = "chora.trusted-host-process.v2"
	directManifestSchema     = "chora.trusted-host-process.v3"
	directArgvManifestSchema = "chora.trusted-host-process.v4"
	manifestOwner            = "chora:internal/trustedhost"
	manifestName             = "process.json"
	attemptPrefix            = "attempt-"
	manifestLimit            = 64 << 10
	directWorkspaceMode      = "direct"
)

type manifestState string

const (
	statePrepared  manifestState = "prepared"
	stateRunning   manifestState = "running"
	stateTerminal  manifestState = "terminal"
	stateUncertain manifestState = "uncertain"
)

type processManifest struct {
	Schema                   string        `json:"schema"`
	Owner                    string        `json:"owner"`
	State                    manifestState `json:"state"`
	Handle                   string        `json:"handle"`
	Identity                 string        `json:"identity,omitempty"`
	LaunchToken              string        `json:"launch_token"`
	PID                      int           `json:"pid,omitempty"`
	PGID                     int           `json:"pgid,omitempty"`
	Birth                    string        `json:"birth,omitempty"`
	Workspace                string        `json:"workspace"`
	WorkspaceMode            string        `json:"workspace_mode,omitempty"`
	OutputDirectory          string        `json:"output_directory"`
	StdoutPath               string        `json:"stdout_path"`
	StderrPath               string        `json:"stderr_path"`
	Executable               string        `json:"executable"`
	ResolvedExecutable       string        `json:"resolved_executable"`
	ExecutableSHA256         string        `json:"executable_sha256"`
	ArgumentDigest           string        `json:"argument_digest"`
	Arguments                []string      `json:"arguments,omitempty"`
	EnvironmentDigest        string        `json:"environment_digest"`
	AmbientEnvironmentDigest string        `json:"ambient_environment_digest"`
	RuntimeClosureDigest     string        `json:"runtime_closure_digest"`
	LaunchContractDigest     string        `json:"launch_contract_digest"`
	GateProtocol             string        `json:"gate_protocol"`
	GateReceiptPath          string        `json:"gate_receipt_path"`
	GateNonce                string        `json:"gate_nonce"`
	GateOwnerPID             int           `json:"gate_owner_pid"`
	GateOwnerBirth           string        `json:"gate_owner_birth"`
	ExitCode                 int           `json:"exit_code"`
	OutputLimitExceeded      bool          `json:"output_limit_exceeded,omitempty"`
	TerminalProof            bool          `json:"terminal_proof"`
	Diagnostic               string        `json:"diagnostic,omitempty"`
}

var environmentKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

var terminalEnvironment = map[string]struct {
	logical string
	guest   string
	name    string
}{
	"CHORA_RESULT_PATH":           {logical: "result", guest: "/output/result.json", name: "result.json"},
	"CHORA_PATCH_PATH":            {logical: "patch", guest: "/output/patch.diff", name: "patch.diff"},
	"CHORA_CHECKS_PATH":           {logical: "checks", guest: "/output/checks.json", name: "checks.json"},
	"CHORA_RUNTIME_IDENTITY_PATH": {logical: "runtime_identity", guest: "/output/runtime-identity.json", name: "runtime-identity.json"},
}

func ensurePrivateRoot(path string) (string, error) {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || !filepath.IsAbs(resolved) {
		return "", errors.New("resolve runtime root")
	}
	info, err := os.Lstat(resolved)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("runtime root must be a private real directory")
	}
	return resolved, nil
}

func validateArguments(arguments []string) error {
	for _, argument := range arguments {
		if strings.IndexByte(argument, 0) >= 0 {
			return errors.New("invocation argument contains NUL")
		}
	}
	return nil
}

func validateEnvironment(environment map[string]string) error {
	for key, value := range environment {
		if !environmentKey.MatchString(key) || strings.IndexByte(key, 0) >= 0 || strings.IndexByte(value, 0) >= 0 {
			return errors.New("invocation environment is malformed")
		}
		if forbiddenRuntimeEnvironment(key) {
			return fmt.Errorf("invocation environment contains forbidden Runtime injection key %s", key)
		}
	}
	for key, output := range terminalEnvironment {
		if environment[key] != output.guest {
			return fmt.Errorf("invocation environment %s must be the frozen %s path", key, output.guest)
		}
	}
	return nil
}

func captureAmbientEnvironment(entries []string) ([]string, error) {
	seen := make(map[string]struct{}, len(entries))
	filtered := make(map[string]string, len(entries))
	for _, entry := range entries {
		separator := strings.IndexByte(entry, '=')
		if separator <= 0 || strings.IndexByte(entry, 0) >= 0 {
			return nil, errors.New("entry is malformed")
		}
		key, value := entry[:separator], entry[separator+1:]
		if !environmentKey.MatchString(key) || strings.IndexByte(value, 0) >= 0 {
			return nil, errors.New("entry is malformed")
		}
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("duplicate key %s", key)
		}
		seen[key] = struct{}{}
		if forbiddenRuntimeEnvironment(key) {
			return nil, fmt.Errorf("forbidden Runtime injection key %s", key)
		}
		upper := strings.ToUpper(key)
		if strings.HasPrefix(upper, "CHORA_") || upper == "PWD" {
			continue
		}
		filtered[key] = value
	}
	keys := make([]string, 0, len(filtered))
	for key := range filtered {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+filtered[key])
	}
	return result, nil
}

func pathsOverlap(first, second string) bool {
	first = filepath.Clean(first)
	second = filepath.Clean(second)
	if first == second {
		return true
	}
	return strings.HasPrefix(first, second+string(filepath.Separator)) || strings.HasPrefix(second, first+string(filepath.Separator))
}

func (supervisor *Supervisor) prepare(ctx context.Context, invocation execution.Invocation, sink execution.RuntimeSink) (*processRecord, processSpec, *os.File, *os.File, error) {
	launch := invocation.LaunchToken()
	digest := launchDigest(launch)
	root := filepath.Join(supervisor.config.RuntimeRoot, attemptPrefix+digest)
	if err := os.Mkdir(root, 0o700); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return nil, processSpec{}, nil, nil, fmt.Errorf("%w: launch token %s", errLaunchStateExists, launchDigest(launch))
		}
		return nil, processSpec{}, nil, nil, fmt.Errorf("create private attempt root: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(root)
		}
	}()
	workspace := invocation.WorkingRoot()
	if supervisor.config.DirectWorkingRoot {
		resolvedWorkspace, err := filepath.EvalSymlinks(workspace)
		if err != nil || !filepath.IsAbs(resolvedWorkspace) {
			return nil, processSpec{}, nil, nil, errors.New("resolve direct execution workspace")
		}
		workspace = filepath.Clean(resolvedWorkspace)
	} else {
		workspace = filepath.Join(root, "workspace")
		if err := copyWorkspace(ctx, invocation.WorkingRoot(), workspace); err != nil {
			return nil, processSpec{}, nil, nil, fmt.Errorf("copy fresh execution workspace: %w", err)
		}
	}
	outputDir := filepath.Join(root, "output")
	if err := os.Mkdir(outputDir, 0o700); err != nil {
		return nil, processSpec{}, nil, nil, fmt.Errorf("create private output directory: %w", err)
	}
	stdoutPath := filepath.Join(root, "stdout.log")
	stderrPath := filepath.Join(root, "stderr.log")
	stdoutFile, err := os.OpenFile(stdoutPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, processSpec{}, nil, nil, fmt.Errorf("create stdout stream: %w", err)
	}
	stderrFile, err := os.OpenFile(stderrPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		_ = stdoutFile.Close()
		return nil, processSpec{}, nil, nil, fmt.Errorf("create stderr stream: %w", err)
	}
	environment, terminal, err := nativeEnvironment(supervisor.config.ambientEnvironment, invocation.Environment(), outputDir)
	if err != nil {
		_ = stdoutFile.Close()
		_ = stderrFile.Close()
		return nil, processSpec{}, nil, nil, err
	}
	arguments := invocation.Arguments()
	gateNonce, err := randomGateNonce()
	if err != nil {
		_ = stdoutFile.Close()
		_ = stderrFile.Close()
		return nil, processSpec{}, nil, nil, fmt.Errorf("create launch gate nonce: %w", err)
	}
	gateReceiptPath := filepath.Join(root, "gate-receipt.json")
	gateOwnerPID := os.Getpid()
	gateOwnerBirth, err := processBirthIdentity(gateOwnerPID)
	if err != nil || gateOwnerBirth == "" {
		_ = stdoutFile.Close()
		_ = stderrFile.Close()
		return nil, processSpec{}, nil, nil, fmt.Errorf("capture launch gate owner identity: %w", err)
	}
	argumentDigest := digestStrings(arguments)
	environmentDigest := digestStrings(environment)
	runtimeClosureDigest := hex.EncodeToString(supervisor.config.runtimeClosureIdentity[:])
	launchContractDigest := digestLaunchContract(supervisor.config.AllowedExecutable, argumentDigest, environmentDigest, runtimeClosureDigest)
	if supervisor.config.DirectWorkingRoot {
		launchContractDigest = digestDirectLaunchContract(launchContractDigest, workspace)
	}
	record := &processRecord{
		root: root, workspace: workspace, outputDir: outputDir, stdoutPath: stdoutPath, stderrPath: stderrPath,
		gateReceiptPath: gateReceiptPath, gateNonce: gateNonce, launchContractDigest: launchContractDigest,
		ambientEnvironmentDigest: supervisor.config.ambientEnvironmentDigest,
		runtimeClosureDigest:     runtimeClosureDigest,
		gateOwnerPID:             gateOwnerPID, gateOwnerBirth: gateOwnerBirth,
		handle: execution.RuntimeHandle{Value: "trustedhost:" + digest}, launch: launch, terminal: terminal,
		sink: sink, done: make(chan struct{}), exitCode: -1, state: statePrepared,
		directWorkspace: supervisor.config.DirectWorkingRoot,
		drained:         map[execution.StreamKind]bool{}, notified: map[execution.StreamKind]int64{},
	}
	manifest := manifestFor(record)
	manifest.Executable = supervisor.config.AllowedExecutable.Path
	manifest.ResolvedExecutable = supervisor.config.AllowedExecutable.ResolvedPath
	manifest.ExecutableSHA256 = hex.EncodeToString(supervisor.config.AllowedExecutable.SHA256[:])
	manifest.ArgumentDigest = argumentDigest
	if supervisor.config.DirectWorkingRoot {
		manifest.Schema = directArgvManifestSchema
		manifest.Arguments = append([]string(nil), arguments...)
	}
	manifest.EnvironmentDigest = environmentDigest
	manifest.AmbientEnvironmentDigest = supervisor.config.ambientEnvironmentDigest
	manifest.RuntimeClosureDigest = runtimeClosureDigest
	manifest.LaunchContractDigest = launchContractDigest
	manifest.GateProtocol = gateProtocol
	manifest.GateReceiptPath = gateReceiptPath
	manifest.GateNonce = gateNonce
	manifest.GateOwnerPID = gateOwnerPID
	manifest.GateOwnerBirth = gateOwnerBirth
	if err := writeManifest(root, manifest); err != nil {
		_ = stdoutFile.Close()
		_ = stderrFile.Close()
		return nil, processSpec{}, nil, nil, fmt.Errorf("persist launch intent: %w", err)
	}
	cleanup = false
	return record, processSpec{
		ExecutableIdentity: supervisor.config.AllowedExecutable, Arguments: append([]string(nil), arguments...),
		Environment: append([]string(nil), environment...), Directory: workspace,
		Stdin: invocation.Stdin(), Stdout: stdoutFile, Stderr: stderrFile,
		FilterPiProgress: supervisor.config.AllowedAdapterID == "pi",
		GateReceiptPath:  gateReceiptPath, GateNonce: gateNonce, GateWait: supervisor.config.GateWait,
	}, stdoutFile, stderrFile, nil
}

func randomGateNonce() (string, error) {
	var nonce [32]byte
	if _, err := io.ReadFull(rand.Reader, nonce[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(nonce[:]), nil
}

func nativeEnvironment(ambient []string, environment map[string]string, outputDir string) ([]string, execution.TerminalFiles, error) {
	clone := make(map[string]string, len(ambient)+len(environment))
	terminal := execution.TerminalFiles{Paths: make(map[string]string, len(terminalEnvironment)), ExitCode: -1}
	for _, entry := range ambient {
		separator := strings.IndexByte(entry, '=')
		if separator <= 0 {
			return nil, execution.TerminalFiles{}, errors.New("captured ambient environment is malformed")
		}
		clone[entry[:separator]] = entry[separator+1:]
	}
	for key, value := range environment {
		clone[key] = value
	}
	for key, output := range terminalEnvironment {
		if clone[key] != output.guest {
			return nil, execution.TerminalFiles{}, fmt.Errorf("invalid frozen terminal path for %s", key)
		}
		hostPath := filepath.Join(outputDir, output.name)
		clone[key] = hostPath
		terminal.Paths[output.logical] = hostPath
	}
	if _, present := clone["PWD"]; present {
		delete(clone, "PWD")
	}
	keys := make([]string, 0, len(clone))
	for key := range clone {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+clone[key])
	}
	return result, terminal, nil
}

func copyWorkspace(ctx context.Context, source, destination string) error {
	if err := os.Mkdir(destination, 0o700); err != nil {
		return err
	}
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		target := filepath.Join(destination, relative)
		if entry.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		if entry.IsDir() {
			return os.Mkdir(target, 0o700)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported workspace entry %s", relative)
		}
		mode := fs.FileMode(0o600)
		if info.Mode().Perm()&0o111 != 0 {
			mode = 0o700
		}
		return copyRegularFile(path, target, mode)
	})
}

func copyRegularFile(source, destination string, mode fs.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	syncErr := output.Sync()
	closeErr := output.Close()
	return errors.Join(copyErr, syncErr, closeErr)
}

func launchDigest(launch execution.LaunchToken) string {
	digest := sha256.Sum256([]byte("chora.trusted-host.launch.v1\x00" + launch.Value))
	return hex.EncodeToString(digest[:])
}

func digestStrings(values []string) string {
	hash := sha256.New()
	for _, value := range values {
		_, _ = io.WriteString(hash, fmt.Sprintf("%d:", len(value)))
		_, _ = io.WriteString(hash, value)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func digestEnvironment(domain string, environment []string) string {
	values := make([]string, 0, len(environment)+1)
	values = append(values, domain)
	values = append(values, environment...)
	return digestStrings(values)
}

func digestLaunchContract(executable ExecutableIdentity, argumentDigest, environmentDigest, runtimeClosureDigest string) string {
	values := []string{
		"chora.trusted-host.launch-contract.v2",
		executable.Path,
		executable.ResolvedPath,
		hex.EncodeToString(executable.SHA256[:]),
		argumentDigest,
		environmentDigest,
		runtimeClosureDigest,
	}
	return digestStrings(values)
}

func digestDirectLaunchContract(baseDigest, workspace string) string {
	return digestStrings([]string{"chora.trusted-host.direct-launch-contract.v1", baseDigest, workspace})
}

func processIdentity(record *processRecord) execution.ProcessIdentity {
	canonical := fmt.Sprintf("chora.trusted-host.process.v1\x00%d\x00%d\x00%s\x00%s", record.pid, record.pgid, record.birth, launchDigest(record.launch))
	digest := sha256.Sum256([]byte(canonical))
	return execution.ProcessIdentity{Value: fmt.Sprintf("trustedhost:v1:%d:%d:%s", record.pid, record.pgid, hex.EncodeToString(digest[:]))}
}

func manifestFor(record *processRecord) processManifest {
	record.mu.Lock()
	defer record.mu.Unlock()
	schema, workspaceMode := manifestSchema, ""
	if record.directWorkspace {
		schema, workspaceMode = directManifestSchema, directWorkspaceMode
	}
	return processManifest{
		Schema: schema, Owner: manifestOwner, State: record.state,
		Handle: record.handle.Value, Identity: record.identity.Value, LaunchToken: record.launch.Value,
		PID: record.pid, PGID: record.pgid, Birth: record.birth, Workspace: record.workspace, WorkspaceMode: workspaceMode,
		OutputDirectory: record.outputDir, StdoutPath: record.stdoutPath, StderrPath: record.stderrPath,
		AmbientEnvironmentDigest: record.ambientEnvironmentDigest,
		RuntimeClosureDigest:     record.runtimeClosureDigest,
		LaunchContractDigest:     record.launchContractDigest, GateProtocol: gateProtocol,
		GateReceiptPath: record.gateReceiptPath, GateNonce: record.gateNonce,
		GateOwnerPID: record.gateOwnerPID, GateOwnerBirth: record.gateOwnerBirth,
		OutputLimitExceeded: record.outputLimitExceeded,
		ExitCode:            record.exitCode, TerminalProof: record.terminalProof, Diagnostic: record.diagnostic,
	}
}

func (supervisor *Supervisor) persist(record *processRecord) error {
	record.persistMu.Lock()
	defer record.persistMu.Unlock()
	current, err := readManifest(record.root)
	if err != nil {
		return err
	}
	next := manifestFor(record)
	next.Executable = current.Executable
	next.ResolvedExecutable = current.ResolvedExecutable
	next.ExecutableSHA256 = current.ExecutableSHA256
	next.ArgumentDigest = current.ArgumentDigest
	next.Arguments = append([]string(nil), current.Arguments...)
	if current.Schema == directArgvManifestSchema {
		next.Schema = directArgvManifestSchema
	}
	next.EnvironmentDigest = current.EnvironmentDigest
	next.AmbientEnvironmentDigest = current.AmbientEnvironmentDigest
	next.RuntimeClosureDigest = current.RuntimeClosureDigest
	next.LaunchContractDigest = current.LaunchContractDigest
	next.GateProtocol = current.GateProtocol
	next.GateReceiptPath = current.GateReceiptPath
	next.GateNonce = current.GateNonce
	next.GateOwnerPID = current.GateOwnerPID
	next.GateOwnerBirth = current.GateOwnerBirth
	return writeManifest(record.root, next)
}

func writeManifest(root string, manifest processManifest) error {
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if len(encoded) > manifestLimit {
		return errors.New("trusted-host process manifest exceeds limit")
	}
	temporary, err := os.CreateTemp(root, ".process-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, filepath.Join(root, manifestName)); err != nil {
		return err
	}
	directory, err := os.Open(root)
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	return errors.Join(syncErr, closeErr)
}

func readManifest(root string) (processManifest, error) {
	path := filepath.Join(root, manifestName)
	linkInfo, err := os.Lstat(path)
	if err != nil || linkInfo.Mode()&os.ModeSymlink != 0 || !linkInfo.Mode().IsRegular() {
		return processManifest{}, errors.New("trusted-host process manifest is not a real regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return processManifest{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > manifestLimit {
		return processManifest{}, errors.New("trusted-host process manifest is not a bounded private regular file")
	}
	decoder := json.NewDecoder(io.LimitReader(file, manifestLimit+1))
	decoder.DisallowUnknownFields()
	var manifest processManifest
	if err := decoder.Decode(&manifest); err != nil {
		return processManifest{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return processManifest{}, errors.New("trusted-host process manifest has trailing data")
	}
	return manifest, nil
}

func (supervisor *Supervisor) loadRecords() error {
	entries, err := os.ReadDir(supervisor.config.RuntimeRoot)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), attemptPrefix) {
			continue
		}
		root := filepath.Join(supervisor.config.RuntimeRoot, entry.Name())
		if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
			return fmt.Errorf("%w: owned attempt entry is not a real directory", ErrRecoveryUncertain)
		}
		if info, err := entry.Info(); err != nil || info.Mode().Perm()&0o077 != 0 {
			return fmt.Errorf("%w: owned attempt entry is not private", ErrRecoveryUncertain)
		}
		manifest, err := readManifest(root)
		if err != nil {
			return fmt.Errorf("%w: read %s: %v", ErrRecoveryUncertain, entry.Name(), err)
		}
		manifestExecutable, executableErr := executableIdentityFromManifest(manifest)
		argvValid := manifest.ArgumentDigest == digestStrings(supervisor.config.AllowedArguments)
		if manifest.Schema == directArgvManifestSchema {
			argvValid = validateAllowedArguments(manifest.Arguments, supervisor.config.AllowedArguments, supervisor.config.AllowedTrailingPathRoot, supervisor.config.AllowedObserverPath) == nil && manifest.ArgumentDigest == digestStrings(manifest.Arguments)
		}
		if executableErr != nil || manifestExecutable != supervisor.config.AllowedExecutable || !argvValid {
			return fmt.Errorf("%w: durable launch contract is outside configured executable/argv policy", ErrRecoveryUncertain)
		}
		// A terminal manifest is historical death evidence. The current
		// environment cannot affect a process already proved dead; preserve
		// its original digest for ownership checks during drain/finalization.
		// Live/prepared/uncertain records still require exact environment.
		if manifest.AmbientEnvironmentDigest != supervisor.config.ambientEnvironmentDigest && !(manifest.State == stateTerminal && manifest.TerminalProof) {
			return fmt.Errorf("%w: durable ambient environment policy drift", ErrRecoveryUncertain)
		}
		if manifest.RuntimeClosureDigest != hex.EncodeToString(supervisor.config.runtimeClosureIdentity[:]) {
			return fmt.Errorf("%w: durable Runtime closure policy drift", ErrRecoveryUncertain)
		}
		record, err := recordFromManifest(root, manifest, supervisor.config.DirectWorkingRoot, supervisor.config.RuntimeRoot)
		if err != nil {
			return fmt.Errorf("%w: validate %s: %v", ErrRecoveryUncertain, entry.Name(), err)
		}
		if manifest.State == statePrepared {
			// A release is issued only after an atomic running-manifest rename.
			// Therefore a valid prepared manifest proves that any launcher inherited
			// only the now-closed parent pipe and can never exec the target.
			if err := provePreparedGateCannotExecute(root, manifest); err != nil {
				return fmt.Errorf("%w: prove prepared gate %s: %v", ErrRecoveryUncertain, entry.Name(), err)
			}
			if err := removeOwnedPrepared(record); err != nil {
				return fmt.Errorf("%w: remove inert prepared state %s: %v", ErrRecoveryUncertain, entry.Name(), err)
			}
			continue
		}
		if supervisor.byHandle[record.handle.Value] != nil || supervisor.byLaunch[record.launch.Value] != nil || record.identity.Valid() && supervisor.byIdentity[record.identity.Value] != nil {
			return fmt.Errorf("%w: duplicate durable process identity", ErrRecoveryUncertain)
		}
		supervisor.byHandle[record.handle.Value] = record
		supervisor.byLaunch[record.launch.Value] = record
		if record.identity.Valid() {
			supervisor.byIdentity[record.identity.Value] = record
		}
	}
	return nil
}

func recordFromManifest(root string, manifest processManifest, directWorkingRoot bool, runtimeRoot string) (*processRecord, error) {
	launch := execution.LaunchToken{Value: manifest.LaunchToken}
	expectedRootName := attemptPrefix + launchDigest(launch)
	workspace := filepath.Join(root, "workspace")
	expectedSchema, expectedWorkspaceMode := manifestSchema, ""
	if directWorkingRoot {
		expectedSchema, expectedWorkspaceMode = directArgvManifestSchema, directWorkspaceMode
		workspace = manifest.Workspace
		if !validDirectWorkspace(workspace, runtimeRoot) {
			return nil, errors.New("direct workspace is unavailable or unsafe")
		}
	}
	outputDir := filepath.Join(root, "output")
	stdoutPath := filepath.Join(root, "stdout.log")
	stderrPath := filepath.Join(root, "stderr.log")
	expectedHandle := "trustedhost:" + launchDigest(launch)
	validSchema := manifest.Schema == expectedSchema
	if directWorkingRoot && manifest.Schema == directManifestSchema && len(manifest.Arguments) == 0 {
		validSchema = true
	}
	if !validSchema || manifest.WorkspaceMode != expectedWorkspaceMode || manifest.Owner != manifestOwner || !launch.Valid() || filepath.Base(root) != expectedRootName ||
		manifest.Handle != expectedHandle || manifest.Workspace != workspace || manifest.OutputDirectory != outputDir ||
		manifest.StdoutPath != stdoutPath || manifest.StderrPath != stderrPath || manifest.Executable == "" || manifest.ResolvedExecutable == "" ||
		manifest.ExecutableSHA256 == "" || manifest.ArgumentDigest == "" || manifest.EnvironmentDigest == "" || manifest.AmbientEnvironmentDigest == "" || manifest.RuntimeClosureDigest == "" || manifest.LaunchContractDigest == "" ||
		manifest.GateProtocol != gateProtocol || manifest.GateReceiptPath != filepath.Join(root, "gate-receipt.json") || manifest.GateNonce == "" ||
		manifest.GateOwnerPID <= 0 || manifest.GateOwnerBirth == "" {
		return nil, errors.New("manifest ownership or derived paths mismatch")
	}
	executable, err := executableIdentityFromManifest(manifest)
	expectedLaunchDigest := digestLaunchContract(executable, manifest.ArgumentDigest, manifest.EnvironmentDigest, manifest.RuntimeClosureDigest)
	if directWorkingRoot {
		expectedLaunchDigest = digestDirectLaunchContract(expectedLaunchDigest, workspace)
	}
	if err != nil || !validSHA256Digest(manifest.RuntimeClosureDigest) || expectedLaunchDigest != manifest.LaunchContractDigest {
		return nil, errors.New("manifest launch contract digest mismatch")
	}
	if manifest.State != statePrepared && manifest.State != stateRunning && manifest.State != stateTerminal && manifest.State != stateUncertain {
		return nil, errors.New("invalid manifest state")
	}
	identity := execution.ProcessIdentity{Value: manifest.Identity}
	if manifest.State == stateRunning {
		if manifest.PID <= 0 || manifest.PGID != manifest.PID || manifest.Birth == "" || !identity.Valid() {
			return nil, errors.New("running manifest lacks exact process identity")
		}
	}
	record := &processRecord{
		root: root, workspace: workspace, outputDir: outputDir, stdoutPath: stdoutPath, stderrPath: stderrPath,
		gateReceiptPath: manifest.GateReceiptPath, gateNonce: manifest.GateNonce, launchContractDigest: manifest.LaunchContractDigest,
		ambientEnvironmentDigest: manifest.AmbientEnvironmentDigest,
		runtimeClosureDigest:     manifest.RuntimeClosureDigest,
		gateOwnerPID:             manifest.GateOwnerPID, gateOwnerBirth: manifest.GateOwnerBirth,
		handle: execution.RuntimeHandle{Value: manifest.Handle}, identity: identity, launch: launch,
		terminal: terminalFiles(outputDir), pid: manifest.PID, pgid: manifest.PGID, birth: manifest.Birth,
		recovered: true, done: make(chan struct{}), leaderReaped: manifest.TerminalProof,
		outputLimitExceeded: manifest.OutputLimitExceeded,
		exitCode:            manifest.ExitCode, terminalProof: manifest.TerminalProof, state: manifest.State,
		terminalPersisted: manifest.TerminalProof,
		diagnostic:        manifest.Diagnostic, directWorkspace: directWorkingRoot,
		drained: map[execution.StreamKind]bool{}, notified: map[execution.StreamKind]int64{},
	}
	if identity.Valid() && identity != processIdentity(record) {
		return nil, errors.New("process identity digest mismatch")
	}
	if manifest.TerminalProof != (manifest.State == stateTerminal) {
		return nil, errors.New("terminal state/proof mismatch")
	}
	if record.terminalProof {
		record.doneOnce.Do(func() { close(record.done) })
	}
	return record, nil
}

func validDirectWorkspace(workspace, runtimeRoot string) bool {
	if workspace == "" || !filepath.IsAbs(workspace) || filepath.Clean(workspace) != workspace || pathsOverlap(workspace, runtimeRoot) {
		return false
	}
	info, err := os.Lstat(workspace)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	resolved, err := filepath.EvalSymlinks(workspace)
	return err == nil && filepath.Clean(resolved) == workspace
}

func executableIdentityFromManifest(manifest processManifest) (ExecutableIdentity, error) {
	bytes, err := hex.DecodeString(manifest.ExecutableSHA256)
	if err != nil || len(bytes) != sha256.Size || !filepath.IsAbs(manifest.Executable) || !filepath.IsAbs(manifest.ResolvedExecutable) ||
		filepath.Clean(manifest.Executable) != manifest.Executable || filepath.Clean(manifest.ResolvedExecutable) != manifest.ResolvedExecutable {
		return ExecutableIdentity{}, errors.New("invalid executable identity in manifest")
	}
	var digest [sha256.Size]byte
	copy(digest[:], bytes)
	return ExecutableIdentity{Path: manifest.Executable, ResolvedPath: manifest.ResolvedExecutable, SHA256: digest}, nil
}

func validSHA256Digest(encoded string) bool {
	decoded, err := hex.DecodeString(encoded)
	return err == nil && len(decoded) == sha256.Size && encoded == strings.ToLower(encoded)
}

func provePreparedGateCannotExecute(root string, manifest processManifest) error {
	if manifest.State != statePrepared || manifest.PID != 0 || manifest.PGID != 0 || manifest.Birth != "" || manifest.Identity != "" || manifest.TerminalProof ||
		manifest.GateProtocol != gateProtocol || manifest.GateReceiptPath != filepath.Join(root, "gate-receipt.json") || manifest.GateOwnerPID <= 0 || manifest.GateOwnerBirth == "" {
		return errors.New("prepared gate invariant is not intact")
	}
	ownerBirth, err := processBirthIdentity(manifest.GateOwnerPID)
	if err == nil && ownerBirth == manifest.GateOwnerBirth {
		return errors.New("prepared gate release owner is still alive")
	}
	if err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("prove prepared gate release owner absent: %w", err)
	}
	_, err = executableIdentityFromManifest(manifest)
	return err
}

func terminalFiles(outputDir string) execution.TerminalFiles {
	files := execution.TerminalFiles{Paths: make(map[string]string, len(terminalEnvironment)), ExitCode: -1}
	for _, output := range terminalEnvironment {
		files.Paths[output.logical] = filepath.Join(outputDir, output.name)
	}
	return files
}

func verifyOwnedRecord(record *processRecord, runtimeRoot string) error {
	expected := filepath.Join(runtimeRoot, attemptPrefix+launchDigest(record.launch))
	if record.root != expected || filepath.Dir(record.root) != runtimeRoot {
		return errors.New("attempt root escaped configured runtime root")
	}
	info, err := os.Lstat(record.root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return errors.New("attempt root ownership or mode changed")
	}
	manifest, err := readManifest(record.root)
	if err != nil {
		return err
	}
	expectedSchema, expectedWorkspaceMode := manifestSchema, ""
	if record.directWorkspace {
		expectedSchema, expectedWorkspaceMode = directArgvManifestSchema, directWorkspaceMode
		if !validDirectWorkspace(record.workspace, runtimeRoot) {
			return errors.New("direct workspace is unavailable or unsafe")
		}
	}
	validSchema := manifest.Schema == expectedSchema || record.recovered && manifest.Schema == directManifestSchema && len(manifest.Arguments) == 0
	if !validSchema || manifest.WorkspaceMode != expectedWorkspaceMode || manifest.Owner != manifestOwner || manifest.Handle != record.handle.Value || manifest.Identity != record.identity.Value || manifest.LaunchToken != record.launch.Value ||
		manifest.Workspace != record.workspace || manifest.OutputDirectory != record.outputDir || manifest.StdoutPath != record.stdoutPath || manifest.StderrPath != record.stderrPath ||
		manifest.AmbientEnvironmentDigest != record.ambientEnvironmentDigest || manifest.RuntimeClosureDigest != record.runtimeClosureDigest || manifest.LaunchContractDigest != record.launchContractDigest || manifest.GateProtocol != gateProtocol || manifest.GateReceiptPath != record.gateReceiptPath || manifest.GateNonce != record.gateNonce ||
		manifest.GateOwnerPID != record.gateOwnerPID || manifest.GateOwnerBirth != record.gateOwnerBirth ||
		!manifest.TerminalProof || manifest.State != stateTerminal {
		return errors.New("terminal ownership manifest mismatch")
	}
	return nil
}

func removeOwnedPrepared(record *processRecord) error {
	if record == nil || record.root == "" || filepath.Base(record.root) != attemptPrefix+launchDigest(record.launch) {
		return errors.New("prepared state ownership mismatch")
	}
	return os.RemoveAll(record.root)
}
