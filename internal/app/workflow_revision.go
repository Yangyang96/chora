package app

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

// RecoverPreparedWorkflowRevision adopts the exact human-prepared successor
// left by an interrupted older retry path. It never creates another Attempt,
// changes its instructions/model, revives a terminal Run or enables automation.
func (s *Service) RecoverPreparedWorkflowRevision(ctx context.Context, q PrepareRetryRequest) (result PrepareRunResult, err error) {
	if q.Automatic {
		return result, ErrUnauthorizedCommand
	}
	if err = s.authorize(ctx, q.CommandMeta, "recover_workflow_revision", q.RunID.String(), q.ExpectedVersion); err != nil {
		return
	}
	unlock := s.runLock(q.RunID)
	defer unlock()
	semantic := struct {
		Instructions string
		ModelBinding domain.ModelBinding
	}{q.Instructions, q.ModelBinding}
	key, err := s.commandKey(q.CommandMeta, "recover_workflow_revision", q.RunID.String(), q.ExpectedVersion, semantic, s.deps.Clock.Now())
	if err != nil {
		return result, err
	}
	err = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		response, replayed, e := tx.LookupCommand(ctx, key)
		if e != nil {
			return e
		}
		if replayed {
			result, e = replayPrepare(ctx, response, tx)
			return e
		}
		run, e := tx.GetRun(ctx, q.RunID)
		if e != nil {
			return e
		}
		if run.Version() != q.ExpectedVersion || run.State() != domain.RunStateReady {
			return storecontract.ErrVersionConflict
		}
		w, e := tx.GetTaskWorkflow(ctx, run.TaskID())
		if e != nil {
			return e
		}
		if w.ActorID != q.ActorID || w.RunID != run.ID() || (w.Status != "awaiting_review" && w.Status != "coding") {
			return ErrUnauthorizedCommand
		}
		check := w
		check.Status = "coding"
		if e = workflowBoundRunAuthority(ctx, tx, check); e != nil {
			return e
		}
		a, e := tx.GetCurrentAttempt(ctx, run.ID())
		if e != nil {
			return e
		}
		predecessor, hasPredecessor := a.Predecessor()
		if a.State() != domain.AttemptStateCreated || a.Sequence() < 2 || !hasPredecessor {
			return ErrUnauthorizedCommand
		}
		if _, e = tx.GetRuntimeSessionForAttempt(ctx, a.ID()); !errors.Is(e, storecontract.ErrNotFound) {
			return ErrUnauthorizedCommand
		}
		old, e := tx.GetAttempt(ctx, predecessor)
		if e != nil {
			return e
		}
		if old.RunID() != run.ID() || old.Sequence()+1 != a.Sequence() || old.ModelBinding().JSON() != a.ModelBinding().JSON() || old.AgentExecutionProfileBinding() != a.AgentExecutionProfileBinding() || old.ContextSnapshotID() != a.ContextSnapshotID() || old.ContextDigest() != a.ContextDigest() {
			return ErrUnauthorizedCommand
		}
		if session, e := tx.GetRuntimeSessionForAttempt(ctx, old.ID()); e == nil {
			if !runtimeSessionFinalizedForRetry(session) {
				return ErrUnauthorizedCommand
			}
		} else if !errors.Is(e, storecontract.ErrNotFound) {
			return e
		}
		if q.ModelBinding.Configured() && q.ModelBinding.JSON() != a.ModelBinding().JSON() {
			return ErrUnauthorizedCommand
		}
		var delta struct {
			Instructions string `json:"instructions"`
			ResultID     string `json:"predecessor_result_id"`
			ReviewID     string `json:"review_decision_id"`
		}
		if json.Unmarshal([]byte(a.ContextDelta()), &delta) != nil || delta.Instructions == "" || delta.Instructions != q.Instructions {
			return ErrUnauthorizedCommand
		}
		review, e := tx.GetLatestReviewForRun(ctx, run.ID())
		if e == nil {
			if review.Kind() != domain.ReviewDecisionReject || review.ExpectedRunVersion()+2 != run.Version() {
				return ErrUnauthorizedCommand
			}
		} else if errors.Is(e, storecontract.ErrNotFound) && delta.ResultID != "" && delta.ReviewID != "" {
			resultID, e := domain.ParseResultID(delta.ResultID)
			if e != nil {
				return e
			}
			decision, e := tx.GetVerifiedReviewForResult(ctx, resultID)
			if e != nil {
				return e
			}
			if decision.ID().String() != delta.ReviewID || decision.RunID() != run.ID() || decision.ExpectedRunVersion()+2 != run.Version() || decision.Binding().AgentAttemptID != old.ID() || !decision.AllowsAgentRetry() || decision.ActorID() != q.ActorID {
				return ErrUnauthorizedCommand
			}
		} else {
			return e
		}
		if e = recordWorkflowRevision(ctx, tx, &w, a); e != nil {
			return e
		}
		snapshot, e := tx.GetSnapshot(ctx, a.ContextSnapshotID())
		if e != nil {
			return e
		}
		if snapshot.Digest() != a.ContextDigest() {
			return ErrUnauthorizedCommand
		}
		result = PrepareRunResult{Run: run, Attempt: a, Snapshot: snapshot}
		response, e = prepareResponse(result)
		if e != nil {
			return e
		}
		return tx.SaveCommand(ctx, key, response)
	})
	return
}
