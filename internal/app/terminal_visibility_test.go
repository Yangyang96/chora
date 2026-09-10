package app

import (
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
)

func TestPiSuccessRequiresCompleteCurrentAttemptResult(t *testing.T) {
	runID, now := domain.NewRunID(), time.Now()
	complete := visibilityEvent(t, runID, 2, "assistant_message", "adapter", `{"text":"done","terminal_complete":true}`, now)
	start := visibilityEvent(t, runID, 1, "attempt.started", "app", `{}`, now)
	if hasCompletePiResult([]domain.RunEvent{complete}) {
		t.Fatal("missing attempt accepted")
	}
	if !hasCompletePiResult([]domain.RunEvent{start, complete}) {
		t.Fatal("complete result rejected")
	}
	if hasCompletePiResult([]domain.RunEvent{start, complete, start}) {
		t.Fatal("predecessor result accepted")
	}
	for _, body := range []string{`{"text":"progress"}`, `{"text":"partial","terminal_complete":true,"truncated":true}`, `{"text":"","terminal_complete":true}`} {
		event := visibilityEvent(t, runID, 3, "assistant_message", "adapter", body, now)
		if hasCompletePiResult([]domain.RunEvent{start, complete, event}) {
			t.Fatalf("incomplete result accepted: %s", body)
		}
	}
}

func TestLatestAttemptAssistantTextDoesNotReusePredecessorReply(t *testing.T) {
	runID := domain.NewRunID()
	now := time.Date(2026, 8, 25, 1, 2, 3, 0, time.UTC)
	events := []domain.RunEvent{
		visibilityEvent(t, runID, 1, "attempt.started", "app", `{}`, now),
		visibilityEvent(t, runID, 2, "assistant_message", "adapter", `{"event_type":"assistant_message","text":"reply from predecessor"}`, now.Add(time.Second)),
		visibilityEvent(t, runID, 3, "attempt.started", "app", `{}`, now.Add(2*time.Second)),
		visibilityEvent(t, runID, 4, "assistant_message", "app", `{"event_type":"assistant_message","text":"untrusted app text"}`, now.Add(3*time.Second)),
	}
	if text, ok := latestAttemptAssistantText(events); ok || text != "" {
		t.Fatalf("predecessor reply leaked into retry: %q", text)
	}
	events = append(events,
		visibilityEvent(t, runID, 5, "assistant_message", "adapter", `{"event_type":"assistant_message","text":"current reply"}`, now.Add(4*time.Second)),
		visibilityEvent(t, runID, 6, "assistant_message", "adapter", `{"event_type":"assistant_message","text":"latest current reply"}`, now.Add(5*time.Second)),
	)
	if text, ok := latestAttemptAssistantText(events); !ok || text != "latest current reply" {
		t.Fatalf("latest reply = %q, %v", text, ok)
	}
}

func TestLatestAttemptAssistantTextRejectsMalformedOrEmptyPayload(t *testing.T) {
	runID := domain.NewRunID()
	now := time.Date(2026, 8, 25, 1, 2, 3, 0, time.UTC)
	events := []domain.RunEvent{
		visibilityEvent(t, runID, 1, "attempt.started", "app", `{}`, now),
		visibilityEvent(t, runID, 2, "assistant_message", "adapter", `{"event_type":"different","text":"wrong type"}`, now.Add(time.Second)),
		visibilityEvent(t, runID, 3, "assistant_message", "adapter", `{"event_type":"assistant_message","text":""}`, now.Add(2*time.Second)),
	}
	if text, ok := latestAttemptAssistantText(events); ok || text != "" {
		t.Fatalf("unsafe reply accepted: %q", text)
	}
}

func visibilityEvent(t *testing.T, runID domain.RunID, sequence int64, eventType, source, body string, occurredAt time.Time) domain.RunEvent {
	t.Helper()
	event, err := domain.NewRunEvent(domain.RunEventParams{
		ID: domain.NewEventID(), RunID: runID, Sequence: sequence, Type: eventType, Source: source,
		OccurredAt: occurredAt, RecordedAt: occurredAt, NormalizedJSON: []byte(body),
	})
	if err != nil {
		t.Fatal(err)
	}
	return event
}
