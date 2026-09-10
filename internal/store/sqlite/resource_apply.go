package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func (tx *writeTx) InsertResourceApplyOperation(ctx context.Context, o storecontract.ResourceApplyOperation) error {
	if o.ID == "" || !o.ResultID.Valid() || len(o.CanonicalJSON) > domain.RepositoryMetadataBytes || !json.Valid(o.CanonicalJSON) || o.CreatedAt.IsZero() {
		return domain.ErrInvalidArgument
	}
	review, _, e := tx.GetResourceResultReview(ctx, o.ResultID)
	if e != nil {
		return e
	}
	if review.Kind() != domain.ReviewDecisionAccept {
		return storecontract.ErrVerificationConflict
	}
	if e = tx.requireActiveRoomForRun(ctx, review.RunID()); e != nil {
		return e
	}
	_, e = tx.tx.ExecContext(ctx, `INSERT INTO apply_operations(operation_id,result_id,canonical_json,created_at) VALUES(?,?,?,?)`, o.ID, o.ResultID.String(), o.CanonicalJSON, timeText(o.CreatedAt))
	return e
}
func (r *reader) GetResourceApplyOperation(ctx context.Context, id domain.ResultID) (storecontract.ResourceApplyOperation, error) {
	o := storecontract.ResourceApplyOperation{ResultID: id}
	var when string
	e := r.q.QueryRowContext(ctx, `SELECT operation_id,canonical_json,created_at FROM apply_operations WHERE result_id=?`, id.String()).Scan(&o.ID, &o.CanonicalJSON, &when)
	if errors.Is(e, sql.ErrNoRows) {
		return o, storecontract.ErrNotFound
	}
	if e != nil {
		return o, e
	}
	o.CreatedAt, e = parseTime(when)
	return o, e
}
func (r *reader) ListResourceApplySteps(ctx context.Context, id string) ([]storecontract.ResourceApplyStep, error) {
	rows, e := r.q.QueryContext(ctx, `SELECT s.repo_id,s.sequence,s.status,s.canonical_json,s.created_at FROM apply_repository_steps s WHERE s.operation_id=? AND s.sequence=(SELECT max(t.sequence) FROM apply_repository_steps t WHERE t.operation_id=s.operation_id AND t.repo_id=s.repo_id) ORDER BY s.repo_id LIMIT 17`, id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []storecontract.ResourceApplyStep{}
	for rows.Next() {
		s := storecontract.ResourceApplyStep{OperationID: id}
		var repo, when string
		if e = rows.Scan(&repo, &s.Sequence, &s.Status, &s.CanonicalJSON, &when); e != nil {
			return nil, e
		}
		s.RepositoryID, e = domain.ParseRepositoryID(repo)
		if e != nil {
			return nil, e
		}
		s.CreatedAt, e = parseTime(when)
		if e != nil {
			return nil, e
		}
		out = append(out, s)
	}
	if len(out) > domain.TaskRepositoryLimit {
		return nil, storecontract.ErrVerificationConflict
	}
	return out, rows.Err()
}
func (tx *writeTx) AppendResourceApplyStep(ctx context.Context, expected uint64, s storecontract.ResourceApplyStep) error {
	var closed bool
	if err := tx.tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM result_closures closure JOIN apply_operations operation ON operation.result_id=closure.result_id, json_each(closure.repository_ids) entry WHERE operation.operation_id=? AND entry.value=?)`, s.OperationID, s.RepositoryID.String()).Scan(&closed); err != nil {
		return err
	}
	if closed {
		return storecontract.ErrVerificationConflict
	}
	if s.Sequence != expected+1 || !s.RepositoryID.Valid() || len(s.CanonicalJSON) > domain.RepositoryMetadataBytes || !json.Valid(s.CanonicalJSON) || s.CreatedAt.IsZero() {
		return domain.ErrInvalidArgument
	}
	var latest uint64
	var status string
	e := tx.tx.QueryRowContext(ctx, `SELECT sequence,status FROM apply_repository_steps WHERE operation_id=? AND repo_id=? ORDER BY sequence DESC LIMIT 1`, s.OperationID, s.RepositoryID.String()).Scan(&latest, &status)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	if latest != expected || status == "applied" {
		return storecontract.ErrVersionConflict
	}
	var belongs int
	e = tx.tx.QueryRowContext(ctx, `SELECT count(*) FROM apply_operations o JOIN result_repository_changes c ON c.result_id=o.result_id WHERE o.operation_id=? AND c.repo_id=?`, s.OperationID, s.RepositoryID.String()).Scan(&belongs)
	if e != nil {
		return e
	}
	if belongs != 1 {
		return storecontract.ErrVerificationConflict
	}
	_, e = tx.tx.ExecContext(ctx, `INSERT INTO apply_repository_steps(operation_id,repo_id,sequence,status,canonical_json,created_at) VALUES(?,?,?,?,?,?)`, s.OperationID, s.RepositoryID.String(), s.Sequence, s.Status, s.CanonicalJSON, timeText(s.CreatedAt))
	return e
}
