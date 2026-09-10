package domain

import (
	"errors"
	"testing"
	"time"
)

func TestNewRoomValidatesCanonicalWorkspaceAndTimestamps(t *testing.T) {
	now := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		room    RoomParams
		wantErr bool
	}{
		{"valid", RoomParams{ID: NewRoomID(), Name: "Chora", Description: "shared room", WorkspaceRoot: "/tmp/chora", CreatedAt: now, UpdatedAt: now}, false},
		{"empty name", RoomParams{ID: NewRoomID(), WorkspaceRoot: "/tmp/chora", CreatedAt: now, UpdatedAt: now}, true},
		{"relative root", RoomParams{ID: NewRoomID(), Name: "Chora", WorkspaceRoot: "tmp/chora", CreatedAt: now, UpdatedAt: now}, true},
		{"non canonical root", RoomParams{ID: NewRoomID(), Name: "Chora", WorkspaceRoot: "/tmp/../tmp/chora", CreatedAt: now, UpdatedAt: now}, true},
		{"zero timestamp", RoomParams{ID: NewRoomID(), Name: "Chora", WorkspaceRoot: "/tmp/chora", UpdatedAt: now}, true},
		{"updated before created", RoomParams{ID: NewRoomID(), Name: "Chora", WorkspaceRoot: "/tmp/chora", CreatedAt: now, UpdatedAt: now.Add(-time.Second)}, true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			room, err := NewRoom(test.room)
			if test.wantErr {
				if !errors.Is(err, ErrInvalidArgument) {
					t.Fatalf("NewRoom() error = %v, want ErrInvalidArgument", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewRoom() error = %v", err)
			}
			if room.Name() != test.room.Name || room.WorkspaceRoot() != test.room.WorkspaceRoot {
				t.Fatalf("room = %#v, want name/root preserved", room)
			}
		})
	}
}

func TestRoomArchiveRestoreLifecycleIsExplicitVersionedAndReversible(t *testing.T) {
	createdAt := time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC)
	room, err := NewRoom(RoomParams{ID: NewRoomID(), Name: "Lifecycle", WorkspaceRoot: "/tmp/chora-lifecycle", CreatedAt: createdAt, UpdatedAt: createdAt})
	if err != nil {
		t.Fatal(err)
	}
	if room.State() != RoomStateActive || room.Version() != 1 || !room.ArchivedAt().IsZero() {
		t.Fatalf("new Room lifecycle = state=%q version=%d archivedAt=%s", room.State(), room.Version(), room.ArchivedAt())
	}
	archivedAt := createdAt.Add(time.Minute)
	archived, err := room.Archive(1, archivedAt)
	if err != nil {
		t.Fatal(err)
	}
	if archived.State() != RoomStateArchived || archived.Version() != 2 || !archived.ArchivedAt().Equal(archivedAt) || !archived.UpdatedAt().Equal(archivedAt) {
		t.Fatalf("archived Room = %#v", archived)
	}
	if _, err := room.Archive(0, archivedAt); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("stale Archive err=%v", err)
	}
	if _, err := archived.Archive(2, archivedAt.Add(time.Second)); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("repeated Archive err=%v", err)
	}
	restoredAt := archivedAt.Add(time.Minute)
	restored, err := archived.Restore(2, restoredAt)
	if err != nil {
		t.Fatal(err)
	}
	if restored.State() != RoomStateActive || restored.Version() != 3 || !restored.ArchivedAt().IsZero() || !restored.UpdatedAt().Equal(restoredAt) {
		t.Fatalf("restored Room = %#v", restored)
	}
	if _, err := archived.Restore(1, restoredAt); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("stale Restore err=%v", err)
	}
	if _, err := restored.Restore(3, restoredAt.Add(time.Second)); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("repeated Restore err=%v", err)
	}
}

func TestRestoreRoomRejectsImpossibleLifecycleRecords(t *testing.T) {
	now := time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC)
	base := RoomRecord{ID: NewRoomID(), Name: "Persisted", WorkspaceRoot: "/tmp/chora-persisted", State: RoomStateActive, Version: 1, CreatedAt: now, UpdatedAt: now}
	tests := []struct {
		name   string
		mutate func(RoomRecord) RoomRecord
	}{
		{"unknown state", func(record RoomRecord) RoomRecord { record.State = "unknown"; return record }},
		{"zero version", func(record RoomRecord) RoomRecord { record.Version = 0; return record }},
		{"active archived timestamp", func(record RoomRecord) RoomRecord { record.ArchivedAt = now; return record }},
		{"archived missing timestamp", func(record RoomRecord) RoomRecord { record.State = RoomStateArchived; return record }},
		{"archived timestamp before creation", func(record RoomRecord) RoomRecord {
			record.State = RoomStateArchived
			record.ArchivedAt = now.Add(-time.Second)
			return record
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := RestoreRoom(test.mutate(base)); !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("RestoreRoom() err=%v", err)
			}
		})
	}
}
