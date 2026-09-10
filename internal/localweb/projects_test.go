package localweb

import (
	"context"
	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/gitsource"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectRoutesUnavailableWithoutRepositorySource(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	webRoot := filepath.Join(root, "web")
	if err := os.Mkdir(webRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(webRoot, "index.html"), []byte("<!doctype html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	server, err := newTestServer(context.Background(), filepath.Join(root, "chora.db"), webRoot, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	handler := server.Handler()

	for _, tc := range []struct {
		method, path, body string
	}{
		{http.MethodPost, "/api/projects", `{"locator":"/tmp/repo"}`},
		{http.MethodPost, "/api/projects/clone", `{"url":"https://example.invalid/repo.git"}`},
	} {
		request := newLocalRequest(tc.method, tc.path, strings.NewReader(tc.body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s status=%d, want 503", tc.method, tc.path, response.Code)
		}
		if !strings.Contains(response.Body.String(), projectsUnavailableMessage) {
			t.Fatalf("%s %s body=%q, want unavailable message", tc.method, tc.path, response.Body.String())
		}
	}

	list := newLocalRequest(http.MethodGet, "/api/projects", nil)
	listResponse := httptest.NewRecorder()
	handler.ServeHTTP(listResponse, list)
	if listResponse.Code != http.StatusOK {
		t.Fatalf("GET /api/projects status=%d, want 200", listResponse.Code)
	}
	if !strings.Contains(listResponse.Body.String(), `"projects"`) {
		t.Fatalf("GET /api/projects body=%q, want projects list", listResponse.Body.String())
	}
}

func TestProjectTopicRoomRoutesKeepIdentityAndIndependentLifecycle(t *testing.T) {
	db := openTaskWorktreeResolverStore(t)
	defer db.Close()
	root, _, _ := newTaskWorktreeTestRepository(t)
	service := app.NewService(app.Dependencies{Store: db, RepositorySource: gitsource.Default{}, Authorizer: localAuthorizer{}})
	server := &Server{store: db, service: service, repositorySource: gitsource.Default{}, pathPiEnabled: true}
	handler := server.Handler()
	var first projectView
	requestJSON(t, handler, http.MethodPost, "/api/projects", map[string]any{"locator": root, "name": "Project Alpha"}, 200, &first)
	if !strings.HasPrefix(first.ID, "project_") || first.ID == first.Room.ID || first.Room.ProjectID != first.ID || first.Room.Name != "General" || len(first.Rooms) != 1 {
		t.Fatalf("identity: %+v", first)
	}
	var second roomView
	requestJSON(t, handler, http.MethodPost, "/api/projects/"+first.ID+"/rooms", map[string]any{"name": "Refund feature", "description": "Only refund context"}, 201, &second)
	if second.ProjectID != first.ID || second.ID == first.Room.ID || second.Description != "Only refund context" {
		t.Fatalf("second room: %+v", second)
	}
	var reopened projectView
	requestJSON(t, handler, http.MethodPost, "/api/projects", map[string]any{"locator": root, "name": "Do not rename"}, 200, &reopened)
	if reopened.ID != first.ID || reopened.Name != first.Name || len(reopened.Rooms) != 2 {
		t.Fatalf("duplicate admission: %+v", reopened)
	}
	var renamed projectView
	requestJSON(t, handler, http.MethodPatch, "/api/projects/"+first.ID, map[string]any{"name": "Renamed Project", "expectedVersion": reopened.Version}, 200, &renamed)
	if renamed.Room.Name != "General" || renamed.RepositoryBinding.Name != first.RepositoryBinding.Name {
		t.Fatal("project rename changed room/resource authority")
	}
	var topic roomView
	requestJSON(t, handler, http.MethodPatch, "/api/rooms/"+second.ID, map[string]any{"name": "Refund UX", "description": "Updated topic", "expectedVersion": second.Version}, 200, &topic)
	requestJSON(t, handler, http.MethodGet, "/api/projects/"+first.ID, nil, 200, &renamed)
	if renamed.Name != "Renamed Project" || topic.Name != "Refund UX" {
		t.Fatal("rename not independent")
	}
	var archived roomView
	requestJSONWithHeaders(t, handler, http.MethodPost, "/api/rooms/"+first.Room.ID+"/archive", map[string]any{"expectedVersion": renamed.Room.Version}, map[string]string{"Idempotency-Key": "archive-default"}, 200, &archived)
	requestJSON(t, handler, http.MethodGet, "/api/projects/"+first.ID, nil, 200, &renamed)
	if renamed.State != "active" || renamed.Room.State != "archived" {
		t.Fatal("default room archive cascaded project")
	}
	requestJSON(t, handler, http.MethodPost, "/api/projects/"+first.ID+"/archive", map[string]any{"expectedVersion": renamed.Version}, 200, &renamed)
	requestJSON(t, handler, http.MethodPost, "/api/projects/"+first.ID+"/rooms", map[string]any{"name": "Blocked"}, 409, nil)
	requestJSON(t, handler, http.MethodPost, "/api/projects/"+first.ID+"/restore", map[string]any{"expectedVersion": renamed.Version}, 200, &renamed)
	if renamed.Room.State != "archived" {
		t.Fatal("project restore rewrote room lifecycle")
	}
	requestJSON(t, handler, http.MethodPost, "/api/rooms", map[string]any{"name": "Standalone"}, 422, nil)
	// Read-only legacy URL explicitly resolves to actual Project identity.
	var legacy projectView
	requestJSON(t, handler, http.MethodGet, "/api/projects/"+second.ID, nil, 200, &legacy)
	if legacy.ID != first.ID {
		t.Fatal("legacy route changed association")
	}
	// A new topic Room must not become a writable legacy Project alias.
	requestJSON(t, handler, http.MethodDelete, "/api/projects/"+second.ID+"?expectedVersion=1", map[string]any{}, 404, nil)
	var afterRejectedDelete projectView
	requestJSON(t, handler, http.MethodGet, "/api/projects/"+first.ID, nil, 200, &afterRejectedDelete)
	if afterRejectedDelete.State != "active" || afterRejectedDelete.Version != renamed.Version || afterRejectedDelete.RepositoryBinding.State != "active" {
		t.Fatal("topic Room alias changed Project/resource authority")
	}
	// Shared resource projects the originating Room and immutable resource name.
	for _, roomText := range []string{first.Room.ID, second.ID} {
		id, _ := domain.ParseRoomID(roomText)
		binding, err := db.Reader().GetRepositoryBinding(context.Background(), id)
		if err != nil || binding.RoomID() != id || binding.LocalLocator() != root || binding.Name() != first.RepositoryBinding.Name {
			t.Fatalf("resource projection: %v %+v", err, binding)
		}
	}
	requestJSON(t, handler, http.MethodPost, "/api/projects/"+second.ID+"/rooms", map[string]any{"name": "Wrong identity type"}, 400, nil)
}
