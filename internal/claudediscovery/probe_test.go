package claudediscovery

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestOnlyBoundedReadOnlyCommands(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	exe := filepath.Join(root, "fixture")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\nprintf '%s' ready\n"), 0700); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"HOME": root, "PATH": "/usr/bin:/bin"}
	if out, err := Probe(context.Background(), exe, root, env, "--version"); err != nil || out != "ready" {
		t.Fatal(out, err)
	}
	for _, args := range [][]string{{"auth", "login"}, {"auth", "logout"}, {"--print", "secret"}, {"--help"}} {
		if _, err := Probe(context.Background(), exe, root, env, args...); err == nil {
			t.Fatal("mutating command permitted")
		}
	}
	if err := os.WriteFile(exe, []byte("#!/bin/sh\ni=0; while [ $i -lt 5000 ]; do printf x; i=$((i+1)); done\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := Probe(context.Background(), exe, root, env, "--version"); err == nil {
		t.Fatal("unbounded probe")
	}
}
