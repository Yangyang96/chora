package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func patchApplicationWriteError(err error) error {
	if err == nil {
		return nil
	}
	text := strings.ToLower(err.Error())
	if strings.Contains(text, "patch application") || strings.Contains(text, "constraint failed") || strings.Contains(text, "unique constraint") {
		return fmt.Errorf("%w: %v", storecontract.ErrPatchApplicationConflict, err)
	}
	return err
}

func (tx *writeTx) InsertPatchApplication(ctx context.Context, application domain.PatchApplication) error {
	if err := tx.requireOpenResultEntry(ctx, application.RunID(), ""); err != nil {
		return err
	}
	paths, err := json.Marshal(application.AffectedPaths())
	if err != nil {
		return err
	}
	var post any
	if value := application.PostStateDigest(); value != ([32]byte{}) {
		post = value[:]
	}
	patchDigest, declaredDigest, preDigest := application.PatchDigest(), application.DeclaredFilesDigest(), application.PreStateDigest()
	_, err = tx.tx.ExecContext(ctx, `INSERT INTO patch_applications(
run_id,review_decision_id,patch_digest,declared_files_digest,target_identity,base_revision,
affected_paths_json,pre_state_digest,post_state_digest,state,version,reason,started_at,updated_at,applied_at
) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		application.RunID().String(), application.ReviewDecisionID().String(), patchDigest[:], declaredDigest[:],
		application.TargetIdentity(), application.BaseRevision(), paths, preDigest[:], post, string(application.State()),
		application.Version(), application.Reason(), timeText(application.StartedAt()), timeText(application.UpdatedAt()), nullableTime(application.AppliedAt()))
	return patchApplicationWriteError(err)
}

func (tx *writeTx) SavePatchApplicationCAS(ctx context.Context, expected uint64, application domain.PatchApplication) error {
	if err := tx.requireOpenResultEntry(ctx, application.RunID(), ""); err != nil {
		return err
	}
	var post any
	if value := application.PostStateDigest(); value != ([32]byte{}) {
		post = value[:]
	}
	preDigest := application.PreStateDigest()
	result, err := tx.tx.ExecContext(ctx, `UPDATE patch_applications SET
target_identity=?,base_revision=?,pre_state_digest=?,post_state_digest=?,state=?,version=?,reason=?,started_at=?,updated_at=?,applied_at=?
WHERE run_id=? AND version=?`, application.TargetIdentity(), application.BaseRevision(), preDigest[:], post,
		string(application.State()), application.Version(), application.Reason(), timeText(application.StartedAt()), timeText(application.UpdatedAt()), nullableTime(application.AppliedAt()),
		application.RunID().String(), expected)
	if err != nil {
		return patchApplicationWriteError(err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return storecontract.ErrVersionConflict
	}
	return nil
}

func (reader *reader) GetPatchApplication(ctx context.Context, runID domain.RunID) (domain.PatchApplication, error) {
	return reader.getPatchApplication(ctx, "run_id=?", runID.String())
}

func (reader *reader) getPatchApplication(ctx context.Context, where string, argument any) (domain.PatchApplication, error) {
	var runText, reviewText, targetIdentity, baseRevision, stateText, reason, startedText, updatedText string
	var patchBytes, declaredBytes, pathsJSON, preBytes []byte
	var postBytes []byte
	var version uint64
	var appliedText sql.NullString
	err := reader.q.QueryRowContext(ctx, `SELECT run_id,review_decision_id,patch_digest,declared_files_digest,target_identity,base_revision,
affected_paths_json,pre_state_digest,post_state_digest,state,version,reason,started_at,updated_at,applied_at FROM patch_applications WHERE `+where, argument).Scan(
		&runText, &reviewText, &patchBytes, &declaredBytes, &targetIdentity, &baseRevision, &pathsJSON, &preBytes, &postBytes,
		&stateText, &version, &reason, &startedText, &updatedText, &appliedText)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.PatchApplication{}, storecontract.ErrNotFound
	}
	if err != nil {
		return domain.PatchApplication{}, err
	}
	var patchDigest, declaredDigest, preDigest, postDigest [32]byte
	if !copyDigest(&patchDigest, patchBytes) || !copyDigest(&declaredDigest, declaredBytes) || !copyDigest(&preDigest, preBytes) || (len(postBytes) != 0 && !copyDigest(&postDigest, postBytes)) {
		return domain.PatchApplication{}, storecontract.ErrPatchApplicationConflict
	}
	var paths []string
	if err := json.Unmarshal(pathsJSON, &paths); err != nil {
		return domain.PatchApplication{}, storecontract.ErrPatchApplicationConflict
	}
	startedAt, err := parseTime(startedText)
	if err != nil {
		return domain.PatchApplication{}, err
	}
	updatedAt, err := parseTime(updatedText)
	if err != nil {
		return domain.PatchApplication{}, err
	}
	var appliedAt time.Time
	if appliedText.Valid {
		appliedAt, err = parseTime(appliedText.String)
		if err != nil {
			return domain.PatchApplication{}, err
		}
	}
	return domain.RestorePatchApplication(domain.PatchApplicationParams{
		RunID: runText, ReviewDecisionID: reviewText, PatchDigest: patchDigest, DeclaredFilesDigest: declaredDigest,
		TargetIdentity: targetIdentity, BaseRevision: baseRevision, AffectedPaths: paths, PreStateDigest: preDigest,
		PostStateDigest: postDigest, State: domain.PatchApplicationState(stateText), Version: version, Reason: reason,
		StartedAt: startedAt, UpdatedAt: updatedAt, AppliedAt: appliedAt,
	})
}

func (reader *reader) ListApplyingPatchApplications(ctx context.Context) ([]domain.PatchApplication, error) {
	rows, err := reader.q.QueryContext(ctx, `SELECT run_id FROM patch_applications WHERE state='applying' ORDER BY updated_at,run_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []domain.RunID
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			return nil, err
		}
		id, err := domain.ParseRunID(text)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result := make([]domain.PatchApplication, 0, len(ids))
	for _, id := range ids {
		application, err := reader.GetPatchApplication(ctx, id)
		if err != nil {
			return nil, err
		}
		result = append(result, application)
	}
	return result, nil
}

func (tx *writeTx) CloseTaskForAppliedRun(ctx context.Context, taskID domain.TaskID, runID domain.RunID, applicationVersion uint64) error {
	if err := tx.requireActiveRoomForTask(ctx, taskID); err != nil {
		return preserveWriteConflict(err, storecontract.ErrVersionConflict)
	}
	result, err := tx.tx.ExecContext(ctx, `UPDATE tasks SET state='closed',updated_at=? WHERE id=? AND state='open' AND EXISTS(
SELECT 1 FROM runs run JOIN patch_applications application ON application.run_id=run.id
WHERE run.id=? AND run.task_id=? AND run.state='accepted' AND application.state='applied' AND application.version=?)`,
		timeText(time.Now()), taskID.String(), runID.String(), taskID.String(), applicationVersion)
	if err != nil {
		return mapWriteError(err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return storecontract.ErrVersionConflict
	}
	return nil
}
