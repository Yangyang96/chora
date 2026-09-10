package app

import (
	"errors"
	"time"

	"github.com/Yangyang96/chora/internal/contextcore"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/speccoding"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

var ErrInvalidCommand = errors.New("invalid application command")
var ErrUnauthorizedCommand = errors.New("application command unauthorized")
var ErrRuntimePolicyViolation = errors.New("runtime policy violation")

var (
	ErrProjectNotFound          = errors.New("project not found")
	ErrProjectOverlap           = errors.New("project locator overlaps an admitted repository")
	ErrRepositoryNotGit         = errors.New("repository is not a Git worktree")
	ErrRepositoryBare           = errors.New("repository is a bare Git repository")
	ErrRepositoryLinkedWorktree = errors.New("repository is a linked Git worktree")
	ErrCloneFailed              = errors.New("repository clone failed")
	ErrRepositoryUnavailable    = errors.New("repository source is unavailable")
)

const UnconfiguredSCMReason = "No task-scoped SCM Adapter and credential are configured; Chora will not commit, push, or create a Draft MR."

type SCMAvailability struct {
	Configured bool
	Reason     string
}

type DraftMergeRequest struct {
	TaskID         domain.TaskID
	ResultID       domain.ResultID
	PatchDigest    [32]byte
	IdempotencyKey string
}

type DraftMergeRequestResult struct {
	ExternalID string
	URL        string
	Replayed   bool
}

type CommandMeta struct {
	ActorID, SessionID, IdempotencyKey string
	RequestDigest                      [32]byte
}

func (m CommandMeta) valid() bool {
	return m.ActorID != "" && m.SessionID != "" && m.IdempotencyKey != ""
}

type CommandAuthorizationRequest struct {
	ActorID, SessionID, Action, ResourceID string
	ExpectedVersion                        uint64
}

type CreateRoomRequest struct {
	CommandMeta
	Name, Description, WorkspaceRoot string
}
type CreateRoomResult struct {
	Room            domain.Room
	InitialRevision domain.RoomRevision
	Replayed        bool
}
type ChangeRoomLifecycleRequest struct {
	CommandMeta
	RoomID          domain.RoomID
	ExpectedVersion uint64
}
type ChangeRoomLifecycleResult struct {
	Room     domain.Room
	Replayed bool
}
type CreateTaskRequest struct {
	CommandMeta
	RoomID                domain.RoomID
	Title, Goal           string
	ExecutionProfile      TaskExecutionProfile
	AgentExecutionProfile domain.AgentExecutionProfile
	Criteria              []domain.AcceptanceCriterion
	RevisionIDs           []domain.ContextRevisionID
	PlanContent           domain.TechnicalPlanContent
	RealSpecCoding        *RealSpecCodingInput
}
type CreateTaskResult struct {
	Task                  domain.Task
	Draft                 domain.TechnicalPlanDraft
	Selection             domain.TaskRevisionSelection
	ExecutionProfile      TaskExecutionProfile
	AgentExecutionProfile domain.AgentExecutionProfile
	Worktree              *domain.TaskWorktreeBinding
	Replayed              bool
}

type AcknowledgeTrustedLocalRequest struct {
	CommandMeta
	PolicyVersion string
}

type AcknowledgeTrustedLocalResult struct {
	Acknowledgement domain.TrustedLocalAcknowledgement
	Replayed        bool
}

type ArchiveTaskRequest struct {
	CommandMeta
	TaskID domain.TaskID
}

type ArchiveTaskResult struct {
	Task     domain.Task
	Replayed bool
}

type TaskExecutionProfile string

const (
	TaskExecutionProfileDiagnosticFake TaskExecutionProfile = "diagnostic_fake"
	TaskExecutionProfileRealSpecCoding TaskExecutionProfile = "real_spec_coding"
)

type RealSpecCodingInput struct {
	Resources            *domain.TaskResourceSnapshot
	Requirement          string
	Constraints          []string
	OutOfScope           []string
	Criteria             []RealSpecCodingCriterion
	WritableFiles        []string
	WritableDirectories  []string
	NoChecks             bool
	VerificationCommands []RealSpecCodingVerificationCommand
}

type RealSpecCodingCriterion struct {
	Title                      string
	Description                string
	VerificationCommandIndexes []int
}

type RealSpecCodingVerificationCommand struct {
	WorkingDirectory string
	Argv             []string
}

type MaterializeSpecCodingContractRequest struct {
	CommandMeta
	Contract              speccoding.CoreContract
	WorkspaceRoot         string
	ContextEntryID        domain.ContextEntryID
	ContextRevisionID     domain.ContextRevisionID
	CharterID             domain.CharterID
	AgentAdapter          string
	AgentExecutionProfile domain.AgentExecutionProfile
	FrozenAt              time.Time
}

type MaterializeSpecCodingContractResult struct {
	Task     domain.Task
	Draft    domain.TechnicalPlanDraft
	Snapshot contextcore.Snapshot
	Binding  storecontract.SpecCodingBinding
	Replayed bool
}

type RegisterSpecCodingContractRequest struct {
	CommandMeta
	Contract speccoding.CoreContract
}

type RegisterSpecCodingContractResult struct {
	Binding  storecontract.SpecCodingBinding
	Replayed bool
}

type SaveTechnicalPlanDraftRequest struct {
	CommandMeta
	DraftID             domain.TechnicalPlanDraftID
	ExpectedEditVersion uint64
	Content             domain.TechnicalPlanContent
}
type SaveTechnicalPlanDraftResult struct {
	Draft    domain.TechnicalPlanDraft
	Replayed bool
}

type SubmitTechnicalPlanDraftRequest struct {
	CommandMeta
	DraftID             domain.TechnicalPlanDraftID
	ExpectedEditVersion uint64
	ConfirmUnchanged    bool
}
type SubmitTechnicalPlanDraftResult struct {
	Draft    domain.TechnicalPlanDraft
	Revision domain.TechnicalPlanRevision
	Replayed bool
}

type ReviewTechnicalPlanRevisionRequest struct {
	CommandMeta
	RevisionID domain.TechnicalPlanRevisionID
	Kind       domain.TechnicalPlanReviewKind
	Note       string
}
type ReviewTechnicalPlanRevisionResult struct {
	Review   domain.TechnicalPlanReview
	Draft    *domain.TechnicalPlanDraft
	Binding  *domain.TechnicalPlanAcceptanceBinding
	Charter  *domain.RunCharter
	Snapshot *contextcore.Snapshot
	Replayed bool
}

type CreateRunRequest struct {
	CommandMeta
	TaskID     domain.TaskID
	RevisionID domain.TechnicalPlanRevisionID
}
type CreateRunResult struct {
	Run      domain.AgentRun
	Binding  domain.TechnicalPlanRunBinding
	Replayed bool
}

type PrepareRunRequest struct {
	CommandMeta
	RunID           domain.RunID
	ExpectedVersion uint64
}
type PrepareRetryRequest struct {
	CommandMeta
	RunID                domain.RunID
	ExpectedVersion      uint64
	Reason, Instructions string
	Automatic            bool
}
type PrepareProfileSwitchRequest struct {
	CommandMeta
	RunID           domain.RunID
	ExpectedVersion uint64
	Profile         domain.AgentExecutionProfile
	Reason          string
}
type PrepareVerifiedAgentRetryRequest struct {
	CommandMeta
	RunID           domain.RunID
	ExpectedVersion uint64
	Instructions    string
}
type PrepareRunResult struct {
	Run      domain.AgentRun
	Attempt  domain.Attempt
	Snapshot contextcore.Snapshot
	Replayed bool
}
type StartAttemptRequest struct {
	CommandMeta
	RunID           domain.RunID
	ExpectedVersion uint64
	Mode            StartAttemptMode
}

type StartAttemptMode string

const (
	StartFresh                 StartAttemptMode = "fresh"
	StartResumeRecordedSession StartAttemptMode = "resume_recorded_session"
)

type StartAttemptResult struct {
	Run      domain.AgentRun
	Attempt  domain.Attempt
	Session  *RuntimeSessionResult
	Replayed bool
}
type RuntimeSessionResult struct {
	ID       domain.RuntimeSessionID
	Identity execution.ProcessIdentity
}
type SubmitTerminalRequest struct {
	CommandMeta
	RunID           domain.RunID
	ExpectedVersion uint64
	Result          execution.TerminalResult
	proof           terminalProof
}
type SubmitTerminalResult struct {
	Run                           domain.AgentRun
	Attempt                       domain.Attempt
	AgentReportID                 *domain.AgentReportID
	Artifacts                     []domain.ArtifactID
	Checks                        []domain.CheckID
	UnknownObservations           []domain.ObservationID
	SummaryObservation            *domain.ObservationID
	ContextConsumptionObservation *domain.ObservationID
	Handoff                       *domain.HandoffID
	Replayed                      bool
}
type VerificationAuthority struct {
	BaselineDigest        [32]byte
	VerifierPolicyVersion string
	VerifierPolicyDigest  [32]byte
}

func (authority VerificationAuthority) valid() bool {
	return authority.BaselineDigest != ([32]byte{}) && authority.VerifierPolicyDigest != ([32]byte{}) && authority.VerifierPolicyVersion != ""
}

type VerificationExecutionRequest struct {
	RunID             domain.RunID
	TaskID            domain.TaskID
	VerificationRunID domain.VerificationRunID
	AttemptID         domain.VerificationAttemptID
	Bindings          domain.VerificationBindings
	Contract          speccoding.CoreContract
	PatchLocator      string
	AgentWorkspace    string
}

type StartVerificationRequest struct {
	CommandMeta
	RunID           domain.RunID
	ExpectedVersion uint64
}
type StartVerificationResult struct {
	Run             domain.AgentRun
	VerificationRun domain.VerificationRun
	Attempt         domain.VerificationAttempt
	Replayed        bool
}
type CancelVerificationRequest struct {
	CommandMeta
	RunID           domain.RunID
	ExpectedVersion uint64
}
type CancelVerificationResult struct {
	Run             domain.AgentRun
	VerificationRun domain.VerificationRun
	Attempt         domain.VerificationAttempt
	Replayed        bool
}
type ReviewRequest struct {
	CommandMeta
	RunID              domain.RunID
	ExpectedVersion    uint64
	Kind               domain.ReviewDecisionKind
	Comment            string `json:"ReviewerNote"` // Retain persisted idempotency fingerprints.
	Checks             []domain.ReviewedCheck
	LinkedArtifacts    []domain.ArtifactLink
	CandidateProposals []CandidateProposal
}
type ReviewResult struct {
	Run        domain.AgentRun
	Decision   domain.ReviewDecision
	Candidates []domain.Candidate
	Replayed   bool
}

type VerifiedReviewRequest struct {
	CommandMeta
	RunID           domain.RunID
	ExpectedVersion uint64
	Kind            domain.ReviewDecisionKind
	Reason          string
	RejectionClass  domain.ReviewRejectionClass
	ActorID         string
	SessionID       string
}

type VerifiedReviewResult struct {
	Run           domain.AgentRun
	Decision      domain.VerifiedReviewDecision
	PlanningDraft *domain.TechnicalPlanDraft
	RelatedTask   *domain.Task
	Replayed      bool
}

type CandidateProposal struct {
	ArtifactID domain.ArtifactID
	Title      string
	Body       string
}

type EditCandidateRequest struct {
	CommandMeta
	CandidateID     domain.CandidateID
	ExpectedVersion uint64
	Title, Body     string
}

type EditCandidateResult struct {
	Candidate domain.Candidate
	Replayed  bool
}

type DecideCandidateRequest struct {
	CommandMeta
	CandidateID     domain.CandidateID
	ExpectedVersion uint64
	Kind            domain.CandidateDecisionKind
	Note            string
}

type DecideCandidateResult struct {
	Candidate domain.Candidate
	Decision  domain.CandidateDecision
	Revision  *domain.RoomRevision
	Replayed  bool
}
type RequestExecutionDecisionRequest struct {
	CommandMeta
	RunID           domain.RunID
	ExpectedVersion uint64
	Question        string
	Context         string
	Options         []domain.DecisionGateOption
	Recommendation  string
	Impact          string
}
type ResolveExecutionDecisionRequest struct {
	CommandMeta
	GateID          domain.DecisionGateID
	RunID           domain.RunID
	ExpectedVersion uint64
	OptionID        string
	Note            string
}
type ExecutionDecisionResult struct {
	Gate     domain.ExecutionDecisionGate
	Replayed bool
}
type StopRequest struct {
	CommandMeta
	RunID           domain.RunID
	ExpectedVersion uint64
	ToActor, Reason string
	AllowRetry      bool
}
type StopResult struct {
	Run       domain.AgentRun
	Attempt   domain.Attempt
	HandoffID *domain.HandoffID
	Replayed  bool
}
type ConsumeStreamRequest struct {
	CommandMeta
	SessionID domain.RuntimeSessionID
	Stream    execution.StreamKind
}
type ExitRequest struct {
	CommandMeta
	SessionID       domain.RuntimeSessionID
	ExpectedVersion uint64
}
type terminalProof struct{ sessionID domain.RuntimeSessionID }
type PresenceRequest struct {
	ActorID, SessionID string
	RunID              domain.RunID
	Kind               domain.ReviewDecisionKind
	ExpectedVersion    uint64
}
type TrustedPresenceProof struct{ HumanIssued, DeviceOwnerPresent bool }

func MintHumanReviewAuthorization(proof TrustedPresenceProof, request ...PresenceRequest) (domain.HumanReviewAuthorization, error) {
	if !proof.HumanIssued || len(request) != 1 {
		return domain.HumanReviewAuthorization{}, domain.ErrUnauthorizedReview
	}
	r := request[0]
	return domain.MintHumanReviewAuthorization(r.RunID, r.Kind, r.ExpectedVersion, proof.DeviceOwnerPresent)
}
