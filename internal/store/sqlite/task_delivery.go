package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

const deliveryColumns = `operation_id,task_id,run_id,repo_id,kind,state,version,request_digest,preview_json,outcome_json,created_at,updated_at`

func scanDelivery(row interface{ Scan(...any) error }) (storecontract.DeliveryOperation, error) {
	var o storecontract.DeliveryOperation
	var task, run, repo, created, updated string
	var digest []byte
	e := row.Scan(&o.ID, &task, &run, &repo, &o.Kind, &o.State, &o.Version, &digest, &o.PreviewJSON, &o.OutcomeJSON, &created, &updated)
	if errors.Is(e, sql.ErrNoRows) {
		return o, storecontract.ErrNotFound
	}
	if e != nil {
		return o, e
	}
	if o.TaskID, e = domain.ParseTaskID(task); e != nil {
		return o, e
	}
	if o.RunID, e = domain.ParseRunID(run); e != nil {
		return o, e
	}
	if o.RepositoryID, e = domain.ParseRepositoryID(repo); e != nil {
		return o, e
	}
	if len(digest) != 32 {
		return o, domain.ErrInvalidArgument
	}
	copy(o.RequestDigest[:], digest)
	if o.CreatedAt, e = parseTime(created); e != nil {
		return o, e
	}
	o.UpdatedAt, e = parseTime(updated)
	return o, e
}
func (r *reader) GetDeliveryOperation(ctx context.Context, id string) (storecontract.DeliveryOperation, error) {
	return scanDelivery(r.q.QueryRowContext(ctx, `SELECT `+deliveryColumns+` FROM task_delivery_operations WHERE operation_id=?`, id))
}
func (r *reader) ListDeliveryOperations(ctx context.Context, id domain.TaskID) ([]storecontract.DeliveryOperation, error) {
	rows, e := r.q.QueryContext(ctx, `SELECT `+deliveryColumns+` FROM task_delivery_operations WHERE task_id=? ORDER BY created_at,operation_id`, id.String())
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []storecontract.DeliveryOperation{}
	for rows.Next() {
		o, e := scanDelivery(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
func (tx *writeTx) SaveDeliveryOperation(ctx context.Context, expected uint64, o storecontract.DeliveryOperation) error {
	if expected == 0 || o.State == "writing" {
		if err := tx.requireOpenResultEntry(ctx, o.RunID, o.RepositoryID.String()); err != nil {
			if !errors.Is(err, storecontract.ErrVerificationConflict) {
				return err
			}
			if err = tx.requireClosedResultCleanup(ctx, o); err != nil {
				return err
			}
		}
	}
	if o.ID == "" || !o.TaskID.Valid() || !o.RunID.Valid() || !o.RepositoryID.Valid() || o.Version != expected+1 || o.CreatedAt.IsZero() || o.UpdatedAt.Before(o.CreatedAt) || !json.Valid(o.PreviewJSON) || !json.Valid(o.OutcomeJSON) {
		return domain.ErrInvalidArgument
	}
	if e := tx.requireActiveRoomForRun(ctx, o.RunID); e != nil {
		return e
	}
	if expected == 0 {
		if o.State != "preview" {
			return domain.ErrInvalidArgument
		}
		run, e := tx.GetRun(ctx, o.RunID)
		if e != nil {
			return e
		}
		if run.TaskID() != o.TaskID {
			return domain.ErrInvalidArgument
		}
		_, e = tx.tx.ExecContext(ctx, `INSERT INTO task_delivery_operations(`+deliveryColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, o.ID, o.TaskID.String(), o.RunID.String(), o.RepositoryID.String(), o.Kind, o.State, o.Version, o.RequestDigest[:], o.PreviewJSON, o.OutcomeJSON, timeText(o.CreatedAt), timeText(o.UpdatedAt))
		return mapWriteError(e)
	}
	old, e := tx.GetDeliveryOperation(ctx, o.ID)
	if e != nil {
		return e
	}
	if old.Version != expected {
		return storecontract.ErrVersionConflict
	}
	valid := old.State == "preview" && (o.State == "writing" || o.State == "failed") || (old.State == "writing" || old.State == "recovery_required") && (o.State == "succeeded" || o.State == "failed" || o.State == "recovery_required")
	if !valid {
		return storecontract.ErrVersionConflict
	}
	out, e := tx.tx.ExecContext(ctx, `UPDATE task_delivery_operations SET state=?,version=?,outcome_json=?,updated_at=? WHERE operation_id=? AND version=? AND request_digest=? AND preview_json=? AND task_id=? AND run_id=? AND repo_id=? AND kind=? AND created_at=?`, o.State, o.Version, o.OutcomeJSON, timeText(o.UpdatedAt), o.ID, expected, o.RequestDigest[:], o.PreviewJSON, o.TaskID.String(), o.RunID.String(), o.RepositoryID.String(), o.Kind, timeText(o.CreatedAt))
	if e != nil {
		return mapWriteError(e)
	}
	n, e := out.RowsAffected()
	if e == nil && n != 1 {
		return storecontract.ErrVersionConflict
	}
	return e
}
