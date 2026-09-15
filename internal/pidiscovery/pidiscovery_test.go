package pidiscovery

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseModelTableUsesRuntimeCatalog(t *testing.T) {
	models := parseModelTable("provider model context max-out thinking images\ndeepseek deepseek-v4-flash 1M 64K yes no\nopenai-codex gpt-5.6-sol 272K 32K yes yes\n")
	if len(models) != 2 || models[0].Provider != "deepseek" || models[1].ModelID != "gpt-5.6-sol" {
		t.Fatalf("models = %#v", models)
	}
}

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

func TestParseModelTableRejectsUnverifiableOutput(t *testing.T) {
	for _, output := range []string{
		"", "No models available. Please log in.",
		"openai model 128K 32K yes yes\n",
		"provider model context\nopenai model 128K\n",
		"provider model context max-out thinking images\nWarning: failed to load model catalog\n",
		"provider model context max-out thinking images\nopenai model 128K 32K yes yes\npartial row\n",
		"provider model context max-out thinking images\n",
	} {
		if models := ParseModelTable(output); len(models) != 0 {
			t.Fatalf("accepted unverifiable output %q: %#v", output, models)
		}
	}
}

func TestParseModelTableSortsAndDeduplicates(t *testing.T) {
	models := ParseModelTable("provider model context max-out thinking images\nzeta b 1.5M 32K yes no\nalpha z 128K 16384 no yes\nzeta a 1M 32K yes no\nzeta b 1.5M 32K yes no\n")
	if len(models) != 3 || models[0] != (ModelOption{"alpha", "z"}) || models[1] != (ModelOption{"zeta", "a"}) || models[2] != (ModelOption{"zeta", "b"}) {
		t.Fatalf("models = %#v", models)
	}
}

func TestDiscoverModelsUsesSelectedRuntimeAndConfiguration(t *testing.T) {
	root := t.TempDir()
	selectedHome := filepath.Join(root, "selected")
	ambientHome := filepath.Join(root, "ambient")
	for _, home := range []string{selectedHome, ambientHome} {
		if err := os.Mkdir(home, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	catalog := "provider model context max-out thinking images\nselected current 128K 32K yes no\n"
	if err := os.WriteFile(filepath.Join(selectedHome, "catalog"), []byte(catalog), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ambientHome, "catalog"), []byte("wrong runtime home"), 0o600); err != nil {
		t.Fatal(err)
	}
	selectedPi := writeCatalogPi(t, t.TempDir(), "[ \"$1\" = --list-models ] || exit 1\n/bin/cat \"$PI_CODING_AGENT_DIR/catalog\"\n")
	ambientPiDir := t.TempDir()
	writeCatalogPi(t, ambientPiDir, "exit 99\n")
	t.Setenv("PATH", ambientPiDir)
	t.Setenv("PI_CODING_AGENT_DIR", ambientHome)
	models, err := DiscoverModelsForRuntime(context.Background(), selectedPi, selectedHome)
	if err != nil || len(models) != 1 || models[0] != (ModelOption{"selected", "current"}) {
		t.Fatalf("models = %#v, error = %v", models, err)
	}
	// A stale default cannot resurrect a model removed from the runtime catalog.
	if err := os.WriteFile(filepath.Join(selectedHome, "settings.json"), []byte(`{"defaultProvider":"selected","defaultModel":"current"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(selectedHome, "catalog"), []byte("No models available\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if models, err := DiscoverModelsForRuntime(context.Background(), selectedPi, selectedHome); err == nil || len(models) != 0 {
		t.Fatalf("accepted removed model: %#v, error = %v", models, err)
	}
}

func TestDiscoverModelsCommandFailureDoesNotFallBack(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "settings.json"), []byte(`{"defaultProvider":"old","defaultModel":"removed"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	pi := writeCatalogPi(t, t.TempDir(), "echo 'provider model context max-out thinking images'\necho 'old removed 128K 32K yes yes'\nexit 1\n")
	if models, err := DiscoverModelsForRuntime(context.Background(), pi, home); err == nil || len(models) != 0 {
		t.Fatalf("accepted failed catalog: %#v, error = %v", models, err)
	}
	if _, err := DiscoverModelsForRuntime(context.Background(), "pi", home); err == nil {
		t.Fatal("accepted ambient executable lookup")
	}
}

func TestDiscoverModelsHonorsTimeout(t *testing.T) {
	pi := writeCatalogPi(t, t.TempDir(), "exec /bin/sleep 10\n")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	models, err := DiscoverModelsForRuntime(ctx, pi, t.TempDir())
	if !errors.Is(err, context.DeadlineExceeded) || len(models) != 0 {
		t.Fatalf("models = %#v, error = %v", models, err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("model discovery did not stop promptly")
	}
}

func writeCatalogPi(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "pi")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestResolvePiHomePrecedenceAndNormalization(t *testing.T) {
	userHome := t.TempDir()
	ambient := t.TempDir()
	explicit := t.TempDir()
	t.Setenv("HOME", userHome)
	t.Setenv("PI_CODING_AGENT_DIR", ambient)
	for _, tt := range []struct{ input, want string }{
		{explicit, explicit},
		{"", ambient},
		{explicit + "/nested/..", explicit},
		{"~/custom", filepath.Join(userHome, "custom")},
	} {
		got, err := ResolvePiHome(tt.input)
		if err != nil || got != tt.want {
			t.Fatalf("ResolvePiHome(%q)=%q,%v want %q", tt.input, got, err, tt.want)
		}
	}
	t.Setenv("PI_CODING_AGENT_DIR", "")
	if got, err := ResolvePiHome(""); err != nil || got != filepath.Join(userHome, ".pi", "agent") {
		t.Fatalf("default PiHome = %q, %v", got, err)
	}
	if got, err := ResolvePiHome("relative/../agent"); err != nil || !filepath.IsAbs(got) || filepath.Clean(got) != got {
		t.Fatalf("unclean relative resolution = %q, %v", got, err)
	}
}

func TestDiscoveryUsesResolvedPiHomeForCatalogAndReadiness(t *testing.T) {
	ambient := writeFakePiHome(t, t.TempDir(), "ambient", nil)
	explicit := writeFakePiHome(t, t.TempDir(), "explicit", nil)
	for _, fixture := range []struct{ home, provider string }{{ambient, "ambient"}, {explicit, "explicit"}} {
		if err := os.WriteFile(filepath.Join(fixture.home, "provider"), []byte(fixture.provider), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	pi := writeCatalogPi(t, t.TempDir(), `provider=$(/bin/cat "$PI_CODING_AGENT_DIR/provider")
case "$1" in
  --version) echo 0.85.1 ;;
  --list-models) echo 'provider model context max-out thinking images'; echo "$provider current 128K 32K yes no" ;;
  auth) [ "$4" = "$provider" ] && echo ready || echo not_ready ;;
  *) exit 1 ;;
esac
`)
	t.Setenv("PI_CODING_AGENT_DIR", ambient)
	for _, fixture := range []struct{ home, provider string }{{"", "ambient"}, {explicit, "explicit"}} {
		models, err := DiscoverModelsForRuntime(context.Background(), pi, fixture.home)
		if err != nil || len(models) != 1 || models[0].Provider != fixture.provider {
			t.Fatalf("catalog home %q: models=%#v err=%v", fixture.home, models, err)
		}
		result, err := Discover(context.Background(), Options{LookPath: fixedPath(pi), PiHome: fixture.home})
		if err != nil || result.State != StateReady || len(result.ReadyProviders) != 1 || result.ReadyProviders[0] != fixture.provider {
			t.Fatalf("readiness home %q: result=%#v err=%v", fixture.home, result, err)
		}
	}
}
