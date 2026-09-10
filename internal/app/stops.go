package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

type stopEventPayload struct {
	storeEvent
	AttemptID    string `json:"attempt_id"`
	StopIntent   string `json:"stop_intent"`
	Reason       string `json:"reason"`
	RetryAllowed bool   `json:"retry_allowed"`
}

func CancelRetryAuthority(events []domain.RunEvent, attemptID domain.AttemptID) (string, bool) {
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if event.Type() != "run.stopping" && event.Type() != "run.cancelled" {
			continue
		}
		var payload stopEventPayload
		if json.Unmarshal(event.NormalizedJSON(), &payload) != nil || payload.AttemptID != attemptID.String() {
			continue
		}
		reason := strings.TrimSpace(payload.Reason)
		return reason, payload.StopIntent == string(execution.StopForCancel) && payload.RetryAllowed && reason != ""
	}
	return "", false
}

func (s *Service) RequestHandoff(ctx context.Context, request StopRequest) (StopResult, error) {
	return s.requestStop(ctx, request, domain.CommandRequestHandoff, execution.StopForHandoff, true)
}
func (s *Service) RequestIntervention(ctx context.Context, request StopRequest) (StopResult, error) {
	return s.requestStop(ctx, request, domain.CommandRequestIntervention, execution.StopForRevision, false)
}
func (s *Service) RequestCancel(ctx context.Context, request StopRequest) (StopResult, error) {
	return s.requestStop(ctx, request, domain.CommandRequestCancel, execution.StopForCancel, false)
}

func (s *Service) requestStop(ctx context.Context, request StopRequest, command domain.CommandKind, intent execution.StopIntentKind, handoff bool) (StopResult, error) {
	if request.AllowRetry && intent != execution.StopForCancel {
		return StopResult{}, fmt.Errorf("%w: retry authority is only valid for cancellation", ErrInvalidCommand)
	}
	if err := s.authorize(ctx, request.CommandMeta, string(command), request.RunID.String(), request.ExpectedVersion); err != nil {
		return StopResult{}, err
	}
	unlock := s.runLock(request.RunID)
	defer unlock()
	now := s.deps.Clock.Now()
	key, err := s.commandKey(request.CommandMeta, string(command), request.RunID.String(), request.ExpectedVersion, struct {
		ToActor, Reason string
		AllowRetry      bool
	}{request.ToActor, request.Reason, request.AllowRetry}, now)
	if err != nil {
		return StopResult{}, err
	}
	var result StopResult
	err = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		response, replay, err := tx.LookupCommand(ctx, key)
		if err != nil {
			return err
		}
		if replay {
			var replayErr error
			result, replayErr = replayStop(response)
			return replayErr
		}
		run, err := tx.GetRun(ctx, request.RunID)
		if err != nil {
			return err
		}
		if run.Version() != request.ExpectedVersion {
			return storecontract.ErrVersionConflict
		}
		if intent == execution.StopForCancel && (run.State() == domain.RunStateRecoveryRequired || run.State() == domain.RunStateReady) {
			automatic, automaticErr := automaticRetryForRun(ctx, tx, run.ID())
			if automaticErr != nil {
				return automaticErr
			}
			if automatic != nil && (automatic.State == AutomaticRetryPending || automatic.State == AutomaticRetryRetrying || automatic.State == AutomaticRetryBlocked) {
				attempt, attemptErr := tx.GetCurrentAttempt(ctx, request.RunID)
				if attemptErr != nil {
					return attemptErr
				}
				safe, safeErr := automaticRetryInactiveCancelReady(ctx, tx, run, attempt, automatic)
				if safeErr != nil {
					return safeErr
				}
				if !safe {
					// Fall through to the ordinary stop path. Recovery with no
					// cleanup proof remains fail-closed.
				} else {
					next, transitionErr := run.Transition(domain.CommandCancelInactiveRun, now)
					if transitionErr != nil {
						return transitionErr
					}
					if err := tx.SaveRunCAS(ctx, run.Version(), next); err != nil {
						return err
					}
					body := responseBody(stopEventPayload{
						storeEvent: s.event("run.cancelled", next), AttemptID: attempt.ID().String(),
						StopIntent: string(intent), Reason: request.Reason, RetryAllowed: request.AllowRetry,
					})
					if _, err := tx.AppendRunEvent(ctx, run.ID(), storecontract.EventDraft{ID: s.deps.IDs.EventID(), Type: "run.cancelled", Source: "app", OccurredAt: now, RecordedAt: now, NormalizedJSON: body}); err != nil {
						return err
					}
					result.Run = next
					result.Attempt = attempt
					response, err = stopResponse(result)
					if err != nil {
						return err
					}
					return tx.SaveCommand(ctx, key, response)
				}
			}
		}
		attempt, err := tx.GetCurrentAttempt(ctx, request.RunID)
		if err != nil {
			return err
		}
		session, err := tx.GetRuntimeSessionForAttempt(ctx, attempt.ID())
		if err != nil {
			return err
		}
		next := run
		if run.State() == domain.RunStateStopping {
			if handoff || session.StopIntent != string(intent) {
				return fmt.Errorf("%w: run is already stopping with intent %q", ErrInvalidCommand, session.StopIntent)
			}
		} else {
			next, err = run.Transition(command, now)
			if err != nil {
				return err
			}
			if err := tx.SaveRunCAS(ctx, run.Version(), next); err != nil {
				return err
			}
			if handoff {
				id := s.deps.IDs.HandoffID()
				attemptID := attempt.ID()
				if err := tx.InsertHandoff(ctx, storecontract.Handoff{ID: id, RunID: run.ID(), AttemptID: &attemptID, FromActor: request.ActorID, ToActor: request.ToActor, Reason: request.Reason, Status: "requested", CreatedAt: now}); err != nil {
					return err
				}
				result.HandoffID = &id
			}
			sessionVersion := session.Version
			session.Version++
			session.State = "stopping"
			session.StopIntent = string(intent)
			session.UpdatedAt = now
			if err := tx.SaveRuntimeSessionCAS(ctx, sessionVersion, session); err != nil {
				return err
			}
			body := responseBody(stopEventPayload{
				storeEvent: s.event("run.stopping", next), AttemptID: attempt.ID().String(),
				StopIntent: string(intent), Reason: request.Reason, RetryAllowed: request.AllowRetry,
			})
			if _, err := tx.AppendRunEvent(ctx, run.ID(), storecontract.EventDraft{ID: s.deps.IDs.EventID(), Type: "run.stopping", Source: "app", OccurredAt: now, RecordedAt: now, NormalizedJSON: body}); err != nil {
				return err
			}
		}
		result.Run = next
		result.Attempt = attempt
		response, err = stopResponse(result)
		if err != nil {
			return err
		}
		if err := tx.SaveCommand(ctx, key, response); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return result, err
	}
	if result.Replayed {
		if result.Run.State() == domain.RunStateRevisionRequired || result.Run.State() == domain.RunStateCancelled {
			session, loadErr := s.deps.Store.Reader().GetRuntimeSessionForAttempt(ctx, result.Attempt.ID())
			if errors.Is(loadErr, storecontract.ErrNotFound) && result.Run.State() == domain.RunStateCancelled {
				return result, nil
			}
			if loadErr != nil {
				return result, loadErr
			}
			if finalizeErr := s.finalizeRuntime(ctx, session.ID); finalizeErr != nil {
				return result, finalizeErr
			}
			return result, nil
		}
		if result.Run.State() != domain.RunStateStopping {
			return result, nil
		}
	}
	if result.Run.State() == domain.RunStateCancelled {
		session, loadErr := s.deps.Store.Reader().GetRuntimeSessionForAttempt(ctx, result.Attempt.ID())
		if errors.Is(loadErr, storecontract.ErrNotFound) {
			return result, nil
		}
		if loadErr != nil {
			return result, loadErr
		}
		if finalizeErr := s.finalizeRuntime(ctx, session.ID); finalizeErr != nil {
			return result, finalizeErr
		}
		return result, nil
	}
	session, err := s.deps.Store.Reader().GetRuntimeSessionForAttempt(ctx, result.Attempt.ID())
	if err != nil {
		return result, err
	}
	var outcome execution.StopOutcome
	var reconciled execution.ReconcileOutcome
	cleanupProven := !session.FinalizedAt.IsZero()
	if cleanupProven {
		if session.State != "stopped" || session.StopIntent != string(intent) {
			return result, fmt.Errorf("%w: invalid finalized stop proof", ErrInvalidCommand)
		}
		outcome = execution.StopOutcome{Kind: execution.StopConfirmed}
	} else {
		var reconcileErr error
		reconciled, reconcileErr = s.deps.Supervisor.Reconcile(ctx, execution.ProcessIdentity{Value: session.ProcessIdentity})
		if reconcileErr != nil {
			outcome = execution.StopOutcome{Kind: execution.StopUncertain, Diagnostic: reconcileErr.Error()}
		} else if reconciled.LaunchToken != (execution.LaunchToken{Value: session.LaunchToken}) {
			outcome = execution.StopOutcome{Kind: execution.StopUncertain, Diagnostic: "reconcile launch token mismatch"}
		} else {
			switch reconciled.Kind {
			case execution.ReconcileDead:
				outcome = execution.StopOutcome{Kind: execution.StopConfirmed}
			case execution.ReconcileAlive:
				if !reconciled.Handle.Valid() {
					outcome = execution.StopOutcome{Kind: execution.StopUncertain, Diagnostic: "reconcile returned no stop handle"}
				} else {
					outcome, err = s.deps.Supervisor.Stop(ctx, reconciled.Handle, execution.StopIntent{Kind: intent, Reason: request.Reason})
					if err != nil {
						outcome = execution.StopOutcome{Kind: execution.StopUncertain, Diagnostic: err.Error()}
					}
				}
			case execution.ReconcileUncertain:
				outcome = execution.StopOutcome{Kind: execution.StopUncertain, Diagnostic: reconciled.Diagnostic}
			default:
				outcome = execution.StopOutcome{Kind: execution.StopUncertain, Diagnostic: "malformed reconcile outcome"}
			}
		}
	}
	if outcome.Kind != execution.StopConfirmed && outcome.Kind != execution.StopUncertain {
		outcome = execution.StopOutcome{Kind: execution.StopUncertain, Diagnostic: "malformed stop outcome"}
	}
	var drainHandle execution.RuntimeHandle
	if outcome.Kind == execution.StopConfirmed && !cleanupProven {
		drainHandle = reconciled.Handle
		if !drainHandle.Valid() {
			outcome = execution.StopOutcome{Kind: execution.StopUncertain, Diagnostic: "confirmed stop has no drain handle"}
		} else {
			adapter, loadErr := s.deps.Agents.Get(session.AdapterID)
			if loadErr != nil {
				outcome = execution.StopOutcome{Kind: execution.StopUncertain, Diagnostic: loadErr.Error()}
			} else {
				runtime := runtimeContext{session: session, attempt: result.Attempt, run: result.Run, adapter: adapter}
				if _, loadErr = s.drainToEOF(ctx, runtime, drainHandle); loadErr != nil {
					outcome = execution.StopOutcome{Kind: execution.StopUncertain, Diagnostic: loadErr.Error()}
				}
			}
		}
	}
	if outcome.Kind == execution.StopConfirmed && !cleanupProven {
		if finalizeErr := s.deps.Supervisor.Finalize(ctx, drainHandle, execution.RetentionPolicy{}); finalizeErr != nil {
			outcome = execution.StopOutcome{Kind: execution.StopUncertain, Diagnostic: "runtime cleanup failed: " + finalizeErr.Error()}
		} else {
			if err := s.markStoppedFinalized(ctx, session.ID, outcome.Diagnostic); err != nil {
				return result, err
			}
			cleanupProven = true
		}
	}
	now = s.deps.Clock.Now()
	err = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		run, err := tx.GetRun(ctx, request.RunID)
		if err != nil {
			return err
		}
		attempt, err := tx.GetCurrentAttempt(ctx, request.RunID)
		if err != nil {
			return err
		}
		var next domain.AgentRun
		var nextAttempt = attempt
		event := "run.recovery_required"
		if outcome.Kind == execution.StopConfirmed {
			runCommand := domain.CommandStopConfirmedForRevision
			attemptEvent := domain.AttemptEventStopConfirmedForRevision
			if intent == execution.StopForCancel {
				runCommand = domain.CommandStopConfirmedForCancel
				attemptEvent = domain.AttemptEventStopConfirmedForCancel
			}
			next, err = run.Transition(runCommand, now)
			if err == nil {
				nextAttempt, err = attempt.Transition(attemptEvent)
			}
			event = "run.stop_confirmed"
		} else {
			next, err = run.Transition(domain.CommandStopUncertain, now)
		}
		if err != nil {
			return err
		}
		if err := tx.SaveRunCAS(ctx, run.Version(), next); err != nil {
			return err
		}
		if nextAttempt.State() != attempt.State() {
			if err := tx.SaveAttemptCAS(ctx, attempt.State(), nextAttempt); err != nil {
				return err
			}
		}
		session, err := tx.GetRuntimeSessionForAttempt(ctx, attempt.ID())
		if err != nil {
			return err
		}
		if outcome.Kind == execution.StopConfirmed {
			if session.State != "stopped" || session.FinalizedAt.IsZero() || session.StopIntent != string(intent) {
				return fmt.Errorf("%w: missing finalized stop proof", ErrInvalidCommand)
			}
		} else {
			sessionVersion := session.Version
			session.Version++
			session.UpdatedAt = now
			session.Diagnostic = outcome.Diagnostic
			session.State = "lost"
			if err := tx.SaveRuntimeSessionCAS(ctx, sessionVersion, session); err != nil {
				return err
			}
		}
		body := responseBody(s.event(event, next))
		if _, err := tx.AppendRunEvent(ctx, run.ID(), storecontract.EventDraft{ID: s.deps.IDs.EventID(), Type: event, Source: "app", OccurredAt: now, RecordedAt: now, NormalizedJSON: body}); err != nil {
			return err
		}
		result.Run = next
		result.Attempt = nextAttempt
		response, err := stopResponse(result)
		if err != nil {
			return err
		}
		return tx.UpdateCommand(ctx, key, response)
	})
	if err != nil {
		return result, err
	}
	return result, nil
}

func automaticRetryInactiveCancelReady(ctx context.Context, reader storecontract.Reader, run domain.AgentRun, attempt domain.Attempt, status *AutomaticRetry) (bool, error) {
	if status == nil {
		return false, nil
	}
	session, err := reader.GetRuntimeSessionForAttempt(ctx, attempt.ID())
	if errors.Is(err, storecontract.ErrNotFound) {
		return run.State() == domain.RunStateReady && (status.State == AutomaticRetryRetrying || status.State == AutomaticRetryBlocked), nil
	}
	if err != nil {
		return false, err
	}
	if run.State() != domain.RunStateRecoveryRequired {
		return false, nil
	}
	return automaticRetryFinalized(attempt, session), nil
}

func (s *Service) markStoppedFinalized(ctx context.Context, sessionID domain.RuntimeSessionID, diagnostic string) error {
	now := s.deps.Clock.Now()
	err := s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		session, err := tx.GetRuntimeSession(ctx, sessionID)
		if err != nil {
			return err
		}
		if !session.FinalizedAt.IsZero() {
			if session.State != "stopped" {
				return fmt.Errorf("%w: finalized runtime is not stopped", ErrInvalidCommand)
			}
			return nil
		}
		version := session.Version
		session.Version++
		session.State = "stopped"
		session.Diagnostic = diagnostic
		session.UpdatedAt = now
		session.TerminalAt = now
		session.FinalizedAt = now
		return tx.SaveRuntimeSessionCAS(ctx, version, session)
	})
	if err == nil {
		s.cleanupRuntimeSignals(sessionID)
	}
	return err
}
