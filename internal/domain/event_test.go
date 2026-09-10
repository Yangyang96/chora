package domain

import (
	"errors"
	"testing"
	"time"
)

func TestRunEventRequiresMonotonicSequenceAndCopiesPayloads(t *testing.T) {
	now := time.Now().UTC()
	normalized := []byte(`{"state":"ready"}`)
	raw := []byte(`{"raw":true}`)
	event, err := NewRunEvent(RunEventParams{
		ID: NewEventID(), RunID: NewRunID(), Sequence: 1, Type: "run.ready", Source: "application",
		OccurredAt: now, RecordedAt: now.Add(time.Millisecond), NormalizedJSON: normalized, RawJSON: raw,
	})
	if err != nil {
		t.Fatalf("NewRunEvent() error = %v", err)
	}
	normalized[0] = '['
	raw[0] = '['
	if string(event.NormalizedJSON()) != `{"state":"ready"}` || string(event.RawJSON()) != `{"raw":true}` {
		t.Fatal("event aliases caller payload bytes")
	}
	returned := event.NormalizedJSON()
	returned[0] = '['
	if string(event.NormalizedJSON()) != `{"state":"ready"}` {
		t.Fatal("NormalizedJSON returns mutable internal bytes")
	}
}

func TestRunEventRejectsInvalidSequenceJSONAndTimestamps(t *testing.T) {
	now := time.Now().UTC()
	base := RunEventParams{ID: NewEventID(), RunID: NewRunID(), Sequence: 1, Type: "run.ready", Source: "application", OccurredAt: now, RecordedAt: now, NormalizedJSON: []byte(`{}`)}
	tests := []struct {
		name   string
		mutate func(*RunEventParams)
	}{
		{"sequence", func(params *RunEventParams) { params.Sequence = 0 }},
		{"normalized JSON", func(params *RunEventParams) { params.NormalizedJSON = []byte(`{`) }},
		{"raw JSON", func(params *RunEventParams) { params.RawJSON = []byte(`{`) }},
		{"recorded before occurred", func(params *RunEventParams) { params.RecordedAt = now.Add(-time.Second) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			params := base
			test.mutate(&params)
			if _, err := NewRunEvent(params); !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("error = %v, want ErrInvalidArgument", err)
			}
		})
	}
}
