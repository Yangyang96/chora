package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func (r *reader) GetRepositoryDeliveryDefault(ctx context.Context, id domain.RepositoryID) (domain.RepositoryDeliveryDefault, error) {
	result := domain.RepositoryDeliveryDefault{RepositoryID: id}
	var updated string
	err := r.q.QueryRowContext(ctx, `SELECT target_ref,version,updated_at FROM repository_delivery_defaults WHERE repo_id=?`, id.String()).Scan(&result.TargetRef, &result.Version, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return result, storecontract.ErrNotFound
	}
	if err != nil {
		return result, err
	}
	result.UpdatedAt, err = parseTime(updated)
	if err != nil {
		return result, err
	}
	return result, result.Validate()
}

func (tx *writeTx) SaveRepositoryDeliveryDefault(ctx context.Context, expected uint64, value domain.RepositoryDeliveryDefault) error {
	if err := value.Validate(); err != nil || value.Version != expected+1 {
		return domain.ErrInvalidArgument
	}
	if _, err := tx.GetRepository(ctx, value.RepositoryID); err != nil {
		return err
	}
	if expected == 0 {
		_, err := tx.tx.ExecContext(ctx, `INSERT INTO repository_delivery_defaults(repo_id,target_ref,version,updated_at) VALUES(?,?,?,?)`, value.RepositoryID.String(), value.TargetRef, value.Version, timeText(value.UpdatedAt))
		return mapWriteError(err)
	}
	result, err := tx.tx.ExecContext(ctx, `UPDATE repository_delivery_defaults SET target_ref=?,version=?,updated_at=? WHERE repo_id=? AND version=?`, value.TargetRef, value.Version, timeText(value.UpdatedAt), value.RepositoryID.String(), expected)
	if err != nil {
		return mapWriteError(err)
	}
	count, err := result.RowsAffected()
	if err == nil && count != 1 {
		return storecontract.ErrVersionConflict
	}
	return err
}
