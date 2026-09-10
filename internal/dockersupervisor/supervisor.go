package dockersupervisor

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Yangyang96/chora/internal/acceptanceauthority"
	agentpi "github.com/Yangyang96/chora/internal/agent/pi"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
)

const (
	allowedAdapter        = "pi"
	allowedExecutable     = "chora-pi-rpc"
	AttemptImage          = "chora/g2-m2e-pi-codex:0.84.2-v5"
	BoundaryImage         = "chora/g2-m2e-codex-boundary:0.1-v4"
	DockerEngineVersion   = "29.6.1"
	DockerContext         = "colima"
	RequiredColimaVersion = "0.10.3"

	pinnedPolicyDigest = "efe8918d0c9c4232c8292941f573fe386d93a3faa051329c883c9b8349b66f04"

	persistedLogBytes = 10 << 20
	artifactBytes     = 100 << 20
	toolCachePath     = "/workspace/.chora-cache/go-build"
	toolTempPath      = "/workspace/.chora-tmp"
	commandTimeout    = 600 * time.Second
	attemptTimeout    = 1200 * time.Second

	EnvRunID               = "CHORA_RUN_ID"
	EnvAttemptID           = "CHORA_ATTEMPT_ID"
	EnvContextDigest       = "CHORA_CONTEXT_DIGEST"
	EnvContractDigest      = "CHORA_CONTRACT_DIGEST"
	EnvPolicyDigest        = "CHORA_POLICY_DIGEST"
	EnvExecutionProfile    = "CHORA_AGENT_EXECUTION_PROFILE"
	EnvRuntimeSource       = "CHORA_RUNTIME_SOURCE"
	EnvRuntimeSourceID     = "CHORA_RUNTIME_SOURCE_IDENTITY"
	EnvRuntimeVersion      = "CHORA_RUNTIME_VERSION"
	EnvExecutionProvider   = "CHORA_EXECUTION_PROVIDER"
	EnvCapabilityPolicy    = "CHORA_CAPABILITY_POLICY"
	EnvCapabilityBundleID  = "CHORA_CAPABILITY_BUNDLE_ID"
	EnvCapabilityBundleSHA = "CHORA_CAPABILITY_BUNDLE_SHA256"
	EnvTrustDisclosure     = "CHORA_TRUST_DISCLOSURE_POLICY"
	EnvAttemptImageID      = "CHORA_ATTEMPT_IMAGE_ID"
	EnvBoundaryImageID     = "CHORA_BOUNDARY_IMAGE_ID"
	EnvCommandTimeout      = "CHORA_COMMAND_TIMEOUT_SECONDS"
	EnvAttemptTimeout      = "CHORA_ATTEMPT_TIMEOUT_SECONDS"
	EnvPersistedLogLimit   = "CHORA_PERSISTED_LOG_LIMIT_BYTES"
	EnvArtifactLimit       = "CHORA_ARTIFACT_LIMIT_BYTES"
	EnvResultPath          = "CHORA_RESULT_PATH"
	EnvPatchPath           = "CHORA_PATCH_PATH"
	EnvChecksPath          = "CHORA_CHECKS_PATH"
	EnvRuntimeIdentityPath = "CHORA_RUNTIME_IDENTITY_PATH"

	managedSourceContractVersion = "chora.pi-runtime-source.v4"
	managedSourceKind            = "managed_image"
	managedRuntimeVersion        = "0.84.2"
	managedRuntimeConfigDigest   = "b4bb9e6e599732e31a5f20ff95f48587c8b702a63c754940b0a4631450f1a4e9"
	attemptRootMarkerSchema      = "chora.docker-attempt-root/v1"
	attemptRootMarkerName        = ".chora-attempt-root.json"

	containerOwnershipObservationFormat = `{"name":{{json .Name}},"labels":{{json .Config.Labels}}}`
	networkOwnershipObservationFormat   = `{"name":{{json .Name}},"labels":{{json .Labels}}}`
)

var (
	ErrInvalidConfig     = errors.New("invalid docker supervisor config")
	ErrUnknownHandle     = errors.New("unknown docker runtime handle")
	ErrInvalidStreamRead = errors.New("invalid docker stream read")
	ErrInvalidDrain      = errors.New("invalid docker stream drain")
	ErrFinalizeNotReady  = errors.New("docker runtime is not ready to finalize")
	ErrNotQualified      = errors.New("Docker execution is not capability-qualified")

	frozenArguments = []string{
		"--mode", "rpc", "--no-session", "--no-extensions", "--no-skills",
		"--no-prompt-templates", "--no-themes", "--no-context-files", "--no-approve",
		"--provider", "openai-codex", "--model", "gpt-5.6-sol",
	}
)

type Config struct {
	Runner                        CommandRunner
	RuntimeRoot                   string
	ArtifactRoot                  string
	PolicyDigest                  string
	AttemptImageID                string
	BoundaryImageID               string
	VerifierImageID               string
	RuntimeSourceIdentity         string
	CredentialSource              string
	TrustAnchorSource             string
	CooperativeWait               time.Duration
	DeathWait                     time.Duration
	AttemptTimeout                time.Duration
	AcceptanceTimeoutPolicyDigest string
	AcceptanceAuthority           *acceptanceauthority.Controller
	CapabilityContract            CapabilityProbeContract
	EngineQualification           EngineQualification
	ColimaVersion                 func(context.Context) (string, error)
}

type ProviderIdentity struct {
	DockerClientVersion string `json:"docker_client_version"`
	DockerServerVersion string `json:"docker_server_version"`
	DockerContext       string `json:"docker_context"`
	ColimaVersion       string `json:"colima_version"`
}

func (identity ProviderIdentity) Valid() bool {
	// ProviderIdentity is a diagnostic compatibility projection only. Engine
	// qualification remains the sole execution authority, so validity must not
	// reintroduce provider names or exact legacy client/version requirements.
	return validText(identity.DockerServerVersion, 128) && validText(identity.DockerContext, 256)
}

type Supervisor struct {
	config           Config
	recoveryScope    string
	operationLedger  *OperationLedger
	mu               sync.Mutex
	byHandle         map[string]*attemptRecord
	byIdentity       map[string]*attemptRecord
	byLaunch         map[string]*attemptRecord
	providerIdentity ProviderIdentity
	engineIdentity   EngineIdentity
	recoveryComplete bool
	loadProjection   func(string, string) (taskProjection, error)
}

type attemptRecord struct {
	mu                                                    sync.Mutex
	handle                                                execution.RuntimeHandle
	identity                                              execution.ProcessIdentity
	launch                                                execution.LaunchToken
	sink                                                  execution.RuntimeSink
	process                                               Process
	boundaryWatch                                         Process
	root, baseline, repository, contextDir                string
	artifactDir, artifactLocatorPrefix                    string
	runID, attemptID, taskID                              string
	executionProfile, capabilityPolicy                    string
	capabilityBundleID, capabilityBundleSHA256            string
	container, boundary, internalNetwork, upstreamNetwork string
	containerDockerID, boundaryDockerID                   string
	stdout, stderr                                        cappedStream
	done                                                  chan struct{}
	exited                                                bool
	exitObserved                                          bool
	exitCode                                              int
	drained                                               map[execution.StreamKind]bool
	stdinClosed, abortSent, stopRequested, timedOut       bool
	terminationCause                                      execution.TerminationCause
	attemptTimeout                                        time.Duration
	acceptanceDirective                                   acceptanceauthority.Directive
	acceptanceInjectionState                              acceptanceInjectionState
	acceptanceInjectionDone                               chan struct{}
	acceptanceExitObserved                                chan struct{}
	acceptanceTerminalUnproven                            bool
	startReturned                                         bool
	recovered                                             bool
	terminal                                              execution.TerminalFiles
	cleanupMu                                             sync.Mutex
}

type acceptanceInjectionState uint8

const (
	acceptanceInjectionNone acceptanceInjectionState = iota
	acceptanceInjectionPending
	acceptanceInjectionConfirmed
	acceptanceInjectionFailed
)

func New(config Config) (*Supervisor, error) {
	if config.Runner == nil {
		return nil, fmt.Errorf("%w: setup-qualified Docker Runner is required", ErrInvalidConfig)
	}
	config.RuntimeRoot = filepath.Clean(config.RuntimeRoot)
	if strings.TrimSpace(config.ArtifactRoot) == "" {
		config.ArtifactRoot = filepath.Join(filepath.Dir(config.RuntimeRoot), "artifacts")
	}
	config.ArtifactRoot = filepath.Clean(config.ArtifactRoot)
	if !filepath.IsAbs(config.RuntimeRoot) || !validDigest(config.PolicyDigest) ||
		!filepath.IsAbs(config.ArtifactRoot) || config.ArtifactRoot == config.RuntimeRoot ||
		!validImageID(config.AttemptImageID) || !validImageID(config.BoundaryImageID) || config.VerifierImageID != "" && !validImageID(config.VerifierImageID) || !validDigest(config.RuntimeSourceIdentity) ||
		(config.CredentialSource != "" && !filepath.IsAbs(config.CredentialSource)) || !filepath.IsAbs(config.TrustAnchorSource) ||
		(config.AcceptanceTimeoutPolicyDigest != "" && !validDigest(config.AcceptanceTimeoutPolicyDigest)) ||
		config.PolicyDigest != pinnedPolicyDigest || config.CooperativeWait < 0 || config.DeathWait < 0 || config.AttemptTimeout < 0 {
		return nil, ErrInvalidConfig
	}
	hasContract := len(config.CapabilityContract.canonical) != 0
	hasQualification := len(config.EngineQualification.canonical) != 0
	if !hasContract || !hasQualification {
		return nil, fmt.Errorf("%w: exact capability contract and Engine qualification are required", ErrInvalidConfig)
	}
	contract, contractErr := RestoreCapabilityProbeContract(config.CapabilityContract.Record())
	restored, qualificationErr := RestoreEngineQualification(config.EngineQualification.Record())
	if contractErr != nil || contract.Digest() != config.CapabilityContract.Digest() ||
		contract.SandboxPolicyDigest() != config.PolicyDigest || contract.ProbeImageID() != config.AttemptImageID ||
		qualificationErr != nil || restored.Digest() != config.EngineQualification.Digest() ||
		restored.ContractDigest() != contract.Digest() || restored.ProbeImageID() != contract.ProbeImageID() ||
		restored.SandboxPolicyDigest() != contract.SandboxPolicyDigest() {
		return nil, fmt.Errorf("%w: Engine qualification does not bind the exact capability contract, Attempt image, and Sandbox policy", ErrInvalidConfig)
	}
	if config.CooperativeWait == 0 || config.CooperativeWait > 2*time.Second {
		config.CooperativeWait = 2 * time.Second
	}
	if config.DeathWait == 0 || config.DeathWait > 5*time.Second {
		config.DeathWait = 5 * time.Second
	}
	if config.AttemptTimeout == 0 || config.AttemptTimeout > attemptTimeout {
		config.AttemptTimeout = attemptTimeout
	}
	if config.ColimaVersion == nil {
		config.ColimaVersion = installedColimaVersion
	}
	operationLedger, _ := config.Runner.(*OperationLedger)
	config.Runner = RunnerForOperationPhase(config.Runner, OperationPhaseAttempt)
	if err := os.MkdirAll(config.RuntimeRoot, 0o700); err != nil {
		return nil, fmt.Errorf("create runtime root: %w", err)
	}
	if err := os.Chmod(config.RuntimeRoot, 0o700); err != nil {
		return nil, fmt.Errorf("secure runtime root: %w", err)
	}
	if err := os.MkdirAll(config.ArtifactRoot, 0o700); err != nil {
		return nil, fmt.Errorf("create artifact root: %w", err)
	}
	if err := os.Chmod(config.ArtifactRoot, 0o700); err != nil {
		return nil, fmt.Errorf("secure artifact root: %w", err)
	}
	recoveryScopeDigest := sha256.Sum256([]byte(config.RuntimeRoot))
	return &Supervisor{
		config: config, recoveryScope: "sha256:" + hex.EncodeToString(recoveryScopeDigest[:]),
		operationLedger: operationLedger,
		byHandle:        map[string]*attemptRecord{}, byIdentity: map[string]*attemptRecord{}, byLaunch: map[string]*attemptRecord{},
		loadProjection: loadTaskProjection,
	}, nil
}

func (supervisor *Supervisor) Verify(ctx context.Context) (ProviderIdentity, error) {
	ctx = WithOperationPhase(ctx, OperationPhaseRestart)
	identity, err := supervisor.qualifyExecution(ctx)
	if err != nil {
		return ProviderIdentity{}, err
	}
	// ProviderIdentity is retained as a package compatibility view only. It no
	// longer grants execution authority; EngineQualification does.
	return ProviderIdentity{
		DockerServerVersion: identity.EngineVersion(), DockerContext: identity.ContextName(),
	}, nil
}

func (supervisor *Supervisor) identityCommand(ctx context.Context, args []string) (string, error) {
	result, err := supervisor.config.Runner.Run(ctx, Command{Args: args})
	if err != nil {
		return "", err
	}
	if result.ExitCode != 0 {
		return "", fmt.Errorf("docker exit %d: %s", result.ExitCode, boundedDiagnostic(result.Stderr))
	}
	return strings.TrimSpace(string(result.Stdout)), nil
}

func installedColimaVersion(ctx context.Context) (string, error) {
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

func (supervisor *Supervisor) Start(ctx context.Context, invocation execution.Invocation, sink execution.RuntimeSink) execution.StartOutcome {
	launch := invocation.LaunchToken()
	metadata, err := supervisor.validateInvocation(invocation, sink)
	if err != nil {
		return noChild(launch, err.Error())
	}
	supervisor.mu.Lock()
	_, duplicate := supervisor.byLaunch[launch.Value]
	supervisor.mu.Unlock()
	if duplicate {
		return noChild(launch, "launch token is already known")
	}
	directive, err := supervisor.consumeAcceptanceDirective(invocation, metadata)
	if err != nil {
		return noChild(launch, err.Error())
	}
	if _, err := supervisor.qualifyExecution(ctx); err != nil {
		return noChild(launch, err.Error())
	}
	projection, err := supervisor.loadProjection(supervisor.config.CredentialSource, supervisor.config.TrustAnchorSource)
	if err != nil {
		return noChild(launch, err.Error())
	}
	defer projection.clear()
	record, err := supervisor.newRecord(invocation, sink, metadata, directive)
	if err != nil {
		return noChild(launch, err.Error())
	}
	if err := supervisor.setup(ctx, record, invocation, metadata, projection); err != nil {
		return supervisor.finishFailedSetup(record, err)
	}

	supervisor.mu.Lock()
	supervisor.byHandle[record.handle.Value] = record
	supervisor.byIdentity[record.identity.Value] = record
	supervisor.byLaunch[record.launch.Value] = record
	supervisor.mu.Unlock()

	go supervisor.wait(record)
	go supervisor.expire(record)
	prompt := invocation.Stdin()
	written, writeErr := record.process.Write(prompt)
	if writeErr != nil || written != len(prompt) {
		record.completeAcceptanceInjection(acceptanceInjectionFailed)
		diagnostic := "write exact prompt failed"
		if writeErr != nil {
			diagnostic = "write prompt: " + writeErr.Error()
		}
		stop, stopErr := supervisor.Stop(context.Background(), record.handle, execution.StopIntent{Kind: execution.StopForCancel, Reason: "prompt write failure"})
		if stopErr != nil {
			diagnostic += "; forced stop failed: " + stopErr.Error()
		} else if stop.Kind != execution.StopConfirmed {
			diagnostic += "; forced stop unproven: " + stop.Diagnostic
		}
		return execution.StartOutcome{Kind: execution.StartReconciliationRequired, Handle: record.handle, Identity: record.identity, LaunchToken: launch, Diagnostic: diagnostic}
	}
	outcome := execution.StartOutcome{Kind: execution.Started, Handle: record.handle, Identity: record.identity, LaunchToken: launch}
	if directive.Action == acceptanceauthority.ActionForceManagedNonzero {
		go supervisor.forceManagedNonzero(record)
	}
	return outcome
}

func (supervisor *Supervisor) finishFailedSetup(record *attemptRecord, setupErr error) execution.StartOutcome {
	if record.process == nil {
		cleanupErr := supervisor.cleanup(context.Background(), record)
		if !record.startReturned && cleanupErr == nil {
			return noChild(record.launch, setupErr.Error())
		}
		supervisor.registerRecord(record)
		diagnostics := []string{setupErr.Error()}
		if record.startReturned {
			diagnostics = append(diagnostics, "Docker CLI non-execution is unproven because Start returned no Process")
		}
		if cleanupErr != nil {
			diagnostics = append(diagnostics, "cleanup unproven: "+cleanupErr.Error())
		}
		return execution.StartOutcome{
			Kind: execution.StartReconciliationRequired, Handle: record.handle, Identity: record.identity, LaunchToken: record.launch,
			Diagnostic: strings.Join(diagnostics, "; "),
		}
	}
	record.mu.Lock()
	record.stopRequested = true
	record.mu.Unlock()
	record.closeStdin()
	killErr := record.process.Kill()
	reaped := make(chan struct{})
	go func() {
		exitCode, _ := record.process.Wait()
		record.mu.Lock()
		record.exitCode = exitCode
		record.exited = true
		close(record.done)
		record.mu.Unlock()
		close(reaped)
	}()
	timer := time.NewTimer(supervisor.config.DeathWait)
	defer timer.Stop()
	reapProven := false
	select {
	case <-reaped:
		reapProven = true
	case <-timer.C:
	}
	var cleanupErr error
	if reapProven {
		// This is the final cleanup and exact absence proof. It must run only
		// after the Docker CLI has been waited/reaped, because a dying CLI may
		// still create or attach an owned resource before it exits.
		cleanupErr = supervisor.cleanup(context.Background(), record)
		if cleanupErr == nil {
			return noChild(record.launch, setupErr.Error())
		}
	}
	supervisor.registerRecord(record)
	diagnostics := []string{setupErr.Error()}
	if cleanupErr != nil {
		diagnostics = append(diagnostics, "cleanup unproven: "+cleanupErr.Error())
	}
	if !reapProven {
		diagnostics = append(diagnostics, "Docker CLI reap unproven; final cleanup deferred to owned reconciliation")
	}
	if killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
		diagnostics = append(diagnostics, "kill Docker CLI: "+killErr.Error())
	}
	return execution.StartOutcome{
		Kind: execution.StartReconciliationRequired, Handle: record.handle, Identity: record.identity, LaunchToken: record.launch,
		Diagnostic: strings.Join(diagnostics, "; "),
	}
}

func (supervisor *Supervisor) registerRecord(record *attemptRecord) {
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	supervisor.byHandle[record.handle.Value] = record
	supervisor.byIdentity[record.identity.Value] = record
	supervisor.byLaunch[record.launch.Value] = record
}

func (supervisor *Supervisor) qualifyExecution(ctx context.Context) (EngineIdentity, error) {
	qualification := supervisor.config.EngineQualification
	contract := supervisor.config.CapabilityContract
	if len(qualification.canonical) == 0 || len(contract.canonical) == 0 {
		return EngineIdentity{}, ErrNotQualified
	}
	restored, err := RestoreEngineQualification(qualification.Record())
	restoredContract, contractErr := RestoreCapabilityProbeContract(contract.Record())
	if err != nil || contractErr != nil || restored.Digest() != qualification.Digest() ||
		restoredContract.Digest() != contract.Digest() || restoredContract.SandboxPolicyDigest() != supervisor.config.PolicyDigest ||
		restoredContract.ProbeImageID() != supervisor.config.AttemptImageID {
		return EngineIdentity{}, fmt.Errorf("%w: qualification or exact capability contract drift", ErrNotQualified)
	}
	observed, err := observeEngine(ctx, supervisor.config.Runner)
	if err != nil {
		return EngineIdentity{}, fmt.Errorf("%w: re-observe Engine: %v", ErrNotQualified, err)
	}
	if !restored.ValidFor(observed, restoredContract) {
		return EngineIdentity{}, fmt.Errorf("%w: Engine identity or exact capability contract drift", ErrNotQualified)
	}
	for _, image := range []struct {
		role string
		id   string
	}{
		{role: "Attempt", id: supervisor.config.AttemptImageID},
		{role: "Boundary", id: supervisor.config.BoundaryImageID},
	} {
		actual, inspectErr := runnerText(ctx, supervisor.config.Runner, []string{"image", "inspect", "--format", "{{.Id}}", image.id})
		if inspectErr != nil {
			return EngineIdentity{}, fmt.Errorf("%w: observe exact %s image: %v", ErrNotQualified, image.role, inspectErr)
		}
		if actual != image.id {
			return EngineIdentity{}, fmt.Errorf("%w: %s image identity drift: got %q", ErrNotQualified, image.role, actual)
		}
	}
	supervisor.mu.Lock()
	supervisor.engineIdentity = observed
	supervisor.mu.Unlock()
	return observed, nil
}

type invocationMetadata struct {
	runID, attemptID, taskID, snapshotID string
	environment                          map[string]string
	prompt                               string
}

func (supervisor *Supervisor) validateInvocation(invocation execution.Invocation, sink execution.RuntimeSink) (invocationMetadata, error) {
	if invocation.AdapterID() != allowedAdapter || invocation.Executable() != allowedExecutable || !slices.Equal(invocation.Arguments(), frozenArguments) {
		return invocationMetadata{}, errors.New("invocation identity or arguments are not frozen Pi RPC")
	}
	if sink == nil || !invocation.LaunchToken().Valid() || sink.Binding() != invocation.LaunchToken() {
		return invocationMetadata{}, errors.New("runtime sink binding does not match launch token")
	}
	root := filepath.Clean(invocation.WorkingRoot())
	if !filepath.IsAbs(root) || root == string(filepath.Separator) {
		return invocationMetadata{}, errors.New("source repository root is invalid")
	}
	environment := invocation.Environment()
	allowed := []string{
		EnvRunID, EnvAttemptID, EnvContextDigest, EnvContractDigest, EnvPolicyDigest,
		EnvExecutionProfile, EnvRuntimeSource, EnvRuntimeSourceID, EnvRuntimeVersion,
		EnvExecutionProvider, EnvCapabilityPolicy, EnvCapabilityBundleID, EnvCapabilityBundleSHA, EnvTrustDisclosure,
		EnvAttemptImageID, EnvBoundaryImageID, EnvCommandTimeout, EnvAttemptTimeout,
		EnvPersistedLogLimit, EnvArtifactLimit, EnvResultPath, EnvPatchPath,
		EnvChecksPath, EnvRuntimeIdentityPath,
	}
	if len(environment) != len(allowed) {
		return invocationMetadata{}, errors.New("invocation environment is not exact allowlist")
	}
	for _, key := range allowed {
		if key != EnvTrustDisclosure && strings.TrimSpace(environment[key]) == "" {
			return invocationMetadata{}, fmt.Errorf("invocation environment missing %s", key)
		}
	}
	profile := domain.AgentExecutionProfile(environment[EnvExecutionProfile])
	var wantCapabilityPolicy string
	switch profile {
	case domain.AgentExecutionProfileMinimal:
		wantCapabilityPolicy = domain.MinimalCapabilityPolicy
	case domain.AgentExecutionProfileStandard:
		wantCapabilityPolicy = domain.StandardCapabilityPolicy
	default:
		return invocationMetadata{}, errors.New("invocation execution profile is not managed Docker")
	}
	bundle, err := agentpi.ManagedCapabilityBundleForProfile(profile)
	if err != nil {
		return invocationMetadata{}, errors.New("invocation managed capability bundle is unavailable")
	}
	if environment[EnvRuntimeSource] != domain.ManagedPiRuntimeSource ||
		environment[EnvRuntimeSourceID] != supervisor.config.RuntimeSourceIdentity || environment[EnvRuntimeVersion] != managedRuntimeVersion ||
		environment[EnvExecutionProvider] != domain.DockerExecutionProvider || environment[EnvCapabilityPolicy] != wantCapabilityPolicy ||
		environment[EnvCapabilityBundleID] != bundle.ID() || environment[EnvCapabilityBundleSHA] != bundle.SHA256() ||
		environment[EnvTrustDisclosure] != "" {
		return invocationMetadata{}, errors.New("invocation execution profile or Runtime source identity mismatch")
	}
	if environment[EnvPolicyDigest] != supervisor.config.PolicyDigest || environment[EnvAttemptImageID] != supervisor.config.AttemptImageID || environment[EnvBoundaryImageID] != supervisor.config.BoundaryImageID || environment[EnvCommandTimeout] != "600" || environment[EnvAttemptTimeout] != "1200" || environment[EnvPersistedLogLimit] != strconv.Itoa(persistedLogBytes) || environment[EnvArtifactLimit] != strconv.Itoa(artifactBytes) {
		return invocationMetadata{}, errors.New("invocation policy, image, or limit identity mismatch")
	}
	wantPaths := map[string]string{EnvResultPath: "/output/result.json", EnvPatchPath: "/output/patch.diff", EnvChecksPath: "/output/checks.json", EnvRuntimeIdentityPath: "/output/runtime-identity.json"}
	for key, value := range wantPaths {
		if environment[key] != value {
			return invocationMetadata{}, fmt.Errorf("invalid output path %s", key)
		}
	}
	prompt, taskID, err := validatePrompt(invocation.Stdin(), environment[EnvContextDigest], environment[EnvContractDigest])
	if err != nil {
		return invocationMetadata{}, err
	}
	snapshotID := ""
	if supervisor.config.AcceptanceAuthority != nil {
		snapshotID, err = snapshotIDFromPrompt(invocation.Stdin(), environment[EnvContextDigest])
		if err != nil {
			return invocationMetadata{}, err
		}
	}
	return invocationMetadata{runID: environment[EnvRunID], attemptID: environment[EnvAttemptID], taskID: taskID, snapshotID: snapshotID, environment: environment, prompt: prompt}, nil
}

func snapshotIDFromPrompt(stdin []byte, contextDigest string) (string, error) {
	var command struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(stdin, &command) != nil {
		return "", errors.New("stdin prompt command is invalid")
	}
	_, contractBytes, ok := execution.UnwrapFrozenExecutionPrompt(command.Message)
	if !ok {
		return "", errors.New("stdin prompt is missing the frozen execution policy")
	}
	var contract struct {
		Execution struct {
			Input struct {
				ContextSnapshotID     string `json:"context_snapshot_id"`
				ContextSnapshotDigest string `json:"context_snapshot_digest"`
			} `json:"input"`
		} `json:"execution"`
	}
	if json.Unmarshal(contractBytes, &contract) != nil || strings.TrimSpace(contract.Execution.Input.ContextSnapshotID) == "" ||
		contract.Execution.Input.ContextSnapshotDigest != contextDigest {
		return "", errors.New("stdin prompt Snapshot identity is invalid")
	}
	return contract.Execution.Input.ContextSnapshotID, nil
}

func (supervisor *Supervisor) consumeAcceptanceDirective(invocation execution.Invocation, metadata invocationMetadata) (acceptanceauthority.Directive, error) {
	if supervisor.config.AcceptanceAuthority == nil {
		return acceptanceauthority.Directive{}, nil
	}
	return supervisor.config.AcceptanceAuthority.Consume(acceptanceauthority.InvocationFact{
		RunID: metadata.runID, AttemptID: metadata.attemptID, TaskID: metadata.taskID,
		SnapshotID: metadata.snapshotID, SnapshotDigest: metadata.environment[EnvContextDigest],
		AgentExecutionProfile: metadata.environment[EnvExecutionProfile], InvocationDigest: digestInvocation(invocation),
	})
}

func digestInvocation(invocation execution.Invocation) string {
	record := struct {
		AdapterID         string `json:"adapterId"`
		ExecutableSHA256  string `json:"executableSha256"`
		ArgumentsSHA256   string `json:"argumentsSha256"`
		EnvironmentSHA256 string `json:"environmentSha256"`
		WorkingRootSHA256 string `json:"workingRootSha256"`
		StdinSHA256       string `json:"stdinSha256"`
		LaunchTokenSHA256 string `json:"launchTokenSha256"`
		ProviderID        string `json:"providerId"`
	}{
		AdapterID: invocation.AdapterID(), ExecutableSHA256: digestText([]byte(invocation.Executable())),
		ArgumentsSHA256: digestCanonical(invocation.Arguments()), EnvironmentSHA256: digestCanonical(invocation.Environment()),
		WorkingRootSHA256: digestText([]byte(invocation.WorkingRoot())), StdinSHA256: digestText(invocation.Stdin()),
		LaunchTokenSHA256: digestText([]byte(invocation.LaunchToken().Value)), ProviderID: invocation.Target().ProviderID(),
	}
	return digestCanonical(record)
}

func digestCanonical(value any) string { data, _ := json.Marshal(value); return digestText(data) }
func digestText(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func validatePrompt(stdin []byte, contextDigest, contractDigest string) (string, string, error) {
	if len(stdin) == 0 || stdin[len(stdin)-1] != '\n' || bytes.Count(stdin, []byte{'\n'}) != 1 {
		return "", "", errors.New("stdin must be exactly one JSONL command")
	}
	var command struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	decoder := json.NewDecoder(bytes.NewReader(stdin))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&command); err != nil || command.ID == "" || command.Type != "prompt" || command.Message == "" {
		return "", "", errors.New("stdin prompt command is invalid")
	}
	snapshotBytes, contractBytes, ok := execution.UnwrapFrozenExecutionPrompt(command.Message)
	if !ok {
		return "", "", errors.New("stdin prompt is missing the frozen execution policy")
	}
	if privateHomePathPattern.Match(snapshotBytes) || privateHomePathPattern.Match(contractBytes) {
		return "", "", errors.New("stdin prompt contains a private home path")
	}
	snapshotHash := sha256.Sum256(snapshotBytes)
	if hex.EncodeToString(snapshotHash[:]) != contextDigest {
		return "", "", errors.New("stdin prompt does not match context digest")
	}
	contractHash := sha256.Sum256(contractBytes)
	if hex.EncodeToString(contractHash[:]) != contractDigest {
		return "", "", errors.New("stdin prompt does not match active Contract digest")
	}
	_, taskID, valid := decodeTerminalPlan(snapshotBytes, contractBytes)
	if !valid {
		return "", "", errors.New("stdin prompt does not contain matching frozen Snapshot and Active Contract authority")
	}
	return string(snapshotBytes), taskID, nil
}

func (supervisor *Supervisor) newRecord(invocation execution.Invocation, sink execution.RuntimeSink, metadata invocationMetadata, directive acceptanceauthority.Directive) (*attemptRecord, error) {
	suffix, err := randomSuffix()
	if err != nil {
		return nil, err
	}
	prefix := "chora-" + suffix
	root := filepath.Join(supervisor.config.RuntimeRoot, prefix)
	baseline := filepath.Join(root, "baseline", "repository")
	repository := filepath.Join(root, "workspace", "repository")
	contextDir := filepath.Join(root, "context")
	if err := os.Mkdir(root, 0o700); err != nil {
		return nil, err
	}
	if err := supervisor.writeAttemptRootMarker(root, metadata.attemptID); err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	if err := os.MkdirAll(contextDir, 0o700); err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	if err := copyTree(invocation.WorkingRoot(), baseline); err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	if err := copyTree(invocation.WorkingRoot(), repository); err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	if err := makeContainerWorkspaceWritable(filepath.Join(root, "workspace")); err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	for _, relative := range []string{".chora-cache/go-build", ".chora-tmp"} {
		toolingPath := filepath.Join(root, "workspace", filepath.FromSlash(relative))
		if err := os.MkdirAll(toolingPath, 0o777); err != nil {
			_ = os.RemoveAll(root)
			return nil, err
		}
		if err := os.Chmod(toolingPath, 0o777); err != nil {
			_ = os.RemoveAll(root)
			return nil, err
		}
	}
	if err := os.WriteFile(filepath.Join(contextDir, "snapshot.jsonl"), invocation.Stdin(), 0o444); err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	if err := os.Chmod(contextDir, 0o755); err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	terminalRoot := filepath.Join(root, "output")
	if err := os.MkdirAll(terminalRoot, 0o700); err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	attemptDigest := sha256.Sum256([]byte(metadata.attemptID))
	artifactLocatorPrefix := filepath.ToSlash(filepath.Join("attempts", hex.EncodeToString(attemptDigest[:])))
	recordTimeout := supervisor.config.AttemptTimeout
	if directive.Action == acceptanceauthority.ActionAcceleratedManagedDeadline {
		recordTimeout = acceptanceauthority.AcceleratedDeadlineSeconds * time.Second
	}
	record := &attemptRecord{
		handle: execution.RuntimeHandle{Value: "docker:" + suffix}, identity: execution.ProcessIdentity{Value: prefix}, launch: invocation.LaunchToken(), sink: sink,
		root: root, baseline: baseline, repository: repository, contextDir: contextDir,
		artifactDir: filepath.Join(supervisor.config.ArtifactRoot, filepath.FromSlash(artifactLocatorPrefix)), artifactLocatorPrefix: artifactLocatorPrefix,
		runID: metadata.runID, attemptID: metadata.attemptID, taskID: metadata.taskID,
		executionProfile: metadata.environment[EnvExecutionProfile], capabilityPolicy: metadata.environment[EnvCapabilityPolicy],
		capabilityBundleID: metadata.environment[EnvCapabilityBundleID], capabilityBundleSHA256: metadata.environment[EnvCapabilityBundleSHA],
		container: prefix + "-attempt", boundary: prefix + "-codex-boundary", internalNetwork: prefix + "-internal", upstreamNetwork: prefix + "-upstream",
		done: make(chan struct{}), exitCode: -1, terminationCause: execution.TerminationNone, attemptTimeout: recordTimeout, drained: map[execution.StreamKind]bool{},
		acceptanceDirective: directive,
		terminal: execution.TerminalFiles{Paths: map[string]string{
			"result": filepath.Join(terminalRoot, "result.json"), "patch": filepath.Join(terminalRoot, "patch.diff"),
			"checks": filepath.Join(terminalRoot, "checks.json"), "runtime_identity": filepath.Join(terminalRoot, "runtime-identity.json"),
		}},
	}
	if directive.Action == acceptanceauthority.ActionForceManagedNonzero {
		record.acceptanceInjectionState = acceptanceInjectionPending
		record.acceptanceInjectionDone = make(chan struct{})
		record.acceptanceExitObserved = make(chan struct{})
	}
	return record, nil
}

type attemptRootMarker struct {
	SchemaVersion string `json:"schema_version"`
	Owner         string `json:"owner"`
	RuntimeScope  string `json:"runtime_scope"`
	RootName      string `json:"root_name"`
	AttemptID     string `json:"attempt_id"`
	PolicyDigest  string `json:"policy_digest"`
}

func (supervisor *Supervisor) writeAttemptRootMarker(root, attemptID string) error {
	marker := attemptRootMarker{
		SchemaVersion: attemptRootMarkerSchema, Owner: "dockersupervisor", RuntimeScope: supervisor.recoveryScope,
		RootName: filepath.Base(root), AttemptID: attemptID, PolicyDigest: supervisor.config.PolicyDigest,
	}
	data, err := json.Marshal(marker)
	if err != nil {
		return fmt.Errorf("encode Attempt root marker: %w", err)
	}
	data = append(data, '\n')
	path := filepath.Join(root, attemptRootMarkerName)
	if err := writeExclusive(path, data); err != nil {
		return fmt.Errorf("write Attempt root marker: %w", err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		return fmt.Errorf("seal Attempt root marker: %w", err)
	}
	return nil
}

func (supervisor *Supervisor) setup(ctx context.Context, record *attemptRecord, invocation execution.Invocation, metadata invocationMetadata, projection taskProjection) error {
	profile := domain.AgentExecutionProfile(metadata.environment[EnvExecutionProfile])
	bundle, err := agentpi.ManagedCapabilityBundleForProfile(profile)
	if err != nil || bundle.ID() != metadata.environment[EnvCapabilityBundleID] || bundle.SHA256() != metadata.environment[EnvCapabilityBundleSHA] {
		return errors.New("managed Pi capability bundle drifted before setup")
	}
	labelsFor := func(imageID string) []string {
		return []string{"--label", "chora.owner=dockersupervisor", "--label", "chora.runtime_scope=" + supervisor.recoveryScope, "--label", "chora.run_id=" + metadata.runID, "--label", "chora.attempt_id=" + metadata.attemptID, "--label", "chora.task_id=" + metadata.taskID, "--label", "chora.image_digest=" + imageID, "--label", "chora.policy_digest=" + supervisor.config.PolicyDigest}
	}
	labels := labelsFor(supervisor.config.AttemptImageID)
	if err := supervisor.runOK(ctx, append([]string{"network", "create", "--internal"}, append(labels, record.internalNetwork)...)); err != nil {
		return fmt.Errorf("create internal network: %w", err)
	}
	if err := supervisor.runOK(ctx, append([]string{"network", "create"}, append(labels, record.upstreamNetwork)...)); err != nil {
		return fmt.Errorf("create upstream network: %w", err)
	}
	boundary := []string{"run", "--pull=never", "-d", "--name", record.boundary}
	boundary = append(boundary, labelsFor(supervisor.config.BoundaryImageID)...)
	boundary = append(boundary, "--network", record.upstreamNetwork, "--add-host", "host.docker.internal:host-gateway", "--read-only", "--log-driver", "none", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--pids-limit", "32", "--cpus", "0.25", "--memory", "64m", "--memory-swap", "64m", "--ulimit", "nofile=1024:1024", supervisor.config.BoundaryImageID)
	if err := supervisor.runOK(ctx, boundary); err != nil {
		return fmt.Errorf("start boundary: %w", err)
	}
	if err := supervisor.runOK(ctx, []string{"network", "connect", "--alias", "codex-boundary", record.internalNetwork, record.boundary}); err != nil {
		return fmt.Errorf("connect boundary: %w", err)
	}

	stdout := &streamWriter{record: record, kind: execution.StreamStdout}
	stderr := &streamWriter{record: record, kind: execution.StreamStderr}
	record.stdout.setSecrets(projection.secrets)
	record.stderr.setSecrets(projection.secrets)
	attempt := []string{"create", "--pull=never", "-i", "--name", record.container}
	attempt = append(attempt, labels...)
	attempt = append(attempt,
		"--network", record.internalNetwork, "--user", "1000:1000", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true",
		"--pids-limit", "256", "--cpus", "2", "--memory", "4096m", "--memory-swap", "4096m", "--ulimit", "nofile=1024:1024",
		"--log-driver", "none",
		"--tmpfs", "/tmp:rw,nosuid,nodev,noexec,uid=1000,gid=1000,size=64m", "--tmpfs", "/run/chora/pi:rw,nosuid,nodev,noexec,uid=1000,gid=1000,size=16m",
		"--tmpfs", "/run/chora/trust:rw,nosuid,nodev,noexec,uid=0,gid=0,mode=0755,size=1m",
		"--env", "HOME=/run/chora/pi", "--env", "GOCACHE="+toolCachePath, "--env", "GOTMPDIR="+toolTempPath,
		"--env", "PI_CODING_AGENT_DIR=/run/chora/pi", "--env", "PI_OFFLINE=1", "--env", "PI_SKIP_VERSION_CHECK=1", "--env", "PI_TELEMETRY=0",
		"--env", "HTTP_PROXY=http://codex-boundary:8080", "--env", "HTTPS_PROXY=http://codex-boundary:8080", "--env", "NO_PROXY=localhost,127.0.0.1",
		"--env", "NODE_EXTRA_CA_CERTS=/run/chora/trust/starpoint-root-ca-2048-g2.pem")
	keys := make([]string, 0, len(metadata.environment))
	for key := range metadata.environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		attempt = append(attempt, "--env", key+"="+metadata.environment[key])
	}
	attempt = append(attempt,
		"--mount", "type=bind,src="+filepath.Join(record.root, "workspace")+",dst=/workspace",
		"--mount", "type=bind,src="+record.contextDir+",dst=/input/context,readonly",
		"--workdir", "/workspace/repository", "--entrypoint", "/bin/sh", supervisor.config.AttemptImageID,
		"-c", `set -eu; i=0; while [ ! -s /run/chora/pi/auth.json ] || [ ! -r /run/chora/trust/starpoint-root-ca-2048-g2.pem ] || [ "$(stat -c %a /run/chora/trust)" != 555 ]; do i=$((i+1)); [ "$i" -lt 200 ] || exit 78; sleep 0.05; done; exec pi "$@"`, "chora-pi")
	attempt = append(attempt, frozenArguments...)
	attempt = append(attempt, "--tools", strings.Join(bundle.Tools(), ","))
	if err := supervisor.runOK(ctx, attempt); err != nil {
		return fmt.Errorf("create attempt container: %w", err)
	}
	process, err := supervisor.config.Runner.Start(context.Background(), Command{Args: []string{"start", "-ai", record.container}, Stdout: stdout, Stderr: stderr})
	if process != nil {
		record.process = process
	}
	if err != nil {
		return fmt.Errorf("start attempt container: %w", err)
	}
	record.startReturned = true
	if process == nil {
		return errors.New("start attempt container: Docker Runner returned no process")
	}
	if err := supervisor.waitForContainer(ctx, record.container); err != nil {
		return err
	}
	if err := supervisor.verifyEffectiveContainers(ctx, record, metadata); err != nil {
		return err
	}
	if err := supervisor.injectTaskProjection(ctx, record.container, projection); err != nil {
		return err
	}
	if record.acceptanceDirective.Action == acceptanceauthority.ActionForceManagedNonzero {
		var err error
		record.containerDockerID, err = supervisor.dockerContainerID(ctx, record.container)
		if err != nil {
			return fmt.Errorf("capture exact acceptance Attempt identity: %w", err)
		}
	}
	return nil
}

func (supervisor *Supervisor) dockerContainerID(ctx context.Context, name string) (string, error) {
	id, err := supervisor.identityCommand(ctx, []string{"inspect", "--type", "container", "--format", "{{.Id}}", name})
	if err != nil {
		return "", err
	}
	if !validDigest(id) {
		return "", errors.New("Docker container identity is malformed")
	}
	return id, nil
}

func (supervisor *Supervisor) runOK(ctx context.Context, args []string) error {
	result, err := supervisor.config.Runner.Run(ctx, Command{Args: args})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("docker exit %d: %s", result.ExitCode, boundedDiagnostic(result.Stderr))
	}
	return nil
}

func (supervisor *Supervisor) wait(record *attemptRecord) {
	exitCode, _ := record.process.Wait()
	record.mu.Lock()
	record.exitCode = exitCode
	record.exitObserved = true
	if record.acceptanceExitObserved != nil {
		close(record.acceptanceExitObserved)
		record.acceptanceExitObserved = nil
	}
	injectionDone := record.acceptanceInjectionDone
	record.mu.Unlock()
	if injectionDone != nil {
		<-injectionDone
	}
	for _, kind := range []execution.StreamKind{execution.StreamStdout, execution.StreamStderr} {
		stream, _ := record.stream(kind)
		if accepted, offset := stream.flush(); accepted > 0 {
			safeNotify(record.sink, kind, offset)
		}
	}
	record.mu.Lock()
	record.classifyObservedExitLocked(exitCode)
	stopRequested, timedOut, unproven := record.stopRequested, record.timedOut, record.acceptanceTerminalUnproven
	injectedFailure := record.acceptanceDirective.Action == acceptanceauthority.ActionForceManagedNonzero && record.acceptanceInjectionState == acceptanceInjectionConfirmed && exitCode == 137
	record.mu.Unlock()
	if unproven {
		record.mu.Lock()
		record.terminal = execution.TerminalFiles{Paths: map[string]string{}}
		record.mu.Unlock()
	} else if injectedFailure {
		supervisor.deriveInjectedFailureTerminal(record)
	} else if !stopRequested || timedOut {
		supervisor.deriveTerminal(record)
	}
	record.mu.Lock()
	record.exited = true
	close(record.done)
	record.mu.Unlock()
	safeExited(record.sink)
}

func (record *attemptRecord) classifyObservedExitLocked(exitCode int) {
	if record.acceptanceDirective.Action == acceptanceauthority.ActionForceManagedNonzero {
		if record.acceptanceInjectionState == acceptanceInjectionConfirmed && !record.timedOut && exitCode == 137 {
			record.terminationCause = execution.TerminationExitNonzero
			return
		}
		record.acceptanceInjectionState = acceptanceInjectionFailed
		record.terminationCause = execution.TerminationNone
		record.acceptanceTerminalUnproven = true
		return
	}
	if record.terminationCause == execution.TerminationNone && exitCode != 0 {
		record.terminationCause = execution.TerminationExitNonzero
	}
}

func (supervisor *Supervisor) forceManagedNonzero(record *attemptRecord) {
	record.mu.Lock()
	if record.exitObserved || record.exited || record.stopRequested || record.acceptanceDirective.Action != acceptanceauthority.ActionForceManagedNonzero || record.acceptanceInjectionState != acceptanceInjectionPending {
		record.mu.Unlock()
		record.completeAcceptanceInjection(acceptanceInjectionFailed)
		return
	}
	containerID := record.containerDockerID
	record.mu.Unlock()
	if !validDigest(containerID) {
		record.completeAcceptanceInjection(acceptanceInjectionFailed)
		return
	}
	killContext, cancel := context.WithTimeout(context.Background(), supervisor.config.DeathWait)
	defer cancel()
	result, err := supervisor.config.Runner.Run(killContext, Command{Args: []string{"kill", "--signal", "KILL", containerID}})
	if err != nil || result.ExitCode != 0 {
		record.completeAcceptanceInjection(acceptanceInjectionFailed)
		return
	}
	record.mu.Lock()
	exitObserved := record.acceptanceExitObserved
	alreadyObserved := record.exitObserved
	record.mu.Unlock()
	if !alreadyObserved {
		select {
		case <-exitObserved:
		case <-killContext.Done():
			record.completeAcceptanceInjection(acceptanceInjectionFailed)
			return
		}
	}
	record.completeAcceptanceInjection(acceptanceInjectionConfirmed)
}

func (record *attemptRecord) completeAcceptanceInjection(state acceptanceInjectionState) {
	record.mu.Lock()
	defer record.mu.Unlock()
	if record.acceptanceInjectionState != acceptanceInjectionPending || record.acceptanceInjectionDone == nil {
		return
	}
	record.acceptanceInjectionState = state
	close(record.acceptanceInjectionDone)
}

func (supervisor *Supervisor) expire(record *attemptRecord) {
	timer := time.NewTimer(record.attemptTimeout)
	defer timer.Stop()
	select {
	case <-record.done:
	case <-timer.C:
		if !record.markDeadlineIfLive() {
			return
		}
		_, _ = supervisor.Stop(context.Background(), record.handle, execution.StopIntent{Kind: execution.StopForCancel, Reason: "attempt timeout"})
	}
}

func (record *attemptRecord) markDeadlineIfLive() bool {
	record.mu.Lock()
	defer record.mu.Unlock()
	if record.exitObserved || record.exited {
		return false
	}
	record.timedOut = true
	record.terminationCause = execution.TerminationDeadlineExceeded
	return true
}

func (supervisor *Supervisor) Stop(ctx context.Context, handle execution.RuntimeHandle, _ execution.StopIntent) (execution.StopOutcome, error) {
	record, err := supervisor.recordForHandle(handle)
	if err != nil {
		return execution.StopOutcome{}, err
	}
	if record.process == nil && !record.isExited() {
		return execution.StopOutcome{Kind: execution.StopUncertain, Diagnostic: "Docker CLI process ownership is unavailable; reconciliation remains required"}, nil
	}
	if record.isExited() {
		return supervisor.confirmStopped(record), nil
	}
	deathDeadline := time.Now().Add(supervisor.config.DeathWait)
	record.mu.Lock()
	sendAbort := !record.abortSent
	record.stopRequested = true
	if !record.abortSent {
		record.abortSent = true
	}
	record.mu.Unlock()
	abortDiagnostic := ""
	var abortResult <-chan error
	if sendAbort {
		result := make(chan error, 1)
		abortResult = result
		go func() {
			_, writeErr := record.process.Write([]byte("{\"id\":\"chora-abort\",\"type\":\"abort\"}\n"))
			result <- writeErr
		}()
	}
	cooperativeWait := min(supervisor.config.CooperativeWait, time.Until(deathDeadline))
	if cooperativeWait > 0 && ctx.Err() == nil {
		timer := time.NewTimer(cooperativeWait)
	cooperative:
		for {
			select {
			case <-record.done:
				timer.Stop()
				return supervisor.confirmStopped(record), nil
			case <-ctx.Done():
				timer.Stop()
				break cooperative
			case <-timer.C:
				break cooperative
			case abortErr := <-abortResult:
				abortResult = nil
				if abortErr != nil {
					abortDiagnostic = "cooperative abort failed: " + abortErr.Error()
					timer.Stop()
					break cooperative
				}
			}
		}
	}
	record.closeStdin()
	forceContext, cancel := context.WithDeadline(context.Background(), deathDeadline)
	defer cancel()
	_ = supervisor.runAllowFailure(forceContext, []string{"network", "disconnect", "-f", record.internalNetwork, record.container})
	_ = supervisor.runAllowFailure(forceContext, []string{"rm", "-f", record.container})
	_ = record.process.Kill()
	for {
		if !time.Now().Before(deathDeadline) {
			break
		}
		result, inspectErr := supervisor.config.Runner.Run(forceContext, Command{Args: []string{"inspect", record.container}})
		if containerMissing(result, inspectErr) {
			remaining := time.Until(deathDeadline)
			if remaining > 0 {
				timer := time.NewTimer(remaining)
				select {
				case <-record.done:
					timer.Stop()
					return supervisor.confirmStopped(record), nil
				case <-timer.C:
				}
			}
			abortDiagnostic = strings.TrimSpace(strings.Join([]string{abortDiagnostic, "container removed but process-tree exit was not observed"}, "; "))
			break
		}
		if inspectErr != nil {
			abortDiagnostic = strings.TrimSpace(strings.Join([]string{abortDiagnostic, "container death probe failed: " + inspectErr.Error()}, "; "))
			break
		}
		select {
		case <-forceContext.Done():
			break
		case <-time.After(time.Millisecond):
		}
	}
	diagnostic := "container death could not be proved within total deadline of " + supervisor.config.DeathWait.String()
	if abortDiagnostic != "" {
		diagnostic += "; " + abortDiagnostic
	}
	return execution.StopOutcome{Kind: execution.StopUncertain, Diagnostic: diagnostic}, nil
}

func (supervisor *Supervisor) confirmStopped(record *attemptRecord) execution.StopOutcome {
	if record.recovered {
		return execution.StopOutcome{Kind: execution.StopConfirmed}
	}
	if err := supervisor.cleanup(context.Background(), record); err != nil {
		return execution.StopOutcome{Kind: execution.StopUncertain, Diagnostic: "process exited but Attempt cleanup is unproven: " + err.Error()}
	}
	return execution.StopOutcome{Kind: execution.StopConfirmed}
}

func containerMissing(result CommandResult, err error) bool {
	if result.ExitCode == 0 {
		return false
	}
	diagnostic := strings.ToLower(string(result.Stderr))
	return strings.Contains(diagnostic, "no such object") || strings.Contains(diagnostic, "no such container")
}

func (supervisor *Supervisor) runAllowFailure(ctx context.Context, args []string) error {
	_, err := supervisor.config.Runner.Run(ctx, Command{Args: args})
	return err
}

func (supervisor *Supervisor) Reconcile(_ context.Context, identity execution.ProcessIdentity) (execution.ReconcileOutcome, error) {
	supervisor.mu.Lock()
	record := supervisor.byIdentity[identity.Value]
	supervisor.mu.Unlock()
	return reconcile(record), nil
}

func (supervisor *Supervisor) ReconcileLaunch(_ context.Context, launch execution.LaunchToken) (execution.ReconcileOutcome, error) {
	supervisor.mu.Lock()
	record := supervisor.byLaunch[launch.Value]
	supervisor.mu.Unlock()
	return reconcile(record), nil
}

func reconcile(record *attemptRecord) execution.ReconcileOutcome {
	if record == nil {
		return execution.ReconcileOutcome{Kind: execution.ReconcileUncertain, Diagnostic: "runtime not owned by this supervisor lifecycle"}
	}
	kind := execution.ReconcileAlive
	if record.isExited() {
		kind = execution.ReconcileDead
	}
	return execution.ReconcileOutcome{Kind: kind, Handle: record.handle, LaunchToken: record.launch}
}

func (supervisor *Supervisor) Read(_ context.Context, handle execution.RuntimeHandle, kind execution.StreamKind, offset int64, limit int) (execution.StreamChunk, error) {
	record, err := supervisor.recordForHandle(handle)
	if err != nil {
		return execution.StreamChunk{}, err
	}
	stream, ok := record.stream(kind)
	if !ok || limit <= 0 {
		return execution.StreamChunk{}, ErrInvalidStreamRead
	}
	data, next, ok := stream.read(offset, limit)
	if !ok {
		return execution.StreamChunk{}, ErrInvalidStreamRead
	}
	return execution.StreamChunk{Data: data, NextOffset: next, EOF: record.isExited() && next == stream.length()}, nil
}

func (supervisor *Supervisor) Drain(_ context.Context, handle execution.RuntimeHandle, offsets execution.StreamOffsets, limit int) (execution.DrainOutcome, error) {
	record, err := supervisor.recordForHandle(handle)
	if err != nil {
		return execution.DrainOutcome{}, err
	}
	if limit <= 0 {
		return execution.DrainOutcome{}, ErrInvalidDrain
	}
	for kind := range offsets {
		if kind != execution.StreamStdout && kind != execution.StreamStderr {
			return execution.DrainOutcome{}, ErrInvalidDrain
		}
		if offsets[kind] < 0 {
			return execution.DrainOutcome{}, ErrInvalidDrain
		}
	}
	if record.recovered {
		outcome := execution.DrainOutcome{
			Chunks: map[execution.StreamKind][]byte{}, Offsets: execution.StreamOffsets{}, EOF: map[execution.StreamKind]bool{},
			TerminalFiles: cloneTerminal(record.terminal),
		}
		record.mu.Lock()
		for _, kind := range []execution.StreamKind{execution.StreamStdout, execution.StreamStderr} {
			outcome.Chunks[kind] = []byte{}
			outcome.Offsets[kind] = offsets[kind]
			outcome.EOF[kind] = true
			record.drained[kind] = true
		}
		outcome.TerminalFiles.ExitCode, outcome.TerminalFiles.TerminationCause = record.terminalExitProjectionLocked()
		record.mu.Unlock()
		return outcome, nil
	}
	outcome := execution.DrainOutcome{Chunks: map[execution.StreamKind][]byte{}, Offsets: execution.StreamOffsets{}, EOF: map[execution.StreamKind]bool{}, TerminalFiles: cloneTerminal(record.terminal)}
	remaining := limit
	for _, kind := range []execution.StreamKind{execution.StreamStdout, execution.StreamStderr} {
		stream, _ := record.stream(kind)
		data, next, ok := stream.read(offsets[kind], remaining)
		if !ok {
			return execution.DrainOutcome{}, ErrInvalidDrain
		}
		outcome.Chunks[kind], outcome.Offsets[kind] = data, next
		remaining -= len(data)
		outcome.EOF[kind] = record.isExited() && next == stream.length()
		if outcome.EOF[kind] {
			record.mu.Lock()
			record.drained[kind] = true
			record.mu.Unlock()
		}
	}
	record.mu.Lock()
	if record.exited {
		outcome.TerminalFiles.ExitCode, outcome.TerminalFiles.TerminationCause = record.terminalExitProjectionLocked()
	}
	record.mu.Unlock()
	return outcome, nil
}

func (record *attemptRecord) terminalExitProjectionLocked() (int, execution.TerminationCause) {
	if record.acceptanceTerminalUnproven {
		return 0, execution.TerminationNone
	}
	return record.exitCode, record.terminationCause
}

func (supervisor *Supervisor) Finalize(ctx context.Context, handle execution.RuntimeHandle, _ execution.RetentionPolicy) error {
	record, err := supervisor.recordForHandle(handle)
	if err != nil {
		return err
	}
	record.mu.Lock()
	ready := record.exited && record.drained[execution.StreamStdout] && record.drained[execution.StreamStderr]
	record.mu.Unlock()
	if !ready {
		return ErrFinalizeNotReady
	}
	if !record.recovered {
		if err := supervisor.cleanup(ctx, record); err != nil {
			return err
		}
	}
	supervisor.mu.Lock()
	delete(supervisor.byHandle, record.handle.Value)
	delete(supervisor.byIdentity, record.identity.Value)
	delete(supervisor.byLaunch, record.launch.Value)
	supervisor.mu.Unlock()
	return nil
}

// RegisterRecoveredDead restores the lifecycle protocol for a persisted Pi
// session after Recover has proved that its runtime resources are gone.
func (supervisor *Supervisor) RegisterRecoveredDead(identity execution.ProcessIdentity, launch execution.LaunchToken) error {
	if !identity.Valid() && !launch.Valid() {
		return errors.New("recovered runtime requires process identity or launch token")
	}
	key := identity.Value + "\x00" + launch.Value
	digest := sha256.Sum256([]byte(key))
	record := &attemptRecord{
		handle:           execution.RuntimeHandle{Value: "docker-recovered:" + hex.EncodeToString(digest[:8])},
		identity:         identity,
		launch:           launch,
		done:             make(chan struct{}),
		exited:           true,
		exitCode:         -1,
		terminationCause: execution.TerminationNone,
		drained:          map[execution.StreamKind]bool{},
		recovered:        true,
		terminal:         execution.TerminalFiles{Paths: map[string]string{}},
	}
	close(record.done)

	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	if !supervisor.recoveryComplete {
		return errors.New("recovered runtime requires completed cleanup proof")
	}
	if _, exists := supervisor.byHandle[record.handle.Value]; exists {
		return errors.New("recovered runtime handle is already known")
	}
	if identity.Valid() {
		if _, exists := supervisor.byIdentity[identity.Value]; exists {
			return errors.New("recovered process identity is already known")
		}
	}
	if launch.Valid() {
		if _, exists := supervisor.byLaunch[launch.Value]; exists {
			return errors.New("recovered launch token is already known")
		}
	}
	supervisor.byHandle[record.handle.Value] = record
	if identity.Valid() {
		supervisor.byIdentity[identity.Value] = record
	}
	if launch.Valid() {
		supervisor.byLaunch[launch.Value] = record
	}
	return nil
}

type dockerResourceInventory struct {
	containers []string
	networks   []string
}

func (supervisor *Supervisor) cleanup(_ context.Context, record *attemptRecord) error {
	record.cleanupMu.Lock()
	defer record.cleanupMu.Unlock()
	cleanupContext, cancel := context.WithTimeout(context.Background(), supervisor.config.DeathWait)
	defer cancel()
	filters := supervisor.ownedResourceFilters(record.attemptID)
	inventory, inventoryErr := supervisor.inventoryResources(cleanupContext, filters)
	exactInventory, exactInventoryErr := supervisor.exactAttemptResources(cleanupContext, record)
	verified, ownershipErr := supervisor.verifyExactResourceOwnership(cleanupContext, inventory, exactInventory, record)
	operationErr := supervisor.removeResources(cleanupContext, verified)
	proofErr := supervisor.proveAttemptResourcesAbsent(cleanupContext, filters, record)
	if dockerErr := errors.Join(inventoryErr, exactInventoryErr, ownershipErr, operationErr, proofErr); dockerErr != nil {
		return dockerErr
	}
	return removeAndProveAbsent(record.root, "runtime workspace")
}

func (supervisor *Supervisor) Recover(ctx context.Context) error {
	ctx = WithOperationPhase(ctx, OperationPhaseRecovery)
	supervisor.mu.Lock()
	supervisor.recoveryComplete = false
	supervisor.mu.Unlock()
	if _, err := supervisor.qualifyExecution(ctx); err != nil {
		return fmt.Errorf("recover: %w", err)
	}
	recoveryContext, cancel := context.WithTimeout(WithOperationPhase(context.Background(), OperationPhaseRecovery), supervisor.config.DeathWait)
	defer cancel()
	filters := supervisor.ownedResourceFilters("")
	inventory, inventoryErr := supervisor.inventoryResources(recoveryContext, filters)
	verified, ownershipErr := supervisor.verifyRecoveryResourceOwnership(recoveryContext, inventory)
	operationErr := supervisor.removeResources(recoveryContext, verified)
	postInventory, proofInventoryErr := supervisor.inventoryResources(recoveryContext, filters)
	if len(postInventory.containers) != 0 || len(postInventory.networks) != 0 {
		proofInventoryErr = errors.Join(proofInventoryErr, fmt.Errorf("prove owned Docker resources absent: containers=%v networks=%v", postInventory.containers, postInventory.networks))
	}
	if dockerErr := errors.Join(inventoryErr, ownershipErr, operationErr, proofInventoryErr); dockerErr != nil {
		return dockerErr
	}
	entries, err := os.ReadDir(supervisor.config.RuntimeRoot)
	if err != nil {
		return fmt.Errorf("enumerate runtime roots: %w", err)
	}
	var rootErrors, preservedErrors []error
	for _, entry := range entries {
		path := filepath.Join(supervisor.config.RuntimeRoot, entry.Name())
		if markerErr := supervisor.validateAttemptRoot(path, entry); markerErr != nil {
			preservedErrors = append(preservedErrors, fmt.Errorf("preserve unowned runtime entry %q: %w", entry.Name(), markerErr))
			continue
		}
		rootErrors = append(rootErrors, removeAndProveAbsent(path, "orphan runtime root"))
	}
	postEntries, rootProofErr := os.ReadDir(supervisor.config.RuntimeRoot)
	if rootProofErr != nil {
		rootProofErr = fmt.Errorf("prove runtime roots absent: %w", rootProofErr)
	} else {
		var ownedResidue []string
		for _, entry := range postEntries {
			path := filepath.Join(supervisor.config.RuntimeRoot, entry.Name())
			if supervisor.validateAttemptRoot(path, entry) == nil {
				ownedResidue = append(ownedResidue, entry.Name())
			}
		}
		if len(ownedResidue) != 0 {
			rootProofErr = fmt.Errorf("prove owned runtime roots absent: %v", ownedResidue)
		}
	}
	cleanupErr := errors.Join(errors.Join(rootErrors...), errors.Join(preservedErrors...), rootProofErr)
	if cleanupErr != nil {
		return cleanupErr
	}
	supervisor.mu.Lock()
	supervisor.recoveryComplete = true
	supervisor.mu.Unlock()
	return nil
}

func (supervisor *Supervisor) validateAttemptRoot(path string, entry os.DirEntry) error {
	if entry.Type()&os.ModeSymlink != 0 {
		return errors.New("symbolic link is never an owned Attempt root")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("entry is not a non-symlink directory")
	}
	name := entry.Name()
	if !validAttemptRootName(name) || filepath.Base(path) != name {
		return errors.New("entry name is not a Chora Attempt root identity")
	}
	markerPath := filepath.Join(path, attemptRootMarkerName)
	markerInfo, err := os.Lstat(markerPath)
	markerStat, markerOwned := markerInfoSys(markerInfo)
	if err != nil || !markerOwned || !markerInfo.Mode().IsRegular() || markerInfo.Mode()&os.ModeSymlink != 0 || markerInfo.Mode().Perm() != 0o400 || markerStat.Nlink != 1 {
		return errors.New("Attempt root ownership marker is missing or unsafe")
	}
	data, err := os.ReadFile(markerPath)
	if err != nil {
		return fmt.Errorf("read Attempt root ownership marker: %w", err)
	}
	var marker attemptRootMarker
	if err := strictJSON(data, &marker); err != nil {
		return fmt.Errorf("decode Attempt root ownership marker: %w", err)
	}
	canonical, marshalErr := json.Marshal(marker)
	if marshalErr != nil || !bytes.Equal(data, append(canonical, '\n')) {
		return errors.New("Attempt root ownership marker is not canonical")
	}
	if marker.SchemaVersion != attemptRootMarkerSchema || marker.Owner != "dockersupervisor" ||
		marker.RuntimeScope != supervisor.recoveryScope || marker.RootName != name ||
		!validText(marker.AttemptID, 256) || !validCanonicalDigest(marker.PolicyDigest) {
		return errors.New("Attempt root ownership marker identity mismatch")
	}
	return nil
}

func markerInfoSys(info os.FileInfo) (*syscall.Stat_t, bool) {
	if info == nil {
		return nil, false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return stat, ok && int(stat.Uid) == os.Geteuid()
}

func validAttemptRootName(name string) bool {
	const prefix = "chora-"
	if !strings.HasPrefix(name, prefix) || len(name) != len(prefix)+24 {
		return false
	}
	return isLowerHex(name[len(prefix):])
}

func (supervisor *Supervisor) ownedResourceFilters(attemptID string) []string {
	filters := []string{
		"label=chora.owner=dockersupervisor",
		"label=chora.runtime_scope=" + supervisor.recoveryScope,
	}
	if attemptID != "" {
		filters = append(filters, "label=chora.policy_digest="+supervisor.config.PolicyDigest, "label=chora.attempt_id="+attemptID)
	}
	return filters
}

func appendDockerFilters(args, filters []string) []string {
	for _, filter := range filters {
		args = append(args, "--filter", filter)
	}
	return args
}

func (supervisor *Supervisor) inventoryResources(ctx context.Context, filters []string) (dockerResourceInventory, error) {
	containerText, containerErr := supervisor.identityCommand(ctx, appendDockerFilters([]string{"ps", "-aq"}, filters))
	networkText, networkErr := supervisor.identityCommand(ctx, appendDockerFilters([]string{"network", "ls", "-q"}, filters))
	return dockerResourceInventory{containers: strings.Fields(containerText), networks: strings.Fields(networkText)},
		errors.Join(wrapError("enumerate owned containers", containerErr), wrapError("enumerate owned networks", networkErr))
}

func (supervisor *Supervisor) removeResources(ctx context.Context, inventory dockerResourceInventory) error {
	containers := uniqueNonempty(inventory.containers)
	networks := uniqueNonempty(inventory.networks)
	var failures []error
	for _, network := range networks {
		for _, container := range containers {
			if err := supervisor.cleanupDockerCommand(ctx, []string{"network", "disconnect", "-f", network, container}); err != nil {
				failures = append(failures, fmt.Errorf("disconnect owned container %s from network %s: %w", container, network, err))
			}
		}
	}
	for _, container := range containers {
		if err := supervisor.cleanupDockerCommand(ctx, []string{"rm", "-f", container}); err != nil {
			failures = append(failures, fmt.Errorf("remove owned container %s: %w", container, err))
		}
	}
	for _, network := range networks {
		if err := supervisor.cleanupDockerCommand(ctx, []string{"network", "rm", network}); err != nil {
			failures = append(failures, fmt.Errorf("remove owned network %s: %w", network, err))
		}
	}
	return errors.Join(failures...)
}

func (supervisor *Supervisor) cleanupDockerCommand(ctx context.Context, args []string) error {
	result, err := supervisor.config.Runner.Run(ctx, Command{Args: slices.Clone(args)})
	if err == nil && result.ExitCode == 0 {
		return nil
	}
	diagnostic := strings.ToLower(string(result.Stderr))
	if strings.Contains(diagnostic, "no such container") || strings.Contains(diagnostic, "no such network") ||
		strings.Contains(diagnostic, "not connected") {
		return nil
	}
	return fmt.Errorf("docker exit %d: %v: %s", result.ExitCode, err, boundedDiagnostic(result.Stderr))
}

func (supervisor *Supervisor) proveAttemptResourcesAbsent(ctx context.Context, filters []string, record *attemptRecord) error {
	inventory, inventoryErr := supervisor.inventoryResources(ctx, filters)
	exactInventory, exactInventoryErr := supervisor.exactAttemptResources(ctx, record)
	inventory.containers = append(inventory.containers, exactInventory.containers...)
	inventory.networks = append(inventory.networks, exactInventory.networks...)
	var residueErr error
	if len(inventory.containers) != 0 || len(inventory.networks) != 0 {
		residueErr = fmt.Errorf("owned Attempt residue remains: containers=%v networks=%v", inventory.containers, inventory.networks)
	}
	return errors.Join(inventoryErr, exactInventoryErr, residueErr)
}

func (supervisor *Supervisor) exactAttemptResources(ctx context.Context, record *attemptRecord) (dockerResourceInventory, error) {
	exactContainers, exactContainerErr := supervisor.identityCommand(ctx, []string{"ps", "-aq", "--filter", "name=^/(" + record.container + "|" + record.boundary + "|" + record.container + "-check-[0-9]+)$"})
	exactNetworks, exactNetworkErr := supervisor.identityCommand(ctx, []string{"network", "ls", "-q", "--filter", "name=^(" + record.internalNetwork + "|" + record.upstreamNetwork + ")$"})
	return dockerResourceInventory{containers: strings.Fields(exactContainers), networks: strings.Fields(exactNetworks)},
		errors.Join(wrapError("inventory exact containers", exactContainerErr), wrapError("inventory exact networks", exactNetworkErr))
}

type resourceOwnershipObservation struct {
	Name   string            `json:"name"`
	Image  string            `json:"image,omitempty"`
	Labels map[string]string `json:"labels"`
}

func (supervisor *Supervisor) verifyExactResourceOwnership(ctx context.Context, labeled, exact dockerResourceInventory, record *attemptRecord) (dockerResourceInventory, error) {
	approved := dockerResourceInventory{}
	var failures []error
	for _, container := range uniqueNonempty(append(slices.Clone(labeled.containers), exact.containers...)) {
		observation, err := supervisor.observeResourceOwnership(ctx, "container", container, containerOwnershipObservationFormat)
		if err != nil || !supervisor.validAttemptOwnership(observation.Labels, record) || !validAttemptContainerName(observation.Name, record) {
			failures = append(failures, fmt.Errorf("candidate container %s ownership is unproven: %w", container, errors.Join(err, errors.New("owner, scope, Attempt, policy, or name mismatch"))))
			continue
		}
		approved.containers = append(approved.containers, container)
	}
	for _, network := range uniqueNonempty(append(slices.Clone(labeled.networks), exact.networks...)) {
		observation, err := supervisor.observeResourceOwnership(ctx, "network", network, networkOwnershipObservationFormat)
		if err != nil || !supervisor.validAttemptOwnership(observation.Labels, record) || !validAttemptNetworkName(observation.Name, record) {
			failures = append(failures, fmt.Errorf("candidate network %s ownership is unproven: %w", network, errors.Join(err, errors.New("owner, scope, Attempt, policy, or name mismatch"))))
			continue
		}
		approved.networks = append(approved.networks, network)
	}
	return approved, errors.Join(failures...)
}

func (supervisor *Supervisor) verifyRecoveryResourceOwnership(ctx context.Context, inventory dockerResourceInventory) (dockerResourceInventory, error) {
	approved := dockerResourceInventory{}
	var failures []error
	for _, container := range uniqueNonempty(inventory.containers) {
		observation, err := supervisor.observeResourceOwnership(ctx, "container", container, containerOwnershipObservationFormat)
		if err != nil || !supervisor.validRecoveryOwnership(observation.Labels) {
			failures = append(failures, fmt.Errorf("recovery container %s ownership is unproven: %w", container, errors.Join(err, errors.New("owner or scope mismatch"))))
			continue
		}
		approved.containers = append(approved.containers, container)
	}
	for _, network := range uniqueNonempty(inventory.networks) {
		observation, err := supervisor.observeResourceOwnership(ctx, "network", network, networkOwnershipObservationFormat)
		if err != nil || !supervisor.validRecoveryOwnership(observation.Labels) {
			failures = append(failures, fmt.Errorf("recovery network %s ownership is unproven: %w", network, errors.Join(err, errors.New("owner or scope mismatch"))))
			continue
		}
		approved.networks = append(approved.networks, network)
	}
	return approved, errors.Join(failures...)
}

func (supervisor *Supervisor) observeResourceOwnership(ctx context.Context, kind, identity, format string) (resourceOwnershipObservation, error) {
	text, err := runnerText(ctx, supervisor.config.Runner, []string{kind, "inspect", "--format", format, identity})
	if err != nil {
		return resourceOwnershipObservation{}, err
	}
	var observation resourceOwnershipObservation
	if err := strictJSON([]byte(text), &observation); err != nil {
		return resourceOwnershipObservation{}, fmt.Errorf("malformed ownership observation: %w", err)
	}
	return observation, nil
}

func (supervisor *Supervisor) validAttemptOwnership(labels map[string]string, record *attemptRecord) bool {
	return supervisor.validRecoveryOwnership(labels) &&
		labels["chora.attempt_id"] == record.attemptID && labels["chora.policy_digest"] == supervisor.config.PolicyDigest
}

func (supervisor *Supervisor) validRecoveryOwnership(labels map[string]string) bool {
	return labels["chora.owner"] == "dockersupervisor" && labels["chora.runtime_scope"] == supervisor.recoveryScope
}

func validAttemptContainerName(name string, record *attemptRecord) bool {
	name = strings.TrimPrefix(name, "/")
	if name == record.container || name == record.boundary {
		return true
	}
	prefix := record.container + "-check-"
	suffix := strings.TrimPrefix(name, prefix)
	if suffix == name || suffix == "" {
		return false
	}
	value, err := strconv.Atoi(suffix)
	return err == nil && value > 0 && strconv.Itoa(value) == suffix
}

func validAttemptNetworkName(name string, record *attemptRecord) bool {
	return name == record.internalNetwork || name == record.upstreamNetwork
}

func removeAndProveAbsent(path, description string) error {
	if path == "" {
		return fmt.Errorf("remove %s: empty path", description)
	}
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("remove %s: %w", description, err)
	}
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("prove %s absent: residue remains", description)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("prove %s absent: %w", description, err)
	}
	return nil
}

func uniqueNonempty(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func wrapError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func (supervisor *Supervisor) deriveTerminal(record *attemptRecord) {
	record.mu.Lock()
	timedOut := record.timedOut
	record.mu.Unlock()
	if timedOut {
		supervisor.deriveTimeoutTerminal(record)
		return
	}
	plan := readTerminalPlan(record.contextDir)
	patch, patchErr := derivePatch(record.baseline, record.repository, plan.writableFiles, artifactBytes)
	var patchArtifact map[string]string
	if patchErr == nil {
		patchErr = writeExclusive(record.terminal.Paths["patch"], patch)
	}
	if patchErr == nil {
		patchArtifact, patchErr = persistArtifact(record, "patch.diff", "text/x-diff", "Chora-owned applicable unified diff", patch)
	}
	checks, checksComplete := supervisor.runDeclaredChecks(record, plan)
	checksBytes, _ := json.Marshal(checks)
	checksBytes = append(checksBytes, '\n')
	checksErr := writeExclusive(record.terminal.Paths["checks"], checksBytes)
	var checksArtifact map[string]string
	if checksErr == nil {
		checksArtifact, checksErr = persistArtifact(record, "checks.json", "application/json", "Chora-owned declared check evidence", checksBytes)
	}
	supervisor.mu.Lock()
	providerIdentity := supervisor.providerIdentity
	engineIdentity := supervisor.engineIdentity
	supervisor.mu.Unlock()
	providerIdentity = providerEvidence(providerIdentity, engineIdentity)
	identity := map[string]string{
		"container": record.container, "attempt_image": AttemptImage, "attempt_image_id": supervisor.config.AttemptImageID,
		"boundary_image": BoundaryImage, "boundary_image_id": supervisor.config.BoundaryImageID,
		"policy_digest": supervisor.config.PolicyDigest, "runtime_scope": supervisor.recoveryScope,
		"agent_execution_profile": record.executionProfile, "capability_policy": record.capabilityPolicy,
		"capability_bundle_id": record.capabilityBundleID, "capability_bundle_sha256": record.capabilityBundleSHA256,
		"engine_qualification_digest": supervisor.config.EngineQualification.Digest(), "docker_engine_digest": engineIdentity.Digest(),
		"docker_api_version": engineIdentity.APIVersion(), "docker_client_version": providerIdentity.DockerClientVersion,
		"docker_server_version": providerIdentity.DockerServerVersion, "docker_context": providerIdentity.DockerContext,
		"docker_provider": engineIdentity.ProviderName(), "colima_version": providerIdentity.ColimaVersion,
		"tool_cache": toolCachePath, "tool_temp": toolTempPath,
	}
	record.mu.Lock()
	injectedFailure := record.acceptanceDirective.Action == acceptanceauthority.ActionForceManagedNonzero && record.acceptanceInjectionState == acceptanceInjectionConfirmed && record.exitCode == 137
	record.mu.Unlock()
	if injectedFailure {
		identity["acceptance_authority_digest"] = record.acceptanceDirective.AuthorityDigest
		identity["acceptance_consumption_digest"] = record.acceptanceDirective.ConsumptionDigest
	}
	identityBytes, _ := json.Marshal(identity)
	identityBytes = append(identityBytes, '\n')
	identityErr := writeExclusive(record.terminal.Paths["runtime_identity"], identityBytes)
	var identityArtifact map[string]string
	if identityErr == nil {
		identityArtifact, identityErr = persistArtifact(record, "runtime-identity.json", "application/json", "Verified Runtime and Sandbox identity", identityBytes)
	}
	sealErr := sealArtifactDirectory(record.artifactDir)
	unknowns := []string{}
	if patchErr != nil {
		unknowns = append(unknowns, "patch evidence unavailable: "+patchErr.Error())
	}
	if checksErr != nil || !checksComplete {
		unknowns = append(unknowns, "declared check evidence unavailable")
	}
	if identityErr != nil {
		unknowns = append(unknowns, "runtime identity evidence unavailable")
	}
	if sealErr != nil {
		unknowns = append(unknowns, "durable artifact directory unavailable: "+sealErr.Error())
	}
	outputs := []map[string]string{}
	if patchErr == nil && sealErr == nil {
		outputs = append(outputs, patchArtifact)
	}
	artifacts := []map[string]string{}
	if checksErr == nil && sealErr == nil {
		artifacts = append(artifacts, checksArtifact)
	}
	if identityErr == nil && sealErr == nil {
		artifacts = append(artifacts, identityArtifact)
	}
	result := map[string]any{"schema_version": "chora.agent-result.v1", "summary": "Chora derived terminal evidence after sandbox death.", "review_ready": patchErr == nil && checksErr == nil && identityErr == nil && sealErr == nil && checksComplete && checksPass(checks), "outputs": outputs, "artifact_candidates": artifacts, "checks": checks, "unknowns": unknowns, "handoff": map[string]any{"requested": false, "reason": ""}}
	data, _ := json.Marshal(result)
	data = append(data, '\n')
	_ = writeExclusive(record.terminal.Paths["result"], data)
}

func (supervisor *Supervisor) deriveTimeoutTerminal(record *attemptRecord) {
	supervisor.mu.Lock()
	providerIdentity := supervisor.providerIdentity
	engineIdentity := supervisor.engineIdentity
	supervisor.mu.Unlock()
	providerIdentity = providerEvidence(providerIdentity, engineIdentity)
	identity := map[string]string{
		"container": record.container, "attempt_image": AttemptImage, "attempt_image_id": supervisor.config.AttemptImageID,
		"boundary_image": BoundaryImage, "boundary_image_id": supervisor.config.BoundaryImageID,
		"policy_digest": supervisor.config.PolicyDigest, "runtime_scope": supervisor.recoveryScope,
		"acceptance_authority_digest":   record.acceptanceDirective.AuthorityDigest,
		"acceptance_consumption_digest": record.acceptanceDirective.ConsumptionDigest,
		"engine_qualification_digest":   supervisor.config.EngineQualification.Digest(), "docker_engine_digest": engineIdentity.Digest(), "docker_api_version": engineIdentity.APIVersion(), "docker_provider": engineIdentity.ProviderName(),
		"docker_client_version": providerIdentity.DockerClientVersion, "docker_server_version": providerIdentity.DockerServerVersion,
		"docker_context": providerIdentity.DockerContext, "colima_version": providerIdentity.ColimaVersion,
	}
	identityBytes, _ := json.Marshal(identity)
	identityBytes = append(identityBytes, '\n')
	identityErr := writeExclusive(record.terminal.Paths["runtime_identity"], identityBytes)
	var identityArtifact map[string]string
	if identityErr == nil {
		identityArtifact, identityErr = persistArtifact(record, "runtime-identity.json", "application/json", "Verified Runtime, Sandbox, and timeout-policy identity", identityBytes)
	}
	sealErr := sealArtifactDirectory(record.artifactDir)
	timeoutUnknown := "attempt timeout exceeded limit of " + record.attemptTimeout.String()
	if record.acceptanceDirective.Action == acceptanceauthority.ActionAcceleratedManagedDeadline {
		timeoutUnknown = "attempt timeout exceeded the fixed acceptance-only three-second deadline"
	}
	unknowns := []string{timeoutUnknown}
	artifacts := []map[string]string{}
	if identityErr == nil && sealErr == nil {
		artifacts = append(artifacts, identityArtifact)
	}
	if identityErr != nil {
		unknowns = append(unknowns, "runtime identity evidence unavailable")
	}
	if sealErr != nil {
		unknowns = append(unknowns, "durable artifact directory unavailable: "+sealErr.Error())
	}
	result := map[string]any{
		"schema_version": "chora.agent-result.v1", "summary": "Chora stopped the Attempt at the acceptance-only timeout boundary.",
		"review_ready": false, "outputs": []map[string]string{}, "artifact_candidates": artifacts,
		"checks": []checkDocument{}, "unknowns": unknowns, "handoff": map[string]any{"requested": false, "reason": ""},
	}
	data, _ := json.Marshal(result)
	data = append(data, '\n')
	_ = writeExclusive(record.terminal.Paths["result"], data)
}

func (supervisor *Supervisor) deriveInjectedFailureTerminal(record *attemptRecord) {
	supervisor.mu.Lock()
	providerIdentity := supervisor.providerIdentity
	engineIdentity := supervisor.engineIdentity
	supervisor.mu.Unlock()
	providerIdentity = providerEvidence(providerIdentity, engineIdentity)
	identity := map[string]string{
		"container": record.container, "attempt_image": AttemptImage, "attempt_image_id": supervisor.config.AttemptImageID,
		"boundary_image": BoundaryImage, "boundary_image_id": supervisor.config.BoundaryImageID,
		"policy_digest": supervisor.config.PolicyDigest, "runtime_scope": supervisor.recoveryScope,
		"acceptance_authority_digest":   record.acceptanceDirective.AuthorityDigest,
		"acceptance_consumption_digest": record.acceptanceDirective.ConsumptionDigest,
		"engine_qualification_digest":   supervisor.config.EngineQualification.Digest(), "docker_engine_digest": engineIdentity.Digest(), "docker_api_version": engineIdentity.APIVersion(), "docker_provider": engineIdentity.ProviderName(),
		"docker_client_version": providerIdentity.DockerClientVersion, "docker_server_version": providerIdentity.DockerServerVersion,
		"docker_context": providerIdentity.DockerContext, "colima_version": providerIdentity.ColimaVersion,
	}
	identityBytes, _ := json.Marshal(identity)
	identityBytes = append(identityBytes, '\n')
	identityErr := writeExclusive(record.terminal.Paths["runtime_identity"], identityBytes)
	var identityArtifact map[string]string
	if identityErr == nil {
		identityArtifact, identityErr = persistArtifact(record, "runtime-identity.json", "application/json", "Verified Runtime, Sandbox, and acceptance-failure identity", identityBytes)
	}
	sealErr := sealArtifactDirectory(record.artifactDir)
	unknowns := []string{"authorized acceptance-only nonzero termination; no Patch or check evidence accepted"}
	artifacts := []map[string]string{}
	if identityErr == nil && sealErr == nil {
		artifacts = append(artifacts, identityArtifact)
	}
	if identityErr != nil {
		unknowns = append(unknowns, "runtime identity evidence unavailable")
	}
	if sealErr != nil {
		unknowns = append(unknowns, "durable artifact directory unavailable")
	}
	result := map[string]any{
		"schema_version": "chora.agent-result.v1", "summary": "Chora confirmed the authorized acceptance-only nonzero termination.",
		"review_ready": false, "outputs": []map[string]string{}, "artifact_candidates": artifacts,
		"checks": []checkDocument{}, "unknowns": unknowns, "handoff": map[string]any{"requested": false, "reason": ""},
	}
	data, _ := json.Marshal(result)
	data = append(data, '\n')
	_ = writeExclusive(record.terminal.Paths["result"], data)
}

func providerEvidence(provider ProviderIdentity, engine EngineIdentity) ProviderIdentity {
	if len(engine.canonical) == 0 {
		return provider
	}
	if provider.DockerServerVersion == "" {
		provider.DockerServerVersion = engine.EngineVersion()
	}
	if provider.DockerContext == "" {
		provider.DockerContext = engine.ContextName()
	}
	return provider
}

func persistArtifact(record *attemptRecord, name, mediaType, description string, data []byte) (map[string]string, error) {
	if err := os.MkdirAll(record.artifactDir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(record.artifactDir, name)
	if err := writeExclusive(path, data); err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o400); err != nil {
		return nil, err
	}
	digest := sha256.Sum256(data)
	return map[string]string{
		"locator":     filepath.ToSlash(filepath.Join(record.artifactLocatorPrefix, name)),
		"description": description,
		"sha256":      hex.EncodeToString(digest[:]),
		"media_type":  mediaType,
	}, nil
}

func sealArtifactDirectory(directory string) error {
	if _, err := os.Stat(directory); err != nil {
		return err
	}
	return os.Chmod(directory, 0o500)
}

type checkDocument struct {
	CriterionID string `json:"criterion_id"`
	Status      string `json:"status"`
	Evidence    string `json:"evidence"`
}

type terminalPlan struct {
	criteria      []string
	commands      []declaredCommand
	writableFiles []string
}

type declaredCommand struct {
	id   string
	argv []string
}

type terminalCriterion struct {
	ID string `json:"id"`
}

type terminalBoundary struct {
	WritableFiles []string `json:"writable_files"`
	Commands      []struct {
		ID   string   `json:"id"`
		Argv []string `json:"argv"`
	} `json:"commands"`
	TestCommandIDs []string `json:"test_command_ids"`
}

type terminalContractDocument struct {
	SchemaVersion string `json:"schema_version"`
	Task          struct {
		ID                 string              `json:"id"`
		AcceptanceCriteria []terminalCriterion `json:"acceptance_criteria"`
	} `json:"task"`
	AcceptanceCriteria []terminalCriterion `json:"acceptance_criteria"`
	Acceptance         struct {
		Criteria []terminalCriterion `json:"criteria"`
	} `json:"acceptance"`
	Execution struct {
		Boundary terminalBoundary `json:"boundary"`
	} `json:"execution"`
}

type terminalSnapshotDocument struct {
	Task struct {
		ID string `json:"id"`
	} `json:"task"`
}

func readTerminalPlan(contextDir string) terminalPlan {
	data, err := os.ReadFile(filepath.Join(contextDir, "snapshot.jsonl"))
	if err != nil {
		return terminalPlan{}
	}
	var command struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &command) != nil {
		return terminalPlan{}
	}
	snapshotBytes, contractBytes, ok := execution.UnwrapFrozenExecutionPrompt(command.Message)
	if !ok {
		return terminalPlan{}
	}
	plan, _, valid := decodeTerminalPlan(snapshotBytes, contractBytes)
	if !valid {
		return terminalPlan{}
	}
	return plan
}

func decodeTerminalPlan(snapshotBytes, contractBytes []byte) (terminalPlan, string, bool) {
	var snapshot terminalSnapshotDocument
	var contract terminalContractDocument
	if json.Unmarshal(snapshotBytes, &snapshot) != nil || json.Unmarshal(contractBytes, &contract) != nil ||
		!registeredTerminalSchema(contract.SchemaVersion) || strings.TrimSpace(snapshot.Task.ID) == "" || contract.Task.ID != snapshot.Task.ID {
		return terminalPlan{}, "", false
	}
	criteria := contract.Acceptance.Criteria
	boundary := contract.Execution.Boundary
	if len(criteria) == 0 || len(boundary.WritableFiles) == 0 || len(boundary.Commands) == 0 || len(boundary.TestCommandIDs) == 0 {
		return terminalPlan{}, "", false
	}
	plan := terminalPlan{criteria: make([]string, 0, len(criteria)), writableFiles: append([]string(nil), boundary.WritableFiles...)}
	for _, criterion := range criteria {
		if criterion.ID == "" {
			return terminalPlan{}, "", false
		}
		plan.criteria = append(plan.criteria, criterion.ID)
	}
	byID := make(map[string][]string, len(boundary.Commands))
	for _, bounded := range boundary.Commands {
		if bounded.ID != "" && len(bounded.Argv) > 0 {
			byID[bounded.ID] = bounded.Argv
		}
	}
	for _, id := range boundary.TestCommandIDs {
		if argv := byID[id]; len(argv) > 0 {
			plan.commands = append(plan.commands, declaredCommand{id: id, argv: argv})
		} else {
			return terminalPlan{}, "", false
		}
	}
	return plan, snapshot.Task.ID, true
}

func registeredTerminalSchema(schema string) bool {
	return schema == "chora.spec-coding-core.v5" || schema == "chora.spec-coding-core.v6" ||
		schema == "chora.spec-coding-core.v7" || schema == "chora.spec-coding-core.v8" ||
		schema == "chora.spec-coding-core.v9" || schema == "chora.spec-coding-core.v10"
}

func (supervisor *Supervisor) runDeclaredChecks(record *attemptRecord, plan terminalPlan) ([]checkDocument, bool) {
	if len(plan.criteria) == 0 || len(plan.commands) == 0 {
		checks := make([]checkDocument, 0, len(plan.criteria))
		for _, id := range plan.criteria {
			checks = append(checks, checkDocument{CriterionID: id, Status: string(execution.CheckUnknown), Evidence: "No command-bound check was available to supervisor."})
		}
		return checks, false
	}
	allPass := true
	evidence := make([]string, 0, len(plan.commands))
	for index, command := range plan.commands {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		args := []string{
			"run", "--pull=never", "--rm", "--name", record.container + "-check-" + strconv.Itoa(index+1),
			"--label", "chora.owner=dockersupervisor", "--label", "chora.policy_digest=" + supervisor.config.PolicyDigest, "--label", "chora.runtime_scope=" + supervisor.recoveryScope,
			"--label", "chora.run_id=" + record.runID, "--label", "chora.attempt_id=" + record.attemptID, "--label", "chora.task_id=" + record.taskID,
			"--label", "chora.image_digest=" + supervisor.config.AttemptImageID,
			"--network", record.internalNetwork, "--user", "1000:1000", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true",
			"--pids-limit", "256", "--cpus", "2", "--memory", "4096m", "--memory-swap", "4096m", "--ulimit", "nofile=1024:1024",
			"--tmpfs", "/tmp:rw,nosuid,nodev,noexec,uid=1000,gid=1000,size=64m", "--tmpfs", "/run/chora/pi:rw,nosuid,nodev,noexec,uid=1000,gid=1000,size=16m",
			"--env", "HOME=/run/chora/pi", "--env", "GOCACHE=" + toolCachePath, "--env", "GOTMPDIR=" + toolTempPath,
			"--mount", "type=bind,src=" + filepath.Join(record.root, "workspace") + ",dst=/workspace",
			"--mount", "type=bind,src=" + record.contextDir + ",dst=/input/context,readonly", "--workdir", "/workspace/repository",
			"--entrypoint", command.argv[0], supervisor.config.AttemptImageID,
		}
		args = append(args, command.argv[1:]...)
		result, err := supervisor.config.Runner.Run(ctx, Command{Args: args})
		cancel()
		passed := err == nil && result.ExitCode == 0
		allPass = allPass && passed
		status := "PASS"
		if !passed {
			status = "FAIL"
		}
		evidence = append(evidence, command.id+"="+status)
	}
	status := string(execution.CheckPass)
	if !allPass {
		status = string(execution.CheckFail)
	}
	checks := make([]checkDocument, 0, len(plan.criteria))
	for _, id := range plan.criteria {
		checks = append(checks, checkDocument{CriterionID: id, Status: status, Evidence: strings.Join(evidence, ", ")})
	}
	return checks, true
}

func checksPass(checks []checkDocument) bool {
	if len(checks) == 0 {
		return false
	}
	for _, check := range checks {
		if check.Status != string(execution.CheckPass) {
			return false
		}
	}
	return true
}

func writeExclusive(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func (supervisor *Supervisor) recordForHandle(handle execution.RuntimeHandle) (*attemptRecord, error) {
	supervisor.mu.Lock()
	record := supervisor.byHandle[handle.Value]
	supervisor.mu.Unlock()
	if record == nil {
		return nil, ErrUnknownHandle
	}
	return record, nil
}
func (supervisor *Supervisor) record(handle execution.RuntimeHandle) *attemptRecord {
	record, _ := supervisor.recordForHandle(handle)
	return record
}
func (record *attemptRecord) isExited() bool {
	record.mu.Lock()
	defer record.mu.Unlock()
	return record.exited
}
func (record *attemptRecord) stream(kind execution.StreamKind) (*cappedStream, bool) {
	if kind == execution.StreamStdout {
		return &record.stdout, true
	}
	if kind == execution.StreamStderr {
		return &record.stderr, true
	}
	return nil, false
}

type cappedStream struct {
	mu      sync.RWMutex
	data    []byte
	pending []byte
	secrets [][]byte
}

func (stream *cappedStream) append(data []byte) (int, int64) {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	combined := make([]byte, 0, len(stream.pending)+len(data))
	combined = append(combined, stream.pending...)
	combined = append(combined, data...)
	redacted := redactCredentialValues(combined, stream.secrets)
	safeEnd := len(redacted)
	for _, secret := range stream.secrets {
		for prefix := min(len(secret)-1, len(redacted)); prefix > 0; prefix-- {
			if bytes.HasSuffix(redacted, secret[:prefix]) {
				safeEnd = min(safeEnd, len(redacted)-prefix)
				break
			}
		}
	}
	stream.pending = append(stream.pending[:0], combined[safeEnd:]...)
	return stream.appendLocked(redacted[:safeEnd])
}

func (stream *cappedStream) setSecrets(secrets [][]byte) {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	stream.secrets = make([][]byte, 0, len(secrets))
	for _, secret := range secrets {
		stream.secrets = append(stream.secrets, append([]byte(nil), secret...))
	}
	slices.SortFunc(stream.secrets, func(left, right []byte) int { return len(right) - len(left) })
}

func (stream *cappedStream) flush() (int, int64) {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	data := redactCredentialValues(stream.pending, stream.secrets)
	clear(stream.pending)
	stream.pending = nil
	return stream.appendLocked(data)
}

func (stream *cappedStream) appendLocked(data []byte) (int, int64) {
	available := persistedLogBytes - len(stream.data)
	if available <= 0 {
		return 0, int64(len(stream.data))
	}
	if len(data) > available {
		data = data[:available]
	}
	stream.data = append(stream.data, data...)
	return len(data), int64(len(stream.data))
}

func redactCredentialValues(data []byte, secrets [][]byte) []byte {
	redacted := append([]byte(nil), data...)
	for _, secret := range secrets {
		if len(secret) == 0 {
			continue
		}
		redacted = bytes.ReplaceAll(redacted, secret, bytes.Repeat([]byte{'*'}, len(secret)))
	}
	return redacted
}
func (stream *cappedStream) read(offset int64, limit int) ([]byte, int64, bool) {
	stream.mu.RLock()
	defer stream.mu.RUnlock()
	if offset < 0 || offset > int64(len(stream.data)) || limit < 0 {
		return nil, 0, false
	}
	end := int64(len(stream.data))
	if end-offset > int64(limit) {
		end = offset + int64(limit)
	}
	return append([]byte(nil), stream.data[offset:end]...), end, true
}
func (stream *cappedStream) length() int64 {
	stream.mu.RLock()
	defer stream.mu.RUnlock()
	return int64(len(stream.data))
}

type streamWriter struct {
	record *attemptRecord
	kind   execution.StreamKind
	scanMu sync.Mutex
	scan   []byte
}

func (writer *streamWriter) Write(data []byte) (int, error) {
	accepted, offset := func() (int, int64) { stream, _ := writer.record.stream(writer.kind); return stream.append(data) }()
	if accepted > 0 {
		safeNotify(writer.record.sink, writer.kind, offset)
	}
	if writer.kind == execution.StreamStdout {
		writer.detectAgentEnd(data)
	}
	return len(data), nil
}
func (writer *streamWriter) detectAgentEnd(data []byte) {
	writer.scanMu.Lock()
	defer writer.scanMu.Unlock()
	writer.scan = append(writer.scan, data...)
	if len(writer.scan) > persistedLogBytes {
		writer.scan = writer.scan[len(writer.scan)-persistedLogBytes:]
	}
	for {
		newline := bytes.IndexByte(writer.scan, '\n')
		if newline < 0 {
			return
		}
		line := writer.scan[:newline]
		writer.scan = writer.scan[newline+1:]
		var event struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(line, &event) == nil && event.Type == "agent_end" {
			writer.record.closeStdin()
		}
	}
}
func (record *attemptRecord) closeStdin() {
	record.mu.Lock()
	defer record.mu.Unlock()
	if !record.stdinClosed && record.process != nil {
		record.stdinClosed = true
		_ = record.process.Close()
	}
}

func cloneTerminal(files execution.TerminalFiles) execution.TerminalFiles {
	paths := make(map[string]string, len(files.Paths))
	for key, value := range files.Paths {
		paths[key] = value
	}
	return execution.TerminalFiles{Paths: paths, ExitCode: files.ExitCode, TerminationCause: files.TerminationCause}
}
func boundedDiagnostic(data []byte) string {
	if len(data) > 8192 {
		data = data[:8192]
	}
	return strings.TrimSpace(string(data))
}
func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
func validImageID(value string) bool {
	return strings.HasPrefix(value, "sha256:") && validDigest(strings.TrimPrefix(value, "sha256:"))
}
func managedRuntimeSourceIdentity(imageID string) string {
	digest := sha256.Sum256([]byte(strings.Join([]string{
		managedSourceContractVersion, managedSourceKind, managedRuntimeVersion,
		allowedExecutable, managedRuntimeConfigDigest, imageID, "development_fixture",
		"", "", "", "", "", "0", "", "", "", "", "", "", "", "", "", "", "", "", "",
	}, "\x00")))
	return hex.EncodeToString(digest[:])
}
func randomSuffix() (string, error) {
	data := make([]byte, 12)
	if _, err := io.ReadFull(rand.Reader, data); err != nil {
		return "", err
	}
	return hex.EncodeToString(data), nil
}
func noChild(launch execution.LaunchToken, diagnostic string) execution.StartOutcome {
	return execution.StartOutcome{Kind: execution.StartProvenNoChild, LaunchToken: launch, Diagnostic: diagnostic}
}
func safeNotify(sink execution.RuntimeSink, kind execution.StreamKind, offset int64) {
	defer func() { _ = recover() }()
	sink.Notify(kind, offset)
}
func safeExited(sink execution.RuntimeSink) { defer func() { _ = recover() }(); sink.Exited() }

var _ execution.ProcessSupervisor = (*Supervisor)(nil)
