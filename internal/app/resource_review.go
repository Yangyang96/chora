package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

// ResourceReviewVerifier proves live Task-branch contents at acceptance. The
// supplied reader is the active transaction; implementations must not open a
// separate database read while holding the review write transaction.
type ResourceReviewVerifier interface {
	Verify(context.Context, storecontract.Reader, domain.TaskID, domain.TaskResourceSnapshot, ResourceReviewView) error
}

type ResourceReviewRepository struct {
	WorktreePath string `json:"worktreePath,omitempty"`
	RepoID       string `json:"repoId"`
	Name         string `json:"name"`
	BaseRef      string `json:"baseRef"`
	DeliveryMode string `json:"deliveryMode,omitempty"`
	TaskBranch   string `json:"taskBranch,omitempty"`
}

type ResourceReviewView struct {
	Repositories []ResourceReviewRepository `json:"repositories"`
	Group        domain.ResourceResultGroup `json:"group"`
	Digest       string                     `json:"digest"`
	Patches      []ResourceReviewPatch      `json:"-"`
}

func (s *Service) LoadResourceReview(ctx context.Context, runID domain.RunID) (ResourceReviewView, error) {
	view, err := s.loadResourceReview(ctx, s.deps.Store.Reader(), runID)
	if err != nil || s.deps.TaskDeliveryWorkspaces == nil {
		return view, err
	}
	taskID, err := domain.ParseTaskID(view.Group.TaskID)
	if err != nil {
		return view, err
	}
	record, err := s.deps.Store.Reader().GetTaskResourceSnapshot(ctx, taskID)
	if err != nil {
		return view, err
	}
	var snapshot domain.TaskResourceSnapshot
	if err = json.Unmarshal(record.CanonicalJSON, &snapshot); err != nil {
		return view, err
	}
	for i, resource := range snapshot.Resources {
		if resource.DeliveryMode != domain.TaskResourceDeliveryTaskBranch {
			continue
		}
		// Optional display metadata, outside the immutable Result digest. A missing
		// or drifted directory never changes the review evidence or invents a path.
		if root, e := s.deps.TaskDeliveryWorkspaces.DeliveryRoot(ctx, taskID, resource); e == nil {
			view.Repositories[i].WorktreePath = root
		}
	}
	return view, nil
}
func (s *Service) loadResourceReview(ctx context.Context, reader storecontract.Reader, runID domain.RunID) (ResourceReviewView, error) {
	run, e := reader.GetRun(ctx, runID)
	if e != nil {
		return ResourceReviewView{}, e
	}
	a, e := reader.GetCurrentAttempt(ctx, runID)
	if e != nil {
		return ResourceReviewView{}, e
	}
	g, e := reader.GetResourceResultGroup(ctx, a.ID())
	if e != nil {
		return ResourceReviewView{}, e
	}
	if g.RunID != runID.String() || g.TaskID != run.TaskID().String() {
		return ResourceReviewView{}, ErrReviewEvidenceUnavailable
	}
	_, d, e := g.CanonicalJSON()
	if e != nil {
		return ResourceReviewView{}, e
	}
	record, e := reader.GetTaskResourceSnapshot(ctx, run.TaskID())
	if e != nil {
		return ResourceReviewView{}, e
	}
	if hex.EncodeToString(record.Digest[:]) != g.ResourceSnapshotDigest {
		return ResourceReviewView{}, ErrReviewEvidenceUnavailable
	}
	var snapshot domain.TaskResourceSnapshot
	if e = json.Unmarshal(record.CanonicalJSON, &snapshot); e != nil {
		return ResourceReviewView{}, e
	}
	if len(snapshot.Resources) != len(g.Repositories) || s.deps.ResourcePatchMaterializer == nil {
		return ResourceReviewView{}, ErrReviewEvidenceUnavailable
	}
	view := ResourceReviewView{Group: g, Digest: hex.EncodeToString(d[:])}
	for _, resource := range snapshot.Resources {
		view.Repositories = append(view.Repositories, ResourceReviewRepository{RepoID: resource.RepoID, Name: resource.Name, BaseRef: resource.BaseRef, DeliveryMode: resource.DeliveryMode, TaskBranch: resource.TaskBranch})
	}
	for i, r := range g.Repositories {
		if snapshot.Resources[i].RepoID != r.RepoID {
			return ResourceReviewView{}, ErrReviewEvidenceUnavailable
		}
		digestBytes, e := hex.DecodeString(r.PatchDigest)
		if e != nil || len(digestBytes) != 32 {
			return ResourceReviewView{}, ErrReviewEvidenceUnavailable
		}
		var digest [32]byte
		copy(digest[:], digestBytes)
		if len(r.ChangedPaths) == 0 {
			if digest != sha256.Sum256(nil) || r.PatchLocator != "" {
				return ResourceReviewView{}, ErrReviewEvidenceUnavailable
			}
			view.Patches = append(view.Patches, ResourceReviewPatch{RepoID: r.RepoID, Patch: ReviewablePatch{PatchDigest: digest}})
			continue
		}
		raw, e := s.deps.ResourcePatchMaterializer.Read(ctx, a.ID(), r.RepoID, digest)
		if e != nil {
			return ResourceReviewView{}, e
		}
		patch, e := BuildResourceReviewablePatch(snapshot.Resources[i], r.ChangedPaths, raw, digest)
		if e != nil {
			return ResourceReviewView{}, e
		}
		view.Patches = append(view.Patches, ResourceReviewPatch{RepoID: r.RepoID, Patch: patch})
	}
	return view, nil
}

type ResourceReviewRequest struct {
	ReviewRequest
	ResultDigest string
}

func (s *Service) ReviewResourceResult(ctx context.Context, request ResourceReviewRequest) (ReviewResult, error) {
	if e := s.authorize(ctx, request.CommandMeta, "review_resource_result", request.RunID.String(), request.ExpectedVersion); e != nil {
		return ReviewResult{}, e
	}
	if s.deps.Presence == nil {
		return ReviewResult{}, domain.ErrUnauthorizedReview
	}
	auth, e := s.deps.Presence.AuthorizeReview(ctx, PresenceRequest{ActorID: request.ActorID, SessionID: request.SessionID, RunID: request.RunID, Kind: request.Kind, ExpectedVersion: request.ExpectedVersion})
	if e != nil {
		return ReviewResult{}, e
	}
	unlock := s.runLock(request.RunID)
	defer unlock()
	now := s.deps.Clock.Now()
	key, e := s.commandKey(request.CommandMeta, "review_resource_result", request.RunID.String(), request.ExpectedVersion, request, now)
	if e != nil {
		return ReviewResult{}, e
	}
	var result ReviewResult
	e = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		response, replay, e := tx.LookupCommand(ctx, key)
		if e != nil {
			return e
		}
		if replay {
			result, e = replayReview(ctx, response, tx)
			return e
		}
		if e := requireRunWithoutClosure(ctx, tx, request.RunID); e != nil {
			return e
		}
		run, e := tx.GetRun(ctx, request.RunID)
		if e != nil {
			return e
		}
		if run.Version() != request.ExpectedVersion {
			return storecontract.ErrVersionConflict
		}
		view, e := s.loadResourceReview(ctx, tx, run.ID())
		if e != nil {
			return e
		}
		if view.Digest != request.ResultDigest || view.Group.Outcome != "review_ready" {
			return ErrReviewEvidenceUnavailable
		}
		if request.Kind == domain.ReviewDecisionAccept {
			record, err := tx.GetTaskResourceSnapshot(ctx, run.TaskID())
			if err != nil {
				return err
			}
			var snapshot domain.TaskResourceSnapshot
			if err = json.Unmarshal(record.CanonicalJSON, &snapshot); err != nil {
				return err
			}
			branchMode := false
			for _, resource := range snapshot.Resources {
				if resource.DeliveryMode == domain.TaskResourceDeliveryTaskBranch {
					branchMode = true
				}
			}
			if branchMode {
				if s.deps.ResourceReviewVerifier == nil {
					return ErrReviewEvidenceUnavailable
				}
				if err = s.deps.ResourceReviewVerifier.Verify(ctx, tx, run.TaskID(), snapshot, view); err != nil {
					return fmt.Errorf("%w: Task worktree differs from the reviewed result; generate a new result before accepting", ErrReviewEvidenceUnavailable)
				}
			}
		}
		params := domain.ReviewDecisionParams{ID: s.deps.IDs.ReviewID(), RunID: run.ID(), ExpectedRunVersion: run.Version(), Comment: request.Comment, DecidedAt: now}
		var decision domain.ReviewDecision
		switch request.Kind {
		case domain.ReviewDecisionAccept:
			decision, e = domain.NewAcceptedReviewDecision(params, auth)
		case domain.ReviewDecisionReject:
			decision, e = domain.NewRejectedReviewDecision(params, auth)
		default:
			return domain.ErrInvalidArgument
		}
		if e != nil {
			return e
		}
		next, e := run.ApplyReview(decision)
		if e != nil {
			return e
		}
		if e = tx.InsertReview(ctx, decision); e != nil {
			return e
		}
		id, _ := domain.ParseResultID(view.Group.ID)
		_, d, _ := view.Group.CanonicalJSON()
		if e = tx.InsertResourceResultReview(ctx, id, d, decision.ID()); e != nil {
			return e
		}
		if e = tx.SaveRunCAS(ctx, run.Version(), next); e != nil {
			return e
		}
		event := "resource_review." + string(request.Kind)
		if _, e = tx.AppendRunEvent(ctx, run.ID(), storecontract.EventDraft{ID: s.deps.IDs.EventID(), Type: event, Source: "app", OccurredAt: now, RecordedAt: now, NormalizedJSON: responseBody(map[string]any{"result_id": id.String(), "result_digest": view.Digest})}); e != nil {
			return e
		}
		result = ReviewResult{Run: next, Decision: decision}
		response, e = reviewResponse(result)
		if e != nil {
			return e
		}
		return tx.SaveCommand(ctx, key, response)
	})
	return result, e
}

// Legacy Review may not bypass the resource Result's exact evidence binding.
func requireLegacyReview(ctx context.Context, reader storecontract.Reader, taskID domain.TaskID) error {
	if _, e := reader.GetTaskResourceSnapshot(ctx, taskID); e == nil {
		return ErrReviewEvidenceUnavailable
	} else if !errors.Is(e, storecontract.ErrNotFound) {
		return e
	}
	return nil
}
