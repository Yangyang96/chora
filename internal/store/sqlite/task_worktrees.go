package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func taskWorktreeWriteError(err error) error {
	if err == nil {
		return nil
	}
	text := strings.ToLower(err.Error())
	if strings.Contains(text, "task worktree") || strings.Contains(text, "task_worktrees") || strings.Contains(text, "constraint failed") {
		return fmt.Errorf("%w: %v", storecontract.ErrTaskWorktreeConflict, err)
	}
	return err
}

func (tx *writeTx) InsertTaskWorktreeBinding(ctx context.Context, binding domain.TaskWorktreeBinding) error {
	fingerprint := binding.ConfiguredRootFingerprint()
	_, err := tx.tx.ExecContext(ctx, `INSERT INTO task_worktrees(
task_id,repository_identity,pinned_base_revision,pinned_base_tree,base_ref,start_policy,relative_locator,configured_root_fingerprint,state,version,reason,created_at,updated_at,ready_at
) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, binding.TaskID().String(), binding.RepositoryIdentity(), binding.PinnedBaseRevision(), binding.PinnedBaseTree(), binding.BaseRef(), binding.StartPolicy(), binding.RelativeLocator(), fingerprint[:],
		string(binding.State()), binding.Version(), binding.Reason(), timeText(binding.CreatedAt()), timeText(binding.UpdatedAt()), nullableTime(binding.ReadyAt()))
	return taskWorktreeWriteError(err)
}

func (tx *writeTx) SaveTaskWorktreeBindingCAS(ctx context.Context, expected uint64, binding domain.TaskWorktreeBinding) error {
	if expected == ^uint64(0) || binding.Version() != expected+1 {
		return fmt.Errorf("%w: Task worktree version must advance by one", storecontract.ErrTaskWorktreeConflict)
	}
	fingerprint := binding.ConfiguredRootFingerprint()
	result, err := tx.tx.ExecContext(ctx, `UPDATE task_worktrees SET state=?,version=?,reason=?,updated_at=?,ready_at=?
WHERE task_id=? AND version=? AND repository_identity=? AND pinned_base_revision=? AND pinned_base_tree=? AND base_ref=? AND start_policy=? AND relative_locator=? AND configured_root_fingerprint=? AND created_at=?`,
		string(binding.State()), binding.Version(), binding.Reason(), timeText(binding.UpdatedAt()), nullableTime(binding.ReadyAt()),
		binding.TaskID().String(), expected, binding.RepositoryIdentity(), binding.PinnedBaseRevision(), binding.PinnedBaseTree(), binding.BaseRef(), binding.StartPolicy(), binding.RelativeLocator(), fingerprint[:], timeText(binding.CreatedAt()))
	if err != nil {
		return taskWorktreeWriteError(err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return storecontract.ErrVersionConflict
	}
	return nil
}

func (reader *reader) GetTaskWorktreeBinding(ctx context.Context, taskID domain.TaskID) (domain.TaskWorktreeBinding, error) {
	row := reader.q.QueryRowContext(ctx, `SELECT task_id,repository_identity,pinned_base_revision,pinned_base_tree,base_ref,start_policy,relative_locator,configured_root_fingerprint,state,version,reason,created_at,updated_at,ready_at FROM task_worktrees WHERE task_id=?`, taskID.String())
	binding, err := scanTaskWorktreeBinding(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.TaskWorktreeBinding{}, storecontract.ErrNotFound
	}
	return binding, err
}

// ListTaskWorktreeRecoveryCandidates returns every non-ready binding whose
// filesystem state must be ensured at startup, including interrupted initial
// provisioning and bindings already marked recovery_required.
func (reader *reader) ListTaskWorktreeRecoveryCandidates(ctx context.Context) ([]domain.TaskWorktreeBinding, error) {
	rows, err := reader.q.QueryContext(ctx, `SELECT task_id,repository_identity,pinned_base_revision,pinned_base_tree,base_ref,start_policy,relative_locator,configured_root_fingerprint,state,version,reason,created_at,updated_at,ready_at FROM task_worktrees WHERE state IN ('provisioning','recovery_required') ORDER BY updated_at,task_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.TaskWorktreeBinding
	for rows.Next() {
		binding, err := scanTaskWorktreeBinding(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, binding)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

type taskWorktreeScanner interface {
	Scan(...any) error
}

func scanTaskWorktreeBinding(scanner taskWorktreeScanner) (domain.TaskWorktreeBinding, error) {
	var taskID, repository, revision, tree, baseRef, startPolicy, locator, state, reason, createdText, updatedText string
	var fingerprintBytes []byte
	var version uint64
	var readyText sql.NullString
	if err := scanner.Scan(&taskID, &repository, &revision, &tree, &baseRef, &startPolicy, &locator, &fingerprintBytes, &state, &version, &reason, &createdText, &updatedText, &readyText); err != nil {
		return domain.TaskWorktreeBinding{}, err
	}
	var fingerprint [32]byte
	if !copyDigest(&fingerprint, fingerprintBytes) {
		return domain.TaskWorktreeBinding{}, fmt.Errorf("%w: invalid configured-root fingerprint", storecontract.ErrTaskWorktreeConflict)
	}
	createdAt, err := parseTime(createdText)
	if err != nil {
		return domain.TaskWorktreeBinding{}, fmt.Errorf("%w: invalid created timestamp", storecontract.ErrTaskWorktreeConflict)
	}
	updatedAt, err := parseTime(updatedText)
	if err != nil {
		return domain.TaskWorktreeBinding{}, fmt.Errorf("%w: invalid updated timestamp", storecontract.ErrTaskWorktreeConflict)
	}
	readyAt, err := parseTaskWorktreeReadyTime(readyText)
	if err != nil {
		return domain.TaskWorktreeBinding{}, err
	}
	binding, err := domain.RestoreTaskWorktreeBinding(domain.TaskWorktreeBindingParams{
		TaskID: taskID, RepositoryIdentity: repository, PinnedBaseRevision: revision, PinnedBaseTree: tree, BaseRef: baseRef, StartPolicy: startPolicy, RelativeLocator: locator,
		ConfiguredRootFingerprint: fingerprint, State: domain.TaskWorktreeState(state), Version: version, Reason: reason,
		CreatedAt: createdAt, UpdatedAt: updatedAt, ReadyAt: readyAt,
	})
	if err != nil {
		return domain.TaskWorktreeBinding{}, fmt.Errorf("%w: %v", storecontract.ErrTaskWorktreeConflict, err)
	}
	return binding, nil
}

func parseTaskWorktreeReadyTime(value sql.NullString) (time.Time, error) {
	readyAt, err := parseNullableTime(value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: invalid ready timestamp", storecontract.ErrTaskWorktreeConflict)
	}
	return readyAt, nil
}
