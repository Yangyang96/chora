package domain

import (
	"fmt"
	"strings"
	"time"
)

type RepositorySourceKind string

const (
	RepositorySourceKindOpen  RepositorySourceKind = "open"
	RepositorySourceKindClone RepositorySourceKind = "clone"
)

type RepositoryBindingState string

const (
	RepositoryBindingStateActive  RepositoryBindingState = "active"
	RepositoryBindingStateRemoved RepositoryBindingState = "removed"
)

type RepositoryBindingParams struct {
	RoomID         RoomID
	Name           string
	LocalLocator   string
	SourceKind     RepositorySourceKind
	CloneURL       string
	AdmittedBase   string
	BaseIdentity   string
	TargetWorktree string
	DirtyAdmitted  bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type RepositoryBindingRecord struct {
	RoomID         RoomID
	Name           string
	LocalLocator   string
	SourceKind     RepositorySourceKind
	CloneURL       string
	AdmittedBase   string
	BaseIdentity   string
	TargetWorktree string
	DirtyAdmitted  bool
	State          RepositoryBindingState
	Version        uint64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type RepositoryBinding struct {
	roomID         RoomID
	name           string
	localLocator   string
	sourceKind     RepositorySourceKind
	cloneURL       string
	admittedBase   string
	baseIdentity   string
	targetWorktree string
	dirtyAdmitted  bool
	state          RepositoryBindingState
	version        uint64
	createdAt      time.Time
	updatedAt      time.Time
}

func NewRepositoryBinding(params RepositoryBindingParams) (RepositoryBinding, error) {
	return RestoreRepositoryBinding(RepositoryBindingRecord{
		RoomID: params.RoomID, Name: params.Name, LocalLocator: params.LocalLocator, SourceKind: params.SourceKind,
		CloneURL: params.CloneURL, AdmittedBase: params.AdmittedBase, BaseIdentity: params.BaseIdentity,
		TargetWorktree: params.TargetWorktree, DirtyAdmitted: params.DirtyAdmitted,
		State: RepositoryBindingStateActive, Version: 1, CreatedAt: params.CreatedAt, UpdatedAt: params.UpdatedAt,
	})
}

func RestoreRepositoryBinding(record RepositoryBindingRecord) (RepositoryBinding, error) {
	if !record.RoomID.Valid() || strings.TrimSpace(record.Name) == "" || !canonicalAbsolutePath(record.LocalLocator) ||
		!canonicalAbsolutePath(record.TargetWorktree) || !validRepositorySourceKind(record.SourceKind) ||
		(record.SourceKind == RepositorySourceKindOpen && record.CloneURL != "") ||
		(record.SourceKind == RepositorySourceKindClone && strings.TrimSpace(record.CloneURL) == "") ||
		strings.TrimSpace(record.AdmittedBase) == "" || !validBaseIdentity(record.BaseIdentity) ||
		(record.State != RepositoryBindingStateActive && record.State != RepositoryBindingStateRemoved) ||
		record.Version == 0 || !validTimestamps(record.CreatedAt, record.UpdatedAt) {
		return RepositoryBinding{}, fmt.Errorf("%w: invalid repository binding", ErrInvalidArgument)
	}
	return RepositoryBinding{
		roomID: record.RoomID, name: record.Name, localLocator: record.LocalLocator, sourceKind: record.SourceKind,
		cloneURL: record.CloneURL, admittedBase: record.AdmittedBase, baseIdentity: record.BaseIdentity,
		targetWorktree: record.TargetWorktree, dirtyAdmitted: record.DirtyAdmitted, state: record.State,
		version: record.Version, createdAt: record.CreatedAt, updatedAt: record.UpdatedAt,
	}, nil
}

func validRepositorySourceKind(kind RepositorySourceKind) bool {
	return kind == RepositorySourceKindOpen || kind == RepositorySourceKindClone
}

func validBaseIdentity(value string) bool {
	if !strings.HasPrefix(value, "sha:") {
		return false
	}
	tree := strings.Index(value, ":tree:")
	return tree > len("sha:") && len(value) > tree+len(":tree:")
}

func (binding RepositoryBinding) Remove(expectedVersion uint64, at time.Time) (RepositoryBinding, error) {
	if binding.State() != RepositoryBindingStateActive || binding.Version() != expectedVersion || at.IsZero() || at.Before(binding.UpdatedAt()) {
		return RepositoryBinding{}, fmt.Errorf("%w: repository binding Remove rejected", ErrInvalidArgument)
	}
	record := binding.record()
	record.State = RepositoryBindingStateRemoved
	record.Version++
	record.UpdatedAt = at
	return RestoreRepositoryBinding(record)
}

func (binding RepositoryBinding) record() RepositoryBindingRecord {
	return RepositoryBindingRecord{
		RoomID: binding.RoomID(), Name: binding.Name(), LocalLocator: binding.LocalLocator(), SourceKind: binding.SourceKind(),
		CloneURL: binding.CloneURL(), AdmittedBase: binding.AdmittedBase(), BaseIdentity: binding.BaseIdentity(),
		TargetWorktree: binding.TargetWorktree(), DirtyAdmitted: binding.DirtyAdmitted(), State: binding.State(),
		Version: binding.Version(), CreatedAt: binding.CreatedAt(), UpdatedAt: binding.UpdatedAt(),
	}
}

func (binding RepositoryBinding) RoomID() RoomID                   { return binding.roomID }
func (binding RepositoryBinding) Name() string                     { return binding.name }
func (binding RepositoryBinding) LocalLocator() string             { return binding.localLocator }
func (binding RepositoryBinding) SourceKind() RepositorySourceKind { return binding.sourceKind }
func (binding RepositoryBinding) CloneURL() string                 { return binding.cloneURL }
func (binding RepositoryBinding) AdmittedBase() string             { return binding.admittedBase }
func (binding RepositoryBinding) BaseIdentity() string             { return binding.baseIdentity }
func (binding RepositoryBinding) TargetWorktree() string           { return binding.targetWorktree }
func (binding RepositoryBinding) DirtyAdmitted() bool              { return binding.dirtyAdmitted }
func (binding RepositoryBinding) State() RepositoryBindingState    { return binding.state }
func (binding RepositoryBinding) Version() uint64                  { return binding.version }
func (binding RepositoryBinding) CreatedAt() time.Time             { return binding.createdAt }
func (binding RepositoryBinding) UpdatedAt() time.Time             { return binding.updatedAt }
