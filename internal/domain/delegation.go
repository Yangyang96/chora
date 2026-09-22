package domain

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Delegation is a bounded authorization to execute a fixed, one-level plan.
// Assignments describe work; they never grant additional resources or delivery.
const MaxDelegationAssignments = 4

type DelegationAssignment struct {
	Role        string `json:"role"`
	Title       string `json:"title"`
	Requirement string `json:"requirement"`
}

type DelegationState string

const (
	DelegationRunning        DelegationState = "running"
	DelegationStopping       DelegationState = "stopping"
	DelegationStopped        DelegationState = "stopped"
	DelegationBlocked        DelegationState = "blocked"
	DelegationAwaitingReview DelegationState = "awaiting_review"
)

type TaskDelegation struct {
	ParentTaskID               TaskID
	Version                    uint64
	State                      DelegationState
	Assignments                []DelegationAssignment
	ActorID, SessionID, Reason string
	CreatedAt, UpdatedAt       time.Time
}

func (d TaskDelegation) Validate() error {
	bad := func() error { return fmt.Errorf("%w: invalid task delegation", ErrInvalidArgument) }
	if !d.ParentTaskID.Valid() || d.Version == 0 || d.CreatedAt.IsZero() || d.UpdatedAt.Before(d.CreatedAt) ||
		!ValidTrustedContextText(d.ActorID, MaxTrustedContextIdentityRunes, true) || !ValidTrustedContextText(d.SessionID, MaxTrustedContextIdentityRunes, true) ||
		!utf8.ValidString(d.Reason) || len(d.Reason) > 4000 || strings.ContainsRune(d.Reason, '\x00') || len(d.Assignments) < 1 || len(d.Assignments) > MaxDelegationAssignments {
		return bad()
	}
	switch d.State {
	case DelegationRunning, DelegationStopping, DelegationStopped, DelegationBlocked, DelegationAwaitingReview:
	default:
		return bad()
	}
	seen := map[string]bool{}
	for _, a := range d.Assignments {
		if !ValidTrustedContextText(a.Role, 80, true) || !ValidTrustedContextText(a.Title, 256, true) || !ValidTrustedContextText(a.Requirement, 4000, true) || seen[strings.ToLower(strings.TrimSpace(a.Role))] {
			return bad()
		}
		seen[strings.ToLower(strings.TrimSpace(a.Role))] = true
	}
	return nil
}

func (d TaskDelegation) Transition(next DelegationState, reason string, at time.Time) (TaskDelegation, error) {
	allowed := false
	switch d.State {
	case DelegationRunning:
		allowed = next == DelegationStopping || next == DelegationBlocked || next == DelegationAwaitingReview
	case DelegationBlocked:
		allowed = next == DelegationRunning || next == DelegationStopping
	case DelegationStopping:
		allowed = next == DelegationStopped || next == DelegationStopping
	}
	if !allowed || at.Before(d.UpdatedAt) {
		return TaskDelegation{}, fmt.Errorf("%w: invalid delegation transition", ErrInvalidArgument)
	}
	d.Version++
	d.State = next
	d.Reason = reason
	d.UpdatedAt = at
	return d, d.Validate()
}

type DelegationChild struct {
	RunID        RunID
	ParentTaskID TaskID
	Position     int
	TaskID       TaskID
	CreatedAt    time.Time
}
