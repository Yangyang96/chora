package localweb

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/nativecapabilities"
)

func TestNativeCapabilitiesConfigAPIUsesProjectCASAndIsolation(t *testing.T) {
	db := openTaskWorktreeResolverStore(t)
	defer db.Close()
	dataRoot := t.TempDir()
	service := app.NewService(app.Dependencies{Store: db, DataRoot: dataRoot, Authorizer: localAuthorizer{}})
	server := &Server{store: db, service: service, pathPiEnabled: true}
	handler := server.Handler()

	var first, second resourceProjectTestView
	requestJSON(t, handler, http.MethodPost, "/api/v2/projects", map[string]any{"name": "First"}, http.StatusCreated, &first)
	requestJSON(t, handler, http.MethodPost, "/api/v2/projects", map[string]any{"name": "Second"}, http.StatusCreated, &second)
	firstPath := "/api/projects/" + first.ID + "/capabilities/config"
	secondPath := "/api/projects/" + second.ID + "/capabilities/config"

	var empty nativecapabilities.Config
	requestJSON(t, handler, http.MethodGet, firstPath, nil, http.StatusOK, &empty)
	if empty.Version != 0 || empty.SkillPaths == nil || empty.DisabledSkillPaths == nil || empty.DisabledMCPServers == nil {
		t.Fatalf("missing config=%+v", empty)
	}

	want := nativecapabilities.Config{
		SkillPaths: []string{"/local/skills/one", "/local/skills/two"}, DisabledSkillPaths: []string{"/local/skills/two"},
		BridgePath: "/local/pi/node_modules/pi-mcp-adapter", MCPConfigPath: "/local/config/mcp.json",
		DisabledMCPServers: []string{"retired-server"},
	}
	var created nativecapabilities.Config
	requestJSON(t, handler, http.MethodPut, firstPath, want, http.StatusOK, &created)
	if created.Version != 1 || !reflect.DeepEqual(created.SkillPaths, want.SkillPaths) || created.BridgePath != want.BridgePath {
		t.Fatalf("created config=%+v", created)
	}
	var loaded nativecapabilities.Config
	requestJSON(t, handler, http.MethodGet, firstPath, nil, http.StatusOK, &loaded)
	if !reflect.DeepEqual(loaded, created) {
		t.Fatalf("loaded=%+v created=%+v", loaded, created)
	}
	requestJSON(t, handler, http.MethodPut, firstPath, want, http.StatusConflict, nil)
	requestJSON(t, handler, http.MethodPut, firstPath, nativecapabilities.Config{Version: 1, SkillPaths: []string{"relative/skill"}}, http.StatusBadRequest, nil)

	malformed := newLocalRequest(http.MethodPut, firstPath, bytes.NewBufferString(`{"version":1,`))
	malformed.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, malformed)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("malformed status=%d body=%s", recorder.Code, recorder.Body.Bytes())
	}

	secondWant := nativecapabilities.Config{SkillPaths: []string{"/local/skills/second"}}
	var secondCreated nativecapabilities.Config
	requestJSON(t, handler, http.MethodPut, secondPath, secondWant, http.StatusOK, &secondCreated)
	requestJSON(t, handler, http.MethodGet, firstPath, nil, http.StatusOK, &loaded)
	if !reflect.DeepEqual(loaded, created) || secondCreated.Version != 1 || reflect.DeepEqual(secondCreated.SkillPaths, created.SkillPaths) {
		t.Fatalf("project configs leaked: first=%+v second=%+v", loaded, secondCreated)
	}

	server.pathPiEnabled = false
	requestJSON(t, handler, http.MethodPost, "/api/projects/"+first.ID+"/capabilities/verify", nil, http.StatusConflict, nil)
	server.pathPiEnabled = true

	requestJSON(t, handler, http.MethodPost, "/api/v2/projects/"+first.ID+"/archive", map[string]any{"expectedVersion": first.Version}, http.StatusOK, &first)
	requestJSON(t, handler, http.MethodGet, firstPath, nil, http.StatusOK, &loaded)
	if !reflect.DeepEqual(loaded, created) {
		t.Fatalf("archived Project read changed config: %+v", loaded)
	}
	requestJSON(t, handler, http.MethodPut, firstPath, nativecapabilities.Config{Version: created.Version}, http.StatusBadRequest, nil)
	requestJSON(t, handler, http.MethodPost, "/api/projects/"+first.ID+"/capabilities/verify", nil, http.StatusBadRequest, nil)
}
