package localweb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
	"github.com/Yangyang96/chora/internal/store/sqlite"
)

// These catalogs, supervisors, credentials and provider responses are synthetic.
// The real SQLite transaction and HTTP handler remain in the path.
func workflowBudgetFixture(t *testing.T, change func(*domain.WorkflowPiAuthorization)) (*Server, *taskResourceLifecycleSupervisor, domain.WorkflowExecution, domain.Attempt, string) {
	t.Helper()
	s, runtime, task, root := workflowFixtureWithModel(t, true)
	id, _ := domain.ParseTaskID(task.ID)
	settings, err := s.store.Reader().GetTaskExecutionSettings(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := domain.NewAgentExecutionProfileBinding(settings.AgentExecutionProfile)
	p := domain.WorkflowPiAuthorization{Version: 1, Reference: "synthetic-only", AuthorizedAt: time.Now().UTC().Add(-time.Second), Deadline: time.Now().UTC().Add(10 * time.Minute), MaxRequests: 2, MaxTokens: 100000, MaxOutputTokens: 16, PerRequestOutputTokens: 8, Model: settings.ModelBinding, Profile: profile.Record(), TransportIdentity: app.WorkflowTransportIdentity()}
	if change != nil {
		change(&p)
	}
	var w domain.WorkflowExecution
	requestJSONWithHeaders(t, s.Handler(), http.MethodPost, "/api/tasks/"+task.ID+"/workflow", map[string]any{"projectId": task.ResourceSnapshot.ProjectID, "question": task.Goal, "piAuthorization": p}, map[string]string{"Idempotency-Key": "budget-workflow"}, 201, &w)
	w = waitingWorkflow(t, s, w)
	requestJSONWithHeaders(t, s.Handler(), http.MethodPost, workflowPath(w, "decision"), workflowDecision(w), map[string]string{"Idempotency-Key": "budget-accept"}, 200, nil)
	s.dispatchWorkflows(context.Background())
	w, err = s.store.Reader().GetWorkflow(context.Background(), w.ID)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := s.store.Reader().GetCurrentAttempt(context.Background(), w.RunID)
	if err != nil {
		t.Fatalf("workflow=%+v: %v", w, err)
	}
	if n, _ := runtime.snapshot(); n != 1 {
		t.Fatalf("synthetic launch count=%d workflow=%+v", n, w)
	}
	return s, runtime, w, attempt, root
}

type workflowFixtureConnection func(*http.Request) (*http.Response, error)

func (f workflowFixtureConnection) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestWorkflowPiPhysicalDispatchReservesBeforeHTTPAndRetainsUnknown(t *testing.T) {
	for _, mode := range []string{"response", "local-functions", "server-error", "output-violation", "missing-usage"} {
		t.Run(mode, func(t *testing.T) {
			s, _, w, a, _ := workflowBudgetFixture(t, nil)
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				calls.Add(1)
				actual, _ := io.ReadAll(request.Body)
				commands, err := s.store.Reader().ListWorkflowCommands(request.Context(), w.ID)
				if err != nil {
					t.Error(err)
					writer.WriteHeader(500)
					return
				}
				reserved := false
				for _, c := range commands {
					if c.Kind == "pi_dispatch" && len(c.Receipt) > 0 {
						var reservation app.WorkflowDispatchReservation
						if json.Unmarshal(c.Receipt, &reservation) == nil && reservation.PayloadDigest == domain.WorkflowDigest(actual) && reservation.InputTokens > 0 && reservation.OutputTokens == 8 {
							reserved = true
						}
					}
				}
				if !reserved {
					t.Error("HTTP arrived before durable reservation")
					writer.WriteHeader(500)
					return
				}
				if request.Header.Get("Authorization") != "Bearer synthetic-test-key" {
					t.Error("wrong synthetic connection")
				}
				if mode == "server-error" {
					writer.WriteHeader(503)
					return
				}
				out := 8
				if mode == "output-violation" {
					out = 9
				}
				usage := fmt.Sprintf(`,"usage":{"prompt_tokens":1,"completion_tokens":%d,"total_tokens":%d}`, out, out+1)
				if mode == "missing-usage" {
					usage = ""
				}
				fmt.Fprintf(writer, `{"model":"deepseek-v4-pro","choices":[{"message":{"role":"assistant","content":"synthetic"}}]%s}`, usage)
			}))
			defer upstream.Close()
			address, _ := url.Parse(upstream.URL)
			connection := workflowFixtureConnection(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() != "https://api.deepseek.com/chat/completions" {
					t.Error("unbounded upstream")
				}
				deadline, ok := r.Context().Deadline()
				if !ok || !deadline.Equal(w.PiAuthorization.Deadline) {
					t.Error("absolute dispatch deadline lost")
				}
				local := r.Clone(r.Context())
				u := *r.URL
				u.Scheme = address.Scheme
				u.Host = address.Host
				local.URL = &u
				return upstream.Client().Transport.RoundTrip(local)
			})
			transport, err := app.NewWorkflowChatTransport(s.service, "synthetic-test-key", connection)
			if err != nil {
				t.Fatal(err)
			}
			body := []byte(`{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"Chora?"}],"tools":[],"max_tokens":8,"thinking":{"type":"disabled"},"stream":false}`)
			for _, unsupported := range []map[string]any{
				{"tools": []any{map[string]string{"type": "web_search"}}},
				{"tools": []any{map[string]any{"type": "function", "function": map[string]any{"name": "read", "parameters": map[string]string{"$ref": "https://example.invalid/schema"}}}}},
				{"messages": []any{map[string]any{"role": "user", "content": []any{map[string]string{"type": "image_url", "image_url": "https://example.invalid/image"}}}}},
				{"messages": []any{map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"id": "call", "type": "web_search", "function": map[string]string{"name": "read", "arguments": "{}"}}}}}},
				{"thinking": map[string]string{"type": "disabled", "unproved_mode": "enabled"}},
			} {
				var changed map[string]any
				_ = json.Unmarshal(body, &changed)
				for key, value := range unsupported {
					changed[key] = value
				}
				bad, _ := json.Marshal(changed)
				if _, err = transport.Dispatch(context.Background(), w.ID, w.RunID, a.ID(), "unsupported", bad); err == nil {
					t.Fatal("unsupported provider renderer admitted")
				}
				if calls.Load() != 0 {
					t.Fatal("unsupported renderer contacted provider")
				}
			}
			if mode == "local-functions" {
				body = []byte(`{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"Chora 中文?"},{"role":"assistant","content":null,"tool_calls":[{"id":"call","type":"function","function":{"name":"read","arguments":"{\"path\":\"README.md\"}"}}]},{"role":"tool","tool_call_id":"call","content":"Supplied synthetic source."}],"tools":[{"type":"function","function":{"name":"read","description":"Read a scoped local file","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}}}],"max_tokens":8,"thinking":{"type":"disabled"},"stream":false}`)
			}
			_, err = transport.Dispatch(context.Background(), w.ID, w.RunID, a.ID(), "physical-one", body)
			knownResponse := mode == "response" || mode == "local-functions"
			if (err == nil) != knownResponse {
				t.Fatalf("mode=%s err=%v", mode, err)
			}
			if calls.Load() != 1 {
				t.Fatalf("physical requests=%d", calls.Load())
			}
			if _, err = transport.Dispatch(context.Background(), w.ID, w.RunID, a.ID(), "physical-one", body); err == nil {
				t.Fatal("replay sent another HTTP request")
			}
			if !knownResponse {
				if _, err = transport.Dispatch(context.Background(), w.ID, w.RunID, a.ID(), "physical-two", body); err == nil {
					t.Fatal("unknown refunded budget")
				}
				if calls.Load() != 1 {
					t.Fatal("unknown permitted a second physical request")
				}
			}
			var invalid map[string]any
			_ = json.Unmarshal(body, &invalid)
			invalid["model"] = "another-model"
			bad, _ := json.Marshal(invalid)
			if _, err = transport.Dispatch(context.Background(), w.ID, w.RunID, a.ID(), "switched-model", bad); err == nil {
				t.Fatal("model switch admitted")
			}
		})
	}
}

func reservePi(t *testing.T, service *app.Service, w domain.WorkflowExecution, a domain.Attempt, request string, input, output int) (app.WorkflowDispatchReservation, error) {
	t.Helper()
	return service.ReserveWorkflowDispatch(context.Background(), w.ID, w.RunID, a.ID(), request, w.PiAuthorization.TransportIdentity, strings.Repeat("a", 64), input, output)
}

func finishPi(t *testing.T, s *app.Service, w domain.WorkflowExecution, r app.WorkflowDispatchReservation, known bool) {
	t.Helper()
	if err := s.FinishWorkflowDispatch(context.Background(), w.ID, r, 1, r.OutputTokens, known); err != nil {
		t.Fatal(err)
	}
}

func TestWorkflowPiBudgetExactAndOverCeilings(t *testing.T) {
	s, _, w, a, _ := workflowBudgetFixture(t, func(p *domain.WorkflowPiAuthorization) { p.MaxTokens = 216 })
	if _, err := reservePi(t, s.service, w, a, "over-total", 209, 8); err == nil {
		t.Fatal("one over token ceiling admitted")
	}
	if _, err := reservePi(t, s.service, w, a, "over-output", 100, 9); err == nil {
		t.Fatal("one over output ceiling admitted")
	}
	for _, id := range []string{"one", "two"} {
		r, err := reservePi(t, s.service, w, a, id, 100, 8)
		if err != nil {
			t.Fatal(err)
		}
		finishPi(t, s.service, w, r, true)
		if _, err = reservePi(t, s.service, w, a, id, 100, 8); err == nil {
			t.Fatal("reserved operation was retransmitted")
		}
	}
	if _, err := reservePi(t, s.service, w, a, "three", 0, 1); err == nil {
		t.Fatal("exhausted Run admitted another request")
	}
	commands, _ := s.store.Reader().ListWorkflowCommands(context.Background(), w.ID)
	n := 0
	for _, c := range commands {
		if c.Kind == "pi_dispatch" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("dispatch reservations=%d", n)
	}
}

func TestWorkflowPiBudgetConcurrentAndCrashUnknownSurvivesSQLiteReopen(t *testing.T) {
	s, _, w, a, root := workflowBudgetFixture(t, nil)
	var wg sync.WaitGroup
	results := make(chan app.WorkflowDispatchReservation, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, err := reservePi(t, s.service, w, a, string(rune('a'+i)), 100, 8)
			if err == nil {
				results <- r
			}
		}(i)
	}
	wg.Wait()
	close(results)
	if len(results) != 1 {
		t.Fatalf("concurrent reservations=%d", len(results))
	}
	r := <-results
	paths, err := filepath.Glob(filepath.Join(filepath.Dir(root), "*", "chora.db"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("database paths=%v %v", paths, err)
	}
	db, err := sqlite.Open(context.Background(), paths[0])
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	fresh := app.NewService(app.Dependencies{Store: db, Authorizer: localAuthorizer{}})
	if _, err = reservePi(t, fresh, w, a, "after-crash", 100, 8); err == nil {
		t.Fatal("crashed dispatch refunded after reopen")
	}
	finishPi(t, fresh, w, r, false)
	if err = fresh.FinishWorkflowDispatch(context.Background(), w.ID, r, 1, 8, true); err == nil {
		t.Fatal("unknown overwritten with available balance")
	}
	if _, err = reservePi(t, fresh, w, a, "after-unknown", 100, 8); err == nil {
		t.Fatal("unknown permitted new dispatch")
	}
}

func TestWorkflowPiBudgetStopAndFrozenIdentity(t *testing.T) {
	s, _, w, a, _ := workflowBudgetFixture(t, nil)
	commands, _ := s.store.Reader().ListWorkflowCommands(context.Background(), w.ID)
	observed := false
	for _, c := range commands {
		if c.Kind != "pi_launch:"+a.ID().String() {
			continue
		}
		var identity struct {
			Version         string              `json:"runtime_version"`
			ArgumentsDigest string              `json:"arguments_digest"`
			Model           domain.ModelBinding `json:"model_binding"`
		}
		if json.Unmarshal(c.Receipt, &identity) != nil || identity.Version != "0.85.1" || len(identity.ArgumentsDigest) != 64 || identity.Model.JSON() != a.ModelBinding().JSON() {
			t.Fatal("launch identity was not frozen from the selected runtime")
		}
		observed = true
	}
	if !observed {
		t.Fatal("missing durable launch identity")
	}
	if _, err := s.service.ReserveWorkflowDispatch(context.Background(), w.ID, w.RunID, a.ID(), "wrong-transport", strings.Repeat("0", 64), strings.Repeat("a", 64), 100, 8); err == nil {
		t.Fatal("wrong transport identity admitted")
	}
	if _, err := s.service.StopWorkflow(context.Background(), commandMeta("budget-stop"), w.ID, w.ProjectID); err != nil {
		t.Fatal(err)
	}
	if _, err := reservePi(t, s.service, w, a, "after-stop", 100, 8); err == nil {
		t.Fatal("Stop permitted new dispatch")
	}
	err := s.store.WithinWriteTx(context.Background(), func(tx storecontract.WriteTx) error {
		current, e := tx.GetWorkflow(context.Background(), w.ID)
		if e != nil {
			return e
		}
		p := *current.PiAuthorization
		p.MaxRequests++
		old := current.Version
		current.Version++
		current.PiAuthorization = &p
		return tx.SaveWorkflowCAS(context.Background(), old, current)
	})
	if err == nil {
		t.Fatal("frozen authorization was increased")
	}
}

func TestWorkflowPiBudgetIsSharedByHumanSuccessorAttempt(t *testing.T) {
	s, runtime, w, a, _ := workflowBudgetFixture(t, nil)
	ctx := context.Background()
	r, err := reservePi(t, s.service, w, a, "attempt-one", 100, 8)
	if err != nil {
		t.Fatal(err)
	}
	finishPi(t, s.service, w, r, true)
	finishDelegationChild(t, s, runtime, w.RunID.String())
	s.dispatchWorkflows(ctx)
	run, _ := s.store.Reader().GetRun(ctx, w.RunID)
	view, err := s.runView(ctx, w.RunID)
	if err != nil {
		t.Fatal(err)
	}
	requestJSONWithHeaders(t, s.Handler(), http.MethodPost, "/api/v2/runs/"+w.RunID.String()+"/review", map[string]any{"expectedVersion": run.Version(), "kind": "reject", "comment": "Add the missing explanation.", "resultDigest": view.ResourceResult.Digest}, map[string]string{"Idempotency-Key": "budget-reject"}, 200, nil)
	run, _ = s.store.Reader().GetRun(ctx, w.RunID)
	requestJSONWithHeaders(t, s.Handler(), http.MethodPost, "/api/runs/"+w.RunID.String()+"/retry", map[string]any{"expectedVersion": run.Version(), "instructions": "Add the missing explanation within the frozen scope."}, map[string]string{"Idempotency-Key": "budget-retry"}, 200, nil)
	next, _ := s.store.Reader().GetCurrentAttempt(ctx, w.RunID)
	if next.Sequence() != 2 || next.ID() == a.ID() || next.ModelBinding().JSON() != a.ModelBinding().JSON() {
		t.Fatal("successor identity drift")
	}
	r, err = reservePi(t, s.service, w, next, "attempt-two", 100, 8)
	if err != nil {
		t.Fatal(err)
	}
	finishPi(t, s.service, w, r, true)
	if _, err = reservePi(t, s.service, w, next, "attempt-two-extra", 1, 1); err == nil {
		t.Fatal("successor reset Run budget")
	}
	current, _ := s.store.Reader().GetWorkflow(ctx, w.ID)
	if !current.PiAuthorization.Deadline.Equal(w.PiAuthorization.Deadline) || current.InputDigest != w.InputDigest || current.Gate.ID != w.Gate.ID {
		t.Fatal("successor renewed authority")
	}
}

func TestWorkflowPiExpiryStopsLocallyWithMissingBridge(t *testing.T) {
	s, _, w, a, _ := workflowBudgetFixture(t, func(p *domain.WorkflowPiAuthorization) { p.Deadline = time.Now().UTC().Add(time.Second) })
	time.Sleep(time.Until(w.PiAuthorization.Deadline) + time.Millisecond)
	s.workflowBridge = nil
	s.dispatchWorkflows(context.Background())
	current, err := s.store.Reader().GetWorkflow(context.Background(), w.ID)
	if err != nil {
		t.Fatal(err)
	}
	run, _ := s.store.Reader().GetRun(context.Background(), w.RunID)
	if !current.StopRequested || current.Status != "unresolved" || run.State() != domain.RunStateCancelled {
		t.Fatalf("expiry with bridge down: workflow=%+v Run=%s", current, run.State())
	}
	if _, err = reservePi(t, s.service, w, a, "expired", 100, 8); err == nil {
		t.Fatal("expired dispatch admitted")
	}
}

func TestWorkflowPiWrongTransportRefusesBeforeResearch(t *testing.T) {
	s, _, task, _ := workflowFixtureWithModel(t, true)
	ctx := context.Background()
	id, _ := domain.ParseTaskID(task.ID)
	settings, err := s.store.Reader().GetTaskExecutionSettings(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := domain.NewAgentExecutionProfileBinding(settings.AgentExecutionProfile)
	p := domain.WorkflowPiAuthorization{Version: 1, Reference: "synthetic", AuthorizedAt: time.Now().UTC().Add(-time.Second), Deadline: time.Now().UTC().Add(time.Minute), MaxRequests: 1, MaxTokens: 10000, MaxOutputTokens: 8, PerRequestOutputTokens: 8, Model: settings.ModelBinding, Profile: profile.Record(), TransportIdentity: strings.Repeat("0", 64)}
	requestJSONWithHeaders(t, s.Handler(), http.MethodPost, "/api/tasks/"+task.ID+"/workflow", map[string]any{"projectId": task.ResourceSnapshot.ProjectID, "question": task.Goal, "piAuthorization": p}, map[string]string{"Idempotency-Key": "wrong-transport"}, 403, nil)
	if s.workflowBridge.(*fixtureResearchBridge).calls != 0 {
		t.Fatal("wrong transport started research")
	}
	if _, err = s.store.Reader().GetTaskWorkflow(ctx, id); !errors.Is(err, storecontract.ErrNotFound) {
		t.Fatalf("refused workflow persisted: %v", err)
	}
}
