package store

import (
	"context"
	"errors"
	"time"

	"github.com/Yangyang96/chora/internal/contextcore"
	"github.com/Yangyang96/chora/internal/domain"
)

var (
	ErrNotFound                  = errors.New("store record not found")
	ErrVersionConflict           = errors.New("store aggregate version conflict")
	ErrIdempotencyConflict       = errors.New("store idempotency conflict")
	ErrEventSequenceConflict     = errors.New("store event sequence conflict")
	ErrActiveRunExists           = errors.New("store active run already exists")
	ErrCommitUnknown             = errors.New("store commit state unknown")
	ErrCandidateConflict         = errors.New("store candidate state conflict")
	ErrSpecCodingConflict        = errors.New("store spec coding binding conflict")
	ErrVerificationConflict      = errors.New("store verification conflict")
	ErrPlanningConflict          = errors.New("store technical planning conflict")
	ErrPatchApplicationConflict  = errors.New("store patch application conflict")
	ErrTaskWorktreeConflict      = errors.New("store Task worktree binding conflict")
	ErrExecutionProfileConflict  = errors.New("store Agent execution profile conflict")
	ErrLegacyPlanningData        = errors.New("store incompatible legacy task planning data")
	ErrRoomStateForbidden        = errors.New("store room state forbids mutation")
	ErrRepositoryBindingConflict = errors.New("store repository binding conflict")
)

const (
	SpecCodingMaterialized = "materialized"
	SpecCodingRegistered   = "registered"
)

type SpecCodingBinding struct {
	TaskID                     domain.TaskID
	SnapshotID                 domain.ContextSnapshotID
	SnapshotDigest             [32]byte
	MaterializedContractDigest [32]byte
	MaterializedContractJSON   []byte
	ActiveContractDigest       [32]byte
	ActiveContractJSON         []byte
	Status                     string
	MaterializedAt             time.Time
	RegisteredAt               time.Time
}

// SpecCodingIntent is the immutable, digest-bound declaration captured when a
// user creates a Real Spec Coding Task. Snapshot and Charter identities are
// deliberately absent: those resources do not exist until Plan acceptance.
type SpecCodingIntent struct {
	TaskID       domain.TaskID
	IntentDigest [32]byte
	IntentJSON   []byte
	CreatedAt    time.Time
}

type VerifiedReviewRoute struct {
	DecisionID      domain.ReviewDecisionID
	SourceRunID     domain.RunID
	SourceTaskID    domain.TaskID
	RejectionClass  domain.ReviewRejectionClass
	PlanningDraftID domain.TechnicalPlanDraftID
	RelatedTaskID   domain.TaskID
	CreatedAt       time.Time
}

type RoomTaskCounts struct {
	Total    int
	Open     int
	Terminal int
}

type RoomDirectoryItem struct {
	Room           domain.Room
	LastActivityAt time.Time
	TaskCounts     RoomTaskCounts
	Tasks          []TaskWorkspaceItem
}

type RoomLifecycleEvent struct {
	RoomID                            domain.RoomID
	FromState, ToState                domain.RoomState
	Version                           uint64
	ActorID, SessionID                string
	IdempotencyKeyHash, RequestDigest [32]byte
	OccurredAt                        time.Time
}

type TaskWorkspaceItem struct {
	Task                                    domain.Task
	CreatedAt, UpdatedAt, LastActivityAt    time.Time
	RunCount, ActiveRunCount                int
	OpenDraftID                             domain.TechnicalPlanDraftID
	OpenDraftPredecessorRevisionID          domain.TechnicalPlanRevisionID
	OpenDraftEditVersion                    uint64
	LatestRevisionID                        domain.TechnicalPlanRevisionID
	LatestRevisionNumber                    uint64
	LatestRevisionSubmittedAt               time.Time
	LatestPlanReviewID                      domain.TechnicalPlanReviewID
	LatestPlanReviewRevisionID              domain.TechnicalPlanRevisionID
	LatestPlanReviewKind                    domain.TechnicalPlanReviewKind
	LatestPlanReviewDecidedAt               time.Time
	AcceptedRevisionID                      domain.TechnicalPlanRevisionID
	LatestRun                               *domain.AgentRun
	LatestRunBindingRevisionID              domain.TechnicalPlanRevisionID
	LatestVerificationResultID              domain.ResultID
	LatestVerificationResultOutcome         domain.ResultOutcome
	LatestVerifiedReviewID                  domain.ReviewDecisionID
	LatestVerifiedReviewKind                domain.ReviewDecisionKind
	LatestVerifiedReviewRunVersion          uint64
	LatestRejectionClass                    domain.ReviewRejectionClass
	RouteRejectionClass                     domain.ReviewRejectionClass
	RouteSourceRunID                        domain.RunID
	RouteSourceTaskID                       domain.TaskID
	RoutePlanningDraftID                    domain.TechnicalPlanDraftID
	RoutePlanningDraftPredecessorRevisionID domain.TechnicalPlanRevisionID
	RouteRelatedTaskID                      domain.TaskID
	RouteRelatedTaskPredecessorTaskID       domain.TaskID
	LatestPatchApplicationState             domain.PatchApplicationState
	LatestPatchApplicationVersion           uint64
	HasTaskBranchDelivery                   bool
}

type RoomWorkspaceProjection struct {
	Room  RoomDirectoryItem
	Tasks []TaskWorkspaceItem
}

type RunSummary struct {
	Run        domain.AgentRun
	EventCount int
}

type Store interface {
	Close() error
	Reader() Reader
	WithinWriteTx(context.Context, func(WriteTx) error) error
	contextcore.PromotionPort
}

type Reader interface {
	ResultClosureReader
	DeliveryReader
	ResourceReader
	GetProject(context.Context, domain.ProjectID) (domain.Project, error)
	GetProjectSettings(context.Context, domain.ProjectID) (domain.ProjectSettings, error)
	ListProjects(context.Context, domain.ProjectState) ([]domain.Project, error)
	ListProjectRooms(context.Context, domain.ProjectID) ([]domain.Room, error)
	GetRoom(context.Context, domain.RoomID) (domain.Room, error)
	ListRoomDirectory(context.Context, domain.RoomState) ([]RoomDirectoryItem, error)
	ListRoomLifecycleEvents(context.Context, domain.RoomID) ([]RoomLifecycleEvent, error)
	GetRoomWorkspace(context.Context, domain.RoomID) (RoomWorkspaceProjection, error)
	ListTaskRunHistory(context.Context, domain.RoomID, domain.TaskID) ([]RunSummary, error)
	GetTaskRun(context.Context, domain.RoomID, domain.TaskID, domain.RunID) (domain.AgentRun, error)
	GetTask(context.Context, domain.TaskID) (domain.Task, error)
	GetTaskAgentExecutionProfilePreference(context.Context, domain.TaskID) (domain.AgentExecutionProfilePreference, error)
	GetTrustedLocalAcknowledgement(context.Context, string, string) (domain.TrustedLocalAcknowledgement, error)
	GetTechnicalPlanDraft(context.Context, domain.TechnicalPlanDraftID) (domain.TechnicalPlanDraft, error)
	GetOpenTechnicalPlanDraft(context.Context, domain.TaskID) (domain.TechnicalPlanDraft, error)
	GetTechnicalPlanRevision(context.Context, domain.TechnicalPlanRevisionID) (domain.TechnicalPlanRevision, error)
	ListTechnicalPlanRevisions(context.Context, domain.TaskID) ([]domain.TechnicalPlanRevision, error)
	GetTechnicalPlanReview(context.Context, domain.TechnicalPlanRevisionID) (domain.TechnicalPlanReview, error)
	ListTechnicalPlanReviews(context.Context, domain.TaskID) ([]domain.TechnicalPlanReview, error)
	GetCurrentTechnicalPlanAcceptance(context.Context, domain.TaskID) (domain.TechnicalPlanAcceptanceBinding, error)
	GetTechnicalPlanRunBinding(context.Context, domain.RunID) (domain.TechnicalPlanRunBinding, error)
	GetCharter(context.Context, domain.CharterID) (domain.RunCharter, error)
	GetRun(context.Context, domain.RunID) (domain.AgentRun, error)
	GetAttempt(context.Context, domain.AttemptID) (domain.Attempt, error)
	GetCurrentAttempt(context.Context, domain.RunID) (domain.Attempt, error)
	GetRuntimeSession(context.Context, domain.RuntimeSessionID) (RuntimeSession, error)
	GetRuntimeSessionForAttempt(context.Context, domain.AttemptID) (RuntimeSession, error)
	GetReview(context.Context, domain.ReviewDecisionID) (domain.ReviewDecision, error)
	GetLatestReviewForRun(context.Context, domain.RunID) (domain.ReviewDecision, error)
	GetSnapshot(context.Context, domain.ContextSnapshotID) (contextcore.Snapshot, error)
	GetObservation(context.Context, domain.ObservationID) (Observation, error)
	GetCheck(context.Context, domain.CheckID) (Check, error)
	GetIntervention(context.Context, domain.InterventionID) (Intervention, error)
	GetHandoff(context.Context, domain.HandoffID) (Handoff, error)
	GetRuntimeStreamOffset(context.Context, domain.RuntimeSessionID, RuntimeStream) (RuntimeStreamOffset, error)
	ListRunEvents(context.Context, domain.RunID) ([]domain.RunEvent, error)
	LookupRevisions(context.Context, domain.RoomID, []domain.ContextRevisionID) ([]domain.RoomContextRevision, error)
	ListStartupCandidates(context.Context) ([]StartupCandidate, error)
	GetTerminalProjection(context.Context, domain.AttemptID) (TerminalProjection, error)
	GetCandidate(context.Context, domain.CandidateID) (domain.Candidate, error)
	ListCandidatesForRun(context.Context, domain.RunID) ([]domain.Candidate, error)
	ListCandidatesForRoom(context.Context, domain.RoomID) ([]domain.Candidate, error)
	GetCandidateDecision(context.Context, domain.CandidateID) (domain.CandidateDecision, error)
	GetRoomRevision(context.Context, domain.ContextRevisionID) (domain.RoomRevision, error)
	ListRoomRevisions(context.Context, domain.RoomID) ([]domain.RoomRevision, error)
	GetTaskRevisionSelection(context.Context, domain.TaskID) (domain.TaskRevisionSelection, error)
	GetDecisionGate(context.Context, domain.DecisionGateID) (domain.ExecutionDecisionGate, error)
	GetLatestDecisionGateForRun(context.Context, domain.RunID) (domain.ExecutionDecisionGate, error)
	ListDecisionGatesForRun(context.Context, domain.RunID) ([]domain.ExecutionDecisionGate, error)
	ListAttemptsForRun(context.Context, domain.RunID) ([]domain.Attempt, error)
	ListReviewsForRun(context.Context, domain.RunID) ([]domain.ReviewDecision, error)
	GetSpecCodingIntent(context.Context, domain.TaskID) (SpecCodingIntent, error)
	GetSpecCodingBinding(context.Context, domain.TaskID) (SpecCodingBinding, error)
	GetAgentReportForRun(context.Context, domain.RunID) (domain.AgentReport, error)
	GetAgentReportForAttempt(context.Context, domain.AttemptID) (domain.AgentReport, error)
	ListAgentReportsForRun(context.Context, domain.RunID) ([]domain.AgentReport, error)
	GetVerificationRun(context.Context, domain.VerificationRunID) (domain.VerificationRun, error)
	GetVerificationRunForRun(context.Context, domain.RunID) (domain.VerificationRun, error)
	GetVerificationRunForAgentAttempt(context.Context, domain.AttemptID) (domain.VerificationRun, error)
	ListVerificationRunsForRun(context.Context, domain.RunID) ([]domain.VerificationRun, error)
	GetVerificationAttempt(context.Context, domain.VerificationAttemptID) (VerificationAttemptRecord, error)
	GetCurrentVerificationAttempt(context.Context, domain.VerificationRunID) (VerificationAttemptRecord, error)
	VerificationCancelRequested(context.Context, domain.VerificationAttemptID) (bool, error)
	ListVerificationAttempts(context.Context, domain.VerificationRunID) ([]VerificationAttemptRecord, error)
	ListVerificationCommandEvidence(context.Context, domain.VerificationAttemptID) ([]domain.VerificationCommandEvidence, error)
	ListVerificationAcceptanceChecks(context.Context, domain.VerificationAttemptID) ([]domain.AcceptanceCheck, error)
	GetVerificationResultForRun(context.Context, domain.RunID) (domain.VerificationResult, error)
	GetVerificationResultForVerificationRun(context.Context, domain.VerificationRunID) (domain.VerificationResult, error)
	GetLocalReviewResult(context.Context, domain.ResultID) (domain.LocalReviewResult, error)
	GetLocalReviewResultForAgentAttempt(context.Context, domain.AttemptID) (domain.LocalReviewResult, error)
	ListVerificationStartupCandidates(context.Context) ([]VerificationStartupCandidate, error)
	GetVerifiedReview(context.Context, domain.ReviewDecisionID) (domain.VerifiedReviewDecision, error)
	GetVerifiedReviewForResult(context.Context, domain.ResultID) (domain.VerifiedReviewDecision, error)
	ListVerifiedReviewsForRun(context.Context, domain.RunID) ([]domain.VerifiedReviewDecision, error)
	GetVerifiedReviewRoute(context.Context, domain.ReviewDecisionID) (VerifiedReviewRoute, error)
	GetPatchApplication(context.Context, domain.RunID) (domain.PatchApplication, error)
	GetPatchInspectionFailure(context.Context, domain.RunID) (domain.PatchInspectionFailure, error)
	ListApplyingPatchApplications(context.Context) ([]domain.PatchApplication, error)
	GetTaskWorktreeBinding(context.Context, domain.TaskID) (domain.TaskWorktreeBinding, error)
	ListTaskWorktreeRecoveryCandidates(context.Context) ([]domain.TaskWorktreeBinding, error)
	GetRepositoryBinding(context.Context, domain.RoomID) (domain.RepositoryBinding, error)
	ListRepositoryBindings(context.Context, domain.RepositoryBindingState) ([]domain.RepositoryBinding, error)
	GetRepositoryBindingByLocator(context.Context, string) (domain.RepositoryBinding, error)
}

type WriteTx interface {
	ResultClosureWriter
	DeliveryWriter
	ResourceWriter
	Reader
	contextcore.SnapshotPort
	InsertRoom(context.Context, domain.Room) error
	InsertProject(context.Context, domain.Project) error
	SaveProjectSettingsCAS(context.Context, uint64, domain.ProjectSettings) error
	SaveProjectCAS(context.Context, uint64, domain.Project) error
	SaveRoomCAS(context.Context, uint64, domain.Room) error
	SaveRoomLifecycleCAS(context.Context, uint64, domain.Room, RoomLifecycleEvent) error
	InsertTask(context.Context, domain.Task) error
	InsertTaskAgentExecutionProfilePreference(context.Context, domain.AgentExecutionProfilePreference) error
	InsertTrustedLocalAcknowledgement(context.Context, domain.TrustedLocalAcknowledgement) error
	InsertTechnicalPlanDraft(context.Context, domain.TechnicalPlanDraft) error
	SaveTechnicalPlanDraftCAS(context.Context, uint64, domain.TechnicalPlanDraft) error
	SubmitTechnicalPlanDraftCAS(context.Context, uint64, domain.TechnicalPlanDraft, domain.TechnicalPlanRevision) error
	InsertTechnicalPlanReviewCAS(context.Context, domain.TechnicalPlanReview) error
	InsertTechnicalPlanAcceptance(context.Context, domain.TechnicalPlanAcceptanceBinding) error
	InsertTechnicalPlanRunBinding(context.Context, domain.TechnicalPlanRunBinding) error
	InsertCharter(context.Context, domain.RunCharter) error
	InsertRun(context.Context, domain.AgentRun) error
	InsertAttempt(context.Context, domain.Attempt) error
	AppendRunEvent(context.Context, domain.RunID, EventDraft) (domain.RunEvent, error)
	InsertRevision(context.Context, domain.RoomContextRevision) error
	InsertArtifact(context.Context, Artifact) error
	InsertObservation(context.Context, Observation) error
	InsertCheck(context.Context, Check) error
	InsertIntervention(context.Context, Intervention) error
	InsertHandoff(context.Context, Handoff) error
	InsertReview(context.Context, domain.ReviewDecision) error
	InsertRuntimeSession(context.Context, RuntimeSession) error
	AdvanceRuntimeStreamOffset(context.Context, domain.RuntimeSessionID, RuntimeStream, int64, int64, bool) error
	SaveRunCAS(context.Context, uint64, domain.AgentRun) error
	SaveAttemptCAS(context.Context, domain.AttemptState, domain.Attempt) error
	CloseTaskForAcceptedRun(context.Context, domain.TaskID, domain.RunID, uint64) error
	CloseTaskForCompletedRun(context.Context, domain.TaskID, domain.RunID, uint64) error
	ArchiveTask(context.Context, domain.TaskID, time.Time) error
	RestoreTask(context.Context, domain.TaskID) error
	LookupCommand(context.Context, CommandKey) (Response, bool, error)
	SaveCommand(context.Context, CommandKey, Response) error
	UpdateCommand(context.Context, CommandKey, Response) error
	SaveRuntimeSessionCAS(context.Context, uint64, RuntimeSession) error
	InsertCandidate(context.Context, domain.Candidate) error
	SaveCandidateEditCAS(context.Context, uint64, domain.Candidate, domain.Candidate) error
	SaveCandidateDecisionCAS(context.Context, domain.Candidate, domain.Candidate, domain.CandidateDecision) error
	InsertRoomRevision(context.Context, domain.RoomRevision) error
	InsertTaskRevisionSelection(context.Context, domain.TaskRevisionSelection) error
	InsertDecisionGate(context.Context, domain.ExecutionDecisionGate) error
	ResolveDecisionGateCAS(context.Context, domain.ExecutionDecisionGate, domain.ExecutionDecisionGate) error
	InsertSpecCodingIntent(context.Context, SpecCodingIntent) error
	InsertSpecCodingBinding(context.Context, SpecCodingBinding) error
	RegisterSpecCodingBinding(context.Context, SpecCodingBinding) error
	InsertAgentReport(context.Context, domain.AgentReport) error
	InsertVerificationRun(context.Context, domain.VerificationRun) error
	SaveVerificationRunCAS(context.Context, domain.VerificationRunState, domain.VerificationRun) error
	InsertVerificationAttempt(context.Context, domain.VerificationAttempt, time.Time) error
	SaveVerificationAttemptCAS(context.Context, domain.VerificationAttemptState, VerificationAttemptRecord) error
	InsertVerificationCancelRequest(context.Context, domain.VerificationAttemptID, time.Time) error
	InsertVerificationCommandEvidence(context.Context, domain.VerificationCommandEvidence) error
	InsertVerificationAcceptanceCheck(context.Context, domain.VerificationAttemptID, domain.AcceptanceCheck) error
	InsertVerificationResult(context.Context, domain.VerificationResult) error
	InsertLocalReviewResult(context.Context, domain.LocalReviewResult) error
	InsertVerifiedReview(context.Context, domain.VerifiedReviewDecision) error
	InsertVerifiedReviewRoute(context.Context, VerifiedReviewRoute) error
	InsertPatchApplication(context.Context, domain.PatchApplication) error
	SavePatchApplicationCAS(context.Context, uint64, domain.PatchApplication) error
	InsertPatchInspectionFailure(context.Context, domain.PatchInspectionFailure) error
	SavePatchInspectionFailureCAS(context.Context, uint64, domain.PatchInspectionFailure) error
	InsertTaskWorktreeBinding(context.Context, domain.TaskWorktreeBinding) error
	SaveTaskWorktreeBindingCAS(context.Context, uint64, domain.TaskWorktreeBinding) error
	CloseTaskForAppliedRun(context.Context, domain.TaskID, domain.RunID, uint64) error
	InsertRepositoryBinding(context.Context, domain.RepositoryBinding) error
	SaveRepositoryBindingCAS(context.Context, uint64, domain.RepositoryBinding) error
}

type VerificationAttemptRecord struct {
	Attempt           domain.VerificationAttempt
	EvidenceComplete  bool
	CleanupProven     bool
	WorkspaceIdentity string
	Reason            string
	UpdatedAt         time.Time
}

type VerificationStartupCandidate struct {
	Run             domain.AgentRun
	VerificationRun domain.VerificationRun
	Attempt         domain.VerificationAttempt
}

type CommandKey struct {
	Command, ResourceID    string
	KeyHash, RequestDigest [32]byte
	CreatedAt, ExpiresAt   time.Time
}

type EventDraft struct {
	ID                      domain.EventID
	Type, Source            string
	OccurredAt, RecordedAt  time.Time
	NormalizedJSON, RawJSON []byte
}
type Response struct {
	Status      int
	ContentType string
	Body        []byte
}
type CommitRunCommandRequest struct {
	ExpectedVersion                   uint64
	NextRun                           domain.AgentRun
	Event                             EventDraft
	IdempotencyKeyHash, RequestDigest [32]byte
	Response                          Response
	ExpiresAt                         time.Time
}
type CommandResult struct {
	Response Response
	Event    domain.RunEvent
	Replayed bool
}
type StartupCandidate struct {
	Run     domain.AgentRun
	Attempt *domain.Attempt
}
type Artifact struct {
	ID                       domain.ArtifactID
	RunID                    domain.RunID
	AttemptID                *domain.AttemptID
	Kind, Locator, MediaType string
	Description, Role        string
	Position                 int
	SourceEventID            *domain.EventID
	Digest                   *[32]byte
	CreatedAt                time.Time
}
type Observation struct {
	ID            domain.ObservationID
	RunID         domain.RunID
	AttemptID     *domain.AttemptID
	Kind, Body    string
	SourceEventID *domain.EventID
	Position      int
	Role          string
	CreatedAt     time.Time
}
type Check struct {
	ID                     domain.CheckID
	RunID                  domain.RunID
	AttemptID              *domain.AttemptID
	CriterionID            string
	Position               int
	Name, Status, Evidence string
	SourceEventID          *domain.EventID
	CreatedAt              time.Time
}
type Intervention struct {
	ID                        domain.InterventionID
	RunID                     domain.RunID
	AttemptID                 *domain.AttemptID
	Kind, Reason, RequestedBy string
	CreatedAt, ResolvedAt     time.Time
}
type Handoff struct {
	ID                                 domain.HandoffID
	RunID                              domain.RunID
	AttemptID                          *domain.AttemptID
	FromActor, ToActor, Reason, Status string
	CreatedAt, ResolvedAt              time.Time
	SourceEventID                      *domain.EventID
}

type TerminalProjection struct {
	Summary            Observation
	ContextConsumption *Observation
	Artifacts          []Artifact
	Checks             []Check
	Unknowns           []Observation
	Handoff            *Handoff
}
type RuntimeStream string

const (
	RuntimeStreamStdout RuntimeStream = "stdout"
	RuntimeStreamStderr RuntimeStream = "stderr"
)

type RuntimeStreamOffset struct {
	SessionID domain.RuntimeSessionID
	Stream    RuntimeStream
	Offset    int64
	EOF       bool
}
type RuntimeSession struct {
	ID                                                     domain.RuntimeSessionID
	AttemptID                                              domain.AttemptID
	PredecessorID                                          *domain.RuntimeSessionID
	AdapterID, RuntimeKind, ExternalReference, LaunchToken string
	Version                                                uint64
	WorkingRoot                                            string
	SecurityFingerprint                                    [32]byte
	RuntimeVersion                                         string
	RuntimeFingerprint                                     [32]byte
	ProcessIdentity, State                                 string
	StopIntent, Diagnostic                                 string
	CreatedAt, UpdatedAt                                   time.Time
	StartedAt, TerminalAt, FinalizedAt                     time.Time
}
