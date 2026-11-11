// Package claudeprint implements the explicitly configured Local Connected Claude Code
// print protocol. It does not grant isolated execution or interactive approvals.
package claudeprint

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/Yangyang96/chora/internal/claudediscovery"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/runtimeenv"
	"github.com/Yangyang96/chora/internal/speccoding"
	"github.com/Yangyang96/chora/internal/trustedhost"
)

const AdapterID = "claude-print"
const QualifiedVersion = "2.1.251 (Claude Code)"

var (
	ErrInstallation   = errors.New("installation_unavailable")
	ErrVersion        = errors.New("incompatible_version")
	ErrAuthentication = errors.New("authentication_unavailable")
	ErrPolicy         = errors.New("configuration_unqualified")
	ErrIdentity       = errors.New("runtime_identity_drift")
)

// Config selects existing native authentication, without reading/copying secrets.
// Home must be a dedicated private directory; AuthHome is an explicit existing
// CLAUDE_CONFIG_DIR. Model is an explicit selection, not a fabricated model catalog.
// Model permissions are established only by real execution.
type Config struct {
	Executable, AuthHome, Home, Model string
	// NetworkEnvironment is explicitly selected, copied and bound at construction.
	// Nil uses native networking; ambient variables are never read.
	NetworkEnvironment map[string]string
}
type Adapter struct {
	config      Config
	executable  trustedhost.ExecutableIdentity
	fingerprint execution.RuntimeFingerprint
	network     runtimeenv.Network
}

var _ execution.Provider = (*Adapter)(nil)
var _ execution.BoundStartPreparer = (*Adapter)(nil)

func canonicalDirectory(path string) bool {
	resolved, err := filepath.EvalSymlinks(path)
	info, statErr := os.Stat(path)
	return !strings.ContainsAny(path, "\x00\r\n") && err == nil && statErr == nil && info.IsDir() && filepath.IsAbs(path) && filepath.Clean(path) == path && resolved == path
}
func New(ctx context.Context, config Config) (*Adapter, error) {
	if !canonicalDirectory(config.AuthHome) || !canonicalDirectory(config.Home) || config.AuthHome == config.Home || strings.TrimSpace(config.Model) != config.Model || strings.HasPrefix(config.Model, "-") || strings.ContainsAny(config.Model, "\x00\r\n") {
		return nil, ErrPolicy
	}

	identity, err := trustedhost.InspectExecutable(config.Executable)
	if err != nil {
		return nil, ErrInstallation
	}
	network, err := runtimeenv.New(config.NetworkEnvironment)
	if err != nil {
		return nil, ErrPolicy
	}
	config.NetworkEnvironment = nil
	a := &Adapter{config: config, executable: identity, network: network}
	version, err := a.probe(ctx, "--version")
	if err != nil || strings.TrimSpace(version) != QualifiedVersion {
		return nil, ErrVersion
	}
	// Reject managed configuration channels that safe mode does not disable.
	if err := a.validatePolicy(); err != nil {
		return nil, err
	}
	if err := a.Authentication(ctx); err != nil {
		return nil, err
	}
	a.fingerprint = a.deriveFingerprint(identity)
	return a, nil
}

// Inspect separates native installation/auth readiness from model permission.
// Ready never means a real task or a particular model has been qualified.
func Inspect(ctx context.Context, config Config) execution.Readiness {
	result := execution.Readiness{RuntimeID: AdapterID}
	adapter, err := New(ctx, config)
	if err != nil {
		result.Failure = err.Error()
		return result
	}
	result.Ready = true
	result.Fingerprint = adapter.fingerprint
	return result
}
func (a *Adapter) ID() string { return AdapterID }
func (a *Adapter) Capabilities() execution.AdapterCapabilities {
	return execution.AdapterCapabilities{ResumeMode: execution.ResumeUnsupported}
}
func (a *Adapter) Describe() execution.ProviderDescriptor {
	mode := "native_default"
	if a.config.Model != "" {
		mode = "explicit"
	}
	return execution.ProviderDescriptor{ID: AdapterID, Name: "Claude Code print", ProtocolVersion: "claude.print.stream-json.v1", Environments: []string{domain.TrustedHostExecutionProvider}, TaskKinds: []string{"code", "document", "research"}, Features: []string{"registered_contract", "runtime.agent_loop", "runtime.jsonl_rpc", "runtime.file_edit", "runtime.command_execution", "runtime.progress_events", "runtime.terminal_result", "runtime.cooperative_abort", "runtime.allowlisted_event_projection", "runtime.chora_owned_result", "runtime.zero_implicit_context"}, ResumeMode: execution.ResumeUnsupported, ShutdownMode: "exit_after_input", ApprovalMode: "native_noninteractive", ModelMode: mode, CheckEvidenceMode: "none"}
}

// The explicitly selected built-in tools preserve coding/document tasks without
// interactive approval. Native customizations are disabled.
func (a *Adapter) arguments() []string {
	args := []string{"--safe-mode", "--restricted", "--setting-sources", "", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "--disable-slash-commands", "--no-chrome", "--print", "--verbose", "--output-format", "stream-json", "--no-session-persistence", "--permission-mode", "dontAsk", "--tools", "Read,Glob,Grep,Edit,Write,Bash", "--allowedTools", "Read,Glob,Grep,Edit,Write,Bash", "--max-turns", "8"}
	if a.config.Model != "" {
		args = append(args, "--model", a.config.Model)
	}
	return args
}
func (a *Adapter) environment() map[string]string {
	env := map[string]string{"HOME": a.config.Home, "CLAUDE_CONFIG_DIR": a.config.AuthHome, "PATH": "/usr/bin:/bin", "LANG": "C.UTF-8", "CLAUDE_CODE_SAFE_MODE": "1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1", "DISABLE_AUTOUPDATER": "1", "CHORA_RESULT_PATH": "/output/result.json", "CHORA_PATCH_PATH": "/output/patch.diff", "CHORA_CHECKS_PATH": "/output/checks.json", "CHORA_RUNTIME_IDENTITY_PATH": "/output/runtime-identity.json"}
	for k, v := range a.network.Values() {
		env[k] = v
	}
	return env
}
func (a *Adapter) probe(ctx context.Context, args ...string) (string, error) {
	return claudediscovery.Probe(ctx, a.config.Executable, a.config.Home, a.environment(), args...)
}
func (a *Adapter) Authentication(ctx context.Context) error {
	// The CLI, not Chora, owns credential inspection. Do not invoke login/logout.
	out, err := a.probe(ctx, "--safe-mode", "--setting-sources", "", "auth", "status")
	var status struct {
		LoggedIn bool   `json:"loggedIn"`
		Method   string `json:"authMethod"`
		Provider string `json:"apiProvider"`
	}
	if err != nil || json.Unmarshal([]byte(out), &status) != nil || !status.LoggedIn || status.Method != "claude.ai" || status.Provider != "firstParty" {
		return ErrAuthentication
	}
	return nil
}
func absent(path string) bool { _, err := os.Lstat(path); return errors.Is(err, os.ErrNotExist) }
func (a *Adapter) validatePolicy() error {
	if a.network.Validate() != nil {
		return ErrPolicy
	}
	if !canonicalDirectory(a.config.AuthHome) || !canonicalDirectory(a.config.Home) {
		return ErrPolicy
	}
	// Safe mode skips user/project customizations but retains managed policy.
	// Refuse managed sources rather than overriding an organization's controls.
	for _, path := range []string{
		"/Library/Application Support/ClaudeCode/managed-settings.json", "/Library/Application Support/ClaudeCode/managed-settings.d",
		"/Library/Managed Preferences/com.anthropic.claudecode.plist", "/etc/claude-code/managed-settings.json", "/etc/claude-code/managed-settings.d",
		filepath.Join(a.config.AuthHome, "managed-settings.json"), filepath.Join(a.config.AuthHome, "managed-settings.d"),
		filepath.Join(a.config.Home, ".env"),
	} {
		if !absent(path) {
			return ErrPolicy
		}
	}
	// Only native scratch state is allowed in the dedicated home. Authentication
	// stays in the explicitly selected native scope; Chora never reads its secrets.
	entries, err := os.ReadDir(a.config.Home)
	if err != nil {
		return ErrPolicy
	}
	for _, entry := range entries {
		switch entry.Name() {
		case ".claude.json", ".claude.json.backup", ".cache":
		default:
			return ErrPolicy
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return ErrPolicy
		}
	}
	return nil
}
func validateWorkspace(root string) error {
	if !canonicalDirectory(root) {
		return ErrPolicy
	}
	// Reject project/ancestor config and native resource directories, including
	// symlinks. This slice never promises to load or project native resources.
	for dir := root; ; dir = filepath.Dir(dir) {
		for _, name := range []string{".claude", ".agents", ".mcp.json", "CLAUDE.md", "CLAUDE.local.md", ".env"} {
			if !absent(filepath.Join(dir, name)) {
				return ErrPolicy
			}
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	return nil
}
func hash(v any) [32]byte { b, _ := json.Marshal(v); return sha256.Sum256(b) }
func (a *Adapter) deriveFingerprint(identity trustedhost.ExecutableIdentity) execution.RuntimeFingerprint {
	source := hash(identity)
	policy := hash(struct {
		Args    []string
		Env     map[string]string
		Policy  string
		Network [32]byte
	}{a.arguments(), a.environment(), "chora.claude-print.local.v1:safe-mode:restricted-explicit-tools:reject-project-config:no-native-resources:no-check-evidence", a.network.Digest()})
	return execution.RuntimeFingerprint{Version: QualifiedVersion, SourceIdentity: source, SecurityFingerprint: policy, Digest: hash([][32]byte{source, policy})}
}
func (a *Adapter) Fingerprint(ctx context.Context) (execution.RuntimeFingerprint, error) {
	if err := a.validatePolicy(); err != nil {
		return execution.RuntimeFingerprint{}, err
	}
	identity, err := trustedhost.InspectExecutable(a.config.Executable)
	if err != nil || identity != a.executable {
		return execution.RuntimeFingerprint{}, ErrIdentity
	}
	return a.deriveFingerprint(identity), nil
}
func (a *Adapter) Binding(ctx context.Context) (domain.AgentExecutionProfileBinding, error) {
	fp, err := a.Fingerprint(ctx)
	if err != nil {
		return domain.AgentExecutionProfileBinding{}, err
	}
	provenance := "native_default"
	if a.config.Model != "" {
		provenance = "explicit"
	}
	return domain.NewRuntimeProfileBinding(domain.RuntimeBindingV1{SchemaVersion: "chora.runtime-binding.v1", RuntimeID: AdapterID, ProtocolVersion: a.Describe().ProtocolVersion, SourceIdentity: fmt.Sprintf("%x", fp.SourceIdentity), ConfigurationFingerprint: fmt.Sprintf("%x", fp.Digest), ExecutionProvider: domain.TrustedHostExecutionProvider, SecurityFingerprint: fmt.Sprintf("%x", fp.SecurityFingerprint), CapabilityDigest: fmt.Sprintf("%x", a.Describe().Digest()), ModelProvenance: provenance})
}
func (a *Adapter) PrepareStart(ctx context.Context, r execution.StartRequest) (execution.Invocation, error) {
	p, e := a.PrepareBoundStart(ctx, r)
	return p.Invocation, e
}
func (a *Adapter) PrepareBoundStart(ctx context.Context, r execution.StartRequest) (execution.StartPreparation, error) {
	fp, err := a.Fingerprint(ctx)
	if err != nil {
		return execution.StartPreparation{}, err
	}
	expected, err := a.Binding(ctx)
	if err != nil {
		return execution.StartPreparation{}, err
	}
	if r.Attempt.AgentExecutionProfileBinding() != expected || r.Attempt.AdapterID() != AdapterID || r.Attempt.ExternalSession() != "" || !r.Run.ID().Valid() || !r.Attempt.ID().Valid() || r.Attempt.RunID() != r.Run.ID() || r.Attempt.ContextDigest() != sha256.Sum256(r.SnapshotDocument) {
		return execution.StartPreparation{}, ErrIdentity
	}
	// Independently frozen model records are wired by the later product slice;
	// never silently ignore one or reinterpret a Pi catalog as Claude Code selection.
	if r.Attempt.ModelBinding().Configured() {
		return execution.StartPreparation{}, execution.ErrUnsupportedCapability
	}
	if err := validateWorkspace(r.WorkspaceRoot); err != nil {
		return execution.StartPreparation{}, err
	}
	if err := a.Authentication(ctx); err != nil {
		return execution.StartPreparation{}, err
	}
	var contract struct {
		Acceptance struct {
			Commands []speccoding.BoundedCommand `json:"verification_commands"`
		} `json:"acceptance"`
		Task struct {
			Resources []struct {
				Checks struct {
					Mode string `json:"mode"`
				} `json:"checks"`
			} `json:"resources"`
		} `json:"task"`
	}
	if json.Unmarshal(r.ExecutionContractDocument, &contract) != nil {
		return execution.StartPreparation{}, execution.ErrInvalidRuntimeConfig
	}
	for _, resource := range contract.Task.Resources {
		if resource.Checks.Mode == "auto" {
			return execution.StartPreparation{}, execution.ErrUnsupportedCapability
		}
	}
	var commands []string
	for _, command := range contract.Acceptance.Commands {
		text, e := speccoding.CanonicalCommandText(command)
		if e != nil {
			return execution.StartPreparation{}, e
		}
		commands = append(commands, text)
	}
	prompt, err := execution.WrapLocalConnectedExecutionPrompt(r.SnapshotDocument, r.ExecutionContractDocument, commands)
	if err != nil {
		return execution.StartPreparation{}, err
	}
	prompt = strings.ReplaceAll(prompt, "Pi's native permissions apply.", "Claude Code restricted explicit tools apply; this is still Local Connected, No Sandbox.")
	prompt = strings.ReplaceAll(prompt, "Pi native permissions apply.", "Claude Code restricted explicit tools apply.")
	prompt += "\nChora cannot independently observe checks from this runtime. Never claim that a tool result establishes human acceptance."
	if r.Attempt.Sequence() > 1 {
		prompt += "\nHuman review correction within the frozen contract:\n" + r.Attempt.ContextDelta()
	}
	target, _ := execution.NewExecutionTarget(AdapterID, domain.TrustedHostExecutionProvider)
	invocation, err := execution.NewInvocation(execution.InvocationParams{AdapterID: AdapterID, Executable: a.config.Executable, Arguments: a.arguments(), Environment: a.environment(), WorkingRoot: r.WorkspaceRoot, Stdin: []byte(prompt), LaunchToken: r.LaunchToken, Target: target})
	if err != nil {
		return execution.StartPreparation{}, err
	}
	return execution.NewStartPreparation(invocation, fp)
}
func (a *Adapter) PrepareResume(context.Context, execution.ResumeRequest) (execution.Invocation, error) {
	return execution.Invocation{}, execution.ErrUnsupportedCapability
}
func (a *Adapter) Recover(execution.RecoveryRequest) execution.RecoveryPolicy {
	return execution.RecoveryPolicy{FreshRetry: true, StopOnRestart: true, MissingRouteUncertain: true}
}

// SupervisorConfig pins the same independently derived policy at the actual spawn
// gate. An embedding caller must register this supervisor only for trusted_host.
func (a *Adapter) SupervisorConfig(root string) trustedhost.Config {
	return trustedhost.Config{RuntimeRoot: root, AllowedAdapterID: AdapterID, AllowedExecutable: a.executable, AllowedArguments: a.arguments(), DirectWorkingRoot: true, CloseStdinAfterInput: true, AmbientEnvironment: []string{}, RuntimeClosureIdentity: a.fingerprint.Digest, ValidateRuntimeClosure: func(ctx context.Context) ([32]byte, error) { fp, e := a.Fingerprint(ctx); return fp.Digest, e }, ValidateCapabilityArguments: func(i execution.Invocation) ([]string, error) {
		if !reflect.DeepEqual(i.Environment(), a.environment()) || !reflect.DeepEqual(i.Arguments(), a.arguments()) {
			return nil, ErrIdentity
		}
		if err := validateWorkspace(i.WorkingRoot()); err != nil {
			return nil, err
		}
		return i.Arguments(), nil
	}}
}
