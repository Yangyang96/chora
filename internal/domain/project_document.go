package domain

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const MaxProjectDocumentBytes = 64 * 1024

type ProjectDocumentRevisionID struct{ idValue }
type ProjectDocumentReviewID struct{ idValue }

func NewProjectDocumentRevisionID() ProjectDocumentRevisionID {
	return ProjectDocumentRevisionID{newIDValue()}
}
func NewProjectDocumentReviewID() ProjectDocumentReviewID {
	return ProjectDocumentReviewID{newIDValue()}
}
func ParseProjectDocumentRevisionID(v string) (ProjectDocumentRevisionID, error) {
	id, e := parseIDValue(v, "project_document_revision_")
	return ProjectDocumentRevisionID{id}, e
}
func ParseProjectDocumentReviewID(v string) (ProjectDocumentReviewID, error) {
	id, e := parseIDValue(v, "project_document_review_")
	return ProjectDocumentReviewID{id}, e
}
func (id ProjectDocumentRevisionID) String() string {
	return id.idValue.string("project_document_revision_")
}
func (id ProjectDocumentReviewID) String() string {
	return id.idValue.string("project_document_review_")
}
func (id ProjectDocumentRevisionID) Valid() bool { return id.idValue.valid() }
func (id ProjectDocumentReviewID) Valid() bool   { return id.idValue.valid() }

type ProjectDocumentRevisionKind string

const (
	ProjectDocumentAgentInitial ProjectDocumentRevisionKind = "agent_initial"
	ProjectDocumentHumanEdit    ProjectDocumentRevisionKind = "human_edit"
)

type ProjectDocumentSource struct {
	RunID            RunID
	AttemptID        AttemptID
	ResultID         ResultID
	AgentReportID    AgentReportID
	EventID          EventID
	EventSequence    int64
	ResultDigest     [32]byte
	SourceTextDigest [32]byte
}

type ProjectDocumentRevision struct {
	ID         ProjectDocumentRevisionID
	TaskID     TaskID
	RoomID     RoomID
	Number     uint64
	Kind       ProjectDocumentRevisionKind
	Body       string
	BodyDigest [32]byte
	Source     ProjectDocumentSource
	EditNote   string
	ActorID    string
	CreatedAt  time.Time
}

func NewProjectDocumentRevision(id ProjectDocumentRevisionID, taskID TaskID, roomID RoomID, number uint64, kind ProjectDocumentRevisionKind, body string, source ProjectDocumentSource, note, actor string, at time.Time) (ProjectDocumentRevision, error) {
	if !id.Valid() || !taskID.Valid() || !roomID.Valid() || number == 0 || at.IsZero() || !validProjectDocumentSource(source) || !validProjectDocumentText(body) || !ValidTrustedContextText(note, MaxCandidateDecisionNoteRunes, false) || !ValidTrustedContextText(actor, MaxTrustedContextIdentityRunes, true) || (kind != ProjectDocumentAgentInitial && kind != ProjectDocumentHumanEdit) {
		return ProjectDocumentRevision{}, fmt.Errorf("%w: invalid project document revision", ErrInvalidArgument)
	}
	return ProjectDocumentRevision{ID: id, TaskID: taskID, RoomID: roomID, Number: number, Kind: kind, Body: body, BodyDigest: sha256.Sum256([]byte(body)), Source: source, EditNote: strings.TrimSpace(note), ActorID: strings.TrimSpace(actor), CreatedAt: at}, nil
}

func RestoreProjectDocumentRevision(v ProjectDocumentRevision) (ProjectDocumentRevision, error) {
	restored, err := NewProjectDocumentRevision(v.ID, v.TaskID, v.RoomID, v.Number, v.Kind, v.Body, v.Source, v.EditNote, v.ActorID, v.CreatedAt)
	if err != nil || restored.BodyDigest != v.BodyDigest {
		return ProjectDocumentRevision{}, fmt.Errorf("%w: invalid persisted project document revision", ErrInvalidArgument)
	}
	return restored, nil
}

func validProjectDocumentText(v string) bool {
	return utf8.ValidString(v) && !strings.ContainsRune(v, '\x00') && strings.TrimSpace(v) != "" && len([]byte(strings.TrimSpace(v))) <= MaxProjectDocumentBytes
}
func validProjectDocumentSource(s ProjectDocumentSource) bool {
	return s.RunID.Valid() && s.AttemptID.Valid() && s.ResultID.Valid() && s.AgentReportID.Valid() && s.EventID.Valid() && s.EventSequence > 0 && s.ResultDigest != ([32]byte{}) && s.SourceTextDigest != ([32]byte{})
}

type ProjectDocumentReviewKind string

const (
	ProjectDocumentReviewAccept ProjectDocumentReviewKind = "accept"
	ProjectDocumentReviewReject ProjectDocumentReviewKind = "reject"
)

type ProjectDocumentReview struct {
	ID                       ProjectDocumentReviewID
	RevisionID               ProjectDocumentRevisionID
	TaskID                   TaskID
	ExpectedVersion          uint64
	Kind                     ProjectDocumentReviewKind
	Note, ActorID, SessionID string
	ContextRevisionID        ContextRevisionID
	DecidedAt                time.Time
}

func NewProjectDocumentReview(id ProjectDocumentReviewID, revision ProjectDocumentRevision, expected uint64, kind ProjectDocumentReviewKind, note, actor, session string, contextID ContextRevisionID, at time.Time) (ProjectDocumentReview, error) {
	if !id.Valid() || expected != revision.Number || at.Before(revision.CreatedAt) || !ValidTrustedContextText(note, MaxCandidateDecisionNoteRunes, kind == ProjectDocumentReviewReject) || !ValidTrustedContextText(actor, MaxTrustedContextIdentityRunes, true) || !ValidTrustedContextText(session, MaxTrustedContextIdentityRunes, true) || (kind != ProjectDocumentReviewAccept && kind != ProjectDocumentReviewReject) || (kind == ProjectDocumentReviewAccept) != contextID.Valid() {
		return ProjectDocumentReview{}, fmt.Errorf("%w: invalid project document review", ErrInvalidArgument)
	}
	return ProjectDocumentReview{ID: id, RevisionID: revision.ID, TaskID: revision.TaskID, ExpectedVersion: expected, Kind: kind, Note: strings.TrimSpace(note), ActorID: strings.TrimSpace(actor), SessionID: strings.TrimSpace(session), ContextRevisionID: contextID, DecidedAt: at}, nil
}

func RestoreProjectDocumentReview(v ProjectDocumentReview) (ProjectDocumentReview, error) {
	if !v.ID.Valid() || !v.RevisionID.Valid() || !v.TaskID.Valid() || v.ExpectedVersion == 0 || v.DecidedAt.IsZero() || !ValidTrustedContextText(v.Note, MaxCandidateDecisionNoteRunes, v.Kind == ProjectDocumentReviewReject) || !ValidTrustedContextText(v.ActorID, MaxTrustedContextIdentityRunes, true) || !ValidTrustedContextText(v.SessionID, MaxTrustedContextIdentityRunes, true) || (v.Kind != ProjectDocumentReviewAccept && v.Kind != ProjectDocumentReviewReject) || (v.Kind == ProjectDocumentReviewAccept) != v.ContextRevisionID.Valid() {
		return ProjectDocumentReview{}, fmt.Errorf("%w: invalid persisted project document review", ErrInvalidArgument)
	}
	v.Note, v.ActorID, v.SessionID = strings.TrimSpace(v.Note), strings.TrimSpace(v.ActorID), strings.TrimSpace(v.SessionID)
	return v, nil
}

func ProjectDocumentLocator(taskID TaskID, version uint64) string {
	return fmt.Sprintf("chora://project-document/%s/revisions/%d", taskID.String(), version)
}
