package localweb

import (
	"context"
	"errors"
	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"net/http"
)

type resourceResultView struct {
	Repositories []app.ResourceReviewRepository `json:"repositories"`
	Group        domain.ResourceResultGroup     `json:"group"`
	Digest       string                         `json:"digest"`
	Patches      []resourceResultPatchView      `json:"patches"`
}
type resourceResultPatchView struct {
	RepoID string                    `json:"repoId"`
	Files  []reviewablePatchFileView `json:"files"`
}

func resourceResultViewOf(view app.ResourceReviewView) resourceResultView {
	result := resourceResultView{Repositories: view.Repositories, Group: view.Group, Digest: view.Digest, Patches: []resourceResultPatchView{}}
	for _, p := range view.Patches {
		result.Patches = append(result.Patches, resourceResultPatchView{RepoID: p.RepoID, Files: patchFilesView(p.Patch)})
	}
	return result
}
func (server *Server) getResourceResult(w http.ResponseWriter, r *http.Request) {
	id, e := domain.ParseRunID(r.PathValue("runID"))
	if e != nil {
		writeProjectError(w, e)
		return
	}
	view, e := server.service.LoadResourceReview(r.Context(), id)
	if e != nil {
		writeProjectError(w, e)
		return
	}
	writeJSON(w, http.StatusOK, resourceResultViewOf(view))
}
func (server *Server) reviewResourceResult(w http.ResponseWriter, r *http.Request) {
	id, e := domain.ParseRunID(r.PathValue("runID"))
	if e != nil {
		writeProjectError(w, e)
		return
	}
	var input struct {
		ExpectedVersion uint64                    `json:"expectedVersion"`
		Kind            domain.ReviewDecisionKind `json:"kind"`
		reviewCommentInput
		ResultDigest string `json:"resultDigest"`
	}
	if e = decodeJSON(r, &input); e != nil {
		writeError(w, 400, e)
		return
	}
	comment, e := input.value()
	if e != nil {
		writeError(w, 400, e)
		return
	}
	meta, ok := requestCommandMetaForProduct(w, r, "review-resource-result", server.product)
	if !ok {
		return
	}
	_, e = server.service.ReviewResourceResult(r.Context(), app.ResourceReviewRequest{ReviewRequest: app.ReviewRequest{CommandMeta: meta, RunID: id, ExpectedVersion: input.ExpectedVersion, Kind: input.Kind, Comment: comment}, ResultDigest: input.ResultDigest})
	if e != nil {
		writeMutationConflictError(w, e)
		return
	}
	view, e := server.runView(r.Context(), id)
	if e != nil {
		writeStoreError(w, e)
		return
	}
	writeJSON(w, 200, view)
}
func (server *Server) attachResourceResult(ctx context.Context, run domain.AgentRun, view *runView) error {
	attempt, e := server.store.Reader().GetCurrentAttempt(ctx, run.ID())
	if e != nil {
		return nil
	}
	if _, e = server.store.Reader().GetResourceResultGroup(ctx, attempt.ID()); e != nil {
		return nil
	}
	loaded, e := server.service.LoadResourceReview(ctx, run.ID())
	if e != nil {
		view.Controls.CanReview = false
		view.Controls.CanAcceptAndApply = false
		view.Blockers = append(view.Blockers, "Resource Result evidence unavailable: "+e.Error())
		return nil
	}
	result := resourceResultViewOf(loaded)
	view.ResourceResult = &result
	view.Controls.CanReview = run.State() == domain.RunStateAwaitingReview && loaded.Group.Outcome == "review_ready"
	view.Controls.CanAcceptAndApply = view.Controls.CanReview && server.service.ResourceApplyAvailable()
	for _, repository := range loaded.Repositories {
		if repository.DeliveryMode == domain.TaskResourceDeliveryTaskBranch {
			view.Controls.CanAcceptAndApply = false
			break
		}
	}
	view.Controls.CanApplyPatch = run.State() == domain.RunStateAccepted && server.service.ResourceApplyAvailable()
	if !server.deliveryCommandsEnabled {
		for _, repository := range loaded.Repositories {
			if repository.DeliveryMode == domain.TaskResourceDeliveryTaskBranch {
				view.Controls.CanApplyPatch = false
				break
			}
		}
	}
	resultID, _ := domain.ParseResultID(loaded.Group.ID)
	if application, err := server.service.LoadResourceApply(ctx, resultID); err == nil {
		view.ResourceApply = &application
		view.Controls.CanApplyPatch = view.Controls.CanApplyPatch && application.Status != "applied"
	}
	return nil
}
func (server *Server) applyResourceResult(w http.ResponseWriter, r *http.Request) {
	id, e := domain.ParseRunID(r.PathValue("runID"))
	if e != nil {
		writeProjectError(w, e)
		return
	}
	var input struct {
		ExpectedVersion uint64 `json:"expectedVersion"`
		ResultDigest    string `json:"resultDigest"`
	}
	if e = decodeJSON(r, &input); e != nil {
		writeError(w, 400, e)
		return
	}
	meta, ok := requestCommandMetaForProduct(w, r, "apply-resource-result", server.product)
	if !ok {
		return
	}
	if !server.deliveryCommandsEnabled {
		result, err := server.service.LoadResourceReview(r.Context(), id)
		if err != nil {
			writeMutationConflictError(w, err)
			return
		}
		for _, repo := range result.Repositories {
			if repo.DeliveryMode == domain.TaskResourceDeliveryTaskBranch {
				writeError(w, http.StatusForbidden, errors.New("Task branch changes stay in their worktree after Review; original-checkout Apply is not part of this stage"))
				return
			}
		}
	}
	_, e = server.service.ApplyResourceResult(r.Context(), app.ResourceApplyRequest{ApplyAcceptedPatchRequest: app.ApplyAcceptedPatchRequest{CommandMeta: meta, RunID: id, ExpectedVersion: input.ExpectedVersion}, ResultDigest: input.ResultDigest})
	if e != nil {
		writeMutationConflictError(w, e)
		return
	}
	view, e := server.runView(r.Context(), id)
	if e != nil {
		writeStoreError(w, e)
		return
	}
	writeJSON(w, 200, view)
}
