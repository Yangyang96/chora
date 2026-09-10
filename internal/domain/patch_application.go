package domain

import (
	"strings"
	"time"
)

type PatchApplicationState string

const (
	PatchApplicationApplying         PatchApplicationState = "applying"
	PatchApplicationApplied          PatchApplicationState = "applied"
	PatchApplicationConflict         PatchApplicationState = "conflict"
	PatchApplicationRecoveryRequired PatchApplicationState = "recovery_required"
)

type PatchApplicationParams struct {
	RunID, ReviewDecisionID          string
	PatchDigest, DeclaredFilesDigest [32]byte
	TargetIdentity, BaseRevision     string
	AffectedPaths                    []string
	PreStateDigest, PostStateDigest  [32]byte
	State                            PatchApplicationState
	Version                          uint64
	Reason                           string
	StartedAt, UpdatedAt, AppliedAt  time.Time
}

type PatchApplication struct{ params PatchApplicationParams }

func NewPatchApplication(params PatchApplicationParams) (PatchApplication, error) {
	params.State = PatchApplicationApplying
	params.Version = 1
	params.PostStateDigest = [32]byte{}
	params.Reason = ""
	params.AppliedAt = time.Time{}
	return restorePatchApplication(params)
}

func RestorePatchApplication(params PatchApplicationParams) (PatchApplication, error) {
	return restorePatchApplication(params)
}

func restorePatchApplication(params PatchApplicationParams) (PatchApplication, error) {
	if _, err := ParseRunID(params.RunID); err != nil {
		return PatchApplication{}, ErrInvalidArgument
	}
	if _, err := ParseReviewDecisionID(params.ReviewDecisionID); err != nil {
		return PatchApplication{}, ErrInvalidArgument
	}
	if params.PatchDigest == ([32]byte{}) || params.DeclaredFilesDigest == ([32]byte{}) ||
		params.PreStateDigest == ([32]byte{}) || strings.TrimSpace(params.TargetIdentity) == "" ||
		strings.TrimSpace(params.BaseRevision) == "" || params.Version == 0 || params.StartedAt.IsZero() ||
		params.UpdatedAt.Before(params.StartedAt) || len(params.AffectedPaths) == 0 || !validApplicationPaths(params.AffectedPaths) {
		return PatchApplication{}, ErrInvalidArgument
	}
	switch params.State {
	case PatchApplicationApplying:
		if params.PostStateDigest != ([32]byte{}) || strings.TrimSpace(params.Reason) != "" || !params.AppliedAt.IsZero() {
			return PatchApplication{}, ErrInvalidArgument
		}
	case PatchApplicationApplied:
		if params.PostStateDigest == ([32]byte{}) || strings.TrimSpace(params.Reason) != "" || params.AppliedAt.IsZero() || params.AppliedAt.Before(params.StartedAt) || !params.UpdatedAt.Equal(params.AppliedAt) {
			return PatchApplication{}, ErrInvalidArgument
		}
	case PatchApplicationConflict, PatchApplicationRecoveryRequired:
		if params.PostStateDigest != ([32]byte{}) || strings.TrimSpace(params.Reason) == "" || !params.AppliedAt.IsZero() {
			return PatchApplication{}, ErrInvalidArgument
		}
	default:
		return PatchApplication{}, ErrInvalidArgument
	}
	params.AffectedPaths = append([]string(nil), params.AffectedPaths...)
	return PatchApplication{params: params}, nil
}

func validApplicationPaths(paths []string) bool {
	seen := make(map[string]struct{}, len(paths))
	for _, value := range paths {
		if strings.TrimSpace(value) == "" || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") || strings.Contains(value, "\x00") {
			return false
		}
		for _, part := range strings.Split(value, "/") {
			if part == "" || part == "." || part == ".." {
				return false
			}
		}
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func (application PatchApplication) Retry(at time.Time) (PatchApplication, error) {
	if application.params.State != PatchApplicationConflict && application.params.State != PatchApplicationRecoveryRequired {
		return PatchApplication{}, ErrInvalidRunTransition
	}
	params := application.params
	params.State, params.Version, params.Reason = PatchApplicationApplying, params.Version+1, ""
	params.StartedAt, params.UpdatedAt, params.AppliedAt, params.PostStateDigest = at, at, time.Time{}, [32]byte{}
	return restorePatchApplication(params)
}

func (application PatchApplication) Applied(postStateDigest [32]byte, at time.Time) (PatchApplication, error) {
	if application.params.State != PatchApplicationApplying || postStateDigest == ([32]byte{}) || at.Before(application.params.StartedAt) {
		return PatchApplication{}, ErrInvalidRunTransition
	}
	params := application.params
	params.State, params.Version, params.PostStateDigest = PatchApplicationApplied, params.Version+1, postStateDigest
	params.UpdatedAt, params.AppliedAt = at, at
	return restorePatchApplication(params)
}

func (application PatchApplication) Failed(state PatchApplicationState, reason string, at time.Time) (PatchApplication, error) {
	if application.params.State != PatchApplicationApplying || (state != PatchApplicationConflict && state != PatchApplicationRecoveryRequired) || strings.TrimSpace(reason) == "" || at.Before(application.params.StartedAt) {
		return PatchApplication{}, ErrInvalidRunTransition
	}
	params := application.params
	params.State, params.Version, params.Reason = state, params.Version+1, strings.TrimSpace(reason)
	params.UpdatedAt = at
	return restorePatchApplication(params)
}

func (application PatchApplication) RunID() RunID {
	id, _ := ParseRunID(application.params.RunID)
	return id
}
func (application PatchApplication) ReviewDecisionID() ReviewDecisionID {
	id, _ := ParseReviewDecisionID(application.params.ReviewDecisionID)
	return id
}
func (application PatchApplication) PatchDigest() [32]byte { return application.params.PatchDigest }
func (application PatchApplication) DeclaredFilesDigest() [32]byte {
	return application.params.DeclaredFilesDigest
}
func (application PatchApplication) TargetIdentity() string { return application.params.TargetIdentity }
func (application PatchApplication) BaseRevision() string   { return application.params.BaseRevision }
func (application PatchApplication) AffectedPaths() []string {
	return append([]string(nil), application.params.AffectedPaths...)
}
func (application PatchApplication) PreStateDigest() [32]byte {
	return application.params.PreStateDigest
}
func (application PatchApplication) PostStateDigest() [32]byte {
	return application.params.PostStateDigest
}
func (application PatchApplication) State() PatchApplicationState { return application.params.State }
func (application PatchApplication) Version() uint64              { return application.params.Version }
func (application PatchApplication) Reason() string               { return application.params.Reason }
func (application PatchApplication) StartedAt() time.Time         { return application.params.StartedAt }
func (application PatchApplication) UpdatedAt() time.Time         { return application.params.UpdatedAt }
func (application PatchApplication) AppliedAt() time.Time         { return application.params.AppliedAt }
