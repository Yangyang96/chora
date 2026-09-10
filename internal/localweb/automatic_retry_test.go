package localweb

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func TestAutomaticRetryDispatcherLaunchesTwoFreshAttemptsThenExhausts(t *testing.T) {
	server, runtime, taskID := newAutomaticRetryServer(t)
	defer server.Close()
	runtime.mu.Lock()
	runtime.terminalFiles = execution.TerminalFiles{ExitCode: 137, TerminationCause: execution.TerminationOutputLimit}
	runtime.mu.Unlock()

	var started runView
	requestJSON(t, server.Handler(), http.MethodPost, "/api/tasks/"+taskID+"/runs", nil, http.StatusCreated, &started)
	for retry := 1; retry <= app.AutomaticRetryMaxRetries; retry++ {
		if err := runtime.Exit(); err != nil {
			t.Fatal(err)
		}
		running := waitForAutomaticRetryView(t, server.Handler(), started.ID, retry+1, app.AutomaticRetryRetrying, domain.RunStateRunning)
		if running.AutomaticRetry == nil || running.AutomaticRetry.RetriesUsed != retry || running.AutomaticRetry.MaxRetries != 2 || running.AutomaticRetry.LastFailureReason != "runtime_output_limit_exceeded" {
			t.Fatalf("automatic retry %d view=%#v", retry, running.AutomaticRetry)
		}
	}
	if err := runtime.Exit(); err != nil {
		t.Fatal(err)
	}
	exhausted := waitForAutomaticRetryView(t, server.Handler(), started.ID, 3, app.AutomaticRetryExhausted, domain.RunStateRecoveryRequired)
	if exhausted.AutomaticRetry == nil || exhausted.AutomaticRetry.RetriesUsed != 2 || exhausted.Controls.CanRetry == false {
		t.Fatalf("exhausted automatic retry view=%#v controls=%#v", exhausted.AutomaticRetry, exhausted.Controls)
	}
}

func TestAutomaticRetryDispatcherRestoresPendingStateAndSkipsArchivedTask(t *testing.T) {
	server, runtime, taskIDText, databasePath := newAutomaticRetryServerWithDatabase(t)
	defer server.Close()
	server.automaticRetryCancel()
	server.automaticRetryWG.Wait()
	runtime.mu.Lock()
	runtime.terminalFiles = execution.TerminalFiles{ExitCode: 137, TerminationCause: execution.TerminationOutputLimit}
	runtime.mu.Unlock()

	var started runView
	requestJSON(t, server.Handler(), http.MethodPost, "/api/tasks/"+taskIDText+"/runs", nil, http.StatusCreated, &started)
	runID, err := domain.ParseRunID(started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Exit(); err != nil {
		t.Fatal(err)
	}
	waitForAutomaticRetryView(t, server.Handler(), started.ID, 1, app.AutomaticRetryPending, domain.RunStateRecoveryRequired)
	status, err := server.service.AutomaticRetryForRun(context.Background(), runID)
	if err != nil || status == nil || status.State != app.AutomaticRetryPending {
		t.Fatalf("persisted status=%#v err=%v", status, err)
	}
	taskID, err := domain.ParseTaskID(taskIDText)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.service.ArchiveTask(context.Background(), app.ArchiveTaskRequest{CommandMeta: commandMeta("archive-pending-auto-retry"), TaskID: taskID}); !errors.Is(err, storecontract.ErrRoomStateForbidden) {
		t.Fatalf("pending automatic retry must block normal Task archive: %v", err)
	}
	// Model a retained historical/restart state to exercise the dispatcher's
	// independent archived-Task defense. Current product commands cannot create
	// this combination because ArchiveTask correctly rejects pending work.
	raw, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = raw.Exec(`UPDATE tasks SET archived=1,archived_at=?,updated_at=? WHERE id=?`, now, now, taskID.String()); err != nil {
		_ = raw.Close()
		t.Fatal(err)
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}
	server.dispatchAutomaticRetries(context.Background())
	archivedRun, err := server.store.Reader().GetRun(context.Background(), runID)
	if err != nil || archivedRun.CurrentAttemptNumber() != 1 || archivedRun.State() != domain.RunStateRecoveryRequired {
		t.Fatalf("archived task dispatched Run=%#v err=%v", archivedRun, err)
	}
	if _, err := server.service.RestoreTask(context.Background(), app.ArchiveTaskRequest{CommandMeta: commandMeta("restore-pending-auto-retry"), TaskID: taskID}); err != nil {
		t.Fatal(err)
	}
	server.dispatchAutomaticRetries(context.Background())
	restored := waitForAutomaticRetryView(t, server.Handler(), started.ID, 2, app.AutomaticRetryRetrying, domain.RunStateRunning)
	if restored.AutomaticRetry == nil || restored.AutomaticRetry.RetriesUsed != 1 {
		t.Fatalf("restored dispatcher view=%#v", restored.AutomaticRetry)
	}
}

func TestBlockedPreparedAutomaticRetryCanBeExplicitlyStarted(t *testing.T) {
	server, runtime, taskID := newAutomaticRetryServer(t)
	defer server.Close()
	server.automaticRetryCancel()
	server.automaticRetryWG.Wait()
	runtime.mu.Lock()
	runtime.terminalFiles = execution.TerminalFiles{ExitCode: 137, TerminationCause: execution.TerminationOutputLimit}
	runtime.mu.Unlock()

	var started runView
	requestJSON(t, server.Handler(), http.MethodPost, "/api/tasks/"+taskID+"/runs", nil, http.StatusCreated, &started)
	runID, err := domain.ParseRunID(started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Exit(); err != nil {
		t.Fatal(err)
	}
	waitForAutomaticRetryView(t, server.Handler(), started.ID, 1, app.AutomaticRetryPending, domain.RunStateRecoveryRequired)
	failedRun, err := server.store.Reader().GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	firstAttempt, err := server.store.Reader().GetCurrentAttempt(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	status, err := server.service.AutomaticRetryForRun(context.Background(), runID)
	if err != nil || status == nil || status.State != app.AutomaticRetryPending {
		t.Fatalf("pending status=%#v err=%v", status, err)
	}
	envelope, err := automaticRetryEnvelope(status.LastFailureReason)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := server.prepareAndBindManagedAttempt(context.Background(), func(ctx context.Context) (app.PrepareRunResult, error) {
		return server.service.PrepareRetry(ctx, app.PrepareRetryRequest{
			CommandMeta: automaticRetryCommandMeta("test-prepare", firstAttempt.ID()), RunID: runID, ExpectedVersion: failedRun.Version(),
			Reason: status.LastFailureReason, Instructions: string(envelope), Automatic: true,
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.service.BlockAutomaticRetry(context.Background(), runID, prepared.Attempt.ID(), prepared.Run.Version(), "automatic_retry_route_unavailable"); err != nil {
		t.Fatal(err)
	}
	var blocked runView
	requestJSON(t, server.Handler(), http.MethodGet, "/api/runs/"+started.ID, nil, http.StatusOK, &blocked)
	if blocked.AutomaticRetry == nil || blocked.AutomaticRetry.State != app.AutomaticRetryBlocked || !blocked.Controls.CanRetry || !blocked.Controls.CanCancel {
		t.Fatalf("blocked prepared view=%#v controls=%#v", blocked.AutomaticRetry, blocked.Controls)
	}
	var resumed runView
	requestJSON(t, server.Handler(), http.MethodPost, "/api/runs/"+started.ID+"/retry", map[string]any{
		"expectedVersion": blocked.Version, "instructions": "Continue the immutable prepared retry.",
	}, http.StatusOK, &resumed)
	if resumed.Status != domain.RunStateRunning || resumed.Attempt != 2 || resumed.AttemptDetail == nil || blocked.AttemptDetail == nil || resumed.AttemptDetail.ID != blocked.AttemptDetail.ID ||
		resumed.AutomaticRetry == nil || resumed.AutomaticRetry.State != app.AutomaticRetryRetrying || resumed.AutomaticRetry.RetriesUsed != 1 || !resumed.Controls.CanCancel {
		t.Fatalf("explicit prepared retry=%#v", resumed)
	}
	if err := runtime.Exit(); err != nil {
		t.Fatal(err)
	}
	pending := waitForAutomaticRetryView(t, server.Handler(), started.ID, 2, app.AutomaticRetryPending, domain.RunStateRecoveryRequired)
	if pending.AutomaticRetry == nil || pending.AutomaticRetry.RetriesUsed != 1 || pending.AutomaticRetry.MaxRetries != 2 {
		t.Fatalf("remaining retry budget=%#v", pending.AutomaticRetry)
	}
}

func newAutomaticRetryServer(t *testing.T) (*Server, *fakeSupervisor, string) {
	server, runtime, taskID, _ := newAutomaticRetryServerWithDatabase(t)
	return server, runtime, taskID
}

func newAutomaticRetryServerWithDatabase(t *testing.T) (*Server, *fakeSupervisor, string, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	webRoot := filepath.Join(root, "web")
	if err := os.MkdirAll(webRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(webRoot, "index.html"), []byte("<!doctype html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	server, err := newTestServer(context.Background(), filepath.Join(root, "chora.db"), webRoot, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	enablePiTestRuntime(t, server)
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	taskID := createRegisteredPiTask(t, server.Handler(), source)
	server.supervisor.mu.RLock()
	runtime := server.supervisor.byAdapter["pi"].(*fakeSupervisor)
	server.supervisor.mu.RUnlock()
	ctx, cancel := context.WithCancel(server.ctx)
	server.automaticRetryCancel = cancel
	server.automaticRetryWG.Add(1)
	go func() {
		defer server.automaticRetryWG.Done()
		server.runAutomaticRetryDispatcher(ctx)
	}()
	return server, runtime, taskID, filepath.Join(root, "chora.db")
}

func waitForAutomaticRetryView(t *testing.T, handler http.Handler, runID string, attempt int, state app.AutomaticRetryState, runState domain.RunState) runView {
	t.Helper()
	deadline := time.Now().Add(12 * time.Second)
	var latest runView
	for time.Now().Before(deadline) {
		requestJSON(t, handler, http.MethodGet, "/api/runs/"+runID, nil, http.StatusOK, &latest)
		if latest.Attempt == attempt && latest.Status == runState && latest.AutomaticRetry != nil && latest.AutomaticRetry.State == state {
			return latest
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("Run %s did not reach attempt=%d run=%s automatic=%s; latest=%#v", runID, attempt, runState, state, latest)
	return runView{}
}
