package localweb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func synthesisFixture(t *testing.T, mode string) (*Server, *taskResourceLifecycleSupervisor, taskRefView) {
	t.Helper()
	var server *Server
	var runtime *taskResourceLifecycleSupervisor
	var parent taskRefView
	if mode == "manual" {
		server, runtime, parent, _ = delegationFixture(t)
	} else {
		server, runtime, parent = planningFixture(t)
	}
	endpoint := "/api/tasks/" + parent.ID + "/delegation"
	h := server.Handler()
	switch mode {
	case "manual":
		payload := delegationPlan()
		payload["synthesize"] = true
		requestJSONWithHeaders(t, h, http.MethodPost, endpoint, payload, map[string]string{"Idempotency-Key": "synthesis-start"}, 201, nil)
	case "planning":
		var v app.DelegationView
		requestJSONWithHeaders(t, h, http.MethodPost, endpoint+"/planning", map[string]any{"synthesize": true}, map[string]string{"Idempotency-Key": "synthesis-start"}, 201, &v)
		server.dispatchDelegations(context.Background())
		finishPlanning(t, server, runtime, v.Planning.RunID, planningResult)
		server.dispatchDelegations(context.Background())
	case "source":
		var run runView
		requestJSONWithHeaders(t, h, http.MethodPost, "/api/tasks/"+parent.ID+"/runs", map[string]any{}, map[string]string{"Idempotency-Key": "source-run"}, 201, &run)
		finishPlanning(t, server, runtime, run.ID, planningResult)
		var proposal app.DelegationProposalView
		requestJSON(t, h, http.MethodGet, endpoint+"/proposal", nil, 200, &proposal)
		requestJSONWithHeaders(t, h, http.MethodPost, endpoint, map[string]any{"sourceAttemptId": proposal.Source.AttemptID, "expectedResultDigest": proposal.Source.ResultDigest, "synthesize": true}, map[string]string{"Idempotency-Key": "synthesis-start"}, 201, nil)
	}
	return server, runtime, parent
}
func finishSynthesisChildren(t *testing.T, server *Server, runtime *taskResourceLifecycleSupervisor, parent taskRefView) app.DelegationView {
	t.Helper()
	ctx := context.Background()
	view := planningView(t, server, parent.ID)
	for i := range view.Assignments {
		server.dispatchDelegations(ctx)
		view = planningView(t, server, parent.ID)
		if view.Children[i].State != "running" {
			t.Fatalf("child %d not running: %#v", i, view)
		}
		finishDelegationChild(t, server, runtime, view.Children[i].RunID)
	}
	return planningView(t, server, parent.ID)
}
func TestSynthesisDelegationAllStartModesAndFrozenEvidence(t *testing.T) {
	for _, mode := range []string{"manual", "source", "planning"} {
		t.Run(mode, func(t *testing.T) {
			server, runtime, parent := synthesisFixture(t, mode)
			ctx := context.Background()
			h := server.Handler()
			view := finishSynthesisChildren(t, server, runtime, parent)
			if view.Synthesis == nil || view.Synthesis.State != "waiting" || view.State != "running" {
				t.Fatalf("missing authorization: %#v", view)
			}
			server.dispatchDelegations(ctx)
			view = planningView(t, server, parent.ID)
			if view.Synthesis == nil || view.Synthesis.State != "running" || view.State != "running" {
				t.Fatalf("synthesis launch=%#v", view)
			}
			parentID, _ := domain.ParseTaskID(parent.ID)
			frozen, err := server.store.Reader().GetDelegationSynthesis(ctx, parentID)
			if err != nil {
				t.Fatal(err)
			}
			selection, err := server.store.Reader().GetTaskRevisionSelection(ctx, frozen.TaskID)
			if err != nil || !selection.SynthesisOnly() || len(selection.Selected()) != 0 || len(selection.Excluded()) != 0 {
				t.Fatalf("synthesis context expanded: %#v %v", selection, err)
			}
			synthesisRun, err := server.store.Reader().GetRun(ctx, frozen.RunID)
			if err != nil {
				t.Fatal(err)
			}
			charter, err := server.store.Reader().GetCharter(ctx, synthesisRun.CharterID())
			if err != nil || !charter.SynthesisOnly() || len(charter.ContextRevisionIDs()) != 0 {
				t.Fatalf("synthesis charter context expanded: %#v %v", charter, err)
			}
			attempt, err := server.store.Reader().GetCurrentAttempt(ctx, frozen.RunID)
			if err != nil {
				t.Fatal(err)
			}
			contextSnapshot, err := server.store.Reader().GetSnapshot(ctx, attempt.ContextSnapshotID())
			if err != nil || len(contextSnapshot.IncludedRevisionIDs()) != 0 {
				t.Fatalf("synthesis snapshot context expanded: %v", err)
			}
			illegal, err := domain.NewSynthesisRevisionSelection(parentID, selection.RoomID(), time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			if err = server.store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.InsertTaskRevisionSelection(ctx, illegal) }); err == nil {
				t.Fatal("ordinary parent received empty synthesis context")
			}
			resource, err := server.store.Reader().GetTaskResourceSnapshot(ctx, frozen.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			var snapshot domain.TaskResourceSnapshot
			if err = json.Unmarshal(resource.CanonicalJSON, &snapshot); err != nil {
				t.Fatal(err)
			}
			if len(snapshot.Resources) != 0 || len(snapshot.Materials) != len(frozen.Inputs) {
				t.Fatal("synthesis expanded material authority")
			}
			for i, input := range frozen.Inputs {
				if snapshot.Materials[i].Body != input.Markdown || snapshot.Materials[i].Locator != "chora-result:"+input.ResultID.String() {
					t.Fatal("synthesis material differs from frozen source")
				}
			}
			// Every frozen input is an original result, not a ProjectDocument import.
			if len(frozen.Inputs) != len(view.Assignments) || !frozen.AttemptID.Valid() {
				t.Fatalf("incomplete provenance: %#v", frozen)
			}
			for i, input := range frozen.Inputs {
				if input.TaskID.String() != view.Children[i].TaskID || input.ResultID.String() != view.Children[i].ResultID || input.Markdown != view.Children[i].Markdown {
					t.Fatal("input substituted child evidence")
				}
			}
			var childView app.DelegationView
			requestJSON(t, h, http.MethodGet, "/api/tasks/"+frozen.TaskID.String()+"/delegation", nil, 200, &childView)
			if childView.State != "child" || childView.ParentTaskID != parent.ID {
				t.Fatal("synthesis lost parent navigation")
			}
			requestJSONWithHeaders(t, h, http.MethodPost, "/api/tasks/"+frozen.TaskID.String()+"/delegation", delegationPlan(), map[string]string{"Idempotency-Key": "recursive-synthesis"}, 422, nil)
			requestJSONWithHeaders(t, h, http.MethodPost, "/api/tasks/"+frozen.TaskID.String()+"/runs", map[string]any{}, map[string]string{"Idempotency-Key": "second-synthesis-run"}, 409, nil)
			finishPlanning(t, server, runtime, view.Synthesis.RunID, "# Final synthesis\n\nPreserve compatibility; deployment remains unknown. Source: chora-result:"+frozen.Inputs[0].ResultID.String()+".")
			server.dispatchDelegations(ctx)
			view = planningView(t, server, parent.ID)
			if view.State != "awaiting_review" || view.Synthesis.ResultID == "" || view.Synthesis.Markdown == "" || !view.Synthesis.Current {
				t.Fatalf("final=%#v", view)
			}
			count, _ := runtime.snapshot()
			want := len(view.Assignments) + 1
			if mode != "manual" {
				want++
			}
			if count != want {
				t.Fatalf("launch budget %d want %d", count, want)
			}
			run, _ := server.store.Reader().GetRun(ctx, frozen.RunID)
			_, err = server.service.PrepareRetry(ctx, app.PrepareRetryRequest{CommandMeta: commandMeta("synthesis-retry"), RunID: run.ID(), ExpectedVersion: run.Version(), Reason: "repair", Instructions: "again"})
			if err == nil {
				t.Fatal("synthesis retried")
			}
			for _, id := range []domain.TaskID{parentID, frozen.TaskID} {
				doc, e := server.service.GetProjectDocument(ctx, id)
				if e != nil || doc.Version != 0 || len(doc.Reviews) != 0 {
					t.Fatalf("synthesis accepted a document: %#v %v", doc, e)
				}
			}
			// A later complete event supersedes one raw source; frozen synthesis inputs
			// and output must remain unchanged, with only current=false projected.
			if err = server.store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
				_, e := tx.AppendRunEvent(ctx, frozen.Inputs[0].RunID, storecontract.EventDraft{ID: domain.NewEventID(), Type: "assistant_message", Source: "adapter", OccurredAt: time.Now().UTC(), RecordedAt: time.Now().UTC(), NormalizedJSON: []byte(`{"text":"later finding","terminal_complete":true}`)})
				return e
			}); err != nil {
				t.Fatal(err)
			}
			after := planningView(t, server, parent.ID)
			stored, e := server.store.Reader().GetDelegationSynthesis(ctx, parentID)
			if e != nil || !reflect.DeepEqual(stored, frozen) || after.Synthesis.Current || after.Synthesis.ResultID != view.Synthesis.ResultID {
				t.Fatalf("source drift rebound synthesis: %#v %v", after, e)
			}
		})
	}
}
func TestSynthesisStopBeforeCreationAndDuringExecution(t *testing.T) {
	for _, started := range []bool{false, true} {
		t.Run(fmt.Sprint(started), func(t *testing.T) {
			server, runtime, parent := synthesisFixture(t, "manual")
			ctx := context.Background()
			view := finishSynthesisChildren(t, server, runtime, parent)
			if started {
				server.dispatchDelegations(ctx)
				view = planningView(t, server, parent.ID)
			}
			requestJSONWithHeaders(t, server.Handler(), http.MethodPost, "/api/tasks/"+parent.ID+"/delegation/stop", map[string]any{"expectedVersion": view.Version}, map[string]string{"Idempotency-Key": "stop-synthesis"}, 200, nil)
			server.dispatchDelegations(ctx)
			view = planningView(t, server, parent.ID)
			if view.State != "stopped" || view.Synthesis.State != "stopped" {
				t.Fatalf("stop=%#v", view)
			}
			id, _ := domain.ParseTaskID(parent.ID)
			if !started {
				if _, err := server.service.CreateDelegationSynthesisTask(ctx, id); !errors.Is(err, storecontract.ErrVersionConflict) {
					t.Fatalf("late synthesis creation: %v", err)
				}
			}
			server.dispatchAutomaticRetries(ctx)
			want := 2
			if started {
				want++
			}
			if n, _ := runtime.snapshot(); n != want {
				t.Fatalf("stop exceeded launches: %d", n)
			}
		})
	}
}
func TestSynthesisRestartKeepsSamePreparedAttempt(t *testing.T) {
	server, runtime, parent := synthesisFixture(t, "manual")
	ctx := context.Background()
	finishSynthesisChildren(t, server, runtime, parent)
	id, _ := domain.ParseTaskID(parent.ID)
	created, err := server.service.CreateDelegationSynthesisTask(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	acceptTaskPlan(t, server.Handler(), created.Task.ID().String())
	acceptance, err := server.store.Reader().GetCurrentTechnicalPlanAcceptance(ctx, created.Task.ID())
	if err != nil {
		t.Fatal(err)
	}
	run, err := server.service.CreateRun(ctx, app.CreateRunRequest{CommandMeta: commandMeta("summary-run"), TaskID: created.Task.ID(), RevisionID: acceptance.RevisionID()})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := server.service.PrepareRun(ctx, app.PrepareRunRequest{CommandMeta: commandMeta("summary-prepare"), RunID: run.Run.ID(), ExpectedVersion: run.Run.Version()})
	if err != nil {
		t.Fatal(err)
	}
	if err = server.recoverDelegations(ctx); err != nil {
		t.Fatal(err)
	}
	server.dispatchDelegations(ctx)
	view := planningView(t, server, parent.ID)
	if view.State != "blocked" {
		t.Fatalf("restart=%#v", view)
	}
	if n, _ := runtime.snapshot(); n != 2 {
		t.Fatal("restart launched synthesis")
	}
	requestJSONWithHeaders(t, server.Handler(), http.MethodPost, "/api/tasks/"+parent.ID+"/delegation/resume", map[string]any{"expectedVersion": view.Version}, map[string]string{"Idempotency-Key": "resume-synthesis"}, 200, nil)
	server.dispatchDelegations(ctx)
	item, err := server.store.Reader().GetDelegationSynthesis(ctx, id)
	if err != nil || item.AttemptID != prepared.Attempt.ID() || item.RunID != run.Run.ID() {
		t.Fatalf("restart duplicated identity: %#v %v", item, err)
	}
}

func TestSynthesisStopRacesCreationWithoutLateLaunch(t *testing.T) {
	for i := 0; i < 3; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			server, runtime, parent := synthesisFixture(t, "manual")
			ctx := context.Background()
			view := finishSynthesisChildren(t, server, runtime, parent)
			id, _ := domain.ParseTaskID(parent.ID)
			start := make(chan struct{})
			created := make(chan error, 1)
			stopped := make(chan error, 1)
			go func() { <-start; _, err := server.service.CreateDelegationSynthesisTask(ctx, id); created <- err }()
			go func() {
				<-start
				_, err := server.service.ChangeDelegation(ctx, app.ChangeDelegationRequest{CommandMeta: commandMeta("race-stop"), ParentTaskID: id, ExpectedVersion: view.Version, Action: "stop"})
				stopped <- err
			}()
			close(start)
			createErr, stopErr := <-created, <-stopped
			if stopErr != nil {
				t.Fatal(stopErr)
			}
			if createErr != nil && !errors.Is(createErr, storecontract.ErrVersionConflict) {
				t.Fatal(createErr)
			}
			server.dispatchDelegations(ctx)
			final := planningView(t, server, parent.ID)
			item, err := server.store.Reader().GetDelegationSynthesis(ctx, id)
			if err != nil || final.State != "stopped" || item.RunID.Valid() || item.AttemptID.Valid() {
				t.Fatalf("late synthesis: %#v %#v %v", final, item, err)
			}
			if count, _ := runtime.snapshot(); count != 2 {
				t.Fatalf("stop launched %d runtimes", count)
			}
		})
	}
}

func TestSynthesisCannotAddAuthorizationAfterStart(t *testing.T) {
	server, _, parent := synthesisFixture(t, "manual")
	payload := delegationPlan()
	payload["synthesize"] = false
	requestJSONWithHeaders(t, server.Handler(), http.MethodPost, "/api/tasks/"+parent.ID+"/delegation", payload, map[string]string{"Idempotency-Key": "synthesis-start"}, 409, nil)
	// A new command cannot mutate the already frozen authorization either.
	requestJSONWithHeaders(t, server.Handler(), http.MethodPost, "/api/tasks/"+parent.ID+"/delegation", payload, map[string]string{"Idempotency-Key": "replace-authorization"}, 409, nil)
}
