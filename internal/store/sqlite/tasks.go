package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func (tx *writeTx) InsertTask(ctx context.Context, task domain.Task) error {
	if err := tx.requireActiveRoom(ctx, task.RoomID()); err != nil {
		return err
	}
	now := timeText(time.Now())
	var predecessor any
	if task.PredecessorTaskID().Valid() {
		predecessor = task.PredecessorTaskID().String()
	}
	_, err := tx.tx.ExecContext(ctx, `INSERT INTO tasks(id,room_id,title,goal,state,predecessor_task_id,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, task.ID().String(), task.RoomID().String(), task.Title(), task.Goal(), string(task.State()), predecessor, now, now)
	if err != nil {
		return mapWriteError(err)
	}
	for i, c := range task.Criteria() {
		if _, err := tx.tx.ExecContext(ctx, `INSERT INTO task_criteria(criterion_id,task_id,position,title,description) VALUES(?,?,?,?,?)`, c.ID().String(), task.ID().String(), i, c.Title(), c.Description()); err != nil {
			return mapWriteError(err)
		}
	}
	return nil
}

func (reader *reader) GetTask(ctx context.Context, id domain.TaskID) (domain.Task, error) {
	var idText, roomText, title, goal, state string
	var predecessor, archivedAt sql.NullString
	var archived bool
	err := reader.q.QueryRowContext(ctx, `SELECT id,room_id,title,goal,state,predecessor_task_id,archived,archived_at FROM tasks WHERE id=?`, id.String()).Scan(&idText, &roomText, &title, &goal, &state, &predecessor, &archived, &archivedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Task{}, storecontract.ErrNotFound
	}
	if err != nil {
		return domain.Task{}, err
	}
	rows, err := reader.q.QueryContext(ctx, `SELECT criterion_id,title,description FROM task_criteria WHERE task_id=? ORDER BY position`, idText)
	if err != nil {
		return domain.Task{}, err
	}
	defer rows.Close()
	var criteria []domain.AcceptanceCriterion
	for rows.Next() {
		var cid, ct, cd string
		if err := rows.Scan(&cid, &ct, &cd); err != nil {
			return domain.Task{}, err
		}
		parsed, err := domain.ParseCriterionID(cid)
		if err != nil {
			return domain.Task{}, err
		}
		criterion, err := domain.NewAcceptanceCriterion(parsed, ct, cd)
		if err != nil {
			return domain.Task{}, err
		}
		criteria = append(criteria, criterion)
	}
	if err := rows.Err(); err != nil {
		return domain.Task{}, err
	}
	parsedID, err := domain.ParseTaskID(idText)
	if err != nil {
		return domain.Task{}, err
	}
	roomID, err := domain.ParseRoomID(roomText)
	if err != nil {
		return domain.Task{}, err
	}
	var predecessorID domain.TaskID
	if predecessor.Valid {
		predecessorID, err = domain.ParseTaskID(predecessor.String)
		if err != nil {
			return domain.Task{}, err
		}
	}
	var archivedTime time.Time
	if archivedAt.Valid {
		archivedTime, err = parseTime(archivedAt.String)
		if err != nil {
			return domain.Task{}, err
		}
	}
	return domain.RestoreTask(domain.TaskRecord{ID: parsedID, RoomID: roomID, PredecessorTaskID: predecessorID, Title: title, Goal: goal, Criteria: criteria, State: domain.TaskState(state), Archived: archived, ArchivedAt: archivedTime})
}

func (tx *writeTx) InsertCharter(ctx context.Context, charter domain.RunCharter) error {
	if err := tx.requireActiveRoomForTask(ctx, charter.TaskID()); err != nil {
		return err
	}
	profile := charter.AgentExecutionProfileBinding()
	_, err := tx.tx.ExecContext(ctx, `INSERT INTO run_charters(id,task_id,task_goal,workspace_root,adapter_id,sandbox_mode,expected_output,responsible_human,initiator,created_at,agent_execution_profile,agent_runtime_source,agent_execution_provider,agent_capability_policy,agent_trust_disclosure_policy) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, charter.ID().String(), charter.TaskID().String(), charter.TaskGoal(), charter.WorkspaceRoot(), charter.AdapterID(), charter.SandboxMode(), charter.ExpectedOutput(), charter.ResponsibleHuman(), charter.Initiator(), timeText(charter.CreatedAt()), string(profile.Profile()), profile.RuntimeSource(), profile.ExecutionProvider(), profile.CapabilityPolicy(), profile.TrustDisclosurePolicy())
	if err != nil {
		return mapWriteError(err)
	}
	for i, c := range charter.Criteria() {
		if _, err := tx.tx.ExecContext(ctx, `INSERT INTO charter_criteria(charter_id,criterion_id,position,title,description) VALUES(?,?,?,?,?)`, charter.ID().String(), c.ID().String(), i, c.Title(), c.Description()); err != nil {
			return mapWriteError(err)
		}
	}
	for name, allowed := range charter.CapabilityEnvelope() {
		if _, err := tx.tx.ExecContext(ctx, `INSERT INTO charter_capabilities(charter_id,name,allowed) VALUES(?,?,?)`, charter.ID().String(), name, allowed); err != nil {
			return mapWriteError(err)
		}
	}
	for i, id := range charter.ContextRevisionIDs() {
		result, err := tx.tx.ExecContext(ctx, `INSERT INTO charter_context_selections(charter_id,revision_id,position) SELECT ?,?,? WHERE EXISTS(SELECT 1 FROM context_revisions WHERE id=?)`, charter.ID().String(), id.String(), i, id.String())
		if err != nil {
			return mapWriteError(err)
		}
		if err := requireFrozenReference(result, "context revision", id.String()); err != nil {
			return err
		}
	}
	for _, id := range charter.ConfirmedSensitiveRevisionIDs() {
		result, err := tx.tx.ExecContext(ctx, `INSERT INTO charter_sensitive_confirmations(charter_id,revision_id) SELECT ?,? WHERE EXISTS(SELECT 1 FROM context_revisions WHERE id=?)`, charter.ID().String(), id.String(), id.String())
		if err != nil {
			return mapWriteError(err)
		}
		if err := requireFrozenReference(result, "confirmed context revision", id.String()); err != nil {
			return err
		}
	}
	for _, id := range charter.SensitiveExclusions() {
		result, err := tx.tx.ExecContext(ctx, `INSERT INTO charter_sensitive_exclusions(charter_id,entry_id) SELECT ?,? WHERE EXISTS(SELECT 1 FROM context_entries WHERE id=?)`, charter.ID().String(), id.String(), id.String())
		if err != nil {
			return mapWriteError(err)
		}
		if err := requireFrozenReference(result, "excluded context entry", id.String()); err != nil {
			return err
		}
	}
	return nil
}

func requireFrozenReference(result sql.Result, kind, id string) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("%w: missing %s %s", storecontract.ErrNotFound, kind, id)
	}
	return nil
}

func (reader *reader) GetCharter(ctx context.Context, id domain.CharterID) (domain.RunCharter, error) {
	var idText, taskText, goal, root, adapter, sandbox, output, human, initiator, created string
	var profileText, runtimeSource, executionProvider, capabilityPolicy, trustDisclosure string
	err := reader.q.QueryRowContext(ctx, `SELECT id,task_id,task_goal,workspace_root,adapter_id,sandbox_mode,expected_output,responsible_human,initiator,created_at,agent_execution_profile,agent_runtime_source,agent_execution_provider,agent_capability_policy,agent_trust_disclosure_policy FROM run_charters WHERE id=?`, id.String()).Scan(&idText, &taskText, &goal, &root, &adapter, &sandbox, &output, &human, &initiator, &created, &profileText, &runtimeSource, &executionProvider, &capabilityPolicy, &trustDisclosure)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.RunCharter{}, storecontract.ErrNotFound
	}
	if err != nil {
		return domain.RunCharter{}, err
	}
	taskID, err := domain.ParseTaskID(taskText)
	if err != nil {
		return domain.RunCharter{}, err
	}
	criteriaRows, err := reader.q.QueryContext(ctx, `SELECT criterion_id,title,description FROM charter_criteria WHERE charter_id=? ORDER BY position`, idText)
	if err != nil {
		return domain.RunCharter{}, err
	}
	var criteria []domain.AcceptanceCriterion
	for criteriaRows.Next() {
		var criterionID, title, description string
		if err := criteriaRows.Scan(&criterionID, &title, &description); err != nil {
			criteriaRows.Close()
			return domain.RunCharter{}, err
		}
		parsed, err := domain.ParseCriterionID(criterionID)
		if err != nil {
			criteriaRows.Close()
			return domain.RunCharter{}, err
		}
		criterion, err := domain.NewAcceptanceCriterion(parsed, title, description)
		if err != nil {
			criteriaRows.Close()
			return domain.RunCharter{}, err
		}
		criteria = append(criteria, criterion)
	}
	if err := criteriaRows.Err(); err != nil {
		criteriaRows.Close()
		return domain.RunCharter{}, err
	}
	if err := criteriaRows.Close(); err != nil {
		return domain.RunCharter{}, err
	}
	rows, err := reader.q.QueryContext(ctx, `SELECT revision_id FROM charter_context_selections WHERE charter_id=? ORDER BY position`, idText)
	if err != nil {
		return domain.RunCharter{}, err
	}
	var revisions []domain.ContextRevisionID
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			return domain.RunCharter{}, err
		}
		value, err := domain.ParseContextRevisionID(text)
		if err != nil {
			return domain.RunCharter{}, err
		}
		revisions = append(revisions, value)
	}
	if err := rows.Err(); err != nil {
		return domain.RunCharter{}, err
	}
	if err := rows.Close(); err != nil {
		return domain.RunCharter{}, err
	}
	capRows, err := reader.q.QueryContext(ctx, `SELECT name,allowed FROM charter_capabilities WHERE charter_id=?`, idText)
	if err != nil {
		return domain.RunCharter{}, err
	}
	caps := domain.CapabilityEnvelope{}
	for capRows.Next() {
		var name string
		var allowed bool
		if err := capRows.Scan(&name, &allowed); err != nil {
			return domain.RunCharter{}, err
		}
		caps[name] = allowed
	}
	if err := capRows.Err(); err != nil {
		return domain.RunCharter{}, err
	}
	if err := capRows.Close(); err != nil {
		return domain.RunCharter{}, err
	}
	confirmedRows, err := reader.q.QueryContext(ctx, `SELECT revision_id FROM charter_sensitive_confirmations WHERE charter_id=? ORDER BY revision_id`, idText)
	if err != nil {
		return domain.RunCharter{}, err
	}
	var confirmed []domain.ContextRevisionID
	for confirmedRows.Next() {
		var text string
		if err := confirmedRows.Scan(&text); err != nil {
			confirmedRows.Close()
			return domain.RunCharter{}, err
		}
		value, err := domain.ParseContextRevisionID(text)
		if err != nil {
			confirmedRows.Close()
			return domain.RunCharter{}, err
		}
		confirmed = append(confirmed, value)
	}
	if err := confirmedRows.Err(); err != nil {
		confirmedRows.Close()
		return domain.RunCharter{}, err
	}
	if err := confirmedRows.Close(); err != nil {
		return domain.RunCharter{}, err
	}
	exclusionRows, err := reader.q.QueryContext(ctx, `SELECT entry_id FROM charter_sensitive_exclusions WHERE charter_id=? ORDER BY entry_id`, idText)
	if err != nil {
		return domain.RunCharter{}, err
	}
	var exclusions []domain.ContextEntryID
	for exclusionRows.Next() {
		var text string
		if err := exclusionRows.Scan(&text); err != nil {
			exclusionRows.Close()
			return domain.RunCharter{}, err
		}
		value, err := domain.ParseContextEntryID(text)
		if err != nil {
			exclusionRows.Close()
			return domain.RunCharter{}, err
		}
		exclusions = append(exclusions, value)
	}
	if err := exclusionRows.Err(); err != nil {
		exclusionRows.Close()
		return domain.RunCharter{}, err
	}
	if err := exclusionRows.Close(); err != nil {
		return domain.RunCharter{}, err
	}
	parsedID, err := domain.ParseCharterID(idText)
	if err != nil {
		return domain.RunCharter{}, err
	}
	createdAt, err := parseTime(created)
	if err != nil {
		return domain.RunCharter{}, err
	}
	profile, err := domain.RestoreAgentExecutionProfileBinding(domain.AgentExecutionProfileBindingRecord{Profile: domain.AgentExecutionProfile(profileText), RuntimeSource: runtimeSource, ExecutionProvider: executionProvider, CapabilityPolicy: capabilityPolicy, TrustDisclosurePolicy: trustDisclosure})
	if err != nil {
		return domain.RunCharter{}, err
	}
	return domain.NewRunCharter(domain.RunCharterParams{ID: parsedID, TaskID: taskID, TaskGoal: goal, Criteria: criteria, ContextRevisionIDs: revisions, ConfirmedSensitiveRevisionIDs: confirmed, SensitiveExclusions: exclusions, WorkspaceRoot: root, AdapterID: adapter, SandboxMode: sandbox, ExpectedOutput: output, ResponsibleHuman: human, CapabilityEnvelope: caps, AgentExecutionProfileBinding: profile, Initiator: initiator, CreatedAt: createdAt})
}

func (tx *writeTx) CloseTaskForAcceptedRun(ctx context.Context, taskID domain.TaskID, runID domain.RunID, acceptedVersion uint64) error {
	if err := tx.requireActiveRoomForTask(ctx, taskID); err != nil {
		return preserveWriteConflict(err, storecontract.ErrVersionConflict)
	}
	result, err := tx.tx.ExecContext(ctx, `UPDATE tasks SET state='closed',updated_at=? WHERE id=? AND state='open' AND EXISTS(SELECT 1 FROM runs WHERE id=? AND task_id=? AND state='accepted' AND version=?)`, timeText(time.Now()), taskID.String(), runID.String(), taskID.String(), acceptedVersion)
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

func (tx *writeTx) CloseTaskForCompletedRun(ctx context.Context, taskID domain.TaskID, runID domain.RunID, acceptedVersion uint64) error {
	if err := tx.requireActiveRoomForTask(ctx, taskID); err != nil {
		return preserveWriteConflict(err, storecontract.ErrVersionConflict)
	}
	result, err := tx.tx.ExecContext(ctx, `UPDATE tasks SET state='closed',updated_at=? WHERE id=? AND state='open' AND EXISTS(SELECT 1 FROM runs WHERE id=? AND task_id=? AND state='completed' AND version=?)`, timeText(time.Now()), taskID.String(), runID.String(), taskID.String(), acceptedVersion)
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

func (tx *writeTx) ArchiveTask(ctx context.Context, taskID domain.TaskID, at time.Time) error {
	if blocked, err := tx.taskHasExternalActivity(ctx, taskID, true); err != nil {
		return err
	} else if blocked {
		return storecontract.ErrRoomStateForbidden
	}
	if err := tx.requireActiveRoomForTask(ctx, taskID); err != nil {
		return preserveWriteConflict(err, storecontract.ErrVersionConflict)
	}
	result, err := tx.tx.ExecContext(ctx, `UPDATE tasks SET archived=1, archived_at=?, updated_at=? WHERE id=? AND archived=0`, timeText(at), timeText(at), taskID.String())
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

func (tx *writeTx) RestoreTask(ctx context.Context, taskID domain.TaskID) error {
	if err := tx.requireActiveRoomForTask(ctx, taskID); err != nil {
		return preserveWriteConflict(err, storecontract.ErrVersionConflict)
	}
	result, err := tx.tx.ExecContext(ctx, `UPDATE tasks SET archived=0, archived_at=NULL, updated_at=? WHERE id=? AND archived=1`, timeText(time.Now()), taskID.String())
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
