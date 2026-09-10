package pidiscovery

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverReady(t *testing.T) {
	root := t.TempDir()
	piPath := writeFakePi(t, root, "0.84.2", map[string]string{"ollama": "ready", "openai-codex": "ready", "google": "not_ready"})
	home := writeFakePiHome(t, root, "ollama", []string{"openai-codex", "google"})

	result, err := Discover(context.Background(), Options{LookPath: fixedPath(piPath), PiHome: home})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != StateReady {
		t.Fatalf("state=%q want ready (reason=%q)", result.State, result.Reason)
	}
	if result.Version != "0.84.2" {
		t.Fatalf("version=%q", result.Version)
	}
	if result.ExecutableSHA256 == ([32]byte{}) {
		t.Fatal("executable sha256 empty")
	}
	if len(result.ReadyProviders) != 2 {
		t.Fatalf("ready providers=%v", result.ReadyProviders)
	}
	if len(result.NotReadyProviders) != 1 || result.NotReadyProviders[0] != "google" {
		t.Fatalf("not-ready providers=%v", result.NotReadyProviders)
	}
}

func TestDiscoverMissing(t *testing.T) {
	result, err := Discover(context.Background(), Options{LookPath: func(string) (string, error) {
		return "", errors.New("not found")
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != StateMissing {
		t.Fatalf("state=%q want missing", result.State)
	}
}

func TestDiscoverIncompatibleVersion(t *testing.T) {
	root := t.TempDir()
	piPath := writeFakePi(t, root, "0.83.0", map[string]string{"ollama": "ready"})
	home := writeFakePiHome(t, root, "ollama", nil)

	result, err := Discover(context.Background(), Options{LookPath: fixedPath(piPath), PiHome: home})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != StateIncompatibleVersion {
		t.Fatalf("state=%q want incompatible_version (reason=%q)", result.State, result.Reason)
	}
}

func TestDiscoverUnconfigured(t *testing.T) {
	root := t.TempDir()
	piPath := writeFakePi(t, root, "0.84.2", map[string]string{"ollama": "not_ready"})
	home := writeFakePiHome(t, root, "ollama", nil)

	result, err := Discover(context.Background(), Options{LookPath: fixedPath(piPath), PiHome: home})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != StateUnconfigured {
		t.Fatalf("state=%q want unconfigured (reason=%q)", result.State, result.Reason)
	}
}

func TestDiscoverNotExecutable(t *testing.T) {
	root := t.TempDir()
	piPath := filepath.Join(root, "pi")
	if err := os.WriteFile(piPath, []byte("not a program"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := Discover(context.Background(), Options{LookPath: fixedPath(piPath)})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != StateNotExecutable {
		t.Fatalf("state=%q want not_executable", result.State)
	}
}

func TestRevalidateDrift(t *testing.T) {
	root := t.TempDir()
	piPath := writeFakePi(t, root, "0.84.2", map[string]string{"ollama": "ready"})
	home := writeFakePiHome(t, root, "ollama", nil)

	result, err := Discover(context.Background(), Options{LookPath: fixedPath(piPath), PiHome: home})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != StateReady {
		t.Fatalf("state=%q", result.State)
	}
	// Replace the executable bytes at the same path.
	if err := os.WriteFile(piPath, []byte("#!/bin/sh\necho 0.84.2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	revalidated, err := Revalidate(context.Background(), result)
	if err != nil {
		t.Fatal(err)
	}
	if revalidated.State != StateDrifted {
		t.Fatalf("state=%q want drifted", revalidated.State)
	}
}

func TestRevalidateStable(t *testing.T) {
	root := t.TempDir()
	piPath := writeFakePi(t, root, "0.84.2", map[string]string{"ollama": "ready"})
	home := writeFakePiHome(t, root, "ollama", nil)

	result, err := Discover(context.Background(), Options{LookPath: fixedPath(piPath), PiHome: home})
	if err != nil {
		t.Fatal(err)
	}
	revalidated, err := Revalidate(context.Background(), result)
	if err != nil {
		t.Fatal(err)
	}
	if revalidated.State != StateReady {
		t.Fatalf("state=%q want ready", revalidated.State)
	}
}

func fixedPath(path string) func(string) (string, error) {
	return func(string) (string, error) { return path, nil }
}

func writeFakePi(t *testing.T, dir, version string, providers map[string]string) string {
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

func writeFakePiHome(t *testing.T, dir, defaultProvider string, storeProviders []string) string {
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
