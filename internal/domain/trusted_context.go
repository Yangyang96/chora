package domain

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

type CandidateState string

const (
	CandidateStatePending           CandidateState = "pending"
	CandidateStateConfirmed         CandidateState = "confirmed"
	CandidateStateDismissed         CandidateState = "dismissed"
	MaxCandidateTitleRunes                         = 200
	MaxCandidateBodyRunes                          = 16_000
	MaxCandidateDecisionNoteRunes                  = 2_000
	MaxTrustedContextIdentityRunes                 = 200
	MaxRevisionExclusionReasonRunes                = 2_000
)

// ValidTrustedContextText is the single text-validation contract shared by the
// Domain and SQLite schema. Length is measured in Unicode runes after applying
// strings.TrimSpace, while NUL is always forbidden in persisted product state.
func ValidTrustedContextText(value string, maxRunes int, required bool) bool {
	if maxRunes < 0 || !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
		return false
	}
	trimmed := strings.TrimSpace(value)
	if required && trimmed == "" {
		return false
	}
	return utf8.RuneCountInString(trimmed) <= maxRunes
}

type CandidateParams struct {
	ID               CandidateID
	RoomID           RoomID
	SourceRunID      RunID
	SourceReviewID   ReviewDecisionID
	SourceArtifactID ArtifactID
	Title            string
	Body             string
	CreatedAt        time.Time
}

type Candidate struct {
	id               CandidateID
	roomID           RoomID
	sourceRunID      RunID
	sourceReviewID   ReviewDecisionID
	sourceArtifactID ArtifactID
	title            string
	body             string
	state            CandidateState
	version          uint64
	createdAt        time.Time
	updatedAt        time.Time
}

func NewCandidate(params CandidateParams) (Candidate, error) {
	if !params.ID.Valid() || !params.RoomID.Valid() || !params.SourceRunID.Valid() || !params.SourceReviewID.Valid() || !params.SourceArtifactID.Valid() || !validCandidateContent(params.Title, params.Body) || params.CreatedAt.IsZero() {
		return Candidate{}, fmt.Errorf("%w: invalid candidate", ErrInvalidArgument)
	}
	return Candidate{id: params.ID, roomID: params.RoomID, sourceRunID: params.SourceRunID, sourceReviewID: params.SourceReviewID, sourceArtifactID: params.SourceArtifactID, title: strings.TrimSpace(params.Title), body: strings.TrimSpace(params.Body), state: CandidateStatePending, version: 1, createdAt: params.CreatedAt, updatedAt: params.CreatedAt}, nil
}

type CandidateRecord struct {
	ID               CandidateID
	RoomID           RoomID
	SourceRunID      RunID
	SourceReviewID   ReviewDecisionID
	SourceArtifactID ArtifactID
	Title            string
	Body             string
	State            CandidateState
	Version          uint64
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func RestoreCandidate(record CandidateRecord) (Candidate, error) {
	candidate, err := NewCandidate(CandidateParams{ID: record.ID, RoomID: record.RoomID, SourceRunID: record.SourceRunID, SourceReviewID: record.SourceReviewID, SourceArtifactID: record.SourceArtifactID, Title: record.Title, Body: record.Body, CreatedAt: record.CreatedAt})
	if err != nil || record.Version == 0 || !validCandidateState(record.State) || !validTimestamps(record.CreatedAt, record.UpdatedAt) {
		return Candidate{}, fmt.Errorf("%w: invalid persisted candidate", ErrInvalidArgument)
	}
	candidate.state = record.State
	candidate.version = record.Version
	candidate.updatedAt = record.UpdatedAt
	return candidate, nil
}

func validCandidateState(state CandidateState) bool {
	return state == CandidateStatePending || state == CandidateStateConfirmed || state == CandidateStateDismissed
}

func (candidate Candidate) ID() CandidateID                  { return candidate.id }
func (candidate Candidate) RoomID() RoomID                   { return candidate.roomID }
func (candidate Candidate) SourceRunID() RunID               { return candidate.sourceRunID }
func (candidate Candidate) SourceReviewID() ReviewDecisionID { return candidate.sourceReviewID }
func (candidate Candidate) SourceArtifactID() ArtifactID     { return candidate.sourceArtifactID }
func (candidate Candidate) Title() string                    { return candidate.title }
func (candidate Candidate) Body() string                     { return candidate.body }
func (candidate Candidate) State() CandidateState            { return candidate.state }
func (candidate Candidate) Version() uint64                  { return candidate.version }
func (candidate Candidate) CreatedAt() time.Time             { return candidate.createdAt }
func (candidate Candidate) UpdatedAt() time.Time             { return candidate.updatedAt }

func (candidate Candidate) Edit(expected uint64, title, body string, now time.Time) (Candidate, error) {
	if candidate.state != CandidateStatePending || expected != candidate.version || !validCandidateContent(title, body) || now.Before(candidate.updatedAt) {
		return Candidate{}, fmt.Errorf("%w: candidate edit rejected", ErrInvalidCandidateTransition)
	}
	candidate.title = strings.TrimSpace(title)
	candidate.body = strings.TrimSpace(body)
	candidate.version++
	candidate.updatedAt = now
	return candidate, nil
}

func validCandidateContent(title, body string) bool {
	return ValidTrustedContextText(title, MaxCandidateTitleRunes, true) && ValidTrustedContextText(body, MaxCandidateBodyRunes, true)
}

type CandidateDecisionKind string

const (
	CandidateDecisionConfirm CandidateDecisionKind = "confirm"
	CandidateDecisionDismiss CandidateDecisionKind = "dismiss"
)

type CandidateDecision struct {
	id              CandidateDecisionID
	candidateID     CandidateID
	kind            CandidateDecisionKind
	expectedVersion uint64
	note            string
	actorID         string
	sessionID       string
	decidedAt       time.Time
}

func NewCandidateDecision(id CandidateDecisionID, candidate Candidate, kind CandidateDecisionKind, expected uint64, note, actorID, sessionID string, decidedAt time.Time) (CandidateDecision, Candidate, error) {
	if !ValidTrustedContextText(note, MaxCandidateDecisionNoteRunes, kind == CandidateDecisionDismiss) {
		return CandidateDecision{}, Candidate{}, fmt.Errorf("%w: invalid candidate decision note", ErrInvalidArgument)
	}
	note = strings.TrimSpace(note)
	if !id.Valid() || candidate.state != CandidateStatePending || candidate.version != expected || expected == 0 || !ValidTrustedContextText(actorID, MaxTrustedContextIdentityRunes, true) || !ValidTrustedContextText(sessionID, MaxTrustedContextIdentityRunes, true) || decidedAt.Before(candidate.updatedAt) || (kind != CandidateDecisionConfirm && kind != CandidateDecisionDismiss) {
		return CandidateDecision{}, Candidate{}, fmt.Errorf("%w: candidate decision rejected", ErrInvalidCandidateTransition)
	}
	next := candidate
	if kind == CandidateDecisionConfirm {
		next.state = CandidateStateConfirmed
	} else {
		next.state = CandidateStateDismissed
	}
	next.version++
	next.updatedAt = decidedAt
	decision := CandidateDecision{id: id, candidateID: candidate.id, kind: kind, expectedVersion: expected, note: note, actorID: strings.TrimSpace(actorID), sessionID: strings.TrimSpace(sessionID), decidedAt: decidedAt}
	return decision, next, nil
}

func RestoreCandidateDecision(id CandidateDecisionID, candidateID CandidateID, kind CandidateDecisionKind, expected uint64, note, actorID, sessionID string, decidedAt time.Time) (CandidateDecision, error) {
	if !id.Valid() || !candidateID.Valid() || expected == 0 || !ValidTrustedContextText(note, MaxCandidateDecisionNoteRunes, kind == CandidateDecisionDismiss) || !ValidTrustedContextText(actorID, MaxTrustedContextIdentityRunes, true) || !ValidTrustedContextText(sessionID, MaxTrustedContextIdentityRunes, true) || decidedAt.IsZero() || (kind != CandidateDecisionConfirm && kind != CandidateDecisionDismiss) {
		return CandidateDecision{}, fmt.Errorf("%w: invalid persisted candidate decision", ErrInvalidArgument)
	}
	return CandidateDecision{id: id, candidateID: candidateID, kind: kind, expectedVersion: expected, note: note, actorID: strings.TrimSpace(actorID), sessionID: strings.TrimSpace(sessionID), decidedAt: decidedAt}, nil
}

func (decision CandidateDecision) ID() CandidateDecisionID     { return decision.id }
func (decision CandidateDecision) CandidateID() CandidateID    { return decision.candidateID }
func (decision CandidateDecision) Kind() CandidateDecisionKind { return decision.kind }
func (decision CandidateDecision) ExpectedVersion() uint64     { return decision.expectedVersion }
func (decision CandidateDecision) Note() string                { return decision.note }
func (decision CandidateDecision) ActorID() string             { return decision.actorID }
func (decision CandidateDecision) SessionID() string           { return decision.sessionID }
func (decision CandidateDecision) DecidedAt() time.Time        { return decision.decidedAt }

type RevisionProvenanceKind string

const (
	RevisionProvenanceHumanRoom    RevisionProvenanceKind = "human_room"
	RevisionProvenanceRunCandidate RevisionProvenanceKind = "accepted_run_candidate"
)

type RevisionProvenance struct {
	Kind                RevisionProvenanceKind `json:"kind"`
	CandidateID         string                 `json:"candidate_id,omitempty"`
	CandidateDecisionID string                 `json:"candidate_decision_id,omitempty"`
	SourceRunID         string                 `json:"source_run_id,omitempty"`
	SourceReviewID      string                 `json:"source_review_id,omitempty"`
	SourceArtifactID    string                 `json:"source_artifact_id,omitempty"`
	Actor               string                 `json:"actor"`
}

func HumanRoomProvenance(actor string) RevisionProvenance {
	return RevisionProvenance{Kind: RevisionProvenanceHumanRoom, Actor: strings.TrimSpace(actor)}
}

func CandidateProvenance(candidate Candidate, decision CandidateDecision, actor string) RevisionProvenance {
	return RevisionProvenance{Kind: RevisionProvenanceRunCandidate, CandidateID: candidate.ID().String(), CandidateDecisionID: decision.ID().String(), SourceRunID: candidate.SourceRunID().String(), SourceReviewID: candidate.SourceReviewID().String(), SourceArtifactID: candidate.SourceArtifactID().String(), Actor: strings.TrimSpace(actor)}
}

func (provenance RevisionProvenance) Valid() bool {
	if !ValidTrustedContextText(provenance.Actor, MaxTrustedContextIdentityRunes, true) {
		return false
	}
	if provenance.Kind == RevisionProvenanceHumanRoom {
		return provenance.CandidateID == "" && provenance.CandidateDecisionID == "" && provenance.SourceRunID == "" && provenance.SourceReviewID == "" && provenance.SourceArtifactID == ""
	}
	if provenance.Kind != RevisionProvenanceRunCandidate {
		return false
	}
	_, candidateErr := ParseCandidateID(provenance.CandidateID)
	_, decisionErr := ParseCandidateDecisionID(provenance.CandidateDecisionID)
	_, runErr := ParseRunID(provenance.SourceRunID)
	_, reviewErr := ParseReviewDecisionID(provenance.SourceReviewID)
	_, artifactErr := ParseArtifactID(provenance.SourceArtifactID)
	return candidateErr == nil && decisionErr == nil && runErr == nil && reviewErr == nil && artifactErr == nil
}

type RoomRevision struct {
	revision    RoomContextRevision
	provenance  RevisionProvenance
	digest      [32]byte
	confirmedAt time.Time
}

func NewRoomRevision(revision RoomContextRevision, provenance RevisionProvenance, confirmedAt time.Time) (RoomRevision, error) {
	if !revision.RevisionID().Valid() || revision.RevisionNumber() != 1 || !provenance.Valid() || confirmedAt.IsZero() || !revision.CreatedAt().Equal(confirmedAt) || !revision.UpdatedAt().Equal(confirmedAt) {
		return RoomRevision{}, fmt.Errorf("%w: invalid room revision", ErrInvalidArgument)
	}
	digest := roomRevisionDigest(revision, provenance, confirmedAt)
	return RoomRevision{revision: revision, provenance: provenance, digest: digest, confirmedAt: confirmedAt}, nil
}

func RestoreRoomRevision(revision RoomContextRevision, provenance RevisionProvenance, digest [32]byte, confirmedAt time.Time) (RoomRevision, error) {
	value, err := NewRoomRevision(revision, provenance, confirmedAt)
	if err != nil || digest == ([32]byte{}) || value.digest != digest {
		return RoomRevision{}, fmt.Errorf("%w: invalid persisted room revision", ErrInvalidArgument)
	}
	return value, nil
}

func roomRevisionDigest(revision RoomContextRevision, provenance RevisionProvenance, confirmedAt time.Time) [32]byte {
	document := struct {
		RevisionID, RoomID, EntryID, Kind, Title, Body, Locator, ConfirmedAt string
		Provenance                                                           RevisionProvenance
	}{revision.RevisionID().String(), revision.RoomID().String(), revision.EntryID().String(), string(revision.Kind()), revision.Title(), revision.Body(), revision.Locator(), confirmedAt.UTC().Format(time.RFC3339Nano), provenance}
	encoded, err := json.Marshal(document)
	if err != nil {
		panic(err)
	}
	return sha256.Sum256(encoded)
}

func (revision RoomRevision) Revision() RoomContextRevision  { return revision.revision }
func (revision RoomRevision) ID() ContextRevisionID          { return revision.revision.RevisionID() }
func (revision RoomRevision) RoomID() RoomID                 { return revision.revision.RoomID() }
func (revision RoomRevision) Provenance() RevisionProvenance { return revision.provenance }
func (revision RoomRevision) Digest() [32]byte               { return revision.digest }
func (revision RoomRevision) ConfirmedAt() time.Time         { return revision.confirmedAt }

type RevisionSelectionItem struct {
	RevisionID ContextRevisionID
	Digest     [32]byte
	Provenance RevisionProvenance
}

type RevisionExclusion struct {
	RevisionSelectionItem
	Reason string
}

type TaskRevisionSelection struct {
	taskID    TaskID
	roomID    RoomID
	selected  []RevisionSelectionItem
	excluded  []RevisionExclusion
	createdAt time.Time
}

type canonicalTaskRevisionSelection struct {
	SchemaVersion string                       `json:"schema_version"`
	TaskID        string                       `json:"task_id"`
	RoomID        string                       `json:"room_id"`
	CreatedAt     string                       `json:"created_at"`
	Selected      []canonicalRevisionSelection `json:"selected"`
	Excluded      []canonicalRevisionSelection `json:"excluded"`
}

type canonicalRevisionSelection struct {
	RevisionID string             `json:"revision_id"`
	Digest     string             `json:"digest"`
	Provenance RevisionProvenance `json:"provenance"`
	Reason     string             `json:"reason,omitempty"`
}

func NewTaskRevisionSelection(taskID TaskID, roomID RoomID, selected []RevisionSelectionItem, excluded []RevisionExclusion, createdAt time.Time) (TaskRevisionSelection, error) {
	if !taskID.Valid() || !roomID.Valid() || len(selected) == 0 || createdAt.IsZero() {
		return TaskRevisionSelection{}, fmt.Errorf("%w: explicit revision selection required", ErrInvalidArgument)
	}
	seen := make(map[ContextRevisionID]struct{}, len(selected)+len(excluded))
	for _, item := range selected {
		if !validSelectionItem(item) {
			return TaskRevisionSelection{}, fmt.Errorf("%w: invalid selected revision", ErrInvalidArgument)
		}
		if _, exists := seen[item.RevisionID]; exists {
			return TaskRevisionSelection{}, fmt.Errorf("%w: duplicate revision selection", ErrInvalidArgument)
		}
		seen[item.RevisionID] = struct{}{}
	}
	for _, item := range excluded {
		if !validSelectionItem(item.RevisionSelectionItem) || !ValidTrustedContextText(item.Reason, MaxRevisionExclusionReasonRunes, true) {
			return TaskRevisionSelection{}, fmt.Errorf("%w: invalid excluded revision", ErrInvalidArgument)
		}
		if _, exists := seen[item.RevisionID]; exists {
			return TaskRevisionSelection{}, fmt.Errorf("%w: duplicate revision selection", ErrInvalidArgument)
		}
		seen[item.RevisionID] = struct{}{}
	}
	excludedCopy := append([]RevisionExclusion(nil), excluded...)
	for index := range excludedCopy {
		excludedCopy[index].Reason = strings.TrimSpace(excludedCopy[index].Reason)
	}
	return TaskRevisionSelection{taskID: taskID, roomID: roomID, selected: append([]RevisionSelectionItem(nil), selected...), excluded: excludedCopy, createdAt: createdAt}, nil
}

func validSelectionItem(item RevisionSelectionItem) bool {
	return item.RevisionID.Valid() && item.Digest != ([32]byte{}) && item.Provenance.Valid()
}

func (selection TaskRevisionSelection) TaskID() TaskID { return selection.taskID }
func (selection TaskRevisionSelection) RoomID() RoomID { return selection.roomID }
func (selection TaskRevisionSelection) Selected() []RevisionSelectionItem {
	return append([]RevisionSelectionItem(nil), selection.selected...)
}
func (selection TaskRevisionSelection) Excluded() []RevisionExclusion {
	return append([]RevisionExclusion(nil), selection.excluded...)
}
func (selection TaskRevisionSelection) CreatedAt() time.Time { return selection.createdAt }

// CanonicalJSON freezes the complete selection manifest used by technical plan
// revisions. Slice order is significant because it is also the assembly order.
func (selection TaskRevisionSelection) CanonicalJSON() []byte {
	document := canonicalTaskRevisionSelection{
		SchemaVersion: "chora.task-revision-selection/v1",
		TaskID:        selection.taskID.String(), RoomID: selection.roomID.String(),
		CreatedAt: selection.createdAt.UTC().Format(time.RFC3339Nano),
		Selected:  []canonicalRevisionSelection{}, Excluded: []canonicalRevisionSelection{},
	}
	for _, item := range selection.selected {
		document.Selected = append(document.Selected, canonicalRevisionSelection{
			RevisionID: item.RevisionID.String(), Digest: fmt.Sprintf("%x", item.Digest), Provenance: item.Provenance,
		})
	}
	for _, item := range selection.excluded {
		document.Excluded = append(document.Excluded, canonicalRevisionSelection{
			RevisionID: item.RevisionID.String(), Digest: fmt.Sprintf("%x", item.Digest), Provenance: item.Provenance, Reason: item.Reason,
		})
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		panic(err)
	}
	return encoded
}

func (selection TaskRevisionSelection) Digest() [32]byte {
	return sha256.Sum256(selection.CanonicalJSON())
}

func (selection TaskRevisionSelection) SelectedRevisionIDs() []ContextRevisionID {
	ids := make([]ContextRevisionID, 0, len(selection.selected))
	for _, item := range selection.selected {
		ids = append(ids, item.RevisionID)
	}
	return ids
}
