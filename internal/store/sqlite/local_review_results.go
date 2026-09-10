package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func (tx *writeTx) InsertLocalReviewResult(ctx context.Context, result domain.LocalReviewResult) error {
	if err := tx.requireActiveRoomForRun(ctx, result.RunID()); err != nil {
		return preserveWriteConflict(err, storecontract.ErrVerificationConflict)
	}
	patchDigest := result.PatchDigest()
	baselineDigest := result.BaselineDigest()
	contextDigest := result.ContextSnapshotDigest()
	contractDigest := result.AcceptanceContractDigest()
	_, err := tx.tx.ExecContext(ctx, `INSERT INTO local_review_results(
id,run_id,agent_attempt_id,agent_report_id,patch_artifact_id,outcome,
patch_digest,baseline_digest,context_snapshot_digest,acceptance_contract_digest,created_at
) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, result.ID().String(), result.RunID().String(), result.AgentAttemptID().String(),
		result.AgentReportID().String(), result.PatchArtifactID().String(), string(result.Outcome()), patchDigest[:],
		baselineDigest[:], contextDigest[:], contractDigest[:], timeText(result.CreatedAt()))
	if err == nil {
		return nil
	}
	text := strings.ToLower(err.Error())
	if strings.Contains(text, "constraint") || strings.Contains(text, "local review") || strings.Contains(text, "unique") {
		return fmt.Errorf("%w: %v", storecontract.ErrVerificationConflict, err)
	}
	return err
}

func (reader *reader) GetLocalReviewResult(ctx context.Context, id domain.ResultID) (domain.LocalReviewResult, error) {
	return reader.getLocalReviewResult(ctx, "result.id=?", id.String())
}

func (reader *reader) GetLocalReviewResultForAgentAttempt(ctx context.Context, attemptID domain.AttemptID) (domain.LocalReviewResult, error) {
	return reader.getLocalReviewResult(ctx, "result.agent_attempt_id=?", attemptID.String())
}

func (reader *reader) getLocalReviewResult(ctx context.Context, where string, value any) (domain.LocalReviewResult, error) {
	var idText, runText, attemptText, reportText, artifactText, outcomeText, createdText string
	var patchDigest, baselineDigest, contextDigest, contractDigest []byte
	err := reader.q.QueryRowContext(ctx, `SELECT result.id,result.run_id,result.agent_attempt_id,result.agent_report_id,
result.patch_artifact_id,result.outcome,result.patch_digest,result.baseline_digest,result.context_snapshot_digest,
result.acceptance_contract_digest,result.created_at FROM local_review_results result WHERE `+where, value).Scan(
		&idText, &runText, &attemptText, &reportText, &artifactText, &outcomeText, &patchDigest, &baselineDigest, &contextDigest, &contractDigest, &createdText)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.LocalReviewResult{}, storecontract.ErrNotFound
	}
	if err != nil {
		return domain.LocalReviewResult{}, err
	}
	id, err := domain.ParseResultID(idText)
	if err != nil {
		return domain.LocalReviewResult{}, err
	}
	runID, err := domain.ParseRunID(runText)
	if err != nil {
		return domain.LocalReviewResult{}, err
	}
	attemptID, err := domain.ParseAttemptID(attemptText)
	if err != nil {
		return domain.LocalReviewResult{}, err
	}
	reportID, err := domain.ParseAgentReportID(reportText)
	if err != nil {
		return domain.LocalReviewResult{}, err
	}
	artifactID, err := domain.ParseArtifactID(artifactText)
	if err != nil {
		return domain.LocalReviewResult{}, err
	}
	var patch, baseline, snapshot, contract [32]byte
	if !copyDigest(&patch, patchDigest) || !copyDigest(&baseline, baselineDigest) || !copyDigest(&snapshot, contextDigest) || !copyDigest(&contract, contractDigest) {
		return domain.LocalReviewResult{}, storecontract.ErrVerificationConflict
	}
	createdAt, err := parseTime(createdText)
	if err != nil {
		return domain.LocalReviewResult{}, err
	}
	return domain.NewLocalReviewResult(domain.LocalReviewResultParams{
		ID: id, RunID: runID, AgentAttemptID: attemptID, AgentReportID: reportID, PatchArtifactID: artifactID,
		PatchDigest: patch, BaselineDigest: baseline, ContextSnapshotDigest: snapshot,
		AcceptanceContractDigest: contract, Outcome: domain.ResultOutcome(outcomeText), CreatedAt: createdAt,
	})
}
