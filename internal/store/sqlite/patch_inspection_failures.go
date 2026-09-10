package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func (tx *writeTx) InsertPatchInspectionFailure(ctx context.Context, failure domain.PatchInspectionFailure) error {
	paths, err := json.Marshal(failure.AffectedPaths())
	if err != nil {
		return err
	}
	patchDigest, declaredDigest := failure.PatchDigest(), failure.DeclaredFilesDigest()
	_, err = tx.tx.ExecContext(ctx, `INSERT INTO patch_inspection_failures(
run_id,review_decision_id,patch_digest,declared_files_digest,target_identity,affected_paths_json,state,version,reason,started_at,updated_at
) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, failure.RunID().String(), failure.ReviewDecisionID().String(), patchDigest[:], declaredDigest[:],
		failure.TargetIdentity(), paths, string(failure.State()), failure.Version(), failure.Reason(), timeText(failure.StartedAt()), timeText(failure.UpdatedAt()))
	return patchApplicationWriteError(err)
}

func (tx *writeTx) SavePatchInspectionFailureCAS(ctx context.Context, expected uint64, failure domain.PatchInspectionFailure) error {
	result, err := tx.tx.ExecContext(ctx, `UPDATE patch_inspection_failures SET state=?,version=?,reason=?,updated_at=? WHERE run_id=? AND version=?`,
		string(failure.State()), failure.Version(), failure.Reason(), timeText(failure.UpdatedAt()), failure.RunID().String(), expected)
	if err != nil {
		return patchApplicationWriteError(err)
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

func (reader *reader) GetPatchInspectionFailure(ctx context.Context, runID domain.RunID) (domain.PatchInspectionFailure, error) {
	var runText, reviewText, targetIdentity, pathsJSON, stateText, reason, startedText, updatedText string
	var patchBytes, declaredBytes []byte
	var version uint64
	err := reader.q.QueryRowContext(ctx, `SELECT run_id,review_decision_id,patch_digest,declared_files_digest,target_identity,affected_paths_json,state,version,reason,started_at,updated_at FROM patch_inspection_failures WHERE run_id=?`, runID.String()).Scan(
		&runText, &reviewText, &patchBytes, &declaredBytes, &targetIdentity, &pathsJSON, &stateText, &version, &reason, &startedText, &updatedText)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.PatchInspectionFailure{}, storecontract.ErrNotFound
	}
	if err != nil {
		return domain.PatchInspectionFailure{}, err
	}
	var patchDigest, declaredDigest [32]byte
	if !copyDigest(&patchDigest, patchBytes) || !copyDigest(&declaredDigest, declaredBytes) {
		return domain.PatchInspectionFailure{}, storecontract.ErrPatchApplicationConflict
	}
	var paths []string
	if err := json.Unmarshal([]byte(pathsJSON), &paths); err != nil {
		return domain.PatchInspectionFailure{}, storecontract.ErrPatchApplicationConflict
	}
	startedAt, err := parseTime(startedText)
	if err != nil {
		return domain.PatchInspectionFailure{}, err
	}
	updatedAt, err := parseTime(updatedText)
	if err != nil {
		return domain.PatchInspectionFailure{}, err
	}
	return domain.RestorePatchInspectionFailure(domain.PatchInspectionFailureParams{
		RunID: runText, ReviewDecisionID: reviewText, PatchDigest: patchDigest, DeclaredFilesDigest: declaredDigest,
		TargetIdentity: targetIdentity, AffectedPaths: paths, State: domain.PatchApplicationState(stateText), Version: version,
		Reason: reason, StartedAt: startedAt, UpdatedAt: updatedAt,
	})
}
