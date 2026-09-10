package app

import (
	"context"
	"encoding/hex"
	"errors"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

type DeliveryRepositorySummary struct {
	RepoID string `json:"repoId"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

type TaskDeliverySummary struct {
	Repositories []DeliveryRepositorySummary `json:"repositories"`
	Unavailable  bool                        `json:"unavailable,omitempty"`
}

// LoadTaskDeliverySummary uses durable evidence only. Polling task lists must
// never materialize patches, inspect worktrees, or contact a hosting provider.
func (s *Service) LoadTaskDeliverySummary(ctx context.Context, run domain.AgentRun) (*TaskDeliverySummary, error) {
	if run.State() != domain.RunStateAccepted {
		return nil, nil
	}
	reader := s.deps.Store.Reader()
	record, err := reader.GetTaskResourceSnapshot(ctx, run.TaskID())
	if errors.Is(err, storecontract.ErrNotFound) {
		return nil, nil // Legacy tasks keep their existing Apply status.
	}
	if err != nil {
		return nil, err
	}
	snapshot, err := s.deliverySnapshot(ctx, run.TaskID())
	if err != nil {
		return nil, err
	}
	branchDelivery := false
	for _, resource := range snapshot.Resources {
		branchDelivery = branchDelivery || resource.DeliveryMode == domain.TaskResourceDeliveryTaskBranch
	}
	if !branchDelivery {
		return nil, nil
	}
	attempt, err := reader.GetCurrentAttempt(ctx, run.ID())
	if err != nil {
		return nil, err
	}
	group, err := reader.GetResourceResultGroup(ctx, attempt.ID())
	if err != nil {
		return nil, err
	}
	if group.RunID != run.ID().String() || group.TaskID != run.TaskID().String() || group.ResourceSnapshotDigest != hex.EncodeToString(record.Digest[:]) || len(group.Repositories) != len(snapshot.Resources) {
		return nil, ErrReviewEvidenceUnavailable
	}
	for i, resource := range snapshot.Resources {
		if group.Repositories[i].RepoID != resource.RepoID {
			return nil, ErrReviewEvidenceUnavailable
		}
	}
	operations, err := reader.ListDeliveryOperations(ctx, run.TaskID())
	if err != nil {
		return nil, err
	}
	repositories, err := projectDeliveryRepositories(run, snapshot, group, operations)
	if err != nil {
		return nil, err
	}
	view := TaskDeliveryView{Repositories: repositories}
	if err = s.projectHostingObservations(ctx, run.ID(), &view, operations); err != nil {
		return nil, err
	}
	if err = projectClosedDeliveries(ctx, reader, group.ID, &view); err != nil {
		return nil, err
	}
	summary := &TaskDeliverySummary{Repositories: make([]DeliveryRepositorySummary, 0, len(repositories))}
	for _, repository := range view.Repositories {
		summary.Repositories = append(summary.Repositories, DeliveryRepositorySummary{RepoID: repository.RepoID, Name: repository.Name, Status: repository.Status})
	}
	return summary, nil
}
