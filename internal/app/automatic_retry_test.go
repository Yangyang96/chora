package app_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func enableAutomaticRetry(t *testing.T, fixture *fixture) {
	t.Helper()
	now := time.Now().UTC()
	if err := fixture.db.WithinWriteTx(context.Background(), func(tx storecontract.WriteTx) error {
		_, err := tx.AppendRunEvent(context.Background(), fixture.run.ID(), storecontract.EventDraft{
			ID: domain.NewEventID(), Type: "automatic_retry.enabled", Source: "app", OccurredAt: now, RecordedAt: now,
			NormalizedJSON: []byte(`{"type":"automatic_retry.enabled","max_retries":2}`),
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func failAttempt(t *testing.T, fixture *fixture, started app.StartAttemptResult, key string, cause execution.TerminationCause) app.SubmitTerminalResult {
	t.Helper()
	fixture.supervisor.drainOutcome = execution.DrainOutcome{
		Chunks: map[execution.StreamKind][]byte{}, Offsets: execution.StreamOffsets{execution.StreamStdout: 0, execution.StreamStderr: 0},
		EOF:           map[execution.StreamKind]bool{execution.StreamStdout: true, execution.StreamStderr: true},
		TerminalFiles: execution.TerminalFiles{TerminationCause: cause},
	}
	fixture.signalExit()
	result, err := fixture.service.HandleExit(context.Background(), app.ExitRequest{CommandMeta: meta(key, key), SessionID: started.Session.ID, ExpectedVersion: started.Run.Version()})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func prepareAutomaticRetry(t *testing.T, fixture *fixture, failed app.SubmitTerminalResult, key string) app.PrepareRunResult {
	t.Helper()
	status, err := fixture.service.AutomaticRetryForRun(context.Background(), failed.Run.ID())
	if err != nil || status == nil || status.State != app.AutomaticRetryPending {
		t.Fatalf("pending status=%#v err=%v", status, err)
	}
	prepared, err := fixture.service.PrepareRetry(context.Background(), app.PrepareRetryRequest{
		CommandMeta: meta(key, key), RunID: failed.Run.ID(), ExpectedVersion: failed.Run.Version(),
		Reason: status.LastFailureReason, Instructions: "bounded automatic retry", Automatic: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}

func startPreparedRetry(t *testing.T, fixture *fixture, prepared app.PrepareRunResult, key string) app.StartAttemptResult {
	t.Helper()
	started, err := fixture.service.StartAttempt(context.Background(), app.StartAttemptRequest{
		CommandMeta: meta(key, key), RunID: prepared.Run.ID(), ExpectedVersion: prepared.Run.Version(), Mode: app.StartFresh,
	})
	if err != nil {
		t.Fatal(err)
	}
	return started
}

func TestAutomaticRetryBudgetExhaustsAfterTwoSuccessors(t *testing.T) {
	fixture := newFixture(t)
	enableAutomaticRetry(t, fixture)

	failed := failAttempt(t, fixture, fixture.started(t), "auto-failure-0", execution.TerminationOutputLimit)
	for retry := 1; retry <= app.AutomaticRetryMaxRetries; retry++ {
		status, err := fixture.service.AutomaticRetryForRun(context.Background(), failed.Run.ID())
		if err != nil || status == nil || status.State != app.AutomaticRetryPending || status.RetriesUsed != retry-1 || status.MaxRetries != 2 || status.LastFailureReason != "runtime_output_limit_exceeded" {
			t.Fatalf("retry %d pending=%#v err=%v", retry, status, err)
		}
		prepared := prepareAutomaticRetry(t, fixture, failed, "auto-prepare-"+string(rune('0'+retry)))
		retrying, err := fixture.service.AutomaticRetryForRun(context.Background(), failed.Run.ID())
		if err != nil || retrying == nil || retrying.State != app.AutomaticRetryRetrying || retrying.RetriesUsed != retry {
			t.Fatalf("retry %d running metadata=%#v err=%v", retry, retrying, err)
		}
		started := startPreparedRetry(t, fixture, prepared, "auto-start-"+string(rune('0'+retry)))
		failed = failAttempt(t, fixture, started, "auto-failure-"+string(rune('0'+retry)), execution.TerminationOutputLimit)
	}
	exhausted, err := fixture.service.AutomaticRetryForRun(context.Background(), failed.Run.ID())
	if err != nil || exhausted == nil || exhausted.State != app.AutomaticRetryExhausted || exhausted.RetriesUsed != 2 || exhausted.MaxRetries != 2 {
		t.Fatalf("exhausted=%#v err=%v", exhausted, err)
	}
	if _, err := fixture.service.PrepareRetry(context.Background(), app.PrepareRetryRequest{
		CommandMeta: meta("auto-over-budget", "auto-over-budget"), RunID: failed.Run.ID(), ExpectedVersion: failed.Run.Version(),
		Reason: exhausted.LastFailureReason, Instructions: "must not start", Automatic: true,
	}); !errors.Is(err, app.ErrInvalidCommand) {
		t.Fatalf("over-budget prepare error=%v", err)
	}
}

func TestAutomaticRetryRequiresExplicitRecoveryAfterInterruptedProcess(t *testing.T) {
	fixture := newFixture(t)
	enableAutomaticRetry(t, fixture)
	started := fixture.started(t)
	fixture.supervisor.reconcileOutcome = execution.ReconcileOutcome{Kind: execution.ReconcileDead, Handle: execution.RuntimeHandle{Value: "handle"}}
	fixture.supervisor.drainOutcome = execution.DrainOutcome{
		Chunks: map[execution.StreamKind][]byte{}, Offsets: execution.StreamOffsets{execution.StreamStdout: 0, execution.StreamStderr: 0},
		EOF:           map[execution.StreamKind]bool{execution.StreamStdout: true, execution.StreamStderr: true},
		TerminalFiles: execution.TerminalFiles{TerminationCause: execution.TerminationExitNonzero, ExitCode: 137},
	}
	if err := fixture.service.RecoverStartup(context.Background()); err != nil {
		t.Fatal(err)
	}
	run, err := fixture.db.Reader().GetRun(context.Background(), started.Run.ID())
	if err != nil {
		t.Fatal(err)
	}
	status, err := fixture.service.AutomaticRetryForRun(context.Background(), run.ID())
	if err != nil || status == nil || status.State != app.AutomaticRetryBlocked || status.LastFailureReason != "runtime_exit_nonzero" || status.RetriesUsed != 0 {
		t.Fatalf("interrupted retry state=%#v err=%v", status, err)
	}
	request := app.PrepareRetryRequest{CommandMeta: meta("crash-auto", "crash-auto"), RunID: run.ID(), ExpectedVersion: run.Version(), Reason: "runtime_exit_nonzero", Instructions: "recover", Automatic: true}
	if _, err := fixture.service.PrepareRetry(context.Background(), request); !errors.Is(err, app.ErrInvalidCommand) {
		t.Fatalf("automatic recovery was not refused: %v", err)
	}
	attempt, err := fixture.db.Reader().GetCurrentAttempt(context.Background(), run.ID())
	if err != nil || attempt.ID() != started.Attempt.ID() || attempt.State() != domain.AttemptStateInterrupted || fixture.supervisor.startCalls != 1 {
		t.Fatalf("interrupted work changed: attempt=%#v starts=%d err=%v", attempt, fixture.supervisor.startCalls, err)
	}
	session, err := fixture.db.Reader().GetRuntimeSessionForAttempt(context.Background(), attempt.ID())
	if err != nil || session.State != "stopped" || session.FinalizedAt.IsZero() {
		t.Fatalf("interrupted session not finalized: session=%#v err=%v", session, err)
	}
	request.CommandMeta = meta("crash-manual", "crash-manual")
	request.Automatic = false
	prepared, err := fixture.service.PrepareRetry(context.Background(), request)
	if err != nil || prepared.Attempt.Sequence() != 2 || prepared.Attempt.ExternalSession() != session.ExternalReference || fixture.supervisor.startCalls != 1 {
		t.Fatalf("explicit recovery unavailable: prepared=%#v err=%v", prepared, err)
	}
}

func TestAutomaticRetryKeepsOrdinaryFailuresEligible(t *testing.T) {
	for _, cause := range []execution.TerminationCause{execution.TerminationOutputLimit, execution.TerminationDeadlineExceeded, execution.TerminationExitNonzero} {
		t.Run(string(cause), func(t *testing.T) {
			fixture := newFixture(t)
			enableAutomaticRetry(t, fixture)
			failed := failAttempt(t, fixture, fixture.started(t), "ordinary-failure", cause)
			status, err := fixture.service.AutomaticRetryForRun(context.Background(), failed.Run.ID())
			if err != nil || failed.Attempt.State() != domain.AttemptStateFailed || status == nil || status.State != app.AutomaticRetryPending {
				t.Fatalf("ordinary failure lost automatic recovery: attempt=%s status=%#v err=%v", failed.Attempt.State(), status, err)
			}
		})
	}
}

func TestAutomaticRetrySuccessClearsMetadata(t *testing.T) {
	fixture := newFixture(t)
	enableAutomaticRetry(t, fixture)
	failed := failAttempt(t, fixture, fixture.started(t), "success-failure", execution.TerminationOutputLimit)
	prepared := prepareAutomaticRetry(t, fixture, failed, "success-prepare")
	started := startPreparedRetry(t, fixture, prepared, "success-start")
	succeeded := fixture.exit(t, started, "success-terminal", execution.TerminalResult{Kind: execution.TerminalReviewReady, Summary: "done"})
	status, err := fixture.service.AutomaticRetryForRun(context.Background(), succeeded.Run.ID())
	if err != nil || status != nil {
		t.Fatalf("success metadata=%#v err=%v", status, err)
	}
}

func TestAutomaticRetryExcludesUnprovenAndModelFailures(t *testing.T) {
	t.Run("historical failure", func(t *testing.T) {
		fixture := newFixture(t)
		failed := failAttempt(t, fixture, fixture.started(t), "historical-failure", execution.TerminationOutputLimit)
		status, err := fixture.service.AutomaticRetryForRun(context.Background(), failed.Run.ID())
		if err != nil || status != nil {
			t.Fatalf("historical metadata=%#v err=%v", status, err)
		}
	})
	t.Run("model error", func(t *testing.T) {
		fixture := newFixture(t)
		enableAutomaticRetry(t, fixture)
		started := fixture.started(t)
		fixture.adapter.decode = []execution.NormalizedEvent{{Type: "error", NormalizedJSON: []byte(`{"event_type":"error","error_class":"model_error"}`)}}
		failed := fixture.exit(t, started, "excluded-model", execution.TerminalResult{Kind: execution.TerminalReviewReady})
		status, err := fixture.service.AutomaticRetryForRun(context.Background(), failed.Run.ID())
		if err != nil || status == nil || status.State != app.AutomaticRetryBlocked || status.LastFailureReason != "agent_model_error" {
			t.Fatalf("model metadata=%#v err=%v", status, err)
		}
	})
}

func TestAutomaticRetryStateSurvivesServiceRestartAndStaleBlockCannotPoisonManualRetry(t *testing.T) {
	fixture := newFixture(t)
	enableAutomaticRetry(t, fixture)
	failed := failAttempt(t, fixture, fixture.started(t), "restart-failure", execution.TerminationOutputLimit)
	restarted := app.NewService(app.Dependencies{Store: fixture.db, Context: app.ContextAssembler{}, Agents: fakeRegistry{fixture.adapter}, Supervisor: fixture.supervisor, Presence: fakePresence{}, Authorizer: allowAuthorizer{}, Clock: &fixedClock{now: time.Now().UTC()}, IDs: app.RandomIDs{}})
	status, err := restarted.AutomaticRetryForRun(context.Background(), failed.Run.ID())
	if err != nil || status == nil || status.State != app.AutomaticRetryPending || status.RetriesUsed != 0 {
		t.Fatalf("restart metadata=%#v err=%v", status, err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	errs := make(chan error, 2)
	go func() {
		defer wg.Done()
		_, err := restarted.PrepareRetry(context.Background(), app.PrepareRetryRequest{CommandMeta: meta("race-auto", "race-auto"), RunID: failed.Run.ID(), ExpectedVersion: failed.Run.Version(), Reason: status.LastFailureReason, Instructions: "auto", Automatic: true})
		errs <- err
	}()
	go func() {
		defer wg.Done()
		_, err := restarted.PrepareRetry(context.Background(), app.PrepareRetryRequest{CommandMeta: meta("race-manual", "race-manual"), RunID: failed.Run.ID(), ExpectedVersion: failed.Run.Version(), Reason: "human retry", Instructions: "manual"})
		errs <- err
	}()
	wg.Wait()
	close(errs)
	successes := 0
	for err := range errs {
		if err == nil {
			successes++
		} else if !errors.Is(err, storecontract.ErrVersionConflict) && !errors.Is(err, app.ErrInvalidCommand) {
			t.Fatalf("unexpected race error=%v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("race successes=%d", successes)
	}
	current, err := fixture.db.Reader().GetCurrentAttempt(context.Background(), failed.Run.ID())
	if err != nil || current.Sequence() != 2 {
		t.Fatalf("race current=%#v err=%v", current, err)
	}
	if err := restarted.BlockAutomaticRetry(context.Background(), failed.Run.ID(), failed.Attempt.ID(), failed.Run.Version(), "stale_dispatch"); !errors.Is(err, storecontract.ErrVersionConflict) {
		t.Fatalf("stale block error=%v", err)
	}
}

func TestAutomaticRetryCanBeCancelledBeforeAndAfterPreparation(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare bool
	}{
		{name: "pending"},
		{name: "prepared", prepare: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFixture(t)
			enableAutomaticRetry(t, fixture)
			failed := failAttempt(t, fixture, fixture.started(t), "cancel-failure-"+test.name, execution.TerminationOutputLimit)
			currentRun := failed.Run
			if test.prepare {
				prepared := prepareAutomaticRetry(t, fixture, failed, "cancel-prepare")
				currentRun = prepared.Run
			}
			cancelled, err := fixture.service.RequestCancel(context.Background(), app.StopRequest{
				CommandMeta: meta("cancel-auto-"+test.name, "cancel-auto-"+test.name), RunID: currentRun.ID(), ExpectedVersion: currentRun.Version(),
				Reason: "Stop automatic continuation.", AllowRetry: true,
			})
			if err != nil || cancelled.Run.State() != domain.RunStateCancelled {
				t.Fatalf("cancelled=%#v err=%v", cancelled, err)
			}
			status, err := fixture.service.AutomaticRetryForRun(context.Background(), currentRun.ID())
			if err != nil || status != nil {
				t.Fatalf("cancel metadata=%#v err=%v", status, err)
			}
		})
	}
}

func TestAutomaticRetryBlockedWithoutCleanupProofCannotUseInactiveCancel(t *testing.T) {
	fixture := newFixture(t)
	enableAutomaticRetry(t, fixture)
	prepared := fixture.prepare(t)
	fixture.supervisor.outcome = execution.StartOutcome{
		Kind: execution.StartReconciliationRequired, Identity: execution.ProcessIdentity{Value: "pid:uncertain"},
		Diagnostic: "runtime identity cannot prove cleanup",
	}
	started, err := fixture.service.StartAttempt(context.Background(), app.StartAttemptRequest{
		CommandMeta: meta("uncertain-auto-start", "uncertain-auto-start"), RunID: prepared.Run.ID(), ExpectedVersion: prepared.Run.Version(), Mode: app.StartFresh,
	})
	if err != nil || started.Run.State() != domain.RunStateRecoveryRequired {
		t.Fatalf("uncertain start=%#v err=%v", started, err)
	}
	status, err := fixture.service.AutomaticRetryForRun(context.Background(), started.Run.ID())
	if err != nil || status == nil || status.State != app.AutomaticRetryBlocked {
		t.Fatalf("uncertain status=%#v err=%v", status, err)
	}
	if err := fixture.service.BlockAutomaticRetry(context.Background(), started.Run.ID(), started.Attempt.ID(), started.Run.Version(), "automatic_retry_start_failed"); err != nil {
		t.Fatal(err)
	}
	status, err = fixture.service.AutomaticRetryForRun(context.Background(), started.Run.ID())
	if err != nil || status == nil || status.BlockedReason != "automatic_retry_start_failed" {
		t.Fatalf("persisted unsafe blocker=%#v err=%v", status, err)
	}
	if _, err := fixture.service.RequestCancel(context.Background(), app.StopRequest{
		CommandMeta: meta("uncertain-auto-cancel", "uncertain-auto-cancel"), RunID: started.Run.ID(), ExpectedVersion: started.Run.Version(), Reason: "must remain fail closed", AllowRetry: true,
	}); err == nil {
		t.Fatal("unproven blocked automatic retry used inactive cancellation")
	}
	preserved, err := fixture.db.Reader().GetRun(context.Background(), started.Run.ID())
	if err != nil || preserved.State() != domain.RunStateRecoveryRequired {
		t.Fatalf("unproven cancellation changed Run=%#v err=%v", preserved, err)
	}
}

func TestAutomaticRetryDoesNotInheritFailedSessionIdentity(t *testing.T) {
	fixture := newFixture(t)
	enableAutomaticRetry(t, fixture)
	started := fixture.started(t)
	fixture.adapter.consumed = 5
	fixture.adapter.decode = []execution.NormalizedEvent{{Type: "agent.session", NormalizedJSON: []byte(`{"safe":true}`), ExternalSession: "123e4567-e89b-12d3-a456-426614174000"}}
	fixture.supervisor.readChunks = []execution.StreamChunk{{Data: []byte("line\n"), NextOffset: 5}}
	fixture.supervisor.sink.Notify(execution.StreamStdout, 5)
	if err := fixture.service.ConsumeStream(context.Background(), app.ConsumeStreamRequest{CommandMeta: meta("session-before-auto", "session-before-auto"), SessionID: started.Session.ID, Stream: execution.StreamStdout}); err != nil {
		t.Fatal(err)
	}
	prior, err := fixture.db.Reader().GetRuntimeSession(context.Background(), started.Session.ID)
	if err != nil || prior.ExternalReference == "" {
		t.Fatalf("missing persisted predecessor session: %v", err)
	}
	fixture.adapter.decode = nil
	fixture.adapter.consumed = 0
	fixture.supervisor.readChunks = nil
	fixture.supervisor.drainOutcome = execution.DrainOutcome{
		Chunks: map[execution.StreamKind][]byte{}, Offsets: execution.StreamOffsets{execution.StreamStdout: 5, execution.StreamStderr: 0},
		EOF:           map[execution.StreamKind]bool{execution.StreamStdout: true, execution.StreamStderr: true},
		TerminalFiles: execution.TerminalFiles{TerminationCause: execution.TerminationOutputLimit},
	}
	fixture.signalExit()
	failed, err := fixture.service.HandleExit(context.Background(), app.ExitRequest{CommandMeta: meta("session-auto-fail", "session-auto-fail"), SessionID: started.Session.ID, ExpectedVersion: started.Run.Version()})
	if err != nil {
		t.Fatal(err)
	}
	prepared := prepareAutomaticRetry(t, fixture, failed, "session-auto-prepare")
	if prepared.Attempt.ExternalSession() != "" {
		t.Fatalf("fresh retry inherited session %q", prepared.Attempt.ExternalSession())
	}
	predecessor, ok := prepared.Attempt.Predecessor()
	if !ok || predecessor != started.Attempt.ID() {
		t.Fatal("retry lost predecessor provenance")
	}
	if prepared.Attempt.ContextSnapshotID() != started.Attempt.ContextSnapshotID() {
		t.Fatal("retry changed frozen context")
	}
	startPreparedRetry(t, fixture, prepared, "session-auto-start")
}
