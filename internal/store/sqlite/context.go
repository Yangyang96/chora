package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Yangyang96/chora/internal/contextcore"
	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func (tx *writeTx) InsertRevision(ctx context.Context, revision domain.RoomContextRevision) error {
	if err := tx.requireActiveRoom(ctx, revision.RoomID()); err != nil {
		return err
	}
	if revision.RevisionNumber() == 1 {
		if _, err := tx.tx.ExecContext(ctx, `INSERT INTO context_entries(id,room_id,kind,created_at) VALUES(?,?,?,?)`, revision.EntryID().String(), revision.RoomID().String(), string(revision.Kind()), timeText(revision.CreatedAt())); err != nil {
			return mapWriteError(err)
		}
	}
	var supersedes any
	if id, ok := revision.Supersedes(); ok {
		supersedes = id.String()
	}
	_, err := tx.tx.ExecContext(ctx, `INSERT INTO context_revisions(id,entry_id,revision_number,supersedes_revision_id,title,body,locator,sensitive,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, revision.RevisionID().String(), revision.EntryID().String(), revision.RevisionNumber(), supersedes, revision.Title(), revision.Body(), revision.Locator(), revision.Sensitive(), timeText(revision.CreatedAt()), timeText(revision.UpdatedAt()))
	return mapWriteError(err)
}

func (reader *reader) LookupRevisions(ctx context.Context, roomID domain.RoomID, ids []domain.ContextRevisionID) ([]domain.RoomContextRevision, error) {
	result := make([]domain.RoomContextRevision, 0, len(ids))
	for _, id := range ids {
		var entryText, revisionText, roomText, kind, title, body, locator, created, updated string
		var number int
		var sensitive bool
		var supersedes sql.NullString
		err := reader.q.QueryRowContext(ctx, `SELECT r.entry_id,r.id,e.room_id,e.kind,r.revision_number,r.title,r.body,r.locator,r.sensitive,r.supersedes_revision_id,r.created_at,r.updated_at FROM context_revisions r JOIN context_entries e ON e.id=r.entry_id WHERE r.id=? AND e.room_id=?`, id.String(), roomID.String()).Scan(&entryText, &revisionText, &roomText, &kind, &number, &title, &body, &locator, &sensitive, &supersedes, &created, &updated)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, storecontract.ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		entryID, err := domain.ParseContextEntryID(entryText)
		if err != nil {
			return nil, err
		}
		revisionID, err := domain.ParseContextRevisionID(revisionText)
		if err != nil {
			return nil, err
		}
		parsedRoom, err := domain.ParseRoomID(roomText)
		if err != nil {
			return nil, err
		}
		var predecessor *domain.ContextRevisionID
		if supersedes.Valid {
			p, err := domain.ParseContextRevisionID(supersedes.String)
			if err != nil {
				return nil, err
			}
			predecessor = &p
		}
		createdAt, err := parseTime(created)
		if err != nil {
			return nil, err
		}
		updatedAt, err := parseTime(updated)
		if err != nil {
			return nil, err
		}
		revision, err := domain.NewRoomContextRevision(domain.RoomContextRevisionParams{EntryID: entryID, RevisionID: revisionID, RoomID: parsedRoom, Kind: domain.ContextKind(kind), RevisionNumber: number, Title: title, Body: body, Locator: locator, Sensitive: sensitive, Supersedes: predecessor, CreatedAt: createdAt, UpdatedAt: updatedAt})
		if err != nil {
			return nil, err
		}
		result = append(result, revision)
	}
	return result, nil
}

func (tx *writeTx) SaveSnapshot(ctx context.Context, snapshot contextcore.Snapshot) error {
	digest := snapshot.Digest()
	_, err := tx.tx.ExecContext(ctx, `INSERT INTO context_snapshots(id,digest,canonical_json,markdown,created_at) VALUES(?,?,?,?,?)`, snapshot.ID().String(), digest[:], snapshot.CanonicalJSON(), snapshot.Markdown(), timeText(time.Now()))
	if err != nil {
		return mapWriteError(err)
	}
	for i, id := range snapshot.IncludedRevisionIDs() {
		if _, err := tx.tx.ExecContext(ctx, `INSERT INTO snapshot_inclusions(snapshot_id,revision_id,position) VALUES(?,?,?)`, snapshot.ID().String(), id.String(), i); err != nil {
			return mapWriteError(err)
		}
	}
	for i, excluded := range snapshot.Excluded() {
		if _, err := tx.tx.ExecContext(ctx, `INSERT INTO snapshot_exclusions(snapshot_id,entry_id,revision_id,position,reason) VALUES(?,?,?,?,?)`, snapshot.ID().String(), excluded.EntryID().String(), excluded.RevisionID().String(), i, excluded.Reason()); err != nil {
			return mapWriteError(err)
		}
	}
	if selection, ok := snapshot.Selection(); ok {
		for position, item := range selection.Selected() {
			provenance, err := json.Marshal(item.Provenance)
			if err != nil {
				return err
			}
			if _, err := tx.tx.ExecContext(ctx, `INSERT INTO snapshot_revision_manifest(snapshot_id,revision_id,disposition,position,digest,provenance_json,reason) VALUES(?,?,?,?,?,?,?)`, snapshot.ID().String(), item.RevisionID.String(), "selected", position, item.Digest[:], provenance, ""); err != nil {
				return mapWriteError(err)
			}
		}
		for position, item := range selection.Excluded() {
			provenance, err := json.Marshal(item.Provenance)
			if err != nil {
				return err
			}
			if _, err := tx.tx.ExecContext(ctx, `INSERT INTO snapshot_revision_manifest(snapshot_id,revision_id,disposition,position,digest,provenance_json,reason) VALUES(?,?,?,?,?,?,?)`, snapshot.ID().String(), item.RevisionID.String(), "excluded", position, item.Digest[:], provenance, item.Reason); err != nil {
				return mapWriteError(err)
			}
		}
	}
	return nil
}

func (reader *reader) GetSnapshot(ctx context.Context, id domain.ContextSnapshotID) (contextcore.Snapshot, error) {
	var idText, created string
	var digest, canonical, markdown []byte
	err := reader.q.QueryRowContext(ctx, `SELECT id,digest,canonical_json,markdown,created_at FROM context_snapshots WHERE id=?`, id.String()).Scan(&idText, &digest, &canonical, &markdown, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return contextcore.Snapshot{}, storecontract.ErrNotFound
	}
	if err != nil {
		return contextcore.Snapshot{}, err
	}
	_ = created
	parsedID, err := domain.ParseContextSnapshotID(idText)
	if err != nil {
		return contextcore.Snapshot{}, err
	}
	if len(digest) != 32 {
		return contextcore.Snapshot{}, errors.New("invalid snapshot digest")
	}
	var sum [32]byte
	copy(sum[:], digest)
	rows, err := reader.q.QueryContext(ctx, `SELECT revision_id FROM snapshot_inclusions WHERE snapshot_id=? ORDER BY position`, idText)
	if err != nil {
		return contextcore.Snapshot{}, err
	}
	var included []domain.ContextRevisionID
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			rows.Close()
			return contextcore.Snapshot{}, err
		}
		parsed, err := domain.ParseContextRevisionID(value)
		if err != nil {
			rows.Close()
			return contextcore.Snapshot{}, err
		}
		included = append(included, parsed)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return contextcore.Snapshot{}, err
	}
	if err := rows.Close(); err != nil {
		return contextcore.Snapshot{}, err
	}
	exRows, err := reader.q.QueryContext(ctx, `SELECT entry_id,revision_id,reason FROM snapshot_exclusions WHERE snapshot_id=? ORDER BY position`, idText)
	if err != nil {
		return contextcore.Snapshot{}, err
	}
	var excluded []contextcore.ExcludedRevisionRecord
	for exRows.Next() {
		var entryText, revisionText, reason string
		if err := exRows.Scan(&entryText, &revisionText, &reason); err != nil {
			exRows.Close()
			return contextcore.Snapshot{}, err
		}
		entryID, err := domain.ParseContextEntryID(entryText)
		if err != nil {
			exRows.Close()
			return contextcore.Snapshot{}, err
		}
		revisionID, err := domain.ParseContextRevisionID(revisionText)
		if err != nil {
			exRows.Close()
			return contextcore.Snapshot{}, err
		}
		excluded = append(excluded, contextcore.ExcludedRevisionRecord{EntryID: entryID, RevisionID: revisionID, Reason: reason})
	}
	if err := exRows.Err(); err != nil {
		exRows.Close()
		return contextcore.Snapshot{}, err
	}
	if err := exRows.Close(); err != nil {
		return contextcore.Snapshot{}, err
	}
	selection, err := reader.loadSnapshotSelection(ctx, idText, canonical)
	if err != nil {
		return contextcore.Snapshot{}, err
	}
	return contextcore.RestoreSnapshot(contextcore.SnapshotRecord{ID: parsedID, Digest: sum, CanonicalJSON: canonical, Markdown: markdown, IncludedRevisionIDs: included, Excluded: excluded, Selection: selection})
}

func (reader *reader) loadSnapshotSelection(ctx context.Context, snapshotID string, canonical []byte) (*domain.TaskRevisionSelection, error) {
	var envelope struct {
		SchemaVersion string `json:"schema_version"`
		Selection     *struct {
			TaskID    string `json:"task_id"`
			RoomID    string `json:"room_id"`
			CreatedAt string `json:"created_at"`
		} `json:"selection"`
	}
	if err := json.Unmarshal(canonical, &envelope); err != nil {
		return nil, err
	}
	if envelope.Selection == nil {
		return nil, nil
	}
	taskID, err := domain.ParseTaskID(envelope.Selection.TaskID)
	if err != nil {
		return nil, err
	}
	roomID, err := domain.ParseRoomID(envelope.Selection.RoomID)
	if err != nil {
		return nil, err
	}
	createdAt, err := parseTime(envelope.Selection.CreatedAt)
	if err != nil {
		return nil, err
	}
	rows, err := reader.q.QueryContext(ctx, `SELECT revision_id,disposition,digest,provenance_json,reason FROM snapshot_revision_manifest WHERE snapshot_id=? ORDER BY disposition DESC,position`, snapshotID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var selected []domain.RevisionSelectionItem
	var excluded []domain.RevisionExclusion
	for rows.Next() {
		var revisionText, disposition, reason string
		var digest, provenance []byte
		if err := rows.Scan(&revisionText, &disposition, &digest, &provenance, &reason); err != nil {
			return nil, err
		}
		item, err := parseSelectionItem(revisionText, digest, provenance)
		if err != nil {
			return nil, err
		}
		switch disposition {
		case "selected":
			selected = append(selected, item)
		case "excluded":
			excluded = append(excluded, domain.RevisionExclusion{RevisionSelectionItem: item, Reason: reason})
		default:
			return nil, errors.New("invalid snapshot manifest disposition")
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	selection, err := domain.NewTaskRevisionSelection(taskID, roomID, selected, excluded, createdAt)
	if err != nil {
		return nil, err
	}
	return &selection, nil
}

func (store *Store) PromoteReviewed(ctx context.Context, request contextcore.PromoteReviewedRequest) (contextcore.PromoteReviewedResult, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return contextcore.PromoteReviewedResult{Outcome: contextcore.PromotionOutcomeRetryableNotApplied}, err
	}
	rollback := func(result contextcore.PromoteReviewedResult, cause error) (contextcore.PromoteReviewedResult, error) {
		if rb := tx.Rollback(); rb != nil && !errors.Is(rb, sql.ErrTxDone) {
			return contextcore.PromoteReviewedResult{Outcome: contextcore.PromotionOutcomeCommitUnknown}, fmt.Errorf("%w: rollback after %v: %v", storecontract.ErrCommitUnknown, cause, rb)
		}
		return result, cause
	}
	var digest []byte
	err = tx.QueryRowContext(ctx, `SELECT command_digest FROM context_promotions WHERE revision_id=?`, request.Revision.RevisionID().String()).Scan(&digest)
	if err == nil {
		if len(digest) != 32 || string(digest) != string(request.CommandDigest[:]) {
			result, _ := rollback(contextcore.PromoteReviewedResult{Outcome: contextcore.PromotionOutcomeRejected, Rejection: contextcore.PromotionRejectionIdempotencyConflict}, nil)
			return result, nil
		}
		record, err := loadPromotionRecord(ctx, tx, request.Revision.RevisionID())
		if err != nil {
			return rollback(contextcore.PromoteReviewedResult{Outcome: contextcore.PromotionOutcomeRetryableNotApplied}, err)
		}
		_ = tx.Rollback()
		return contextcore.PromoteReviewedResult{Outcome: contextcore.PromotionOutcomeAlreadyApplied, Record: record}, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return rollback(contextcore.PromoteReviewedResult{Outcome: contextcore.PromotionOutcomeRetryableNotApplied}, err)
	}
	w := &writeTx{reader: reader{q: tx}, tx: tx}
	if err := w.requireActiveRoomForRun(ctx, request.RunID); err != nil {
		return rollback(contextcore.PromoteReviewedResult{Outcome: contextcore.PromotionOutcomeRetryableNotApplied}, err)
	}
	var reviewRun, reviewKind string
	var reviewExpected uint64
	err = tx.QueryRowContext(ctx, `SELECT run_id,kind,expected_run_version FROM review_decisions WHERE id=?`, request.ReviewDecisionID.String()).Scan(&reviewRun, &reviewKind, &reviewExpected)
	if errors.Is(err, sql.ErrNoRows) {
		result, _ := rollback(contextcore.PromoteReviewedResult{Outcome: contextcore.PromotionOutcomeRejected, Rejection: contextcore.PromotionRejectionReviewNotAccepted}, nil)
		return result, nil
	}
	if err != nil {
		return rollback(contextcore.PromoteReviewedResult{Outcome: contextcore.PromotionOutcomeRetryableNotApplied}, err)
	}
	if reviewKind != string(domain.ReviewDecisionAccept) {
		result, _ := rollback(contextcore.PromoteReviewedResult{Outcome: contextcore.PromotionOutcomeRejected, Rejection: contextcore.PromotionRejectionReviewNotAccepted}, nil)
		return result, nil
	}
	if reviewRun != request.RunID.String() {
		result, _ := rollback(contextcore.PromoteReviewedResult{Outcome: contextcore.PromotionOutcomeRejected, Rejection: contextcore.PromotionRejectionProvenanceMismatch}, nil)
		return result, nil
	}
	var state string
	var version uint64
	var room string
	err = tx.QueryRowContext(ctx, `SELECT r.state,r.version,t.room_id FROM runs r JOIN tasks t ON t.id=r.task_id WHERE r.id=?`, request.RunID.String()).Scan(&state, &version, &room)
	if errors.Is(err, sql.ErrNoRows) {
		result, _ := rollback(contextcore.PromoteReviewedResult{Outcome: contextcore.PromotionOutcomeRejected, Rejection: contextcore.PromotionRejectionRunNotAccepted}, nil)
		return result, nil
	}
	if err != nil {
		return rollback(contextcore.PromoteReviewedResult{Outcome: contextcore.PromotionOutcomeRetryableNotApplied}, err)
	}
	if state != string(domain.RunStateAccepted) {
		result, _ := rollback(contextcore.PromoteReviewedResult{Outcome: contextcore.PromotionOutcomeRejected, Rejection: contextcore.PromotionRejectionRunNotAccepted}, nil)
		return result, nil
	}
	if version != request.ExpectedAcceptedRunVersion || version != reviewExpected+1 {
		result, _ := rollback(contextcore.PromoteReviewedResult{Outcome: contextcore.PromotionOutcomeRejected, Rejection: contextcore.PromotionRejectionVersionMismatch}, nil)
		return result, nil
	}
	if room != request.Revision.RoomID().String() {
		result, _ := rollback(contextcore.PromoteReviewedResult{Outcome: contextcore.PromotionOutcomeRejected, Rejection: contextcore.PromotionRejectionRoomMismatch}, nil)
		return result, nil
	}
	var artifactRun, eventRun string
	if err := tx.QueryRowContext(ctx, `SELECT run_id FROM artifacts WHERE id=?`, request.ArtifactID.String()).Scan(&artifactRun); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return rollback(contextcore.PromoteReviewedResult{Outcome: contextcore.PromotionOutcomeRetryableNotApplied}, err)
		}
		result, _ := rollback(contextcore.PromoteReviewedResult{Outcome: contextcore.PromotionOutcomeRejected, Rejection: contextcore.PromotionRejectionProvenanceMismatch}, nil)
		return result, nil
	}
	if artifactRun != request.RunID.String() {
		result, _ := rollback(contextcore.PromoteReviewedResult{Outcome: contextcore.PromotionOutcomeRejected, Rejection: contextcore.PromotionRejectionProvenanceMismatch}, nil)
		return result, nil
	}
	if err := tx.QueryRowContext(ctx, `SELECT run_id FROM run_events WHERE id=?`, request.EventID.String()).Scan(&eventRun); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return rollback(contextcore.PromoteReviewedResult{Outcome: contextcore.PromotionOutcomeRetryableNotApplied}, err)
		}
		result, _ := rollback(contextcore.PromoteReviewedResult{Outcome: contextcore.PromotionOutcomeRejected, Rejection: contextcore.PromotionRejectionProvenanceMismatch}, nil)
		return result, nil
	}
	if eventRun != request.RunID.String() {
		result, _ := rollback(contextcore.PromoteReviewedResult{Outcome: contextcore.PromotionOutcomeRejected, Rejection: contextcore.PromotionRejectionProvenanceMismatch}, nil)
		return result, nil
	}
	if err := w.InsertRevision(ctx, request.Revision); err != nil {
		return rollback(contextcore.PromoteReviewedResult{Outcome: contextcore.PromotionOutcomeRetryableNotApplied}, err)
	}
	binding := request.Authorization.Binding()
	if _, err := tx.ExecContext(ctx, `INSERT INTO context_promotions(revision_id,command_digest,review_decision_id,review_kind,review_expected_run_version,run_id,accepted_run_version,artifact_id,event_id,authorization_id,authorization_binding,authorized_at,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, request.Revision.RevisionID().String(), request.CommandDigest[:], request.ReviewDecisionID.String(), reviewKind, reviewExpected, request.RunID.String(), version, request.ArtifactID.String(), request.EventID.String(), request.Authorization.ID().String(), binding[:], timeText(request.Authorization.AuthorizedAt()), timeText(time.Now())); err != nil {
		return rollback(contextcore.PromoteReviewedResult{Outcome: contextcore.PromotionOutcomeRetryableNotApplied}, err)
	}
	record, err := loadPromotionRecord(ctx, tx, request.Revision.RevisionID())
	if err != nil {
		return rollback(contextcore.PromoteReviewedResult{Outcome: contextcore.PromotionOutcomeRetryableNotApplied}, err)
	}
	if err := store.beforeCommit(); err != nil {
		return rollback(contextcore.PromoteReviewedResult{Outcome: contextcore.PromotionOutcomeRetryableNotApplied}, err)
	}
	if err := tx.Commit(); err != nil {
		return contextcore.PromoteReviewedResult{Outcome: contextcore.PromotionOutcomeCommitUnknown}, fmt.Errorf("%w: %v", storecontract.ErrCommitUnknown, err)
	}
	return contextcore.PromoteReviewedResult{Outcome: contextcore.PromotionOutcomeApplied, Record: record}, nil
}

func loadPromotionRecord(ctx context.Context, q queryer, revisionID domain.ContextRevisionID) (contextcore.PromotionRecord, error) {
	var commandDigest, authorizationBinding []byte
	var reviewID, reviewKind, runID, artifactID, eventID, authorizationID, authorizedAt string
	var reviewExpected, accepted uint64
	err := q.QueryRowContext(ctx, `SELECT command_digest,review_decision_id,review_kind,review_expected_run_version,run_id,accepted_run_version,artifact_id,event_id,authorization_id,authorization_binding,authorized_at FROM context_promotions WHERE revision_id=?`, revisionID.String()).Scan(&commandDigest, &reviewID, &reviewKind, &reviewExpected, &runID, &accepted, &artifactID, &eventID, &authorizationID, &authorizationBinding, &authorizedAt)
	if err != nil {
		return contextcore.PromotionRecord{}, err
	}
	var roomText string
	if err := q.QueryRowContext(ctx, `SELECT room_id FROM context_entries e JOIN context_revisions r ON r.entry_id=e.id WHERE r.id=?`, revisionID.String()).Scan(&roomText); err != nil {
		return contextcore.PromotionRecord{}, err
	}
	roomID, err := domain.ParseRoomID(roomText)
	if err != nil {
		return contextcore.PromotionRecord{}, err
	}
	revisions, err := (&reader{q: q}).LookupRevisions(ctx, roomID, []domain.ContextRevisionID{revisionID})
	if err != nil {
		return contextcore.PromotionRecord{}, err
	}
	parsedReview, err := domain.ParseReviewDecisionID(reviewID)
	if err != nil {
		return contextcore.PromotionRecord{}, err
	}
	parsedRun, err := domain.ParseRunID(runID)
	if err != nil {
		return contextcore.PromotionRecord{}, err
	}
	parsedArtifact, err := domain.ParseArtifactID(artifactID)
	if err != nil {
		return contextcore.PromotionRecord{}, err
	}
	parsedEvent, err := domain.ParseEventID(eventID)
	if err != nil {
		return contextcore.PromotionRecord{}, err
	}
	parsedAuthorization, err := contextcore.ParseAuthorizationID(authorizationID)
	if err != nil {
		return contextcore.PromotionRecord{}, err
	}
	authorized, err := parseTime(authorizedAt)
	if err != nil {
		return contextcore.PromotionRecord{}, err
	}
	var command, binding [32]byte
	if len(commandDigest) != 32 || len(authorizationBinding) != 32 {
		return contextcore.PromotionRecord{}, errors.New("invalid promotion digest")
	}
	copy(command[:], commandDigest)
	copy(binding[:], authorizationBinding)
	return contextcore.NewPromotionRecord(contextcore.PromotionRecordParams{Revision: revisions[0], CommandDigest: command, ReviewDecisionID: parsedReview, ReviewKind: domain.ReviewDecisionKind(reviewKind), ReviewExpectedRunVersion: reviewExpected, RunID: parsedRun, AcceptedRunVersion: accepted, ArtifactID: parsedArtifact, EventID: parsedEvent, AuthorizationID: parsedAuthorization, AuthorizationBinding: binding, AuthorizedAt: authorized})
}
