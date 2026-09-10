package localweb

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/pidiscovery"
)

func TestComposePathPiRuntimeReadyWiresTrustedLocalOnly(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	piPath := writeFakePathPi(t, base, "0.84.2", map[string]string{"ollama": "ready"})
	home := writeFakePathPiHome(t, base, "ollama", nil)
	runtimeRoot := filepath.Join(base, "runtime")
	sessionRoot := filepath.Join(base, "sessions")
	for _, dir := range []string{runtimeRoot, sessionRoot} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	composition, result, err := composePathPiRuntime(context.Background(), runtimeRoot, sessionRoot,
		pidiscovery.Options{LookPath: fixedPathPi(piPath), PiHome: home}, acceptanceTimeoutPolicyFor(nil))
	if err != nil {
		t.Fatal(err)
	}
	if result.State != pidiscovery.StateReady {
		t.Fatalf("state=%q reason=%q", result.State, result.Reason)
	}
	if composition.dockerSupervisor != nil || composition.adapter == nil || composition.trustedSupervisor == nil {
		t.Fatalf("unexpected composition: %#v", composition)
	}
	if err := validatePiCompositionFingerprints(context.Background(), composition); err != nil {
		t.Fatalf("fingerprints: %v", err)
	}
	status := piCompositionStatus(composition)
	if !status.Enabled || status.Provider != domain.TrustedHostExecutionProvider || status.Image != "" {
		t.Fatalf("status=%#v", status)
	}
}

func TestComposePathPiRuntimeNonReadyStillBoots(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runtimeRoot := filepath.Join(base, "runtime")
	sessionRoot := filepath.Join(base, "sessions")
	for _, dir := range []string{runtimeRoot, sessionRoot} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	composition, result, err := composePathPiRuntime(context.Background(), runtimeRoot, sessionRoot,
		pidiscovery.Options{LookPath: func(string) (string, error) { return "", errors.New("not found") }},
		acceptanceTimeoutPolicyFor(nil))
	if err != nil {
		t.Fatalf("non-ready discovery returned an error: %v", err)
	}
	if result.State != pidiscovery.StateMissing {
		t.Fatalf("state=%q want missing", result.State)
	}
	if composition.adapter != nil || composition.trustedSupervisor != nil || composition.dockerSupervisor != nil {
		t.Fatalf("non-ready discovery composed a runtime: %#v", composition)
	}
}

func TestComposePathPiRuntimeIncompatibleVersionStillBoots(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	piPath := writeFakePathPi(t, base, "0.83.0", map[string]string{"ollama": "ready"})
	home := writeFakePathPiHome(t, base, "ollama", nil)
	runtimeRoot := filepath.Join(base, "runtime")
	sessionRoot := filepath.Join(base, "sessions")
	for _, dir := range []string{runtimeRoot, sessionRoot} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	composition, result, err := composePathPiRuntime(context.Background(), runtimeRoot, sessionRoot,
		pidiscovery.Options{LookPath: fixedPathPi(piPath), PiHome: home}, acceptanceTimeoutPolicyFor(nil))
	if err != nil {
		t.Fatalf("incompatible discovery returned an error: %v", err)
	}
	if result.State != pidiscovery.StateIncompatibleVersion {
		t.Fatalf("state=%q want incompatible_version", result.State)
	}
	if composition.adapter != nil || composition.trustedSupervisor != nil {
		t.Fatalf("incompatible discovery composed a runtime: %#v", composition)
	}
}

func fixedPathPi(path string) func(string) (string, error) {
	return func(string) (string, error) { return path, nil }
}

func writeFakePathPi(t *testing.T, dir, version string, providers map[string]string) string {
	t.Helper()
	script := "#!/bin/sh\n"
	script += "if [ \"$1\" = \"--version\" ]; then echo \"" + version + "\"; exit 0; fi\n"
	script += "if [ \"$1\" = \"auth\" ]; then\n"
	script += "  provider=\"\"\n  shift\n"
	script += "  while [ $# -gt 0 ]; do\n"
	script += "    if [ \"$1\" = \"--provider\" ]; then provider=\"$2\"; shift; fi\n"
	script += "    shift\n  done\n"
	script += "  case \"$provider\" in\n"
	for provider, answer := range providers {
		script += "    " + provider + ") echo \"" + answer + "\" ;;\n"
	}
	script += "    *) echo \"not_ready\" ;;\n  esac\n  exit 0\nfi\n"
	script += "echo \"unexpected args: $@\" >&2\nexit 1\n"
	path := filepath.Join(dir, "pi")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeFakePathPiHome(t *testing.T, dir, defaultProvider string, storeProviders []string) string {
	t.Helper()
	home := filepath.Join(dir, "agent")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	settings, _ := json.Marshal(map[string]string{"defaultProvider": defaultProvider})
	if err := os.WriteFile(filepath.Join(home, "settings.json"), settings, 0o600); err != nil {
		t.Fatal(err)
	}
	store := map[string]any{}
	for _, provider := range storeProviders {
		store[provider] = struct{}{}
	}
	storeJSON, _ := json.Marshal(store)
	if err := os.WriteFile(filepath.Join(home, "models-store.json"), storeJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}
