package localweb

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Yangyang96/chora/internal/agent/codexexec"
	"github.com/Yangyang96/chora/internal/domain"
)

func TestCodexCompositionRegistersOnlySelectedLocalRoute(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	for _, name := range []string{"home", "auth"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	executable := filepath.Join(root, "codex-fixture")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\ncase \"$1\" in --version) echo 'codex-cli 0.155.1';; login) echo 'Logged in using ChatGPT';; *) exit 1;; esac\n"), 0700); err != nil {
		t.Fatal(err)
	}
	registration, err := NewCodexRuntimeProvider(context.Background(), codexexec.Config{Executable: executable, AuthHome: filepath.Join(root, "auth"), Home: filepath.Join(root, "home")}, filepath.Join(root, "runtime"))
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
	if _, err := registry.Get(codexexec.AdapterID); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Get("codex"); err == nil {
		t.Fatal("legacy runtime enabled")
	}
}
