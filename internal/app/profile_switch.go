package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

type profileSwitchDelta struct {
	SchemaVersion      string                       `json:"schema_version"`
	Reason             string                       `json:"reason"`
	PreviousProfile    domain.AgentExecutionProfile `json:"previous_profile"`
	SuccessorProfile   domain.AgentExecutionProfile `json:"successor_profile"`
	PredecessorAttempt string                       `json:"predecessor_attempt_id"`
}

// PrepareProfileSwitch is the sole successor-only profile mutation. It appends
// a Task preference and creates a new Attempt in one transaction; the Charter
// and every predecessor Attempt remain immutable historical evidence.
func (s *Service) PrepareProfileSwitch(ctx context.Context, request PrepareProfileSwitchRequest) (PrepareRunResult, error) {
	if err := s.require(); err != nil {
		return PrepareRunResult{}, err
	}
	target, err := normalizedAgentExecutionProfile(request.Profile)
	if err != nil {
		return PrepareRunResult{}, err
	}
	reason, err := profileSwitchReason(request.Reason)
	if err != nil {
		return PrepareRunResult{}, err
	}
	if err := s.authorize(ctx, request.CommandMeta, "switch_agent_execution_profile", request.RunID.String(), request.ExpectedVersion); err != nil {
		return PrepareRunResult{}, err
	}
	unlock := s.runLock(request.RunID)
	defer unlock()
	now := s.deps.Clock.Now()
	semantic := struct {
		Profile domain.AgentExecutionProfile
		Reason  string
	}{target, reason}
	key, err := s.commandKey(request.CommandMeta, "switch_agent_execution_profile", request.RunID.String(), request.ExpectedVersion, semantic, now)
	if err != nil {
		return PrepareRunResult{}, err
	}
	var result PrepareRunResult
	err = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		response, replayed, err := tx.LookupCommand(ctx, key)
		if err != nil {
			return err
		}
		if replayed {
			result, err = replayPrepare(ctx, response, tx)
			return err
		}
		run, err := tx.GetRun(ctx, request.RunID)
		if err != nil {
			return err
		}
		if run.Version() != request.ExpectedVersion {
			return storecontract.ErrVersionConflict
		}
		task, err := tx.GetTask(ctx, run.TaskID())
		if err != nil {
			return err
		}
		planBinding, charter, snapshot, err := loadBoundRunContext(ctx, tx, run, task)
		if err != nil {
			return err
		}
		if charter.AdapterID() != "pi" {
			return fmt.Errorf("%w: Agent execution profiles are available only for Pi", ErrInvalidCommand)
		}
		oldAttempt, err := tx.GetCurrentAttempt(ctx, run.ID())
		if err != nil {
			return err
		}
		if oldAttempt.ContextSnapshotID() != planBinding.SnapshotID() || oldAttempt.ContextDigest() != planBinding.SnapshotDigest() {
			return fmt.Errorf("%w: predecessor Attempt drifted from immutable planning binding", ErrInvalidCommand)
		}
		if run.State() == domain.RunStateCancelled {
			events, err := tx.ListRunEvents(ctx, run.ID())
			if err != nil {
				return err
			}
			if _, allowed := CancelRetryAuthority(events, oldAttempt.ID()); !allowed {
				return fmt.Errorf("%w: cancelled run has no persisted retry authority", ErrInvalidCommand)
			}
		}
		session, sessionErr := tx.GetRuntimeSessionForAttempt(ctx, oldAttempt.ID())
		if sessionErr == nil && !runtimeSessionFinalizedForRetry(session) {
			return fmt.Errorf("%w: predecessor runtime is not proven dead and finalized", ErrInvalidCommand)
		}
		if sessionErr != nil && !errors.Is(sessionErr, storecontract.ErrNotFound) {
			return sessionErr
		}
		current, currentBinding, err := taskAgentExecutionProfileBinding(ctx, tx, task.ID())
		if err != nil {
			return err
		}
		if oldAttempt.AgentExecutionProfileBinding() != currentBinding {
			return fmt.Errorf("%w: predecessor Attempt drifted from current Agent execution profile preference", ErrInvalidCommand)
		}
		if current.Profile() == target {
			return fmt.Errorf("%w: successor profile must differ from the current profile", ErrInvalidCommand)
		}
		if err := requireProfileAcknowledgement(ctx, tx, target, request.ActorID); err != nil {
			return err
		}
		nextBinding, err := domain.NewAgentExecutionProfileBinding(target)
		if err != nil {
			return err
		}
		delta, err := json.Marshal(profileSwitchDelta{
			SchemaVersion: "chora.agent-execution-profile-switch.v1", Reason: reason,
			PreviousProfile: current.Profile(), SuccessorProfile: target, PredecessorAttempt: oldAttempt.ID().String(),
		})
		if err != nil {
			return err
		}
		nextRun, err := run.Transition(domain.CommandPrepareRetry, now)
		if err != nil {
			return err
		}
		predecessor := oldAttempt.ID()
		nextAttempt, err := domain.NewAttempt(domain.AttemptParams{
			ID: s.deps.IDs.AttemptID(), RunID: run.ID(), Sequence: nextRun.CurrentAttemptNumber(), Predecessor: &predecessor,
			ContextSnapshotID: snapshot.ID(), ContextDigest: snapshot.Digest(), AdapterID: charter.AdapterID(),
			AgentExecutionProfileBinding: nextBinding, RetryReason: "Agent execution profile switch: " + reason,
			ContextDelta: string(delta), ExternalSession: "", CreatedAt: now,
		})
		if err != nil {
			return err
		}
		preference, err := newTaskAgentExecutionProfilePreference(task.ID(), current.Version()+1, target, request.CommandMeta, now)
		if err != nil {
			return err
		}
		if err := tx.InsertTaskAgentExecutionProfilePreference(ctx, preference); err != nil {
			return err
		}
		if err := tx.SaveRunCAS(ctx, run.Version(), nextRun); err != nil {
			return err
		}
		if err := tx.InsertAttempt(ctx, nextAttempt); err != nil {
			return err
		}
		if _, err := tx.AppendRunEvent(ctx, run.ID(), storecontract.EventDraft{
			ID: s.deps.IDs.EventID(), Type: "agent_execution_profile.switched", Source: "app", OccurredAt: now, RecordedAt: now,
			NormalizedJSON: responseBody(map[string]any{
				"type": "agent_execution_profile.switched", "predecessor_attempt_id": oldAttempt.ID().String(),
				"successor_attempt_id": nextAttempt.ID().String(), "previous_profile": current.Profile(), "successor_profile": target,
				"preference_version": preference.Version(), "actor_id": preference.ActorID(), "policy_version": nextBinding.TrustDisclosurePolicy(),
			}),
		}); err != nil {
			return err
		}
		if err := s.appendAutomaticRetryReset(ctx, tx, run.ID(), nextAttempt.ID(), now); err != nil {
			return err
		}
		result = PrepareRunResult{Run: nextRun, Attempt: nextAttempt, Snapshot: snapshot}
		response, err = prepareResponse(result)
		if err != nil {
			return err
		}
		return tx.SaveCommand(ctx, key, response)
	})
	return result, err
}
