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

func (tx *writeTx) InsertCandidate(ctx context.Context, candidate domain.Candidate) error {
	if err := tx.requireActiveRoom(ctx, candidate.RoomID()); err != nil {
		return err
	}
	_, err := tx.tx.ExecContext(ctx, `INSERT INTO candidates(id,room_id,source_run_id,source_review_id,source_artifact_id,title,body,state,version,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		candidate.ID().String(), candidate.RoomID().String(), candidate.SourceRunID().String(), candidate.SourceReviewID().String(), candidate.SourceArtifactID().String(), candidate.Title(), candidate.Body(), string(candidate.State()), candidate.Version(), timeText(candidate.CreatedAt()), timeText(candidate.UpdatedAt()))
	if err != nil && (containsConstraint(err, "candidates.source_run_id, candidates.source_artifact_id") || containsConstraint(err, "candidates.id")) {
		return fmt.Errorf("%w: %v", storecontract.ErrCandidateConflict, err)
	}
	return mapWriteError(err)
}

func (reader *reader) GetCandidate(ctx context.Context, id domain.CandidateID) (domain.Candidate, error) {
	return reader.getCandidateWhere(ctx, `WHERE id=?`, id.String())
}

func (reader *reader) getCandidateWhere(ctx context.Context, clause string, args ...any) (domain.Candidate, error) {
	var idText, roomText, runText, reviewText, artifactText, title, body, state, created, updated string
	var version uint64
	err := reader.q.QueryRowContext(ctx, `SELECT id,room_id,source_run_id,source_review_id,source_artifact_id,title,body,state,version,created_at,updated_at FROM candidates `+clause, args...).Scan(&idText, &roomText, &runText, &reviewText, &artifactText, &title, &body, &state, &version, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Candidate{}, storecontract.ErrNotFound
	}
	if err != nil {
		return domain.Candidate{}, err
	}
	id, err := domain.ParseCandidateID(idText)
	if err != nil {
		return domain.Candidate{}, err
	}
	roomID, err := domain.ParseRoomID(roomText)
	if err != nil {
		return domain.Candidate{}, err
	}
	runID, err := domain.ParseRunID(runText)
	if err != nil {
		return domain.Candidate{}, err
	}
	reviewID, err := domain.ParseReviewDecisionID(reviewText)
	if err != nil {
		return domain.Candidate{}, err
	}
	artifactID, err := domain.ParseArtifactID(artifactText)
	if err != nil {
		return domain.Candidate{}, err
	}
	createdAt, err := parseTime(created)
	if err != nil {
		return domain.Candidate{}, err
	}
	updatedAt, err := parseTime(updated)
	if err != nil {
		return domain.Candidate{}, err
	}
	return domain.RestoreCandidate(domain.CandidateRecord{ID: id, RoomID: roomID, SourceRunID: runID, SourceReviewID: reviewID, SourceArtifactID: artifactID, Title: title, Body: body, State: domain.CandidateState(state), Version: version, CreatedAt: createdAt, UpdatedAt: updatedAt})
}

func (reader *reader) ListCandidatesForRun(ctx context.Context, runID domain.RunID) ([]domain.Candidate, error) {
	return reader.listCandidates(ctx, `WHERE source_run_id=? ORDER BY created_at,id`, runID.String())
}

func (reader *reader) ListCandidatesForRoom(ctx context.Context, roomID domain.RoomID) ([]domain.Candidate, error) {
	return reader.listCandidates(ctx, `WHERE room_id=? ORDER BY created_at,id`, roomID.String())
}

func (reader *reader) listCandidates(ctx context.Context, clause string, args ...any) ([]domain.Candidate, error) {
	rows, err := reader.q.QueryContext(ctx, `SELECT id FROM candidates `+clause, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []domain.CandidateID
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			return nil, err
		}
		id, err := domain.ParseCandidateID(text)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result := make([]domain.Candidate, 0, len(ids))
	for _, id := range ids {
		candidate, err := reader.GetCandidate(ctx, id)
		if err != nil {
			return nil, err
		}
		result = append(result, candidate)
	}
	return result, nil
}

func (tx *writeTx) SaveCandidateEditCAS(ctx context.Context, expected uint64, before, after domain.Candidate) error {
	if err := tx.requireActiveRoomForCandidate(ctx, before.ID()); err != nil {
		return preserveWriteConflict(err, storecontract.ErrCandidateConflict)
	}
	if before.ID() != after.ID() || before.Version() != expected || after.Version() != expected+1 || after.State() != domain.CandidateStatePending {
		return storecontract.ErrCandidateConflict
	}
	result, err := tx.tx.ExecContext(ctx, `UPDATE candidates SET title=?,body=?,state=?,version=?,updated_at=? WHERE id=? AND state='pending' AND version=?`, after.Title(), after.Body(), string(after.State()), after.Version(), timeText(after.UpdatedAt()), after.ID().String(), expected)
	if err != nil {
		return mapWriteError(err)
	}
	if err := requireCandidateCAS(result); err != nil {
		return err
	}
	_, err = tx.tx.ExecContext(ctx, `INSERT INTO candidate_edits(candidate_id,from_version,to_version,previous_title,previous_body,edited_title,edited_body,edited_at) VALUES(?,?,?,?,?,?,?,?)`, after.ID().String(), expected, after.Version(), before.Title(), before.Body(), after.Title(), after.Body(), timeText(after.UpdatedAt()))
	return mapWriteError(err)
}

func (tx *writeTx) SaveCandidateDecisionCAS(ctx context.Context, before, after domain.Candidate, decision domain.CandidateDecision) error {
	if err := tx.requireActiveRoomForCandidate(ctx, before.ID()); err != nil {
		return preserveWriteConflict(err, storecontract.ErrCandidateConflict)
	}
	if before.ID() != after.ID() || decision.CandidateID() != before.ID() || before.Version() != decision.ExpectedVersion() || after.Version() != before.Version()+1 || after.State() == domain.CandidateStatePending {
		return storecontract.ErrCandidateConflict
	}
	result, err := tx.tx.ExecContext(ctx, `UPDATE candidates SET state=?,version=?,updated_at=? WHERE id=? AND state='pending' AND version=?`, string(after.State()), after.Version(), timeText(after.UpdatedAt()), after.ID().String(), before.Version())
	if err != nil {
		return mapWriteError(err)
	}
	if err := requireCandidateCAS(result); err != nil {
		return err
	}
	_, err = tx.tx.ExecContext(ctx, `INSERT INTO candidate_decisions(id,candidate_id,kind,expected_candidate_version,note,actor_id,session_id,decided_at) VALUES(?,?,?,?,?,?,?,?)`, decision.ID().String(), decision.CandidateID().String(), string(decision.Kind()), decision.ExpectedVersion(), decision.Note(), decision.ActorID(), decision.SessionID(), timeText(decision.DecidedAt()))
	if err != nil && containsConstraint(err, "candidate_decisions.candidate_id") {
		return fmt.Errorf("%w: %v", storecontract.ErrCandidateConflict, err)
	}
	return mapWriteError(err)
}

func requireCandidateCAS(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return storecontract.ErrCandidateConflict
	}
	return nil
}

func (reader *reader) GetCandidateDecision(ctx context.Context, candidateID domain.CandidateID) (domain.CandidateDecision, error) {
	var idText, candidateText, kind, note, actorID, sessionID, decided string
	var expected uint64
	err := reader.q.QueryRowContext(ctx, `SELECT id,candidate_id,kind,expected_candidate_version,note,actor_id,session_id,decided_at FROM candidate_decisions WHERE candidate_id=?`, candidateID.String()).Scan(&idText, &candidateText, &kind, &expected, &note, &actorID, &sessionID, &decided)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.CandidateDecision{}, storecontract.ErrNotFound
	}
	if err != nil {
		return domain.CandidateDecision{}, err
	}
	id, err := domain.ParseCandidateDecisionID(idText)
	if err != nil {
		return domain.CandidateDecision{}, err
	}
	parsedCandidate, err := domain.ParseCandidateID(candidateText)
	if err != nil {
		return domain.CandidateDecision{}, err
	}
	decidedAt, err := parseTime(decided)
	if err != nil {
		return domain.CandidateDecision{}, err
	}
	return domain.RestoreCandidateDecision(id, parsedCandidate, domain.CandidateDecisionKind(kind), expected, note, actorID, sessionID, decidedAt)
}

func (tx *writeTx) InsertRoomRevision(ctx context.Context, revision domain.RoomRevision) error {
	if err := tx.requireActiveRoom(ctx, revision.RoomID()); err != nil {
		return err
	}
	provenance := revision.Provenance()
	digest := revision.Digest()
	_, err := tx.tx.ExecContext(ctx, `INSERT INTO room_revision_records(revision_id,room_id,provenance_kind,candidate_id,candidate_decision_id,source_run_id,source_review_id,source_artifact_id,actor,digest,confirmed_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		revision.ID().String(), revision.RoomID().String(), string(provenance.Kind), nullableString(provenance.CandidateID), nullableString(provenance.CandidateDecisionID), nullableString(provenance.SourceRunID), nullableString(provenance.SourceReviewID), nullableString(provenance.SourceArtifactID), provenance.Actor, digest[:], timeText(revision.ConfirmedAt()))
	if err != nil && (containsConstraint(err, "room_revision_records.revision_id") || containsConstraint(err, "room_revision_records.candidate_id") || containsConstraint(err, "room_revision_records.candidate_decision_id")) {
		return fmt.Errorf("%w: %v", storecontract.ErrCandidateConflict, err)
	}
	return mapWriteError(err)
}

func (reader *reader) GetRoomRevision(ctx context.Context, id domain.ContextRevisionID) (domain.RoomRevision, error) {
	var roomText, kind, candidate, decision, run, review, artifact, actor, confirmed string
	var digest []byte
	err := reader.q.QueryRowContext(ctx, `SELECT room_id,provenance_kind,COALESCE(candidate_id,''),COALESCE(candidate_decision_id,''),COALESCE(source_run_id,''),COALESCE(source_review_id,''),COALESCE(source_artifact_id,''),actor,digest,confirmed_at FROM room_revision_records WHERE revision_id=?`, id.String()).Scan(&roomText, &kind, &candidate, &decision, &run, &review, &artifact, &actor, &digest, &confirmed)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.RoomRevision{}, storecontract.ErrNotFound
	}
	if err != nil {
		return domain.RoomRevision{}, err
	}
	roomID, err := domain.ParseRoomID(roomText)
	if err != nil {
		return domain.RoomRevision{}, err
	}
	revisions, err := reader.LookupRevisions(ctx, roomID, []domain.ContextRevisionID{id})
	if err != nil {
		return domain.RoomRevision{}, err
	}
	if len(digest) != 32 || len(revisions) != 1 {
		return domain.RoomRevision{}, errors.New("invalid room revision record")
	}
	var sum [32]byte
	copy(sum[:], digest)
	confirmedAt, err := parseTime(confirmed)
	if err != nil {
		return domain.RoomRevision{}, err
	}
	provenance := domain.RevisionProvenance{Kind: domain.RevisionProvenanceKind(kind), CandidateID: candidate, CandidateDecisionID: decision, SourceRunID: run, SourceReviewID: review, SourceArtifactID: artifact, Actor: actor}
	return domain.RestoreRoomRevision(revisions[0], provenance, sum, confirmedAt)
}

func (reader *reader) ListRoomRevisions(ctx context.Context, roomID domain.RoomID) ([]domain.RoomRevision, error) {
	rows, err := reader.q.QueryContext(ctx, `SELECT revision_id FROM room_revision_records WHERE room_id=? ORDER BY confirmed_at,revision_id`, roomID.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []domain.ContextRevisionID
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			return nil, err
		}
		id, err := domain.ParseContextRevisionID(text)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result := make([]domain.RoomRevision, 0, len(ids))
	for _, id := range ids {
		revision, err := reader.GetRoomRevision(ctx, id)
		if err != nil {
			return nil, err
		}
		result = append(result, revision)
	}
	return result, nil
}

func (tx *writeTx) InsertTaskRevisionSelection(ctx context.Context, selection domain.TaskRevisionSelection) error {
	if err := tx.requireActiveRoomForTask(ctx, selection.TaskID()); err != nil {
		return err
	}
	for position, item := range selection.Selected() {
		provenance, err := json.Marshal(item.Provenance)
		if err != nil {
			return err
		}
		result, err := tx.tx.ExecContext(ctx, `INSERT INTO task_revision_selections(task_id,room_id,revision_id,position,digest,provenance_json,selected_at) SELECT ?,?,?,?,?,?,? WHERE EXISTS(SELECT 1 FROM room_revision_records WHERE revision_id=? AND room_id=? AND digest=?)`, selection.TaskID().String(), selection.RoomID().String(), item.RevisionID.String(), position, item.Digest[:], provenance, timeText(selection.CreatedAt()), item.RevisionID.String(), selection.RoomID().String(), item.Digest[:])
		if err != nil {
			return mapWriteError(err)
		}
		if err := requireFrozenReference(result, "room revision", item.RevisionID.String()); err != nil {
			return err
		}
	}
	for position, item := range selection.Excluded() {
		provenance, err := json.Marshal(item.Provenance)
		if err != nil {
			return err
		}
		result, err := tx.tx.ExecContext(ctx, `INSERT INTO task_revision_exclusions(task_id,room_id,revision_id,position,digest,provenance_json,reason,selected_at) SELECT ?,?,?,?,?,?,?,? WHERE EXISTS(SELECT 1 FROM room_revision_records WHERE revision_id=? AND room_id=? AND digest=?)`, selection.TaskID().String(), selection.RoomID().String(), item.RevisionID.String(), position, item.Digest[:], provenance, item.Reason, timeText(selection.CreatedAt()), item.RevisionID.String(), selection.RoomID().String(), item.Digest[:])
		if err != nil {
			return mapWriteError(err)
		}
		if err := requireFrozenReference(result, "excluded room revision", item.RevisionID.String()); err != nil {
			return err
		}
	}
	return nil
}

func (reader *reader) GetTaskRevisionSelection(ctx context.Context, taskID domain.TaskID) (domain.TaskRevisionSelection, error) {
	var roomText, created string
	rows, err := reader.q.QueryContext(ctx, `SELECT room_id,revision_id,digest,provenance_json,selected_at FROM task_revision_selections WHERE task_id=? ORDER BY position`, taskID.String())
	if err != nil {
		return domain.TaskRevisionSelection{}, err
	}
	var selected []domain.RevisionSelectionItem
	for rows.Next() {
		var revisionText string
		var digest, provenanceJSON []byte
		var rowRoom, rowCreated string
		if err := rows.Scan(&rowRoom, &revisionText, &digest, &provenanceJSON, &rowCreated); err != nil {
			rows.Close()
			return domain.TaskRevisionSelection{}, err
		}
		if roomText == "" {
			roomText, created = rowRoom, rowCreated
		} else if roomText != rowRoom || created != rowCreated {
			rows.Close()
			return domain.TaskRevisionSelection{}, errors.New("inconsistent task selection")
		}
		item, err := parseSelectionItem(revisionText, digest, provenanceJSON)
		if err != nil {
			rows.Close()
			return domain.TaskRevisionSelection{}, err
		}
		selected = append(selected, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return domain.TaskRevisionSelection{}, err
	}
	if err := rows.Close(); err != nil {
		return domain.TaskRevisionSelection{}, err
	}
	if len(selected) == 0 {
		return domain.TaskRevisionSelection{}, storecontract.ErrNotFound
	}
	exRows, err := reader.q.QueryContext(ctx, `SELECT room_id,revision_id,digest,provenance_json,reason,selected_at FROM task_revision_exclusions WHERE task_id=? ORDER BY position`, taskID.String())
	if err != nil {
		return domain.TaskRevisionSelection{}, err
	}
	defer exRows.Close()
	var excluded []domain.RevisionExclusion
	for exRows.Next() {
		var rowRoom, revisionText, reason, rowCreated string
		var digest, provenanceJSON []byte
		if err := exRows.Scan(&rowRoom, &revisionText, &digest, &provenanceJSON, &reason, &rowCreated); err != nil {
			return domain.TaskRevisionSelection{}, err
		}
		if roomText != rowRoom || created != rowCreated {
			return domain.TaskRevisionSelection{}, errors.New("inconsistent task exclusion")
		}
		item, err := parseSelectionItem(revisionText, digest, provenanceJSON)
		if err != nil {
			return domain.TaskRevisionSelection{}, err
		}
		excluded = append(excluded, domain.RevisionExclusion{RevisionSelectionItem: item, Reason: reason})
	}
	if err := exRows.Err(); err != nil {
		return domain.TaskRevisionSelection{}, err
	}
	roomID, err := domain.ParseRoomID(roomText)
	if err != nil {
		return domain.TaskRevisionSelection{}, err
	}
	createdAt, err := parseTime(created)
	if err != nil {
		return domain.TaskRevisionSelection{}, err
	}
	return domain.NewTaskRevisionSelection(taskID, roomID, selected, excluded, createdAt)
}

func parseSelectionItem(revisionText string, digest, provenanceJSON []byte) (domain.RevisionSelectionItem, error) {
	revisionID, err := domain.ParseContextRevisionID(revisionText)
	if err != nil {
		return domain.RevisionSelectionItem{}, err
	}
	if len(digest) != 32 {
		return domain.RevisionSelectionItem{}, errors.New("invalid selection digest")
	}
	var sum [32]byte
	copy(sum[:], digest)
	var provenance domain.RevisionProvenance
	if err := json.Unmarshal(provenanceJSON, &provenance); err != nil || !provenance.Valid() {
		return domain.RevisionSelectionItem{}, errors.New("invalid selection provenance")
	}
	return domain.RevisionSelectionItem{RevisionID: revisionID, Digest: sum, Provenance: provenance}, nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func containsConstraint(err error, fragment string) bool {
	return err != nil && strings.Contains(err.Error(), fragment)
}
