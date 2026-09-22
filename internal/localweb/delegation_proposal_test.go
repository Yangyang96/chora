package localweb

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
)

func TestDelegationProposalHTTPRequiresCurrentSourceAndExplicitStart(t *testing.T) {
	server, runtime, parent, _ := delegationFixture(t)
	h := server.Handler()
	endpoint := "/api/tasks/" + parent.ID + "/delegation"
	var proposal app.DelegationProposalView
	requestJSON(t, h, http.MethodGet, endpoint+"/proposal", nil, 200, &proposal)
	if proposal.Available {
		t.Fatal("proposal available before parent execution")
	}
	acceptTaskPlan(t, h, parent.ID)
	var run runView
	requestJSONWithHeaders(t, h, http.MethodPost, "/api/tasks/"+parent.ID+"/runs", map[string]any{}, map[string]string{"Idempotency-Key": "planning-run"}, 201, &run)
	markdown := "# Proposed work\n\nSource: supplied:design.\n\n```chora-delegation-plan\n" + `{"schemaVersion":"chora.delegation-plan.v1","assignments":[{"role":"Designer","title":"Compare options","requirement":"Compare the supplied design options and label unknowns."}]}` + "\n```\n"
	raw, _ := json.Marshal(map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "content": []map[string]any{{"type": "text", "text": markdown}}, "stopReason": "stop"}})
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
	waitForRunState(t, h, run.ID, "awaiting_review")
	requestJSON(t, h, http.MethodGet, endpoint+"/proposal", nil, 200, &proposal)
	if !proposal.Available || proposal.Source == nil || !proposal.Source.Current || len(proposal.Assignments) != 1 {
		t.Fatalf("proposal=%#v", proposal)
	}
	var view app.DelegationView
	requestJSON(t, h, http.MethodGet, endpoint, nil, 200, &view)
	if view.State != "not_started" {
		t.Fatal("preview started delegation")
	}
	requestJSONWithHeaders(t, h, http.MethodPost, endpoint, map[string]any{"sourceAttemptId": "invalid", "expectedResultDigest": proposal.Source.ResultDigest}, map[string]string{"Idempotency-Key": "invalid-source"}, 400, nil)
	requestJSONWithHeaders(t, h, http.MethodPost, endpoint, map[string]any{"sourceAttemptId": proposal.Source.AttemptID, "expectedResultDigest": strings.Repeat("0", 64)}, map[string]string{"Idempotency-Key": "stale-source"}, 409, nil)
	requestJSONWithHeaders(t, h, http.MethodPost, endpoint, map[string]any{"sourceAttemptId": proposal.Source.AttemptID, "expectedResultDigest": proposal.Source.ResultDigest, "assignments": []any{}}, map[string]string{"Idempotency-Key": "mixed-source"}, 422, nil)
	requestJSON(t, h, http.MethodGet, endpoint, nil, 200, &view)
	if view.State != "not_started" {
		t.Fatal("invalid source mutated delegation")
	}
	payload := map[string]any{"sourceAttemptId": proposal.Source.AttemptID, "expectedResultDigest": proposal.Source.ResultDigest}
	requestJSONWithHeaders(t, h, http.MethodPost, endpoint, payload, map[string]string{"Idempotency-Key": "start-source"}, 201, &view)
	requestJSONWithHeaders(t, h, http.MethodPost, endpoint, payload, map[string]string{"Idempotency-Key": "start-source"}, 201, &view)
	if view.Source == nil || view.Source.PlanDigest != proposal.Source.PlanDigest || len(view.Assignments) != 1 {
		t.Fatalf("frozen source=%#v", view)
	}
	server.dispatchDelegations(context.Background())
	requestJSON(t, h, http.MethodGet, endpoint, nil, 200, &view)
	if len(view.Children) != 1 || view.Children[0].State != "running" {
		t.Fatalf("child=%#v", view)
	}
	finishDelegationChild(t, server, runtime, view.Children[0].RunID)
	server.dispatchDelegations(context.Background())
	requestJSON(t, h, http.MethodGet, endpoint, nil, 200, &view)
	if view.State != "awaiting_review" || view.Source == nil || !view.Source.Current {
		t.Fatalf("completed=%#v", view)
	}
	if n, _ := runtime.snapshot(); n != 2 {
		t.Fatalf("want parent plus one child, got %d", n)
	}
	parentID, _ := domain.ParseTaskID(parent.ID)
	document, err := server.service.GetProjectDocument(context.Background(), parentID)
	if err != nil || document.Version != 0 || len(document.Reviews) != 0 {
		t.Fatalf("import accepted a document: %#v %v", document, err)
	}
}
