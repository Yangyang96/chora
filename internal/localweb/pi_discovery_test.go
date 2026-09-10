package localweb

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Yangyang96/chora/internal/pidiscovery"
)

func TestGetPiDiscoveryUnavailableWithoutPathPi(t *testing.T) {
	server := newPiDiscoveryTestServer(t)
	defer server.Close()

	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, newLocalRequest(http.MethodGet, "/api/pi/discovery", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d", response.Code)
	}
	var view piDiscoveryView
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.State != "unavailable" || view.Reason != piDiscoveryUnavailableReason {
		t.Fatalf("view=%#v", view)
	}
}

func TestGetPiDiscoveryReadyReportsProviders(t *testing.T) {
	server := newPiDiscoveryTestServer(t)
	defer server.Close()

	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	piPath := writeFakePathPi(t, base, "0.84.2", map[string]string{"ollama": "ready", "google": "not_ready"})
	home := writeFakePathPiHome(t, base, "ollama", []string{"google"})
	server.pathPiEnabled = true
	server.piDiscoveryOptions = pidiscovery.Options{LookPath: fixedPathPi(piPath), PiHome: home}

	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, newLocalRequest(http.MethodGet, "/api/pi/discovery", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var view piDiscoveryView
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.State != "ready" || view.Version != "0.84.2" || view.ExecutablePath != piPath {
		t.Fatalf("view=%#v", view)
	}
	if view.ExecutableSHA256 == "" || len(view.ExecutableSHA256) != 64 {
		t.Fatalf("executable sha256=%q", view.ExecutableSHA256)
	}
	if len(view.ReadyProviders) != 1 || view.ReadyProviders[0] != "ollama" {
		t.Fatalf("ready providers=%v", view.ReadyProviders)
	}
	if len(view.NotReadyProviders) != 1 || view.NotReadyProviders[0] != "google" {
		t.Fatalf("not-ready providers=%v", view.NotReadyProviders)
	}
}

func TestGetPiDiscoveryMissingReportsActionableReason(t *testing.T) {
	server := newPiDiscoveryTestServer(t)
	defer server.Close()

	server.pathPiEnabled = true
	server.piDiscoveryOptions = pidiscovery.Options{LookPath: func(string) (string, error) {
		return "", errors.New("executable file not found in $PATH")
	}}

	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, newLocalRequest(http.MethodGet, "/api/pi/discovery", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d", response.Code)
	}
	var view piDiscoveryView
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.State != "missing" || view.Reason == "" {
		t.Fatalf("view=%#v", view)
	}
}

func newPiDiscoveryTestServer(t *testing.T) *Server {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	webRoot := filepath.Join(root, "web")
	if err := os.Mkdir(webRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(webRoot, "index.html"), []byte("<!doctype html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	server, err := newTestServer(context.Background(), filepath.Join(root, "chora.db"), webRoot, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	return server
}
