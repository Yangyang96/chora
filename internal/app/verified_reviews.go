package app

import (
	"context"
	"fmt"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

type verifiedReviewWire struct {
	Run                runWire
	DecisionID         string
	ExpectedRunVersion uint64
	Kind               domain.ReviewDecisionKind
	Reason             string
	RejectionClass     domain.ReviewRejectionClass
	ActorID            string
	SessionID          string
	Binding            verifiedReviewBindingWire
	PlanningDraftID    string
	RelatedTaskID      string
	DecidedAt          time.Time
}

type verifiedReviewBindingWire struct {
	EvidenceKind          domain.ReviewEvidenceKind
	ResultID              string
	AgentAttemptID        string
	AgentReportID         string
	VerificationAttemptID string
	PatchArtifactID       string
	PatchDigest           [32]byte
	BaselineDigest        [32]byte
	DeclaredFilesDigest   [32]byte
}

func wireVerifiedReviewBinding(binding domain.VerifiedReviewBinding) verifiedReviewBindingWire {
	return verifiedReviewBindingWire{
		EvidenceKind: binding.NormalizedEvidenceKind(), ResultID: binding.ResultID.String(), AgentAttemptID: binding.AgentAttemptID.String(), AgentReportID: binding.AgentReportID.String(), VerificationAttemptID: binding.VerificationAttemptID.String(),
		PatchArtifactID: binding.PatchArtifactID.String(), PatchDigest: binding.PatchDigest, BaselineDigest: binding.BaselineDigest, DeclaredFilesDigest: binding.DeclaredFilesDigest,
	}
}

func restoreVerifiedReviewBinding(wire verifiedReviewBindingWire) (domain.VerifiedReviewBinding, error) {
	resultID, err := domain.ParseResultID(wire.ResultID)
	if err != nil {
		return domain.VerifiedReviewBinding{}, err
	}
	agentAttemptID, err := domain.ParseAttemptID(wire.AgentAttemptID)
	if err != nil {
		return domain.VerifiedReviewBinding{}, err
	}
	var verificationAttemptID domain.VerificationAttemptID
	var agentReportID domain.AgentReportID
	if wire.EvidenceKind == domain.ReviewEvidenceAgentReport {
		agentReportID, err = domain.ParseAgentReportID(wire.AgentReportID)
		if err != nil {
			return domain.VerifiedReviewBinding{}, err
		}
	} else {
		verificationAttemptID, err = domain.ParseVerificationAttemptID(wire.VerificationAttemptID)
		if err != nil {
			return domain.VerifiedReviewBinding{}, err
		}
	}
	patchArtifactID, err := domain.ParseArtifactID(wire.PatchArtifactID)
	if err != nil {
		return domain.VerifiedReviewBinding{}, err
	}
	return domain.VerifiedReviewBinding{
		EvidenceKind: wire.EvidenceKind, ResultID: resultID, AgentAttemptID: agentAttemptID, AgentReportID: agentReportID, VerificationAttemptID: verificationAttemptID, PatchArtifactID: patchArtifactID,
		PatchDigest: wire.PatchDigest, BaselineDigest: wire.BaselineDigest, DeclaredFilesDigest: wire.DeclaredFilesDigest,
	}, nil
}

func (s *Service) ReviewVerifiedResult(ctx context.Context, request VerifiedReviewRequest) (VerifiedReviewResult, error) {
	if s == nil || s.deps.Store == nil || s.deps.PatchSource == nil {
		return VerifiedReviewResult{}, fmt.Errorf("%w: verified review dependencies unavailable", ErrInvalidCommand)
	}
	if err := s.authorize(ctx, request.CommandMeta, "review_verified_result", request.RunID.String(), request.ExpectedVersion); err != nil {
		return VerifiedReviewResult{}, err
	}
	if s.deps.Presence == nil {
		return VerifiedReviewResult{}, domain.ErrUnauthorizedReview
	}
	presence := PresenceRequest{ActorID: request.ActorID, SessionID: request.SessionID, RunID: request.RunID, Kind: request.Kind, ExpectedVersion: request.ExpectedVersion}
	authorization, err := s.deps.Presence.AuthorizeReview(ctx, presence)
	if err != nil {
		return VerifiedReviewResult{}, err
	}
	unlock := s.runLock(request.RunID)
	defer unlock()
	now := s.deps.Clock.Now()
	semantic := struct {
		Kind           domain.ReviewDecisionKind
		Reason         string
		RejectionClass domain.ReviewRejectionClass
		ActorID        string
		SessionID      string
	}{request.Kind, request.Reason, request.RejectionClass, request.ActorID, request.SessionID}
	key, err := s.commandKey(request.CommandMeta, "review_verified_result", request.RunID.String(), request.ExpectedVersion, semantic, now)
	if err != nil {
		return VerifiedReviewResult{}, err
	}
	var result VerifiedReviewResult
	err = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		response, replay, err := tx.LookupCommand(ctx, key)
		if err != nil {
			return err
		}
		if replay {
			result, err = replayVerifiedReview(ctx, tx, response)
			return err
		}
		if e := requireRunWithoutClosure(ctx, tx, request.RunID); e != nil {
			return e
		}
		run, err := tx.GetRun(ctx, request.RunID)
		if err != nil {
			return err
		}
		if run.Version() != request.ExpectedVersion {
			return storecontract.ErrVersionConflict
		}
		change, err := s.loadReviewableChange(ctx, tx, run.ID())
		if err != nil {
			return err
		}
		decision, err := domain.NewVerifiedReviewDecision(domain.VerifiedReviewDecisionParams{
			ID: s.deps.IDs.ReviewID(), RunID: run.ID(), ExpectedRunVersion: request.ExpectedVersion, Kind: request.Kind,
			Reason: request.Reason, RejectionClass: request.RejectionClass, ActorID: request.ActorID, SessionID: request.SessionID,
			Binding: change.Binding, DecidedAt: now,
		}, authorization)
		if err != nil {
			return err
		}
		next, err := run.ApplyVerifiedReview(decision)
		if err != nil {
			return err
		}
		if err := tx.InsertVerifiedReview(ctx, decision); err != nil {
			return err
		}
		if err := tx.SaveRunCAS(ctx, run.Version(), next); err != nil {
			return err
		}
		result = VerifiedReviewResult{Run: next, Decision: decision}
		if decision.Kind() != domain.ReviewDecisionAccept && decision.RejectionClass() == domain.ReviewRejectionPlanningGap {
			binding, err := tx.GetTechnicalPlanRunBinding(ctx, run.ID())
			if err != nil || binding.TaskID() != run.TaskID() {
				return fmt.Errorf("%w: planning-gap Run has no exact accepted Technical Plan binding", ErrInvalidCommand)
			}
			if _, err := tx.GetOpenTechnicalPlanDraft(ctx, run.TaskID()); err == nil {
				return fmt.Errorf("%w: planning-gap Task already has an open Draft", storecontract.ErrPlanningConflict)
			} else if err != storecontract.ErrNotFound {
				return err
			}
			revision, err := tx.GetTechnicalPlanRevision(ctx, binding.RevisionID())
			if err != nil {
				return err
			}
			draft, err := domain.NewTechnicalPlanSuccessorDraft(s.deps.IDs.TechnicalPlanDraftID(), revision, now)
			if err != nil {
				return err
			}
			if err := tx.InsertTechnicalPlanDraft(ctx, draft); err != nil {
				return err
			}
			route := storecontract.VerifiedReviewRoute{DecisionID: decision.ID(), SourceRunID: run.ID(), SourceTaskID: run.TaskID(), RejectionClass: decision.RejectionClass(), PlanningDraftID: draft.ID(), CreatedAt: now}
			if err := tx.InsertVerifiedReviewRoute(ctx, route); err != nil {
				return err
			}
			result.PlanningDraft = &draft
		} else if decision.RejectionClass() == domain.ReviewRejectionContractChangeRequired {
			sourceTask, err := tx.GetTask(ctx, run.TaskID())
			if err != nil {
				return err
			}
			criterion, err := domain.NewAcceptanceCriterion(domain.NewCriterionID(), "Approve the required contract change", decision.Reason())
			if err != nil {
				return err
			}
			related, err := domain.NewRelatedTask(s.deps.IDs.TaskID(), sourceTask, sourceTask.Title()+" — Contract Change", decision.Reason(), []domain.AcceptanceCriterion{criterion})
			if err != nil {
				return err
			}
			if err := tx.InsertTask(ctx, related); err != nil {
				return err
			}
			route := storecontract.VerifiedReviewRoute{DecisionID: decision.ID(), SourceRunID: run.ID(), SourceTaskID: run.TaskID(), RejectionClass: decision.RejectionClass(), RelatedTaskID: related.ID(), CreatedAt: now}
			if err := tx.InsertVerifiedReviewRoute(ctx, route); err != nil {
				return err
			}
			result.RelatedTask = &related
		}
		eventType := "verified_review.rejected"
		if decision.Kind() == domain.ReviewDecisionAccept {
			eventType = "verified_review.accepted"
		}
		if _, err := tx.AppendRunEvent(ctx, run.ID(), storecontract.EventDraft{
			ID: s.deps.IDs.EventID(), Type: eventType, Source: "app", OccurredAt: now, RecordedAt: now,
			NormalizedJSON: responseBody(map[string]any{"type": eventType, "result_id": change.Binding.ResultID.String(), "patch_digest": fmt.Sprintf("%x", change.Binding.PatchDigest), "rejection_class": decision.RejectionClass()}),
		}); err != nil {
			return err
		}
		response, err = verifiedReviewResponse(result)
		if err != nil {
			return err
		}
		return tx.SaveCommand(ctx, key, response)
	})
	return result, err
}

func verifiedReviewResponse(result VerifiedReviewResult) (storecontract.Response, error) {
	decision := result.Decision
	return jsonResponse(verifiedReviewWire{
		Run: wireRun(result.Run), DecisionID: decision.ID().String(), ExpectedRunVersion: decision.ExpectedRunVersion(), Kind: decision.Kind(),
		Reason: decision.Reason(), RejectionClass: decision.RejectionClass(), ActorID: decision.ActorID(), SessionID: decision.SessionID(),
		Binding: wireVerifiedReviewBinding(decision.Binding()), PlanningDraftID: planningDraftID(result.PlanningDraft), RelatedTaskID: relatedTaskID(result.RelatedTask), DecidedAt: decision.DecidedAt(),
	})
}

func planningDraftID(draft *domain.TechnicalPlanDraft) string {
	if draft == nil {
		return ""
	}
	return draft.ID().String()
}

func relatedTaskID(task *domain.Task) string {
	if task == nil {
		return ""
	}
	return task.ID().String()
}

func replayVerifiedReview(ctx context.Context, reader storecontract.Reader, response storecontract.Response) (VerifiedReviewResult, error) {
	var wire verifiedReviewWire
	if err := decodeResponse(response, &wire); err != nil {
		return VerifiedReviewResult{}, err
	}
	run, err := restoreRun(wire.Run)
	if err != nil {
		return VerifiedReviewResult{}, err
	}
	id, err := domain.ParseReviewDecisionID(wire.DecisionID)
	if err != nil {
		return VerifiedReviewResult{}, err
	}
	binding, err := restoreVerifiedReviewBinding(wire.Binding)
	if err != nil {
		return VerifiedReviewResult{}, err
	}
	decision, err := domain.RestoreVerifiedReviewDecision(domain.VerifiedReviewDecisionParams{
		ID: id, RunID: run.ID(), ExpectedRunVersion: wire.ExpectedRunVersion, Kind: wire.Kind, Reason: wire.Reason,
		RejectionClass: wire.RejectionClass, ActorID: wire.ActorID, SessionID: wire.SessionID, Binding: binding, DecidedAt: wire.DecidedAt,
	})
	if err != nil {
		return VerifiedReviewResult{}, err
	}
	result := VerifiedReviewResult{Run: run, Decision: decision, Replayed: true}
	if wire.PlanningDraftID != "" {
		id, err := domain.ParseTechnicalPlanDraftID(wire.PlanningDraftID)
		if err != nil {
			return VerifiedReviewResult{}, err
		}
		draft, err := reader.GetTechnicalPlanDraft(ctx, id)
		if err != nil {
			return VerifiedReviewResult{}, err
		}
		result.PlanningDraft = &draft
	}
	if wire.RelatedTaskID != "" {
		id, err := domain.ParseTaskID(wire.RelatedTaskID)
		if err != nil {
			return VerifiedReviewResult{}, err
		}
		task, err := reader.GetTask(ctx, id)
		if err != nil {
			return VerifiedReviewResult{}, err
		}
		result.RelatedTask = &task
	}
	return result, nil
}
