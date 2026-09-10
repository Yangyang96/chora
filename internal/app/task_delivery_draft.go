package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/Yangyang96/chora/internal/taskdelivery"
)

type DeliveryDraftContext struct {
	TaskTitle   string                              `json:"taskTitle"`
	TaskGoal    string                              `json:"taskGoal"`
	Repository  string                              `json:"repository"`
	Patch       string                              `json:"reviewedDiff"`
	Checks      json.RawMessage                     `json:"checks"`
	Kind        string                              `json:"kind"`
	Source      taskdelivery.DraftRepositoryContext `json:"source"`
	Fingerprint string                              `json:"fingerprint"`
}

func (s *Service) LoadDeliveryDraftContext(ctx context.Context, req DeliveryRequest) (DeliveryDraftContext, error) {
	var v DeliveryDraftContext
	if req.Kind != "commit" && req.Kind != "pr" {
		return v, ErrInvalidCommand
	}
	if err := s.authorize(ctx, req.CommandMeta, "suggest_task_delivery_draft", req.RunID.String(), req.ExpectedVersion); err != nil {
		return v, err
	}
	unlock := s.runLock(req.RunID)
	defer unlock()
	run, review, resource, _, err := s.deliveryAuthority(ctx, req)
	if err != nil {
		return v, err
	}
	release := resourceApplyLocks.lock(resource.PhysicalIdentity)
	defer release()
	binding, err := s.deliveryBinding(ctx, run.TaskID(), resource)
	if err != nil {
		return v, err
	}
	task, err := s.deps.Store.Reader().GetTask(ctx, run.TaskID())
	if err != nil {
		return v, err
	}
	v.TaskTitle, v.TaskGoal, v.Repository, v.Kind = task.Title(), task.Goal(), resource.Name, req.Kind
	var paths []string
	for i, patch := range review.Patches {
		if patch.RepoID == resource.RepoID {
			v.Patch = string(patch.Patch.Raw)
			v.Checks = review.Group.Repositories[i].Checks
			for _, file := range patch.Patch.Files {
				paths = append(paths, file.Path)
			}
		}
	}
	if v.Patch == "" {
		return v, ErrReviewEvidenceUnavailable
	}
	ops, err := s.deps.Store.Reader().ListDeliveryOperations(ctx, run.TaskID())
	if err != nil {
		return v, err
	}
	var committedTree, committedHead string
	for _, op := range ops {
		if op.RepositoryID.String() != req.RepoID {
			continue
		}
		if op.State == "writing" || op.State == "recovery_required" {
			return v, fmt.Errorf("%w: refresh the interrupted delivery before generating a draft", taskdelivery.ErrConflict)
		}
		if op.Kind == "commit" && op.State == "succeeded" {
			var in deliveryIntent
			var out deliveryOutcome
			if json.Unmarshal(op.PreviewJSON, &in) != nil || json.Unmarshal(op.OutcomeJSON, &out) != nil || in.Commit == nil || in.ResultDigest != req.ResultDigest || in.Binding != binding {
				return v, ErrReviewEvidenceUnavailable
			}
			committedTree, committedHead = in.Commit.Tree, out.Commit
		}
	}
	var head, base string
	if req.Kind == "commit" {
		if committedHead != "" {
			return v, taskdelivery.ErrConflict
		}
		if err := (taskdelivery.Git{}).VerifyReviewedChanges(ctx, binding, []byte(v.Patch), paths); err != nil {
			return v, err
		}
	} else {
		// A hosting preview is a read, not a persisted delivery intent or write.
		request := req
		request.Title = "Prepare pull request description"
		preview, _, err := s.prepareHosting(ctx, request, "draft-context", binding, ops)
		if err != nil {
			return v, err
		}
		if committedHead == "" || preview.Head != committedHead {
			return v, ErrReviewEvidenceUnavailable
		}
		head, base = preview.Head, preview.BaseHead
	}
	v.Source, err = (taskdelivery.Git{}).ReadDraftContext(ctx, binding, head, base, committedTree)
	if err != nil {
		return v, err
	}
	if req.Kind == "pr" {
		v.Patch = v.Source.Diff
		v.Source.Diff = ""
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return v, err
	}
	digest := sha256.Sum256(append([]byte(req.RunID.String()+"\x00"+req.RepoID+"\x00"+req.ResultDigest+"\x00"+binding.Branch+"\x00"+binding.TargetRef+"\x00"), raw...))
	v.Fingerprint = hex.EncodeToString(digest[:])
	return v, nil
}
