package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func (tx *writeTx) InsertDecisionGate(ctx context.Context, gate domain.ExecutionDecisionGate) error {
	if err := tx.requireActiveRoomForRun(ctx, gate.RunID()); err != nil {
		return err
	}
	if gate.Status() != domain.DecisionGateOpen {
		return storecontract.ErrVersionConflict
	}
	_, err := tx.tx.ExecContext(ctx, `INSERT INTO execution_decision_gates(id,run_id,attempt_id,question,context,recommendation,impact,status,requested_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		gate.ID().String(), gate.RunID().String(), gate.AttemptID().String(), gate.Question(), gate.Context(), gate.Recommendation(), gate.Impact(), string(gate.Status()), timeText(gate.RequestedAt()))
	if err != nil {
		return mapWriteError(err)
	}
	for position, option := range gate.Options() {
		if _, err := tx.tx.ExecContext(ctx, `INSERT INTO execution_decision_gate_options(gate_id,option_id,position,label,impact) VALUES(?,?,?,?,?)`, gate.ID().String(), option.ID, position, option.Label, option.Impact); err != nil {
			return mapWriteError(err)
		}
	}
	return nil
}

func (reader *reader) GetDecisionGate(ctx context.Context, id domain.DecisionGateID) (domain.ExecutionDecisionGate, error) {
	var idText, runText, attemptText, question, contextText, recommendation, impact, status, requested string
	var selected, note, actor, session, resolved sql.NullString
	err := reader.q.QueryRowContext(ctx, `SELECT id,run_id,attempt_id,question,context,recommendation,impact,status,selected_option_id,note,actor_id,session_id,requested_at,resolved_at FROM execution_decision_gates WHERE id=?`, id.String()).Scan(
		&idText, &runText, &attemptText, &question, &contextText, &recommendation, &impact, &status, &selected, &note, &actor, &session, &requested, &resolved)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ExecutionDecisionGate{}, storecontract.ErrNotFound
	}
	if err != nil {
		return domain.ExecutionDecisionGate{}, err
	}
	parsedID, err := domain.ParseDecisionGateID(idText)
	if err != nil {
		return domain.ExecutionDecisionGate{}, err
	}
	runID, err := domain.ParseRunID(runText)
	if err != nil {
		return domain.ExecutionDecisionGate{}, err
	}
	attemptID, err := domain.ParseAttemptID(attemptText)
	if err != nil {
		return domain.ExecutionDecisionGate{}, err
	}
	requestedAt, err := parseTime(requested)
	if err != nil {
		return domain.ExecutionDecisionGate{}, err
	}
	resolvedAt, err := parseNullableTime(resolved)
	if err != nil {
		return domain.ExecutionDecisionGate{}, err
	}
	rows, err := reader.q.QueryContext(ctx, `SELECT option_id,label,impact FROM execution_decision_gate_options WHERE gate_id=? ORDER BY position`, idText)
	if err != nil {
		return domain.ExecutionDecisionGate{}, err
	}
	defer rows.Close()
	var options []domain.DecisionGateOption
	for rows.Next() {
		var option domain.DecisionGateOption
		if err := rows.Scan(&option.ID, &option.Label, &option.Impact); err != nil {
			return domain.ExecutionDecisionGate{}, err
		}
		options = append(options, option)
	}
	if err := rows.Err(); err != nil {
		return domain.ExecutionDecisionGate{}, err
	}
	return domain.RestoreExecutionDecisionGate(domain.ExecutionDecisionGateParams{
		ID: parsedID, RunID: runID, AttemptID: attemptID, Question: question, Context: contextText,
		Options: options, Recommendation: recommendation, Impact: impact, RequestedAt: requestedAt,
	}, domain.DecisionGateStatus(status), selected.String, note.String, actor.String, session.String, resolvedAt)
}

func (reader *reader) GetLatestDecisionGateForRun(ctx context.Context, runID domain.RunID) (domain.ExecutionDecisionGate, error) {
	var idText string
	err := reader.q.QueryRowContext(ctx, `SELECT id FROM execution_decision_gates WHERE run_id=? ORDER BY requested_at DESC,id DESC LIMIT 1`, runID.String()).Scan(&idText)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ExecutionDecisionGate{}, storecontract.ErrNotFound
	}
	if err != nil {
		return domain.ExecutionDecisionGate{}, err
	}
	id, err := domain.ParseDecisionGateID(idText)
	if err != nil {
		return domain.ExecutionDecisionGate{}, err
	}
	return reader.GetDecisionGate(ctx, id)
}

func (reader *reader) ListDecisionGatesForRun(ctx context.Context, runID domain.RunID) ([]domain.ExecutionDecisionGate, error) {
	rows, err := reader.q.QueryContext(ctx, `SELECT id FROM execution_decision_gates WHERE run_id=? ORDER BY requested_at,id`, runID.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.ExecutionDecisionGate
	for rows.Next() {
		var idText string
		if err := rows.Scan(&idText); err != nil {
			return nil, err
		}
		id, err := domain.ParseDecisionGateID(idText)
		if err != nil {
			return nil, err
		}
		gate, err := reader.GetDecisionGate(ctx, id)
		if err != nil {
			return nil, err
		}
		result = append(result, gate)
	}
	return result, rows.Err()
}

func (tx *writeTx) ResolveDecisionGateCAS(ctx context.Context, current, next domain.ExecutionDecisionGate) error {
	if err := tx.requireActiveRoomForDecisionGate(ctx, current.ID()); err != nil {
		return preserveWriteConflict(err, storecontract.ErrVersionConflict)
	}
	if current.ID() != next.ID() || current.RunID() != next.RunID() || current.AttemptID() != next.AttemptID() ||
		current.Status() != domain.DecisionGateOpen || next.Status() != domain.DecisionGateResolved ||
		current.Question() != next.Question() || current.Context() != next.Context() || current.Recommendation() != next.Recommendation() ||
		current.Impact() != next.Impact() || !current.RequestedAt().Equal(next.RequestedAt()) || fmt.Sprint(current.Options()) != fmt.Sprint(next.Options()) {
		return storecontract.ErrVersionConflict
	}
	result, err := tx.tx.ExecContext(ctx, `UPDATE execution_decision_gates SET status='resolved',selected_option_id=?,note=?,actor_id=?,session_id=?,resolved_at=? WHERE id=? AND status='open' AND EXISTS(SELECT 1 FROM execution_decision_gate_options WHERE gate_id=? AND option_id=?)`,
		next.SelectedOptionID(), next.Note(), next.ActorID(), next.SessionID(), timeText(next.ResolvedAt()), next.ID().String(), next.ID().String(), next.SelectedOptionID())
	if err != nil {
		return mapWriteError(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return storecontract.ErrVersionConflict
	}
	return nil
}
