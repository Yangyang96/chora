package localweb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func (server *Server) taskWorkflow(w http.ResponseWriter, r *http.Request) {
	id, err := domain.ParseTaskID(r.PathValue("taskID"))
	if err != nil {
		writeError(w, 400, err)
		return
	}
	task, err := server.store.Reader().GetTask(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	room, err := server.store.Reader().GetRoom(r.Context(), task.RoomID())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if r.Method == http.MethodGet {
		v, err := server.store.Reader().GetTaskWorkflow(r.Context(), id)
		if errors.Is(err, storecontract.ErrNotFound) {
			_, planErr := server.store.Reader().GetCurrentTechnicalPlanAcceptance(r.Context(), id)
			history, historyErr := server.store.Reader().ListTaskRunHistory(r.Context(), task.RoomID(), id)
			writeJSON(w, 200, map[string]any{"enabled": server.workflowBridge != nil, "eligible": planErr == nil && historyErr == nil && len(history) == 0, "projectId": room.ProjectID().String(), "question": task.Goal(), "workflow": nil})
			return
		}
		if err != nil {
			writeStoreError(w, err)
			return
		}
		if v.ActorID != localActor {
			writeError(w, 403, app.ErrUnauthorizedCommand)
			return
		}
		writeJSON(w, 200, map[string]any{"enabled": server.workflowBridge != nil, "workflow": v})
		return
	}
	if server.workflowBridge == nil {
		writeError(w, 503, errors.New("research workflow is not configured"))
		return
	}
	var input struct {
		Question  string `json:"question"`
		ProjectID string `json:"projectId"`
	}
	if err = decodeJSON(r, &input); err != nil {
		writeError(w, 400, err)
		return
	}
	if input.ProjectID != room.ProjectID().String() {
		writeError(w, 403, app.ErrUnauthorizedCommand)
		return
	}
	meta, ok := requireRequestCommandMeta(w, r, "submit-workflow")
	if !ok {
		return
	}
	config, err := server.workflowBridge.Describe(r.Context())
	if err != nil {
		writeError(w, 503, err)
		return
	}
	v, err := server.service.SubmitWorkflow(r.Context(), app.SubmitWorkflowRequest{CommandMeta: meta, TaskID: id, ProjectID: room.ProjectID(), Question: input.Question, Configuration: config})
	if err != nil {
		writeWorkflowError(w, err)
		return
	}
	writeJSON(w, 201, v)
}

func (server *Server) workflowCommand(w http.ResponseWriter, r *http.Request) {
	p, err := domain.ParseProjectID(r.PathValue("projectID"))
	if err != nil {
		writeError(w, 400, err)
		return
	}
	id := r.PathValue("workflowID")
	meta, ok := requireRequestCommandMeta(w, r, "workflow-command")
	if !ok {
		return
	}
	var v domain.WorkflowExecution
	if r.PathValue("action") == "stop" {
		if err = decodeOptionalJSON(r, &struct{}{}); err == nil {
			v, err = server.service.StopWorkflow(r.Context(), meta, id, p)
			if err == nil && server.workflowBridge != nil {
				// Go stop intent is already committed. Deliver independently of the
				// research dispatch mutex so an in-flight model call cannot hide it.
				stopErr := server.deliverWorkflowStop(r.Context(), v)
				if stopErr != nil {
					_ = server.service.MarkWorkflow(r.Context(), id, "unresolved", "Stop is saved; the existing research operation or Pi process still needs reconciliation.")
				}
				v, err = server.store.Reader().GetWorkflow(r.Context(), id)
			}
		}
	} else if r.PathValue("action") == "reconcile" {
		if err = decodeOptionalJSON(r, &struct{}{}); err == nil {
			v, err = server.service.ReconcileWorkflow(r.Context(), meta, id, p)
		}
	} else if r.PathValue("action") == "decision" {
		var input struct {
			Version      uint64 `json:"version"`
			GateID       string `json:"gateId"`
			Digest       string `json:"inputDigest"`
			Decision     string `json:"decision"`
			Instructions string `json:"instructions"`
		}
		if err = decodeJSON(r, &input); err == nil {
			v, err = server.service.DecideWorkflow(r.Context(), app.DecideWorkflowRequest{CommandMeta: meta, ID: id, ProjectID: p, ExpectedVersion: input.Version, GateID: input.GateID, InputDigest: input.Digest, Decision: input.Decision, Instructions: input.Instructions})
		}
	} else {
		writeError(w, 404, errors.New("workflow action not found"))
		return
	}
	if err != nil {
		writeWorkflowError(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (server *Server) deliverWorkflowStop(ctx context.Context, w domain.WorkflowExecution) error {
	commands, err := server.store.Reader().ListWorkflowCommands(ctx, w.ID)
	if err != nil {
		return err
	}
	for _, c := range commands {
		if c.Kind == "stop" && len(c.Receipt) == 0 {
			b, e := server.workflowBridge.Execute(ctx, c)
			if e != nil {
				return e
			}
			if e = server.service.ApplyWorkflowReceipt(ctx, c, b); e != nil {
				return e
			}
		}
	}
	w, err = server.store.Reader().GetWorkflow(ctx, w.ID)
	if err != nil {
		return err
	}
	return server.stopWorkflowRun(ctx, w)
}

func writeWorkflowError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, app.ErrUnauthorizedCommand):
		writeError(w, 403, err)
	case errors.Is(err, storecontract.ErrNotFound):
		writeError(w, 404, err)
	case errors.Is(err, storecontract.ErrVersionConflict), errors.Is(err, storecontract.ErrIdempotencyConflict), errors.Is(err, storecontract.ErrActiveRunExists):
		writeError(w, 409, err)
	case errors.Is(err, app.ErrInvalidCommand), errors.Is(err, storecontract.ErrRoomStateForbidden):
		writeError(w, 422, err)
	default:
		writeError(w, 500, err)
	}
}

func (server *Server) workflowEvents(w http.ResponseWriter, r *http.Request) {
	p, err := domain.ParseProjectID(r.PathValue("projectID"))
	if err != nil {
		writeError(w, 400, err)
		return
	}
	id := r.PathValue("workflowID")
	if _, err = server.service.GetWorkflow(r.Context(), commandMeta("read-workflow"), id, p); err != nil {
		writeWorkflowError(w, err)
		return
	}
	after := uint64(0)
	cursor := r.Header.Get("Last-Event-ID")
	if cursor == "" {
		cursor = r.URL.Query().Get("after")
	}
	if cursor != "" {
		after, err = strconv.ParseUint(cursor, 10, 64)
		if err != nil {
			writeError(w, 400, err)
			return
		}
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, 500, errors.New("stream unavailable"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	for {
		events, err := server.store.Reader().ListWorkflowEvents(r.Context(), id, after)
		if err != nil {
			return
		}
		for _, event := range events {
			b, _ := json.Marshal(event)
			if _, err = fmt.Fprintf(w, "id: %d\nevent: workflow\ndata: %s\n\n", event.Sequence, b); err != nil {
				return
			}
			after = event.Sequence
		}
		if len(events) == 200 {
			flusher.Flush()
			continue
		}
		if _, err = fmt.Fprint(w, ": keepalive\n\n"); err != nil {
			return
		}
		flusher.Flush()
		if !wait(r.Context(), time.Second) {
			return
		}
	}
}

func (server *Server) runWorkflowDispatcher(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		server.dispatchWorkflows(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (server *Server) dispatchWorkflows(ctx context.Context) {
	if server.workflowBridge == nil {
		return
	}
	server.workflowMu.Lock()
	defer server.workflowMu.Unlock()
	items, err := server.store.Reader().ListActiveWorkflows(ctx)
	if err != nil {
		return
	}
	for _, item := range items {
		if ctx.Err() != nil {
			return
		}
		if err = server.advanceWorkflow(ctx, item); err != nil && ctx.Err() == nil {
			_ = server.service.MarkWorkflow(ctx, item.ID, "unresolved", "Research or Pi execution needs reconciliation. Restore the frozen worker configuration and inspect the existing operation/Run before retrying.")
		}
	}
}
func (server *Server) advanceWorkflow(ctx context.Context, item domain.WorkflowExecution) error {
	// unresolved is a visible hold, never an automatic new launch/retry loop.
	if item.Status == "unresolved" {
		return nil
	}
	commands, err := server.store.Reader().ListWorkflowCommands(ctx, item.ID)
	if err != nil {
		return err
	}
	for _, c := range commands {
		if len(c.Receipt) > 0 || c.Kind == "create_run" {
			continue
		}
		if item.StopRequested && c.Kind != "stop" {
			continue
		}
		receipt, err := server.workflowBridge.Execute(ctx, c)
		if err != nil {
			return err
		}
		if err = server.service.ApplyWorkflowReceipt(ctx, c, receipt); err != nil {
			return err
		}
	}
	item, err = server.store.Reader().GetWorkflow(ctx, item.ID)
	if err != nil {
		return err
	}
	if item.StopRequested {
		return server.stopWorkflowRun(ctx, item)
	}
	if item.Status != "approved" && item.Status != "coding" {
		return nil
	}
	run, err := server.service.CreateWorkflowRun(ctx, item.ID)
	if err != nil {
		return err
	}
	// SQLite already owns this Run. PG records the same Go operation receipt
	// before launch; a lost response replays the binding rather than a new Run.
	commands, err = server.store.Reader().ListWorkflowCommands(ctx, item.ID)
	if err != nil {
		return err
	}
	for _, c := range commands {
		if c.Kind == "bind_run" && len(c.Receipt) == 0 {
			b, e := server.workflowBridge.Execute(ctx, c)
			if e != nil {
				return e
			}
			if e = server.service.ApplyWorkflowReceipt(ctx, c, b); e != nil {
				return e
			}
		}
	}
	if run.State() == domain.RunStateAwaitingReview || run.State() == domain.RunStateAccepted || run.State() == domain.RunStateCompleted {
		return server.service.MarkWorkflow(ctx, item.ID, "awaiting_review", "")
	}
	switch run.State() {
	case domain.RunStateRunning, domain.RunStateStopping, domain.RunStateAwaitingVerification, domain.RunStateVerifying:
		return nil
	case domain.RunStateDraft, domain.RunStateReady:
		return server.launchWorkflowRun(ctx, item, run)
	default:
		return errors.New("existing Run requires explicit recovery")
	}
}
func (server *Server) launchWorkflowRun(ctx context.Context, item domain.WorkflowExecution, run domain.AgentRun) error {
	settings, err := server.store.Reader().GetTaskExecutionSettings(ctx, item.TaskID)
	if err != nil {
		return err
	}
	if !server.pathPiEnabled {
		return errors.New("Pi execution unavailable")
	}
	if settings.AgentExecutionProfile == domain.AgentExecutionProfileIsolatedLocal {
		if err = server.isolatedLocal.available(ctx); err != nil {
			return err
		}
	}
	if server.product && (!server.verifierStatus.Enabled || server.verifier == nil) {
		return errors.New("independent verifier unavailable")
	}
	meta := app.CommandMeta{ActorID: item.ActorID, SessionID: "workflow.pi", IdempotencyKey: item.ID + ":pi"}
	var prepared app.PrepareRunResult
	if run.State() == domain.RunStateDraft {
		prepared, err = server.prepareAndBindManagedAttempt(ctx, func(c context.Context) (app.PrepareRunResult, error) {
			return server.service.PrepareRun(c, app.PrepareRunRequest{CommandMeta: childCommandMeta(meta, "prepare"), RunID: run.ID(), ExpectedVersion: run.Version()})
		})
	} else {
		prepared.Run = run
		prepared.Attempt, err = server.store.Reader().GetCurrentAttempt(ctx, run.ID())
		if err == nil && prepared.Attempt.Sequence() != 1 {
			return errors.New("prepared retry requires explicit recovery")
		}
		if err == nil {
			err = server.bindAndPublishManagedAttempt(ctx, prepared.Attempt)
		}
	}
	if err != nil {
		return err
	}
	started, err := server.startManagedAttempt(ctx, prepared.Attempt, app.StartAttemptRequest{CommandMeta: childCommandMeta(meta, "start"), RunID: run.ID(), ExpectedVersion: prepared.Run.Version(), Mode: app.StartFresh})
	if err != nil {
		return err
	}
	if started.Session == nil || !started.Session.Identity.Valid() || started.Run.State() != domain.RunStateRunning {
		return errors.New("Pi launch outcome unknown")
	}
	server.startRuntimeMonitor("pi", started.Run.ID(), started.Session.ID, started.Session.Identity)
	return nil
}
func (server *Server) stopWorkflowRun(ctx context.Context, item domain.WorkflowExecution) error {
	if item.RunID.Valid() {
		run, err := server.store.Reader().GetRun(ctx, item.RunID)
		if err != nil {
			return err
		}
		switch run.State() {
		case domain.RunStateDraft, domain.RunStateReady:
			if err = server.service.CancelUnstartedWorkflowRun(ctx, item.ID); err != nil {
				return err
			}
		case domain.RunStateAwaitingReview, domain.RunStateAccepted, domain.RunStateCompleted, domain.RunStateCancelled, domain.RunStateRevisionRequired:
		case domain.RunStateAwaitingVerification, domain.RunStateVerifying:
			return nil
		default:
			stopped, e := server.service.RequestCancel(ctx, app.StopRequest{CommandMeta: app.CommandMeta{ActorID: item.ActorID, SessionID: "workflow.pi", IdempotencyKey: fmt.Sprintf("%s:stop:%d", item.ID, run.Version())}, RunID: run.ID(), ExpectedVersion: run.Version(), Reason: "Research workflow stopped", AllowRetry: false})
			if e != nil {
				return e
			}
			if stopped.Run.State() != domain.RunStateCancelled {
				return errors.New("Pi stop outcome unresolved")
			}
		}
	}
	return server.service.MarkWorkflow(ctx, item.ID, "stopped", "")
}
