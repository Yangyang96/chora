package localweb

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/Yangyang96/chora/internal/dockersupervisor"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/pidistribution"
	"github.com/Yangyang96/chora/internal/releaseassets"
)

const (
	o4RunnerSessionSchema    = "chora.m1-o4-runner-session-authority.v1"
	o4ObservationSchema      = "chora.m1-o4-product-observation.v1"
	o4BoundaryResponseSchema = "chora.m1-o4-evidence-boundary-response.v1"
	o4MaxAuthorityBytes      = 64 << 10
)

// O4EvidenceOptions is an optional, installed-product-only evidence boundary.
// NewProduct accepts either nil or one complete value; no individual field has
// a default and the development constructor never enables the boundary.
type O4EvidenceOptions struct {
	RunnerSessionAuthorityPath   string
	RunnerSessionAuthoritySHA256 string
	InstalledDoctorReportPath    string
	InstalledDoctorReportSHA256  string
	PreflightFingerprint         string
	TupleIdentity                string
	GenerationID                 string
	A3LedgerPath                 string
	VerifierLedgerPath           string
	CResourceDirectory           string
	DResourceDirectory           string
	ActivatedRelease             *releaseassets.ActivatedRelease
	LocalPiSelection             pidistribution.Selection
}

type o4EvidencePublisher interface {
	PublishO4Boundary(context.Context, dockersupervisor.O4BoundaryRequest) (dockersupervisor.O4BoundaryResult, error)
}

type o4EvidenceBoundary struct {
	authority        o4RunnerSessionAuthority
	authorityPath    string
	authorityRawSHA  string
	doctorReportPath string
	doctorReportSHA  string
	doctorDigest     string
	tupleIdentity    string
	generationID     string
	a3Path           string
	verifierPath     string
	cResourceDir     string
	dResourceDir     string
	roleImages       map[string]dockersupervisor.O4RoleImageObservation
	bindingDigest    string
	localPiSelection pidistribution.Selection
	publisher        o4EvidencePublisher
}

type o4RunnerSessionAuthority struct {
	SchemaVersion    string           `json:"schemaVersion"`
	Status           string           `json:"status"`
	ManifestSHA256   string           `json:"manifestSha256"`
	RunnerSHA256     string           `json:"runnerSha256"`
	SocketPathDigest string           `json:"socketPathDigest"`
	TupleIdentity    string           `json:"tupleIdentity"`
	Identity         o4RunnerIdentity `json:"identity"`
	ParentPID        int              `json:"parentPid"`
	SessionID        string           `json:"sessionId"`
	SessionSecret    string           `json:"sessionSecret"`
	SequenceStart    int              `json:"sequenceStart"`
	AuthorityDigest  string           `json:"authorityDigest"`
}

type o4RunnerIdentity struct {
	EnvironmentID string           `json:"environmentId"`
	InstallID     string           `json:"installId"`
	GenerationID  string           `json:"generationId"`
	Platform      o4RunnerPlatform `json:"platform"`
}

type o4RunnerPlatform struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
}

type o4InstalledDoctorReport struct {
	SchemaVersion      string              `json:"schemaVersion"`
	Status             string              `json:"status"`
	EnvironmentID      string              `json:"environmentId"`
	InstallID          string              `json:"installId"`
	GenerationID       string              `json:"generationId"`
	Platform           o4RunnerPlatform    `json:"platform"`
	Bindings           o4InstalledBindings `json:"bindings"`
	BindingDigest      string              `json:"bindingDigest"`
	SetupReceiptSHA256 string              `json:"setupReceiptSha256"`
	ModelAuthoritySHA  string              `json:"modelRequestAuthoritySha256"`
	EngineSHA          string              `json:"engineQualificationSha256"`
	ModelSHA           string              `json:"modelObservationSha256"`
	InputFingerprint   string              `json:"inputFingerprint"`
	ReadOnly           bool                `json:"readOnly"`
	ModelAuthenticated bool                `json:"modelAuthenticated"`
	ResourcesCreated   bool                `json:"resourcesCreated"`
	MutationsAttempted int                 `json:"mutationsAttempted"`
	Digest             string              `json:"digest"`
}

type o4InstalledBindings struct {
	Candidate map[string]any                          `json:"candidate"`
	Product   map[string]any                          `json:"product"`
	Release   map[string]any                          `json:"release"`
	Pi        map[string]any                          `json:"pi"`
	Engine    map[string]any                          `json:"engine"`
	Roles     map[string]o4InstalledDoctorRoleBinding `json:"roles"`
}

type o4InstalledDoctorRoleBinding struct {
	ArtifactID          string `json:"artifactId"`
	ArchiveSHA256       string `json:"archiveSha256"`
	ArchiveSize         int64  `json:"archiveSize"`
	DockerConfigImageID string `json:"dockerConfigImageId"`
	PolicyDigest        string `json:"policyDigest"`
}

type o4BoundaryInput struct {
	Phase             string `json:"phase"`
	Sequence          int    `json:"sequence"`
	TaskID            string `json:"taskId"`
	RunID             string `json:"runId"`
	AttemptID         string `json:"attemptId"`
	ObservationDigest string `json:"observationDigest"`
	SessionID         string `json:"sessionId"`
	TupleIdentity     string `json:"tupleIdentity"`
	GenerationID      string `json:"generationId"`
	MAC               string `json:"mac"`
}

// Field declaration order is canonical JSON key order. Node's existing
// canonicalJSONStringify sorts object keys lexicographically.
type o4AuthenticatedSelectors struct {
	AttemptID         string `json:"attemptId"`
	ObservationDigest string `json:"observationDigest"`
	Phase             string `json:"phase"`
	RunID             string `json:"runId"`
	Sequence          int    `json:"sequence"`
	TaskID            string `json:"taskId"`
}

type o4ProductProjection struct {
	SchemaVersion string                    `json:"schemaVersion"`
	Phase         string                    `json:"phase"`
	Sequence      int                       `json:"sequence"`
	TaskID        string                    `json:"taskId"`
	RunID         string                    `json:"runId"`
	AttemptID     string                    `json:"attemptId"`
	Run           o4RunProjection           `json:"run"`
	Attempt       o4AttemptProjection       `json:"attempt"`
	Verification  *o4VerificationProjection `json:"verification"`
	Patch         o4PatchProjection         `json:"patch"`
}

type o4RunProjection struct {
	State          domain.RunState `json:"state"`
	Version        uint64          `json:"version"`
	TerminalReason string          `json:"terminalReason"`
}

type o4AttemptProjection struct {
	Sequence int                          `json:"sequence"`
	State    domain.AttemptState          `json:"state"`
	Profile  domain.AgentExecutionProfile `json:"profile"`
	Runtime  *runtimeIdentityView         `json:"runtime"`
}

type o4VerificationProjection struct {
	ID       string                            `json:"id"`
	State    string                            `json:"state"`
	Bindings verificationBindingsView          `json:"bindings"`
	Attempts []o4VerificationAttemptProjection `json:"attempts"`
	Result   *o4VerificationResultProjection   `json:"result"`
}

type o4VerificationAttemptProjection struct {
	ID                string `json:"id"`
	Sequence          int    `json:"sequence"`
	State             string `json:"state"`
	EvidenceComplete  bool   `json:"evidenceComplete"`
	CleanupProven     bool   `json:"cleanupProven"`
	WorkspaceIdentity string `json:"workspaceIdentity"`
	Reason            string `json:"reason"`
}

type o4VerificationResultProjection struct {
	ID        string `json:"id"`
	AttemptID string `json:"attemptId"`
	Outcome   string `json:"outcome"`
}

type o4PatchProjection struct {
	ReviewablePatchDigest  string `json:"reviewablePatchDigest"`
	ApplicationState       string `json:"applicationState"`
	ApplicationPatchDigest string `json:"applicationPatchDigest"`
	TargetIdentity         string `json:"targetIdentity"`
}

type o4BoundaryResponse struct {
	SchemaVersion         string `json:"schemaVersion"`
	Status                string `json:"status"`
	Phase                 string `json:"phase"`
	Sequence              int    `json:"sequence"`
	ObservationDigest     string `json:"observationDigest"`
	OperationLedgerSHA256 string `json:"operationLedgerSha256"`
	OperationLedgerCount  int    `json:"operationLedgerCount"`
	A3LedgerSHA256        string `json:"a3LedgerSha256"`
	VerifierLedgerSHA256  string `json:"verifierLedgerSha256"`
	ResourcePath          string `json:"resourcePath"`
	ResourceSHA256        string `json:"resourceSha256"`
}

func newO4EvidenceBoundary(options *O4EvidenceOptions, publisher o4EvidencePublisher) (*o4EvidenceBoundary, error) {
	if options == nil {
		return nil, nil
	}
	if publisher == nil || !validO4Digest(options.RunnerSessionAuthoritySHA256) ||
		!validO4Digest(options.InstalledDoctorReportSHA256) || !validO4Digest(options.PreflightFingerprint) ||
		!validO4Digest(options.TupleIdentity) || options.GenerationID == "" {
		return nil, errors.New("O4 evidence producer requires complete installed authority")
	}
	for _, path := range []string{options.RunnerSessionAuthorityPath, options.InstalledDoctorReportPath, options.A3LedgerPath, options.VerifierLedgerPath, options.CResourceDirectory, options.DResourceDirectory} {
		if !canonicalO4Absolute(path) {
			return nil, errors.New("O4 evidence producer paths must be canonical absolute paths")
		}
	}
	paths := []string{options.RunnerSessionAuthorityPath, options.InstalledDoctorReportPath, options.A3LedgerPath, options.VerifierLedgerPath, options.CResourceDirectory, options.DResourceDirectory}
	for left := range paths {
		for right := left + 1; right < len(paths); right++ {
			if pathsOverlapO4(paths[left], paths[right]) {
				return nil, errors.New("O4 evidence producer paths overlap")
			}
		}
	}
	for _, path := range []string{options.RunnerSessionAuthorityPath, options.InstalledDoctorReportPath, options.CResourceDirectory, options.DResourceDirectory} {
		if err := validateO4SymlinkFreePath(path, false); err != nil {
			return nil, err
		}
	}
	for _, path := range []string{options.A3LedgerPath, options.VerifierLedgerPath} {
		if err := validateO4SymlinkFreePath(path, true); err != nil {
			return nil, err
		}
	}
	authority, rawSHA, err := loadO4RunnerAuthority(options.RunnerSessionAuthorityPath, options.RunnerSessionAuthoritySHA256)
	if err != nil {
		return nil, err
	}
	if authority.TupleIdentity != options.TupleIdentity || authority.Identity.GenerationID != options.GenerationID {
		return nil, errors.New("O4 Runner session tuple or generation drifted")
	}
	report, err := loadO4InstalledDoctorReport(options.InstalledDoctorReportPath, options.InstalledDoctorReportSHA256)
	if err != nil {
		return nil, err
	}
	if report.GenerationID != options.GenerationID || report.InputFingerprint != options.PreflightFingerprint ||
		report.EnvironmentID != authority.Identity.EnvironmentID || report.InstallID != authority.Identity.InstallID ||
		report.Platform != authority.Identity.Platform {
		return nil, errors.New("O4 installed Doctor/session identity drifted")
	}
	if err := validateO4Directory(options.CResourceDirectory); err != nil {
		return nil, fmt.Errorf("secure O4 C resource directory: %w", err)
	}
	if err := validateO4Directory(options.DResourceDirectory); err != nil {
		return nil, fmt.Errorf("secure O4 D resource directory: %w", err)
	}
	roleImages, err := o4RoleImages(options.ActivatedRelease)
	if err != nil {
		return nil, err
	}
	if err := verifyO4DoctorRoleImages(report.Bindings.Roles, roleImages); err != nil {
		return nil, err
	}
	return &o4EvidenceBoundary{
		authority: authority, authorityPath: options.RunnerSessionAuthorityPath, authorityRawSHA: rawSHA,
		doctorReportPath: options.InstalledDoctorReportPath, doctorReportSHA: options.InstalledDoctorReportSHA256, doctorDigest: report.Digest,
		tupleIdentity: options.TupleIdentity, generationID: options.GenerationID,
		a3Path: options.A3LedgerPath, verifierPath: options.VerifierLedgerPath,
		cResourceDir: options.CResourceDirectory, dResourceDir: options.DResourceDirectory,
		roleImages: roleImages, bindingDigest: report.BindingDigest, localPiSelection: options.LocalPiSelection, publisher: publisher,
	}, nil
}

func (server *Server) publishO4EvidenceBoundary(writer http.ResponseWriter, request *http.Request) {
	if server.o4Evidence == nil {
		http.NotFound(writer, request)
		return
	}
	var input o4BoundaryInput
	if err := decodeO4BoundaryInput(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	result, err := server.o4Evidence.publish(request.Context(), server, input)
	if err != nil {
		server.logger.Printf("O4 evidence boundary rejected: %v", err)
		writeError(writer, http.StatusConflict, errors.New("O4 evidence boundary rejected"))
		return
	}
	writeJSON(writer, http.StatusCreated, result)
}

func decodeO4BoundaryInput(request *http.Request, target *o4BoundaryInput) error {
	defer request.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(request.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("O4 evidence request contains trailing JSON")
		}
		return err
	}
	return nil
}

func (boundary *o4EvidenceBoundary) publish(ctx context.Context, server *Server, input o4BoundaryInput) (o4BoundaryResponse, error) {
	if err := boundary.revalidateAuthority(); err != nil {
		return o4BoundaryResponse{}, err
	}
	phase, resourceDir, err := validateO4BoundaryInput(input)
	if err != nil {
		return o4BoundaryResponse{}, err
	}
	if phase == dockersupervisor.O4BoundaryPhaseC {
		resourceDir = boundary.cResourceDir
	} else {
		resourceDir = boundary.dResourceDir
	}
	if input.SessionID != boundary.authority.SessionID || input.TupleIdentity != boundary.tupleIdentity || input.GenerationID != boundary.generationID {
		return o4BoundaryResponse{}, errors.New("O4 evidence session binding drifted")
	}
	selectors := o4AuthenticatedSelectors{AttemptID: input.AttemptID, ObservationDigest: input.ObservationDigest, Phase: input.Phase, RunID: input.RunID, Sequence: input.Sequence, TaskID: input.TaskID}
	if err := authenticateO4Selectors(boundary.authority.SessionSecret, selectors, input.MAC); err != nil {
		return o4BoundaryResponse{}, err
	}
	taskID, taskErr := domain.ParseTaskID(input.TaskID)
	runID, runErr := domain.ParseRunID(input.RunID)
	attemptID, attemptErr := domain.ParseAttemptID(input.AttemptID)
	if taskErr != nil || runErr != nil || attemptErr != nil {
		return o4BoundaryResponse{}, errors.New("O4 evidence selectors are invalid")
	}
	view, err := server.runView(ctx, runID)
	if err != nil {
		return o4BoundaryResponse{}, fmt.Errorf("reload O4 product Run: %w", err)
	}
	if view.ID != input.RunID || view.Task.ID != input.TaskID || view.AttemptDetail == nil || view.AttemptDetail.ID != input.AttemptID {
		return o4BoundaryResponse{}, errors.New("O4 evidence selectors do not name the current product Attempt")
	}
	projection := projectO4ProductObservation(input.Phase, input.Sequence, view)
	projectionDigest, err := digestCanonicalJSON(projection)
	if err != nil || subtle.ConstantTimeCompare([]byte(projectionDigest), []byte(input.ObservationDigest)) != 1 {
		return o4BoundaryResponse{}, errors.New("O4 public observation digest drifted")
	}
	productObservation, profile, err := boundary.productObservation(ctx, server, taskID, runID, attemptID, view)
	if err != nil {
		return o4BoundaryResponse{}, err
	}
	result, err := boundary.publisher.PublishO4Boundary(ctx, dockersupervisor.O4BoundaryRequest{
		Phase: phase, Sequence: input.Sequence, TaskID: input.TaskID, RunID: input.RunID, AttemptID: input.AttemptID,
		AgentExecutionProfile: profile, ObservationDigest: projectionDigest, TupleIdentity: boundary.tupleIdentity,
		GenerationID: boundary.generationID, A3Path: boundary.a3Path, VerifierPath: boundary.verifierPath,
		ResourceDirectory: resourceDir, ProductObservation: productObservation,
	})
	if err != nil {
		return o4BoundaryResponse{}, err
	}
	return o4BoundaryResponse{
		SchemaVersion: o4BoundaryResponseSchema, Status: "published", Phase: input.Phase, Sequence: input.Sequence,
		ObservationDigest: result.ObservationDigest, OperationLedgerSHA256: result.OperationLedgerSHA256,
		OperationLedgerCount: result.OperationLedgerCount, A3LedgerSHA256: result.A3LedgerSHA256,
		VerifierLedgerSHA256: result.VerifierLedgerSHA256, ResourcePath: result.ResourcePath, ResourceSHA256: result.ResourceSHA256,
	}, nil
}

func authenticateO4Selectors(secret string, selectors o4AuthenticatedSelectors, encodedMAC string) error {
	macBytes, err := hex.DecodeString(encodedMAC)
	if err != nil || len(macBytes) != sha256.Size {
		return errors.New("O4 evidence authentication is malformed")
	}
	canonicalSelectors, _ := json.Marshal(selectors)
	wantMAC := hmac.New(sha256.New, []byte(secret))
	_, _ = wantMAC.Write(canonicalSelectors)
	if !hmac.Equal(macBytes, wantMAC.Sum(nil)) {
		return errors.New("O4 evidence authentication failed")
	}
	return nil
}

func validateO4BoundaryInput(input o4BoundaryInput) (dockersupervisor.O4BoundaryPhase, string, error) {
	if !validO4Digest(input.ObservationDigest) || !validO4Digest(input.TupleIdentity) || !validO4Digest(input.SessionID) || !validO4Digest(input.MAC) || input.GenerationID == "" {
		return "", "", errors.New("O4 evidence request is invalid")
	}
	switch input.Phase {
	case "C":
		if input.Sequence < 1 || input.Sequence > 5 {
			return "", "", errors.New("O4 C evidence sequence is invalid")
		}
		return dockersupervisor.O4BoundaryPhaseC, "", nil
	case "D":
		if input.Sequence < 1 || input.Sequence > 8 {
			return "", "", errors.New("O4 D evidence sequence is invalid")
		}
		return dockersupervisor.O4BoundaryPhaseD, "", nil
	default:
		return "", "", errors.New("O4 evidence phase is invalid")
	}
}

func projectO4ProductObservation(phase string, sequence int, view runView) o4ProductProjection {
	projection := o4ProductProjection{
		SchemaVersion: o4ObservationSchema, Phase: phase, Sequence: sequence,
		TaskID: view.Task.ID, RunID: view.ID, Run: o4RunProjection{State: view.Status, Version: view.Version, TerminalReason: view.TerminalReason},
		Patch: o4PatchProjection{},
	}
	if view.AttemptDetail != nil {
		projection.AttemptID = view.AttemptDetail.ID
		projection.Attempt = o4AttemptProjection{Sequence: view.AttemptDetail.Sequence, State: view.AttemptDetail.State, Runtime: view.AttemptDetail.Runtime}
		if view.AttemptDetail.AgentExecution != nil {
			projection.Attempt.Profile = view.AttemptDetail.AgentExecution.Profile
		}
	}
	if view.Verification != nil {
		verification := &o4VerificationProjection{ID: view.Verification.ID, State: view.Verification.State, Bindings: view.Verification.Bindings, Attempts: []o4VerificationAttemptProjection{}}
		for _, attempt := range view.Verification.Attempts {
			verification.Attempts = append(verification.Attempts, o4VerificationAttemptProjection{
				ID: attempt.ID, Sequence: attempt.Sequence, State: attempt.State, EvidenceComplete: attempt.EvidenceComplete,
				CleanupProven: attempt.CleanupProven, WorkspaceIdentity: attempt.WorkspaceIdentity, Reason: attempt.Reason,
			})
		}
		if view.Verification.Result != nil {
			verification.Result = &o4VerificationResultProjection{ID: view.Verification.Result.ID, AttemptID: view.Verification.Result.AttemptID, Outcome: view.Verification.Result.Outcome}
		}
		projection.Verification = verification
	}
	if view.ReviewablePatch != nil {
		projection.Patch.ReviewablePatchDigest = view.ReviewablePatch.PatchDigest
	}
	if view.PatchApplication != nil {
		projection.Patch.ApplicationState = view.PatchApplication.State
		projection.Patch.ApplicationPatchDigest = view.PatchApplication.PatchDigest
		projection.Patch.TargetIdentity = view.PatchApplication.TargetIdentity
	}
	return projection
}

func (boundary *o4EvidenceBoundary) productObservation(ctx context.Context, server *Server, taskID domain.TaskID, runID domain.RunID, attemptID domain.AttemptID, view runView) (dockersupervisor.O4ProductObservation, string, error) {
	reader := server.store.Reader()
	run, err := reader.GetRun(ctx, runID)
	if err != nil || run.TaskID() != taskID {
		return dockersupervisor.O4ProductObservation{}, "", errors.New("O4 product Run binding drifted")
	}
	attempt, err := reader.GetAttempt(ctx, attemptID)
	if err != nil || attempt.RunID() != runID || view.AttemptDetail == nil || attempt.ID().String() != view.AttemptDetail.ID {
		return dockersupervisor.O4ProductObservation{}, "", errors.New("O4 product Attempt binding drifted")
	}
	profile := string(attempt.AgentExecutionProfileBinding().Profile())
	observation := dockersupervisor.O4ProductObservation{
		EnvironmentID: boundary.authority.Identity.EnvironmentID, InstallID: boundary.authority.Identity.InstallID,
		BindingDigest: boundary.bindingDigest,
		Platform:      dockersupervisor.O4PlatformObservation{OS: boundary.authority.Identity.Platform.OS, Architecture: boundary.authority.Identity.Platform.Architecture},
		RoleImages:    cloneO4RoleImages(boundary.roleImages),
	}
	if view.Verification != nil && len(view.Verification.Attempts) > 0 {
		observation.WorkspaceIdentity = view.Verification.Attempts[len(view.Verification.Attempts)-1].WorkspaceIdentity
	}
	worktree, err := reader.GetTaskWorktreeBinding(ctx, taskID)
	if err != nil {
		return dockersupervisor.O4ProductObservation{}, "", fmt.Errorf("reload O4 Task workspace: %w", err)
	}
	configuredRootFingerprint := worktree.ConfiguredRootFingerprint()
	workspaceDocument := map[string]any{
		"configuredRootFingerprint": hex.EncodeToString(configuredRootFingerprint[:]),
		"pinnedBaseRevision":        worktree.PinnedBaseRevision(), "relativeLocator": worktree.RelativeLocator(),
		"repositoryIdentity": worktree.RepositoryIdentity(), "taskId": taskID.String(),
	}
	workspaceDigest, _ := digestCanonicalJSON(workspaceDocument)
	observation.WorkspaceID = "wsp_" + workspaceDigest
	session, err := reader.GetRuntimeSessionForAttempt(ctx, attemptID)
	if err != nil {
		return dockersupervisor.O4ProductObservation{}, "", fmt.Errorf("reload O4 Runtime Session: %w", err)
	}
	observation.RuntimeIdentitySHA256 = hex.EncodeToString(session.RuntimeFingerprint[:])
	executionIdentity := map[string]any{
		"attemptId": attemptID.String(), "runId": runID.String(), "taskId": taskID.String(),
		"tupleIdentity": boundary.tupleIdentity, "workspaceIdentity": observation.WorkspaceIdentity,
	}
	observation.ExecutionIdentityDigest, _ = digestCanonicalJSON(executionIdentity)
	if profile == string(domain.AgentExecutionProfileTrustedLocal) {
		selection := boundary.localPiSelection
		if !selection.Configured() || session.StartedAt.IsZero() || session.TerminalAt.IsZero() || session.FinalizedAt.IsZero() {
			return dockersupervisor.O4ProductObservation{}, "", errors.New("O4 Trusted Local terminal identity is unavailable")
		}
		executableDigest := selection.ExecutableSHA256()
		closureDigest := selection.ClosureSHA256()
		selectedSource := "path"
		if selection.Kind() == pidistribution.SelectionPrivate {
			selectedSource = "private_fallback"
		}
		processDigest := digestText([]byte(session.ProcessIdentity))
		sessionDigest := digestText([]byte(session.ID.String()))
		trustedRuntime, _ := digestCanonicalJSON(map[string]any{
			"processGroupIdentitySha256": processDigest, "runtimeFingerprint": hex.EncodeToString(session.RuntimeFingerprint[:]),
			"sessionIdentitySha256": sessionDigest,
		})
		terminalCleanupDigest, _ := digestCanonicalJSON(map[string]any{
			"activeReferences": []any{}, "ownedConfigs": []any{}, "ownedContainers": []any{}, "ownedNetworks": []any{},
			"ownedProcessGroups": []any{}, "ownedVolumes": []any{}, "ownedWorkspaces": []any{}, "recoverableReferences": []any{},
		})
		observation.RuntimeIdentitySHA256 = trustedRuntime
		observation.TrustedHost = &dockersupervisor.O4TrustedHostObservation{
			SelectedPiPath: selection.ResolvedPath(), SelectedPiSourceRoot: selection.PackageRoot(), SelectedSource: selectedSource,
			PiExecutableSHA256: hex.EncodeToString(executableDigest[:]), PiSourceProvenanceSHA256: hex.EncodeToString(closureDigest[:]),
			RuntimeFingerprint: hex.EncodeToString(session.RuntimeFingerprint[:]), ProcessGroupIdentitySHA256: processDigest,
			SessionIdentitySHA256: sessionDigest, ProcessStartedAt: session.StartedAt.UTC().Format(time.RFC3339Nano),
			ProcessExitedAt: session.TerminalAt.UTC().Format(time.RFC3339Nano), ProcessGroupTerminated: true, SessionClosed: session.State == "stopped" && !session.FinalizedAt.IsZero(),
			TerminalCleanupDigest: terminalCleanupDigest,
		}
	}
	return observation, profile, nil
}

func loadO4RunnerAuthority(path, expectedSHA string) (o4RunnerSessionAuthority, string, error) {
	data, err := readOwnerO4File(path, 0o400, o4MaxAuthorityBytes)
	if err != nil {
		return o4RunnerSessionAuthority{}, "", fmt.Errorf("read O4 Runner session authority: %w", err)
	}
	raw := sha256.Sum256(data)
	rawSHA := hex.EncodeToString(raw[:])
	if subtle.ConstantTimeCompare([]byte(rawSHA), []byte(expectedSHA)) != 1 {
		return o4RunnerSessionAuthority{}, "", errors.New("O4 Runner session raw SHA-256 drifted")
	}
	var generic map[string]any
	if err := json.Unmarshal(data, &generic); err != nil {
		return o4RunnerSessionAuthority{}, "", errors.New("O4 Runner session authority JSON is invalid")
	}
	wantKeys := []string{"authorityDigest", "identity", "manifestSha256", "parentPid", "runnerSha256", "schemaVersion", "sequenceStart", "sessionId", "sessionSecret", "socketPathDigest", "status", "tupleIdentity"}
	if !exactO4Keys(generic, wantKeys) {
		return o4RunnerSessionAuthority{}, "", errors.New("O4 Runner session authority fields drifted")
	}
	var authority o4RunnerSessionAuthority
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&authority); err != nil {
		return o4RunnerSessionAuthority{}, "", errors.New("O4 Runner session authority is invalid")
	}
	if authority.SchemaVersion != o4RunnerSessionSchema || authority.Status != "exclusive" || authority.SequenceStart != 0 || authority.ParentPID <= 1 ||
		!validO4Digest(authority.ManifestSHA256) || !validO4Digest(authority.RunnerSHA256) || !validO4Digest(authority.SocketPathDigest) ||
		!validO4Digest(authority.TupleIdentity) || !validO4Digest(authority.SessionID) || !validO4Digest(authority.SessionSecret) || !validO4Digest(authority.AuthorityDigest) ||
		authority.Identity.EnvironmentID == "" || authority.Identity.InstallID == "" || authority.Identity.GenerationID == "" ||
		authority.Identity.Platform.OS == "" || authority.Identity.Platform.Architecture == "" {
		return o4RunnerSessionAuthority{}, "", errors.New("O4 Runner session authority binding is invalid")
	}
	delete(generic, "authorityDigest")
	canonical, _ := json.Marshal(generic)
	digest := sha256.Sum256(canonical)
	if subtle.ConstantTimeCompare([]byte(hex.EncodeToString(digest[:])), []byte(authority.AuthorityDigest)) != 1 {
		return o4RunnerSessionAuthority{}, "", errors.New("O4 Runner session authority digest drifted")
	}
	return authority, rawSHA, nil
}

func loadO4InstalledDoctorReport(path, expectedSHA string) (o4InstalledDoctorReport, error) {
	data, err := readOwnerO4File(path, 0o400, 1<<20)
	if err != nil {
		return o4InstalledDoctorReport{}, fmt.Errorf("read O4 installed Doctor report: %w", err)
	}
	rawDigest := sha256.Sum256(data)
	if subtle.ConstantTimeCompare([]byte(hex.EncodeToString(rawDigest[:])), []byte(expectedSHA)) != 1 {
		return o4InstalledDoctorReport{}, errors.New("O4 installed Doctor report raw SHA-256 drifted")
	}
	var generic map[string]any
	if err := json.Unmarshal(data, &generic); err != nil {
		return o4InstalledDoctorReport{}, errors.New("O4 installed Doctor report JSON is invalid")
	}
	want := []string{"bindingDigest", "bindings", "digest", "engineQualificationSha256", "environmentId", "generationId", "inputFingerprint", "installId", "modelAuthenticated", "modelObservationSha256", "modelRequestAuthoritySha256", "mutationsAttempted", "platform", "readOnly", "resourcesCreated", "schemaVersion", "setupReceiptSha256", "status"}
	if !exactO4Keys(generic, want) {
		return o4InstalledDoctorReport{}, errors.New("O4 installed Doctor report fields drifted")
	}
	var report o4InstalledDoctorReport
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		return o4InstalledDoctorReport{}, errors.New("O4 installed Doctor report is invalid")
	}
	if report.SchemaVersion != "chora.m1-o4-installed-doctor-report.v2" || report.Status != "passed" ||
		!report.ReadOnly || !report.ModelAuthenticated || report.ResourcesCreated || report.MutationsAttempted != 0 ||
		!validO4Digest(report.BindingDigest) || !validO4Digest(report.InputFingerprint) || !validO4Digest(report.Digest) ||
		!validO4Digest(report.SetupReceiptSHA256) || !validO4Digest(report.ModelAuthoritySHA) || !validO4Digest(report.EngineSHA) || !validO4Digest(report.ModelSHA) ||
		report.EnvironmentID == "" || report.InstallID == "" || report.GenerationID == "" || report.Platform.OS == "" || report.Platform.Architecture == "" {
		return o4InstalledDoctorReport{}, errors.New("O4 installed Doctor report binding is invalid")
	}
	bindingsDigest, err := digestCanonicalJSON(report.Bindings)
	if err != nil || validateO4InstalledBindings(report.Bindings) != nil || subtle.ConstantTimeCompare([]byte(bindingsDigest), []byte(report.BindingDigest)) != 1 {
		return o4InstalledDoctorReport{}, errors.New("O4 installed Doctor binding digest drifted")
	}
	delete(generic, "digest")
	canonical, _ := json.Marshal(generic)
	recordDigest := sha256.Sum256(canonical)
	if subtle.ConstantTimeCompare([]byte(hex.EncodeToString(recordDigest[:])), []byte(report.Digest)) != 1 {
		return o4InstalledDoctorReport{}, errors.New("O4 installed Doctor report digest drifted")
	}
	return report, nil
}

func validateO4InstalledBindings(bindings o4InstalledBindings) error {
	sets := []struct {
		value map[string]any
		keys  []string
	}{
		{bindings.Candidate, []string{"candidateManifestSha256", "sourceManifestSha256", "sourceAggregateSha256"}},
		{bindings.Product, []string{"binarySha256", "webAggregateSha256"}},
		{bindings.Release, []string{"offlineBundleEvidenceSha256", "releaseManifestSha256", "releaseSpecSha256"}},
		{bindings.Engine, []string{"endpointEvidenceSha256", "qualificationSha256"}},
	}
	for _, set := range sets {
		if !exactO4Keys(set.value, set.keys) {
			return errors.New("installed Doctor binding fields drifted")
		}
		for _, value := range set.value {
			text, ok := value.(string)
			if !ok || !validO4Digest(text) {
				return errors.New("installed Doctor binding digest is invalid")
			}
		}
	}
	if !exactO4Keys(bindings.Pi, []string{"pathProvenanceSha256", "privateProvenanceSha256", "selected"}) {
		return errors.New("installed Doctor Pi binding fields drifted")
	}
	selected, _ := bindings.Pi["selected"].(string)
	pathDigest, _ := bindings.Pi["pathProvenanceSha256"].(string)
	privateDigest, _ := bindings.Pi["privateProvenanceSha256"].(string)
	if selected != "path" && selected != "private" || !validO4Digest(pathDigest) || !validO4Digest(privateDigest) || pathDigest == privateDigest {
		return errors.New("installed Doctor Pi binding is invalid")
	}
	wantRoles := []string{releaseassets.RoleCapabilityProbe, releaseassets.RoleIndependentVerifier, releaseassets.RoleManagedPiRuntime, releaseassets.RoleNetworkBoundary}
	if len(bindings.Roles) != len(wantRoles) {
		return errors.New("installed Doctor role binding set drifted")
	}
	for _, role := range wantRoles {
		value, ok := bindings.Roles[role]
		if !ok || value.ArtifactID == "" || !validO4Digest(value.ArchiveSHA256) || value.ArchiveSize <= 0 ||
			!strings.HasPrefix(value.DockerConfigImageID, "sha256:") || !validO4Digest(strings.TrimPrefix(value.DockerConfigImageID, "sha256:")) || !validO4Digest(value.PolicyDigest) {
			return errors.New("installed Doctor role binding is invalid")
		}
	}
	return nil
}

func verifyO4DoctorRoleImages(report map[string]o4InstalledDoctorRoleBinding, installed map[string]dockersupervisor.O4RoleImageObservation) error {
	if len(report) != len(installed) {
		return errors.New("O4 installed Doctor role set drifted")
	}
	for role, image := range installed {
		bound, ok := report[role]
		if !ok || bound.ArtifactID != image.ArtifactID || bound.ArchiveSHA256 != image.ArchiveSHA256 ||
			bound.ArchiveSize != image.ArchiveSize || bound.DockerConfigImageID != image.DockerConfigImageID ||
			!validO4Digest(bound.PolicyDigest) {
			return errors.New("O4 installed Doctor role identity drifted")
		}
	}
	return nil
}

func (boundary *o4EvidenceBoundary) revalidateAuthority() error {
	authority, rawSHA, err := loadO4RunnerAuthority(boundary.authorityPath, boundary.authorityRawSHA)
	if err != nil || authority.AuthorityDigest != boundary.authority.AuthorityDigest || rawSHA != boundary.authorityRawSHA {
		return errors.New("O4 Runner session authority changed")
	}
	report, err := loadO4InstalledDoctorReport(boundary.doctorReportPath, boundary.doctorReportSHA)
	if err != nil || report.Digest != boundary.doctorDigest || report.BindingDigest != boundary.bindingDigest {
		return errors.New("O4 installed Doctor authority changed")
	}
	return nil
}

func o4RoleImages(release *releaseassets.ActivatedRelease) (map[string]dockersupervisor.O4RoleImageObservation, error) {
	if release == nil {
		return nil, errors.New("O4 evidence producer requires the activated release")
	}
	result := make(map[string]dockersupervisor.O4RoleImageObservation, 4)
	for _, role := range []string{releaseassets.RoleManagedPiRuntime, releaseassets.RoleNetworkBoundary, releaseassets.RoleIndependentVerifier, releaseassets.RoleCapabilityProbe} {
		image, err := release.ImageForRole(role)
		if err != nil {
			return nil, err
		}
		result[role] = dockersupervisor.O4RoleImageObservation{ArtifactID: image.ArtifactID, ArchiveSHA256: image.ArchiveSHA256, ArchiveSize: image.ArchiveSize, DockerConfigImageID: image.LocalDockerConfigImageID}
	}
	return result, nil
}

func cloneO4RoleImages(source map[string]dockersupervisor.O4RoleImageObservation) map[string]dockersupervisor.O4RoleImageObservation {
	result := make(map[string]dockersupervisor.O4RoleImageObservation, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func digestCanonicalJSON(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return "", err
	}
	canonical, err := json.Marshal(generic)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

func digestText(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func validO4Digest(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && strings.ToLower(value) == value
}

func exactO4Keys(value map[string]any, want []string) bool {
	got := make([]string, 0, len(value))
	for key := range value {
		got = append(got, key)
	}
	sort.Strings(got)
	sort.Strings(want)
	return strings.Join(got, "\x00") == strings.Join(want, "\x00")
}

func canonicalO4Absolute(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path && path != string(filepath.Separator)
}

func pathsOverlapO4(left, right string) bool {
	relative, err := filepath.Rel(left, right)
	if err == nil && (relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		return true
	}
	relative, err = filepath.Rel(right, left)
	return err == nil && (relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func readOwnerO4File(path string, mode os.FileMode, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode() != mode || !info.Mode().IsRegular() {
		return nil, errors.New("path is not an exact owner-only regular file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 || int(stat.Uid) != os.Getuid() {
		return nil, errors.New("path ownership or link count is unsafe")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("path changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("file exceeds its bound")
	}
	return data, nil
}

func validateO4Directory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || info.Mode() != os.ModeDir|0o700 {
		return errors.New("path is not an exact owner-private directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() {
		return errors.New("directory ownership is unsafe")
	}
	return nil
}

func validateO4SymlinkFreePath(path string, finalMayBeAbsent bool) error {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		if resolved != path {
			return errors.New("O4 evidence path contains a symbolic link")
		}
		return nil
	}
	if !finalMayBeAbsent || !errors.Is(err, os.ErrNotExist) {
		return errors.New("O4 evidence path is unavailable")
	}
	parent := filepath.Dir(path)
	resolvedParent, parentErr := filepath.EvalSymlinks(parent)
	if parentErr != nil || resolvedParent != parent {
		return errors.New("O4 evidence output parent contains a symbolic link")
	}
	return nil
}
