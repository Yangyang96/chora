package domain

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	MaxTaskPlanTextRunes       = 16_000
	MaxTaskPlanReviewNoteRunes = 2_000
)

type TechnicalPlanContent struct {
	TechnicalSteps []string `json:"technical_steps"`
	Decisions      []string `json:"decisions"`
	Risks          []string `json:"risks"`
	Unknowns       []string `json:"unknowns"`
}

type NewTechnicalPlanDraftParams struct {
	ID              TechnicalPlanDraftID
	TaskID          TaskID
	SelectionDigest [32]byte
	Content         TechnicalPlanContent
	CreatedAt       time.Time
}

type TechnicalPlanDraftRecord struct {
	ID                    TechnicalPlanDraftID
	TaskID                TaskID
	EditVersion           uint64
	PredecessorRevisionID TechnicalPlanRevisionID
	NextRevisionNumber    uint64
	BaseContentDigest     [32]byte
	SelectionDigest       [32]byte
	Content               TechnicalPlanContent
	CreatedAt             time.Time
	UpdatedAt             time.Time
	ClosedAt              time.Time
	SubmittedRevisionID   TechnicalPlanRevisionID
}

type TechnicalPlanDraft struct{ record TechnicalPlanDraftRecord }

func NewTechnicalPlanDraft(params NewTechnicalPlanDraftParams) (TechnicalPlanDraft, error) {
	return RestoreTechnicalPlanDraft(TechnicalPlanDraftRecord{
		ID: params.ID, TaskID: params.TaskID, EditVersion: 1, NextRevisionNumber: 1,
		SelectionDigest: params.SelectionDigest, Content: params.Content,
		CreatedAt: params.CreatedAt, UpdatedAt: params.CreatedAt,
	})
}

func NewTechnicalPlanSuccessorDraft(id TechnicalPlanDraftID, predecessor TechnicalPlanRevision, at time.Time) (TechnicalPlanDraft, error) {
	if !predecessor.ID().Valid() {
		return TechnicalPlanDraft{}, invalidPlanning("invalid predecessor")
	}
	return RestoreTechnicalPlanDraft(TechnicalPlanDraftRecord{
		ID: id, TaskID: predecessor.TaskID(), EditVersion: 1, PredecessorRevisionID: predecessor.ID(),
		NextRevisionNumber: predecessor.RevisionNumber() + 1, BaseContentDigest: predecessor.ContentDigest(),
		SelectionDigest: predecessor.SelectionDigest(), Content: predecessor.Content(), CreatedAt: at, UpdatedAt: at,
	})
}

func RestoreTechnicalPlanDraft(record TechnicalPlanDraftRecord) (TechnicalPlanDraft, error) {
	record.Content = clonePlanContent(record.Content)
	if !validDraftRecord(record) {
		return TechnicalPlanDraft{}, invalidPlanning("invalid persisted technical plan draft")
	}
	return TechnicalPlanDraft{record: record}, nil
}

func (draft TechnicalPlanDraft) Edit(expected uint64, content TechnicalPlanContent, at time.Time) (TechnicalPlanDraft, error) {
	if !draft.Open() || expected != draft.EditVersion() || !validPlanContent(content) || !validPlanTime(at) || at.Before(draft.UpdatedAt()) {
		return TechnicalPlanDraft{}, invalidPlanning("technical plan draft edit rejected")
	}
	next := draft.record
	next.EditVersion++
	next.Content = clonePlanContent(content)
	next.UpdatedAt = at
	return RestoreTechnicalPlanDraft(next)
}

func (draft TechnicalPlanDraft) Submit(expected uint64, revisionID TechnicalPlanRevisionID, confirmUnchanged bool, at time.Time) (TechnicalPlanDraft, TechnicalPlanRevision, error) {
	if !draft.Open() || expected != draft.EditVersion() || !revisionID.Valid() || !validPlanTime(at) || at.Before(draft.UpdatedAt()) {
		return TechnicalPlanDraft{}, TechnicalPlanRevision{}, invalidPlanning("technical plan draft submission rejected")
	}
	digest := CanonicalTechnicalPlanContentDigest(draft.Content())
	unchanged := draft.PredecessorRevisionID().Valid() && digest == draft.BaseContentDigest()
	if unchanged != confirmUnchanged {
		return TechnicalPlanDraft{}, TechnicalPlanRevision{}, invalidPlanning("unchanged submission confirmation mismatch")
	}
	revision, err := RestoreTechnicalPlanRevision(TechnicalPlanRevisionRecord{
		ID: revisionID, TaskID: draft.TaskID(), SourceDraftID: draft.ID(), RevisionNumber: draft.NextRevisionNumber(),
		PredecessorRevisionID: draft.PredecessorRevisionID(), Content: draft.Content(), ContentDigest: digest,
		SelectionDigest: draft.SelectionDigest(), Unchanged: unchanged, SubmittedAt: at,
	})
	if err != nil {
		return TechnicalPlanDraft{}, TechnicalPlanRevision{}, err
	}
	closedRecord := draft.record
	closedRecord.EditVersion++
	closedRecord.UpdatedAt = at
	closedRecord.ClosedAt = at
	closedRecord.SubmittedRevisionID = revisionID
	closed, err := RestoreTechnicalPlanDraft(closedRecord)
	return closed, revision, err
}

func (draft TechnicalPlanDraft) ID() TechnicalPlanDraftID { return draft.record.ID }
func (draft TechnicalPlanDraft) TaskID() TaskID           { return draft.record.TaskID }
func (draft TechnicalPlanDraft) EditVersion() uint64      { return draft.record.EditVersion }
func (draft TechnicalPlanDraft) PredecessorRevisionID() TechnicalPlanRevisionID {
	return draft.record.PredecessorRevisionID
}
func (draft TechnicalPlanDraft) NextRevisionNumber() uint64  { return draft.record.NextRevisionNumber }
func (draft TechnicalPlanDraft) BaseContentDigest() [32]byte { return draft.record.BaseContentDigest }
func (draft TechnicalPlanDraft) SelectionDigest() [32]byte   { return draft.record.SelectionDigest }
func (draft TechnicalPlanDraft) Content() TechnicalPlanContent {
	return clonePlanContent(draft.record.Content)
}
func (draft TechnicalPlanDraft) CreatedAt() time.Time { return draft.record.CreatedAt }
func (draft TechnicalPlanDraft) UpdatedAt() time.Time { return draft.record.UpdatedAt }
func (draft TechnicalPlanDraft) ClosedAt() time.Time  { return draft.record.ClosedAt }
func (draft TechnicalPlanDraft) SubmittedRevisionID() TechnicalPlanRevisionID {
	return draft.record.SubmittedRevisionID
}
func (draft TechnicalPlanDraft) Open() bool { return draft.record.ClosedAt.IsZero() }

type TechnicalPlanRevisionRecord struct {
	ID                    TechnicalPlanRevisionID
	TaskID                TaskID
	SourceDraftID         TechnicalPlanDraftID
	RevisionNumber        uint64
	PredecessorRevisionID TechnicalPlanRevisionID
	Content               TechnicalPlanContent
	ContentDigest         [32]byte
	SelectionDigest       [32]byte
	Unchanged             bool
	SubmittedAt           time.Time
}

type TechnicalPlanRevision struct{ record TechnicalPlanRevisionRecord }

func RestoreTechnicalPlanRevision(record TechnicalPlanRevisionRecord) (TechnicalPlanRevision, error) {
	record.Content = clonePlanContent(record.Content)
	if !record.ID.Valid() || !record.TaskID.Valid() || !record.SourceDraftID.Valid() || record.RevisionNumber == 0 ||
		!validPlanContent(record.Content) || record.ContentDigest == ([32]byte{}) || record.ContentDigest != CanonicalTechnicalPlanContentDigest(record.Content) ||
		record.SelectionDigest == ([32]byte{}) || !validPlanTime(record.SubmittedAt) ||
		(record.RevisionNumber == 1 && (record.PredecessorRevisionID.Valid() || record.Unchanged)) ||
		(record.RevisionNumber > 1 && !record.PredecessorRevisionID.Valid()) {
		return TechnicalPlanRevision{}, invalidPlanning("invalid persisted technical plan revision")
	}
	return TechnicalPlanRevision{record: record}, nil
}

func (revision TechnicalPlanRevision) ID() TechnicalPlanRevisionID { return revision.record.ID }
func (revision TechnicalPlanRevision) TaskID() TaskID              { return revision.record.TaskID }
func (revision TechnicalPlanRevision) SourceDraftID() TechnicalPlanDraftID {
	return revision.record.SourceDraftID
}
func (revision TechnicalPlanRevision) RevisionNumber() uint64 { return revision.record.RevisionNumber }
func (revision TechnicalPlanRevision) PredecessorRevisionID() TechnicalPlanRevisionID {
	return revision.record.PredecessorRevisionID
}
func (revision TechnicalPlanRevision) Content() TechnicalPlanContent {
	return clonePlanContent(revision.record.Content)
}
func (revision TechnicalPlanRevision) ContentDigest() [32]byte { return revision.record.ContentDigest }
func (revision TechnicalPlanRevision) SelectionDigest() [32]byte {
	return revision.record.SelectionDigest
}
func (revision TechnicalPlanRevision) Unchanged() bool        { return revision.record.Unchanged }
func (revision TechnicalPlanRevision) SubmittedAt() time.Time { return revision.record.SubmittedAt }

type TechnicalPlanReviewKind string

const (
	TechnicalPlanReviewAccept          TechnicalPlanReviewKind = "accept"
	TechnicalPlanReviewRequestRevision TechnicalPlanReviewKind = "request_revision"
)

type NewTechnicalPlanReviewParams struct {
	ID             TechnicalPlanReviewID
	RevisionID     TechnicalPlanRevisionID
	TaskID         TaskID
	Kind           TechnicalPlanReviewKind
	Reviewer, Note string
	DecidedAt      time.Time
}

type TechnicalPlanReviewRecord = NewTechnicalPlanReviewParams
type TechnicalPlanReview struct{ record TechnicalPlanReviewRecord }

func NewTechnicalPlanReview(params NewTechnicalPlanReviewParams) (TechnicalPlanReview, error) {
	params.Reviewer = strings.TrimSpace(params.Reviewer)
	params.Note = strings.TrimSpace(params.Note)
	if !params.ID.Valid() || !params.RevisionID.Valid() || !params.TaskID.Valid() ||
		(params.Kind != TechnicalPlanReviewAccept && params.Kind != TechnicalPlanReviewRequestRevision) ||
		!ValidTrustedContextText(params.Reviewer, MaxTrustedContextIdentityRunes, true) ||
		!ValidTrustedContextText(params.Note, MaxTaskPlanReviewNoteRunes, true) || !validPlanTime(params.DecidedAt) {
		return TechnicalPlanReview{}, invalidPlanning("invalid technical plan review")
	}
	return TechnicalPlanReview{record: params}, nil
}

func RestoreTechnicalPlanReview(record TechnicalPlanReviewRecord) (TechnicalPlanReview, error) {
	return NewTechnicalPlanReview(record)
}
func (review TechnicalPlanReview) ID() TechnicalPlanReviewID { return review.record.ID }
func (review TechnicalPlanReview) RevisionID() TechnicalPlanRevisionID {
	return review.record.RevisionID
}
func (review TechnicalPlanReview) TaskID() TaskID                { return review.record.TaskID }
func (review TechnicalPlanReview) Kind() TechnicalPlanReviewKind { return review.record.Kind }
func (review TechnicalPlanReview) Reviewer() string              { return review.record.Reviewer }
func (review TechnicalPlanReview) Note() string                  { return review.record.Note }
func (review TechnicalPlanReview) DecidedAt() time.Time          { return review.record.DecidedAt }

type TechnicalPlanAcceptanceBindingRecord struct {
	TaskID         TaskID
	RevisionID     TechnicalPlanRevisionID
	ReviewID       TechnicalPlanReviewID
	SnapshotID     ContextSnapshotID
	SnapshotDigest [32]byte
	BoundAt        time.Time
}
type TechnicalPlanAcceptanceBinding struct {
	record TechnicalPlanAcceptanceBindingRecord
}

func NewTechnicalPlanAcceptanceBinding(record TechnicalPlanAcceptanceBindingRecord) (TechnicalPlanAcceptanceBinding, error) {
	if !record.TaskID.Valid() || !record.RevisionID.Valid() || !record.ReviewID.Valid() || !record.SnapshotID.Valid() || record.SnapshotDigest == ([32]byte{}) || !validPlanTime(record.BoundAt) {
		return TechnicalPlanAcceptanceBinding{}, invalidPlanning("invalid technical plan acceptance binding")
	}
	return TechnicalPlanAcceptanceBinding{record: record}, nil
}
func (binding TechnicalPlanAcceptanceBinding) TaskID() TaskID { return binding.record.TaskID }
func (binding TechnicalPlanAcceptanceBinding) RevisionID() TechnicalPlanRevisionID {
	return binding.record.RevisionID
}
func (binding TechnicalPlanAcceptanceBinding) ReviewID() TechnicalPlanReviewID {
	return binding.record.ReviewID
}
func (binding TechnicalPlanAcceptanceBinding) SnapshotID() ContextSnapshotID {
	return binding.record.SnapshotID
}
func (binding TechnicalPlanAcceptanceBinding) SnapshotDigest() [32]byte {
	return binding.record.SnapshotDigest
}
func (binding TechnicalPlanAcceptanceBinding) BoundAt() time.Time { return binding.record.BoundAt }

type TechnicalPlanRunBindingRecord struct {
	RunID          RunID
	TaskID         TaskID
	RevisionID     TechnicalPlanRevisionID
	CharterID      CharterID
	SnapshotID     ContextSnapshotID
	SnapshotDigest [32]byte
	BoundAt        time.Time
}
type TechnicalPlanRunBinding struct{ record TechnicalPlanRunBindingRecord }

func NewTechnicalPlanRunBinding(record TechnicalPlanRunBindingRecord) (TechnicalPlanRunBinding, error) {
	if !record.RunID.Valid() || !record.TaskID.Valid() || !record.RevisionID.Valid() || !record.CharterID.Valid() || !record.SnapshotID.Valid() || record.SnapshotDigest == ([32]byte{}) || !validPlanTime(record.BoundAt) {
		return TechnicalPlanRunBinding{}, invalidPlanning("invalid technical plan run binding")
	}
	return TechnicalPlanRunBinding{record: record}, nil
}
func (binding TechnicalPlanRunBinding) RunID() RunID   { return binding.record.RunID }
func (binding TechnicalPlanRunBinding) TaskID() TaskID { return binding.record.TaskID }
func (binding TechnicalPlanRunBinding) RevisionID() TechnicalPlanRevisionID {
	return binding.record.RevisionID
}
func (binding TechnicalPlanRunBinding) CharterID() CharterID { return binding.record.CharterID }
func (binding TechnicalPlanRunBinding) SnapshotID() ContextSnapshotID {
	return binding.record.SnapshotID
}
func (binding TechnicalPlanRunBinding) SnapshotDigest() [32]byte {
	return binding.record.SnapshotDigest
}
func (binding TechnicalPlanRunBinding) BoundAt() time.Time { return binding.record.BoundAt }

func CanonicalTechnicalPlanContentDigest(content TechnicalPlanContent) [32]byte {
	canonical, err := json.Marshal(content)
	if err != nil {
		panic(fmt.Sprintf("canonical technical plan content: %v", err))
	}
	return sha256.Sum256(canonical)
}

func validDraftRecord(record TechnicalPlanDraftRecord) bool {
	if !record.ID.Valid() || !record.TaskID.Valid() || record.EditVersion == 0 || record.NextRevisionNumber == 0 ||
		record.SelectionDigest == ([32]byte{}) || !validPlanContent(record.Content) || !validPlanTime(record.CreatedAt) ||
		!validPlanTime(record.UpdatedAt) || record.UpdatedAt.Before(record.CreatedAt) ||
		(record.NextRevisionNumber == 1 && (record.PredecessorRevisionID.Valid() || record.BaseContentDigest != ([32]byte{}))) ||
		(record.NextRevisionNumber > 1 && (!record.PredecessorRevisionID.Valid() || record.BaseContentDigest == ([32]byte{}))) {
		return false
	}
	if record.ClosedAt.IsZero() {
		return !record.SubmittedRevisionID.Valid()
	}
	return record.SubmittedRevisionID.Valid() && validPlanTime(record.ClosedAt) && record.ClosedAt.Equal(record.UpdatedAt)
}

func validPlanContent(content TechnicalPlanContent) bool {
	return validPlanTexts(content.TechnicalSteps) && validPlanTexts(content.Decisions) && validPlanTexts(content.Risks) && validPlanTexts(content.Unknowns)
}
func validPlanTime(value time.Time) bool { return !value.IsZero() && value.Location() == time.UTC }
func validPlanText(value string) bool {
	return ValidTrustedContextText(value, MaxTaskPlanTextRunes, true)
}
func validPlanTexts(values []string) bool {
	if len(values) == 0 {
		return false
	}
	for _, value := range values {
		if !validPlanText(value) {
			return false
		}
	}
	return true
}
func clonePlanContent(content TechnicalPlanContent) TechnicalPlanContent {
	return TechnicalPlanContent{TechnicalSteps: cloneStrings(content.TechnicalSteps), Decisions: cloneStrings(content.Decisions), Risks: cloneStrings(content.Risks), Unknowns: cloneStrings(content.Unknowns)}
}
func cloneStrings(values []string) []string { return append([]string(nil), values...) }
func invalidPlanning(message string) error  { return fmt.Errorf("%w: %s", ErrInvalidArgument, message) }
