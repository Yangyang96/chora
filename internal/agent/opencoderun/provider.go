// Package opencoderun implements the Local Connected OpenCode run candidate.
// Public construction remains refused until native configuration, authentication
// scope and retry control can be qualified. Execution is Local Connected.
package opencoderun

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
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/opencodediscovery"
	"github.com/Yangyang96/chora/internal/runtimeenv"
	"github.com/Yangyang96/chora/internal/speccoding"
	"github.com/Yangyang96/chora/internal/trustedhost"
)

const AdapterID = "opencode-run"
const protocolVersion = "opencode.run.jsonl.v1"
const maxRuntime = 120 * time.Second

var (
	ErrInstallation   = errors.New("installation_unavailable")
	ErrVersion        = errors.New("incompatible_version")
	ErrAuthentication = errors.New("authentication_unavailable")
	ErrPolicy         = errors.New("configuration_unqualified")
	ErrIdentity       = errors.New("runtime_identity_drift")
	ErrNativeConfig   = fmt.Errorf("%w: native_configuration_sources_unverifiable", ErrPolicy)
	ErrNativeRetry    = fmt.Errorf("%w: native_retry_control_unavailable", ErrPolicy)
	ErrAuthScope      = fmt.Errorf("%w: native_authentication_scope_unverifiable", ErrAuthentication)
)

// Config explicitly selects the existing native home and XDG scope. No credential
// contents, proof import or policy bypass can be supplied. Model is provider/model.
// Empty XDG values use the native defaults under Home. Chora does not change them.
type Config struct {
	Executable, Home, Model                    string
	DataHome, ConfigHome, CacheHome, StateHome string
	NetworkEnvironment                         map[string]string
}
type Adapter struct {
	config        Config
	executable    trustedhost.ExecutableIdentity
	fingerprint   execution.RuntimeFingerprint
	network       runtimeenv.Network
	version       string
	qualification execution.QualificationCache
	// Package fixtures alone supply this checker. Public New has no override.
	nativeCheck      func(context.Context) error
	protocolReviews  *ProtocolReviews
	protocolApproval [32]byte
}

var _ execution.Provider = (*Adapter)(nil)
var _ execution.BoundStartPreparer = (*Adapter)(nil)

func canonicalDirectory(path string) bool {
	resolved, err := filepath.EvalSymlinks(path)
	info, statErr := os.Stat(path)
	return !strings.ContainsAny(path, "\x00\r\n") && err == nil && statErr == nil && info.IsDir() && filepath.IsAbs(path) && filepath.Clean(path) == path && resolved == path
}
func New(ctx context.Context, config Config) (*Adapter, error) {
	return newReviewedAdapter(ctx, config, nil, nil)
}

// NewReviewed consumes a host-owned protocol trust service. It retains the
// independent native configuration, retry and authentication gates.
func NewReviewed(ctx context.Context, config Config, reviews *ProtocolReviews) (*Adapter, error) {
	return newReviewedAdapter(ctx, config, nil, reviews)
}

func newAdapter(ctx context.Context, config Config, nativeCheck func(context.Context) error) (*Adapter, error) {
	return newReviewedAdapter(ctx, config, nativeCheck, nil)
}

func newReviewedAdapter(ctx context.Context, config Config, nativeCheck func(context.Context) error, reviews *ProtocolReviews) (*Adapter, error) {
	a, err := metadataAdapter(ctx, config)
	if err != nil {
		return nil, err
	}
	a.nativeCheck, a.protocolReviews = nativeCheck, reviews
	if err := a.qualify(ctx); err != nil {
		return nil, err
	}
	if approval, ok := reviews.lookup(a.qualificationIdentity()); ok && approval.request.Fixture {
		return nil, ErrProtocolReview
	}
	a.fingerprint, err = a.Fingerprint(ctx)
	if err != nil {
		return nil, err
	}
	return a, nil
}

func metadataAdapter(ctx context.Context, config Config) (*Adapter, error) {
	if !canonicalDirectory(config.Home) || !validModel(config.Model) {
		return nil, ErrPolicy
	}
	for _, path := range []string{config.DataHome, config.ConfigHome, config.CacheHome, config.StateHome} {
		if path != "" && !canonicalDirectory(path) {
			return nil, ErrPolicy
		}
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
	if err != nil {
		return nil, errors.Join(ErrVersion, err)
	}
	if !nativeVersion.MatchString(strings.TrimSpace(version)) {
		return nil, ErrVersion
	}
	a.version = strings.TrimSpace(version)
	return a, nil
}

func validModel(model string) bool {
	provider, name, ok := strings.Cut(model, "/")
	return ok && identifier(provider) && name != "" && len(model) <= 256 &&
		strings.TrimSpace(model) == model && !strings.ContainsAny(model, "\x00\r\n\t ")
}

// Inspect runs bounded metadata probes only. It never attempts inference.
func Inspect(ctx context.Context, config Config) execution.Readiness {
	return InspectReviewed(ctx, config, nil)
}

func InspectReviewed(ctx context.Context, config Config, reviews *ProtocolReviews) execution.Readiness {
	result := execution.Readiness{RuntimeID: AdapterID}
	a, err := NewReviewed(ctx, config, reviews)
	if err != nil {
		result.Failure = err.Error()
		var qualification *QualificationError
		if errors.As(err, &qualification) {
			report := qualification.Report
			result.Qualification = &report
		}
		return result
	}
	result.Ready, result.Fingerprint = true, a.fingerprint
	report, _ := a.qualification.Lookup(a.qualificationIdentity())
	result.Qualification = &report
	return result
}
func (a *Adapter) ID() string { return AdapterID }
func (a *Adapter) Capabilities() execution.AdapterCapabilities {
	return execution.AdapterCapabilities{ResumeMode: execution.ResumeUnsupported}
}
func (a *Adapter) Describe() execution.ProviderDescriptor {
	return execution.ProviderDescriptor{
		ID: AdapterID, Name: "OpenCode run", ProtocolVersion: protocolVersion,
		Environments: []string{domain.TrustedHostExecutionProvider},
		TaskKinds:    []string{"code", "document", "research"},
		Features: []string{
			"registered_contract", "runtime.agent_loop", "runtime.jsonl_rpc",
			"runtime.file_edit", "runtime.command_execution", "runtime.progress_events",
			"runtime.terminal_result", "runtime.cooperative_abort",
			"runtime.allowlisted_event_projection", "runtime.chora_owned_result",
			"runtime.zero_implicit_context",
		},
		ResumeMode: execution.ResumeUnsupported, ShutdownMode: "exit_after_input",
		ApprovalMode: "native_noninteractive", ModelMode: "explicit", CheckEvidenceMode: "none",
	}
}
func (a *Adapter) arguments() []string {
	return []string{"run", "--pure", "--format", "json", "--model", a.config.Model}
}
func (a *Adapter) environment() map[string]string {
	env := map[string]string{"HOME": a.config.Home, "PATH": "/usr/bin:/bin", "LANG": "C.UTF-8",
		"NO_COLOR": "1", "OPENCODE_DISABLE_AUTOUPDATE": "1", "OPENCODE_DISABLE_MODELS_FETCH": "1",
		"OPENCODE_DISABLE_PROJECT_CONFIG": "1", "OPENCODE_DISABLE_DEFAULT_PLUGINS": "1",
		"OPENCODE_DISABLE_EXTERNAL_SKILLS": "1", "OPENCODE_DISABLE_CLAUDE_CODE": "1",
		"CHORA_RESULT_PATH": "/output/result.json", "CHORA_PATCH_PATH": "/output/patch.diff",
		"CHORA_CHECKS_PATH": "/output/checks.json", "CHORA_RUNTIME_IDENTITY_PATH": "/output/runtime-identity.json"}
	for key, value := range map[string]string{"XDG_DATA_HOME": a.config.DataHome, "XDG_CONFIG_HOME": a.config.ConfigHome, "XDG_CACHE_HOME": a.config.CacheHome, "XDG_STATE_HOME": a.config.StateHome} {
		if value != "" {
			env[key] = value
		}
	}
	for key, value := range a.network.Values() {
		env[key] = value
	}
	return env
}
func (a *Adapter) probe(ctx context.Context, args ...string) (string, error) {
	return opencodediscovery.Probe(ctx, a.config.Executable, a.config.Home, a.environment(), args...)
}

func absent(path string) bool { _, err := os.Lstat(path); return errors.Is(err, os.ErrNotExist) }
func (a *Adapter) validatePolicy(ctx context.Context) error {
	if a.network.Validate() != nil || !canonicalDirectory(a.config.Home) {
		return ErrPolicy
	}
	for _, path := range []string{a.config.DataHome, a.config.ConfigHome, a.config.CacheHome, a.config.StateHome} {
		if path != "" && !canonicalDirectory(path) {
			return ErrPolicy
		}
	}
	if a.nativeCheck == nil {
		return errors.Join(ErrNativeConfig, ErrNativeRetry)
	}
	return a.nativeCheck(ctx)
}
func validateWorkspace(root string) error {
	if !canonicalDirectory(root) {
		return ErrPolicy
	}
	// Reject project/ancestor config and native resource directories, including
	// symlinks. This slice never promises to load or project native resources.
	for dir := root; ; dir = filepath.Dir(dir) {
		for _, name := range []string{".opencode", ".agents", ".claude", "opencode.json", "opencode.jsonc", "AGENTS.md", "CLAUDE.md", "CONTEXT.md"} {
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
func (a *Adapter) baseFingerprint(identity trustedhost.ExecutableIdentity) execution.RuntimeFingerprint {
	source := hash(identity)
	policy := hash(struct {
		Config     Config
		Args       []string
		Env        map[string]string
		Policy     string
		Descriptor [32]byte
		Network    [32]byte
		MaxRuntime time.Duration
	}{a.config, a.arguments(), a.environment(), "chora.opencode-run.local.v2:isolated-metadata:host-protocol-review:qualified-native-sources-and-retry-required:no-check-evidence", a.Describe().Digest(), a.network.Digest(), maxRuntime})
	return execution.RuntimeFingerprint{Version: a.version, SourceIdentity: source, SecurityFingerprint: policy, Digest: hash([][32]byte{source, policy})}
}
func (a *Adapter) deriveFingerprint(identity trustedhost.ExecutableIdentity) execution.RuntimeFingerprint {
	fp := a.baseFingerprint(identity)
	fp.Digest = hash(struct{ Base, ProtocolApproval [32]byte }{fp.Digest, a.protocolApproval})
	return fp
}
func (a *Adapter) Fingerprint(ctx context.Context) (execution.RuntimeFingerprint, error) {
	if err := a.validatePolicy(ctx); err != nil {
		return execution.RuntimeFingerprint{}, err
	}
	identity, err := trustedhost.InspectExecutable(a.config.Executable)
	if err != nil || identity != a.executable {
		return execution.RuntimeFingerprint{}, ErrIdentity
	}
	if a.protocolApproval != ([32]byte{}) {
		approval, ok := a.protocolReviews.lookup(a.qualificationIdentity())
		if !ok || approval.digest != a.protocolApproval || approval.request.Fixture {
			return execution.RuntimeFingerprint{}, ErrIdentity
		}
	}
	report, ok := a.qualification.Lookup(a.qualificationIdentity())
	if !ok || report.State != execution.QualificationVerified {
		return execution.RuntimeFingerprint{}, ErrIdentity
	}
	return a.deriveFingerprint(identity), nil
}
func (a *Adapter) Binding(ctx context.Context) (domain.AgentExecutionProfileBinding, error) {
	fp, err := a.Fingerprint(ctx)
	if err != nil {
		return domain.AgentExecutionProfileBinding{}, err
	}
	provenance := "explicit"
	return domain.NewRuntimeProfileBinding(domain.RuntimeBindingV1{
		SchemaVersion: "chora.runtime-binding.v1", RuntimeID: AdapterID,
		ProtocolVersion:          a.Describe().ProtocolVersion,
		SourceIdentity:           fmt.Sprintf("%x", fp.SourceIdentity),
		ConfigurationFingerprint: fmt.Sprintf("%x", fp.Digest),
		ExecutionProvider:        domain.TrustedHostExecutionProvider,
		SecurityFingerprint:      fmt.Sprintf("%x", fp.SecurityFingerprint),
		CapabilityDigest:         fmt.Sprintf("%x", a.Describe().Digest()), ModelProvenance: provenance,
	})
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
	// never silently ignore one or reinterpret a Pi catalog as OpenCode selection.
	if r.Attempt.ModelBinding().Configured() {
		return execution.StartPreparation{}, execution.ErrUnsupportedCapability
	}
	if err := validateWorkspace(r.WorkspaceRoot); err != nil {
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
	prompt = strings.ReplaceAll(prompt, "Pi's native permissions apply.", "OpenCode native permissions apply. Execution is Local Connected, No Sandbox.")
	prompt = strings.ReplaceAll(prompt, "Pi native permissions apply.", "OpenCode native permissions apply.")
	prompt += "\nChora cannot independently observe checks from this runtime. Never claim that a tool result establishes human acceptance."
	if r.Attempt.Sequence() > 1 {
		prompt += "\nHuman review correction within the frozen contract:\n" + r.Attempt.ContextDelta()
	}
	target, _ := execution.NewExecutionTarget(AdapterID, domain.TrustedHostExecutionProvider)
	invocation, err := execution.NewInvocation(execution.InvocationParams{
		AdapterID: AdapterID, Executable: a.config.Executable,
		Arguments: a.arguments(), Environment: a.environment(),
		WorkingRoot: r.WorkspaceRoot, Stdin: []byte(prompt), LaunchToken: r.LaunchToken, Target: target,
	})
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
	return trustedhost.Config{
		RuntimeRoot: root, MaxRuntime: maxRuntime, AllowedAdapterID: AdapterID,
		AllowedExecutable: a.executable, AllowedArguments: a.arguments(),
		DirectWorkingRoot: true, CloseStdinAfterInput: true, AmbientEnvironment: []string{},
		RuntimeClosureIdentity: a.fingerprint.Digest,
		ValidateRuntimeClosure: func(ctx context.Context) ([32]byte, error) {
			fp, err := a.Fingerprint(ctx)
			return fp.Digest, err
		},
		ValidateCapabilityArguments: func(i execution.Invocation) ([]string, error) {
			if !reflect.DeepEqual(i.Environment(), a.environment()) || !reflect.DeepEqual(i.Arguments(), a.arguments()) {
				return nil, ErrIdentity
			}
			if err := validateWorkspace(i.WorkingRoot()); err != nil {
				return nil, err
			}
			return i.Arguments(), nil
		},
	}
}
