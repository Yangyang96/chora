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

func verifiedReviewWriteError(err error) error {
	if err == nil {
		return nil
	}
	text := strings.ToLower(err.Error())
	if strings.Contains(text, "constraint failed") || strings.Contains(text, "verified review") || strings.Contains(text, "unique constraint") {
		return fmt.Errorf("%w: %v", storecontract.ErrVerificationConflict, err)
	}
	return err
}

func (tx *writeTx) InsertVerifiedReview(ctx context.Context, decision domain.VerifiedReviewDecision) error {
	if err := tx.requireActiveRoomForRun(ctx, decision.RunID()); err != nil {
		return preserveWriteConflict(err, storecontract.ErrVerificationConflict)
	}
	binding := decision.Binding()
	if binding.NormalizedEvidenceKind() == domain.ReviewEvidenceAgentReport {
		_, err := tx.tx.ExecContext(ctx, `INSERT INTO local_review_decisions(
id,run_id,result_id,agent_attempt_id,agent_report_id,patch_artifact_id,
expected_run_version,kind,reason,rejection_class,actor_id,session_id,
patch_digest,baseline_digest,declared_files_digest,decided_at
) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			decision.ID().String(), decision.RunID().String(), binding.ResultID.String(), binding.AgentAttemptID.String(),
			binding.AgentReportID.String(), binding.PatchArtifactID.String(), decision.ExpectedRunVersion(), string(decision.Kind()),
			decision.Reason(), string(decision.RejectionClass()), decision.ActorID(), decision.SessionID(), binding.PatchDigest[:],
			binding.BaselineDigest[:], binding.DeclaredFilesDigest[:], timeText(decision.DecidedAt()))
		return verifiedReviewWriteError(err)
	}
	_, err := tx.tx.ExecContext(ctx, `INSERT INTO verified_review_decisions(
id,run_id,result_id,agent_attempt_id,verification_attempt_id,patch_artifact_id,
expected_run_version,kind,reason,rejection_class,actor_id,session_id,
patch_digest,baseline_digest,declared_files_digest,decided_at
) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		decision.ID().String(), decision.RunID().String(), binding.ResultID.String(), binding.AgentAttemptID.String(),
		binding.VerificationAttemptID.String(), binding.PatchArtifactID.String(), decision.ExpectedRunVersion(), string(decision.Kind()),
		decision.Reason(), string(decision.RejectionClass()), decision.ActorID(), decision.SessionID(), binding.PatchDigest[:],
		binding.BaselineDigest[:], binding.DeclaredFilesDigest[:], timeText(decision.DecidedAt()))
	return verifiedReviewWriteError(err)
}

func (reader *reader) GetVerifiedReview(ctx context.Context, id domain.ReviewDecisionID) (domain.VerifiedReviewDecision, error) {
	decision, err := reader.getVerifiedReview(ctx, "decision.id=?", id.String())
	if !errors.Is(err, storecontract.ErrNotFound) {
		return decision, err
	}
	return reader.getLocalReviewDecision(ctx, "decision.id=?", id.String())
}

func (reader *reader) GetVerifiedReviewForResult(ctx context.Context, resultID domain.ResultID) (domain.VerifiedReviewDecision, error) {
	decision, err := reader.getVerifiedReview(ctx, "decision.result_id=?", resultID.String())
	if !errors.Is(err, storecontract.ErrNotFound) {
		return decision, err
	}
	return reader.getLocalReviewDecision(ctx, "decision.result_id=?", resultID.String())
}

func (reader *reader) getVerifiedReview(ctx context.Context, where string, value any) (domain.VerifiedReviewDecision, error) {
	var idText, runText, resultText, agentAttemptText, verificationAttemptText, patchArtifactText string
	var kindText, reason, rejectionClass, actorID, sessionID, decidedText string
	var expected uint64
	var patchDigest, baselineDigest, declaredFilesDigest []byte
	err := reader.q.QueryRowContext(ctx, `SELECT
decision.id,decision.run_id,decision.result_id,decision.agent_attempt_id,decision.verification_attempt_id,decision.patch_artifact_id,
decision.expected_run_version,decision.kind,decision.reason,decision.rejection_class,decision.actor_id,decision.session_id,
decision.patch_digest,decision.baseline_digest,decision.declared_files_digest,decision.decided_at
FROM verified_review_decisions decision WHERE `+where, value).Scan(
		&idText, &runText, &resultText, &agentAttemptText, &verificationAttemptText, &patchArtifactText,
		&expected, &kindText, &reason, &rejectionClass, &actorID, &sessionID,
		&patchDigest, &baselineDigest, &declaredFilesDigest, &decidedText)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.VerifiedReviewDecision{}, storecontract.ErrNotFound
	}
	if err != nil {
		return domain.VerifiedReviewDecision{}, err
	}
	return restoreVerifiedReview(idText, runText, resultText, agentAttemptText, verificationAttemptText, patchArtifactText,
		expected, kindText, reason, rejectionClass, actorID, sessionID, patchDigest, baselineDigest, declaredFilesDigest, decidedText)
}

func (reader *reader) getLocalReviewDecision(ctx context.Context, where string, value any) (domain.VerifiedReviewDecision, error) {
	var idText, runText, resultText, agentAttemptText, agentReportText, patchArtifactText string
	var kindText, reason, rejectionClass, actorID, sessionID, decidedText string
	var expected uint64
	var patchDigest, baselineDigest, declaredFilesDigest []byte
	err := reader.q.QueryRowContext(ctx, `SELECT
decision.id,decision.run_id,decision.result_id,decision.agent_attempt_id,decision.agent_report_id,decision.patch_artifact_id,
decision.expected_run_version,decision.kind,decision.reason,decision.rejection_class,decision.actor_id,decision.session_id,
decision.patch_digest,decision.baseline_digest,decision.declared_files_digest,decision.decided_at
FROM local_review_decisions decision WHERE `+where, value).Scan(
		&idText, &runText, &resultText, &agentAttemptText, &agentReportText, &patchArtifactText,
		&expected, &kindText, &reason, &rejectionClass, &actorID, &sessionID,
		&patchDigest, &baselineDigest, &declaredFilesDigest, &decidedText)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.VerifiedReviewDecision{}, storecontract.ErrNotFound
	}
	if err != nil {
		return domain.VerifiedReviewDecision{}, err
	}
	return restoreLocalReviewDecision(idText, runText, resultText, agentAttemptText, agentReportText, patchArtifactText,
		expected, kindText, reason, rejectionClass, actorID, sessionID, patchDigest, baselineDigest, declaredFilesDigest, decidedText)
}

func (reader *reader) ListVerifiedReviewsForRun(ctx context.Context, runID domain.RunID) ([]domain.VerifiedReviewDecision, error) {
	rows, err := reader.q.QueryContext(ctx, `SELECT id FROM (
SELECT id,decided_at FROM verified_review_decisions WHERE run_id=?
UNION ALL SELECT id,decided_at FROM local_review_decisions WHERE run_id=?
) ORDER BY decided_at,id`, runID.String(), runID.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []domain.ReviewDecisionID
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			return nil, err
		}
		id, err := domain.ParseReviewDecisionID(text)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result := make([]domain.VerifiedReviewDecision, 0, len(ids))
	for _, id := range ids {
		decision, err := reader.GetVerifiedReview(ctx, id)
		if err != nil || decision.RunID() != runID {
			if err == nil {
				err = storecontract.ErrVerificationConflict
			}
			return nil, err
		}
		result = append(result, decision)
	}
	return result, nil
}

func restoreLocalReviewDecision(idText, runText, resultText, agentAttemptText, agentReportText, patchArtifactText string,
	expected uint64, kindText, reason, rejectionClass, actorID, sessionID string,
	patchBytes, baselineBytes, declaredBytes []byte, decidedText string,
) (domain.VerifiedReviewDecision, error) {
	id, err := domain.ParseReviewDecisionID(idText)
	if err != nil {
		return domain.VerifiedReviewDecision{}, err
	}
	runID, err := domain.ParseRunID(runText)
	if err != nil {
		return domain.VerifiedReviewDecision{}, err
	}
	resultID, err := domain.ParseResultID(resultText)
	if err != nil {
		return domain.VerifiedReviewDecision{}, err
	}
	agentAttemptID, err := domain.ParseAttemptID(agentAttemptText)
	if err != nil {
		return domain.VerifiedReviewDecision{}, err
	}
	agentReportID, err := domain.ParseAgentReportID(agentReportText)
	if err != nil {
		return domain.VerifiedReviewDecision{}, err
	}
	patchArtifactID, err := domain.ParseArtifactID(patchArtifactText)
	if err != nil {
		return domain.VerifiedReviewDecision{}, err
	}
	var patchDigest, baselineDigest, declaredFilesDigest [32]byte
	if !copyDigest(&patchDigest, patchBytes) || !copyDigest(&baselineDigest, baselineBytes) || !copyDigest(&declaredFilesDigest, declaredBytes) {
		return domain.VerifiedReviewDecision{}, storecontract.ErrVerificationConflict
	}
	decidedAt, err := parseTime(decidedText)
	if err != nil {
		return domain.VerifiedReviewDecision{}, err
	}
	return domain.RestoreVerifiedReviewDecision(domain.VerifiedReviewDecisionParams{
		ID: id, RunID: runID, ExpectedRunVersion: expected, Kind: domain.ReviewDecisionKind(kindText), Reason: reason,
		RejectionClass: domain.ReviewRejectionClass(rejectionClass), ActorID: actorID, SessionID: sessionID,
		Binding: domain.VerifiedReviewBinding{EvidenceKind: domain.ReviewEvidenceAgentReport, ResultID: resultID,
			AgentAttemptID: agentAttemptID, AgentReportID: agentReportID, PatchArtifactID: patchArtifactID,
			PatchDigest: patchDigest, BaselineDigest: baselineDigest, DeclaredFilesDigest: declaredFilesDigest},
		DecidedAt: decidedAt,
	})
}

func restoreVerifiedReview(idText, runText, resultText, agentAttemptText, verificationAttemptText, patchArtifactText string,
	expected uint64, kindText, reason, rejectionClass, actorID, sessionID string,
	patchBytes, baselineBytes, declaredBytes []byte, decidedText string,
) (domain.VerifiedReviewDecision, error) {
	id, err := domain.ParseReviewDecisionID(idText)
	if err != nil {
		return domain.VerifiedReviewDecision{}, err
	}
	runID, err := domain.ParseRunID(runText)
	if err != nil {
		return domain.VerifiedReviewDecision{}, err
	}
	resultID, err := domain.ParseResultID(resultText)
	if err != nil {
		return domain.VerifiedReviewDecision{}, err
	}
	agentAttemptID, err := domain.ParseAttemptID(agentAttemptText)
	if err != nil {
		return domain.VerifiedReviewDecision{}, err
	}
	verificationAttemptID, err := domain.ParseVerificationAttemptID(verificationAttemptText)
	if err != nil {
		return domain.VerifiedReviewDecision{}, err
	}
	patchArtifactID, err := domain.ParseArtifactID(patchArtifactText)
	if err != nil {
		return domain.VerifiedReviewDecision{}, err
	}
	var patchDigest, baselineDigest, declaredFilesDigest [32]byte
	if !copyDigest(&patchDigest, patchBytes) || !copyDigest(&baselineDigest, baselineBytes) || !copyDigest(&declaredFilesDigest, declaredBytes) {
		return domain.VerifiedReviewDecision{}, storecontract.ErrVerificationConflict
	}
	decidedAt, err := parseTime(decidedText)
	if err != nil {
		return domain.VerifiedReviewDecision{}, err
	}
	return domain.RestoreVerifiedReviewDecision(domain.VerifiedReviewDecisionParams{
		ID: id, RunID: runID, ExpectedRunVersion: expected, Kind: domain.ReviewDecisionKind(kindText), Reason: reason,
		RejectionClass: domain.ReviewRejectionClass(rejectionClass), ActorID: actorID, SessionID: sessionID,
		Binding: domain.VerifiedReviewBinding{ResultID: resultID, AgentAttemptID: agentAttemptID, VerificationAttemptID: verificationAttemptID,
			PatchArtifactID: patchArtifactID, PatchDigest: patchDigest, BaselineDigest: baselineDigest, DeclaredFilesDigest: declaredFilesDigest},
		DecidedAt: decidedAt,
	})
}
