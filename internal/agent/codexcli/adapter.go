package codexcli

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
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/Yangyang96/chora/internal/agent"
	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/processbound"
	"github.com/Yangyang96/chora/internal/runtimeboundary"
)

const (
	AdapterID                 = "codex"
	CodexExecutable           = "/usr/bin/env"
	InnerCodexExecutable      = "/opt/homebrew/bin/codex"
	EnvironmentExecutable     = "/usr/bin/env"
	SandboxExecutable         = "/usr/bin/sandbox-exec"
	TerminalResultEnvironment = "CHORA_TERMINAL_RESULT_PATH"
	resultFileName            = "result.json"
	requiredCodexVersion      = "0.146.0"
	requiredCodexSHA256       = "ae1d3ffe6d48aec6a4dc3f50e7eb8e0d11962485a6a9406c5a7012139383da02"
	requiredEnvironmentSHA256 = "9eb7c5aed7f3c7fe07b77d9a84d0a7c6a8c68c17a15aa3dace0d8ff02d352776"
)

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type CommandRunner func(context.Context, string, ...string) ([]byte, error)

// Config contains Chora-owned paths. ReadFile, RunCommand, and OSBuild are
// injectable seams for deterministic tests; production callers should leave
// them nil.
type Config struct {
	RepoRoot     string
	RuntimeRoot  string
	SchemaPath   string
	GatePath     string
	ContractPath string
	ManagedPath  string

	ReadFile   func(string) ([]byte, error)
	RunCommand CommandRunner
	OSBuild    func(context.Context) (string, error)

	expectedCodexSHA256       string
	expectedEnvironmentSHA256 string
}

type Adapter struct {
	config    Config
	configErr error
	contract  managedShellContract
}

type writableRootsContract struct {
	ConfigKey         string `json:"config_key"`
	NPMCacheDirectory string `json:"npm_cache_directory"`
	TmpDirectory      string `json:"tmp_directory"`
	PlaceholderShape  string `json:"placeholder_shape"`
}

func (contract writableRootsContract) validate(staticArguments []string) error {
	if contract.ConfigKey != "sandbox_workspace_write.writable_roots" || contract.NPMCacheDirectory != "npm-cache" || contract.TmpDirectory != "tmp" ||
		contract.PlaceholderShape != `["<attempt npm-cache>","<attempt tmp>"]` {
		return errors.New("Codex writable-roots contract mismatch")
	}
	for _, argument := range staticArguments {
		if strings.HasPrefix(argument, contract.ConfigKey+"=") {
			return errors.New("Codex static config contains an unowned writable-roots override")
		}
	}
	return nil
}

func (contract writableRootsContract) render(attemptRoot string) (string, error) {
	if err := contract.validate(nil); err != nil {
		return "", err
	}
	return contract.ConfigKey + "=[" + strconv.Quote(filepath.Join(attemptRoot, contract.NPMCacheDirectory)) + "," + strconv.Quote(filepath.Join(attemptRoot, contract.TmpDirectory)) + "]", nil
}

func New(config Config) (*Adapter, error) {
	adapter := NewAdapter(config)
	if adapter.configErr != nil {
		return nil, adapter.configErr
	}
	return adapter, nil
}

func NewAdapter(config Config) *Adapter {
	config, err := normalizeConfig(config)
	contract := defaultManagedShellContract()
	contract.CodexSHA256 = config.expectedCodexSHA256
	contract.EnvironmentSHA256 = config.expectedEnvironmentSHA256
	return &Adapter{config: config, configErr: err, contract: contract}
}

func (adapter *Adapter) ID() string { return AdapterID }

func (adapter *Adapter) Capabilities() execution.AdapterCapabilities {
	return execution.AdapterCapabilities{ResumeMode: execution.ResumeExplicitSession}
}

func (adapter *Adapter) PrepareStart(_ context.Context, request execution.StartRequest) (execution.Invocation, error) {
	if err := adapter.ready(); err != nil {
		return execution.Invocation{}, err
	}
	if !request.Run.ID().Valid() || !request.Attempt.ID().Valid() || request.Attempt.RunID() != request.Run.ID() || !filepath.IsAbs(request.WorkspaceRoot) || len(bytes.TrimSpace(request.SnapshotDocument)) == 0 {
		return execution.Invocation{}, errors.New("invalid Codex start request")
	}
	resultPath, err := adapter.resultPath(request.Run.ID().String(), request.Attempt.ID().String())
	if err != nil {
		return execution.Invocation{}, err
	}
	codexArguments := []string{
		"exec", "--ignore-user-config", "--ignore-rules", "--json",
		"--output-schema", adapter.config.SchemaPath,
		"-o", resultPath,
		"--skip-git-repo-check",
		"-C", request.WorkspaceRoot,
	}
	managedArguments, err := adapter.managedNetworkArguments(filepath.Dir(resultPath))
	if err != nil {
		return execution.Invocation{}, err
	}
	codexArguments = append(codexArguments, managedArguments...)
	codexArguments = append(codexArguments, "-")
	return execution.NewInvocation(execution.InvocationParams{
		AdapterID:   AdapterID,
		Executable:  EnvironmentExecutable,
		Arguments:   adapter.sanitizedCodexArguments(codexArguments),
		Environment: adapter.environment(request.Run.ID().String(), resultPath),
		WorkingRoot: request.WorkspaceRoot,
		Stdin:       startPrompt(request.SnapshotDocument),
		LaunchToken: request.LaunchToken,
	})
}

func (adapter *Adapter) PrepareResume(ctx context.Context, request execution.ResumeRequest) (execution.Invocation, error) {
	if err := adapter.ready(); err != nil {
		return execution.Invocation{}, err
	}
	fingerprint, err := adapter.Fingerprint(ctx)
	if err != nil {
		return execution.Invocation{}, err
	}
	if err := agent.ValidateExplicitResume(adapter.Capabilities(), request, fingerprint); err != nil {
		return execution.Invocation{}, err
	}
	if !request.Run.ID().Valid() || !request.Attempt.ID().Valid() || request.Attempt.RunID() != request.Run.ID() || len(bytes.TrimSpace(request.DeltaInstruction)) == 0 {
		return execution.Invocation{}, errors.New("invalid Codex resume request")
	}
	resultPath, err := adapter.resultPath(request.Run.ID().String(), request.Attempt.ID().String())
	if err != nil {
		return execution.Invocation{}, err
	}
	codexArguments := []string{
		"exec", "resume", "--ignore-user-config", "--ignore-rules", "--json",
		"--output-schema", adapter.config.SchemaPath,
		"-o", resultPath,
		"--skip-git-repo-check",
	}
	managedArguments, err := adapter.managedNetworkArguments(filepath.Dir(resultPath))
	if err != nil {
		return execution.Invocation{}, err
	}
	codexArguments = append(codexArguments, managedArguments...)
	codexArguments = append(codexArguments, request.Binding.ExternalSession, "-")
	return execution.NewInvocation(execution.InvocationParams{
		AdapterID:   AdapterID,
		Executable:  EnvironmentExecutable,
		Arguments:   adapter.sanitizedCodexArguments(codexArguments),
		Environment: adapter.environment(request.Run.ID().String(), resultPath),
		WorkingRoot: request.Binding.WorkingRoot,
		Stdin:       request.DeltaInstruction,
		LaunchToken: request.LaunchToken,
	})
}

func (adapter *Adapter) DecodeEvent(chunk execution.EventChunk) (execution.DecodedChunk, error) {
	consumed := len(chunk.Data)
	if !chunk.EOF {
		lastNewline := bytes.LastIndexByte(chunk.Data, '\n')
		if lastNewline < 0 {
			return execution.DecodedChunk{}, nil
		}
		consumed = lastNewline + 1
	}

	decoded := execution.DecodedChunk{ConsumedBytes: consumed}
	remaining := chunk.Data[:consumed]
	offset := chunk.Offset
	for len(remaining) > 0 {
		line := remaining
		advance := len(remaining)
		if newline := bytes.IndexByte(remaining, '\n'); newline >= 0 {
			line = remaining[:newline]
			advance = newline + 1
		}
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if len(bytes.TrimSpace(line)) > 0 {
			decoded.Events = append(decoded.Events, decodeLine(chunk.Stream, offset, line))
		}
		offset += int64(advance)
		remaining = remaining[advance:]
	}
	return decoded, nil
}

func (adapter *Adapter) DecodeTerminal(files execution.TerminalFiles) (execution.TerminalResult, error) {
	if err := adapter.ready(); err != nil {
		return execution.TerminalResult{}, err
	}
	path := strings.TrimSpace(files.Paths["result"])
	if path == "" {
		return execution.TerminalResult{}, errors.New("Codex terminal result path is required")
	}
	data, err := adapter.config.ReadFile(path)
	if err != nil {
		return execution.TerminalResult{}, fmt.Errorf("read Codex terminal result: %w", err)
	}
	if len(data) > processbound.ResultLimit {
		return execution.TerminalResult{}, processbound.ErrOutputLimit
	}
	result, err := agent.DecodeResult(data)
	if err != nil {
		return execution.TerminalResult{}, err
	}
	return result, nil
}

func (adapter *Adapter) Fingerprint(ctx context.Context) (execution.RuntimeFingerprint, error) {
	if err := adapter.ready(); err != nil {
		return execution.RuntimeFingerprint{}, err
	}
	manifest, err := adapter.loadGate()
	if err != nil {
		return execution.RuntimeFingerprint{}, err
	}
	if err := adapter.verifyGate(ctx, manifest); err != nil {
		return execution.RuntimeFingerprint{}, err
	}

	contentDigest := sha256.Sum256([]byte(adapter.contract.BootstrapContent))
	canonical, err := json.Marshal(managedShellFingerprint{
		Schema:                  adapter.contract.Schema,
		GateManifest:            manifest,
		ManagedPath:             adapter.config.ManagedPath,
		BootstrapFileName:       adapter.contract.BootstrapFileName,
		AttemptDirectoryMode:    uint32(adapter.contract.AttemptDirectoryMode),
		BootstrapFileMode:       uint32(adapter.contract.BootstrapFileMode),
		ManagedEnvironmentKeys:  adapter.contract.EnvironmentKeys.canonical(),
		BootstrapContentSHA256:  hex.EncodeToString(contentDigest[:]),
		ManagedNetworkArguments: append([]string(nil), adapter.contract.ManagedNetworkArguments...),
		ParentEnvironmentUnset:  append([]string(nil), adapter.contract.ParentEnvironmentUnset...),
		EnvironmentExecutable:   adapter.contract.EnvironmentExecutable,
		EnvironmentSHA256:       adapter.contract.EnvironmentSHA256,
		EnvironmentUnsetOption:  adapter.contract.EnvironmentUnsetOption,
		EnvironmentExecReplace:  adapter.contract.EnvironmentExecReplace,
		CodexExecutable:         InnerCodexExecutable,
		CodexSHA256:             adapter.contract.CodexSHA256,
		NPMCacheDirectoryName:   adapter.contract.NPMCacheDirectoryName,
		TmpDirectoryName:        adapter.contract.TmpDirectoryName,
		WritableDirectoryMode:   uint32(adapter.contract.WritableDirectoryMode),
		NPMCacheEnvironmentKey:  adapter.contract.NPMCacheEnvironmentKey,
		TmpEnvironmentKey:       adapter.contract.TmpEnvironmentKey,
		WritableRoots:           adapter.contract.WritableRoots,
	})
	if err != nil {
		return execution.RuntimeFingerprint{}, err
	}
	digest := sha256.Sum256(canonical)
	return execution.RuntimeFingerprint{Digest: digest, Version: manifest.CodexVersion}, nil
}

func (adapter *Adapter) ready() error {
	if adapter == nil {
		return errors.New("Codex adapter is nil")
	}
	return adapter.configErr
}

func (adapter *Adapter) resultPath(runID, attemptID string) (string, error) {
	directory := filepath.Join(adapter.config.RuntimeRoot, "runs", runID, "attempts", attemptID)
	if err := ensurePrivateDirectoryChain(adapter.config.RuntimeRoot, directory, adapter.contract.AttemptDirectoryMode); err != nil {
		return "", err
	}
	if err := ensureManagedBashEnvironment(filepath.Join(directory, adapter.contract.BootstrapFileName), adapter.contract); err != nil {
		return "", err
	}
	for _, name := range []string{adapter.contract.NPMCacheDirectoryName, adapter.contract.TmpDirectoryName} {
		if err := ensureContainedDirectory(directory, filepath.Join(directory, name), adapter.contract.WritableDirectoryMode); err != nil {
			return "", err
		}
	}
	if err := validatePrivateDirectoryChain(adapter.config.RuntimeRoot, directory, adapter.contract.AttemptDirectoryMode); err != nil {
		return "", err
	}
	return filepath.Join(directory, resultFileName), nil
}

func ensurePrivateDirectoryChain(root, directory string, mode os.FileMode) error {
	root, directory = filepath.Clean(root), filepath.Clean(directory)
	relative, err := filepath.Rel(root, directory)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("Codex attempt directory escapes runtime root")
	}
	if err := os.Mkdir(root, mode); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("create Codex runtime root: %w", err)
	}
	if err := validateDirectory(root, mode); err != nil {
		return err
	}
	current := root
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		if err := os.Mkdir(current, mode); err != nil && !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("create Codex attempt ancestor: %w", err)
		}
		if err := validateDirectory(current, mode); err != nil {
			return err
		}
	}
	return validatePrivateDirectoryChain(root, directory, mode)
}

func validatePrivateDirectoryChain(root, directory string, mode os.FileMode) error {
	root, directory = filepath.Clean(root), filepath.Clean(directory)
	relative, err := filepath.Rel(root, directory)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("Codex attempt directory escapes runtime root")
	}
	current := root
	if err := validateDirectory(current, mode); err != nil {
		return err
	}
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		if err := validateDirectory(current, mode); err != nil {
			return err
		}
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	canonicalDirectory, err := filepath.EvalSymlinks(directory)
	if err != nil || canonicalDirectory != filepath.Join(canonicalRoot, relative) {
		return errors.New("Codex attempt ancestor containment mismatch")
	}
	return nil
}

func validateDirectory(path string, mode os.FileMode) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect Codex private directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || strictPermissionMode(info.Mode()) != mode {
		return errors.New("Codex private directory contract mismatch")
	}
	return nil
}

func ensureContainedDirectory(root, directory string, mode os.FileMode) error {
	if err := os.Mkdir(directory, mode); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("create Codex attempt writable directory: %w", err)
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return fmt.Errorf("inspect Codex attempt writable directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || strictPermissionMode(info.Mode()) != mode {
		return errors.New("Codex attempt writable directory contract mismatch")
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("resolve Codex attempt root: %w", err)
	}
	canonicalDirectory, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return fmt.Errorf("resolve Codex attempt writable directory: %w", err)
	}
	relative, err := filepath.Rel(canonicalRoot, canonicalDirectory)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("Codex attempt writable directory escapes attempt root")
	}
	return nil
}

func ensureAttemptDirectory(directory string, mode os.FileMode) error {
	if err := os.MkdirAll(directory, mode); err != nil {
		return fmt.Errorf("create Codex attempt runtime directory: %w", err)
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return fmt.Errorf("inspect Codex attempt runtime directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || strictPermissionMode(info.Mode()) != mode {
		return errors.New("Codex attempt runtime directory contract mismatch")
	}
	return nil
}

func ensureManagedBashEnvironment(path string, contract managedShellContract) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, contract.BootstrapFileMode)
	if err == nil {
		written, writeErr := io.WriteString(file, contract.BootstrapContent)
		closeErr := file.Close()
		if writeErr != nil {
			return fmt.Errorf("write managed Bash environment: %w", writeErr)
		}
		if written != len(contract.BootstrapContent) {
			return errors.New("write managed Bash environment: partial write")
		}
		if closeErr != nil {
			return fmt.Errorf("close managed Bash environment: %w", closeErr)
		}
		return validateManagedBashEnvironment(path, contract)
	}
	if !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("create managed Bash environment: %w", err)
	}
	return validateManagedBashEnvironment(path, contract)
}

func validateManagedBashEnvironment(path string, contract managedShellContract) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect managed Bash environment: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || strictPermissionMode(info.Mode()) != contract.BootstrapFileMode {
		return errors.New("managed Bash environment file contract mismatch")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read managed Bash environment: %w", err)
	}
	if string(content) != contract.BootstrapContent {
		return errors.New("managed Bash environment content mismatch")
	}
	return nil
}

func strictPermissionMode(mode os.FileMode) os.FileMode {
	return mode & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky)
}

func (adapter *Adapter) environment(runID, resultPath string) map[string]string {
	runRoot := filepath.Join(adapter.config.RuntimeRoot, "runs", runID)
	attemptRoot := filepath.Dir(resultPath)
	return map[string]string{
		adapter.contract.EnvironmentKeys.Path:        adapter.config.ManagedPath,
		adapter.contract.EnvironmentKeys.ManagedPath: adapter.config.ManagedPath,
		adapter.contract.EnvironmentKeys.BashEnv:     filepath.Join(filepath.Dir(resultPath), adapter.contract.BootstrapFileName),
		adapter.contract.EnvironmentKeys.Home:        filepath.Join(runRoot, "home"),
		adapter.contract.EnvironmentKeys.CodexHome:   filepath.Join(runRoot, "codex-home"),
		adapter.contract.NPMCacheEnvironmentKey:      filepath.Join(attemptRoot, adapter.contract.NPMCacheDirectoryName),
		adapter.contract.TmpEnvironmentKey:           filepath.Join(attemptRoot, adapter.contract.TmpDirectoryName),
		TerminalResultEnvironment:                    resultPath,
	}
}

func (adapter *Adapter) managedNetworkArguments(attemptRoot string) ([]string, error) {
	if err := adapter.contract.WritableRoots.validate(adapter.contract.ManagedNetworkArguments); err != nil {
		return nil, err
	}
	arguments := append([]string(nil), adapter.contract.ManagedNetworkArguments...)
	rendered, err := adapter.contract.WritableRoots.render(attemptRoot)
	if err != nil {
		return nil, err
	}
	return append(arguments, "-c", rendered), nil
}

func (adapter *Adapter) sanitizedCodexArguments(codexArguments []string) []string {
	arguments := make([]string, 0, 2*len(adapter.contract.ParentEnvironmentUnset)+1+len(codexArguments))
	for _, key := range adapter.contract.ParentEnvironmentUnset {
		arguments = append(arguments, adapter.contract.EnvironmentUnsetOption, key)
	}
	arguments = append(arguments, InnerCodexExecutable)
	return append(arguments, codexArguments...)
}

func startPrompt(snapshot []byte) []byte {
	var prompt bytes.Buffer
	prompt.WriteString("Chora managed agent attempt.\n")
	prompt.WriteString("Treat the following context snapshot as frozen input. Follow it exactly and return the final response using the required output schema.\n")
	prompt.WriteString("Every checks[].criterion_id must exactly match one of the frozen acceptance_criteria[].id values; use only frozen criterion IDs.\n")
	prompt.WriteString("Do not emit extra checks for scope or any other non-criterion concept.\n")
	prompt.WriteString("Put non-criterion observations in unknowns without inventing a criterion_id.\n")
	prompt.WriteString("Emit exactly one check for every frozen acceptance_criteria[].id, in the same order; use UNKNOWN when evidence is insufficient instead of omitting a criterion.\n")
	prompt.WriteString("Terminal semantics: review_ready is true only when handoff.requested is false; handoff.requested is true only when review_ready is false. When both are false, revision is required.\n")
	prompt.WriteString("<chora_frozen_context_snapshot>\n")
	prompt.Write(snapshot)
	if len(snapshot) == 0 || snapshot[len(snapshot)-1] != '\n' {
		prompt.WriteByte('\n')
	}
	prompt.WriteString("</chora_frozen_context_snapshot>\n")
	return prompt.Bytes()
}

func decodeLine(stream execution.StreamKind, offset int64, line []byte) execution.NormalizedEvent {
	var header struct {
		Type     string `json:"type"`
		ThreadID string `json:"thread_id"`
	}
	if err := json.Unmarshal(line, &header); err != nil || strings.TrimSpace(header.Type) == "" {
		normalized, _ := json.Marshal(map[string]any{
			"type":   "adapter.parse_error",
			"stream": stream,
			"offset": offset,
		})
		return execution.NormalizedEvent{
			Type:           "adapter.parse_error",
			NormalizedJSON: normalized,
			RawJSON:        json.RawMessage(`{"redacted":"parse_error"}`),
		}
	}
	raw := append(json.RawMessage(nil), line...)
	return execution.NormalizedEvent{
		Type:            header.Type,
		NormalizedJSON:  raw,
		RawJSON:         raw,
		ExternalSession: header.ThreadID,
	}
}

type gateManifest struct {
	SchemaVersion                      string `json:"schema_version"`
	CodexVersion                       string `json:"codex_version"`
	RealAdapterGate                    string `json:"real_adapter_gate"`
	HostReadIsolation                  string `json:"host_read_isolation"`
	WorkspaceWriteIsolation            string `json:"workspace_write_isolation"`
	ControlPlaneIsolation              string `json:"control_plane_isolation"`
	LoopbackIsolation                  string `json:"loopback_isolation"`
	ProfileTemplateSHA256              string `json:"profile_template_sha256"`
	ProfileRendererVersion             string `json:"profile_renderer_version"`
	ProbeBinarySHA256                  string `json:"probe_binary_sha256"`
	CodexBinarySHA256                  string `json:"codex_binary_sha256"`
	CodexExecutable                    string `json:"codex_executable"`
	SandboxExecSHA256                  string `json:"sandbox_exec_sha256"`
	OSBuild                            string `json:"os_build"`
	ContractSHA256                     string `json:"contract_sha256"`
	ManagedNetworkProxy                string `json:"managed_network_proxy"`
	RegistryEgress                     string `json:"registry_egress"`
	NonAllowlistedEgressIsolation      string `json:"non_allowlisted_egress_isolation"`
	ManagedLoopbackIsolation           string `json:"managed_loopback_isolation"`
	ManagedLocalBindingIsolation       string `json:"managed_local_binding_isolation"`
	UpstreamProxy                      string `json:"upstream_proxy"`
	SOCKS5                             string `json:"socks5"`
	SOCKS5UDP                          string `json:"socks5_udp"`
	NonLoopbackProxy                   string `json:"non_loopback_proxy"`
	AllUnixSockets                     string `json:"all_unix_sockets"`
	UnixSocketAllowlist                string `json:"unix_socket_allowlist"`
	EnvironmentWrapper                 string `json:"environment_wrapper"`
	EnvironmentWrapperSHA256           string `json:"environment_wrapper_sha256"`
	AllowedDestinationPayloadIsolation string `json:"allowed_destination_payload_isolation"`
}

type managedEnvironmentKeys struct {
	Path        string
	ManagedPath string
	BashEnv     string
	Home        string
	CodexHome   string
}

func (keys managedEnvironmentKeys) canonical() [5]string {
	return [5]string{keys.Path, keys.ManagedPath, keys.BashEnv, keys.Home, keys.CodexHome}
}

type managedShellContract struct {
	Schema                  string
	BootstrapFileName       string
	AttemptDirectoryMode    os.FileMode
	BootstrapFileMode       os.FileMode
	EnvironmentKeys         managedEnvironmentKeys
	BootstrapContent        string
	ManagedNetworkArguments []string
	ParentEnvironmentUnset  []string
	EnvironmentExecutable   string
	EnvironmentSHA256       string
	EnvironmentUnsetOption  string
	EnvironmentExecReplace  string
	NPMCacheDirectoryName   string
	TmpDirectoryName        string
	WritableDirectoryMode   os.FileMode
	NPMCacheEnvironmentKey  string
	TmpEnvironmentKey       string
	WritableRoots           writableRootsContract
	CodexSHA256             string
}

func defaultManagedShellContract() managedShellContract {
	return managedShellContract{
		Schema:               "chora.codex-managed-shell.v2",
		BootstrapFileName:    ".chora-managed-bash-env",
		AttemptDirectoryMode: 0o700,
		BootstrapFileMode:    0o600,
		EnvironmentKeys: managedEnvironmentKeys{
			Path: "PATH", ManagedPath: "CHORA_MANAGED_PATH", BashEnv: "BASH_ENV", Home: "HOME", CodexHome: "CODEX_HOME",
		},
		BootstrapContent: "export PATH=\"$CHORA_MANAGED_PATH\"\n",
		ManagedNetworkArguments: []string{
			"--strict-config", "-c", `sandbox_mode="workspace-write"`,
			"-c", "sandbox_workspace_write.network_access=true",
			"-c", "features.network_proxy.enabled=true",
			"-c", `features.network_proxy.mode="full"`,
			"-c", `features.network_proxy.domains={"registry.npmjs.org"="allow"}`,
			"-c", "features.network_proxy.allow_upstream_proxy=false",
			"-c", "features.network_proxy.allow_local_binding=false",
			"-c", "features.network_proxy.enable_socks5=false",
			"-c", "features.network_proxy.enable_socks5_udp=false",
			"-c", "features.network_proxy.dangerously_allow_non_loopback_proxy=false",
			"-c", "features.network_proxy.dangerously_allow_all_unix_sockets=false",
			"-c", "sandbox_workspace_write.exclude_tmpdir_env_var=true",
			"-c", "sandbox_workspace_write.exclude_slash_tmp=true",
		},
		ParentEnvironmentUnset: []string{
			"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy", "FTP_PROXY", "ftp_proxy",
			"WS_PROXY", "ws_proxy", "WSS_PROXY", "wss_proxy", "NO_PROXY", "no_proxy", "YARN_HTTP_PROXY", "YARN_HTTPS_PROXY", "YARN_NO_PROXY",
			"NPM_CONFIG_HTTP_PROXY", "NPM_CONFIG_HTTPS_PROXY", "NPM_CONFIG_PROXY", "NPM_CONFIG_NOPROXY", "npm_config_http_proxy", "npm_config_https_proxy", "npm_config_proxy", "npm_config_noproxy",
			"BUNDLE_HTTP_PROXY", "BUNDLE_HTTPS_PROXY", "BUNDLE_NO_PROXY", "PIP_PROXY", "DOCKER_HTTP_PROXY", "DOCKER_HTTPS_PROXY",
			"CODEX_NETWORK_PROXY_ACTIVE", "CODEX_NETWORK_ALLOW_LOCAL_BINDING", "ELECTRON_GET_USE_PROXY_ENV", "NODE_USE_ENV_PROXY",
		},
		EnvironmentExecutable: EnvironmentExecutable, EnvironmentSHA256: requiredEnvironmentSHA256,
		EnvironmentUnsetOption: "-u", EnvironmentExecReplace: "required",
		NPMCacheDirectoryName: "npm-cache", TmpDirectoryName: "tmp", WritableDirectoryMode: 0o700,
		NPMCacheEnvironmentKey: "NPM_CONFIG_CACHE", TmpEnvironmentKey: "TMPDIR",
		WritableRoots: writableRootsContract{ConfigKey: "sandbox_workspace_write.writable_roots", NPMCacheDirectory: "npm-cache", TmpDirectory: "tmp", PlaceholderShape: `["<attempt npm-cache>","<attempt tmp>"]`},
		CodexSHA256:   requiredCodexSHA256,
	}
}

type managedShellFingerprint struct {
	Schema                  string                `json:"schema"`
	GateManifest            gateManifest          `json:"gate_manifest"`
	ManagedPath             string                `json:"managed_path"`
	BootstrapFileName       string                `json:"bootstrap_file_name"`
	AttemptDirectoryMode    uint32                `json:"attempt_directory_mode"`
	BootstrapFileMode       uint32                `json:"bootstrap_file_mode"`
	ManagedEnvironmentKeys  [5]string             `json:"managed_environment_keys"`
	BootstrapContentSHA256  string                `json:"bootstrap_content_sha256"`
	ManagedNetworkArguments []string              `json:"managed_network_arguments"`
	ParentEnvironmentUnset  []string              `json:"parent_environment_unset"`
	EnvironmentExecutable   string                `json:"environment_executable"`
	EnvironmentSHA256       string                `json:"environment_sha256"`
	EnvironmentUnsetOption  string                `json:"environment_unset_option"`
	EnvironmentExecReplace  string                `json:"environment_exec_replace"`
	CodexExecutable         string                `json:"codex_executable"`
	CodexSHA256             string                `json:"codex_sha256"`
	NPMCacheDirectoryName   string                `json:"npm_cache_directory_name"`
	TmpDirectoryName        string                `json:"tmp_directory_name"`
	WritableDirectoryMode   uint32                `json:"writable_directory_mode"`
	NPMCacheEnvironmentKey  string                `json:"npm_cache_environment_key"`
	TmpEnvironmentKey       string                `json:"tmp_environment_key"`
	WritableRoots           writableRootsContract `json:"writable_roots"`
}

func (adapter *Adapter) loadGate() (gateManifest, error) {
	data, err := adapter.config.ReadFile(adapter.config.GatePath)
	if err != nil {
		return gateManifest{}, fmt.Errorf("read Codex gate evidence: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest gateManifest
	if err := decoder.Decode(&manifest); err != nil {
		return gateManifest{}, fmt.Errorf("decode Codex gate evidence: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return gateManifest{}, errors.New("Codex gate evidence contains trailing data")
	}
	return manifest, nil
}

func (adapter *Adapter) verifyGate(ctx context.Context, manifest gateManifest) error {
	if manifest.SchemaVersion != "chora.runtime-gate.v2" ||
		manifest.CodexVersion != requiredCodexVersion ||
		manifest.RealAdapterGate != "PASS" ||
		manifest.HostReadIsolation != "unavailable" ||
		manifest.WorkspaceWriteIsolation != "proven" ||
		manifest.ControlPlaneIsolation != "proven" ||
		manifest.LoopbackIsolation != "proven" || manifest.ManagedNetworkProxy != "proven" ||
		manifest.RegistryEgress != "proven" || manifest.NonAllowlistedEgressIsolation != "proven" ||
		manifest.ManagedLoopbackIsolation != "proven" || manifest.ManagedLocalBindingIsolation != "proven" ||
		manifest.UpstreamProxy != "disabled" || manifest.SOCKS5 != "disabled" || manifest.SOCKS5UDP != "disabled" ||
		manifest.NonLoopbackProxy != "disabled" || manifest.AllUnixSockets != "disabled" || manifest.UnixSocketAllowlist != "empty" ||
		manifest.EnvironmentWrapper != "proven" || manifest.EnvironmentWrapperSHA256 != adapter.config.expectedEnvironmentSHA256 ||
		manifest.CodexExecutable != InnerCodexExecutable ||
		manifest.CodexBinarySHA256 != adapter.contract.CodexSHA256 ||
		manifest.AllowedDestinationPayloadIsolation != "unavailable" {
		return errors.New("Codex runtime gate is not proven")
	}
	for name, digest := range map[string]string{
		"profile template":    manifest.ProfileTemplateSHA256,
		"probe binary":        manifest.ProbeBinarySHA256,
		"Codex binary":        manifest.CodexBinarySHA256,
		"sandbox-exec":        manifest.SandboxExecSHA256,
		"environment wrapper": manifest.EnvironmentWrapperSHA256,
		"contract":            manifest.ContractSHA256,
	} {
		if !sha256Pattern.MatchString(digest) {
			return fmt.Errorf("invalid %s SHA-256 in Codex gate", name)
		}
	}
	if manifest.ProfileTemplateSHA256 != runtimeboundary.ProfileTemplateSHA256() || manifest.ProfileRendererVersion != runtimeboundary.RendererVersion {
		return errors.New("Codex runtime profile evidence mismatch")
	}
	if err := adapter.verifyFileSHA(adapter.config.ContractPath, manifest.ContractSHA256, "contract"); err != nil {
		return err
	}
	if err := adapter.verifyFileSHA(InnerCodexExecutable, adapter.contract.CodexSHA256, "Codex binary"); err != nil {
		return err
	}
	if err := adapter.verifyFileSHA(SandboxExecutable, manifest.SandboxExecSHA256, "sandbox-exec"); err != nil {
		return err
	}
	if err := adapter.verifyFileSHA(EnvironmentExecutable, manifest.EnvironmentWrapperSHA256, "environment wrapper"); err != nil {
		return err
	}
	versionOutput, err := adapter.config.RunCommand(ctx, InnerCodexExecutable, "--version")
	if err != nil {
		return fmt.Errorf("read Codex version: %w", err)
	}
	const versionPrefix = "codex-cli "
	versionText := strings.TrimSpace(string(versionOutput))
	if !strings.HasPrefix(versionText, versionPrefix) || strings.TrimPrefix(versionText, versionPrefix) != manifest.CodexVersion {
		return errors.New("Codex version evidence mismatch")
	}
	osBuild, err := adapter.config.OSBuild(ctx)
	if err != nil {
		return fmt.Errorf("read OS build: %w", err)
	}
	if strings.TrimSpace(osBuild) == "" || strings.TrimSpace(osBuild) != manifest.OSBuild {
		return errors.New("OS build evidence mismatch")
	}
	return nil
}

func (adapter *Adapter) verifyFileSHA(path, expected, label string) error {
	data, err := adapter.config.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", label, err)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != expected {
		return fmt.Errorf("%s SHA-256 evidence mismatch", label)
	}
	return nil
}

func normalizeConfig(config Config) (Config, error) {
	if config.expectedCodexSHA256 == "" {
		config.expectedCodexSHA256 = requiredCodexSHA256
	}
	if !sha256Pattern.MatchString(config.expectedCodexSHA256) {
		return config, errors.New("Codex adapter expected Codex SHA-256 is invalid")
	}
	if config.expectedEnvironmentSHA256 == "" {
		config.expectedEnvironmentSHA256 = requiredEnvironmentSHA256
	}
	if !sha256Pattern.MatchString(config.expectedEnvironmentSHA256) {
		return config, errors.New("Codex adapter expected environment SHA-256 is invalid")
	}
	if config.ManagedPath == "" {
		config.ManagedPath = os.Getenv("PATH")
	}
	if err := validateManagedPath(config.ManagedPath); err != nil {
		return config, err
	}
	if config.SchemaPath == "" && config.RepoRoot != "" {
		config.SchemaPath = filepath.Join(config.RepoRoot, "schemas", "chora.agent-result.v1.schema.json")
	}
	if config.GatePath == "" && config.RepoRoot != "" {
		config.GatePath = filepath.Join(config.RepoRoot, "docs", "validation", "codex-cli-0.146.0-gate.json")
	}
	if config.ContractPath == "" && config.RepoRoot != "" {
		config.ContractPath = filepath.Join(config.RepoRoot, "docs", "validation", "codex-cli-0.146.0-contract.md")
	}
	for name, path := range map[string]string{
		"runtime root": config.RuntimeRoot,
		"schema":       config.SchemaPath,
		"gate":         config.GatePath,
		"contract":     config.ContractPath,
	} {
		if !filepath.IsAbs(path) {
			return config, fmt.Errorf("Codex adapter %s path must be absolute", name)
		}
	}
	if config.ReadFile == nil {
		config.ReadFile = os.ReadFile
	}
	if config.RunCommand == nil {
		config.RunCommand = defaultCommandRunner
	}
	if config.OSBuild == nil {
		config.OSBuild = func(ctx context.Context) (string, error) {
			output, err := config.RunCommand(ctx, "/usr/bin/sw_vers", "-buildVersion")
			return strings.TrimSpace(string(output)), err
		}
	}
	return config, nil
}

func validateManagedPath(path string) error {
	if strings.ContainsAny(path, "\x00\r\n") {
		return errors.New("Codex adapter managed PATH contains a forbidden byte")
	}
	for _, segment := range strings.Split(path, ":") {
		if segment == "" || !filepath.IsAbs(segment) {
			return errors.New("Codex adapter managed PATH must contain only non-empty absolute segments")
		}
	}
	return nil
}

func defaultCommandRunner(ctx context.Context, name string, arguments ...string) ([]byte, error) {
	result, err := processbound.Run(ctx, processbound.Spec{
		Name: name, Args: arguments,
		StdoutLimit: 64 << 10,
		StderrLimit: 64 << 10,
	})
	if err != nil {
		return nil, errors.Join(err, errorFromStderr(result.Stderr))
	}
	return result.Stdout, nil
}

func errorFromStderr(stderr []byte) error {
	if len(bytes.TrimSpace(stderr)) == 0 {
		return nil
	}
	return errors.New("command failed with stderr output")
}

var _ execution.AgentAdapter = (*Adapter)(nil)
