package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func (s *Service) Review(ctx context.Context, request ReviewRequest) (ReviewResult, error) {
	if err := s.authorize(ctx, request.CommandMeta, "review", request.RunID.String(), request.ExpectedVersion); err != nil {
		return ReviewResult{}, err
	}
	if s.deps.Presence == nil {
		return ReviewResult{}, domain.ErrUnauthorizedReview
	}
	presence := PresenceRequest{ActorID: request.ActorID, SessionID: request.SessionID, RunID: request.RunID, Kind: request.Kind, ExpectedVersion: request.ExpectedVersion}
	authorization, err := s.deps.Presence.AuthorizeReview(ctx, presence)
	if err != nil {
		return ReviewResult{}, err
	}
	unlock := s.runLock(request.RunID)
	defer unlock()
	now := s.deps.Clock.Now()
	key, err := s.commandKey(request.CommandMeta, "review", request.RunID.String(), request.ExpectedVersion, struct {
		Kind               domain.ReviewDecisionKind
		Comment            string `json:"Note"`
		Checks             []domain.ReviewedCheck
		Artifacts          []domain.ArtifactLink
		CandidateProposals []CandidateProposal
	}{request.Kind, request.Comment, request.Checks, request.LinkedArtifacts, request.CandidateProposals}, now)
	if err != nil {
		return ReviewResult{}, err
	}
	var result ReviewResult
	err = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		response, replay, err := tx.LookupCommand(ctx, key)
		if err != nil {
			return err
		}
		if replay {
			var replayErr error
			result, replayErr = replayReview(ctx, response, tx)
			return replayErr
		}
		run, err := tx.GetRun(ctx, request.RunID)
		if err != nil {
			return err
		}
		if err := requireLegacyReview(ctx, tx, run.TaskID()); err != nil {
			return err
		}
		if run.Version() != request.ExpectedVersion {
			return storecontract.ErrVersionConflict
		}
		if _, verificationErr := tx.GetVerificationRunForRun(ctx, run.ID()); verificationErr == nil {
			return fmt.Errorf("%w: verified Result review belongs to G2-M5", ErrInvalidCommand)
		} else if !errors.Is(verificationErr, storecontract.ErrNotFound) {
			return verificationErr
		}
		params := domain.ReviewDecisionParams{ID: s.deps.IDs.ReviewID(), RunID: run.ID(), ExpectedRunVersion: request.ExpectedVersion, Comment: request.Comment, Checks: request.Checks, LinkedArtifacts: request.LinkedArtifacts, DecidedAt: now}
		var decision domain.ReviewDecision
		if request.Kind == domain.ReviewDecisionAccept {
			decision, err = domain.NewAcceptedReviewDecision(params, authorization)
		} else {
			decision, err = domain.NewRejectedReviewDecision(params, authorization)
		}
		if err != nil {
			return err
		}
		next, err := run.ApplyReview(decision)
		if err != nil {
			return err
		}
		if err := tx.SaveRunCAS(ctx, run.Version(), next); err != nil {
			return err
		}
		if err := tx.InsertReview(ctx, decision); err != nil {
			return err
		}
		if request.Kind == domain.ReviewDecisionAccept {
			if err := tx.CloseTaskForAcceptedRun(ctx, run.TaskID(), run.ID(), next.Version()); err != nil {
				return err
			}
			if len(request.CandidateProposals) > 0 {
				task, err := tx.GetTask(ctx, run.TaskID())
				if err != nil {
					return err
				}
				attempt, err := tx.GetCurrentAttempt(ctx, run.ID())
				if err != nil {
					return err
				}
				projection, err := tx.GetTerminalProjection(ctx, attempt.ID())
				if err != nil {
					return err
				}
				artifacts := make(map[domain.ArtifactID]storecontract.Artifact, len(projection.Artifacts))
				for _, artifact := range projection.Artifacts {
					artifacts[artifact.ID] = artifact
				}
				for _, proposal := range request.CandidateProposals {
					artifact, found := artifacts[proposal.ArtifactID]
					if !found || artifact.Role != "candidate" {
						return fmt.Errorf("%w: candidate proposal must reference a candidate-role artifact from the accepted Run", ErrInvalidCommand)
					}
					candidate, err := domain.NewCandidate(domain.CandidateParams{ID: s.deps.IDs.CandidateID(), RoomID: task.RoomID(), SourceRunID: run.ID(), SourceReviewID: decision.ID(), SourceArtifactID: proposal.ArtifactID, Title: proposal.Title, Body: proposal.Body, CreatedAt: now})
					if err != nil {
						return err
					}
					if err := tx.InsertCandidate(ctx, candidate); err != nil {
						return err
					}
					result.Candidates = append(result.Candidates, candidate)
				}
			}
		}
		event := "review.rejected"
		if request.Kind == domain.ReviewDecisionAccept {
			event = "review.accepted"
		}
		body := responseBody(s.event(event, next))
		if _, err := tx.AppendRunEvent(ctx, run.ID(), storecontract.EventDraft{ID: s.deps.IDs.EventID(), Type: event, Source: "app", OccurredAt: now, RecordedAt: now, NormalizedJSON: body}); err != nil {
			return err
		}
		result.Run = next
		result.Decision = decision
		response, err = reviewResponse(result)
		if err != nil {
			return err
		}
		if err := tx.SaveCommand(ctx, key, response); err != nil {
			return err
		}
		return nil
	})
	return result, err
}
