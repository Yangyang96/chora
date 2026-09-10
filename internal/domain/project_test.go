package domain

import (
	"errors"
	"testing"
	"time"
)

func TestProjectIdentityRenameAndLifecycle(t *testing.T) {
	now := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	project, err := NewProject(ProjectParams{ID: NewProjectID(), Name: "Initial", DefaultRoomID: NewRoomID(), CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	renamed, err := project.Rename("Renamed", 1, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if renamed.ID() != project.ID() || renamed.DefaultRoomID() != project.DefaultRoomID() || renamed.Version() != 2 {
		t.Fatalf("rename changed identity: %#v", renamed)
	}
	archived, err := renamed.Archive(2, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	restored, err := archived.Restore(3, now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if restored.State() != ProjectStateActive || restored.Version() != 4 {
		t.Fatalf("restore=%#v", restored)
	}
	if _, err = project.Archive(99, now); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("stale archive error=%v", err)
	}
}

func TestRoomOwnershipRequiresProjectOnlyForProjectKind(t *testing.T) {
	now := time.Now().UTC()
	base := RoomParams{ID: NewRoomID(), Name: "Room", WorkspaceRoot: "/tmp/chora-room", CreatedAt: now, UpdatedAt: now}
	base.OwnershipKind = RoomOwnershipProject
	if _, err := NewRoom(base); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("project Room without owner error=%v", err)
	}
	base.ProjectID = NewProjectID()
	room, err := NewRoom(base)
	if err != nil {
		t.Fatal(err)
	}
	if room.OwnershipKind() != RoomOwnershipProject || !room.ProjectID().Valid() {
		t.Fatalf("ownership=%#v", room)
	}
	base.OwnershipKind = RoomOwnershipUnclassified
	if _, err = NewRoom(base); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("unclassified Room with owner error=%v", err)
	}
}
