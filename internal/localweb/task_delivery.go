package localweb

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/gitsource"
	storecontract "github.com/Yangyang96/chora/internal/store"
	"github.com/Yangyang96/chora/internal/taskdelivery"
)

type taskDeliveryWorkspaceResolver struct{ manager *taskResourceWorkspaceManager }

func newTaskDeliveryWorkspaceResolver(resolver app.TaskResourceWorkspaceResolver) app.TaskDeliveryWorkspaces {
	manager, ok := resolver.(*taskResourceWorkspaceManager)
	if !ok {
		return nil
	}
	return taskDeliveryWorkspaceResolver{manager}
}
func (r taskDeliveryWorkspaceResolver) DeliveryRoot(ctx context.Context, taskID domain.TaskID, resource domain.TaskRepositoryResource) (string, error) {
	if r.manager == nil {
		return "", taskdelivery.ErrUnsupported
	}
	return r.deliveryRoot(ctx, r.manager.store.Reader(), taskID, resource)
}
func (r taskDeliveryWorkspaceResolver) DeliveryCleanupRoot(ctx context.Context, taskID domain.TaskID, resource domain.TaskRepositoryResource) (string, error) {
	return r.resolveDeliveryRoot(ctx, r.manager.store.Reader(), taskID, resource, true)
}
func (r taskDeliveryWorkspaceResolver) deliveryRoot(ctx context.Context, reader storecontract.Reader, taskID domain.TaskID, resource domain.TaskRepositoryResource) (string, error) {
	return r.resolveDeliveryRoot(ctx, reader, taskID, resource, false)
}
func (r taskDeliveryWorkspaceResolver) resolveDeliveryRoot(ctx context.Context, reader storecontract.Reader, taskID domain.TaskID, resource domain.TaskRepositoryResource, allowMissing bool) (string, error) {
	m := r.manager
	if m == nil {
		return "", taskdelivery.ErrUnsupported
	}
	if e := m.proveTaskRoot(taskID, resource); e != nil {
		return "", e
	}
	identity, e := (gitsource.Default{}).Inspect(ctx, resource.Checkout)
	if e != nil || identity.CanonicalPath != resource.Checkout || identity.CommonGitDir != resource.CommonGitDir || identity.PhysicalIdentity != resource.PhysicalIdentity {
		return "", taskdelivery.ErrConflict
	}
	rows, e := reader.ListTaskRepositoryWorktrees(ctx, taskID)
	if e != nil {
		return "", e
	}
	for _, row := range rows {
		if row.RepositoryID.String() != resource.RepoID {
			continue
		}
		if row.State != "ready" || !m.rowMatches(taskID, resource, row) {
			return "", taskdelivery.ErrConflict
		}
		path := filepath.Join(m.dataRoot, row.RelativePath)
		info, e := os.Lstat(path)
		if allowMissing && os.IsNotExist(e) {
			return path, nil
		}
		if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", taskdelivery.ErrConflict
		}
		canonical, e := filepath.EvalSymlinks(path)
		if e != nil || canonical != path {
			return "", taskdelivery.ErrConflict
		}
		common, e := taskWorktreeCommonDirectory(ctx, path)
		if e != nil || common != resource.CommonGitDir {
			return "", taskdelivery.ErrConflict
		}
		return path, nil
	}
	return "", taskdelivery.ErrConflict
}
func (server *Server) getTaskDelivery(w http.ResponseWriter, r *http.Request) {
	id, e := domain.ParseRunID(r.PathValue("runID"))
	if e != nil {
		writeProjectError(w, e)
		return
	}
	v, e := server.service.LoadTaskDelivery(r.Context(), id)
	if e != nil {
		writeDeliveryError(w, e)
		return
	}
	writeJSON(w, 200, v)
}
func (server *Server) taskDeliveryCommand(w http.ResponseWriter, r *http.Request) {

	id, e := domain.ParseRunID(r.PathValue("runID"))
	if e != nil {
		writeProjectError(w, e)
		return
	}
	var input struct {
		RepoID          string `json:"repoId"`
		ExpectedVersion uint64 `json:"expectedVersion"`
		ResultDigest    string `json:"resultDigest"`
		Kind            string `json:"kind"`
		Message         string `json:"message"`
		Remote          string `json:"remote"`
		Title           string `json:"title"`
		Body            string `json:"body"`
		OperationID     string `json:"operationId"`
	}
	if e = decodeJSON(r, &input); e != nil {
		writeError(w, 400, e)
		return
	}
	action := r.PathValue("action")
	if action != "preview" && action != "confirm" && action != "refresh" {
		writeError(w, 404, errors.New("delivery action not found"))
		return
	}
	meta, ok := requestCommandMetaForProduct(w, r, "task-delivery-"+action, server.product)
	if !ok {
		return
	}
	req := app.DeliveryRequest{CommandMeta: meta, RunID: id, ExpectedVersion: input.ExpectedVersion, ResultDigest: input.ResultDigest, RepoID: input.RepoID, Kind: input.Kind, Message: input.Message, Remote: input.Remote, Title: input.Title, Body: input.Body, OperationID: input.OperationID}
	var result any
	switch action {
	case "preview":
		result, e = server.service.PreviewTaskDelivery(r.Context(), req)
	case "confirm":
		result, e = server.service.ConfirmTaskDelivery(r.Context(), req)
	case "refresh":
		result, e = server.service.RefreshTaskDelivery(r.Context(), req)
	}
	if e != nil {
		writeDeliveryError(w, e)
		return
	}
	writeJSON(w, 200, result)
}
func writeDeliveryError(w http.ResponseWriter, e error) {
	if errors.Is(e, taskdelivery.ErrUnsupported) {
		writeError(w, http.StatusUnprocessableEntity, e)
		return
	}
	if errors.Is(e, taskdelivery.ErrConflict) || errors.Is(e, taskdelivery.ErrRecovery) {
		writeError(w, http.StatusConflict, e)
		return
	}
	writeMutationConflictError(w, e)
}

// Review uses the same immutable workspace ownership proof as delivery, but only
// reads the working directory and index. No temporary staging or commit occurs.
type resourceReviewVerifier struct{ resolver taskDeliveryWorkspaceResolver }

func newResourceReviewVerifier(resolver app.TaskResourceWorkspaceResolver) app.ResourceReviewVerifier {
	manager, ok := resolver.(*taskResourceWorkspaceManager)
	if !ok {
		return nil
	}
	return resourceReviewVerifier{resolver: taskDeliveryWorkspaceResolver{manager}}
}
func (v resourceReviewVerifier) Verify(ctx context.Context, reader storecontract.Reader, taskID domain.TaskID, snapshot domain.TaskResourceSnapshot, view app.ResourceReviewView) error {
	for i, resource := range snapshot.Resources {
		if resource.DeliveryMode != domain.TaskResourceDeliveryTaskBranch {
			continue
		}
		root, err := v.resolver.deliveryRoot(ctx, reader, taskID, resource)
		if err != nil {
			return err
		}
		binding := taskdelivery.Binding{Root: root, CommonGitDir: resource.CommonGitDir, BaseCommit: resource.BaseCommit, BaseTree: resource.BaseTree, Branch: resource.TaskBranch, TargetRef: resource.BaseRef}
		if i >= len(view.Patches) || view.Patches[i].RepoID != resource.RepoID {
			return app.ErrReviewEvidenceUnavailable
		}
		paths := make([]string, 0, len(view.Patches[i].Patch.Files))
		for _, file := range view.Patches[i].Patch.Files {
			paths = append(paths, file.Path)
		}
		if err = (taskdelivery.Git{}).VerifyReviewedChanges(ctx, binding, view.Patches[i].Patch.Raw, paths); err != nil {
			return err
		}
	}
	return nil
}
