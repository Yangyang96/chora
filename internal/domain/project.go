package domain

import (
	"fmt"
	"strings"
	"time"
)

type ProjectID struct{ idValue }

func NewProjectID() ProjectID { return ProjectID{newIDValue()} }

func ParseProjectID(value string) (ProjectID, error) {
	id, err := parseIDValue(value, "project_")
	return ProjectID{id}, err
}

func (id ProjectID) String() string { return id.idValue.string("project_") }
func (id ProjectID) Valid() bool    { return id.idValue.valid() }

type ProjectState string

const (
	ProjectStateActive   ProjectState = "active"
	ProjectStateArchived ProjectState = "archived"
)

type ProjectParams struct {
	ID            ProjectID
	Name          string
	Description   string
	DefaultRoomID RoomID
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type ProjectRecord struct {
	ID            ProjectID
	Name          string
	Description   string
	State         ProjectState
	Version       uint64
	DefaultRoomID RoomID
	CreatedAt     time.Time
	UpdatedAt     time.Time
	ArchivedAt    time.Time
}

type Project struct {
	id            ProjectID
	name          string
	description   string
	state         ProjectState
	version       uint64
	defaultRoomID RoomID
	createdAt     time.Time
	updatedAt     time.Time
	archivedAt    time.Time
}

func NewProject(params ProjectParams) (Project, error) {
	return RestoreProject(ProjectRecord{ID: params.ID, Name: params.Name, Description: params.Description, State: ProjectStateActive, Version: 1, DefaultRoomID: params.DefaultRoomID, CreatedAt: params.CreatedAt, UpdatedAt: params.UpdatedAt})
}

func RestoreProject(record ProjectRecord) (Project, error) {
	name := strings.TrimSpace(record.Name)
	if !record.ID.Valid() || name == "" || len(record.Description) > 16384 || !record.DefaultRoomID.Valid() || record.Version == 0 || !validTimestamps(record.CreatedAt, record.UpdatedAt) ||
		(record.State != ProjectStateActive && record.State != ProjectStateArchived) ||
		(record.State == ProjectStateActive && !record.ArchivedAt.IsZero()) ||
		(record.State == ProjectStateArchived && (record.ArchivedAt.IsZero() || !record.ArchivedAt.Equal(record.UpdatedAt))) {
		return Project{}, fmt.Errorf("%w: invalid project", ErrInvalidArgument)
	}
	return Project{id: record.ID, name: name, description: record.Description, state: record.State, version: record.Version, defaultRoomID: record.DefaultRoomID, createdAt: record.CreatedAt, updatedAt: record.UpdatedAt, archivedAt: record.ArchivedAt}, nil
}

func (project Project) Rename(name string, expectedVersion uint64, at time.Time) (Project, error) {
	if project.Version() != expectedVersion || at.IsZero() || at.Before(project.UpdatedAt()) || strings.TrimSpace(name) == "" {
		return Project{}, fmt.Errorf("%w: project Rename rejected", ErrInvalidArgument)
	}
	record := project.record()
	record.Name = strings.TrimSpace(name)
	record.Version++
	record.UpdatedAt = at
	if record.State == ProjectStateArchived {
		record.ArchivedAt = at
	}
	return RestoreProject(record)
}

func (project Project) Archive(expectedVersion uint64, at time.Time) (Project, error) {
	if project.State() != ProjectStateActive || project.Version() != expectedVersion || at.IsZero() || at.Before(project.UpdatedAt()) {
		return Project{}, fmt.Errorf("%w: project Archive rejected", ErrInvalidArgument)
	}
	record := project.record()
	record.State, record.Version, record.UpdatedAt, record.ArchivedAt = ProjectStateArchived, record.Version+1, at, at
	return RestoreProject(record)
}

func (project Project) Restore(expectedVersion uint64, at time.Time) (Project, error) {
	if project.State() != ProjectStateArchived || project.Version() != expectedVersion || at.IsZero() || at.Before(project.UpdatedAt()) {
		return Project{}, fmt.Errorf("%w: project Restore rejected", ErrInvalidArgument)
	}
	record := project.record()
	record.State, record.Version, record.UpdatedAt, record.ArchivedAt = ProjectStateActive, record.Version+1, at, time.Time{}
	return RestoreProject(record)
}

func (project Project) record() ProjectRecord {
	return ProjectRecord{ID: project.ID(), Name: project.Name(), Description: project.Description(), State: project.State(), Version: project.Version(), DefaultRoomID: project.DefaultRoomID(), CreatedAt: project.CreatedAt(), UpdatedAt: project.UpdatedAt(), ArchivedAt: project.ArchivedAt()}
}

func (project Project) ID() ProjectID         { return project.id }
func (project Project) Name() string          { return project.name }
func (project Project) State() ProjectState   { return project.state }
func (project Project) Version() uint64       { return project.version }
func (project Project) DefaultRoomID() RoomID { return project.defaultRoomID }
func (project Project) CreatedAt() time.Time  { return project.createdAt }
func (project Project) UpdatedAt() time.Time  { return project.updatedAt }
func (project Project) ArchivedAt() time.Time { return project.archivedAt }

func (project Project) Description() string { return project.description }
