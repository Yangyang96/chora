package sqlite

import (
	"context"
	"database/sql"
	"errors"

	storecontract "github.com/Yangyang96/chora/internal/store"
)

func (tx *writeTx) LookupCommand(ctx context.Context, key storecontract.CommandKey) (storecontract.Response, bool, error) {
	var digest, body []byte
	var status int
	var contentType string
	var command, resource string
	err := tx.tx.QueryRowContext(ctx, `SELECT command,resource_id,request_digest,response_status,response_content_type,response_body FROM command_idempotency WHERE key_hash=?`, key.KeyHash[:]).Scan(&command, &resource, &digest, &status, &contentType, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return storecontract.Response{}, false, nil
	}
	if err != nil {
		return storecontract.Response{}, false, err
	}
	if command != key.Command || resource != key.ResourceID || len(digest) != 32 || string(digest) != string(key.RequestDigest[:]) {
		return storecontract.Response{}, false, storecontract.ErrIdempotencyConflict
	}
	return storecontract.Response{Status: status, ContentType: contentType, Body: append([]byte(nil), body...)}, true, nil
}

func (tx *writeTx) SaveCommand(ctx context.Context, key storecontract.CommandKey, response storecontract.Response) error {
	if response.Status == 0 {
		response.Status = 200
	}
	if response.ContentType == "" {
		response.ContentType = "application/json"
	}
	if response.Body == nil {
		response.Body = []byte{}
	}
	_, err := tx.tx.ExecContext(ctx, `INSERT INTO command_idempotency(key_hash,command,resource_id,request_digest,response_status,response_content_type,response_body,created_at,expires_at) VALUES(?,?,?,?,?,?,?,?,?)`, key.KeyHash[:], key.Command, key.ResourceID, key.RequestDigest[:], response.Status, response.ContentType, response.Body, timeText(key.CreatedAt), nullableTime(key.ExpiresAt))
	return mapWriteError(err)
}

func (tx *writeTx) UpdateCommand(ctx context.Context, key storecontract.CommandKey, response storecontract.Response) error {
	if response.Status == 0 {
		response.Status = 200
	}
	if response.ContentType == "" {
		response.ContentType = "application/json"
	}
	if response.Body == nil {
		response.Body = []byte{}
	}
	result, err := tx.tx.ExecContext(ctx, `UPDATE command_idempotency SET response_status=?,response_content_type=?,response_body=? WHERE key_hash=? AND command=? AND resource_id=? AND request_digest=?`, response.Status, response.ContentType, response.Body, key.KeyHash[:], key.Command, key.ResourceID, key.RequestDigest[:])
	if err != nil {
		return mapWriteError(err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return storecontract.ErrIdempotencyConflict
	}
	return nil
}
