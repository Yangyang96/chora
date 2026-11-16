package opencodediscovery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestProbeAllowsOnlyBoundedMetadata(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "fixture")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\necho metadata\n"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"--version"}, {"run", "--help"}, {"auth", "list"}} {
		out, err := Probe(context.Background(), executable, root, map[string]string{"PATH": "/usr/bin:/bin"}, args...)
		if err != nil || out != "metadata\n" {
			t.Fatal(out, err)
		}
	}
	for _, args := range [][]string{{"run", "prompt"}, {"debug", "config"}, {"auth", "login"}, {"upgrade"}} {
		if _, err := Probe(context.Background(), executable, root, nil, args...); !errors.Is(err, ErrProbe) {
			t.Fatal(args, err)
		}
	}
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n/usr/bin/head -c 40000 /dev/zero\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := Probe(context.Background(), executable, root, nil, "--version"); !errors.Is(err, ErrProbe) {
		t.Fatal("unbounded probe", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Probe(ctx, executable, root, nil, "--version"); !errors.Is(err, ErrProbe) {
		t.Fatal(err)
	}
}
