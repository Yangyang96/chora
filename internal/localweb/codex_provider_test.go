package localweb

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Yangyang96/chora/internal/agent/codexexec"
)

func TestCodexCompositionRejectsUnverifiableManagedPolicy(t *testing.T) {
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
	if !errors.Is(err, codexexec.ErrManagedPolicy) || registration.Provider != nil || len(registration.Supervisors) != 0 {
		t.Fatalf("unqualified registration: %+v, %v", registration, err)
	}
}
