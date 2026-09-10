package domain

import (
	"fmt"
	"strings"
	"time"
)

type ReviewRejectionClass string

type ReviewEvidenceKind string

const (
	ReviewEvidenceIndependent ReviewEvidenceKind = "independent_verification"
	ReviewEvidenceAgentReport ReviewEvidenceKind = "agent_reported"

	ReviewRejectionImplementationGap      ReviewRejectionClass = "implementation_gap"
	ReviewRejectionPlanningGap            ReviewRejectionClass = "planning_gap"
	ReviewRejectionContractChangeRequired ReviewRejectionClass = "contract_change_required"
)

type VerifiedReviewBinding struct {
	EvidenceKind          ReviewEvidenceKind
	ResultID              ResultID
	AgentAttemptID        AttemptID
	AgentReportID         AgentReportID
	VerificationAttemptID VerificationAttemptID
	PatchArtifactID       ArtifactID
	PatchDigest           [32]byte
	BaselineDigest        [32]byte
	DeclaredFilesDigest   [32]byte
}

func (binding VerifiedReviewBinding) valid() bool {
	base := binding.ResultID.Valid() && binding.AgentAttemptID.Valid() && binding.PatchArtifactID.Valid() &&
		binding.PatchDigest != ([32]byte{}) && binding.BaselineDigest != ([32]byte{}) && binding.DeclaredFilesDigest != ([32]byte{})
	switch binding.NormalizedEvidenceKind() {
	case ReviewEvidenceIndependent:
		return base && binding.VerificationAttemptID.Valid() && !binding.AgentReportID.Valid()
	case ReviewEvidenceAgentReport:
		return base && binding.AgentReportID.Valid() && !binding.VerificationAttemptID.Valid()
	default:
		return false
	}
}

// NormalizedEvidenceKind keeps pre-M2 persisted/test bindings compatible: an
// empty kind with a Verification Attempt is the historical independent path.
func (binding VerifiedReviewBinding) NormalizedEvidenceKind() ReviewEvidenceKind {
	if binding.EvidenceKind == "" && binding.VerificationAttemptID.Valid() {
		return ReviewEvidenceIndependent
	}
	return binding.EvidenceKind
}

type VerifiedReviewDecisionParams struct {
	ID                 ReviewDecisionID
	RunID              RunID
	ExpectedRunVersion uint64
	Kind               ReviewDecisionKind
	Reason             string
	RejectionClass     ReviewRejectionClass
	ActorID            string
	SessionID          string
	Binding            VerifiedReviewBinding
	DecidedAt          time.Time
}

type VerifiedReviewDecision struct {
	id                 ReviewDecisionID
	runID              RunID
	expectedRunVersion uint64
	kind               ReviewDecisionKind
	reason             string
	rejectionClass     ReviewRejectionClass
	actorID            string
	sessionID          string
	binding            VerifiedReviewBinding
	decidedAt          time.Time
}

func NewVerifiedReviewDecision(params VerifiedReviewDecisionParams, authorization HumanReviewAuthorization) (VerifiedReviewDecision, error) {
	if !authorization.humanIssued || !authorization.deviceOwnerPresent || authorization.runID != params.RunID || authorization.decisionKind != params.Kind || authorization.expectedRunVersion != params.ExpectedRunVersion {
		return VerifiedReviewDecision{}, ErrUnauthorizedReview
	}
	if !params.ID.Valid() || !params.RunID.Valid() || params.ExpectedRunVersion == 0 ||
		strings.TrimSpace(params.Reason) == "" || strings.TrimSpace(params.ActorID) == "" || strings.TrimSpace(params.SessionID) == "" ||
		!params.Binding.valid() || params.DecidedAt.IsZero() {
		return VerifiedReviewDecision{}, fmt.Errorf("%w: invalid verified review decision", ErrInvalidArgument)
	}
	switch params.Kind {
	case ReviewDecisionAccept:
		if params.RejectionClass != "" {
			return VerifiedReviewDecision{}, fmt.Errorf("%w: accepted review cannot classify a rejection", ErrInvalidArgument)
		}
	case ReviewDecisionReject:
		if params.RejectionClass != ReviewRejectionImplementationGap && params.RejectionClass != ReviewRejectionPlanningGap && params.RejectionClass != ReviewRejectionContractChangeRequired {
			return VerifiedReviewDecision{}, fmt.Errorf("%w: invalid review rejection class", ErrInvalidArgument)
		}
	default:
		return VerifiedReviewDecision{}, fmt.Errorf("%w: invalid verified review kind", ErrInvalidArgument)
	}
	return VerifiedReviewDecision{
		id: params.ID, runID: params.RunID, expectedRunVersion: params.ExpectedRunVersion, kind: params.Kind,
		reason: strings.TrimSpace(params.Reason), rejectionClass: params.RejectionClass,
		actorID: strings.TrimSpace(params.ActorID), sessionID: strings.TrimSpace(params.SessionID), binding: params.Binding, decidedAt: params.DecidedAt,
	}, nil
}

func RestoreVerifiedReviewDecision(params VerifiedReviewDecisionParams) (VerifiedReviewDecision, error) {
	authorization := HumanReviewAuthorization{
		humanIssued: true, deviceOwnerPresent: true, runID: params.RunID,
		decisionKind: params.Kind, expectedRunVersion: params.ExpectedRunVersion,
	}
	return NewVerifiedReviewDecision(params, authorization)
}

func (decision VerifiedReviewDecision) ID() ReviewDecisionID { return decision.id }
func (decision VerifiedReviewDecision) RunID() RunID         { return decision.runID }
func (decision VerifiedReviewDecision) ExpectedRunVersion() uint64 {
	return decision.expectedRunVersion
}
func (decision VerifiedReviewDecision) Kind() ReviewDecisionKind { return decision.kind }
func (decision VerifiedReviewDecision) Reason() string           { return decision.reason }
func (decision VerifiedReviewDecision) RejectionClass() ReviewRejectionClass {
	return decision.rejectionClass
}
func (decision VerifiedReviewDecision) ActorID() string                { return decision.actorID }
func (decision VerifiedReviewDecision) SessionID() string              { return decision.sessionID }
func (decision VerifiedReviewDecision) Binding() VerifiedReviewBinding { return decision.binding }
func (decision VerifiedReviewDecision) DecidedAt() time.Time           { return decision.decidedAt }
func (decision VerifiedReviewDecision) AllowsAgentRetry() bool {
	return decision.kind == ReviewDecisionReject && decision.rejectionClass == ReviewRejectionImplementationGap
}
