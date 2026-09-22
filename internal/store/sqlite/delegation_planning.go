package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

const planningColumns = `parent_task_id,run_id,attempt_id,version,state,authority_digest,actor_id,session_id,reason,created_at,updated_at`

func scanPlanning(row interface{ Scan(...any) error }) (domain.DelegationPlanningIntent, error) {
	var p domain.DelegationPlanningIntent
	var task, run, created, updated string
	var attempt sql.NullString
	var digest []byte
	err := row.Scan(&task, &run, &attempt, &p.Version, &p.State, &digest, &p.ActorID, &p.SessionID, &p.Reason, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return p, storecontract.ErrNotFound
	}
	if err != nil {
		return p, err
	}
	if p.ParentTaskID, err = domain.ParseTaskID(task); err != nil {
		return p, err
	}
	if p.RunID, err = domain.ParseRunID(run); err != nil {
		return p, err
	}
	if attempt.Valid {
		if p.AttemptID, err = domain.ParseAttemptID(attempt.String); err != nil {
			return p, err
		}
	}
	if p.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
		return p, err
	}
	if p.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated); err != nil {
		return p, err
	}
	if len(digest) != 32 {
		return p, storecontract.ErrVerificationConflict
	}
	copy(p.AuthorityDigest[:], digest)
	return p, p.Validate()
}
func (r *reader) GetDelegationPlanning(ctx context.Context, id domain.TaskID) (domain.DelegationPlanningIntent, error) {
	return scanPlanning(r.q.QueryRowContext(ctx, `SELECT `+planningColumns+` FROM delegation_planning_intents WHERE parent_task_id=?`, id.String()))
}
func (r *reader) ListActiveDelegationPlanning(ctx context.Context) ([]domain.DelegationPlanningIntent, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT `+planningColumns+` FROM delegation_planning_intents WHERE state IN ('planning','stopping') ORDER BY created_at,parent_task_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.DelegationPlanningIntent{}
	for rows.Next() {
		p, e := scanPlanning(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (tx *writeTx) InsertDelegationPlanning(ctx context.Context, p domain.DelegationPlanningIntent) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if p.Version != 1 || p.State != domain.DelegationPlanningRunning || p.AttemptID.Valid() {
		return storecontract.ErrVersionConflict
	}
	if _, e := tx.GetTaskDelegation(ctx, p.ParentTaskID); !errors.Is(e, storecontract.ErrNotFound) {
		if e != nil {
			return e
		}
		return storecontract.ErrVersionConflict
	}
	_, err := tx.tx.ExecContext(ctx, `INSERT INTO delegation_planning_intents (`+planningColumns+`) VALUES (?,?,NULL,?,?,?,?,?,?,?,?)`, p.ParentTaskID.String(), p.RunID.String(), p.Version, p.State, p.AuthorityDigest[:], p.ActorID, p.SessionID, p.Reason, timeText(p.CreatedAt), timeText(p.UpdatedAt))
	return mapWriteError(err)
}
func (tx *writeTx) SaveDelegationPlanningCAS(ctx context.Context, expected uint64, p domain.DelegationPlanningIntent) error {
	old, err := tx.GetDelegationPlanning(ctx, p.ParentTaskID)
	if err != nil {
		return err
	}
	var next domain.DelegationPlanningIntent
	if !old.AttemptID.Valid() && p.AttemptID.Valid() && p.State == old.State {
		next, err = old.BindAttempt(p.AttemptID, p.UpdatedAt)
	} else {
		next, err = old.Transition(p.State, p.Reason, p.UpdatedAt)
	}
	if err != nil || old.Version != expected || !reflect.DeepEqual(next, p) {
		return storecontract.ErrVersionConflict
	}
	var attempt any
	if p.AttemptID.Valid() {
		attempt = p.AttemptID.String()
	}
	result, err := tx.tx.ExecContext(ctx, `UPDATE delegation_planning_intents SET attempt_id=?,version=?,state=?,reason=?,updated_at=? WHERE parent_task_id=? AND version=?`, attempt, p.Version, p.State, p.Reason, timeText(p.UpdatedAt), p.ParentTaskID.String(), expected)
	if err != nil {
		return mapWriteError(err)
	}
	return requireOnePlanningCAS(result)
}
