package localweb

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

const planningRequirement = "Research supplied designs and propose bounded assignments.\n" + domain.DelegationPlanningMarker + "\nReturn one chora-delegation-plan block using schemaVersion chora.delegation-plan.v1 with one to four assignments."
const planningResult = "# Plan\n\nSource: supplied:design.\n```chora-delegation-plan\n" + `{"schemaVersion":"chora.delegation-plan.v1","assignments":[{"role":"Analyst","title":"Compare designs","requirement":"Compare supplied designs and cite sources."}]}` + "\n```\n"

func planningFixture(t *testing.T) (*Server, *taskResourceLifecycleSupervisor, taskRefView) {
	t.Helper()
	server, runtime, parent, _ := delegationFixtureWithRequirement(t, planningRequirement)
	acceptTaskPlan(t, server.Handler(), parent.ID)
	return server, runtime, parent
}
func startPlanning(t *testing.T, server *Server, id string) app.DelegationView {
	t.Helper()
	var view app.DelegationView
	requestJSONWithHeaders(t, server.Handler(), http.MethodPost, "/api/tasks/"+id+"/delegation/planning", map[string]any{}, map[string]string{"Idempotency-Key": "start-planning"}, 201, &view)
	if view.Planning == nil || view.Planning.RunID == "" {
		t.Fatalf("missing planning Run: %#v", view)
	}
	return view
}
func planningView(t *testing.T, server *Server, id string) app.DelegationView {
	t.Helper()
	var view app.DelegationView
	requestJSON(t, server.Handler(), http.MethodGet, "/api/tasks/"+id+"/delegation", nil, 200, &view)
	return view
}
func planningAction(t *testing.T, server *Server, id, action string, version uint64) app.DelegationView {
	t.Helper()
	var view app.DelegationView
	requestJSONWithHeaders(t, server.Handler(), http.MethodPost, "/api/tasks/"+id+"/delegation/planning/"+action, map[string]any{"expectedVersion": version}, map[string]string{"Idempotency-Key": action + "-planning"}, 200, &view)
	return view
}
func finishPlanning(t *testing.T, server *Server, runtime *taskResourceLifecycleSupervisor, runID, text string) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "content": []map[string]any{{"type": "text", "text": text}}, "stopReason": "stop"}})
	runtime.fakeSupervisor.mu.Lock()
	runtime.fakeSupervisor.streams[execution.StreamStdout] = append(raw, []byte("\n{\"type\":\"agent_settled\"}\n")...)
	runtime.fakeSupervisor.streams[execution.StreamStderr] = nil
	runtime.fakeSupervisor.mu.Unlock()
	if err := runtime.Notify(execution.StreamStdout); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Exit(); err != nil {
		t.Fatal(err)
	}
	waitForRunState(t, server.Handler(), runID, "awaiting_review")
}
func TestPlanningDelegationSingleStartFreezesSourceAndFinishes(t *testing.T) {
	server, runtime, parent := planningFixture(t)
	ctx := context.Background()
	h := server.Handler()
	view := startPlanning(t, server, parent.ID)
	replay := startPlanning(t, server, parent.ID)
	if replay.Planning.RunID != view.Planning.RunID {
		t.Fatal("replay created another planning Run")
	}
	endpoint := "/api/tasks/" + parent.ID + "/delegation"
	requestJSONWithHeaders(t, h, http.MethodPost, endpoint+"/planning", map[string]any{}, map[string]string{"Idempotency-Key": "second-planning-start"}, 409, nil)
	requestJSONWithHeaders(t, h, http.MethodPost, endpoint, delegationPlan(), map[string]string{"Idempotency-Key": "conflicting-manual"}, 409, nil)
	requestJSONWithHeaders(t, h, http.MethodPost, "/api/tasks/"+parent.ID+"/runs", map[string]any{}, map[string]string{"Idempotency-Key": "conflicting-run"}, 409, nil)
	server.dispatchDelegations(ctx)
	view = planningView(t, server, parent.ID)
	finishPlanning(t, server, runtime, view.Planning.RunID, planningResult)
	server.dispatchDelegations(ctx)
	view = planningView(t, server, parent.ID)
	if view.Planning.State != domain.DelegationPlanningImported || view.Source == nil || view.Source.RunID != view.Planning.RunID || len(view.Assignments) != 1 || view.Children[0].State != "running" {
		t.Fatalf("import=%#v", view)
	}
	source := *view.Source
	finishDelegationChild(t, server, runtime, view.Children[0].RunID)
	server.dispatchDelegations(ctx)
	view = planningView(t, server, parent.ID)
	if view.State != "awaiting_review" || *view.Source != source {
		t.Fatalf("complete=%#v", view)
	}
	if n, _ := runtime.snapshot(); n != 2 {
		t.Fatalf("want one planner and one child, got %d", n)
	}
	taskID, _ := domain.ParseTaskID(parent.ID)
	doc, err := server.service.GetProjectDocument(ctx, taskID)
	if err != nil || doc.Version != 0 || len(doc.Reviews) != 0 {
		t.Fatalf("fabricated document review: %#v %v", doc, err)
	}
	runID, _ := domain.ParseRunID(view.Planning.RunID)
	run, _ := server.store.Reader().GetRun(ctx, runID)
	_, err = server.service.PrepareRetry(ctx, app.PrepareRetryRequest{CommandMeta: app.CommandMeta{ActorID: "local-human", SessionID: "test", IdempotencyKey: "forbidden-replan"}, RunID: runID, ExpectedVersion: run.Version(), Reason: "another plan", Instructions: "repair"})
	if !errors.Is(err, app.ErrInvalidCommand) {
		t.Fatalf("second planning Attempt accepted: %v", err)
	}
}
func TestPlanningDelegationStopBeforeLaunchAndBeforeImport(t *testing.T) {
	for _, started := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-launch", true: "before-import"}[started], func(t *testing.T) {
			server, runtime, parent := planningFixture(t)
			ctx := context.Background()
			view := startPlanning(t, server, parent.ID)
			if started {
				server.dispatchDelegations(ctx)
				finishPlanning(t, server, runtime, view.Planning.RunID, planningResult)
				view = planningView(t, server, parent.ID)
			}
			planningAction(t, server, parent.ID, "stop", view.Planning.Version)
			server.dispatchDelegations(ctx)
			view = planningView(t, server, parent.ID)
			if view.Planning.State != domain.DelegationPlanningStopped || view.State != "not_started" || view.Source != nil {
				t.Fatalf("stop imported plan: %#v", view)
			}
			taskID, _ := domain.ParseTaskID(parent.ID)
			if err := server.service.ImportPlanningDelegation(ctx, taskID); !errors.Is(err, storecontract.ErrVersionConflict) {
				t.Fatalf("late import allowed: %v", err)
			}
			want := 0
			if started {
				want = 1
			}
			if n, _ := runtime.snapshot(); n != want {
				t.Fatalf("late launch %d", n)
			}
		})
	}
}
func TestPlanningDelegationRestartResumesSamePreparedAttempt(t *testing.T) {
	server, runtime, parent := planningFixture(t)
	ctx := context.Background()
	view := startPlanning(t, server, parent.ID)
	taskID, _ := domain.ParseTaskID(parent.ID)
	runID, _ := domain.ParseRunID(view.Planning.RunID)
	p, _ := server.store.Reader().GetDelegationPlanning(ctx, taskID)
	run, _ := server.store.Reader().GetRun(ctx, runID)
	prepared, err := server.service.PrepareRun(ctx, app.PrepareRunRequest{CommandMeta: planningMeta(p, "prepare"), RunID: runID, ExpectedVersion: run.Version()})
	if err != nil {
		t.Fatal(err)
	}
	if err = server.recoverDelegations(ctx); err != nil {
		t.Fatal(err)
	}
	server.dispatchDelegations(ctx)
	view = planningView(t, server, parent.ID)
	if view.Planning.State != domain.DelegationPlanningBlocked {
		t.Fatalf("restart=%#v", view)
	}
	if n, _ := runtime.snapshot(); n != 0 {
		t.Fatal("restart launched without resume")
	}
	planningAction(t, server, parent.ID, "resume", view.Planning.Version)
	server.dispatchDelegations(ctx)
	view = planningView(t, server, parent.ID)
	current, err := server.store.Reader().GetCurrentAttempt(ctx, runID)
	if err != nil || current.ID() != prepared.Attempt.ID() || current.Sequence() != 1 {
		t.Fatalf("resume replanned: %v", err)
	}
	finishPlanning(t, server, runtime, runID.String(), planningResult)
	if err = server.recoverDelegations(ctx); err != nil {
		t.Fatal(err)
	}
	server.dispatchDelegations(ctx)
	view = planningView(t, server, parent.ID)
	if view.Source != nil {
		t.Fatal("restart imported before explicit resume")
	}
	// Different command identity for the second explicit recovery.
	requestJSONWithHeaders(t, server.Handler(), http.MethodPost, "/api/tasks/"+parent.ID+"/delegation/planning/resume", map[string]any{"expectedVersion": view.Planning.Version}, map[string]string{"Idempotency-Key": "resume-complete"}, 200, nil)
	server.dispatchDelegations(ctx)
	view = planningView(t, server, parent.ID)
	if view.Source == nil || view.Source.AttemptID != prepared.Attempt.ID().String() {
		t.Fatalf("lost source after resume: %#v", view)
	}
}
func TestPlanningDelegationInvalidProposalBlocksWithoutRepair(t *testing.T) {
	server, runtime, parent := planningFixture(t)
	ctx := context.Background()
	view := startPlanning(t, server, parent.ID)
	server.dispatchDelegations(ctx)
	finishPlanning(t, server, runtime, view.Planning.RunID, "# Finding\nNo plan was produced. Source: supplied:design.")
	server.dispatchDelegations(ctx)
	server.dispatchAutomaticRetries(ctx)
	view = planningView(t, server, parent.ID)
	if view.Planning.State != domain.DelegationPlanningBlocked || view.Source != nil || view.State != "not_started" {
		t.Fatalf("invalid plan imported: %#v", view)
	}
	if n, _ := runtime.snapshot(); n != 1 {
		t.Fatalf("repaired automatically: %d", n)
	}
}
func TestPlanningDelegationRequiresFrozenPlanningIntent(t *testing.T) {
	server, runtime, parent, _ := delegationFixture(t)
	acceptTaskPlan(t, server.Handler(), parent.ID)
	requestJSONWithHeaders(t, server.Handler(), http.MethodPost, "/api/tasks/"+parent.ID+"/delegation/planning", map[string]any{}, map[string]string{"Idempotency-Key": "not-a-planner"}, 422, nil)
	if view := planningView(t, server, parent.ID); view.Planning != nil {
		t.Fatal("invalid intent persisted")
	}
	if n, _ := runtime.snapshot(); n != 0 {
		t.Fatal("invalid intent launched")
	}
}

func TestPlanningDelegationStopCancelsRunningPlanner(t *testing.T) {
	server, runtime, parent := planningFixture(t)
	ctx := context.Background()
	view := startPlanning(t, server, parent.ID)
	server.dispatchDelegations(ctx)
	view = planningView(t, server, parent.ID)
	planningAction(t, server, parent.ID, "stop", view.Planning.Version)
	server.dispatchDelegations(ctx)
	server.dispatchAutomaticRetries(ctx)
	view = planningView(t, server, parent.ID)
	runID, _ := domain.ParseRunID(view.Planning.RunID)
	run, err := server.store.Reader().GetRun(ctx, runID)
	if err != nil || run.State() != domain.RunStateCancelled || view.Planning.State != domain.DelegationPlanningStopped || view.Source != nil {
		t.Fatalf("running stop=%#v run=%v err=%v", view, run.State(), err)
	}
	if n, _ := runtime.snapshot(); n != 1 {
		t.Fatalf("stop launched children: %d", n)
	}
}
func TestPlanningDelegationStopFencesPreparedLaunch(t *testing.T) {
	server, runtime, parent := planningFixture(t)
	ctx := context.Background()
	view := startPlanning(t, server, parent.ID)
	taskID, _ := domain.ParseTaskID(parent.ID)
	runID, _ := domain.ParseRunID(view.Planning.RunID)
	p, _ := server.store.Reader().GetDelegationPlanning(ctx, taskID)
	run, _ := server.store.Reader().GetRun(ctx, runID)
	prepared, err := server.service.PrepareRun(ctx, app.PrepareRunRequest{CommandMeta: planningMeta(p, "prepare"), RunID: runID, ExpectedVersion: run.Version()})
	if err != nil {
		t.Fatal(err)
	}
	view = planningView(t, server, parent.ID)
	planningAction(t, server, parent.ID, "stop", view.Planning.Version)
	if _, err = server.service.StartAttempt(ctx, app.StartAttemptRequest{CommandMeta: planningMeta(p, "late-start"), RunID: runID, ExpectedVersion: prepared.Run.Version(), Mode: app.StartFresh}); !errors.Is(err, app.ErrUnauthorizedCommand) {
		t.Fatalf("late launch not denied: %v", err)
	}
	if err = server.recoverDelegations(ctx); err != nil {
		t.Fatal(err)
	}
	server.dispatchDelegations(ctx)
	view = planningView(t, server, parent.ID)
	if view.Planning.State != domain.DelegationPlanningStopped {
		t.Fatalf("prepared stop=%#v", view)
	}
	if n, _ := runtime.snapshot(); n != 0 {
		t.Fatal("prepared stop launched")
	}
}

func TestPlanningDelegationStopAndImportCommitOnlyOneWinner(t *testing.T) {
	server, runtime, parent := planningFixture(t)
	ctx := context.Background()
	view := startPlanning(t, server, parent.ID)
	server.dispatchDelegations(ctx)
	finishPlanning(t, server, runtime, view.Planning.RunID, planningResult)
	view = planningView(t, server, parent.ID)
	id, _ := domain.ParseTaskID(parent.ID)
	gate := make(chan struct{})
	imported := make(chan error, 1)
	stopped := make(chan error, 1)
	go func() { <-gate; imported <- server.service.ImportPlanningDelegation(ctx, id) }()
	go func() {
		<-gate
		_, err := server.service.ChangeDelegationPlanning(ctx, app.ChangeDelegationRequest{CommandMeta: app.CommandMeta{ActorID: "local-human", SessionID: "test", IdempotencyKey: "racing-stop"}, ParentTaskID: id, ExpectedVersion: view.Planning.Version, Action: "stop"})
		stopped <- err
	}()
	close(gate)
	importErr, stopErr := <-imported, <-stopped
	view = planningView(t, server, parent.ID)
	switch view.Planning.State {
	case domain.DelegationPlanningImported:
		if importErr != nil || !errors.Is(stopErr, storecontract.ErrVersionConflict) || view.Source == nil || len(view.Assignments) != 1 {
			t.Fatalf("import winner lost atomicity: import=%v stop=%v view=%#v", importErr, stopErr, view)
		}
	case domain.DelegationPlanningStopping:
		if stopErr != nil || !errors.Is(importErr, storecontract.ErrVersionConflict) || view.Source != nil || len(view.Assignments) != 0 {
			t.Fatalf("stop winner leaked source: import=%v stop=%v view=%#v", importErr, stopErr, view)
		}
	default:
		t.Fatalf("unexpected race result: %#v import=%v stop=%v", view, importErr, stopErr)
	}
	if n, _ := runtime.snapshot(); n != 1 {
		t.Fatal("commit race launched a child outside dispatcher")
	}
}

func TestPlanningDelegationFailedAttemptNeverRetriesAndCanStop(t *testing.T) {
	server, runtime, parent := planningFixture(t)
	ctx := context.Background()
	view := startPlanning(t, server, parent.ID)
	server.dispatchDelegations(ctx)
	runtime.fakeSupervisor.mu.Lock()
	runtime.fakeSupervisor.streams[execution.StreamStdout] = []byte("{\"type\":\"agent_settled\"}\n")
	runtime.fakeSupervisor.streams[execution.StreamStderr] = nil
	runtime.fakeSupervisor.mu.Unlock()
	if err := runtime.Notify(execution.StreamStdout); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Exit(); err != nil {
		t.Fatal(err)
	}
	waitForRunState(t, server.Handler(), view.Planning.RunID, "recovery_required")
	server.dispatchAutomaticRetries(ctx)
	view = planningView(t, server, parent.ID)
	if view.Planning.State != domain.DelegationPlanningBlocked {
		t.Fatalf("failure not blocked: %#v", view)
	}
	if n, _ := runtime.snapshot(); n != 1 {
		t.Fatal("failed planning retried")
	}
	planningAction(t, server, parent.ID, "stop", view.Planning.Version)
	server.dispatchDelegations(ctx)
	view = planningView(t, server, parent.ID)
	if view.Planning.State != domain.DelegationPlanningStopped {
		t.Fatalf("failed planner could not stop: %#v", view)
	}
}
