// Package codexexec implements the explicitly configured Local Connected Codex
// exec protocol. It does not enable the historical codex adapter or isolation.
package codexexec

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

	"github.com/Yangyang96/chora/internal/codexdiscovery"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/speccoding"
	"github.com/Yangyang96/chora/internal/trustedhost"
)

const AdapterID = "codex-exec"
const QualifiedVersion = "codex-cli 0.155.1"

var (
	ErrInstallation   = errors.New("installation_unavailable")
	ErrVersion        = errors.New("incompatible_version")
	ErrAuthentication = errors.New("authentication_unavailable")
	ErrPolicy         = errors.New("configuration_unqualified")
	ErrIdentity       = errors.New("runtime_identity_drift")
)

// Config selects existing native authentication, without reading/copying secrets.
// Home must be a dedicated empty directory; AuthHome is an explicit existing
// CODEX_HOME. Model is an explicit selection, not a fabricated model catalog.
// Model permissions are established only by real execution.
type Config struct{ Executable, AuthHome, Home, Model string }
type Adapter struct {
	config      Config
	executable  trustedhost.ExecutableIdentity
	fingerprint execution.RuntimeFingerprint
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
	entries, err := os.ReadDir(config.Home)
	if err != nil || len(entries) != 0 {
		return nil, ErrPolicy
	}
	identity, err := trustedhost.InspectExecutable(config.Executable)
	if err != nil {
		return nil, ErrInstallation
	}
	a := &Adapter{config: config, executable: identity}
	version, err := a.probe(ctx, "--version")
	if err != nil || strings.TrimSpace(version) != QualifiedVersion {
		return nil, ErrVersion
	}
	// Reject configuration channels that --ignore-user-config does not disable.
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
	return execution.ProviderDescriptor{ID: AdapterID, Name: "Codex exec", ProtocolVersion: "codex.exec.jsonl.v1", Environments: []string{domain.TrustedHostExecutionProvider}, TaskKinds: []string{"code", "document", "research"}, Features: []string{"registered_contract", "runtime.agent_loop", "runtime.jsonl_rpc", "runtime.file_edit", "runtime.command_execution", "runtime.progress_events", "runtime.terminal_result", "runtime.cooperative_abort", "runtime.allowlisted_event_projection", "runtime.chora_owned_result", "runtime.zero_implicit_context"}, ResumeMode: execution.ResumeUnsupported, ShutdownMode: "exit_after_input", ApprovalMode: "native_noninteractive", ModelMode: mode, CheckEvidenceMode: "none"}
}
func quoted(value string) string { raw, _ := json.Marshal(value); return string(raw) }
func (a *Adapter) arguments() []string {
	args := []string{"exec", "--json", "--ephemeral", "--ignore-user-config", "--ignore-rules", "--skip-git-repo-check", "--sandbox", "workspace-write", "--color", "never"}
	if a.config.Model != "" {
		args = append(args, "--model", a.config.Model)
	}
	for _, setting := range []string{`approval_policy="never"`, `project_doc_max_bytes=0`, `features.hooks=false`, `features.plugins=false`, `features.apps=false`, `features.remote_plugin=false`, `features.memories=false`, `features.chronicle=false`, `features.workspace_dependencies=false`, `features.skill_mcp_dependency_install=false`, `features.browser_use=false`, `features.computer_use=false`, `features.image_generation=false`, `features.skip_host_skill_discovery=true`, `features.skill_search=false`, `features.multi_agent=false`, `features.multi_agent_v2=false`, `features.shell_snapshot=false`, `web_search="disabled"`, `shell_environment_policy.inherit="none"`, `shell_environment_policy.set={PATH="/usr/bin:/bin",HOME=` + quoted(a.config.Home) + `}`} {
		args = append(args, "-c", setting)
	}
	return append(args, "-")
}
func (a *Adapter) environment() map[string]string {
	return map[string]string{"HOME": a.config.Home, "CODEX_HOME": a.config.AuthHome, "PATH": "/usr/bin:/bin", "LANG": "C.UTF-8", "CHORA_RESULT_PATH": "/output/result.json", "CHORA_PATCH_PATH": "/output/patch.diff", "CHORA_CHECKS_PATH": "/output/checks.json", "CHORA_RUNTIME_IDENTITY_PATH": "/output/runtime-identity.json"}
}
func (a *Adapter) probe(ctx context.Context, args ...string) (string, error) {
	return codexdiscovery.Probe(ctx, a.config.Executable, a.config.Home, a.config.AuthHome, args...)
}
func (a *Adapter) Authentication(ctx context.Context) error {
	// The CLI, not Chora, owns credential inspection. Do not invoke login/logout.
	out, err := a.probe(ctx, "login", "status")
	if err != nil || strings.TrimSpace(out) != "Logged in using ChatGPT" {
		return ErrAuthentication
	}
	return nil
}
func absent(path string) bool { _, err := os.Lstat(path); return errors.Is(err, os.ErrNotExist) }
func (a *Adapter) validatePolicy() error {
	if !canonicalDirectory(a.config.AuthHome) || !canonicalDirectory(a.config.Home) {
		return ErrPolicy
	}
	homeEntries, homeErr := os.ReadDir(a.config.Home)
	if homeErr != nil || len(homeEntries) != 0 {
		return ErrPolicy
	}
	// Fail closed on system requirements/config and extra user config layers.
	// Never parse these potentially secret-bearing files or execute their hooks.
	for _, path := range []string{filepath.Join(a.config.Home, ".codex"), filepath.Join(a.config.Home, ".agents"), "/etc/codex/config.toml", "/etc/codex/requirements.toml", filepath.Join(a.config.AuthHome, "managed_config.toml")} {
		if !absent(path) {
			return ErrPolicy
		}
	}
	entries, err := os.ReadDir(a.config.AuthHome)
	if err != nil {
		return ErrPolicy
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".config.toml") {
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
		for _, name := range []string{".codex", ".agents"} {
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
		Args   []string
		Env    map[string]string
		Policy string
	}{a.arguments(), a.environment(), "chora.codex-exec.local.v1:empty-home:reject-project-config:no-native-resources:no-check-evidence"})
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
	// never silently ignore one or reinterpret a Pi catalog as Codex selection.
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
	prompt = strings.ReplaceAll(prompt, "Pi's native permissions apply.", "Codex workspace-write permissions apply; this is still Local Connected, No Sandbox.")
	prompt = strings.ReplaceAll(prompt, "Pi native permissions apply.", "Codex workspace-write permissions apply.")
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
