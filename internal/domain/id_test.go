package domain

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestTypedIDsRoundTripAsUUIDv7(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
		newID  func() string
		parse  func(string) (string, error)
	}{
		{"RoomID", "room_", func() string { return NewRoomID().String() }, func(value string) (string, error) { id, err := ParseRoomID(value); return id.String(), err }},
		{"TaskID", "task_", func() string { return NewTaskID().String() }, func(value string) (string, error) { id, err := ParseTaskID(value); return id.String(), err }},
		{"CriterionID", "criterion_", func() string { return NewCriterionID().String() }, func(value string) (string, error) { id, err := ParseCriterionID(value); return id.String(), err }},
		{"RunID", "run_", func() string { return NewRunID().String() }, func(value string) (string, error) { id, err := ParseRunID(value); return id.String(), err }},
		{"AttemptID", "attempt_", func() string { return NewAttemptID().String() }, func(value string) (string, error) { id, err := ParseAttemptID(value); return id.String(), err }},
		{"EventID", "event_", func() string { return NewEventID().String() }, func(value string) (string, error) { id, err := ParseEventID(value); return id.String(), err }},
		{"ContextEntryID", "context_entry_", func() string { return NewContextEntryID().String() }, func(value string) (string, error) { id, err := ParseContextEntryID(value); return id.String(), err }},
		{"ContextRevisionID", "context_revision_", func() string { return NewContextRevisionID().String() }, func(value string) (string, error) { id, err := ParseContextRevisionID(value); return id.String(), err }},
		{"CharterID", "charter_", func() string { return NewCharterID().String() }, func(value string) (string, error) { id, err := ParseCharterID(value); return id.String(), err }},
		{"ContextSnapshotID", "context_snapshot_", func() string { return NewContextSnapshotID().String() }, func(value string) (string, error) { id, err := ParseContextSnapshotID(value); return id.String(), err }},
		{"ReviewDecisionID", "review_decision_", func() string { return NewReviewDecisionID().String() }, func(value string) (string, error) { id, err := ParseReviewDecisionID(value); return id.String(), err }},
		{"ArtifactID", "artifact_", func() string { return NewArtifactID().String() }, func(value string) (string, error) { id, err := ParseArtifactID(value); return id.String(), err }},
		{"TechnicalPlanDraftID", "plan_draft_", func() string { return NewTechnicalPlanDraftID().String() }, func(value string) (string, error) {
			id, err := ParseTechnicalPlanDraftID(value)
			return id.String(), err
		}},
		{"TechnicalPlanRevisionID", "plan_revision_", func() string { return NewTechnicalPlanRevisionID().String() }, func(value string) (string, error) {
			id, err := ParseTechnicalPlanRevisionID(value)
			return id.String(), err
		}},
		{"TechnicalPlanReviewID", "plan_review_", func() string { return NewTechnicalPlanReviewID().String() }, func(value string) (string, error) {
			id, err := ParseTechnicalPlanReviewID(value)
			return id.String(), err
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := test.newID()
			if !strings.HasPrefix(value, test.prefix) {
				t.Fatalf("new ID = %q, want prefix %q", value, test.prefix)
			}
			parsedUUID, err := uuid.Parse(strings.TrimPrefix(value, test.prefix))
			if err != nil {
				t.Fatalf("parse UUID payload: %v", err)
			}
			if parsedUUID.Version() != 7 {
				t.Fatalf("UUID version = %d, want 7", parsedUUID.Version())
			}
			got, err := test.parse(value)
			if err != nil {
				t.Fatalf("parse generated ID: %v", err)
			}
			if got != value {
				t.Fatalf("round trip = %q, want %q", got, value)
			}
		})
	}
}

func TestIDParsingRejectsEmptyWrongPrefixAndNonV7(t *testing.T) {
	validTask := NewTaskID().String()
	v4 := "room_" + uuid.New().String()
	canonical := NewRoomID().String()
	payload := strings.TrimPrefix(canonical, "room_")
	parsed, err := uuid.Parse(payload)
	if err != nil {
		t.Fatal(err)
	}
	nonRFC := parsed
	nonRFC[8] &= 0x3f
	tests := []struct {
		name  string
		value string
	}{
		{"empty", ""},
		{"wrong type prefix", validTask},
		{"malformed UUID", "room_not-a-uuid"},
		{"UUIDv4", v4},
		{"raw UUID", "room_" + strings.ReplaceAll(payload, "-", "")},
		{"braced UUID", "room_{" + payload + "}"},
		{"URN UUID", "room_urn:uuid:" + payload},
		{"uppercase UUID", "room_" + strings.ToUpper(payload)},
		{"non RFC 4122 variant", "room_" + nonRFC.String()},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ParseRoomID(test.value); !errors.Is(err, ErrInvalidID) {
				t.Fatalf("ParseRoomID(%q) error = %v, want ErrInvalidID", test.value, err)
			}
		})
	}
}

func TestZeroIDIsInvalid(t *testing.T) {
	var id RoomID
	if id.Valid() {
		t.Fatal("zero RoomID reported valid")
	}
	if id.String() != "" {
		t.Fatalf("zero RoomID String() = %q, want empty", id.String())
	}
}
