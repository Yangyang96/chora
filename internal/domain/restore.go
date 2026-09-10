package domain

import (
	"fmt"
	"strings"
	"time"
)

type TaskRecord struct {
	ID                TaskID
	RoomID            RoomID
	PredecessorTaskID TaskID
	Title             string
	Goal              string
	Criteria          []AcceptanceCriterion
	State             TaskState
	Archived          bool
	ArchivedAt        time.Time
}

func RestoreTask(record TaskRecord) (Task, error) {
	task, err := NewTask(record.ID, record.RoomID, record.Title, record.Goal, record.Criteria)
	if err != nil {
		return Task{}, err
	}
	if record.PredecessorTaskID.Valid() {
		if record.PredecessorTaskID == record.ID {
			return Task{}, fmt.Errorf("%w: task cannot be its own predecessor", ErrInvalidArgument)
		}
		task.predecessorTaskID = record.PredecessorTaskID
	}
	switch record.State {
	case TaskStateOpen:
	case TaskStateClosed:
		task = task.Close()
	default:
		return Task{}, fmt.Errorf("%w: invalid task state", ErrInvalidArgument)
	}
	if record.Archived {
		if record.ArchivedAt.IsZero() {
			return Task{}, fmt.Errorf("%w: archived task lacks timestamp", ErrInvalidArgument)
		}
		task.archived = true
		task.archivedAt = record.ArchivedAt
	} else if !record.ArchivedAt.IsZero() {
		return Task{}, fmt.Errorf("%w: active task has archived timestamp", ErrInvalidArgument)
	}
	return task, nil
}

type AgentRunRecord struct {
	ID                   RunID
	TaskID               TaskID
	CharterID            CharterID
	State                RunState
	Version              uint64
	CurrentAttemptNumber int
	CreatedAt            time.Time
	UpdatedAt            time.Time
	StartedAt            time.Time
	ReviewRequestedAt    time.Time
	TerminalAt           time.Time
}

func RestoreAgentRun(record AgentRunRecord) (AgentRun, error) {
	if !record.ID.Valid() || !record.TaskID.Valid() || !record.CharterID.Valid() || !validRunState(record.State) || record.CurrentAttemptNumber < 0 || !validTimestamps(record.CreatedAt, record.UpdatedAt) || !validOptionalTimestamp(record.StartedAt, record.CreatedAt, record.UpdatedAt) || !validOptionalTimestamp(record.ReviewRequestedAt, record.CreatedAt, record.UpdatedAt) || !validOptionalTimestamp(record.TerminalAt, record.CreatedAt, record.UpdatedAt) {
		return AgentRun{}, fmt.Errorf("%w: invalid persisted agent run", ErrInvalidArgument)
	}
	return AgentRun{id: record.ID, taskID: record.TaskID, charterID: record.CharterID, state: record.State, version: record.Version, currentAttemptNumber: record.CurrentAttemptNumber, createdAt: record.CreatedAt, updatedAt: record.UpdatedAt, startedAt: record.StartedAt, reviewRequestedAt: record.ReviewRequestedAt, terminalAt: record.TerminalAt}, nil
}

func validRunState(state RunState) bool {
	switch state {
	case RunStateDraft, RunStateReady, RunStateRunning, RunStateStopping, RunStateAwaitingVerification, RunStateVerifying, RunStateAwaitingReview, RunStateRevisionRequired, RunStateRecoveryRequired, RunStateVerificationRecoveryRequired, RunStateAccepted, RunStateCancelled, RunStateCompleted:
		return true
	default:
		return false
	}
}

func validOptionalTimestamp(value, lower, upper time.Time) bool {
	return value.IsZero() || (!value.Before(lower) && !value.After(upper))
}

type AttemptRecord struct {
	ID                           AttemptID
	RunID                        RunID
	Sequence                     int
	Predecessor                  *AttemptID
	ContextSnapshotID            ContextSnapshotID
	ContextDigest                [32]byte
	AdapterID                    string
	AgentExecutionProfileBinding AgentExecutionProfileBinding
	ExternalSession              string
	RetryReason                  string
	InterventionReason           string
	ContextDelta                 string
	State                        AttemptState
	CreatedAt                    time.Time
}

func RestoreAttempt(record AttemptRecord) (Attempt, error) {
	attempt, err := NewAttempt(AttemptParams{ID: record.ID, RunID: record.RunID, Sequence: record.Sequence, Predecessor: record.Predecessor, ContextSnapshotID: record.ContextSnapshotID, ContextDigest: record.ContextDigest, AdapterID: record.AdapterID, AgentExecutionProfileBinding: record.AgentExecutionProfileBinding, ExternalSession: record.ExternalSession, RetryReason: record.RetryReason, InterventionReason: record.InterventionReason, ContextDelta: record.ContextDelta, CreatedAt: record.CreatedAt})
	if err != nil {
		return Attempt{}, err
	}
	if !validAttemptState(record.State) {
		return Attempt{}, fmt.Errorf("%w: invalid persisted attempt state", ErrInvalidArgument)
	}
	attempt.state = record.State
	return attempt, nil
}

func validAttemptState(state AttemptState) bool {
	switch state {
	case AttemptStateCreated, AttemptStateStarting, AttemptStateRunning, AttemptStateOutputSubmitted, AttemptStateFailed, AttemptStateInterrupted, AttemptStateCancelled:
		return true
	default:
		return false
	}
}

type ReviewDecisionRecord struct {
	ID                 ReviewDecisionID
	RunID              RunID
	Kind               ReviewDecisionKind
	ExpectedRunVersion uint64
	Comment            string
	Checks             []ReviewedCheck
	LinkedArtifacts    []ArtifactLink
	DecidedAt          time.Time
}

func RestoreReviewDecision(record ReviewDecisionRecord) (ReviewDecision, error) {
	if record.Kind != ReviewDecisionAccept && record.Kind != ReviewDecisionReject {
		return ReviewDecision{}, fmt.Errorf("%w: invalid persisted review kind", ErrInvalidArgument)
	}
	if record.Kind == ReviewDecisionReject && strings.TrimSpace(record.Comment) == "" {
		return ReviewDecision{}, fmt.Errorf("%w: rejection requires comment", ErrInvalidArgument)
	}
	return newReviewDecision(ReviewDecisionParams{ID: record.ID, RunID: record.RunID, ExpectedRunVersion: record.ExpectedRunVersion, Comment: record.Comment, Checks: record.Checks, LinkedArtifacts: record.LinkedArtifacts, DecidedAt: record.DecidedAt}, record.Kind)
}
