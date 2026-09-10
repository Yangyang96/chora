package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func (tx *writeTx) InsertRun(ctx context.Context, run domain.AgentRun) error {
	if err := tx.requireActiveRoomForTask(ctx, run.TaskID()); err != nil {
		return err
	}
	task, err := tx.GetTask(ctx, run.TaskID())
	if err != nil {
		return err
	}
	if task.State() != domain.TaskStateOpen {
		return storecontract.ErrVersionConflict
	}
	_, err = tx.tx.ExecContext(ctx, `INSERT INTO runs(id,task_id,charter_id,state,version,current_attempt_number,last_event_sequence,created_at,updated_at,started_at,review_requested_at,terminal_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, run.ID().String(), run.TaskID().String(), run.CharterID().String(), string(run.State()), run.Version(), run.CurrentAttemptNumber(), 0, timeText(run.CreatedAt()), timeText(run.UpdatedAt()), nullableTime(run.StartedAt()), nullableTime(run.ReviewRequestedAt()), nullableTime(run.TerminalAt()))
	return mapWriteError(err)
}

func (reader *reader) GetRun(ctx context.Context, id domain.RunID) (domain.AgentRun, error) {
	var idText, taskText, charterText, state, created, updated string
	var version uint64
	var attempt int
	var started, reviewed, terminal sql.NullString
	err := reader.q.QueryRowContext(ctx, `SELECT id,task_id,charter_id,state,version,current_attempt_number,created_at,updated_at,started_at,review_requested_at,terminal_at FROM runs WHERE id=?`, id.String()).Scan(&idText, &taskText, &charterText, &state, &version, &attempt, &created, &updated, &started, &reviewed, &terminal)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AgentRun{}, storecontract.ErrNotFound
	}
	if err != nil {
		return domain.AgentRun{}, err
	}
	rid, err := domain.ParseRunID(idText)
	if err != nil {
		return domain.AgentRun{}, err
	}
	tid, err := domain.ParseTaskID(taskText)
	if err != nil {
		return domain.AgentRun{}, err
	}
	cid, err := domain.ParseCharterID(charterText)
	if err != nil {
		return domain.AgentRun{}, err
	}
	createdAt, err := parseTime(created)
	if err != nil {
		return domain.AgentRun{}, err
	}
	updatedAt, err := parseTime(updated)
	if err != nil {
		return domain.AgentRun{}, err
	}
	startedAt, err := parseNullableTime(started)
	if err != nil {
		return domain.AgentRun{}, err
	}
	reviewedAt, err := parseNullableTime(reviewed)
	if err != nil {
		return domain.AgentRun{}, err
	}
	terminalAt, err := parseNullableTime(terminal)
	if err != nil {
		return domain.AgentRun{}, err
	}
	return domain.RestoreAgentRun(domain.AgentRunRecord{ID: rid, TaskID: tid, CharterID: cid, State: domain.RunState(state), Version: version, CurrentAttemptNumber: attempt, CreatedAt: createdAt, UpdatedAt: updatedAt, StartedAt: startedAt, ReviewRequestedAt: reviewedAt, TerminalAt: terminalAt})
}

func (tx *writeTx) SaveRunCAS(ctx context.Context, expected uint64, run domain.AgentRun) error {
	if err := tx.requireActiveRoomForRun(ctx, run.ID()); err != nil {
		return preserveWriteConflict(err, storecontract.ErrVersionConflict)
	}
	current, err := tx.GetRun(ctx, run.ID())
	if errors.Is(err, storecontract.ErrNotFound) {
		return storecontract.ErrVersionConflict
	}
	if err != nil {
		return err
	}
	if current.Version() != expected || domain.ValidateRunSuccessor(current, run) != nil {
		return storecontract.ErrVersionConflict
	}
	result, err := tx.tx.ExecContext(ctx, `UPDATE runs SET state=?,version=?,current_attempt_number=?,updated_at=?,started_at=?,review_requested_at=?,terminal_at=? WHERE id=? AND version=?`, string(run.State()), run.Version(), run.CurrentAttemptNumber(), timeText(run.UpdatedAt()), nullableTime(run.StartedAt()), nullableTime(run.ReviewRequestedAt()), nullableTime(run.TerminalAt()), run.ID().String(), expected)
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

func (tx *writeTx) InsertAttempt(ctx context.Context, attempt domain.Attempt) error {
	if err := tx.requireActiveRoomForRun(ctx, attempt.RunID()); err != nil {
		return err
	}
	var predecessor any
	if id, ok := attempt.Predecessor(); ok {
		predecessor = id.String()
	}
	digest := attempt.ContextDigest()
	profile := attempt.AgentExecutionProfileBinding()
	_, err := tx.tx.ExecContext(ctx, `INSERT INTO attempts(id,run_id,sequence,predecessor_attempt_id,context_snapshot_id,context_digest,adapter_id,state,retry_reason,intervention_reason,context_delta,external_session,created_at,agent_execution_profile,agent_runtime_source,agent_execution_provider,agent_capability_policy,agent_trust_disclosure_policy) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, attempt.ID().String(), attempt.RunID().String(), attempt.Sequence(), predecessor, attempt.ContextSnapshotID().String(), digest[:], attempt.AdapterID(), string(attempt.State()), attempt.RetryReason(), attempt.InterventionReason(), attempt.ContextDelta(), attempt.ExternalSession(), timeText(attempt.CreatedAt()), string(profile.Profile()), profile.RuntimeSource(), profile.ExecutionProvider(), profile.CapabilityPolicy(), profile.TrustDisclosurePolicy())
	return mapWriteError(err)
}

func (reader *reader) GetAttempt(ctx context.Context, id domain.AttemptID) (domain.Attempt, error) {
	var idText, runText, snapshotText, adapter, state, retry, intervention, delta, external, created string
	var profileText, runtimeSource, executionProvider, capabilityPolicy, trustDisclosure string
	var sequence int
	var predecessor sql.NullString
	var digest []byte
	err := reader.q.QueryRowContext(ctx, `SELECT id,run_id,sequence,predecessor_attempt_id,context_snapshot_id,context_digest,adapter_id,state,retry_reason,intervention_reason,context_delta,external_session,created_at,agent_execution_profile,agent_runtime_source,agent_execution_provider,agent_capability_policy,agent_trust_disclosure_policy FROM attempts WHERE id=?`, id.String()).Scan(&idText, &runText, &sequence, &predecessor, &snapshotText, &digest, &adapter, &state, &retry, &intervention, &delta, &external, &created, &profileText, &runtimeSource, &executionProvider, &capabilityPolicy, &trustDisclosure)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Attempt{}, storecontract.ErrNotFound
	}
	if err != nil {
		return domain.Attempt{}, err
	}
	aid, err := domain.ParseAttemptID(idText)
	if err != nil {
		return domain.Attempt{}, err
	}
	rid, err := domain.ParseRunID(runText)
	if err != nil {
		return domain.Attempt{}, err
	}
	sid, err := domain.ParseContextSnapshotID(snapshotText)
	if err != nil {
		return domain.Attempt{}, err
	}
	var pred *domain.AttemptID
	if predecessor.Valid {
		p, err := domain.ParseAttemptID(predecessor.String)
		if err != nil {
			return domain.Attempt{}, err
		}
		pred = &p
	}
	var sum [32]byte
	if len(digest) != 32 {
		return domain.Attempt{}, errors.New("invalid persisted context digest")
	}
	copy(sum[:], digest)
	createdAt, err := parseTime(created)
	if err != nil {
		return domain.Attempt{}, err
	}
	profile, err := domain.RestoreAgentExecutionProfileBinding(domain.AgentExecutionProfileBindingRecord{Profile: domain.AgentExecutionProfile(profileText), RuntimeSource: runtimeSource, ExecutionProvider: executionProvider, CapabilityPolicy: capabilityPolicy, TrustDisclosurePolicy: trustDisclosure})
	if err != nil {
		return domain.Attempt{}, err
	}
	return domain.RestoreAttempt(domain.AttemptRecord{ID: aid, RunID: rid, Sequence: sequence, Predecessor: pred, ContextSnapshotID: sid, ContextDigest: sum, AdapterID: adapter, AgentExecutionProfileBinding: profile, ExternalSession: external, RetryReason: retry, InterventionReason: intervention, ContextDelta: delta, State: domain.AttemptState(state), CreatedAt: createdAt})
}

func (reader *reader) GetCurrentAttempt(ctx context.Context, runID domain.RunID) (domain.Attempt, error) {
	var id string
	err := reader.q.QueryRowContext(ctx, `SELECT id FROM attempts WHERE run_id=? ORDER BY sequence DESC LIMIT 1`, runID.String()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Attempt{}, storecontract.ErrNotFound
	}
	if err != nil {
		return domain.Attempt{}, err
	}
	parsed, err := domain.ParseAttemptID(id)
	if err != nil {
		return domain.Attempt{}, err
	}
	return reader.GetAttempt(ctx, parsed)
}

func (reader *reader) ListAttemptsForRun(ctx context.Context, runID domain.RunID) ([]domain.Attempt, error) {
	rows, err := reader.q.QueryContext(ctx, `SELECT id FROM attempts WHERE run_id=? ORDER BY sequence`, runID.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var attempts []domain.Attempt
	for rows.Next() {
		var idText string
		if err := rows.Scan(&idText); err != nil {
			return nil, err
		}
		id, err := domain.ParseAttemptID(idText)
		if err != nil {
			return nil, err
		}
		attempt, err := reader.GetAttempt(ctx, id)
		if err != nil {
			return nil, err
		}
		attempts = append(attempts, attempt)
	}
	return attempts, rows.Err()
}

func (tx *writeTx) SaveAttemptCAS(ctx context.Context, expected domain.AttemptState, attempt domain.Attempt) error {
	if err := tx.requireActiveRoomForAttempt(ctx, attempt.ID()); err != nil {
		return err
	}
	current, err := tx.GetAttempt(ctx, attempt.ID())
	if err != nil {
		return err
	}
	if current.State() != expected || current.AgentExecutionProfileBinding() != attempt.AgentExecutionProfileBinding() || domain.ValidateAttemptSuccessor(current, attempt) != nil {
		return storecontract.ErrVersionConflict
	}
	result, err := tx.tx.ExecContext(ctx, `UPDATE attempts SET state=? WHERE id=? AND state=?`, string(attempt.State()), attempt.ID().String(), string(expected))
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
