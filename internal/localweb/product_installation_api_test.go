package localweb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Yangyang96/chora/internal/preflight"
	"github.com/Yangyang96/chora/internal/productinstall"
)

type fakeProductInstallationController struct {
	mu               sync.Mutex
	doctorCalls      int
	mutationRequests []ProductInstallationMutationRequest
	doctorView       ProductInstallationView
	doctorErr        error
	mutate           func(ProductInstallationMutationRequest) (ProductInstallationView, error)
}

func (controller *fakeProductInstallationController) Doctor(context.Context) (ProductInstallationView, error) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	controller.doctorCalls++
	return controller.doctorView, controller.doctorErr
}

func (controller *fakeProductInstallationController) Mutate(_ context.Context, request ProductInstallationMutationRequest) (ProductInstallationView, error) {
	controller.mu.Lock()
	controller.mutationRequests = append(controller.mutationRequests, request)
	mutate := controller.mutate
	controller.mu.Unlock()
	if mutate == nil {
		return validProductInstallationTestView(), nil
	}
	return mutate(request)
}

func (controller *fakeProductInstallationController) counts() (int, int) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return controller.doctorCalls, len(controller.mutationRequests)
}

func (controller *fakeProductInstallationController) requests() []ProductInstallationMutationRequest {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return append([]ProductInstallationMutationRequest(nil), controller.mutationRequests...)
}

func validProductInstallationTestView() ProductInstallationView {
	return ProductInstallationView{
		SchemaVersion: ProductInstallationSchemaVersion,
		Status:        ProductInstallationStatusReady,
		Engine: ProductInstallationEngineView{
			Ready: true, APIVersion: "1.47", OperatingSystem: "linux", Architecture: "arm64", ContextName: "chora",
		},
		ActiveGenerationID: "generation-1",
		Actions: ProductInstallationActionsView{
			Upgrade: true, GC: true, Uninstall: true,
		},
	}
}

func productInstallationTestServer(t *testing.T, controller ProductInstallationController) *Server {
	t.Helper()
	server := newReadinessTestServer(t)
	server.product = true
	server.installationController = controller
	return server
}

func TestProductInstallationDoctorIsReadOnlyAndPublic(t *testing.T) {
	view := validProductInstallationTestView()
	controller := &fakeProductInstallationController{doctorView: view}
	server := productInstallationTestServer(t, controller)

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, newLocalRequest(http.MethodGet, "/api/product-installation/doctor", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("doctor status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if doctorCalls, mutationCalls := controller.counts(); doctorCalls != 1 || mutationCalls != 0 {
		t.Fatalf("doctor calls=%d mutation calls=%d", doctorCalls, mutationCalls)
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	engine, ok := body["engine"].(map[string]any)
	if !ok || engine["ready"] != true || engine["operatingSystem"] != "linux" || engine["apiVersion"] != "1.47" {
		t.Fatalf("public Engine view=%#v", body["engine"])
	}
	actions, ok := body["actions"].(map[string]any)
	if !ok || len(actions) != 4 || actions["setup"] != false || actions["upgrade"] != true || actions["gc"] != true || actions["uninstall"] != true {
		t.Fatalf("public actions=%#v", body["actions"])
	}
	for _, forbidden := range []string{"endpoint", "daemon", "path", "digest", "credential", "grant"} {
		if strings.Contains(strings.ToLower(recorder.Body.String()), forbidden) {
			t.Fatalf("doctor leaked forbidden field %q: %s", forbidden, recorder.Body.String())
		}
	}
}

func TestProductInstallationRoutesAreProductOnlyAndControllerRequired(t *testing.T) {
	legacy := newReadinessTestServer(t)
	for _, path := range []string{"/api/product-installation/doctor", "/api/product-installation/setup"} {
		method := http.MethodGet
		if strings.HasSuffix(path, "/setup") {
			method = http.MethodPost
		}
		requestJSON(t, legacy.Handler(), method, path, map[string]any{}, http.StatusNotFound, nil)
	}
	product := productInstallationTestServer(t, nil)
	requestJSON(t, product.Handler(), http.MethodGet, "/api/product-installation/doctor", nil, http.StatusServiceUnavailable, nil)
}

func TestInstallationOnlyProductStartsWithoutPublishingInferredReferences(t *testing.T) {
	root := t.TempDir()
	webRoot := filepath.Join(root, "web")
	repositoryRoot := filepath.Join(root, "installed-source")
	if err := os.MkdirAll(webRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(webRoot, "index.html"), []byte("<!doctype html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(repositoryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	stateRoot := filepath.Join(root, "installation-state")
	controller := &fakeProductInstallationController{doctorView: validProductInstallationTestView()}
	server, err := NewProduct(context.Background(), filepath.Join(root, "data", "chora.db"), webRoot, log.New(io.Discard, "", 0), ProductOptions{
		RepoRoot: repositoryRoot, RepositoryRoot: repositoryRoot, PiAuthFile: filepath.Join(root, "missing-auth.json"),
		InstallationStateRoot: stateRoot, InstallationController: controller,
		PiPreflight: func(context.Context) preflight.Report { return preflight.Report{Status: preflight.StatusFailed} },
	})
	if err != nil {
		t.Fatalf("installation-only product failed to start: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	if _, err := os.Stat(stateRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("installation-only startup touched reference state root: %v", err)
	}
	requestJSON(t, server.Handler(), http.MethodGet, "/api/product-installation/doctor", nil, http.StatusOK, nil)
	if server.requireManagedGenerationConfiguration() == nil {
		t.Fatal("installation-only product unexpectedly enabled managed execution")
	}
}

func TestProductInstallationMutationRejectsInvalidProtocolBeforeController(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		body    map[string]any
		headers map[string]string
	}{
		{name: "missing key", path: "setup", body: map[string]any{"confirmation": "setup", "confirmPinnedEngineTools": true, "confirmColimaVM": true}},
		{name: "wrong confirmation", path: "upgrade", body: map[string]any{"confirmation": "setup"}, headers: map[string]string{"Idempotency-Key": "upgrade-1"}},
		{name: "non-exact confirmation", path: "upgrade", body: map[string]any{"confirmation": " upgrade "}, headers: map[string]string{"Idempotency-Key": "upgrade-1"}},
		{name: "uppercase key", path: "upgrade", body: map[string]any{"confirmation": "upgrade"}, headers: map[string]string{"Idempotency-Key": "Upgrade-1"}},
		{name: "overlong key", path: "upgrade", body: map[string]any{"confirmation": "upgrade"}, headers: map[string]string{"Idempotency-Key": "u" + strings.Repeat("p", 128)}},
		{name: "missing setup confirmation", path: "setup", body: map[string]any{"confirmation": "setup", "confirmPinnedEngineTools": true}, headers: map[string]string{"Idempotency-Key": "setup-1"}},
		{name: "gc limit zero", path: "gc", body: map[string]any{"confirmation": "gc", "limit": 0}, headers: map[string]string{"Idempotency-Key": "gc-1"}},
		{name: "gc limit unbounded", path: "gc", body: map[string]any{"confirmation": "gc", "limit": 65}, headers: map[string]string{"Idempotency-Key": "gc-2"}},
		{name: "forbidden path", path: "uninstall", body: map[string]any{"confirmation": "uninstall", "path": "/private/secret"}, headers: map[string]string{"Idempotency-Key": "uninstall-1"}},
		{name: "forbidden command", path: "upgrade", body: map[string]any{"confirmation": "upgrade", "command": "docker pull secret"}, headers: map[string]string{"Idempotency-Key": "upgrade-2"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			controller := &fakeProductInstallationController{}
			server := productInstallationTestServer(t, controller)
			requestJSONWithHeaders(t, server.Handler(), http.MethodPost, "/api/product-installation/"+test.path, test.body, test.headers, http.StatusBadRequest, nil)
			if _, calls := controller.counts(); calls != 0 {
				t.Fatalf("controller mutation calls=%d", calls)
			}
		})
	}
}

func TestProductInstallationMutationRejectsTrailingJSONBeforeController(t *testing.T) {
	controller := &fakeProductInstallationController{}
	server := productInstallationTestServer(t, controller)
	request := newLocalRequest(http.MethodPost, "/api/product-installation/upgrade", strings.NewReader(`{"confirmation":"upgrade"}{"confirmation":"upgrade"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "upgrade-1")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	if _, calls := controller.counts(); calls != 0 {
		t.Fatalf("controller mutation calls=%d", calls)
	}
}

func TestProductInstallationMutationDelegatesExactActions(t *testing.T) {
	tests := []struct {
		action ProductInstallationAction
		body   map[string]any
		want   ProductInstallationMutationRequest
	}{
		{ProductInstallationActionSetup, map[string]any{"confirmation": "setup", "confirmPinnedEngineTools": true, "confirmColimaVM": true}, ProductInstallationMutationRequest{ConfirmPinnedEngineTools: true, ConfirmColimaVM: true}},
		{ProductInstallationActionUpgrade, map[string]any{"confirmation": "upgrade"}, ProductInstallationMutationRequest{}},
		{ProductInstallationActionGC, map[string]any{"confirmation": "gc", "limit": 64}, ProductInstallationMutationRequest{Limit: 64}},
		{ProductInstallationActionUninstall, map[string]any{"confirmation": "uninstall"}, ProductInstallationMutationRequest{}},
	}
	for _, test := range tests {
		t.Run(string(test.action), func(t *testing.T) {
			controller := &fakeProductInstallationController{}
			server := productInstallationTestServer(t, controller)
			key := string(test.action) + "-key"
			var got ProductInstallationView
			requestJSONWithHeaders(t, server.Handler(), http.MethodPost, "/api/product-installation/"+string(test.action), test.body, map[string]string{"Idempotency-Key": key}, http.StatusOK, &got)
			requests := controller.requests()
			if len(requests) != 1 {
				t.Fatalf("requests=%#v", requests)
			}
			want := test.want
			want.ActorID, want.SessionID, want.IdempotencyKey, want.Action = localActor, localSession, key, test.action
			if requests[0] != want {
				t.Fatalf("request=%#v want=%#v", requests[0], want)
			}
		})
	}
}

func TestProductInstallationMutationPreservesControllerReplay(t *testing.T) {
	seen := map[string]bool{}
	controller := &fakeProductInstallationController{}
	controller.mutate = func(request ProductInstallationMutationRequest) (ProductInstallationView, error) {
		view := validProductInstallationTestView()
		view.Replayed = seen[request.IdempotencyKey]
		seen[request.IdempotencyKey] = true
		return view, nil
	}
	server := productInstallationTestServer(t, controller)
	for index, wantReplay := range []bool{false, true} {
		var got ProductInstallationView
		requestJSONWithHeaders(t, server.Handler(), http.MethodPost, "/api/product-installation/upgrade", map[string]any{"confirmation": "upgrade"}, map[string]string{"Idempotency-Key": "same-upgrade"}, http.StatusOK, &got)
		if got.Replayed != wantReplay {
			t.Fatalf("response %d replayed=%v want=%v", index, got.Replayed, wantReplay)
		}
	}
}

func TestProductInstallationErrorsAndInvalidViewsAreSecretSafe(t *testing.T) {
	secret := "/private/credentials sk-super-secret"
	tests := []struct {
		name   string
		err    error
		view   ProductInstallationView
		status int
	}{
		{name: "conflict", err: fmt.Errorf("%w: %s", productinstall.ErrBusy, secret), status: http.StatusConflict},
		{name: "idempotency conflict", err: fmt.Errorf("%w: %s", productinstall.ErrIdempotencyConflict, secret), status: http.StatusConflict},
		{name: "authority", err: fmt.Errorf("%w: %s", productinstall.ErrAuthorityRequired, secret), status: http.StatusForbidden},
		{name: "invalid", err: fmt.Errorf("%w: %s", productinstall.ErrInvalidRequest, secret), status: http.StatusUnprocessableEntity},
		{name: "operation", err: fmt.Errorf("%w: %s", productinstall.ErrOperationFailed, secret), status: http.StatusServiceUnavailable},
		{name: "hostile view", view: ProductInstallationView{SchemaVersion: ProductInstallationSchemaVersion, Status: ProductInstallationStatusReady, ActiveGenerationID: secret}, status: http.StatusServiceUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			controller := &fakeProductInstallationController{}
			controller.mutate = func(ProductInstallationMutationRequest) (ProductInstallationView, error) { return test.view, test.err }
			server := productInstallationTestServer(t, controller)
			recorder := httptest.NewRecorder()
			request := newLocalRequest(http.MethodPost, "/api/product-installation/upgrade", strings.NewReader(`{"confirmation":"upgrade"}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Idempotency-Key", "upgrade-safe")
			server.Handler().ServeHTTP(recorder, request)
			if recorder.Code != test.status || strings.Contains(recorder.Body.String(), secret) || strings.Contains(recorder.Body.String(), "sk-super-secret") {
				t.Fatalf("status=%d body=%q", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestProductInstallationConcurrentMutationIsDelegatedAndFailsClosed(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	controller := &fakeProductInstallationController{}
	controller.mutate = func(ProductInstallationMutationRequest) (ProductInstallationView, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
			return validProductInstallationTestView(), nil
		}
		return ProductInstallationView{}, errors.Join(productinstall.ErrBusy, errors.New("private lock detail"))
	}
	server := productInstallationTestServer(t, controller)
	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		firstDone <- productInstallationRawMutation(server.Handler(), "first")
	}()
	<-started
	second := productInstallationRawMutation(server.Handler(), "second")
	if second.Code != http.StatusConflict || strings.Contains(second.Body.String(), "private lock detail") {
		t.Fatalf("second status=%d body=%q", second.Code, second.Body.String())
	}
	close(release)
	if first := <-firstDone; first.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%q", first.Code, first.Body.String())
	}
}

func productInstallationRawMutation(handler http.Handler, key string) *httptest.ResponseRecorder {
	request := newLocalRequest(http.MethodPost, "/api/product-installation/upgrade", strings.NewReader(`{"confirmation":"upgrade"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", key)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}
