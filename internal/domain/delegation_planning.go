package domain

import (
	"fmt"
	"time"
)

const DelegationPlanningMarker = "[Chora research delegation planning v1]"

type DelegationPlanningState string

const (
	DelegationPlanningRunning  DelegationPlanningState = "planning"
	DelegationPlanningBlocked  DelegationPlanningState = "blocked"
	DelegationPlanningStopping DelegationPlanningState = "stopping"
	DelegationPlanningStopped  DelegationPlanningState = "stopped"
	DelegationPlanningImported DelegationPlanningState = "imported"
)

// DelegationPlanningIntent authorizes one planning Attempt and the fixed plan it
// produces. It never authorizes another planning round or human acceptance.
type DelegationPlanningIntent struct {
	ParentTaskID               TaskID
	RunID                      RunID
	AttemptID                  AttemptID
	Version                    uint64
	State                      DelegationPlanningState
	AuthorityDigest            [32]byte
	ActorID, SessionID, Reason string
	CreatedAt, UpdatedAt       time.Time
}

func (p DelegationPlanningIntent) Validate() error {
	if !p.ParentTaskID.Valid() || !p.RunID.Valid() || p.Version == 0 || p.AuthorityDigest == ([32]byte{}) || p.CreatedAt.IsZero() || p.UpdatedAt.Before(p.CreatedAt) || !ValidTrustedContextText(p.ActorID, MaxTrustedContextIdentityRunes, true) || !ValidTrustedContextText(p.SessionID, MaxTrustedContextIdentityRunes, true) || !ValidTrustedContextText(p.Reason, 4000, false) {
		return fmt.Errorf("%w: invalid planning authorization", ErrInvalidArgument)
	}
	switch p.State {
	case DelegationPlanningRunning, DelegationPlanningBlocked, DelegationPlanningStopping, DelegationPlanningStopped:
	case DelegationPlanningImported:
		if !p.AttemptID.Valid() {
			return ErrInvalidArgument
		}
	default:
		return ErrInvalidArgument
	}
	return nil
}
func (p DelegationPlanningIntent) Transition(state DelegationPlanningState, reason string, now time.Time) (DelegationPlanningIntent, error) {
	allowed := p.State == DelegationPlanningRunning && (state == DelegationPlanningBlocked || state == DelegationPlanningStopping || state == DelegationPlanningImported) || p.State == DelegationPlanningBlocked && (state == DelegationPlanningRunning || state == DelegationPlanningStopping) || p.State == DelegationPlanningStopping && (state == DelegationPlanningStopped || state == DelegationPlanningStopping)
	if !allowed || now.Before(p.UpdatedAt) {
		return p, fmt.Errorf("%w: invalid planning transition", ErrInvalidArgument)
	}
	p.Version++
	p.State = state
	p.Reason = reason
	p.UpdatedAt = now
	return p, p.Validate()
}
func (p DelegationPlanningIntent) BindAttempt(id AttemptID, now time.Time) (DelegationPlanningIntent, error) {
	if p.State != DelegationPlanningRunning || p.AttemptID.Valid() || !id.Valid() || now.Before(p.UpdatedAt) {
		return p, ErrInvalidArgument
	}
	p.Version++
	p.AttemptID = id
	p.UpdatedAt = now
	return p, p.Validate()
}
