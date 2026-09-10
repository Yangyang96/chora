package localweb

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/preflight"
)

func TestReadinessAPIIsCachedUntilExplicitRefresh(t *testing.T) {
	server := newReadinessTestServer(t)
	server.piStatus = piRuntimeStatus{Enabled: true, Reason: "ready"}
	server.verifierStatus = verifierRuntimeStatus{Enabled: true, Reason: "ready", BaselineDigest: strings.Repeat("b", 64)}
	server.piBaseline = func() error { return nil }
	calls := 0
	server.piPreflight = func(context.Context) preflight.Report {
		calls++
		if calls == 1 {
			return preflight.Report{Status: preflight.StatusPassed, ResourcesCreated: false, InputFingerprint: strings.Repeat("a", 64)}
		}
		return preflight.Report{Status: preflight.StatusFailed, ResourcesCreated: false, InputFingerprint: strings.Repeat("c", 64), Failure: &preflight.Failure{
			Boundary: "oauth.provider", Observed: "provider entry absent", Required: "owner-only openai-codex OAuth entry", Action: "Authenticate, then Refresh readiness.",
		}}
	}

	var first, cached, refreshed readinessView
	requestJSON(t, server.Handler(), http.MethodGet, "/api/readiness", nil, http.StatusOK, &first)
	requestJSON(t, server.Handler(), http.MethodGet, "/api/readiness", nil, http.StatusOK, &cached)
	if calls != 1 || first.InputFingerprint != strings.Repeat("a", 64) || cached.InputFingerprint != first.InputFingerprint {
		t.Fatalf("cached readiness calls=%d first=%#v cached=%#v", calls, first, cached)
	}
	requestJSON(t, server.Handler(), http.MethodPost, "/api/readiness/refresh", nil, http.StatusOK, &refreshed)
	if calls != 2 || refreshed.InputFingerprint != strings.Repeat("c", 64) || readinessItemByKey(t, refreshed, "oauth").State != readinessBlocked {
		t.Fatalf("refreshed readiness calls=%d view=%#v", calls, refreshed)
	}
}

func TestReadinessAPIExposesCheckingWhileRefreshIsRunning(t *testing.T) {
	server := newReadinessTestServer(t)
	started := make(chan struct{})
	release := make(chan struct{})
	server.piPreflight = func(context.Context) preflight.Report {
		close(started)
		<-release
		return preflight.Report{Status: preflight.StatusPassed, ResourcesCreated: false, InputFingerprint: strings.Repeat("e", 64)}
	}
	refreshDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		request := newLocalRequest(http.MethodPost, "/api/readiness/refresh", nil)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		refreshDone <- response
	}()
	<-started
	var checking readinessView
	requestJSON(t, server.Handler(), http.MethodGet, "/api/readiness", nil, http.StatusOK, &checking)
	for _, item := range checking.Items {
		if item.State != readinessChecking {
			t.Fatalf("item %q state during refresh = %q, want checking", item.Key, item.State)
		}
	}
	close(release)
	response := <-refreshDone
	if response.Code != http.StatusOK {
		t.Fatalf("refresh status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestProductionHandlerClosesLegacySpecCodingBootstrapRoutes(t *testing.T) {
	server := newReadinessTestServer(t)
	server.product = true
	for _, route := range []string{"/api/spec-coding/materializations", "/api/spec-coding/contracts"} {
		request := newLocalRequest(http.MethodPost, route, strings.NewReader(`{}`))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("production route %s status=%d body=%s", route, response.Code, response.Body.String())
		}
	}
}

func TestProductServerStartsBlockedWhenExecutionDependenciesAreUnavailable(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	webRoot := filepath.Join(root, "web")
	if err := os.MkdirAll(webRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(webRoot, "index.html"), []byte("<!doctype html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	repositoryRoot := filepath.Join(root, "installed-source")
	if err := os.Mkdir(repositoryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	server, err := NewProduct(context.Background(), filepath.Join(root, "data", "chora.db"), webRoot, log.New(io.Discard, "", 0), ProductOptions{
		RepoRoot:       repositoryRoot,
		RepositoryRoot: repositoryRoot,
		PiAuthFile:     filepath.Join(root, "missing-auth.json"),
		PiPreflight: func(context.Context) preflight.Report {
			return preflight.Report{Status: preflight.StatusFailed, ResourcesCreated: false, InputFingerprint: strings.Repeat("d", 64), Failure: &preflight.Failure{
				Boundary: "docker.daemon", Observed: "Docker daemon unavailable", Required: "pinned Docker daemon", Action: "Start the pinned Colima profile, then Refresh readiness.",
			}}
		},
	})
	if err != nil {
		t.Fatalf("blocked product server failed to start: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	if server.piStatus.Enabled || server.verifierStatus.Enabled {
		t.Fatalf("blocked dependencies reported enabled: pi=%#v verifier=%#v", server.piStatus, server.verifierStatus)
	}
	statusRequest := newLocalRequest(http.MethodGet, "/api/status", nil)
	statusResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(statusResponse, statusRequest)
	statusBody := statusResponse.Body.String()
	if statusResponse.Code != http.StatusOK || strings.Contains(statusBody, root) || strings.Contains(statusBody, repositoryRoot) || strings.Contains(statusBody, filepath.Join(root, "missing-auth.json")) ||
		!strings.Contains(statusBody, "legacy Codex Runtime is disabled") || !strings.Contains(statusBody, "inspect public readiness") {
		t.Fatalf("product status is not path-safe: status=%d body=%q", statusResponse.Code, statusBody)
	}
	var view readinessView
	requestJSON(t, server.Handler(), http.MethodGet, "/api/readiness", nil, http.StatusOK, &view)
	if view.InputFingerprint != strings.Repeat("d", 64) || readinessItemByKey(t, view, "docker_engine").State != readinessBlocked {
		t.Fatalf("blocked readiness = %#v", view)
	}
}

func newReadinessTestServer(t *testing.T) *Server {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	webRoot := filepath.Join(root, "web")
	if err := os.MkdirAll(webRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(webRoot, "index.html"), []byte("<!doctype html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	server, err := newTestServer(context.Background(), filepath.Join(root, "chora.db"), webRoot, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return server
}
