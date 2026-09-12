package localweb

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	agentpi "github.com/Yangyang96/chora/internal/agent/pi"
	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/speccoding"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

type runView struct {
	ResultClosed            bool                        `json:"resultClosed,omitempty"`
	ResourceResult          *resourceResultView         `json:"resourceResult,omitempty"`
	ResourceApply           *app.ResourceApplyView      `json:"resourceApply,omitempty"`
	Result                  *verificationResultView     `json:"result,omitempty"`
	ID                      string                      `json:"id"`
	Status                  domain.RunState             `json:"status"`
	TerminalReason          string                      `json:"terminalReason,omitempty"`
	AutomaticRetry          *automaticRetryView         `json:"automaticRetry,omitempty"`
	Version                 uint64                      `json:"version"`
	Attempt                 int                         `json:"attempt"`
	Adapter                 string                      `json:"adapter"`
	Room                    roomView                    `json:"room"`
	Task                    taskView                    `json:"task"`
	Plan                    *runPlanView                `json:"plan,omitempty"`
	Context                 []contextView               `json:"context"`
	Timeline                []timelineView              `json:"timeline"`
	Trajectory              []trajectoryView            `json:"trajectory"`
	Artifacts               []artifactView              `json:"artifacts"`
	Unknowns                []string                    `json:"unknowns"`
	Criteria                []criterionView             `json:"criteria"`
	Review                  *reviewView                 `json:"review,omitempty"`
	Retry                   *retryView                  `json:"retry,omitempty"`
	CancelReason            string                      `json:"cancelReason,omitempty"`
	Candidates              []candidateView             `json:"candidates"`
	Revisions               []roomRevisionView          `json:"revisions"`
	Selection               *selectionView              `json:"selection,omitempty"`
	Snapshot                *snapshotView               `json:"snapshot,omitempty"`
	Agent                   agentView                   `json:"agent"`
	AgentExecution          *agentExecutionView         `json:"agentExecution,omitempty"`
	AttemptDetail           *attemptDetailView          `json:"attemptDetail,omitempty"`
	Activity                activityView                `json:"activity"`
	Blockers                []string                    `json:"blockers"`
	Gate                    *decisionGateView           `json:"decisionGate,omitempty"`
	DecisionHistory         []decisionGateView          `json:"decisionHistory"`
	AttemptHistory          []attemptHistoryView        `json:"attemptHistory"`
	ReviewHistory           []reviewView                `json:"reviewHistory"`
	ContextConsumption      *contextConsumptionView     `json:"contextConsumption,omitempty"`
	AgentReport             *agentReportView            `json:"agentReport,omitempty"`
	VerificationDisposition verificationDispositionView `json:"verificationDisposition"`
	Verification            *verificationView           `json:"verification,omitempty"`
	VerificationHistory     []verificationView          `json:"verificationHistory"`
	ReviewablePatch         *reviewablePatchView        `json:"reviewablePatch,omitempty"`
	VerifiedReview          *verifiedReviewView         `json:"verifiedReview,omitempty"`
	VerifiedReviewHistory   []verifiedReviewView        `json:"verifiedReviewHistory"`
	PatchApplication        *patchApplicationView       `json:"patchApplication,omitempty"`
	SCM                     scmView                     `json:"scm"`
	Controls                controlsView                `json:"controls"`
}

type automaticRetryView struct {
	State             app.AutomaticRetryState `json:"state"`
	RetriesUsed       int                     `json:"retriesUsed"`
	MaxRetries        int                     `json:"maxRetries"`
	LastFailureReason string                  `json:"lastFailureReason"`
}

type verificationDispositionView struct {
	State  string `json:"state"`
	Reason string `json:"reason"`
}

type runPlanView struct {
	RevisionID  string                      `json:"revisionId"`
	Content     domain.TechnicalPlanContent `json:"content"`
	ActivatedBy string                      `json:"activatedBy"`
}

type reviewablePatchLineView struct {
	Kind    string `json:"kind"`
	OldLine int    `json:"oldLine,omitempty"`
	NewLine int    `json:"newLine,omitempty"`
	Text    string `json:"text"`
}

type reviewablePatchHunkView struct {
	Header   string                    `json:"header"`
	OldStart int                       `json:"oldStart"`
	OldCount int                       `json:"oldCount"`
	NewStart int                       `json:"newStart"`
	NewCount int                       `json:"newCount"`
	Lines    []reviewablePatchLineView `json:"lines"`
}

type reviewablePatchFileView struct {
	Kind      string                    `json:"kind,omitempty"`
	Path      string                    `json:"path"`
	Additions int                       `json:"additions"`
	Deletions int                       `json:"deletions"`
	Hunks     []reviewablePatchHunkView `json:"hunks"`
	Lines     []reviewablePatchLineView `json:"lines"`
}

type reviewablePatchView struct {
	EvidenceKind          string                    `json:"evidenceKind"`
	PatchDigest           string                    `json:"patchDigest"`
	BaselineDigest        string                    `json:"baselineDigest"`
	DeclaredFilesDigest   string                    `json:"declaredFilesDigest"`
	ResultID              string                    `json:"resultId"`
	AgentAttemptID        string                    `json:"agentAttemptId"`
	AgentReportID         string                    `json:"agentReportId,omitempty"`
	VerificationAttemptID string                    `json:"verificationAttemptId"`
	ArtifactID            string                    `json:"artifactId"`
	RawDownload           string                    `json:"rawDownload"`
	Files                 []reviewablePatchFileView `json:"files"`
}

type verifiedReviewView struct {
	ID              string `json:"id"`
	ResultID        string `json:"resultId"`
	Kind            string `json:"kind"`
	Reason          string `json:"reason"`
	RejectionClass  string `json:"rejectionClass,omitempty"`
	ActorID         string `json:"actorId"`
	SessionID       string `json:"sessionId"`
	PatchDigest     string `json:"patchDigest"`
	SourceTaskID    string `json:"sourceTaskId,omitempty"`
	PlanningDraftID string `json:"planningDraftId,omitempty"`
	RelatedTaskID   string `json:"relatedTaskId,omitempty"`
	DecidedAt       string `json:"decidedAt"`
}

type scmView struct {
	Configured       bool   `json:"configured"`
	CanCreateDraftMR bool   `json:"canCreateDraftMR"`
	Reason           string `json:"reason"`
}

type patchApplicationView struct {
	State           string   `json:"state"`
	Version         uint64   `json:"version"`
	PatchDigest     string   `json:"patchDigest"`
	TargetIdentity  string   `json:"targetIdentity"`
	BaseRevision    string   `json:"baseRevision,omitempty"`
	AffectedPaths   []string `json:"affectedPaths"`
	PreStateDigest  string   `json:"preStateDigest,omitempty"`
	PostStateDigest string   `json:"postStateDigest,omitempty"`
	Reason          string   `json:"reason,omitempty"`
	StartedAt       string   `json:"startedAt"`
	UpdatedAt       string   `json:"updatedAt"`
	AppliedAt       string   `json:"appliedAt,omitempty"`
}

type agentClaimView struct {
	CriterionID string `json:"criterionId"`
	Status      string `json:"status"`
	Evidence    string `json:"evidence"`
}

type agentReportView struct {
	ID            string           `json:"id"`
	AttemptID     string           `json:"attemptId"`
	Summary       string           `json:"summary"`
	FinalText     string           `json:"finalText"`
	ClaimedChecks []agentClaimView `json:"claimedChecks"`
	CompletedAt   string           `json:"completedAt"`
	Authority     string           `json:"authority"`
}

type verificationBindingsView struct {
	BaselineDigest           string `json:"baselineDigest"`
	PatchDigest              string `json:"patchDigest"`
	ContextSnapshotDigest    string `json:"contextSnapshotDigest"`
	AcceptanceContractDigest string `json:"acceptanceContractDigest"`
	VerifierPolicyVersion    string `json:"verifierPolicyVersion"`
	VerifierPolicyDigest     string `json:"verifierPolicyDigest"`
}

type verificationLogView struct {
	FullSHA256             string `json:"fullSha256"`
	TotalBytes             int64  `json:"totalBytes"`
	RetainedBody           string `json:"retainedBody"`
	Truncated              bool   `json:"truncated"`
	TruncationBoundary     int64  `json:"truncationBoundary"`
	RedactionPolicyVersion string `json:"redactionPolicyVersion"`
}

type verificationCommandView struct {
	ID                string              `json:"id"`
	CommandID         string              `json:"commandId"`
	CriterionIDs      []string            `json:"criterionIds"`
	Argv              []string            `json:"argv"`
	Classification    string              `json:"classification"`
	ExitCode          *int                `json:"exitCode,omitempty"`
	Stdout            verificationLogView `json:"stdout"`
	Stderr            verificationLogView `json:"stderr"`
	VerifierIdentity  string              `json:"verifierIdentity"`
	WorkspaceIdentity string              `json:"workspaceIdentity"`
	StartedAt         string              `json:"startedAt"`
	EndedAt           string              `json:"endedAt"`
}

type verificationCheckView struct {
	ID          string   `json:"id"`
	CriterionID string   `json:"criterionId"`
	Status      string   `json:"status"`
	Trust       string   `json:"trust"`
	EvidenceIDs []string `json:"evidenceIds"`
}

type verificationAttemptView struct {
	ID                string                    `json:"id"`
	Sequence          int                       `json:"sequence"`
	PredecessorID     string                    `json:"predecessorId,omitempty"`
	State             string                    `json:"state"`
	EvidenceComplete  bool                      `json:"evidenceComplete"`
	CleanupProven     bool                      `json:"cleanupProven"`
	WorkspaceIdentity string                    `json:"workspaceIdentity,omitempty"`
	Reason            string                    `json:"reason,omitempty"`
	Commands          []verificationCommandView `json:"commands"`
	Checks            []verificationCheckView   `json:"checks"`
	CreatedAt         string                    `json:"createdAt"`
	UpdatedAt         string                    `json:"updatedAt"`
}

type verificationResultView struct {
	ID        string `json:"id"`
	AttemptID string `json:"attemptId"`
	Outcome   string `json:"outcome"`
	CreatedAt string `json:"createdAt"`
}

type verificationView struct {
	ID             string                    `json:"id"`
	AgentAttemptID string                    `json:"agentAttemptId"`
	State          string                    `json:"state"`
	Bindings       verificationBindingsView  `json:"bindings"`
	Attempts       []verificationAttemptView `json:"attempts"`
	Result         *verificationResultView   `json:"result,omitempty"`
}

type roomView struct {
	ProjectID     string `json:"projectId"`
	OwnershipKind string `json:"ownershipKind"`
	ID            string `json:"id"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	WorkspaceRoot string `json:"workspaceRoot,omitempty"`
	State         string `json:"state"`
	Version       uint64 `json:"version"`
	ArchivedAt    string `json:"archivedAt"`
}

type roomDetailView struct {
	roomView
	Revisions []roomRevisionView `json:"revisions"`
}

type taskView struct {
	ID                    string            `json:"id"`
	Title                 string            `json:"title"`
	Goal                  string            `json:"goal"`
	AgentExecutionProfile string            `json:"agentExecutionProfile,omitempty"`
	Worktree              *taskWorktreeView `json:"worktree,omitempty"`
}

type roomTaskCountsView struct {
	Total    int `json:"total"`
	Open     int `json:"open"`
	Terminal int `json:"terminal"`
}

type roomSummaryView struct {
	ProjectID           string             `json:"projectId"`
	OwnershipKind       string             `json:"ownershipKind"`
	ID                  string             `json:"id"`
	Name                string             `json:"name"`
	Description         string             `json:"description"`
	WorkspaceRoot       string             `json:"workspaceRoot,omitempty"`
	State               string             `json:"state"`
	Version             uint64             `json:"version"`
	ArchivedAt          string             `json:"archivedAt"`
	LastActivityAt      string             `json:"lastActivityAt"`
	TaskCounts          roomTaskCountsView `json:"taskCounts"`
	HumanActionRequired bool               `json:"humanActionRequired"`
}

type roomDirectoryView struct {
	ActiveRooms   []roomSummaryView `json:"activeRooms"`
	ArchivedRooms []roomSummaryView `json:"archivedRooms"`
}

type planRevisionSummaryView struct {
	ID             string `json:"id"`
	RevisionNumber uint64 `json:"revisionNumber"`
}

type runSummaryView struct {
	PatchApplicationState string              `json:"patchApplicationState,omitempty"`
	ID                    string              `json:"id"`
	TaskID                string              `json:"taskId"`
	Status                domain.RunState     `json:"status"`
	Version               uint64              `json:"version"`
	Attempt               int                 `json:"attempt"`
	CreatedAt             string              `json:"createdAt"`
	UpdatedAt             string              `json:"updatedAt"`
	StartedAt             string              `json:"startedAt"`
	ReviewRequestedAt     string              `json:"reviewRequestedAt"`
	TerminalAt            string              `json:"terminalAt"`
	TerminalReason        string              `json:"terminalReason,omitempty"`
	AutomaticRetry        *automaticRetryView `json:"automaticRetry,omitempty"`
	AgentExecution        *agentExecutionView `json:"agentExecution,omitempty"`
}

type runHistorySummaryView struct {
	runSummaryView
	EventCount int `json:"eventCount"`
}

type currentActionTargetView struct {
	RoomID          string `json:"roomId,omitempty"`
	TaskID          string `json:"taskId,omitempty"`
	RunID           string `json:"runId,omitempty"`
	DraftID         string `json:"draftId,omitempty"`
	RevisionID      string `json:"revisionId,omitempty"`
	PlanReviewID    string `json:"planReviewId,omitempty"`
	ResultID        string `json:"resultId,omitempty"`
	ResultReviewID  string `json:"resultReviewId,omitempty"`
	ExpectedVersion uint64 `json:"expectedVersion,omitempty"`
}

type currentActionView struct {
	Kind   app.CurrentActionKind   `json:"kind"`
	Target currentActionTargetView `json:"target"`
	URL    string                  `json:"url"`
	Reason string                  `json:"reason"`
}

type taskSummaryView struct {
	ResultClosed          bool                     `json:"resultClosed,omitempty"`
	Delivery              *app.TaskDeliverySummary `json:"delivery,omitempty"`
	ID                    string                   `json:"id"`
	Title                 string                   `json:"title"`
	Status                domain.TaskState         `json:"status"`
	Archived              bool                     `json:"archived"`
	LastActivityAt        string                   `json:"lastActivityAt"`
	CurrentPlanRevision   *planRevisionSummaryView `json:"currentPlanRevision"`
	LatestRun             *runSummaryView          `json:"latestRun"`
	RunCount              int                      `json:"runCount"`
	CurrentAction         currentActionView        `json:"currentAction"`
	AgentExecutionProfile string                   `json:"agentExecutionProfile,omitempty"`
}

type roomWorkspaceView struct {
	Room  roomSummaryView   `json:"room"`
	Tasks []taskSummaryView `json:"tasks"`
}

type taskRunHistoryView struct {
	RoomID string                  `json:"roomId"`
	TaskID string                  `json:"taskId"`
	Runs   []runHistorySummaryView `json:"runs"`
}

type contextView struct {
	Kind  string `json:"kind"`
	Title string `json:"title"`
	Body  string `json:"body"`
}

type timelineView struct {
	Sequence int64  `json:"sequence"`
	Type     string `json:"type"`
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	Time     string `json:"time"`
}

type artifactView struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Locator     string `json:"locator"`
	Digest      string `json:"digest,omitempty"`
	MediaType   string `json:"mediaType,omitempty"`
	Description string `json:"description"`
}

func artifactDigest(artifact storecontract.Artifact) string {
	if artifact.Digest == nil {
		return ""
	}
	return fmt.Sprintf("%x", *artifact.Digest)
}

type criterionView struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Evidence string `json:"evidence"`
}

type reviewView struct {
	Kind      string `json:"kind"`
	Comment   string `json:"comment"`
	DecidedAt string `json:"decidedAt"`
}

type retryView struct {
	RejectionNote     string   `json:"rejectionNote"`
	UnmetCriterionIDs []string `json:"unmetCriterionIds"`
	Instructions      string   `json:"instructions"`
}

type roomRevisionView struct {
	Locator        string                    `json:"locator"`
	RevisionNumber int                       `json:"revisionNumber"`
	ID             string                    `json:"id"`
	Title          string                    `json:"title"`
	Body           string                    `json:"body"`
	Digest         string                    `json:"digest"`
	Provenance     domain.RevisionProvenance `json:"provenance"`
	ConfirmedAt    string                    `json:"confirmedAt"`
}

type candidateDecisionView struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Note      string `json:"note"`
	ActorID   string `json:"actorId"`
	SessionID string `json:"sessionId"`
	DecidedAt string `json:"decidedAt"`
}

type candidateView struct {
	ID               string                 `json:"id"`
	Title            string                 `json:"title"`
	Body             string                 `json:"body"`
	State            domain.CandidateState  `json:"state"`
	Version          uint64                 `json:"version"`
	SourceRunID      string                 `json:"sourceRunId"`
	SourceArtifactID string                 `json:"sourceArtifactId"`
	Decision         *candidateDecisionView `json:"decision,omitempty"`
	Revision         *roomRevisionView      `json:"revision,omitempty"`
}

type selectionItemView struct {
	RevisionID string                    `json:"revisionId"`
	Digest     string                    `json:"digest"`
	Provenance domain.RevisionProvenance `json:"provenance"`
	Reason     string                    `json:"reason,omitempty"`
}

type selectionView struct {
	TaskID    string              `json:"taskId"`
	Selected  []selectionItemView `json:"selected"`
	Excluded  []selectionItemView `json:"excluded"`
	CreatedAt string              `json:"createdAt"`
}

type snapshotView struct {
	ID       string              `json:"id"`
	Digest   string              `json:"digest"`
	Selected []selectionItemView `json:"selected"`
	Excluded []selectionItemView `json:"excluded"`
}

type agentProfileView struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Role         string   `json:"role"`
	Capabilities []string `json:"capabilities"`
}

type agentView struct {
	Profile  agentProfileView     `json:"profile"`
	Presence domain.AgentPresence `json:"presence"`
	RunState domain.RunState      `json:"runState"`
}

type runtimeIdentityView struct {
	SessionID       string `json:"sessionId"`
	AdapterID       string `json:"adapterId"`
	Kind            string `json:"kind"`
	Version         string `json:"version"`
	Fingerprint     string `json:"fingerprint"`
	ExternalSession string `json:"externalSession,omitempty"`
	State           string `json:"state"`
}

type sandboxIdentityView struct {
	Status            string `json:"status"`
	Provider          string `json:"provider"`
	Mode              string `json:"mode"`
	Image             string `json:"image"`
	PolicyFingerprint string `json:"policyFingerprint"`
}

type agentExecutionView struct {
	ReportedCost          *float64                     `json:"reportedCost,omitempty"`
	AttemptTimeoutSeconds int64                        `json:"attemptTimeoutSeconds,omitempty"`
	CostStatus            string                       `json:"costStatus,omitempty"`
	Profile               domain.AgentExecutionProfile `json:"profile"`
	RuntimeSource         string                       `json:"runtimeSource"`
	ExecutionProvider     string                       `json:"executionProvider"`
	CapabilityPolicy      string                       `json:"capabilityPolicy"`
	TrustDisclosurePolicy string                       `json:"trustDisclosurePolicy"`
	Sandboxed             bool                         `json:"sandboxed"`
	DisclosureLabel       string                       `json:"disclosureLabel"`
}

func agentExecutionViewOf(binding domain.AgentExecutionProfileBinding) *agentExecutionView {
	if !binding.Bound() {
		return nil
	}
	view := &agentExecutionView{
		Profile: binding.Profile(), RuntimeSource: binding.RuntimeSource(), ExecutionProvider: binding.ExecutionProvider(),
		CapabilityPolicy: binding.CapabilityPolicy(), TrustDisclosurePolicy: binding.TrustDisclosurePolicy(), Sandboxed: true,
	}
	if binding.RuntimeSource() == "local_pi" {
		view.AttemptTimeoutSeconds = int64(productionAttemptTimeout.Seconds())
		view.CostStatus = "unknown"
	}
	switch binding.Profile() {
	case domain.AgentExecutionProfileIsolatedLocal:
		view.DisclosureLabel = "Isolated Local · Sandboxed"
		view.AttemptTimeoutSeconds = 1200
		view.CostStatus = "unknown"
	case domain.AgentExecutionProfileMinimal:
		view.DisclosureLabel = "Minimal · Sandboxed"
	case domain.AgentExecutionProfileStandard:
		view.DisclosureLabel = "Standard · Sandboxed"
	case domain.AgentExecutionProfileTrustedLocal:
		view.Sandboxed = false
		view.DisclosureLabel = "Trusted Local · No Sandbox"
	}
	return view
}

func sandboxIdentityViewOf(binding domain.AgentExecutionProfileBinding, status piRuntimeStatus) sandboxIdentityView {
	if binding.ExecutionProvider() == domain.TrustedHostExecutionProvider {
		return sandboxIdentityView{Status: "not_applicable", Provider: domain.TrustedHostExecutionProvider, Mode: "No Sandbox", Image: "not_applicable", PolicyFingerprint: "not_applicable"}
	}
	return sandboxIdentityView{Status: "adopted", Provider: status.Provider, Mode: "attempt-private-codex-only", Image: status.Image, PolicyFingerprint: status.PolicyFingerprint}
}

type attemptDetailView struct {
	ID                 string               `json:"id"`
	Sequence           int                  `json:"sequence"`
	State              domain.AttemptState  `json:"state"`
	ExecutionWorkspace string               `json:"executionWorkspace"`
	Runtime            *runtimeIdentityView `json:"runtime,omitempty"`
	Sandbox            sandboxIdentityView  `json:"sandbox"`
	AgentExecution     *agentExecutionView  `json:"agentExecution,omitempty"`
	ModelProvenance    modelProvenanceView  `json:"modelProvenance"`
}

type activityView struct {
	Phase         domain.AgentPhase `json:"phase"`
	CurrentAction string            `json:"currentAction"`
	ElapsedMS     int64             `json:"elapsedMs"`
	LatestEvent   *timelineView     `json:"latestEvent,omitempty"`
}

type decisionOptionView struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Impact string `json:"impact"`
}

type decisionGateView struct {
	ID               string                    `json:"id"`
	Kind             string                    `json:"kind"`
	Status           domain.DecisionGateStatus `json:"status"`
	Question         string                    `json:"question"`
	Context          string                    `json:"context"`
	Options          []decisionOptionView      `json:"options"`
	Recommendation   string                    `json:"recommendation"`
	Impact           string                    `json:"impact"`
	SelectedOptionID string                    `json:"selectedOptionId,omitempty"`
	Note             string                    `json:"note,omitempty"`
	ActorID          string                    `json:"actorId,omitempty"`
	SessionID        string                    `json:"sessionId,omitempty"`
	RequestedAt      string                    `json:"requestedAt"`
	ResolvedAt       string                    `json:"resolvedAt,omitempty"`
}

type contextConsumptionView struct {
	SnapshotID              string   `json:"snapshotId"`
	SnapshotDigest          string   `json:"snapshotDigest"`
	CandidateRevisionID     string   `json:"candidateRevisionId"`
	IncludedRevisionIDs     []string `json:"includedRevisionIds"`
	ExcludedRevisionIDs     []string `json:"excludedRevisionIds"`
	Derivation              string   `json:"derivation"`
	ExcludedContentObserved bool     `json:"excludedContentObserved"`
}

type attemptHistoryView struct {
	ID              string              `json:"id"`
	Sequence        int                 `json:"sequence"`
	PredecessorID   string              `json:"predecessorId,omitempty"`
	State           domain.AttemptState `json:"state"`
	SnapshotID      string              `json:"snapshotId"`
	SnapshotDigest  string              `json:"snapshotDigest"`
	Summary         string              `json:"summary,omitempty"`
	Artifacts       []artifactView      `json:"artifacts"`
	Unknowns        []string            `json:"unknowns"`
	AgentExecution  *agentExecutionView `json:"agentExecution,omitempty"`
	ModelProvenance modelProvenanceView `json:"modelProvenance"`
}

type controlsView struct {
	CanCloseResult                 bool `json:"canCloseResult"`
	CanCancel                      bool `json:"canCancel"`
	CanRetry                       bool `json:"canRetry"`
	CanReview                      bool `json:"canReview"`
	CanAcceptAndApply              bool `json:"canAcceptAndApply"`
	CanStartVerification           bool `json:"canStartVerification"`
	CanCancelVerification          bool `json:"canCancelVerification"`
	CanRetryVerification           bool `json:"canRetryVerification"`
	CanApplyPatch                  bool `json:"canApplyPatch"`
	CanCreateDraftMR               bool `json:"canCreateDraftMR"`
	CanSwitchAgentExecutionProfile bool `json:"canSwitchAgentExecutionProfile"`
}

func recoveryRetryReady(runState domain.RunState, attemptState domain.AttemptState, session storecontract.RuntimeSession) bool {
	if runState != domain.RunStateRecoveryRequired || session.TerminalAt.IsZero() || session.FinalizedAt.IsZero() {
		return false
	}
	if attemptState == domain.AttemptStateInterrupted {
		return session.State == "stopped"
	}
	if attemptState != domain.AttemptStateFailed {
		return false
	}
	if session.State == "failed" {
		return session.ProcessIdentity == "" && session.StartedAt.IsZero()
	}
	return session.State == "stopped" && session.ProcessIdentity != "" && !session.StartedAt.IsZero()
}

func cancelledRetryReady(runState domain.RunState, attemptState domain.AttemptState, session storecontract.RuntimeSession, authorized bool) bool {
	if !authorized || runState != domain.RunStateCancelled || session.TerminalAt.IsZero() || session.FinalizedAt.IsZero() {
		return false
	}
	if attemptState == domain.AttemptStateCancelled {
		return session.State == "stopped" && session.StopIntent == string(execution.StopForCancel)
	}
	return automaticRetryFinalizedView(attemptState, session)
}

func automaticRetryFinalizedView(attemptState domain.AttemptState, session storecontract.RuntimeSession) bool {
	if session.TerminalAt.IsZero() || session.FinalizedAt.IsZero() {
		return false
	}
	if attemptState == domain.AttemptStateInterrupted {
		return session.State == "stopped"
	}
	if attemptState != domain.AttemptStateFailed {
		return false
	}
	if session.State == "failed" {
		return session.ProcessIdentity == "" && session.StartedAt.IsZero()
	}
	return session.State == "stopped" && session.ProcessIdentity != "" && !session.StartedAt.IsZero()
}

type taskRefView struct {
	ResourceSnapshot      *domain.TaskResourceSnapshot `json:"resourceSnapshot,omitempty"`
	ID                    string                       `json:"id"`
	RoomID                string                       `json:"roomId"`
	Title                 string                       `json:"title"`
	Goal                  string                       `json:"goal"`
	ExecutionProfile      string                       `json:"executionProfile"`
	AgentExecutionProfile string                       `json:"agentExecutionProfile,omitempty"`
	Repository            *repositoryIdentityView      `json:"repository,omitempty"`
	Worktree              *taskWorktreeView            `json:"worktree,omitempty"`
	Criteria              []taskCriterionView          `json:"criteria"`
	Selection             *selectionView               `json:"selection,omitempty"`
	Planning              technicalPlanningView        `json:"planning"`
	FrozenSnapshot        *frozenSnapshotRefView       `json:"frozenSnapshot,omitempty"`
}

type taskWorktreeView struct {
	BaseRevision string `json:"baseRevision"`
	BaseTree     string `json:"baseTree,omitempty"`
	BaseRef      string `json:"baseRef,omitempty"`
	StartPolicy  string `json:"startPolicy"`
	Locator      string `json:"locator"`
	State        string `json:"state"`
	Reason       string `json:"reason,omitempty"`
}

func taskWorktreeViewOf(binding domain.TaskWorktreeBinding) taskWorktreeView {
	return taskWorktreeView{BaseRevision: binding.PinnedBaseRevision(), BaseTree: binding.PinnedBaseTree(), BaseRef: binding.BaseRef(), StartPolicy: binding.StartPolicy(), Locator: binding.RelativeLocator(), State: string(binding.State()), Reason: binding.Reason()}
}

type repositoryIdentityView struct {
	Name           string `json:"name"`
	SourceRevision string `json:"sourceRevision"`
	BaselineDigest string `json:"baselineDigest"`
}

type frozenSnapshotRefView struct {
	ID     string `json:"id"`
	Digest string `json:"digest"`
	Status string `json:"status"`
}

type taskCriterionView struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

type technicalPlanDraftView struct {
	ID                    string                      `json:"id"`
	TaskID                string                      `json:"taskId"`
	EditVersion           uint64                      `json:"editVersion"`
	PredecessorRevisionID string                      `json:"predecessorRevisionId,omitempty"`
	NextRevisionNumber    uint64                      `json:"nextRevisionNumber"`
	SelectionDigest       string                      `json:"selectionDigest"`
	Content               domain.TechnicalPlanContent `json:"content"`
	CreatedAt             string                      `json:"createdAt"`
	UpdatedAt             string                      `json:"updatedAt"`
}

type technicalPlanReviewView struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Reviewer  string `json:"reviewer"`
	Note      string `json:"note"`
	DecidedAt string `json:"decidedAt"`
}

type technicalPlanRevisionView struct {
	ID                    string                      `json:"id"`
	TaskID                string                      `json:"taskId"`
	SourceDraftID         string                      `json:"sourceDraftId"`
	RevisionNumber        uint64                      `json:"revisionNumber"`
	PredecessorRevisionID string                      `json:"predecessorRevisionId,omitempty"`
	Content               domain.TechnicalPlanContent `json:"content"`
	ContentDigest         string                      `json:"contentDigest"`
	SelectionDigest       string                      `json:"selectionDigest"`
	Unchanged             bool                        `json:"unchanged"`
	SubmittedAt           string                      `json:"submittedAt"`
	Review                *technicalPlanReviewView    `json:"review,omitempty"`
	Current               bool                        `json:"current"`
}

type technicalPlanAcceptanceView struct {
	RevisionID     string `json:"revisionId"`
	ReviewID       string `json:"reviewId"`
	SnapshotID     string `json:"snapshotId"`
	SnapshotDigest string `json:"snapshotDigest"`
	CharterID      string `json:"charterId"`
	AdapterID      string `json:"adapterId"`
	BoundAt        string `json:"boundAt"`
}

type technicalPlanningView struct {
	Draft      *technicalPlanDraftView      `json:"draft,omitempty"`
	Revisions  []technicalPlanRevisionView  `json:"revisions"`
	Acceptance *technicalPlanAcceptanceView `json:"acceptance,omitempty"`
}

type saveTechnicalPlanDraftCommandView struct {
	Draft technicalPlanDraftView `json:"draft"`
}

type submitTechnicalPlanDraftCommandView struct {
	Draft    technicalPlanDraftView    `json:"draft"`
	Revision technicalPlanRevisionView `json:"revision"`
}

type reviewTechnicalPlanRevisionCommandView struct {
	RevisionID string                       `json:"revisionId"`
	Review     technicalPlanReviewView      `json:"review"`
	Draft      *technicalPlanDraftView      `json:"draft,omitempty"`
	Acceptance *technicalPlanAcceptanceView `json:"acceptance,omitempty"`
}

func roomRevisionViewOf(revision domain.RoomRevision) roomRevisionView {
	contextRevision := revision.Revision()
	return roomRevisionView{Locator: contextRevision.Locator(), RevisionNumber: contextRevision.RevisionNumber(), ID: revision.ID().String(), Title: contextRevision.Title(), Body: contextRevision.Body(), Digest: fmt.Sprintf("%x", revision.Digest()), Provenance: revision.Provenance(), ConfirmedAt: revision.ConfirmedAt().Format(time.RFC3339Nano)}
}

func selectionViewOf(selection domain.TaskRevisionSelection) selectionView {
	view := selectionView{TaskID: selection.TaskID().String(), Selected: []selectionItemView{}, Excluded: []selectionItemView{}, CreatedAt: selection.CreatedAt().Format(time.RFC3339Nano)}
	for _, item := range selection.Selected() {
		view.Selected = append(view.Selected, selectionItemView{RevisionID: item.RevisionID.String(), Digest: fmt.Sprintf("%x", item.Digest), Provenance: item.Provenance})
	}
	for _, item := range selection.Excluded() {
		view.Excluded = append(view.Excluded, selectionItemView{RevisionID: item.RevisionID.String(), Digest: fmt.Sprintf("%x", item.Digest), Provenance: item.Provenance, Reason: item.Reason})
	}
	return view
}

func taskReferenceView(task domain.Task, selection domain.TaskRevisionSelection, planning technicalPlanningView) taskRefView {
	criteria := make([]taskCriterionView, 0, len(task.Criteria()))
	for _, criterion := range task.Criteria() {
		criteria = append(criteria, taskCriterionView{ID: criterion.ID().String(), Title: criterion.Title(), Description: criterion.Description()})
	}
	selectionView := selectionViewOf(selection)
	return taskRefView{
		ID: task.ID().String(), RoomID: task.RoomID().String(), Title: task.Title(), Goal: task.Goal(), Criteria: criteria,
		Selection: &selectionView, Planning: planning,
	}
}

func relatedTaskReferenceView(task domain.Task, planning technicalPlanningView) taskRefView {
	criteria := make([]taskCriterionView, 0, len(task.Criteria()))
	for _, criterion := range task.Criteria() {
		criteria = append(criteria, taskCriterionView{ID: criterion.ID().String(), Title: criterion.Title(), Description: criterion.Description()})
	}
	return taskRefView{
		ID: task.ID().String(), RoomID: task.RoomID().String(), Title: task.Title(), Goal: task.Goal(), Criteria: criteria,
		Planning: planning,
	}
}

func technicalPlanDraftViewOf(draft domain.TechnicalPlanDraft) technicalPlanDraftView {
	return technicalPlanDraftView{
		ID: draft.ID().String(), TaskID: draft.TaskID().String(), EditVersion: draft.EditVersion(), PredecessorRevisionID: draft.PredecessorRevisionID().String(),
		NextRevisionNumber: draft.NextRevisionNumber(), SelectionDigest: fmt.Sprintf("%x", draft.SelectionDigest()), Content: draft.Content(),
		CreatedAt: draft.CreatedAt().Format(time.RFC3339Nano), UpdatedAt: draft.UpdatedAt().Format(time.RFC3339Nano),
	}
}

func technicalPlanReviewViewOf(review domain.TechnicalPlanReview) technicalPlanReviewView {
	return technicalPlanReviewView{ID: review.ID().String(), Kind: string(review.Kind()), Reviewer: review.Reviewer(), Note: review.Note(), DecidedAt: review.DecidedAt().Format(time.RFC3339Nano)}
}

func technicalPlanRevisionViewOf(revision domain.TechnicalPlanRevision) technicalPlanRevisionView {
	return technicalPlanRevisionView{
		ID: revision.ID().String(), TaskID: revision.TaskID().String(), SourceDraftID: revision.SourceDraftID().String(), RevisionNumber: revision.RevisionNumber(),
		PredecessorRevisionID: revision.PredecessorRevisionID().String(), Content: revision.Content(), ContentDigest: fmt.Sprintf("%x", revision.ContentDigest()),
		SelectionDigest: fmt.Sprintf("%x", revision.SelectionDigest()), Unchanged: revision.Unchanged(), SubmittedAt: revision.SubmittedAt().Format(time.RFC3339Nano),
	}
}

func roomViewOf(room domain.Room) roomView {
	return roomView{
		ID: room.ID().String(), ProjectID: room.ProjectID().String(), OwnershipKind: string(room.OwnershipKind()), Name: room.Name(), Description: room.Description(), WorkspaceRoot: room.WorkspaceRoot(),
		State: string(room.State()), Version: room.Version(), ArchivedAt: formatOptionalTime(room.ArchivedAt()),
	}
}

func roomSummaryViewOf(summary app.RoomSummary) roomSummaryView {
	room := summary.Room
	return roomSummaryView{
		ID: room.ID().String(), ProjectID: room.ProjectID().String(), OwnershipKind: string(room.OwnershipKind()), Name: room.Name(), Description: room.Description(), WorkspaceRoot: room.WorkspaceRoot(),
		State: string(room.State()), Version: room.Version(), ArchivedAt: formatOptionalTime(room.ArchivedAt()),
		LastActivityAt:      summary.LastActivityAt.Format(time.RFC3339Nano),
		TaskCounts:          roomTaskCountsView{Total: summary.TaskCounts.Total, Open: summary.TaskCounts.Open, Terminal: summary.TaskCounts.Terminal},
		HumanActionRequired: summary.HumanActionRequired,
	}
}

func roomDirectoryViewOf(directory app.RoomDirectory) roomDirectoryView {
	view := roomDirectoryView{
		ActiveRooms:   make([]roomSummaryView, 0, len(directory.ActiveRooms)),
		ArchivedRooms: make([]roomSummaryView, 0, len(directory.ArchivedRooms)),
	}
	for _, summary := range directory.ActiveRooms {
		view.ActiveRooms = append(view.ActiveRooms, roomSummaryViewOf(summary))
	}
	for _, summary := range directory.ArchivedRooms {
		view.ArchivedRooms = append(view.ArchivedRooms, roomSummaryViewOf(summary))
	}
	return view
}

func (view *roomDirectoryView) hideWorkspaceRoots() {
	for index := range view.ActiveRooms {
		view.ActiveRooms[index].WorkspaceRoot = ""
	}
	for index := range view.ArchivedRooms {
		view.ArchivedRooms[index].WorkspaceRoot = ""
	}
}

func roomWorkspaceViewOf(workspace app.RoomWorkspace) roomWorkspaceView {
	view := roomWorkspaceView{Room: roomSummaryViewOf(workspace.Room), Tasks: make([]taskSummaryView, 0, len(workspace.Tasks))}
	for _, summary := range workspace.Tasks {
		item := taskSummaryView{
			ID: summary.Task.ID().String(), Title: summary.Task.Title(), Status: summary.Task.State(), Archived: summary.Task.Archived(),
			LastActivityAt: summary.LastActivityAt.Format(time.RFC3339Nano), RunCount: summary.RunCount,
			CurrentAction: currentActionViewOf(summary.CurrentAction),
		}
		if summary.CurrentPlanRevisionID.Valid() {
			item.CurrentPlanRevision = &planRevisionSummaryView{ID: summary.CurrentPlanRevisionID.String(), RevisionNumber: summary.CurrentPlanRevisionNumber}
		}
		if summary.LatestRun != nil {
			run := runSummaryViewOf(*summary.LatestRun)
			item.LatestRun = &run
		}
		view.Tasks = append(view.Tasks, item)
	}
	return view
}

func (view *roomWorkspaceView) hideWorkspaceRoot() {
	view.Room.WorkspaceRoot = ""
}

func currentActionViewOf(action app.CurrentAction) currentActionView {
	return currentActionView{
		Kind: action.Kind, URL: action.URL, Reason: action.Reason,
		Target: currentActionTargetView{
			RoomID: action.Target.RoomID.String(), TaskID: action.Target.TaskID.String(), RunID: action.Target.RunID.String(),
			DraftID: action.Target.DraftID.String(), RevisionID: action.Target.RevisionID.String(), PlanReviewID: action.Target.PlanReviewID.String(),
			ResultID: action.Target.ResultID.String(), ResultReviewID: action.Target.ResultReviewID.String(), ExpectedVersion: action.Target.ExpectedVersion,
		},
	}
}

func taskRunHistoryViewOf(history app.TaskRunHistory) taskRunHistoryView {
	view := taskRunHistoryView{RoomID: history.RoomID.String(), TaskID: history.TaskID.String(), Runs: make([]runHistorySummaryView, 0, len(history.Runs))}
	for _, summary := range history.Runs {
		view.Runs = append(view.Runs, runHistorySummaryView{runSummaryView: runSummaryViewOf(summary.Run), EventCount: summary.EventCount})
	}
	return view
}

func (server *Server) taskRunHistoryView(ctx context.Context, history app.TaskRunHistory) (taskRunHistoryView, error) {
	view := taskRunHistoryViewOf(history)
	for index := range history.Runs {
		executionView, err := server.agentExecutionForRun(ctx, history.Runs[index].Run)
		if err != nil {
			return taskRunHistoryView{}, err
		}
		view.Runs[index].AgentExecution = executionView
		events, err := server.store.Reader().ListRunEvents(ctx, history.Runs[index].Run.ID())
		if err != nil {
			return taskRunHistoryView{}, err
		}
		view.Runs[index].TerminalReason = safeTerminalReason(events)
		automatic, err := server.service.AutomaticRetryForRun(ctx, history.Runs[index].Run.ID())
		if err != nil {
			return taskRunHistoryView{}, err
		}
		view.Runs[index].AutomaticRetry = automaticRetryViewOf(automatic)
	}
	return view, nil
}

func (server *Server) enrichRoomWorkspaceAgentExecution(ctx context.Context, workspace app.RoomWorkspace, view *roomWorkspaceView) error {
	view.Room.HumanActionRequired = false
	for index, summary := range workspace.Tasks {
		preference, err := server.store.Reader().GetTaskAgentExecutionProfilePreference(ctx, summary.Task.ID())
		switch {
		case err == nil:
			view.Tasks[index].AgentExecutionProfile = string(preference.Profile())
		case errors.Is(err, storecontract.ErrNotFound):
		default:
			return err
		}
		if summary.LatestRun == nil {
			if !summary.Task.Archived() && localwebCurrentActionNeedsHuman(view.Tasks[index].CurrentAction.Kind) {
				view.Room.HumanActionRequired = true
			}
			continue
		}
		executionView, err := server.agentExecutionForRun(ctx, *summary.LatestRun)
		if err != nil {
			return err
		}
		view.Tasks[index].LatestRun.AgentExecution = executionView
		delivery, deliveryErr := server.service.LoadTaskDeliverySummary(ctx, *summary.LatestRun)
		if deliveryErr != nil {
			// Missing evidence must not make an accepted task look delivered, or
			// prevent the remaining tasks in this Room from being read.
			delivery = &app.TaskDeliverySummary{Unavailable: true, Repositories: []app.DeliveryRepositorySummary{}}
		}
		view.Tasks[index].Delivery = delivery
		events, err := server.store.Reader().ListRunEvents(ctx, summary.LatestRun.ID())
		if err != nil {
			return err
		}
		view.Tasks[index].LatestRun.TerminalReason = safeTerminalReason(events)
		automatic, err := server.service.AutomaticRetryForRun(ctx, summary.LatestRun.ID())
		if err != nil {
			return err
		}
		view.Tasks[index].LatestRun.AutomaticRetry = automaticRetryViewOf(automatic)
		view.Tasks[index].CurrentAction = automaticRetryCurrentAction(view.Tasks[index].CurrentAction, automatic)
		if automatic != nil && (automatic.State == app.AutomaticRetryBlocked || automatic.State == app.AutomaticRetryExhausted) && summary.LatestRun.State() == domain.RunStateReady {
			view.Tasks[index].CurrentAction.Kind = app.CurrentActionRecoverRun
			view.Tasks[index].CurrentAction.Reason = "The automatic Agent retry could not safely start and requires human intervention."
		}
		application, err := server.store.Reader().GetPatchApplication(ctx, summary.LatestRun.ID())
		if err == nil {
			view.Tasks[index].LatestRun.PatchApplicationState = string(application.State())
		} else if !errors.Is(err, storecontract.ErrNotFound) {
			return err
		}

		closures, err := server.store.Reader().ListResultClosuresForRun(ctx, summary.LatestRun.ID())
		if err != nil {
			return err
		}
		view.Tasks[index].ResultClosed = len(closures) > 0
		if len(closures) > 0 && summary.LatestRun.State() != domain.RunStateAccepted {
			view.Tasks[index].CurrentAction.Kind = app.CurrentActionViewTerminal
			view.Tasks[index].CurrentAction.Reason = "Remaining result eligibility is closed; files and history are retained until explicit cleanup."
		}
		if !summary.Task.Archived() && (localwebCurrentActionNeedsHuman(view.Tasks[index].CurrentAction.Kind) || automatic != nil && (automatic.State == app.AutomaticRetryBlocked || automatic.State == app.AutomaticRetryExhausted)) {
			view.Room.HumanActionRequired = true
		}
	}
	return nil
}

func localwebCurrentActionNeedsHuman(kind app.CurrentActionKind) bool {
	switch kind {
	case app.CurrentActionEditPlan, app.CurrentActionReviewPlan, app.CurrentActionStartRun, app.CurrentActionStartAttempt,
		app.CurrentActionReviewResult, app.CurrentActionRetryImplementation, app.CurrentActionContinueSuccessorPlan, app.CurrentActionOpenRelatedTask:
		return true
	default:
		return false
	}
}

func (server *Server) enrichRoomDirectoryAutomaticRetry(ctx context.Context, directory app.RoomDirectory, view *roomDirectoryView) error {
	for index, room := range directory.ActiveRooms {
		workspace, err := server.service.GetRoomWorkspace(ctx, room.Room.ID())
		if err != nil {
			return err
		}
		human := false
		for _, task := range workspace.Tasks {
			if task.Task.Archived() {
				continue
			}
			var automatic *app.AutomaticRetry
			if task.LatestRun != nil {
				automatic, err = server.service.AutomaticRetryForRun(ctx, task.LatestRun.ID())
				if err != nil {
					return err
				}
			}
			if automatic != nil && (automatic.State == app.AutomaticRetryPending || automatic.State == app.AutomaticRetryRetrying) {
				continue
			}
			if localwebCurrentActionNeedsHuman(task.CurrentAction.Kind) || automatic != nil && (automatic.State == app.AutomaticRetryBlocked || automatic.State == app.AutomaticRetryExhausted) {
				human = true
				break
			}
		}
		view.ActiveRooms[index].HumanActionRequired = human
	}
	return nil
}

func (server *Server) agentExecutionForRun(ctx context.Context, run domain.AgentRun) (*agentExecutionView, error) {
	attempt, err := server.store.Reader().GetCurrentAttempt(ctx, run.ID())
	if err == nil {
		return agentExecutionViewOf(attempt.AgentExecutionProfileBinding()), nil
	}
	if !errors.Is(err, storecontract.ErrNotFound) {
		return nil, err
	}
	charter, err := server.store.Reader().GetCharter(ctx, run.CharterID())
	if err != nil {
		return nil, err
	}
	return agentExecutionViewOf(charter.AgentExecutionProfileBinding()), nil
}

func runSummaryViewOf(run domain.AgentRun) runSummaryView {
	return runSummaryView{
		ID: run.ID().String(), TaskID: run.TaskID().String(), Status: run.State(), Version: run.Version(), Attempt: run.CurrentAttemptNumber(),
		CreatedAt: run.CreatedAt().Format(time.RFC3339Nano), UpdatedAt: run.UpdatedAt().Format(time.RFC3339Nano),
		StartedAt: formatOptionalTime(run.StartedAt()), ReviewRequestedAt: formatOptionalTime(run.ReviewRequestedAt()), TerminalAt: formatOptionalTime(run.TerminalAt()),
	}
}

func formatOptionalTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format(time.RFC3339Nano)
}

func patchApplicationViewOf(application domain.PatchApplication) patchApplicationView {
	post := application.PostStateDigest()
	postText := ""
	if post != ([32]byte{}) {
		postText = fmt.Sprintf("%x", post)
	}
	return patchApplicationView{
		State: string(application.State()), Version: application.Version(), PatchDigest: fmt.Sprintf("%x", application.PatchDigest()),
		TargetIdentity: application.TargetIdentity(), BaseRevision: application.BaseRevision(), AffectedPaths: application.AffectedPaths(),
		PreStateDigest: fmt.Sprintf("%x", application.PreStateDigest()), PostStateDigest: postText, Reason: application.Reason(),
		StartedAt: application.StartedAt().Format(time.RFC3339Nano), UpdatedAt: application.UpdatedAt().Format(time.RFC3339Nano),
		AppliedAt: formatOptionalTime(application.AppliedAt()),
	}
}

func patchInspectionFailureViewOf(failure domain.PatchInspectionFailure) patchApplicationView {
	return patchApplicationView{
		State: string(failure.State()), Version: failure.Version(), PatchDigest: fmt.Sprintf("%x", failure.PatchDigest()),
		TargetIdentity: failure.TargetIdentity(), AffectedPaths: failure.AffectedPaths(), Reason: failure.Reason(),
		StartedAt: failure.StartedAt().Format(time.RFC3339Nano), UpdatedAt: failure.UpdatedAt().Format(time.RFC3339Nano),
	}
}

func (server *Server) runView(ctx context.Context, runID domain.RunID) (runView, error) {
	reader := server.store.Reader()
	run, err := reader.GetRun(ctx, runID)
	if err != nil {
		return runView{}, err
	}
	task, err := reader.GetTask(ctx, run.TaskID())
	if err != nil {
		return runView{}, err
	}
	room, err := reader.GetRoom(ctx, task.RoomID())
	if err != nil {
		return runView{}, err
	}
	charter, err := reader.GetCharter(ctx, run.CharterID())
	if err != nil {
		return runView{}, err
	}
	revisions, err := reader.LookupRevisions(ctx, room.ID(), charter.ContextRevisionIDs())
	if err != nil {
		return runView{}, err
	}
	events, err := reader.ListRunEvents(ctx, run.ID())
	if err != nil {
		return runView{}, err
	}
	attempts, err := reader.ListAttemptsForRun(ctx, run.ID())
	if err != nil {
		return runView{}, err
	}
	modelProvenance := modelProvenanceByAttempt(attempts, events)
	automaticRetry, err := server.service.AutomaticRetryForRun(ctx, run.ID())
	if err != nil {
		return runView{}, err
	}
	now := time.Now().UTC()
	capabilities := make([]string, 0, len(charter.CapabilityEnvelope()))
	for capability, allowed := range charter.CapabilityEnvelope() {
		if allowed {
			capabilities = append(capabilities, capability)
		}
	}
	sort.Strings(capabilities)
	agentID, agentName := agentIdentity(charter.AdapterID())
	scm, err := server.service.SCMAvailability(ctx, task.ID())
	if err != nil {
		return runView{}, err
	}
	patchTargetReady := server.taskWorktreesReady
	_, resourceSnapshotErr := reader.GetTaskResourceSnapshot(ctx, task.ID())
	if resourceSnapshotErr != nil && !errors.Is(resourceSnapshotErr, storecontract.ErrNotFound) {
		return runView{}, resourceSnapshotErr
	}
	hasResourceSnapshot := resourceSnapshotErr == nil
	localConnectedReview := charter.CapabilityEnvelope()[speccoding.LocalConnectedNoSandboxCapability] || charter.CapabilityEnvelope()[speccoding.IsolatedLocalCapability]
	verificationDisposition := verificationDispositionView{
		State:  "not_applicable",
		Reason: "Independent verification applies only to registered Spec Coding Tasks; this diagnostic Agent Run is complete without a Verification Result.",
	}
	if binding, bindingErr := reader.GetSpecCodingBinding(ctx, task.ID()); bindingErr == nil {
		if binding.Status == storecontract.SpecCodingRegistered && localConnectedReview {
			verificationDisposition = verificationDispositionView{State: "not_applicable", Reason: "Local Connected uses Agent-reported checks; no independent Verifier ran."}
		} else if binding.Status == storecontract.SpecCodingRegistered {
			verificationDisposition = verificationDispositionView{State: "available", Reason: "Registered Spec Coding inputs are ready for independent verification."}
			if !server.verifierStatus.Enabled || server.verifier == nil {
				verificationDisposition = verificationDispositionView{State: "unavailable", Reason: "Independent verification is unavailable; inspect readiness before retrying."}
			}
		}
	} else if !errors.Is(bindingErr, storecontract.ErrNotFound) {
		return runView{}, bindingErr
	}
	var runTaskWorktree *taskWorktreeView
	if server.product || server.pathPiEnabled {
		binding, bindingErr := reader.GetTaskWorktreeBinding(ctx, task.ID())
		patchTargetReady = patchTargetReady && bindingErr == nil && binding.State() == domain.TaskWorktreeReady
		if bindingErr == nil {
			item := taskWorktreeViewOf(binding)
			runTaskWorktree = &item
		}
	}
	var taskAgentExecutionProfile string
	if preference, preferenceErr := reader.GetTaskAgentExecutionProfilePreference(ctx, task.ID()); preferenceErr == nil {
		taskAgentExecutionProfile = string(preference.Profile())
	} else if charter.AdapterID() == agentpi.AdapterID {
		return runView{}, preferenceErr
	} else if !errors.Is(preferenceErr, storecontract.ErrNotFound) {
		return runView{}, preferenceErr
	}

	view := runView{
		ID: run.ID().String(), Status: run.State(), Version: run.Version(), Adapter: charter.AdapterID(),
		TerminalReason:        safeTerminalReason(events),
		AutomaticRetry:        automaticRetryViewOf(automaticRetry),
		Room:                  roomViewOf(room),
		Task:                  taskView{ID: task.ID().String(), Title: task.Title(), Goal: task.Goal(), AgentExecutionProfile: taskAgentExecutionProfile, Worktree: runTaskWorktree},
		Context:               []contextView{},
		Timeline:              []timelineView{},
		Trajectory:            []trajectoryView{},
		Artifacts:             []artifactView{},
		Unknowns:              []string{},
		Criteria:              []criterionView{},
		Candidates:            []candidateView{},
		Revisions:             []roomRevisionView{},
		DecisionHistory:       []decisionGateView{},
		AttemptHistory:        []attemptHistoryView{},
		ReviewHistory:         []reviewView{},
		VerificationHistory:   []verificationView{},
		VerifiedReviewHistory: []verifiedReviewView{},
		SCM:                   scmView{Configured: scm.Configured, CanCreateDraftMR: scm.Configured && run.State() == domain.RunStateAccepted, Reason: scm.Reason},
		Agent: agentView{
			Profile:  agentProfileView{ID: agentID, Name: agentName, Role: "Coding Agent", Capabilities: capabilities},
			RunState: run.State(),
		},
		AgentExecution:          agentExecutionViewOf(charter.AgentExecutionProfileBinding()),
		VerificationDisposition: verificationDisposition,
		Blockers:                []string{},
		Controls: controlsView{
			CanCancel:             run.State() == domain.RunStateRunning || run.State() == domain.RunStateStopping,
			CanRetry:              run.State() == domain.RunStateRevisionRequired,
			CanReview:             run.State() == domain.RunStateAwaitingReview,
			CanAcceptAndApply:     run.State() == domain.RunStateAwaitingReview && patchTargetReady,
			CanStartVerification:  run.State() == domain.RunStateAwaitingVerification && verificationDisposition.State == "available",
			CanCancelVerification: run.State() == domain.RunStateVerifying,
			CanCreateDraftMR:      scm.Configured && run.State() == domain.RunStateAccepted,
		},
	}
	if application, applicationErr := reader.GetPatchApplication(ctx, run.ID()); applicationErr == nil {
		item := patchApplicationViewOf(application)
		view.PatchApplication = &item
		view.Controls.CanApplyPatch = run.State() == domain.RunStateAccepted && patchTargetReady &&
			(application.State() == domain.PatchApplicationConflict || application.State() == domain.PatchApplicationRecoveryRequired)
		if application.State() == domain.PatchApplicationConflict || application.State() == domain.PatchApplicationRecoveryRequired {
			view.Blockers = append(view.Blockers, application.Reason())
		}
	} else if errors.Is(applicationErr, storecontract.ErrNotFound) {
		if failure, failureErr := reader.GetPatchInspectionFailure(ctx, run.ID()); failureErr == nil {
			item := patchInspectionFailureViewOf(failure)
			view.PatchApplication = &item
			view.Controls.CanApplyPatch = run.State() == domain.RunStateAccepted && patchTargetReady
			view.Blockers = append(view.Blockers, failure.Reason())
		} else if errors.Is(failureErr, storecontract.ErrNotFound) {
			view.Controls.CanApplyPatch = run.State() == domain.RunStateAccepted && patchTargetReady
			if run.State() == domain.RunStateAccepted && !patchTargetReady && !hasResourceSnapshot {
				view.Blockers = append(view.Blockers, "This Task's managed worktree is unavailable; inspect its worktree state and restart Chora to retry recovery.")
			}
		} else {
			return runView{}, failureErr
		}
	} else {
		return runView{}, applicationErr
	}
	if acceptance, acceptanceErr := reader.GetCurrentTechnicalPlanAcceptance(ctx, task.ID()); acceptanceErr == nil {
		planRevision, revisionErr := reader.GetTechnicalPlanRevision(ctx, acceptance.RevisionID())
		planReview, reviewErr := reader.GetTechnicalPlanReview(ctx, planRevision.ID())
		if revisionErr != nil || reviewErr != nil || planRevision.TaskID() != task.ID() || planReview.ID() != acceptance.ReviewID() || planReview.RevisionID() != planRevision.ID() {
			return runView{}, fmt.Errorf("accepted Run Plan lineage drift")
		}
		view.Plan = &runPlanView{RevisionID: planRevision.ID().String(), Content: planRevision.Content(), ActivatedBy: planReview.Reviewer()}
	} else if !errors.Is(acceptanceErr, storecontract.ErrNotFound) {
		return runView{}, acceptanceErr
	}
	if server.product || server.pathPiEnabled {
		view.Room.WorkspaceRoot = ""
	}
	roomRevisions, err := reader.ListRoomRevisions(ctx, room.ID())
	if err != nil {
		return runView{}, err
	}
	revisionByCandidate := make(map[string]roomRevisionView)
	for _, revision := range roomRevisions {
		item := roomRevisionViewOf(revision)
		view.Revisions = append(view.Revisions, item)
		if candidateID := revision.Provenance().CandidateID; candidateID != "" {
			revisionByCandidate[candidateID] = item
		}
	}
	selection, err := reader.GetTaskRevisionSelection(ctx, task.ID())
	if err == nil {
		item := selectionViewOf(selection)
		view.Selection = &item
	} else if !errors.Is(err, storecontract.ErrNotFound) {
		return runView{}, err
	}
	candidates, err := reader.ListCandidatesForRun(ctx, run.ID())
	if err != nil {
		return runView{}, err
	}
	for _, candidate := range candidates {
		item := candidateView{ID: candidate.ID().String(), Title: candidate.Title(), Body: candidate.Body(), State: candidate.State(), Version: candidate.Version(), SourceRunID: candidate.SourceRunID().String(), SourceArtifactID: candidate.SourceArtifactID().String()}
		if decision, decisionErr := reader.GetCandidateDecision(ctx, candidate.ID()); decisionErr == nil {
			item.Decision = &candidateDecisionView{ID: decision.ID().String(), Kind: string(decision.Kind()), Note: decision.Note(), ActorID: decision.ActorID(), SessionID: decision.SessionID(), DecidedAt: decision.DecidedAt().Format(time.RFC3339Nano)}
		} else if !errors.Is(decisionErr, storecontract.ErrNotFound) {
			return runView{}, decisionErr
		}
		if revision, found := revisionByCandidate[candidate.ID().String()]; found {
			item.Revision = &revision
		}
		view.Candidates = append(view.Candidates, item)
	}
	for _, revision := range revisions {
		view.Context = append(view.Context, contextView{Kind: string(revision.Kind()), Title: revision.Title(), Body: revision.Body()})
	}
	var latestEventAt time.Time
	for _, event := range events {
		title, detail := eventCopy(event.Type())
		if event.Type() == "decision.resolved" {
			var payload struct {
				SelectedOptionID string `json:"selected_option_id"`
			}
			if json.Unmarshal(event.NormalizedJSON(), &payload) == nil && payload.SelectedOptionID != "" {
				detail = "The human selected " + payload.SelectedOptionID + "; execution resumed from the persisted choice."
			}
		}
		view.Timeline = append(view.Timeline, timelineView{
			Sequence: event.Sequence(), Type: event.Type(), Title: title, Detail: detail, Time: event.OccurredAt().Format(time.RFC3339),
		})
		latestEventAt = event.OccurredAt()
	}
	view.Trajectory = projectTrajectory(events, run.State())
	gates, err := reader.ListDecisionGatesForRun(ctx, run.ID())
	if err != nil {
		return runView{}, err
	}
	var openGate bool
	for _, gate := range gates {
		item := decisionGateViewOf(gate)
		view.DecisionHistory = append(view.DecisionHistory, item)
		if gate.Status() == domain.DecisionGateOpen {
			value := item
			view.Gate = &value
			openGate = true
		}
	}
	visibility := domain.ProjectAgentVisibility(run.State(), latestEventAt, now)
	if openGate {
		visibility.Presence = domain.AgentPresenceWaiting
		visibility.Phase = domain.AgentPhaseWaitingForDecision
	}
	if automaticRetry != nil {
		switch automaticRetry.State {
		case app.AutomaticRetryPending:
			visibility.Presence = domain.AgentPresenceWaiting
		case app.AutomaticRetryRetrying:
			visibility.Presence = domain.AgentPresenceActive
		}
	}
	view.Agent.Presence = visibility.Presence
	view.Activity = activityView{
		Phase: visibility.Phase, CurrentAction: currentActionCopy(visibility.Phase, view.Timeline),
		ElapsedMS: runElapsedMS(run, now),
	}
	if automaticRetry != nil {
		switch automaticRetry.State {
		case app.AutomaticRetryPending:
			view.Activity.CurrentAction = fmt.Sprintf("Waiting for automatic retry %d of %d", automaticRetry.RetriesUsed+1, automaticRetry.MaxRetries)
		case app.AutomaticRetryRetrying:
			view.Activity.CurrentAction = fmt.Sprintf("Running automatic retry %d of %d", automaticRetry.RetriesUsed, automaticRetry.MaxRetries)
		}
	}
	if len(view.Timeline) > 0 {
		latest := view.Timeline[len(view.Timeline)-1]
		view.Activity.LatestEvent = &latest
	}
	if visibility.Presence == domain.AgentPresenceUnresponsive && (run.State() == domain.RunStateRunning || run.State() == domain.RunStateStopping) {
		view.Blockers = append(view.Blockers, "No persisted Agent event has arrived within 30 seconds.")
	}
	if run.State() == domain.RunStateRecoveryRequired && (automaticRetry == nil || automaticRetry.State == app.AutomaticRetryBlocked || automaticRetry.State == app.AutomaticRetryExhausted) {
		view.Blockers = append(view.Blockers, "The managed execution requires an explicit recovery decision.")
	}
	if automaticRetry != nil && automaticRetry.State == app.AutomaticRetryBlocked && automaticRetry.BlockedReason != "" {
		view.Blockers = append(view.Blockers, automaticRetryBlockedCopy(automaticRetry.BlockedReason))
	}
	if openGate {
		view.Blockers = append(view.Blockers, "The Agent is waiting for a bounded execution decision.")
	}

	checks := map[string]storecontract.Check{}
	verifiedChecks := map[string]domain.AcceptanceCheck{}
	var currentAgentAttemptID domain.AttemptID
	var hasCurrentAgentAttempt bool
	if attempt, attemptErr := reader.GetCurrentAttempt(ctx, run.ID()); attemptErr == nil {
		currentAgentAttemptID, hasCurrentAgentAttempt = attempt.ID(), true
		cancelReason, cancelRetryAuthorized := app.CancelRetryAuthority(events, attempt.ID())
		if run.State() == domain.RunStateCancelled {
			view.CancelReason = cancelReason
		}
		sandbox := sandboxIdentityView{
			Status: "unavailable", Provider: "not-adopted", Mode: "not-reported",
			Image: "not-reported", PolicyFingerprint: "not-reported",
		}
		executionWorkspace := charter.WorkspaceRoot()
		if attempt.AdapterID() == agentpi.AdapterID {
			sandbox = sandboxIdentityViewOf(attempt.AgentExecutionProfileBinding(), server.piStatus)
			if attempt.AgentExecutionProfileBinding().Profile() == domain.AgentExecutionProfileIsolatedLocal {
				sandbox = sandboxIdentityView{Status: "unavailable", Provider: domain.DockerExecutionProvider, Mode: "Isolated Local", Image: "not-reported", PolicyFingerprint: "not-reported"}
				if binding, err := reader.GetSpecCodingBinding(ctx, run.TaskID()); err == nil && sha256.Sum256(binding.ActiveContractJSON) == binding.ActiveContractDigest {
					var frozen struct {
						Candidate struct {
							Sandbox struct {
								SHA256 string `json:"sha256"`
							} `json:"sandbox"`
							Policy struct {
								SHA256 string `json:"sha256"`
							} `json:"policy"`
						} `json:"candidate"`
					}
					if json.Unmarshal(binding.ActiveContractJSON, &frozen) == nil && len(frozen.Candidate.Sandbox.SHA256) == 64 && len(frozen.Candidate.Policy.SHA256) == 64 {
						sandbox.Status = "adopted"
						sandbox.Image = "sha256:" + frozen.Candidate.Sandbox.SHA256
						sandbox.PolicyFingerprint = frozen.Candidate.Policy.SHA256
					}
				}
			}

			if attempt.AgentExecutionProfileBinding().ExecutionProvider() == domain.DockerExecutionProvider {
				executionWorkspace = "/workspace/repository"
			} else if server.product || server.pathPiEnabled {
				executionWorkspace = "trusted local Task worktree (path hidden)"
				if runTaskWorktree != nil {
					executionWorkspace = "Task worktree · " + runTaskWorktree.Locator
				}
			}
		}
		view.AgentExecution = agentExecutionViewOf(attempt.AgentExecutionProfileBinding())
		view.Attempt = attempt.Sequence()
		view.AttemptDetail = &attemptDetailView{
			ID: attempt.ID().String(), Sequence: attempt.Sequence(), State: attempt.State(),
			ExecutionWorkspace: executionWorkspace, Sandbox: sandbox, AgentExecution: agentExecutionViewOf(attempt.AgentExecutionProfileBinding()), ModelProvenance: modelProvenance[attempt.ID().String()],
		}
		var sessionFound bool
		var attemptSession storecontract.RuntimeSession
		if session, sessionErr := reader.GetRuntimeSessionForAttempt(ctx, attempt.ID()); sessionErr == nil {
			sessionFound = true
			attemptSession = session
			view.AttemptDetail.Runtime = &runtimeIdentityView{
				SessionID: session.ID.String(), AdapterID: session.AdapterID, Kind: session.RuntimeKind,
				Version: valueOrNotReported(session.RuntimeVersion), Fingerprint: fingerprintText(session.RuntimeFingerprint),
				ExternalSession: session.ExternalReference, State: session.State,
			}
			if attempt.AdapterID() != agentpi.AdapterID {
				view.AttemptDetail.ExecutionWorkspace = session.WorkingRoot
			}
			if run.State() == domain.RunStateRecoveryRequired && strings.TrimSpace(session.Diagnostic) != "" {
				view.Blockers = append(view.Blockers, session.Diagnostic)
			}
			if recoveryRetryReady(run.State(), attempt.State(), session) {
				view.Controls.CanRetry = true
			}
			if cancelledRetryReady(run.State(), attempt.State(), session, cancelRetryAuthorized) {
				view.Controls.CanRetry = true
			}
		} else if !errors.Is(sessionErr, storecontract.ErrNotFound) {
			return runView{}, sessionErr
		}
		if automaticRetry != nil {
			candidate := automaticRetry.State == app.AutomaticRetryPending || automaticRetry.State == app.AutomaticRetryRetrying || automaticRetry.State == app.AutomaticRetryBlocked
			view.Controls.CanCancel = candidate && (run.State() == domain.RunStateRunning || run.State() == domain.RunStateStopping ||
				(run.State() == domain.RunStateReady && !sessionFound) ||
				(run.State() == domain.RunStateRecoveryRequired && sessionFound && automaticRetryFinalizedView(attempt.State(), attemptSession)))
			if automaticRetry.State == app.AutomaticRetryBlocked && run.State() == domain.RunStateReady && !sessionFound {
				view.Controls.CanRetry = true
			}
			if automaticRetry.State == app.AutomaticRetryPending || automaticRetry.State == app.AutomaticRetryRetrying {
				view.Controls.CanRetry = false
			}
		}
		if server.pathPiEnabled && attempt.AdapterID() == agentpi.AdapterID && !server.piStatus.Enabled {
			view.Controls.CanRetry = false
			view.Blockers = append(view.Blockers, "Pi recovery is unavailable. Restore the recorded Pi installation and Task worktree, then restart Chora. "+server.piStatus.Reason)
		}
		if server.product && attempt.AdapterID() != agentpi.AdapterID {
			view.AttemptDetail.ExecutionWorkspace = "diagnostic workspace (path hidden)"
		}
		if attempt.Sequence() > 1 && attempt.ContextDelta() != "" {
			var delta retryDeltaEnvelope
			if err := json.Unmarshal([]byte(attempt.ContextDelta()), &delta); err != nil {
				return runView{}, err
			}
			if delta.SchemaVersion == "chora.retry-delta.v1" {
				view.Retry = &retryView{
					RejectionNote: delta.RejectionNote, UnmetCriterionIDs: delta.UnmetCriterionIDs, Instructions: delta.Instructions,
				}
			}
		}
		projection, projectionErr := reader.GetTerminalProjection(ctx, attempt.ID())
		if projectionErr != nil {
			return runView{}, projectionErr
		}
		for _, artifact := range projection.Artifacts {
			view.Artifacts = append(view.Artifacts, artifactView{ID: artifact.ID.String(), Kind: artifact.Kind, Locator: artifact.Locator, Digest: artifactDigest(artifact), MediaType: artifact.MediaType, Description: artifact.Description})
		}
		for _, unknown := range projection.Unknowns {
			view.Unknowns = append(view.Unknowns, unknown.Body)
		}
		for _, check := range projection.Checks {
			checks[check.CriterionID] = check
		}
		if projection.ContextConsumption != nil {
			var evidence execution.ContextConsumption
			if err := json.Unmarshal([]byte(projection.ContextConsumption.Body), &evidence); err != nil || !evidence.Valid() {
				return runView{}, fmt.Errorf("invalid persisted context consumption evidence")
			}
			view.ContextConsumption = &contextConsumptionView{
				SnapshotID: evidence.SnapshotID, SnapshotDigest: evidence.SnapshotDigest, CandidateRevisionID: evidence.CandidateRevisionID,
				IncludedRevisionIDs: append([]string(nil), evidence.IncludedRevisionIDs...), ExcludedRevisionIDs: append([]string(nil), evidence.ExcludedRevisionIDs...),
				Derivation: evidence.Derivation, ExcludedContentObserved: evidence.ExcludedContentObserved,
			}
		}
		if snapshot, snapshotErr := reader.GetSnapshot(ctx, attempt.ContextSnapshotID()); snapshotErr == nil {
			item := snapshotView{ID: snapshot.ID().String(), Digest: fmt.Sprintf("%x", snapshot.Digest()), Selected: []selectionItemView{}, Excluded: []selectionItemView{}}
			if frozenSelection, ok := snapshot.Selection(); ok {
				manifest := selectionViewOf(frozenSelection)
				item.Selected = append(item.Selected, manifest.Selected...)
				item.Excluded = append(item.Excluded, manifest.Excluded...)
			}
			view.Snapshot = &item
		} else if !errors.Is(snapshotErr, storecontract.ErrNotFound) {
			return runView{}, snapshotErr
		}
	} else if !errors.Is(attemptErr, storecontract.ErrNotFound) {
		return runView{}, attemptErr
	}
	if report, reportErr := reader.GetAgentReportForRun(ctx, run.ID()); reportErr == nil {
		item := agentReportView{ID: report.ID().String(), AttemptID: report.AttemptID().String(), Summary: report.Summary(), FinalText: report.FinalText(), ClaimedChecks: []agentClaimView{}, CompletedAt: report.CompletedAt().Format(time.RFC3339Nano), Authority: "non_authoritative_agent_claim"}
		for _, claim := range report.ClaimedChecks() {
			item.ClaimedChecks = append(item.ClaimedChecks, agentClaimView{CriterionID: claim.CriterionID.String(), Status: claim.Status, Evidence: claim.Evidence})
		}
		view.AgentReport = &item
	} else if !errors.Is(reportErr, storecontract.ErrNotFound) {
		return runView{}, reportErr
	}
	verificationRuns, err := reader.ListVerificationRunsForRun(ctx, run.ID())
	if err != nil {
		return runView{}, err
	}
	var currentVerification *verificationView
	var currentReviewResultID string
	for _, verificationRun := range verificationRuns {
		item, err := verificationViewOf(ctx, reader, verificationRun)
		if err != nil {
			return runView{}, err
		}
		view.VerificationHistory = append(view.VerificationHistory, item)
		if hasCurrentAgentAttempt && verificationRun.AgentAttemptID() == currentAgentAttemptID {
			copy := item
			currentVerification = &copy
			view.Verification = &copy
			view.Controls.CanStartVerification = false
			view.Controls.CanReview = false
			view.Controls.CanRetry = false
			for _, attempt := range item.Attempts {
				if attempt.State == string(domain.VerificationAttemptCancelled) && run.State() == domain.RunStateAwaitingVerification ||
					attempt.State == string(domain.VerificationAttemptRecoveryRequired) && run.State() == domain.RunStateVerificationRecoveryRequired {
					view.Controls.CanRetryVerification = true
				}
			}
			if item.Result != nil {
				currentReviewResultID = item.Result.ID
				attemptID, err := domain.ParseVerificationAttemptID(item.Result.AttemptID)
				if err != nil {
					return runView{}, err
				}
				checks, err := reader.ListVerificationAcceptanceChecks(ctx, attemptID)
				if err != nil {
					return runView{}, err
				}
				for _, check := range checks {
					verifiedChecks[check.CriterionID().String()] = check
				}
			}
		}
	}
	hasCurrentReviewResult := currentVerification != nil && currentVerification.Result != nil
	if hasCurrentReviewResult {
		currentReviewResultID = currentVerification.Result.ID
	}
	if !hasCurrentReviewResult && localConnectedReview && hasCurrentAgentAttempt {
		localResult, localErr := reader.GetLocalReviewResultForAgentAttempt(ctx, currentAgentAttemptID)
		if localErr == nil {
			currentReviewResultID = localResult.ID().String()
			hasCurrentReviewResult = true
		} else if !errors.Is(localErr, storecontract.ErrNotFound) {
			return runView{}, localErr
		}
	}
	if hasCurrentReviewResult &&
		(run.State() == domain.RunStateAwaitingReview || run.State() == domain.RunStateRevisionRequired || run.State() == domain.RunStateAccepted) {
		change, changeErr := server.service.LoadReviewableChange(ctx, run.ID())
		if changeErr != nil {
			view.Controls.CanReview = false
			view.Controls.CanAcceptAndApply = false
			view.Controls.CanRetry = false
			view.Blockers = append(view.Blockers, "Review disabled: "+changeErr.Error())
		} else {
			item := reviewablePatchViewOf(run.ID(), change)
			view.ReviewablePatch = &item
			view.Controls.CanReview = run.State() == domain.RunStateAwaitingReview && change.Outcome == domain.ResultReviewReady
			view.Controls.CanAcceptAndApply = view.Controls.CanReview && patchTargetReady
			if run.State() == domain.RunStateRevisionRequired {
				decision, decisionErr := reader.GetVerifiedReviewForResult(ctx, change.Binding.ResultID)
				switch {
				case decisionErr == nil && decision.AllowsAgentRetry():
					view.Controls.CanRetry = true
				case decisionErr == nil:
					route, routeErr := reader.GetVerifiedReviewRoute(ctx, decision.ID())
					if routeErr != nil {
						return runView{}, routeErr
					}
					if route.RejectionClass == domain.ReviewRejectionPlanningGap {
						view.Blockers = append(view.Blockers, "Agent Retry blocked: planning_gap created Technical Plan Draft "+route.PlanningDraftID.String()+"; accept a successor Revision before starting a new Run.")
					} else {
						view.Blockers = append(view.Blockers, "Agent Retry blocked: contract_change_required created related Task "+route.RelatedTaskID.String()+".")
					}
				case errors.Is(decisionErr, storecontract.ErrNotFound) && change.Outcome == domain.ResultNeedsRevision:
					view.Controls.CanRetry = true
				case decisionErr != nil && !errors.Is(decisionErr, storecontract.ErrNotFound):
					return runView{}, decisionErr
				}
			}
		}
	}
	for _, attempt := range attempts {
		item := attemptHistoryView{ID: attempt.ID().String(), Sequence: attempt.Sequence(), State: attempt.State(), SnapshotID: attempt.ContextSnapshotID().String(), SnapshotDigest: fmt.Sprintf("%x", attempt.ContextDigest()), Artifacts: []artifactView{}, Unknowns: []string{}, AgentExecution: agentExecutionViewOf(attempt.AgentExecutionProfileBinding()), ModelProvenance: modelProvenance[attempt.ID().String()]}
		if predecessor, ok := attempt.Predecessor(); ok {
			item.PredecessorID = predecessor.String()
		}
		projection, err := reader.GetTerminalProjection(ctx, attempt.ID())
		if err != nil {
			return runView{}, err
		}
		item.Summary = projection.Summary.Body
		for _, artifact := range projection.Artifacts {
			item.Artifacts = append(item.Artifacts, artifactView{ID: artifact.ID.String(), Kind: artifact.Kind, Locator: artifact.Locator, Digest: artifactDigest(artifact), MediaType: artifact.MediaType, Description: artifact.Description})
		}
		for _, unknown := range projection.Unknowns {
			item.Unknowns = append(item.Unknowns, unknown.Body)
		}
		view.AttemptHistory = append(view.AttemptHistory, item)
	}
	for _, criterion := range task.Criteria() {
		pendingEvidence := "Waiting for independent verification."
		if localConnectedReview {
			pendingEvidence = "Waiting for an Agent-reported check; no independent Verifier is configured for Local Connected."
		}
		item := criterionView{ID: criterion.ID().String(), Title: criterion.Title(), Status: "pending", Evidence: pendingEvidence}
		if check, ok := verifiedChecks[item.ID]; ok {
			item.Status = string(check.Status())
			ids := check.EvidenceIDs()
			values := make([]string, 0, len(ids))
			for _, id := range ids {
				values = append(values, id.String())
			}
			item.Evidence = "Independent Verifier evidence: " + strings.Join(values, ", ")
		} else if check, ok := checks[item.ID]; ok {
			item.Status = check.Status
			item.Evidence = "Legacy Agent claim (non-authoritative): " + check.Evidence
		}
		view.Criteria = append(view.Criteria, item)
	}
	verifiedReviews, err := reader.ListVerifiedReviewsForRun(ctx, run.ID())
	if err != nil {
		return runView{}, err
	}
	for _, decision := range verifiedReviews {
		item := verifiedReviewViewOf(decision)
		if decision.Kind() == domain.ReviewDecisionReject && !decision.AllowsAgentRetry() {
			route, routeErr := reader.GetVerifiedReviewRoute(ctx, decision.ID())
			if routeErr != nil {
				return runView{}, routeErr
			}
			item.SourceTaskID = route.SourceTaskID.String()
			if route.PlanningDraftID.Valid() {
				item.PlanningDraftID = route.PlanningDraftID.String()
			}
			if route.RelatedTaskID.Valid() {
				item.RelatedTaskID = route.RelatedTaskID.String()
			}
		}
		view.VerifiedReviewHistory = append(view.VerifiedReviewHistory, item)
		view.ReviewHistory = append(view.ReviewHistory, reviewView{Kind: string(decision.Kind()), Comment: decision.Reason(), DecidedAt: decision.DecidedAt().Format(time.RFC3339Nano)})
		if (run.State() == domain.RunStateRevisionRequired || run.State() == domain.RunStateAccepted) && item.ResultID == currentReviewResultID {
			copy := item
			view.VerifiedReview = &copy
			view.Review = &reviewView{Kind: string(decision.Kind()), Comment: decision.Reason(), DecidedAt: decision.DecidedAt().Format(time.RFC3339)}
		}
	}
	if review, reviewErr := reader.GetLatestReviewForRun(ctx, run.ID()); reviewErr == nil {
		if run.State() == domain.RunStateRevisionRequired || run.State() == domain.RunStateAccepted {
			view.Review = &reviewView{Kind: string(review.Kind()), Comment: review.Comment(), DecidedAt: review.DecidedAt().Format(time.RFC3339)}
			wantedType := "review." + string(review.Kind()) + "ed"
			for index := len(view.Timeline) - 1; index >= 0; index-- {
				if view.Timeline[index].Type == wantedType {
					view.Timeline[index].Detail = review.Comment()
					break
				}
			}
		}
	} else if !errors.Is(reviewErr, storecontract.ErrNotFound) {
		return runView{}, reviewErr
	}
	reviews, err := reader.ListReviewsForRun(ctx, run.ID())
	if err != nil {
		return runView{}, err
	}
	for _, review := range reviews {
		view.ReviewHistory = append(view.ReviewHistory, reviewView{Kind: string(review.Kind()), Comment: review.Comment(), DecidedAt: review.DecidedAt().Format(time.RFC3339Nano)})
	}
	if view.AgentExecution != nil && view.AgentExecution.RuntimeSource == "local_pi" {
		var total float64
		seen, missing := false, false
		for _, event := range events {
			if event.Type() == "run.started" || event.Type() == "attempt.started" {
				total, seen, missing = 0, false, false
			}
			if event.Type() != "assistant_message" || event.Source() != "adapter" {
				continue
			}
			var payload struct {
				Cost *float64 `json:"reported_cost"`
			}
			if json.Unmarshal(event.NormalizedJSON(), &payload) == nil && payload.Cost != nil && *payload.Cost >= 0 {
				total += *payload.Cost
				seen = true
			} else {
				missing = true
			}
		}
		if seen {
			view.AgentExecution.ReportedCost = &total
			view.AgentExecution.CostStatus = "pi_reported_estimate"
			if missing {
				view.AgentExecution.CostStatus = "partial_pi_reported_estimate"
			}
		}
	}
	if localConnectedReview && (run.State() == domain.RunStateCompleted || run.State() == domain.RunStateRecoveryRequired) && view.AgentReport != nil {
		binding, bindingErr := reader.GetSpecCodingBinding(ctx, run.TaskID())
		attempt, attemptErr := reader.GetCurrentAttempt(ctx, run.ID())
		for _, event := range events {
			if event.Type() != "run.completed_no_change" && event.Type() != "attempt.failed" {
				continue
			}
			var terminal struct {
				ResultID       string `json:"result_id"`
				AttemptID      string `json:"attempt_id"`
				ReportID       string `json:"agent_report_id"`
				Outcome        string `json:"outcome"`
				ContractDigest string `json:"contract_digest"`
				SnapshotDigest string `json:"context_snapshot_digest"`
			}
			if json.Unmarshal(event.NormalizedJSON(), &terminal) != nil || terminal.ResultID == "" {
				continue
			}
			if attemptErr != nil || terminal.AttemptID != attempt.ID().String() {
				continue
			}
			validOutcome := (run.State() == domain.RunStateCompleted && terminal.Outcome == "completed_no_change" && event.Type() == "run.completed_no_change" && attempt.State() == domain.AttemptStateOutputSubmitted) || (run.State() == domain.RunStateRecoveryRequired && (terminal.Outcome == "checks_failed" || terminal.Outcome == "checks_incomplete") && attempt.State() == domain.AttemptStateFailed)
			_, idErr := domain.ParseResultID(terminal.ResultID)
			if !validOutcome || idErr != nil || bindingErr != nil || binding.Status != storecontract.SpecCodingRegistered || sha256.Sum256(binding.ActiveContractJSON) != binding.ActiveContractDigest || terminal.ReportID != view.AgentReport.ID || terminal.AttemptID != view.AgentReport.AttemptID || terminal.ContractDigest != fmt.Sprintf("%x", binding.ActiveContractDigest) || terminal.SnapshotDigest != fmt.Sprintf("%x", attempt.ContextDigest()) || binding.SnapshotDigest != attempt.ContextDigest() || view.Result != nil {
				return runView{}, errors.New("terminal Result authority is inconsistent")
			}
			view.Result = &verificationResultView{ID: terminal.ResultID, AttemptID: terminal.AttemptID, Outcome: terminal.Outcome, CreatedAt: event.OccurredAt().Format(time.RFC3339Nano)}
		}
	}
	typedCheckFailure := run.State() == domain.RunStateRecoveryRequired && (safeTerminalReason(events) == "checks_failed" || safeTerminalReason(events) == "checks_incomplete")
	if (run.State() == domain.RunStateCompleted || typedCheckFailure) && (view.Result == nil || view.AgentReport == nil) {
		return runView{}, errors.New("terminal Run result evidence is unavailable")
	}
	view.Controls.CanSwitchAgentExecutionProfile = charter.AdapterID() == agentpi.AdapterID && view.Controls.CanRetry
	if err := server.attachResourceResult(ctx, run, &view); err != nil {
		return runView{}, err
	}
	if closure, closureErr := server.service.LoadResultClosure(ctx, run.ID()); closureErr == nil {
		view.ResultClosed = closure.ClosedAt != nil
		for _, entry := range closure.Entries {
			view.Controls.CanCloseResult = view.Controls.CanCloseResult || entry.Status == "eligible"
		}
		if view.ResultClosed {
			view.Controls.CanReview = false
			view.Controls.CanAcceptAndApply = false
			view.Controls.CanApplyPatch = false
			view.Controls.CanRetry = false
			view.Controls.CanSwitchAgentExecutionProfile = false
		}
	} else if !errors.Is(closureErr, storecontract.ErrNotFound) && !errors.Is(closureErr, app.ErrResultClosureBlocked) && !errors.Is(closureErr, app.ErrReviewEvidenceUnavailable) && !errors.Is(closureErr, app.ErrInvalidCommand) {
		return runView{}, closureErr
	}
	return view, nil
}

func safeTerminalReason(events []domain.RunEvent) string {
	allowed := map[string]struct{}{
		"runtime_output_limit_exceeded": {}, "runtime_stream_invalid": {},
		"checks_failed": {}, "checks_incomplete": {}, "attempt_timeout": {}, "runtime_exit_nonzero": {}, "result_contract_invalid": {}, "review_patch_materialization_failed": {},
		"context_consumption_invalid": {}, "runtime_policy_violation": {}, "agent_model_error": {},
	}
	result := ""
	for _, event := range events {
		if event.Type() == "run.started" || event.Type() == "attempt.started" {
			result = ""
		}
		if event.Type() == "attempt.start_failed" {
			result = "runtime_start_failed"
			continue
		}
		if event.Type() != "attempt.failed" && event.Type() != "attempt.reconciled_dead" {
			continue
		}
		var payload struct {
			FailureReason string `json:"failure_reason"`
		}
		if json.Unmarshal(event.NormalizedJSON(), &payload) != nil {
			continue
		}
		if _, ok := allowed[payload.FailureReason]; ok {
			result = payload.FailureReason
		}
	}
	return result
}

func verificationBindingsViewOf(bindings domain.VerificationBindings) verificationBindingsView {
	return verificationBindingsView{
		BaselineDigest: fmt.Sprintf("%x", bindings.BaselineDigest), PatchDigest: fmt.Sprintf("%x", bindings.PatchDigest),
		ContextSnapshotDigest: fmt.Sprintf("%x", bindings.ContextSnapshotDigest), AcceptanceContractDigest: fmt.Sprintf("%x", bindings.AcceptanceContractDigest),
		VerifierPolicyVersion: bindings.VerifierPolicyVersion, VerifierPolicyDigest: fmt.Sprintf("%x", bindings.VerifierPolicyDigest),
	}
}

func verificationViewOf(ctx context.Context, reader storecontract.Reader, run domain.VerificationRun) (verificationView, error) {
	view := verificationView{ID: run.ID().String(), AgentAttemptID: run.AgentAttemptID().String(), State: string(run.State()), Bindings: verificationBindingsViewOf(run.Bindings()), Attempts: []verificationAttemptView{}}
	attempts, err := reader.ListVerificationAttempts(ctx, run.ID())
	if err != nil {
		return verificationView{}, err
	}
	for _, attempt := range attempts {
		item, err := verificationAttemptViewOf(ctx, reader, attempt)
		if err != nil {
			return verificationView{}, err
		}
		view.Attempts = append(view.Attempts, item)
	}
	result, err := reader.GetVerificationResultForVerificationRun(ctx, run.ID())
	if err == nil {
		view.Result = &verificationResultView{ID: result.ID().String(), AttemptID: result.VerificationAttemptID().String(), Outcome: string(result.Outcome()), CreatedAt: result.CreatedAt().Format(time.RFC3339Nano)}
	} else if !errors.Is(err, storecontract.ErrNotFound) {
		return verificationView{}, err
	}
	return view, nil
}

func reviewablePatchViewOf(runID domain.RunID, change app.ReviewableChange) reviewablePatchView {
	view := reviewablePatchView{
		EvidenceKind: string(change.Binding.NormalizedEvidenceKind()), AgentReportID: change.Binding.AgentReportID.String(),
		PatchDigest: fmt.Sprintf("%x", change.Binding.PatchDigest), BaselineDigest: fmt.Sprintf("%x", change.Binding.BaselineDigest),
		DeclaredFilesDigest: fmt.Sprintf("%x", change.Binding.DeclaredFilesDigest), ResultID: change.Binding.ResultID.String(),
		AgentAttemptID: change.Binding.AgentAttemptID.String(), VerificationAttemptID: change.Binding.VerificationAttemptID.String(),
		ArtifactID: change.Binding.PatchArtifactID.String(), RawDownload: "/api/runs/" + runID.String() + "/patch", Files: []reviewablePatchFileView{},
	}
	view.Files = patchFilesView(change.Patch)
	return view
}

func patchFilesView(patch app.ReviewablePatch) []reviewablePatchFileView {
	files := []reviewablePatchFileView{}
	for _, file := range patch.Files {
		item := reviewablePatchFileView{Kind: string(file.Kind), Path: file.Path, Additions: file.Additions, Deletions: file.Deletions, Hunks: []reviewablePatchHunkView{}, Lines: []reviewablePatchLineView{}}
		for _, line := range file.Lines {
			item.Lines = append(item.Lines, reviewablePatchLineView{Kind: string(line.Kind), OldLine: line.OldLine, NewLine: line.NewLine, Text: line.Text})
		}
		for _, hunk := range file.Hunks {
			projected := reviewablePatchHunkView{Header: hunk.Header, OldStart: hunk.OldStart, OldCount: hunk.OldCount, NewStart: hunk.NewStart, NewCount: hunk.NewCount, Lines: []reviewablePatchLineView{}}
			for _, line := range hunk.Lines {
				projected.Lines = append(projected.Lines, reviewablePatchLineView{Kind: string(line.Kind), OldLine: line.OldLine, NewLine: line.NewLine, Text: line.Text})
			}
			item.Hunks = append(item.Hunks, projected)
		}
		files = append(files, item)
	}
	return files
}

func verifiedReviewViewOf(decision domain.VerifiedReviewDecision) verifiedReviewView {
	binding := decision.Binding()
	return verifiedReviewView{
		ID: decision.ID().String(), ResultID: binding.ResultID.String(), Kind: string(decision.Kind()), Reason: decision.Reason(),
		RejectionClass: string(decision.RejectionClass()), ActorID: decision.ActorID(), SessionID: decision.SessionID(),
		PatchDigest: fmt.Sprintf("%x", binding.PatchDigest), DecidedAt: decision.DecidedAt().Format(time.RFC3339Nano),
	}
}

func verificationAttemptViewOf(ctx context.Context, reader storecontract.Reader, record storecontract.VerificationAttemptRecord) (verificationAttemptView, error) {
	attempt := record.Attempt
	view := verificationAttemptView{ID: attempt.ID().String(), Sequence: attempt.Sequence(), State: string(attempt.State()), EvidenceComplete: record.EvidenceComplete,
		CleanupProven: record.CleanupProven, WorkspaceIdentity: record.WorkspaceIdentity, Reason: record.Reason, Commands: []verificationCommandView{}, Checks: []verificationCheckView{},
		CreatedAt: attempt.CreatedAt().Format(time.RFC3339Nano), UpdatedAt: record.UpdatedAt.Format(time.RFC3339Nano)}
	if predecessor, ok := attempt.Predecessor(); ok {
		view.PredecessorID = predecessor.String()
	}
	commands, err := reader.ListVerificationCommandEvidence(ctx, attempt.ID())
	if err != nil {
		return verificationAttemptView{}, err
	}
	for _, command := range commands {
		criteria := command.CriterionIDs()
		criterionIDs := make([]string, 0, len(criteria))
		for _, id := range criteria {
			criterionIDs = append(criterionIDs, id.String())
		}
		var exitCode *int
		if value, ok := command.ExitCode(); ok {
			exitCode = &value
		}
		view.Commands = append(view.Commands, verificationCommandView{ID: command.ID().String(), CommandID: command.CommandID(), CriterionIDs: criterionIDs,
			Argv: command.Argv(), Classification: string(command.Classification()), ExitCode: exitCode, Stdout: verificationLogViewOf(command.Stdout()),
			Stderr: verificationLogViewOf(command.Stderr()), VerifierIdentity: command.VerifierIdentity(), WorkspaceIdentity: command.WorkspaceIdentity(), StartedAt: command.StartedAt().Format(time.RFC3339Nano), EndedAt: command.EndedAt().Format(time.RFC3339Nano)})
	}
	checks, err := reader.ListVerificationAcceptanceChecks(ctx, attempt.ID())
	if err != nil {
		return verificationAttemptView{}, err
	}
	for _, check := range checks {
		evidence := check.EvidenceIDs()
		evidenceIDs := make([]string, 0, len(evidence))
		for _, id := range evidence {
			evidenceIDs = append(evidenceIDs, id.String())
		}
		view.Checks = append(view.Checks, verificationCheckView{ID: check.ID().String(), CriterionID: check.CriterionID().String(), Status: string(check.Status()), Trust: string(check.Trust()), EvidenceIDs: evidenceIDs})
	}
	return view, nil
}

func verificationLogViewOf(log domain.VerificationStreamLog) verificationLogView {
	return verificationLogView{FullSHA256: fmt.Sprintf("%x", log.FullSHA256), TotalBytes: log.TotalBytes, RetainedBody: log.RetainedBody,
		Truncated: log.Truncated, TruncationBoundary: log.TruncationBoundary, RedactionPolicyVersion: log.RedactionPolicyVersion}
}

func decisionGateViewOf(gate domain.ExecutionDecisionGate) decisionGateView {
	view := decisionGateView{
		ID: gate.ID().String(), Kind: "execution_decision", Status: gate.Status(), Question: gate.Question(), Context: gate.Context(),
		Options: []decisionOptionView{}, Recommendation: gate.Recommendation(), Impact: gate.Impact(), SelectedOptionID: gate.SelectedOptionID(),
		Note: gate.Note(), ActorID: gate.ActorID(), SessionID: gate.SessionID(), RequestedAt: gate.RequestedAt().Format(time.RFC3339Nano),
	}
	if !gate.ResolvedAt().IsZero() {
		view.ResolvedAt = gate.ResolvedAt().Format(time.RFC3339Nano)
	}
	for _, option := range gate.Options() {
		view.Options = append(view.Options, decisionOptionView{ID: option.ID, Label: option.Label, Impact: option.Impact})
	}
	return view
}

func eventCopy(eventType string) (string, string) {
	switch eventType {
	case "run.prepared":
		return "Run prepared", "The Room context and acceptance criteria were frozen in SQLite."
	case "run.started":
		return "Run started", "The managed Agent Run entered active execution."
	case "attempt.started":
		return "Agent started", "A managed Agent attempt entered the running state."
	case "attempt.start_failed":
		return "Agent start failed", "The managed runtime proved that no child execution started."
	case "attempt.reconciliation_required":
		return "Agent start uncertain", "The runtime identity requires recovery before Chora can proceed."
	case "automatic_retry.enabled":
		return "Automatic retry enabled", "Transient Agent failures may be retried automatically within the fixed retry budget."
	case "automatic_retry.reset":
		return "Automatic retry budget reset", "An explicit human retry started a new bounded automatic retry budget."
	case "automatic_retry.prepared":
		return "Automatic retry prepared", "Chora prepared the next immutable Agent attempt in the same Task worktree."
	case "automatic_retry.blocked":
		return "Automatic retry blocked", "Chora could not prove a safe automatic start and stopped for human recovery."
	case "thread.started":
		return "Agent session opened", "The adapter exposed an explicit Agent session."
	case "runtime.notice":
		return "Runtime notice recorded", "A normalized runtime event was persisted without exposing a transcript."
	case "run.stopping":
		return "Stopping Agent", "Chora requested process termination and execution-resource cleanup."
	case "run.stop_confirmed":
		return "Agent stopped", "Process termination and cleanup were confirmed before the Run was cancelled."
	case "run.recovery_required":
		return "Recovery required", "Chora could not prove a clean execution boundary and failed closed."
	case "run.awaiting_review":
		return "Awaiting review", "Artifact, Unknown, and Acceptance evidence are ready for the human decision."
	case "run.awaiting_verification":
		return "Awaiting verification", "The Agent stopped and emitted an immutable non-authoritative AgentReport; no Result exists yet."
	case "verification.started":
		return "Verification started", "Chora created a fresh Baseline-plus-Patch Verification Workspace and started the frozen argv."
	case "verification.retried":
		return "Verification retried", "A successor Verification Attempt started without rerunning the Agent."
	case "verification.cancelled":
		return "Verification cancelled", "Process death and cleanup were proven; no Result was created."
	case "verification.review_ready":
		return "Verification passed", "All Criterion-bound Checks passed and the Run stopped at awaiting_review."
	case "verification.needs_revision":
		return "Verification needs revision", "A trusted failed or unknown Check preserved the Patch without a green Result."
	case "verification.recovery_required":
		return "Verification recovery required", "Verification continuity or cleanup could not be proven; no Result was created."
	case "decision.requested":
		return "Decision requested", "The Agent paused at a bounded execution Decision Gate."
	case "decision.resolved":
		return "Decision recorded", "The persisted choice resumed Agent execution."
	case "review.accepted", "resource_review.accept":
		return "Run accepted", "Comment saved in Chora."
	case "review.rejected", "resource_review.reject":
		return "Revision requested", "Comment saved in Chora for the next repair."
	case "patch_application.started":
		return "Applying accepted Patch", "Chora persisted the exact Patch and target binding before writing the local working tree."
	case "patch_application.applied":
		return "Patch applied", "The exact accepted Patch is present in the local working tree and remains uncommitted."
	case "patch_application.conflict":
		return "Apply conflict", "The accepted Patch was preserved and the target working tree was not overwritten."
	case "patch_application.recovery_required":
		return "Apply recovery required", "Patch application continuity is uncertain and must be reconciled before completion."
	default:
		return eventType, "A structured Room event was persisted."
	}
}

func agentIdentity(adapterID string) (string, string) {
	if adapterID == "codex" {
		return "agent.codex", "Codex"
	}
	if adapterID == "fake" {
		return "agent.fake", "Fake Agent"
	}
	return "agent." + adapterID, adapterID
}

func currentActionCopy(phase domain.AgentPhase, timeline []timelineView) string {
	switch phase {
	case domain.AgentPhaseExecuting:
		if len(timeline) > 0 {
			return timeline[len(timeline)-1].Title
		}
		return "Executing the Task"
	case domain.AgentPhaseWaitingForDecision:
		return "Waiting for your bounded execution decision"
	case domain.AgentPhaseStopping:
		return "Stopping the Agent and cleaning execution resources"
	case domain.AgentPhaseReviewReady:
		return "Waiting for human review"
	case domain.AgentPhaseWaitingForRetry:
		return "Waiting for bounded retry instructions"
	case domain.AgentPhaseRecovery:
		return "Waiting for an explicit recovery decision"
	case domain.AgentPhaseCompleted:
		return "Run accepted"
	case domain.AgentPhaseCancelled:
		return "Run cancelled"
	default:
		return "Freezing Task and Context input"
	}
}

func runElapsedMS(run domain.AgentRun, now time.Time) int64 {
	start := run.StartedAt()
	if start.IsZero() {
		start = run.CreatedAt()
	}
	end := now
	if !run.TerminalAt().IsZero() {
		end = run.TerminalAt()
	} else if !run.ReviewRequestedAt().IsZero() &&
		(run.State() == domain.RunStateAwaitingReview || run.State() == domain.RunStateRevisionRequired || run.State() == domain.RunStateAccepted) {
		end = run.ReviewRequestedAt()
	}
	if end.Before(start) {
		return 0
	}
	return end.Sub(start).Milliseconds()
}

func fingerprintText(value [32]byte) string {
	if value == ([32]byte{}) {
		return "not-reported"
	}
	return fmt.Sprintf("%x", value)
}

func valueOrNotReported(value string) string {
	if strings.TrimSpace(value) == "" {
		return "not-reported"
	}
	return value
}
