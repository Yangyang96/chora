package app

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
	"github.com/Yangyang96/chora/internal/taskdelivery"
)

const hostingObservationEvent = "task_delivery.github.observed"

type hostingObservation struct {
	OperationID string
	RepoID      string
	PR          taskdelivery.PullRequest
}

func (s *Service) hostingCapabilities() DeliveryCapabilities {
	if s.deps.TaskDeliveryHosting == nil || !s.deps.TaskDeliveryHosting.Available() {
		return DeliveryCapabilities{Provider: "github", Reason: "Install GitHub CLI (gh) and authenticate to github.com to use pull request delivery."}
	}
	return DeliveryCapabilities{Cleanup: true, Hosting: true, CreatePR: true, Merge: true, Provider: "github"}
}

func hostingStage(state string) string {
	switch state {
	case "open":
		return "pr_open"
	case "closed":
		return "pr_closed"
	case "merged":
		return "merged"
	}
	return "recovery_required"
}
func deliveryStageRank(state string) int {
	switch state {
	case "committed":
		return 1
	case "pushed":
		return 2
	case "pr_open", "pr_closed":
		return 3
	case "merged":
		return 4
	case "cleaned":
		return 5
	case "recovery_required":
		return 6
	}
	return 0
}

func (s *Service) prepareHosting(ctx context.Context, req DeliveryRequest, id string, binding taskdelivery.Binding, operations []storecontract.DeliveryOperation) (taskdelivery.HostingPreview, *taskdelivery.PushPreview, error) {
	var empty taskdelivery.HostingPreview
	if !s.hostingCapabilities().Hosting {
		return empty, nil, fmt.Errorf("%w: install gh and authenticate to github.com", taskdelivery.ErrUnsupported)
	}
	var pushed *taskdelivery.PushPreview
	var created *taskdelivery.HostingPreview
	var number int
	for _, o := range operations {
		if o.RepositoryID.String() != req.RepoID || o.State != "succeeded" {
			continue
		}
		var in deliveryIntent
		var out deliveryOutcome
		if err := json.Unmarshal(o.PreviewJSON, &in); err != nil {
			return empty, nil, err
		}
		if err := json.Unmarshal(o.OutcomeJSON, &out); err != nil {
			return empty, nil, err
		}
		if o.Kind == "push" && in.Push != nil {
			if pushed != nil && (pushed.URL != in.Push.URL || pushed.Head != in.Push.Head) {
				return empty, nil, fmt.Errorf("%w: multiple recorded push destinations require explicit resolution", taskdelivery.ErrConflict)
			}
			pushed = in.Push
		}
		if o.Kind == "pr" && in.Hosting != nil && out.PR != nil {
			created = in.Hosting
			number = out.PR.Number
		}
		if o.Kind == "merge" {
			return empty, nil, fmt.Errorf("%w: this Task pull request is already merged", taskdelivery.ErrConflict)
		}
	}
	if pushed == nil || pushed.Binding != binding {
		return empty, nil, fmt.Errorf("%w: push this Task repository first", taskdelivery.ErrConflict)
	}
	done, err := s.deps.TaskDeliveryGit.ReconcilePush(ctx, *pushed)
	if err != nil {
		return empty, nil, err
	}
	if !done {
		return empty, nil, taskdelivery.ErrConflict
	}
	var preview taskdelivery.HostingPreview
	if req.Kind == "pr" {
		if created != nil {
			return empty, nil, fmt.Errorf("%w: a pull request is already recorded; refresh its status", taskdelivery.ErrConflict)
		}
		preview, err = s.deps.TaskDeliveryHosting.PreviewPR(ctx, *pushed, req.Title, req.Body, id)
	} else {
		if created == nil || number <= 0 {
			return empty, nil, fmt.Errorf("%w: create and record the pull request first", taskdelivery.ErrConflict)
		}
		preview, err = s.deps.TaskDeliveryHosting.PreviewMerge(ctx, *created, number)
	}
	return preview, pushed, err
}

func (s *Service) observeHosting(ctx context.Context, runID domain.RunID, o storecontract.DeliveryOperation, pr taskdelivery.PullRequest) error {
	// Observations are append-only; never rewrite the successful mutation outcome.
	now := s.deps.Clock.Now()
	return s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		_, err := tx.AppendRunEvent(ctx, runID, storecontract.EventDraft{ID: s.deps.IDs.EventID(), Type: hostingObservationEvent, Source: "app", OccurredAt: now, RecordedAt: now, NormalizedJSON: responseBody(hostingObservation{OperationID: o.ID, RepoID: o.RepositoryID.String(), PR: pr})})
		return err
	})
}
func (s *Service) projectHostingObservations(ctx context.Context, runID domain.RunID, view *TaskDeliveryView, operations []storecontract.DeliveryOperation) error {
	events, err := s.deps.Store.Reader().ListRunEvents(ctx, runID)
	if err != nil {
		return err
	}
	valid := map[string]bool{}
	for _, o := range operations {
		if o.Kind == "pr" && o.State == "succeeded" {
			valid[o.ID] = true
		}
	}
	for _, event := range events {
		if event.Type() != hostingObservationEvent {
			continue
		}
		var observation hostingObservation
		if err := json.Unmarshal(event.NormalizedJSON(), &observation); err != nil {
			return err
		}
		if !valid[observation.OperationID] {
			continue
		}
		for i := range view.Repositories {
			repo := &view.Repositories[i]
			if repo.RepoID != observation.RepoID {
				continue
			}
			stage := hostingStage(observation.PR.State)
			if repo.Status == "recovery_required" || repo.Status == "cleaned" {
				continue
			}
			if repo.Status != "merged" || stage == "merged" {
				repo.Status = stage
				repo.PRURL = observation.PR.URL
			}
		}
	}
	for i := range view.Repositories {
		projectDeliveryReason(&view.Repositories[i])
	}
	return nil
}
