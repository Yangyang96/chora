package localweb

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Yangyang96/chora/internal/agent/hermes"
)

func TestHermesCompositionRefusesBeforeSupervisorOrNativeProbe(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"auth", "hermes_cli"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(root, "fixture")
	if err := os.WriteFile(path, []byte("#!/bin/sh\ntouch '"+filepath.Join(root, "auth", "unexpected")+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"install-stamp.json", "pyproject.toml", "hermes_bootstrap.py", "hermes_cli/main.py", "hermes_cli/_parser.py", "hermes_cli/stream_json.py", "hermes_cli/env_loader.py", "hermes_cli/gateway_chat.py", "hermes_cli/gateway_chat_startup.py"} {
		data := []byte("# synthetic\n")
		if name == "install-stamp.json" {
			data = []byte(`{"displayVersion":"0.25.0"}`)
		}
		if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	runtimeRoot := filepath.Join(root, "runtime")
	c := hermes.Config{Executable: path, NativeLauncher: path, Interpreter: path, SourceRoot: root, AuthHome: filepath.Join(root, "auth")}
	registration, err := NewHermesRuntimeProvider(context.Background(), c, runtimeRoot)
	if !errors.Is(err, hermes.ErrSafety) || registration.Provider != nil || len(registration.Supervisors) != 0 {
		t.Fatal(registration, err)
	}
	if _, err := os.Stat(runtimeRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("supervisor side effect", err)
	}
	entries, err := os.ReadDir(c.AuthHome)
	if err != nil || len(entries) != 0 {
		t.Fatal("native probe side effect", entries, err)
	}
}
