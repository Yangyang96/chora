package localweb

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/nativecapabilities"
	"github.com/Yangyang96/chora/internal/pidiscovery"
)

func TestExecutionSettingsAPIInheritsFreezesAndRequiresLocalAcknowledgement(t *testing.T) {
	server, room, resource, _, closeStore := newTaskResourcePathValidationFixture(t)
	defer closeStore()
	handler := server.Handler()
	settingsPath := "/api/projects/" + room.ProjectID().String() + "/execution-settings"
	var defaults projectExecutionSettingsView
	requestJSON(t, handler, http.MethodGet, settingsPath, nil, http.StatusOK, &defaults)
	if defaults.Version != 0 || defaults.AgentExecutionProfile != domain.AgentExecutionProfileIsolatedLocal || defaults.Model != nil {
		t.Fatalf("unsafe initial defaults: %+v", defaults)
	}
	requestJSONWithHeaders(t, handler, http.MethodPut, settingsPath,
		map[string]any{"version": 0, "agentExecutionProfile": "trusted_local", "model": nil},
		map[string]string{"Idempotency-Key": "save-local-default"}, http.StatusOK, &defaults)
	if defaults.Version != 1 {
		t.Fatal(defaults)
	}
	capabilitiesPath := "/api/projects/" + room.ProjectID().String() + "/capabilities/config"
	var capabilities nativecapabilities.Config
	requestJSON(t, handler, http.MethodPut, capabilitiesPath, nativecapabilities.Config{SkillPaths: []string{"/local/skills/original"}}, http.StatusOK, &capabilities)
	revisions, err := server.store.Reader().ListRoomRevisions(context.Background(), room.ID())
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, revision := range revisions {
		ids = append(ids, revision.ID().String())
	}
	path := "/api/v2/rooms/" + room.ID().String() + "/tasks"
	body := map[string]any{"requirement": "Use the Project execution settings", "resources": []taskResourceSelection{resource}, "revisionIds": ids,
		"executionSettings": map[string]any{"projectVersion": 1}}
	headers := map[string]string{"Idempotency-Key": "create-inherited-task"}
	requestJSONWithHeaders(t, handler, http.MethodPost, path, body, headers, http.StatusForbidden, nil)
	requestJSONWithHeaders(t, handler, http.MethodPost, "/api/agent-execution/trusted-local-acknowledgements",
		map[string]any{"policyVersion": domain.TrustedLocalDisclosurePolicy}, map[string]string{"Idempotency-Key": "ack-local"}, http.StatusOK, nil)
	var created taskRefView
	requestJSONWithHeaders(t, handler, http.MethodPost, path, body, headers, http.StatusCreated, &created)
	if created.ExecutionSettings == nil || created.ExecutionSettings.ProjectVersion != 1 || created.ExecutionSettings.EnvironmentSource != "project" || created.ExecutionSettings.ModelSource != "project" || created.ExecutionSettings.ModelBinding.Configured() || created.AgentExecutionProfile != "trusted_local" {
		t.Fatalf("missing inherited settings: %+v", created)
	}
	taskID, _ := domain.ParseTaskID(created.ID)
	task, err := server.store.Reader().GetTask(context.Background(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	// A runtime preparing a later retry must read this original config, even
	// after the Project's settings and native references have changed.
	frozen, err := taskNativeCapabilities(context.Background(), server.store.Reader(), "", task, room.ProjectID())
	if err != nil || !reflect.DeepEqual(frozen.SkillPaths, capabilities.SkillPaths) {
		t.Fatalf("frozen capabilities=%+v: %v", frozen, err)
	}
	requestJSONWithHeaders(t, handler, http.MethodPut, settingsPath,
		map[string]any{"version": 1, "agentExecutionProfile": "isolated_local", "model": nil},
		map[string]string{"Idempotency-Key": "save-isolated-default"}, http.StatusOK, &defaults)
	requestJSON(t, handler, http.MethodPut, capabilitiesPath, nativecapabilities.Config{Version: capabilities.Version, SkillPaths: []string{"/local/skills/later"}}, http.StatusOK, nil)
	var reopened, replay taskRefView
	requestJSON(t, handler, http.MethodGet, "/api/tasks/"+created.ID, nil, http.StatusOK, &reopened)
	requestJSONWithHeaders(t, handler, http.MethodPost, path, body, headers, http.StatusCreated, &replay)
	if replay.ID != created.ID || !reflect.DeepEqual(reopened.ExecutionSettings, created.ExecutionSettings) || !reflect.DeepEqual(replay.ExecutionSettings, created.ExecutionSettings) {
		t.Fatalf("Project edit changed task/replay: created=%+v reopened=%+v replay=%+v", created.ExecutionSettings, reopened.ExecutionSettings, replay.ExecutionSettings)
	}
	after, err := taskNativeCapabilities(context.Background(), server.store.Reader(), "", task, room.ProjectID())
	if err != nil || !reflect.DeepEqual(after, frozen) {
		t.Fatalf("runtime rebound capabilities: %+v: %v", after, err)
	}
	requestJSONWithHeaders(t, handler, http.MethodPost, path, body, map[string]string{"Idempotency-Key": "stale-project-default"}, http.StatusConflict, nil)
	// A new Task inherits isolation and fails closed when isolation is absent.
	body["executionSettings"] = map[string]any{"projectVersion": 2}
	requestJSONWithHeaders(t, handler, http.MethodPost, path, body, map[string]string{"Idempotency-Key": "unavailable-isolation"}, http.StatusUnprocessableEntity, nil)
	// An explicit Task override is separate from editing the Project default.
	body["executionSettings"] = map[string]any{"projectVersion": 2, "agentExecutionProfile": "trusted_local", "model": nil}
	requestJSONWithHeaders(t, handler, http.MethodPost, path, body, map[string]string{"Idempotency-Key": "explicit-local-override"}, http.StatusCreated, &created)
	if created.ExecutionSettings.EnvironmentSource != "task" || created.ExecutionSettings.ModelSource != "task" {
		t.Fatal(created.ExecutionSettings)
	}
	requestJSON(t, handler, http.MethodGet, settingsPath, nil, http.StatusOK, &defaults)
	if defaults.AgentExecutionProfile != "isolated_local" || defaults.Version != 2 {
		t.Fatalf("Task override changed Project: %+v", defaults)
	}
}

func TestProjectExecutionSettingsAPIValidatesModelsWithoutBreakingReplay(t *testing.T) {
	server, room, _, _, closeStore := newTaskResourcePathValidationFixture(t)
	defer closeStore()
	root := t.TempDir()
	executable := filepath.Join(root, "pi")
	catalog := filepath.Join(root, "catalog")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\ncase \"$1\" in\n--version) echo 0.85.1;;\nauth) echo ready;;\n--list-models) cat \"$PI_CODING_AGENT_DIR/catalog\";;\n*) exit 1;;\nesac\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog, []byte("provider model context max-out thinking images\np selected 128K 16K yes no\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "settings.json"), []byte(`{"defaultProvider":"p","defaultModel":"selected"}`), 0600); err != nil {
		t.Fatal(err)
	}
	server.piDiscoveryOptions = pidiscovery.Options{PiHome: root, LookPath: func(string) (string, error) { return executable, nil }}
	path := "/api/projects/" + room.ProjectID().String() + "/execution-settings"
	body := map[string]any{"version": 0, "agentExecutionProfile": "trusted_local", "model": domain.ModelIdentity{Provider: "p", ModelID: "selected"}}
	headers := map[string]string{"Idempotency-Key": "select-model-default"}
	var saved, replay projectExecutionSettingsView
	requestJSONWithHeaders(t, server.Handler(), http.MethodPut, path, body, headers, http.StatusOK, &saved)
	if saved.ProjectID != room.ProjectID().String() || saved.Model == nil || saved.Model.ModelID != "selected" {
		t.Fatal(saved)
	}
	if err := os.WriteFile(catalog, []byte("no available models\n"), 0600); err != nil {
		t.Fatal(err)
	}
	requestJSONWithHeaders(t, server.Handler(), http.MethodPut, path, body, headers, http.StatusOK, &replay)
	if !reflect.DeepEqual(saved, replay) {
		t.Fatalf("replay consulted changed model state: %+v %+v", saved, replay)
	}
	body["version"] = 1
	requestJSONWithHeaders(t, server.Handler(), http.MethodPut, path, body, map[string]string{"Idempotency-Key": "unavailable-model"}, http.StatusBadRequest, nil)
	body["agentExecutionProfile"] = "isolated_local"
	requestJSONWithHeaders(t, server.Handler(), http.MethodPut, path, body, map[string]string{"Idempotency-Key": "no-cross-environment-fallback"}, http.StatusBadRequest, nil)
	requestJSON(t, server.Handler(), http.MethodGet, path, nil, http.StatusOK, &replay)
	if !reflect.DeepEqual(saved, replay) {
		t.Fatal("rejected selection changed defaults")
	}
}
