package localweb

import (
	"bytes"
	"context"
	agentpi "github.com/Yangyang96/chora/internal/agent/pi"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/isolatedproxy"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsolatedUnavailableNeverAdmitsHostExecution(t *testing.T) {
	environment := newIsolatedLocalEnvironment("", t.TempDir())
	if err := environment.available(context.Background()); err == nil {
		t.Fatal("unprepared environment admitted execution")
	}
	server := &Server{isolatedLocal: environment}
	var view isolatedLocalView
	requestJSON(t, http.HandlerFunc(server.getIsolatedLocal), http.MethodGet, "/api/isolated-local", nil, http.StatusOK, &view)
	if view.State != "not_prepared" || view.PreparationAvailable {
		t.Fatalf("readiness=%+v", view)
	}
	binding, _ := domain.NewAgentExecutionProfileBinding(domain.AgentExecutionProfileIsolatedLocal)
	display := agentExecutionViewOf(binding)
	if !display.Sandboxed || display.ExecutionProvider != domain.DockerExecutionProvider {
		t.Fatalf("misleading disclosure: %+v", display)
	}
	source, err := agentpi.NewIsolatedSource(agentpi.IsolatedSourceParams{ImageID: "sha256:" + strings.Repeat("a", 64), HelperSHA256: strings.Repeat("b", 64), PolicySHA256: strings.Repeat("c", 64)})
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := agentpi.New(agentpi.Config{IsolatedSource: source})
	if err != nil {
		t.Fatal(err)
	}
	host, _ := domain.NewAgentExecutionProfileBinding(domain.AgentExecutionProfileTrustedLocal)
	if _, err = adapter.FingerprintForBinding(context.Background(), host); err == nil {
		t.Fatal("isolated adapter became a host source")
	}
}

func TestIsolatedProxyFileChangesRequireRestart(t *testing.T) {
	root := t.TempDir()
	e := newIsolatedLocalEnvironment("", root)
	if e.proxyUnchanged() != nil {
		t.Fatal("absent configuration changed")
	}
	path := filepath.Join(root, isolatedproxy.Filename)
	data := []byte(`{"schema":"chora.isolated-proxy.v1","httpsProxy":"http://proxy.example:8080"}`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if e.proxyUnchanged() == nil {
		t.Fatal("new proxy was hot-loaded")
	}
	e.proxy, _ = isolatedproxy.Load(root)
	if e.proxyUnchanged() != nil {
		t.Fatal("stable proxy rejected")
	}
	if err := os.WriteFile(path, bytes.ReplaceAll(data, []byte("8080"), []byte("8081")), 0600); err != nil {
		t.Fatal(err)
	}
	if e.proxyUnchanged() == nil {
		t.Fatal("changed proxy accepted")
	}
	os.Remove(path)
	if e.proxyUnchanged() == nil {
		t.Fatal("proxy removal silently switched to direct")
	}
}
