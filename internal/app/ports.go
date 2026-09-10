package app

import (
	"context"
	"time"

	"github.com/Yangyang96/chora/internal/contextcore"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/gitsource"
	storecontract "github.com/Yangyang96/chora/internal/store"
	"github.com/Yangyang96/chora/internal/verifier"
)

type Store interface {
	Reader() storecontract.Reader
	WithinWriteTx(context.Context, func(storecontract.WriteTx) error) error
}
type ContextPort interface {
	Assemble(context.Context, contextcore.SnapshotPort, contextcore.AssembleRequest) (contextcore.Snapshot, error)
}
type AgentRegistry interface {
	Get(string) (execution.AgentAdapter, error)
}
type PresenceAuthorizer interface {
	AuthorizeReview(context.Context, PresenceRequest) (domain.HumanReviewAuthorization, error)
}
type CommandAuthorizer interface {
	Authorize(context.Context, CommandAuthorizationRequest) error
}
type VerificationExecutor interface {
	Authority() VerificationAuthority
	Verify(context.Context, VerificationExecutionRequest) (verifier.AttemptEvidence, error)
}
type ReviewPatchSource interface {
	ReadReviewPatch(context.Context, string) ([]byte, error)
}

// ReviewPatchMaterializer freezes the exact review Patch produced in a Task's
// execution worktree. Runtime adapters report execution; they do not own Git
// diff or durable review-artifact semantics.
type ReviewPatchMaterializer interface {
	MaterializeReviewPatch(context.Context, ReviewPatchMaterializationRequest) (execution.WorkspaceArtifact, error)
}
type PatchApplicationTarget interface {
	Inspect(context.Context, PatchTargetRequest) (PatchTargetInspection, error)
	Apply(context.Context, PatchTargetRequest, PatchTargetInspection) (PatchTargetEvidence, error)
}

type ReviewPatchMaterializationRequest struct {
	RunID       domain.RunID
	TaskID      domain.TaskID
	AttemptID   domain.AttemptID
	WorkingRoot string
}

// TaskWorktreeManager plans and ensures a durable Task worktree against one
// fixed repository authority (the M1 process-global --repository path).
type TaskWorktreeManager interface {
	Plan(domain.TaskID, string, time.Time) (domain.TaskWorktreeBinding, error)
	Ensure(context.Context, domain.TaskWorktreeBinding) error
}

// TaskWorktreeResolver resolves a Task's durable worktree against the Task's
// Room-bound repository. Rooms without a RepositoryBinding fall back to the
// process-global TaskWorktreeManager (the M1 source-checkout/serve/installed
// fixed-repository path).
type TaskWorktreeResolver interface {
	Plan(context.Context, domain.Task, time.Time) (domain.TaskWorktreeBinding, error)
	Ensure(context.Context, domain.TaskWorktreeBinding) error
	// ResolveExecutionRoot returns the absolute host path where the Task's
	// Attempt executes. For a Room-bound repository it is the proven Task
	// worktree; for an unbound Room it returns "" so the caller keeps the
	// charter's WorkspaceRoot (the M1 logical installed root).
	ResolveExecutionRoot(context.Context, domain.Task) (string, error)
}
type SCMAdapter interface {
	Availability(context.Context, domain.TaskID) (SCMAvailability, error)
	CreateDraftMergeRequest(context.Context, DraftMergeRequest) (DraftMergeRequestResult, error)
}
type RepositorySource interface {
	Inspect(context.Context, string) (gitsource.Inspection, error)
	Clone(context.Context, string, string) (gitsource.Inspection, error)
}
type Clock interface{ Now() time.Time }
type IDGenerator interface {
	RoomID() domain.RoomID
	TaskID() domain.TaskID
	CharterID() domain.CharterID
	RunID() domain.RunID
	SnapshotID() domain.ContextSnapshotID
	TechnicalPlanDraftID() domain.TechnicalPlanDraftID
	TechnicalPlanRevisionID() domain.TechnicalPlanRevisionID
	TechnicalPlanReviewID() domain.TechnicalPlanReviewID
	AttemptID() domain.AttemptID
	EventID() domain.EventID
	RuntimeSessionID() domain.RuntimeSessionID
	ArtifactID() domain.ArtifactID
	ObservationID() domain.ObservationID
	CheckID() domain.CheckID
	HandoffID() domain.HandoffID
	ReviewID() domain.ReviewDecisionID
	ContextEntryID() domain.ContextEntryID
	ContextRevisionID() domain.ContextRevisionID
	CandidateID() domain.CandidateID
	CandidateDecisionID() domain.CandidateDecisionID
	DecisionGateID() domain.DecisionGateID
	AgentReportID() domain.AgentReportID
	VerificationRunID() domain.VerificationRunID
	VerificationAttemptID() domain.VerificationAttemptID
	VerificationCommandEvidenceID() domain.VerificationCommandEvidenceID
	ResultID() domain.ResultID
}

type ContextAssembler struct{}

func (ContextAssembler) Assemble(ctx context.Context, port contextcore.SnapshotPort, request contextcore.AssembleRequest) (contextcore.Snapshot, error) {
	return contextcore.NewAssembler(port).Assemble(ctx, request)
}

type RandomIDs struct{}

func (RandomIDs) RoomID() domain.RoomID                { return domain.NewRoomID() }
func (RandomIDs) TaskID() domain.TaskID                { return domain.NewTaskID() }
func (RandomIDs) CharterID() domain.CharterID          { return domain.NewCharterID() }
func (RandomIDs) RunID() domain.RunID                  { return domain.NewRunID() }
func (RandomIDs) SnapshotID() domain.ContextSnapshotID { return domain.NewContextSnapshotID() }
func (RandomIDs) TechnicalPlanDraftID() domain.TechnicalPlanDraftID {
	return domain.NewTechnicalPlanDraftID()
}
func (RandomIDs) TechnicalPlanRevisionID() domain.TechnicalPlanRevisionID {
	return domain.NewTechnicalPlanRevisionID()
}
func (RandomIDs) TechnicalPlanReviewID() domain.TechnicalPlanReviewID {
	return domain.NewTechnicalPlanReviewID()
}
func (RandomIDs) AttemptID() domain.AttemptID                 { return domain.NewAttemptID() }
func (RandomIDs) EventID() domain.EventID                     { return domain.NewEventID() }
func (RandomIDs) RuntimeSessionID() domain.RuntimeSessionID   { return domain.NewRuntimeSessionID() }
func (RandomIDs) ArtifactID() domain.ArtifactID               { return domain.NewArtifactID() }
func (RandomIDs) ObservationID() domain.ObservationID         { return domain.NewObservationID() }
func (RandomIDs) CheckID() domain.CheckID                     { return domain.NewCheckID() }
func (RandomIDs) HandoffID() domain.HandoffID                 { return domain.NewHandoffID() }
func (RandomIDs) ReviewID() domain.ReviewDecisionID           { return domain.NewReviewDecisionID() }
func (RandomIDs) ContextEntryID() domain.ContextEntryID       { return domain.NewContextEntryID() }
func (RandomIDs) ContextRevisionID() domain.ContextRevisionID { return domain.NewContextRevisionID() }
func (RandomIDs) CandidateID() domain.CandidateID             { return domain.NewCandidateID() }
func (RandomIDs) CandidateDecisionID() domain.CandidateDecisionID {
	return domain.NewCandidateDecisionID()
}
func (RandomIDs) DecisionGateID() domain.DecisionGateID { return domain.NewDecisionGateID() }
func (RandomIDs) AgentReportID() domain.AgentReportID   { return domain.NewAgentReportID() }
func (RandomIDs) VerificationRunID() domain.VerificationRunID {
	return domain.NewVerificationRunID()
}
func (RandomIDs) VerificationAttemptID() domain.VerificationAttemptID {
	return domain.NewVerificationAttemptID()
}
func (RandomIDs) VerificationCommandEvidenceID() domain.VerificationCommandEvidenceID {
	return domain.NewVerificationCommandEvidenceID()
}
func (RandomIDs) ResultID() domain.ResultID { return domain.NewResultID() }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

// TaskResourceWorkspaceResolver prepares exactly the immutable v2 resource vector.
// Its preparation must finish for every repository before a native Attempt starts.
type TaskResourceWorkspaceResolver interface {
	Ensure(context.Context, domain.TaskID) error
	ResolveExecutionRoot(context.Context, domain.TaskID) (string, error)
}
