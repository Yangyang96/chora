package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type RunEventParams struct {
	ID             EventID
	RunID          RunID
	Sequence       int64
	Type           string
	Source         string
	OccurredAt     time.Time
	RecordedAt     time.Time
	NormalizedJSON []byte
	RawJSON        []byte
}

type RunEvent struct {
	id             EventID
	runID          RunID
	sequence       int64
	eventType      string
	source         string
	occurredAt     time.Time
	recordedAt     time.Time
	normalizedJSON []byte
	rawJSON        []byte
}

func NewRunEvent(params RunEventParams) (RunEvent, error) {
	if !params.ID.Valid() || !params.RunID.Valid() || params.Sequence <= 0 || strings.TrimSpace(params.Type) == "" || strings.TrimSpace(params.Source) == "" || !validTimestamps(params.OccurredAt, params.RecordedAt) || len(params.NormalizedJSON) == 0 || !json.Valid(params.NormalizedJSON) || len(params.RawJSON) > 0 && !json.Valid(params.RawJSON) {
		return RunEvent{}, fmt.Errorf("%w: invalid run event", ErrInvalidArgument)
	}
	return RunEvent{id: params.ID, runID: params.RunID, sequence: params.Sequence, eventType: params.Type, source: params.Source, occurredAt: params.OccurredAt, recordedAt: params.RecordedAt, normalizedJSON: append([]byte(nil), params.NormalizedJSON...), rawJSON: append([]byte(nil), params.RawJSON...)}, nil
}

func (event RunEvent) ID() EventID            { return event.id }
func (event RunEvent) RunID() RunID           { return event.runID }
func (event RunEvent) Sequence() int64        { return event.sequence }
func (event RunEvent) Type() string           { return event.eventType }
func (event RunEvent) Source() string         { return event.source }
func (event RunEvent) OccurredAt() time.Time  { return event.occurredAt }
func (event RunEvent) RecordedAt() time.Time  { return event.recordedAt }
func (event RunEvent) NormalizedJSON() []byte { return append([]byte(nil), event.normalizedJSON...) }
func (event RunEvent) RawJSON() []byte        { return append([]byte(nil), event.rawJSON...) }
