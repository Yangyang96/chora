package domain

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

type RoomParams struct {
	ID            RoomID
	ProjectID     ProjectID
	OwnershipKind RoomOwnershipKind
	Name          string
	Description   string
	WorkspaceRoot string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type RoomOwnershipKind string

const (
	RoomOwnershipProject          RoomOwnershipKind = "project"
	RoomOwnershipLegacyStandalone RoomOwnershipKind = "legacy_standalone"
	RoomOwnershipUnclassified     RoomOwnershipKind = "unclassified"
)

type RoomState string

const (
	RoomStateActive   RoomState = "active"
	RoomStateArchived RoomState = "archived"
)

type RoomRecord struct {
	ID            RoomID
	ProjectID     ProjectID
	OwnershipKind RoomOwnershipKind
	Name          string
	Description   string
	WorkspaceRoot string
	State         RoomState
	Version       uint64
	CreatedAt     time.Time
	UpdatedAt     time.Time
	ArchivedAt    time.Time
}

type Room struct {
	id            RoomID
	projectID     ProjectID
	ownershipKind RoomOwnershipKind
	name          string
	description   string
	workspaceRoot string
	state         RoomState
	version       uint64
	createdAt     time.Time
	updatedAt     time.Time
	archivedAt    time.Time
}

func NewRoom(params RoomParams) (Room, error) {
	return RestoreRoom(RoomRecord{
		ID: params.ID, ProjectID: params.ProjectID, OwnershipKind: normalizedRoomOwnership(params.OwnershipKind), Name: params.Name, Description: params.Description, WorkspaceRoot: params.WorkspaceRoot,
		State: RoomStateActive, Version: 1, CreatedAt: params.CreatedAt, UpdatedAt: params.UpdatedAt,
	})
}

func RestoreRoom(record RoomRecord) (Room, error) {
	if !record.ID.Valid() || strings.TrimSpace(record.Name) == "" || !canonicalAbsolutePath(record.WorkspaceRoot) || !validRoomOwnership(record.ProjectID, record.OwnershipKind) ||
		!validTimestamps(record.CreatedAt, record.UpdatedAt) || record.Version == 0 ||
		(record.State != RoomStateActive && record.State != RoomStateArchived) ||
		(record.State == RoomStateActive && !record.ArchivedAt.IsZero()) ||
		(record.State == RoomStateArchived && (record.ArchivedAt.IsZero() || record.ArchivedAt.Before(record.CreatedAt) || !record.ArchivedAt.Equal(record.UpdatedAt))) {
		return Room{}, fmt.Errorf("%w: invalid room", ErrInvalidArgument)
	}
	return Room{
		id: record.ID, projectID: record.ProjectID, ownershipKind: record.OwnershipKind, name: record.Name, description: record.Description, workspaceRoot: record.WorkspaceRoot,
		state: record.State, version: record.Version, createdAt: record.CreatedAt, updatedAt: record.UpdatedAt, archivedAt: record.ArchivedAt,
	}, nil
}

func (room Room) Archive(expectedVersion uint64, at time.Time) (Room, error) {
	if room.State() != RoomStateActive || room.Version() != expectedVersion || at.IsZero() || at.Before(room.UpdatedAt()) {
		return Room{}, fmt.Errorf("%w: room Archive rejected", ErrInvalidArgument)
	}
	record := room.record()
	record.State = RoomStateArchived
	record.Version++
	record.UpdatedAt = at
	record.ArchivedAt = at
	return RestoreRoom(record)
}

func (room Room) Rename(name, description string, expectedVersion uint64, at time.Time) (Room, error) {
	if room.Version() != expectedVersion || strings.TrimSpace(name) == "" || at.IsZero() || at.Before(room.UpdatedAt()) {
		return Room{}, fmt.Errorf("%w: room Rename rejected", ErrInvalidArgument)
	}
	record := room.record()
	record.Name = strings.TrimSpace(name)
	record.Description = description
	record.Version++
	record.UpdatedAt = at
	if record.State == RoomStateArchived {
		record.ArchivedAt = at
	}
	return RestoreRoom(record)
}

func (room Room) Restore(expectedVersion uint64, at time.Time) (Room, error) {
	if room.State() != RoomStateArchived || room.Version() != expectedVersion || at.IsZero() || at.Before(room.UpdatedAt()) {
		return Room{}, fmt.Errorf("%w: room Restore rejected", ErrInvalidArgument)
	}
	record := room.record()
	record.State = RoomStateActive
	record.Version++
	record.UpdatedAt = at
	record.ArchivedAt = time.Time{}
	return RestoreRoom(record)
}

func (room Room) record() RoomRecord {
	return RoomRecord{
		ID: room.ID(), ProjectID: room.ProjectID(), OwnershipKind: room.OwnershipKind(), Name: room.Name(), Description: room.Description(), WorkspaceRoot: room.WorkspaceRoot(),
		State: room.State(), Version: room.Version(), CreatedAt: room.CreatedAt(), UpdatedAt: room.UpdatedAt(), ArchivedAt: room.ArchivedAt(),
	}
}

func (room Room) ID() RoomID                       { return room.id }
func (room Room) ProjectID() ProjectID             { return room.projectID }
func (room Room) OwnershipKind() RoomOwnershipKind { return room.ownershipKind }
func (room Room) Name() string                     { return room.name }
func (room Room) Description() string              { return room.description }
func (room Room) WorkspaceRoot() string            { return room.workspaceRoot }
func (room Room) State() RoomState                 { return room.state }
func (room Room) Version() uint64                  { return room.version }
func (room Room) CreatedAt() time.Time             { return room.createdAt }
func (room Room) UpdatedAt() time.Time             { return room.updatedAt }
func (room Room) ArchivedAt() time.Time            { return room.archivedAt }

func canonicalAbsolutePath(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path
}

func normalizedRoomOwnership(kind RoomOwnershipKind) RoomOwnershipKind {
	if kind == "" {
		return RoomOwnershipUnclassified
	}
	return kind
}

func validRoomOwnership(projectID ProjectID, kind RoomOwnershipKind) bool {
	switch kind {
	case RoomOwnershipProject:
		return projectID.Valid()
	case RoomOwnershipLegacyStandalone, RoomOwnershipUnclassified:
		return !projectID.Valid()
	default:
		return false
	}
}

func validTimestamps(createdAt, updatedAt time.Time) bool {
	return !createdAt.IsZero() && !updatedAt.IsZero() && !updatedAt.Before(createdAt)
}
