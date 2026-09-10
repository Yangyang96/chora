package localweb

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
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/baselinebundle"
	"github.com/Yangyang96/chora/internal/dockersupervisor"
	"github.com/Yangyang96/chora/internal/releaseassets"
	storecontract "github.com/Yangyang96/chora/internal/store"
	"github.com/Yangyang96/chora/internal/verifier"
)

const verifierPolicyModeEnvironment = "CHORA_VERIFIER_POLICY_MODE"

type verifierRuntimeStatus struct {
	Enabled          bool   `json:"enabled"`
	Reason           string `json:"reason"`
	Mode             string `json:"mode"`
	PolicyVersion    string `json:"policyVersion"`
	PolicyDigest     string `json:"policyDigest"`
	BaselineDigest   string `json:"baselineDigest"`
	Image            string `json:"image"`
	Network          string `json:"network"`
	Credentials      string `json:"credentials"`
	ResourceBoundary string `json:"resourceBoundary"`
}

type productVerifier struct {
	core             verifierCore
	authority        app.VerificationAuthority
	workspaceFactory *verifier.FilesystemWorkspaceFactory
	sandboxLifecycle verifierSandboxLifecycle
	baselineRoot     string
	baselineManifest string
	artifactRoot     string
	patchLimit       int64
}

type verifierCore interface {
	Verify(context.Context, verifier.VerifyRequest) (verifier.AttemptEvidence, error)
}

type verifierSandboxLifecycle interface {
	verifier.SandboxFactory
	Recover(context.Context) error
}

type verifierSandboxLifecycleContextKey struct{}

func withVerifierSandboxLifecycle(ctx context.Context, lifecycle verifierSandboxLifecycle) context.Context {
	return context.WithValue(ctx, verifierSandboxLifecycleContextKey{}, lifecycle)
}

func verifierSandboxLifecycleFromContext(ctx context.Context) verifierSandboxLifecycle {
	lifecycle, _ := ctx.Value(verifierSandboxLifecycleContextKey{}).(verifierSandboxLifecycle)
	return lifecycle
}

func newProductVerifier(ctx context.Context, repoRoot, runtimeRoot string, reconstructBundledBaseline bool, options ProductOptions) (*productVerifier, verifierRuntimeStatus, error) {
	mode := strings.TrimSpace(os.Getenv(verifierPolicyModeEnvironment))
	policyName := "verifier-policy.v1.json"
	if mode == "" || mode == "full" {
		mode = "full"
	} else if mode == "acceptance-only" {
		policyName = "verifier-policy.acceptance-only.v1.json"
	} else {
		return nil, verifierRuntimeStatus{}, fmt.Errorf("%s must be full or acceptance-only", verifierPolicyModeEnvironment)
	}
	policyBytes, err := os.ReadFile(filepath.Join(repoRoot, "contracts", "g2-m4", policyName))
	if err != nil {
		return nil, verifierRuntimeStatus{}, fmt.Errorf("read VerifierPolicy: %w", err)
	}
	policy, err := verifier.DecodePolicyAuthority(policyBytes)
	if err != nil {
		return nil, verifierRuntimeStatus{}, err
	}
	var installedAuthority *installedVerifierDockerAuthority
	if reconstructBundledBaseline && !options.SourceCheckout {
		installedAuthority, err = bindInstalledVerifierDockerAuthority(ctx, filepath.ToSlash(filepath.Join("contracts", "g2-m4", policyName)), policyBytes, policy, options)
		if err != nil {
			return nil, verifierRuntimeStatus{}, err
		}
	} else if reconstructBundledBaseline && options.SourceCheckout {
		installedAuthority = &installedVerifierDockerAuthority{
			runner:     dockersupervisor.RunnerForOperationPhase(options.DockerRunner, dockersupervisor.OperationPhaseVerifier),
			capability: options.DockerCapability, qualification: options.DockerQualification,
			imageID: policy.Document().Image,
		}
		if err = installedAuthority.Verify(ctx); err != nil {
			return nil, verifierRuntimeStatus{}, fmt.Errorf("source-checkout Verifier Docker authority unavailable: %w", err)
		}
	}
	baselineRoot, baselineManifest, err := prepareProductBaseline(ctx, repoRoot, runtimeRoot, reconstructBundledBaseline)
	if err != nil {
		return nil, verifierRuntimeStatus{}, err
	}
	workspaceFactory, err := verifier.NewFilesystemWorkspaceFactory(filepath.Join(runtimeRoot, "verification", "workspaces"), isolatedGitExecutable)
	if err != nil {
		return nil, verifierRuntimeStatus{}, err
	}
	var sandboxLifecycle verifierSandboxLifecycle
	if !reconstructBundledBaseline {
		sandboxLifecycle = verifierSandboxLifecycleFromContext(ctx)
	}
	if sandboxLifecycle == nil {
		recoveryScopeDigest := sha256.Sum256([]byte(filepath.Clean(runtimeRoot)))
		dockerConfig := verifier.DockerFactoryConfig{RecoveryScope: "sha256:" + hex.EncodeToString(recoveryScopeDigest[:])}
		if installedAuthority != nil {
			dockerConfig.Runner = installedAuthority
			dockerConfig.InstalledAuthority = installedAuthority
		}
		dockerFactory, dockerErr := verifier.NewDockerSandboxFactory(dockerConfig)
		if dockerErr != nil {
			return nil, verifierRuntimeStatus{}, dockerErr
		}
		sandboxLifecycle = dockerFactory
	}
	core, err := verifier.New(verifier.Config{Policy: policy, WorkspaceFactory: workspaceFactory, SandboxFactory: sandboxLifecycle, Redactor: productLogRedactor{}})
	if err != nil {
		return nil, verifierRuntimeStatus{}, err
	}
	baselineDigest, err := decodeVerifierDigest(verifier.M1FrozenBaselineDigestHex)
	if err != nil {
		return nil, verifierRuntimeStatus{}, err
	}
	service := &productVerifier{
		core: core, authority: app.VerificationAuthority{BaselineDigest: baselineDigest, VerifierPolicyVersion: policy.Document().Version, VerifierPolicyDigest: policy.Digest()},
		workspaceFactory: workspaceFactory, sandboxLifecycle: sandboxLifecycle,
		baselineRoot: baselineRoot, baselineManifest: baselineManifest,
		artifactRoot: filepath.Join(runtimeRoot, "artifacts"), patchLimit: policy.Document().ArtifactBytes,
	}
	policyDigest := policy.Digest()
	imageID := policy.Document().Image
	if installedAuthority != nil {
		imageID = installedAuthority.VerifierImageID()
	}
	status := verifierRuntimeStatus{Enabled: true, Reason: "digest-pinned independent verifier configured", Mode: mode,
		PolicyVersion: policy.Document().Version, PolicyDigest: hex.EncodeToString(policyDigest[:]), BaselineDigest: verifier.M1FrozenBaselineDigestHex,
		Image: imageID, Network: "none", Credentials: "zero", ResourceBoundary: "2 CPU · 4096 MiB · 256 PID · 1024 FD"}
	return service, status, nil
}

type installedVerifierRolePolicy struct {
	SchemaVersion       string `json:"schema_version"`
	ExecutionPolicy     string `json:"execution_policy"`
	AgentRuntimeInvoked bool   `json:"agent_runtime_invoked"`
	Network             string `json:"network"`
	Credentials         string `json:"credentials"`
	Workspace           string `json:"workspace"`
	ImageAliasRole      string `json:"image_alias_role"`
	FailClosed          bool   `json:"fail_closed"`
}

type installedVerifierDockerAuthority struct {
	runner        dockersupervisor.CommandRunner
	capability    dockersupervisor.CapabilityProbeContract
	qualification dockersupervisor.EngineQualification
	imageID       string
}

func bindInstalledVerifierDockerAuthority(ctx context.Context, executionPolicyPath string, policyBytes []byte, policy verifier.Policy, options ProductOptions) (*installedVerifierDockerAuthority, error) {
	if options.ActivatedRelease == nil || options.ActivatedReleaseRoot == "" || options.DockerRunner == nil {
		return nil, errors.New("installed Verifier requires one explicit activated release root, its validated release, and setup-qualified Docker Runner")
	}
	release := *options.ActivatedRelease
	managed, managedErr := release.ImageForRole(releaseassets.RoleManagedPiRuntime)
	independent, independentErr := release.ImageForRole(releaseassets.RoleIndependentVerifier)
	probe, probeErr := release.ImageForRole(releaseassets.RoleCapabilityProbe)
	if err := errors.Join(managedErr, independentErr, probeErr); err != nil {
		return nil, fmt.Errorf("resolve installed Verifier roles: %w", err)
	}
	rolePolicyBytes, err := release.ReadPolicyForRole(options.ActivatedReleaseRoot, releaseassets.RoleIndependentVerifier)
	if err != nil {
		return nil, fmt.Errorf("read activated independent Verifier role policy: %w", err)
	}
	return bindInstalledVerifierDockerAuthorityFromImages(ctx, executionPolicyPath, policyBytes, rolePolicyBytes, policy, options.DockerRunner, options.DockerCapability, options.DockerQualification, managed, independent, probe)
}

func bindInstalledVerifierDockerAuthorityFromImages(ctx context.Context, executionPolicyPath string, policyBytes, rolePolicyBytes []byte, policy verifier.Policy, runner dockersupervisor.CommandRunner, capability dockersupervisor.CapabilityProbeContract, qualification dockersupervisor.EngineQualification, managed, independent, probe releaseassets.ActivatedImage) (*installedVerifierDockerAuthority, error) {
	if !dockerCommandRunnerAvailable(runner) || managed.Role != releaseassets.RoleManagedPiRuntime || independent.Role != releaseassets.RoleIndependentVerifier || probe.Role != releaseassets.RoleCapabilityProbe {
		return nil, errors.New("installed Verifier requires exact managed, independent Verifier, and capability Probe roles with one setup-qualified Runner")
	}
	if !exactActivatedArtifactAlias(independent, managed) || !exactActivatedArtifactAlias(probe, managed) {
		return nil, errors.New("installed independent Verifier and capability Probe roles must explicitly alias the managed image")
	}
	if independent.LocalDockerConfigImageID != policy.Document().Image {
		return nil, errors.New("installed independent Verifier role does not bind the exact execution-policy image")
	}
	if err := verifyInstalledVerifierRolePolicy(executionPolicyPath, policyBytes, rolePolicyBytes, policy, independent); err != nil {
		return nil, err
	}
	restoredContract, contractErr := dockersupervisor.RestoreCapabilityProbeContract(capability.Record())
	restoredQualification, qualificationErr := dockersupervisor.RestoreEngineQualification(qualification.Record())
	if contractErr != nil || qualificationErr != nil || restoredContract.Digest() != capability.Digest() ||
		restoredQualification.Digest() != qualification.Digest() ||
		restoredContract.ProbeImageID() != independent.LocalDockerConfigImageID ||
		restoredQualification.ContractDigest() != restoredContract.Digest() ||
		restoredQualification.ProbeImageID() != restoredContract.ProbeImageID() ||
		restoredQualification.SandboxPolicyDigest() != restoredContract.SandboxPolicyDigest() {
		return nil, errors.New("installed Verifier qualification does not bind the exact aliased image and capability contract")
	}
	authority := &installedVerifierDockerAuthority{
		runner: dockersupervisor.RunnerForOperationPhase(runner, dockersupervisor.OperationPhaseVerifier), capability: restoredContract, qualification: restoredQualification,
		imageID: independent.LocalDockerConfigImageID,
	}
	if err := authority.Verify(ctx); err != nil {
		return nil, err
	}
	return authority, nil
}

func exactActivatedArtifactAlias(candidate, managed releaseassets.ActivatedImage) bool {
	return candidate.ArtifactID == managed.ArtifactID && candidate.LocalDockerConfigImageID == managed.LocalDockerConfigImageID &&
		candidate.ArchiveFormat == managed.ArchiveFormat && candidate.ArchivePath == managed.ArchivePath &&
		candidate.ArchiveSize == managed.ArchiveSize && candidate.ArchiveSHA256 == managed.ArchiveSHA256 &&
		candidate.ReleaseID == managed.ReleaseID &&
		candidate.SpecSHA256 == managed.SpecSHA256 && candidate.Platform == managed.Platform &&
		candidate.RuntimePiName == managed.RuntimePiName && candidate.RuntimePiVersion == managed.RuntimePiVersion &&
		candidate.RuntimePiNPMIntegrity == managed.RuntimePiNPMIntegrity && candidate.RuntimeNodeVersion == managed.RuntimeNodeVersion &&
		candidate.RuntimeNodeExecutable == managed.RuntimeNodeExecutable && candidate.RuntimeNodeVersionOutput == managed.RuntimeNodeVersionOutput &&
		candidate.EntrypointJSON == managed.EntrypointJSON
}

func verifyInstalledVerifierRolePolicy(executionPolicyPath string, policyBytes, rolePolicyBytes []byte, policy verifier.Policy, image releaseassets.ActivatedImage) error {
	if !safeInstalledRelativePath(executionPolicyPath) || !safeInstalledRelativePath(image.PolicyPath) {
		return errors.New("installed Verifier policy provenance is outside the release root")
	}
	roleDigest := sha256.Sum256(rolePolicyBytes)
	if hex.EncodeToString(roleDigest[:]) != image.PolicySHA256 || sha256.Sum256(policyBytes) != policy.Digest() {
		return errors.New("installed Verifier policy provenance digest mismatch")
	}
	var rolePolicy installedVerifierRolePolicy
	decoder := json.NewDecoder(bytes.NewReader(rolePolicyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&rolePolicy); err != nil {
		return errors.New("installed Verifier role policy is malformed")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("installed Verifier role policy is malformed")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(rolePolicyBytes, &fields); err != nil || len(fields) != 8 {
		return errors.New("installed Verifier role policy is incomplete")
	}
	for _, name := range []string{"schema_version", "execution_policy", "agent_runtime_invoked", "network", "credentials", "workspace", "image_alias_role", "fail_closed"} {
		if _, ok := fields[name]; !ok {
			return errors.New("installed Verifier role policy is incomplete")
		}
	}
	if rolePolicy.SchemaVersion != "chora.independent-verifier-role-policy.v1" || rolePolicy.ExecutionPolicy != executionPolicyPath ||
		rolePolicy.AgentRuntimeInvoked || rolePolicy.Network != "none" || rolePolicy.Credentials != "none" ||
		rolePolicy.Workspace != "fresh_per_verification_attempt" || rolePolicy.ImageAliasRole != releaseassets.RoleManagedPiRuntime || !rolePolicy.FailClosed {
		return errors.New("installed Verifier role policy does not bind the required independent boundary")
	}
	return nil
}

func safeInstalledRelativePath(value string) bool {
	clean := filepath.Clean(filepath.FromSlash(value))
	return value != "" && filepath.ToSlash(clean) == value && !filepath.IsAbs(clean) && clean != "." && clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

func (authority *installedVerifierDockerAuthority) VerifierImageID() string {
	if authority == nil {
		return ""
	}
	return authority.imageID
}

func (authority *installedVerifierDockerAuthority) Verify(ctx context.Context) error {
	if authority == nil || !dockerCommandRunnerAvailable(authority.runner) {
		return errors.New("installed Verifier Docker authority is unavailable")
	}
	contract, contractErr := dockersupervisor.RestoreCapabilityProbeContract(authority.capability.Record())
	qualification, qualificationErr := dockersupervisor.RestoreEngineQualification(authority.qualification.Record())
	if contractErr != nil || qualificationErr != nil || contract.Digest() != authority.capability.Digest() ||
		qualification.Digest() != authority.qualification.Digest() || contract.ProbeImageID() != authority.imageID {
		return errors.New("installed Verifier Docker authority drifted")
	}
	observed, err := dockersupervisor.ObserveEngine(ctx, authority.runner)
	if err != nil || !qualification.ValidFor(observed, contract) {
		return errors.New("installed Verifier Engine identity does not match its qualification")
	}
	image, err := authority.runner.Run(ctx, dockersupervisor.Command{Args: []string{"image", "inspect", "--format", "{{.Id}}", authority.imageID}})
	if err != nil || image.ExitCode != 0 || strings.TrimSpace(string(image.Stdout)) != authority.imageID {
		return errors.New("installed Verifier image identity does not match the activated release")
	}
	return nil
}

func (authority *installedVerifierDockerAuthority) Run(ctx context.Context, request verifier.DockerCommand) (verifier.DockerCommandResult, error) {
	if authority == nil || !dockerCommandRunnerAvailable(authority.runner) {
		return verifier.DockerCommandResult{}, errors.New("installed Verifier Docker authority is unavailable")
	}
	if request.Stdout != nil || request.Stderr != nil {
		process, err := authority.runner.Start(ctx, dockersupervisor.Command{
			Args: slices.Clone(request.Args), Stdout: request.Stdout, Stderr: request.Stderr,
		})
		if err != nil {
			return verifier.DockerCommandResult{}, err
		}
		closeErr := process.Close()
		exitCode, waitErr := process.Wait()
		if waitErr != nil && exitCode > 0 {
			waitErr = nil
		}
		return verifier.DockerCommandResult{ExitCode: exitCode}, errors.Join(closeErr, waitErr)
	}
	result, runErr := authority.runner.Run(ctx, dockersupervisor.Command{Args: slices.Clone(request.Args)})
	if runErr != nil && result.ExitCode > 0 {
		runErr = nil
	}
	var writeErr error
	if request.Stdout != nil && len(result.Stdout) > 0 {
		_, writeErr = request.Stdout.Write(result.Stdout)
	}
	if request.Stderr != nil && len(result.Stderr) > 0 {
		_, err := request.Stderr.Write(result.Stderr)
		writeErr = errors.Join(writeErr, err)
	}
	return verifier.DockerCommandResult{ExitCode: result.ExitCode, Stdout: slices.Clone(result.Stdout), Stderr: slices.Clone(result.Stderr)}, errors.Join(runErr, writeErr)
}

func dockerCommandRunnerAvailable(runner dockersupervisor.CommandRunner) bool {
	if runner == nil {
		return false
	}
	value := reflect.ValueOf(runner)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return !value.IsNil()
	default:
		return true
	}
}

type verificationStartupRecovery interface {
	Recover(context.Context) error
}

type verificationStartupCandidateLister func(context.Context) ([]storecontract.VerificationStartupCandidate, error)

func recoverInstalledVerifierStartup(ctx context.Context, recovery verificationStartupRecovery, list verificationStartupCandidateLister) (int, error) {
	if recovery == nil || list == nil {
		return 0, errors.New("installed Verifier recovery dependencies unavailable")
	}
	// Exact-scope Docker and workspace recovery is deliberately first and
	// unconditional: a crash can leave a labeled container before any DB row is
	// committed, so persisted candidates are not discovery authority.
	if err := recovery.Recover(ctx); err != nil {
		return 0, err
	}
	candidates, err := list(ctx)
	if err != nil {
		return 0, err
	}
	return len(candidates), nil
}

func prepareProductBaseline(ctx context.Context, repoRoot, runtimeRoot string, bundled bool) (string, string, error) {
	if !bundled {
		outerRoot := filepath.Clean(filepath.Join(repoRoot, ".."))
		baselineRoot := filepath.Join(outerRoot, ".agent", "evidence", "G2-M3", "source-baseline-v4", "repo")
		return baselineRoot, filepath.Join(filepath.Dir(baselineRoot), "manifest.json"), nil
	}
	fixtureRoot := filepath.Join(repoRoot, "contracts", "g2-m4", "source-baseline-v6")
	manifestPath := filepath.Join(fixtureRoot, "manifest.json")
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		return "", "", fmt.Errorf("read bundled frozen Baseline manifest: %w", err)
	}
	parent := filepath.Join(runtimeRoot, "verification")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", "", fmt.Errorf("create frozen Baseline parent: %w", err)
	}
	if err := os.Chmod(parent, 0o700); err != nil {
		return "", "", fmt.Errorf("secure frozen Baseline parent: %w", err)
	}
	target := filepath.Join(parent, "baseline-v6")
	if _, err := os.Lstat(target); errors.Is(err, os.ErrNotExist) {
		if _, err := baselinebundle.Reconstruct(ctx, baselinebundle.Request{
			InstalledRoot: repoRoot, DeltaRoot: filepath.Join(fixtureRoot, "delta"), TargetRoot: target, Manifest: manifest,
		}); err != nil {
			return "", "", fmt.Errorf("reconstruct bundled frozen Baseline: %w", err)
		}
	} else if err != nil {
		return "", "", fmt.Errorf("inspect bundled frozen Baseline: %w", err)
	} else if _, err := baselinebundle.Verify(target, manifest); err != nil {
		return "", "", fmt.Errorf("verify existing bundled frozen Baseline: %w", err)
	}
	return target, manifestPath, nil
}

func (service *productVerifier) Recover(ctx context.Context) error {
	if service == nil || service.workspaceFactory == nil || service.sandboxLifecycle == nil {
		return errors.New("Verifier recovery dependencies unavailable")
	}
	if err := service.sandboxLifecycle.Recover(ctx); err != nil {
		return fmt.Errorf("recover labeled Verifier containers: %w", err)
	}
	if err := service.workspaceFactory.Recover(ctx); err != nil {
		return fmt.Errorf("recover Verification Workspaces: %w", err)
	}
	return nil
}

func (service *productVerifier) Authority() app.VerificationAuthority { return service.authority }

func (service *productVerifier) VerifyBaselineIdentity() error {
	if service == nil {
		return errors.New("Verifier Baseline is unavailable")
	}
	manifest, err := os.ReadFile(service.baselineManifest)
	if err != nil {
		return fmt.Errorf("read frozen Baseline manifest: %w", err)
	}
	result, err := baselinebundle.Verify(service.baselineRoot, manifest)
	if err != nil {
		return err
	}
	if result.AggregateSHA256 != verifier.M1FrozenBaselineDigestHex {
		return errors.New("frozen Baseline aggregate mismatch")
	}
	return nil
}

func (service *productVerifier) Verify(ctx context.Context, request app.VerificationExecutionRequest) (verifier.AttemptEvidence, error) {
	patchPath, err := service.resolvePatch(request.PatchLocator)
	if err != nil {
		return verifier.AttemptEvidence{}, err
	}
	patch, err := os.ReadFile(patchPath)
	if err != nil {
		return verifier.AttemptEvidence{}, fmt.Errorf("read final Patch: %w", err)
	}
	manifest, err := os.ReadFile(service.baselineManifest)
	if err != nil {
		return verifier.AttemptEvidence{}, fmt.Errorf("read frozen Baseline manifest: %w", err)
	}
	return service.core.Verify(ctx, verifier.VerifyRequest{
		Contract: request.Contract, Bindings: request.Bindings, RunID: request.RunID.String(),
		VerificationRunID: request.VerificationRunID.String(), AttemptID: request.AttemptID.String(), BaselineRoot: service.baselineRoot,
		BaselineManifest: manifest, AgentWorkspace: filepath.Dir(patchPath), Patch: patch,
	})
}

func (service *productVerifier) ReadReviewPatch(ctx context.Context, locator string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := service.resolvePatch(locator)
	if err != nil {
		return nil, err
	}
	patch, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read review Patch: %w", err)
	}
	return patch, nil
}

func (service *productVerifier) resolvePatch(locator string) (string, error) {
	if service == nil || strings.TrimSpace(locator) == "" {
		return "", errors.New("final Patch locator is empty")
	}
	clean := filepath.Clean(filepath.FromSlash(locator))
	if filepath.IsAbs(clean) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("final Patch locator escapes the artifact root")
	}
	path := filepath.Join(service.artifactRoot, clean)
	relative, err := filepath.Rel(service.artifactRoot, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("final Patch locator escapes the artifact root")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > service.patchLimit {
		return "", errors.New("final Patch artifact is unavailable or outside limits")
	}
	return path, nil
}

func decodeVerifierDigest(value string) ([32]byte, error) {
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size {
		return [32]byte{}, errors.New("invalid verifier SHA-256")
	}
	var digest [32]byte
	copy(digest[:], decoded)
	return digest, nil
}

type productLogRedactor struct{}

var productLogSecrets = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(authorization:\s*bearer\s+)[^\s]+`),
	regexp.MustCompile(`(?i)((?:token|password|secret|api[_-]?key)\s*[=:]\s*)[^\s]+`),
}

func (productLogRedactor) Version() string { return "chora.verifier-redaction.v1" }

func (productLogRedactor) Redact(body []byte) ([]byte, error) {
	redacted := append([]byte(nil), body...)
	for _, pattern := range productLogSecrets {
		redacted = pattern.ReplaceAll(redacted, []byte("${1}[REDACTED]"))
	}
	return redacted, nil
}

var _ app.VerificationExecutor = (*productVerifier)(nil)
var _ app.ReviewPatchSource = (*productVerifier)(nil)
