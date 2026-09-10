package app

import (
	"encoding/json"
	"testing"

	"github.com/Yangyang96/chora/internal/execution"
)

func TestFinalResourceCheckSelectionUsesOnlyFinalCompleteAssistantMessage(t *testing.T) {
	selection := selectionTestEvent(t, "```chora-check-selection\n{\"repositories\":[]}\n```", true, false)
	progress := selectionTestEvent(t, "Still working", false, false)
	if _, found, err := finalResourceCheckSelection([]execution.NormalizedEvent{selection, progress}); err != nil || found {
		t.Fatalf("non-final selection was accepted: found=%v err=%v", found, err)
	}
	truncated := selectionTestEvent(t, "```chora-check-selection\n{\"repositories\":[]}\n```", true, true)
	if _, found, err := finalResourceCheckSelection([]execution.NormalizedEvent{truncated}); err == nil || found {
		t.Fatalf("truncated final selection was accepted: found=%v err=%v", found, err)
	}
}

func TestFinalResourceCheckSelectionRequiresStrictBoundedJSONFence(t *testing.T) {
	for _, text := range []string{
		"```chora-check-selection\n{\"repositories\":[]}\n``` trailing",
		"```chora-check-selection\n{\"repositories\":[],\"unknown\":true}\n```",
		"```chora-check-selection\n{}\n```\n```chora-check-selection\n{}\n```",
	} {
		if _, found, err := finalResourceCheckSelection([]execution.NormalizedEvent{selectionTestEvent(t, text, true, false)}); err == nil || found {
			t.Fatalf("invalid selection fence accepted: found=%v err=%v text=%q", found, err, text)
		}
	}
}

func selectionTestEvent(t *testing.T, text string, complete, truncated bool) execution.NormalizedEvent {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"event_type": "assistant_message", "text": text,
		"terminal_complete": complete, "truncated": truncated,
	})
	if err != nil {
		t.Fatal(err)
	}
	return execution.NormalizedEvent{Type: "assistant_message", NormalizedJSON: raw}
}
