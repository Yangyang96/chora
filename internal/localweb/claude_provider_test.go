package localweb

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Yangyang96/chora/internal/agent/claudeprint"
	"github.com/Yangyang96/chora/internal/domain"
)

func TestClaudeCompositionRegistersOnlySelectedLocalRoute(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	for _, name := range []string{"home", "auth"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	executable := filepath.Join(root, "claude-fixture")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\ncase \"$1\" in --version) echo '2.1.251 (Claude Code)';; --safe-mode) echo '{\"loggedIn\":true,\"authMethod\":\"claude.ai\",\"apiProvider\":\"firstParty\"}';; *) exit 1;; esac\n"), 0700); err != nil {
		t.Fatal(err)
	}
	registration, err := NewClaudeRuntimeProvider(context.Background(), claudeprint.Config{Executable: executable, AuthHome: filepath.Join(root, "auth"), Home: filepath.Join(root, "home")}, filepath.Join(root, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	if len(registration.Supervisors) != 1 || registration.Supervisors[domain.TrustedHostExecutionProvider] == nil {
		t.Fatal("unexpected execution route")
	}
	registry := newAdapterRegistry()
	router := newRoutingSupervisor(nil)
	if err := registerRuntimeProviders(registry, router, []RuntimeProviderRegistration{registration}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Get(claudeprint.AdapterID); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Get("codex"); err == nil {
		t.Fatal("legacy runtime enabled")
	}
}
