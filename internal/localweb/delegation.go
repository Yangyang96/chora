package localweb

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func (server *Server) getDelegation(w http.ResponseWriter, r *http.Request) {
	id, err := domain.ParseTaskID(r.PathValue("taskID"))
	if err != nil {
		writeProjectError(w, err)
		return
	}
	v, err := server.service.GetDelegation(r.Context(), id)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	writeJSON(w, 200, v)
}
func (server *Server) startDelegation(w http.ResponseWriter, r *http.Request) {
	if !server.pathPiEnabled {
		writeError(w, 422, errors.New("delegation requires the local workbench"))
		return
	}
	id, err := domain.ParseTaskID(r.PathValue("taskID"))
	if err != nil {
		writeProjectError(w, err)
		return
	}
	var input struct {
		Assignments []domain.DelegationAssignment `json:"assignments"`
	}
	if err = decodeJSON(r, &input); err != nil {
		writeError(w, 400, err)
		return
	}
	meta, ok := requestCommandMetaForProduct(w, r, "start-delegation", server.product)
	if !ok {
		return
	}
	v, err := server.service.StartDelegation(r.Context(), app.StartDelegationRequest{CommandMeta: meta, ParentTaskID: id, Assignments: input.Assignments})
	if err != nil {
		writeAgentExecutionCommandError(w, err)
		return
	}
	writeJSON(w, 201, v)
}
func (server *Server) changeDelegation(w http.ResponseWriter, r *http.Request) {
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
	meta, ok := requestCommandMetaForProduct(w, r, "change-delegation", server.product)
	if !ok {
		return
	}
	// Persist stop intent independently of slow dispatcher I/O. Transactional
	// lineage binding and StartAttempt fence launches against this state.
	v, err := server.service.ChangeDelegation(r.Context(), app.ChangeDelegationRequest{CommandMeta: meta, ParentTaskID: id, ExpectedVersion: input.ExpectedVersion, Action: r.PathValue("action")})
	if err != nil {
		writeAgentExecutionCommandError(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func delegationMeta(d domain.TaskDelegation, action string) app.CommandMeta {
	return app.CommandMeta{ActorID: d.ActorID, SessionID: "system.delegation", IdempotencyKey: fmt.Sprintf("delegation:%s:%s", d.ParentTaskID, action)}
}
func (server *Server) setDelegationState(ctx context.Context, d domain.TaskDelegation, state domain.DelegationState, reason string) error {
	next, err := d.Transition(state, reason, time.Now().UTC())
	if err != nil {
		return err
	}
	return server.store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.SaveTaskDelegationCAS(ctx, d.Version, next) })
}
func (server *Server) recoverDelegations(ctx context.Context) error {
	active, err := server.store.Reader().ListActiveTaskDelegations(ctx)
	if err != nil {
		return err
	}
	for _, d := range active {
		// Persisted stop intent survives restart and is retried as a stop, never a launch.
		if d.State == domain.DelegationStopping {
			continue
		}
		if err = server.setDelegationState(ctx, d, domain.DelegationBlocked, "Workbench restarted. Inspect existing child Runs and resolve recovery before resuming delegation."); err != nil {
			return err
		}
	}
	return nil
}
func (server *Server) dispatchDelegations(ctx context.Context) {
	server.delegationMu.Lock()
	defer server.delegationMu.Unlock()
	active, err := server.store.Reader().ListActiveTaskDelegations(ctx)
	if err != nil {
		server.logger.Printf("list delegations: %v", err)
		return
	}
	for _, d := range active {
		if ctx.Err() != nil {
			return
		}
		if err := server.advanceDelegation(ctx, d); err != nil {
			if ctx.Err() != nil {
				return
			}
			next := domain.DelegationBlocked
			if d.State == domain.DelegationStopping {
				next = domain.DelegationStopping
			}
			if d.State == next && d.Reason == delegationReason(err) {
				continue
			}
			if e := server.setDelegationState(ctx, d, next, delegationReason(err)); e != nil {
				server.logger.Printf("block delegation: %v", e)
			}
		}
	}
}
func delegationReason(err error) string {
	// Store bounded diagnostic context; detailed process errors remain in Run evidence.
	text := err.Error()
	if len(text) > 3000 {
		text = "Delegation could not advance. Inspect the current child Run."
	}
	return text
}
func (server *Server) advanceDelegation(ctx context.Context, d domain.TaskDelegation) error {
	children, err := server.store.Reader().ListDelegationChildren(ctx, d.ParentTaskID)
	if err != nil {
		return err
	}
	if d.State == domain.DelegationStopping {
		return server.stopDelegationChildren(ctx, d, children)
	}
	view, err := server.service.GetDelegation(ctx, d.ParentTaskID)
	if err != nil {
		return err
	}
	if !view.Eligible {
		return errors.New("parent Task, Room or Project no longer permits delegation")
	}
	for _, child := range children {
		if !child.RunID.Valid() {
			return server.launchDelegationChild(ctx, d, child)
		}
		run, err := server.store.Reader().GetRun(ctx, child.RunID)
		if err != nil {
			return err
		}
		switch run.State() {
		case domain.RunStateAwaitingReview, domain.RunStateAccepted, domain.RunStateCompleted:
			// Completion is evidence of execution only. No human review is synthesized.
		case domain.RunStateDraft, domain.RunStateReady:
			retry, e := server.service.AutomaticRetryForRun(ctx, run.ID())
			if e != nil {
				return e
			}
			if retry != nil && retry.State == app.AutomaticRetryRetrying {
				return nil
			}
			if run.State() == domain.RunStateReady {
				attempt, e := server.store.Reader().GetCurrentAttempt(ctx, run.ID())
				if e != nil {
					return e
				}
				if attempt.Sequence() != 1 {
					return fmt.Errorf("child %d has a prepared retry requiring explicit recovery; continue its existing Run before resuming", child.Position+1)
				}
			}
			return server.launchDelegationChild(ctx, d, child)
		case domain.RunStateRunning, domain.RunStateStopping, domain.RunStateAwaitingVerification, domain.RunStateVerifying:
			return nil
		case domain.RunStateRecoveryRequired:
			retry, e := server.service.AutomaticRetryForRun(ctx, run.ID())
			if e != nil {
				return e
			}
			if retry != nil && (retry.State == app.AutomaticRetryPending || retry.State == app.AutomaticRetryRetrying) {
				return nil
			}
			return fmt.Errorf("child %d requires recovery; resolve its Run before resuming", child.Position+1)
		default:
			return fmt.Errorf("child %d needs attention (%s); inspect its result before resuming", child.Position+1, run.State())
		}
	}
	if len(children) == len(d.Assignments) {
		return server.setDelegationState(ctx, d, domain.DelegationAwaitingReview, "")
	}
	// Creation and parent linkage commit together. Deterministic command identity
	// makes an uncertain response safe to retry without inventing another Task.
	created, err := server.service.CreateDelegationChild(ctx, d.ParentTaskID, len(children))
	if err != nil {
		return err
	}
	return server.launchDelegationChild(ctx, d, domain.DelegationChild{ParentTaskID: d.ParentTaskID, Position: len(children), TaskID: created.Task.ID()})
}
func (server *Server) launchDelegationChild(ctx context.Context, d domain.TaskDelegation, child domain.DelegationChild) error {
	if !server.pathPiEnabled {
		return errors.New("local workbench execution unavailable")
	}
	settings, err := server.store.Reader().GetTaskExecutionSettings(ctx, child.TaskID)
	if err != nil {
		return err
	}
	binding, err := domain.NewAgentExecutionProfileBinding(settings.AgentExecutionProfile)
	if err != nil {
		return err
	}
	if _, err = server.registry.Get("pi"); err != nil {
		return err
	}
	target, err := execution.NewExecutionTarget("pi", binding.ExecutionProvider())
	if err != nil || !server.supervisor.hasTarget(target) {
		return errors.New("inherited execution route is unavailable")
	}
	if settings.AgentExecutionProfile == domain.AgentExecutionProfileIsolatedLocal {
		if err = server.isolatedLocal.available(ctx); err != nil {
			return err
		}
	}
	if server.product && (!server.verifierStatus.Enabled || server.verifier == nil) {
		return errors.New("independent verifier is unavailable")
	}
	meta := delegationMeta(d, fmt.Sprintf("child-%d", child.Position))
	acceptance, err := server.store.Reader().GetCurrentTechnicalPlanAcceptance(ctx, child.TaskID)
	if errors.Is(err, storecontract.ErrNotFound) {
		// Reuse the normal default-pass plan, retaining an explicit system reviewer.
		var revisionID domain.TechnicalPlanRevisionID
		draft, e := server.store.Reader().GetOpenTechnicalPlanDraft(ctx, child.TaskID)
		if e == nil {
			if draft.EditVersion() != 1 || draft.PredecessorRevisionID().Valid() {
				return errors.New("delegated plan changed; inspect child Task before continuing")
			}
			submitted, e := server.service.SubmitTechnicalPlanDraft(ctx, app.SubmitTechnicalPlanDraftRequest{CommandMeta: childCommandMeta(meta, "submit"), DraftID: draft.ID(), ExpectedEditVersion: draft.EditVersion()})
			if e != nil {
				return e
			}
			revisionID = submitted.Revision.ID()
		} else if errors.Is(e, storecontract.ErrNotFound) {
			revisions, e := server.store.Reader().ListTechnicalPlanRevisions(ctx, child.TaskID)
			if e != nil {
				return e
			}
			if len(revisions) != 1 {
				return errors.New("delegated plan requires reconciliation")
			}
			revisionID = revisions[0].ID()
		} else {
			return e
		}
		if e := server.service.ValidateDelegatedPlan(ctx, child.TaskID, revisionID); e != nil {
			return e
		}
		activation := childCommandMeta(meta, "activate")
		activation.ActorID = systemActor
		reviewed, e := server.service.ReviewTechnicalPlanRevision(ctx, app.ReviewTechnicalPlanRevisionRequest{CommandMeta: activation, RevisionID: revisionID, Kind: domain.TechnicalPlanReviewAccept, Note: "System activated the fixed assignment under the parent's explicit delegation authorization; this does not accept its result."})
		if e != nil {
			return e
		}
		if reviewed.Binding == nil {
			return errors.New("delegated plan has no binding")
		}
		acceptance = *reviewed.Binding
	} else if err != nil {
		return err
	}
	if err := server.service.ValidateDelegatedPlan(ctx, child.TaskID, acceptance.RevisionID()); err != nil {
		return err
	}
	var run domain.AgentRun
	if child.RunID.Valid() {
		run, err = server.store.Reader().GetRun(ctx, child.RunID)
	} else {
		created, e := server.service.CreateRun(ctx, app.CreateRunRequest{CommandMeta: childCommandMeta(meta, "run"), TaskID: child.TaskID, RevisionID: acceptance.RevisionID()})
		err = e
		run = created.Run
	}
	if err != nil {
		return err
	}
	task, err := server.store.Reader().GetTask(ctx, child.TaskID)
	if err != nil {
		return err
	}
	fixture, err := server.e2eDelegationRunAdapter(task.Criteria(), run.ID())
	if err != nil {
		return err
	}
	var prepared app.PrepareRunResult
	if run.State() == domain.RunStateDraft {
		prepared, err = server.prepareAndBindManagedAttempt(ctx, func(c context.Context) (app.PrepareRunResult, error) {
			return server.service.PrepareRun(c, app.PrepareRunRequest{CommandMeta: childCommandMeta(meta, "prepare"), RunID: run.ID(), ExpectedVersion: run.Version()})
		})
	} else if run.State() == domain.RunStateReady {
		prepared.Run = run
		prepared.Attempt, err = server.store.Reader().GetCurrentAttempt(ctx, run.ID())
		if err == nil {
			err = server.bindAndPublishManagedAttempt(ctx, prepared.Attempt)
		}
	} else {
		return fmt.Errorf("delegated launch requires a prepared Run, found %s", run.State())
	}
	if err != nil {
		return err
	}
	started, err := server.startManagedAttempt(ctx, prepared.Attempt, app.StartAttemptRequest{CommandMeta: childCommandMeta(meta, "start"), RunID: run.ID(), ExpectedVersion: prepared.Run.Version(), Mode: app.StartFresh})
	if err != nil {
		return err
	}
	if started.Session == nil || !started.Session.Identity.Valid() || started.Run.State() != domain.RunStateRunning {
		return errors.New("delegated launch requires reconciliation")
	}
	if fixture != nil {
		server.supervisor.mu.RLock()
		runtime, ok := server.supervisor.byAdapter["pi"].(*fakeSupervisor)
		server.supervisor.mu.RUnlock()
		if !ok {
			return errors.New("delegation fixture runtime is unavailable")
		}
		go server.completeFakeRunOn(started.Run.ID(), started.Session.ID, fixture, runtime)
	} else if fake, ok := server.automaticRetryFakeAdapter(); ok {
		go server.completeFakeRunOn(started.Run.ID(), started.Session.ID, fake.adapter, fake.runtime)
	} else {
		server.startRuntimeMonitor("pi", started.Run.ID(), started.Session.ID, started.Session.Identity)
	}
	return nil
}
func (server *Server) stopDelegationChildren(ctx context.Context, d domain.TaskDelegation, children []domain.DelegationChild) error {
	for _, child := range children {
		if !child.RunID.Valid() {
			continue
		}
		run, err := server.store.Reader().GetRun(ctx, child.RunID)
		if err != nil {
			return err
		}
		switch run.State() {
		case domain.RunStateAwaitingReview, domain.RunStateAccepted, domain.RunStateCompleted, domain.RunStateCancelled, domain.RunStateRevisionRequired:
			continue
		case domain.RunStateDraft, domain.RunStateReady:
			if err := server.service.CancelUnstartedDelegationChild(ctx, d.ParentTaskID, child.TaskID); err != nil {
				return err
			}
			continue
		case domain.RunStateAwaitingVerification, domain.RunStateVerifying:
			return nil // Wait for the already-started verifier; never start another Agent.
		case domain.RunStateVerificationRecoveryRequired:
			return errors.New("child verification requires recovery before stop can be confirmed")
		default:
			stopped, e := server.service.RequestCancel(ctx, app.StopRequest{CommandMeta: delegationMeta(d, fmt.Sprintf("stop-%s-%d", run.ID(), run.Version())), RunID: run.ID(), ExpectedVersion: run.Version(), Reason: "Parent delegation stopped", AllowRetry: false})
			if e != nil {
				return e
			}
			if stopped.Run.State() != domain.RunStateCancelled {
				return nil
			}
		}
	}
	return server.setDelegationState(ctx, d, domain.DelegationStopped, "")
}
