package localweb

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/pidiscovery"
)

func TestModelAPIUsesSelectedRuntimeAndRejectsUnavailableCatalog(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "pi")
	table := filepath.Join(root, "catalog")
	body := "#!/bin/sh\ncase \"$1\" in\n--version) echo 0.85.1;;\nauth) echo ready;;\n--list-models) cat \"$PI_CODING_AGENT_DIR/catalog\";;\n*) exit 1;;\nesac\n"
	if err := os.WriteFile(executable, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "settings.json"), []byte(`{"defaultProvider":"p","defaultModel":"removed"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(table, []byte("provider model context max-out thinking images\np second 128K 16K yes no\np first 128K 16K yes no\n"), 0600); err != nil {
		t.Fatal(err)
	}
	server := &Server{piDiscoveryOptions: pidiscovery.Options{PiHome: root, LookPath: func(string) (string, error) { return executable, nil }}, isolatedLocal: newIsolatedLocalEnvironment("", root)}
	fetch := func(query string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		server.getSupportedModels(w, httptest.NewRequest(http.MethodGet, "/api/models"+query, nil))
		return w
	}
	response := fetch("?agentExecutionProfile=local_connected")
	if response.Code != http.StatusOK {
		t.Fatalf("catalog: %d %s", response.Code, response.Body.String())
	}
	var catalog domain.ModelCatalog
	if err := json.Unmarshal(response.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Validate(); err != nil {
		t.Fatalf("wire catalog cannot create binding: %v", err)
	}
	if catalog.AgentID != "path-pi" || catalog.RuntimeVersion != "0.85.1" || catalog.RuntimeIdentity == root || catalog.Models[0].ModelID != "first" {
		t.Fatalf("incorrect runtime catalog: %#v", catalog)
	}
	selected, err := domain.NewModelBinding(catalog, catalog.Models[0], time.Now())
	if err != nil {
		t.Fatal(err)
	}
	authority, err := pathModelCatalog(context.Background(), server.piDiscoveryOptions)
	if err != nil || selected.ValidateCatalog(authority) != nil {
		t.Fatalf("API/launch catalog mismatch: %v", err)
	}
	if got := fetch("?agentExecutionProfile=isolated_local"); got.Code != http.StatusServiceUnavailable {
		t.Fatal("isolated discovery fell back to host")
	}
	if got := fetch("?agentExecutionProfile=unknown"); got.Code != http.StatusBadRequest {
		t.Fatal("unknown profile accepted")
	}
	if err := os.WriteFile(table, []byte("no available models\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := fetch("?agentExecutionProfile=local_connected"); got.Code != http.StatusServiceUnavailable {
		t.Fatal("unavailable Runtime fell back to settings")
	}
}
