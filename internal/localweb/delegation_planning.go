package localweb

import (
	"context"
	"errors"
	"fmt"
	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
	"net/http"
	"time"
)

func (server *Server) startDelegationPlanning(w http.ResponseWriter, r *http.Request) {
	if !server.pathPiEnabled {
		writeError(w, 422, errors.New("planning delegation requires the local workbench"))
		return
	}
	id, err := domain.ParseTaskID(r.PathValue("taskID"))
	if err != nil {
		writeProjectError(w, err)
		return
	}
	var input struct{}
	if err = decodeJSON(r, &input); err != nil {
		writeError(w, 400, err)
		return
	}
	meta, ok := requestCommandMetaForProduct(w, r, "start-delegation-planning", server.product)
	if !ok {
		return
	}
	v, err := server.service.StartDelegationPlanning(r.Context(), app.StartDelegationPlanningRequest{CommandMeta: meta, ParentTaskID: id})
	if err != nil {
		writePlanningDelegationError(w, err)
		return
	}
	writeJSON(w, 201, v)
}
func (server *Server) changeDelegationPlanning(w http.ResponseWriter, r *http.Request) {
	id, err := domain.ParseTaskID(r.PathValue("taskID"))
	if err != nil {
		writeProjectError(w, err)
		return
	}
	var input struct {
		ExpectedVersion uint64 `json:"expectedVersion"`
	}
	if err = decodeJSON(r, &input); err != nil {
		writeError(w, 400, err)
		return
	}
	meta, ok := requestCommandMetaForProduct(w, r, "change-delegation-planning", server.product)
	if !ok {
		return
	}
	v, err := server.service.ChangeDelegationPlanning(r.Context(), app.ChangeDelegationRequest{CommandMeta: meta, ParentTaskID: id, ExpectedVersion: input.ExpectedVersion, Action: r.PathValue("action")})
	if err != nil {
		writePlanningDelegationError(w, err)
		return
	}
	writeJSON(w, 200, v)
}
func writePlanningDelegationError(w http.ResponseWriter, err error) {
	if errors.Is(err, storecontract.ErrRoomStateForbidden) || errors.Is(err, app.ErrReviewEvidenceUnavailable) {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeAgentExecutionCommandError(w, err)
}
func planningMeta(p domain.DelegationPlanningIntent, action string) app.CommandMeta {
	return app.CommandMeta{ActorID: p.ActorID, SessionID: "system.delegation-planning", IdempotencyKey: fmt.Sprintf("delegation-planning:%s:%s", p.ParentTaskID, action)}
}
func (server *Server) setPlanningState(ctx context.Context, p domain.DelegationPlanningIntent, state domain.DelegationPlanningState, reason string) error {
	next, err := p.Transition(state, reason, time.Now().UTC())
	if err != nil {
		return err
	}
	return server.store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.SaveDelegationPlanningCAS(ctx, p.Version, next) })
}
func (server *Server) recoverDelegationPlanning(ctx context.Context) error {
	plans, err := server.store.Reader().ListActiveDelegationPlanning(ctx)
	if err != nil {
		return err
	}
	for _, p := range plans {
		if p.State == domain.DelegationPlanningStopping {
			continue
		}
		if err = server.setPlanningState(ctx, p, domain.DelegationPlanningBlocked, "Workbench restarted. Inspect the existing planning Run before resuming; no new planning Attempt is authorized."); err != nil {
			return err
		}
	}
	return nil
}

// Called under the existing dispatcher mutex. HTTP commands deliberately do not
// take this mutex: stop intent must persist while external launch I/O is pending.
func (server *Server) dispatchDelegationPlanning(ctx context.Context) {
	plans, err := server.store.Reader().ListActiveDelegationPlanning(ctx)
	if err != nil {
		server.logger.Printf("list planning delegations: %v", err)
		return
	}
	for _, p := range plans {
		if ctx.Err() != nil {
			return
		}
		if err = server.advanceDelegationPlanning(ctx, p); err != nil {
			if ctx.Err() != nil {
				return
			}
			// Preparation may have bound the unique Attempt. Reload only to record an
			// error, never to override a concurrently persisted stop or resume decision.
			current, e := server.store.Reader().GetDelegationPlanning(ctx, p.ParentTaskID)
			if e != nil {
				continue
			}
			if current.State != p.State {
				continue
			}
			next := domain.DelegationPlanningBlocked
			if current.State == domain.DelegationPlanningStopping {
				next = domain.DelegationPlanningStopping
			}
			if current.State == next && current.Reason == delegationReason(err) {
				continue
			}
			if e = server.setPlanningState(ctx, current, next, delegationReason(err)); e != nil {
				server.logger.Printf("block planning delegation: %v", e)
			}
		}
	}
}
func (server *Server) advanceDelegationPlanning(ctx context.Context, p domain.DelegationPlanningIntent) error {
	run, err := server.store.Reader().GetRun(ctx, p.RunID)
	if err != nil {
		return err
	}
	if p.State == domain.DelegationPlanningStopping {
		return server.stopDelegationPlanning(ctx, p, run)
	}
	switch run.State() {
	case domain.RunStateDraft, domain.RunStateReady:
		return server.launchDelegationPlanning(ctx, p, run)
	case domain.RunStateRunning, domain.RunStateStopping, domain.RunStateAwaitingVerification, domain.RunStateVerifying:
		return nil
	case domain.RunStateAwaitingReview:
		return server.service.ImportPlanningDelegation(ctx, p.ParentTaskID)
	default:
		return fmt.Errorf("planning Run needs attention (%s); this authorization permits no repair or second Attempt", run.State())
	}
}
func (server *Server) launchDelegationPlanning(ctx context.Context, p domain.DelegationPlanningIntent, run domain.AgentRun) error {
	if !server.pathPiEnabled {
		return errors.New("local workbench execution unavailable")
	}
	if server.product && (!server.verifierStatus.Enabled || server.verifier == nil) {
		return errors.New("independent verifier is unavailable")
	}
	var prepared app.PrepareRunResult
	var err error
	if run.State() == domain.RunStateDraft {
		prepared, err = server.prepareAndBindManagedAttempt(ctx, func(c context.Context) (app.PrepareRunResult, error) {
			return server.service.PrepareRun(c, app.PrepareRunRequest{CommandMeta: planningMeta(p, "prepare"), RunID: run.ID(), ExpectedVersion: run.Version()})
		})
	} else {
		prepared.Run = run
		prepared.Attempt, err = server.store.Reader().GetCurrentAttempt(ctx, run.ID())
		if err == nil {
			err = server.bindAndPublishManagedAttempt(ctx, prepared.Attempt)
		}
	}
	if err != nil {
		return err
	}
	if prepared.Attempt.Sequence() != 1 {
		return errors.New("planning permits one Attempt only")
	}
	if err = server.automaticRetryRouteError(ctx, prepared.Attempt); err != nil {
		return err
	}
	started, err := server.startManagedAttempt(ctx, prepared.Attempt, app.StartAttemptRequest{CommandMeta: planningMeta(p, "start"), RunID: run.ID(), ExpectedVersion: prepared.Run.Version(), Mode: app.StartFresh})
	if err != nil {
		return err
	}
	if started.Session == nil || !started.Session.Identity.Valid() || started.Run.State() != domain.RunStateRunning {
		return errors.New("planning launch requires reconciliation")
	}
	if fake, ok := server.automaticRetryFakeAdapter(); ok {
		go server.completeFakeRunOn(started.Run.ID(), started.Session.ID, fake.adapter, fake.runtime)
	} else {
		server.startRuntimeMonitor("pi", started.Run.ID(), started.Session.ID, started.Session.Identity)
	}
	return nil
}
func (server *Server) stopDelegationPlanning(ctx context.Context, p domain.DelegationPlanningIntent, run domain.AgentRun) error {
	switch run.State() {
	case domain.RunStateAwaitingReview, domain.RunStateAccepted, domain.RunStateCompleted, domain.RunStateCancelled, domain.RunStateRevisionRequired:
	case domain.RunStateDraft, domain.RunStateReady:
		if err := server.service.CancelUnstartedPlanningRun(ctx, p.ParentTaskID); err != nil {
			return err
		}
	case domain.RunStateAwaitingVerification, domain.RunStateVerifying:
		return nil
	case domain.RunStateVerificationRecoveryRequired:
		return errors.New("planning verification requires recovery before stop can be confirmed")
	default:
		if run.State() == domain.RunStateRecoveryRequired {
			if err := server.service.CancelUnstartedPlanningRun(ctx, p.ParentTaskID); err == nil {
				break
			} else if !errors.Is(err, storecontract.ErrVersionConflict) {
				return err
			}
		}
		stopped, err := server.service.RequestCancel(ctx, app.StopRequest{CommandMeta: planningMeta(p, fmt.Sprintf("stop-%d", run.Version())), RunID: run.ID(), ExpectedVersion: run.Version(), Reason: "Planning delegation stopped", AllowRetry: false})
		if err != nil {
			return err
		}
		if stopped.Run.State() != domain.RunStateCancelled {
			return nil
		}
	}
	return server.setPlanningState(ctx, p, domain.DelegationPlanningStopped, "")
}
