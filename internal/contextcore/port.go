package contextcore

import (
	"context"

	"github.com/Yangyang96/chora/internal/domain"
)

// ContextPort is the minimal persistence boundary for the embedded Context Core.
// Storage and transaction semantics are supplied by a later adapter.
type SnapshotPort interface {
	LookupRevisions(context.Context, domain.RoomID, []domain.ContextRevisionID) ([]domain.RoomContextRevision, error)
	SaveSnapshot(context.Context, Snapshot) error
}

type PromotionPort interface {
	PromoteReviewed(context.Context, PromoteReviewedRequest) (PromoteReviewedResult, error)
}

type ContextPort interface {
	SnapshotPort
	PromotionPort
}

// PromoteReviewedRequest is the transaction input for reviewed promotion.
// The Task 5 adapter must atomically:
//   - use RevisionID plus CommandDigest as the idempotency key;
//   - load an accepted persisted ReviewDecision for the same Run;
//   - load an accepted Run whose version equals ExpectedAcceptedRunVersion and
//     whose version is exactly review.expected_version + 1;
//   - verify Artifact, Event, and target Room ownership against that Run; and
//   - write the revision and complete PromotionRecord provenance.
//
// No successful result may be returned before the transaction is committed.
type PromoteReviewedRequest struct {
	Revision                   domain.RoomContextRevision
	ReviewDecisionID           domain.ReviewDecisionID
	RunID                      domain.RunID
	ExpectedAcceptedRunVersion uint64
	ArtifactID                 domain.ArtifactID
	EventID                    domain.EventID
	CommandDigest              [32]byte
	Authorization              AuthorizationReceipt
}

type PromotionOutcome string

const (
	PromotionOutcomeApplied             PromotionOutcome = "applied"
	PromotionOutcomeAlreadyApplied      PromotionOutcome = "already_applied"
	PromotionOutcomeRejected            PromotionOutcome = "rejected"
	PromotionOutcomeRetryableNotApplied PromotionOutcome = "retryable_not_applied"
	PromotionOutcomeCommitUnknown       PromotionOutcome = "commit_unknown"
)

type PromotionRejection string

const (
	PromotionRejectionReviewNotAccepted   PromotionRejection = "review_not_accepted"
	PromotionRejectionRunNotAccepted      PromotionRejection = "run_not_accepted"
	PromotionRejectionProvenanceMismatch  PromotionRejection = "provenance_mismatch"
	PromotionRejectionRoomMismatch        PromotionRejection = "room_mismatch"
	PromotionRejectionVersionMismatch     PromotionRejection = "version_mismatch"
	PromotionRejectionIdempotencyConflict PromotionRejection = "idempotency_conflict"
)

// PromoteReviewedResult and error have these legal combinations:
//   - applied/already_applied: valid Record and nil error;
//   - rejected: a non-empty Rejection and nil error;
//   - retryable_not_applied: no Record and a non-nil error proving rollback;
//   - commit_unknown: no trusted Record and an optional diagnostic error.
//
// Any other combination is malformed and is treated as commit_unknown.
type PromoteReviewedResult struct {
	Outcome   PromotionOutcome
	Record    PromotionRecord
	Rejection PromotionRejection
}
