package localweb

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Yangyang96/chora/internal/agent/opencoderun"
)

func TestOpenCodeRegistrationPropagatesNativeRefusal(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(root, "opencode-fixture")
	script := "#!/bin/sh\ncase \"$1\" in --version) echo 1.19.5;; run) test \"$2\" = --help || exit 99; echo '--pure --format --model';; auth) echo '0 credentials';; *) exit 99;; esac\n"
	if err := os.WriteFile(executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	registration, err := NewOpenCodeRuntimeProvider(context.Background(), opencoderun.Config{Executable: executable, Home: root, Model: "deepseek/fixture"}, filepath.Join(root, "runtime"))
	var qualification *opencoderun.QualificationError
	if !errors.Is(err, opencoderun.ErrNativeConfig) || !errors.Is(err, opencoderun.ErrNativeRetry) || !errors.As(err, &qualification) || registration.Provider != nil || len(registration.Supervisors) != 0 {
		t.Fatal(registration, err)
	}
	if _, err := os.Stat(filepath.Join(root, "runtime")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("supervisor created before qualification", err)
	}
}
