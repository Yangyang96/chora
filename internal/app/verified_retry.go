package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

type verifiedAgentRetryDelta struct {
	SchemaVersion                    string   `json:"schema_version"`
	RejectionNote                    string   `json:"rejection_note"`
	UnmetCriterionIDs                []string `json:"unmet_criterion_ids"`
	Instructions                     string   `json:"instructions"`
	PredecessorResultID              string   `json:"predecessor_result_id"`
	PredecessorAgentAttemptID        string   `json:"predecessor_agent_attempt_id"`
	PredecessorVerificationAttemptID string   `json:"predecessor_verification_attempt_id"`
	SourceBaselineDigest             string   `json:"source_baseline_digest"`
	AcceptanceContractDigest         string   `json:"acceptance_contract_digest"`
	ContextSnapshotDigest            string   `json:"context_snapshot_digest"`
	ReviewDecisionID                 string   `json:"review_decision_id,omitempty"`
}

func (s *Service) PrepareVerifiedAgentRetry(ctx context.Context, request PrepareVerifiedAgentRetryRequest) (PrepareRunResult, error) {
	if s == nil || s.deps.Store == nil || s.deps.PatchSource == nil {
		return PrepareRunResult{}, fmt.Errorf("%w: verified Agent Retry dependencies unavailable", ErrInvalidCommand)
	}
	request.Instructions = strings.TrimSpace(request.Instructions)
	if request.Instructions == "" {
		return PrepareRunResult{}, fmt.Errorf("%w: Retry instructions are required", ErrInvalidCommand)
	}
	if err := s.authorize(ctx, request.CommandMeta, "prepare_verified_agent_retry", request.RunID.String(), request.ExpectedVersion); err != nil {
		return PrepareRunResult{}, err
	}
	unlock := s.runLock(request.RunID)
	defer unlock()
	now := s.deps.Clock.Now()
	key, err := s.commandKey(request.CommandMeta, "prepare_verified_agent_retry", request.RunID.String(), request.ExpectedVersion, request.Instructions, now)
	if err != nil {
		return PrepareRunResult{}, err
	}
	var result PrepareRunResult
	err = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		response, replay, err := tx.LookupCommand(ctx, key)
		if err != nil {
			return err
		}
		if replay {
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
		if run.State() != domain.RunStateRevisionRequired {
			return fmt.Errorf("%w: Run is not revision_required", ErrInvalidCommand)
		}
		change, err := s.loadReviewableChange(ctx, tx, run.ID())
		if err != nil {
			return err
		}
		oldAttempt, err := tx.GetCurrentAttempt(ctx, run.ID())
		if err != nil || oldAttempt.ID() != change.Binding.AgentAttemptID || oldAttempt.State() != domain.AttemptStateOutputSubmitted {
			return reviewEvidenceError("predecessor Agent Attempt binding changed")
		}
		if oldAttempt.AdapterID() == "pi" {
			preference, binding, err := taskAgentExecutionProfileBinding(ctx, tx, run.TaskID())
			if err != nil {
				return err
			}
			if oldAttempt.AgentExecutionProfileBinding() != binding {
				return reviewEvidenceError("predecessor Agent execution profile is not current")
			}
			if err := requireProfileAcknowledgement(ctx, tx, preference.Profile(), preference.ActorID()); err != nil {
				return err
			}
		}
		reason, reviewID, err := verifiedRetryAuthority(ctx, tx, change)
		if err != nil {
			return err
		}
		snapshot, err := tx.GetSnapshot(ctx, oldAttempt.ContextSnapshotID())
		if err != nil || snapshot.Digest() != oldAttempt.ContextDigest() || snapshot.Digest() != change.ContextSnapshotDigest() {
			return reviewEvidenceError("frozen Context Snapshot is unavailable or drifted")
		}
		unmet := make([]string, 0, len(change.Checks))
		for _, check := range change.Checks {
			if check.Status() != domain.AcceptanceCheckPassed {
				unmet = append(unmet, check.CriterionID().String())
			}
		}
		deltaBytes, err := json.Marshal(verifiedAgentRetryDelta{
			SchemaVersion: "chora.verified-agent-retry.v1", RejectionNote: reason, UnmetCriterionIDs: unmet, Instructions: request.Instructions,
			PredecessorResultID: change.Binding.ResultID.String(), PredecessorAgentAttemptID: oldAttempt.ID().String(),
			PredecessorVerificationAttemptID: change.Binding.VerificationAttemptID.String(), SourceBaselineDigest: fmt.Sprintf("%x", change.Binding.BaselineDigest),
			AcceptanceContractDigest: fmt.Sprintf("%x", change.ContractDigest), ContextSnapshotDigest: fmt.Sprintf("%x", oldAttempt.ContextDigest()), ReviewDecisionID: reviewID,
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
			ContextSnapshotID: oldAttempt.ContextSnapshotID(), ContextDigest: oldAttempt.ContextDigest(), AdapterID: oldAttempt.AdapterID(), AgentExecutionProfileBinding: oldAttempt.AgentExecutionProfileBinding(),
			RetryReason: reason, ContextDelta: string(deltaBytes), ExternalSession: "", CreatedAt: now,
		})
		if err != nil {
			return err
		}
		if err := tx.SaveRunCAS(ctx, run.Version(), nextRun); err != nil {
			return err
		}
		if err := tx.InsertAttempt(ctx, nextAttempt); err != nil {
			return err
		}
		if _, err := tx.AppendRunEvent(ctx, run.ID(), storecontract.EventDraft{
			ID: s.deps.IDs.EventID(), Type: "agent_retry.prepared", Source: "app", OccurredAt: now, RecordedAt: now,
			NormalizedJSON: responseBody(map[string]any{"type": "agent_retry.prepared", "predecessor_agent_attempt_id": oldAttempt.ID().String(), "successor_agent_attempt_id": nextAttempt.ID().String(), "predecessor_result_id": change.Binding.ResultID.String()}),
		}); err != nil {
			return err
		}
		if nextAttempt.AdapterID() == "pi" {
			if err := s.appendAutomaticRetryReset(ctx, tx, run.ID(), nextAttempt.ID(), now); err != nil {
				return err
			}
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

func verifiedRetryAuthority(ctx context.Context, reader storecontract.Reader, change ReviewableChange) (string, string, error) {
	decision, err := reader.GetVerifiedReviewForResult(ctx, change.Binding.ResultID)
	if err == nil {
		if decision.Binding() != change.Binding {
			return "", "", reviewEvidenceError("Human Review binding drifted")
		}
		if !decision.AllowsAgentRetry() {
			return "", "", fmt.Errorf("%w: %s blocks Agent Retry and requires its explicit successor route", ErrInvalidCommand, decision.RejectionClass())
		}
		return decision.Reason(), decision.ID().String(), nil
	}
	if !errors.Is(err, storecontract.ErrNotFound) {
		return "", "", err
	}
	if change.Outcome != domain.ResultNeedsRevision {
		return "", "", fmt.Errorf("%w: no implementation-gap Retry authority", ErrInvalidCommand)
	}
	return "Independent verification produced a trusted needs_revision Result.", "", nil
}

// ContextSnapshotDigest is the Result/contract digest revalidated by the loader.
func (change ReviewableChange) ContextSnapshotDigest() [32]byte {
	return change.contextSnapshotDigest
}
