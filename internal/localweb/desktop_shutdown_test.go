package localweb

import (
	"context"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	storesqlite "github.com/Yangyang96/chora/internal/store/sqlite"
)

func TestDesktopShutdownPersistsStopBeforeClosingStore(t *testing.T) {
	for _, mode := range []string{"confirmed", "uncertain", "monitor-exited"} {
		uncertain := mode == "uncertain"
		name := mode
		if uncertain {
			name = "uncertain"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			web := filepath.Join(root, "web")
			if err := os.Mkdir(web, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(web, "index.html"), []byte("desktop shutdown fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			database := filepath.Join(root, "chora.db")
			server, err := newTestServer(context.Background(), database, web, log.New(io.Discard, "", 0))
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			server.gracefulShutdown = true
			handler := server.Handler()
			var room struct {
				ID              string `json:"id"`
				InitialRevision struct {
					ID string `json:"id"`
				} `json:"initialRevision"`
			}
			requestJSON(t, handler, http.MethodPost, "/api/rooms", map[string]any{"name": "Desktop exit", "workspaceRoot": root}, http.StatusCreated, &room)
			var task struct {
				ID string `json:"id"`
			}
			requestJSON(t, handler, http.MethodPost, "/api/rooms/"+room.ID+"/tasks", map[string]any{"title": "Stop safely", "goal": "Preserve shutdown state", "criteria": []string{"State is retained"}, "revisionIds": []string{room.InitialRevision.ID}}, http.StatusCreated, &task)
			acceptTaskPlan(t, handler, task.ID)
			var started runView
			requestJSON(t, handler, http.MethodPost, "/api/tasks/"+task.ID+"/runs", nil, http.StatusCreated, &started)
			runID, err := domain.ParseRunID(started.ID)
			if err != nil {
				t.Fatal(err)
			}
			attempt, err := server.store.Reader().GetCurrentAttempt(context.Background(), runID)
			if err != nil {
				t.Fatal(err)
			}
			session, err := server.store.Reader().GetRuntimeSessionForAttempt(context.Background(), attempt.ID())
			if err != nil {
				t.Fatal(err)
			}
			if uncertain {
				server.fakeSupervisor.mu.Lock()
				server.fakeSupervisor.reconcileOverride = execution.ReconcileUncertain
				server.fakeSupervisor.mu.Unlock()
			}
			if mode != "monitor-exited" {
				server.startRuntimeMonitor("fake", runID, session.ID, execution.ProcessIdentity{Value: session.ProcessIdentity})
			}
			closeErr := server.Close()
			if uncertain && closeErr == nil {
				t.Fatal("uncertain shutdown reported success")
			}
			if !uncertain && closeErr != nil {
				t.Fatal(closeErr)
			}
			reopened, err := storesqlite.Open(context.Background(), database)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			retained, err := reopened.Reader().GetRun(context.Background(), runID)
			if err != nil {
				t.Fatal(err)
			}
			want := domain.RunStateCancelled
			if uncertain {
				want = domain.RunStateRecoveryRequired
			}
			if retained.State() != want {
				t.Fatalf("retained state=%s want=%s", retained.State(), want)
			}
			if !uncertain {
				retainedSession, e := reopened.Reader().GetRuntimeSessionForAttempt(context.Background(), attempt.ID())
				if e != nil {
					t.Fatal(e)
				}
				if retainedSession.State != "stopped" || retainedSession.FinalizedAt.IsZero() {
					t.Fatalf("no persisted stop/cleanup proof: %+v", retainedSession)
				}
			}
		})
	}
}
