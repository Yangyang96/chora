package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func (tx *writeTx) InsertTechnicalPlanDraft(ctx context.Context, draft domain.TechnicalPlanDraft) error {
	if err := tx.requireActiveRoomForTask(ctx, draft.TaskID()); err != nil {
		return preserveWriteConflict(err, storecontract.ErrPlanningConflict)
	}
	if !draft.Open() || draft.EditVersion() != 1 {
		return storecontract.ErrPlanningConflict
	}
	content, err := encodePlanContent(draft.Content())
	if err != nil {
		return err
	}
	_, err = tx.tx.ExecContext(ctx, `INSERT INTO technical_plan_drafts(id,task_id,edit_version,predecessor_revision_id,next_revision_number,base_content_digest,selection_digest,content_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		draft.ID().String(), draft.TaskID().String(), draft.EditVersion(), nullablePlanRevisionID(draft.PredecessorRevisionID()), draft.NextRevisionNumber(), nullableDigest(draft.BaseContentDigest()), digestBytes(draft.SelectionDigest()), content, timeText(draft.CreatedAt()), timeText(draft.UpdatedAt()))
	return mapPlanningWriteError(err)
}

func (tx *writeTx) SaveTechnicalPlanDraftCAS(ctx context.Context, expected uint64, next domain.TechnicalPlanDraft) error {
	if err := tx.requireActiveRoomForTechnicalPlanDraft(ctx, next.ID()); err != nil {
		return preserveWriteConflict(err, storecontract.ErrVersionConflict)
	}
	current, err := tx.GetTechnicalPlanDraft(ctx, next.ID())
	if err != nil {
		return planningVersionError(err)
	}
	if !validDraftEdit(current, expected, next) {
		return storecontract.ErrVersionConflict
	}
	content, err := encodePlanContent(next.Content())
	if err != nil {
		return err
	}
	result, err := tx.tx.ExecContext(ctx, `UPDATE technical_plan_drafts SET edit_version=?,content_json=?,updated_at=? WHERE id=? AND edit_version=? AND closed_at IS NULL`, next.EditVersion(), content, timeText(next.UpdatedAt()), next.ID().String(), expected)
	if err != nil {
		return mapPlanningWriteError(err)
	}
	return requireOnePlanningCAS(result)
}

func (tx *writeTx) SubmitTechnicalPlanDraftCAS(ctx context.Context, expected uint64, closed domain.TechnicalPlanDraft, revision domain.TechnicalPlanRevision) error {
	if err := tx.requireActiveRoomForTechnicalPlanDraft(ctx, closed.ID()); err != nil {
		return preserveWriteConflict(err, storecontract.ErrVersionConflict)
	}
	current, err := tx.GetTechnicalPlanDraft(ctx, closed.ID())
	if err != nil {
		return planningVersionError(err)
	}
	if !validDraftSubmission(current, expected, closed, revision) {
		return storecontract.ErrVersionConflict
	}
	content, err := encodePlanContent(revision.Content())
	if err != nil {
		return err
	}
	_, err = tx.tx.ExecContext(ctx, `INSERT INTO technical_plan_revisions(id,task_id,source_draft_id,revision_number,predecessor_revision_id,content_json,content_digest,selection_digest,unchanged,submitted_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		revision.ID().String(), revision.TaskID().String(), revision.SourceDraftID().String(), revision.RevisionNumber(), nullablePlanRevisionID(revision.PredecessorRevisionID()), content, digestBytes(revision.ContentDigest()), digestBytes(revision.SelectionDigest()), revision.Unchanged(), timeText(revision.SubmittedAt()))
	if err != nil {
		return mapPlanningWriteError(err)
	}
	result, err := tx.tx.ExecContext(ctx, `UPDATE technical_plan_drafts SET edit_version=?,updated_at=?,closed_at=?,submitted_revision_id=? WHERE id=? AND edit_version=? AND closed_at IS NULL`,
		closed.EditVersion(), timeText(closed.UpdatedAt()), timeText(closed.ClosedAt()), revision.ID().String(), closed.ID().String(), expected)
	if err != nil {
		return mapPlanningWriteError(err)
	}
	return requireOnePlanningCAS(result)
}

func (tx *writeTx) InsertTechnicalPlanReviewCAS(ctx context.Context, review domain.TechnicalPlanReview) error {
	if err := tx.requireActiveRoomForTechnicalPlanRevision(ctx, review.RevisionID()); err != nil {
		return preserveWriteConflict(err, storecontract.ErrPlanningConflict)
	}
	_, err := tx.tx.ExecContext(ctx, `INSERT INTO technical_plan_reviews(id,revision_id,task_id,kind,reviewer,note,decided_at) VALUES(?,?,?,?,?,?,?)`, review.ID().String(), review.RevisionID().String(), review.TaskID().String(), string(review.Kind()), review.Reviewer(), review.Note(), timeText(review.DecidedAt()))
	return mapPlanningWriteError(err)
}

func (tx *writeTx) InsertTechnicalPlanAcceptance(ctx context.Context, binding domain.TechnicalPlanAcceptanceBinding) error {
	if err := tx.requireActiveRoomForTechnicalPlanRevision(ctx, binding.RevisionID()); err != nil {
		return preserveWriteConflict(err, storecontract.ErrPlanningConflict)
	}
	_, err := tx.tx.ExecContext(ctx, `INSERT INTO technical_plan_acceptance_bindings(review_id,task_id,revision_id,snapshot_id,snapshot_digest,bound_at) VALUES(?,?,?,?,?,?)`, binding.ReviewID().String(), binding.TaskID().String(), binding.RevisionID().String(), binding.SnapshotID().String(), digestBytes(binding.SnapshotDigest()), timeText(binding.BoundAt()))
	if err != nil {
		return mapPlanningWriteError(err)
	}
	_, err = tx.tx.ExecContext(ctx, `INSERT INTO current_technical_plan_acceptances(task_id,revision_id) VALUES(?,?) ON CONFLICT(task_id) DO UPDATE SET revision_id=excluded.revision_id`, binding.TaskID().String(), binding.RevisionID().String())
	return mapPlanningWriteError(err)
}

func (tx *writeTx) InsertTechnicalPlanRunBinding(ctx context.Context, binding domain.TechnicalPlanRunBinding) error {
	if err := tx.requireActiveRoomForRun(ctx, binding.RunID()); err != nil {
		return preserveWriteConflict(err, storecontract.ErrPlanningConflict)
	}
	_, err := tx.tx.ExecContext(ctx, `INSERT INTO technical_plan_run_bindings(run_id,task_id,revision_id,charter_id,snapshot_id,snapshot_digest,bound_at) VALUES(?,?,?,?,?,?,?)`, binding.RunID().String(), binding.TaskID().String(), binding.RevisionID().String(), binding.CharterID().String(), binding.SnapshotID().String(), digestBytes(binding.SnapshotDigest()), timeText(binding.BoundAt()))
	return mapPlanningWriteError(err)
}

func (reader *reader) GetTechnicalPlanDraft(ctx context.Context, id domain.TechnicalPlanDraftID) (domain.TechnicalPlanDraft, error) {
	return reader.scanPlanDraft(reader.q.QueryRowContext(ctx, `SELECT id,task_id,edit_version,predecessor_revision_id,next_revision_number,base_content_digest,selection_digest,content_json,created_at,updated_at,closed_at,submitted_revision_id FROM technical_plan_drafts WHERE id=?`, id.String()))
}

func (reader *reader) GetOpenTechnicalPlanDraft(ctx context.Context, taskID domain.TaskID) (domain.TechnicalPlanDraft, error) {
	return reader.scanPlanDraft(reader.q.QueryRowContext(ctx, `SELECT id,task_id,edit_version,predecessor_revision_id,next_revision_number,base_content_digest,selection_digest,content_json,created_at,updated_at,closed_at,submitted_revision_id FROM technical_plan_drafts WHERE task_id=? AND closed_at IS NULL`, taskID.String()))
}

func (reader *reader) scanPlanDraft(row *sql.Row) (domain.TechnicalPlanDraft, error) {
	var idText, taskText, contentJSON, created, updated string
	var editVersion, nextNumber uint64
	var predecessor, closed, submitted sql.NullString
	var baseDigest, selectionDigest []byte
	if err := row.Scan(&idText, &taskText, &editVersion, &predecessor, &nextNumber, &baseDigest, &selectionDigest, &contentJSON, &created, &updated, &closed, &submitted); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.TechnicalPlanDraft{}, storecontract.ErrNotFound
		}
		return domain.TechnicalPlanDraft{}, err
	}
	id, err := domain.ParseTechnicalPlanDraftID(idText)
	if err != nil {
		return domain.TechnicalPlanDraft{}, err
	}
	taskID, err := domain.ParseTaskID(taskText)
	if err != nil {
		return domain.TechnicalPlanDraft{}, err
	}
	predecessorID, err := parseNullablePlanRevisionID(predecessor)
	if err != nil {
		return domain.TechnicalPlanDraft{}, err
	}
	submittedID, err := parseNullablePlanRevisionID(submitted)
	if err != nil {
		return domain.TechnicalPlanDraft{}, err
	}
	content, err := decodePlanContent(contentJSON)
	if err != nil {
		return domain.TechnicalPlanDraft{}, err
	}
	createdAt, err := parseTime(created)
	if err != nil {
		return domain.TechnicalPlanDraft{}, err
	}
	updatedAt, err := parseTime(updated)
	if err != nil {
		return domain.TechnicalPlanDraft{}, err
	}
	closedAt, err := parseNullableTime(closed)
	if err != nil {
		return domain.TechnicalPlanDraft{}, err
	}
	return domain.RestoreTechnicalPlanDraft(domain.TechnicalPlanDraftRecord{ID: id, TaskID: taskID, EditVersion: editVersion, PredecessorRevisionID: predecessorID, NextRevisionNumber: nextNumber, BaseContentDigest: digest32(baseDigest), SelectionDigest: digest32(selectionDigest), Content: content, CreatedAt: createdAt, UpdatedAt: updatedAt, ClosedAt: closedAt, SubmittedRevisionID: submittedID})
}

func (reader *reader) GetTechnicalPlanRevision(ctx context.Context, id domain.TechnicalPlanRevisionID) (domain.TechnicalPlanRevision, error) {
	return scanPlanRevision(reader.q.QueryRowContext(ctx, `SELECT id,task_id,source_draft_id,revision_number,predecessor_revision_id,content_json,content_digest,selection_digest,unchanged,submitted_at FROM technical_plan_revisions WHERE id=?`, id.String()))
}

func (reader *reader) ListTechnicalPlanRevisions(ctx context.Context, taskID domain.TaskID) ([]domain.TechnicalPlanRevision, error) {
	rows, err := reader.q.QueryContext(ctx, `SELECT id,task_id,source_draft_id,revision_number,predecessor_revision_id,content_json,content_digest,selection_digest,unchanged,submitted_at FROM technical_plan_revisions WHERE task_id=? ORDER BY revision_number`, taskID.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.TechnicalPlanRevision
	for rows.Next() {
		revision, err := scanPlanRevision(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, revision)
	}
	return result, rows.Err()
}

type planRevisionScanner interface{ Scan(...any) error }

func scanPlanRevision(row planRevisionScanner) (domain.TechnicalPlanRevision, error) {
	var idText, taskText, draftText, contentJSON, submitted string
	var number uint64
	var predecessor sql.NullString
	var contentDigest, selectionDigest []byte
	var unchanged bool
	if err := row.Scan(&idText, &taskText, &draftText, &number, &predecessor, &contentJSON, &contentDigest, &selectionDigest, &unchanged, &submitted); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.TechnicalPlanRevision{}, storecontract.ErrNotFound
		}
		return domain.TechnicalPlanRevision{}, err
	}
	id, err := domain.ParseTechnicalPlanRevisionID(idText)
	if err != nil {
		return domain.TechnicalPlanRevision{}, err
	}
	taskID, err := domain.ParseTaskID(taskText)
	if err != nil {
		return domain.TechnicalPlanRevision{}, err
	}
	draftID, err := domain.ParseTechnicalPlanDraftID(draftText)
	if err != nil {
		return domain.TechnicalPlanRevision{}, err
	}
	predecessorID, err := parseNullablePlanRevisionID(predecessor)
	if err != nil {
		return domain.TechnicalPlanRevision{}, err
	}
	content, err := decodePlanContent(contentJSON)
	if err != nil {
		return domain.TechnicalPlanRevision{}, err
	}
	submittedAt, err := parseTime(submitted)
	if err != nil {
		return domain.TechnicalPlanRevision{}, err
	}
	return domain.RestoreTechnicalPlanRevision(domain.TechnicalPlanRevisionRecord{ID: id, TaskID: taskID, SourceDraftID: draftID, RevisionNumber: number, PredecessorRevisionID: predecessorID, Content: content, ContentDigest: digest32(contentDigest), SelectionDigest: digest32(selectionDigest), Unchanged: unchanged, SubmittedAt: submittedAt})
}

func (reader *reader) GetTechnicalPlanReview(ctx context.Context, revisionID domain.TechnicalPlanRevisionID) (domain.TechnicalPlanReview, error) {
	return scanPlanReview(reader.q.QueryRowContext(ctx, `SELECT id,revision_id,task_id,kind,reviewer,note,decided_at FROM technical_plan_reviews WHERE revision_id=?`, revisionID.String()))
}

func (reader *reader) ListTechnicalPlanReviews(ctx context.Context, taskID domain.TaskID) ([]domain.TechnicalPlanReview, error) {
	rows, err := reader.q.QueryContext(ctx, `SELECT id,revision_id,task_id,kind,reviewer,note,decided_at FROM technical_plan_reviews WHERE task_id=? ORDER BY decided_at,id`, taskID.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.TechnicalPlanReview
	for rows.Next() {
		review, err := scanPlanReview(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, review)
	}
	return result, rows.Err()
}

type planReviewScanner interface{ Scan(...any) error }

func scanPlanReview(row planReviewScanner) (domain.TechnicalPlanReview, error) {
	var idText, revisionText, taskText, kind, reviewer, note, decided string
	if err := row.Scan(&idText, &revisionText, &taskText, &kind, &reviewer, &note, &decided); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.TechnicalPlanReview{}, storecontract.ErrNotFound
		}
		return domain.TechnicalPlanReview{}, err
	}
	id, err := domain.ParseTechnicalPlanReviewID(idText)
	if err != nil {
		return domain.TechnicalPlanReview{}, err
	}
	revisionID, err := domain.ParseTechnicalPlanRevisionID(revisionText)
	if err != nil {
		return domain.TechnicalPlanReview{}, err
	}
	taskID, err := domain.ParseTaskID(taskText)
	if err != nil {
		return domain.TechnicalPlanReview{}, err
	}
	decidedAt, err := parseTime(decided)
	if err != nil {
		return domain.TechnicalPlanReview{}, err
	}
	return domain.RestoreTechnicalPlanReview(domain.TechnicalPlanReviewRecord{ID: id, RevisionID: revisionID, TaskID: taskID, Kind: domain.TechnicalPlanReviewKind(kind), Reviewer: reviewer, Note: note, DecidedAt: decidedAt})
}

func (reader *reader) GetCurrentTechnicalPlanAcceptance(ctx context.Context, taskID domain.TaskID) (domain.TechnicalPlanAcceptanceBinding, error) {
	var taskText, revisionText, reviewText, snapshotText, bound string
	var digest []byte
	err := reader.q.QueryRowContext(ctx, `SELECT binding.task_id,binding.revision_id,binding.review_id,binding.snapshot_id,binding.snapshot_digest,binding.bound_at FROM current_technical_plan_acceptances current JOIN technical_plan_acceptance_bindings binding ON binding.task_id=current.task_id AND binding.revision_id=current.revision_id WHERE current.task_id=?`, taskID.String()).Scan(&taskText, &revisionText, &reviewText, &snapshotText, &digest, &bound)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.TechnicalPlanAcceptanceBinding{}, storecontract.ErrNotFound
	}
	if err != nil {
		return domain.TechnicalPlanAcceptanceBinding{}, err
	}
	parsedTask, err := domain.ParseTaskID(taskText)
	if err != nil {
		return domain.TechnicalPlanAcceptanceBinding{}, err
	}
	revisionID, err := domain.ParseTechnicalPlanRevisionID(revisionText)
	if err != nil {
		return domain.TechnicalPlanAcceptanceBinding{}, err
	}
	reviewID, err := domain.ParseTechnicalPlanReviewID(reviewText)
	if err != nil {
		return domain.TechnicalPlanAcceptanceBinding{}, err
	}
	snapshotID, err := domain.ParseContextSnapshotID(snapshotText)
	if err != nil {
		return domain.TechnicalPlanAcceptanceBinding{}, err
	}
	boundAt, err := parseTime(bound)
	if err != nil {
		return domain.TechnicalPlanAcceptanceBinding{}, err
	}
	return domain.NewTechnicalPlanAcceptanceBinding(domain.TechnicalPlanAcceptanceBindingRecord{TaskID: parsedTask, RevisionID: revisionID, ReviewID: reviewID, SnapshotID: snapshotID, SnapshotDigest: digest32(digest), BoundAt: boundAt})
}

func (reader *reader) GetTechnicalPlanRunBinding(ctx context.Context, runID domain.RunID) (domain.TechnicalPlanRunBinding, error) {
	var runText, taskText, revisionText, charterText, snapshotText, bound string
	var digest []byte
	err := reader.q.QueryRowContext(ctx, `SELECT run_id,task_id,revision_id,charter_id,snapshot_id,snapshot_digest,bound_at FROM technical_plan_run_bindings WHERE run_id=?`, runID.String()).Scan(&runText, &taskText, &revisionText, &charterText, &snapshotText, &digest, &bound)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.TechnicalPlanRunBinding{}, storecontract.ErrNotFound
	}
	if err != nil {
		return domain.TechnicalPlanRunBinding{}, err
	}
	parsedRun, err := domain.ParseRunID(runText)
	if err != nil {
		return domain.TechnicalPlanRunBinding{}, err
	}
	taskID, err := domain.ParseTaskID(taskText)
	if err != nil {
		return domain.TechnicalPlanRunBinding{}, err
	}
	revisionID, err := domain.ParseTechnicalPlanRevisionID(revisionText)
	if err != nil {
		return domain.TechnicalPlanRunBinding{}, err
	}
	charterID, err := domain.ParseCharterID(charterText)
	if err != nil {
		return domain.TechnicalPlanRunBinding{}, err
	}
	snapshotID, err := domain.ParseContextSnapshotID(snapshotText)
	if err != nil {
		return domain.TechnicalPlanRunBinding{}, err
	}
	boundAt, err := parseTime(bound)
	if err != nil {
		return domain.TechnicalPlanRunBinding{}, err
	}
	return domain.NewTechnicalPlanRunBinding(domain.TechnicalPlanRunBindingRecord{RunID: parsedRun, TaskID: taskID, RevisionID: revisionID, CharterID: charterID, SnapshotID: snapshotID, SnapshotDigest: digest32(digest), BoundAt: boundAt})
}

func encodePlanContent(content domain.TechnicalPlanContent) (string, error) {
	body, err := json.Marshal(content)
	return string(body), err
}
func decodePlanContent(value string) (domain.TechnicalPlanContent, error) {
	var content domain.TechnicalPlanContent
	err := json.Unmarshal([]byte(value), &content)
	return content, err
}
func nullablePlanRevisionID(id domain.TechnicalPlanRevisionID) any {
	if !id.Valid() {
		return nil
	}
	return id.String()
}
func parseNullablePlanRevisionID(value sql.NullString) (domain.TechnicalPlanRevisionID, error) {
	if !value.Valid {
		return domain.TechnicalPlanRevisionID{}, nil
	}
	return domain.ParseTechnicalPlanRevisionID(value.String)
}
func nullableDigest(value [32]byte) any {
	if value == ([32]byte{}) {
		return nil
	}
	return value[:]
}
func digestBytes(value [32]byte) []byte { return value[:] }
func digest32(value []byte) [32]byte    { var result [32]byte; copy(result[:], value); return result }
func planningVersionError(err error) error {
	if errors.Is(err, storecontract.ErrNotFound) {
		return storecontract.ErrVersionConflict
	}
	return err
}
func mapPlanningWriteError(err error) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	if strings.Contains(message, "UNIQUE constraint failed: technical_plan_reviews.revision_id") {
		return fmt.Errorf("%w: %v", storecontract.ErrVersionConflict, err)
	}
	if strings.Contains(message, "technical plan") || strings.Contains(message, "technical_plan_") || strings.Contains(message, "technical_plan_drafts.task_id") {
		return fmt.Errorf("%w: %v", storecontract.ErrPlanningConflict, err)
	}
	return mapWriteError(err)
}
func requireOnePlanningCAS(result sql.Result) error {
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return storecontract.ErrVersionConflict
	}
	return nil
}

func validDraftEdit(current domain.TechnicalPlanDraft, expected uint64, next domain.TechnicalPlanDraft) bool {
	return current.Open() && next.Open() && current.EditVersion() == expected && next.EditVersion() == expected+1 &&
		current.ID() == next.ID() && current.TaskID() == next.TaskID() && current.PredecessorRevisionID() == next.PredecessorRevisionID() &&
		current.NextRevisionNumber() == next.NextRevisionNumber() && current.BaseContentDigest() == next.BaseContentDigest() && current.SelectionDigest() == next.SelectionDigest() && current.CreatedAt().Equal(next.CreatedAt())
}
func validDraftSubmission(current domain.TechnicalPlanDraft, expected uint64, closed domain.TechnicalPlanDraft, revision domain.TechnicalPlanRevision) bool {
	return current.Open() && !closed.Open() && current.EditVersion() == expected && closed.EditVersion() == expected+1 &&
		current.ID() == closed.ID() && current.TaskID() == closed.TaskID() && current.PredecessorRevisionID() == closed.PredecessorRevisionID() &&
		current.NextRevisionNumber() == closed.NextRevisionNumber() && current.BaseContentDigest() == closed.BaseContentDigest() && current.SelectionDigest() == closed.SelectionDigest() &&
		current.CreatedAt().Equal(closed.CreatedAt()) && domain.CanonicalTechnicalPlanContentDigest(current.Content()) == domain.CanonicalTechnicalPlanContentDigest(closed.Content()) &&
		closed.SubmittedRevisionID() == revision.ID() && revision.SourceDraftID() == closed.ID() &&
		revision.TaskID() == closed.TaskID() && revision.RevisionNumber() == closed.NextRevisionNumber() && revision.PredecessorRevisionID() == closed.PredecessorRevisionID() &&
		revision.SelectionDigest() == closed.SelectionDigest() && revision.ContentDigest() == domain.CanonicalTechnicalPlanContentDigest(closed.Content()) && revision.SubmittedAt().Equal(closed.ClosedAt())
}
