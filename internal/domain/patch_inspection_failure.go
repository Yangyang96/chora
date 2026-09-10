package domain

import (
	"strings"
	"time"
)

type PatchInspectionFailureParams struct {
	RunID, ReviewDecisionID          string
	PatchDigest, DeclaredFilesDigest [32]byte
	TargetIdentity                   string
	AffectedPaths                    []string
	State                            PatchApplicationState
	Version                          uint64
	Reason                           string
	StartedAt, UpdatedAt             time.Time
}

type PatchInspectionFailure struct{ params PatchInspectionFailureParams }

func NewPatchInspectionFailure(params PatchInspectionFailureParams) (PatchInspectionFailure, error) {
	params.Version = 1
	return restorePatchInspectionFailure(params)
}

func RestorePatchInspectionFailure(params PatchInspectionFailureParams) (PatchInspectionFailure, error) {
	return restorePatchInspectionFailure(params)
}

func restorePatchInspectionFailure(params PatchInspectionFailureParams) (PatchInspectionFailure, error) {
	if _, err := ParseRunID(params.RunID); err != nil {
		return PatchInspectionFailure{}, ErrInvalidArgument
	}
	if _, err := ParseReviewDecisionID(params.ReviewDecisionID); err != nil {
		return PatchInspectionFailure{}, ErrInvalidArgument
	}
	if params.PatchDigest == ([32]byte{}) || params.DeclaredFilesDigest == ([32]byte{}) ||
		strings.TrimSpace(params.TargetIdentity) == "" || len(params.AffectedPaths) == 0 || !validApplicationPaths(params.AffectedPaths) ||
		(params.State != PatchApplicationConflict && params.State != PatchApplicationRecoveryRequired) || params.Version == 0 ||
		strings.TrimSpace(params.Reason) == "" || params.StartedAt.IsZero() || params.UpdatedAt.Before(params.StartedAt) {
		return PatchInspectionFailure{}, ErrInvalidArgument
	}
	params.AffectedPaths = append([]string(nil), params.AffectedPaths...)
	params.Reason = strings.TrimSpace(params.Reason)
	return PatchInspectionFailure{params: params}, nil
}

func (failure PatchInspectionFailure) Repeat(state PatchApplicationState, reason string, at time.Time) (PatchInspectionFailure, error) {
	params := failure.params
	params.State, params.Version, params.Reason, params.UpdatedAt = state, params.Version+1, reason, at
	return restorePatchInspectionFailure(params)
}

func (failure PatchInspectionFailure) RunID() RunID {
	id, _ := ParseRunID(failure.params.RunID)
	return id
}
func (failure PatchInspectionFailure) ReviewDecisionID() ReviewDecisionID {
	id, _ := ParseReviewDecisionID(failure.params.ReviewDecisionID)
	return id
}
func (failure PatchInspectionFailure) PatchDigest() [32]byte { return failure.params.PatchDigest }
func (failure PatchInspectionFailure) DeclaredFilesDigest() [32]byte {
	return failure.params.DeclaredFilesDigest
}
func (failure PatchInspectionFailure) TargetIdentity() string { return failure.params.TargetIdentity }
func (failure PatchInspectionFailure) AffectedPaths() []string {
	return append([]string(nil), failure.params.AffectedPaths...)
}
func (failure PatchInspectionFailure) State() PatchApplicationState { return failure.params.State }
func (failure PatchInspectionFailure) Version() uint64              { return failure.params.Version }
func (failure PatchInspectionFailure) Reason() string               { return failure.params.Reason }
func (failure PatchInspectionFailure) StartedAt() time.Time         { return failure.params.StartedAt }
func (failure PatchInspectionFailure) UpdatedAt() time.Time         { return failure.params.UpdatedAt }
