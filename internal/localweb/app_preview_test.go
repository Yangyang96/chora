package localweb

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/apppreview"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/isolatedenv"
)

const previewTestScript = `const http = require('node:http');
const fs = require('node:fs');
http.createServer((req,res) => { res.end(fs.readFileSync('same.txt')); })
 .listen(Number(process.env.PORT), '127.0.0.1', () => console.log('preview fixture ready'));
`

func newAppPreviewFixture(t *testing.T) (resourceTerminalScenarioResult, string) {
	t.Helper()
	var taskRoot string
	f := runResourceTerminalScenario(t, resourceTerminalScenario{
		name: "optional app preview", futureDelivery: true, repositoryCount: 2, referenceLast: true, checkMode: "none", completeAssistant: true,
		wantRunState: domain.RunStateAwaitingReview,
		change: func(t *testing.T, root string, resources []domain.TaskRepositoryResource) {
			taskRoot = root
			for _, r := range resources {
				if r.Role != "write" {
					continue
				}
				writeResourceTerminalFile(t, filepath.Join(root, r.WorkspaceDirectory()), "same.txt", []byte("Task preview content\n"))
				writeResourceTerminalFile(t, filepath.Join(root, r.WorkspaceDirectory()), "preview.cjs", []byte(previewTestScript))
				writeResourceTerminalFile(t, filepath.Join(root, r.WorkspaceDirectory()), "package.json", []byte(`{"scripts":{"dev":"node preview.cjs"}}`))
			}
		},
	})
	dataRoot := filepath.Dir(filepath.Dir(taskRoot))
	resolver, err := newTaskResourceWorkspaceManager(f.server.store, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	f.server.appPreviewWorkspaces = resolver.(*taskResourceWorkspaceManager)
	f.server.runtimeRoot = filepath.Join(dataRoot, "runtime")
	t.Cleanup(func() {
		if f.server.appPreviews != nil {
			_ = f.server.appPreviews.StopAll(context.Background())
		}
	})
	return f, taskRoot
}

func appPreviewTestPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

func waitAppPreviewHTTP(t *testing.T, url, want string) {
	t.Helper()
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		response, err := client.Get(url)
		if err == nil {
			body, _ := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if string(body) == want {
				return
			}
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("app did not serve expected Task content at %s", url)
}

func TestAppPreviewOptionalLifecyclePreservesReviewAndOriginal(t *testing.T) {
	f, _ := newAppPreviewFixture(t)
	h := f.server.Handler()
	prefix := "/api/v2/runs/" + f.run.ID + "/app-preview"
	var view appPreviewView
	requestJSON(t, h, "GET", prefix, nil, 200, &view)
	if !view.Available || view.Preview.State != "idle" || len(view.Repositories) != 1 || len(view.Suggestions) != 1 {
		t.Fatalf("unexpected initial preview: %+v", view)
	}
	config := apppreview.Config{Command: "node preview.cjs", WorkingDirectory: ".", Port: appPreviewTestPort(t)}
	body := map[string]any{"repoId": view.RepoID, "expectedVersion": f.run.Version, "config": config}
	requestJSON(t, h, "POST", prefix+"/save", body, 200, &view)
	if view.Preview.State != "idle" {
		t.Fatal("saving config launched application")
	}
	requestJSON(t, h, "POST", prefix+"/start", body, 200, &view)
	if view.Preview.State != "running" {
		t.Fatalf("start=%+v", view)
	}
	url := view.Preview.URL
	waitAppPreviewHTTP(t, url, "Task preview content\n")
	requestJSON(t, h, "GET", prefix, nil, 200, &view)
	if !strings.Contains(view.Preview.Logs, "preview fixture ready") {
		t.Fatalf("missing logs: %+v", view)
	}
	// A lost POST response cannot launch a duplicate process.
	requestJSON(t, h, "POST", prefix+"/start", body, 409, nil)
	var current runView
	requestJSON(t, h, "GET", "/api/runs/"+f.run.ID, nil, 200, &current)
	if current.Version != f.run.Version || current.Status != f.run.Status || current.ResourceResult.Digest != f.run.ResourceResult.Digest {
		t.Fatal("preview changed code acceptance evidence")
	}
	// Archival removes start authority but must retain stop/cleanup controls.
	var closed app.ResultClosureView
	requestJSONWithHeaders(t, h, "POST", "/api/v2/runs/"+f.run.ID+"/closure/preview", map[string]any{"expectedVersion": f.run.Version}, map[string]string{"Idempotency-Key": "archive-preview-close"}, 200, &closed)
	requestJSONWithHeaders(t, h, "POST", "/api/v2/runs/"+f.run.ID+"/closure/confirm", map[string]any{"expectedVersion": f.run.Version, "resultDigest": closed.ResultDigest, "previewDigest": closed.PreviewDigest}, map[string]string{"Idempotency-Key": "archive-confirm-close"}, 200, &closed)
	requestJSONWithHeaders(t, h, "POST", "/api/tasks/"+f.run.Task.ID+"/archive", nil, map[string]string{"Idempotency-Key": "archive-preview-task"}, 200, nil)
	requestJSON(t, h, "GET", prefix, nil, 200, &view)
	if view.Available || view.Preview.State != "running" {
		t.Fatalf("archived preview=%+v", view)
	}
	// Stop remains available from a stale page and releases the process/port.
	body["expectedVersion"] = 0
	requestJSON(t, h, "POST", prefix+"/stop", body, 200, &view)
	if view.Preview.State != "stopped" {
		t.Fatalf("stop=%+v", view)
	}
	response, err := (&http.Client{Timeout: time.Second}).Get(url)
	if err == nil {
		_ = response.Body.Close()
		t.Fatal("stopped app remains reachable")
	}
	requestJSON(t, h, "POST", prefix+"/cleanup", body, 200, &view)
	if view.Preview.Config != config {
		t.Fatal("cleanup erased saved command")
	}
	for repoID, root := range f.originals {
		data, err := os.ReadFile(filepath.Join(root, "same.txt"))
		if err != nil || string(data) != "original "+repoID+"\n" {
			t.Fatal("preview changed original repository")
		}
	}
}

func TestAppPreviewRejectsStaleConfigAndUnsafeDirectory(t *testing.T) {
	f, _ := newAppPreviewFixture(t)
	h := f.server.Handler()
	prefix := "/api/v2/runs/" + f.run.ID + "/app-preview"
	var view appPreviewView
	requestJSON(t, h, "GET", prefix, nil, 200, &view)
	body := map[string]any{"repoId": view.RepoID, "expectedVersion": f.run.Version - 1, "config": apppreview.Config{Command: "node preview.cjs", WorkingDirectory: ".", Port: appPreviewTestPort(t)}}
	requestJSON(t, h, "POST", prefix+"/start", body, 409, nil)
	body["expectedVersion"] = f.run.Version
	body["config"] = apppreview.Config{Command: "node preview.cjs", WorkingDirectory: "../..", Port: appPreviewTestPort(t)}
	requestJSON(t, h, "POST", prefix+"/save", body, 409, nil)
	body["repoId"] = domain.NewRepositoryID().String()
	requestJSON(t, h, "POST", prefix+"/start", body, 400, nil)
	requestJSON(t, h, "GET", prefix, nil, 200, &view)
	if view.Preview.State != "idle" {
		t.Fatal("rejected request changed preview state")
	}
}

func TestAppPreviewServerCloseStopsApplication(t *testing.T) {
	f, _ := newAppPreviewFixture(t)
	h := f.server.Handler()
	prefix := "/api/v2/runs/" + f.run.ID + "/app-preview"
	var view appPreviewView
	requestJSON(t, h, "GET", prefix, nil, 200, &view)
	requestJSON(t, h, "POST", prefix+"/start", map[string]any{"repoId": view.RepoID, "expectedVersion": f.run.Version, "config": apppreview.Config{Command: "node preview.cjs", WorkingDirectory: ".", Port: appPreviewTestPort(t)}}, 200, &view)
	waitAppPreviewHTTP(t, view.Preview.URL, "Task preview content\n")
	// A restarted Workbench has durable records but has not opened preview UI.
	f.server.appPreviews = nil
	if err := f.server.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.server.Close(); err != nil {
		t.Fatal(err)
	}
	response, err := (&http.Client{Timeout: time.Second}).Get(view.Preview.URL)
	if err == nil {
		_ = response.Body.Close()
		t.Fatal("application survived normal server close")
	}
}

func TestAppPreviewBrowser(t *testing.T) {
	if os.Getenv("CHORA_APP_PREVIEW_BROWSER") != "1" {
		t.Skip("set CHORA_APP_PREVIEW_BROWSER=1 with CHORA_PREVIEW_TEST_WEB built assets")
	}
	f, _ := newAppPreviewFixture(t)
	f.server.webRoot = os.Getenv("CHORA_PREVIEW_TEST_WEB")
	if _, err := os.Stat(filepath.Join(f.server.webRoot, "index.html")); err != nil {
		t.Fatal(err)
	}
	h := httptest.NewServer(f.server.Handler())
	defer h.Close()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	route := "/rooms/" + f.run.Room.ID + "/tasks/" + f.run.Task.ID + "/runs/" + f.run.ID
	cmd := exec.Command("node", filepath.Join(root, "e2e", "app-preview-browser.mjs"), h.URL, route, fmt.Sprint(appPreviewTestPort(t)))
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("app preview browser: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "APP_PREVIEW_BROWSER_PASS") {
		t.Fatalf("missing browser witness: %s", output)
	}
}

func TestAppPreviewWorktreeCleanupStopsAppFirst(t *testing.T) {
	f, taskRoot := newAppPreviewFixture(t)
	h := f.server.Handler()
	prefix := "/api/v2/runs/" + f.run.ID
	var view appPreviewView
	requestJSON(t, h, "GET", prefix+"/app-preview", nil, 200, &view)
	repoID := view.RepoID
	requestJSON(t, h, "POST", prefix+"/app-preview/start", map[string]any{"repoId": repoID, "expectedVersion": f.run.Version, "config": apppreview.Config{Command: "node preview.cjs", WorkingDirectory: ".", Port: appPreviewTestPort(t)}}, 200, &view)
	waitAppPreviewHTTP(t, view.Preview.URL, "Task preview content\n")
	var closure app.ResultClosureView
	requestJSONWithHeaders(t, h, "POST", prefix+"/closure/preview", map[string]any{"expectedVersion": f.run.Version}, map[string]string{"Idempotency-Key": "preview-close"}, 200, &closure)
	requestJSONWithHeaders(t, h, "POST", prefix+"/closure/confirm", map[string]any{"expectedVersion": f.run.Version, "resultDigest": closure.ResultDigest, "previewDigest": closure.PreviewDigest}, map[string]string{"Idempotency-Key": "confirm-close"}, 200, &closure)
	var cleanup app.DeliveryOperationView
	body := map[string]any{"repoId": repoID, "expectedVersion": f.run.Version, "resultDigest": closure.ResultDigest}
	requestJSONWithHeaders(t, h, "POST", prefix+"/closure/cleanup/preview", body, map[string]string{"Idempotency-Key": "preview-cleanup"}, 200, &cleanup)
	body["operationId"] = cleanup.ID
	body["expectedVersion"] = f.run.Version - 1
	requestJSONWithHeaders(t, h, "POST", prefix+"/closure/cleanup/confirm", body, map[string]string{"Idempotency-Key": "stale-preview-cleanup"}, 409, nil)
	waitAppPreviewHTTP(t, view.Preview.URL, "Task preview content\n")
	body["expectedVersion"] = f.run.Version
	requestJSONWithHeaders(t, h, "POST", prefix+"/closure/cleanup/confirm", body, map[string]string{"Idempotency-Key": "confirm-cleanup"}, 200, &cleanup)
	if cleanup.Status != "succeeded" {
		t.Fatalf("cleanup=%+v", cleanup)
	}
	if _, err := os.Stat(filepath.Join(taskRoot, repoID)); !os.IsNotExist(err) {
		t.Fatalf("worktree survived cleanup: %v", err)
	}
	requestJSON(t, h, "GET", prefix+"/app-preview", nil, 200, &view)
	if view.Preview.State != "stopped" || view.Available {
		t.Fatalf("cleaned preview=%+v", view)
	}
}

// This explicit, credential-free route uses a previously prepared real image.
// The test root must be shared with the local Docker engine; no Agent runs.
func TestAppPreviewIsolated(t *testing.T) {
	dataRoot := os.Getenv("CHORA_PREVIEW_ISOLATED_DATA")
	if dataRoot == "" {
		t.Skip("set CHORA_PREVIEW_ISOLATED_DATA and a Docker-shared TMPDIR to verify real isolated app preview")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	resolve := func(ctx context.Context) (apppreview.IsolatedRuntime, error) {
		record, err := isolatedenv.Load(ctx, dataRoot)
		if err != nil {
			return apppreview.IsolatedRuntime{}, err
		}
		return apppreview.IsolatedRuntime{Runner: record.Runner, ImageID: record.Source.ImageID(), EngineIdentity: record.EngineIdentity.Digest()}, nil
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source")
	if err = os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	script := strings.Replace(previewTestScript, "'127.0.0.1'", "'0.0.0.0'", 1)
	if err = os.WriteFile(filepath.Join(source, "preview.cjs"), []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(source, "same.txt"), []byte("isolated preview snapshot\n"), 0600); err != nil {
		t.Fatal(err)
	}
	options := apppreview.Options{RuntimeRoot: filepath.Join(root, "runtime"), ResolveIsolated: resolve}
	m, err := apppreview.New(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.StopAll(context.Background()) })
	target := apppreview.Target{Key: "isolated-preview-test", Root: source, AttemptID: "test-attempt", Profile: "isolated_local"}
	config := apppreview.Config{Command: "node preview.cjs", WorkingDirectory: ".", Port: 3000}
	v, err := m.Start(ctx, target, config)
	if err != nil {
		t.Fatal(err)
	}
	waitAppPreviewHTTP(t, v.URL, "isolated preview snapshot\n")
	// The app reads its snapshot; host edits after startup do not enter it.
	if err = os.WriteFile(filepath.Join(source, "same.txt"), []byte("new host content\n"), 0600); err != nil {
		t.Fatal(err)
	}
	waitAppPreviewHTTP(t, v.URL, "isolated preview snapshot\n")
	v, err = m.Inspect(ctx, target.Key)
	if err != nil {
		t.Fatal(err)
	}
	logs, err := m.Logs(target.Key, 0, 65536)
	if err != nil || !strings.Contains(string(logs.Data), "preview fixture ready") {
		t.Fatalf("isolated logs=%q err=%v", logs.Data, err)
	}
	// A new manager proves the persisted container without launching another.
	restarted, err := apppreview.New(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = restarted.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = restarted.Cleanup(ctx, target.Key); err != nil {
		t.Fatal(err)
	}
	if content, _ := os.ReadFile(filepath.Join(source, "same.txt")); string(content) != "new host content\n" {
		t.Fatal("isolated cleanup changed host source")
	}
	if response, err := (&http.Client{Timeout: time.Second}).Get(v.URL); err == nil {
		_ = response.Body.Close()
		t.Fatal("isolated app survived cleanup")
	}
	v, err = restarted.Inspect(ctx, target.Key)
	if err != nil || v.Config != config {
		t.Fatalf("saved configuration after restart/cleanup=%+v %v", v, err)
	}
}
