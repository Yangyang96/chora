package localweb

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/gitsource"
	"github.com/Yangyang96/chora/internal/piinstall"
)

// This network-bearing qualification is separate from credential-free CI. An
// explicit invocation fails on absent prerequisites; it never substitutes a mock.
func TestPiInstallationRealBrowser(t *testing.T) {
	if os.Getenv("CHORA_PI_INSTALL_REAL") != "1" {
		t.Skip("explicit upstream installation qualification")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	data := os.Getenv("CHORA_PI_INSTALL_ROOT")
	if data == "" {
		data, err = os.MkdirTemp("", "chora-real-pi-install-")
	}
	if err != nil {
		t.Fatal(err)
	}
	data, err = filepath.EvalSymlinks(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("retained installation: %s", data)
	home := filepath.Join(data, "empty-pi-home")
	if err = os.Mkdir(home, 0700); err != nil && !os.IsExist(err) {
		t.Fatal(err)
	}
	options := ProductOptions{RepositorySource: gitsource.Default{}, DataRoot: data, PathPiEnabled: true, PathPiSessionRoot: filepath.Join(data, "pi-sessions"), PathPiHome: home, PathPiLookPath: func(string) (string, error) { return "", os.ErrNotExist }}
	server, err := newServer(context.Background(), filepath.Join(data, "chora.db"), filepath.Join(root, "web", "dist"), log.New(io.Discard, "", 0), options, false)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	command := exec.Command("node", filepath.Join(root, "e2e", "pi-installation-browser.mjs"), httpServer.URL, data)
	command.Dir = root
	output, runErr := command.CombinedOutput()
	httpServer.Close()
	server.Close()
	if runErr != nil || !strings.Contains(string(output), "PI_INSTALLATION_BROWSER_PASS") {
		t.Fatalf("browser: %v\n%s", runErr, output)
	}
	// Reopen the same database with deliberately missing PATH and the same empty
	// Pi home: selected bytes must resolve, yet never claim configured or active.
	server, err = newServer(context.Background(), filepath.Join(data, "chora.db"), filepath.Join(root, "web", "dist"), log.New(io.Discard, "", 0), options, false)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	view, err := server.inspectPiInstallation(context.Background())
	if err != nil || view.State == nil || view.State.Code != piinstall.StateInstalled || view.State.Configured || view.Active {
		t.Fatalf("restarted state=%#v err=%v", view, err)
	}
	selected, err := server.piInstaller.ResolveSelection(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if selected.PiVersion != "0.85.1" || server.piInstalledSelection != selected {
		t.Fatal("selected installation not composed after restart")
	}
	// Exact runtime guard rejects changed selection even if an old PATH is ready.
	guard := piInstallationGuard{installer: server.piInstaller, selection: selected}
	if _, err = guard.validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	selectionFile := filepath.Join(data, "pi", "selection.json")
	bytes, err := os.ReadFile(selectionFile)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(selectionFile, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, guardErr := guard.validate(context.Background())
	if restoreErr := os.WriteFile(selectionFile, bytes, 0600); restoreErr != nil {
		t.Fatal(restoreErr)
	}
	if guardErr == nil {
		t.Fatal("changed installation was accepted")
	}
	if _, err = guard.validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(home, "auth.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("installation wrote native credentials")
	}
	report, _ := json.MarshalIndent(map[string]any{"selection": selected, "restart": view, "source": "real fixed upstream npm installation through browser", "nativeCredentialsCreated": false, "driftRefused": true}, "", "  ")
	if err = os.WriteFile(filepath.Join(data, "qualification.json"), append(report, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("PI_INSTALLATION_REAL_PASS: installation, missing PATH restart, unconfigured refusal, selected identity drift, native credentials unchanged")
}
