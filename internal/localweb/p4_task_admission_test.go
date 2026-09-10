package localweb

import (
	"context"
	"net/http"
	"testing"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/gitsource"
)

func TestProjectTaskAdmissionCannotBypassSavedSettings(t *testing.T) {
	db := openTaskWorktreeResolverStore(t)
	defer db.Close()
	root, _, _ := newTaskWorktreeTestRepository(t)
	service := app.NewService(app.Dependencies{Store: db, RepositorySource: gitsource.Default{}, Authorizer: localAuthorizer{}})
	server := &Server{store: db, service: service, repositorySource: gitsource.Default{}, pathPiEnabled: true}
	handler := server.Handler()
	var project projectView
	requestJSON(t, handler, http.MethodPost, "/api/projects", map[string]any{"locator": root}, http.StatusOK, &project)
	requestJSON(t, handler, http.MethodPut, "/api/projects/"+project.ID+"/settings", projectSettingsView{Version: 0, WritableFiles: []string{"README.md"}, NoChecks: true}, http.StatusOK, nil)
	path := "/api/rooms/" + project.Room.ID + "/tasks"
	requestJSON(t, handler, http.MethodPost, path, map[string]any{"title": "missing settings version", "goal": "inspect"}, http.StatusBadRequest, nil)
	requestJSON(t, handler, http.MethodPost, path, map[string]any{"title": "direct bypass", "projectSettingsVersion": 1, "realSpecCoding": map[string]any{"requirement": "bypass", "writableDirectories": []string{"unconfigured"}, "noChecks": true}}, http.StatusBadRequest, nil)
	requestJSON(t, handler, http.MethodPost, path, map[string]any{"title": "stale version", "goal": "inspect", "projectSettingsVersion": 0}, http.StatusConflict, nil)
	roomID, err := domain.ParseRoomID(project.Room.ID)
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := db.Reader().GetRoomWorkspace(context.Background(), roomID)
	if err != nil || len(tasks.Tasks) != 0 {
		t.Fatalf("rejected admission created tasks: %v, %v", tasks, err)
	}
}
