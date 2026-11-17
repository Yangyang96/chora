package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func (r *reader) GetRestrictedLaunch(ctx context.Context, channel string) (b domain.RestrictedLaunch, revoked bool, err error) {
	var raw string
	err = r.q.QueryRowContext(ctx, `SELECT binding,revoked FROM workflow_pi_launches WHERE channel=?`, channel).Scan(&raw, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		err = storecontract.ErrNotFound
	}
	if err == nil {
		err = json.Unmarshal([]byte(raw), &b)
		if err == nil {
			err = b.Validate()
		}
	}
	return
}
func (tx *writeTx) InsertRestrictedLaunch(ctx context.Context, b domain.RestrictedLaunch) error {
	if err := b.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	_, err = tx.tx.ExecContext(ctx, `INSERT INTO workflow_pi_launches(channel,workflow_id,attempt_id,binding) VALUES(?,?,?,?)`, b.Channel, b.Workflow, b.Attempt, string(raw))
	return mapWriteError(err)
}
func (tx *writeTx) RevokeRestrictedLaunch(ctx context.Context, channel string) error {
	_, err := tx.tx.ExecContext(ctx, `UPDATE workflow_pi_launches SET revoked=1 WHERE channel=? AND revoked=0`, channel)
	return mapWriteError(err)
}

func (tx *writeTx) RevokeRestrictedChannels(ctx context.Context) error {
	_, err := tx.tx.ExecContext(ctx, `UPDATE workflow_pi_launches SET revoked=1 WHERE revoked=0`)
	return mapWriteError(err)
}
