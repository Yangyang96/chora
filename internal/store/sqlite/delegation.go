package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

const delegationColumns = `parent_task_id,version,state,assignments_json,actor_id,session_id,reason,created_at,updated_at`

func scanDelegation(row interface{ Scan(...any) error }) (domain.TaskDelegation, error) {
	var d domain.TaskDelegation
	var id, assignments, created, updated string
	if err := row.Scan(&id, &d.Version, &d.State, &assignments, &d.ActorID, &d.SessionID, &d.Reason, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = storecontract.ErrNotFound
		}
		return d, err
	}
	var err error
	if d.ParentTaskID, err = domain.ParseTaskID(id); err != nil {
		return d, err
	}
	if err = json.Unmarshal([]byte(assignments), &d.Assignments); err != nil {
		return d, err
	}
	if d.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
		return d, err
	}
	if d.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated); err != nil {
		return d, err
	}
	return d, d.Validate()
}
func (r *reader) GetTaskDelegation(ctx context.Context, id domain.TaskID) (domain.TaskDelegation, error) {
	return scanDelegation(r.q.QueryRowContext(ctx, `SELECT `+delegationColumns+` FROM task_delegations WHERE parent_task_id=?`, id.String()))
}
func (r *reader) ListActiveTaskDelegations(ctx context.Context) ([]domain.TaskDelegation, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT `+delegationColumns+` FROM task_delegations WHERE state IN ('running','stopping') ORDER BY created_at,parent_task_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.TaskDelegation{}
	for rows.Next() {
		d, err := scanDelegation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
func (tx *writeTx) InsertTaskDelegation(ctx context.Context, d domain.TaskDelegation) error {
	if err := d.Validate(); err != nil {
		return err
	}
	if d.Version != 1 || d.State != domain.DelegationRunning {
		return storecontract.ErrVersionConflict
	}
	if err := tx.requireActiveRoomForTask(ctx, d.ParentTaskID); err != nil {
		return err
	}
	if _, err := tx.GetDelegationChild(ctx, d.ParentTaskID); !errors.Is(err, storecontract.ErrNotFound) {
		if err != nil {
			return err
		}
		return storecontract.ErrVersionConflict
	}
	raw, err := json.Marshal(d.Assignments)
	if err != nil {
		return err
	}
	_, err = tx.tx.ExecContext(ctx, `INSERT INTO task_delegations (`+delegationColumns+`) VALUES (?,?,?,?,?,?,?,?,?)`, d.ParentTaskID.String(), d.Version, d.State, string(raw), d.ActorID, d.SessionID, d.Reason, timeText(d.CreatedAt), timeText(d.UpdatedAt))
	return mapWriteError(err)
}
func (tx *writeTx) SaveTaskDelegationCAS(ctx context.Context, expected uint64, d domain.TaskDelegation) error {
	old, err := tx.GetTaskDelegation(ctx, d.ParentTaskID)
	if err != nil {
		return err
	}
	next, err := old.Transition(d.State, d.Reason, d.UpdatedAt)
	if err != nil || old.Version != expected || !reflect.DeepEqual(next, d) {
		return storecontract.ErrVersionConflict
	}
	result, err := tx.tx.ExecContext(ctx, `UPDATE task_delegations SET version=?,state=?,reason=?,updated_at=? WHERE parent_task_id=? AND version=?`, d.Version, d.State, d.Reason, timeText(d.UpdatedAt), d.ParentTaskID.String(), expected)
	if err != nil {
		return mapWriteError(err)
	}
	return requireOnePlanningCAS(result)
}
func (tx *writeTx) InsertDelegationChild(ctx context.Context, c domain.DelegationChild) error {
	d, err := tx.GetTaskDelegation(ctx, c.ParentTaskID)
	if err != nil {
		return err
	}
	if d.State != domain.DelegationRunning || c.Position < 0 || c.Position >= len(d.Assignments) || c.CreatedAt.Before(d.CreatedAt) {
		return storecontract.ErrVersionConflict
	}
	parent, err := tx.GetTask(ctx, c.ParentTaskID)
	if err != nil {
		return err
	}
	child, err := tx.GetTask(ctx, c.TaskID)
	if err != nil {
		return err
	}
	if parent.RoomID() != child.RoomID() || parent.ID() == child.ID() {
		return storecontract.ErrVersionConflict
	}
	if err := tx.requireActiveRoomForTask(ctx, c.ParentTaskID); err != nil {
		return err
	}
	_, err = tx.tx.ExecContext(ctx, `INSERT INTO task_delegation_children(parent_task_id,position,task_id,created_at) VALUES(?,?,?,?)`, c.ParentTaskID.String(), c.Position, c.TaskID.String(), timeText(c.CreatedAt))
	return mapWriteError(err)
}
func scanDelegationChild(row interface{ Scan(...any) error }) (domain.DelegationChild, error) {
	var c domain.DelegationChild
	var parent, child, created string
	var run sql.NullString
	if err := row.Scan(&parent, &c.Position, &child, &created, &run); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = storecontract.ErrNotFound
		}
		return c, err
	}
	var err error
	if c.ParentTaskID, err = domain.ParseTaskID(parent); err != nil {
		return c, err
	}
	if c.TaskID, err = domain.ParseTaskID(child); err != nil {
		return c, err
	}
	if run.Valid {
		if c.RunID, err = domain.ParseRunID(run.String); err != nil {
			return c, err
		}
	}
	c.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	return c, err
}
func (r *reader) ListDelegationChildren(ctx context.Context, id domain.TaskID) ([]domain.DelegationChild, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT parent_task_id,position,task_id,created_at,run_id FROM task_delegation_children WHERE parent_task_id=? ORDER BY position`, id.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.DelegationChild{}
	for rows.Next() {
		c, err := scanDelegationChild(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
func (r *reader) GetDelegationChild(ctx context.Context, id domain.TaskID) (domain.DelegationChild, error) {
	return scanDelegationChild(r.q.QueryRowContext(ctx, `SELECT parent_task_id,position,task_id,created_at,run_id FROM task_delegation_children WHERE task_id=?`, id.String()))
}

func (tx *writeTx) BindDelegationChildRun(ctx context.Context, taskID domain.TaskID, runID domain.RunID) error {
	result, err := tx.tx.ExecContext(ctx, `UPDATE task_delegation_children SET run_id=? WHERE task_id=? AND run_id IS NULL AND EXISTS(SELECT 1 FROM task_delegations d WHERE d.parent_task_id=task_delegation_children.parent_task_id AND d.state='running') AND EXISTS(SELECT 1 FROM runs WHERE id=? AND task_id=?)`, runID.String(), taskID.String(), runID.String(), taskID.String())
	if err != nil {
		return mapWriteError(err)
	}
	return requireOnePlanningCAS(result)
}
