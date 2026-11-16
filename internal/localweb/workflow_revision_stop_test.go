package localweb

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/store/sqlite"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

// Independent behavioral regressions on the pinned product. All execution is synthetic.
func TestWorkflowRegressionStopWhenPGUnavailable(t *testing.T) {
	s, runtime, task, _ := workflowFixture(t)
	ctx := context.Background()
	w := waitingWorkflow(t, s, submitWorkflowFixture(t, s, task))
	requestJSONWithHeaders(t, s.Handler(), http.MethodPost, workflowPath(w, "decision"), workflowDecision(w), map[string]string{"Idempotency-Key": "accept"}, 200, nil)
	s.dispatchWorkflows(ctx)
	w, _ = s.store.Reader().GetWorkflow(ctx, w.ID)
	if n, _ := runtime.snapshot(); n != 1 {
		t.Fatal("fixture did not launch", n)
	}
	s.workflowBridge.(*fixtureResearchBridge).fail = true
	requestJSONWithHeaders(t, s.Handler(), http.MethodPost, workflowPath(w, "stop"), map[string]any{}, map[string]string{"Idempotency-Key": "stop-offline"}, 200, nil)
	w, _ = s.store.Reader().GetWorkflow(ctx, w.ID)
	run, _ := s.store.Reader().GetRun(ctx, w.RunID)
	t.Logf("stopRequested=%v workflow=%s run=%s", w.StopRequested, w.Status, run.State())
	if !w.StopRequested {
		t.Fatal("stop intent lost")
	}
	if run.State() == domain.RunStateRunning {
		t.Fatal("PG outage prevented local Pi cancellation: Run remains running")
	}
}

type workflowStopFaultBridge struct {
	fixtureResearchBridge
	mode             string
	entered, release chan struct{}
}

func (b *workflowStopFaultBridge) Execute(ctx context.Context, c domain.WorkflowCommand) (json.RawMessage, error) {
	if c.Kind != "stop" {
		return b.fixtureResearchBridge.Execute(ctx, c)
	}
	if b.mode == "blocked" {
		close(b.entered)
		select {
		case <-b.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if b.mode == "execute" {
		return nil, errors.New("synthetic research transport unavailable")
	}
	if b.mode == "receipt" {
		return []byte(`{"operation_id":"wrong"}`), nil
	}
	if b.mode == "unknown" {
		return json.Marshal(map[string]string{"operation_id": c.OperationID, "workflow_id": c.WorkflowID, "input_digest": c.InputDigest, "status": "unresolved"})
	}
	return b.fixtureResearchBridge.Execute(ctx, c)
}

type workflowUncertainStop struct{ delegationTestSupervisor }

type workflowBlockedLaunch struct {
	delegationTestSupervisor
	entered, release chan struct{}
}

func (s workflowBlockedLaunch) Start(ctx context.Context, invocation execution.Invocation, sink execution.RuntimeSink) execution.StartOutcome {
	close(s.entered)
	<-s.release
	return s.delegationTestSupervisor.Start(ctx, invocation, sink)
}

func TestWorkflowStopPersistsWhileAcceptedLaunchIsInFlight(t *testing.T) {
	s, runtime, task, _ := workflowFixture(t)
	ctx := context.Background()
	w := approvedWorkflow(t, s, submitWorkflowFixture(t, s, task))
	blocked := workflowBlockedLaunch{delegationTestSupervisor: delegationTestSupervisor{runtime}, entered: make(chan struct{}), release: make(chan struct{})}
	target, _ := execution.NewExecutionTarget("pi", domain.TrustedHostExecutionProvider)
	s.supervisor.byTarget[target] = blocked
	s.workflowBridge = &workflowStopFaultBridge{mode: "execute"}
	dispatchDone := make(chan struct{})
	go func() { defer close(dispatchDone); s.dispatchWorkflows(ctx) }()
	select {
	case <-blocked.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("fixture launch did not enter supervisor")
	}
	stopDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		r := newLocalRequest(http.MethodPost, workflowPath(w, "stop"), bytes.NewBufferString("{}"))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", "race-stop")
		writer := httptest.NewRecorder()
		s.Handler().ServeHTTP(writer, r)
		stopDone <- writer
	}()
	defer func() {
		close(blocked.release)
		<-dispatchDone
		writer := <-stopDone
		if writer.Code != 200 {
			t.Errorf("Stop=%d %s", writer.Code, writer.Body.String())
		}
		got, _ := s.store.Reader().GetWorkflow(ctx, w.ID)
		run, _ := s.store.Reader().GetRun(ctx, got.RunID)
		if !got.StopRequested || got.Status != "unresolved" || run.State() != domain.RunStateCancelled {
			t.Errorf("post-race workflow=%+v Run=%s", got, run.State())
		}
	}()
	deadline := time.Now().Add(time.Second)
	for {
		got, _ := s.store.Reader().GetWorkflow(ctx, w.ID)
		if got.StopRequested {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Stop intent waited for external launch I/O")
		}
		time.Sleep(time.Millisecond)
	}
}

func (s workflowUncertainStop) Stop(context.Context, execution.RuntimeHandle, execution.StopIntent) (execution.StopOutcome, error) {
	return execution.StopOutcome{Kind: execution.StopUncertain}, nil
}

func TestWorkflowStopSideFaultsRetainSeparateProofs(t *testing.T) {
	for _, mode := range []string{"execute", "receipt", "unknown", "local-unknown"} {
		t.Run(mode, func(t *testing.T) {
			s, runtime, task, _ := workflowFixture(t)
			ctx := context.Background()
			w := waitingWorkflow(t, s, submitWorkflowFixture(t, s, task))
			requestJSONWithHeaders(t, s.Handler(), http.MethodPost, workflowPath(w, "decision"), workflowDecision(w), map[string]string{"Idempotency-Key": "accept"}, 200, nil)
			s.dispatchWorkflows(ctx)
			w, _ = s.store.Reader().GetWorkflow(ctx, w.ID)
			s.workflowBridge = &workflowStopFaultBridge{mode: mode}
			if mode == "local-unknown" {
				target, _ := execution.NewExecutionTarget("pi", domain.TrustedHostExecutionProvider)
				s.supervisor.byTarget[target] = workflowUncertainStop{delegationTestSupervisor{runtime}}
			}
			requestJSONWithHeaders(t, s.Handler(), http.MethodPost, workflowPath(w, "stop"), map[string]any{}, map[string]string{"Idempotency-Key": "stop"}, 200, nil)
			w, _ = s.store.Reader().GetWorkflow(ctx, w.ID)
			run, _ := s.store.Reader().GetRun(ctx, w.RunID)
			if w.Status != "unresolved" || !w.StopRequested {
				t.Fatalf("false stop proof: %+v", w)
			}
			if mode != "local-unknown" && run.State() != domain.RunStateCancelled {
				t.Fatalf("research fault blocked local Stop: %s", run.State())
			}
			if mode == "local-unknown" && run.State() == domain.RunStateCancelled {
				t.Fatal("uncertain local process claimed cancelled")
			}
			events, _ := s.store.Reader().ListWorkflowEvents(ctx, w.ID, 0)
			outcomes := map[string]string{}
			for _, event := range events {
				if event.Kind == "stop_result" {
					var value map[string]string
					_ = json.Unmarshal(event.Data, &value)
					outcomes[value["side"]] = value["result"]
				}
			}
			if len(outcomes) != 2 || outcomes["pi"] == "" || outcomes["research"] == "" {
				t.Fatalf("independent outcomes missing: %v", outcomes)
			}
			if mode == "local-unknown" {
				if outcomes["pi"] != "unresolved" || outcomes["research"] != "confirmed" {
					t.Fatal(outcomes)
				}
			} else {
				if outcomes["pi"] != "confirmed" || outcomes["research"] != "unresolved" {
					t.Fatal(outcomes)
				}
			}
		})
	}
}

func TestWorkflowStopDoesNotWaitForResearchBeforeCancellingPi(t *testing.T) {
	s, _, task, _ := workflowFixture(t)
	ctx := context.Background()
	w := waitingWorkflow(t, s, submitWorkflowFixture(t, s, task))
	requestJSONWithHeaders(t, s.Handler(), http.MethodPost, workflowPath(w, "decision"), workflowDecision(w), map[string]string{"Idempotency-Key": "accept"}, 200, nil)
	s.dispatchWorkflows(ctx)
	w, _ = s.store.Reader().GetWorkflow(ctx, w.ID)
	bridge := &workflowStopFaultBridge{mode: "blocked", entered: make(chan struct{}), release: make(chan struct{})}
	s.workflowBridge = bridge
	w, err := s.service.StopWorkflow(ctx, commandMeta("stop-blocked"), w.ID, w.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.deliverWorkflowStop(ctx, w) }()
	select {
	case <-bridge.entered:
	case <-time.After(time.Second):
		t.Fatal("bridge not entered")
	}
	// Always release the bridge before returning, including assertion failure.
	defer func() {
		close(bridge.release)
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	deadline := time.Now().Add(time.Second)
	for {
		run, _ := s.store.Reader().GetRun(ctx, w.RunID)
		if run.State() == domain.RunStateCancelled {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("local cancel waited on research")
		}
		time.Sleep(time.Millisecond)
	}
}

func rejectedWorkflowRun(t *testing.T, s *Server, runtime *taskResourceLifecycleSupervisor, task taskRefView) (domain.WorkflowExecution, domain.AgentRun) {
	t.Helper()
	ctx := context.Background()
	w := waitingWorkflow(t, s, submitWorkflowFixture(t, s, task))
	requestJSONWithHeaders(t, s.Handler(), http.MethodPost, workflowPath(w, "decision"), workflowDecision(w), map[string]string{"Idempotency-Key": "accept"}, 200, nil)
	s.dispatchWorkflows(ctx)
	w, _ = s.store.Reader().GetWorkflow(ctx, w.ID)
	finishDelegationChild(t, s, runtime, w.RunID.String())
	s.dispatchWorkflows(ctx)
	w, _ = s.store.Reader().GetWorkflow(ctx, w.ID)
	run, _ := s.store.Reader().GetRun(ctx, w.RunID)
	view, err := s.runView(ctx, run.ID())
	if err != nil {
		t.Fatal(err)
	}
	requestJSONWithHeaders(t, s.Handler(), http.MethodPost, "/api/v2/runs/"+run.ID().String()+"/review", map[string]any{"expectedVersion": run.Version(), "kind": "reject", "comment": "Add the missing explanation.", "resultDigest": view.ResourceResult.Digest}, map[string]string{"Idempotency-Key": "human-reject"}, 200, nil)
	run, _ = s.store.Reader().GetRun(ctx, w.RunID)
	return w, run
}

func TestWorkflowPreparedRevisionRecoveryReusesAttemptAndHumanAuthority(t *testing.T) {
	s, runtime, task, root := workflowFixture(t)
	ctx := context.Background()
	w, run := rejectedWorkflowRun(t, s, runtime, task)
	instructions := "Add the missing explanation within the frozen scope."
	delta, _ := json.Marshal(map[string]string{"instructions": instructions})
	prepared, err := s.service.PrepareRetry(ctx, app.PrepareRetryRequest{CommandMeta: commandMeta("prepare-revision"), RunID: run.ID(), ExpectedVersion: run.Version(), Reason: "Human rejected result", Instructions: string(delta)})
	if err != nil {
		t.Fatal(err)
	}
	// Emulate exactly the old persisted failure: Attempt2 exists, no session,
	// projection is awaiting_review and no new revision authorization receipt.
	paths, _ := filepath.Glob(filepath.Join(filepath.Dir(root), "*", "chora.db"))
	if len(paths) != 1 {
		t.Fatal(paths)
	}
	raw, err := sql.Open("sqlite", paths[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec("DROP TRIGGER workflow_command_no_delete"); err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec("DELETE FROM workflow_commands WHERE workflow_id=? AND kind LIKE 'revision:%'", w.ID); err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()
	if err = s.service.MarkWorkflow(ctx, w.ID, "awaiting_review", ""); err != nil {
		t.Fatal(err)
	}
	db, err := sqlite.Open(ctx, paths[0])
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	fresh := app.NewService(app.Dependencies{Store: db, Authorizer: localAuthorizer{}})
	q := app.PrepareRetryRequest{CommandMeta: commandMeta("recover-revision"), RunID: run.ID(), ExpectedVersion: prepared.Run.Version(), Instructions: instructions}
	bad := q
	bad.Instructions = "Change the frozen instruction"
	if _, err = fresh.RecoverPreparedWorkflowRevision(ctx, bad); err == nil {
		t.Fatal("changed prepared instructions admitted")
	}
	for i := 0; i < 2; i++ {
		replayed, e := fresh.RecoverPreparedWorkflowRevision(ctx, q)
		if e != nil || replayed.Attempt.ID() != prepared.Attempt.ID() {
			t.Fatalf("recovery %d: %+v %v", i, replayed, e)
		}
	}
	s.dispatchWorkflows(ctx)
	current, _ := s.store.Reader().GetCurrentAttempt(ctx, run.ID())
	if current.ID() != prepared.Attempt.ID() || current.Sequence() != 2 {
		t.Fatal("recovery created another Attempt")
	}
	if n, _ := runtime.snapshot(); n != 2 {
		t.Fatalf("starts=%d", n)
	}
	currentWorkflow, _ := s.store.Reader().GetWorkflow(ctx, w.ID)
	if currentWorkflow.RunID != w.RunID || currentWorkflow.Gate.ID != w.Gate.ID || currentWorkflow.InputDigest != w.InputDigest || currentWorkflow.Gate.Instructions != w.Gate.Instructions {
		t.Fatal("revision replaced frozen authority")
	}
	s.dispatchWorkflows(ctx)
	if n, _ := runtime.snapshot(); n != 2 {
		t.Fatal("recovery dispatch duplicated launch")
	}
}

func TestWorkflowHumanRevisionNeverOverridesStopOrAutomation(t *testing.T) {
	s, runtime, task, _ := workflowFixture(t)
	ctx := context.Background()
	w, run := rejectedWorkflowRun(t, s, runtime, task)
	q := app.PrepareRetryRequest{CommandMeta: commandMeta("automatic-revision"), RunID: run.ID(), ExpectedVersion: run.Version(), Reason: "fake retry", Instructions: `{"instructions":"same scope"}`, Automatic: true}
	if _, err := s.service.PrepareRetry(ctx, q); err == nil {
		t.Fatal("old automatic retry admitted workflow")
	}
	if _, err := s.service.StopWorkflow(ctx, commandMeta("stop-revision"), w.ID, w.ProjectID); err != nil {
		t.Fatal(err)
	}
	q.Automatic = false
	q.CommandMeta = commandMeta("human-after-stop")
	if _, err := s.service.PrepareRetry(ctx, q); err == nil {
		t.Fatal("human revision ignored Stop")
	}
	attempt, _ := s.store.Reader().GetCurrentAttempt(ctx, run.ID())
	if attempt.Sequence() != 1 {
		t.Fatal("refused revision still created Attempt")
	}
}

func TestWorkflowRegressionStopAvailableControl(t *testing.T) {
	s, _, task, _ := workflowFixture(t)
	ctx := context.Background()
	w := waitingWorkflow(t, s, submitWorkflowFixture(t, s, task))
	requestJSONWithHeaders(t, s.Handler(), http.MethodPost, workflowPath(w, "decision"), workflowDecision(w), map[string]string{"Idempotency-Key": "accept"}, 200, nil)
	s.dispatchWorkflows(ctx)
	w, _ = s.store.Reader().GetWorkflow(ctx, w.ID)
	requestJSONWithHeaders(t, s.Handler(), http.MethodPost, workflowPath(w, "stop"), map[string]any{}, map[string]string{"Idempotency-Key": "stop-available"}, 200, nil)
	w, _ = s.store.Reader().GetWorkflow(ctx, w.ID)
	run, _ := s.store.Reader().GetRun(ctx, w.RunID)
	t.Logf("stopRequested=%v workflow=%s run=%s", w.StopRequested, w.Status, run.State())
	if !w.StopRequested || w.Status != "stopped" || run.State() != domain.RunStateCancelled {
		t.Fatal("normal stop failed")
	}
}

func TestWorkflowRegressionHumanRetryAfterReview(t *testing.T) {
	s, runtime, task, _ := workflowFixture(t)
	ctx := context.Background()
	w := waitingWorkflow(t, s, submitWorkflowFixture(t, s, task))
	requestJSONWithHeaders(t, s.Handler(), http.MethodPost, workflowPath(w, "decision"), workflowDecision(w), map[string]string{"Idempotency-Key": "accept"}, 200, nil)
	s.dispatchWorkflows(ctx)
	w, _ = s.store.Reader().GetWorkflow(ctx, w.ID)
	finishDelegationChild(t, s, runtime, w.RunID.String())
	s.dispatchWorkflows(ctx)
	w, _ = s.store.Reader().GetWorkflow(ctx, w.ID)
	run, _ := s.store.Reader().GetRun(ctx, w.RunID)
	view, e := s.runView(ctx, w.RunID)
	if e != nil || view.ResourceResult == nil {
		t.Fatal("resource result missing", e)
	}
	requestJSONWithHeaders(t, s.Handler(), http.MethodPost, "/api/v2/runs/"+w.RunID.String()+"/review", map[string]any{"expectedVersion": run.Version(), "kind": "reject", "comment": "Add the missing compatibility explanation.", "resultDigest": view.ResourceResult.Digest}, map[string]string{"Idempotency-Key": "human-reject"}, 200, nil)
	run, _ = s.store.Reader().GetRun(ctx, w.RunID)
	t.Logf("workflow=%s rejectedRun=%s", w.Status, run.State())
	requestJSONWithHeaders(t, s.Handler(), http.MethodPost, "/api/runs/"+w.RunID.String()+"/retry", map[string]any{"expectedVersion": run.Version(), "instructions": "Add the missing explanation within the original scope."}, map[string]string{"Idempotency-Key": "human-retry"}, 200, nil)
}

func TestWorkflowRegressionLegacyHumanRetryControl(t *testing.T) {
	s, runtime, task, _ := workflowFixture(t)
	ctx := context.Background()
	var started runView
	requestJSONWithHeaders(t, s.Handler(), http.MethodPost, "/api/tasks/"+task.ID+"/runs", map[string]any{}, map[string]string{"Idempotency-Key": "legacy-start"}, 201, &started)
	id, _ := domain.ParseRunID(started.ID)
	finishDelegationChild(t, s, runtime, started.ID)
	view, e := s.runView(ctx, id)
	if e != nil || view.ResourceResult == nil {
		t.Fatal("legacy resource result missing", e)
	}
	requestJSONWithHeaders(t, s.Handler(), http.MethodPost, "/api/v2/runs/"+started.ID+"/review", map[string]any{"expectedVersion": view.Version, "kind": "reject", "comment": "Add the missing explanation.", "resultDigest": view.ResourceResult.Digest}, map[string]string{"Idempotency-Key": "legacy-human-reject"}, 200, &view)
	requestJSONWithHeaders(t, s.Handler(), http.MethodPost, "/api/runs/"+started.ID+"/retry", map[string]any{"expectedVersion": view.Version, "instructions": "Add the missing explanation within the original scope."}, map[string]string{"Idempotency-Key": "legacy-human-retry"}, 200, nil)
	if n, _ := runtime.snapshot(); n != 2 {
		t.Fatal("legacy retry did not launch", n)
	}
}

func TestWorkflowRegressionNoStartBeforePGBinding(t *testing.T) {
	s, runtime, task, _ := workflowFixture(t)
	ctx := context.Background()
	w := approvedWorkflow(t, s, submitWorkflowFixture(t, s, task))
	run, e := s.service.CreateWorkflowRun(ctx, w.ID)
	if e != nil {
		t.Fatal(e)
	}
	commands, _ := s.store.Reader().ListWorkflowCommands(ctx, w.ID)
	for _, c := range commands {
		if c.Kind == "bind_run" && len(c.Receipt) > 0 {
			t.Fatal("fixture unexpectedly bound PG")
		}
	}
	prepared, e := s.service.PrepareRun(ctx, app.PrepareRunRequest{CommandMeta: commandMeta("manual-prepare"), RunID: run.ID(), ExpectedVersion: run.Version()})
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.service.StartAttempt(ctx, app.StartAttemptRequest{CommandMeta: commandMeta("manual-start"), RunID: run.ID(), ExpectedVersion: prepared.Run.Version(), Mode: app.StartFresh})
	n, _ := runtime.snapshot()
	t.Logf("pending bind_run receipt; startError=%v supervisorStarts=%d", e, n)
	if e == nil || n != 0 {
		t.Fatal("Pi launched before durable PG binding acknowledgement")
	}
}
