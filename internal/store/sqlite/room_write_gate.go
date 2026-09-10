package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func (tx *writeTx) requireActiveRoomResolved(ctx context.Context, query string, identity any) error {
	var roomText string
	err := tx.tx.QueryRowContext(ctx, query, identity).Scan(&roomText)
	if errors.Is(err, sql.ErrNoRows) {
		return storecontract.ErrNotFound
	}
	if err != nil {
		return err
	}
	roomID, err := domain.ParseRoomID(roomText)
	if err != nil {
		return err
	}
	return tx.requireActiveRoom(ctx, roomID)
}

func preserveWriteConflict(err, conflict error) error {
	if errors.Is(err, storecontract.ErrNotFound) {
		return conflict
	}
	return err
}

func (tx *writeTx) requireActiveRoomForTask(ctx context.Context, taskID domain.TaskID) error {
	return tx.requireActiveRoomResolved(ctx, `SELECT room_id FROM tasks WHERE id=?`, taskID.String())
}

func (tx *writeTx) requireActiveRoomForRun(ctx context.Context, runID domain.RunID) error {
	return tx.requireActiveRoomResolved(ctx, `SELECT task.room_id FROM runs run JOIN tasks task ON task.id=run.task_id WHERE run.id=?`, runID.String())
}

func (tx *writeTx) requireActiveRoomForAttempt(ctx context.Context, attemptID domain.AttemptID) error {
	return tx.requireActiveRoomResolved(ctx, `SELECT task.room_id FROM attempts attempt JOIN runs run ON run.id=attempt.run_id JOIN tasks task ON task.id=run.task_id WHERE attempt.id=?`, attemptID.String())
}

func (tx *writeTx) requireActiveRoomForRuntimeSession(ctx context.Context, sessionID domain.RuntimeSessionID) error {
	return tx.requireActiveRoomResolved(ctx, `SELECT task.room_id FROM runtime_sessions session JOIN attempts attempt ON attempt.id=session.attempt_id JOIN runs run ON run.id=attempt.run_id JOIN tasks task ON task.id=run.task_id WHERE session.id=?`, sessionID.String())
}

func (tx *writeTx) requireActiveRoomForVerificationRun(ctx context.Context, verificationRunID domain.VerificationRunID) error {
	return tx.requireActiveRoomResolved(ctx, `SELECT task.room_id FROM verification_runs verification JOIN runs run ON run.id=verification.run_id JOIN tasks task ON task.id=run.task_id WHERE verification.id=?`, verificationRunID.String())
}

func (tx *writeTx) requireActiveRoomForVerificationAttempt(ctx context.Context, attemptID domain.VerificationAttemptID) error {
	return tx.requireActiveRoomResolved(ctx, `SELECT task.room_id FROM verification_attempts attempt JOIN verification_runs verification ON verification.id=attempt.verification_run_id JOIN runs run ON run.id=verification.run_id JOIN tasks task ON task.id=run.task_id WHERE attempt.id=?`, attemptID.String())
}

func (tx *writeTx) requireActiveRoomForTechnicalPlanDraft(ctx context.Context, draftID domain.TechnicalPlanDraftID) error {
	return tx.requireActiveRoomResolved(ctx, `SELECT task.room_id FROM technical_plan_drafts draft JOIN tasks task ON task.id=draft.task_id WHERE draft.id=?`, draftID.String())
}

func (tx *writeTx) requireActiveRoomForTechnicalPlanRevision(ctx context.Context, revisionID domain.TechnicalPlanRevisionID) error {
	return tx.requireActiveRoomResolved(ctx, `SELECT task.room_id FROM technical_plan_revisions revision JOIN tasks task ON task.id=revision.task_id WHERE revision.id=?`, revisionID.String())
}

func (tx *writeTx) requireActiveRoomForCandidate(ctx context.Context, candidateID domain.CandidateID) error {
	return tx.requireActiveRoomResolved(ctx, `SELECT room_id FROM candidates WHERE id=?`, candidateID.String())
}

func (tx *writeTx) requireActiveRoomForDecisionGate(ctx context.Context, gateID domain.DecisionGateID) error {
	return tx.requireActiveRoomResolved(ctx, `SELECT task.room_id FROM execution_decision_gates gate_record JOIN runs run ON run.id=gate_record.run_id JOIN tasks task ON task.id=run.task_id WHERE gate_record.id=?`, gateID.String())
}
