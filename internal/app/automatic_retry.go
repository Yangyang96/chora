package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

const AutomaticRetryMaxRetries = 2

const (
	automaticRetryEnabledEvent  = "automatic_retry.enabled"
	automaticRetryResetEvent    = "automatic_retry.reset"
	automaticRetryPreparedEvent = "automatic_retry.prepared"
	automaticRetryBlockedEvent  = "automatic_retry.blocked"
)

type AutomaticRetryState string

const (
	AutomaticRetryPending   AutomaticRetryState = "pending"
	AutomaticRetryRetrying  AutomaticRetryState = "retrying"
	AutomaticRetryExhausted AutomaticRetryState = "exhausted"
	AutomaticRetryBlocked   AutomaticRetryState = "blocked"
)

// AutomaticRetry describes the durable continuation state for one Run. A nil
// value means that there is no active automatic continuation to present.
type AutomaticRetry struct {
	State             AutomaticRetryState
	RetriesUsed       int
	MaxRetries        int
	LastFailureReason string
	BlockedReason     string
}

type automaticRetryEvent struct {
	Type          string `json:"type"`
	RunID         string `json:"run_id"`
	AttemptID     string `json:"attempt_id,omitempty"`
	PredecessorID string `json:"predecessor_attempt_id,omitempty"`
	FailureReason string `json:"failure_reason,omitempty"`
	RetriesUsed   int    `json:"retries_used"`
	MaxRetries    int    `json:"max_retries"`
	BlockedReason string `json:"blocked_reason,omitempty"`
}

// AutomaticRetryForRun projects retry state only from persisted Run, Attempt,
// session, and event evidence. This makes it safe to call after a restart.
func (s *Service) AutomaticRetryForRun(ctx context.Context, runID domain.RunID) (*AutomaticRetry, error) {
	if s == nil || s.deps.Store == nil {
		return nil, fmt.Errorf("%w: missing store", ErrInvalidCommand)
	}
	return automaticRetryForRun(ctx, s.deps.Store.Reader(), runID)
}

func automaticRetryForRun(ctx context.Context, reader storecontract.Reader, runID domain.RunID) (*AutomaticRetry, error) {
	closures, err := reader.ListResultClosuresForRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if len(closures) > 0 {
		return nil, nil
	}
	run, err := reader.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	events, err := reader.ListRunEvents(ctx, runID)
	if err != nil {
		return nil, err
	}
	state, err := automaticRetryEventState(events)
	if err != nil || !state.enabled {
		return nil, err
	}
	if run.State() == domain.RunStateAccepted || run.State() == domain.RunStateCompleted || run.State() == domain.RunStateCancelled ||
		run.State() == domain.RunStateAwaitingVerification || run.State() == domain.RunStateVerifying || run.State() == domain.RunStateAwaitingReview || run.State() == domain.RunStateRevisionRequired || run.State() == domain.RunStateVerificationRecoveryRequired {
		return nil, nil
	}
	attempt, err := reader.GetCurrentAttempt(ctx, runID)
	if err != nil {
		if errors.Is(err, storecontract.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	exactFailureReason := automaticRetryFailureForAttempt(events, attempt.ID())
	result := &AutomaticRetry{RetriesUsed: state.retriesUsed, MaxRetries: AutomaticRetryMaxRetries, LastFailureReason: exactFailureReason}
	if state.blockedAttemptID == attempt.ID().String() {
		result.State = AutomaticRetryBlocked
		result.BlockedReason = state.blockedReason
		if result.LastFailureReason == "" {
			result.LastFailureReason = state.lastFailureReason
		}
		return result, nil
	}
	currentAutomatic := state.preparedAttemptID == attempt.ID().String()
	if currentAutomatic && (run.State() == domain.RunStateReady || run.State() == domain.RunStateRunning || run.State() == domain.RunStateStopping) {
		result.State = AutomaticRetryRetrying
		if result.LastFailureReason == "" {
			result.LastFailureReason = state.lastFailureReason
		}
		return result, nil
	}
	if run.State() != domain.RunStateRecoveryRequired {
		return nil, nil
	}
	if result.LastFailureReason == "" {
		result.LastFailureReason = state.lastFailureReason
	}
	// Startup reconciliation proves that an interrupted process is stopped,
	// not that its work is safe to repeat in a fresh session. Keep explicit
	// recovery available instead of letting the dispatcher consume it.
	if attempt.State() == domain.AttemptStateInterrupted {
		result.State = AutomaticRetryBlocked
		return result, nil
	}
	session, sessionErr := reader.GetRuntimeSessionForAttempt(ctx, attempt.ID())
	if sessionErr != nil && !errors.Is(sessionErr, storecontract.ErrNotFound) {
		return nil, sessionErr
	}
	if exactFailureReason == "" || sessionErr != nil || !automaticRetryFinalized(attempt, session) || !automaticRetryableFailure(exactFailureReason) {
		result.State = AutomaticRetryBlocked
		return result, nil
	}
	if result.RetriesUsed >= AutomaticRetryMaxRetries {
		result.State = AutomaticRetryExhausted
		return result, nil
	}
	result.State = AutomaticRetryPending
	return result, nil
}

type automaticRetryProjection struct {
	enabled           bool
	retriesUsed       int
	preparedAttemptID string
	blockedAttemptID  string
	blockedReason     string
	lastFailureReason string
}

func automaticRetryEventState(events []domain.RunEvent) (automaticRetryProjection, error) {
	var result automaticRetryProjection
	for _, event := range events {
		switch event.Type() {
		case "run.started":
			// A human may explicitly start an automatically prepared Attempt
			// after the dispatcher was blocked. The durable start supersedes
			// that block while preserving the consumed retry count.
			result.blockedAttemptID = ""
			result.blockedReason = ""
		case automaticRetryEnabledEvent:
			result.enabled = true
		case automaticRetryResetEvent:
			result.enabled = true
			result.retriesUsed = 0
			result.preparedAttemptID = ""
			result.blockedAttemptID = ""
			result.lastFailureReason = ""
		case automaticRetryPreparedEvent:
			var payload automaticRetryEvent
			if err := json.Unmarshal(event.NormalizedJSON(), &payload); err != nil || payload.AttemptID == "" || payload.PredecessorID == "" || payload.RetriesUsed < 1 || payload.RetriesUsed > AutomaticRetryMaxRetries || payload.MaxRetries != AutomaticRetryMaxRetries {
				return automaticRetryProjection{}, fmt.Errorf("%w: malformed automatic retry preparation", ErrInvalidCommand)
			}
			result.enabled = true
			result.retriesUsed = payload.RetriesUsed
			result.preparedAttemptID = payload.AttemptID
			result.blockedAttemptID = ""
			result.lastFailureReason = payload.FailureReason
		case automaticRetryBlockedEvent:
			var payload automaticRetryEvent
			if err := json.Unmarshal(event.NormalizedJSON(), &payload); err != nil || payload.AttemptID == "" || payload.MaxRetries != AutomaticRetryMaxRetries {
				return automaticRetryProjection{}, fmt.Errorf("%w: malformed automatic retry block", ErrInvalidCommand)
			}
			result.blockedAttemptID = payload.AttemptID
			result.blockedReason = payload.BlockedReason
			if payload.FailureReason != "" {
				result.lastFailureReason = payload.FailureReason
			}
		}
	}
	return result, nil
}

func automaticRetryFailureForAttempt(events []domain.RunEvent, attemptID domain.AttemptID) string {
	result := ""
	for _, event := range events {
		if event.Type() != "attempt.failed" && event.Type() != "attempt.reconciled_dead" {
			continue
		}
		var payload struct {
			AttemptID     string `json:"attempt_id"`
			FailureReason string `json:"failure_reason"`
		}
		if json.Unmarshal(event.NormalizedJSON(), &payload) == nil && payload.AttemptID == attemptID.String() {
			result = strings.TrimSpace(payload.FailureReason)
		}
	}
	return result
}

func automaticRetryableFailure(reason string) bool {
	switch reason {
	case "runtime_output_limit_exceeded", "attempt_timeout", "runtime_exit_nonzero":
		return true
	default:
		// model_error is deliberately excluded: today's sanitized Pi event does
		// not distinguish transient provider failure from auth/model access.
		return false
	}
}

func automaticRetryFinalized(attempt domain.Attempt, session storecontract.RuntimeSession) bool {
	if session.TerminalAt.IsZero() || session.FinalizedAt.IsZero() {
		return false
	}
	switch attempt.State() {
	case domain.AttemptStateInterrupted:
		return session.State == "stopped"
	case domain.AttemptStateFailed:
		if session.State == "failed" {
			return session.ProcessIdentity == "" && session.StartedAt.IsZero()
		}
		return session.State == "stopped" && session.ProcessIdentity != "" && !session.StartedAt.IsZero()
	default:
		return false
	}
}

func (s *Service) appendAutomaticRetryReset(ctx context.Context, tx storecontract.WriteTx, runID domain.RunID, attemptID domain.AttemptID, now time.Time) error {
	payload := automaticRetryEvent{Type: automaticRetryResetEvent, RunID: runID.String(), AttemptID: attemptID.String(), MaxRetries: AutomaticRetryMaxRetries}
	_, err := tx.AppendRunEvent(ctx, runID, storecontract.EventDraft{
		ID: s.deps.IDs.EventID(), Type: automaticRetryResetEvent, Source: "app", OccurredAt: now, RecordedAt: now, NormalizedJSON: responseBody(payload),
	})
	return err
}

// BlockAutomaticRetry persists a safe, user-visible terminal state when the
// dispatcher cannot start an already opted-in continuation.
func (s *Service) BlockAutomaticRetry(ctx context.Context, runID domain.RunID, attemptID domain.AttemptID, expectedVersion uint64, reason string) error {
	if s == nil || s.deps.Store == nil {
		return fmt.Errorf("%w: missing store", ErrInvalidCommand)
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return fmt.Errorf("%w: automatic retry block reason is required", ErrInvalidCommand)
	}
	unlock := s.runLock(runID)
	defer unlock()
	now := s.deps.Clock.Now()
	return s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		run, err := tx.GetRun(ctx, runID)
		if err != nil {
			return err
		}
		if run.Version() != expectedVersion {
			return storecontract.ErrVersionConflict
		}
		attempt, err := tx.GetCurrentAttempt(ctx, runID)
		if err != nil {
			return err
		}
		if attempt.ID() != attemptID {
			return storecontract.ErrVersionConflict
		}
		status, err := automaticRetryForRun(ctx, tx, runID)
		if err != nil {
			return err
		}
		if status == nil || status.State == AutomaticRetryExhausted || status.State == AutomaticRetryBlocked && status.BlockedReason != "" {
			return nil
		}
		payload := automaticRetryEvent{Type: automaticRetryBlockedEvent, RunID: runID.String(), AttemptID: attempt.ID().String(), FailureReason: status.LastFailureReason, RetriesUsed: status.RetriesUsed, MaxRetries: AutomaticRetryMaxRetries, BlockedReason: reason}
		_, err = tx.AppendRunEvent(ctx, runID, storecontract.EventDraft{ID: s.deps.IDs.EventID(), Type: automaticRetryBlockedEvent, Source: "app", OccurredAt: now, RecordedAt: now, NormalizedJSON: responseBody(payload)})
		return err
	})
}
