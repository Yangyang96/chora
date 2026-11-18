package localweb

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Yangyang96/chora/internal/domain"
)

type pendingResearchBridge struct {
	fixtureResearchBridge
	idle         bool
	observations int
}

func (b *pendingResearchBridge) Execute(ctx context.Context, c domain.WorkflowCommand) (json.RawMessage, error) {
	if c.Kind != "stop" {
		return b.fixtureResearchBridge.Execute(ctx, c)
	}
	return json.Marshal(map[string]any{"operation_id": c.OperationID, "workflow_id": c.WorkflowID, "input_digest": c.InputDigest, "status": "stopping", "admission_closed": true, "terminated": false, "graph_idle": false, "pending_operations": []any{map[string]string{"operation_id": "original-model-call"}}})
}
func (b *pendingResearchBridge) ObserveStop(_ context.Context, c domain.WorkflowCommand) (json.RawMessage, error) {
	b.observations++
	pending := []any{map[string]string{"operation_id": "original-model-call"}}
	status := "stopping"
	if b.idle {
		pending = []any{}
		status = "stopped"
	}
	return json.Marshal(map[string]any{"schema": "chora.research-stop-observation.v1", "operation_id": c.OperationID, "workflow_id": c.WorkflowID, "input_digest": c.InputDigest, "status": status, "admission_closed": true, "terminated": b.idle, "graph_idle": b.idle, "pending_operations": pending})
}
func TestWorkflowResearchPendingStopAndLateObservationPreserveReceipt(t *testing.T) {
	s, runtime, task, _ := workflowFixture(t)
	b := &pendingResearchBridge{}
	s.workflowBridge = b
	w := approvedWorkflow(t, s, submitWorkflowFixture(t, s, task))
	s.dispatchWorkflows(context.Background())
	w, _ = s.store.Reader().GetWorkflow(context.Background(), w.ID)
	requestJSONWithHeaders(t, s.Handler(), http.MethodPost, workflowPath(w, "stop"), map[string]any{}, map[string]string{"Idempotency-Key": "pending-stop"}, 200, &w)
	if w.Status != "unresolved" || !w.StopRequested {
		t.Fatalf("pending stop=%+v", w)
	}
	run, _ := s.store.Reader().GetRun(context.Background(), w.RunID)
	if run.State() == domain.RunStateRunning {
		t.Fatal("pending research blocked independent Pi stop")
	}
	commands, _ := s.store.Reader().ListWorkflowCommands(context.Background(), w.ID)
	var stop domain.WorkflowCommand
	for _, c := range commands {
		if c.Kind == "stop" {
			stop = c
		}
	}
	if len(stop.Receipt) == 0 {
		t.Fatal("missing original immutable receipt")
	}
	if err := s.stopWorkflowResearch(context.Background(), w); err == nil {
		t.Fatal("permanently pending call confirmed after retry")
	}
	// A fresh observer represents a new interpreter after restart. Only its
	// current observation changes; original command/Run/reservations remain.
	b = &pendingResearchBridge{idle: true}
	s.workflowBridge = b
	if err := s.deliverWorkflowStop(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	commands, _ = s.store.Reader().ListWorkflowCommands(context.Background(), w.ID)
	for _, c := range commands {
		if c.Kind == "stop" && string(c.Receipt) != string(stop.Receipt) {
			t.Fatal("late observation overwrote original stop receipt")
		}
	}
	w, _ = s.store.Reader().GetWorkflow(context.Background(), w.ID)
	if w.Status != "stopped" || b.observations != 1 {
		t.Fatalf("late observed=%+v", w)
	}
	if n, _ := runtime.snapshot(); n != 1 {
		t.Fatal("stop reconciliation restarted Pi", n)
	}
}
