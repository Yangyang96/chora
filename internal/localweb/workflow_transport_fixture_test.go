//go:build chora_offline_fixture

package localweb

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
)

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
			transport, err := app.NewWorkflowChatTransportForFixture(s.service, "synthetic-test-key", connection)
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
