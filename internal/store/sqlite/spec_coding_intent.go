package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func (tx *writeTx) InsertSpecCodingIntent(ctx context.Context, intent storecontract.SpecCodingIntent) error {
	if err := tx.requireActiveRoomForTask(ctx, intent.TaskID); err != nil {
		return preserveWriteConflict(err, storecontract.ErrSpecCodingConflict)
	}
	if !validSpecCodingIntent(intent) {
		return storecontract.ErrSpecCodingConflict
	}
	if _, err := tx.tx.ExecContext(ctx, `INSERT INTO user_spec_coding_intents(task_id,intent_digest,intent_json,created_at) VALUES(?,?,?,?)`,
		intent.TaskID.String(), intent.IntentDigest[:], intent.IntentJSON, timeText(intent.CreatedAt)); err != nil {
		return storecontract.ErrSpecCodingConflict
	}
	return nil
}

func (reader *reader) GetSpecCodingIntent(ctx context.Context, taskID domain.TaskID) (storecontract.SpecCodingIntent, error) {
	if !taskID.Valid() {
		return storecontract.SpecCodingIntent{}, storecontract.ErrSpecCodingConflict
	}
	var taskText, createdAtText string
	var digest, canonicalJSON []byte
	err := reader.q.QueryRowContext(ctx, `SELECT task_id,intent_digest,intent_json,created_at FROM user_spec_coding_intents WHERE task_id=?`, taskID.String()).Scan(
		&taskText, &digest, &canonicalJSON, &createdAtText)
	if errors.Is(err, sql.ErrNoRows) {
		return storecontract.SpecCodingIntent{}, storecontract.ErrNotFound
	}
	if err != nil {
		return storecontract.SpecCodingIntent{}, err
	}
	parsedTask, err := domain.ParseTaskID(taskText)
	if err != nil {
		return storecontract.SpecCodingIntent{}, storecontract.ErrSpecCodingConflict
	}
	createdAt, err := parseTime(createdAtText)
	if err != nil {
		return storecontract.SpecCodingIntent{}, storecontract.ErrSpecCodingConflict
	}
	intent := storecontract.SpecCodingIntent{
		TaskID:     parsedTask,
		IntentJSON: append([]byte(nil), canonicalJSON...),
		CreatedAt:  createdAt,
	}
	if !copyDigest(&intent.IntentDigest, digest) || !validSpecCodingIntent(intent) {
		return storecontract.SpecCodingIntent{}, storecontract.ErrSpecCodingConflict
	}
	return intent, nil
}

func validSpecCodingIntent(intent storecontract.SpecCodingIntent) bool {
	return intent.TaskID.Valid() &&
		intent.IntentDigest != ([32]byte{}) &&
		len(intent.IntentJSON) != 0 &&
		json.Valid(intent.IntentJSON) &&
		sha256.Sum256(intent.IntentJSON) == intent.IntentDigest &&
		!intent.CreatedAt.IsZero()
}
