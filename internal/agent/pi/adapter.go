package pi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Yangyang96/chora/internal/agent"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/processbound"
	"github.com/Yangyang96/chora/internal/speccoding"
)

const (
	AdapterID           = "pi"
	Executable          = "chora-pi-rpc"
	RuntimeVersion      = "0.84.2"
	RuntimeConfigSHA256 = "b4bb9e6e599732e31a5f20ff95f48587c8b702a63c754940b0a4631450f1a4e9"
	PolicySHA256        = "efe8918d0c9c4232c8292941f573fe386d93a3faa051329c883c9b8349b66f04"
	AttemptImageID      = "sha256:91698efead5641a633519f5f229373e08a59264045ca27f6d01fc06505deeea7"
	BoundaryImageID     = "sha256:4f7746f3cdbe55dc454775ead5958a9ed8a78b93776ea1df598255c1606b25c6"

	ResultPath          = "/output/result.json"
	PatchPath           = "/output/patch.diff"
	ChecksPath          = "/output/checks.json"
	RuntimeIdentityPath = "/output/runtime-identity.json"

	MinimalCapabilityBundleID      = "chora.minimal-core.v1"
	StandardCapabilityBundleID     = "chora.standard-core.v1"
	MinimalCapabilityBundleSHA256  = "ac50658500e1138d456d1df0f647877129d4583404862117337f4e237c54e328"
	StandardCapabilityBundleSHA256 = "0415ce865a64838801030c17901a94eddc20567da26449a334f364bdda1fbc09"

	frozenModelID     = "gpt-5.6-sol"
	frozenModelDigest = "sha256:4a73818291987693fcb53e9e61b4be3429ffd78b3f64ea877a34c5f9928c89e1"
	maxCommandSummary = 256
	maxAssistantText  = 64 * 1024
)

var (
	ErrResumeUnsupported = errors.New("Pi resume unsupported")
	exactRPCArguments    = []string{
		"--mode", "rpc", "--no-session", "--no-extensions", "--no-skills",
		"--no-prompt-templates", "--no-themes", "--no-context-files", "--no-approve",
		"--provider", "openai-codex", "--model", frozenModelID,
	}
	nativeRPCArguments   = []string{"--mode", "rpc", "--no-session", "--no-approve"}
	pathPiArguments      = []string{"--mode", "rpc"}
	sensitiveCommand     = regexp.MustCompile(`(?i)authorization\s*:|bearer\s+|api[_-]?key|token|password|secret`)
	relativePath         = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]*(/[A-Za-z0-9_-][A-Za-z0-9._-]*)*$`)
	allowedAbsoluteRoots = []string{
		"/workspace", "/input/context", "/output", "/tmp", "/run/chora/pi",
		"/bin", "/usr/bin", "/usr/local/bin",
	}
	allowedToolNames = map[string]struct{}{
		"read": {}, "view": {}, "edit": {}, "write": {}, "str_replace_editor": {},
		"bash": {}, "shell": {}, "exec": {}, "exec_command": {},
		"grep": {}, "find": {}, "ls": {},
	}
	allowedErrorClasses = map[string]struct{}{
		"runtime_error": {}, "undeclared_path": {}, "undeclared_command": {},
	}
)

// ManagedCapabilityBundle is the immutable Pi tool surface bound to one
// managed execution profile. Tools returns a copy so callers cannot widen a
// previously selected profile in place.
type ManagedCapabilityBundle struct {
	id     string
	sha256 string
	tools  []string
}

func (bundle ManagedCapabilityBundle) ID() string     { return bundle.id }
func (bundle ManagedCapabilityBundle) SHA256() string { return bundle.sha256 }
func (bundle ManagedCapabilityBundle) Tools() []string {
	return append([]string(nil), bundle.tools...)
}

// ManagedCapabilityBundleForProfile returns the only Pi tool allowlist that a
// managed profile may expose. Trusted Local deliberately has no managed
// bundle: its native Pi configuration is governed by the Trusted Host path.
func ManagedCapabilityBundleForProfile(profile domain.AgentExecutionProfile) (ManagedCapabilityBundle, error) {
	var id, policy, expectedSHA256 string
	var tools []string
	switch profile {
	case domain.AgentExecutionProfileMinimal:
		id, policy, expectedSHA256 = MinimalCapabilityBundleID, domain.MinimalCapabilityPolicy, MinimalCapabilityBundleSHA256
		tools = []string{"read", "bash", "edit", "write"}
	case domain.AgentExecutionProfileStandard:
		id, policy, expectedSHA256 = StandardCapabilityBundleID, domain.StandardCapabilityPolicy, StandardCapabilityBundleSHA256
		tools = []string{"read", "bash", "edit", "write", "grep", "find", "ls"}
	default:
		return ManagedCapabilityBundle{}, errors.New("managed Pi capability bundle requires Minimal or Standard profile")
	}
	canonical := strings.Join([]string{
		"chora.pi-managed-capability-bundle.v1", id, policy, strings.Join(tools, ","), "NO_SKILL",
	}, "\x00")
	if digest([]byte(canonical)) != expectedSHA256 {
		return ManagedCapabilityBundle{}, errors.New("managed Pi capability bundle identity drift")
	}
	return ManagedCapabilityBundle{id: id, sha256: expectedSHA256, tools: tools}, nil
}

// NativeRPCArguments returns the exact argv accepted by the Pi Runtime Adapter
// for Trusted Local execution. The Trusted Host supervisor pins the same copy.
func NativeRPCArguments() []string { return append([]string(nil), nativeRPCArguments...) }

type Config struct {
	IsolatedSource      IsolatedSource
	RuntimeConfig       []byte
	Policy              []byte
	AttemptImageID      string
	BoundaryImageID     string
	ManagedImageSource  ManagedImageSource
	LocalPiSource       LocalPiSource
	PathPiSource        PathPiSource
	SessionRoot         string
	ResourceObserver    *ResourceObserverBinding
	ReadFile            func(string) ([]byte, error)
	ReadSourceFile      func(string) ([]byte, error)
	ValidateLocalSource func(context.Context, LocalPiSource) error
}

type Adapter struct {
	config             Config
	managedSource      ManagedImageSource
	localSource        LocalPiSource
	pathPiSource       PathPiSource
	sessionRoot        string
	strictManagedModel bool
}

func New(config Config) (*Adapter, error) {
	managed := config.ManagedImageSource
	if !managed.Configured() {
		if config.AttemptImageID != "" {
			var err error
			managed, err = NewManagedImageSource(ManagedImageSourceParams{
				RuntimeVersion: RuntimeVersion, Executable: Executable,
				RuntimeConfigSHA256: RuntimeConfigSHA256, RuntimeImageID: config.AttemptImageID,
				ReleaseProvenance: ManagedReleaseProvenance{Kind: managedDevelopmentProvenanceKind},
			})
			if err != nil {
				return nil, errors.New("Pi image identity mismatch")
			}
		}
	} else if !managed.valid() ||
		config.AttemptImageID != "" && config.AttemptImageID != managed.RuntimeImageID() {
		return nil, errors.New("Pi managed image source identity mismatch")
	}
	if managed.Configured() {
		if !json.Valid(config.RuntimeConfig) || digest(config.RuntimeConfig) != RuntimeConfigSHA256 {
			return nil, errors.New("Pi runtime config identity mismatch")
		}
		if !json.Valid(config.Policy) || digest(config.Policy) != PolicySHA256 {
			return nil, errors.New("Pi policy identity mismatch")
		}
	}
	if config.LocalPiSource.Configured() && (!config.LocalPiSource.valid() || config.ValidateLocalSource == nil) {
		return nil, errors.New("Pi local source identity or closure validator mismatch")
	}
	if config.PathPiSource.Configured() {
		if !config.PathPiSource.valid() {
			return nil, errors.New("PATH Pi source identity mismatch")
		}
		if !validAbsoluteCleanPath(config.SessionRoot) {
			return nil, errors.New("PATH Pi session root must be an absolute clean path")
		}
	}
	if managed.Configured() && config.PathPiSource.Configured() {
		return nil, errors.New("managed and PATH Pi sources are mutually exclusive")
	}
	if config.IsolatedSource.Configured() && !config.IsolatedSource.valid() {
		return nil, errors.New("invalid isolated Pi source")
	}
	if !managed.Configured() && !config.LocalPiSource.Configured() && !config.PathPiSource.Configured() && !config.IsolatedSource.Configured() {
		return nil, errors.New("Pi Runtime requires at least one configured source")
	}
	if managed.Configured() {
		if !validSHA256Identity(config.BoundaryImageID) {
			return nil, errors.New("Pi boundary image identity mismatch")
		}
	} else if config.BoundaryImageID != "" {
		return nil, errors.New("Pi boundary image requires a managed Runtime source")
	}
	config.RuntimeConfig = append([]byte(nil), config.RuntimeConfig...)
	config.Policy = append([]byte(nil), config.Policy...)
	if config.ReadFile == nil {
		config.ReadFile = os.ReadFile
	}
	if config.ReadSourceFile == nil {
		config.ReadSourceFile = os.ReadFile
	}
	return &Adapter{config: config, managedSource: managed, localSource: config.LocalPiSource,
		pathPiSource: config.PathPiSource, sessionRoot: config.SessionRoot,
		strictManagedModel: managed.Configured()}, nil
}

func (adapter *Adapter) ID() string { return AdapterID }

func (adapter *Adapter) Capabilities() execution.AdapterCapabilities {
	mode := execution.ResumeUnsupported
	if adapter != nil && adapter.pathPiSource.Configured() {
		mode = execution.ResumeExplicitSession
	}
	return execution.AdapterCapabilities{ResumeMode: mode}
}

func (adapter *Adapter) PrepareStart(ctx context.Context, request execution.StartRequest) (execution.Invocation, error) {
	preparation, err := adapter.PrepareBoundStart(ctx, request)
	if err != nil {
		return execution.Invocation{}, err
	}
	return preparation.Invocation, nil
}

func (adapter *Adapter) PrepareBoundStart(ctx context.Context, request execution.StartRequest) (execution.StartPreparation, error) {
	if adapter == nil {
		return execution.StartPreparation{}, errors.New("Pi adapter is nil")
	}
	source, err := adapter.sourceForBinding(request.Attempt.AgentExecutionProfileBinding())
	if err != nil {
		return execution.StartPreparation{}, err
	}
	if err := adapter.validateSelectedSource(ctx, source); err != nil {
		return execution.StartPreparation{}, err
	}
	if source.isolated {
		if err := adapter.config.IsolatedSource.ValidateContract(request.ExecutionContractDocument); err != nil {
			return execution.StartPreparation{}, err
		}
	}
	invocation, err := adapter.prepareStartForSource(request, source)
	if err != nil {
		return execution.StartPreparation{}, err
	}
	fingerprint, err := adapter.observerFingerprint(ctx, source, request.ExecutionContractDocument)
	if err != nil {
		return execution.StartPreparation{}, err
	}
	return execution.NewStartPreparation(invocation, fingerprint)
}

func (adapter *Adapter) prepareStartForSource(request execution.StartRequest, source selectedSource) (execution.Invocation, error) {
	if !request.Run.ID().Valid() || !request.Run.TaskID().Valid() || !request.Run.CharterID().Valid() ||
		!request.Attempt.ID().Valid() || request.Attempt.RunID() != request.Run.ID() ||
		request.Attempt.AdapterID() != AdapterID || request.Attempt.ExternalSession() != "" ||
		!filepath.IsAbs(request.WorkspaceRoot) || filepath.Clean(request.WorkspaceRoot) != request.WorkspaceRoot ||
		!request.LaunchToken.Valid() {
		return execution.Invocation{}, errors.New("invalid Pi start identity or source root")
	}
	if len(bytes.TrimSpace(request.SnapshotDocument)) == 0 || !json.Valid(request.SnapshotDocument) {
		return execution.Invocation{}, errors.New("invalid Pi Context Snapshot")
	}
	if len(bytes.TrimSpace(request.ExecutionContractDocument)) == 0 || !json.Valid(request.ExecutionContractDocument) {
		return execution.Invocation{}, errors.New("invalid Pi Active Spec Coding Contract")
	}
	contextDigest := sha256.Sum256(request.SnapshotDocument)
	if contextDigest != request.Attempt.ContextDigest() {
		return execution.Invocation{}, errors.New("Pi Context Snapshot digest mismatch")
	}
	message, err := execution.WrapFrozenExecutionPrompt(request.SnapshotDocument, request.ExecutionContractDocument)
	if err != nil {
		return execution.Invocation{}, err
	}
	if source.pathPi || source.isolated {
		var document struct {
			Acceptance struct {
				VerificationCommands []speccoding.BoundedCommand `json:"verification_commands"`
			} `json:"acceptance"`
		}
		if err := json.Unmarshal(request.ExecutionContractDocument, &document); err != nil {
			return execution.Invocation{}, err
		}
		var commands []string
		for _, command := range document.Acceptance.VerificationCommands {
			text, commandErr := speccoding.CanonicalCommandText(command)
			if commandErr != nil {
				return execution.Invocation{}, commandErr
			}
			commands = append(commands, text)
		}
		if source.isolated {
			message, err = execution.WrapIsolatedExecutionPrompt(request.SnapshotDocument, request.ExecutionContractDocument, commands)
		} else {
			message, err = execution.WrapLocalConnectedExecutionPrompt(request.SnapshotDocument, request.ExecutionContractDocument, commands)
		}
		if err != nil {
			return execution.Invocation{}, err
		}
	}
	if (source.pathPi || source.isolated) && request.Attempt.Sequence() > 1 && strings.TrimSpace(request.Attempt.ContextDelta()) != "" {
		message += "\n\nHuman Review correction for this successor Attempt. Apply these instructions within the frozen execution contract; preserve its authority and boundaries:\n" + request.Attempt.ContextDelta()
	}
	prompt, err := json.Marshal(struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Message string `json:"message"`
	}{ID: "chora-prompt", Type: "prompt", Message: message})
	if err != nil {
		return execution.Invocation{}, fmt.Errorf("encode Pi prompt: %w", err)
	}
	prompt = append(prompt, '\n')
	if source.pathPi || source.isolated {
		preflight := []byte("{\"id\":\"chora-get-state\",\"type\":\"get_state\"}\n")
		prompt = append(preflight, prompt...)
	}
	environment := map[string]string{
		"CHORA_RUN_ID":                    request.Run.ID().String(),
		"CHORA_ATTEMPT_ID":                request.Attempt.ID().String(),
		"CHORA_CONTEXT_DIGEST":            hex.EncodeToString(contextDigest[:]),
		"CHORA_CONTRACT_DIGEST":           digest(request.ExecutionContractDocument),
		"CHORA_POLICY_DIGEST":             PolicySHA256,
		"CHORA_AGENT_EXECUTION_PROFILE":   string(source.binding.Profile()),
		"CHORA_RUNTIME_SOURCE":            source.binding.RuntimeSource(),
		"CHORA_RUNTIME_SOURCE_IDENTITY":   hex.EncodeToString(source.identity[:]),
		"CHORA_RUNTIME_VERSION":           source.version,
		"CHORA_EXECUTION_PROVIDER":        source.binding.ExecutionProvider(),
		"CHORA_CAPABILITY_POLICY":         source.binding.CapabilityPolicy(),
		"CHORA_TRUST_DISCLOSURE_POLICY":   source.binding.TrustDisclosurePolicy(),
		"CHORA_COMMAND_TIMEOUT_SECONDS":   "600",
		"CHORA_ATTEMPT_TIMEOUT_SECONDS":   "1200",
		"CHORA_PERSISTED_LOG_LIMIT_BYTES": "10485760",
		"CHORA_ARTIFACT_LIMIT_BYTES":      "104857600",
		"CHORA_RESULT_PATH":               ResultPath,
		"CHORA_PATCH_PATH":                PatchPath,
		"CHORA_CHECKS_PATH":               ChecksPath,
		"CHORA_RUNTIME_IDENTITY_PATH":     RuntimeIdentityPath,
	}
	if source.isolated {
		if err := adapter.configureIsolatedObserver(request.Attempt.ID(), request.ExecutionContractDocument, environment); err != nil {
			return execution.Invocation{}, err
		}
	}
	if source.managed {
		environment["CHORA_ATTEMPT_IMAGE_ID"] = adapter.managedSource.RuntimeImageID()
		environment["CHORA_BOUNDARY_IMAGE_ID"] = adapter.config.BoundaryImageID
		environment["CHORA_CAPABILITY_BUNDLE_ID"] = source.capabilityBundle.ID()
		environment["CHORA_CAPABILITY_BUNDLE_SHA256"] = source.capabilityBundle.SHA256()
	}
	target, err := execution.NewExecutionTarget(AdapterID, source.binding.ExecutionProvider())
	if err != nil {
		return execution.Invocation{}, errors.New("Pi execution target binding is invalid")
	}
	arguments := append([]string(nil), source.arguments...)
	if source.pathPi {
		sessionDir := filepath.Join(adapter.sessionRoot, request.Attempt.ID().String())
		arguments = append(arguments, "--session-dir", sessionDir)
	}
	arguments, err = adapter.configureResourceObserver(source, request.Attempt.ID(), request.WorkspaceRoot, request.ExecutionContractDocument, arguments, environment)
	if err != nil {
		return execution.Invocation{}, err
	}
	return execution.NewInvocation(execution.InvocationParams{
		AdapterID: AdapterID, Executable: source.executable, Arguments: arguments,
		Environment: environment, WorkingRoot: request.WorkspaceRoot, Stdin: prompt,
		LaunchToken: request.LaunchToken, Target: target,
	})
}

func (adapter *Adapter) PrepareResume(ctx context.Context, request execution.ResumeRequest) (execution.Invocation, error) {
	if adapter == nil || !adapter.pathPiSource.Configured() {
		return execution.Invocation{}, ErrResumeUnsupported
	}
	source, err := adapter.sourceForBinding(request.Attempt.AgentExecutionProfileBinding())
	if err != nil || !source.pathPi {
		return execution.Invocation{}, errors.New("Pi explicit-session resume requires PATH Pi")
	}
	if err := adapter.validateSelectedSource(ctx, source); err != nil {
		return execution.Invocation{}, err
	}
	fingerprint := fingerprintForSource(source)
	if len(request.ExecutionContractDocument) > 0 {
		fingerprint, err = adapter.observerFingerprint(ctx, source, request.ExecutionContractDocument)
		if err != nil {
			return execution.Invocation{}, err
		}
	}
	sessionID := strings.TrimSpace(request.Binding.ExternalSession)
	ownerID := request.Binding.SessionOwnerAttemptID
	_, hasPredecessor := request.Attempt.Predecessor()
	if !request.Run.ID().Valid() || !request.Attempt.ID().Valid() || request.Attempt.RunID() != request.Run.ID() ||
		request.Attempt.AdapterID() != AdapterID || request.Attempt.Sequence() <= 1 || !hasPredecessor || !ownerID.Valid() || !validSessionUUID(sessionID) ||
		request.Attempt.ExternalSession() != sessionID || !request.Binding.RuntimeFingerprint.Valid() ||
		request.Binding.RuntimeFingerprint != fingerprint ||
		request.Binding.WorkingRoot == "" || request.Binding.WorkingRoot != filepath.Clean(request.Binding.WorkingRoot) || !filepath.IsAbs(request.Binding.WorkingRoot) ||
		request.Binding.SecurityFingerprint == ([32]byte{}) || request.SecurityFingerprint != request.Binding.SecurityFingerprint ||
		!request.LaunchToken.Valid() || len(bytes.TrimSpace(request.DeltaInstruction)) == 0 || string(request.DeltaInstruction) != request.Attempt.ContextDelta() {
		return execution.Invocation{}, errors.New("invalid PATH Pi recorded-session binding")
	}
	sessionDir := filepath.Join(adapter.sessionRoot, ownerID.String())
	if filepath.Dir(sessionDir) != adapter.sessionRoot {
		return execution.Invocation{}, errors.New("PATH Pi session owner escaped configured root")
	}
	prompt, err := json.Marshal(struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Message string `json:"message"`
	}{ID: "chora-resume", Type: "prompt", Message: string(request.DeltaInstruction)})
	if err != nil {
		return execution.Invocation{}, err
	}
	prompt = append(prompt, '\n')
	prompt = append([]byte("{\"id\":\"chora-get-state\",\"type\":\"get_state\"}\n"), prompt...)
	target, err := execution.NewExecutionTarget(AdapterID, source.binding.ExecutionProvider())
	if err != nil {
		return execution.Invocation{}, err
	}
	environment := map[string]string{
		"CHORA_RUN_ID": request.Run.ID().String(), "CHORA_ATTEMPT_ID": request.Attempt.ID().String(),
		"CHORA_AGENT_EXECUTION_PROFILE": string(source.binding.Profile()), "CHORA_RUNTIME_SOURCE": source.binding.RuntimeSource(),
		"CHORA_RUNTIME_SOURCE_IDENTITY": hex.EncodeToString(source.identity[:]), "CHORA_RUNTIME_VERSION": source.version,
		"CHORA_EXECUTION_PROVIDER": source.binding.ExecutionProvider(), "CHORA_CAPABILITY_POLICY": source.binding.CapabilityPolicy(),
		"CHORA_TRUST_DISCLOSURE_POLICY": source.binding.TrustDisclosurePolicy(),
		"CHORA_RESULT_PATH":             ResultPath, "CHORA_PATCH_PATH": PatchPath, "CHORA_CHECKS_PATH": ChecksPath, "CHORA_RUNTIME_IDENTITY_PATH": RuntimeIdentityPath,
	}
	arguments := []string{"--mode", "rpc", "--session-dir", sessionDir, "--session", sessionID}
	if len(request.ExecutionContractDocument) > 0 {
		arguments, err = adapter.configureResourceObserver(source, request.Attempt.ID(), request.Binding.WorkingRoot, request.ExecutionContractDocument, arguments, environment)
		if err != nil {
			return execution.Invocation{}, err
		}
	}
	return execution.NewInvocation(execution.InvocationParams{AdapterID: AdapterID, Executable: source.executable, Arguments: arguments, Environment: environment, WorkingRoot: request.Binding.WorkingRoot, Stdin: prompt, LaunchToken: request.LaunchToken, Target: target})
}

func validSessionUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	for index, r := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') && !(r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}

func (adapter *Adapter) DecodeEvent(chunk execution.EventChunk) (execution.DecodedChunk, error) {
	if chunk.Stream != execution.StreamStdout && chunk.Stream != execution.StreamStderr {
		return execution.DecodedChunk{}, errors.New("invalid Pi stream")
	}
	if chunk.Stream == execution.StreamStderr {
		// stderr is diagnostic text, not the Pi JSONL protocol.
		return execution.DecodedChunk{ConsumedBytes: len(chunk.Data)}, nil
	}
	consumed := len(chunk.Data)
	if !chunk.EOF {
		lastLF := bytes.LastIndexByte(chunk.Data, '\n')
		if lastLF < 0 {
			return execution.DecodedChunk{}, nil
		}
		consumed = lastLF + 1
	} else if len(chunk.Data) > 0 && chunk.Data[len(chunk.Data)-1] != '\n' {
		return execution.DecodedChunk{}, fmt.Errorf("%w: unterminated Pi JSONL record", execution.ErrEventStreamInvalid)
	}
	decoded := execution.DecodedChunk{ConsumedBytes: consumed}
	remaining := chunk.Data[:consumed]
	for len(remaining) > 0 {
		newline := bytes.IndexByte(remaining, '\n')
		line := remaining[:newline]
		remaining = remaining[newline+1:]
		if len(line) == 0 || bytes.IndexByte(line, '\r') >= 0 {
			return execution.DecodedChunk{}, fmt.Errorf("%w: invalid strict-LF Pi JSONL record", execution.ErrEventStreamInvalid)
		}
		strictModel := adapter.strictManagedModel
		if chunk.Profile == domain.AgentExecutionProfileIsolatedLocal {
			if !adapter.config.IsolatedSource.Configured() {
				return execution.DecodedChunk{}, fmt.Errorf("%w: isolated decoder source unavailable", execution.ErrEventStreamInvalid)
			}
			if err := validateIsolatedEventModel(line); err != nil {
				return execution.DecodedChunk{}, fmt.Errorf("%w: %v", execution.ErrEventStreamInvalid, err)
			}
			// Isolated checks use the public observer, independently of the
			// legacy managed model/path policy on a composed adapter.
			strictModel = false
		}
		event, ok, err := projectLine(line, strictModel, chunk.WorkingRoot)
		if err != nil {
			return execution.DecodedChunk{}, fmt.Errorf("%w: %v", execution.ErrEventStreamInvalid, err)
		}
		if ok {
			decoded.Events = append(decoded.Events, event)
		}
	}
	return decoded, nil
}

func projectLine(line []byte, strictModel bool, workingRoots ...string) (execution.NormalizedEvent, bool, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(line, &raw); err != nil {
		return execution.NormalizedEvent{}, false, fmt.Errorf("decode Pi JSONL: %w", err)
	}
	eventType, ok := stringField(raw, "type")
	if !ok || strings.TrimSpace(eventType) == "" {
		return execution.NormalizedEvent{}, false, errors.New("Pi event type required")
	}
	projectedType := ""
	switch eventType {
	case "message_end":
		return projectAssistantMessage(raw)
	case "agent_settled":
		return projectAgentSettled(raw)
	case "response":
		return projectResponseFrame(raw)
	case "extension_error":
		return projectExtensionError(raw)
	case "agent_start", "agent_end", "file_path", "command_summary", "exit_code", "error", "model_identity", "usage":
		projectedType = eventType
	case "tool_execution_start", "tool_start":
		projectedType = "tool_start"
	case "tool_execution_end", "tool_end":
		projectedType = "tool_end"
	case "tool_execution_update", "turn_start", "turn_end", "message_start", "message_update":
		// Real Pi emits transient progress frames. They carry no reviewable
		// authority and are intentionally dropped; the terminal assistant text
		// arrives through message_end.
		return execution.NormalizedEvent{}, false, nil
	default:
		return execution.NormalizedEvent{}, false, nil
	}
	projected := map[string]any{"event_type": projectedType}
	var occurredAt time.Time
	if value, ok := stringField(raw, "timestamp"); ok {
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return execution.NormalizedEvent{}, false, errors.New("invalid Pi event timestamp")
		}
		occurredAt = parsed
		projected["timestamp"] = value
	}
	toolName, _ := firstString(raw, "toolName", "tool_name", "name")
	toolName = normalizedToolName(toolName)
	if (projectedType == "tool_start" || projectedType == "tool_end") && toolName != "" {
		projected["tool_name"] = toolName
	}
	toolCallID, _ := firstString(raw, "toolCallId", "tool_call_id")
	if (projectedType == "tool_start" || projectedType == "tool_end") && strings.TrimSpace(toolCallID) != "" {
		projected["tool_call_id"] = toolCallID
	}
	args := objectField(raw, "args", "arguments")
	path, _ := firstString(args, "path", "file_path")
	if path == "" {
		path, _ = firstString(raw, "workspace_relative_path", "path")
	}
	if relative, ok := workspaceRelative(path); ok {
		projected["workspace_relative_path"] = relative
	}
	command, _ := firstString(args, "command")
	if command == "" {
		command, _ = firstString(raw, "redacted_command_summary", "command")
	}
	originalCommand := command
	// Persist a one-way semantic binding before display redaction. No argv or
	// host path is exposed; alternate quoting retains check attribution.
	if observed, normalizeErr := speccoding.NormalizeObservedCheckCommandAtRoot(command, firstWorkingRoot(workingRoots)); normalizeErr == nil && observed.ExplicitWorkingDirectory {
		repoPart := strings.SplitN(observed.WorkingDirectory, "/", 2)[0]
		if validResourceCommandLocator(repoPart) {
			semantic, _ := json.Marshal(struct {
				Argv []string `json:"argv"`
				Cwd  string   `json:"cwd"`
			}{observed.Argv, observed.WorkingDirectory})
			projected["resource_command_digest"] = digest(semantic)
		}
	}

	absoluteHostReference := undeclaredAbsolutePath(path) || commandUsesUndeclaredAbsolutePath(command)
	if absoluteHostReference && strictModel {
		projectedType = "error"
		projected = map[string]any{"event_type": projectedType, "error_class": "undeclared_path"}
		if !occurredAt.IsZero() {
			projected["timestamp"] = occurredAt.Format(time.RFC3339Nano)
		}
		path = ""
		command = ""
	} else if absoluteHostReference {
		// PATH Pi Local Connected owns its native file/command permission
		// policy. Chora still withholds arbitrary absolute host paths from its
		// persisted normalized activity.
		path = ""
	}
	if !strictModel && command != "" {
		command = redactHostCommand(originalCommand, firstWorkingRoot(workingRoots))
	}
	if command != "" {
		projected["redacted_command_summary"] = redactCommand(command)
		projected["command_digest"] = digest([]byte(originalCommand))
	}
	if exitCode, ok := integerField(raw, "exit_code", "exitCode"); ok {
		projected["exit_code"] = exitCode
	} else if result := objectField(raw, "result"); result != nil {
		if exitCode, ok := integerField(result, "exit_code", "exitCode"); ok {
			projected["exit_code"] = exitCode
		} else if details := objectField(result, "details"); details != nil {
			if exitCode, ok := integerField(details, "exitCode", "exit_code"); ok {
				projected["exit_code"] = exitCode
			}
		}
	}
	if projectedType == "tool_end" && !strictModel {
		if details := objectField(objectField(raw, "result"), "details"); details != nil {
			if observation, present := details["choraResourceCheckObservation"]; present {
				encoded, encodeErr := json.Marshal(observation)
				proof, proofErr := execution.DecodeResourceCheckObservation(encoded, ResourceObserverSHA256())
				if encodeErr == nil && proofErr == nil && proof.ToolCallID == toolCallID {
					projected["resource_check_observation"] = proof
				}
			}
		}
	}
	if isError, ok := boolField(raw, "isError", "is_error"); ok {
		projected["tool_error"] = isError
	}
	if projectedType == "error" {
		if _, classified := projected["error_class"]; !classified {
			errorClass, _ := firstString(raw, "error_class", "name")
			if errorClass == "" {
				errorClass, _ = firstString(objectField(raw, "error"), "name", "class")
			}
			if errorClass == "" {
				errorClass = "runtime_error"
			}
			projected["error_class"] = normalizedErrorClass(errorClass)
		}
	}
	if projectedType == "model_identity" || projectedType == "agent_start" {
		modelID, _ := firstString(raw, "model_id")
		if modelID == "" {
			modelID, _ = firstString(objectField(raw, "model"), "id")
		}
		if modelID != "" {
			if strictModel && modelID != frozenModelID {
				return execution.NormalizedEvent{}, false, errors.New("Pi model identity mismatch")
			}
			projected["model_id"] = modelID
			if strictModel {
				projected["model_digest"] = frozenModelDigest
			} else if provider, ok := firstString(raw, "provider"); ok {
				projected["provider"] = provider
			}
		}
	}
	if projectedType == "usage" {
		if usage := projectUsage(objectField(raw, "usage")); len(usage) > 0 {
			projected["usage"] = usage
		}
	}
	normalized, err := json.Marshal(projected)
	if err != nil {
		return execution.NormalizedEvent{}, false, err
	}
	return execution.NormalizedEvent{Type: projectedType, OccurredAt: occurredAt, NormalizedJSON: normalized}, true, nil
}

func validResourceCommandLocator(value string) bool {
	if _, err := domain.ParseRepositoryID(value); err == nil {
		return true
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 6 && value == strings.ToLower(value)
}

func projectAssistantMessage(raw map[string]json.RawMessage) (execution.NormalizedEvent, bool, error) {
	message := objectField(raw, "message")
	role, ok := stringField(message, "role")
	if !ok || role != "assistant" {
		return execution.NormalizedEvent{}, false, nil
	}
	if stopReason, _ := firstString(message, "stopReason", "stop_reason"); stopReason == "error" {
		// Pi places provider/model failures on the terminal assistant message and
		// may emit no text or separate response failure frame. Persist only a safe
		// classification plus model provenance; never persist the vendor error
		// message, which can contain URLs, account details, or other diagnostics.
		projected := map[string]any{"event_type": "error", "error_class": "model_error"}
		if provider, ok := stringField(message, "provider"); ok && provider != "" {
			projected["provider"] = provider
		}
		if modelID, ok := stringField(message, "model"); ok && modelID != "" {
			projected["model_id"] = modelID
		}
		normalized, err := json.Marshal(projected)
		if err != nil {
			return execution.NormalizedEvent{}, false, err
		}
		return execution.NormalizedEvent{Type: "error", NormalizedJSON: normalized}, true, nil
	}
	var content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	encoded, ok := message["content"]
	if !ok || json.Unmarshal(encoded, &content) != nil {
		return execution.NormalizedEvent{}, false, errors.New("invalid Pi assistant message content")
	}
	parts := make([]string, 0, len(content))
	for _, block := range content {
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			parts = append(parts, block.Text)
		}
	}
	text := strings.TrimSpace(strings.Join(parts, "\n\n"))
	if text == "" {
		return execution.NormalizedEvent{}, false, nil
	}
	text, truncated := boundedAssistantText(text)
	stopReason, _ := firstString(message, "stopReason", "stop_reason")
	projected := map[string]any{"event_type": "assistant_message", "text": text, "terminal_complete": stopReason == "stop" && !truncated}
	if truncated {
		projected["truncated"] = true
	}
	// Real Pi records provider/model/usage inside the assistant message object,
	// purely as provenance. The managed path's strict model check is applied at
	// the caller for model_identity events, not here.
	if provider, ok := stringField(message, "provider"); ok && provider != "" {
		projected["provider"] = provider
	}
	if modelID, ok := stringField(message, "model"); ok && modelID != "" {
		projected["model_id"] = modelID
	}
	if usage := projectUsage(objectField(message, "usage")); len(usage) > 0 {
		projected["usage"] = usage
	}
	if cost, ok := reportedPiCost(objectField(message, "usage")); ok {
		projected["reported_cost"] = cost
	}
	var occurredAt time.Time
	if value, ok := stringField(raw, "timestamp"); ok {
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return execution.NormalizedEvent{}, false, errors.New("invalid Pi event timestamp")
		}
		occurredAt = parsed
		projected["timestamp"] = value
	}
	normalized, err := json.Marshal(projected)
	if err != nil {
		return execution.NormalizedEvent{}, false, err
	}
	return execution.NormalizedEvent{Type: "assistant_message", OccurredAt: occurredAt, NormalizedJSON: normalized}, true, nil
}

// projectAgentSettled projects the real-Pi terminal idle signal. It is the frame
// the drain layer keys on to close stdin and let the RPC process shut down.
func projectAgentSettled(raw map[string]json.RawMessage) (execution.NormalizedEvent, bool, error) {
	projected := map[string]any{"event_type": "agent_settled"}
	var occurredAt time.Time
	if value, ok := stringField(raw, "timestamp"); ok {
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return execution.NormalizedEvent{}, false, errors.New("invalid Pi event timestamp")
		}
		occurredAt = parsed
		projected["timestamp"] = value
	}
	normalized, err := json.Marshal(projected)
	if err != nil {
		return execution.NormalizedEvent{}, false, err
	}
	return execution.NormalizedEvent{Type: "agent_settled", OccurredAt: occurredAt, NormalizedJSON: normalized}, true, nil
}

// projectResponseFrame projects the real-Pi RPC correlated response frames.
// Success frames are preflight acknowledgements and produce no event; failure
// frames (parse, unknown command, or prompt/model errors) become explicit
// runtime_error events so a provider error is never a silent success.
func projectResponseFrame(raw map[string]json.RawMessage) (execution.NormalizedEvent, bool, error) {
	if success, ok := boolField(raw, "success"); ok && success {
		command, _ := stringField(raw, "command")
		correlationID, _ := stringField(raw, "id")
		if command == "get_state" || correlationID == "chora-get-state" {
			data := objectField(raw, "data")
			sessionID, _ := firstString(data, "sessionId", "session_id")
			if !validSessionUUID(sessionID) {
				return execution.NormalizedEvent{}, false, errors.New("Pi get_state returned invalid session identity")
			}
			encoded, err := json.Marshal(map[string]any{"event_type": "agent.session"})
			if err != nil {
				return execution.NormalizedEvent{}, false, err
			}
			return execution.NormalizedEvent{Type: "agent.session", NormalizedJSON: encoded, ExternalSession: sessionID}, true, nil
		}
		return execution.NormalizedEvent{}, false, nil
	}
	command, _ := stringField(raw, "command")
	detail, _ := stringField(raw, "error")
	projected := map[string]any{"event_type": "error", "error_class": "runtime_error"}
	if command != "" {
		projected["command"] = command
	}
	if detail != "" {
		projected["error"] = detail
	}
	normalized, err := json.Marshal(projected)
	if err != nil {
		return execution.NormalizedEvent{}, false, err
	}
	return execution.NormalizedEvent{Type: "error", NormalizedJSON: normalized}, true, nil
}

// projectExtensionError projects real-Pi extension failures as explicit errors.
func projectExtensionError(raw map[string]json.RawMessage) (execution.NormalizedEvent, bool, error) {
	detail, _ := firstString(raw, "error", "event")
	projected := map[string]any{"event_type": "error", "error_class": "runtime_error"}
	if path, ok := stringField(raw, "extensionPath"); ok && path != "" {
		projected["extension_path"] = path
	}
	if detail != "" {
		projected["error"] = detail
	}
	normalized, err := json.Marshal(projected)
	if err != nil {
		return execution.NormalizedEvent{}, false, err
	}
	return execution.NormalizedEvent{Type: "error", NormalizedJSON: normalized}, true, nil
}

func boundedAssistantText(value string) (string, bool) {
	value = strings.ToValidUTF8(value, "\uFFFD")
	if len(value) <= maxAssistantText {
		return value, false
	}
	value = value[:maxAssistantText]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return strings.TrimSpace(value), true
}

func (adapter *Adapter) DecodeTerminal(files execution.TerminalFiles) (execution.TerminalResult, error) {
	if adapter == nil {
		return execution.TerminalResult{}, errors.New("Pi adapter is nil")
	}
	if adapter.pathPiSource.Configured() && (files.Profile == domain.AgentExecutionProfileTrustedLocal || files.Profile == "") {
		// Real Pi RPC streams events on stdout and never writes the shim-only
		// /output/result.json. Synthesize a review-ready result here; the final
		// assistant text is substituted by the application from the persisted
		// assistant_message events (latestAttemptAssistantText).
		return execution.TerminalResult{Kind: execution.TerminalReviewReady, Summary: "Agent completed the Local Connected turn."}, nil
	}
	path := strings.TrimSpace(files.Paths["result"])
	if path == "" || !filepath.IsAbs(path) {
		return execution.TerminalResult{}, errors.New("Pi terminal result path is required")
	}
	data, err := adapter.config.ReadFile(path)
	if err != nil {
		return execution.TerminalResult{}, fmt.Errorf("read Pi terminal result: %w", err)
	}
	if len(data) > processbound.ResultLimit {
		return execution.TerminalResult{}, processbound.ErrOutputLimit
	}
	return agent.DecodeResult(data)
}

func (adapter *Adapter) Fingerprint(ctx context.Context) (execution.RuntimeFingerprint, error) {
	if adapter == nil {
		return execution.RuntimeFingerprint{}, errors.New("Pi adapter is nil")
	}
	binding, err := domain.NewAgentExecutionProfileBinding(domain.DefaultAgentExecutionProfile())
	if err != nil {
		return execution.RuntimeFingerprint{}, err
	}
	return adapter.FingerprintForBinding(ctx, binding)
}

func (adapter *Adapter) FingerprintForBinding(ctx context.Context, binding domain.AgentExecutionProfileBinding) (execution.RuntimeFingerprint, error) {
	if adapter == nil {
		return execution.RuntimeFingerprint{}, errors.New("Pi adapter is nil")
	}
	source, err := adapter.sourceForBinding(binding)
	if err != nil {
		return execution.RuntimeFingerprint{}, err
	}
	if err := adapter.validateSelectedSource(ctx, source); err != nil {
		return execution.RuntimeFingerprint{}, err
	}
	return fingerprintForSource(source), nil
}

func fingerprintForSource(source selectedSource) execution.RuntimeFingerprint {
	canonical := strings.Join([]string{
		"chora.pi-runtime-fingerprint.v1", source.version, hex.EncodeToString(source.identity[:]),
	}, "\x00")
	return execution.RuntimeFingerprint{Digest: sha256.Sum256([]byte(canonical)), Version: source.version}
}

type selectedSource struct {
	binding          domain.AgentExecutionProfileBinding
	executable       string
	arguments        []string
	version          string
	identity         [32]byte
	managed          bool
	pathPi           bool
	isolated         bool
	capabilityBundle ManagedCapabilityBundle
}

func (adapter *Adapter) sourceForBinding(binding domain.AgentExecutionProfileBinding) (selectedSource, error) {
	if !binding.Bound() {
		return selectedSource{}, errors.New("Pi Attempt execution profile binding is required")
	}
	restored, err := domain.RestoreAgentExecutionProfileBinding(binding.Record())
	if err != nil || restored != binding {
		return selectedSource{}, errors.New("Pi Attempt execution profile binding drift")
	}
	switch binding.Profile() {
	case domain.AgentExecutionProfileIsolatedLocal:
		s := adapter.config.IsolatedSource
		if !s.valid() {
			return selectedSource{}, errors.New("public isolated Pi source is not prepared")
		}
		return selectedSource{binding: binding, executable: Executable, arguments: IsolatedRPCArguments(), version: IsolatedPiVersion, identity: s.SourceIdentity(), isolated: true}, nil
	case domain.AgentExecutionProfileMinimal, domain.AgentExecutionProfileStandard:
		if binding.RuntimeSource() != domain.ManagedPiRuntimeSource || !adapter.managedSource.valid() {
			return selectedSource{}, errors.New("Pi managed source does not match Attempt profile binding")
		}
		bundle, err := ManagedCapabilityBundleForProfile(binding.Profile())
		if err != nil {
			return selectedSource{}, err
		}
		return selectedSource{
			binding: binding, executable: adapter.managedSource.Executable(),
			arguments: append([]string(nil), exactRPCArguments...), version: adapter.managedSource.RuntimeVersion(),
			identity: adapter.managedSource.SourceIdentity(), managed: true, capabilityBundle: bundle,
		}, nil
	case domain.AgentExecutionProfileTrustedLocal:
		if adapter.pathPiSource.Configured() {
			return selectedSource{
				binding: binding, executable: adapter.pathPiSource.ExecutablePath(),
				arguments: append([]string(nil), pathPiArguments...), version: adapter.pathPiSource.Version(),
				identity: adapter.pathPiSource.SourceIdentity(), pathPi: true,
			}, nil
		}
		if binding.RuntimeSource() != domain.LocalPiRuntimeSource || !adapter.localSource.Configured() || !adapter.localSource.valid() {
			return selectedSource{}, errors.New("Trusted Local requires an explicitly configured compatible local Pi source")
		}
		return selectedSource{
			binding: binding, executable: adapter.localSource.ExecutablePath(),
			arguments: append([]string(nil), nativeRPCArguments...), version: adapter.localSource.RuntimeVersion(),
			identity: adapter.localSource.SourceIdentity(),
		}, nil
	default:
		return selectedSource{}, errors.New("unsupported Pi Attempt execution profile")
	}
}

func (adapter *Adapter) validateSelectedSource(ctx context.Context, source selectedSource) error {
	if source.managed || source.isolated {
		return nil
	}
	if source.pathPi {
		data, err := adapter.config.ReadSourceFile(source.executable)
		if err != nil {
			return fmt.Errorf("read PATH Pi source: %w", err)
		}
		if sha256.Sum256(data) != adapter.pathPiSource.ExecutableSHA256() {
			return errors.New("PATH Pi source identity drift")
		}
		return nil
	}
	if adapter.config.ValidateLocalSource == nil {
		return errors.New("configured local Pi closure validator is unavailable")
	}
	if err := adapter.config.ValidateLocalSource(ctx, adapter.localSource); err != nil {
		return fmt.Errorf("validate configured local Pi package closure: %w", err)
	}
	data, err := adapter.config.ReadSourceFile(adapter.localSource.ResolvedExecutablePath())
	if err != nil {
		return fmt.Errorf("read configured local Pi source: %w", err)
	}
	if sha256.Sum256(data) != adapter.localSource.ExecutableSHA256() {
		return errors.New("configured local Pi source identity drift")
	}
	return nil
}

func digest(data []byte) string {
	value := sha256.Sum256(data)
	return hex.EncodeToString(value[:])
}

func stringField(raw map[string]json.RawMessage, key string) (string, bool) {
	if raw == nil {
		return "", false
	}
	var value string
	if data, ok := raw[key]; !ok || json.Unmarshal(data, &value) != nil {
		return "", false
	}
	return value, true
}

func firstString(raw map[string]json.RawMessage, keys ...string) (string, bool) {
	for _, key := range keys {
		if value, ok := stringField(raw, key); ok {
			return value, true
		}
	}
	return "", false
}

func objectField(raw map[string]json.RawMessage, keys ...string) map[string]json.RawMessage {
	for _, key := range keys {
		var value map[string]json.RawMessage
		if data, ok := raw[key]; ok && json.Unmarshal(data, &value) == nil {
			return value
		}
	}
	return nil
}

func boolField(raw map[string]json.RawMessage, keys ...string) (bool, bool) {
	for _, key := range keys {
		var value bool
		if data, ok := raw[key]; ok && json.Unmarshal(data, &value) == nil {
			return value, true
		}
	}
	return false, false
}

func integerField(raw map[string]json.RawMessage, keys ...string) (int64, bool) {
	for _, key := range keys {
		var number json.Number
		if data, ok := raw[key]; ok && json.Unmarshal(data, &number) == nil {
			value, err := strconv.ParseInt(number.String(), 10, 64)
			if err == nil {
				return value, true
			}
		}
	}
	return 0, false
}

func workspaceRelative(path string) (string, bool) {
	const root = "/workspace/repository/"
	if strings.HasPrefix(path, root) {
		path = strings.TrimPrefix(path, root)
	}
	if path == "" || filepath.IsAbs(path) || filepath.Clean(path) != path || !relativePath.MatchString(filepath.ToSlash(path)) {
		return "", false
	}
	return filepath.ToSlash(path), true
}

func undeclaredAbsolutePath(path string) bool {
	path = strings.TrimSpace(path)
	return filepath.IsAbs(path) && !allowedAbsolutePath(path)
}

func commandUsesUndeclaredAbsolutePath(command string) bool {
	for index := 0; index < len(command); index++ {
		if command[index] != '/' || index > 0 && !absolutePathBoundary(command[index-1]) {
			continue
		}
		if index > 0 && command[index-1] == ':' && index+1 < len(command) && command[index+1] == '/' {
			continue
		}
		end := index + 1
		for end < len(command) && !absolutePathTerminator(command[end]) {
			end++
		}
		candidate := strings.TrimRight(command[index:end], ".,")
		if filepath.IsAbs(candidate) && !allowedAbsolutePath(candidate) {
			return true
		}
		index = end - 1
	}
	return false
}

func allowedAbsolutePath(path string) bool {
	clean := filepath.Clean(path)
	if clean == "/dev/null" {
		return true
	}
	for _, root := range allowedAbsoluteRoots {
		if clean == root || strings.HasPrefix(clean, root+"/") {
			return true
		}
	}
	return false
}

func absolutePathBoundary(value byte) bool {
	return strings.ContainsRune(" \t\r\n'\"`=([{;|&<>", rune(value)) || value == ':'
}

func absolutePathTerminator(value byte) bool {
	return strings.ContainsRune(" \t\r\n'\"`)]};|&<>,", rune(value))
}

func redactCommand(command string) string {
	command = strings.TrimSpace(command)
	if sensitiveCommand.MatchString(command) {
		return "[REDACTED]"
	}
	if len(command) > maxCommandSummary {
		command = command[:maxCommandSummary]
	}
	return command
}

func normalizedToolName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if _, ok := allowedToolNames[value]; !ok {
		return ""
	}
	return value
}

func normalizedErrorClass(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if _, ok := allowedErrorClasses[value]; !ok {
		return "runtime_error"
	}
	return value
}

func projectUsage(raw map[string]json.RawMessage) map[string]int64 {
	projected := make(map[string]int64)
	for _, field := range []struct{ camel, snake string }{
		{"input", "input_tokens"},
		{"output", "output_tokens"},
		{"cacheRead", "cache_read_tokens"},
		{"cacheWrite", "cache_write_tokens"},
		{"totalTokens", "total_tokens"},
	} {
		if value, ok := integerField(raw, field.camel); ok && value >= 0 {
			projected[field.snake] = value
		} else if value, ok := integerField(raw, field.snake); ok && value >= 0 {
			projected[field.snake] = value
		}
	}
	return projected
}

var _ execution.AgentAdapter = (*Adapter)(nil)
var _ execution.BindingFingerprinter = (*Adapter)(nil)
var _ execution.BoundStartPreparer = (*Adapter)(nil)

func reportedPiCost(usage map[string]json.RawMessage) (float64, bool) {
	raw := objectField(usage, "cost")
	value, ok := raw["total"]
	if !ok {
		return 0, false
	}
	var cost float64
	if json.Unmarshal(value, &cost) != nil || cost < 0 || string(value) == "null" {
		return 0, false
	}
	return cost, true
}
