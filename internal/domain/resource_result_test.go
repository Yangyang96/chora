package domain

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestResourceFinalAssistantBindsLastCompleteCurrentAttempt(t *testing.T) {
	now := time.Now().UTC()
	run := NewRunID()
	event := func(sequence int64, kind string, payload map[string]any) RunEvent {
		raw, _ := json.Marshal(payload)
		source := "adapter"
		if kind == "attempt.started" {
			source = "app"
		}
		e, err := NewRunEvent(RunEventParams{ID: NewEventID(), RunID: run, Sequence: sequence, Type: kind, Source: source, OccurredAt: now, RecordedAt: now, NormalizedJSON: raw})
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	start := event(1, "attempt.started", map[string]any{})
	complete := event(2, "assistant_message", map[string]any{"text": "exact final text", "terminal_complete": true})
	proof, text, ok := CurrentCompleteAssistantEvidence([]RunEvent{start, complete})
	if !ok || text != "exact final text" || proof.EventID != complete.ID().String() || proof.Sequence != 2 || proof.TextDigest != fmt.Sprintf("%x", sha256.Sum256([]byte(text))) {
		t.Fatalf("bad final binding: %#v %q", proof, text)
	}
	for _, later := range []RunEvent{
		event(3, "assistant_message", map[string]any{"text": "cut off", "terminal_complete": true, "truncated": true}),
		event(3, "assistant_message", map[string]any{"text": "progress", "terminal_complete": false}),
		event(3, "attempt.started", map[string]any{}),
	} {
		if _, _, ok := CurrentCompleteAssistantEvidence([]RunEvent{start, complete, later}); ok {
			t.Fatal("earlier final message escaped newer incomplete response/Attempt")
		}
	}
	if _, _, ok := CurrentCompleteAssistantEvidence([]RunEvent{complete}); ok {
		t.Fatal("assistant without current Attempt accepted")
	}
}
