package app

import (
	"context"
	"encoding/json"
	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
	"github.com/Yangyang96/chora/internal/taskdelivery"
)

type taskDeliveryCleanupWorkspaces interface {
	DeliveryCleanupRoot(context.Context, domain.TaskID, domain.TaskRepositoryResource) (string, error)
}

func (s *Service) deliveryBindingForRecovery(ctx context.Context, taskID domain.TaskID, r domain.TaskRepositoryResource, kind string) (taskdelivery.Binding, error) {
	if kind != "cleanup" {
		return s.deliveryBinding(ctx, taskID, r)
	}
	resolver, ok := s.deps.TaskDeliveryWorkspaces.(taskDeliveryCleanupWorkspaces)
	if !ok {
		return taskdelivery.Binding{}, taskdelivery.ErrUnsupported
	}
	root, err := resolver.DeliveryCleanupRoot(ctx, taskID, r)
	if err != nil {
		return taskdelivery.Binding{}, err
	}
	return taskdelivery.Binding{Root: root, CommonGitDir: r.CommonGitDir, BaseCommit: r.BaseCommit, BaseTree: r.BaseTree, Branch: r.TaskBranch, TargetRef: r.BaseRef}, nil
}
func (s *Service) prepareCleanup(ctx context.Context, binding taskdelivery.Binding, ops []storecontract.DeliveryOperation, repoID string) (taskdelivery.CleanupPreview, error) {
	if s.deps.TaskDeliveryHosting == nil {
		return taskdelivery.CleanupPreview{}, taskdelivery.ErrUnsupported
	}
	var hosting *taskdelivery.HostingPreview
	var head string
	for _, o := range ops {
		if o.RepositoryID.String() != repoID || o.State != "succeeded" {
			continue
		}
		if o.Kind == "cleanup" {
			return taskdelivery.CleanupPreview{}, taskdelivery.ErrConflict
		}
		var in deliveryIntent
		var out deliveryOutcome
		if err := json.Unmarshal(o.PreviewJSON, &in); err != nil {
			return taskdelivery.CleanupPreview{}, err
		}
		if err := json.Unmarshal(o.OutcomeJSON, &out); err != nil {
			return taskdelivery.CleanupPreview{}, err
		}
		if o.Kind == "commit" {
			head = out.Commit
		}
		if o.Kind == "pr" && in.Binding == binding {
			hosting = in.Hosting
		}
	}
	if hosting == nil || head == "" || hosting.Head != head {
		return taskdelivery.CleanupPreview{}, taskdelivery.ErrConflict
	}
	pr, err := s.deps.TaskDeliveryHosting.Observe(ctx, *hosting)
	if err != nil {
		return taskdelivery.CleanupPreview{}, err
	}
	if pr.State != "merged" || pr.Head != head || pr.MergeCommit == "" {
		return taskdelivery.CleanupPreview{}, taskdelivery.ErrConflict
	}
	return (taskdelivery.Git{}).PreviewCleanup(ctx, binding, head)
}
