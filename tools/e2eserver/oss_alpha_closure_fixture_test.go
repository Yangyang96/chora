//go:build chora_e2e

package main

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/localweb"
	"github.com/Yangyang96/chora/internal/productinstall"
)

func TestOSSAlphaAcknowledgementIsCurrentBeforeSuccessBecomesObservable(t *testing.T) {
	dataRoot := t.TempDir()
	if err := os.Chmod(dataRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	webRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(webRoot, "index.html"), []byte("<!doctype html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	server, err := newOSSAlphaClosureServer(context.Background(), filepath.Join(dataRoot, "chora.db"), webRoot, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	server.ackMode = "stale"
	request := httptest.NewRequest(http.MethodPost, "http://localhost/api/agent-execution/trusted-local-acknowledgements", strings.NewReader(`{"policyVersion":"chora.trusted-local-disclosure.v1"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "acknowledgement-order")
	response := &acknowledgementPublicationObserver{ResponseRecorder: httptest.NewRecorder(), beforeHeader: func() {
		// Simulate a client reading current state as soon as success is published.
		current := httptest.NewRecorder()
		server.Handler().ServeHTTP(current, httptest.NewRequest(http.MethodGet, "http://localhost/api/agent-execution/trusted-local-acknowledgements/current", nil))
		if current.Code != http.StatusOK || !strings.Contains(current.Body.String(), `"policyVersion":"chora.trusted-local-disclosure.v1"`) || !strings.Contains(current.Body.String(), `"acknowledged":true`) {
			t.Errorf("current acknowledgement at response publication: %d %s", current.Code, current.Body.String())
		}
	}}
	server.Handler().ServeHTTP(response, request)
	if response.Code < 200 || response.Code >= 300 {
		t.Fatalf("acknowledgement: %d %s", response.Code, response.Body.String())
	}
}

type acknowledgementPublicationObserver struct {
	*httptest.ResponseRecorder
	beforeHeader func()
}

func (observer *acknowledgementPublicationObserver) WriteHeader(code int) {
	observer.beforeHeader()
	observer.ResponseRecorder.WriteHeader(code)
}

func TestOSSAlphaControllerRecordsFirstReplayAndConflictWithoutExternalExecution(t *testing.T) {
	controller := newOSSAlphaInstallationController()
	request := localweb.ProductInstallationMutationRequest{
		ActorID: "owner", SessionID: "session", IdempotencyKey: "gc-key",
		Action: localweb.ProductInstallationActionGC, Limit: 7,
	}
	first, err := controller.Mutate(context.Background(), request)
	if err != nil || first.Replayed {
		t.Fatalf("first replayed=%v err=%v", first.Replayed, err)
	}
	replay, err := controller.Mutate(context.Background(), request)
	if err != nil || !replay.Replayed {
		t.Fatalf("replay replayed=%v err=%v", replay.Replayed, err)
	}
	conflict := request
	conflict.Limit = 8
	if _, err := controller.Mutate(context.Background(), conflict); !errors.Is(err, productinstall.ErrIdempotencyConflict) {
		t.Fatalf("conflict err=%v", err)
	}
	audit := controller.audit()
	if audit.ExternalOperationCount != 0 || audit.ProductionExecutorPresent || len(audit.Mutations) != 3 ||
		audit.Mutations[0].Outcome != "first" || audit.Mutations[1].Outcome != "replay" || audit.Mutations[2].Outcome != "conflict" ||
		audit.Mutations[0].IdempotencySlot != 1 || audit.Mutations[1].IdempotencySlot != 1 || audit.Mutations[2].IdempotencySlot != 1 {
		t.Fatalf("audit=%#v", audit)
	}
}
