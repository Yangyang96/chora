package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func (tx *writeTx) InsertProjectDocumentRevision(ctx context.Context, v domain.ProjectDocumentRevision) error {
	_, err := tx.tx.ExecContext(ctx, `INSERT INTO project_document_revisions(id,task_id,room_id,revision_number,kind,body,body_digest,source_run_id,source_attempt_id,source_result_id,source_agent_report_id,source_event_id,source_event_sequence,source_result_digest,source_text_digest,edit_note,actor_id,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, v.ID.String(), v.TaskID.String(), v.RoomID.String(), v.Number, string(v.Kind), v.Body, v.BodyDigest[:], v.Source.RunID.String(), v.Source.AttemptID.String(), v.Source.ResultID.String(), v.Source.AgentReportID.String(), v.Source.EventID.String(), v.Source.EventSequence, v.Source.ResultDigest[:], v.Source.SourceTextDigest[:], v.EditNote, v.ActorID, v.CreatedAt.UTC().Format(time.RFC3339Nano))
	if containsConstraint(err, "project_document_revisions.task_id, project_document_revisions.revision_number") || containsConstraint(err, "project_document_revisions.task_id, project_document_revisions.source_attempt_id") {
		return storecontract.ErrVersionConflict
	}
	return mapWriteError(err)
}

func (tx *writeTx) InsertProjectDocumentReview(ctx context.Context, v domain.ProjectDocumentReview) error {
	var contextID any
	if v.ContextRevisionID.Valid() {
		contextID = v.ContextRevisionID.String()
	}
	_, err := tx.tx.ExecContext(ctx, `INSERT INTO project_document_reviews(id,revision_id,task_id,expected_version,kind,note,actor_id,session_id,context_revision_id,decided_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, v.ID.String(), v.RevisionID.String(), v.TaskID.String(), v.ExpectedVersion, string(v.Kind), v.Note, v.ActorID, v.SessionID, contextID, v.DecidedAt.UTC().Format(time.RFC3339Nano))
	if containsConstraint(err, "project_document_reviews.revision_id") {
		return storecontract.ErrVersionConflict
	}
	return mapWriteError(err)
}

func (tx *writeTx) CloseTaskForAcceptedProjectDocument(ctx context.Context, taskID domain.TaskID) error {
	if err := tx.requireActiveRoomForTask(ctx, taskID); err != nil {
		return preserveWriteConflict(err, storecontract.ErrVersionConflict)
	}
	result, err := tx.tx.ExecContext(ctx, `UPDATE tasks SET state='closed',updated_at=?
WHERE id=? AND state='open' AND EXISTS(
 SELECT 1 FROM project_document_reviews WHERE task_id=? AND kind='accept'
)`, timeText(time.Now()), taskID.String(), taskID.String())
	if err != nil {
		return mapWriteError(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 1 {
		return nil
	}
	var state string
	if err := tx.tx.QueryRowContext(ctx, `SELECT state FROM tasks WHERE id=?`, taskID.String()).Scan(&state); err != nil {
		return err
	}
	if state == string(domain.TaskStateClosed) {
		return nil
	}
	return storecontract.ErrVersionConflict
}

func (r *reader) ListProjectDocumentRevisions(ctx context.Context, taskID domain.TaskID) ([]domain.ProjectDocumentRevision, error) {
	rows, e := r.q.QueryContext(ctx, `SELECT id,room_id,revision_number,kind,body,body_digest,source_run_id,source_attempt_id,source_result_id,source_agent_report_id,source_event_id,source_event_sequence,source_result_digest,source_text_digest,edit_note,actor_id,created_at FROM project_document_revisions WHERE task_id=? ORDER BY revision_number`, taskID.String())
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []domain.ProjectDocumentRevision
	for rows.Next() {
		var id, room, kind, body, run, attempt, result, report, event, note, actor, created string
		var number uint64
		var seq int64
		var bd, rd, td []byte
		if e = rows.Scan(&id, &room, &number, &kind, &body, &bd, &run, &attempt, &result, &report, &event, &seq, &rd, &td, &note, &actor, &created); e != nil {
			return nil, e
		}
		vid, x := domain.ParseProjectDocumentRevisionID(id)
		if x != nil {
			return nil, x
		}
		rid, x := domain.ParseRoomID(room)
		if x != nil {
			return nil, x
		}
		runID, x := domain.ParseRunID(run)
		if x != nil {
			return nil, x
		}
		aid, x := domain.ParseAttemptID(attempt)
		if x != nil {
			return nil, x
		}
		resultID, x := domain.ParseResultID(result)
		if x != nil {
			return nil, x
		}
		reportID, x := domain.ParseAgentReportID(report)
		if x != nil {
			return nil, x
		}
		eventID, x := domain.ParseEventID(event)
		if x != nil {
			return nil, x
		}
		at, x := time.Parse(time.RFC3339Nano, created)
		if x != nil {
			return nil, x
		}
		if len(bd) != 32 || len(rd) != 32 || len(td) != 32 {
			return nil, storecontract.ErrVerificationConflict
		}
		var bds, rds, tds [32]byte
		copy(bds[:], bd)
		copy(rds[:], rd)
		copy(tds[:], td)
		v, x := domain.RestoreProjectDocumentRevision(domain.ProjectDocumentRevision{ID: vid, TaskID: taskID, RoomID: rid, Number: number, Kind: domain.ProjectDocumentRevisionKind(kind), Body: body, BodyDigest: bds, Source: domain.ProjectDocumentSource{RunID: runID, AttemptID: aid, ResultID: resultID, AgentReportID: reportID, EventID: eventID, EventSequence: seq, ResultDigest: rds, SourceTextDigest: tds}, EditNote: note, ActorID: actor, CreatedAt: at})
		if x != nil {
			return nil, x
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (r *reader) ListProjectDocumentReviews(ctx context.Context, taskID domain.TaskID) ([]domain.ProjectDocumentReview, error) {
	rows, e := r.q.QueryContext(ctx, `SELECT id,revision_id,expected_version,kind,note,actor_id,session_id,context_revision_id,decided_at FROM project_document_reviews WHERE task_id=? ORDER BY decided_at,id`, taskID.String())
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []domain.ProjectDocumentReview
	for rows.Next() {
		var id, revision, kind, note, actor, session, decided string
		var version uint64
		var contextID sql.NullString
		if e = rows.Scan(&id, &revision, &version, &kind, &note, &actor, &session, &contextID, &decided); e != nil {
			return nil, e
		}
		rid, x := domain.ParseProjectDocumentReviewID(id)
		if x != nil {
			return nil, x
		}
		vrid, x := domain.ParseProjectDocumentRevisionID(revision)
		if x != nil {
			return nil, x
		}
		at, x := time.Parse(time.RFC3339Nano, decided)
		if x != nil {
			return nil, x
		}
		var cid domain.ContextRevisionID
		if contextID.Valid {
			cid, x = domain.ParseContextRevisionID(contextID.String)
			if x != nil {
				return nil, x
			}
		}
		review, x := domain.RestoreProjectDocumentReview(domain.ProjectDocumentReview{ID: rid, RevisionID: vrid, TaskID: taskID, ExpectedVersion: version, Kind: domain.ProjectDocumentReviewKind(kind), Note: note, ActorID: actor, SessionID: session, ContextRevisionID: cid, DecidedAt: at})
		if x != nil {
			return nil, x
		}
		out = append(out, review)
	}
	return out, rows.Err()
}
