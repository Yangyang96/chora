package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func (reader *reader) ListRunEvents(ctx context.Context, runID domain.RunID) ([]domain.RunEvent, error) {
	rows, err := reader.q.QueryContext(ctx, `SELECT id,sequence,type,source,occurred_at,recorded_at,normalized_json,raw_json FROM run_events WHERE run_id=? ORDER BY sequence`, runID.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []domain.RunEvent
	for rows.Next() {
		var idText, eventType, source, occurred, recorded string
		var sequence int64
		var normalized []byte
		var raw []byte
		if err := rows.Scan(&idText, &sequence, &eventType, &source, &occurred, &recorded, &normalized, &raw); err != nil {
			return nil, err
		}
		id, err := domain.ParseEventID(idText)
		if err != nil {
			return nil, err
		}
		occurredAt, err := parseTime(occurred)
		if err != nil {
			return nil, err
		}
		recordedAt, err := parseTime(recorded)
		if err != nil {
			return nil, err
		}
		event, err := domain.NewRunEvent(domain.RunEventParams{ID: id, RunID: runID, Sequence: sequence, Type: eventType, Source: source, OccurredAt: occurredAt, RecordedAt: recordedAt, NormalizedJSON: normalized, RawJSON: raw})
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (tx *writeTx) AppendRunEvent(ctx context.Context, runID domain.RunID, draft storecontract.EventDraft) (domain.RunEvent, error) {
	if err := tx.requireActiveRoomForRun(ctx, runID); err != nil {
		return domain.RunEvent{}, err
	}
	if _, err := tx.tx.ExecContext(ctx, `SAVEPOINT append_run_event`); err != nil {
		return domain.RunEvent{}, err
	}
	rollback := func(cause error) (domain.RunEvent, error) {
		_, _ = tx.tx.ExecContext(ctx, `ROLLBACK TO append_run_event`)
		_, _ = tx.tx.ExecContext(ctx, `RELEASE append_run_event`)
		return domain.RunEvent{}, cause
	}
	var sequence int64
	if err := tx.tx.QueryRowContext(ctx, `UPDATE runs SET last_event_sequence=last_event_sequence+1 WHERE id=? RETURNING last_event_sequence`, runID.String()).Scan(&sequence); errors.Is(err, sql.ErrNoRows) {
		return rollback(storecontract.ErrNotFound)
	} else if err != nil {
		return rollback(err)
	}
	event, err := domain.NewRunEvent(domain.RunEventParams{ID: draft.ID, RunID: runID, Sequence: sequence, Type: draft.Type, Source: draft.Source, OccurredAt: draft.OccurredAt, RecordedAt: draft.RecordedAt, NormalizedJSON: draft.NormalizedJSON, RawJSON: draft.RawJSON})
	if err != nil {
		return rollback(err)
	}
	var raw any
	if len(event.RawJSON()) > 0 {
		raw = event.RawJSON()
	}
	if _, err := tx.tx.ExecContext(ctx, `INSERT INTO run_events(id,run_id,sequence,type,source,occurred_at,recorded_at,normalized_json,raw_json) VALUES(?,?,?,?,?,?,?,?,?)`, event.ID().String(), event.RunID().String(), event.Sequence(), event.Type(), event.Source(), timeText(event.OccurredAt()), timeText(event.RecordedAt()), event.NormalizedJSON(), raw); err != nil {
		return rollback(mapWriteError(err))
	}
	if _, err := tx.tx.ExecContext(ctx, `RELEASE append_run_event`); err != nil {
		return rollback(err)
	}
	return event, nil
}

func (store *Store) CommitRunCommand(ctx context.Context, request storecontract.CommitRunCommandRequest) (storecontract.CommandResult, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return storecontract.CommandResult{}, err
	}
	rollback := func(result storecontract.CommandResult, cause error) (storecontract.CommandResult, error) {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			return storecontract.CommandResult{}, fmt.Errorf("%w: rollback after %v: %v", storecontract.ErrCommitUnknown, cause, rbErr)
		}
		return result, cause
	}
	var storedDigest, body []byte
	var status int
	var contentType string
	err = tx.QueryRowContext(ctx, `SELECT request_digest,response_status,response_content_type,response_body FROM command_idempotency WHERE key_hash=?`, request.IdempotencyKeyHash[:]).Scan(&storedDigest, &status, &contentType, &body)
	if err == nil {
		if len(storedDigest) != 32 || string(storedDigest) != string(request.RequestDigest[:]) {
			return rollback(storecontract.CommandResult{}, storecontract.ErrIdempotencyConflict)
		}
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			return storecontract.CommandResult{}, fmt.Errorf("%w: %v", storecontract.ErrCommitUnknown, err)
		}
		return storecontract.CommandResult{Response: storecontract.Response{Status: status, ContentType: contentType, Body: append([]byte(nil), body...)}, Replayed: true}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return rollback(storecontract.CommandResult{}, err)
	}
	if request.IdempotencyKeyHash == ([32]byte{}) || request.RequestDigest == ([32]byte{}) || !request.NextRun.ID().Valid() || request.Event.ID.Valid() == false {
		return rollback(storecontract.CommandResult{}, fmt.Errorf("invalid commit run command"))
	}
	w := &writeTx{reader: reader{q: tx}, tx: tx}
	if err := w.requireActiveRoomForRun(ctx, request.NextRun.ID()); err != nil {
		return rollback(storecontract.CommandResult{}, err)
	}
	var persistedVersion uint64
	if err := tx.QueryRowContext(ctx, `SELECT version FROM runs WHERE id=?`, request.NextRun.ID().String()).Scan(&persistedVersion); errors.Is(err, sql.ErrNoRows) {
		return rollback(storecontract.CommandResult{}, storecontract.ErrNotFound)
	} else if err != nil {
		return rollback(storecontract.CommandResult{}, err)
	}
	if persistedVersion != request.ExpectedVersion {
		return rollback(storecontract.CommandResult{}, storecontract.ErrVersionConflict)
	}
	if request.NextRun.Version() != request.ExpectedVersion+1 {
		return rollback(storecontract.CommandResult{}, storecontract.ErrVersionConflict)
	}
	if err := w.SaveRunCAS(ctx, request.ExpectedVersion, request.NextRun); err != nil {
		return rollback(storecontract.CommandResult{}, err)
	}
	event, err := w.AppendRunEvent(ctx, request.NextRun.ID(), request.Event)
	if err != nil {
		return rollback(storecontract.CommandResult{}, err)
	}
	status = request.Response.Status
	if status == 0 {
		status = 200
	}
	contentType = request.Response.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	created := timeText(time.Now())
	responseBody := request.Response.Body
	if responseBody == nil {
		responseBody = []byte{}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO command_idempotency(key_hash,command,resource_id,request_digest,response_status,response_content_type,response_body,created_at,expires_at) VALUES(?,?,?,?,?,?,?,?,?)`, request.IdempotencyKeyHash[:], "commit_run", request.NextRun.ID().String(), request.RequestDigest[:], status, contentType, responseBody, created, nullableTime(request.ExpiresAt)); err != nil {
		return rollback(storecontract.CommandResult{}, mapWriteError(err))
	}
	if err := store.beforeCommit(); err != nil {
		return rollback(storecontract.CommandResult{}, err)
	}
	if err := tx.Commit(); err != nil {
		return storecontract.CommandResult{}, fmt.Errorf("%w: %v", storecontract.ErrCommitUnknown, err)
	}
	return storecontract.CommandResult{Response: storecontract.Response{Status: status, ContentType: contentType, Body: append([]byte(nil), responseBody...)}, Event: event}, nil
}
