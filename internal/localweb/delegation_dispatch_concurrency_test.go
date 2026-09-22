package localweb

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
)

func TestDelegationStopPersistsWhileDispatcherIsBusy(t *testing.T) {
	server, runtime, parent, _ := delegationFixture(t)
	endpoint := "/api/tasks/" + parent.ID + "/delegation"
	id, _ := domain.ParseTaskID(parent.ID)
	// Model an unrelated slow launch holding the dispatcher mutex. HTTP intent
	// must persist without waiting for that external operation to complete.
	server.delegationMu.Lock()
	var release sync.Once
	unlock := func() { release.Do(server.delegationMu.Unlock) }
	defer unlock()
	post := func(path string, body any, key string, status int) {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := newLocalRequest(http.MethodPost, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		response := httptest.NewRecorder()
		done := make(chan struct{})
		go func() {
			defer close(done)
			server.Handler().ServeHTTP(response, req)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			unlock()
			<-done
			t.Fatal("HTTP delegation intent waited for dispatcher I/O")
		}
		if response.Code != status {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	}
	post(endpoint, delegationPlan(), "start-while-busy", http.StatusCreated)
	d, err := server.store.Reader().GetTaskDelegation(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	post(endpoint+"/stop", map[string]any{"expectedVersion": d.Version}, "stop-while-busy", http.StatusOK)
	d, err = server.store.Reader().GetTaskDelegation(context.Background(), id)
	if err != nil || d.State != domain.DelegationStopping {
		t.Fatalf("stop intent not durable: %#v err=%v", d, err)
	}
	unlock()
	server.dispatchDelegations(context.Background())
	d, err = server.store.Reader().GetTaskDelegation(context.Background(), id)
	if err != nil || d.State != domain.DelegationStopped {
		t.Fatalf("stop did not settle: %#v err=%v", d, err)
	}
	if n, _ := runtime.snapshot(); n != 0 {
		t.Fatalf("dispatcher launched after persisted stop: %d", n)
	}
}
