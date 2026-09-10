package localweb

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/Yangyang96/chora/internal/acceptanceauthority"
	"github.com/Yangyang96/chora/internal/agent/codexcli"
	agentfake "github.com/Yangyang96/chora/internal/agent/fake"
	agentpi "github.com/Yangyang96/chora/internal/agent/pi"
	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/devsupervisor"
	"github.com/Yangyang96/chora/internal/dockersupervisor"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/gitsource"
	"github.com/Yangyang96/chora/internal/pidiscovery"
	"github.com/Yangyang96/chora/internal/pidistribution"
	"github.com/Yangyang96/chora/internal/piinstall"
	"github.com/Yangyang96/chora/internal/preflight"
	"github.com/Yangyang96/chora/internal/productinstall"
	"github.com/Yangyang96/chora/internal/releaseassets"
	"github.com/Yangyang96/chora/internal/speccoding"
	storecontract "github.com/Yangyang96/chora/internal/store"
	"github.com/Yangyang96/chora/internal/store/sqlite"
	"github.com/Yangyang96/chora/internal/taskdelivery"
)

const (
	localActor                = "local-human"
	localSession              = "local-browser"
	systemActor               = "chora-system"
	systemSession             = "intent-first-flow"
	maxReviewNoteLength       = domain.MaxCandidateDecisionNoteRunes
	maxRetryInstructionLength = 4_000
	maxCancelReasonLength     = 1_000
)

type retryDeltaEnvelope struct {
	SchemaVersion     string   `json:"schema_version"`
	RejectionNote     string   `json:"rejection_note"`
	UnmetCriterionIDs []string `json:"unmet_criterion_ids"`
	Instructions      string   `json:"instructions"`
}

type Server struct {
	// Only historical Apply fixtures bypass the new Task branch writeback gate.
	// Delivery itself is active and still requires explicit preview/confirmation.
	deliveryCommandsEnabled bool
	ctx                     context.Context
	cancel                  context.CancelFunc
	store                   *sqlite.Store
	service                 *app.Service
	registry                *adapterRegistry
	supervisor              *routingSupervisor
	fakeSupervisor          *fakeSupervisor
	runtimeRoot             string
	repoRoot                string
	installedEnvelope       *speccoding.InstalledEnvelope
	codexStatus             codexRuntimeStatus
	piStatus                piRuntimeStatus
	verifierStatus          verifierRuntimeStatus
	verifier                *productVerifier
	taskWorktreesReady      bool
	webRoot                 string
	logger                  *log.Logger
	piPreflight             func(context.Context) preflight.Report
	piBaseline              func() error
	product                 bool
	// sourceCheckout preserves product behavior without claiming installed
	// generation lifecycle authority.
	sourceCheckout           bool
	readinessRefreshMu       sync.Mutex
	readinessMu              sync.RWMutex
	readiness                *readinessView
	generationRefMu          sync.Mutex
	generationPublisher      generationReferencePublisher
	installationGenerationID string
	generationConfigErr      error
	installationController   ProductInstallationController
	dockerLedgerCloser       interface{ Close() error }
	o4AcceptanceAuthority    *acceptanceauthority.Controller
	o4DockerSupervisor       *dockersupervisor.Supervisor
	o4Evidence               *o4EvidenceBoundary
	repositorySource         app.RepositorySource
	worktreeResolver         *taskWorktreeResolver
	externalOpener           externalDirectoryOpener
	directoryPicker          projectDirectoryPicker
	directoryPickerMu        sync.Mutex
	piInstaller              piinstall.Installer
	piInstalledSelection     piinstall.Selection
	piInstallationBlocked    bool
	pathPiEnabled            bool
	piDiscoveryOptions       pidiscovery.Options
	commitMessages           commitMessageSuggestions
	automaticRetryWG         sync.WaitGroup
	automaticRetryCancel     context.CancelFunc
	closeOnce                sync.Once
	closeErr                 error
}

type codexRuntimeStatus struct {
	Enabled           bool   `json:"enabled"`
	Reason            string `json:"reason"`
	HostReadIsolation string `json:"hostReadIsolation"`
}

type piRuntimeStatus struct {
	Enabled               bool   `json:"enabled"`
	Reason                string `json:"reason"`
	HostReadIsolation     string `json:"hostReadIsolation"`
	Provider              string `json:"provider"`
	Image                 string `json:"image"`
	PolicyFingerprint     string `json:"policyFingerprint"`
	DockerVersion         string `json:"dockerVersion"`
	ColimaVersion         string `json:"colimaVersion"`
	EngineIdentityDigest  string `json:"engineIdentityDigest,omitempty"`
	DockerContext         string `json:"dockerContext,omitempty"`
	ContextEndpointDigest string `json:"contextEndpointDigest,omitempty"`
	DockerServerVersion   string `json:"dockerServerVersion,omitempty"`
}

type ProductOptions struct {
	RepoRoot               string
	RepositoryRoot         string
	PiAuthFile             string
	InstallationStateRoot  string
	GenerationID           string
	InstallationController ProductInstallationController
	PiPreflight            func(context.Context) preflight.Report

	// Runtime activation values are produced by setup from one marker-bound
	// installed release. Missing values keep only the affected profile route
	// disabled; they are never replaced with a different execution provider.
	ActivatedRelease *releaseassets.ActivatedRelease
	// ActivatedReleaseRoot is the canonical symlink-free root that produced
	// ActivatedRelease. It is independent from RepoRoot and has no fallback.
	ActivatedReleaseRoot  string
	DockerRunner          dockersupervisor.CommandRunner
	DockerLedgerOwner     *DockerOperationLedgerOwnership
	DockerEngineIdentity  dockersupervisor.EngineIdentity
	DockerCapability      dockersupervisor.CapabilityProbeContract
	DockerQualification   dockersupervisor.EngineQualification
	LocalPiResolver       *pidistribution.Resolver
	LocalPiSelection      pidistribution.Selection
	O4AcceptanceAuthority *acceptanceauthority.Controller
	O4Evidence            *O4EvidenceOptions
	// SourceCheckout selects the bounded development startup path. It keeps the
	// product Task/Room semantics while deliberately omitting installed
	// generation and lifecycle authority.
	SourceCheckout bool
	// RepositorySource wires the real Git provider for the Workbench project
	// entry path. It is nil for the installed product and source-checkout
	// servers, which return 503/unavailable on project routes.
	RepositorySource app.RepositorySource
	// DataRoot is the owner-private clone destination root for the Workbench.
	DataRoot string
	// PathPiEnabled selects the M2-S1 Local Connected PathPi + trustedhost
	// composition. It carries no Docker, pidistribution, --repository, or
	// private-auth authority.
	PathPiEnabled bool
	// PathPiSessionRoot is the owner-private per-Attempt Pi session directory
	// root. Required when PathPiEnabled is true.
	PathPiSessionRoot string
	// PathPiHome overrides the Pi agent home used for provider readiness
	// probing; empty uses the ambient user Pi home.
	PathPiHome string
	// PathPiLookPath resolves the "pi" executable; nil uses exec.LookPath. It
	// is an embedding/test seam only.
	PathPiLookPath func(string) (string, error)
}

// DockerOperationLedgerOwnership is the single-use ownership token for the
// exact installed ledger. It never grants authority to close an arbitrary
// CommandRunner.
type DockerOperationLedgerOwnership struct {
	mu     sync.Mutex
	ledger *dockersupervisor.OperationLedger
}

func NewDockerOperationLedgerOwnership(ledger *dockersupervisor.OperationLedger) *DockerOperationLedgerOwnership {
	if ledger == nil {
		return nil
	}
	return &DockerOperationLedgerOwnership{ledger: ledger}
}

func (ownership *DockerOperationLedgerOwnership) owns(runner dockersupervisor.CommandRunner) bool {
	if ownership == nil {
		return runner == nil
	}
	ownership.mu.Lock()
	defer ownership.mu.Unlock()
	candidate, ok := runner.(*dockersupervisor.OperationLedger)
	return ok && ownership.ledger != nil && candidate == ownership.ledger
}

func (ownership *DockerOperationLedgerOwnership) close() error {
	if ownership == nil {
		return nil
	}
	ownership.mu.Lock()
	ledger := ownership.ledger
	ownership.ledger = nil
	ownership.mu.Unlock()
	if ledger == nil {
		return nil
	}
	return ledger.Close()
}

func (ownership *DockerOperationLedgerOwnership) transfer(runner dockersupervisor.CommandRunner) (*dockersupervisor.OperationLedger, error) {
	if ownership == nil {
		if runner == nil {
			return nil, nil
		}
		return nil, errors.New("installed Docker Runner ownership is unavailable")
	}
	ownership.mu.Lock()
	defer ownership.mu.Unlock()
	candidate, ok := runner.(*dockersupervisor.OperationLedger)
	if !ok || ownership.ledger == nil || candidate != ownership.ledger {
		return nil, errors.New("installed Docker Runner ownership does not match its ledger")
	}
	ledger := ownership.ledger
	ownership.ledger = nil
	return ledger, nil
}

func (options ProductOptions) CloseOwnedDockerOperationLedger() error {
	return options.DockerLedgerOwner.close()
}

func New(ctx context.Context, databasePath, webRoot string, logger *log.Logger) (*Server, error) {
	return newServer(ctx, databasePath, webRoot, logger, ProductOptions{}, false)
}

// NewProduct constructs the installed Local Alpha service. Unlike the legacy
// development constructor, it requires an explicit immutable repository root,
// reconstructs the bundled frozen baseline, and fail-closes Pi without a
// same-process Preflight gate.
func NewProduct(ctx context.Context, databasePath, webRoot string, logger *log.Logger, options ProductOptions) (server *Server, resultErr error) {
	defer func() {
		if server == nil {
			resultErr = errors.Join(resultErr, options.CloseOwnedDockerOperationLedger())
		}
	}()
	if !filepath.IsAbs(options.RepoRoot) || filepath.Clean(options.RepoRoot) != options.RepoRoot || !filepath.IsAbs(options.RepositoryRoot) || filepath.Clean(options.RepositoryRoot) != options.RepositoryRoot || !filepath.IsAbs(options.PiAuthFile) || filepath.Clean(options.PiAuthFile) != options.PiAuthFile || options.PiPreflight == nil {
		return nil, errors.New("product server requires explicit immutable Source, repository, OAuth, and Pi Preflight identities")
	}
	return newServer(ctx, databasePath, webRoot, logger, options, true)
}

// NewSourceCheckout constructs the bounded Local Alpha development service
// from a source checkout. The caller must provide an exact, capability-qualified
// Docker authority. Missing or invalid isolation authority fails before startup;
// this constructor never enables a direct-host fallback.
func NewSourceCheckout(ctx context.Context, databasePath, webRoot string, logger *log.Logger, options ProductOptions) (*Server, error) {
	options.SourceCheckout = true
	if !filepath.IsAbs(options.RepoRoot) || filepath.Clean(options.RepoRoot) != options.RepoRoot ||
		!filepath.IsAbs(options.RepositoryRoot) || filepath.Clean(options.RepositoryRoot) != options.RepositoryRoot ||
		!filepath.IsAbs(options.PiAuthFile) || filepath.Clean(options.PiAuthFile) != options.PiAuthFile ||
		options.PiPreflight == nil || options.DockerRunner == nil || options.DockerLedgerOwner == nil ||
		len(options.DockerEngineIdentity.CanonicalJSON()) == 0 ||
		len(options.DockerCapability.CanonicalJSON()) == 0 || len(options.DockerQualification.CanonicalJSON()) == 0 {
		return nil, errors.New("source-checkout server requires explicit Source, repository, OAuth, qualified Docker Sandbox, and Preflight identities")
	}
	if err := validateSourceCheckoutDockerAuthority(options); err != nil {
		return nil, fmt.Errorf("source-checkout qualified Docker Sandbox identity is inconsistent: %w", err)
	}
	return newServer(ctx, databasePath, webRoot, logger, options, true)
}

// WorkbenchOptions configures the bounded Workbench project-entry service. It
// deliberately carries no repository root, Docker Engine identity, OAuth file,
// or Preflight authority.
type WorkbenchOptions struct {
	SourceRoot string
	DataRoot   string
}

// NewWorkbench constructs the Workbench project-entry service from a source
// checkout. It wires the real Git source provider and the clone data root, and
// requires no Docker, OAuth, or Preflight identity.
func NewWorkbench(ctx context.Context, databasePath, webRoot string, logger *log.Logger, options WorkbenchOptions) (*Server, error) {
	if !filepath.IsAbs(options.SourceRoot) || filepath.Clean(options.SourceRoot) != options.SourceRoot ||
		!filepath.IsAbs(options.DataRoot) || filepath.Clean(options.DataRoot) != options.DataRoot {
		return nil, errors.New("workbench server requires absolute Source and data roots")
	}
	absoluteWebRoot, err := filepath.Abs(webRoot)
	if err != nil {
		return nil, err
	}
	if !pathInsidePath(options.SourceRoot, absoluteWebRoot) {
		return nil, errors.New("workbench Web build must be inside the source checkout")
	}
	return newServer(ctx, databasePath, webRoot, logger, ProductOptions{
		RepositorySource:  gitsource.Default{},
		DataRoot:          options.DataRoot,
		PathPiEnabled:     true,
		PathPiSessionRoot: filepath.Join(options.DataRoot, "pi-sessions"),
	}, false)
}

func pathInsidePath(root, child string) bool {
	if !filepath.IsAbs(root) || !filepath.IsAbs(child) {
		return false
	}
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(child))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func validateSourceCheckoutDockerAuthority(options ProductOptions) error {
	identity, identityErr := dockersupervisor.RestoreEngineIdentity(options.DockerEngineIdentity.Record())
	contract, contractErr := dockersupervisor.RestoreCapabilityProbeContract(options.DockerCapability.Record())
	qualification, qualificationErr := dockersupervisor.RestoreEngineQualification(options.DockerQualification.Record())
	if identityErr != nil || contractErr != nil || qualificationErr != nil ||
		identity.Digest() != options.DockerEngineIdentity.Digest() || contract.Digest() != options.DockerCapability.Digest() ||
		qualification.Digest() != options.DockerQualification.Digest() || !qualification.ValidFor(identity, contract) {
		return errors.New("Engine identity, capability contract, and qualification do not bind one exact Docker authority")
	}
	return nil
}

func newServer(ctx context.Context, databasePath, webRoot string, logger *log.Logger, options ProductOptions, product bool) (result *Server, resultErr error) {
	installedProduct := product && !options.SourceCheckout
	if product {
		defer func() {
			if result == nil {
				resultErr = errors.Join(resultErr, options.CloseOwnedDockerOperationLedger())
			}
		}()
		if options.DockerRunner != nil && !options.DockerLedgerOwner.owns(options.DockerRunner) ||
			options.DockerRunner == nil && options.DockerLedgerOwner != nil {
			return nil, errors.New("installed Docker Runner requires one matching owned operation ledger")
		}
	}
	store, err := sqlite.Open(ctx, databasePath)
	if err != nil {
		return nil, err
	}
	absoluteWebRoot, err := filepath.Abs(webRoot)
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("resolve web root: %w", err)
	}
	if _, err := os.Stat(filepath.Join(absoluteWebRoot, "index.html")); err != nil {
		store.Close()
		return nil, fmt.Errorf("web build not found at %s; run npm run web:build: %w", absoluteWebRoot, err)
	}
	if logger == nil {
		logger = log.Default()
	}
	runtimeRoot := filepath.Join(filepath.Dir(databasePath), "runtime")
	if err := os.MkdirAll(runtimeRoot, 0o700); err != nil {
		store.Close()
		return nil, fmt.Errorf("create runtime root: %w", err)
	}
	if err := os.Chmod(runtimeRoot, 0o700); err != nil {
		store.Close()
		return nil, fmt.Errorf("secure runtime root: %w", err)
	}
	runtimeContext, cancel := context.WithCancel(ctx)
	registry := newAdapterRegistry()
	fakeSupervisor := newFakeSupervisor()
	supervisors := map[string]execution.ProcessSupervisor{"fake": fakeSupervisor}
	codexStatus := codexRuntimeStatus{Reason: "runtime gate verification failed", HostReadIsolation: "unavailable"}
	repoRoot := filepath.Clean(filepath.Join(absoluteWebRoot, "..", ".."))
	if product {
		repoRoot = options.RepoRoot
	}
	var installedSpecCodingEnvelope *speccoding.InstalledEnvelope
	var patchTarget app.PatchApplicationTarget
	var taskWorktrees *taskWorktreeManager
	if product {
		envelope, envelopeErr := speccoding.LoadInstalledEnvelope(repoRoot)
		if envelopeErr == nil {
			installedSpecCodingEnvelope = &envelope
			taskWorktrees, _ = newTaskWorktreeManagerForMode(
				options.RepositoryRoot,
				envelope.Repository(),
				options.SourceCheckout,
				envelope.ValidateApplyTarget,
			)
			if taskWorktrees != nil {
				patchTarget = &taskScopedGitPatchTarget{manager: taskWorktrees, reader: store.Reader()}
			}
		}
	}
	var verificationExecutor *productVerifier
	var verifierStatus verifierRuntimeStatus
	var verifierErr error
	if options.PathPiEnabled {
		verifierStatus = verifierRuntimeStatus{Reason: "not applicable to Local Connected · No Sandbox", Mode: "not_applicable", Network: "host", Credentials: "Pi native configuration"}
	} else {
		verificationExecutor, verifierStatus, verifierErr = newProductVerifier(runtimeContext, repoRoot, runtimeRoot, product, options)
		if verifierErr == nil {
			if product {
				if _, recoveryErr := recoverInstalledVerifierStartup(runtimeContext, verificationExecutor, store.Reader().ListVerificationStartupCandidates); recoveryErr != nil {
					verifierErr = recoveryErr
				} else {
					verifierStatus.Reason = "digest-pinned independent verifier configured; exact-scope labeled resources removed and cleanup proven"
				}
			} else {
				verificationCandidates, candidatesErr := store.Reader().ListVerificationStartupCandidates(runtimeContext)
				if candidatesErr != nil {
					verifierErr = fmt.Errorf("list Verification recovery candidates: %w", candidatesErr)
				} else if len(verificationCandidates) > 0 {
					if recoveryErr := verificationExecutor.Recover(runtimeContext); recoveryErr != nil {
						verifierErr = recoveryErr
					} else {
						verifierStatus.Reason = "digest-pinned independent verifier configured; stale labeled resources removed and cleanup proven"
					}
				}
			}
		}
		if verifierErr != nil {
			reason := verifierErr.Error()
			if product {
				reason = "independent Verifier dependencies unavailable; inspect public readiness"
			}
			verifierStatus = verifierRuntimeStatus{Reason: reason, Mode: strings.TrimSpace(os.Getenv(verifierPolicyModeEnvironment)), Network: "none", Credentials: "zero"}
			verificationExecutor = nil
		}
	}
	if product || options.PathPiEnabled {
		codexStatus = codexRuntimeStatus{Enabled: false, Reason: "legacy Codex Runtime is disabled", HostReadIsolation: "unavailable"}
	} else {
		codexAdapter, codexErr := codexcli.New(codexcli.Config{RepoRoot: repoRoot, RuntimeRoot: runtimeRoot})
		if codexErr == nil {
			_, codexErr = codexAdapter.Fingerprint(runtimeContext)
		}
		if codexErr == nil {
			codexSupervisor, supervisorErr := devsupervisor.New(devsupervisor.Config{
				AllowedAdapterID: codexcli.AdapterID, AllowedExecutable: codexcli.CodexExecutable,
			})
			if supervisorErr != nil {
				codexErr = supervisorErr
			} else {
				registry.Set(codexAdapter)
				supervisors[codexcli.AdapterID] = codexSupervisor
				codexStatus = codexRuntimeStatus{Enabled: true, Reason: "runtime gate verified", HostReadIsolation: "unavailable"}
			}
		}
		if codexErr != nil {
			codexStatus.Reason = codexErr.Error()
		}
	}
	piStatus := piRuntimeStatus{
		Reason: "Pi Runtime/Sandbox gate verification failed", HostReadIsolation: "denied",
		Provider: domain.DockerExecutionProvider, PolicyFingerprint: agentpi.PolicySHA256,
	}
	var piRecoveryRoutes []piRecoveryRoute
	var composedPi piComposition
	var pathPiDiscovery pidiscovery.Result
	var piErr error
	var installer piinstall.Installer
	var installedSelection piinstall.Selection
	installationBlocked := false
	discoveryOptions := pidiscovery.Options{PiHome: options.PathPiHome, LookPath: options.PathPiLookPath}
	if options.PathPiEnabled {
		// M2-S1 Local Connected: PATH-discovered Pi supervised by trustedhost.
		// A non-ready discovery still boots the server (empty composition); the
		// actionable state is reported by GET /api/pi/discovery.
		timeoutPolicy := acceptanceTimeoutPolicyFor(options.O4AcceptanceAuthority)
		if options.DataRoot == "" {
			options.DataRoot = filepath.Dir(databasePath)
		}
		installer, installedSelection, discoveryOptions, piErr = preparePiInstallation(runtimeContext, options.DataRoot, discoveryOptions)
		if piErr == nil {
			composedPi, pathPiDiscovery, piErr = composePathPiRuntime(runtimeContext, runtimeRoot, options.PathPiSessionRoot, discoveryOptions, timeoutPolicy, piInstallationGuard{installer: installer, selection: installedSelection})
		} else {
			installationBlocked = true
			discoveryOptions.LookPath = func(string) (string, error) { return "", errors.New("managed Pi selection is unavailable") }
			pathPiDiscovery = pidiscovery.Result{State: pidiscovery.StateDrifted, Reason: "Chora-selected Pi installation is unavailable or changed; repair it before starting Tasks."}
		}
		if piErr == nil && composedPi.adapter != nil {
			piErr = validatePiCompositionFingerprints(runtimeContext, composedPi)
		}
		if piErr == nil && composedPi.adapter != nil {
			piRecoveryRoutes, piErr = restorePiStartupRoutes(runtimeContext, store.Reader(), composedPi.dockerSupervisor)
		}
		if piErr == nil && composedPi.adapter != nil {
			registry.Set(composedPi.adapter)
			piStatus = piCompositionStatus(composedPi)
		}
	} else {
		piRuntimeConfig, configErr := os.ReadFile(filepath.Join(repoRoot, "contracts", "g2-m1a", "pi-runtime-config.v6.json"))
		piErr = configErr
		if configErr == nil {
			var piPolicy []byte
			piPolicy, piErr = os.ReadFile(filepath.Join(repoRoot, "contracts", "g2-m1a", "sandbox-policy.v4.json"))
			if piErr == nil {
				timeoutPolicy := acceptanceTimeoutPolicyFor(options.O4AcceptanceAuthority)
				composedPi, piErr = composePiRuntime(runtimeContext, runtimeRoot, filepath.Join(runtimeRoot, "artifacts"), repoRoot, piRuntimeConfig, piPolicy, options, product, timeoutPolicy)
				if piErr == nil && product {
					if verificationExecutor == nil {
						piErr = errors.New("installed frozen Baseline is unavailable")
					} else {
						composedPi.adapter, piErr = newPiWorkspaceAdapter(composedPi.adapter, speccoding.InstalledWorkspaceRoot, verificationExecutor.baselineRoot)
					}
				}
				if piErr == nil {
					piErr = validatePiCompositionFingerprints(runtimeContext, composedPi)
				}
				if piErr == nil {
					piRecoveryRoutes, piErr = restorePiStartupRoutes(runtimeContext, store.Reader(), composedPi.dockerSupervisor)
				}
				if piErr == nil {
					registry.Set(composedPi.adapter)
					piStatus = piCompositionStatus(composedPi)
					if options.SourceCheckout {
						piStatus, piErr = sourceCheckoutPiRuntimeStatus(piStatus, options)
					}
				}
			}
		}
	}
	if piErr != nil {
		if product {
			piStatus.Reason = "Pi Runtime/Sandbox dependencies unavailable; inspect public readiness"
		} else {
			piStatus.Reason = piErr.Error()
		}
	}
	if options.O4AcceptanceAuthority != nil && (verificationExecutor == nil || composedPi.dockerSupervisor == nil || piErr != nil) {
		cancel()
		_ = store.Close()
		return nil, errors.New("O4 acceptance execution and residue authorities are unavailable")
	}
	supervisor := newRoutingSupervisor(supervisors)
	for _, registration := range []struct {
		provider string
		process  execution.ProcessSupervisor
	}{
		{provider: domain.DockerExecutionProvider, process: composedPi.dockerSupervisor},
		{provider: domain.TrustedHostExecutionProvider, process: composedPi.trustedSupervisor},
	} {
		if registration.process == nil {
			continue
		}
		target, targetErr := execution.NewExecutionTarget(agentpi.AdapterID, registration.provider)
		if targetErr != nil {
			cancel()
			store.Close()
			return nil, fmt.Errorf("construct Pi execution target: %w", targetErr)
		}
		if err := supervisor.registerTarget(target, registration.process); err != nil {
			cancel()
			store.Close()
			return nil, fmt.Errorf("register Pi supervisor: %w", err)
		}
	}
	for _, route := range piRecoveryRoutes {
		if err := supervisor.RestoreTarget(runtimeContext, route.target, route.identity, route.launch); err != nil {
			cancel()
			store.Close()
			return nil, fmt.Errorf("restore Pi runtime route: %w", err)
		}
	}
	worktreeResolver := newTaskWorktreeResolver(store.Reader(), taskWorktrees)
	if options.PathPiEnabled {
		if options.DataRoot == "" {
			options.DataRoot = filepath.Dir(databasePath)
		}
		if err = os.MkdirAll(options.DataRoot, 0700); err != nil {
			cancel()
			_ = store.Close()
			return nil, err
		}
		options.DataRoot, err = filepath.EvalSymlinks(options.DataRoot)
		if err != nil {
			cancel()
			_ = store.Close()
			return nil, err
		}
	}
	var resourceWorkspaces app.TaskResourceWorkspaceResolver
	if options.PathPiEnabled {
		resourceWorkspaces, err = newTaskResourceWorkspaceManager(store, options.DataRoot)
		if err != nil {
			cancel()
			_ = store.Close()
			return nil, err
		}
	}
	var resourcePatches app.ResourceReviewPatchMaterializer
	if options.PathPiEnabled {
		resourcePatches, err = newResourcePatchStore(filepath.Join(runtimeRoot, "resource-patches"), store.Reader(), resourceWorkspaces)
		if err != nil {
			cancel()
			_ = store.Close()
			return nil, err
		}
	}
	var patchMaterializer app.ReviewPatchMaterializer
	verificationService, patchSource := optionalVerificationServices(verificationExecutor)
	if options.PathPiEnabled {
		patchStore, patchErr := newLocalConnectedPatchStore(filepath.Join(runtimeRoot, "review-patches"), store.Reader(), worktreeResolver)
		if patchErr != nil {
			cancel()
			_ = store.Close()
			return nil, patchErr
		}
		patchMaterializer = patchStore
		patchSource = patchStore
		patchTarget = &repositoryBoundGitPatchTarget{reader: store.Reader()}
	}
	roomCreationMode := app.RoomCreationLegacyStandalone
	if options.PathPiEnabled {
		roomCreationMode = app.RoomCreationProjectOnly
	}
	service := app.NewService(app.Dependencies{
		RoomCreationMode:            roomCreationMode,
		Lifecycle:                   runtimeContext,
		Store:                       store,
		Context:                     app.ContextAssembler{},
		Agents:                      registry,
		Supervisor:                  supervisor,
		Presence:                    localPresence{},
		Authorizer:                  localAuthorizer{},
		IDs:                         app.RandomIDs{},
		Verifier:                    verificationService,
		PatchMaterializer:           patchMaterializer,
		ResourcePatchMaterializer:   resourcePatches,
		ResourceReviewVerifier:      newResourceReviewVerifier(resourceWorkspaces),
		ResourceCheckObserverSHA256: agentpi.ResourceObserverSHA256(),
		ResourcePatchTarget:         newResourcePatchTarget(),
		PatchSource:                 patchSource,
		PatchTarget:                 patchTarget,
		TaskWorktrees:               worktreeResolver,
		TaskResourceWorkspaces:      resourceWorkspaces,
		TaskDeliveryWorkspaces:      newTaskDeliveryWorkspaceResolver(resourceWorkspaces),
		TaskDeliveryGit:             taskdelivery.Git{},
		TaskDeliveryHosting:         taskdelivery.NewGitHub(),
		InstalledSpecCodingEnvelope: installedSpecCodingEnvelope,
		SpecCodingEnvelopeResolver:  newBoundSpecCodingEnvelopeResolver(store.Reader(), installedSpecCodingEnvelope, pathPiDiscovery),
		RepositorySource:            options.RepositorySource,
		DataRoot:                    options.DataRoot,
		AutoStartVerification:       true,
	})
	server := &Server{
		ctx: runtimeContext, cancel: cancel, store: store, service: service, registry: registry, supervisor: supervisor,
		fakeSupervisor: fakeSupervisor, runtimeRoot: runtimeRoot, repoRoot: repoRoot, installedEnvelope: installedSpecCodingEnvelope,
		codexStatus: codexStatus, piStatus: piStatus, verifierStatus: verifierStatus, verifier: verificationExecutor, taskWorktreesReady: taskWorktrees != nil || options.PathPiEnabled,
		webRoot: absoluteWebRoot, logger: logger, piPreflight: options.PiPreflight, product: product, sourceCheckout: options.SourceCheckout,
		installationController: options.InstallationController,
		o4AcceptanceAuthority:  options.O4AcceptanceAuthority, o4DockerSupervisor: composedPi.dockerSupervisor,
		repositorySource: options.RepositorySource,
		worktreeResolver: worktreeResolver, externalOpener: macOSDirectoryOpener{}, directoryPicker: macOSProjectDirectoryPicker{},
		pathPiEnabled:      options.PathPiEnabled,
		piDiscoveryOptions: discoveryOptions,
		piInstaller:        installer, piInstalledSelection: installedSelection, piInstallationBlocked: installationBlocked,
	}
	server.o4Evidence, err = newO4EvidenceBoundary(options.O4Evidence, composedPi.dockerSupervisor)
	if err != nil {
		cancel()
		store.Close()
		return nil, err
	}
	if server.o4Evidence != nil && server.o4AcceptanceAuthority != nil && server.o4AcceptanceAuthority.TupleIdentity() != server.o4Evidence.tupleIdentity {
		cancel()
		store.Close()
		return nil, errors.New("O4 evidence and fault authorities have different tuple identities")
	}
	managedGenerationConfigured := false
	if installedProduct {
		server.installationGenerationID = strings.TrimSpace(options.GenerationID)
		managedGenerationConfigured = validInstallationGenerationID(server.installationGenerationID)
		if managedGenerationConfigured {
			backend, backendErr := productinstall.NewFileBackend(options.InstallationStateRoot)
			if backendErr != nil {
				server.generationConfigErr = backendErr
			} else if publisher, ok := any(backend).(generationReferencePublisher); ok {
				server.generationPublisher = publisher
			} else {
				server.generationConfigErr = errors.New("installed generation reference publication is unavailable")
			}
			if err := server.publishStartupGenerationReferences(runtimeContext); err != nil {
				cancel()
				store.Close()
				return nil, fmt.Errorf("publish pre-recovery generation references: %w", err)
			}
		} else {
			server.generationConfigErr = errors.New("installed generation identity is invalid")
			hasManaged, candidateErr := server.hasManagedStartupCandidate(runtimeContext)
			if candidateErr != nil || hasManaged {
				cancel()
				store.Close()
				if candidateErr != nil {
					return nil, fmt.Errorf("inspect managed recovery candidates: %w", candidateErr)
				}
				return nil, errors.New("managed recovery requires an exact installed generation")
			}
		}
	}
	if product && verificationExecutor != nil {
		server.piBaseline = verificationExecutor.VerifyBaselineIdentity
	}
	if err := service.RecoverStartup(runtimeContext); err != nil {
		cancel()
		store.Close()
		return nil, fmt.Errorf("recover persisted runtime state: %w", err)
	}
	if installedProduct && managedGenerationConfigured {
		if err := server.publishStartupGenerationReferences(runtimeContext); err != nil {
			cancel()
			store.Close()
			return nil, fmt.Errorf("publish recovered generation references: %w", err)
		}
	}
	if product {
		ledger, err := options.DockerLedgerOwner.transfer(options.DockerRunner)
		if err != nil {
			cancel()
			_ = store.Close()
			return nil, err
		}
		if ledger != nil {
			server.dockerLedgerCloser = ledger
		}
	}
	automaticRetryContext, automaticRetryCancel := context.WithCancel(runtimeContext)
	server.automaticRetryCancel = automaticRetryCancel
	server.automaticRetryWG.Add(1)
	go func() {
		defer server.automaticRetryWG.Done()
		server.runAutomaticRetryDispatcher(automaticRetryContext)
	}()
	return server, nil
}

func optionalVerificationServices(executor *productVerifier) (app.VerificationExecutor, app.ReviewPatchSource) {
	if executor == nil {
		return nil, nil
	}
	return executor, executor
}

func productCredentialSource(product bool, explicit string) string {
	if product {
		return explicit
	}
	return os.Getenv("CHORA_PI_CODEX_AUTH_FILE")
}

type piRecoveryRoute struct {
	target   execution.ExecutionTarget
	identity execution.ProcessIdentity
	launch   execution.LaunchToken
}

func restorePiStartupRoutes(ctx context.Context, reader storecontract.Reader, supervisor *dockersupervisor.Supervisor) ([]piRecoveryRoute, error) {
	candidates, err := reader.ListStartupCandidates(ctx)
	if err != nil {
		return nil, err
	}
	routes := make([]piRecoveryRoute, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Attempt == nil {
			continue
		}
		session, err := reader.GetRuntimeSessionForAttempt(ctx, candidate.Attempt.ID())
		if errors.Is(err, storecontract.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if session.AdapterID != agentpi.AdapterID || !session.FinalizedAt.IsZero() {
			continue
		}
		binding := candidate.Attempt.AgentExecutionProfileBinding()
		if !binding.Bound() {
			return nil, fmt.Errorf("persisted Pi session %s has no execution-provider binding", session.ID)
		}
		target, err := execution.NewExecutionTarget(agentpi.AdapterID, binding.ExecutionProvider())
		if err != nil {
			return nil, fmt.Errorf("restore Pi session %s target: %w", session.ID, err)
		}
		route := piRecoveryRoute{
			target:   target,
			identity: execution.ProcessIdentity{Value: session.ProcessIdentity},
			launch:   execution.LaunchToken{Value: session.LaunchToken},
		}
		switch binding.ExecutionProvider() {
		case domain.DockerExecutionProvider:
			if supervisor == nil {
				return nil, fmt.Errorf("restore managed Pi session %s: Docker supervisor unavailable", session.ID)
			}
			if err := supervisor.RegisterRecoveredDead(route.identity, route.launch); err != nil {
				return nil, fmt.Errorf("register recovered Pi session %s: %w", session.ID, err)
			}
		case domain.TrustedHostExecutionProvider:
			// trustedhost.New restores exact process records from its private
			// runtime root; the target router performs the ownership proof.
		default:
			return nil, fmt.Errorf("restore Pi session %s: unsupported execution provider %q", session.ID, binding.ExecutionProvider())
		}
		routes = append(routes, route)
	}
	return routes, nil
}

func (server *Server) Close() error {
	server.closeOnce.Do(func() {
		if server.automaticRetryCancel != nil {
			server.automaticRetryCancel()
		}
		server.cancel()
		server.automaticRetryWG.Wait()
		server.closeErr = errors.Join(server.store.Close(), closeIfPresent(server.dockerLedgerCloser))
	})
	return server.closeErr
}

func closeIfPresent(closer interface{ Close() error }) error {
	if closer == nil {
		return nil
	}
	return closer.Close()
}

func (server *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", server.getStatus)
	mux.HandleFunc("GET /api/pi/discovery", server.getPiDiscovery)
	mux.HandleFunc("GET /api/pi/installation", server.getPiInstallation)
	mux.HandleFunc("POST /api/pi/installation", server.installPi)
	mux.HandleFunc("POST /api/pi/installation/cancel", server.cancelPiInstallation)
	mux.HandleFunc("GET /api/readiness", server.getReadiness)
	mux.HandleFunc("POST /api/readiness/refresh", server.refreshReadiness)
	mux.HandleFunc("GET /api/product-installation/doctor", server.productInstallationDoctor)
	mux.HandleFunc("POST /api/product-installation/{action}", server.mutateProductInstallation)
	if server.o4AcceptanceAuthority != nil {
		mux.HandleFunc("GET /api/o4/residue", server.getO4Residue)
	}
	if server.o4Evidence != nil {
		mux.HandleFunc("POST /api/o4/evidence-boundaries", server.publishO4EvidenceBoundary)
	}
	if !server.product {
		mux.HandleFunc("POST /api/spec-coding/materializations", server.materializeSpecCodingContract)
		mux.HandleFunc("POST /api/spec-coding/contracts", server.registerSpecCodingContract)
	}
	mux.HandleFunc("POST /api/rooms", server.createRoom)
	mux.HandleFunc("GET /api/rooms", server.listRooms)
	mux.Handle("POST /api/projects", http.NewCrossOriginProtection().Handler(http.HandlerFunc(server.openProject)))
	mux.Handle("POST /api/projects/choose-directory", http.NewCrossOriginProtection().Handler(http.HandlerFunc(server.chooseProjectDirectory)))
	mux.HandleFunc("POST /api/projects/clone", server.cloneProject)
	mux.HandleFunc("GET /api/projects", server.listProjects)
	mux.HandleFunc("POST /api/v2/projects", server.createEmptyProject)
	mux.HandleFunc("GET /api/v2/projects", server.listResourceProjects)
	mux.HandleFunc("GET /api/v2/projects/{projectID}", server.getProjectResources)
	mux.HandleFunc("PATCH /api/v2/projects/{projectID}", server.changeResourceProject)
	mux.HandleFunc("POST /api/v2/projects/{projectID}/archive", server.changeResourceProject)
	mux.HandleFunc("POST /api/v2/projects/{projectID}/restore", server.changeResourceProject)
	mux.HandleFunc("GET /api/v2/projects/{projectID}/repositories", server.getProjectRepositoryPage)
	mux.HandleFunc("POST /api/v2/projects/{projectID}/repositories", server.addProjectRepository)
	mux.HandleFunc("POST /api/v2/projects/{projectID}/repositories/choose-directory", server.chooseProjectRepository)
	mux.HandleFunc("DELETE /api/v2/projects/{projectID}/repositories/{repoID}", server.removeProjectRepository)
	mux.HandleFunc("GET /api/v2/rooms/{roomID}/resources", server.getRoomResources)
	mux.HandleFunc("PUT /api/v2/rooms/{roomID}/resources", server.putRoomResources)
	mux.HandleFunc("GET /api/v2/rooms/{roomID}/task-options", server.taskResourceOptions)
	mux.HandleFunc("POST /api/v2/rooms/{roomID}/tasks", server.createResourceTask)
	mux.HandleFunc("GET /api/v2/repositories/{repoID}/delivery-defaults", server.repositoryDeliveryDefaults)
	mux.HandleFunc("PUT /api/v2/repositories/{repoID}/delivery-defaults", server.saveRepositoryDeliveryDefaults)
	mux.HandleFunc("GET /api/v2/repositories/{repoID}/branches", server.taskBranches)
	mux.HandleFunc("GET /api/v2/runs/{runID}/delivery", server.getTaskDelivery)
	mux.HandleFunc("POST /api/v2/runs/{runID}/delivery/message", server.suggestCommitMessage)
	mux.HandleFunc("POST /api/v2/runs/{runID}/delivery/draft-context", server.deliveryDraftContext)
	mux.HandleFunc("POST /api/v2/runs/{runID}/delivery/{action}", server.taskDeliveryCommand)
	mux.HandleFunc("GET /api/v2/tasks/{taskID}/resources", server.getTaskResources)
	mux.HandleFunc("GET /api/v2/runs/{runID}/result", server.getResourceResult)
	mux.HandleFunc("GET /api/v2/runs/{runID}/closure", server.getResultClosure)
	mux.HandleFunc("POST /api/v2/runs/{runID}/closure/{action}", server.resultClosureCommand)
	mux.HandleFunc("POST /api/v2/runs/{runID}/closure/cleanup/{action}", server.closedResultCleanupCommand)
	mux.HandleFunc("POST /api/v2/runs/{runID}/review", server.reviewResourceResult)
	mux.HandleFunc("POST /api/v2/runs/{runID}/apply", server.applyResourceResult)
	mux.HandleFunc("GET /api/v2/projects/{projectID}/repositories/{repoID}/entries", server.getRepositoryEntries)
	mux.HandleFunc("GET /api/v2/projects/{projectID}/repositories/{repoID}/checks", server.getRepositoryChecks)
	mux.HandleFunc("PUT /api/v2/projects/{projectID}/repositories/{repoID}/checks", server.putRepositoryChecks)

	mux.HandleFunc("GET /api/projects/{projectID}", server.getProject)
	mux.HandleFunc("GET /api/projects/{projectID}/settings", server.getProjectSettings)
	mux.HandleFunc("PUT /api/projects/{projectID}/settings", server.putProjectSettings)
	mux.HandleFunc("GET /api/projects/{projectID}/continuity", server.projectContinuity)
	mux.Handle("POST /api/projects/{projectID}/open-external", http.NewCrossOriginProtection().Handler(http.HandlerFunc(server.openExternalDirectory)))
	mux.HandleFunc("DELETE /api/projects/{projectID}", server.removeProject)
	mux.HandleFunc("POST /api/projects/{projectID}/rooms", server.createProjectRoom)
	mux.HandleFunc("PATCH /api/projects/{projectID}", server.renameProject)
	mux.HandleFunc("POST /api/projects/{projectID}/archive", server.archiveProject)
	mux.HandleFunc("POST /api/projects/{projectID}/restore", server.restoreProject)
	mux.HandleFunc("PATCH /api/rooms/{roomID}", server.renameRoom)
	mux.HandleFunc("GET /api/rooms/{roomID}", server.getRoom)
	mux.HandleFunc("GET /api/rooms/{roomID}/workspace", server.getRoomWorkspace)
	mux.HandleFunc("POST /api/rooms/{roomID}/archive", server.archiveRoom)
	mux.HandleFunc("POST /api/rooms/{roomID}/restore", server.restoreRoom)
	mux.HandleFunc("POST /api/rooms/{roomID}/tasks", server.createTask)
	mux.HandleFunc("POST /api/agent-execution/trusted-local-acknowledgements", server.acknowledgeTrustedLocal)
	mux.HandleFunc("GET /api/agent-execution/trusted-local-acknowledgements/current", server.getCurrentTrustedLocalAcknowledgement)
	mux.HandleFunc("GET /api/rooms/{roomID}/tasks/{taskID}", server.getRoomTask)
	mux.HandleFunc("GET /api/rooms/{roomID}/tasks/{taskID}/runs", server.listTaskRuns)
	mux.HandleFunc("GET /api/rooms/{roomID}/tasks/{taskID}/runs/{runID}", server.getRoomTaskRun)
	mux.HandleFunc("GET /api/tasks/{taskID}", server.getTask)
	mux.HandleFunc("POST /api/tasks/{taskID}/archive", server.archiveTask)
	mux.HandleFunc("POST /api/tasks/{taskID}/restore", server.restoreTask)
	mux.HandleFunc("PATCH /api/tasks/{taskID}/plan/drafts/{draftID}", server.saveTechnicalPlanDraft)
	mux.HandleFunc("POST /api/tasks/{taskID}/plan/drafts/{draftID}/submit", server.submitTechnicalPlanDraft)
	mux.HandleFunc("POST /api/tasks/{taskID}/plan/revisions/{revisionID}/reviews", server.reviewTechnicalPlanRevision)
	mux.HandleFunc("POST /api/tasks/{taskID}/plan/revisions/{revisionID}/activate", server.activateTechnicalPlanRevision)
	mux.HandleFunc("GET /api/rooms/{roomID}/revisions", server.getRoomRevisions)
	mux.HandleFunc("POST /api/tasks/{taskID}/runs", server.startRun)
	mux.HandleFunc("GET /api/runs/{runID}", server.getRun)
	mux.HandleFunc("GET /api/runs/{runID}/patch", server.getReviewPatch)
	mux.HandleFunc("GET /api/runs/{runID}/events", server.getRunEvents)
	mux.HandleFunc("POST /api/runs/{runID}/verification", server.startVerification)
	mux.HandleFunc("POST /api/runs/{runID}/verification/cancel", server.cancelVerification)
	mux.HandleFunc("POST /api/runs/{runID}/verification/retry", server.retryVerification)
	mux.HandleFunc("POST /api/runs/{runID}/cancel", server.cancelRun)
	mux.HandleFunc("POST /api/runs/{runID}/review", server.reviewRun)
	mux.HandleFunc("POST /api/runs/{runID}/apply", server.applyRunPatch)
	mux.HandleFunc("POST /api/runs/{runID}/retry", server.retryRun)
	mux.HandleFunc("POST /api/runs/{runID}/agent-execution-profile", server.switchAgentExecutionProfile)
	mux.HandleFunc("POST /api/decision-gates/{gateID}/resolve", server.resolveDecisionGate)
	mux.HandleFunc("PATCH /api/candidates/{candidateID}", server.editCandidate)
	mux.HandleFunc("POST /api/candidates/{candidateID}/decision", server.decideCandidate)
	mux.HandleFunc("/", server.serveWeb)
	return localHTTPAuthority(mux)
}

func (server *Server) getStatus(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]any{"codex": server.codexStatus, "pi": server.piStatus, "verifier": server.verifierStatus})
}

type o4GenerationReferenceProof struct {
	Serving                 dockersupervisor.ResidueCount `json:"serving"`
	ActiveAttempts          dockersupervisor.ResidueCount `json:"activeAttempts"`
	RecoverableAttempts     dockersupervisor.ResidueCount `json:"recoverableAttempts"`
	CurrentGenerationSHA256 string                        `json:"currentGenerationSha256"`
}

type o4ResidueProof struct {
	SchemaVersion          string                              `json:"schemaVersion"`
	Status                 string                              `json:"status"`
	AuthoritySHA256        string                              `json:"authoritySha256"`
	TupleIdentity          string                              `json:"tupleIdentity"`
	Docker                 dockersupervisor.ResidueObservation `json:"docker"`
	VerificationWorkspaces verifierWorkspaceResidueView        `json:"verificationWorkspaces"`
	GenerationReferences   o4GenerationReferenceProof          `json:"generationReferences"`
}

type verifierWorkspaceResidueView struct {
	Count           int    `json:"count"`
	AggregateSHA256 string `json:"aggregateSha256"`
}

type generationReferenceReader interface {
	References(context.Context) (productinstall.GenerationReferences, error)
}

func (server *Server) getO4Residue(writer http.ResponseWriter, request *http.Request) {
	proof, err := server.observeO4Residue(request.Context())
	if err != nil {
		writeJSON(writer, http.StatusConflict, map[string]string{"error": "o4_residue_proof_unavailable"})
		return
	}
	writeJSON(writer, http.StatusOK, proof)
}

func (server *Server) observeO4Residue(ctx context.Context) (o4ResidueProof, error) {
	controller := server.o4AcceptanceAuthority
	if controller == nil || len(controller.AuthorityDigest()) != sha256.Size*2 || len(controller.TupleIdentity()) != sha256.Size*2 || server.o4DockerSupervisor == nil || server.verifier == nil || server.verifier.workspaceFactory == nil {
		return o4ResidueProof{}, errors.New("O4 residue observer is not authenticated")
	}
	dockerProof, err := server.o4DockerSupervisor.ObserveResidue(ctx)
	if err != nil {
		return o4ResidueProof{}, err
	}
	verification, err := server.verifier.workspaceFactory.ObserveWorkspaces()
	if err != nil {
		return o4ResidueProof{}, err
	}
	var references o4GenerationReferenceProof
	err = server.withGenerationLifecycleLock(ctx, func(lockCtx context.Context) error {
		if err := server.requireLoadedGenerationCurrent(lockCtx); err != nil {
			return err
		}
		reader, ok := server.generationPublisher.(generationReferenceReader)
		if !ok {
			return errors.New("installed generation references are unreadable")
		}
		observed, err := reader.References(lockCtx)
		if err != nil {
			return err
		}
		if len(observed.ServingGenerationIDs) != 1 || observed.ServingGenerationIDs[0] != server.installationGenerationID ||
			len(observed.ActiveAttemptGenerationIDs) != 0 || len(observed.RecoverableAttemptGenerationIDs) != 0 {
			return errors.New("installed generation references do not prove a terminal exact-current state")
		}
		references = o4GenerationReferenceProof{
			Serving: safeStringAggregate(observed.ServingGenerationIDs), ActiveAttempts: safeStringAggregate(observed.ActiveAttemptGenerationIDs),
			RecoverableAttempts:     safeStringAggregate(observed.RecoverableAttemptGenerationIDs),
			CurrentGenerationSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(server.installationGenerationID))),
		}
		return nil
	})
	if err != nil {
		return o4ResidueProof{}, err
	}
	if dockerProof.Containers.Count != 0 || dockerProof.VerifierContainers.Count != 0 || dockerProof.Networks.Count != 0 || dockerProof.Volumes.Count != 0 || dockerProof.Configs.Count != 0 ||
		dockerProof.ManagedWorkspaces.Count != 0 || dockerProof.AttemptProcessGroups.Count != 0 || verification.Count != 0 {
		return o4ResidueProof{}, errors.New("authenticated terminal residue remains")
	}
	return o4ResidueProof{
		SchemaVersion: "chora.m1-o4-residue-proof.v1", Status: "proven_zero", AuthoritySHA256: controller.AuthorityDigest(), TupleIdentity: controller.TupleIdentity(),
		Docker: dockerProof, VerificationWorkspaces: verifierWorkspaceResidueView{Count: verification.Count, AggregateSHA256: verification.AggregateSHA256}, GenerationReferences: references,
	}, nil
}

func safeStringAggregate(values []string) dockersupervisor.ResidueCount {
	hash := sha256.New()
	for _, value := range values {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{'\n'})
	}
	return dockersupervisor.ResidueCount{Count: len(values), AggregateSHA256: fmt.Sprintf("%x", hash.Sum(nil))}
}

func (server *Server) getReadiness(writer http.ResponseWriter, request *http.Request) {
	server.readinessMu.RLock()
	cached := cloneReadiness(server.readiness)
	server.readinessMu.RUnlock()
	if cached != nil {
		writeJSON(writer, http.StatusOK, *cached)
		return
	}
	server.writeRefreshedReadiness(writer, request)
}

func (server *Server) refreshReadiness(writer http.ResponseWriter, request *http.Request) {
	if err := decodeOptionalJSON(request, &struct{}{}); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	server.writeRefreshedReadiness(writer, request)
}

func (server *Server) writeRefreshedReadiness(writer http.ResponseWriter, request *http.Request) {
	server.readinessRefreshMu.Lock()
	defer server.readinessRefreshMu.Unlock()
	server.readinessMu.RLock()
	previous := cloneReadiness(server.readiness)
	server.readinessMu.RUnlock()
	fingerprint := ""
	if previous != nil {
		fingerprint = previous.InputFingerprint
	}
	checking := checkingReadiness(time.Now().UTC(), fingerprint)
	server.readinessMu.Lock()
	server.readiness = cloneReadiness(&checking)
	server.readinessMu.Unlock()
	report := preflight.Report{Status: preflight.StatusFailed, ResourcesCreated: false, Failure: &preflight.Failure{
		Boundary: "preflight.unconfigured", Observed: "live readiness gate unavailable", Required: "same-process live dependency checks", Action: "Restart Chora from the marker-bound installation, then Refresh readiness.",
	}}
	if server.piPreflight != nil {
		report = server.piPreflight(request.Context())
	}
	var baselineErr error
	if server.product {
		_, baselineErr = speccoding.LoadInstalledEnvelope(server.repoRoot)
	}
	if baselineErr == nil && server.piBaseline != nil {
		baselineErr = server.piBaseline()
	}
	view := projectReadiness(readinessInputs{
		Report: report, Pi: server.piStatus, Verifier: server.verifierStatus, BaselineErr: baselineErr,
		TaskWorktreesRequired: server.product, TaskWorktreesReady: server.taskWorktreesReady, CheckedAt: time.Now().UTC(),
	})
	server.readinessMu.Lock()
	server.readiness = cloneReadiness(&view)
	server.readinessMu.Unlock()
	writeJSON(writer, http.StatusOK, view)
}

func checkingReadiness(checkedAt time.Time, fingerprint string) readinessView {
	view := readinessView{CheckedAt: checkedAt, InputFingerprint: publicFingerprint(fingerprint), Items: make([]readinessItem, len(readinessDescriptions))}
	for index, description := range readinessDescriptions {
		view.Items[index] = readinessItem{
			Key: description.key, State: readinessChecking, Observed: description.checking,
			Required: description.required, Action: "Wait for the current readiness Refresh to finish.",
		}
	}
	return view
}

func cloneReadiness(source *readinessView) *readinessView {
	if source == nil {
		return nil
	}
	result := *source
	result.Items = append([]readinessItem(nil), source.Items...)
	return &result
}

func (server *Server) materializeSpecCodingContract(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Contract          json.RawMessage `json:"contract"`
		WorkspaceRoot     string          `json:"workspaceRoot"`
		ContextEntryID    string          `json:"contextEntryId"`
		ContextRevisionID string          `json:"contextRevisionId"`
		CharterID         string          `json:"charterId"`
		AgentAdapter      string          `json:"agentAdapter"`
		FrozenAt          string          `json:"frozenAt"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	contract, err := speccoding.DecodeCoreContract(input.Contract)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	workspaceRoot, err := normalizeWorkspaceRoot(input.WorkspaceRoot)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	entryID, entryErr := domain.ParseContextEntryID(input.ContextEntryID)
	revisionID, revisionErr := domain.ParseContextRevisionID(input.ContextRevisionID)
	charterID, charterErr := domain.ParseCharterID(input.CharterID)
	frozenAt, timeErr := time.Parse(time.RFC3339Nano, input.FrozenAt)
	if entryErr != nil || revisionErr != nil || charterErr != nil || timeErr != nil || frozenAt.Location() != time.UTC {
		writeError(writer, http.StatusBadRequest, errors.New("invalid Spec Coding materialization identity"))
		return
	}
	result, err := server.service.MaterializeSpecCodingContract(request.Context(), app.MaterializeSpecCodingContractRequest{
		CommandMeta: commandMeta("materialize-spec-coding"), Contract: contract, WorkspaceRoot: workspaceRoot,
		ContextEntryID: entryID, ContextRevisionID: revisionID, CharterID: charterID, AgentAdapter: input.AgentAdapter, FrozenAt: frozenAt,
	})
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusCreated, specCodingBindingView(result.Binding))
}

func (server *Server) registerSpecCodingContract(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Contract json.RawMessage `json:"contract"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	contract, err := speccoding.DecodeCoreContract(input.Contract)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	result, err := server.service.RegisterSpecCodingContract(request.Context(), app.RegisterSpecCodingContractRequest{CommandMeta: commandMeta("register-spec-coding"), Contract: contract})
	if err != nil {
		writeMutationConflictError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, specCodingBindingView(result.Binding))
}

func specCodingBindingView(binding storecontract.SpecCodingBinding) map[string]any {
	return map[string]any{
		"taskId": binding.TaskID.String(), "status": binding.Status,
		"snapshot": map[string]string{"id": binding.SnapshotID.String(), "digest": fmt.Sprintf("%x", binding.SnapshotDigest)},
	}
}

func (server *Server) listRooms(writer http.ResponseWriter, request *http.Request) {
	directory, err := server.service.ListRoomDirectory(request.Context())
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	view := roomDirectoryViewOf(directory)
	if err := server.enrichRoomDirectoryAutomaticRetry(request.Context(), directory, &view); err != nil {
		writeStoreError(writer, err)
		return
	}
	if server.product {
		view.hideWorkspaceRoots()
	}
	writeJSON(writer, http.StatusOK, view)
}

func (server *Server) getRoomWorkspace(writer http.ResponseWriter, request *http.Request) {
	roomID, err := domain.ParseRoomID(request.PathValue("roomID"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	workspace, err := server.service.GetRoomWorkspace(request.Context(), roomID)
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	view := roomWorkspaceViewOf(workspace)
	if err := server.enrichRoomWorkspaceAgentExecution(request.Context(), workspace, &view); err != nil {
		writeStoreError(writer, err)
		return
	}
	if server.product {
		view.hideWorkspaceRoot()
	}
	writeJSON(writer, http.StatusOK, view)
}

func (server *Server) getRoomTask(writer http.ResponseWriter, request *http.Request) {
	roomID, err := domain.ParseRoomID(request.PathValue("roomID"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	taskID, err := domain.ParseTaskID(request.PathValue("taskID"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	workspace, err := server.service.GetRoomWorkspace(request.Context(), roomID)
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	found := false
	for _, summary := range workspace.Tasks {
		if summary.Task.ID() == taskID {
			found = true
			break
		}
	}
	if !found {
		writeError(writer, http.StatusNotFound, storecontract.ErrNotFound)
		return
	}
	view, err := server.taskReferenceView(request.Context(), taskID)
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

func (server *Server) listTaskRuns(writer http.ResponseWriter, request *http.Request) {
	roomID, taskID, ok := parseRoomTaskPath(writer, request)
	if !ok {
		return
	}
	history, err := server.service.ListTaskRunHistory(request.Context(), roomID, taskID)
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	view, err := server.taskRunHistoryView(request.Context(), history)
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

func (server *Server) getRoomTaskRun(writer http.ResponseWriter, request *http.Request) {
	roomID, taskID, ok := parseRoomTaskPath(writer, request)
	if !ok {
		return
	}
	runID, err := domain.ParseRunID(request.PathValue("runID"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	validated, err := server.service.GetTaskRun(request.Context(), roomID, taskID, runID)
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	view, err := server.runView(request.Context(), validated.ID())
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

func parseRoomTaskPath(writer http.ResponseWriter, request *http.Request) (domain.RoomID, domain.TaskID, bool) {
	roomID, err := domain.ParseRoomID(request.PathValue("roomID"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return domain.RoomID{}, domain.TaskID{}, false
	}
	taskID, err := domain.ParseTaskID(request.PathValue("taskID"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return domain.RoomID{}, domain.TaskID{}, false
	}
	return roomID, taskID, true
}

func (server *Server) archiveRoom(writer http.ResponseWriter, request *http.Request) {
	server.changeRoomLifecycle(writer, request, domain.RoomStateArchived)
}

func (server *Server) restoreRoom(writer http.ResponseWriter, request *http.Request) {
	server.changeRoomLifecycle(writer, request, domain.RoomStateActive)
}

func (server *Server) changeRoomLifecycle(writer http.ResponseWriter, request *http.Request, target domain.RoomState) {
	roomID, err := domain.ParseRoomID(request.PathValue("roomID"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	var input struct {
		ExpectedVersion uint64 `json:"expectedVersion"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	action := "archive-room"
	if target == domain.RoomStateActive {
		action = "restore-room"
	}
	meta, ok := requireRequestCommandMeta(writer, request, action)
	if !ok {
		return
	}
	command := app.ChangeRoomLifecycleRequest{CommandMeta: meta, RoomID: roomID, ExpectedVersion: input.ExpectedVersion}
	var result app.ChangeRoomLifecycleResult
	if target == domain.RoomStateArchived {
		result, err = server.service.ArchiveRoom(request.Context(), command)
	} else {
		result, err = server.service.RestoreRoom(request.Context(), command)
	}
	if err != nil {
		writeRoomLifecycleError(writer, err)
		return
	}
	view := roomViewOf(result.Room)
	if server.product {
		view.WorkspaceRoot = ""
	}
	writeJSON(writer, http.StatusOK, view)
}

func (server *Server) archiveTask(writer http.ResponseWriter, request *http.Request) {
	server.changeTaskArchive(writer, request, true)
}

func (server *Server) restoreTask(writer http.ResponseWriter, request *http.Request) {
	server.changeTaskArchive(writer, request, false)
}

func (server *Server) changeTaskArchive(writer http.ResponseWriter, request *http.Request, archive bool) {
	taskID, err := domain.ParseTaskID(request.PathValue("taskID"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	action := "restore-task"
	if archive {
		action = "archive-task"
	}
	meta, ok := requireRequestCommandMeta(writer, request, action)
	if !ok {
		return
	}
	command := app.ArchiveTaskRequest{CommandMeta: meta, TaskID: taskID}
	var result app.ArchiveTaskResult
	if archive {
		result, err = server.service.ArchiveTask(request.Context(), command)
	} else {
		result, err = server.service.RestoreTask(request.Context(), command)
	}
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"id": result.Task.ID().String(), "archived": result.Task.Archived(), "archivedAt": result.Task.ArchivedAt()})
}

func (server *Server) getRoom(writer http.ResponseWriter, request *http.Request) {
	roomID, err := domain.ParseRoomID(request.PathValue("roomID"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	room, err := server.store.Reader().GetRoom(request.Context(), roomID)
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	revisions, err := server.store.Reader().ListRoomRevisions(request.Context(), roomID)
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	view := roomDetailView{roomView: roomViewOf(room), Revisions: []roomRevisionView{}}
	if server.product {
		view.WorkspaceRoot = ""
	}
	for _, revision := range revisions {
		view.Revisions = append(view.Revisions, roomRevisionViewOf(revision))
	}
	writeJSON(writer, http.StatusOK, view)
}

func (server *Server) createRoom(writer http.ResponseWriter, request *http.Request) {
	if server.pathPiEnabled {
		writeError(writer, http.StatusUnprocessableEntity, errors.New("Create a Room inside a Project; standalone Rooms are unavailable in Workbench"))
		return
	}
	var input struct {
		Name          string `json:"name"`
		Description   string `json:"description"`
		WorkspaceRoot string `json:"workspaceRoot"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	var (
		root string
		err  error
	)
	if server.product {
		if strings.TrimSpace(input.WorkspaceRoot) != "" {
			writeError(writer, http.StatusUnprocessableEntity, errors.New("installed product Rooms use the fixed repository identity; workspaceRoot is unsupported"))
			return
		}
		root = speccoding.InstalledWorkspaceRoot
	} else if strings.TrimSpace(input.WorkspaceRoot) == "" {
		root = server.repoRoot
	} else {
		root, err = normalizeWorkspaceRoot(input.WorkspaceRoot)
		if err != nil {
			writeError(writer, http.StatusBadRequest, err)
			return
		}
	}
	meta := requestCommandMeta(request, "create-room")
	if server.product {
		var ok bool
		meta, ok = requireRequestCommandMeta(writer, request, "create-room")
		if !ok {
			return
		}
	}
	result, err := server.service.CreateRoom(request.Context(), app.CreateRoomRequest{
		CommandMeta:   meta,
		Name:          input.Name,
		Description:   input.Description,
		WorkspaceRoot: root,
	})
	if err != nil {
		if errors.Is(err, storecontract.ErrIdempotencyConflict) {
			writeError(writer, http.StatusConflict, err)
			return
		}
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	view := map[string]any{
		"id": result.Room.ID().String(), "name": result.Room.Name(), "description": result.Room.Description(),
		"state": string(result.Room.State()), "version": result.Room.Version(), "archivedAt": formatOptionalTime(result.Room.ArchivedAt()),
		"initialRevision": roomRevisionViewOf(result.InitialRevision),
	}
	if !server.product {
		view["workspaceRoot"] = result.Room.WorkspaceRoot()
	}
	writeJSON(writer, http.StatusCreated, view)
}

func (server *Server) createTask(writer http.ResponseWriter, request *http.Request) {
	roomID, err := domain.ParseRoomID(request.PathValue("roomID"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if server.pathPiEnabled {
		room, roomErr := server.store.Reader().GetRoom(request.Context(), roomID)
		if roomErr != nil {
			writeStoreError(writer, roomErr)
			return
		}
		if !room.ProjectID().Valid() || string(room.OwnershipKind()) != "project" {
			writeError(writer, http.StatusUnprocessableEntity, errors.New("Room has no qualified Project ownership; open a Project and create a topic Room"))
			return
		}
		if err := server.requireLegacyProject(request.Context(), room.ProjectID()); err != nil {
			writeProjectError(writer, err)
			return
		}
	}
	var input struct {
		ProjectSettingsVersion *uint64                      `json:"projectSettingsVersion"`
		Title                  string                       `json:"title"`
		Goal                   string                       `json:"goal"`
		ExecutionProfile       app.TaskExecutionProfile     `json:"executionProfile"`
		AgentExecutionProfile  domain.AgentExecutionProfile `json:"agentExecutionProfile"`
		Criteria               []string                     `json:"criteria"`
		RevisionIDs            []string                     `json:"revisionIds"`
		RealSpecCoding         *struct {
			Requirement string   `json:"requirement"`
			Constraints []string `json:"constraints"`
			OutOfScope  []string `json:"outOfScope"`
			Criteria    []struct {
				Title                      string `json:"title"`
				Description                string `json:"description"`
				VerificationCommandIndexes []int  `json:"verificationCommandIndexes"`
			} `json:"criteria"`
			WritableFiles        []string `json:"writableFiles"`
			WritableDirectories  []string `json:"writableDirectories"`
			NoChecks             bool     `json:"noChecks"`
			VerificationCommands []struct {
				Argv             []string `json:"argv"`
				WorkingDirectory string   `json:"workingDirectory"`
			} `json:"verificationCommands"`
		} `json:"realSpecCoding"`
		Plan struct {
			TechnicalSteps []string `json:"technicalSteps"`
			Decisions      []string `json:"decisions"`
			Risks          []string `json:"risks"`
			Unknowns       []string `json:"unknowns"`
		} `json:"plan"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if input.ExecutionProfile == "" {
		if server.product || server.pathPiEnabled {
			input.ExecutionProfile = app.TaskExecutionProfileRealSpecCoding
		} else {
			input.ExecutionProfile = app.TaskExecutionProfileDiagnosticFake
		}
	}
	if (server.product || server.pathPiEnabled) && input.ExecutionProfile != app.TaskExecutionProfileDiagnosticFake && input.ExecutionProfile != app.TaskExecutionProfileRealSpecCoding {
		writeError(writer, http.StatusBadRequest, errors.New("executionProfile must explicitly select diagnostic_fake or real_spec_coding"))
		return
	}
	if server.pathPiEnabled && input.ExecutionProfile == app.TaskExecutionProfileRealSpecCoding {
		if input.RealSpecCoding != nil {
			writeError(writer, http.StatusBadRequest, errors.New("Project tasks must use saved Project scope and checks; update Project settings before starting"))
			return
		}
		if input.ProjectSettingsVersion == nil {
			writeError(writer, http.StatusBadRequest, errors.New("Project settings version is required; refresh the task form before starting"))
			return
		}
	}
	if (server.product || server.pathPiEnabled) && input.ExecutionProfile == app.TaskExecutionProfileRealSpecCoding && input.RealSpecCoding == nil {
		var defaults speccoding.DefaultUserTaskInput
		var defaultErr error
		if server.pathPiEnabled {
			binding, bindingErr := server.store.Reader().GetRepositoryBinding(request.Context(), roomID)
			if bindingErr != nil || binding.State() != domain.RepositoryBindingStateActive {
				writeError(writer, http.StatusServiceUnavailable, errors.New("active Project repository binding is unavailable"))
				return
			}
			project, projectErr := server.service.GetProject(request.Context(), roomID)
			if projectErr != nil {
				writeProjectError(writer, projectErr)
				return
			}
			settings, settingsErr := server.resolveProjectSettings(request.Context(), project)
			if settingsErr != nil {
				writeProjectError(writer, settingsErr)
				return
			}
			if input.ProjectSettingsVersion != nil && *input.ProjectSettingsVersion != settings.Version() {
				writeError(writer, http.StatusConflict, errors.New("Project settings changed; refresh the task form before starting"))
				return
			}
			defaults, defaultErr = defaultLocalConnectedUserTaskFromSettings(request.Context(), binding.LocalLocator(), settings)
		} else if server.installedEnvelope != nil {
			defaults, defaultErr = server.installedEnvelope.DefaultUserTask()
		} else {
			defaultErr = errors.New("installed Real Spec Coding envelope is unavailable")
		}
		if defaultErr != nil {
			writeError(writer, http.StatusUnprocessableEntity, defaultErr)
			return
		}
		requirement := strings.TrimSpace(input.Goal)
		if requirement == "" {
			requirement = strings.TrimSpace(input.Title)
		}
		input.RealSpecCoding = &struct {
			Requirement string   `json:"requirement"`
			Constraints []string `json:"constraints"`
			OutOfScope  []string `json:"outOfScope"`
			Criteria    []struct {
				Title                      string `json:"title"`
				Description                string `json:"description"`
				VerificationCommandIndexes []int  `json:"verificationCommandIndexes"`
			} `json:"criteria"`
			WritableFiles        []string `json:"writableFiles"`
			WritableDirectories  []string `json:"writableDirectories"`
			NoChecks             bool     `json:"noChecks"`
			VerificationCommands []struct {
				Argv             []string `json:"argv"`
				WorkingDirectory string   `json:"workingDirectory"`
			} `json:"verificationCommands"`
		}{Requirement: requirement, Constraints: defaults.Constraints, OutOfScope: defaults.OutOfScope, WritableFiles: defaults.WritableFiles, WritableDirectories: defaults.WritableDirectories, NoChecks: defaults.NoChecks}
		input.RealSpecCoding.Criteria = append(input.RealSpecCoding.Criteria, struct {
			Title                      string `json:"title"`
			Description                string `json:"description"`
			VerificationCommandIndexes []int  `json:"verificationCommandIndexes"`
		}{Title: "Requested behavior is implemented", Description: requirement})
		for index := range defaults.VerificationCommands {
			input.RealSpecCoding.Criteria[0].VerificationCommandIndexes = append(input.RealSpecCoding.Criteria[0].VerificationCommandIndexes, index)
		}
		for _, command := range defaults.VerificationCommands {
			input.RealSpecCoding.VerificationCommands = append(input.RealSpecCoding.VerificationCommands, struct {
				Argv             []string `json:"argv"`
				WorkingDirectory string   `json:"workingDirectory"`
			}{Argv: command.Argv, WorkingDirectory: command.WorkingDirectory})
		}
		input.Goal = ""
		input.Criteria = nil
	}
	if (server.product || server.pathPiEnabled) && input.ExecutionProfile == app.TaskExecutionProfileRealSpecCoding && !server.taskWorktreesReady {
		writeError(writer, http.StatusServiceUnavailable, errors.New("Task worktree manager is unavailable; inspect readiness and restart Chora"))
		return
	}
	criteria := make([]domain.AcceptanceCriterion, 0, len(input.Criteria))
	if input.ExecutionProfile != app.TaskExecutionProfileRealSpecCoding {
		for _, title := range input.Criteria {
			criterion, err := domain.NewAcceptanceCriterion(domain.NewCriterionID(), strings.TrimSpace(title), "")
			if err != nil {
				writeError(writer, http.StatusBadRequest, err)
				return
			}
			criteria = append(criteria, criterion)
		}
	}
	revisionIDs := make([]domain.ContextRevisionID, 0, len(input.RevisionIDs))
	for _, value := range input.RevisionIDs {
		revisionID, err := domain.ParseContextRevisionID(value)
		if err != nil {
			writeError(writer, http.StatusBadRequest, err)
			return
		}
		revisionIDs = append(revisionIDs, revisionID)
	}
	planContent := domain.TechnicalPlanContent{TechnicalSteps: input.Plan.TechnicalSteps, Decisions: input.Plan.Decisions, Risks: input.Plan.Risks, Unknowns: input.Plan.Unknowns}
	if input.ExecutionProfile != app.TaskExecutionProfileRealSpecCoding && len(planContent.TechnicalSteps) == 0 && len(planContent.Decisions) == 0 && len(planContent.Risks) == 0 && len(planContent.Unknowns) == 0 {
		planContent = initialTechnicalPlanContent(input.Goal)
	}
	var realInput *app.RealSpecCodingInput
	if input.RealSpecCoding != nil {
		realInput = &app.RealSpecCodingInput{
			Requirement: input.RealSpecCoding.Requirement, Constraints: input.RealSpecCoding.Constraints,
			OutOfScope: input.RealSpecCoding.OutOfScope, WritableFiles: input.RealSpecCoding.WritableFiles, WritableDirectories: input.RealSpecCoding.WritableDirectories, NoChecks: input.RealSpecCoding.NoChecks,
		}
		for _, criterion := range input.RealSpecCoding.Criteria {
			realInput.Criteria = append(realInput.Criteria, app.RealSpecCodingCriterion{Title: criterion.Title, Description: criterion.Description, VerificationCommandIndexes: criterion.VerificationCommandIndexes})
		}
		for _, command := range input.RealSpecCoding.VerificationCommands {
			realInput.VerificationCommands = append(realInput.VerificationCommands, app.RealSpecCodingVerificationCommand{Argv: command.Argv, WorkingDirectory: command.WorkingDirectory})
		}
	}
	meta := requestCommandMeta(request, "create-task")
	if server.product || input.ExecutionProfile == app.TaskExecutionProfileRealSpecCoding {
		var ok bool
		meta, ok = requireRequestCommandMeta(writer, request, "create-task")
		if !ok {
			return
		}
	}
	result, err := server.service.CreateTask(request.Context(), app.CreateTaskRequest{
		CommandMeta: meta, RoomID: roomID, Title: input.Title, Goal: input.Goal, ExecutionProfile: input.ExecutionProfile,
		AgentExecutionProfile: input.AgentExecutionProfile, Criteria: criteria, RevisionIDs: revisionIDs, PlanContent: planContent, RealSpecCoding: realInput,
	})
	if err != nil {
		writePlanningCommandError(writer, err)
		return
	}
	view, err := server.createdTaskReferenceView(request.Context(), result)
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, view)
}

func (server *Server) createdTaskReferenceView(ctx context.Context, result app.CreateTaskResult) (taskRefView, error) {
	draft := technicalPlanDraftViewOf(result.Draft)
	view := taskReferenceView(result.Task, result.Selection, technicalPlanningView{
		Draft:     &draft,
		Revisions: []technicalPlanRevisionView{},
	})
	view.ExecutionProfile = string(result.ExecutionProfile)
	if result.AgentExecutionProfile != "" {
		view.AgentExecutionProfile = string(result.AgentExecutionProfile)
	}
	if result.Worktree != nil {
		worktree := taskWorktreeViewOf(*result.Worktree)
		view.Worktree = &worktree
	}
	if record, err := server.store.Reader().GetTaskResourceSnapshot(ctx, result.Task.ID()); err == nil {
		var snapshot domain.TaskResourceSnapshot
		if err = json.Unmarshal(record.CanonicalJSON, &snapshot); err != nil {
			return taskRefView{}, err
		}
		view.ResourceSnapshot = &snapshot
		return view, nil
	} else if !errors.Is(err, storecontract.ErrNotFound) {
		return taskRefView{}, err
	}
	if result.ExecutionProfile == app.TaskExecutionProfileRealSpecCoding {
		var repository speccoding.RepositoryIdentity
		if server.pathPiEnabled {
			intent, err := server.store.Reader().GetSpecCodingIntent(ctx, result.Task.ID())
			if err != nil || sha256.Sum256(intent.IntentJSON) != intent.IntentDigest {
				return taskRefView{}, errors.New("Local Connected Spec Coding intent is unavailable")
			}
			var frozen struct {
				Repository speccoding.RepositoryIdentity `json:"repository"`
			}
			if json.Unmarshal(intent.IntentJSON, &frozen) != nil || frozen.Repository.Name == "" || frozen.Repository.SourceRevision == "" {
				return taskRefView{}, errors.New("Local Connected repository identity is invalid")
			}
			repository = frozen.Repository
		} else {
			if server.installedEnvelope == nil {
				return taskRefView{}, errors.New("installed Real Spec Coding envelope is unavailable")
			}
			repository = server.installedEnvelope.Repository()
		}
		view.Repository = &repositoryIdentityView{
			Name: repository.Name, SourceRevision: repository.SourceRevision, BaselineDigest: repository.BaselineDigest,
		}
	}
	return view, nil
}

func (server *Server) getTask(writer http.ResponseWriter, request *http.Request) {
	taskID, err := domain.ParseTaskID(request.PathValue("taskID"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	view, err := server.taskReferenceView(request.Context(), taskID)
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

func (server *Server) saveTechnicalPlanDraft(writer http.ResponseWriter, request *http.Request) {
	taskID, err := domain.ParseTaskID(request.PathValue("taskID"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	draftID, err := domain.ParseTechnicalPlanDraftID(request.PathValue("draftID"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	var input struct {
		ExpectedEditVersion uint64   `json:"expectedEditVersion"`
		TechnicalSteps      []string `json:"technicalSteps"`
		Decisions           []string `json:"decisions"`
		Risks               []string `json:"risks"`
		Unknowns            []string `json:"unknowns"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	draft, err := server.store.Reader().GetTechnicalPlanDraft(request.Context(), draftID)
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	if draft.TaskID() != taskID {
		writeError(writer, http.StatusNotFound, storecontract.ErrNotFound)
		return
	}
	meta, ok := requireRequestCommandMeta(writer, request, "save-technical-plan-draft")
	if !ok {
		return
	}
	result, err := server.service.SaveTechnicalPlanDraft(request.Context(), app.SaveTechnicalPlanDraftRequest{
		CommandMeta: meta, DraftID: draftID, ExpectedEditVersion: input.ExpectedEditVersion,
		Content: domain.TechnicalPlanContent{TechnicalSteps: input.TechnicalSteps, Decisions: input.Decisions, Risks: input.Risks, Unknowns: input.Unknowns},
	})
	if err != nil {
		writePlanningCommandError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, saveTechnicalPlanDraftCommandView{Draft: technicalPlanDraftViewOf(result.Draft)})
}

func (server *Server) submitTechnicalPlanDraft(writer http.ResponseWriter, request *http.Request) {
	taskID, err := domain.ParseTaskID(request.PathValue("taskID"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	draftID, err := domain.ParseTechnicalPlanDraftID(request.PathValue("draftID"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	var input struct {
		ExpectedEditVersion uint64 `json:"expectedEditVersion"`
		ConfirmUnchanged    bool   `json:"confirmUnchanged"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	draft, err := server.store.Reader().GetTechnicalPlanDraft(request.Context(), draftID)
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	if draft.TaskID() != taskID {
		writeError(writer, http.StatusNotFound, storecontract.ErrNotFound)
		return
	}
	meta, ok := requireRequestCommandMeta(writer, request, "submit-technical-plan-draft")
	if !ok {
		return
	}
	result, err := server.service.SubmitTechnicalPlanDraft(request.Context(), app.SubmitTechnicalPlanDraftRequest{CommandMeta: meta, DraftID: draftID, ExpectedEditVersion: input.ExpectedEditVersion, ConfirmUnchanged: input.ConfirmUnchanged})
	if err != nil {
		writePlanningCommandError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, submitTechnicalPlanDraftCommandView{Draft: technicalPlanDraftViewOf(result.Draft), Revision: technicalPlanRevisionViewOf(result.Revision)})
}

func (server *Server) reviewTechnicalPlanRevision(writer http.ResponseWriter, request *http.Request) {
	taskID, err := domain.ParseTaskID(request.PathValue("taskID"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	revisionID, err := domain.ParseTechnicalPlanRevisionID(request.PathValue("revisionID"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	var input struct {
		Kind string `json:"kind"`
		Note string `json:"note"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	revision, err := server.store.Reader().GetTechnicalPlanRevision(request.Context(), revisionID)
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	if revision.TaskID() != taskID {
		writeError(writer, http.StatusNotFound, storecontract.ErrNotFound)
		return
	}
	meta, ok := requireRequestCommandMeta(writer, request, "review-technical-plan-revision")
	if !ok {
		return
	}
	result, err := server.service.ReviewTechnicalPlanRevision(request.Context(), app.ReviewTechnicalPlanRevisionRequest{CommandMeta: meta, RevisionID: revisionID, Kind: domain.TechnicalPlanReviewKind(input.Kind), Note: input.Note})
	if err != nil {
		writePlanningCommandError(writer, err)
		return
	}
	view := reviewTechnicalPlanRevisionCommandView{RevisionID: revisionID.String(), Review: technicalPlanReviewViewOf(result.Review)}
	if result.Draft != nil {
		draftView := technicalPlanDraftViewOf(*result.Draft)
		view.Draft = &draftView
	}
	if result.Binding != nil && result.Charter != nil {
		view.Acceptance = &technicalPlanAcceptanceView{
			RevisionID: result.Binding.RevisionID().String(), ReviewID: result.Binding.ReviewID().String(), SnapshotID: result.Binding.SnapshotID().String(),
			SnapshotDigest: fmt.Sprintf("%x", result.Binding.SnapshotDigest()), CharterID: result.Charter.ID().String(), AdapterID: result.Charter.AdapterID(),
			BoundAt: result.Binding.BoundAt().Format(time.RFC3339Nano),
		}
	}
	writeJSON(writer, http.StatusOK, view)
}

// activateTechnicalPlanRevision records Chora's non-human activation of a
// generated Plan inside the Room's already declared reversible boundary.
func (server *Server) activateTechnicalPlanRevision(writer http.ResponseWriter, request *http.Request) {
	taskID, err := domain.ParseTaskID(request.PathValue("taskID"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	revisionID, err := domain.ParseTechnicalPlanRevisionID(request.PathValue("revisionID"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if err := decodeOptionalJSON(request, &struct{}{}); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	revision, err := server.store.Reader().GetTechnicalPlanRevision(request.Context(), revisionID)
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	if revision.TaskID() != taskID {
		writeError(writer, http.StatusNotFound, storecontract.ErrNotFound)
		return
	}
	meta, ok := requireRequestCommandMeta(writer, request, "activate-technical-plan-revision")
	if !ok {
		return
	}
	meta.ActorID = systemActor
	meta.SessionID = systemSession
	result, err := server.service.ReviewTechnicalPlanRevision(request.Context(), app.ReviewTechnicalPlanRevisionRequest{
		CommandMeta: meta, RevisionID: revisionID, Kind: domain.TechnicalPlanReviewAccept,
		Note: "Automatically activated inside the Room's pre-declared reversible boundary.",
	})
	if err != nil {
		writePlanningCommandError(writer, err)
		return
	}
	view := reviewTechnicalPlanRevisionCommandView{RevisionID: revisionID.String(), Review: technicalPlanReviewViewOf(result.Review)}
	if result.Binding != nil && result.Charter != nil {
		view.Acceptance = &technicalPlanAcceptanceView{
			RevisionID: result.Binding.RevisionID().String(), ReviewID: result.Binding.ReviewID().String(), SnapshotID: result.Binding.SnapshotID().String(),
			SnapshotDigest: fmt.Sprintf("%x", result.Binding.SnapshotDigest()), CharterID: result.Charter.ID().String(), AdapterID: result.Charter.AdapterID(),
			BoundAt: result.Binding.BoundAt().Format(time.RFC3339Nano),
		}
	}
	writeJSON(writer, http.StatusOK, view)
}

func (server *Server) taskReferenceView(ctx context.Context, taskID domain.TaskID) (taskRefView, error) {
	reader := server.store.Reader()
	task, err := reader.GetTask(ctx, taskID)
	if err != nil {
		return taskRefView{}, err
	}
	selection, err := reader.GetTaskRevisionSelection(ctx, taskID)
	hasSelection := err == nil
	if err != nil && !(errors.Is(err, storecontract.ErrNotFound) && task.PredecessorTaskID().Valid()) {
		return taskRefView{}, err
	}
	planning := technicalPlanningView{Revisions: []technicalPlanRevisionView{}}
	draft, draftErr := reader.GetOpenTechnicalPlanDraft(ctx, taskID)
	if draftErr == nil {
		draftView := technicalPlanDraftViewOf(draft)
		planning.Draft = &draftView
	} else if !errors.Is(draftErr, storecontract.ErrNotFound) {
		return taskRefView{}, draftErr
	}
	revisions, err := reader.ListTechnicalPlanRevisions(ctx, taskID)
	if err != nil {
		return taskRefView{}, err
	}
	reviews, err := reader.ListTechnicalPlanReviews(ctx, taskID)
	if err != nil {
		return taskRefView{}, err
	}
	reviewByRevision := make(map[domain.TechnicalPlanRevisionID]domain.TechnicalPlanReview, len(reviews))
	for _, review := range reviews {
		reviewByRevision[review.RevisionID()] = review
	}
	acceptance, acceptanceErr := reader.GetCurrentTechnicalPlanAcceptance(ctx, taskID)
	for _, revision := range revisions {
		revisionView := technicalPlanRevisionViewOf(revision)
		if review, ok := reviewByRevision[revision.ID()]; ok {
			reviewView := technicalPlanReviewViewOf(review)
			revisionView.Review = &reviewView
		}
		revisionView.Current = acceptanceErr == nil && acceptance.RevisionID() == revision.ID()
		planning.Revisions = append(planning.Revisions, revisionView)
	}
	if acceptanceErr == nil {
		snapshot, err := reader.GetSnapshot(ctx, acceptance.SnapshotID())
		if err != nil || snapshot.Digest() != acceptance.SnapshotDigest() {
			return taskRefView{}, fmt.Errorf("accepted planning Snapshot drift")
		}
		charterID, err := snapshotCharterID(snapshot.CanonicalJSON())
		if err != nil {
			return taskRefView{}, err
		}
		charter, err := reader.GetCharter(ctx, charterID)
		if err != nil || charter.TaskID() != taskID {
			return taskRefView{}, fmt.Errorf("accepted planning Charter drift")
		}
		planning.Acceptance = &technicalPlanAcceptanceView{RevisionID: acceptance.RevisionID().String(), ReviewID: acceptance.ReviewID().String(), SnapshotID: acceptance.SnapshotID().String(), SnapshotDigest: fmt.Sprintf("%x", acceptance.SnapshotDigest()), CharterID: charter.ID().String(), AdapterID: charter.AdapterID(), BoundAt: acceptance.BoundAt().Format(time.RFC3339Nano)}
	} else if !errors.Is(acceptanceErr, storecontract.ErrNotFound) {
		return taskRefView{}, acceptanceErr
	}
	view := relatedTaskReferenceView(task, planning)
	if hasSelection {
		view = taskReferenceView(task, selection, planning)
	}
	view.ExecutionProfile = string(app.TaskExecutionProfileDiagnosticFake)
	intent, intentErr := reader.GetSpecCodingIntent(ctx, taskID)
	resourceRecord, resourceErr := reader.GetTaskResourceSnapshot(ctx, taskID)
	if resourceErr == nil {
		var snapshot domain.TaskResourceSnapshot
		if err = json.Unmarshal(resourceRecord.CanonicalJSON, &snapshot); err != nil {
			return taskRefView{}, err
		}
		view.ResourceSnapshot = &snapshot
		view.ExecutionProfile = string(app.TaskExecutionProfileRealSpecCoding)
		preference, err := reader.GetTaskAgentExecutionProfilePreference(ctx, taskID)
		if err != nil {
			return taskRefView{}, err
		}
		view.AgentExecutionProfile = string(preference.Profile())
	} else if !errors.Is(resourceErr, storecontract.ErrNotFound) {
		return taskRefView{}, resourceErr
	} else if intentErr == nil {
		if sha256.Sum256(intent.IntentJSON) != intent.IntentDigest {
			return taskRefView{}, fmt.Errorf("stored Real Spec Coding intent digest drift")
		}
		var frozen struct {
			Repository speccoding.RepositoryIdentity `json:"repository"`
		}
		if err := json.Unmarshal(intent.IntentJSON, &frozen); err != nil || frozen.Repository.Name == "" || frozen.Repository.SourceRevision == "" || (!server.pathPiEnabled && frozen.Repository.BaselineDigest == "") {
			return taskRefView{}, fmt.Errorf("stored Real Spec Coding repository identity is invalid")
		}
		view.ExecutionProfile = string(app.TaskExecutionProfileRealSpecCoding)
		preference, preferenceErr := reader.GetTaskAgentExecutionProfilePreference(ctx, taskID)
		if preferenceErr != nil {
			return taskRefView{}, preferenceErr
		}
		view.AgentExecutionProfile = string(preference.Profile())
		view.Repository = &repositoryIdentityView{Name: frozen.Repository.Name, SourceRevision: frozen.Repository.SourceRevision, BaselineDigest: frozen.Repository.BaselineDigest}
	} else if !errors.Is(intentErr, storecontract.ErrNotFound) {
		return taskRefView{}, intentErr
	}
	binding, bindingErr := reader.GetSpecCodingBinding(ctx, taskID)
	if bindingErr == nil {
		view.FrozenSnapshot = &frozenSnapshotRefView{ID: binding.SnapshotID.String(), Digest: fmt.Sprintf("%x", binding.SnapshotDigest), Status: binding.Status}
	} else if !errors.Is(bindingErr, storecontract.ErrNotFound) {
		return taskRefView{}, bindingErr
	}
	worktree, worktreeErr := reader.GetTaskWorktreeBinding(ctx, taskID)
	if worktreeErr == nil {
		item := taskWorktreeViewOf(worktree)
		view.Worktree = &item
	} else if !errors.Is(worktreeErr, storecontract.ErrNotFound) {
		return taskRefView{}, worktreeErr
	}
	return view, nil
}

func (server *Server) getRoomRevisions(writer http.ResponseWriter, request *http.Request) {
	roomID, err := domain.ParseRoomID(request.PathValue("roomID"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	revisions, err := server.store.Reader().ListRoomRevisions(request.Context(), roomID)
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	views := make([]roomRevisionView, 0, len(revisions))
	for _, revision := range revisions {
		views = append(views, roomRevisionViewOf(revision))
	}
	writeJSON(writer, http.StatusOK, views)
}

func (server *Server) startRun(writer http.ResponseWriter, request *http.Request) {
	taskID, err := domain.ParseTaskID(request.PathValue("taskID"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	task, err := server.store.Reader().GetTask(request.Context(), taskID)
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	if task.State() != domain.TaskStateOpen {
		writeError(writer, http.StatusConflict, storecontract.ErrVersionConflict)
		return
	}
	input, err := decodeStartRunInput(request)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	acceptance, acceptanceErr := server.store.Reader().GetCurrentTechnicalPlanAcceptance(request.Context(), task.ID())
	if acceptanceErr != nil {
		if errors.Is(acceptanceErr, storecontract.ErrNotFound) {
			writeError(writer, http.StatusConflict, errors.New("task has no accepted technical plan revision"))
			return
		}
		writePlanningCommandError(writer, acceptanceErr)
		return
	}
	revisionID := acceptance.RevisionID()
	if strings.TrimSpace(input.RevisionID) != "" {
		revisionID, err = domain.ParseTechnicalPlanRevisionID(input.RevisionID)
		if err != nil {
			writeError(writer, http.StatusBadRequest, errors.New("invalid technical plan revisionId"))
			return
		}
		if revisionID != acceptance.RevisionID() {
			writeError(writer, http.StatusConflict, storecontract.ErrVersionConflict)
			return
		}
	}
	snapshot, err := server.store.Reader().GetSnapshot(request.Context(), acceptance.SnapshotID())
	if err != nil || snapshot.Digest() != acceptance.SnapshotDigest() {
		writeError(writer, http.StatusConflict, errors.New("accepted Context Snapshot is unavailable or changed"))
		return
	}
	charterID, err := snapshotCharterID(snapshot.CanonicalJSON())
	if err != nil {
		writeMutationConflictError(writer, err)
		return
	}
	charter, err := server.store.Reader().GetCharter(request.Context(), charterID)
	if err != nil || charter.TaskID() != task.ID() {
		writeError(writer, http.StatusConflict, errors.New("accepted Run Charter is unavailable or changed"))
		return
	}
	adapterID := charter.AdapterID()
	if adapterID != "fake" && adapterID != agentpi.AdapterID {
		writeError(writer, http.StatusConflict, errors.New("accepted technical plan uses an unsupported execution profile"))
		return
	}
	meta, ok := requestCommandMetaForProduct(writer, request, "create-run", server.product)
	if !ok {
		return
	}
	workspace, err := server.service.GetRoomWorkspace(request.Context(), task.RoomID())
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	startIsCurrentAction := false
	for _, summary := range workspace.Tasks {
		if summary.Task.ID() == task.ID() {
			startIsCurrentAction = summary.CurrentAction.Kind == app.CurrentActionStartRun
			break
		}
	}
	if adapterID == agentpi.AdapterID && startIsCurrentAction {
		preference, preferenceErr := server.store.Reader().GetTaskAgentExecutionProfilePreference(request.Context(), task.ID())
		if preferenceErr != nil {
			writeStoreError(writer, preferenceErr)
			return
		}
		profileBinding, bindingErr := domain.NewAgentExecutionProfileBinding(preference.Profile())
		if bindingErr != nil {
			writeError(writer, http.StatusConflict, errors.New("Task Agent execution profile is invalid"))
			return
		}
		if !server.requireAgentExecutionRoute(writer, request, profileBinding) {
			return
		}
	}
	if server.product && adapterID == agentpi.AdapterID && (!server.verifierStatus.Enabled || server.verifier == nil) {
		writeError(writer, http.StatusServiceUnavailable, fmt.Errorf("independent Verifier is disabled: %s", server.verifierStatus.Reason))
		return
	}
	created, err := server.service.CreateRun(request.Context(), app.CreateRunRequest{CommandMeta: meta, TaskID: task.ID(), RevisionID: revisionID})
	if err != nil {
		writePlanningCommandError(writer, err)
		return
	}
	if created.Binding.CharterID() != charter.ID() || created.Run.CharterID() != charter.ID() || created.Binding.SnapshotID() != snapshot.ID() || created.Binding.SnapshotDigest() != snapshot.Digest() {
		writeError(writer, http.StatusConflict, errors.New("created Run substituted accepted planning identity"))
		return
	}
	if !startIsCurrentAction {
		view, viewErr := server.runView(request.Context(), created.Run.ID())
		if viewErr != nil {
			writeMutationError(writer, http.StatusInternalServerError, viewErr)
			return
		}
		writeJSON(writer, http.StatusCreated, view)
		return
	}
	selection, selectionErr := server.store.Reader().GetTaskRevisionSelection(request.Context(), task.ID())
	if selectionErr != nil {
		writeStoreError(writer, selectionErr)
		return
	}
	snapshotEvidence := false
	for _, item := range selection.Selected() {
		if item.Provenance.Kind == domain.RevisionProvenanceRunCandidate {
			snapshotEvidence = true
			break
		}
	}
	var productionFake *agentfake.Adapter
	if adapterID == "fake" {
		terminal, terminalErr := fakeTerminalResult(task.Criteria(), false)
		if terminalErr != nil {
			writeError(writer, http.StatusInternalServerError, terminalErr)
			return
		}
		plan := agentfake.Plan{
			AdapterID: adapterID, Executable: "/fake", Arguments: []string{"run", "--json"},
			Environment: map[string]string{"CHORA_FAKE": "1"}, ResumeMode: execution.ResumeExplicitSession, TerminalResult: terminal,
			SnapshotEvidence: snapshotEvidence,
		}
		if !snapshotEvidence {
			plan.DecisionGate = &agentfake.DecisionGatePlan{
				Question: "Should the Agent continue with the frozen Room evidence?",
				Context:  "The Agent has reached a bounded execution choice before producing its Result.",
				Options: []agentfake.DecisionOption{
					{ID: "continue", Label: "Continue", Impact: "Complete the structured Result from the frozen input."},
					{ID: "stop", Label: "Stop for revision", Impact: "Return a revision-required Result without accepting output."},
				},
				Recommendation: "Continue with the frozen evidence.", Impact: "This choice resumes execution; it does not accept the Result.",
			}
		}
		productionFake = agentfake.NewAdapter(plan)
		server.registry.Set(productionFake)
	} else {
		productionFake, err = server.e2eStartRunAdapter(input, task.Criteria(), created.Run.ID(), adapterID, snapshotEvidence)
		if err != nil {
			writeError(writer, http.StatusInternalServerError, err)
			return
		}
	}
	prepared, err := server.prepareAndBindManagedAttempt(request.Context(), func(prepareCtx context.Context) (app.PrepareRunResult, error) {
		return server.service.PrepareRun(prepareCtx, app.PrepareRunRequest{
			CommandMeta: childCommandMeta(meta, "prepare-run"), RunID: created.Run.ID(), ExpectedVersion: created.Run.Version(),
		})
	})
	if err != nil {
		writeMutationError(writer, http.StatusInternalServerError, err)
		return
	}
	started, err := server.startManagedAttempt(request.Context(), prepared.Attempt, app.StartAttemptRequest{
		CommandMeta: childCommandMeta(meta, "start-run"), RunID: created.Run.ID(), ExpectedVersion: prepared.Run.Version(), Mode: app.StartFresh,
	})
	if err != nil || started.Session == nil {
		if refreshErr := server.publishManagedGenerationReferences(request.Context(), nil); refreshErr != nil {
			err = errors.Join(err, refreshErr)
		}
		if err == nil {
			err = errors.New("runtime did not create a session")
		}
		writeMutationError(writer, http.StatusInternalServerError, err)
		return
	}
	if productionFake != nil {
		fakeRuntime := server.fakeSupervisor
		if adapterID != "fake" {
			server.supervisor.mu.RLock()
			routed, ok := server.supervisor.byAdapter[adapterID].(*fakeSupervisor)
			server.supervisor.mu.RUnlock()
			if !ok {
				writeError(writer, http.StatusInternalServerError, errors.New("fixture runtime is not a Fake supervisor"))
				return
			}
			fakeRuntime = routed
		}
		go server.completeFakeRunOn(started.Run.ID(), started.Session.ID, productionFake, fakeRuntime)
	} else if started.Session.Identity.Valid() {
		go server.monitorRuntimeRun(adapterID, started.Run.ID(), started.Session.ID, started.Session.Identity)
	}
	view, err := server.runView(request.Context(), started.Run.ID())
	if err != nil {
		writeMutationError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusCreated, view)
}

func snapshotCharterID(canonical []byte) (domain.CharterID, error) {
	var document struct {
		Charter struct {
			ID string `json:"id"`
		} `json:"charter"`
	}
	if err := json.Unmarshal(canonical, &document); err != nil {
		return domain.CharterID{}, errors.New("invalid registered Spec Coding Snapshot")
	}
	return domain.ParseCharterID(document.Charter.ID)
}

func (server *Server) getRun(writer http.ResponseWriter, request *http.Request) {
	runID, err := domain.ParseRunID(request.PathValue("runID"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	view, err := server.runView(request.Context(), runID)
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

func (server *Server) getReviewPatch(writer http.ResponseWriter, request *http.Request) {
	runID, err := domain.ParseRunID(request.PathValue("runID"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	change, err := server.service.LoadReviewableChange(request.Context(), runID)
	if err != nil {
		writeMutationConflictError(writer, err)
		return
	}
	digest := fmt.Sprintf("%x", change.Patch.PatchDigest)
	writer.Header().Set("Content-Type", "text/x-diff; charset=utf-8")
	writer.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="chora-%s.patch"`, runID.String()))
	writer.Header().Set("ETag", `"sha256:`+digest+`"`)
	writer.Header().Set("X-Chora-Patch-SHA256", digest)
	writer.Header().Set("X-Chora-Result-ID", change.Binding.ResultID.String())
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(change.Patch.Raw)
}

func (server *Server) startVerification(writer http.ResponseWriter, request *http.Request) {
	server.changeVerification(writer, request, "start")
}

func (server *Server) retryVerification(writer http.ResponseWriter, request *http.Request) {
	server.changeVerification(writer, request, "retry")
}

func (server *Server) changeVerification(writer http.ResponseWriter, request *http.Request, action string) {
	runID, err := domain.ParseRunID(request.PathValue("runID"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if err := decodeOptionalJSON(request, &struct{}{}); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	run, err := server.store.Reader().GetRun(request.Context(), runID)
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	charter, err := server.store.Reader().GetCharter(request.Context(), run.CharterID())
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	meta, ok := requestCommandMetaForProduct(writer, request, action+"-verification", server.product)
	if !ok {
		return
	}
	if strings.EqualFold(strings.TrimSpace(request.Header.Get("X-Chora-Automatic")), "true") {
		meta.ActorID = systemActor
		meta.SessionID = systemSession
	}
	if charter.AdapterID() == agentpi.AdapterID {
		if !server.requirePiPreflight(writer, request) {
			return
		}
		if server.product && (!server.verifierStatus.Enabled || server.verifier == nil) {
			writeError(writer, http.StatusServiceUnavailable, fmt.Errorf("independent Verifier is disabled: %s", server.verifierStatus.Reason))
			return
		}
		if !server.piStatus.Enabled {
			writeError(writer, http.StatusServiceUnavailable, fmt.Errorf("Pi adapter is disabled: %s", server.piStatus.Reason))
			return
		}
	}
	input := app.StartVerificationRequest{CommandMeta: meta, RunID: runID, ExpectedVersion: run.Version()}
	if action == "retry" {
		_, err = server.service.RetryVerification(request.Context(), input)
	} else {
		_, err = server.service.StartVerification(request.Context(), input)
	}
	if err != nil {
		writeMutationConflictError(writer, err)
		return
	}
	view, err := server.runView(request.Context(), runID)
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, view)
}

func (server *Server) cancelVerification(writer http.ResponseWriter, request *http.Request) {
	runID, err := domain.ParseRunID(request.PathValue("runID"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if err := decodeOptionalJSON(request, &struct{}{}); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	run, err := server.store.Reader().GetRun(request.Context(), runID)
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	meta, ok := requestCommandMetaForProduct(writer, request, "cancel-verification", server.product)
	if !ok {
		return
	}
	if _, err := server.service.CancelVerification(request.Context(), app.CancelVerificationRequest{CommandMeta: meta, RunID: runID, ExpectedVersion: run.Version()}); err != nil {
		writeMutationConflictError(writer, err)
		return
	}
	view, err := server.runView(request.Context(), runID)
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, view)
}

func (server *Server) getRunEvents(writer http.ResponseWriter, request *http.Request) {
	runID, err := domain.ParseRunID(request.PathValue("runID"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	after, err := nonNegativeQueryInt(request, "after", 0)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	limit, err := nonNegativeQueryInt(request, "limit", 50)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if limit > 100 {
		limit = 100
	}
	events, err := server.store.Reader().ListRunEvents(request.Context(), runID)
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	result := make([]map[string]any, 0, min(len(events), int(limit)))
	for _, event := range events {
		if event.Sequence() <= after {
			continue
		}
		if int64(len(result)) == limit {
			break
		}
		result = append(result, map[string]any{
			"sequence":   event.Sequence(),
			"type":       event.Type(),
			"source":     event.Source(),
			"occurredAt": event.OccurredAt().Format(time.RFC3339Nano),
			"recordedAt": event.RecordedAt().Format(time.RFC3339Nano),
			"normalized": json.RawMessage(event.NormalizedJSON()),
		})
	}
	writeJSON(writer, http.StatusOK, map[string]any{"runId": runID.String(), "events": result})
}

func nonNegativeQueryInt(request *http.Request, name string, fallback int64) (int64, error) {
	values, present := request.URL.Query()[name]
	if !present {
		return fallback, nil
	}
	if len(values) != 1 || values[0] == "" {
		return 0, fmt.Errorf("%s must be a non-negative integer", name)
	}
	value, err := strconv.ParseInt(values[0], 10, 64)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("%s must be a non-negative integer", name)
	}
	return value, nil
}

func (server *Server) resolveDecisionGate(writer http.ResponseWriter, request *http.Request) {
	gateID, err := domain.ParseDecisionGateID(request.PathValue("gateID"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	var input struct {
		OptionID string `json:"optionId"`
		Note     string `json:"note"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	input.OptionID = strings.TrimSpace(input.OptionID)
	input.Note = strings.TrimSpace(input.Note)
	if input.OptionID == "" || len([]rune(input.Note)) > domain.MaxDecisionGateResolutionNoteRunes {
		writeError(writer, http.StatusBadRequest, errors.New("a bounded Decision Gate option and valid note are required"))
		return
	}
	meta, ok := requestCommandMetaForProduct(writer, request, "resolve-execution-decision", server.product)
	if !ok {
		return
	}
	gate, err := server.store.Reader().GetDecisionGate(request.Context(), gateID)
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	run, err := server.store.Reader().GetRun(request.Context(), gate.RunID())
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	if _, err := server.service.ResolveExecutionDecision(request.Context(), app.ResolveExecutionDecisionRequest{
		CommandMeta: meta, GateID: gateID, RunID: run.ID(), ExpectedVersion: run.Version(), OptionID: input.OptionID, Note: input.Note,
	}); err != nil {
		writeMutationConflictError(writer, err)
		return
	}
	view, err := server.runView(request.Context(), run.ID())
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

func (server *Server) cancelRun(writer http.ResponseWriter, request *http.Request) {
	runID, err := domain.ParseRunID(request.PathValue("runID"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	var input struct {
		Reason string `json:"reason"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	input.Reason = strings.TrimSpace(input.Reason)
	if input.Reason == "" {
		writeError(writer, http.StatusBadRequest, errors.New("cancel reason is required"))
		return
	}
	if len([]rune(input.Reason)) > maxCancelReasonLength {
		writeError(writer, http.StatusBadRequest, errors.New("cancel reason is too long"))
		return
	}
	meta, ok := requestCommandMetaForProduct(writer, request, "cancel-run", server.product)
	if !ok {
		return
	}
	run, err := server.store.Reader().GetRun(request.Context(), runID)
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	if _, err := server.service.RequestCancel(request.Context(), app.StopRequest{
		CommandMeta: meta, RunID: runID, ExpectedVersion: run.Version(), Reason: input.Reason, AllowRetry: true,
	}); err != nil {
		writeMutationConflictError(writer, err)
		return
	}
	view, err := server.runView(request.Context(), runID)
	if err != nil {
		writeMutationError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

func (server *Server) reviewRun(writer http.ResponseWriter, request *http.Request) {
	runID, err := domain.ParseRunID(request.PathValue("runID"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	var input struct {
		ExpectedVersion *uint64 `json:"expectedVersion"`
		Kind            string  `json:"kind"`
		reviewCommentInput
		RejectionClass string `json:"rejectionClass"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	comment, err := input.value()
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	comment = strings.TrimSpace(comment)
	if len([]rune(comment)) > maxReviewNoteLength {
		writeError(writer, http.StatusBadRequest, errors.New("comment is too long"))
		return
	}
	kind := domain.ReviewDecisionKind(input.Kind)
	if kind != domain.ReviewDecisionAccept && kind != domain.ReviewDecisionReject {
		writeError(writer, http.StatusBadRequest, errors.New("review kind must be accept or reject"))
		return
	}
	if kind == domain.ReviewDecisionReject && comment == "" {
		writeError(writer, http.StatusBadRequest, errors.New("comment is required"))
		return
	}
	meta, ok := requestCommandMetaForProduct(writer, request, "review-run", server.product)
	if !ok {
		return
	}
	run, err := server.store.Reader().GetRun(request.Context(), runID)
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	attempt, attemptErr := server.store.Reader().GetCurrentAttempt(request.Context(), runID)
	if attemptErr == nil {
		_, verificationErr := server.store.Reader().GetVerificationRunForAgentAttempt(request.Context(), attempt.ID())
		if errors.Is(verificationErr, storecontract.ErrNotFound) {
			_, verificationErr = server.store.Reader().GetLocalReviewResultForAgentAttempt(request.Context(), attempt.ID())
		}
		if verificationErr == nil {
			if input.ExpectedVersion == nil {
				writeError(writer, http.StatusBadRequest, errors.New("expectedVersion is required for verified Result review"))
				return
			}
			if comment == "" {
				writeError(writer, http.StatusBadRequest, errors.New("verified Result review reason is required"))
				return
			}
			class := domain.ReviewRejectionClass(input.RejectionClass)
			if kind == domain.ReviewDecisionReject && class != domain.ReviewRejectionImplementationGap && class != domain.ReviewRejectionPlanningGap && class != domain.ReviewRejectionContractChangeRequired {
				writeError(writer, http.StatusBadRequest, errors.New("rejectionClass must be implementation_gap, planning_gap, or contract_change_required"))
				return
			}
			if kind == domain.ReviewDecisionAccept && class != "" {
				writeError(writer, http.StatusBadRequest, errors.New("Accept cannot include a rejectionClass"))
				return
			}
			_, err = server.service.ReviewVerifiedResult(request.Context(), app.VerifiedReviewRequest{
				CommandMeta: meta, RunID: runID, ExpectedVersion: *input.ExpectedVersion,
				Kind: kind, Reason: comment, RejectionClass: class, ActorID: localActor, SessionID: localSession,
			})
			if err != nil {
				writeMutationConflictError(writer, err)
				return
			}
			view, err := server.runView(request.Context(), runID)
			if err != nil {
				writeError(writer, http.StatusInternalServerError, err)
				return
			}
			writeJSON(writer, http.StatusOK, view)
			return
		} else if !errors.Is(verificationErr, storecontract.ErrNotFound) {
			writeStoreError(writer, verificationErr)
			return
		}
	} else if !errors.Is(attemptErr, storecontract.ErrNotFound) {
		writeStoreError(writer, attemptErr)
		return
	}
	var proposals []app.CandidateProposal
	if kind == domain.ReviewDecisionAccept {
		attempt, projectionErr := server.store.Reader().GetCurrentAttempt(request.Context(), runID)
		if projectionErr != nil {
			writeStoreError(writer, projectionErr)
			return
		}
		projection, projectionErr := server.store.Reader().GetTerminalProjection(request.Context(), attempt.ID())
		if projectionErr != nil {
			writeStoreError(writer, projectionErr)
			return
		}
		for _, artifact := range projection.Artifacts {
			if artifact.Role != "candidate" {
				continue
			}
			body := strings.TrimSpace(projection.Summary.Body)
			if body == "" {
				body = strings.TrimSpace(artifact.Description)
			}
			proposals = append(proposals, app.CandidateProposal{ArtifactID: artifact.ID, Title: "Candidate from " + artifact.Locator, Body: body})
		}
	}
	_, err = server.service.Review(request.Context(), app.ReviewRequest{
		CommandMeta: meta, RunID: runID, ExpectedVersion: run.Version(), Kind: kind, Comment: comment,
		CandidateProposals: proposals,
	})
	if err != nil {
		writeMutationConflictError(writer, err)
		return
	}
	view, err := server.runView(request.Context(), runID)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

func (server *Server) applyRunPatch(writer http.ResponseWriter, request *http.Request) {
	runID, err := domain.ParseRunID(request.PathValue("runID"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	var input struct {
		ExpectedVersion *uint64 `json:"expectedVersion"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if input.ExpectedVersion == nil {
		writeError(writer, http.StatusBadRequest, errors.New("Apply expectedVersion is required"))
		return
	}
	meta, ok := requestCommandMetaForProduct(writer, request, "apply-run-patch", server.product)
	if !ok {
		return
	}
	if _, err := server.service.ApplyAcceptedPatch(request.Context(), app.ApplyAcceptedPatchRequest{
		CommandMeta: meta, RunID: runID, ExpectedVersion: *input.ExpectedVersion,
	}); err != nil {
		writeMutationConflictError(writer, err)
		return
	}
	view, err := server.runView(request.Context(), runID)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

func (server *Server) retryRun(writer http.ResponseWriter, request *http.Request) {
	runID, err := domain.ParseRunID(request.PathValue("runID"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	var input struct {
		ExpectedVersion *uint64 `json:"expectedVersion"`
		Instructions    string  `json:"instructions"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	input.Instructions = strings.TrimSpace(input.Instructions)
	if input.Instructions == "" {
		writeError(writer, http.StatusBadRequest, errors.New("retry instructions are required"))
		return
	}
	if len([]rune(input.Instructions)) > maxRetryInstructionLength {
		writeError(writer, http.StatusBadRequest, errors.New("retry instructions are too long"))
		return
	}
	meta, ok := requestCommandMetaForProduct(writer, request, "retry-run", server.product)
	if !ok {
		return
	}

	reader := server.store.Reader()
	run, err := reader.GetRun(request.Context(), runID)
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	if input.ExpectedVersion == nil {
		writeError(writer, http.StatusBadRequest, errors.New("retry expectedVersion is required"))
		return
	}
	if run.Version() != *input.ExpectedVersion {
		writeError(writer, http.StatusConflict, storecontract.ErrVersionConflict)
		return
	}
	recoveryRetry := run.State() == domain.RunStateRecoveryRequired
	cancelRetry := run.State() == domain.RunStateCancelled
	automaticRetry, automaticRetryErr := server.service.AutomaticRetryForRun(request.Context(), runID)
	if automaticRetryErr != nil {
		writeStoreError(writer, automaticRetryErr)
		return
	}
	blockedPreparedRetry := run.State() == domain.RunStateReady && automaticRetry != nil && automaticRetry.State == app.AutomaticRetryBlocked
	if run.State() != domain.RunStateRevisionRequired && !recoveryRetry && !cancelRetry && !blockedPreparedRetry {
		writeError(writer, http.StatusConflict, errors.New("run is not ready for retry"))
		return
	}
	attempt, err := reader.GetCurrentAttempt(request.Context(), runID)
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	if blockedPreparedRetry {
		server.startBlockedAutomaticRetry(writer, request, run, attempt, meta)
		return
	}
	if run.State() == domain.RunStateRevisionRequired {
		_, verificationErr := reader.GetVerificationRunForAgentAttempt(request.Context(), attempt.ID())
		if errors.Is(verificationErr, storecontract.ErrNotFound) {
			_, verificationErr = reader.GetLocalReviewResultForAgentAttempt(request.Context(), attempt.ID())
		}
		if verificationErr == nil {
			server.retryVerifiedAgent(writer, request, run, *input.ExpectedVersion, input.Instructions, meta)
			return
		} else if !errors.Is(verificationErr, storecontract.ErrNotFound) {
			writeStoreError(writer, verificationErr)
			return
		}
	}
	rejectionNote := ""
	if recoveryRetry {
		session, sessionErr := reader.GetRuntimeSessionForAttempt(request.Context(), attempt.ID())
		if sessionErr != nil {
			writeStoreError(writer, sessionErr)
			return
		}
		if !recoveryRetryReady(run.State(), attempt.State(), session) {
			writeError(writer, http.StatusConflict, errors.New("recovery-required run has no proven terminal runtime"))
			return
		}
		if session.State == "failed" {
			rejectionNote = "The Agent launch failed before a child process existed; owned launch resources were finalized."
		} else if attempt.State() == domain.AttemptStateFailed {
			rejectionNote = "The Agent process exited nonzero; its terminal Result was rejected and owned runtime resources were finalized."
		} else {
			rejectionNote = "The recovered runtime was terminated and cleaned after backend restart."
		}
	} else if cancelRetry {
		session, sessionErr := reader.GetRuntimeSessionForAttempt(request.Context(), attempt.ID())
		if sessionErr != nil {
			writeStoreError(writer, sessionErr)
			return
		}
		events, eventsErr := reader.ListRunEvents(request.Context(), runID)
		if eventsErr != nil {
			writeStoreError(writer, eventsErr)
			return
		}
		var allowed bool
		rejectionNote, allowed = app.CancelRetryAuthority(events, attempt.ID())
		if !allowed || !cancelledRetryReady(run.State(), attempt.State(), session, true) {
			writeError(writer, http.StatusConflict, errors.New("cancelled run has no finalized retry authority"))
			return
		}
	} else {
		review, reviewErr := reader.GetLatestReviewForRun(request.Context(), runID)
		switch {
		case reviewErr == nil:
			if review.Kind() != domain.ReviewDecisionReject {
				writeError(writer, http.StatusConflict, errors.New("run is not ready for retry"))
				return
			}
			rejectionNote = strings.TrimSpace(review.Comment())
		case errors.Is(reviewErr, storecontract.ErrNotFound):
			gate, gateErr := reader.GetLatestDecisionGateForRun(request.Context(), runID)
			if gateErr == nil && gate.Status() == domain.DecisionGateResolved && gate.AttemptID() == attempt.ID() {
				rejectionNote = strings.TrimSpace(gate.Note())
				if rejectionNote == "" {
					rejectionNote = fmt.Sprintf("Execution Decision Gate selected %q.", gate.SelectedOptionID())
				}
			} else if errors.Is(gateErr, storecontract.ErrNotFound) && attempt.State() == domain.AttemptStateOutputSubmitted {
				rejectionNote = "The structured Result requires revision before retry."
			} else if gateErr != nil && !errors.Is(gateErr, storecontract.ErrNotFound) {
				writeStoreError(writer, gateErr)
				return
			} else {
				writeError(writer, http.StatusConflict, errors.New("revision-required run has no persisted retry authority"))
				return
			}
		default:
			writeStoreError(writer, reviewErr)
			return
		}
	}
	if rejectionNote == "" || len([]rune(rejectionNote)) > maxReviewNoteLength {
		writeError(writer, http.StatusConflict, errors.New("persisted rejection note is invalid"))
		return
	}
	unmetCriterionIDs := []string{}
	if !recoveryRetry && !cancelRetry {
		projection, err := reader.GetTerminalProjection(request.Context(), attempt.ID())
		if err != nil {
			writeStoreError(writer, err)
			return
		}
		unmetCriterionIDs = make([]string, 0, len(projection.Checks))
		for _, check := range projection.Checks {
			if check.Status != "passed" {
				unmetCriterionIDs = append(unmetCriterionIDs, check.CriterionID)
			}
		}
	}
	envelope, err := json.Marshal(retryDeltaEnvelope{
		SchemaVersion: "chora.retry-delta.v1", RejectionNote: rejectionNote,
		UnmetCriterionIDs: unmetCriterionIDs, Instructions: input.Instructions,
	})
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	task, err := reader.GetTask(request.Context(), run.TaskID())
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	charter, err := reader.GetCharter(request.Context(), run.CharterID())
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	var productionFake *agentfake.Adapter
	if charter.AdapterID() == "fake" {
		terminal, terminalErr := fakeTerminalResult(task.Criteria(), true)
		if terminalErr != nil {
			writeError(writer, http.StatusInternalServerError, terminalErr)
			return
		}
		productionFake = agentfake.NewAdapter(agentfake.Plan{
			AdapterID: "fake", Executable: "/fake", Arguments: []string{"run", "--json"},
			Environment: map[string]string{"CHORA_FAKE": "1"}, ResumeMode: execution.ResumeExplicitSession, TerminalResult: terminal,
		})
		server.registry.Set(productionFake)
	} else if charter.AdapterID() == codexcli.AdapterID {
		if !server.codexStatus.Enabled {
			writeError(writer, http.StatusServiceUnavailable, fmt.Errorf("Codex adapter is disabled: %s", server.codexStatus.Reason))
			return
		}
		if _, statErr := os.Stat(charter.WorkspaceRoot()); statErr != nil {
			writeError(writer, http.StatusServiceUnavailable, fmt.Errorf("Codex retry workspace is unavailable: %w", statErr))
			return
		}
	} else if charter.AdapterID() == agentpi.AdapterID {
		if !server.requireAgentExecutionRoute(writer, request, attempt.AgentExecutionProfileBinding()) {
			return
		}
		if server.product && (!server.verifierStatus.Enabled || server.verifier == nil) {
			writeError(writer, http.StatusServiceUnavailable, fmt.Errorf("independent Verifier is disabled: %s", server.verifierStatus.Reason))
			return
		}
		_, resourceErr := reader.GetTaskResourceSnapshot(request.Context(), task.ID())
		if resourceErr != nil && !errors.Is(resourceErr, storecontract.ErrNotFound) {
			writeStoreError(writer, resourceErr)
			return
		}
		// A multi-repository charter names a logical Project, not a checkout.
		// PrepareRetry proves every frozen Task worktree before preparing a successor.
		if server.product || errors.Is(resourceErr, storecontract.ErrNotFound) {
			if workspaceErr := validatePiRetryWorkspaceAuthority(server.product, charter.WorkspaceRoot()); workspaceErr != nil {
				writeError(writer, http.StatusServiceUnavailable, fmt.Errorf("Pi retry source repository is unavailable: %w", workspaceErr))
				return
			}
		}
	} else {
		writeError(writer, http.StatusConflict, fmt.Errorf("run adapter %q is disabled", charter.AdapterID()))
		return
	}
	prepared, err := server.prepareAndBindManagedAttempt(request.Context(), func(prepareCtx context.Context) (app.PrepareRunResult, error) {
		return server.service.PrepareRetry(prepareCtx, app.PrepareRetryRequest{
			CommandMeta: childCommandMeta(meta, "prepare-retry"), RunID: runID, ExpectedVersion: *input.ExpectedVersion,
			Reason: rejectionNote, Instructions: string(envelope),
		})
	})
	if err != nil {
		writeMutationConflictError(writer, err)
		return
	}
	startMode := app.StartResumeRecordedSession
	// Failed or incompatible Pi sessions start fresh. An interrupted local
	// session is resumed only by this explicit action, through the existing
	// recorded-session identity and workspace checks.
	if charter.AdapterID() == agentpi.AdapterID && ((recoveryRetry && attempt.State() != domain.AttemptStateInterrupted) || attempt.AgentExecutionProfileBinding().ExecutionProvider() != domain.TrustedHostExecutionProvider) {
		startMode = app.StartFresh
	}
	started, err := server.startManagedAttempt(request.Context(), prepared.Attempt, app.StartAttemptRequest{
		CommandMeta: childCommandMeta(meta, "start-retry"), RunID: runID, ExpectedVersion: prepared.Run.Version(), Mode: startMode,
	})
	if err != nil || started.Session == nil {
		if refreshErr := server.publishManagedGenerationReferences(request.Context(), nil); refreshErr != nil {
			err = errors.Join(err, refreshErr)
		}
		if err == nil {
			err = errors.New("retry did not create a session")
		}
		writeMutationError(writer, http.StatusInternalServerError, err)
		return
	}
	if charter.AdapterID() == "fake" {
		go server.completeFakeRun(started.Run.ID(), started.Session.ID, productionFake)
	} else if started.Session.Identity.Valid() {
		go server.monitorRuntimeRun(charter.AdapterID(), started.Run.ID(), started.Session.ID, started.Session.Identity)
	}
	view, err := server.runView(request.Context(), runID)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

func (server *Server) startBlockedAutomaticRetry(writer http.ResponseWriter, request *http.Request, run domain.AgentRun, attempt domain.Attempt, meta app.CommandMeta) {
	if _, err := server.store.Reader().GetRuntimeSessionForAttempt(request.Context(), attempt.ID()); !errors.Is(err, storecontract.ErrNotFound) {
		if err == nil {
			writeError(writer, http.StatusConflict, errors.New("prepared automatic retry already has runtime state"))
		} else {
			writeStoreError(writer, err)
		}
		return
	}
	if err := server.automaticRetryRouteError(request.Context(), attempt); err != nil {
		writeMutationError(writer, http.StatusServiceUnavailable, err)
		return
	}
	if err := server.bindAndPublishManagedAttempt(request.Context(), attempt); err != nil {
		writeMutationError(writer, http.StatusServiceUnavailable, err)
		return
	}
	started, err := server.startManagedAttempt(request.Context(), attempt, app.StartAttemptRequest{
		CommandMeta: childCommandMeta(meta, "start-blocked-automatic-retry"), RunID: run.ID(), ExpectedVersion: run.Version(), Mode: app.StartFresh,
	})
	if err != nil || started.Session == nil || !started.Session.Identity.Valid() {
		if err == nil {
			err = errors.New("retry did not create a live owned session")
		}
		writeMutationError(writer, http.StatusInternalServerError, err)
		return
	}
	if adapter, ok := server.automaticRetryFakeAdapter(); ok {
		go server.completeFakeRunOn(started.Run.ID(), started.Session.ID, adapter.adapter, adapter.runtime)
	} else {
		go server.monitorRuntimeRun(attempt.AdapterID(), started.Run.ID(), started.Session.ID, started.Session.Identity)
	}
	view, err := server.runView(request.Context(), run.ID())
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

func (server *Server) retryVerifiedAgent(writer http.ResponseWriter, request *http.Request, run domain.AgentRun, expectedVersion uint64, instructions string, meta app.CommandMeta) {
	reader := server.store.Reader()
	task, err := reader.GetTask(request.Context(), run.TaskID())
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	charter, err := reader.GetCharter(request.Context(), run.CharterID())
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	var productionFake *agentfake.Adapter
	switch charter.AdapterID() {
	case "fake":
		terminal, terminalErr := fakeTerminalResult(task.Criteria(), true)
		if terminalErr != nil {
			writeError(writer, http.StatusInternalServerError, terminalErr)
			return
		}
		productionFake = agentfake.NewAdapter(agentfake.Plan{
			AdapterID: "fake", Executable: "/fake", Arguments: []string{"run", "--json"}, Environment: map[string]string{"CHORA_FAKE": "1"},
			ResumeMode: execution.ResumeExplicitSession, TerminalResult: terminal,
		})
		server.registry.Set(productionFake)
	case codexcli.AdapterID:
		if !server.codexStatus.Enabled {
			writeError(writer, http.StatusServiceUnavailable, fmt.Errorf("Codex adapter is disabled: %s", server.codexStatus.Reason))
			return
		}
	case agentpi.AdapterID:
		attempt, attemptErr := reader.GetCurrentAttempt(request.Context(), run.ID())
		if attemptErr != nil {
			writeStoreError(writer, attemptErr)
			return
		}
		if !server.requireAgentExecutionRoute(writer, request, attempt.AgentExecutionProfileBinding()) {
			return
		}
		if server.product && (!server.verifierStatus.Enabled || server.verifier == nil) {
			writeError(writer, http.StatusServiceUnavailable, fmt.Errorf("independent Verifier is disabled: %s", server.verifierStatus.Reason))
			return
		}
		productionFake, err = server.e2eVerifiedRetryAdapter(task.Criteria(), run.ID())
		if err != nil {
			writeError(writer, http.StatusInternalServerError, err)
			return
		}
	default:
		writeError(writer, http.StatusConflict, fmt.Errorf("run adapter %q is disabled", charter.AdapterID()))
		return
	}
	prepared, err := server.prepareAndBindManagedAttempt(request.Context(), func(prepareCtx context.Context) (app.PrepareRunResult, error) {
		return server.service.PrepareVerifiedAgentRetry(prepareCtx, app.PrepareVerifiedAgentRetryRequest{
			CommandMeta: childCommandMeta(meta, "prepare-verified-agent-retry"), RunID: run.ID(), ExpectedVersion: expectedVersion, Instructions: instructions,
		})
	})
	if err != nil {
		writeMutationConflictError(writer, err)
		return
	}
	started, err := server.startManagedAttempt(request.Context(), prepared.Attempt, app.StartAttemptRequest{
		CommandMeta: childCommandMeta(meta, "start-verified-agent-retry"), RunID: run.ID(), ExpectedVersion: prepared.Run.Version(), Mode: app.StartFresh,
	})
	if err != nil || started.Session == nil {
		if refreshErr := server.publishManagedGenerationReferences(request.Context(), nil); refreshErr != nil {
			err = errors.Join(err, refreshErr)
		}
		if err == nil {
			err = errors.New("verified Agent Retry did not create a fresh session")
		}
		writeMutationError(writer, http.StatusInternalServerError, err)
		return
	}
	if productionFake != nil {
		fakeRuntime := server.fakeSupervisor
		if charter.AdapterID() != "fake" {
			server.supervisor.mu.RLock()
			routed, ok := server.supervisor.byAdapter[charter.AdapterID()].(*fakeSupervisor)
			server.supervisor.mu.RUnlock()
			if !ok {
				writeError(writer, http.StatusInternalServerError, errors.New("fixture retry runtime is not a Fake supervisor"))
				return
			}
			fakeRuntime = routed
		}
		go server.completeFakeRunOn(started.Run.ID(), started.Session.ID, productionFake, fakeRuntime)
	} else if started.Session.Identity.Valid() {
		go server.monitorRuntimeRun(charter.AdapterID(), started.Run.ID(), started.Session.ID, started.Session.Identity)
	}
	view, err := server.runView(request.Context(), run.ID())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

func (server *Server) requirePiPreflight(writer http.ResponseWriter, request *http.Request) bool {
	if server.piPreflight == nil {
		writeJSON(writer, http.StatusServiceUnavailable, preflight.Report{
			Status: preflight.StatusFailed, ResourcesCreated: false,
			Failure: &preflight.Failure{Boundary: "preflight.unconfigured", Observed: "Pi Preflight gate unavailable", Required: "same-process pre-Attempt revalidation", Action: "Restart Chora through the installed Local Alpha entry point."},
		})
		return false
	}
	report := server.piPreflight(request.Context())
	if report.Status != preflight.StatusPassed || report.ResourcesCreated {
		if report.Status == preflight.StatusPassed {
			report.Status = preflight.StatusFailed
			report.Failure = &preflight.Failure{Boundary: "preflight.resource-order", Observed: "Preflight reported created resources", Required: "zero resources before Pi Attempt", Action: "Stop and clean all Chora-owned resources, then rerun doctor."}
		}
		writeJSON(writer, http.StatusServiceUnavailable, report)
		return false
	}
	if server.piBaseline != nil {
		if err := server.piBaseline(); err != nil {
			writeJSON(writer, http.StatusServiceUnavailable, preflight.Report{
				Status: preflight.StatusFailed, ResourcesCreated: false, InputFingerprint: report.InputFingerprint,
				Failure: &preflight.Failure{Boundary: "baseline.identity", Observed: "installed frozen Baseline verification failed", Required: "Source Baseline v6 aggregate b28cb1624124736e745f2b217e841957330f763b6b41fab87b2a88b809b8a0cd", Action: "Stop Chora and repeat the fresh marker-bound installation flow."},
			})
			return false
		}
	}
	return true
}

func (server *Server) editCandidate(writer http.ResponseWriter, request *http.Request) {
	candidateID, err := domain.ParseCandidateID(request.PathValue("candidateID"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	var input struct {
		ExpectedVersion uint64 `json:"expectedVersion"`
		Title           string `json:"title"`
		Body            string `json:"body"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if len([]rune(strings.TrimSpace(input.Title))) > domain.MaxCandidateTitleRunes || len([]rune(strings.TrimSpace(input.Body))) > domain.MaxCandidateBodyRunes {
		writeError(writer, http.StatusBadRequest, errors.New("candidate title or body is too long"))
		return
	}
	meta, ok := requestCommandMetaForProduct(writer, request, "edit-candidate", server.product)
	if !ok {
		return
	}
	result, err := server.service.EditCandidate(request.Context(), app.EditCandidateRequest{CommandMeta: meta, CandidateID: candidateID, ExpectedVersion: input.ExpectedVersion, Title: input.Title, Body: input.Body})
	if err != nil {
		writeError(writer, http.StatusConflict, err)
		return
	}
	view, err := server.runView(request.Context(), result.Candidate.SourceRunID())
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

func (server *Server) decideCandidate(writer http.ResponseWriter, request *http.Request) {
	candidateID, err := domain.ParseCandidateID(request.PathValue("candidateID"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	var input struct {
		ExpectedVersion uint64 `json:"expectedVersion"`
		Kind            string `json:"kind"`
		Note            string `json:"note"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	input.Note = strings.TrimSpace(input.Note)
	if len([]rune(input.Note)) > domain.MaxCandidateDecisionNoteRunes {
		writeError(writer, http.StatusBadRequest, errors.New("candidate decision note is too long"))
		return
	}
	kind := domain.CandidateDecisionKind(input.Kind)
	if kind != domain.CandidateDecisionConfirm && kind != domain.CandidateDecisionDismiss {
		writeError(writer, http.StatusBadRequest, errors.New("candidate decision must be confirm or dismiss"))
		return
	}
	meta, ok := requestCommandMetaForProduct(writer, request, "decide-candidate-"+input.Kind, server.product)
	if !ok {
		return
	}
	result, err := server.service.DecideCandidate(request.Context(), app.DecideCandidateRequest{CommandMeta: meta, CandidateID: candidateID, ExpectedVersion: input.ExpectedVersion, Kind: kind, Note: input.Note})
	if err != nil {
		writeError(writer, http.StatusConflict, err)
		return
	}
	view, err := server.runView(request.Context(), result.Candidate.SourceRunID())
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

func (server *Server) completeFakeRun(runID domain.RunID, sessionID domain.RuntimeSessionID, adapter *agentfake.Adapter) {
	server.completeFakeRunOn(runID, sessionID, adapter, server.fakeSupervisor)
}

func (server *Server) completeFakeRunOn(runID domain.RunID, sessionID domain.RuntimeSessionID, adapter *agentfake.Adapter, runtime *fakeSupervisor) {
	if !wait(server.ctx, 650*time.Millisecond) {
		return
	}
	for index, stream := range []execution.StreamKind{execution.StreamStdout, execution.StreamStderr} {
		if err := runtime.Notify(stream); err != nil {
			server.logger.Printf("fake notify: %v", err)
			return
		}
		if err := server.service.ConsumeStream(server.ctx, app.ConsumeStreamRequest{
			CommandMeta: commandMeta(fmt.Sprintf("consume-%d", index)), SessionID: sessionID, Stream: stream,
		}); err != nil {
			server.logger.Printf("fake consume: %v", err)
			return
		}
		if !wait(server.ctx, 450*time.Millisecond) {
			return
		}
	}
	if adapter != nil {
		if plan, ok := adapter.DecisionGatePlan(); ok {
			run, err := server.store.Reader().GetRun(server.ctx, runID)
			if err != nil {
				server.logger.Printf("load fake Run for Decision Gate: %v", err)
				return
			}
			options := make([]domain.DecisionGateOption, 0, len(plan.Options))
			for _, option := range plan.Options {
				options = append(options, domain.DecisionGateOption{ID: option.ID, Label: option.Label, Impact: option.Impact})
			}
			requested, err := server.service.RequestExecutionDecision(server.ctx, app.RequestExecutionDecisionRequest{
				CommandMeta: app.CommandMeta{ActorID: "agent.fake", SessionID: sessionID.String(), IdempotencyKey: "fake-decision-" + runID.String()},
				RunID:       runID, ExpectedVersion: run.Version(), Question: plan.Question, Context: plan.Context, Options: options, Recommendation: plan.Recommendation, Impact: plan.Impact,
			})
			if err != nil {
				server.logger.Printf("request fake Decision Gate: %v", err)
				return
			}
			for {
				if !wait(server.ctx, 80*time.Millisecond) {
					return
				}
				gate, err := server.store.Reader().GetDecisionGate(server.ctx, requested.Gate.ID())
				if err != nil {
					server.logger.Printf("load fake Decision Gate: %v", err)
					return
				}
				if gate.Status() == domain.DecisionGateResolved {
					if err := adapter.ResolveDecision(gate.SelectedOptionID()); err != nil {
						server.logger.Printf("apply fake Decision Gate: %v", err)
						return
					}
					break
				}
				run, err := server.store.Reader().GetRun(server.ctx, runID)
				if err != nil || run.State() != domain.RunStateRunning {
					return
				}
			}
		}
	}
	if err := runtime.Exit(); err != nil {
		server.logger.Printf("fake exit: %v", err)
		return
	}
	if !wait(server.ctx, 350*time.Millisecond) {
		return
	}
	run, err := server.store.Reader().GetRun(server.ctx, runID)
	if err != nil {
		server.logger.Printf("load fake run: %v", err)
		return
	}
	if _, err := server.service.HandleExit(server.ctx, app.ExitRequest{
		CommandMeta: commandMeta("handle-exit"), SessionID: sessionID, ExpectedVersion: run.Version(),
	}); err != nil {
		server.logger.Printf("complete fake run: %v", err)
	}
}

func (server *Server) monitorRuntimeRun(adapterID string, runID domain.RunID, sessionID domain.RuntimeSessionID, identity execution.ProcessIdentity) {
	if adapterID == agentpi.AdapterID {
		defer server.logGenerationReferenceRefresh("runtime monitor termination")
	}
	if !identity.Valid() {
		server.logger.Printf("monitor %s run %s: missing process identity", adapterID, runID)
		return
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-server.ctx.Done():
			stopContext, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			reconciled, err := server.supervisor.Reconcile(stopContext, identity)
			if err == nil && reconciled.Kind == execution.ReconcileAlive && reconciled.Handle.Valid() {
				if _, stopErr := server.supervisor.Stop(stopContext, reconciled.Handle, execution.StopIntent{Kind: execution.StopForCancel, Reason: "Chora server is closing"}); stopErr != nil {
					server.logger.Printf("stop %s run %s during server close: %v", adapterID, runID, stopErr)
				}
			}
			return
		case <-ticker.C:
		}
		run, err := server.store.Reader().GetRun(server.ctx, runID)
		if err != nil {
			server.logger.Printf("load monitored %s run %s: %v", adapterID, runID, err)
			return
		}
		if run.State() != domain.RunStateRunning && run.State() != domain.RunStateStopping {
			return
		}
		reconciled, err := server.supervisor.Reconcile(server.ctx, identity)
		if err != nil {
			server.logger.Printf("reconcile %s run %s: %v", adapterID, runID, err)
			return
		}
		switch reconciled.Kind {
		case execution.ReconcileAlive:
			for _, stream := range []execution.StreamKind{execution.StreamStdout, execution.StreamStderr} {
				err := server.service.ConsumeStream(server.ctx, app.ConsumeStreamRequest{
					CommandMeta: commandMeta("consume-" + adapterID + "-" + string(stream)), SessionID: sessionID, Stream: stream,
				})
				if errors.Is(err, app.ErrRuntimePolicyViolation) {
					current, loadErr := server.store.Reader().GetRun(server.ctx, runID)
					if loadErr != nil {
						server.logger.Printf("load policy-violating %s run %s: %v", adapterID, runID, loadErr)
						return
					}
					if _, cancelErr := server.service.RequestCancel(server.ctx, app.StopRequest{
						CommandMeta: commandMeta("runtime-policy-violation"), RunID: runID,
						ExpectedVersion: current.Version(), Reason: "runtime violated the declared execution policy",
					}); cancelErr != nil {
						server.logger.Printf("cancel policy-violating %s run %s: %v", adapterID, runID, cancelErr)
					}
					return
				}
				if err != nil && !isMissingStreamNotification(err) {
					server.logger.Printf("consume %s %s for run %s: %v", adapterID, stream, runID, err)
				}
			}
		case execution.ReconcileDead:
			run, loadErr := server.store.Reader().GetRun(server.ctx, runID)
			if loadErr != nil {
				server.logger.Printf("load exited %s run %s: %v", adapterID, runID, loadErr)
				return
			}
			_, exitErr := server.service.HandleExit(server.ctx, app.ExitRequest{
				CommandMeta: commandMeta("handle-" + adapterID + "-exit"), SessionID: sessionID, ExpectedVersion: run.Version(),
			})
			if exitErr != nil {
				if strings.Contains(exitErr.Error(), "exit requires trusted runtime signal") {
					continue
				}
				server.logger.Printf("complete %s run %s: %v", adapterID, runID, exitErr)
			}
			return
		case execution.ReconcileUncertain:
			server.logger.Printf("%s run %s became uncertain: %s", adapterID, runID, reconciled.Diagnostic)
			current, loadErr := server.store.Reader().GetRun(server.ctx, runID)
			if loadErr != nil {
				server.logger.Printf("load uncertain %s run %s: %v", adapterID, runID, loadErr)
				return
			}
			if current.State() == domain.RunStateRunning {
				_, stopErr := server.service.RequestIntervention(server.ctx, app.StopRequest{
					CommandMeta: commandMeta("runtime-reconcile-uncertain-" + sessionID.String()),
					RunID:       runID, ExpectedVersion: current.Version(),
					Reason: "runtime reconciliation became uncertain; fail-closed recovery is required",
				})
				if stopErr != nil {
					server.logger.Printf("persist uncertain %s run %s: %v", adapterID, runID, stopErr)
				}
			}
			return
		default:
			server.logger.Printf("%s run %s returned invalid reconcile state %q", adapterID, runID, reconciled.Kind)
			return
		}
	}
}

func isMissingStreamNotification(err error) bool {
	return errors.Is(err, app.ErrInvalidCommand) && strings.Contains(err.Error(), "stream read requires supervisor notification")
}

func (server *Server) serveWeb(writer http.ResponseWriter, request *http.Request) {
	if strings.HasPrefix(request.URL.Path, "/api/") {
		http.NotFound(writer, request)
		return
	}
	path := filepath.Join(server.webRoot, filepath.Clean(request.URL.Path))
	if request.URL.Path != "/" {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			http.ServeFile(writer, request, path)
			return
		}
	}
	http.ServeFile(writer, request, filepath.Join(server.webRoot, "index.html"))
}

func fakeTerminalResult(criteria []domain.AcceptanceCriterion, resolved bool) ([]byte, error) {
	type check struct {
		CriterionID string `json:"criterion_id"`
		Status      string `json:"status"`
		Evidence    string `json:"evidence"`
	}
	checks := make([]check, 0, len(criteria))
	for index, criterion := range criteria {
		status := string(execution.CheckPass)
		evidence := "The persisted Room view exposes this result."
		if !resolved && index == len(criteria)-1 {
			status = string(execution.CheckUnknown)
			evidence = "This criterion requires the human review decision."
		}
		checks = append(checks, check{CriterionID: criterion.ID().String(), Status: status, Evidence: evidence})
	}
	document := struct {
		SchemaVersion      string              `json:"schema_version"`
		Summary            string              `json:"summary"`
		ReviewReady        bool                `json:"review_ready"`
		Outputs            []map[string]string `json:"outputs"`
		ArtifactCandidates []map[string]string `json:"artifact_candidates"`
		Checks             []check             `json:"checks"`
		Unknowns           []string            `json:"unknowns"`
		Handoff            map[string]any      `json:"handoff"`
	}{
		SchemaVersion: "chora.agent-result.v1", Summary: "The Fake Agent produced a persistent Room collaboration result.", ReviewReady: true,
		Outputs:            []map[string]string{{"locator": "report.md", "description": "Persistent Fake-Agent collaboration result"}},
		ArtifactCandidates: []map[string]string{{"locator": "room-context-candidate.md", "description": "Reviewed candidate for explicit human confirmation"}}, Checks: checks,
		Unknowns: func() []string {
			if resolved {
				return []string{}
			}
			return []string{"Does the structured Room evidence support a confident decision without the raw transcript?"}
		}(),
		Handoff: map[string]any{"requested": false, "reason": ""},
	}
	return json.Marshal(document)
}

func normalizeWorkspaceRoot(value string) (string, error) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		value = filepath.Join(home, strings.TrimPrefix(value, "~/"))
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	return filepath.Clean(absolute), nil
}

func initialTechnicalPlanContent(goal string) domain.TechnicalPlanContent {
	goal = strings.TrimSpace(goal)
	return domain.TechnicalPlanContent{
		TechnicalSteps: []string{"Define the bounded implementation steps for: " + goal},
		Decisions:      []string{"Record implementation decisions before submitting this Draft."},
		Risks:          []string{"Review implementation risks before accepting this Revision."},
		Unknowns:       []string{"Resolve or explicitly retain implementation unknowns before acceptance."},
	}
}

func commandMeta(action string) app.CommandMeta {
	return app.CommandMeta{ActorID: localActor, SessionID: localSession, IdempotencyKey: action + "-" + uuid.NewString()}
}

func requestCommandMeta(request *http.Request, action string) app.CommandMeta {
	key := strings.TrimSpace(request.Header.Get("Idempotency-Key"))
	if key == "" {
		key = action + "-" + uuid.NewString()
	}
	return app.CommandMeta{ActorID: localActor, SessionID: localSession, IdempotencyKey: key}
}

func requireRequestCommandMeta(writer http.ResponseWriter, request *http.Request, action string) (app.CommandMeta, bool) {
	if strings.TrimSpace(request.Header.Get("Idempotency-Key")) == "" {
		writeError(writer, http.StatusBadRequest, errors.New("Idempotency-Key header is required"))
		return app.CommandMeta{}, false
	}
	return requestCommandMeta(request, action), true
}

func requestCommandMetaForProduct(writer http.ResponseWriter, request *http.Request, action string, required bool) (app.CommandMeta, bool) {
	if required {
		return requireRequestCommandMeta(writer, request, action)
	}
	return requestCommandMeta(request, action), true
}

func childCommandMeta(parent app.CommandMeta, action string) app.CommandMeta {
	parent.IdempotencyKey += ":" + action
	return parent
}

func decodeJSON(request *http.Request, target any) error {
	defer request.Body.Close()
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("unexpected trailing JSON content")
	}
	return nil
}

func decodeOptionalJSON(request *http.Request, target any) error {
	err := decodeJSON(request, target)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

func writeStoreError(writer http.ResponseWriter, err error) {
	if errors.Is(err, storecontract.ErrNotFound) {
		writeError(writer, http.StatusNotFound, err)
		return
	}
	if errors.Is(err, storecontract.ErrRoomStateForbidden) {
		writeError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	if errors.Is(err, storecontract.ErrActiveRunExists) || errors.Is(err, storecontract.ErrVersionConflict) || errors.Is(err, storecontract.ErrIdempotencyConflict) {
		writeError(writer, http.StatusConflict, err)
		return
	}
	writeError(writer, http.StatusInternalServerError, err)
}

func writePlanningCommandError(writer http.ResponseWriter, err error) {
	if errors.Is(err, app.ErrUnauthorizedCommand) {
		writeError(writer, http.StatusForbidden, errors.New("Agent execution authorization is required"))
		return
	}
	if errors.Is(err, app.ErrTaskWorktreeUnavailable) {
		writeError(writer, http.StatusServiceUnavailable, err)
		return
	}
	if errors.Is(err, storecontract.ErrNotFound) {
		writeError(writer, http.StatusNotFound, err)
		return
	}
	if errors.Is(err, storecontract.ErrRoomStateForbidden) {
		writeError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	if errors.Is(err, speccoding.ErrUnsupportedUserTask) {
		writeError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	if errors.Is(err, speccoding.ErrInvalidInstalledEnvelope) {
		writeError(writer, http.StatusServiceUnavailable, err)
		return
	}
	if errors.Is(err, storecontract.ErrVersionConflict) || errors.Is(err, storecontract.ErrPlanningConflict) || errors.Is(err, storecontract.ErrActiveRunExists) || errors.Is(err, storecontract.ErrIdempotencyConflict) {
		writeError(writer, http.StatusConflict, err)
		return
	}
	writeError(writer, http.StatusBadRequest, err)
}

func writeRoomLifecycleError(writer http.ResponseWriter, err error) {
	if errors.Is(err, storecontract.ErrNotFound) {
		writeError(writer, http.StatusNotFound, err)
		return
	}
	if errors.Is(err, storecontract.ErrVersionConflict) || errors.Is(err, storecontract.ErrIdempotencyConflict) {
		writeError(writer, http.StatusConflict, err)
		return
	}
	if errors.Is(err, storecontract.ErrRoomStateForbidden) || errors.Is(err, domain.ErrInvalidArgument) {
		writeError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	if errors.Is(err, app.ErrInvalidCommand) {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeError(writer, http.StatusInternalServerError, err)
}

func writeMutationConflictError(writer http.ResponseWriter, err error) {
	writeMutationError(writer, http.StatusConflict, err)
}

func writeMutationError(writer http.ResponseWriter, defaultStatus int, err error) {
	if errors.Is(err, app.ErrTaskWorktreeUnavailable) {
		writeError(writer, http.StatusServiceUnavailable, err)
		return
	}
	if errors.Is(err, storecontract.ErrRoomStateForbidden) {
		writeError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	writeError(writer, defaultStatus, err)
}

func writeError(writer http.ResponseWriter, status int, err error) {
	writeJSON(writer, status, map[string]string{"error": err.Error()})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func wait(ctx context.Context, duration time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(duration):
		return true
	}
}

type localAuthorizer struct{}

func (localAuthorizer) Authorize(context.Context, app.CommandAuthorizationRequest) error { return nil }

type localPresence struct{}

func (localPresence) AuthorizeReview(_ context.Context, request app.PresenceRequest) (domain.HumanReviewAuthorization, error) {
	return app.MintHumanReviewAuthorization(app.TrustedPresenceProof{HumanIssued: true, DeviceOwnerPresent: true}, request)
}
