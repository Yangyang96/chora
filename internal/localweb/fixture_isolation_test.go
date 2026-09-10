//go:build !chora_e2e

package localweb

import (
	"context"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProductionRunAPIRejectsE2EPatchFixture(t *testing.T) {
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
	var room struct {
		ID              string `json:"id"`
		InitialRevision struct {
			ID string `json:"id"`
		} `json:"initialRevision"`
	}
	requestJSON(t, server.Handler(), http.MethodPost, "/api/rooms", map[string]any{"name": "Fixture boundary", "workspaceRoot": root}, http.StatusCreated, &room)
	var task struct {
		ID string `json:"id"`
	}
	requestJSON(t, server.Handler(), http.MethodPost, "/api/rooms/"+room.ID+"/tasks", map[string]any{
		"title": "Reject fixtures", "goal": "Keep E2E substitutions out of production.", "criteria": []string{"Fixture input is rejected"}, "revisionIds": []string{room.InitialRevision.ID},
	}, http.StatusCreated, &task)
	var failure struct {
		Error string `json:"error"`
	}
	requestJSON(t, server.Handler(), http.MethodPost, "/api/tasks/"+task.ID+"/runs", map[string]any{"patchFixture": "g2-m3-authoritative"}, http.StatusBadRequest, &failure)
	if !strings.Contains(failure.Error, `unknown field "patchFixture"`) {
		t.Fatalf("production fixture rejection = %q", failure.Error)
	}
}
