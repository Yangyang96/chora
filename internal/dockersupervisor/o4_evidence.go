package dockersupervisor

import (
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
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	o4A3LedgerSchema             = "chora.m1-o4-sealed-a3-runner-ledger.v1"
	o4VerifierLedgerSchema       = "chora.m1-o4-verifier-ledger.v1"
	o4ProfileResourceSchema      = "chora.m1-o4-profile-reuse-resource-ledger.v1"
	o4RecoveryResourceSchema     = "chora.m1-o4-recovery-residue-resource-ledger.v1"
	o4EvidenceStatusRecording    = "recording"
	o4EvidenceMaxBytes           = 4 << 20
	o4EvidenceZeroPreviousDigest = "0000000000000000000000000000000000000000000000000000000000000000"
)

type O4BoundaryPhase string

const (
	O4BoundaryPhaseC O4BoundaryPhase = "C"
	O4BoundaryPhaseD O4BoundaryPhase = "D"
)

// O4BoundaryRequest contains selectors and installed paths only. The HTTP
// boundary must populate ProductObservation from its private, reloaded product
// state; ProductObservation is deliberately not a JSON input surface.
type O4BoundaryRequest struct {
	Phase                 O4BoundaryPhase      `json:"phase"`
	Sequence              int                  `json:"sequence"`
	TaskID                string               `json:"taskId"`
	RunID                 string               `json:"runId"`
	AttemptID             string               `json:"attemptId"`
	AgentExecutionProfile string               `json:"agentExecutionProfile"`
	ObservationDigest     string               `json:"observationDigest"`
	TupleIdentity         string               `json:"tupleIdentity"`
	GenerationID          string               `json:"generationId"`
	A3Path                string               `json:"a3Path"`
	VerifierPath          string               `json:"verifierPath"`
	ResourceDirectory     string               `json:"resourceDirectory"`
	ProductObservation    O4ProductObservation `json:"-"`
}

// O4ProductObservation is the narrow trusted projection reloaded by the
// installed product. Its members all use json:"-" so decoding an untrusted
// request can never manufacture product facts.
type O4ProductObservation struct {
	EnvironmentID           string                            `json:"-"`
	InstallID               string                            `json:"-"`
	BindingDigest           string                            `json:"-"`
	Platform                O4PlatformObservation             `json:"-"`
	RoleImages              map[string]O4RoleImageObservation `json:"-"`
	WorkspaceIdentity       string                            `json:"-"`
	WorkspaceID             string                            `json:"-"`
	RuntimeIdentitySHA256   string                            `json:"-"`
	ExecutionIdentityDigest string                            `json:"-"`
	TrustedHost             *O4TrustedHostObservation         `json:"-"`
}

type O4PlatformObservation struct {
	OS           string `json:"-"`
	Architecture string `json:"-"`
}

type O4RoleImageObservation struct {
	ArtifactID          string `json:"-"`
	ArchiveSHA256       string `json:"-"`
	ArchiveSize         int64  `json:"-"`
	DockerConfigImageID string `json:"-"`
}

type O4TrustedHostObservation struct {
	SelectedPiPath             string `json:"-"`
	SelectedPiSourceRoot       string `json:"-"`
	SelectedSource             string `json:"-"`
	PiExecutableSHA256         string `json:"-"`
	PiSourceProvenanceSHA256   string `json:"-"`
	RuntimeFingerprint         string `json:"-"`
	ProcessGroupIdentitySHA256 string `json:"-"`
	SessionIdentitySHA256      string `json:"-"`
	ProcessStartedAt           string `json:"-"`
	ProcessExitedAt            string `json:"-"`
	TerminalCleanupDigest      string `json:"-"`
	ProcessGroupTerminated     bool   `json:"-"`
	SessionClosed              bool   `json:"-"`
}

type O4BoundaryResult struct {
	Phase                 O4BoundaryPhase `json:"phase"`
	Sequence              int             `json:"sequence"`
	ObservationDigest     string          `json:"observationDigest"`
	OperationLedgerSHA256 string          `json:"operationLedgerSha256"`
	OperationLedgerCount  int             `json:"operationLedgerCount"`
	A3LedgerSHA256        string          `json:"a3LedgerSha256"`
	VerifierLedgerSHA256  string          `json:"verifierLedgerSha256"`
	ResourcePath          string          `json:"resourcePath"`
	ResourceSHA256        string          `json:"resourceSha256"`
}

type o4EvidenceBoundary struct {
	Phase                 string `json:"phase"`
	Sequence              int    `json:"sequence"`
	TaskID                string `json:"taskId"`
	RunID                 string `json:"runId"`
	AttemptID             string `json:"attemptId"`
	AgentExecutionProfile string `json:"agentExecutionProfile"`
	ObservationDigest     string `json:"observationDigest"`
	OperationLedgerCount  int    `json:"operationLedgerCount"`
	OperationLedgerSHA256 string `json:"operationLedgerSha256"`
	ResourcePath          string `json:"resourcePath"`
	ResourceSHA256        string `json:"resourceSha256"`
	PreviousDigest        string `json:"previousDigest"`
	Digest                string `json:"digest"`
}

type o4VerifierLedger struct {
	SchemaVersion string               `json:"schemaVersion"`
	Status        string               `json:"status"`
	EnvironmentID string               `json:"environmentId"`
	InstallID     string               `json:"installId"`
	GenerationID  string               `json:"generationId"`
	BindingDigest string               `json:"bindingDigest"`
	TupleIdentity string               `json:"tupleIdentity"`
	Attempts      []map[string]any     `json:"attempts"`
	Boundaries    []o4EvidenceBoundary `json:"boundaries"`
}

type o4A3Ledger struct {
	SchemaVersion     string                    `json:"schemaVersion"`
	Status            string                    `json:"status"`
	EnvironmentID     string                    `json:"environmentId"`
	InstallID         string                    `json:"installId"`
	GenerationID      string                    `json:"generationId"`
	Platform          map[string]string         `json:"platform"`
	BindingDigest     string                    `json:"bindingDigest"`
	TupleIdentity     string                    `json:"tupleIdentity"`
	ObservationSource string                    `json:"observationSource"`
	Complete          bool                      `json:"complete"`
	EngineEventsUsed  bool                      `json:"engineEventsUsed"`
	RoleImages        map[string]map[string]any `json:"roleImages"`
	CommandAudit      []OperationRecord         `json:"commandAudit"`
	AuditRecordCount  int                       `json:"auditRecordCount"`
	AuditFinalDigest  string                    `json:"auditFinalDigest"`
	AuditSealed       bool                      `json:"auditSealed"`
	Attempts          []map[string]any          `json:"attempts"`
}

type o4SourceRange struct {
	StartSequence int    `json:"startSequence"`
	EndSequence   int    `json:"endSequence"`
	SliceDigest   string `json:"sliceDigest"`
}

type o4StoredFile struct {
	path   string
	bytes  []byte
	info   os.FileInfo
	exists bool
}

type o4PreparedFile struct {
	path      string
	temporary string
	mode      os.FileMode
}

type o4DirectoryAuthority struct {
	path string
	info os.FileInfo
}

var o4ProductIDPattern = regexp.MustCompile(`^(task|run|attempt)_[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
var o4OpaqueIDPattern = regexp.MustCompile(`^(env|ins|gen|wsp)_[A-Za-z0-9_-]{24,80}$`)

var o4PublisherLocks sync.Map

var o4CContracts = []struct {
	profile string
	name    string
}{
	{"minimal", "01-minimal-resources.json"},
	{"standard", "02-standard-1-resources.json"},
	{"standard", "03-standard-2-resources.json"},
	{"standard", "04-standard-3-resources.json"},
	{"trusted_local", "05-trusted-local-resources.json"},
}

var o4DContracts = []struct {
	scenario string
	name     string
}{
	{"failure", "01-failure-resources.json"},
	{"cancel", "02-cancel-resources.json"},
	{"timeout", "03-timeout-resources.json"},
	{"retry", "04-retry-resources.json"},
	{"restart", "05-restart-resources.json"},
	{"orphan", "06-orphan-resources.json"},
	{"reopen", "07-reopen-resources.json"},
	{"apply", "08-apply-resources.json"},
}

// PublishO4Boundary publishes only product-authenticated selectors joined to a
// fresh, settled operation-ledger snapshot and an independent zero-residue
// observation. The live ledgers are mutable owner-0600 files; the phase
// resource is the owner-0400 commit marker for the three-file transaction.
func (supervisor *Supervisor) PublishO4Boundary(ctx context.Context, request O4BoundaryRequest) (O4BoundaryResult, error) {
	if supervisor == nil || supervisor.operationLedger == nil {
		return O4BoundaryResult{}, errors.New("installed O4 Supervisor evidence authority is unavailable")
	}
	contract, err := validateO4BoundaryRequest(request)
	if err != nil {
		return O4BoundaryResult{}, err
	}
	resourcePath := filepath.Join(request.ResourceDirectory, contract.name)
	if err := validateO4OutputTopology(request, resourcePath); err != nil {
		return O4BoundaryResult{}, err
	}
	directoryAuthority, err := captureO4DirectoryAuthority(request)
	if err != nil {
		return O4BoundaryResult{}, err
	}

	// Serialize publishers without taking Supervisor.mu: qualification refreshes
	// the Supervisor identity under that mutex. The ledger's process-exclusive
	// flock and Snapshot validation authenticate the underlying journal.
	publisherLock := o4PublisherLock(supervisor)
	publisherLock.Lock()
	defer publisherLock.Unlock()
	supervisor.operationLedger.boundary.Lock()
	defer supervisor.operationLedger.boundary.Unlock()
	ctx = context.WithValue(ctx, operationLedgerBoundaryContextKey{}, supervisor.operationLedger)

	a3Stored, err := readO4LiveFile(request.A3Path)
	if err != nil {
		return O4BoundaryResult{}, err
	}
	verifierStored, err := readO4LiveFile(request.VerifierPath)
	if err != nil {
		return O4BoundaryResult{}, err
	}
	if a3Stored.exists != verifierStored.exists {
		return O4BoundaryResult{}, errors.New("partial O4 live evidence output is forbidden")
	}
	if _, err := os.Lstat(resourcePath); !errors.Is(err, os.ErrNotExist) {
		return O4BoundaryResult{}, errors.New("O4 phase resource replay or replacement is forbidden")
	}

	var a3 o4A3Ledger
	var verifier o4VerifierLedger
	if a3Stored.exists {
		if err := decodeO4Exact(a3Stored.bytes, &a3); err != nil {
			return O4BoundaryResult{}, errors.New("decode O4 A3 live ledger failed")
		}
		if err := decodeO4Exact(verifierStored.bytes, &verifier); err != nil {
			return O4BoundaryResult{}, errors.New("decode O4 Verifier live ledger failed")
		}
		if err := validateO4LiveLedgers(a3, verifier, request); err != nil {
			return O4BoundaryResult{}, err
		}
		if len(verifier.Boundaries) == 0 {
			return O4BoundaryResult{}, errors.New("prepopulated O4 live ledgers have no authenticated boundary")
		}
	} else {
		if request.Phase != O4BoundaryPhaseC || request.Sequence != 1 {
			return O4BoundaryResult{}, errors.New("O4 boundary sequence has no authenticated live-ledger prefix")
		}
		a3, verifier, err = newO4LiveLedgers(request)
		if err != nil {
			return O4BoundaryResult{}, err
		}
	}
	if err := validateO4NextBoundary(verifier.Boundaries, request); err != nil {
		return O4BoundaryResult{}, err
	}
	if err := validateO4PriorResources(verifier.Boundaries); err != nil {
		return O4BoundaryResult{}, err
	}

	before, err := supervisor.operationLedger.Snapshot()
	if err != nil {
		return O4BoundaryResult{}, errors.New("O4 operation ledger is not settled before observation")
	}
	previousCount := 0
	previousBoundaryDigest := o4EvidenceZeroPreviousDigest
	if len(verifier.Boundaries) > 0 {
		previous := verifier.Boundaries[len(verifier.Boundaries)-1]
		previousCount = previous.OperationLedgerCount
		previousBoundaryDigest = previous.Digest
	}
	if before.AuditRecordCount < previousCount {
		return O4BoundaryResult{}, errors.New("O4 operation ledger prefix was truncated")
	}
	if a3Stored.exists {
		if a3.AuditRecordCount != previousCount || previousCount > len(before.CommandAudit) ||
			(previousCount > 0 && before.CommandAudit[previousCount-1].Digest != a3.AuditFinalDigest) {
			return O4BoundaryResult{}, errors.New("O4 A3 projection does not match the live operation-ledger prefix")
		}
	}

	residue, err := supervisor.ObserveResidue(ctx)
	if err != nil {
		return O4BoundaryResult{}, fmt.Errorf("O4 residue observation failed: %w", err)
	}
	if err := validateO4ZeroResidue(residue); err != nil {
		return O4BoundaryResult{}, err
	}
	after, err := supervisor.operationLedger.Snapshot()
	if err != nil || after.AuditRecordCount != residue.OperationLedgerCount || after.AuditFinalDigest != residue.OperationLedgerSHA256 {
		return O4BoundaryResult{}, errors.New("O4 operation ledger changed outside the authenticated residue observation")
	}
	if after.AuditSealed {
		return O4BoundaryResult{}, errors.New("sealed O4 operation ledger cannot publish a live boundary")
	}

	resource, err := buildO4Resource(request, residue, before.CommandAudit, previousCount)
	if err != nil {
		return O4BoundaryResult{}, err
	}
	resourceBytes, err := marshalO4JSON(resource)
	if err != nil {
		return O4BoundaryResult{}, errors.New("encode O4 phase resource failed")
	}
	resourceSHA := o4SHA256(resourceBytes)

	boundary := o4EvidenceBoundary{
		Phase: string(request.Phase), Sequence: request.Sequence,
		TaskID: request.TaskID, RunID: request.RunID, AttemptID: request.AttemptID,
		AgentExecutionProfile: request.AgentExecutionProfile, ObservationDigest: request.ObservationDigest,
		OperationLedgerCount: after.AuditRecordCount, OperationLedgerSHA256: after.AuditFinalDigest,
		ResourcePath: resourcePath, ResourceSHA256: resourceSHA, PreviousDigest: previousBoundaryDigest,
	}
	boundary.Digest, err = o4CanonicalDigest(map[string]any{
		"phase": boundary.Phase, "sequence": boundary.Sequence, "taskId": boundary.TaskID,
		"runId": boundary.RunID, "attemptId": boundary.AttemptID,
		"agentExecutionProfile": boundary.AgentExecutionProfile, "observationDigest": boundary.ObservationDigest,
		"operationLedgerCount": boundary.OperationLedgerCount, "operationLedgerSha256": boundary.OperationLedgerSHA256,
		"resourcePath": boundary.ResourcePath, "resourceSha256": boundary.ResourceSHA256,
		"previousDigest": boundary.PreviousDigest,
	})
	if err != nil {
		return O4BoundaryResult{}, err
	}

	a3.CommandAudit = cloneOperationRecords(after.CommandAudit)
	a3.AuditRecordCount = after.AuditRecordCount
	a3.AuditFinalDigest = after.AuditFinalDigest
	if request.Phase == O4BoundaryPhaseC && request.Sequence <= 4 {
		attempt, attemptErr := buildO4ManagedCSourceAttempt(request, resourceSHA, before.CommandAudit, previousCount)
		if attemptErr != nil {
			return O4BoundaryResult{}, attemptErr
		}
		a3.Attempts = append(a3.Attempts, attempt)
	} else if request.Phase == O4BoundaryPhaseD {
		a3.Attempts = append(a3.Attempts, buildO4DSourceAttempt(request, resourceSHA, residue, boundary))
	}
	if request.Phase == O4BoundaryPhaseC || request.Phase == O4BoundaryPhaseD && request.Sequence >= 4 {
		verifier.Attempts = append(verifier.Attempts, buildO4VerifierAttempt(request, resourceSHA, residue, boundary))
	}
	verifier.Boundaries = append(verifier.Boundaries, boundary)

	a3Bytes, err := marshalO4JSON(a3)
	if err != nil {
		return O4BoundaryResult{}, errors.New("encode O4 A3 live ledger failed")
	}
	verifierBytes, err := marshalO4JSON(verifier)
	if err != nil {
		return O4BoundaryResult{}, errors.New("encode O4 Verifier live ledger failed")
	}
	if err := commitO4Evidence(a3Stored, verifierStored, directoryAuthority, resourcePath, a3Bytes, verifierBytes, resourceBytes); err != nil {
		return O4BoundaryResult{}, err
	}
	return O4BoundaryResult{
		Phase: request.Phase, Sequence: request.Sequence, ObservationDigest: request.ObservationDigest,
		OperationLedgerSHA256: after.AuditFinalDigest, OperationLedgerCount: after.AuditRecordCount,
		A3LedgerSHA256: o4SHA256(a3Bytes), VerifierLedgerSHA256: o4SHA256(verifierBytes),
		ResourcePath: resourcePath, ResourceSHA256: resourceSHA,
	}, nil
}

func o4PublisherLock(supervisor *Supervisor) *sync.Mutex {
	value, _ := o4PublisherLocks.LoadOrStore(supervisor, &sync.Mutex{})
	return value.(*sync.Mutex)
}

type o4BoundaryContract struct {
	name     string
	profile  string
	scenario string
}

func validateO4BoundaryRequest(request O4BoundaryRequest) (o4BoundaryContract, error) {
	var contract o4BoundaryContract
	switch request.Phase {
	case O4BoundaryPhaseC:
		if request.Sequence < 1 || request.Sequence > len(o4CContracts) {
			return contract, errors.New("O4 phase C sequence is out of range")
		}
		value := o4CContracts[request.Sequence-1]
		contract = o4BoundaryContract{name: value.name, profile: value.profile}
	case O4BoundaryPhaseD:
		if request.Sequence < 1 || request.Sequence > len(o4DContracts) {
			return contract, errors.New("O4 phase D sequence is out of range")
		}
		value := o4DContracts[request.Sequence-1]
		contract = o4BoundaryContract{name: value.name, profile: "standard", scenario: value.scenario}
	default:
		return contract, errors.New("O4 evidence phase is invalid")
	}
	if request.AgentExecutionProfile != contract.profile {
		return contract, errors.New("O4 evidence profile selector does not match phase sequence")
	}
	for prefix, value := range map[string]string{"task": request.TaskID, "run": request.RunID, "attempt": request.AttemptID} {
		if !o4ProductIDPattern.MatchString(value) || !strings.HasPrefix(value, prefix+"_") {
			return contract, errors.New("O4 product identity selector is invalid")
		}
	}
	if !validCanonicalDigest(request.ObservationDigest) || !validCanonicalDigest(request.TupleIdentity) ||
		!o4OpaqueIDPattern.MatchString(request.GenerationID) || !strings.HasPrefix(request.GenerationID, "gen_") {
		return contract, errors.New("O4 observation, tuple, or generation selector is invalid")
	}
	return contract, nil
}

func validateO4OutputTopology(request O4BoundaryRequest, resourcePath string) error {
	paths := []string{request.A3Path, request.VerifierPath, request.ResourceDirectory, resourcePath}
	for _, path := range paths {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
			return errors.New("O4 evidence output path is not canonical absolute")
		}
	}
	if request.A3Path == request.VerifierPath || request.A3Path == resourcePath || request.VerifierPath == resourcePath {
		return errors.New("O4 evidence output paths overlap")
	}
	for _, directory := range []string{filepath.Dir(request.A3Path), filepath.Dir(request.VerifierPath), request.ResourceDirectory} {
		if err := validateO4OwnerDirectory(directory); err != nil {
			return err
		}
	}
	return nil
}

func captureO4DirectoryAuthority(request O4BoundaryRequest) ([]o4DirectoryAuthority, error) {
	directories := uniqueO4Strings([]string{filepath.Dir(request.A3Path), filepath.Dir(request.VerifierPath), request.ResourceDirectory})
	authority := make([]o4DirectoryAuthority, 0, len(directories))
	for _, directory := range directories {
		if err := validateO4OwnerDirectory(directory); err != nil {
			return nil, err
		}
		info, err := os.Lstat(directory)
		if err != nil {
			return nil, errors.New("capture O4 output directory authority failed")
		}
		authority = append(authority, o4DirectoryAuthority{path: directory, info: info})
	}
	return authority, nil
}

func validateO4DirectoryAuthority(authority []o4DirectoryAuthority) error {
	for _, expected := range authority {
		if err := validateO4OwnerDirectory(expected.path); err != nil {
			return err
		}
		info, err := os.Lstat(expected.path)
		if err != nil || !os.SameFile(info, expected.info) {
			return errors.New("O4 output directory authority was replaced")
		}
	}
	return nil
}

func validateO4OwnerDirectory(path string) error {
	info, err := os.Lstat(path)
	resolved, resolveErr := filepath.EvalSymlinks(path)
	if err != nil || resolveErr != nil || resolved != path || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm() != 0o700 || !ownerOnly(info) {
		return errors.New("O4 evidence output directory is unsafe")
	}
	return nil
}

func readO4LiveFile(path string) (o4StoredFile, error) {
	stored := o4StoredFile{path: path}
	file, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return stored, nil
	}
	if err != nil {
		return stored, errors.New("open O4 live ledger failed")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || !ownerOnly(info) {
		return stored, errors.New("O4 live ledger is unsafe")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		return stored, errors.New("O4 live ledger links are forbidden")
	}
	bytes, err := io.ReadAll(io.LimitReader(file, o4EvidenceMaxBytes+1))
	if err != nil || len(bytes) == 0 || len(bytes) > o4EvidenceMaxBytes {
		return stored, errors.New("O4 live ledger size is invalid")
	}
	stored.bytes, stored.info, stored.exists = bytes, info, true
	return stored, nil
}

func decodeO4Exact(data []byte, output any) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing data")
	}
	return nil
}

func newO4LiveLedgers(request O4BoundaryRequest) (o4A3Ledger, o4VerifierLedger, error) {
	product := request.ProductObservation
	if !o4OpaqueIDPattern.MatchString(product.EnvironmentID) || !strings.HasPrefix(product.EnvironmentID, "env_") ||
		!o4OpaqueIDPattern.MatchString(product.InstallID) || !strings.HasPrefix(product.InstallID, "ins_") ||
		!validCanonicalDigest(product.BindingDigest) || !validText(product.Platform.OS, 64) || !validText(product.Platform.Architecture, 64) ||
		len(product.RoleImages) == 0 {
		return o4A3Ledger{}, o4VerifierLedger{}, errors.New("O4 installed product observation is incomplete")
	}
	roles := make(map[string]map[string]any, len(product.RoleImages))
	for role, image := range product.RoleImages {
		if !validText(role, 128) || !validText(image.ArtifactID, 256) || !validCanonicalDigest(image.ArchiveSHA256) ||
			image.ArchiveSize <= 0 || !validImageID(image.DockerConfigImageID) {
			return o4A3Ledger{}, o4VerifierLedger{}, errors.New("O4 installed role image observation is invalid")
		}
		roles[role] = map[string]any{"artifactId": image.ArtifactID, "archiveSha256": image.ArchiveSHA256, "archiveSize": image.ArchiveSize, "dockerConfigImageId": image.DockerConfigImageID}
	}
	a3 := o4A3Ledger{
		SchemaVersion: o4A3LedgerSchema, Status: o4EvidenceStatusRecording,
		EnvironmentID: product.EnvironmentID, InstallID: product.InstallID, GenerationID: request.GenerationID,
		Platform:      map[string]string{"os": product.Platform.OS, "architecture": product.Platform.Architecture},
		BindingDigest: product.BindingDigest, TupleIdentity: request.TupleIdentity,
		ObservationSource: "exact-runner-command-audit", Complete: false, EngineEventsUsed: false,
		RoleImages: roles, CommandAudit: []OperationRecord{}, AuditFinalDigest: zeroOperationDigest,
		AuditSealed: false, Attempts: []map[string]any{},
	}
	verifier := o4VerifierLedger{
		SchemaVersion: o4VerifierLedgerSchema, Status: o4EvidenceStatusRecording,
		EnvironmentID: product.EnvironmentID, InstallID: product.InstallID, GenerationID: request.GenerationID,
		BindingDigest: product.BindingDigest, TupleIdentity: request.TupleIdentity,
		Attempts: []map[string]any{}, Boundaries: []o4EvidenceBoundary{},
	}
	return a3, verifier, nil
}

func validateO4LiveLedgers(a3 o4A3Ledger, verifier o4VerifierLedger, request O4BoundaryRequest) error {
	if a3.SchemaVersion != o4A3LedgerSchema || a3.Status != o4EvidenceStatusRecording || a3.ObservationSource != "exact-runner-command-audit" ||
		a3.Complete || a3.EngineEventsUsed || a3.AuditSealed || verifier.SchemaVersion != o4VerifierLedgerSchema || verifier.Status != o4EvidenceStatusRecording {
		return errors.New("O4 live ledger schema or mutability state drifted")
	}
	if a3.EnvironmentID != verifier.EnvironmentID || a3.InstallID != verifier.InstallID || a3.GenerationID != request.GenerationID ||
		verifier.GenerationID != request.GenerationID || a3.BindingDigest != verifier.BindingDigest || a3.TupleIdentity != request.TupleIdentity ||
		verifier.TupleIdentity != request.TupleIdentity || !validCanonicalDigest(a3.BindingDigest) {
		return errors.New("O4 live ledger installed identity mismatch")
	}
	product := request.ProductObservation
	if product.EnvironmentID != a3.EnvironmentID || product.InstallID != a3.InstallID || product.BindingDigest != a3.BindingDigest ||
		product.Platform.OS != a3.Platform["os"] || product.Platform.Architecture != a3.Platform["architecture"] ||
		!equalO4RoleImages(a3.RoleImages, product.RoleImages) {
		return errors.New("O4 product observation does not match live-ledger installed identity")
	}
	if a3.AuditRecordCount != len(a3.CommandAudit) || len(a3.CommandAudit) > maxOperationRecords {
		return errors.New("O4 A3 operation ledger projection is invalid")
	}
	previousOperation := zeroOperationDigest
	for index, record := range a3.CommandAudit {
		if record.Sequence != index+1 || record.PreviousDigest != previousOperation || record.Digest != operationRecordDigest(record) {
			return errors.New("O4 A3 operation ledger chain is invalid")
		}
		previousOperation = record.Digest
	}
	if a3.AuditFinalDigest != previousOperation {
		return errors.New("O4 A3 operation ledger final digest is invalid")
	}
	previous := o4EvidenceZeroPreviousDigest
	for index, boundary := range verifier.Boundaries {
		if boundary.PreviousDigest != previous || boundary.Digest != digestO4Boundary(boundary) ||
			!validCanonicalDigest(boundary.ResourceSHA256) || !validCanonicalDigest(boundary.OperationLedgerSHA256) ||
			boundary.OperationLedgerCount < 0 || index > 0 && boundary.OperationLedgerCount < verifier.Boundaries[index-1].OperationLedgerCount {
			return errors.New("O4 Verifier boundary chain is invalid")
		}
		previous = boundary.Digest
	}
	if err := validateO4BoundaryChainContents(verifier.Boundaries); err != nil {
		return err
	}
	if err := validateO4AttemptPrefixes(a3.Attempts, verifier.Attempts, verifier.Boundaries, a3.TupleIdentity); err != nil {
		return err
	}
	return nil
}

func validateO4BoundaryChainContents(boundaries []o4EvidenceBoundary) error {
	for index, boundary := range boundaries {
		phase, sequence := "C", index+1
		if index >= len(o4CContracts) {
			phase, sequence = "D", index-len(o4CContracts)+1
		}
		if phase == "D" && sequence > len(o4DContracts) {
			return errors.New("O4 boundary chain exceeds the frozen sequence")
		}
		profile := "standard"
		name := o4DContracts[sequence-1].name
		if phase == "C" {
			profile = o4CContracts[sequence-1].profile
			name = o4CContracts[sequence-1].name
		}
		if boundary.Phase != phase || boundary.Sequence != sequence || boundary.AgentExecutionProfile != profile ||
			!o4ProductIDPattern.MatchString(boundary.TaskID) || !o4ProductIDPattern.MatchString(boundary.RunID) ||
			!o4ProductIDPattern.MatchString(boundary.AttemptID) || !validCanonicalDigest(boundary.ObservationDigest) ||
			filepath.Base(boundary.ResourcePath) != name {
			return errors.New("O4 boundary chain selector or filename drifted")
		}
	}
	return nil
}

func validateO4AttemptPrefixes(a3Attempts, verifierAttempts []map[string]any, boundaries []o4EvidenceBoundary, tupleIdentity string) error {
	expectedA3 := make([]o4EvidenceBoundary, 0, len(boundaries))
	expectedVerifier := make([]o4EvidenceBoundary, 0, len(boundaries))
	for _, boundary := range boundaries {
		if boundary.Phase == "C" && boundary.Sequence <= 4 || boundary.Phase == "D" {
			expectedA3 = append(expectedA3, boundary)
		}
		if boundary.Phase == "C" || boundary.Phase == "D" && boundary.Sequence >= 4 {
			expectedVerifier = append(expectedVerifier, boundary)
		}
	}
	if len(a3Attempts) != len(expectedA3) || len(verifierAttempts) != len(expectedVerifier) {
		return errors.New("O4 live ledger Attempt cardinality drifted")
	}
	for index, boundary := range expectedA3 {
		attempt := a3Attempts[index]
		if attempt["taskId"] != boundary.TaskID || attempt["runId"] != boundary.RunID || attempt["attemptId"] != boundary.AttemptID ||
			attempt["tupleIdentity"] != tupleIdentity || attempt["resourceLedgerSha256"] != boundary.ResourceSHA256 {
			return errors.New("O4 A3 Attempt binding drifted")
		}
		if boundary.Phase == "C" {
			if attempt["profile"] != boundary.AgentExecutionProfile || attempt["sourceAttemptOrdinal"] != json.Number(fmt.Sprint(boundary.Sequence)) && attempt["sourceAttemptOrdinal"] != float64(boundary.Sequence) {
				return errors.New("O4 phase C A3 Attempt order/profile drifted")
			}
		} else if attempt["phase"] != "D" || attempt["sequence"] != json.Number(fmt.Sprint(boundary.Sequence)) && attempt["sequence"] != float64(boundary.Sequence) {
			return errors.New("O4 phase D A3 Attempt order drifted")
		}
	}
	for index, boundary := range expectedVerifier {
		attempt := verifierAttempts[index]
		if attempt["phase"] != boundary.Phase || attempt["taskId"] != boundary.TaskID || attempt["runId"] != boundary.RunID ||
			attempt["attemptId"] != boundary.AttemptID || attempt["tupleIdentity"] != tupleIdentity ||
			attempt["resourceLedgerSha256"] != boundary.ResourceSHA256 || attempt["boundaryDigest"] != boundary.Digest {
			return errors.New("O4 Verifier Attempt binding drifted")
		}
	}
	return nil
}

func digestO4Boundary(boundary o4EvidenceBoundary) string {
	digest, _ := o4CanonicalDigest(map[string]any{
		"phase": boundary.Phase, "sequence": boundary.Sequence, "taskId": boundary.TaskID,
		"runId": boundary.RunID, "attemptId": boundary.AttemptID,
		"agentExecutionProfile": boundary.AgentExecutionProfile, "observationDigest": boundary.ObservationDigest,
		"operationLedgerCount": boundary.OperationLedgerCount, "operationLedgerSha256": boundary.OperationLedgerSHA256,
		"resourcePath": boundary.ResourcePath, "resourceSha256": boundary.ResourceSHA256,
		"previousDigest": boundary.PreviousDigest,
	})
	return digest
}

func validateO4NextBoundary(boundaries []o4EvidenceBoundary, request O4BoundaryRequest) error {
	if len(boundaries) == 0 {
		if request.Phase != O4BoundaryPhaseC || request.Sequence != 1 {
			return errors.New("O4 evidence sequence must begin with C/1")
		}
		return nil
	}
	last := boundaries[len(boundaries)-1]
	if last.Phase == "C" {
		if last.Sequence < len(o4CContracts) {
			if request.Phase != O4BoundaryPhaseC || request.Sequence != last.Sequence+1 {
				return errors.New("O4 phase C sequence gap or replay")
			}
			return nil
		}
		if request.Phase != O4BoundaryPhaseD || request.Sequence != 1 {
			return errors.New("O4 phase D must follow the complete phase C sequence")
		}
		return nil
	}
	if last.Phase != "D" || request.Phase != O4BoundaryPhaseD || request.Sequence != last.Sequence+1 || last.Sequence >= len(o4DContracts) {
		return errors.New("O4 phase D sequence gap or replay")
	}
	return nil
}

func validateO4PriorResources(boundaries []o4EvidenceBoundary) error {
	for _, boundary := range boundaries {
		file, err := os.OpenFile(boundary.ResourcePath, os.O_RDONLY|unix.O_NOFOLLOW, 0)
		if err != nil {
			return errors.New("O4 committed phase resource is missing")
		}
		info, statErr := file.Stat()
		data, readErr := io.ReadAll(io.LimitReader(file, o4EvidenceMaxBytes+1))
		closeErr := file.Close()
		if statErr != nil || info == nil {
			return errors.New("O4 committed phase resource identity changed")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if readErr != nil || closeErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o400 ||
			!ownerOnly(info) || !ok || stat.Nlink != 1 || len(data) == 0 || len(data) > o4EvidenceMaxBytes || o4SHA256(data) != boundary.ResourceSHA256 {
			return errors.New("O4 committed phase resource identity changed")
		}
		if err := validateO4PriorResourceContents(boundary, data); err != nil {
			return err
		}
	}
	return nil
}

func validateO4PriorResourceContents(boundary o4EvidenceBoundary, data []byte) error {
	var resource map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	if err := decoder.Decode(&resource); err != nil {
		return errors.New("decode committed O4 phase resource failed")
	}
	if resource["status"] != "terminal" || resource["taskId"] != boundary.TaskID || resource["runId"] != boundary.RunID ||
		resource["attemptId"] != boundary.AttemptID || resource["tupleIdentity"] == nil {
		return errors.New("committed O4 phase resource selector binding drifted")
	}
	if boundary.Phase == "C" {
		if resource["schemaVersion"] != o4ProfileResourceSchema || resource["profile"] != boundary.AgentExecutionProfile ||
			resource["tupleIdentity"] == "" {
			return errors.New("committed O4 phase C resource schema/profile drifted")
		}
		return nil
	}
	if resource["schemaVersion"] != o4RecoveryResourceSchema || resource["scenario"] != o4DContracts[boundary.Sequence-1].scenario ||
		resource["sequence"] != json.Number(fmt.Sprint(boundary.Sequence)) {
		return errors.New("committed O4 phase D resource schema/scenario drifted")
	}
	ledgerDigest, ok := resource["ledgerDigest"].(string)
	if !ok || !validCanonicalDigest(ledgerDigest) {
		return errors.New("committed O4 phase D resource digest is invalid")
	}
	delete(resource, "ledgerDigest")
	want, err := o4CanonicalDigest(resource)
	if err != nil || ledgerDigest != want {
		return errors.New("committed O4 phase D resource digest drifted")
	}
	return nil
}

func validateO4ZeroResidue(value ResidueObservation) error {
	counts := []ResidueCount{value.Containers, value.VerifierContainers, value.Networks, value.Volumes, value.Configs, value.ManagedWorkspaces, value.AttemptProcessGroups}
	if value.SchemaVersion != residueObservationSchema || value.Status != "proven" || !value.SwarmInactive || !value.ServingProcessExcluded ||
		!value.OperationLedgerSettled || !validCanonicalDigest(value.EngineQualificationSHA256) || !validCanonicalDigest(value.RuntimeScopeSHA256) ||
		!validCanonicalDigest(value.OperationLedgerSHA256) || value.OperationLedgerCount < 0 {
		return errors.New("O4 residue observation is not an authenticated terminal proof")
	}
	for _, count := range counts {
		if count.Count != 0 || !validCanonicalDigest(count.AggregateSHA256) {
			return errors.New("O4 terminal boundary has resource residue")
		}
	}
	return nil
}

func buildO4Resource(request O4BoundaryRequest, residue ResidueObservation, audit []OperationRecord, previousCount int) (map[string]any, error) {
	if request.Phase == O4BoundaryPhaseD {
		workspaceID := request.ProductObservation.WorkspaceID
		if !o4OpaqueIDPattern.MatchString(workspaceID) || !strings.HasPrefix(workspaceID, "wsp_") {
			return nil, errors.New("O4 phase D Workspace identity is invalid")
		}
		safe := map[string]any{
			"schemaVersion": o4RecoveryResourceSchema, "status": "terminal", "sequence": request.Sequence,
			"scenario": o4DContracts[request.Sequence-1].scenario, "tupleIdentity": request.TupleIdentity,
			"generationId": request.GenerationID, "taskId": request.TaskID, "runId": request.RunID,
			"attemptId": request.AttemptID, "workspaceId": workspaceID, "terminalResidue": o4ZeroResidueCounts(),
		}
		digest, _ := o4CanonicalDigest(safe)
		safe["ledgerDigest"] = digest
		return safe, nil
	}
	product := request.ProductObservation
	if !strings.HasPrefix(product.WorkspaceIdentity, "sha256:") || !validCanonicalDigest(strings.TrimPrefix(product.WorkspaceIdentity, "sha256:")) ||
		!validCanonicalDigest(product.RuntimeIdentitySHA256) {
		return nil, errors.New("O4 phase C workspace or runtime identity is invalid")
	}
	wantExecution, _ := o4CanonicalDigest(map[string]any{
		"tupleIdentity": request.TupleIdentity, "taskId": request.TaskID, "runId": request.RunID,
		"attemptId": request.AttemptID, "workspaceIdentity": product.WorkspaceIdentity,
	})
	if product.ExecutionIdentityDigest != wantExecution {
		return nil, errors.New("O4 phase C execution identity digest mismatch")
	}
	resource := map[string]any{
		"schemaVersion": o4ProfileResourceSchema, "status": "terminal", "profile": request.AgentExecutionProfile,
		"taskId": request.TaskID, "runId": request.RunID, "attemptId": request.AttemptID,
		"workspaceIdentity": product.WorkspaceIdentity, "tupleIdentity": request.TupleIdentity,
		"executionIdentityDigest": product.ExecutionIdentityDigest, "runtimeIdentitySha256": product.RuntimeIdentitySHA256,
	}
	if request.AgentExecutionProfile != "trusted_local" {
		if product.TrustedHost != nil {
			return nil, errors.New("managed O4 profile carries Trusted Host facts")
		}
		resource["managedInventory"] = o4EmptyInventory()
		resource["trustedHost"] = nil
		return resource, nil
	}
	if product.TrustedHost == nil {
		return nil, errors.New("Trusted Local O4 product observation is missing")
	}
	wantRuntime, _ := o4CanonicalDigest(map[string]any{
		"runtimeFingerprint":         product.TrustedHost.RuntimeFingerprint,
		"processGroupIdentitySha256": product.TrustedHost.ProcessGroupIdentitySHA256,
		"sessionIdentitySha256":      product.TrustedHost.SessionIdentitySHA256,
	})
	if product.RuntimeIdentitySHA256 != wantRuntime {
		return nil, errors.New("Trusted Local O4 runtime identity digest mismatch")
	}
	trusted, err := buildO4TrustedHost(product.TrustedHost, audit, previousCount)
	if err != nil {
		return nil, err
	}
	resource["managedInventory"] = nil
	resource["trustedHost"] = trusted
	return resource, nil
}

func buildO4TrustedHost(value *O4TrustedHostObservation, audit []OperationRecord, previousCount int) (map[string]any, error) {
	if value == nil || !filepath.IsAbs(value.SelectedPiPath) || filepath.Clean(value.SelectedPiPath) != value.SelectedPiPath ||
		!filepath.IsAbs(value.SelectedPiSourceRoot) || filepath.Clean(value.SelectedPiSourceRoot) != value.SelectedPiSourceRoot ||
		value.SelectedPiPath == value.SelectedPiSourceRoot || !slices.Contains([]string{"path", "private_fallback"}, value.SelectedSource) {
		return nil, errors.New("Trusted Local O4 product observation has invalid source topology")
	}
	for _, digest := range []string{value.PiExecutableSHA256, value.PiSourceProvenanceSHA256, value.RuntimeFingerprint, value.ProcessGroupIdentitySHA256, value.SessionIdentitySHA256} {
		if !validCanonicalDigest(digest) {
			return nil, errors.New("Trusted Local O4 product identity digest is invalid")
		}
	}
	started, startErr := time.Parse(time.RFC3339Nano, value.ProcessStartedAt)
	exited, exitErr := time.Parse(time.RFC3339Nano, value.ProcessExitedAt)
	if startErr != nil || exitErr != nil || exited.Before(started) || !value.ProcessGroupTerminated || !value.SessionClosed {
		return nil, errors.New("Trusted Local O4 process/session lifecycle is not terminal")
	}
	verifierRange, err := o4PhaseRange(audit, previousCount, string(OperationPhaseVerifier), true)
	if err != nil {
		return nil, err
	}
	inventory := o4EmptyInventory()
	cleanupDigest, _ := o4CanonicalDigest(inventory)
	if value.TerminalCleanupDigest != cleanupDigest {
		return nil, errors.New("Trusted Local O4 terminal inventory digest is not proven empty")
	}
	return map[string]any{
		"selectedPiPath": value.SelectedPiPath, "selectedPiSourceRoot": value.SelectedPiSourceRoot,
		"selectedSource": value.SelectedSource, "piExecutableSha256": value.PiExecutableSHA256,
		"piSourceProvenanceSha256": value.PiSourceProvenanceSHA256, "runtimeFingerprint": value.RuntimeFingerprint,
		"processGroupIdentitySha256": value.ProcessGroupIdentitySHA256, "sessionIdentitySha256": value.SessionIdentitySHA256,
		"processStartedAt": value.ProcessStartedAt, "processExitedAt": value.ProcessExitedAt,
		"processGroupTerminated": true, "sessionClosed": true, "terminalCleanupDigest": value.TerminalCleanupDigest,
		"terminalInventory": inventory, "verifierSource": verifierRange,
	}, nil
}

func buildO4ManagedCSourceAttempt(request O4BoundaryRequest, resourceSHA string, audit []OperationRecord, previousCount int) (map[string]any, error) {
	attemptRange, err := o4PhaseRange(audit, previousCount, string(OperationPhaseAttempt), true)
	if err != nil {
		return nil, err
	}
	verifierRange, err := o4PhaseRange(audit, previousCount, string(OperationPhaseVerifier), true)
	if err != nil {
		return nil, err
	}
	cohort := any(nil)
	if request.AgentExecutionProfile == "standard" {
		cohort = request.Sequence - 1
	}
	product := request.ProductObservation
	roleTuple, _ := o4CanonicalDigest(roleImagesForDigest(product.RoleImages))
	return map[string]any{
		"sourceAttemptOrdinal": request.Sequence, "profile": request.AgentExecutionProfile, "cohortSequence": cohort,
		"taskId": request.TaskID, "runId": request.RunID, "attemptId": request.AttemptID, "scenario": nil,
		"attemptState": "output_submitted", "terminalReason": nil, "verifierRequired": true,
		"verifierStartSequence": verifierRange.StartSequence, "verifierEndSequence": verifierRange.EndSequence,
		"verifierSliceDigest": verifierRange.SliceDigest, "workspaceIdentity": product.WorkspaceIdentity,
		"tupleIdentity": request.TupleIdentity, "executionIdentityDigest": product.ExecutionIdentityDigest,
		"status": "succeeded", "runtimeIdentitySha256": product.RuntimeIdentitySHA256,
		"resourceLedgerSha256": resourceSHA, "roleTupleDigest": roleTuple,
		"frameworkRetryCount": 0, "hiddenRetryCount": 0,
		"auditStartSequence": attemptRange.StartSequence, "auditEndSequence": attemptRange.EndSequence,
		"auditSliceDigest": attemptRange.SliceDigest, "operationCounts": o4ImageOperationCounts(audit, attemptRange),
		"terminalResidue": o4ZeroResidueCounts(),
	}, nil
}

func buildO4DSourceAttempt(request O4BoundaryRequest, resourceSHA string, residue ResidueObservation, boundary o4EvidenceBoundary) map[string]any {
	contract := o4DContracts[request.Sequence-1]
	terminalStatus := map[string]string{"failure": "failed", "cancel": "canceled", "timeout": "timed_out", "retry": "succeeded", "restart": "succeeded", "orphan": "recovered", "reopen": "succeeded", "apply": "succeeded"}[contract.scenario]
	return map[string]any{
		"phase": "D", "sequence": request.Sequence, "scenario": contract.scenario,
		"taskId": request.TaskID, "runId": request.RunID, "attemptId": request.AttemptID,
		"workspaceId": request.ProductObservation.WorkspaceID, "tupleIdentity": request.TupleIdentity,
		"generationId": request.GenerationID, "terminalStatus": terminalStatus,
		"observationDigest": request.ObservationDigest, "resourceLedgerSha256": resourceSHA,
		"operationLedgerCount": residue.OperationLedgerCount, "operationLedgerSha256": residue.OperationLedgerSHA256,
		"boundaryDigest": boundary.Digest, "terminalResidue": o4ZeroResidueCounts(),
	}
}

func buildO4VerifierAttempt(request O4BoundaryRequest, resourceSHA string, residue ResidueObservation, boundary o4EvidenceBoundary) map[string]any {
	return map[string]any{
		"phase": string(request.Phase), "sequence": request.Sequence, "taskId": request.TaskID,
		"runId": request.RunID, "attemptId": request.AttemptID, "tupleIdentity": request.TupleIdentity,
		"generationId": request.GenerationID, "observationDigest": request.ObservationDigest,
		"resourceLedgerSha256": resourceSHA, "operationLedgerCount": residue.OperationLedgerCount,
		"operationLedgerSha256": residue.OperationLedgerSHA256, "boundaryDigest": boundary.Digest,
		"status": "passed",
	}
}

func o4PhaseRange(records []OperationRecord, previousCount int, phase string, required bool) (*o4SourceRange, error) {
	if previousCount < 0 || previousCount > len(records) {
		return nil, errors.New("O4 operation range prefix is invalid")
	}
	start, end := -1, -1
	for index := previousCount; index < len(records); index++ {
		if records[index].Phase != phase {
			continue
		}
		if start < 0 {
			start, end = index, index
			continue
		}
		if index != end+1 {
			return nil, errors.New("O4 operation phase range is split or ambiguous")
		}
		end = index
	}
	if start < 0 {
		if required {
			return nil, errors.New("O4 operation phase range is missing")
		}
		return nil, nil
	}
	digest, err := o4CanonicalDigest(records[start : end+1])
	if err != nil {
		return nil, err
	}
	return &o4SourceRange{StartSequence: start + 1, EndSequence: end + 1, SliceDigest: digest}, nil
}

func o4ImageOperationCounts(records []OperationRecord, source *o4SourceRange) map[string]int {
	counts := map[string]int{"build": 0, "pull": 0, "load": 0}
	for _, record := range records[source.StartSequence-1 : source.EndSequence] {
		if _, ok := counts[record.OperationClass]; ok {
			counts[record.OperationClass]++
		}
	}
	return counts
}

func roleImagesForDigest(images map[string]O4RoleImageObservation) map[string]map[string]any {
	result := make(map[string]map[string]any, len(images))
	for role, image := range images {
		result[role] = map[string]any{"artifactId": image.ArtifactID, "archiveSha256": image.ArchiveSHA256, "archiveSize": image.ArchiveSize, "dockerConfigImageId": image.DockerConfigImageID}
	}
	return result
}

func equalO4RoleImages(actual map[string]map[string]any, observed map[string]O4RoleImageObservation) bool {
	want := roleImagesForDigest(observed)
	actualBytes, actualErr := json.Marshal(actual)
	wantBytes, wantErr := json.Marshal(want)
	return actualErr == nil && wantErr == nil && slices.Equal(actualBytes, wantBytes)
}

func o4EmptyInventory() map[string]any {
	return map[string]any{
		"ownedContainers": []any{}, "ownedNetworks": []any{}, "ownedVolumes": []any{}, "ownedConfigs": []any{},
		"ownedWorkspaces": []any{}, "ownedProcessGroups": []any{}, "activeReferences": []any{}, "recoverableReferences": []any{},
	}
}

func o4ZeroResidueCounts() map[string]int {
	return map[string]int{
		"ownedContainers": 0, "ownedNetworks": 0, "ownedVolumes": 0, "ownedConfigs": 0,
		"ownedWorkspaces": 0, "ownedProcessGroups": 0, "activeReferences": 0, "recoverableReferences": 0,
	}
}

func marshalO4JSON(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	encoded = append(encoded, '\n')
	if len(encoded) > o4EvidenceMaxBytes {
		return nil, errors.New("O4 evidence exceeds durable size bound")
	}
	return encoded, nil
}

func o4CanonicalDigest(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.UseNumber()
	var canonical any
	if err := decoder.Decode(&canonical); err != nil {
		return "", err
	}
	encoded, err = json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func o4SHA256(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func commitO4Evidence(a3Stored, verifierStored o4StoredFile, authority []o4DirectoryAuthority, resourcePath string, a3Bytes, verifierBytes, resourceBytes []byte) error {
	if err := validateO4DirectoryAuthority(authority); err != nil {
		return err
	}
	prepared := make([]o4PreparedFile, 0, 3)
	for _, output := range []struct {
		path string
		data []byte
		mode os.FileMode
	}{{a3Stored.path, a3Bytes, 0o600}, {verifierStored.path, verifierBytes, 0o600}, {resourcePath, resourceBytes, 0o400}} {
		item, err := prepareO4File(output.path, output.data, output.mode)
		if err != nil {
			cleanupO4Prepared(prepared)
			return err
		}
		prepared = append(prepared, item)
	}
	if err := revalidateO4Stored(a3Stored); err != nil {
		cleanupO4Prepared(prepared)
		return err
	}
	if err := revalidateO4Stored(verifierStored); err != nil {
		cleanupO4Prepared(prepared)
		return err
	}
	if _, err := os.Lstat(resourcePath); !errors.Is(err, os.ErrNotExist) {
		cleanupO4Prepared(prepared)
		return errors.New("O4 phase resource appeared during publication")
	}
	if err := validateO4DirectoryAuthority(authority); err != nil {
		cleanupO4Prepared(prepared)
		return err
	}

	committed := 0
	for index, item := range prepared[:2] {
		if err := os.Rename(item.temporary, item.path); err != nil {
			rollbackErr := rollbackO4Evidence(a3Stored, verifierStored, resourcePath, committed)
			cleanupO4Prepared(prepared[index:])
			return errors.Join(errors.New("commit O4 evidence failed"), rollbackErr)
		}
		prepared[index].temporary = ""
		committed++
	}
	// The immutable resource is the commit marker. Link, rather than rename,
	// gives creation O_EXCL semantics and can never overwrite a raced path.
	resource := prepared[2]
	if err := os.Link(resource.temporary, resource.path); err != nil {
		rollbackErr := rollbackO4Evidence(a3Stored, verifierStored, resourcePath, committed)
		cleanupO4Prepared(prepared[2:])
		return errors.Join(errors.New("commit O4 phase resource failed"), rollbackErr)
	}
	committed++
	if err := os.Remove(resource.temporary); err != nil {
		rollbackErr := rollbackO4Evidence(a3Stored, verifierStored, resourcePath, committed)
		cleanupO4Prepared(prepared[2:])
		return errors.Join(errors.New("finalize O4 phase resource failed"), rollbackErr)
	}
	prepared[2].temporary = ""
	directories := uniqueO4Strings([]string{filepath.Dir(a3Stored.path), filepath.Dir(verifierStored.path), filepath.Dir(resourcePath)})
	for _, directory := range directories {
		if err := syncO4Directory(directory); err != nil {
			return errors.New("sync O4 evidence directory failed")
		}
	}
	if err := validateO4DirectoryAuthority(authority); err != nil {
		return err
	}
	return nil
}

func prepareO4File(path string, data []byte, mode os.FileMode) (o4PreparedFile, error) {
	file, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-")
	if err != nil {
		return o4PreparedFile{}, errors.New("create O4 evidence temporary failed")
	}
	temporary := file.Name()
	fail := func(err error) (o4PreparedFile, error) {
		_ = file.Close()
		_ = os.Remove(temporary)
		return o4PreparedFile{}, err
	}
	if err := file.Chmod(mode); err != nil {
		return fail(errors.New("secure O4 evidence temporary failed"))
	}
	if _, err := file.Write(data); err != nil {
		return fail(errors.New("write O4 evidence temporary failed"))
	}
	if err := file.Sync(); err != nil {
		return fail(errors.New("sync O4 evidence temporary failed"))
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(temporary)
		return o4PreparedFile{}, errors.New("close O4 evidence temporary failed")
	}
	return o4PreparedFile{path: path, temporary: temporary, mode: mode}, nil
}

func revalidateO4Stored(stored o4StoredFile) error {
	current, err := readO4LiveFile(stored.path)
	if err != nil {
		return err
	}
	if current.exists != stored.exists {
		return errors.New("O4 live ledger identity changed before commit")
	}
	if stored.exists && (!os.SameFile(current.info, stored.info) || !slices.Equal(current.bytes, stored.bytes)) {
		return errors.New("O4 live ledger was replaced before commit")
	}
	return nil
}

func rollbackO4Evidence(a3Stored, verifierStored o4StoredFile, resourcePath string, committed int) error {
	var result error
	if committed >= 3 {
		result = errors.Join(result, os.Remove(resourcePath))
	}
	if committed >= 2 {
		result = errors.Join(result, restoreO4Stored(verifierStored))
	}
	if committed >= 1 {
		result = errors.Join(result, restoreO4Stored(a3Stored))
	}
	return result
}

func restoreO4Stored(stored o4StoredFile) error {
	if !stored.exists {
		if err := os.Remove(stored.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	prepared, err := prepareO4File(stored.path, stored.bytes, 0o600)
	if err != nil {
		return err
	}
	if err := os.Rename(prepared.temporary, stored.path); err != nil {
		_ = os.Remove(prepared.temporary)
		return err
	}
	return syncO4Directory(filepath.Dir(stored.path))
}

func cleanupO4Prepared(files []o4PreparedFile) {
	for _, file := range files {
		if file.temporary != "" {
			_ = os.Remove(file.temporary)
		}
	}
}

func syncO4Directory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	err = directory.Sync()
	return errors.Join(err, directory.Close())
}

func uniqueO4Strings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if !slices.Contains(result, value) {
			result = append(result, value)
		}
	}
	return result
}
