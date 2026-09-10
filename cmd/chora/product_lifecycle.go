package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	agentpi "github.com/Yangyang96/chora/internal/agent/pi"
	"github.com/Yangyang96/chora/internal/dockersupervisor"
	"github.com/Yangyang96/chora/internal/pidistribution"
	"github.com/Yangyang96/chora/internal/productinstall"
	"github.com/Yangyang96/chora/internal/releaseassets"
)

const productUsage = "usage: chora setup [explicit options] | chora product doctor|setup|upgrade|gc|uninstall --docker-cli ABSOLUTE_PATH --docker-context NAME --endpoint-digest SHA256 [action-specific explicit options] [--json]"

var productIdentifier = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

type productCommand struct {
	action productinstall.Action
	target productinstall.EngineTarget
	json   bool

	stateRoot               string
	key                     string
	authority               productinstall.MutationAuthority
	releaseRoot             string
	releaseManifest         string
	manifestSHA256          string
	releaseID               string
	generationID            string
	probeRuntime            string
	privatePiRoot           string
	privatePiSource         string
	privatePiManifest       string
	privatePiManifestSHA256 string
	colima                  colimaBootstrapRequest
	limit                   int
	generationIDs           []string
}

type productCommandResult struct {
	SchemaVersion        string   `json:"schema_version"`
	Status               string   `json:"status"`
	Action               string   `json:"action"`
	Reason               string   `json:"reason,omitempty"`
	ContextName          string   `json:"context_name,omitempty"`
	EndpointDigest       string   `json:"endpoint_digest,omitempty"`
	DaemonID             string   `json:"daemon_id,omitempty"`
	APIVersion           string   `json:"api_version,omitempty"`
	OperatingSystem      string   `json:"operating_system,omitempty"`
	Architecture         string   `json:"architecture,omitempty"`
	GenerationID         string   `json:"generation_id,omitempty"`
	Active               bool     `json:"active,omitempty"`
	Replayed             bool     `json:"replayed,omitempty"`
	RemovedAssetIDs      []string `json:"removed_asset_ids,omitempty"`
	SkippedGenerationIDs []string `json:"skipped_generation_ids,omitempty"`
}

type productExecutor interface {
	Execute(context.Context, productCommand) (productCommandResult, error)
}

func runProduct(args []string, stdout, stderr io.Writer, executor productExecutor) int {
	command, err := parseProductCommand(args)
	if err != nil || executor == nil {
		fmt.Fprintln(stderr, productUsage)
		return 2
	}
	result, err := executor.Execute(context.Background(), command)
	if err != nil {
		result = productCommandResult{
			SchemaVersion: "chora.product-command-result/v1", Status: "failed",
			Action: string(command.action), Reason: productErrorCode(err),
		}
	}
	if command.json {
		if encodeErr := json.NewEncoder(stdout).Encode(result); encodeErr != nil {
			fmt.Fprintln(stderr, "product command output failed")
			return 2
		}
	} else if result.Status == "passed" {
		fmt.Fprintf(stdout, "PASS product %s", result.Action)
		if result.GenerationID != "" {
			fmt.Fprintf(stdout, " generation=%s", result.GenerationID)
		}
		if result.ContextName != "" {
			fmt.Fprintf(stdout, " context=%s api=%s os=%s arch=%s", result.ContextName, result.APIVersion, result.OperatingSystem, result.Architecture)
		}
		if result.Replayed {
			fmt.Fprint(stdout, " replayed=true")
		}
		fmt.Fprintln(stdout)
	} else {
		fmt.Fprintf(stdout, "FAIL product %s reason=%s\n", result.Action, result.Reason)
	}
	if err != nil || result.Status != "passed" {
		return 1
	}
	return 0
}

func parseProductCommand(args []string) (productCommand, error) {
	if len(args) == 0 {
		return productCommand{}, productinstall.ErrInvalidRequest
	}
	actionName := args[0]
	action := productinstall.Action(actionName)
	if actionName == "gc" {
		action = productinstall.ActionGarbageGC
	}
	if actionName == "doctor" {
		action = "doctor"
	}
	if action != "doctor" && action != productinstall.ActionSetup && action != productinstall.ActionUpgrade &&
		action != productinstall.ActionGarbageGC && action != productinstall.ActionUninstall {
		return productCommand{}, productinstall.ErrInvalidRequest
	}
	flags := flag.NewFlagSet("product "+actionName, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dockerCLI := flags.String("docker-cli", "", "exact Docker CLI")
	dockerContext := flags.String("docker-context", "", "explicit Docker context")
	endpointDigest := flags.String("endpoint-digest", "", "explicit endpoint digest")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	stateRoot := flags.String("state-root", "", "owner-private installation state root")
	key := flags.String("key", "", "idempotency key")
	grant := flags.String("grant", "", "authorization grant")
	actor := flags.String("actor", "", "local actor")
	session := flags.String("session", "", "local session")
	authorizedAction := flags.String("authorize", "", "explicit authorized action")
	releaseRoot := flags.String("release-root", "", "installed release root")
	releaseManifest := flags.String("release-manifest", "", "root-relative release manifest")
	manifestSHA := flags.String("manifest-sha256", "", "release manifest digest")
	releaseID := flags.String("release-id", "", "release identity")
	generationID := flags.String("generation", "", "generation identity")
	probeRuntime := flags.String("probe-runtime-root", "", "capability Probe runtime root")
	privatePiRoot := flags.String("private-pi-root", "", "owner-private Pi installation root")
	privatePiSource := flags.String("private-pi-source", "", "authenticated private Pi source root")
	privatePiManifest := flags.String("private-pi-manifest", "", "authenticated private Pi manifest")
	privatePiManifestSHA := flags.String("private-pi-manifest-sha256", "", "private Pi manifest digest")
	bootstrapColima := flags.Bool("bootstrap-colima", false, "offer exact pinned Colima bootstrap when the explicit Engine is unavailable")
	colimaToolRoot := flags.String("colima-tool-root", "", "owner-private exact Colima client root")
	colimaProfile := flags.String("colima-profile", "", "explicit Colima VM profile")
	colimaDownloadAuthorization := flags.String("authorize-colima-download", "", "explicit pinned Colima client installation authorization")
	colimaVMAuthorization := flags.String("authorize-colima-vm", "", "explicit exact Colima VM allocation authorization")
	colimaSource := flags.String("colima-source", "", "authenticated exact Colima executable source")
	dockerClientSource := flags.String("docker-client-source", "", "authenticated exact Docker client executable source")
	limit := flags.Int("limit", 0, "bounded GC asset limit")
	generations := flags.String("generations", "", "comma-separated generation identities")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
		return productCommand{}, productinstall.ErrInvalidRequest
	}
	command := productCommand{
		action: action, json: *jsonOutput,
		target:    productinstall.EngineTarget{CLIPath: *dockerCLI, ContextName: *dockerContext, EndpointDigest: *endpointDigest},
		stateRoot: *stateRoot, key: *key, releaseRoot: *releaseRoot, releaseManifest: *releaseManifest,
		manifestSHA256: *manifestSHA, releaseID: *releaseID, generationID: *generationID,
		probeRuntime: *probeRuntime, privatePiRoot: *privatePiRoot, privatePiSource: *privatePiSource,
		privatePiManifest: *privatePiManifest, privatePiManifestSHA256: *privatePiManifestSHA, limit: *limit,
		colima: colimaBootstrapRequest{Enabled: *bootstrapColima, ToolRoot: *colimaToolRoot, Profile: *colimaProfile,
			DownloadAuthorization: *colimaDownloadAuthorization, VMAuthorization: *colimaVMAuthorization,
			ColimaSource: *colimaSource, DockerClientSource: *dockerClientSource},
	}
	if !canonicalAbsolute(*dockerCLI) || !productIdentifier.MatchString(*dockerContext) || !validFingerprint(*endpointDigest) {
		return productCommand{}, productinstall.ErrInvalidRequest
	}
	if action == "doctor" {
		return command, nil
	}
	if !canonicalAbsolute(*stateRoot) || !productIdentifier.MatchString(*key) || !productIdentifier.MatchString(*grant) ||
		!productIdentifier.MatchString(*actor) || !productIdentifier.MatchString(*session) || *authorizedAction != string(action) {
		return productCommand{}, productinstall.ErrAuthorityRequired
	}
	command.authority = productinstall.MutationAuthority{
		GrantID: *grant, ActorID: *actor, SessionID: *session, Action: action, IssuedAt: time.Now().UTC(),
	}
	if !canonicalAbsolute(*privatePiRoot) || !canonicalAbsolute(*privatePiSource) || !canonicalAbsolute(*privatePiManifest) || !validFingerprint(*privatePiManifestSHA) {
		return productCommand{}, productinstall.ErrInvalidRequest
	}
	if action == productinstall.ActionSetup || action == productinstall.ActionUpgrade {
		if !canonicalAbsolute(*releaseRoot) || *releaseManifest == "" || filepath.IsAbs(*releaseManifest) ||
			filepath.Clean(*releaseManifest) != *releaseManifest || !validFingerprint(*manifestSHA) ||
			!productIdentifier.MatchString(*releaseID) || !productIdentifier.MatchString(*generationID) || !canonicalAbsolute(*probeRuntime) {
			return productCommand{}, productinstall.ErrInvalidRequest
		}
		if command.colima.Enabled && (!canonicalAbsolute(command.colima.ToolRoot) || !productIdentifier.MatchString(command.colima.Profile) ||
			!canonicalAbsolute(command.colima.ColimaSource) || !canonicalAbsolute(command.colima.DockerClientSource) ||
			command.target.CLIPath != filepath.Join(command.colima.ToolRoot, "docker-"+dockerClientVersion)) {
			return productCommand{}, productinstall.ErrInvalidRequest
		}
	}
	if command.colima.Enabled && action != productinstall.ActionSetup {
		return productCommand{}, productinstall.ErrInvalidRequest
	}
	if action == productinstall.ActionGarbageGC && (*limit < 1 || *limit > 64) {
		return productCommand{}, productinstall.ErrInvalidRequest
	}
	if action == productinstall.ActionUninstall && *generations != "" {
		command.generationIDs = strings.Split(*generations, ",")
		slices.Sort(command.generationIDs)
		command.generationIDs = slices.Compact(command.generationIDs)
		for _, id := range command.generationIDs {
			if !productIdentifier.MatchString(id) {
				return productCommand{}, productinstall.ErrInvalidRequest
			}
		}
	}
	return command, nil
}

type productionProductExecutor struct {
	colimaPorts colimaBootstrapPorts
}

func (executor productionProductExecutor) Execute(ctx context.Context, command productCommand) (finalResult productCommandResult, finalErr error) {
	var bound productinstall.BoundRelease
	var backend *productinstall.FileBackend
	var piResolver *pidistribution.Resolver
	var piBinding pidistribution.PrivateBinding
	if command.action == productinstall.ActionSetup || command.action == productinstall.ActionUpgrade {
		release, err := releaseassets.LoadActivatedRelease(releaseassets.ActivationInput{
			Root: command.releaseRoot, ManifestPath: command.releaseManifest,
			ManifestSHA256: command.manifestSHA256, ReleaseID: command.releaseID,
		})
		if err != nil {
			// This happens before Runner construction: missing or invalid local
			// offline release evidence can never lead to image load or Probe.
			return productCommandResult{}, productinstall.ErrInvalidRequest
		}
		bound, err = productinstall.BindActivatedRelease(release, command.generationID, command.manifestSHA256, agentpi.PolicySHA256)
		if err != nil {
			return productCommandResult{}, err
		}
		piResolver, piBinding, err = loadPrivatePiLifecycle(command)
		if err != nil {
			return productCommandResult{}, err
		}
		if _, pathErr := piResolver.InspectCompatiblePATH(ctx); pathErr != nil {
			bound, err = productinstall.BindPrivatePi(bound, piBinding)
			if err != nil {
				return productCommandResult{}, err
			}
		}
	} else if command.action != "doctor" {
		var err error
		piResolver, piBinding, err = loadPrivatePiLifecycle(command)
		if err != nil {
			return productCommandResult{}, err
		}
	}
	if command.action != "doctor" {
		var err error
		backend, err = productinstall.NewFileBackend(command.stateRoot)
		if err != nil {
			return productCommandResult{}, err
		}
		if command.action == productinstall.ActionGarbageGC || command.action == productinstall.ActionUninstall {
			if err := backend.RequireReferenceSnapshot(ctx); err != nil {
				return productCommandResult{}, err
			}
		}
	}
	ports := executor.colimaPorts
	if ports == nil {
		ports = productionColimaPorts{}
	}
	bootstrapped := false
	runner, cliPath, err := exactDockerRunner(command.target)
	if err != nil && command.action == productinstall.ActionSetup && command.colima.Enabled {
		if bootstrapErr := bootstrapColima(ctx, false, command.colima, ports); bootstrapErr != nil {
			return productCommandResult{}, bootstrapErr
		}
		bootstrapped = true
		runner, cliPath, err = exactDockerRunner(command.target)
	}
	if err != nil {
		return productCommandResult{}, err
	}
	command.target.CLIPath = cliPath
	var operationLedger *dockersupervisor.OperationLedger
	if command.action == productinstall.ActionSetup || command.action == productinstall.ActionUpgrade {
		operationLedger, err = dockersupervisor.OpenOperationLedger(runner, command.stateRoot, command.generationID)
		if err != nil {
			return productCommandResult{}, err
		}
		defer func() {
			finalErr = joinProductLedgerClose(finalErr, operationLedger)
		}()
		runner = dockersupervisor.RunnerForOperationPhase(operationLedger, dockersupervisor.OperationPhaseSetup)
	}
	dockerAssets, err := productinstall.NewExactDockerAssets(runner, command.target)
	if err != nil {
		return productCommandResult{}, err
	}
	if command.action == "doctor" {
		report, err := productinstall.ReadOnlyEngineDoctor(ctx, productinstall.SystemClock{}, dockerAssets, command.target)
		result := productCommandResult{
			SchemaVersion: "chora.product-command-result/v1", Action: "doctor", Status: "failed",
			Reason: report.Reason, ContextName: report.Engine.ContextName,
			EndpointDigest: report.Engine.EndpointDigest, DaemonID: report.Engine.DaemonID,
			APIVersion: report.Engine.APIVersion, OperatingSystem: report.Engine.OperatingSystem,
			Architecture: report.Engine.Architecture,
		}
		if report.Ready {
			result.Status = "passed"
			result.Reason = ""
		}
		return result, err
	}
	if command.action == productinstall.ActionUpgrade || command.action == productinstall.ActionGarbageGC || command.action == productinstall.ActionUninstall {
		if err := requirePersistedEngineMatchForCommand(ctx, backend, command, dockerAssets); err != nil {
			return productCommandResult{}, err
		}
	}
	if command.action == productinstall.ActionSetup {
		_, observationErr := dockerAssets.Observe(ctx, command.target)
		if observationErr != nil {
			if !bootstrapped {
				if err := bootstrapColima(ctx, false, command.colima, ports); err != nil {
					return productCommandResult{}, err
				}
			}
			if _, err := dockerAssets.Observe(ctx, command.target); err != nil {
				return productCommandResult{}, errors.New("explicit Engine unavailable after authorized bootstrap")
			}
		}
	}
	privatePiAssets, err := productinstall.NewExactPrivatePiAssets(command.target, piResolver, piBinding)
	if err != nil {
		return productCommandResult{}, err
	}
	productAssets, err := productinstall.NewExactProductAssets(dockerAssets, privatePiAssets)
	if err != nil {
		return productCommandResult{}, err
	}
	var prober productinstall.CapabilityProber = failClosedProductProber{}
	if command.action == productinstall.ActionSetup || command.action == productinstall.ActionUpgrade {
		prober, err = productinstall.NewDockerCapabilityProber(ctx, runner, command.target, command.probeRuntime, bound.ProbeImageID, bound.SandboxPolicyDigest)
		if err != nil {
			return productCommandResult{}, err
		}
	}
	service, err := productinstall.New(productinstall.Dependencies{
		Clock: productinstall.SystemClock{}, Locker: backend, Observer: dockerAssets,
		Authorizer: productinstall.ExplicitLocalAuthorizer{GrantID: command.authority.GrantID, ActorID: command.authority.ActorID, SessionID: command.authority.SessionID},
		Store:      backend, Inspector: productAssets, Acquirer: productAssets, Remover: productAssets,
		Prober: prober, Verifier: productAssets, Activator: backend, References: backend,
	})
	if err != nil {
		return productCommandResult{}, err
	}
	result := productCommandResult{SchemaVersion: "chora.product-command-result/v1", Status: "passed", Action: string(command.action)}
	switch command.action {
	case productinstall.ActionSetup, productinstall.ActionUpgrade:
		request := productinstall.InstallRequest{
			Authority: command.authority, Key: command.key, Target: command.target, Generation: bound.Generation,
		}
		var installed productinstall.InstallResult
		if command.action == productinstall.ActionSetup {
			installed, err = service.Setup(ctx, request)
		} else {
			installed, err = service.Upgrade(ctx, request)
		}
		result.GenerationID, result.Active, result.Replayed = installed.GenerationID, installed.Active, installed.Replayed
	case productinstall.ActionGarbageGC:
		var collected productinstall.GCResult
		collected, err = service.GarbageCollect(ctx, productinstall.GCRequest{
			Authority: command.authority, Key: command.key, Target: command.target, Limit: command.limit,
		})
		result.RemovedAssetIDs = collected.RemovedAssetIDs
		result.SkippedGenerationIDs = collected.SkippedGenerationIDs
		result.Replayed = collected.Replayed
	case productinstall.ActionUninstall:
		var uninstalled productinstall.UninstallResult
		uninstalled, err = service.Uninstall(ctx, productinstall.UninstallRequest{
			Authority: command.authority, Key: command.key, Target: command.target, GenerationIDs: command.generationIDs,
		})
		result.RemovedAssetIDs, result.Replayed = uninstalled.RemovedAssetIDs, uninstalled.Replayed
		if err == nil && len(command.generationIDs) == 0 {
			present := false
			if _, observed, inspectErr := piResolver.InspectPrivate(ctx); inspectErr != nil {
				err = inspectErr
			} else {
				present = observed
			}
			pending, pendingErr := piResolver.RemovalPending(ctx)
			if pendingErr != nil {
				err = pendingErr
			}
			if err == nil && (present || pending) {
				if removeErr := piResolver.RemovePrivate(ctx); removeErr != nil {
					if errors.Is(removeErr, pidistribution.ErrRemovalInterrupted) {
						err = productinstall.ErrInterrupted
					} else {
						err = removeErr
					}
				} else {
					result.RemovedAssetIDs = append(result.RemovedAssetIDs, "private_pi_fallback")
				}
			}
		}
	default:
		return productCommandResult{}, productinstall.ErrInvalidRequest
	}
	return result, err
}

func joinProductLedgerClose(primary error, closer interface{ Close() error }) error {
	if closer == nil {
		return primary
	}
	return errors.Join(primary, closer.Close())
}

func requirePersistedEngineMatch(ctx context.Context, backend *productinstall.FileBackend, target productinstall.EngineTarget, observer *productinstall.ExactDockerAssets) error {
	active, err := loadActiveEngineEvidence(ctx, backend, target)
	if err != nil {
		return err
	}
	return requireObservedEngineMatch(ctx, active, target, observer)
}

func requirePersistedEngineMatchForCommand(ctx context.Context, backend *productinstall.FileBackend, command productCommand, observer *productinstall.ExactDockerAssets) error {
	if backend == nil || observer == nil {
		return productinstall.ErrInvalidRequest
	}
	state, err := backend.Load(ctx)
	if err != nil {
		return errors.New("active installed Engine evidence is unavailable")
	}
	if state.ActiveGenerationID != "" {
		active, activeErr := generationEngineEvidence(state, state.ActiveGenerationID, command.target)
		if activeErr != nil {
			return activeErr
		}
		return requireObservedEngineMatch(ctx, active, command.target, observer)
	}
	prior, replayErr := uninstallReplayEngineEvidence(state, command)
	if replayErr != nil {
		return replayErr
	}
	return requireObservedEngineMatch(ctx, prior, command.target, observer)
}

func loadActiveEngineEvidence(ctx context.Context, backend *productinstall.FileBackend, target productinstall.EngineTarget) (productinstall.Generation, error) {
	if backend == nil {
		return productinstall.Generation{}, productinstall.ErrInvalidRequest
	}
	state, err := backend.Load(ctx)
	if err != nil || state.ActiveGenerationID == "" {
		return productinstall.Generation{}, errors.New("active installed Engine evidence is unavailable")
	}
	return generationEngineEvidence(state, state.ActiveGenerationID, target)
}

func uninstallReplayEngineEvidence(state productinstall.State, command productCommand) (productinstall.Generation, error) {
	if command.action != productinstall.ActionUninstall || command.key == "" {
		return productinstall.Generation{}, errors.New("active installed Engine evidence is unavailable")
	}
	requestedIDs := slices.Clone(command.generationIDs)
	slices.Sort(requestedIDs)
	requestedIDs = slices.Compact(requestedIDs)
	digest, err := uninstallRequestDigest(command.target, requestedIDs)
	if err != nil {
		return productinstall.Generation{}, errors.New("active installed Engine evidence is unavailable")
	}
	for _, operation := range state.Operations {
		if operation.Key != command.key {
			continue
		}
		if operation.Action != productinstall.ActionUninstall || operation.RequestDigest != digest || operation.Target != command.target ||
			(operation.Phase != productinstall.PhaseRemoving && operation.Phase != productinstall.PhaseComplete) ||
			operation.PriorActiveGeneration == "" || !slices.Contains(operation.TargetGenerationIDs, operation.PriorActiveGeneration) ||
			len(requestedIDs) > 0 && !slices.Equal(requestedIDs, operation.TargetGenerationIDs) ||
			!journalTargetGenerationsExist(state, operation.TargetGenerationIDs) {
			return productinstall.Generation{}, errors.New("journaled installed Engine evidence does not match uninstall replay")
		}
		return generationEngineEvidence(state, operation.PriorActiveGeneration, operation.Target)
	}
	return productinstall.Generation{}, errors.New("active installed Engine evidence is unavailable")
}

func uninstallRequestDigest(target productinstall.EngineTarget, generationIDs []string) (string, error) {
	encoded, err := json.Marshal(struct {
		Action        productinstall.Action
		Target        productinstall.EngineTarget
		GenerationIDs []string
	}{productinstall.ActionUninstall, target, generationIDs})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func journalTargetGenerationsExist(state productinstall.State, targetIDs []string) bool {
	for _, targetID := range targetIDs {
		found := false
		for _, generation := range state.Generations {
			if generation.Spec.GenerationID == targetID {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func generationEngineEvidence(state productinstall.State, generationID string, target productinstall.EngineTarget) (productinstall.Generation, error) {
	for _, generation := range state.Generations {
		if generation.Spec.GenerationID != generationID {
			continue
		}
		if generation.Target != target || generation.Probe.QualificationDigest != generation.QualificationDigest {
			return productinstall.Generation{}, errors.New("active installed Engine evidence does not match target")
		}
		return generation, nil
	}
	return productinstall.Generation{}, errors.New("active installed Engine evidence does not match target")
}

func requireObservedEngineMatch(ctx context.Context, evidence productinstall.Generation, target productinstall.EngineTarget, observer *productinstall.ExactDockerAssets) error {
	if observer == nil || evidence.Target != target || evidence.Probe.QualificationDigest != evidence.QualificationDigest {
		return errors.New("active installed Engine evidence does not match target")
	}
	observed, err := observer.Observe(ctx, target)
	if err != nil || observed != evidence.Probe.Engine {
		return errors.New("active installed Engine identity changed")
	}
	return nil
}

func loadPrivatePiLifecycle(command productCommand) (*pidistribution.Resolver, pidistribution.PrivateBinding, error) {
	manifest, expected, err := readPinnedPrivateManifest(command.privatePiManifest, command.privatePiManifestSHA256)
	if err != nil {
		return nil, pidistribution.PrivateBinding{}, productinstall.ErrInvalidRequest
	}
	asset := &pidistribution.PrivateAsset{
		ManifestBytes: manifest, ExpectedManifestSHA256: expected, SourceRoot: command.privatePiSource,
	}
	binding, err := pidistribution.BindPrivateAsset(asset, "", "")
	if err != nil {
		return nil, pidistribution.PrivateBinding{}, productinstall.ErrInvalidRequest
	}
	resolver, err := pidistribution.NewResolver(pidistribution.Config{PrivateRoot: command.privatePiRoot, PrivateAsset: asset})
	if err != nil {
		return nil, pidistribution.PrivateBinding{}, productinstall.ErrInvalidRequest
	}
	return resolver, binding, nil
}

func readPinnedPrivateManifest(path, expectedText string) ([]byte, [sha256.Size]byte, error) {
	var expected [sha256.Size]byte
	if !canonicalAbsolute(path) || !validFingerprint(expectedText) {
		return nil, expected, productinstall.ErrInvalidRequest
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() < 1 || info.Size() > 16<<20 {
		return nil, expected, productinstall.ErrInvalidRequest
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return nil, expected, productinstall.ErrInvalidRequest
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, expected, productinstall.ErrInvalidRequest
	}
	decoded, err := hex.DecodeString(expectedText)
	if err != nil || len(decoded) != sha256.Size {
		return nil, expected, productinstall.ErrInvalidRequest
	}
	copy(expected[:], decoded)
	if sha256.Sum256(data) != expected {
		return nil, expected, productinstall.ErrInvalidRequest
	}
	return data, expected, nil
}

type failClosedProductProber struct{}

func (failClosedProductProber) Probe(context.Context, productinstall.MutationAuthority, productinstall.EngineTarget, productinstall.GenerationSpec) (productinstall.ProbeResult, error) {
	return productinstall.ProbeResult{}, productinstall.ErrProbeFailed
}

func exactDockerRunner(target productinstall.EngineTarget) (dockersupervisor.CommandRunner, string, error) {
	runner, err := newExactDockerCLIRunner(target)
	if err != nil {
		return nil, "", errors.New("explicit Docker authority is unavailable")
	}
	return runner, target.CLIPath, nil
}

func productErrorCode(err error) string {
	switch {
	case errors.Is(err, productinstall.ErrAuthorityRequired):
		return "authority_required"
	case errors.Is(err, productinstall.ErrIdempotencyConflict):
		return "idempotency_conflict"
	case errors.Is(err, productinstall.ErrBusy):
		return "installation_busy"
	case errors.Is(err, productinstall.ErrProbeFailed):
		return "capability_probe_failed"
	case errors.Is(err, productinstall.ErrGenerationReferenced):
		return "generation_referenced"
	case errors.Is(err, productinstall.ErrAssetConflict):
		return "asset_identity_conflict"
	case errors.Is(err, productinstall.ErrInvalidRequest):
		return "invalid_request"
	case errors.Is(err, productinstall.ErrOperationFailed):
		return "operation_failed"
	default:
		return "product_lifecycle_unavailable"
	}
}

func canonicalAbsolute(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && path != filepath.VolumeName(path)+string(os.PathSeparator)
}
