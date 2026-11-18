package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func workflowFrozen(w domain.WorkflowExecution) []byte {
	b, _ := json.Marshal(struct {
		Graph, Question, Revision string
		Configuration             json.RawMessage
		Created                   any
		PiAuthorization *domain.WorkflowPiAuthorization `json:",omitempty"`
	}{w.GraphVersion, w.Question, w.RevisionID.String(), w.Configuration, w.CreatedAt, w.PiAuthorization})
	return b
}
func (tx *writeTx) InsertWorkflow(ctx context.Context, w domain.WorkflowExecution) error {
	b, err := json.Marshal(w)
	if err != nil {
		return err
	}
	_, err = tx.tx.ExecContext(ctx, `INSERT INTO workflow_executions(id,task_id,project_id,actor_id,session_id,version,status,input_digest,authority_digest,frozen_json,state_json) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, w.ID, w.TaskID.String(), w.ProjectID.String(), w.ActorID, w.SessionID, w.Version, w.Status, w.InputDigest, w.AuthorityDigest, string(workflowFrozen(w)), string(b))
	return mapWriteError(err)
}
func (r *reader) GetWorkflow(ctx context.Context, id string) (domain.WorkflowExecution, error) {
	return r.readWorkflow(ctx, `SELECT state_json,session_id FROM workflow_executions WHERE id=?`, id)
}
func (r *reader) GetTaskWorkflow(ctx context.Context, id domain.TaskID) (domain.WorkflowExecution, error) {
	return r.readWorkflow(ctx, `SELECT state_json,session_id FROM workflow_executions WHERE task_id=?`, id.String())
}
func (r *reader) readWorkflow(ctx context.Context, query, id string) (w domain.WorkflowExecution, err error) {
	var b, session string
	err = r.q.QueryRowContext(ctx, query, id).Scan(&b, &session)
	if errors.Is(err, sql.ErrNoRows) {
		return w, storecontract.ErrNotFound
	}
	if err != nil {
		return
	}
	err = json.Unmarshal([]byte(b), &w)
	w.SessionID = session
	return
}
func (r *reader) ListActiveWorkflows(ctx context.Context) ([]domain.WorkflowExecution, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT state_json,session_id FROM workflow_executions WHERE status NOT IN ('stopped','rejected','failed','awaiting_review') ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.WorkflowExecution{}
	for rows.Next() {
		var b, session string
		if err = rows.Scan(&b, &session); err != nil {
			return nil, err
		}
		var w domain.WorkflowExecution
		if err = json.Unmarshal([]byte(b), &w); err != nil {
			return nil, err
		}
		w.SessionID = session
		result = append(result, w)
	}
	return result, rows.Err()
}
func (tx *writeTx) SaveWorkflowCAS(ctx context.Context, expected uint64, w domain.WorkflowExecution) error {
	b, err := json.Marshal(w)
	if err != nil {
		return err
	}
	res, err := tx.tx.ExecContext(ctx, `UPDATE workflow_executions SET version=?,status=?,state_json=?,frozen_json=?,task_id=?,project_id=?,actor_id=?,session_id=?,input_digest=?,authority_digest=? WHERE id=? AND version=?`, w.Version, w.Status, string(b), string(workflowFrozen(w)), w.TaskID.String(), w.ProjectID.String(), w.ActorID, w.SessionID, w.InputDigest, w.AuthorityDigest, w.ID, expected)
	if err != nil {
		return mapWriteError(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return storecontract.ErrVersionConflict
	}
	return nil
}
func (tx *writeTx) PutWorkflowCommand(ctx context.Context, c domain.WorkflowCommand) error {
	if domain.WorkflowDigest([]byte(c.Payload)) != c.InputDigest {
		return storecontract.ErrIdempotencyConflict
	}
	_, err := tx.tx.ExecContext(ctx, `INSERT INTO workflow_commands(operation_id,workflow_id,kind,input_digest,payload) VALUES(?,?,?,?,?) ON CONFLICT DO NOTHING`, c.OperationID, c.WorkflowID, c.Kind, c.InputDigest, c.Payload)
	if err != nil {
		return mapWriteError(err)
	}
	var workflow, kind, digest, payload string
	if err = tx.tx.QueryRowContext(ctx, `SELECT workflow_id,kind,input_digest,payload FROM workflow_commands WHERE operation_id=?`, c.OperationID).Scan(&workflow, &kind, &digest, &payload); err != nil {
		return err
	}
	if workflow != c.WorkflowID || kind != c.Kind || digest != c.InputDigest || payload != c.Payload {
		return storecontract.ErrIdempotencyConflict
	}
	return nil
}
func (r *reader) ListWorkflowCommands(ctx context.Context, id string) ([]domain.WorkflowCommand, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT operation_id,kind,input_digest,payload,receipt FROM workflow_commands WHERE workflow_id=? ORDER BY CASE kind WHEN 'stop' THEN 0 ELSE 1 END,operation_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.WorkflowCommand{}
	for rows.Next() {
		c := domain.WorkflowCommand{WorkflowID: id}
		var receipt sql.NullString
		if err = rows.Scan(&c.OperationID, &c.Kind, &c.InputDigest, &c.Payload, &receipt); err != nil {
			return nil, err
		}
		if receipt.Valid {
			c.Receipt = json.RawMessage(receipt.String)
		}
		result = append(result, c)
	}
	return result, rows.Err()
}
func (tx *writeTx) RecordWorkflowReceipt(ctx context.Context, c domain.WorkflowCommand, b json.RawMessage) error {
	// PG jsonb may reorder keys when a committed receipt is read after a lost
	// response. Compare canonical JSON, retaining exact integer representations.
	var value any
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return err
	}
	b = canonical
	var digest string
	var old sql.NullString
	if err := tx.tx.QueryRowContext(ctx, `SELECT input_digest,receipt FROM workflow_commands WHERE operation_id=? AND workflow_id=?`, c.OperationID, c.WorkflowID).Scan(&digest, &old); err != nil {
		return err
	}
	if digest != c.InputDigest || old.Valid && old.String != string(b) {
		return storecontract.ErrIdempotencyConflict
	}
	if old.Valid {
		return nil
	}
	_, err = tx.tx.ExecContext(ctx, `UPDATE workflow_commands SET receipt=? WHERE operation_id=?`, string(b), c.OperationID)
	return mapWriteError(err)
}
func (tx *writeTx) AppendWorkflowEvent(ctx context.Context, id, kind string, b json.RawMessage) error {
	_, err := tx.tx.ExecContext(ctx, `INSERT INTO workflow_events(workflow_id,sequence,kind,data) SELECT ?,COALESCE(MAX(sequence),0)+1,?,? FROM workflow_events WHERE workflow_id=?`, id, kind, string(b), id)
	return mapWriteError(err)
}
func (r *reader) ListWorkflowEvents(ctx context.Context, id string, after uint64) ([]domain.WorkflowEvent, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT sequence,kind,data FROM workflow_events WHERE workflow_id=? AND sequence>? ORDER BY sequence LIMIT 200`, id, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.WorkflowEvent{}
	for rows.Next() {
		var e domain.WorkflowEvent
		var b string
		if err = rows.Scan(&e.Sequence, &e.Kind, &b); err != nil {
			return nil, err
		}
		e.Data = json.RawMessage(b)
		result = append(result, e)
	}
	return result, rows.Err()
}
