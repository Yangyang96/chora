package app

import (
	"context"
	"fmt"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func (s *Service) RequestExecutionDecision(ctx context.Context, request RequestExecutionDecisionRequest) (ExecutionDecisionResult, error) {
	if err := s.authorize(ctx, request.CommandMeta, "request_execution_decision", request.RunID.String(), request.ExpectedVersion); err != nil {
		return ExecutionDecisionResult{}, err
	}
	unlock := s.runLock(request.RunID)
	defer unlock()
	now := s.deps.Clock.Now()
	semantic := struct {
		Question, Context, Recommendation, Impact string
		Options                                   []domain.DecisionGateOption
	}{request.Question, request.Context, request.Recommendation, request.Impact, request.Options}
	key, err := s.commandKey(request.CommandMeta, "request_execution_decision", request.RunID.String(), request.ExpectedVersion, semantic, now)
	if err != nil {
		return ExecutionDecisionResult{}, err
	}
	var result ExecutionDecisionResult
	err = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		response, replayed, err := tx.LookupCommand(ctx, key)
		if err != nil {
			return err
		}
		if replayed {
			result, err = replayExecutionDecision(response)
			return err
		}
		run, err := tx.GetRun(ctx, request.RunID)
		if err != nil {
			return err
		}
		if run.Version() != request.ExpectedVersion || run.State() != domain.RunStateRunning {
			return fmt.Errorf("%w: Run is not executing", ErrInvalidCommand)
		}
		attempt, err := tx.GetCurrentAttempt(ctx, run.ID())
		if err != nil {
			return err
		}
		if attempt.State() != domain.AttemptStateRunning {
			return fmt.Errorf("%w: Attempt is not executing", ErrInvalidCommand)
		}
		if latest, latestErr := tx.GetLatestDecisionGateForRun(ctx, run.ID()); latestErr == nil && latest.Status() == domain.DecisionGateOpen {
			return fmt.Errorf("%w: Run already has an open Decision Gate", ErrInvalidCommand)
		} else if latestErr != nil && latestErr != storecontract.ErrNotFound {
			return latestErr
		}
		gate, err := domain.NewExecutionDecisionGate(domain.ExecutionDecisionGateParams{
			ID: s.deps.IDs.DecisionGateID(), RunID: run.ID(), AttemptID: attempt.ID(), Question: request.Question,
			Context: request.Context, Options: request.Options, Recommendation: request.Recommendation, Impact: request.Impact, RequestedAt: now,
		})
		if err != nil {
			return err
		}
		if err := tx.InsertDecisionGate(ctx, gate); err != nil {
			return err
		}
		payload := map[string]any{"type": "decision.requested", "gate_id": gate.ID().String(), "run_id": run.ID().String(), "attempt_id": attempt.ID().String(), "question": gate.Question(), "options": gate.Options()}
		if _, err := tx.AppendRunEvent(ctx, run.ID(), storecontract.EventDraft{ID: s.deps.IDs.EventID(), Type: "decision.requested", Source: "adapter", OccurredAt: now, RecordedAt: now, NormalizedJSON: responseBody(payload)}); err != nil {
			return err
		}
		result.Gate = gate
		response, err = executionDecisionResponse(result)
		if err != nil {
			return err
		}
		return tx.SaveCommand(ctx, key, response)
	})
	return result, err
}

func (s *Service) ResolveExecutionDecision(ctx context.Context, request ResolveExecutionDecisionRequest) (ExecutionDecisionResult, error) {
	if err := s.authorize(ctx, request.CommandMeta, "resolve_execution_decision", request.GateID.String(), request.ExpectedVersion); err != nil {
		return ExecutionDecisionResult{}, err
	}
	unlock := s.runLock(request.RunID)
	defer unlock()
	now := s.deps.Clock.Now()
	semantic := struct{ OptionID, Note string }{request.OptionID, request.Note}
	key, err := s.commandKey(request.CommandMeta, "resolve_execution_decision", request.GateID.String(), request.ExpectedVersion, semantic, now)
	if err != nil {
		return ExecutionDecisionResult{}, err
	}
	var result ExecutionDecisionResult
	err = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		response, replayed, err := tx.LookupCommand(ctx, key)
		if err != nil {
			return err
		}
		if replayed {
			result, err = replayExecutionDecision(response)
			return err
		}
		run, err := tx.GetRun(ctx, request.RunID)
		if err != nil {
			return err
		}
		if run.Version() != request.ExpectedVersion || run.State() != domain.RunStateRunning {
			return fmt.Errorf("%w: Run is not waiting inside execution", ErrInvalidCommand)
		}
		gate, err := tx.GetDecisionGate(ctx, request.GateID)
		if err != nil {
			return err
		}
		attempt, err := tx.GetCurrentAttempt(ctx, run.ID())
		if err != nil {
			return err
		}
		if gate.RunID() != run.ID() || gate.AttemptID() != attempt.ID() || gate.Status() != domain.DecisionGateOpen {
			return fmt.Errorf("%w: Decision Gate is not the current open execution boundary", ErrInvalidCommand)
		}
		resolved, err := gate.Resolve(request.OptionID, request.Note, request.ActorID, request.SessionID, now)
		if err != nil {
			return err
		}
		if err := tx.ResolveDecisionGateCAS(ctx, gate, resolved); err != nil {
			return err
		}
		payload := map[string]any{"type": "decision.resolved", "gate_id": gate.ID().String(), "run_id": run.ID().String(), "attempt_id": attempt.ID().String(), "selected_option_id": resolved.SelectedOptionID(), "note": resolved.Note(), "actor_id": resolved.ActorID(), "session_id": resolved.SessionID()}
		if _, err := tx.AppendRunEvent(ctx, run.ID(), storecontract.EventDraft{ID: s.deps.IDs.EventID(), Type: "decision.resolved", Source: "human", OccurredAt: now, RecordedAt: now, NormalizedJSON: responseBody(payload)}); err != nil {
			return err
		}
		result.Gate = resolved
		response, err = executionDecisionResponse(result)
		if err != nil {
			return err
		}
		return tx.SaveCommand(ctx, key, response)
	})
	return result, err
}
