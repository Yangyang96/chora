package localweb

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/gitsource"
)

type recordingDirectoryOpener struct{ paths []string }

func (o *recordingDirectoryOpener) Open(_ context.Context, application, path string) error {
	o.paths = append(o.paths, application+":"+path)
	return nil
}

func TestExternalDirectoryArgvNeverInterpretsPath(t *testing.T) {
	for _, app := range []string{"editor", "terminal"} {
		path := "/tmp/space $(touch pwned); --args"
		args, err := externalDirectoryArgs(app, path)
		if err != nil || len(args) != 3 || args[2] != path {
			t.Fatalf("args=%v err=%v", args, err)
		}
	}
	for _, tc := range [][2]string{{"shell", "/tmp/repo"}, {"terminal", "relative"}, {"editor", "/tmp/../etc"}, {"editor", "/"}} {
		if _, err := externalDirectoryArgs(tc[0], tc[1]); err == nil {
			t.Fatalf("accepted %v", tc)
		}
	}
}

func TestExternalHandoffRevalidatesRepositoryAndTaskIdentity(t *testing.T) {
	ctx := context.Background()
	db := openTaskWorktreeResolverStore(t)
	defer db.Close()
	anchor, _, revision := newTaskWorktreeTestRepository(t)
	room, task := insertResolverRoomAndTask(t, db, anchor, revision, "External", time.Now().UTC())
	_, foreign := insertResolverRoomWithoutBinding(t, db, time.Now().UTC())
	opener := &recordingDirectoryOpener{}
	server := &Server{store: db, service: app.NewService(app.Dependencies{Store: db}), repositorySource: gitsource.Default{}, worktreeResolver: newTaskWorktreeResolver(db.Reader(), nil), externalOpener: opener}
	post := func(body string) *httptest.ResponseRecorder {
		r := newLocalRequest(http.MethodPost, "/api/projects/"+room.ID().String()+"/open-external", strings.NewReader(body))
		r.Host = "127.0.0.1:8787"
		r.Header.Set("Content-Type", "application/json")
		r.SetPathValue("projectID", room.ID().String())
		w := httptest.NewRecorder()
		server.openExternalDirectory(w, r)
		return w
	}
	if w := post(`{"application":"editor"}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if !reflect.DeepEqual(opener.paths, []string{"editor:" + anchor}) {
		t.Fatal(opener.paths)
	}
	for _, body := range []string{`{"application":"shell"}`, `{"application":"terminal","taskId":"` + foreign.ID().String() + `"}`, `{"application":"terminal","taskId":"` + task.ID().String() + `"}`, `{"application":"terminal","path":"/etc"}`} {
		if w := post(body); w.Code == 200 {
			t.Fatalf("accepted %s", body)
		}
	}
	if len(opener.paths) != 1 {
		t.Fatal(opener.paths)
	}
	moved := anchor + "-moved"
	if err := os.Rename(anchor, moved); err != nil {
		t.Fatal(err)
	}
	defer os.Rename(moved, anchor)
	if w := post(`{"application":"terminal"}`); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "restore") {
		t.Fatal(w.Code, w.Body.String())
	}
	if err := os.Symlink(moved, anchor); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(anchor)
	if _, err := server.continuityDirectory(ctx, room.ID().String(), ""); err == nil {
		t.Fatal("accepted changed canonical repository")
	}
	if filepath.Clean(anchor) != anchor {
		t.Fatal("test anchor not canonical")
	}
}

type testProjectPicker struct {
	path  string
	err   error
	calls int
}

func (p *testProjectPicker) Choose(context.Context) (string, error) {
	p.calls++
	return p.path, p.err
}

func TestProjectDirectoryPickerBoundary(t *testing.T) {
	picker := &testProjectPicker{path: "/tmp/project with spaces"}
	server := &Server{repositorySource: gitsource.Default{}, directoryPicker: picker}
	handler := server.Handler()
	post := func(host, origin, contentType, body string) *httptest.ResponseRecorder {
		r := newLocalRequest(http.MethodPost, "http://"+host+"/api/projects/choose-directory", strings.NewReader(body))
		r.Header.Set("Content-Type", contentType)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, tc := range []struct {
		host, origin, contentType, body string
		status                          int
	}{
		{"127.0.0.1:8787", "https://evil.example", "application/json", "{}", 403},
		{"evil.example", "http://evil.example", "application/json", "{}", 403},
		{"127.0.0.1:8787", "", "text/plain", "{}", 415},
		{"127.0.0.1:8787", "", "application/json", `{"script":"do something"}`, 400},
	} {
		if w := post(tc.host, tc.origin, tc.contentType, tc.body); w.Code != tc.status {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if picker.calls != 0 {
		t.Fatal("rejected request opened a native dialog")
	}
	server.directoryPickerMu.Lock()
	w := post("localhost:8787", "http://localhost:8787", "application/json", "{}")
	server.directoryPickerMu.Unlock()
	if w.Code != 409 || picker.calls != 0 {
		t.Fatal("concurrent dialog was allowed", w.Code)
	}
	w = post("localhost:8787", "http://localhost:8787", "application/json", "{}")
	if w.Code != 200 || !strings.Contains(w.Body.String(), picker.path) {
		t.Fatal(w.Code, w.Body.String())
	}
	picker.path = ""
	w = post("localhost:8787", "", "application/json", "{}")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"cancelled":true`) {
		t.Fatal(w.Code, w.Body.String())
	}
	picker.err = context.DeadlineExceeded
	w = post("localhost:8787", "", "application/json", "{}")
	if w.Code != 503 {
		t.Fatal(w.Code, w.Body.String())
	}
	picker.err = nil
	w = post("localhost:8787", "", "application/json", "{}")
	if w.Code != 200 {
		t.Fatal("chooser lock remained held", w.Code)
	}
}

func TestProjectAdmissionRejectsCrossSitePickerBypass(t *testing.T) {
	server := &Server{repositorySource: gitsource.Default{}}
	for _, tc := range []struct {
		origin, mediaType string
		status            int
	}{
		{"https://evil.example", "text/plain", 403},
		{"https://evil.example", "application/json", 403},
		{"", "text/plain", 415},
	} {
		r := newLocalRequest(http.MethodPost, "http://127.0.0.1:8787/api/projects", strings.NewReader(`{"locator":"/tmp/guessed-project"}`))
		if tc.origin != "" {
			r.Header.Set("Origin", tc.origin)
		}
		r.Header.Set("Content-Type", tc.mediaType)
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	r := newLocalRequest(http.MethodPost, "http://evil.example/api/projects", strings.NewReader(`{"locator":"/tmp/guessed-project"}`))
	r.Header.Set("Origin", "http://evil.example")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatal("non-local Host reached admission", w.Code)
	}
	// No service is configured: reaching admission at all would panic.
}

func TestTaskFirstRoomContinuityRequiresTaskWithoutLegacyAPIError(t *testing.T) {
	root := newRepositoryEntriesFixture(t)
	server, _, projectID, _ := newRepositoryEntriesTestServer(t, root)
	id, err := domain.ParseProjectID(projectID)
	if err != nil {
		t.Fatal(err)
	}
	project, err := server.store.Reader().GetProject(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	opener := &recordingDirectoryOpener{}
	server.externalOpener = opener
	for _, routeID := range []string{projectID, project.DefaultRoomID().String()} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.SetPathValue("projectID", routeID)
		w := httptest.NewRecorder()
		server.projectContinuity(w, r)
		var body struct {
			Available, Ready, SelectionRequired bool
			Path, Reason                        string
			Applications                        []any
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if w.Code != http.StatusOK || !body.Available || body.Ready || !body.SelectionRequired || body.Path != "" || body.Reason != "" || len(body.Applications) != 0 {
			t.Fatalf("ambiguous Room continuity: %d %s", w.Code, w.Body.String())
		}
		post := newLocalRequest(http.MethodPost, "/", strings.NewReader(`{"application":"terminal"}`))
		post.Host = "127.0.0.1:18915"
		post.Header.Set("Content-Type", "application/json")
		post.SetPathValue("projectID", routeID)
		result := httptest.NewRecorder()
		server.openExternalDirectory(result, post)
		if result.Code != http.StatusConflict {
			t.Fatalf("ambiguous open accepted: %d %s", result.Code, result.Body.String())
		}
	}
	if len(opener.paths) != 0 {
		t.Fatalf("opened unselected path: %v", opener.paths)
	}
}

func TestTaskContinuityWaitsForPreparationWithoutCreatingWorktrees(t *testing.T) {
	fixture := newTaskResourceWorkspaceFixtureMode(t, true, newTaskResourceTestRepository(t, false))
	resolver, err := newTaskResourceWorkspaceManager(fixture.db, fixture.dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{store: fixture.db, service: app.NewService(app.Dependencies{Store: fixture.db, TaskResourceWorkspaces: resolver})}
	probe := func() map[string]any {
		request := newLocalRequest(http.MethodGet, "/api/projects/"+fixture.task.RoomID().String()+"/continuity?taskId="+fixture.task.ID().String(), nil)
		request.SetPathValue("projectID", fixture.task.RoomID().String())
		response := httptest.NewRecorder()
		server.projectContinuity(response, request)
		var body map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || response.Code != 200 {
			t.Fatalf("probe: %d %s %v", response.Code, response.Body.String(), err)
		}
		return body
	}
	before := probe()
	if before["preparing"] != true || before["ready"] != false || before["path"] != nil {
		t.Fatalf("creation reported failure or exposed path: %v", before)
	}
	rows, err := fixture.db.Reader().ListTaskRepositoryWorktrees(context.Background(), fixture.task.ID())
	if err != nil || len(rows) != 0 {
		t.Fatalf("read-only probe created records: %v %v", rows, err)
	}
	if _, err := os.Stat(filepath.Join(fixture.dataRoot, "task-workspaces")); !os.IsNotExist(err) {
		t.Fatalf("read-only probe created directory: %v", err)
	}
	if err := resolver.Ensure(context.Background(), fixture.task.ID()); err != nil {
		t.Fatal(err)
	}
	ready := probe()
	if ready["ready"] != true || ready["preparing"] == true || ready["path"] == nil {
		t.Fatalf("prepared path not available: %v", ready)
	}
	root := ready["path"].(string)
	repositoryRoot := filepath.Join(root, fixture.snapshot.Resources[0].WorkspaceDirectory())
	runTaskWorktreeGitTest(t, repositoryRoot, "-c", "user.name=Chora Test", "-c", "user.email=test@example.test", "commit", "--allow-empty", "-m", "delivery commit")
	if committed := probe(); committed["ready"] != true {
		t.Fatalf("committed Task became unavailable for handoff: %v", committed)
	}
	if _, err := resolver.ResolveExecutionRoot(context.Background(), fixture.task.ID()); err == nil {
		t.Fatal("handoff weakened execution base proof")
	}
	originalBranch := fixture.snapshot.Resources[0].TaskBranch
	runTaskWorktreeGitTest(t, repositoryRoot, "branch", "-m", "unrelated-handoff-branch")
	if foreign := probe(); foreign["ready"] != false || foreign["preparing"] == true {
		t.Fatalf("foreign branch exposed: %v", foreign)
	}
	runTaskWorktreeGitTest(t, repositoryRoot, "branch", "-m", originalBranch)

	if err := os.Rename(root, root+"-moved"); err != nil {
		t.Fatal(err)
	}
	defer os.Rename(root+"-moved", root)
	failed := probe()
	if failed["ready"] != false || failed["preparing"] == true || failed["reason"] == nil {
		t.Fatalf("lost worktree hidden as preparing: %v", failed)
	}
}
