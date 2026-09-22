package localweb

import (
	"context"
	"fmt"

	"github.com/Yangyang96/chora/internal/domain"
)

func (server *Server) advanceDelegationSynthesis(ctx context.Context, d domain.TaskDelegation, item domain.DelegationSynthesis) error {
	if !item.TaskID.Valid() {
		created, err := server.service.CreateDelegationSynthesisTask(ctx, d.ParentTaskID)
		if err != nil {
			return err
		}
		item.TaskID = created.Task.ID()
	}
	child := domain.DelegationChild{ParentTaskID: d.ParentTaskID, Position: -1, TaskID: item.TaskID, RunID: item.RunID}
	if !item.RunID.Valid() {
		return server.launchDelegationChild(ctx, d, child)
	}
	run, err := server.store.Reader().GetRun(ctx, item.RunID)
	if err != nil {
		return err
	}
	switch run.State() {
	case domain.RunStateDraft, domain.RunStateReady:
		return server.launchDelegationChild(ctx, d, child)
	case domain.RunStateRunning, domain.RunStateStopping, domain.RunStateAwaitingVerification, domain.RunStateVerifying:
		return nil
	case domain.RunStateAwaitingReview, domain.RunStateAccepted, domain.RunStateCompleted:
		view, e := server.service.GetDelegation(ctx, d.ParentTaskID)
		if e != nil {
			return e
		}
		if view.Synthesis == nil || view.Synthesis.ResultID == "" {
			return fmt.Errorf("synthesis result evidence is unavailable")
		}
		return server.setDelegationState(ctx, d, domain.DelegationAwaitingReview, "")
	default:
		return fmt.Errorf("synthesis Run needs attention (%s); no repair or second Attempt is authorized", run.State())
	}
}
