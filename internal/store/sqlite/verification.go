package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

type agentClaimJSON struct {
	CriterionID string `json:"criterion_id"`
	Status      string `json:"status"`
	Evidence    string `json:"evidence"`
}

func verificationWriteError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, storecontract.ErrNotFound) {
		return fmt.Errorf("%w: %v", storecontract.ErrVerificationConflict, err)
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "constraint failed") || strings.Contains(message, "is immutable") || strings.Contains(message, "invalid verification") || strings.Contains(message, "agent report") {
		return fmt.Errorf("%w: %v", storecontract.ErrVerificationConflict, err)
	}
	return err
}

func marshalAgentClaims(claims []domain.AgentClaimedCheck) ([]byte, error) {
	encoded := make([]agentClaimJSON, len(claims))
	for i, claim := range claims {
		encoded[i] = agentClaimJSON{CriterionID: claim.CriterionID.String(), Status: claim.Status, Evidence: claim.Evidence}
	}
	return json.Marshal(encoded)
}

func decodeStrictJSON[T any](raw []byte, target *T) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("trailing JSON value")
		}
		return err
	}
	return nil
}

func (tx *writeTx) InsertAgentReport(ctx context.Context, report domain.AgentReport) error {
	if err := tx.requireActiveRoomForRun(ctx, report.RunID()); err != nil {
		return verificationWriteError(err)
	}
	claims, err := marshalAgentClaims(report.ClaimedChecks())
	if err != nil {
		return storecontract.ErrVerificationConflict
	}
	_, err = tx.tx.ExecContext(ctx, `INSERT INTO agent_reports(id,run_id,attempt_id,summary,final_text,claimed_checks_json,completed_at) VALUES(?,?,?,?,?,?,?)`,
		report.ID().String(), report.RunID().String(), report.AttemptID().String(), report.Summary(), report.FinalText(), claims, timeText(report.CompletedAt()))
	return verificationWriteError(err)
}

func (reader *reader) GetAgentReportForRun(ctx context.Context, runID domain.RunID) (domain.AgentReport, error) {
	return reader.getAgentReport(ctx, `attempt_id=(SELECT id FROM attempts WHERE run_id=? ORDER BY sequence DESC LIMIT 1)`, runID.String())
}

func (reader *reader) GetAgentReportForAttempt(ctx context.Context, attemptID domain.AttemptID) (domain.AgentReport, error) {
	return reader.getAgentReport(ctx, `attempt_id=?`, attemptID.String())
}

func (reader *reader) getAgentReport(ctx context.Context, where string, value any) (domain.AgentReport, error) {
	var idText, runText, attemptText, summary, finalText, completedText string
	var claimsRaw []byte
	err := reader.q.QueryRowContext(ctx, `SELECT id,run_id,attempt_id,summary,final_text,claimed_checks_json,completed_at FROM agent_reports WHERE `+where, value).Scan(
		&idText, &runText, &attemptText, &summary, &finalText, &claimsRaw, &completedText)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AgentReport{}, storecontract.ErrNotFound
	}
	if err != nil {
		return domain.AgentReport{}, err
	}
	id, err := domain.ParseAgentReportID(idText)
	if err != nil {
		return domain.AgentReport{}, err
	}
	storedRunID, err := domain.ParseRunID(runText)
	if err != nil {
		return domain.AgentReport{}, err
	}
	attemptID, err := domain.ParseAttemptID(attemptText)
	if err != nil {
		return domain.AgentReport{}, err
	}
	completedAt, err := parseTime(completedText)
	if err != nil {
		return domain.AgentReport{}, err
	}
	var encoded []agentClaimJSON
	if err := decodeStrictJSON(claimsRaw, &encoded); err != nil || encoded == nil {
		return domain.AgentReport{}, storecontract.ErrVerificationConflict
	}
	claims := make([]domain.AgentClaimedCheck, len(encoded))
	for i, claim := range encoded {
		criterionID, err := domain.ParseCriterionID(claim.CriterionID)
		if err != nil {
			return domain.AgentReport{}, err
		}
		claims[i] = domain.AgentClaimedCheck{CriterionID: criterionID, Status: claim.Status, Evidence: claim.Evidence}
	}
	return domain.NewAgentReport(domain.AgentReportParams{ID: id, RunID: storedRunID, AttemptID: attemptID, Summary: summary, FinalText: finalText, ClaimedChecks: claims, CompletedAt: completedAt})
}

func (reader *reader) ListAgentReportsForRun(ctx context.Context, runID domain.RunID) ([]domain.AgentReport, error) {
	rows, err := reader.q.QueryContext(ctx, `SELECT report.attempt_id FROM agent_reports report JOIN attempts attempt ON attempt.id=report.attempt_id WHERE report.run_id=? ORDER BY attempt.sequence`, runID.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []domain.AttemptID
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			return nil, err
		}
		id, err := domain.ParseAttemptID(text)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	reports := make([]domain.AgentReport, 0, len(ids))
	for _, id := range ids {
		report, err := reader.GetAgentReportForAttempt(ctx, id)
		if err != nil || report.RunID() != runID {
			if err == nil {
				err = storecontract.ErrVerificationConflict
			}
			return nil, err
		}
		reports = append(reports, report)
	}
	return reports, nil
}

func bindingsArgs(bindings domain.VerificationBindings) []any {
	return []any{bindings.BaselineDigest[:], bindings.PatchDigest[:], bindings.ContextSnapshotDigest[:], bindings.AcceptanceContractDigest[:], bindings.VerifierPolicyVersion, bindings.VerifierPolicyDigest[:]}
}

func scanBindings(baseline, patch, snapshot, contract []byte, policyVersion string, policyDigest []byte) (domain.VerificationBindings, error) {
	var bindings domain.VerificationBindings
	if !copyDigest(&bindings.BaselineDigest, baseline) || !copyDigest(&bindings.PatchDigest, patch) || !copyDigest(&bindings.ContextSnapshotDigest, snapshot) || !copyDigest(&bindings.AcceptanceContractDigest, contract) || !copyDigest(&bindings.VerifierPolicyDigest, policyDigest) {
		return domain.VerificationBindings{}, storecontract.ErrVerificationConflict
	}
	bindings.VerifierPolicyVersion = policyVersion
	return bindings, nil
}

func (tx *writeTx) InsertVerificationRun(ctx context.Context, run domain.VerificationRun) error {
	if err := tx.requireActiveRoomForRun(ctx, run.RunID()); err != nil {
		return verificationWriteError(err)
	}
	if run.State() != domain.VerificationRunAwaiting || !run.UpdatedAt().Equal(run.CreatedAt()) {
		return storecontract.ErrVerificationConflict
	}
	b := run.Bindings()
	args := append([]any{run.ID().String(), run.RunID().String(), run.AgentAttemptID().String()}, bindingsArgs(b)...)
	args = append(args, string(run.State()), timeText(run.CreatedAt()), timeText(run.UpdatedAt()))
	_, err := tx.tx.ExecContext(ctx, `INSERT INTO verification_runs(id,run_id,agent_attempt_id,baseline_digest,patch_digest,context_snapshot_digest,acceptance_contract_digest,verifier_policy_version,verifier_policy_digest,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, args...)
	return verificationWriteError(err)
}

func sameVerificationRunIdentity(left, right domain.VerificationRun) bool {
	return left.ID() == right.ID() && left.RunID() == right.RunID() && left.AgentAttemptID() == right.AgentAttemptID() && left.Bindings() == right.Bindings() && left.CreatedAt().Equal(right.CreatedAt())
}

func (tx *writeTx) SaveVerificationRunCAS(ctx context.Context, expected domain.VerificationRunState, next domain.VerificationRun) error {
	if err := tx.requireActiveRoomForVerificationRun(ctx, next.ID()); err != nil {
		return verificationWriteError(err)
	}
	current, err := tx.GetVerificationRun(ctx, next.ID())
	if err != nil || current.State() != expected || !sameVerificationRunIdentity(current, next) || next.UpdatedAt().Before(current.UpdatedAt()) {
		return storecontract.ErrVerificationConflict
	}
	result, err := tx.tx.ExecContext(ctx, `UPDATE verification_runs SET state=?,updated_at=? WHERE id=? AND state=? AND updated_at=?`, string(next.State()), timeText(next.UpdatedAt()), next.ID().String(), string(expected), timeText(current.UpdatedAt()))
	if err != nil {
		return verificationWriteError(err)
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return storecontract.ErrVerificationConflict
	}
	return nil
}

func (reader *reader) GetVerificationRun(ctx context.Context, id domain.VerificationRunID) (domain.VerificationRun, error) {
	return reader.getVerificationRun(ctx, `WHERE id=?`, id.String(), &id)
}

func (reader *reader) GetVerificationRunForRun(ctx context.Context, runID domain.RunID) (domain.VerificationRun, error) {
	return reader.getVerificationRun(ctx, `WHERE agent_attempt_id=(SELECT id FROM attempts WHERE run_id=? ORDER BY sequence DESC LIMIT 1)`, runID.String(), nil)
}

func (reader *reader) GetVerificationRunForAgentAttempt(ctx context.Context, attemptID domain.AttemptID) (domain.VerificationRun, error) {
	return reader.getVerificationRun(ctx, `WHERE agent_attempt_id=?`, attemptID.String(), nil)
}

func (reader *reader) ListVerificationRunsForRun(ctx context.Context, runID domain.RunID) ([]domain.VerificationRun, error) {
	rows, err := reader.q.QueryContext(ctx, `SELECT verification.id FROM verification_runs verification JOIN attempts attempt ON attempt.id=verification.agent_attempt_id WHERE verification.run_id=? ORDER BY attempt.sequence`, runID.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []domain.VerificationRunID
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			return nil, err
		}
		id, err := domain.ParseVerificationRunID(text)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	runs := make([]domain.VerificationRun, 0, len(ids))
	for _, id := range ids {
		run, err := reader.GetVerificationRun(ctx, id)
		if err != nil || run.RunID() != runID {
			if err == nil {
				err = storecontract.ErrVerificationConflict
			}
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, nil
}

func (reader *reader) getVerificationRun(ctx context.Context, where string, arg any, expectedID *domain.VerificationRunID) (domain.VerificationRun, error) {
	var idText, runText, agentAttemptText, policyVersion, stateText, createdText, updatedText string
	var baseline, patch, snapshot, contract, policyDigest []byte
	err := reader.q.QueryRowContext(ctx, `SELECT id,run_id,agent_attempt_id,baseline_digest,patch_digest,context_snapshot_digest,acceptance_contract_digest,verifier_policy_version,verifier_policy_digest,state,created_at,updated_at FROM verification_runs `+where, arg).Scan(
		&idText, &runText, &agentAttemptText, &baseline, &patch, &snapshot, &contract, &policyVersion, &policyDigest, &stateText, &createdText, &updatedText)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.VerificationRun{}, storecontract.ErrNotFound
	}
	if err != nil {
		return domain.VerificationRun{}, err
	}
	id, err := domain.ParseVerificationRunID(idText)
	if err != nil || expectedID != nil && id != *expectedID {
		return domain.VerificationRun{}, storecontract.ErrVerificationConflict
	}
	runID, err := domain.ParseRunID(runText)
	if err != nil {
		return domain.VerificationRun{}, err
	}
	agentAttemptID, err := domain.ParseAttemptID(agentAttemptText)
	if err != nil {
		return domain.VerificationRun{}, err
	}
	bindings, err := scanBindings(baseline, patch, snapshot, contract, policyVersion, policyDigest)
	if err != nil {
		return domain.VerificationRun{}, err
	}
	createdAt, err := parseTime(createdText)
	if err != nil {
		return domain.VerificationRun{}, err
	}
	updatedAt, err := parseTime(updatedText)
	if err != nil {
		return domain.VerificationRun{}, err
	}
	return domain.RestoreVerificationRun(domain.VerificationRunRecord{ID: id, RunID: runID, AgentAttemptID: agentAttemptID, Bindings: bindings, State: domain.VerificationRunState(stateText), CreatedAt: createdAt, UpdatedAt: updatedAt})
}

func (tx *writeTx) InsertVerificationAttempt(ctx context.Context, attempt domain.VerificationAttempt, updatedAt time.Time) error {
	if err := tx.requireActiveRoomForVerificationRun(ctx, attempt.VerificationRunID()); err != nil {
		return verificationWriteError(err)
	}
	if attempt.State() != domain.VerificationAttemptPending || updatedAt.Before(attempt.CreatedAt()) {
		return storecontract.ErrVerificationConflict
	}
	var predecessor any
	if id, ok := attempt.Predecessor(); ok {
		predecessor = id.String()
	}
	b := attempt.Bindings()
	args := []any{attempt.ID().String(), attempt.VerificationRunID().String(), attempt.Sequence(), predecessor}
	args = append(args, bindingsArgs(b)...)
	args = append(args, string(attempt.State()), 0, 0, "", "", timeText(attempt.CreatedAt()), timeText(updatedAt))
	_, err := tx.tx.ExecContext(ctx, `INSERT INTO verification_attempts(id,verification_run_id,sequence,predecessor_id,baseline_digest,patch_digest,context_snapshot_digest,acceptance_contract_digest,verifier_policy_version,verifier_policy_digest,state,evidence_complete,cleanup_proven,workspace_identity,reason,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, args...)
	return verificationWriteError(err)
}

func sameVerificationAttemptIdentity(left, right domain.VerificationAttempt) bool {
	leftPred, leftHas := left.Predecessor()
	rightPred, rightHas := right.Predecessor()
	return left.ID() == right.ID() && left.VerificationRunID() == right.VerificationRunID() && left.Sequence() == right.Sequence() && leftHas == rightHas && (!leftHas || leftPred == rightPred) && left.Bindings() == right.Bindings() && left.CreatedAt().Equal(right.CreatedAt())
}

func validAttemptProof(record storecontract.VerificationAttemptRecord) bool {
	switch record.Attempt.State() {
	case domain.VerificationAttemptPending, domain.VerificationAttemptRunning:
		return !record.EvidenceComplete && !record.CleanupProven && record.WorkspaceIdentity == ""
	case domain.VerificationAttemptCompleted:
		return record.EvidenceComplete && record.CleanupProven && validWorkspaceIdentity(record.WorkspaceIdentity)
	case domain.VerificationAttemptCancelled:
		return !record.EvidenceComplete && record.CleanupProven && validWorkspaceIdentity(record.WorkspaceIdentity)
	case domain.VerificationAttemptRecoveryRequired:
		return !record.EvidenceComplete && !record.CleanupProven && (record.WorkspaceIdentity == "" || validWorkspaceIdentity(record.WorkspaceIdentity))
	default:
		return false
	}
}

func validWorkspaceIdentity(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, character := range strings.TrimPrefix(value, "sha256:") {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func (tx *writeTx) SaveVerificationAttemptCAS(ctx context.Context, expected domain.VerificationAttemptState, next storecontract.VerificationAttemptRecord) error {
	if err := tx.requireActiveRoomForVerificationAttempt(ctx, next.Attempt.ID()); err != nil {
		return verificationWriteError(err)
	}
	current, err := tx.GetVerificationAttempt(ctx, next.Attempt.ID())
	if err != nil || current.Attempt.State() != expected || !sameVerificationAttemptIdentity(current.Attempt, next.Attempt) || next.UpdatedAt.Before(current.UpdatedAt) || !validAttemptProof(next) {
		return storecontract.ErrVerificationConflict
	}
	result, err := tx.tx.ExecContext(ctx, `UPDATE verification_attempts SET state=?,evidence_complete=?,cleanup_proven=?,workspace_identity=?,reason=?,updated_at=? WHERE id=? AND state=? AND updated_at=?`,
		string(next.Attempt.State()), next.EvidenceComplete, next.CleanupProven, next.WorkspaceIdentity, next.Reason, timeText(next.UpdatedAt), next.Attempt.ID().String(), string(expected), timeText(current.UpdatedAt))
	if err != nil {
		return verificationWriteError(err)
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return storecontract.ErrVerificationConflict
	}
	return nil
}

func (reader *reader) GetVerificationAttempt(ctx context.Context, id domain.VerificationAttemptID) (storecontract.VerificationAttemptRecord, error) {
	return reader.getVerificationAttempt(ctx, `WHERE id=?`, id.String(), &id)
}

func (reader *reader) GetCurrentVerificationAttempt(ctx context.Context, runID domain.VerificationRunID) (storecontract.VerificationAttemptRecord, error) {
	return reader.getVerificationAttempt(ctx, `WHERE verification_run_id=? ORDER BY sequence DESC LIMIT 1`, runID.String(), nil)
}

func (reader *reader) VerificationCancelRequested(ctx context.Context, attemptID domain.VerificationAttemptID) (bool, error) {
	var exists bool
	if err := reader.q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM verification_cancel_requests WHERE verification_attempt_id=?)`, attemptID.String()).Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}

func (tx *writeTx) InsertVerificationCancelRequest(ctx context.Context, attemptID domain.VerificationAttemptID, requestedAt time.Time) error {
	if err := tx.requireActiveRoomForVerificationAttempt(ctx, attemptID); err != nil {
		return verificationWriteError(err)
	}
	if !attemptID.Valid() || requestedAt.IsZero() {
		return storecontract.ErrVerificationConflict
	}
	_, err := tx.tx.ExecContext(ctx, `INSERT INTO verification_cancel_requests(verification_attempt_id,requested_at) VALUES(?,?)`, attemptID.String(), timeText(requestedAt))
	return verificationWriteError(err)
}

func (reader *reader) getVerificationAttempt(ctx context.Context, where string, arg any, expectedID *domain.VerificationAttemptID) (storecontract.VerificationAttemptRecord, error) {
	var idText, runText, policyVersion, stateText, workspaceIdentity, reason, createdText, updatedText string
	var predecessor sql.NullString
	var sequence int
	var evidenceComplete, cleanupProven bool
	var baseline, patch, snapshot, contract, policyDigest []byte
	err := reader.q.QueryRowContext(ctx, `SELECT id,verification_run_id,sequence,predecessor_id,baseline_digest,patch_digest,context_snapshot_digest,acceptance_contract_digest,verifier_policy_version,verifier_policy_digest,state,evidence_complete,cleanup_proven,workspace_identity,reason,created_at,updated_at FROM verification_attempts `+where, arg).Scan(
		&idText, &runText, &sequence, &predecessor, &baseline, &patch, &snapshot, &contract, &policyVersion, &policyDigest, &stateText, &evidenceComplete, &cleanupProven, &workspaceIdentity, &reason, &createdText, &updatedText)
	if errors.Is(err, sql.ErrNoRows) {
		return storecontract.VerificationAttemptRecord{}, storecontract.ErrNotFound
	}
	if err != nil {
		return storecontract.VerificationAttemptRecord{}, err
	}
	id, err := domain.ParseVerificationAttemptID(idText)
	if err != nil || expectedID != nil && id != *expectedID {
		return storecontract.VerificationAttemptRecord{}, storecontract.ErrVerificationConflict
	}
	runID, err := domain.ParseVerificationRunID(runText)
	if err != nil {
		return storecontract.VerificationAttemptRecord{}, err
	}
	var predecessorID *domain.VerificationAttemptID
	if predecessor.Valid {
		parsed, err := domain.ParseVerificationAttemptID(predecessor.String)
		if err != nil {
			return storecontract.VerificationAttemptRecord{}, err
		}
		predecessorID = &parsed
	}
	bindings, err := scanBindings(baseline, patch, snapshot, contract, policyVersion, policyDigest)
	if err != nil {
		return storecontract.VerificationAttemptRecord{}, err
	}
	createdAt, err := parseTime(createdText)
	if err != nil {
		return storecontract.VerificationAttemptRecord{}, err
	}
	updatedAt, err := parseTime(updatedText)
	if err != nil {
		return storecontract.VerificationAttemptRecord{}, err
	}
	attempt, err := domain.RestoreVerificationAttempt(domain.VerificationAttemptRecord{ID: id, VerificationRunID: runID, Sequence: sequence, Predecessor: predecessorID, Bindings: bindings, State: domain.VerificationAttemptState(stateText), CreatedAt: createdAt})
	if err != nil {
		return storecontract.VerificationAttemptRecord{}, err
	}
	record := storecontract.VerificationAttemptRecord{Attempt: attempt, EvidenceComplete: evidenceComplete, CleanupProven: cleanupProven, WorkspaceIdentity: workspaceIdentity, Reason: reason, UpdatedAt: updatedAt}
	if updatedAt.Before(createdAt) || !validAttemptProof(record) {
		return storecontract.VerificationAttemptRecord{}, storecontract.ErrVerificationConflict
	}
	return record, nil
}

func (reader *reader) ListVerificationAttempts(ctx context.Context, runID domain.VerificationRunID) ([]storecontract.VerificationAttemptRecord, error) {
	rows, err := reader.q.QueryContext(ctx, `SELECT id FROM verification_attempts WHERE verification_run_id=? ORDER BY sequence`, runID.String())
	if err != nil {
		return nil, err
	}
	var ids []domain.VerificationAttemptID
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			rows.Close()
			return nil, err
		}
		id, err := domain.ParseVerificationAttemptID(text)
		if err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	if closeErr := rows.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	result := make([]storecontract.VerificationAttemptRecord, 0, len(ids))
	for _, id := range ids {
		record, err := reader.GetVerificationAttempt(ctx, id)
		if err != nil || record.Attempt.VerificationRunID() != runID {
			if err == nil {
				err = storecontract.ErrVerificationConflict
			}
			return nil, err
		}
		result = append(result, record)
	}
	return result, nil
}

func (tx *writeTx) InsertVerificationCommandEvidence(ctx context.Context, evidence domain.VerificationCommandEvidence) error {
	if err := tx.requireActiveRoomForVerificationAttempt(ctx, evidence.VerificationAttemptID()); err != nil {
		return verificationWriteError(err)
	}
	criterionIDs := evidence.CriterionIDs()
	criterionText := make([]string, len(criterionIDs))
	for i, id := range criterionIDs {
		criterionText[i] = id.String()
	}
	criteriaJSON, err := json.Marshal(criterionText)
	if err != nil {
		return storecontract.ErrVerificationConflict
	}
	argvJSON, err := json.Marshal(evidence.Argv())
	if err != nil {
		return storecontract.ErrVerificationConflict
	}
	var exitCode any
	if value, ok := evidence.ExitCode(); ok {
		exitCode = value
	}
	_, err = tx.tx.ExecContext(ctx, `INSERT INTO verification_command_evidence(id,verification_attempt_id,command_id,criterion_ids_json,argv_json,started_at,ended_at,classification,exit_code,verifier_identity,workspace_identity) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		evidence.ID().String(), evidence.VerificationAttemptID().String(), evidence.CommandID(), criteriaJSON, argvJSON, timeText(evidence.StartedAt()), timeText(evidence.EndedAt()), string(evidence.Classification()), exitCode, evidence.VerifierIdentity(), evidence.WorkspaceIdentity())
	if err != nil {
		return verificationWriteError(err)
	}
	for _, entry := range []struct {
		name string
		log  domain.VerificationStreamLog
	}{{"stdout", evidence.Stdout()}, {"stderr", evidence.Stderr()}} {
		_, err := tx.tx.ExecContext(ctx, `INSERT INTO verification_stream_logs(verification_command_evidence_id,stream,full_sha256,total_bytes,retained_body,truncated,truncation_boundary,redaction_policy_version) VALUES(?,?,?,?,?,?,?,?)`,
			evidence.ID().String(), entry.name, entry.log.FullSHA256[:], entry.log.TotalBytes, entry.log.RetainedBody, entry.log.Truncated, entry.log.TruncationBoundary, entry.log.RedactionPolicyVersion)
		if err != nil {
			return verificationWriteError(err)
		}
	}
	return nil
}

type commandRow struct {
	id, attemptID, commandID, started, ended, classification, verifier, workspace string
	criteriaJSON, argvJSON                                                        []byte
	exitCode                                                                      sql.NullInt64
}

func (reader *reader) ListVerificationCommandEvidence(ctx context.Context, attemptID domain.VerificationAttemptID) ([]domain.VerificationCommandEvidence, error) {
	rows, err := reader.q.QueryContext(ctx, `SELECT id,verification_attempt_id,command_id,criterion_ids_json,argv_json,started_at,ended_at,classification,exit_code,verifier_identity,workspace_identity FROM verification_command_evidence WHERE verification_attempt_id=? ORDER BY started_at,id`, attemptID.String())
	if err != nil {
		return nil, err
	}
	var records []commandRow
	for rows.Next() {
		var row commandRow
		if err := rows.Scan(&row.id, &row.attemptID, &row.commandID, &row.criteriaJSON, &row.argvJSON, &row.started, &row.ended, &row.classification, &row.exitCode, &row.verifier, &row.workspace); err != nil {
			rows.Close()
			return nil, err
		}
		records = append(records, row)
	}
	err = rows.Err()
	if closeErr := rows.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	result := make([]domain.VerificationCommandEvidence, 0, len(records))
	for _, row := range records {
		evidence, err := reader.restoreVerificationCommandEvidence(ctx, row, attemptID)
		if err != nil {
			return nil, err
		}
		result = append(result, evidence)
	}
	return result, nil
}

func (reader *reader) restoreVerificationCommandEvidence(ctx context.Context, row commandRow, expectedAttempt domain.VerificationAttemptID) (domain.VerificationCommandEvidence, error) {
	id, err := domain.ParseVerificationCommandEvidenceID(row.id)
	if err != nil {
		return domain.VerificationCommandEvidence{}, err
	}
	attemptID, err := domain.ParseVerificationAttemptID(row.attemptID)
	if err != nil || attemptID != expectedAttempt {
		return domain.VerificationCommandEvidence{}, storecontract.ErrVerificationConflict
	}
	var criterionText []string
	if err := decodeStrictJSON(row.criteriaJSON, &criterionText); err != nil || criterionText == nil {
		return domain.VerificationCommandEvidence{}, storecontract.ErrVerificationConflict
	}
	criteria := make([]domain.CriterionID, len(criterionText))
	for i, text := range criterionText {
		criteria[i], err = domain.ParseCriterionID(text)
		if err != nil {
			return domain.VerificationCommandEvidence{}, err
		}
	}
	var argv []string
	if err := decodeStrictJSON(row.argvJSON, &argv); err != nil || argv == nil {
		return domain.VerificationCommandEvidence{}, storecontract.ErrVerificationConflict
	}
	startedAt, err := parseTime(row.started)
	if err != nil {
		return domain.VerificationCommandEvidence{}, err
	}
	endedAt, err := parseTime(row.ended)
	if err != nil {
		return domain.VerificationCommandEvidence{}, err
	}
	logs := make(map[string]domain.VerificationStreamLog, 2)
	logRows, err := reader.q.QueryContext(ctx, `SELECT stream,full_sha256,total_bytes,retained_body,truncated,truncation_boundary,redaction_policy_version FROM verification_stream_logs WHERE verification_command_evidence_id=? ORDER BY stream`, row.id)
	if err != nil {
		return domain.VerificationCommandEvidence{}, err
	}
	for logRows.Next() {
		var stream, body, policy string
		var digest []byte
		var total, boundary int64
		var truncated bool
		if err := logRows.Scan(&stream, &digest, &total, &body, &truncated, &boundary, &policy); err != nil {
			logRows.Close()
			return domain.VerificationCommandEvidence{}, err
		}
		var full [32]byte
		if (stream != "stdout" && stream != "stderr") || !copyDigest(&full, digest) {
			logRows.Close()
			return domain.VerificationCommandEvidence{}, storecontract.ErrVerificationConflict
		}
		logs[stream] = domain.VerificationStreamLog{FullSHA256: full, TotalBytes: total, RetainedBody: body, Truncated: truncated, TruncationBoundary: boundary, RedactionPolicyVersion: policy}
	}
	err = logRows.Err()
	if closeErr := logRows.Close(); err == nil {
		err = closeErr
	}
	if err != nil || len(logs) != 2 {
		if err == nil {
			err = storecontract.ErrVerificationConflict
		}
		return domain.VerificationCommandEvidence{}, err
	}
	var exitCode *int
	if row.exitCode.Valid {
		value := int(row.exitCode.Int64)
		exitCode = &value
	}
	return domain.NewVerificationCommandEvidence(domain.VerificationCommandEvidenceParams{ID: id, VerificationAttemptID: attemptID, CommandID: row.commandID, CriterionIDs: criteria, Argv: argv, StartedAt: startedAt, EndedAt: endedAt, Classification: domain.VerificationCommandClassification(row.classification), ExitCode: exitCode, Stdout: logs["stdout"], Stderr: logs["stderr"], VerifierIdentity: row.verifier, WorkspaceIdentity: row.workspace})
}

func (tx *writeTx) InsertVerificationAcceptanceCheck(ctx context.Context, attemptID domain.VerificationAttemptID, check domain.AcceptanceCheck) error {
	if err := tx.requireActiveRoomForVerificationAttempt(ctx, attemptID); err != nil {
		return verificationWriteError(err)
	}
	_, err := tx.tx.ExecContext(ctx, `INSERT INTO verification_acceptance_checks(id,verification_attempt_id,criterion_id,status,trust) VALUES(?,?,?,?,?)`, check.ID().String(), attemptID.String(), check.CriterionID().String(), string(check.Status()), string(check.Trust()))
	if err != nil {
		return verificationWriteError(err)
	}
	for position, evidenceID := range check.EvidenceIDs() {
		_, err := tx.tx.ExecContext(ctx, `INSERT INTO verification_check_evidence(check_id,verification_attempt_id,position,verification_command_evidence_id) VALUES(?,?,?,?)`, check.ID().String(), attemptID.String(), position, evidenceID.String())
		if err != nil {
			return verificationWriteError(err)
		}
	}
	return nil
}

type checkRow struct{ id, attemptID, criterionID, status, trust string }

func (reader *reader) ListVerificationAcceptanceChecks(ctx context.Context, attemptID domain.VerificationAttemptID) ([]domain.AcceptanceCheck, error) {
	rows, err := reader.q.QueryContext(ctx, `SELECT id,verification_attempt_id,criterion_id,status,trust FROM verification_acceptance_checks WHERE verification_attempt_id=? ORDER BY criterion_id,id`, attemptID.String())
	if err != nil {
		return nil, err
	}
	var records []checkRow
	for rows.Next() {
		var row checkRow
		if err := rows.Scan(&row.id, &row.attemptID, &row.criterionID, &row.status, &row.trust); err != nil {
			rows.Close()
			return nil, err
		}
		records = append(records, row)
	}
	err = rows.Err()
	if closeErr := rows.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	result := make([]domain.AcceptanceCheck, 0, len(records))
	for _, row := range records {
		id, err := domain.ParseCheckID(row.id)
		if err != nil {
			return nil, err
		}
		storedAttemptID, err := domain.ParseVerificationAttemptID(row.attemptID)
		if err != nil || storedAttemptID != attemptID {
			return nil, storecontract.ErrVerificationConflict
		}
		criterionID, err := domain.ParseCriterionID(row.criterionID)
		if err != nil {
			return nil, err
		}
		linkRows, err := reader.q.QueryContext(ctx, `SELECT verification_command_evidence_id FROM verification_check_evidence WHERE check_id=? AND verification_attempt_id=? ORDER BY position`, row.id, row.attemptID)
		if err != nil {
			return nil, err
		}
		var evidenceIDs []domain.VerificationCommandEvidenceID
		for linkRows.Next() {
			var text string
			if err := linkRows.Scan(&text); err != nil {
				linkRows.Close()
				return nil, err
			}
			evidenceID, err := domain.ParseVerificationCommandEvidenceID(text)
			if err != nil {
				linkRows.Close()
				return nil, err
			}
			evidenceIDs = append(evidenceIDs, evidenceID)
		}
		err = linkRows.Err()
		if closeErr := linkRows.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return nil, err
		}
		check, err := domain.NewAcceptanceCheck(id, criterionID, domain.AcceptanceCheckStatus(row.status), domain.VerificationEvidenceTrust(row.trust), evidenceIDs)
		if err != nil {
			return nil, err
		}
		result = append(result, check)
	}
	return result, nil
}

func sameChecks(left, right []domain.AcceptanceCheck) bool {
	if len(left) != len(right) {
		return false
	}
	byID := make(map[domain.CheckID]domain.AcceptanceCheck, len(left))
	for _, check := range left {
		byID[check.ID()] = check
	}
	for _, check := range right {
		other, ok := byID[check.ID()]
		if !ok || other.CriterionID() != check.CriterionID() || other.Status() != check.Status() || other.Trust() != check.Trust() {
			return false
		}
		leftEvidence, rightEvidence := other.EvidenceIDs(), check.EvidenceIDs()
		if len(leftEvidence) != len(rightEvidence) {
			return false
		}
		for i := range leftEvidence {
			if leftEvidence[i] != rightEvidence[i] {
				return false
			}
		}
	}
	return true
}

func (tx *writeTx) InsertVerificationResult(ctx context.Context, result domain.VerificationResult) error {
	if err := tx.requireActiveRoomForVerificationRun(ctx, result.VerificationRunID()); err != nil {
		return verificationWriteError(err)
	}
	checks, err := tx.ListVerificationAcceptanceChecks(ctx, result.VerificationAttemptID())
	if err != nil || !sameChecks(checks, result.Checks()) {
		return storecontract.ErrVerificationConflict
	}
	b := result.Bindings()
	args := []any{result.ID().String(), result.VerificationRunID().String(), result.VerificationAttemptID().String(), string(result.Outcome())}
	args = append(args, bindingsArgs(b)...)
	args = append(args, timeText(result.CreatedAt()))
	_, err = tx.tx.ExecContext(ctx, `INSERT INTO verification_results(id,verification_run_id,verification_attempt_id,outcome,baseline_digest,patch_digest,context_snapshot_digest,acceptance_contract_digest,verifier_policy_version,verifier_policy_digest,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, args...)
	return verificationWriteError(err)
}

func (reader *reader) GetVerificationResultForRun(ctx context.Context, runID domain.RunID) (domain.VerificationResult, error) {
	verificationRun, err := reader.GetVerificationRunForRun(ctx, runID)
	if err != nil {
		return domain.VerificationResult{}, err
	}
	return reader.GetVerificationResultForVerificationRun(ctx, verificationRun.ID())
}

func (reader *reader) GetVerificationResultForVerificationRun(ctx context.Context, expectedVerificationRunID domain.VerificationRunID) (domain.VerificationResult, error) {
	var idText, verificationRunText, attemptText, outcomeText, policyVersion, createdText string
	var baseline, patch, snapshot, contract, policyDigest []byte
	err := reader.q.QueryRowContext(ctx, `SELECT result.id,result.verification_run_id,result.verification_attempt_id,result.outcome,result.baseline_digest,result.patch_digest,result.context_snapshot_digest,result.acceptance_contract_digest,result.verifier_policy_version,result.verifier_policy_digest,result.created_at FROM verification_results result WHERE result.verification_run_id=?`, expectedVerificationRunID.String()).Scan(
		&idText, &verificationRunText, &attemptText, &outcomeText, &baseline, &patch, &snapshot, &contract, &policyVersion, &policyDigest, &createdText)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.VerificationResult{}, storecontract.ErrNotFound
	}
	if err != nil {
		return domain.VerificationResult{}, err
	}
	id, err := domain.ParseResultID(idText)
	if err != nil {
		return domain.VerificationResult{}, err
	}
	verificationRunID, err := domain.ParseVerificationRunID(verificationRunText)
	if err != nil {
		return domain.VerificationResult{}, err
	}
	if verificationRunID != expectedVerificationRunID {
		return domain.VerificationResult{}, storecontract.ErrVerificationConflict
	}
	attemptID, err := domain.ParseVerificationAttemptID(attemptText)
	if err != nil {
		return domain.VerificationResult{}, err
	}
	bindings, err := scanBindings(baseline, patch, snapshot, contract, policyVersion, policyDigest)
	if err != nil {
		return domain.VerificationResult{}, err
	}
	createdAt, err := parseTime(createdText)
	if err != nil {
		return domain.VerificationResult{}, err
	}
	run, err := reader.GetVerificationRun(ctx, verificationRunID)
	if err != nil {
		return domain.VerificationResult{}, storecontract.ErrVerificationConflict
	}
	attemptRecord, err := reader.GetVerificationAttempt(ctx, attemptID)
	if err != nil {
		return domain.VerificationResult{}, err
	}
	checks, err := reader.ListVerificationAcceptanceChecks(ctx, attemptID)
	if err != nil {
		return domain.VerificationResult{}, err
	}
	result, err := domain.NewVerificationResult(id, run, attemptRecord.Attempt, checks, domain.VerificationCompletionProof{EvidenceComplete: attemptRecord.EvidenceComplete, CleanupProven: attemptRecord.CleanupProven}, createdAt)
	if err != nil {
		return domain.VerificationResult{}, err
	}
	if result.VerificationRunID() != verificationRunID || result.VerificationAttemptID() != attemptID || result.Bindings() != bindings || string(result.Outcome()) != outcomeText {
		return domain.VerificationResult{}, storecontract.ErrVerificationConflict
	}
	return result, nil
}

func (reader *reader) ListVerificationStartupCandidates(ctx context.Context) ([]storecontract.VerificationStartupCandidate, error) {
	rows, err := reader.q.QueryContext(ctx, `SELECT run.run_id,run.id,attempt.id FROM verification_runs run JOIN verification_attempts attempt ON attempt.verification_run_id=run.id WHERE attempt.sequence=(SELECT MAX(latest.sequence) FROM verification_attempts latest WHERE latest.verification_run_id=run.id) AND ((run.state='verifying' AND attempt.state='running') OR (run.state='recovery_required' AND attempt.state='recovery_required')) ORDER BY run.run_id`)
	if err != nil {
		return nil, err
	}
	type candidateIDs struct {
		run             domain.RunID
		verificationRun domain.VerificationRunID
		attempt         domain.VerificationAttemptID
	}
	var ids []candidateIDs
	for rows.Next() {
		var runText, verificationRunText, attemptText string
		if err := rows.Scan(&runText, &verificationRunText, &attemptText); err != nil {
			rows.Close()
			return nil, err
		}
		runID, err := domain.ParseRunID(runText)
		if err != nil {
			rows.Close()
			return nil, err
		}
		verificationRunID, err := domain.ParseVerificationRunID(verificationRunText)
		if err != nil {
			rows.Close()
			return nil, err
		}
		attemptID, err := domain.ParseVerificationAttemptID(attemptText)
		if err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, candidateIDs{runID, verificationRunID, attemptID})
	}
	err = rows.Err()
	if closeErr := rows.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	result := make([]storecontract.VerificationStartupCandidate, 0, len(ids))
	for _, id := range ids {
		run, err := reader.GetRun(ctx, id.run)
		if err != nil {
			return nil, err
		}
		verificationRun, err := reader.GetVerificationRun(ctx, id.verificationRun)
		if err != nil {
			return nil, err
		}
		attempt, err := reader.GetVerificationAttempt(ctx, id.attempt)
		if err != nil {
			return nil, err
		}
		result = append(result, storecontract.VerificationStartupCandidate{Run: run, VerificationRun: verificationRun, Attempt: attempt.Attempt})
	}
	return result, nil
}
