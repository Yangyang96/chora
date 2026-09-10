// Package productinstall owns the durable, provider-neutral installation
// transaction. Concrete Docker, release-manifest, source-bundle, and private-Pi
// implementations remain behind the narrow ports declared here.
package productinstall

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	StateSchema      = "chora.product-install-state/v1"
	GenerationSchema = "chora.product-generation/v1"
	DoctorSchema     = "chora.product-doctor-report/v1"
	maxOperations    = 128

	DoctorReasonObservationUnavailable = "engine_observation_unavailable"
	DoctorReasonIdentityMismatch       = "engine_identity_mismatch"
)

var (
	ErrAuthorityRequired    = errors.New("product installation mutation requires explicit authority")
	ErrInvalidRequest       = errors.New("invalid product installation request")
	ErrInvalidState         = errors.New("invalid product installation state")
	ErrIdempotencyConflict  = errors.New("product installation idempotency conflict")
	ErrBusy                 = errors.New("another product installation operation is active")
	ErrConcurrentUpdate     = errors.New("product installation state changed concurrently")
	ErrAssetConflict        = errors.New("installed asset identity conflicts with the manifest")
	ErrProbeFailed          = errors.New("Engine capability Probe failed")
	ErrGenerationReferenced = errors.New("product generation remains referenced")
	ErrOperationFailed      = errors.New("product installation operation failed")
	// ErrInterrupted tells the state machine that a port stopped at an unknown
	// point. The durable journal is intentionally left resumable.
	ErrInterrupted = errors.New("product installation operation interrupted")
)

type Action string

const (
	ActionSetup     Action = "setup"
	ActionUpgrade   Action = "upgrade"
	ActionGarbageGC Action = "garbage_collect"
	ActionUninstall Action = "uninstall"
)

type OperationPhase string

const (
	PhasePlanned      OperationPhase = "planned"
	PhaseAcquiring    OperationPhase = "acquiring"
	PhaseProbing      OperationPhase = "probing"
	PhaseVerifying    OperationPhase = "verifying"
	PhaseActivating   OperationPhase = "activating"
	PhaseDeactivating OperationPhase = "deactivating"
	PhaseRemoving     OperationPhase = "removing"
	PhaseRollingBack  OperationPhase = "rolling_back"
	PhaseComplete     OperationPhase = "complete"
	PhaseFailed       OperationPhase = "failed"
)

type GenerationStatus string

const (
	GenerationActive     GenerationStatus = "active"
	GenerationSuperseded GenerationStatus = "superseded"
	GenerationRemoved    GenerationStatus = "removed"
)

type AssetOwnership string

const (
	OwnershipChora    AssetOwnership = "chora"
	OwnershipExternal AssetOwnership = "external"
)

type AcquisitionKind string

const (
	AcquisitionNone               AcquisitionKind = "none"
	AcquisitionLocalDockerArchive AcquisitionKind = "local_docker_archive"
	// AcquisitionRegistryPull is retained as a source-compatibility sentinel so
	// legacy callers get an explicit validation failure instead of silently
	// acquiring from a Registry. It is never a valid persisted acquisition kind.
	AcquisitionRegistryPull   AcquisitionKind = "registry_pull"
	AcquisitionPrivateRuntime AcquisitionKind = "private_runtime_install"
	AcquisitionSourceBundle   AcquisitionKind = "source_bundle_install"
)

// EngineTarget identifies one explicit CLI/context/endpoint. Implementations
// must pass these values to every Engine operation and must never substitute a
// process-global or "current" context.
type EngineTarget struct {
	CLIPath        string `json:"cli_path"`
	ContextName    string `json:"context_name"`
	EndpointDigest string `json:"endpoint_digest"`
}

type EngineObservation struct {
	DaemonID        string `json:"daemon_id"`
	APIVersion      string `json:"api_version"`
	OperatingSystem string `json:"operating_system"`
	Architecture    string `json:"architecture"`
	ContextName     string `json:"context_name"`
	EndpointDigest  string `json:"endpoint_digest"`
}

type DoctorReport struct {
	SchemaVersion string            `json:"schema_version"`
	ObservedAt    time.Time         `json:"observed_at"`
	Target        EngineTarget      `json:"target"`
	Engine        EngineObservation `json:"engine"`
	Ready         bool              `json:"ready"`
	Reason        string            `json:"reason,omitempty"`
}

type AssetSpec struct {
	ID              string          `json:"id"`
	Kind            string          `json:"kind"`
	Identity        string          `json:"identity"`
	Ownership       AssetOwnership  `json:"ownership"`
	AcquisitionKind AcquisitionKind `json:"acquisition_kind"`
	AcquisitionRef  string          `json:"acquisition_ref,omitempty"`
	ArchivePath     string          `json:"archive_path,omitempty"`
	ArchiveSize     int64           `json:"archive_size,omitempty"`
	ArchiveSHA256   string          `json:"archive_sha256,omitempty"`
}

type GenerationSpec struct {
	SchemaVersion  string      `json:"schema_version"`
	GenerationID   string      `json:"generation_id"`
	ReleaseID      string      `json:"release_id"`
	ManifestSHA256 string      `json:"manifest_sha256"`
	Assets         []AssetSpec `json:"assets"`
}

type InstalledAsset struct {
	Spec    AssetSpec `json:"spec"`
	Reused  bool      `json:"reused"`
	Removed bool      `json:"removed"`
}

type Generation struct {
	Spec                GenerationSpec   `json:"spec"`
	Status              GenerationStatus `json:"status"`
	QualificationDigest string           `json:"qualification_digest"`
	Target              EngineTarget     `json:"target"`
	Probe               ProbeResult      `json:"probe"`
	Assets              []InstalledAsset `json:"assets"`
	ActivatedAt         time.Time        `json:"activated_at"`
}

type ProbeResult struct {
	Passed              bool              `json:"passed"`
	CleanupProven       bool              `json:"cleanup_proven"`
	QualificationDigest string            `json:"qualification_digest"`
	Engine              EngineObservation `json:"engine"`
	CapabilityContract  json.RawMessage   `json:"capability_contract,omitempty"`
	EngineQualification json.RawMessage   `json:"engine_qualification,omitempty"`
	Reason              string            `json:"reason,omitempty"`
}

type Operation struct {
	Key                        string            `json:"key"`
	RequestDigest              string            `json:"request_digest"`
	Action                     Action            `json:"action"`
	Authority                  MutationAuthority `json:"authority"`
	Phase                      OperationPhase    `json:"phase"`
	Generation                 GenerationSpec    `json:"generation"`
	Target                     EngineTarget      `json:"target"`
	PriorActiveGeneration      string            `json:"prior_active_generation,omitempty"`
	PendingAssetID             string            `json:"pending_asset_id,omitempty"`
	PendingAssetIdentity       string            `json:"pending_asset_identity,omitempty"`
	NextAssetIndex             int               `json:"next_asset_index"`
	TargetGenerationIDs        []string          `json:"target_generation_ids,omitempty"`
	RetiredServingGenerationID string            `json:"retired_serving_generation_id,omitempty"`
	AcquiredAssetIDs           []string          `json:"acquired_asset_ids"`
	ReusedAssetIDs             []string          `json:"reused_asset_ids"`
	RemovedAssetIDs            []string          `json:"removed_asset_ids,omitempty"`
	RemovedAssetIdentities     []string          `json:"removed_asset_identities,omitempty"`
	SkippedGenerationIDs       []string          `json:"skipped_generation_ids,omitempty"`
	QualificationDigest        string            `json:"qualification_digest,omitempty"`
	Probe                      ProbeResult       `json:"probe,omitempty"`
	Failure                    string            `json:"failure,omitempty"`
	CreatedAt                  time.Time         `json:"created_at"`
	UpdatedAt                  time.Time         `json:"updated_at"`
}

type State struct {
	SchemaVersion      string       `json:"schema_version"`
	Revision           uint64       `json:"revision"`
	ActiveGenerationID string       `json:"active_generation_id,omitempty"`
	Generations        []Generation `json:"generations"`
	Operations         []Operation  `json:"operations"`
}

type MutationAuthority struct {
	GrantID   string    `json:"grant_id"`
	ActorID   string    `json:"actor_id"`
	SessionID string    `json:"session_id"`
	Action    Action    `json:"action"`
	IssuedAt  time.Time `json:"issued_at"`
}

type InstallRequest struct {
	Authority  MutationAuthority
	Key        string
	Target     EngineTarget
	Generation GenerationSpec
}

type InstallResult struct {
	GenerationID string
	Active       bool
	Replayed     bool
}

type GCRequest struct {
	Authority MutationAuthority
	Key       string
	Target    EngineTarget
	Limit     int
}

type GCResult struct {
	RemovedAssetIDs      []string
	SkippedGenerationIDs []string
	Replayed             bool
}

type UninstallRequest struct {
	Authority     MutationAuthority
	Key           string
	Target        EngineTarget
	GenerationIDs []string
}

type UninstallResult struct {
	RemovedAssetIDs []string
	Replayed        bool
}

type AssetStatus struct {
	Present  bool
	Identity string
}

type GenerationReferences struct {
	ServingGenerationIDs            []string
	ActiveAttemptGenerationIDs      []string
	RecoverableAttemptGenerationIDs []string
}

type Clock interface{ Now() time.Time }

// InstallationLocker serializes the complete mutation transaction across all
// Service instances and processes that share product installation state. Its
// implementation must be crash-safe (for example an OS-held lock or a fenced
// durable lease), keep ownership until fn returns, and cancel lockCtx if fenced
// ownership is lost. It must never expire/unlock a live callback implicitly.
// A release error is returned only after fn has stopped; journal idempotency
// makes retry after an ambiguous release safe.
type InstallationLocker interface {
	WithInstallationLock(context.Context, func(lockCtx context.Context) error) error
}
type EngineObserver interface {
	Observe(context.Context, EngineTarget) (EngineObservation, error)
}
type Authorizer interface {
	Authorize(context.Context, MutationAuthority, Action) error
}
type StateStore interface {
	Load(context.Context) (State, error)
	SaveCAS(context.Context, uint64, State) error
}
type AssetInspector interface {
	InspectExact(context.Context, EngineTarget, AssetSpec) (AssetStatus, error)
}
type AssetAcquirer interface {
	AcquirePinned(context.Context, EngineTarget, AssetSpec) error
}
type AssetRemover interface {
	RemoveExact(context.Context, EngineTarget, AssetSpec) error
}
type CapabilityProber interface {
	Probe(context.Context, MutationAuthority, EngineTarget, GenerationSpec) (ProbeResult, error)
}
type GenerationVerifier interface {
	Verify(context.Context, EngineTarget, GenerationSpec, []InstalledAsset, ProbeResult) error
}
type Activator interface {
	Current(context.Context) (string, error)
	AtomicActivate(context.Context, string, GenerationSpec) error
	AtomicDeactivate(context.Context, string) error
}
type ReferenceReader interface {
	References(context.Context) (GenerationReferences, error)
}
type ReferenceStore interface {
	ReferenceReader
	RequireReferenceSnapshot(context.Context) error
	RetireServingGeneration(context.Context, string) error
}

type Dependencies struct {
	Clock      Clock
	Locker     InstallationLocker
	Observer   EngineObserver
	Authorizer Authorizer
	Store      StateStore
	Inspector  AssetInspector
	Acquirer   AssetAcquirer
	Remover    AssetRemover
	Prober     CapabilityProber
	Verifier   GenerationVerifier
	Activator  Activator
	References ReferenceStore
}

type Service struct {
	deps Dependencies
	mu   sync.Mutex
}

var identifierPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func New(deps Dependencies) (*Service, error) {
	if deps.Clock == nil || deps.Locker == nil || deps.Observer == nil || deps.Authorizer == nil || deps.Store == nil ||
		deps.Inspector == nil || deps.Acquirer == nil || deps.Remover == nil || deps.Prober == nil ||
		deps.Verifier == nil || deps.Activator == nil || deps.References == nil {
		return nil, fmt.Errorf("%w: missing dependency", ErrInvalidRequest)
	}
	return &Service{deps: deps}, nil
}

func EmptyState() State {
	return State{SchemaVersion: StateSchema, Generations: []Generation{}, Operations: []Operation{}}
}

func (service *Service) Doctor(ctx context.Context, target EngineTarget) (DoctorReport, error) {
	if err := validateTarget(target); err != nil {
		return DoctorReport{}, err
	}
	observation, err := service.deps.Observer.Observe(ctx, target)
	report := DoctorReport{SchemaVersion: DoctorSchema, ObservedAt: service.deps.Clock.Now(), Target: target}
	if err != nil {
		report.Reason = DoctorReasonObservationUnavailable
		return report, nil
	}
	report.Engine = observation
	if err := validateObservation(target, observation); err != nil {
		report.Reason = DoctorReasonIdentityMismatch
		return report, nil
	}
	report.Ready = true
	return report, nil
}

func validateTarget(target EngineTarget) error {
	if !filepath.IsAbs(target.CLIPath) || filepath.Clean(target.CLIPath) != target.CLIPath ||
		!identifierPattern.MatchString(target.ContextName) || !digestPattern.MatchString(target.EndpointDigest) {
		return fmt.Errorf("%w: explicit Engine target is invalid", ErrInvalidRequest)
	}
	return nil
}

func validateObservation(target EngineTarget, observed EngineObservation) error {
	if strings.TrimSpace(observed.DaemonID) == "" || strings.TrimSpace(observed.APIVersion) == "" ||
		observed.OperatingSystem == "" || observed.Architecture == "" ||
		observed.ContextName != target.ContextName || observed.EndpointDigest != target.EndpointDigest {
		return fmt.Errorf("%w: observed Engine identity does not match explicit target", ErrInvalidRequest)
	}
	return nil
}

func validateAuthority(authority MutationAuthority, action Action) error {
	if !identifierPattern.MatchString(authority.GrantID) || strings.TrimSpace(authority.ActorID) == "" ||
		strings.TrimSpace(authority.SessionID) == "" || authority.Action != action || authority.IssuedAt.IsZero() {
		return ErrAuthorityRequired
	}
	return nil
}

func validateGeneration(spec GenerationSpec) error {
	if spec.SchemaVersion != GenerationSchema || !identifierPattern.MatchString(spec.GenerationID) ||
		!identifierPattern.MatchString(spec.ReleaseID) || !digestPattern.MatchString(spec.ManifestSHA256) || len(spec.Assets) == 0 {
		return fmt.Errorf("%w: generation identity is invalid", ErrInvalidRequest)
	}
	if !slices.IsSortedFunc(spec.Assets, func(left, right AssetSpec) int { return strings.Compare(left.ID, right.ID) }) {
		return fmt.Errorf("%w: generation assets are not sorted", ErrInvalidRequest)
	}
	archiveByIdentity := map[string]struct {
		path   string
		size   int64
		digest string
	}{}
	identityByArchivePath := map[string]string{}
	identityByArchiveDigest := map[string]string{}
	for index, asset := range spec.Assets {
		if err := validateAsset(asset); err != nil || index > 0 && spec.Assets[index-1].ID == asset.ID {
			return fmt.Errorf("%w: invalid or duplicate generation asset %q", ErrInvalidRequest, asset.ID)
		}
		if asset.AcquisitionKind == AcquisitionLocalDockerArchive {
			binding := struct {
				path   string
				size   int64
				digest string
			}{asset.ArchivePath, asset.ArchiveSize, asset.ArchiveSHA256}
			if prior, exists := archiveByIdentity[asset.Identity]; exists && prior != binding {
				return fmt.Errorf("%w: conflicting archive binding for image %q", ErrInvalidRequest, asset.Identity)
			}
			if prior, exists := identityByArchivePath[asset.ArchivePath]; exists && prior != asset.Identity {
				return fmt.Errorf("%w: archive path is bound to multiple images", ErrInvalidRequest)
			}
			if prior, exists := identityByArchiveDigest[asset.ArchiveSHA256]; exists && prior != asset.Identity {
				return fmt.Errorf("%w: archive digest is bound to multiple images", ErrInvalidRequest)
			}
			archiveByIdentity[asset.Identity] = binding
			identityByArchivePath[asset.ArchivePath] = asset.Identity
			identityByArchiveDigest[asset.ArchiveSHA256] = asset.Identity
		}
	}
	return nil
}

func validateAsset(asset AssetSpec) error {
	if !identifierPattern.MatchString(asset.ID) || !identifierPattern.MatchString(asset.Kind) || !validAssetIdentity(asset.Identity) {
		return ErrInvalidRequest
	}
	if asset.Ownership == OwnershipExternal {
		if asset.AcquisitionKind != AcquisitionNone || asset.AcquisitionRef != "" ||
			asset.ArchivePath != "" || asset.ArchiveSize != 0 || asset.ArchiveSHA256 != "" {
			return ErrInvalidRequest
		}
		return nil
	}
	if asset.Ownership != OwnershipChora || asset.AcquisitionKind == AcquisitionNone {
		return ErrInvalidRequest
	}
	if asset.AcquisitionKind == AcquisitionLocalDockerArchive {
		if asset.Kind != dockerImageKind || !strings.HasPrefix(asset.Identity, "sha256:") || asset.AcquisitionRef != "" ||
			!filepath.IsAbs(asset.ArchivePath) || filepath.Clean(asset.ArchivePath) != asset.ArchivePath ||
			asset.ArchiveSize <= 0 || !digestPattern.MatchString(asset.ArchiveSHA256) || strings.ContainsAny(asset.ArchivePath, "\x00\r\n") {
			return ErrInvalidRequest
		}
		return nil
	}
	if asset.AcquisitionKind != AcquisitionPrivateRuntime && asset.AcquisitionKind != AcquisitionSourceBundle ||
		asset.ArchivePath != "" || asset.ArchiveSize != 0 || asset.ArchiveSHA256 != "" ||
		strings.TrimSpace(asset.AcquisitionRef) == "" || asset.AcquisitionRef != strings.TrimSpace(asset.AcquisitionRef) || strings.ContainsAny(asset.AcquisitionRef, "\x00\r\n") {
		return ErrInvalidRequest
	}
	return nil
}

func validAssetIdentity(value string) bool {
	return digestPattern.MatchString(value) || strings.HasPrefix(value, "sha256:") && digestPattern.MatchString(strings.TrimPrefix(value, "sha256:"))
}

func requestDigest(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func cloneState(state State) State {
	cloned := state
	cloned.Generations = make([]Generation, len(state.Generations))
	for index, generation := range state.Generations {
		cloned.Generations[index] = generation
		cloned.Generations[index].Spec.Assets = slices.Clone(generation.Spec.Assets)
		cloned.Generations[index].Assets = slices.Clone(generation.Assets)
		cloned.Generations[index].Probe.CapabilityContract = slices.Clone(generation.Probe.CapabilityContract)
		cloned.Generations[index].Probe.EngineQualification = slices.Clone(generation.Probe.EngineQualification)
	}
	cloned.Operations = make([]Operation, len(state.Operations))
	for index, operation := range state.Operations {
		cloned.Operations[index] = operation
		cloned.Operations[index].Generation.Assets = slices.Clone(operation.Generation.Assets)
		cloned.Operations[index].TargetGenerationIDs = slices.Clone(operation.TargetGenerationIDs)
		cloned.Operations[index].AcquiredAssetIDs = slices.Clone(operation.AcquiredAssetIDs)
		cloned.Operations[index].ReusedAssetIDs = slices.Clone(operation.ReusedAssetIDs)
		cloned.Operations[index].RemovedAssetIDs = slices.Clone(operation.RemovedAssetIDs)
		cloned.Operations[index].RemovedAssetIdentities = slices.Clone(operation.RemovedAssetIdentities)
		cloned.Operations[index].SkippedGenerationIDs = slices.Clone(operation.SkippedGenerationIDs)
		cloned.Operations[index].Probe.CapabilityContract = slices.Clone(operation.Probe.CapabilityContract)
		cloned.Operations[index].Probe.EngineQualification = slices.Clone(operation.Probe.EngineQualification)
	}
	return cloned
}

func validateState(state State) error {
	if state.SchemaVersion != StateSchema || len(state.Operations) > maxOperations {
		return ErrInvalidState
	}
	seenGenerations := map[string]bool{}
	active := 0
	for _, generation := range state.Generations {
		if seenGenerations[generation.Spec.GenerationID] || validateGeneration(generation.Spec) != nil ||
			!digestPattern.MatchString(generation.QualificationDigest) || generation.ActivatedAt.IsZero() ||
			len(generation.Assets) != len(generation.Spec.Assets) {
			return ErrInvalidState
		}
		for index, asset := range generation.Assets {
			if asset.Spec != generation.Spec.Assets[index] {
				return ErrInvalidState
			}
		}
		seenGenerations[generation.Spec.GenerationID] = true
		if generation.Status == GenerationActive {
			active++
			if state.ActiveGenerationID != generation.Spec.GenerationID {
				return ErrInvalidState
			}
		} else if generation.Status != GenerationSuperseded && generation.Status != GenerationRemoved {
			return ErrInvalidState
		}
	}
	if state.ActiveGenerationID == "" && active != 0 || state.ActiveGenerationID != "" && active != 1 {
		return ErrInvalidState
	}
	seenOperations := map[string]string{}
	activeOperations := 0
	for _, operation := range state.Operations {
		if !identifierPattern.MatchString(operation.Key) || !digestPattern.MatchString(operation.RequestDigest) || seenOperations[operation.Key] != "" {
			return ErrInvalidState
		}
		if validateAuthority(operation.Authority, operation.Action) != nil || validateTarget(operation.Target) != nil || !validOperationPhase(operation.Phase) {
			return ErrInvalidState
		}
		if operation.Action == ActionSetup || operation.Action == ActionUpgrade {
			if validateGeneration(operation.Generation) != nil || len(operation.TargetGenerationIDs) != 0 ||
				operation.NextAssetIndex < 0 || operation.NextAssetIndex > len(operation.Generation.Assets) {
				return ErrInvalidState
			}
		} else if operation.Action != ActionGarbageGC && operation.Action != ActionUninstall {
			return ErrInvalidState
		} else {
			if !slices.IsSorted(operation.TargetGenerationIDs) {
				return ErrInvalidState
			}
			for index, generationID := range operation.TargetGenerationIDs {
				if !seenGenerations[generationID] || index > 0 && operation.TargetGenerationIDs[index-1] == generationID {
					return ErrInvalidState
				}
			}
		}
		if operation.RetiredServingGenerationID != "" {
			if operation.Action != ActionUninstall || operation.RetiredServingGenerationID != operation.PriorActiveGeneration ||
				!slices.Contains(operation.TargetGenerationIDs, operation.RetiredServingGenerationID) ||
				state.ActiveGenerationID == operation.RetiredServingGenerationID {
				return ErrInvalidState
			}
			switch operation.Phase {
			case PhaseRemoving, PhaseComplete, PhaseFailed:
			default:
				return ErrInvalidState
			}
		}
		seenOperations[operation.Key] = operation.RequestDigest
		if operation.Phase != PhaseComplete && operation.Phase != PhaseFailed {
			activeOperations++
		}
	}
	if activeOperations > 1 {
		return ErrInvalidState
	}
	return nil
}

func validOperationPhase(phase OperationPhase) bool {
	switch phase {
	case PhasePlanned, PhaseAcquiring, PhaseProbing, PhaseVerifying, PhaseActivating,
		PhaseDeactivating, PhaseRemoving, PhaseRollingBack, PhaseComplete, PhaseFailed:
		return true
	default:
		return false
	}
}

func generationByID(state *State, id string) (*Generation, int) {
	for index := range state.Generations {
		if state.Generations[index].Spec.GenerationID == id {
			return &state.Generations[index], index
		}
	}
	return nil, -1
}

func operationByKey(state *State, key string) (*Operation, int) {
	for index := range state.Operations {
		if state.Operations[index].Key == key {
			return &state.Operations[index], index
		}
	}
	return nil, -1
}

func contains(values []string, value string) bool { return slices.Contains(values, value) }

type realClock struct{}

func (realClock) Now() time.Time { return time.Now().UTC() }
