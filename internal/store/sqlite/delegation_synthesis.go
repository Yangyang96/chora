package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

const synthesisColumns = `parent_task_id,task_id,run_id,attempt_id,inputs_json,actor_id,session_id,created_at`

func scanSynthesis(row interface{ Scan(...any) error }) (domain.DelegationSynthesis, error) {
	var s domain.DelegationSynthesis
	var parent, created string
	var task, run, attempt, inputs sql.NullString
	err := row.Scan(&parent, &task, &run, &attempt, &inputs, &s.ActorID, &s.SessionID, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return s, storecontract.ErrNotFound
	}
	if err != nil {
		return s, err
	}
	if s.ParentTaskID, err = domain.ParseTaskID(parent); err != nil {
		return s, err
	}
	if task.Valid {
		if s.TaskID, err = domain.ParseTaskID(task.String); err != nil {
			return s, err
		}
	}
	if run.Valid {
		if s.RunID, err = domain.ParseRunID(run.String); err != nil {
			return s, err
		}
	}
	if attempt.Valid {
		if s.AttemptID, err = domain.ParseAttemptID(attempt.String); err != nil {
			return s, err
		}
	}
	if inputs.Valid {
		if err = json.Unmarshal([]byte(inputs.String), &s.Inputs); err != nil {
			return s, err
		}
	}
	if s.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
		return s, err
	}
	return s, s.Validate()
}
func (r *reader) GetDelegationSynthesis(ctx context.Context, id domain.TaskID) (domain.DelegationSynthesis, error) {
	return scanSynthesis(r.q.QueryRowContext(ctx, `SELECT `+synthesisColumns+` FROM delegation_synthesis WHERE parent_task_id=?`, id.String()))
}
func (r *reader) GetSynthesisForTask(ctx context.Context, id domain.TaskID) (domain.DelegationSynthesis, error) {
	return scanSynthesis(r.q.QueryRowContext(ctx, `SELECT `+synthesisColumns+` FROM delegation_synthesis WHERE task_id=?`, id.String()))
}
func (tx *writeTx) InsertDelegationSynthesis(ctx context.Context, s domain.DelegationSynthesis) error {
	if e := s.Validate(); e != nil {
		return e
	}
	if s.TaskID.Valid() {
		return storecontract.ErrVersionConflict
	}
	_, err := tx.tx.ExecContext(ctx, `INSERT INTO delegation_synthesis(parent_task_id,actor_id,session_id,created_at) VALUES(?,?,?,?)`, s.ParentTaskID.String(), s.ActorID, s.SessionID, timeText(s.CreatedAt))
	return mapWriteError(err)
}
func (tx *writeTx) BindSynthesisTask(ctx context.Context, parent, task domain.TaskID, inputs []domain.DelegationSynthesisInput) error {
	s, err := tx.GetDelegationSynthesis(ctx, parent)
	if err != nil {
		return err
	}
	if s.TaskID.Valid() {
		return storecontract.ErrVersionConflict
	}
	d, err := tx.GetTaskDelegation(ctx, parent)
	if err != nil {
		return err
	}
	if d.State != domain.DelegationRunning {
		return storecontract.ErrVersionConflict
	}
	s.TaskID = task
	s.Inputs = inputs
	if err = s.Validate(); err != nil {
		return err
	}
	children, err := tx.ListDelegationChildren(ctx, parent)
	if err != nil {
		return err
	}
	if len(children) != len(inputs) || len(inputs) != len(d.Assignments) {
		return storecontract.ErrVerificationConflict
	}
	for i, input := range inputs {
		if children[i].TaskID != input.TaskID || children[i].RunID != input.RunID {
			return storecontract.ErrVerificationConflict
		}
		group, e := tx.GetResourceResultGroup(ctx, input.AttemptID)
		if e != nil {
			return e
		}
		_, digest, e := group.CanonicalJSON()
		if e != nil {
			return e
		}
		if digest != input.ResultDigest || group.ID != input.ResultID.String() || group.Markdown != input.Markdown {
			return storecontract.ErrVerificationConflict
		}
	}
	raw, err := json.Marshal(inputs)
	if err != nil {
		return err
	}
	result, err := tx.tx.ExecContext(ctx, `UPDATE delegation_synthesis SET task_id=?,inputs_json=? WHERE parent_task_id=? AND task_id IS NULL`, task.String(), string(raw), parent.String())
	if err != nil {
		return mapWriteError(err)
	}
	return requireOnePlanningCAS(result)
}
func (tx *writeTx) BindSynthesisRun(ctx context.Context, task domain.TaskID, run domain.RunID) error {
	s, err := tx.GetSynthesisForTask(ctx, task)
	if err != nil {
		return err
	}
	d, err := tx.GetTaskDelegation(ctx, s.ParentTaskID)
	if err != nil {
		return err
	}
	if s.RunID.Valid() || d.State != domain.DelegationRunning {
		return storecontract.ErrVersionConflict
	}
	result, err := tx.tx.ExecContext(ctx, `UPDATE delegation_synthesis SET run_id=? WHERE task_id=? AND run_id IS NULL`, run.String(), task.String())
	if err != nil {
		return mapWriteError(err)
	}
	return requireOnePlanningCAS(result)
}
func (tx *writeTx) BindSynthesisAttempt(ctx context.Context, task domain.TaskID, attempt domain.AttemptID) error {
	s, err := tx.GetSynthesisForTask(ctx, task)
	if err != nil {
		return err
	}
	d, err := tx.GetTaskDelegation(ctx, s.ParentTaskID)
	if err != nil {
		return err
	}
	if s.AttemptID.Valid() || !s.RunID.Valid() || d.State != domain.DelegationRunning {
		return storecontract.ErrVersionConflict
	}
	result, err := tx.tx.ExecContext(ctx, `UPDATE delegation_synthesis SET attempt_id=? WHERE task_id=? AND attempt_id IS NULL`, attempt.String(), task.String())
	if err != nil {
		return mapWriteError(err)
	}
	return requireOnePlanningCAS(result)
}
