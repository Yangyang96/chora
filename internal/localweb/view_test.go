package localweb

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func TestAutomaticRetryViewUsesExactAPIContract(t *testing.T) {
	status := &app.AutomaticRetry{
		State: app.AutomaticRetryPending, RetriesUsed: 1,
		MaxRetries: 2, LastFailureReason: "runtime_output_limit_exceeded",
	}
	view := runSummaryView{ID: domain.NewRunID().String(), AutomaticRetry: automaticRetryViewOf(status)}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		`"automaticRetry":{"state":"pending"`,
		`"retriesUsed":1`,
		`"maxRetries":2`,
		`"lastFailureReason":"runtime_output_limit_exceeded"`,
	} {
		if !strings.Contains(string(encoded), field) {
			t.Fatalf("automatic retry response missing %s: %s", field, encoded)
		}
	}
	action := automaticRetryCurrentAction(currentActionView{Kind: app.CurrentActionRecoverRun}, status)
	if action.Kind != app.CurrentActionMonitorRun {
		t.Fatalf("pending automatic retry action = %q", action.Kind)
	}
	if automaticRetryCurrentAction(currentActionView{Kind: app.CurrentActionRecoverRun}, &app.AutomaticRetry{State: app.AutomaticRetryBlocked}).Kind != app.CurrentActionRecoverRun {
		t.Fatal("blocked automatic retry hid the human recovery action")
	}
	if blocker := automaticRetryBlockedCopy("automatic_retry_route_unavailable"); !strings.Contains(blocker, "Pi execution route") {
		t.Fatalf("route blocker = %q", blocker)
	}
}

func TestRecoveryRetryReadyRequiresInterruptedFinalizedSession(t *testing.T) {
	now := time.Now().UTC()
	ready := storecontract.RuntimeSession{State: "stopped", TerminalAt: now, FinalizedAt: now}
	if !recoveryRetryReady(domain.RunStateRecoveryRequired, domain.AttemptStateInterrupted, ready) {
		t.Fatal("finalized recovery did not expose explicit Retry")
	}
	for _, session := range []storecontract.RuntimeSession{
		{State: "stopped", TerminalAt: now},
		{State: "running", TerminalAt: now, FinalizedAt: now},
	} {
		if recoveryRetryReady(domain.RunStateRecoveryRequired, domain.AttemptStateInterrupted, session) {
			t.Fatalf("unsafe recovery exposed Retry: %#v", session)
		}
	}
	if recoveryRetryReady(domain.RunStateRunning, domain.AttemptStateInterrupted, ready) {
		t.Fatal("non-recovery Run exposed recovery Retry")
	}
}

func TestSafeTerminalReasonExposesOnlyFixedReasons(t *testing.T) {
	now := time.Now().UTC()
	runID := domain.NewRunID()
	event := func(sequence int64, reason string) domain.RunEvent {
		value, err := domain.NewRunEvent(domain.RunEventParams{
			ID: domain.NewEventID(), RunID: runID, Sequence: sequence, Type: "attempt.failed", Source: "application",
			OccurredAt: now, RecordedAt: now, NormalizedJSON: []byte(fmt.Sprintf(`{"failure_reason":%q,"diagnostic":"private raw detail"}`, reason)),
		})
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	for _, reason := range []string{"runtime_stream_invalid", "runtime_output_limit_exceeded"} {
		if got := safeTerminalReason([]domain.RunEvent{event(1, reason)}); got != reason {
			t.Fatalf("reason=%q", got)
		}
	}

	if got := safeTerminalReason([]domain.RunEvent{event(1, "private raw detail")}); got != "" {
		t.Fatalf("unknown terminal reason escaped: %q", got)
	}
	if got := safeTerminalReason([]domain.RunEvent{event(1, "private raw detail"), event(2, "attempt_timeout")}); got != "attempt_timeout" {
		t.Fatalf("safe terminal reason = %q", got)
	}
	if got := safeTerminalReason([]domain.RunEvent{event(1, "agent_model_error")}); got != "agent_model_error" {
		t.Fatalf("safe model terminal reason = %q", got)
	}
}

func TestRecoveryRetryReadyAcceptsOnlyFinalizedProvenNoChildFailure(t *testing.T) {
	now := time.Now().UTC()
	ready := storecontract.RuntimeSession{State: "failed", TerminalAt: now, FinalizedAt: now}
	if !recoveryRetryReady(domain.RunStateRecoveryRequired, domain.AttemptStateFailed, ready) {
		t.Fatal("finalized proven-no-child failure did not expose explicit Retry")
	}
	ready.ProcessIdentity = "unexpected-child"
	if recoveryRetryReady(domain.RunStateRecoveryRequired, domain.AttemptStateFailed, ready) {
		t.Fatal("failed session with child identity exposed Retry")
	}
}

func TestRecoveryRetryReadyAcceptsFinalizedAgentProcessDeath(t *testing.T) {
	now := time.Now().UTC()
	ready := storecontract.RuntimeSession{
		State:           "stopped",
		ProcessIdentity: "container:agent-attempt-1",
		StartedAt:       now,
		TerminalAt:      now,
		FinalizedAt:     now,
	}
	if !recoveryRetryReady(domain.RunStateRecoveryRequired, domain.AttemptStateFailed, ready) {
		t.Fatal("finalized Agent process death did not expose explicit Retry Agent")
	}
	ready.FinalizedAt = time.Time{}
	if recoveryRetryReady(domain.RunStateRecoveryRequired, domain.AttemptStateFailed, ready) {
		t.Fatal("unclean Agent process death exposed Retry Agent")
	}
}

func TestCancelledRetryReadyRequiresExplicitAuthorityAndFinalizedSession(t *testing.T) {
	now := time.Now().UTC()
	ready := storecontract.RuntimeSession{State: "stopped", StopIntent: "cancel", TerminalAt: now, FinalizedAt: now}
	if !cancelledRetryReady(domain.RunStateCancelled, domain.AttemptStateCancelled, ready, true) {
		t.Fatal("authorized finalized cancellation did not expose Retry")
	}
	if cancelledRetryReady(domain.RunStateCancelled, domain.AttemptStateCancelled, ready, false) {
		t.Fatal("cancellation without persisted authority exposed Retry")
	}
	ready.FinalizedAt = time.Time{}
	if cancelledRetryReady(domain.RunStateCancelled, domain.AttemptStateCancelled, ready, true) {
		t.Fatal("unclean cancellation exposed Retry")
	}
}

func TestSafeTerminalReasonUsesCurrentLaunchFailure(t *testing.T) {
	now := time.Now().UTC()
	runID := domain.NewRunID()
	var events []domain.RunEvent
	for i, kind := range []string{"attempt.failed", "run.started", "attempt.start_failed"} {
		event, err := domain.NewRunEvent(domain.RunEventParams{ID: domain.NewEventID(), RunID: runID, Sequence: int64(i + 1), Type: kind, Source: "application", OccurredAt: now, RecordedAt: now, NormalizedJSON: []byte(`{"failure_reason":"runtime_output_limit_exceeded","diagnostic":"private host detail"}`)})
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if got := safeTerminalReason(events[:2]); got != "" {
		t.Fatalf("previous failure survived new launch: %q", got)
	}
	if got := safeTerminalReason(events); got != "runtime_start_failed" {
		t.Fatalf("launch failure = %q", got)
	}
}
