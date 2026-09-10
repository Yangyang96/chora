package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/preflight"
)

func TestRunServeInstallationOnlyStartsWithoutActiveGenerationOrReferences(t *testing.T) {
	root := canonicalTempRoot(t)
	installRoot := filepath.Join(root, "install")
	webRoot := filepath.Join(installRoot, "build", "web-workspace", "web", "dist")
	dataRoot := filepath.Join(root, "data")
	for _, directory := range []string{webRoot, dataRoot} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(webRoot, "index.html"), []byte("<!doctype html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	stateRoot := filepath.Join(root, "installation-state")
	toolRoot := filepath.Join(root, "private-tools")
	args := []string{
		"--db", filepath.Join(dataRoot, "chora.db"), "--web", webRoot, "--port", "8787",
		"--source", installRoot, "--source-manifest", filepath.Join(installRoot, "source-manifest.json"),
		"--bundle-aggregate", strings.Repeat("b", 64), "--install", installRoot, "--data", dataRoot,
		"--repository", filepath.Join(root, "user-repository"), "--auth", filepath.Join(root, "auth.json"),
		"--ca", filepath.Join(root, "ca.pem"), "--proxy", preflight.FixedProxyURL, "--model-url", preflight.AllowedModelURL,
		"--preflight-fingerprint", strings.Repeat("a", 64), "--installation-state-root", stateRoot, "--generation", "g1",
		"--docker-cli", filepath.Join(toolRoot, "docker-"+dockerClientVersion), "--docker-context", "chora-local",
		"--endpoint-digest", strings.Repeat("e", 64), "--release-root", filepath.Join(root, "release"),
		"--release-manifest", "manifest.json", "--manifest-sha256", strings.Repeat("c", 64), "--release-id", "release-1",
		"--private-pi-root", filepath.Join(root, "private-pi"), "--private-pi-source", filepath.Join(root, "private-pi-source"),
		"--private-pi-manifest", filepath.Join(root, "private-pi-manifest.json"), "--private-pi-manifest-sha256", strings.Repeat("d", 64),
		"--probe-runtime-root", filepath.Join(root, "probe"), "--colima-tool-root", toolRoot, "--colima-profile", "chora-local",
		"--colima-source", filepath.Join(root, "colima-source"), "--docker-client-source", filepath.Join(root, "docker-source"),
	}
	probes := preflight.Probes{
		Host: func() (string, string) { return "linux", "amd64" },
		Command: func(context.Context, string, ...string) preflight.CommandResult {
			t.Fatal("installation-only startup used PATH-based Engine probe after host failure")
			return preflight.CommandResult{}
		},
	}
	var stdout, stderr bytes.Buffer
	listenCalls := 0
	listenErr := errors.New("injected listener stop")
	listen := func(server *http.Server) error {
		listenCalls++
		if server.Addr != "127.0.0.1:8787" {
			t.Fatalf("listen address = %q", server.Addr)
		}
		return listenErr
	}
	if exit := runServeWithProbesAndListen(args, &stdout, &stderr, probes, listen); exit != 1 || listenCalls != 1 || !strings.Contains(stdout.String(), "Chora Room available") {
		t.Fatalf("installation-only serve = exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
	if _, err := os.Lstat(stateRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("installation-only serve published inferred generation references: %v", err)
	}
}
