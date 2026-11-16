package localweb

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
	"github.com/Yangyang96/chora/internal/store/sqlite"
)

type fixtureResearchBridge struct {
	fail  bool
	calls int
}

func (b *fixtureResearchBridge) Describe(context.Context) (json.RawMessage, error) {
	return json.RawMessage(`{"fixture":true}`), nil
}
func (b *fixtureResearchBridge) Execute(_ context.Context, c domain.WorkflowCommand) (json.RawMessage, error) {
	b.calls++
	if b.fail {
		return nil, errors.New("injected lost response")
	}
	v := map[string]any{"operation_id": c.OperationID, "workflow_id": c.WorkflowID, "input_digest": c.InputDigest, "budget": map[string]any{"spent_tokens": 21, "usage_unknown": false, "deadline": 1700000000}}
	switch c.Kind {
	case "submit":
		v["status"] = "waiting"
		v["gate"] = map[string]any{"gate_id": c.WorkflowID + ":gate", "input_digest": strings.Repeat("a", 64), "report": map[string]any{"summary": "Use the existing bounded workbench (synthetic evidence).", "citations": []string{"readme"}, "no_answer": false}}
	case "resume":
		var p struct {
			Decision struct {
				Decision string `json:"decision"`
			} `json:"decision"`
		}
		_ = json.Unmarshal([]byte(c.Payload), &p)
		v["status"] = "accepted"
		if p.Decision.Decision == "reject" {
			v["status"] = "rejected"
		}
	case "stop":
		v["status"] = "stopped"
	case "bind_run":
		var payload map[string]string
		_ = json.Unmarshal([]byte(c.Payload), &payload)
		v["status"] = "coding"
		v["run_id"] = payload["run_id"]
	}
	return json.Marshal(v)
}
func workflowFixture(t *testing.T) (*Server, *taskResourceLifecycleSupervisor, taskRefView, string) {
	t.Helper()
	server, runtime, task, root := delegationFixture(t)
	id, _ := domain.ParseTaskID(task.ID)
	ctx := context.Background()
	draft, err := server.store.Reader().GetOpenTechnicalPlanDraft(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	submitted, err := server.service.SubmitTechnicalPlanDraft(ctx, app.SubmitTechnicalPlanDraftRequest{CommandMeta: commandMeta("submit"), DraftID: draft.ID(), ExpectedEditVersion: draft.EditVersion()})
	if err != nil {
		t.Fatal(err)
	}
	meta := commandMeta("activate")
	meta.ActorID = systemActor
	if _, err = server.service.ReviewTechnicalPlanRevision(ctx, app.ReviewTechnicalPlanRevisionRequest{CommandMeta: meta, RevisionID: submitted.Revision.ID(), Kind: domain.TechnicalPlanReviewAccept, Note: "Fixture initial plan activation; not business acceptance"}); err != nil {
		t.Fatal(err)
	}
	server.workflowBridge = &fixtureResearchBridge{}
	return server, runtime, task, root
}
func submitWorkflowFixture(t *testing.T, s *Server, task taskRefView) domain.WorkflowExecution {
	t.Helper()
	var w domain.WorkflowExecution
	requestJSONWithHeaders(t, s.Handler(), http.MethodPost, "/api/tasks/"+task.ID+"/workflow", map[string]string{"projectId": task.ResourceSnapshot.ProjectID, "question": task.Goal}, map[string]string{"Idempotency-Key": "workflow-submit"}, 201, &w)
	return w
}
func workflowPath(w domain.WorkflowExecution, action string) string {
	return "/api/projects/" + w.ProjectID.String() + "/workflows/" + w.ID + "/" + action
}
func workflowDecision(w domain.WorkflowExecution) map[string]any {
	return map[string]any{"version": w.Gate.Version, "gateId": w.Gate.ID, "inputDigest": w.Gate.InputDigest, "decision": "accept", "instructions": "Keep changes inside the frozen task boundary."}
}
func waitingWorkflow(t *testing.T, s *Server, w domain.WorkflowExecution) domain.WorkflowExecution {
	t.Helper()
	s.dispatchWorkflows(context.Background())
	v, e := s.store.Reader().GetWorkflow(context.Background(), w.ID)
	if e != nil || v.Status != "waiting" {
		t.Fatalf("waiting: %+v %v", v, e)
	}
	return v
}
func approvedWorkflow(t *testing.T, s *Server, w domain.WorkflowExecution) domain.WorkflowExecution {
	t.Helper()
	w = waitingWorkflow(t, s, w)
	requestJSONWithHeaders(t, s.Handler(), http.MethodPost, workflowPath(w, "decision"), workflowDecision(w), map[string]string{"Idempotency-Key": "confirm"}, 200, nil)
	commands, e := s.store.Reader().ListWorkflowCommands(context.Background(), w.ID)
	if e != nil {
		t.Fatal(e)
	}
	for _, c := range commands {
		if c.Kind == "resume" {
			b, e := s.workflowBridge.Execute(context.Background(), c)
			if e != nil {
				t.Fatal(e)
			}
			if e = s.service.ApplyWorkflowReceipt(context.Background(), c, b); e != nil {
				t.Fatal(e)
			}
		}
	}
	w, e = s.store.Reader().GetWorkflow(context.Background(), w.ID)
	if e != nil || w.Status != "approved" {
		t.Fatalf("approved: %+v %v", w, e)
	}
	return w
}

func TestWorkflowHTTPJourneyAndIdempotentRun(t *testing.T) {
	s, runtime, task, _ := workflowFixture(t)
	ctx := context.Background()
	w := submitWorkflowFixture(t, s, task)
	if w.ID == "" || w.InputDigest == "" || w.TaskID.String() != task.ID {
		t.Fatalf("wire identity %+v", w)
	}
	w = waitingWorkflow(t, s, w)
	decision := workflowDecision(w)
	requestJSONWithHeaders(t, s.Handler(), http.MethodPost, workflowPath(w, "decision"), decision, map[string]string{"Idempotency-Key": "confirm"}, 200, nil)
	requestJSONWithHeaders(t, s.Handler(), http.MethodPost, workflowPath(w, "decision"), decision, map[string]string{"Idempotency-Key": "confirm-again"}, 200, nil)
	s.dispatchWorkflows(ctx)
	w, e := s.store.Reader().GetWorkflow(ctx, w.ID)
	if e != nil || !w.RunID.Valid() || w.Status != "coding" {
		t.Fatalf("coding %+v %v", w, e)
	}
	s.dispatchWorkflows(ctx)
	if n, _ := runtime.snapshot(); n != 1 {
		t.Fatalf("starts=%d", n)
	}
	run, e := s.service.CreateWorkflowRun(ctx, w.ID)
	if e != nil || run.ID() != w.RunID {
		t.Fatalf("reconcile Run %s %v", run.ID(), e)
	}
	finishDelegationChild(t, s, runtime, run.ID().String())
	s.dispatchWorkflows(ctx)
	w, e = s.store.Reader().GetWorkflow(ctx, w.ID)
	if e != nil || w.Status != "awaiting_review" {
		t.Fatalf("review %+v %v", w, e)
	}
	if run, e = s.store.Reader().GetRun(ctx, w.RunID); e != nil || run.State() == domain.RunStateAccepted {
		t.Fatalf("synthetic gate accepted result %s %v", run.State(), e)
	}
	events, e := s.store.Reader().ListWorkflowEvents(ctx, w.ID, 0)
	if e != nil || len(events) < 5 {
		t.Fatalf("events %v %v", events, e)
	}
	for i, event := range events {
		if event.Sequence != uint64(i+1) {
			t.Fatalf("nonmonotonic %+v", events)
		}
	}
	tail, e := s.store.Reader().ListWorkflowEvents(ctx, w.ID, events[len(events)-2].Sequence)
	if e != nil || len(tail) != 1 || tail[0].Kind != "awaiting_review" {
		t.Fatalf("tail %v %v", tail, e)
	}
}

func TestWorkflowRejectsStaleConflictingAndForeignDecisions(t *testing.T) {
	s, _, task, _ := workflowFixture(t)
	w := waitingWorkflow(t, s, submitWorkflowFixture(t, s, task))
	for _, mutation := range []string{"version", "digest", "gate", "project"} {
		t.Run(mutation, func(t *testing.T) {
			v := workflowDecision(w)
			path := workflowPath(w, "decision")
			if mutation == "version" {
				v["version"] = w.Version - 1
			}
			if mutation == "digest" {
				v["inputDigest"] = strings.Repeat("b", 64)
			}
			if mutation == "gate" {
				v["gateId"] = "other"
			}
			status := 409
			if mutation == "project" {
				path = strings.Replace(path, w.ProjectID.String(), domain.NewProjectID().String(), 1)
				status = 403
			}
			requestJSONWithHeaders(t, s.Handler(), http.MethodPost, path, v, map[string]string{"Idempotency-Key": mutation}, status, nil)
		})
	}
	meta := commandMeta("foreign")
	meta.ActorID = "another-human"
	_, err := s.service.DecideWorkflow(context.Background(), app.DecideWorkflowRequest{CommandMeta: meta, ID: w.ID, ProjectID: w.ProjectID, ExpectedVersion: w.Version, GateID: w.Gate.ID, InputDigest: w.Gate.InputDigest, Decision: "accept"})
	if !errors.Is(err, app.ErrUnauthorizedCommand) {
		t.Fatalf("foreign actor: %v", err)
	}
	requestJSONWithHeaders(t, s.Handler(), http.MethodPost, workflowPath(w, "decision"), workflowDecision(w), map[string]string{"Idempotency-Key": "confirm"}, 200, nil)
	v := workflowDecision(w)
	v["decision"] = "reject"
	requestJSONWithHeaders(t, s.Handler(), http.MethodPost, workflowPath(w, "decision"), v, map[string]string{"Idempotency-Key": "conflict"}, 409, nil)
	requestJSONWithHeaders(t, s.Handler(), http.MethodPost, "/api/tasks/"+task.ID+"/workflow", map[string]string{"projectId": w.ProjectID.String(), "question": "changed"}, map[string]string{"Idempotency-Key": "workflow-submit"}, 409, nil)
}

func TestWorkflowStopFencesRunCreationAndLateReceipt(t *testing.T) {
	for _, create := range []bool{false, true} {
		t.Run(fmt.Sprint(create), func(t *testing.T) {
			s, runtime, task, _ := workflowFixture(t)
			ctx := context.Background()
			w := approvedWorkflow(t, s, submitWorkflowFixture(t, s, task))
			if create {
				if _, e := s.service.CreateWorkflowRun(ctx, w.ID); e != nil {
					t.Fatal(e)
				}
			}
			requestJSONWithHeaders(t, s.Handler(), http.MethodPost, workflowPath(w, "stop"), map[string]any{}, map[string]string{"Idempotency-Key": "stop"}, 200, nil)
			if !create {
				if _, e := s.service.CreateWorkflowRun(ctx, w.ID); e == nil {
					t.Fatal("created Run after Stop")
				}
			}
			s.dispatchWorkflows(ctx)
			w, e := s.store.Reader().GetWorkflow(ctx, w.ID)
			if e != nil || w.Status != "stopped" {
				t.Fatalf("stop %+v %v", w, e)
			}
			if n, _ := runtime.snapshot(); n != 0 {
				t.Fatalf("launched %d after Stop", n)
			}
			commands, _ := s.store.Reader().ListWorkflowCommands(ctx, w.ID)
			for _, c := range commands {
				if c.Kind == "resume" {
					if e = s.service.ApplyWorkflowReceipt(ctx, c, c.Receipt); e != nil {
						t.Fatal(e)
					}
				}
			}
			after, _ := s.store.Reader().GetWorkflow(ctx, w.ID)
			if after.Status != "stopped" {
				t.Fatal("late receipt undid Stop")
			}
		})
	}
}

func TestWorkflowRestartAfterCommittedRunLostResponse(t *testing.T) {
	s, _, task, root := workflowFixture(t)
	ctx := context.Background()
	w := approvedWorkflow(t, s, submitWorkflowFixture(t, s, task))
	run, e := s.service.CreateWorkflowRun(ctx, w.ID)
	if e != nil {
		t.Fatal(e)
	}
	// Deliberately discard its response, close SQLite, and construct a fresh app.
	if e = s.store.Close(); e != nil {
		t.Fatal(e)
	}
	paths, e := filepath.Glob(filepath.Join(filepath.Dir(root), "*", "chora.db"))
	if e != nil || len(paths) != 1 {
		t.Fatalf("fixture database: %v %v", paths, e)
	}
	path := paths[0]
	fresh, e := sqlite.Open(ctx, path)
	if e != nil {
		t.Fatal(e)
	}
	defer fresh.Close()
	service := app.NewService(app.Dependencies{Store: fresh, Authorizer: localAuthorizer{}})
	replayed, e := service.CreateWorkflowRun(ctx, w.ID)
	if e != nil || replayed.ID() != run.ID() {
		t.Fatalf("replayed %s expected %s: %v", replayed.ID(), run.ID(), e)
	}
	got, e := fresh.Reader().GetWorkflow(ctx, w.ID)
	if e != nil || got.Gate.ID != w.Gate.ID || got.InputDigest != w.InputDigest {
		t.Fatalf("identity drift %+v %v", got, e)
	}
	id, _ := domain.ParseTaskID(task.ID)
	room, _ := domain.ParseRoomID(task.RoomID)
	history, e := fresh.Reader().ListTaskRunHistory(ctx, room, id)
	if e != nil || len(history) != 1 {
		t.Fatalf("runs %d %v", len(history), e)
	}
}

func TestWorkflowUnknownBridgeRequiresExplicitReconciliation(t *testing.T) {
	s, runtime, task, _ := workflowFixture(t)
	w := submitWorkflowFixture(t, s, task)
	b := s.workflowBridge.(*fixtureResearchBridge)
	b.fail = true
	s.dispatchWorkflows(context.Background())
	w, _ = s.store.Reader().GetWorkflow(context.Background(), w.ID)
	if w.Status != "unresolved" {
		t.Fatalf("unknown: %+v", w)
	}
	calls := b.calls
	s.dispatchWorkflows(context.Background())
	if b.calls != calls {
		t.Fatal("automatically retried unknown operation")
	}
	b.fail = false
	requestJSONWithHeaders(t, s.Handler(), http.MethodPost, workflowPath(w, "reconcile"), map[string]any{}, map[string]string{"Idempotency-Key": "reconcile"}, 200, nil)
	w = waitingWorkflow(t, s, w)
	if w.Gate == nil {
		t.Fatal("missing gate")
	}
	if n, _ := runtime.snapshot(); n != 0 {
		t.Fatal("research launched Pi")
	}
}

func TestWorkflowReceiptConflictRollsBackProjection(t *testing.T) {
	s, _, task, _ := workflowFixture(t)
	w := waitingWorkflow(t, s, submitWorkflowFixture(t, s, task))
	ctx := context.Background()
	commands, _ := s.store.Reader().ListWorkflowCommands(ctx, w.ID)
	c := commands[0]
	var receipt map[string]any
	_ = json.Unmarshal(c.Receipt, &receipt)
	receipt["status"] = "accepted"
	changed, _ := json.Marshal(receipt)
	if e := s.service.ApplyWorkflowReceipt(ctx, c, changed); !errors.Is(e, storecontract.ErrIdempotencyConflict) {
		t.Fatalf("conflicting receipt: %v", e)
	}
	after, _ := s.store.Reader().GetWorkflow(ctx, w.ID)
	if after.Version != w.Version || after.Status != "waiting" {
		t.Fatalf("projection changed %+v", after)
	}
}

func TestWorkflowConcurrentRunRequestsUseSameOperation(t *testing.T) {
	s, _, task, _ := workflowFixture(t)
	ctx := context.Background()
	w := approvedWorkflow(t, s, submitWorkflowFixture(t, s, task))
	var wg sync.WaitGroup
	results := make(chan domain.RunID, 6)
	errs := make(chan error, 6)
	for range 6 {
		wg.Go(func() {
			run, e := s.service.CreateWorkflowRun(ctx, w.ID)
			if e != nil {
				errs <- e
			} else {
				results <- run.ID()
			}
		})
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	var expected domain.RunID
	for id := range results {
		if !expected.Valid() {
			expected = id
		}
		if id != expected {
			t.Fatal("duplicate Run")
		}
	}
	if !expected.Valid() {
		t.Fatal("no Run")
	}
}

func TestWorkflowStopFencesPreparedAttemptAndLateNewReceipt(t *testing.T) {
	s, runtime, task, _ := workflowFixture(t)
	ctx := context.Background()
	w := approvedWorkflow(t, s, submitWorkflowFixture(t, s, task))
	run, e := s.service.CreateWorkflowRun(ctx, w.ID)
	if e != nil {
		t.Fatal(e)
	}
	prepared, e := s.service.PrepareRun(ctx, app.PrepareRunRequest{CommandMeta: commandMeta("prepare"), RunID: run.ID(), ExpectedVersion: run.Version()})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.service.StopWorkflow(ctx, commandMeta("stop"), w.ID, w.ProjectID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.service.StartAttempt(ctx, app.StartAttemptRequest{CommandMeta: commandMeta("late-start"), RunID: run.ID(), ExpectedVersion: prepared.Run.Version(), Mode: app.StartFresh}); !errors.Is(e, app.ErrUnauthorizedCommand) {
		t.Fatalf("late launch: %v", e)
	}
	s.dispatchWorkflows(ctx)
	w, _ = s.store.Reader().GetWorkflow(ctx, w.ID)
	if w.Status != "stopped" {
		t.Fatalf("stop %+v", w)
	}
	if n, _ := runtime.snapshot(); n != 0 {
		t.Fatalf("starts=%d", n)
	}
	// A distinct Task receives its first research response only after Stop.
	s2, _, task2, _ := workflowFixture(t)
	late := submitWorkflowFixture(t, s2, task2)
	commands, _ := s2.store.Reader().ListWorkflowCommands(ctx, late.ID)
	c := commands[0]
	b, e := s2.workflowBridge.Execute(ctx, c)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s2.service.StopWorkflow(ctx, commandMeta("stop"), late.ID, late.ProjectID); e != nil {
		t.Fatal(e)
	}
	if e = s2.service.ApplyWorkflowReceipt(ctx, c, b); e != nil {
		t.Fatal(e)
	}
	late, _ = s2.store.Reader().GetWorkflow(ctx, late.ID)
	if !late.StopRequested || late.Status != "stopping" || late.Gate != nil {
		t.Fatalf("late response replaced stop: %+v", late)
	}
}

func TestWorkflowAuthorityAndGateCannotBeRewritten(t *testing.T) {
	s, _, task, _ := workflowFixture(t)
	ctx := context.Background()
	w := waitingWorkflow(t, s, submitWorkflowFixture(t, s, task))
	for _, field := range []string{"actor", "question", "digest", "gate", "stop"} {
		t.Run(field, func(t *testing.T) {
			v, e := s.store.Reader().GetWorkflow(ctx, w.ID)
			if e != nil {
				t.Fatal(e)
			}
			old := v.Version
			v.Version++
			switch field {
			case "actor":
				v.ActorID = "other"
			case "question":
				v.Question = "changed"
			case "digest":
				v.InputDigest = strings.Repeat("0", 64)
			case "gate":
				v.Gate.ID = "changed"
			case "stop":
				if _, e = s.service.StopWorkflow(ctx, commandMeta("stop"), w.ID, w.ProjectID); e != nil {
					t.Fatal(e)
				}
				v, e = s.store.Reader().GetWorkflow(ctx, w.ID)
				if e != nil {
					t.Fatal(e)
				}
				old = v.Version
				v.Version++
				v.StopRequested = false
			}
			if e = s.store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.SaveWorkflowCAS(ctx, old, v) }); e == nil {
				t.Fatalf("rewrote %s", field)
			}
		})
	}
}

func TestWorkflowNoAnswerCannotAuthorizeCoding(t *testing.T) {
	s, _, task, _ := workflowFixture(t)
	ctx := context.Background()
	w := submitWorkflowFixture(t, s, task)
	commands, _ := s.store.Reader().ListWorkflowCommands(ctx, w.ID)
	c := commands[0]
	b, e := s.workflowBridge.Execute(ctx, c)
	if e != nil {
		t.Fatal(e)
	}
	b = json.RawMessage(strings.Replace(string(b), `"no_answer":false`, `"no_answer":true`, 1))
	if e = s.service.ApplyWorkflowReceipt(ctx, c, b); e != nil {
		t.Fatal(e)
	}
	w, _ = s.store.Reader().GetWorkflow(ctx, w.ID)
	requestJSONWithHeaders(t, s.Handler(), http.MethodPost, workflowPath(w, "decision"), workflowDecision(w), map[string]string{"Idempotency-Key": "no-answer"}, 422, nil)
}

func TestWorkflowSSEReplayUsesLastEventID(t *testing.T) {
	s, _, task, _ := workflowFixture(t)
	w := waitingWorkflow(t, s, submitWorkflowFixture(t, s, task))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1"+workflowPath(w, "events"), nil).WithContext(ctx)
	r.Header.Set("Last-Event-ID", "1")
	writer := &cancelStreamWriter{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
	s.Handler().ServeHTTP(writer, r)
	if !strings.Contains(writer.Body.String(), "id: 2") || strings.Contains(writer.Body.String(), "id: 1\n") {
		t.Fatalf("SSE replay: %s", writer.Body.String())
	}
}

type cancelStreamWriter struct {
	*httptest.ResponseRecorder
	cancel context.CancelFunc
}

func (w *cancelStreamWriter) Flush() { w.ResponseRecorder.Flush(); w.cancel() }

// Optional real PG + fresh Python interpreters; model and Pi remain fixtures.
type postgresResearchBridge struct{ python, script, dsn string }

func (b postgresResearchBridge) call(ctx context.Context, c any) (json.RawMessage, error) {
	raw, _ := json.Marshal(c)
	cmd := exec.CommandContext(ctx, b.python, b.script)
	cmd.Env = append(os.Environ(), "CHOR50_TEST_DSN="+b.dsn)
	cmd.Stdin = strings.NewReader(string(raw))
	out, e := cmd.Output()
	return out, e
}
func (b postgresResearchBridge) Describe(ctx context.Context) (json.RawMessage, error) {
	return b.call(ctx, map[string]string{"kind": "describe"})
}
func (b postgresResearchBridge) Execute(ctx context.Context, c domain.WorkflowCommand) (json.RawMessage, error) {
	return b.call(ctx, c)
}

func TestWorkflowPostgresPythonHTTPJourney(t *testing.T) {
	python := os.Getenv("CHOR50_TEST_PYTHON")
	dsn := os.Getenv("CHOR50_TEST_DSN")
	if python == "" || dsn == "" {
		t.Skip("requires disposable CHOR50_TEST_DSN and CHOR50_TEST_PYTHON")
	}
	s, runtime, task, _ := workflowFixture(t)
	script, err := filepath.Abs("../../services/workflow/tests/product_worker.py")
	if err != nil {
		t.Fatal(err)
	}
	s.workflowBridge = postgresResearchBridge{python, script, dsn}
	w := submitWorkflowFixture(t, s, task)
	w = waitingWorkflow(t, s, w)
	// Every bridge call is a new interpreter; the gate is durable in PG/SQLite.
	var p struct {
		Budget struct {
			Spent int `json:"spent_tokens"`
		} `json:"budget"`
	}
	_ = json.Unmarshal(w.Projection, &p)
	if p.Budget.Spent != 21 {
		t.Fatalf("PG budget %+v", p)
	}
	requestJSONWithHeaders(t, s.Handler(), http.MethodPost, workflowPath(w, "decision"), workflowDecision(w), map[string]string{"Idempotency-Key": "confirm"}, 200, nil)
	s.dispatchWorkflows(context.Background())
	w, err = s.store.Reader().GetWorkflow(context.Background(), w.ID)
	if err != nil || w.Status != "coding" {
		t.Fatalf("PG→Go→Pi: %+v %v", w, err)
	}
	if n, _ := runtime.snapshot(); n != 1 {
		t.Fatalf("starts=%d", n)
	}
	finishDelegationChild(t, s, runtime, w.RunID.String())
	s.dispatchWorkflows(context.Background())
	w, _ = s.store.Reader().GetWorkflow(context.Background(), w.ID)
	if w.Status != "awaiting_review" {
		t.Fatalf("final %+v", w)
	}
}

type lostBindingBridge struct {
	postgresResearchBridge
	lost bool
}

func (b *lostBindingBridge) Execute(ctx context.Context, c domain.WorkflowCommand) (json.RawMessage, error) {
	receipt, err := b.postgresResearchBridge.Execute(ctx, c)
	if err == nil && c.Kind == "bind_run" && !b.lost {
		b.lost = true
		return nil, errors.New("injected response loss after PG committed Go receipt")
	}
	return receipt, err
}
func TestWorkflowPostgresRunReceiptLossReusesRun(t *testing.T) {
	python, dsn := os.Getenv("CHOR50_TEST_PYTHON"), os.Getenv("CHOR50_TEST_DSN")
	if python == "" || dsn == "" {
		t.Skip("requires disposable PG and Python")
	}
	s, runtime, task, _ := workflowFixture(t)
	script, e := filepath.Abs("../../services/workflow/tests/product_worker.py")
	if e != nil {
		t.Fatal(e)
	}
	bridge := postgresResearchBridge{python, script, dsn}
	s.workflowBridge = &lostBindingBridge{postgresResearchBridge: bridge}
	w := waitingWorkflow(t, s, submitWorkflowFixture(t, s, task))
	requestJSONWithHeaders(t, s.Handler(), http.MethodPost, workflowPath(w, "decision"), workflowDecision(w), map[string]string{"Idempotency-Key": "confirm"}, 200, nil)
	s.dispatchWorkflows(context.Background())
	w, _ = s.store.Reader().GetWorkflow(context.Background(), w.ID)
	if w.Status != "unresolved" || !w.RunID.Valid() {
		t.Fatalf("lost receipt %+v", w)
	}
	original := w.RunID
	if n, _ := runtime.snapshot(); n != 0 {
		t.Fatalf("launched before receipt reconciliation: %d", n)
	}
	s.workflowBridge = bridge // Fresh interpreter queries the same PG operation.
	requestJSONWithHeaders(t, s.Handler(), http.MethodPost, workflowPath(w, "reconcile"), map[string]any{}, map[string]string{"Idempotency-Key": "reconcile"}, 200, nil)
	s.dispatchWorkflows(context.Background())
	w, _ = s.store.Reader().GetWorkflow(context.Background(), w.ID)
	if w.RunID != original || w.Status != "coding" {
		t.Fatalf("reconciled %+v", w)
	}
	if n, _ := runtime.snapshot(); n != 1 {
		t.Fatalf("starts=%d", n)
	}
}

func TestWorkflowBrowserJourney(t *testing.T) {
	if os.Getenv("CHOR50_BROWSER") != "1" {
		t.Skip("requires CHOR50_BROWSER=1 and a built web client")
	}
	s, runtime, task, _ := workflowFixture(t)
	webRoot, err := filepath.Abs("../../web/dist")
	if err != nil {
		t.Fatal(err)
	}
	s.webRoot = webRoot
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); s.runWorkflowDispatcher(ctx) }()
	defer func() { cancel(); <-done }()
	httpServer := httptest.NewServer(s.Handler())
	defer httpServer.Close()
	script, err := filepath.Abs("../../e2e/workflow-journey.mjs")
	if err != nil {
		t.Fatal(err)
	}
	cmdCtx, cmdCancel := context.WithTimeout(ctx, 90*time.Second)
	defer cmdCancel()
	cmd := exec.CommandContext(cmdCtx, "node", script)
	cmd.Env = append(os.Environ(), "CHOR50_BROWSER_URL="+httpServer.URL+"/rooms/"+task.RoomID+"/tasks/"+task.ID)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(stdout)
	passed := false
	for scanner.Scan() {
		line := scanner.Text()
		t.Log(line)
		if strings.HasPrefix(line, "CODING:") {
			finishDelegationChild(t, s, runtime, strings.TrimPrefix(line, "CODING:"))
		}
		if strings.HasPrefix(line, "BROWSER_PASS:") {
			passed = true
		}
	}
	if err = scanner.Err(); err != nil {
		t.Error(err)
	}
	if err = cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if !passed {
		t.Fatal("browser did not report completion")
	}
}
