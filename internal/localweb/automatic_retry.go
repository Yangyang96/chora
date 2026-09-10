package localweb

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	agentfake "github.com/Yangyang96/chora/internal/agent/fake"
	agentpi "github.com/Yangyang96/chora/internal/agent/pi"
	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/preflight"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

const automaticRetryDispatchInterval = 500 * time.Millisecond

func automaticRetryCommandMeta(action string, attemptID domain.AttemptID) app.CommandMeta {
	return app.CommandMeta{
		ActorID:        "system.automatic-retry",
		SessionID:      "automatic-retry-dispatcher",
		IdempotencyKey: action + ":" + attemptID.String(),
	}
}

func (server *Server) runAutomaticRetryDispatcher(ctx context.Context) {
	server.dispatchAutomaticRetries(ctx)
	ticker := time.NewTicker(automaticRetryDispatchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			server.dispatchAutomaticRetries(ctx)
		}
	}
}

func (server *Server) dispatchAutomaticRetries(ctx context.Context) {
	directory, err := server.service.ListRoomDirectory(ctx)
	if err != nil {
		server.logger.Printf("list automatic retry Rooms: %v", err)
		return
	}
	for _, room := range directory.ActiveRooms {
		workspace, err := server.service.GetRoomWorkspace(ctx, room.Room.ID())
		if err != nil {
			server.logger.Printf("load automatic retry Room %s: %v", room.Room.ID(), err)
			continue
		}
		for _, task := range workspace.Tasks {
			if task.Task.Archived() || task.LatestRun == nil {
				continue
			}
			status, err := server.service.AutomaticRetryForRun(ctx, task.LatestRun.ID())
			if err != nil {
				server.logger.Printf("project automatic retry Run %s: %v", task.LatestRun.ID(), err)
				continue
			}
			if status == nil || status.State != app.AutomaticRetryPending && !(status.State == app.AutomaticRetryRetrying && task.LatestRun.State() == domain.RunStateReady) {
				continue
			}
			server.dispatchAutomaticRetry(ctx, *task.LatestRun, *status)
		}
	}
}

func (server *Server) dispatchAutomaticRetry(ctx context.Context, run domain.AgentRun, status app.AutomaticRetry) {
	attempt, err := server.store.Reader().GetCurrentAttempt(ctx, run.ID())
	if err != nil {
		server.logger.Printf("load automatic retry Attempt for Run %s: %v", run.ID(), err)
		return
	}
	if routeErr := server.automaticRetryRouteError(ctx, attempt); routeErr != nil {
		server.blockAutomaticRetry(ctx, run, attempt.ID(), "automatic_retry_route_unavailable", routeErr)
		return
	}
	prepared := app.PrepareRunResult{Run: run, Attempt: attempt}
	if status.State == app.AutomaticRetryPending {
		envelope, err := automaticRetryEnvelope(status.LastFailureReason)
		if err != nil {
			server.blockAutomaticRetry(ctx, run, attempt.ID(), "automatic_retry_instruction_invalid", err)
			return
		}
		meta := automaticRetryCommandMeta("prepare", attempt.ID())
		prepared, err = server.prepareAndBindManagedAttempt(ctx, func(ctx context.Context) (app.PrepareRunResult, error) {
			return server.service.PrepareRetry(ctx, app.PrepareRetryRequest{
				CommandMeta: meta, RunID: run.ID(), ExpectedVersion: run.Version(),
				Reason: status.LastFailureReason, Instructions: string(envelope), Automatic: true,
			})
		})
		if err != nil {
			if !errors.Is(err, storecontract.ErrVersionConflict) {
				server.blockAutomaticRetry(ctx, run, attempt.ID(), "automatic_retry_prepare_failed", err)
			}
			return
		}
	} else if err := server.bindAndPublishManagedAttempt(ctx, attempt); err != nil {
		server.blockAutomaticRetry(ctx, run, attempt.ID(), "automatic_retry_binding_unavailable", err)
		return
	}
	startMeta := automaticRetryCommandMeta("start", prepared.Attempt.ID())
	started, err := server.startManagedAttempt(ctx, prepared.Attempt, app.StartAttemptRequest{
		CommandMeta: startMeta, RunID: prepared.Run.ID(), ExpectedVersion: prepared.Run.Version(), Mode: app.StartFresh,
	})
	if err != nil {
		server.blockAutomaticRetry(ctx, prepared.Run, prepared.Attempt.ID(), "automatic_retry_start_failed", err)
		return
	}
	if started.Session == nil || started.Run.State() == domain.RunStateRecoveryRequired || !started.Session.Identity.Valid() {
		server.blockAutomaticRetry(ctx, started.Run, started.Attempt.ID(), "automatic_retry_start_failed", errors.New("runtime did not produce a live owned session"))
		return
	}
	if adapter, ok := server.automaticRetryFakeAdapter(); ok {
		go server.completeFakeRunOn(started.Run.ID(), started.Session.ID, adapter.adapter, adapter.runtime)
		return
	}
	go server.monitorRuntimeRun(prepared.Attempt.AdapterID(), started.Run.ID(), started.Session.ID, started.Session.Identity)
}

type automaticRetryFakeRoute struct {
	adapter *agentfake.Adapter
	runtime *fakeSupervisor
}

func (server *Server) automaticRetryFakeAdapter() (automaticRetryFakeRoute, bool) {
	adapter, err := server.registry.Get(agentpi.AdapterID)
	if err != nil {
		return automaticRetryFakeRoute{}, false
	}
	fakeAdapter, ok := adapter.(*agentfake.Adapter)
	if !ok {
		return automaticRetryFakeRoute{}, false
	}
	server.supervisor.mu.RLock()
	runtime, ok := server.supervisor.byAdapter[agentpi.AdapterID].(*fakeSupervisor)
	server.supervisor.mu.RUnlock()
	return automaticRetryFakeRoute{adapter: fakeAdapter, runtime: runtime}, ok
}

func automaticRetryEnvelope(reason string) ([]byte, error) {
	instructions := "Retry automatically in the same frozen Task and worktree. Keep commands focused and return only the bounded evidence needed for review."
	if reason == "runtime_output_limit_exceeded" {
		instructions = "Retry automatically in the same frozen Task and worktree. Keep command and tool output bounded, use focused queries and summaries, and do not emit large files or logs to the Agent stream."
	}
	return json.Marshal(retryDeltaEnvelope{
		SchemaVersion: "chora.retry-delta.v1",
		RejectionNote: "Automatic retry after " + reason + ".",
		Instructions:  instructions,
	})
}

func (server *Server) automaticRetryRouteError(ctx context.Context, attempt domain.Attempt) error {
	if attempt.AdapterID() != agentpi.AdapterID || !attempt.AgentExecutionProfileBinding().Bound() {
		return errors.New("automatic retry requires a bound Pi Attempt")
	}
	if _, err := server.registry.Get(agentpi.AdapterID); err != nil {
		return errors.New("Pi adapter is unavailable")
	}
	target, err := execution.NewExecutionTarget(agentpi.AdapterID, attempt.AgentExecutionProfileBinding().ExecutionProvider())
	if err != nil || !server.supervisor.hasTarget(target) {
		return errors.New("Pi execution target is unavailable")
	}
	if attempt.AgentExecutionProfileBinding().ExecutionProvider() != domain.DockerExecutionProvider {
		return nil
	}
	if err := server.requireManagedGenerationConfiguration(); err != nil {
		return err
	}
	if server.managesInstalledGeneration() {
		if err := server.requireLoadedGenerationCurrent(ctx); err != nil {
			return err
		}
	}
	if server.piPreflight == nil {
		return errors.New("Pi Preflight gate is unavailable")
	}
	report := server.piPreflight(ctx)
	if report.Status != preflight.StatusPassed || report.ResourcesCreated {
		return errors.New("Pi Preflight did not prove a zero-resource ready state")
	}
	if server.piBaseline != nil {
		if err := server.piBaseline(); err != nil {
			return errors.New("frozen Baseline identity is unavailable")
		}
	}
	if server.product && (!server.verifierStatus.Enabled || server.verifier == nil) {
		return errors.New("independent Verifier is unavailable")
	}
	return nil
}

func (server *Server) blockAutomaticRetry(ctx context.Context, run domain.AgentRun, attemptID domain.AttemptID, code string, cause error) {
	server.logger.Printf("block automatic retry Run %s (%s): %v", run.ID(), code, cause)
	if err := server.service.BlockAutomaticRetry(ctx, run.ID(), attemptID, run.Version(), code); err != nil && !errors.Is(err, storecontract.ErrVersionConflict) {
		server.logger.Printf("persist automatic retry block Run %s: %v", run.ID(), err)
	}
}

func automaticRetryViewOf(status *app.AutomaticRetry) *automaticRetryView {
	if status == nil {
		return nil
	}
	return &automaticRetryView{State: status.State, RetriesUsed: status.RetriesUsed, MaxRetries: status.MaxRetries, LastFailureReason: status.LastFailureReason}
}

func automaticRetryCurrentAction(action currentActionView, status *app.AutomaticRetry) currentActionView {
	if status == nil || status.State != app.AutomaticRetryPending && status.State != app.AutomaticRetryRetrying {
		return action
	}
	action.Kind = app.CurrentActionMonitorRun
	action.Reason = "The automatic Agent retry is queued or running."
	return action
}

func automaticRetryBlockedCopy(reason string) string {
	switch reason {
	case "automatic_retry_route_unavailable":
		return "Automatic retry is blocked because the recorded Pi execution route is unavailable. Restore Pi and the Task worktree, then recover explicitly."
	case "automatic_retry_instruction_invalid":
		return "Automatic retry could not create a valid bounded retry request. Inspect the Run before recovering explicitly."
	case "automatic_retry_prepare_failed":
		return "Automatic retry could not safely prepare the next Agent attempt. Inspect the Run before recovering explicitly."
	case "automatic_retry_binding_unavailable":
		return "Automatic retry is blocked because the recorded Agent execution binding is unavailable. Restore the binding, then recover explicitly."
	case "automatic_retry_start_failed":
		return "Automatic retry could not prove a safe Agent start. Inspect runtime readiness before recovering explicitly."
	default:
		return "Automatic retry is blocked and requires an explicit recovery decision."
	}
}
