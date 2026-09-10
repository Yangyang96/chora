package app

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

type candidateCommandWire struct {
	Candidate  candidateWire
	DecisionID string
	RevisionID string
}

func (s *Service) candidateLock(id domain.CandidateID) func() {
	return s.candidateLocks.lock(id.String())
}

type candidateWire struct {
	ID, RoomID, SourceRunID, SourceReviewID, SourceArtifactID string
	Title, Body                                               string
	State                                                     domain.CandidateState
	Version                                                   uint64
	CreatedAt, UpdatedAt                                      time.Time
}

func wireCandidate(candidate domain.Candidate) candidateWire {
	return candidateWire{ID: candidate.ID().String(), RoomID: candidate.RoomID().String(), SourceRunID: candidate.SourceRunID().String(), SourceReviewID: candidate.SourceReviewID().String(), SourceArtifactID: candidate.SourceArtifactID().String(), Title: candidate.Title(), Body: candidate.Body(), State: candidate.State(), Version: candidate.Version(), CreatedAt: candidate.CreatedAt(), UpdatedAt: candidate.UpdatedAt()}
}

func restoreCandidate(w candidateWire) (domain.Candidate, error) {
	id, err := domain.ParseCandidateID(w.ID)
	if err != nil {
		return domain.Candidate{}, err
	}
	roomID, err := domain.ParseRoomID(w.RoomID)
	if err != nil {
		return domain.Candidate{}, err
	}
	runID, err := domain.ParseRunID(w.SourceRunID)
	if err != nil {
		return domain.Candidate{}, err
	}
	reviewID, err := domain.ParseReviewDecisionID(w.SourceReviewID)
	if err != nil {
		return domain.Candidate{}, err
	}
	artifactID, err := domain.ParseArtifactID(w.SourceArtifactID)
	if err != nil {
		return domain.Candidate{}, err
	}
	return domain.RestoreCandidate(domain.CandidateRecord{ID: id, RoomID: roomID, SourceRunID: runID, SourceReviewID: reviewID, SourceArtifactID: artifactID, Title: w.Title, Body: w.Body, State: w.State, Version: w.Version, CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt})
}

func (s *Service) EditCandidate(ctx context.Context, request EditCandidateRequest) (EditCandidateResult, error) {
	if err := s.authorize(ctx, request.CommandMeta, "edit_candidate", request.CandidateID.String(), request.ExpectedVersion); err != nil {
		return EditCandidateResult{}, err
	}
	unlock := s.candidateLock(request.CandidateID)
	defer unlock()
	now := s.deps.Clock.Now()
	key, err := s.commandKey(request.CommandMeta, "edit_candidate", request.CandidateID.String(), request.ExpectedVersion, struct{ Title, Body string }{request.Title, request.Body}, now)
	if err != nil {
		return EditCandidateResult{}, err
	}
	var result EditCandidateResult
	err = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		response, found, err := tx.LookupCommand(ctx, key)
		if err != nil {
			return err
		}
		if found {
			var wire candidateCommandWire
			if err := json.Unmarshal(response.Body, &wire); err != nil {
				return err
			}
			candidate, err := restoreCandidate(wire.Candidate)
			result = EditCandidateResult{Candidate: candidate, Replayed: true}
			return err
		}
		before, err := tx.GetCandidate(ctx, request.CandidateID)
		if err != nil {
			return err
		}
		after, err := before.Edit(request.ExpectedVersion, request.Title, request.Body, now)
		if err != nil {
			return err
		}
		if err := tx.SaveCandidateEditCAS(ctx, request.ExpectedVersion, before, after); err != nil {
			return err
		}
		result = EditCandidateResult{Candidate: after}
		stored, err := jsonResponse(candidateCommandWire{Candidate: wireCandidate(after)})
		if err != nil {
			return err
		}
		return tx.SaveCommand(ctx, key, stored)
	})
	return result, err
}

func (s *Service) DecideCandidate(ctx context.Context, request DecideCandidateRequest) (DecideCandidateResult, error) {
	action := "confirm_candidate"
	if request.Kind == domain.CandidateDecisionDismiss {
		action = "dismiss_candidate"
	} else if request.Kind != domain.CandidateDecisionConfirm {
		return DecideCandidateResult{}, fmt.Errorf("%w: invalid candidate decision", ErrInvalidCommand)
	}
	if err := s.authorize(ctx, request.CommandMeta, action, request.CandidateID.String(), request.ExpectedVersion); err != nil {
		return DecideCandidateResult{}, err
	}
	unlock := s.candidateLock(request.CandidateID)
	defer unlock()
	now := s.deps.Clock.Now()
	key, err := s.commandKey(request.CommandMeta, action, request.CandidateID.String(), request.ExpectedVersion, struct {
		Kind domain.CandidateDecisionKind
		Note string
	}{request.Kind, request.Note}, now)
	if err != nil {
		return DecideCandidateResult{}, err
	}
	var result DecideCandidateResult
	err = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		response, found, err := tx.LookupCommand(ctx, key)
		if err != nil {
			return err
		}
		if found {
			return replayCandidateDecision(ctx, tx, response, &result)
		}
		before, err := tx.GetCandidate(ctx, request.CandidateID)
		if err != nil {
			return err
		}
		decision, after, err := domain.NewCandidateDecision(s.deps.IDs.CandidateDecisionID(), before, request.Kind, request.ExpectedVersion, request.Note, request.ActorID, request.SessionID, now)
		if err != nil {
			return err
		}
		if err := tx.SaveCandidateDecisionCAS(ctx, before, after, decision); err != nil {
			return err
		}
		result = DecideCandidateResult{Candidate: after, Decision: decision}
		if request.Kind == domain.CandidateDecisionConfirm {
			contextRevision, err := domain.NewRoomContextRevision(domain.RoomContextRevisionParams{
				EntryID: s.deps.IDs.ContextEntryID(), RevisionID: s.deps.IDs.ContextRevisionID(), RoomID: after.RoomID(), Kind: domain.ContextKindDecision,
				RevisionNumber: 1, Title: after.Title(), Body: after.Body(), Locator: "candidate://" + after.ID().String(), CreatedAt: now, UpdatedAt: now,
			})
			if err != nil {
				return err
			}
			revision, err := domain.NewRoomRevision(contextRevision, domain.CandidateProvenance(after, decision, request.ActorID), now)
			if err != nil {
				return err
			}
			if err := tx.InsertRevision(ctx, contextRevision); err != nil {
				return err
			}
			if err := tx.InsertRoomRevision(ctx, revision); err != nil {
				return err
			}
			result.Revision = &revision
		}
		wire := candidateCommandWire{Candidate: wireCandidate(after), DecisionID: decision.ID().String()}
		if result.Revision != nil {
			wire.RevisionID = result.Revision.ID().String()
		}
		stored, err := jsonResponse(wire)
		if err != nil {
			return err
		}
		return tx.SaveCommand(ctx, key, stored)
	})
	return result, err
}

func replayCandidateDecision(ctx context.Context, reader storecontract.Reader, response storecontract.Response, result *DecideCandidateResult) error {
	var wire candidateCommandWire
	if err := json.Unmarshal(response.Body, &wire); err != nil {
		return err
	}
	candidate, err := restoreCandidate(wire.Candidate)
	if err != nil {
		return err
	}
	decision, err := reader.GetCandidateDecision(ctx, candidate.ID())
	if err != nil {
		return err
	}
	*result = DecideCandidateResult{Candidate: candidate, Decision: decision, Replayed: true}
	if wire.RevisionID != "" {
		revisionID, err := domain.ParseContextRevisionID(wire.RevisionID)
		if err != nil {
			return err
		}
		revision, err := reader.GetRoomRevision(ctx, revisionID)
		if err != nil {
			return err
		}
		result.Revision = &revision
	}
	return nil
}
