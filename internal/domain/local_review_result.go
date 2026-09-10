package domain

import (
	"fmt"
	"time"
)

// LocalReviewResult is the canonical, explicitly non-independent result for a
// Local Connected Attempt. Its evidence authority is the AgentReport plus the
// immutable Patch copied from the Task worktree.
type LocalReviewResultParams struct {
	ID                       ResultID
	RunID                    RunID
	AgentAttemptID           AttemptID
	AgentReportID            AgentReportID
	PatchArtifactID          ArtifactID
	PatchDigest              [32]byte
	BaselineDigest           [32]byte
	ContextSnapshotDigest    [32]byte
	AcceptanceContractDigest [32]byte
	Outcome                  ResultOutcome
	CreatedAt                time.Time
}

type LocalReviewResult struct{ params LocalReviewResultParams }

func NewLocalReviewResult(params LocalReviewResultParams) (LocalReviewResult, error) {
	if !params.ID.Valid() || !params.RunID.Valid() || !params.AgentAttemptID.Valid() || !params.AgentReportID.Valid() ||
		!params.PatchArtifactID.Valid() || params.PatchDigest == ([32]byte{}) || params.BaselineDigest == ([32]byte{}) ||
		params.ContextSnapshotDigest == ([32]byte{}) || params.AcceptanceContractDigest == ([32]byte{}) ||
		params.Outcome != ResultReviewReady || params.CreatedAt.IsZero() {
		return LocalReviewResult{}, fmt.Errorf("%w: invalid Local Connected review Result", ErrInvalidArgument)
	}
	return LocalReviewResult{params: params}, nil
}

func (result LocalReviewResult) ID() ResultID                 { return result.params.ID }
func (result LocalReviewResult) RunID() RunID                 { return result.params.RunID }
func (result LocalReviewResult) AgentAttemptID() AttemptID    { return result.params.AgentAttemptID }
func (result LocalReviewResult) AgentReportID() AgentReportID { return result.params.AgentReportID }
func (result LocalReviewResult) PatchArtifactID() ArtifactID  { return result.params.PatchArtifactID }
func (result LocalReviewResult) PatchDigest() [32]byte        { return result.params.PatchDigest }
func (result LocalReviewResult) BaselineDigest() [32]byte     { return result.params.BaselineDigest }
func (result LocalReviewResult) ContextSnapshotDigest() [32]byte {
	return result.params.ContextSnapshotDigest
}
func (result LocalReviewResult) AcceptanceContractDigest() [32]byte {
	return result.params.AcceptanceContractDigest
}
func (result LocalReviewResult) Outcome() ResultOutcome { return result.params.Outcome }
func (result LocalReviewResult) CreatedAt() time.Time   { return result.params.CreatedAt }
