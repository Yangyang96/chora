package app

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
	"github.com/Yangyang96/chora/internal/taskdelivery"
)

func (s *Service) closedCleanupAuthority(ctx context.Context, req DeliveryRequest) (domain.AgentRun, ResourceReviewView, domain.TaskRepositoryResource, storecontract.ResultClosure, error) {
	var resource domain.TaskRepositoryResource
	var closure storecontract.ResultClosure
	run, err := s.deps.Store.Reader().GetRun(ctx, req.RunID)
	if err != nil {
		return run, ResourceReviewView{}, resource, closure, err
	}
	fail := func(e error) (domain.AgentRun, ResourceReviewView, domain.TaskRepositoryResource, storecontract.ResultClosure, error) {
		return run, ResourceReviewView{}, resource, closure, e
	}
	if run.Version() != req.ExpectedVersion {
		return fail(storecontract.ErrVersionConflict)
	}
	view, err := s.loadResourceReview(ctx, s.deps.Store.Reader(), run.ID())
	if err != nil {
		return fail(err)
	}
	if view.Digest != req.ResultDigest {
		return fail(ErrReviewEvidenceUnavailable)
	}
	id, err := domain.ParseResultID(view.Group.ID)
	if err != nil {
		return fail(err)
	}
	closure, err = s.deps.Store.Reader().GetResultClosure(ctx, id)
	if err != nil {
		return fail(err)
	}
	eligible := false
	for _, repo := range closure.RepositoryIDs {
		if repo == req.RepoID {
			eligible = true
		}
	}
	if !eligible || hex.EncodeToString(closure.ResultDigest[:]) != view.Digest {
		return fail(ErrReviewEvidenceUnavailable)
	}
	snapshot, err := s.deliverySnapshot(ctx, run.TaskID())
	if err != nil {
		return fail(err)
	}
	for _, repo := range snapshot.Resources {
		if repo.RepoID == req.RepoID {
			resource = repo
			break
		}
	}
	if resource.RepoID == "" || resource.Role != "write" || resource.DeliveryMode != "task_branch" {
		return fail(taskdelivery.ErrUnsupported)
	}
	task, err := s.deps.Store.Reader().GetTask(ctx, run.TaskID())
	if err != nil {
		return fail(err)
	}
	history, err := s.deps.Store.Reader().ListTaskRunHistory(ctx, task.RoomID(), task.ID())
	if err != nil {
		return fail(err)
	}
	for _, item := range history {
		switch item.Run.State() {
		case domain.RunStateReady, domain.RunStateRunning, domain.RunStateStopping, domain.RunStateAwaitingVerification, domain.RunStateVerifying, domain.RunStateVerificationRecoveryRequired:
			return fail(ErrResultClosureBlocked)
		}
	}
	return run, view, resource, closure, nil
}

func (s *Service) PreviewClosedResultCleanup(ctx context.Context, req DeliveryRequest) (DeliveryOperationView, error) {
	if err := s.authorize(ctx, req.CommandMeta, "preview_closed_result_cleanup", req.RunID.String(), req.ExpectedVersion); err != nil {
		return DeliveryOperationView{}, err
	}
	unlock := s.runLock(req.RunID)
	defer unlock()
	run, view, resource, closure, err := s.closedCleanupAuthority(ctx, req)
	if err != nil {
		return DeliveryOperationView{}, err
	}
	release := resourceApplyLocks.lock(resource.PhysicalIdentity)
	defer release()
	key, err := s.commandKey(req.CommandMeta, "preview_closed_result_cleanup", req.RunID.String(), req.ExpectedVersion, req, s.deps.Clock.Now())
	if err != nil {
		return DeliveryOperationView{}, err
	}
	id := "delivery_" + hex.EncodeToString(key.KeyHash[:])
	if old, e := s.deps.Store.Reader().GetDeliveryOperation(ctx, id); e == nil {
		if old.RequestDigest != key.RequestDigest {
			return DeliveryOperationView{}, storecontract.ErrIdempotencyConflict
		}
		return deliveryOperationView(old)
	} else if !errors.Is(e, storecontract.ErrNotFound) {
		return DeliveryOperationView{}, e
	}
	ops, err := s.deps.Store.Reader().ListDeliveryOperations(ctx, run.TaskID())
	if err != nil {
		return DeliveryOperationView{}, err
	}
	for _, op := range ops {
		if op.State == "writing" || op.State == "recovery_required" || (op.RepositoryID.String() == req.RepoID && op.State == "succeeded") {
			return DeliveryOperationView{}, taskdelivery.ErrConflict
		}
	}
	binding, err := s.deliveryBinding(ctx, run.TaskID(), resource)
	if err != nil {
		return DeliveryOperationView{}, err
	}
	var patch ReviewablePatch
	for _, p := range view.Patches {
		if p.RepoID == req.RepoID {
			patch = p.Patch
		}
	}
	paths := []string{}
	for _, file := range patch.Files {
		paths = append(paths, file.Path)
	}
	preview, err := (taskdelivery.Git{}).PreviewResultCleanup(ctx, binding, patch.Raw, paths)
	if err != nil {
		return DeliveryOperationView{}, err
	}
	intent := deliveryIntent{ActorID: req.ActorID, SessionID: req.SessionID, ResultDigest: view.Digest, ReviewID: closure.ReviewID, ClosedResultID: closure.ResultID.String(), ResultCleanup: &preview, Binding: binding}
	repoID, _ := domain.ParseRepositoryID(req.RepoID)
	now := s.deps.Clock.Now()
	op := storecontract.DeliveryOperation{ID: id, TaskID: run.TaskID(), RunID: run.ID(), RepositoryID: repoID, Kind: "cleanup", State: "preview", Version: 1, RequestDigest: key.RequestDigest, PreviewJSON: responseBody(intent), OutcomeJSON: responseBody(deliveryOutcome{}), CreatedAt: now, UpdatedAt: now}
	err = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.SaveDeliveryOperation(ctx, 0, op) })
	if err != nil {
		return DeliveryOperationView{}, err
	}
	return deliveryOperationView(op)
}

func (s *Service) ConfirmClosedResultCleanup(ctx context.Context, req DeliveryRequest) (DeliveryOperationView, error) {
	if err := s.authorize(ctx, req.CommandMeta, "confirm_closed_result_cleanup", req.RunID.String(), req.ExpectedVersion); err != nil {
		return DeliveryOperationView{}, err
	}
	unlock := s.runLock(req.RunID)
	defer unlock()
	op, err := s.deps.Store.Reader().GetDeliveryOperation(ctx, req.OperationID)
	if err != nil {
		return DeliveryOperationView{}, err
	}
	if op.RunID != req.RunID || op.Kind != "cleanup" {
		return DeliveryOperationView{}, ErrInvalidCommand
	}
	req.RepoID = op.RepositoryID.String()
	run, _, resource, closure, err := s.closedCleanupAuthority(ctx, req)
	if err != nil {
		return DeliveryOperationView{}, err
	}
	release := resourceApplyLocks.lock(resource.PhysicalIdentity)
	defer release()
	var intent deliveryIntent
	if err = json.Unmarshal(op.PreviewJSON, &intent); err != nil {
		return DeliveryOperationView{}, err
	}
	if intent.ResultCleanup == nil || intent.ClosedResultID != closure.ResultID.String() || intent.ResultDigest != req.ResultDigest || intent.ActorID != req.ActorID || intent.SessionID != req.SessionID || intent.ReviewID != closure.ReviewID {
		return DeliveryOperationView{}, ErrReviewEvidenceUnavailable
	}
	binding, err := s.deliveryBindingForRecovery(ctx, run.TaskID(), resource, "cleanup")
	if err != nil {
		return DeliveryOperationView{}, err
	}
	if binding != intent.Binding || binding != intent.ResultCleanup.Binding {
		return DeliveryOperationView{}, taskdelivery.ErrConflict
	}
	if err = s.recordDeliveryConfirmation(ctx, req, op); err != nil {
		return DeliveryOperationView{}, err
	}
	if op.State == "succeeded" || op.State == "failed" {
		return deliveryOperationView(op)
	}
	if op.State == "writing" || op.State == "recovery_required" {
		if err = (taskdelivery.Git{}).ReconcileResultCleanup(ctx, *intent.ResultCleanup); err != nil {
			return deliveryOperationView(op)
		}
	} else {
		if err = s.saveDeliveryState(ctx, &op, "writing", deliveryOutcome{}); err != nil {
			return DeliveryOperationView{}, err
		}
		err = (taskdelivery.Git{}).CleanupClosedResult(ctx, *intent.ResultCleanup)
	}
	state, outcome := "succeeded", deliveryOutcome{}
	if err != nil {
		state, outcome.Reason = "recovery_required", err.Error()
		if errors.Is(err, taskdelivery.ErrConflict) || errors.Is(err, taskdelivery.ErrUnsupported) {
			state = "failed"
		}
	}
	if err = s.saveDeliveryState(context.WithoutCancel(ctx), &op, state, outcome); err != nil {
		return DeliveryOperationView{}, err
	}
	return deliveryOperationView(op)
}
