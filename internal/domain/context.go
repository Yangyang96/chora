package domain

import (
	"fmt"
	"strings"
	"time"
)

type ContextKind string

const (
	ContextKindBrief      ContextKind = "brief"
	ContextKindDecision   ContextKind = "decision"
	ContextKindConstraint ContextKind = "constraint"
	ContextKindSourceRef  ContextKind = "source_ref"
	ContextKindUnknown    ContextKind = "unknown"
)

type RoomContextRevisionParams struct {
	EntryID        ContextEntryID
	RevisionID     ContextRevisionID
	RoomID         RoomID
	Kind           ContextKind
	RevisionNumber int
	Title          string
	Body           string
	Locator        string
	Sensitive      bool
	Supersedes     *ContextRevisionID
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type RoomContextRevision struct {
	entryID        ContextEntryID
	revisionID     ContextRevisionID
	roomID         RoomID
	kind           ContextKind
	revisionNumber int
	title          string
	body           string
	locator        string
	sensitive      bool
	supersedes     ContextRevisionID
	hasSupersedes  bool
	createdAt      time.Time
	updatedAt      time.Time
}

func NewRoomContextRevision(params RoomContextRevisionParams) (RoomContextRevision, error) {
	if !params.EntryID.Valid() || !params.RevisionID.Valid() || !params.RoomID.Valid() || !params.Kind.valid() || params.RevisionNumber <= 0 || strings.TrimSpace(params.Title) == "" || !validTimestamps(params.CreatedAt, params.UpdatedAt) {
		return RoomContextRevision{}, fmt.Errorf("%w: invalid room context revision", ErrInvalidArgument)
	}
	revision := RoomContextRevision{
		entryID: params.EntryID, revisionID: params.RevisionID, roomID: params.RoomID, kind: params.Kind,
		revisionNumber: params.RevisionNumber, title: params.Title, body: params.Body, locator: params.Locator,
		sensitive: params.Sensitive, createdAt: params.CreatedAt, updatedAt: params.UpdatedAt,
	}
	if params.Supersedes != nil {
		if !params.Supersedes.Valid() {
			return RoomContextRevision{}, fmt.Errorf("%w: invalid superseded revision", ErrInvalidArgument)
		}
		revision.supersedes = *params.Supersedes
		revision.hasSupersedes = true
	}
	return revision, nil
}

func (kind ContextKind) valid() bool {
	switch kind {
	case ContextKindBrief, ContextKindDecision, ContextKindConstraint, ContextKindSourceRef, ContextKindUnknown:
		return true
	default:
		return false
	}
}

func (revision RoomContextRevision) EntryID() ContextEntryID       { return revision.entryID }
func (revision RoomContextRevision) RevisionID() ContextRevisionID { return revision.revisionID }
func (revision RoomContextRevision) RoomID() RoomID                { return revision.roomID }
func (revision RoomContextRevision) Kind() ContextKind             { return revision.kind }
func (revision RoomContextRevision) RevisionNumber() int           { return revision.revisionNumber }
func (revision RoomContextRevision) Title() string                 { return revision.title }
func (revision RoomContextRevision) Body() string                  { return revision.body }
func (revision RoomContextRevision) Locator() string               { return revision.locator }
func (revision RoomContextRevision) Sensitive() bool               { return revision.sensitive }
func (revision RoomContextRevision) CreatedAt() time.Time          { return revision.createdAt }
func (revision RoomContextRevision) UpdatedAt() time.Time          { return revision.updatedAt }
func (revision RoomContextRevision) Supersedes() (ContextRevisionID, bool) {
	return revision.supersedes, revision.hasSupersedes
}

type RunContextSnapshot struct {
	id                  ContextSnapshotID
	includedRevisionIDs []ContextRevisionID
	excludedEntryIDs    []ContextEntryID
	digest              [32]byte
}

func NewRunContextSnapshot(id ContextSnapshotID, digest [32]byte, includedRevisionIDs []ContextRevisionID, excludedEntryIDs []ContextEntryID) (RunContextSnapshot, error) {
	if !id.Valid() || digest == ([32]byte{}) || !allContextRevisionIDsValid(includedRevisionIDs) || !allContextEntryIDsValid(excludedEntryIDs) {
		return RunContextSnapshot{}, fmt.Errorf("%w: invalid run context snapshot", ErrInvalidArgument)
	}
	return RunContextSnapshot{
		id:                  id,
		includedRevisionIDs: append([]ContextRevisionID(nil), includedRevisionIDs...),
		excludedEntryIDs:    append([]ContextEntryID(nil), excludedEntryIDs...), digest: digest,
	}, nil
}

func (snapshot RunContextSnapshot) ID() ContextSnapshotID { return snapshot.id }
func (snapshot RunContextSnapshot) IncludedRevisionIDs() []ContextRevisionID {
	return append([]ContextRevisionID(nil), snapshot.includedRevisionIDs...)
}
func (snapshot RunContextSnapshot) ExcludedEntryIDs() []ContextEntryID {
	return append([]ContextEntryID(nil), snapshot.excludedEntryIDs...)
}
func (snapshot RunContextSnapshot) Digest() [32]byte { return snapshot.digest }

func allContextRevisionIDsValid(ids []ContextRevisionID) bool {
	for _, id := range ids {
		if !id.Valid() {
			return false
		}
	}
	return true
}

func allContextEntryIDsValid(ids []ContextEntryID) bool {
	for _, id := range ids {
		if !id.Valid() {
			return false
		}
	}
	return true
}

func uniqueContextRevisionIDs(ids []ContextRevisionID) []ContextRevisionID {
	unique := make([]ContextRevisionID, 0, len(ids))
	seen := make(map[ContextRevisionID]struct{}, len(ids))
	for _, id := range ids {
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	return unique
}
