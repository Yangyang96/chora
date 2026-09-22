package localweb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/nativecapabilities"
)

func delegationFixture(t *testing.T) (*Server, *taskResourceLifecycleSupervisor, taskRefView, string) {
	return delegationFixtureWithRequirement(t, "Research compatibility and migration risk.")
}
func delegationFixtureWithRequirement(t *testing.T, requirement string) (*Server, *taskResourceLifecycleSupervisor, taskRefView, string) {
	t.Helper()
	server, runtime, dataRoot := newResourceTerminalIntegrationServer(t)
	target, _ := execution.NewExecutionTarget("pi", domain.TrustedHostExecutionProvider)
	server.supervisor.byTarget[target] = delegationTestSupervisor{runtime}
	h := server.Handler()
	requestJSONWithHeaders(t, h, http.MethodPost, "/api/agent-execution/trusted-local-acknowledgements", map[string]any{"policyVersion": domain.TrustedLocalDisclosurePolicy}, map[string]string{"Idempotency-Key": "delegation-ack"}, 200, nil)
	var project resourceProjectTestView
	requestJSON(t, h, http.MethodPost, "/api/v2/projects", map[string]any{"name": "Research delegation"}, 201, &project)
	roomID, _ := domain.ParseRoomID(project.DefaultRoomID)
	revisions, err := server.store.Reader().ListRoomRevisions(context.Background(), roomID)
	if err != nil || len(revisions) == 0 {
		t.Fatalf("room revisions: %v", err)
	}
	var task taskRefView
	requestJSONWithHeaders(t, h, http.MethodPost, "/api/v2/rooms/"+project.DefaultRoomID+"/tasks", map[string]any{"revisionIds": []string{revisions[0].ID().String()}, "title": "Compare designs", "requirement": requirement, "agentExecutionProfile": "trusted_local", "outcomeKind": "document", "materials": []map[string]string{{"title": "Design", "locator": "supplied:design", "body": "Keep existing clients compatible. Unknown: deployment sequence."}}}, map[string]string{"Idempotency-Key": "delegation-parent"}, 201, &task)
	return server, runtime, task, dataRoot
}
func delegationPlan() map[string]any {
	return map[string]any{"assignments": []domain.DelegationAssignment{{Role: "Designer", Title: "Design options", Requirement: "Compare implementation options using supplied material."}, {Role: "Reviewer", Title: "Migration risks", Requirement: "Identify compatibility risks and missing evidence."}}}
}
func finishDelegationChild(t *testing.T, server *Server, runtime *taskResourceLifecycleSupervisor, runID string) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "content": []map[string]any{{"type": "text", "text": "# Finding\n\nKeep compatibility (source: supplied:design). Deployment sequence is unknown."}}, "stopReason": "stop"}})
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
func TestDelegationSequentialJourneyAndFrozenAuthority(t *testing.T) {
	server, runtime, parent, dataRoot := delegationFixture(t)
	h := server.Handler()
	ctx := context.Background()
	endpoint := "/api/tasks/" + parent.ID + "/delegation"
	// Later native config changes must not enter the already-frozen parent or children.
	if _, err := nativecapabilities.Save(dataRoot, parent.ResourceSnapshot.ProjectID, 0, nativecapabilities.Config{DisabledMCPServers: []string{"later-server"}}); err != nil {
		t.Fatal(err)
	}

	var view app.DelegationView
	requestJSONWithHeaders(t, h, http.MethodPost, endpoint, delegationPlan(), map[string]string{"Idempotency-Key": "delegation-start"}, 201, &view)
	requestJSONWithHeaders(t, h, http.MethodPost, endpoint, delegationPlan(), map[string]string{"Idempotency-Key": "delegation-start"}, 201, nil)
	requestJSONWithHeaders(t, h, http.MethodPost, endpoint, delegationPlan(), map[string]string{"Idempotency-Key": "delegation-second-start"}, 409, nil)
	server.dispatchDelegations(ctx)
	requestJSON(t, h, http.MethodGet, endpoint, nil, 200, &view)
	if view.State != "running" || len(view.Children) != 2 || view.Children[0].State != "running" || view.Children[1].TaskID != "" {
		t.Fatalf("first dispatch: %#v", view)
	}
	server.dispatchDelegations(ctx)
	if n, _ := runtime.snapshot(); n != 1 {
		t.Fatalf("duplicate dispatch started %d processes", n)
	}
	parentID, _ := domain.ParseTaskID(parent.ID)
	childID, _ := domain.ParseTaskID(view.Children[0].TaskID)
	ps, e := server.store.Reader().GetTaskExecutionSettings(ctx, parentID)
	if e != nil {
		t.Fatal(e)
	}
	cs, e := server.store.Reader().GetTaskExecutionSettings(ctx, childID)
	if e != nil {
		t.Fatal(e)
	}
	cs.TaskID = ps.TaskID
	cs.CreatedAt = ps.CreatedAt
	if !reflect.DeepEqual(ps, cs) {
		t.Fatalf("child settings drift: %#v != %#v", ps, cs)
	}
	requestJSONWithHeaders(t, h, http.MethodPost, "/api/tasks/"+childID.String()+"/delegation", delegationPlan(), map[string]string{"Idempotency-Key": "recursive"}, 422, nil)
	finishDelegationChild(t, server, runtime, view.Children[0].RunID)
	server.dispatchDelegations(ctx)
	requestJSON(t, h, http.MethodGet, endpoint, nil, 200, &view)
	if view.Children[1].State != "running" {
		t.Fatalf("second dispatch: %#v", view)
	}
	finishDelegationChild(t, server, runtime, view.Children[1].RunID)
	server.dispatchDelegations(ctx)
	requestJSON(t, h, http.MethodGet, endpoint, nil, 200, &view)
	if view.State != "awaiting_review" {
		t.Fatalf("aggregate: %#v", view)
	}
	for _, c := range view.Children {
		id, _ := domain.ParseTaskID(c.TaskID)
		doc, e := server.service.GetProjectDocument(ctx, id)
		if e != nil {
			t.Fatal(e)
		}
		if doc.Version != 0 || len(doc.Reviews) != 0 {
			t.Fatal("delegation synthesized human document review")
		}
	}
	if n, _ := runtime.snapshot(); n != 2 {
		t.Fatalf("expected two launches, got %d", n)
	}
}
func TestDelegationStopPreventsChildrenAndRestartRequiresResume(t *testing.T) {
	server, runtime, parent, _ := delegationFixture(t)
	h := server.Handler()
	ctx := context.Background()
	endpoint := "/api/tasks/" + parent.ID + "/delegation"
	var view app.DelegationView
	requestJSONWithHeaders(t, h, http.MethodPost, endpoint, delegationPlan(), map[string]string{"Idempotency-Key": "start"}, 201, &view)
	if err := server.recoverDelegations(ctx); err != nil {
		t.Fatal(err)
	}
	server.dispatchDelegations(ctx)
	requestJSON(t, h, http.MethodGet, endpoint, nil, 200, &view)
	if view.State != "blocked" {
		t.Fatalf("recovery: %#v", view)
	}
	if n, _ := runtime.snapshot(); n != 0 {
		t.Fatal("restart launched a child")
	}
	requestJSONWithHeaders(t, h, http.MethodPost, endpoint+"/resume", map[string]any{"expectedVersion": view.Version}, map[string]string{"Idempotency-Key": "resume"}, 200, &view)
	requestJSONWithHeaders(t, h, http.MethodPost, endpoint+"/stop", map[string]any{"expectedVersion": view.Version}, map[string]string{"Idempotency-Key": "stop"}, 200, &view)
	server.dispatchDelegations(ctx)
	requestJSON(t, h, http.MethodGet, endpoint, nil, 200, &view)
	if view.State != "stopped" {
		t.Fatalf("stop: %#v", view)
	}
	if n, _ := runtime.snapshot(); n != 0 {
		t.Fatal("stop launched a child")
	}
}

// Preserve the distinct launch identities of this sequential-process fixture
// during reconciliation as well as Start.
type delegationTestSupervisor struct {
	*taskResourceLifecycleSupervisor
}

func (s delegationTestSupervisor) Reconcile(ctx context.Context, id execution.ProcessIdentity) (execution.ReconcileOutcome, error) {
	out, err := s.fakeSupervisor.Reconcile(ctx, id)
	out.Handle.Value = strings.Replace(id.Value, "process", "handle", 1)
	return out, err
}
func (s delegationTestSupervisor) ReconcileLaunch(ctx context.Context, token execution.LaunchToken) (execution.ReconcileOutcome, error) {
	out, err := s.fakeSupervisor.ReconcileLaunch(ctx, token)
	n, _ := s.snapshot()
	if n > 1 {
		out.Handle.Value = fmt.Sprintf("local-fake-handle-%d", n)
	}
	return out, err
}

func TestDelegationRecoversSubmittedPlanAndRejectsEditedPlan(t *testing.T) {
	for _, edit := range []bool{false, true} {
		t.Run(fmt.Sprintf("edited=%v", edit), func(t *testing.T) {
			server, runtime, parent, _ := delegationFixture(t)
			ctx := context.Background()
			id, _ := domain.ParseTaskID(parent.ID)
			requestJSONWithHeaders(t, server.Handler(), http.MethodPost, "/api/tasks/"+parent.ID+"/delegation", delegationPlan(), map[string]string{"Idempotency-Key": "start"}, 201, nil)
			child, err := server.service.CreateDelegationChild(ctx, id, 0)
			if err != nil {
				t.Fatal(err)
			}
			draft := child.Draft
			if edit {
				content := draft.Content()
				content.TechnicalSteps = []string{"Changed after delegation authorization"}
				changed, e := server.service.SaveTechnicalPlanDraft(ctx, app.SaveTechnicalPlanDraftRequest{CommandMeta: commandMeta("edit"), DraftID: draft.ID(), ExpectedEditVersion: draft.EditVersion(), Content: content})
				if e != nil {
					t.Fatal(e)
				}
				draft = changed.Draft
			}
			_, err = server.service.SubmitTechnicalPlanDraft(ctx, app.SubmitTechnicalPlanDraftRequest{CommandMeta: commandMeta("submit"), DraftID: draft.ID(), ExpectedEditVersion: draft.EditVersion()})
			if err != nil {
				t.Fatal(err)
			}
			server.dispatchDelegations(ctx)
			view, err := server.service.GetDelegation(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			n, _ := runtime.snapshot()
			if edit {
				if n != 0 || view.State != "blocked" {
					t.Fatalf("edited plan executed: %#v starts=%d", view, n)
				}
			} else {
				if n != 1 || view.Children[0].State != "running" {
					t.Fatalf("submitted plan did not recover: %#v starts=%d", view, n)
				}
			}
		})
	}
}

func TestDelegationStopsPreparedChildWithoutRuntime(t *testing.T) {
	server, runtime, parent, _ := delegationFixture(t)
	ctx := context.Background()
	id, _ := domain.ParseTaskID(parent.ID)
	endpoint := "/api/tasks/" + parent.ID + "/delegation"
	var v app.DelegationView
	requestJSONWithHeaders(t, server.Handler(), http.MethodPost, endpoint, delegationPlan(), map[string]string{"Idempotency-Key": "start"}, 201, &v)
	child, err := server.service.CreateDelegationChild(ctx, id, 0)
	if err != nil {
		t.Fatal(err)
	}
	acceptTaskPlan(t, server.Handler(), child.Task.ID().String())
	acceptance, err := server.store.Reader().GetCurrentTechnicalPlanAcceptance(ctx, child.Task.ID())
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.service.CreateRun(ctx, app.CreateRunRequest{CommandMeta: commandMeta("run"), TaskID: child.Task.ID(), RevisionID: acceptance.RevisionID()})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := server.service.PrepareRun(ctx, app.PrepareRunRequest{CommandMeta: commandMeta("prepare"), RunID: created.Run.ID(), ExpectedVersion: created.Run.Version()})
	if err != nil {
		t.Fatal(err)
	}
	requestJSONWithHeaders(t, server.Handler(), http.MethodPost, endpoint+"/stop", map[string]any{"expectedVersion": v.Version}, map[string]string{"Idempotency-Key": "stop"}, 200, nil)
	server.dispatchDelegations(ctx)
	requestJSON(t, server.Handler(), http.MethodGet, endpoint, nil, 200, &v)
	if v.State != "stopped" || v.Children[0].State != "cancelled" {
		t.Fatalf("unstarted stop: %#v", v)
	}
	if _, err = server.service.StartAttempt(ctx, app.StartAttemptRequest{CommandMeta: commandMeta("late-start"), RunID: prepared.Run.ID(), ExpectedVersion: prepared.Run.Version(), Mode: app.StartFresh}); err == nil {
		t.Fatal("cancelled child started")
	}
	if n, _ := runtime.snapshot(); n != 0 {
		t.Fatal("cancel started a runtime")
	}
}

func TestDelegationStopCancelsActiveChildAndNeverLaunchesNext(t *testing.T) {
	server, runtime, parent, _ := delegationFixture(t)
	ctx := context.Background()
	endpoint := "/api/tasks/" + parent.ID + "/delegation"
	var v app.DelegationView
	requestJSONWithHeaders(t, server.Handler(), http.MethodPost, endpoint, delegationPlan(), map[string]string{"Idempotency-Key": "start"}, 201, &v)
	server.dispatchDelegations(ctx)
	requestJSONWithHeaders(t, server.Handler(), http.MethodPost, endpoint+"/stop", map[string]any{"expectedVersion": v.Version}, map[string]string{"Idempotency-Key": "stop"}, 200, nil)
	server.dispatchDelegations(ctx)
	requestJSON(t, server.Handler(), http.MethodGet, endpoint, nil, 200, &v)
	if v.State != "stopped" || v.Children[0].State != "cancelled" || v.Children[1].TaskID != "" {
		t.Fatalf("active stop: %#v", v)
	}
	server.dispatchDelegations(ctx)
	server.dispatchAutomaticRetries(ctx)
	if n, _ := runtime.snapshot(); n != 1 {
		t.Fatalf("stop allowed extra launch: %d", n)
	}
}

func TestDelegationRejectsOversizedOrAmbiguousAssignmentsWithoutMutation(t *testing.T) {
	server, _, parent, _ := delegationFixture(t)
	endpoint := "/api/tasks/" + parent.ID + "/delegation"
	cases := []any{
		[]domain.DelegationAssignment{},
		[]domain.DelegationAssignment{{Role: "same", Title: "One", Requirement: "First"}, {Role: " SAME ", Title: "Two", Requirement: "Second"}},
		[]domain.DelegationAssignment{{Role: "Role", Title: "Title", Requirement: strings.Repeat("x", 4001)}},
	}
	for i, assignments := range cases {
		requestJSONWithHeaders(t, server.Handler(), http.MethodPost, endpoint, map[string]any{"assignments": assignments}, map[string]string{"Idempotency-Key": fmt.Sprintf("invalid-%d", i)}, 422, nil)
	}
	var v app.DelegationView
	requestJSON(t, server.Handler(), http.MethodGet, endpoint, nil, 200, &v)
	if v.State != "not_started" {
		t.Fatalf("invalid request persisted: %#v", v)
	}
}
