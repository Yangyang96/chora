package localweb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/pidiscovery"
	"github.com/Yangyang96/chora/internal/piinstall"
)

type fakePiInstaller struct {
	mu         sync.Mutex
	state      piinstall.State
	selection  piinstall.Selection
	inspectErr error
	installErr error
	cancelErr  error
	installs   []piinstall.InstallRequest
	cancels    []string
}

func (f *fakePiInstaller) Inspect(context.Context) (piinstall.State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state, f.inspectErr
}
func (f *fakePiInstaller) ResolveSelection(context.Context) (piinstall.Selection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.selection.ExecutablePath == "" {
		return piinstall.Selection{}, errors.New("selection missing")
	}
	return f.selection, nil
}
func (f *fakePiInstaller) Install(_ context.Context, request piinstall.InstallRequest) (piinstall.Outcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.installs = append(f.installs, request)
	return piinstall.Outcome{State: f.state, Selection: f.selection, RestartRequired: true}, f.installErr
}
func (f *fakePiInstaller) Cancel(_ context.Context, operationID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancels = append(f.cancels, operationID)
	return f.cancelErr
}

func piInstallationState(code piinstall.StateCode) piinstall.State {
	return piinstall.State{Code: code, StateVersion: 7, DestinationPath: "/private/chora/pi", Version: "0.85.1", Source: "chora", UpdatedAt: time.Unix(1, 0).UTC()}
}

func piInstallationRequest(method, path, body, key string) *http.Request {
	request := newLocalRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	return request
}

func decodePiInstallationView(t *testing.T, recorder *httptest.ResponseRecorder) piInstallationView {
	t.Helper()
	var view piInstallationView
	if err := json.Unmarshal(recorder.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	return view
}

func TestGetPiInstallationIsReadOnlyAndUnavailableWithoutInstaller(t *testing.T) {
	fake := &fakePiInstaller{state: piInstallationState(piinstall.StateMissing)}
	server := &Server{pathPiEnabled: true, piInstaller: fake}
	recorder := httptest.NewRecorder()
	server.getPiInstallation(recorder, piInstallationRequest(http.MethodGet, "/api/pi/installation", "", ""))
	if recorder.Code != http.StatusOK || decodePiInstallationView(t, recorder).State.Code != piinstall.StateMissing || len(fake.installs) != 0 {
		t.Fatalf("response=%d installs=%d", recorder.Code, len(fake.installs))
	}

	unavailable := &Server{}
	recorder = httptest.NewRecorder()
	unavailable.getPiInstallation(recorder, piInstallationRequest(http.MethodGet, "/api/pi/installation", "", ""))
	view := decodePiInstallationView(t, recorder)
	if view.Available || view.State != nil {
		t.Fatalf("view=%#v", view)
	}
}

func TestInstallPiPassesOnlyFrozenVersionAndIdempotency(t *testing.T) {
	state := piInstallationState(piinstall.StateInstalled)
	state.SelectionPresent = true
	fake := &fakePiInstaller{state: state, selection: piinstall.Selection{ExecutablePath: "/managed/pi"}}
	server := &Server{ctx: context.Background(), product: true, pathPiEnabled: true, piInstaller: fake}
	recorder := httptest.NewRecorder()
	server.installPi(recorder, piInstallationRequest(http.MethodPost, "/api/pi/installation", `{"expectedStateVersion":7}`, "install-key"))
	if recorder.Code != http.StatusOK || len(fake.installs) != 1 || fake.installs[0].ExpectedStateVersion != 7 || fake.installs[0].IdempotencyKey != "install-key" {
		t.Fatalf("status=%d installs=%#v", recorder.Code, fake.installs)
	}
}

func TestCancelPiInstallationPassesExactOperation(t *testing.T) {
	fake := &fakePiInstaller{state: piInstallationState(piinstall.StateInstalling)}
	server := &Server{ctx: context.Background(), product: true, pathPiEnabled: true, piInstaller: fake}
	recorder := httptest.NewRecorder()
	server.cancelPiInstallation(recorder, piInstallationRequest(http.MethodPost, "/api/pi/installation/cancel", `{"operationId":"operation_12345678"}`, "cancel-key"))
	if recorder.Code != http.StatusOK || len(fake.cancels) != 1 || fake.cancels[0] != "operation_12345678" {
		t.Fatalf("status=%d cancels=%v", recorder.Code, fake.cancels)
	}
}

func TestPiInstallationCommandsRejectUnknownFieldsAndMissingProtection(t *testing.T) {
	fake := &fakePiInstaller{state: piInstallationState(piinstall.StateMissing)}
	server := &Server{ctx: context.Background(), product: true, pathPiEnabled: true, piInstaller: fake}
	for _, test := range []struct{ body, key string }{{`{"expectedStateVersion":7,"url":"https://evil.invalid"}`, "key"}, {`{}`, "key"}, {`{"expectedStateVersion":7}`, ""}} {
		recorder := httptest.NewRecorder()
		server.installPi(recorder, piInstallationRequest(http.MethodPost, "/api/pi/installation", test.body, test.key))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("body=%s status=%d", test.body, recorder.Code)
		}
	}
	if len(fake.installs) != 0 {
		t.Fatalf("unexpected installs=%d", len(fake.installs))
	}
}

func TestPiInstallationSeparatesRestartReadinessAndDrift(t *testing.T) {
	root := t.TempDir()
	piPath := writeFakePathPi(t, root, "0.85.1", map[string]string{"ollama": "ready"})
	piPath, err := filepath.EvalSymlinks(piPath)
	if err != nil {
		t.Fatal(err)
	}
	home := writeFakePathPiHome(t, root, "ollama", nil)
	contents, err := os.ReadFile(piPath)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(contents)
	selection := piinstall.Selection{SchemaVersion: 1, ContractDigest: "contract", LockSHA256: "lock", ClosureDigest: "closure", ContentID: "contract", ExecutableRelative: "consumer/node_modules/pi/dist/bundle/cli.js", ExecutablePath: piPath, ExecutableSHA256: hex.EncodeToString(digest[:]), PiVersion: "0.85.1", NodeVersion: "22.19.0", NPMVersion: "11.0.0", OperationID: "operation_12345678", CreatedAt: time.Unix(1, 0).UTC()}
	state := piInstallationState(piinstall.StateInstalled)
	state.SelectionPresent = true
	fake := &fakePiInstaller{state: state, selection: selection}
	server := &Server{pathPiEnabled: true, piInstaller: fake, piInstalledSelection: selection, piStatus: piRuntimeStatus{Enabled: true}, piDiscoveryOptions: pidiscovery.Options{LookPath: fixedPathPi(piPath), PiHome: home}}
	view, err := server.inspectPiInstallation(context.Background())
	if err != nil || !view.Active || view.RestartRequired || !view.State.Configured {
		t.Fatalf("ready view=%#v err=%v", view, err)
	}

	server.piDiscoveryOptions = pidiscovery.Options{LookPath: fixedPathPi(piPath), PiHome: t.TempDir()}
	view, err = server.inspectPiInstallation(context.Background())
	if err != nil || view.Active || view.RestartRequired || view.State.Configured || view.State.ConfigurationAction != piinstall.ConfigurationCommand(selection.ExecutablePath) {
		t.Fatalf("unconfigured composed view=%#v err=%v", view, err)
	}
	server.piDiscoveryOptions = pidiscovery.Options{LookPath: fixedPathPi(piPath), PiHome: home}

	server.piInstalledSelection = piinstall.Selection{}
	view, err = server.inspectPiInstallation(context.Background())
	if err != nil || view.Active || !view.RestartRequired {
		t.Fatalf("restart view=%#v err=%v", view, err)
	}

	fake.selection = piinstall.Selection{}
	view, err = server.inspectPiInstallation(context.Background())
	if err != nil || view.State.Code != piinstall.StateDrifted || view.Active || view.RestartRequired {
		t.Fatalf("drift view=%#v err=%v", view, err)
	}
}

func TestPiInstallationErrorIsStableAndDoesNotExposeSecrets(t *testing.T) {
	fake := &fakePiInstaller{state: piInstallationState(piinstall.StateMissing), installErr: &piinstall.Error{Reason: piinstall.ReasonDownload, Err: errors.New("TOKEN secret /Users/alice/.npmrc")}}
	server := &Server{ctx: context.Background(), product: true, pathPiEnabled: true, piInstaller: fake}
	recorder := httptest.NewRecorder()
	server.installPi(recorder, piInstallationRequest(http.MethodPost, "/api/pi/installation", `{"expectedStateVersion":7}`, "install-key"))
	if recorder.Code != http.StatusInternalServerError || recorder.Body.String() != "{\"error\":\"download_failed\"}\n" || strings.Contains(recorder.Body.String(), "TOKEN") || strings.Contains(recorder.Body.String(), "/Users/") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestPreparePiInstallationAllowsTrueAbsenceButRejectsClaimedMissingSelection(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	original := pidiscovery.Options{LookPath: func(string) (string, error) { return "original", nil }, PiHome: "home"}
	installer, selection, returned, err := preparePiInstallation(ctx, root, original)
	if err != nil || installer == nil || selection != (piinstall.Selection{}) || returned.PiHome != original.PiHome {
		t.Fatalf("missing selection installer=%T selection=%#v err=%v", installer, selection, err)
	}
	if got, _ := returned.LookPath("pi"); got != "original" {
		t.Fatalf("original discovery changed: %q", got)
	}

	piRoot := filepath.Join(root, "pi")
	if err = os.MkdirAll(piRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	claimed := piInstallationState(piinstall.StateInstalled)
	claimed.SelectionPresent = true
	data, err := json.Marshal(claimed)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(piRoot, "state.json"), append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = preparePiInstallation(ctx, root, original); err == nil {
		t.Fatal("claimed installed state fell back without its selection")
	}

	malformedRoot := t.TempDir()
	malformedPiRoot := filepath.Join(malformedRoot, "pi")
	if err = os.MkdirAll(malformedPiRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(malformedPiRoot, "selection.json"), []byte("not-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = preparePiInstallation(ctx, malformedRoot, original); err == nil {
		t.Fatal("malformed selection with no state fell back to PATH")
	}
}

func TestServerLinkedContextStopsWithServerLifetime(t *testing.T) {
	lifetime, stop := context.WithCancel(context.Background())
	ctx, cancel := serverLinkedContext(context.Background(), lifetime)
	defer cancel()
	stop()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("linked context remained active")
	}
}
