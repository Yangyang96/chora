package localweb

import (
	"context"
	"fmt"

	"github.com/Yangyang96/chora/internal/app"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

// taskScopedGitPatchTarget resolves every Patch through the Run's Task-owned
// durable worktree binding. It never falls back to a process-wide working tree.
type taskScopedGitPatchTarget struct {
	manager *taskWorktreeManager
	reader  storecontract.Reader
}

func (target *taskScopedGitPatchTarget) Inspect(ctx context.Context, request app.PatchTargetRequest) (app.PatchTargetInspection, error) {
	resolved, inspection, err := target.resolve(ctx, request)
	if err != nil {
		return inspection, err
	}
	return resolved.Inspect(ctx, request)
}

func (target *taskScopedGitPatchTarget) Apply(ctx context.Context, request app.PatchTargetRequest, inspection app.PatchTargetInspection) (app.PatchTargetEvidence, error) {
	resolved, _, err := target.resolve(ctx, request)
	if err != nil {
		return app.PatchTargetEvidence{}, err
	}
	return resolved.Apply(ctx, request, inspection)
}

func (target *taskScopedGitPatchTarget) resolve(ctx context.Context, request app.PatchTargetRequest) (*gitPatchTarget, app.PatchTargetInspection, error) {
	if target == nil || target.manager == nil || target.reader == nil || !request.TaskID.Valid() {
		return nil, app.PatchTargetInspection{}, fmt.Errorf("%w: Task worktree binding is unavailable", app.ErrPatchTargetRecovery)
	}
	binding, err := target.reader.GetTaskWorktreeBinding(ctx, request.TaskID)
	if err != nil {
		return nil, app.PatchTargetInspection{}, fmt.Errorf("%w: Task worktree binding is unavailable", app.ErrPatchTargetRecovery)
	}
	path, err := target.manager.ResolveReady(ctx, binding)
	if err != nil {
		return nil, app.PatchTargetInspection{}, fmt.Errorf("%w: Task worktree cannot be proven", app.ErrPatchTargetRecovery)
	}
	resolved, err := newGitPatchTarget(path, nil)
	if err != nil {
		return nil, app.PatchTargetInspection{}, fmt.Errorf("%w: Task worktree cannot be proven", app.ErrPatchTargetRecovery)
	}
	return resolved, app.PatchTargetInspection{TargetIdentity: resolved.identity}, nil
}

var _ app.PatchApplicationTarget = (*taskScopedGitPatchTarget)(nil)
