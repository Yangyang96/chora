package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func (tx *writeTx) InsertSpecCodingBinding(ctx context.Context, binding storecontract.SpecCodingBinding) error {
	if err := tx.requireActiveRoomForTask(ctx, binding.TaskID); err != nil {
		return preserveWriteConflict(err, storecontract.ErrSpecCodingConflict)
	}
	if !validMaterializedBinding(binding) {
		return storecontract.ErrSpecCodingConflict
	}
	_, err := tx.tx.ExecContext(ctx, `INSERT INTO spec_coding_bindings(task_id,snapshot_id,snapshot_digest,materialized_contract_digest,materialized_contract_json,status,materialized_at) VALUES(?,?,?,?,?,'materialized',?)`,
		binding.TaskID.String(), binding.SnapshotID.String(), binding.SnapshotDigest[:], binding.MaterializedContractDigest[:], binding.MaterializedContractJSON, timeText(binding.MaterializedAt))
	if err != nil {
		return storecontract.ErrSpecCodingConflict
	}
	return nil
}

func (tx *writeTx) RegisterSpecCodingBinding(ctx context.Context, binding storecontract.SpecCodingBinding) error {
	if err := tx.requireActiveRoomForTask(ctx, binding.TaskID); err != nil {
		return preserveWriteConflict(err, storecontract.ErrSpecCodingConflict)
	}
	if !validRegisteredBinding(binding) {
		return storecontract.ErrSpecCodingConflict
	}
	result, err := tx.tx.ExecContext(ctx, `UPDATE spec_coding_bindings SET active_contract_digest=?,active_contract_json=?,status='registered',registered_at=? WHERE task_id=? AND snapshot_id=? AND snapshot_digest=? AND materialized_contract_digest=? AND materialized_contract_json=? AND status='materialized'`,
		binding.ActiveContractDigest[:], binding.ActiveContractJSON, timeText(binding.RegisteredAt), binding.TaskID.String(), binding.SnapshotID.String(), binding.SnapshotDigest[:], binding.MaterializedContractDigest[:], binding.MaterializedContractJSON)
	if err != nil {
		return storecontract.ErrSpecCodingConflict
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return storecontract.ErrSpecCodingConflict
	}
	return nil
}

func (reader *reader) GetSpecCodingBinding(ctx context.Context, taskID domain.TaskID) (storecontract.SpecCodingBinding, error) {
	var taskText, snapshotText, status, materializedAt string
	var registeredAt sql.NullString
	var snapshotDigest, materializedDigest, materializedJSON, activeDigest, activeJSON []byte
	err := reader.q.QueryRowContext(ctx, `SELECT task_id,snapshot_id,snapshot_digest,materialized_contract_digest,materialized_contract_json,COALESCE(active_contract_digest,X''),COALESCE(active_contract_json,X''),status,materialized_at,registered_at FROM spec_coding_bindings WHERE task_id=?`, taskID.String()).Scan(
		&taskText, &snapshotText, &snapshotDigest, &materializedDigest, &materializedJSON, &activeDigest, &activeJSON, &status, &materializedAt, &registeredAt)
	if errors.Is(err, sql.ErrNoRows) {
		return storecontract.SpecCodingBinding{}, storecontract.ErrNotFound
	}
	if err != nil {
		return storecontract.SpecCodingBinding{}, err
	}
	parsedTask, err := domain.ParseTaskID(taskText)
	if err != nil {
		return storecontract.SpecCodingBinding{}, err
	}
	parsedSnapshot, err := domain.ParseContextSnapshotID(snapshotText)
	if err != nil {
		return storecontract.SpecCodingBinding{}, err
	}
	materializedTime, err := parseTime(materializedAt)
	if err != nil {
		return storecontract.SpecCodingBinding{}, err
	}
	binding := storecontract.SpecCodingBinding{TaskID: parsedTask, SnapshotID: parsedSnapshot, Status: status, MaterializedContractJSON: append([]byte(nil), materializedJSON...), ActiveContractJSON: append([]byte(nil), activeJSON...), MaterializedAt: materializedTime}
	if !copyDigest(&binding.SnapshotDigest, snapshotDigest) || !copyDigest(&binding.MaterializedContractDigest, materializedDigest) {
		return storecontract.SpecCodingBinding{}, storecontract.ErrSpecCodingConflict
	}
	if registeredAt.Valid {
		binding.RegisteredAt, err = parseTime(registeredAt.String)
		if err != nil || !copyDigest(&binding.ActiveContractDigest, activeDigest) {
			return storecontract.SpecCodingBinding{}, storecontract.ErrSpecCodingConflict
		}
	}
	if binding.Status == storecontract.SpecCodingMaterialized && !validMaterializedBinding(binding) || binding.Status == storecontract.SpecCodingRegistered && !validRegisteredBinding(binding) || binding.Status != storecontract.SpecCodingMaterialized && binding.Status != storecontract.SpecCodingRegistered {
		return storecontract.SpecCodingBinding{}, storecontract.ErrSpecCodingConflict
	}
	return binding, nil
}

func validMaterializedBinding(binding storecontract.SpecCodingBinding) bool {
	return binding.TaskID.Valid() && binding.SnapshotID.Valid() && binding.SnapshotDigest != ([32]byte{}) && binding.MaterializedContractDigest != ([32]byte{}) && sha256.Sum256(binding.MaterializedContractJSON) == binding.MaterializedContractDigest && binding.ActiveContractDigest == ([32]byte{}) && len(binding.ActiveContractJSON) == 0 && binding.Status == storecontract.SpecCodingMaterialized && !binding.MaterializedAt.IsZero() && binding.RegisteredAt.IsZero()
}

func validRegisteredBinding(binding storecontract.SpecCodingBinding) bool {
	return binding.TaskID.Valid() && binding.SnapshotID.Valid() && binding.SnapshotDigest != ([32]byte{}) && binding.MaterializedContractDigest != ([32]byte{}) && sha256.Sum256(binding.MaterializedContractJSON) == binding.MaterializedContractDigest && binding.ActiveContractDigest != ([32]byte{}) && sha256.Sum256(binding.ActiveContractJSON) == binding.ActiveContractDigest && binding.Status == storecontract.SpecCodingRegistered && !binding.MaterializedAt.IsZero() && !binding.RegisteredAt.Before(binding.MaterializedAt)
}

func copyDigest(target *[32]byte, source []byte) bool {
	if len(source) != len(target) {
		return false
	}
	copy(target[:], source)
	return true
}
