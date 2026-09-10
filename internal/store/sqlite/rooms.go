package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func (tx *writeTx) InsertRoom(ctx context.Context, room domain.Room) error {
	var projectID any
	if room.ProjectID().Valid() {
		projectID = room.ProjectID().String()
	}
	_, err := tx.tx.ExecContext(ctx, `INSERT INTO rooms(id,name,description,workspace_root,state,version,created_at,updated_at,archived_at,project_id,ownership_kind) VALUES(?,?,?,?,?,?,?,?,NULL,?,?)`,
		room.ID().String(), room.Name(), room.Description(), room.WorkspaceRoot(), string(room.State()), room.Version(), timeText(room.CreatedAt()), timeText(room.UpdatedAt()), projectID, string(room.OwnershipKind()))
	return mapWriteError(err)
}

func (tx *writeTx) SaveRoomCAS(ctx context.Context, expectedVersion uint64, next domain.Room) error {
	current, err := tx.GetRoom(ctx, next.ID())
	if err != nil {
		return err
	}
	if current.Version() != expectedVersion {
		return storecontract.ErrVersionConflict
	}
	want, validateErr := current.Rename(next.Name(), next.Description(), expectedVersion, next.UpdatedAt())
	if validateErr != nil || !sameRoom(want, next) {
		return fmt.Errorf("%w: invalid Room successor", domain.ErrInvalidArgument)
	}
	var archived any
	if !next.ArchivedAt().IsZero() {
		archived = timeText(next.ArchivedAt())
	}
	result, err := tx.tx.ExecContext(ctx, `UPDATE rooms SET name=?,description=?,version=?,updated_at=?,archived_at=? WHERE id=? AND version=?`, next.Name(), next.Description(), next.Version(), timeText(next.UpdatedAt()), archived, next.ID().String(), expectedVersion)
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

func (tx *writeTx) SaveRoomLifecycleCAS(ctx context.Context, expectedVersion uint64, next domain.Room, event storecontract.RoomLifecycleEvent) error {
	current, err := tx.GetRoom(ctx, next.ID())
	if err != nil {
		return err
	}
	if current.Version() != expectedVersion {
		return storecontract.ErrVersionConflict
	}
	var want domain.Room
	switch {
	case current.State() == domain.RoomStateActive && next.State() == domain.RoomStateArchived:
		want, err = current.Archive(expectedVersion, next.UpdatedAt())
	case current.State() == domain.RoomStateArchived && next.State() == domain.RoomStateActive:
		want, err = current.Restore(expectedVersion, next.UpdatedAt())
	default:
		return fmt.Errorf("%w: unsupported Room lifecycle transition", storecontract.ErrRoomStateForbidden)
	}
	if err != nil || !sameRoom(want, next) || !validRoomLifecycleEvent(current, next, event) {
		return fmt.Errorf("%w: invalid Room lifecycle successor or audit event", domain.ErrInvalidArgument)
	}
	if next.State() == domain.RoomStateArchived {
		blocked, err := tx.roomHasExternalActivity(ctx, next.ID())
		if err != nil {
			return err
		}
		if blocked {
			return fmt.Errorf("%w: Room has active Run or Verification work", storecontract.ErrRoomStateForbidden)
		}
	}
	_, err = tx.tx.ExecContext(ctx, `INSERT INTO room_lifecycle_events(room_id,version,from_state,to_state,actor_id,session_id,idempotency_key_hash,request_digest,occurred_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		event.RoomID.String(), event.Version, string(event.FromState), string(event.ToState), event.ActorID, event.SessionID, event.IdempotencyKeyHash[:], event.RequestDigest[:], timeText(event.OccurredAt))
	if err != nil {
		return mapWriteError(err)
	}
	stored, err := tx.GetRoom(ctx, next.ID())
	if err != nil {
		return err
	}
	if !sameRoom(stored, next) {
		return fmt.Errorf("persisted Room lifecycle successor mismatch")
	}
	return nil
}

func (tx *writeTx) roomHasExternalActivity(ctx context.Context, roomID domain.RoomID) (bool, error) {
	room, err := tx.GetRoom(ctx, roomID)
	if err != nil {
		return false, err
	}
	rows, err := tx.tx.QueryContext(ctx, `SELECT id FROM tasks WHERE room_id=?`, roomID.String())
	if err != nil {
		return false, err
	}
	var ids []domain.TaskID
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return false, err
		}
		id, e := domain.ParseTaskID(raw)
		if e != nil {
			rows.Close()
			return false, e
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	for _, id := range ids {
		blocked, e := tx.taskHasExternalActivity(ctx, id, room.OwnershipKind() == domain.RoomOwnershipProject)
		if e != nil || blocked {
			return blocked, e
		}
	}
	return false, nil
}

func (tx *writeTx) requireActiveRoom(ctx context.Context, roomID domain.RoomID) error {
	result, err := tx.tx.ExecContext(ctx, `UPDATE rooms SET version=version+1 WHERE id=? AND state='active' AND (ownership_kind<>'project' OR EXISTS(SELECT 1 FROM projects WHERE projects.id=rooms.project_id AND projects.state='active'))`, roomID.String())
	if err != nil {
		return mapWriteError(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 1 {
		return nil
	}
	var state string
	err = tx.tx.QueryRowContext(ctx, `SELECT CASE WHEN room.ownership_kind='project' AND project.state<>'active' THEN 'project_'||project.state ELSE room.state END FROM rooms room LEFT JOIN projects project ON project.id=room.project_id WHERE room.id=?`, roomID.String()).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return storecontract.ErrNotFound
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("%w: Room %s is %s", storecontract.ErrRoomStateForbidden, roomID, state)
}

func (reader *reader) GetRoom(ctx context.Context, id domain.RoomID) (domain.Room, error) {
	room, err := scanRoom(reader.q.QueryRowContext(ctx, `SELECT id,name,description,workspace_root,state,version,created_at,updated_at,archived_at,project_id,ownership_kind FROM rooms WHERE id=?`, id.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Room{}, storecontract.ErrNotFound
	}
	return room, err
}

func scanRoom(scanner interface{ Scan(...any) error }) (domain.Room, error) {
	var idText, name, description, root, state, created, updated, ownership string
	var version uint64
	var archived, project sql.NullString
	if err := scanner.Scan(&idText, &name, &description, &root, &state, &version, &created, &updated, &archived, &project, &ownership); err != nil {
		return domain.Room{}, err
	}
	room, err := restoreRoom(idText, name, description, root, state, version, created, updated, archived)
	if err != nil {
		return domain.Room{}, err
	}
	record := domain.RoomRecord{ID: room.ID(), Name: room.Name(), Description: room.Description(), WorkspaceRoot: room.WorkspaceRoot(), State: room.State(), Version: room.Version(), CreatedAt: room.CreatedAt(), UpdatedAt: room.UpdatedAt(), ArchivedAt: room.ArchivedAt(), OwnershipKind: domain.RoomOwnershipKind(ownership)}
	if project.Valid {
		record.ProjectID, err = domain.ParseProjectID(project.String)
		if err != nil {
			return domain.Room{}, err
		}
	}
	return domain.RestoreRoom(record)
}

func (reader *reader) ListRoomDirectory(ctx context.Context, state domain.RoomState) ([]storecontract.RoomDirectoryItem, error) {
	if state != domain.RoomStateActive && state != domain.RoomStateArchived {
		return nil, fmt.Errorf("%w: invalid Room directory state", domain.ErrInvalidArgument)
	}
	rows, err := reader.q.QueryContext(ctx, `
WITH activity(room_id,at) AS (
  SELECT id,updated_at FROM rooms
  UNION ALL SELECT room_id,updated_at FROM tasks
  UNION ALL SELECT task.room_id,draft.updated_at FROM technical_plan_drafts draft JOIN tasks task ON task.id=draft.task_id
  UNION ALL SELECT task.room_id,revision.submitted_at FROM technical_plan_revisions revision JOIN tasks task ON task.id=revision.task_id
  UNION ALL SELECT task.room_id,review.decided_at FROM technical_plan_reviews review JOIN tasks task ON task.id=review.task_id
  UNION ALL SELECT task.room_id,binding.bound_at FROM technical_plan_acceptance_bindings binding JOIN tasks task ON task.id=binding.task_id
  UNION ALL SELECT task.room_id,run.updated_at FROM runs run JOIN tasks task ON task.id=run.task_id
  UNION ALL SELECT task.room_id,event.occurred_at FROM run_events event JOIN runs run ON run.id=event.run_id JOIN tasks task ON task.id=run.task_id
  UNION ALL SELECT task.room_id,review.decided_at FROM review_decisions review JOIN runs run ON run.id=review.run_id JOIN tasks task ON task.id=run.task_id
  UNION ALL SELECT task.room_id,verification.updated_at FROM verification_runs verification JOIN runs run ON run.id=verification.run_id JOIN tasks task ON task.id=run.task_id
  UNION ALL SELECT task.room_id,result.created_at FROM local_review_results result JOIN runs run ON run.id=result.run_id JOIN tasks task ON task.id=run.task_id
  UNION ALL SELECT task.room_id,review.decided_at FROM verified_review_decisions review JOIN runs run ON run.id=review.run_id JOIN tasks task ON task.id=run.task_id
  UNION ALL SELECT task.room_id,review.decided_at FROM local_review_decisions review JOIN runs run ON run.id=review.run_id JOIN tasks task ON task.id=run.task_id
), latest_activity AS (
  SELECT room_id,MAX(at) AS at FROM activity GROUP BY room_id
), task_counts AS (
  SELECT room_id,COUNT(*) AS total,
    SUM(CASE WHEN state='open' THEN 1 ELSE 0 END) AS open_count,
    SUM(CASE WHEN state='closed' THEN 1 ELSE 0 END) AS terminal_count
  FROM tasks GROUP BY room_id
)
SELECT room.id,room.name,room.description,room.workspace_root,room.state,room.version,
       room.created_at,room.updated_at,room.archived_at,room.project_id,room.ownership_kind,latest_activity.at,
       COALESCE(task_counts.total,0),COALESCE(task_counts.open_count,0),COALESCE(task_counts.terminal_count,0)
FROM rooms room
JOIN latest_activity ON latest_activity.room_id=room.id
LEFT JOIN task_counts ON task_counts.room_id=room.id
WHERE room.state=?
ORDER BY CASE WHEN ?='archived' THEN room.archived_at ELSE latest_activity.at END DESC,room.id ASC`, string(state), string(state))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []storecontract.RoomDirectoryItem
	for rows.Next() {
		var idText, name, description, root, stateText, created, updated, activity string
		var version uint64
		var archived, project sql.NullString
		var ownership string
		var counts storecontract.RoomTaskCounts
		if err := rows.Scan(&idText, &name, &description, &root, &stateText, &version, &created, &updated, &archived, &project, &ownership, &activity, &counts.Total, &counts.Open, &counts.Terminal); err != nil {
			return nil, err
		}
		room, err := restoreRoomWithOwnership(idText, name, description, root, stateText, version, created, updated, archived, project, ownership)
		if err != nil {
			return nil, err
		}
		activityAt, err := parseTime(activity)
		if err != nil {
			return nil, err
		}
		result = append(result, storecontract.RoomDirectoryItem{Room: room, LastActivityAt: activityAt, TaskCounts: counts})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	tasks, err := reader.queryWorkspaceTasks(ctx, directoryTasksQuery, string(state))
	if err != nil {
		return nil, err
	}
	roomsByID := make(map[domain.RoomID]int, len(result))
	for index := range result {
		roomsByID[result[index].Room.ID()] = index
	}
	for _, task := range tasks {
		index, ok := roomsByID[task.Task.RoomID()]
		if !ok {
			return nil, fmt.Errorf("directory Task references a Room outside the requested state")
		}
		result[index].Tasks = append(result[index].Tasks, task)
	}
	return result, nil
}

func (reader *reader) ListRoomLifecycleEvents(ctx context.Context, roomID domain.RoomID) ([]storecontract.RoomLifecycleEvent, error) {
	rows, err := reader.q.QueryContext(ctx, `SELECT room_id,version,from_state,to_state,actor_id,session_id,idempotency_key_hash,request_digest,occurred_at FROM room_lifecycle_events WHERE room_id=? ORDER BY version`, roomID.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []storecontract.RoomLifecycleEvent
	for rows.Next() {
		var roomText, from, to, actor, session, occurred string
		var version uint64
		var keyHash, requestDigest []byte
		if err := rows.Scan(&roomText, &version, &from, &to, &actor, &session, &keyHash, &requestDigest, &occurred); err != nil {
			return nil, err
		}
		id, err := domain.ParseRoomID(roomText)
		if err != nil || len(keyHash) != 32 || len(requestDigest) != 32 {
			return nil, fmt.Errorf("invalid persisted Room lifecycle event")
		}
		at, err := parseTime(occurred)
		if err != nil {
			return nil, err
		}
		event := storecontract.RoomLifecycleEvent{RoomID: id, FromState: domain.RoomState(from), ToState: domain.RoomState(to), Version: version, ActorID: actor, SessionID: session, OccurredAt: at}
		copy(event.IdempotencyKeyHash[:], keyHash)
		copy(event.RequestDigest[:], requestDigest)
		result = append(result, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func restoreRoom(idText, name, description, root, state string, version uint64, created, updated string, archived sql.NullString) (domain.Room, error) {
	return restoreRoomWithOwnership(idText, name, description, root, state, version, created, updated, archived, sql.NullString{}, string(domain.RoomOwnershipUnclassified))
}

func restoreRoomWithOwnership(idText, name, description, root, state string, version uint64, created, updated string, archived, project sql.NullString, ownership string) (domain.Room, error) {
	id, err := domain.ParseRoomID(idText)
	if err != nil {
		return domain.Room{}, err
	}
	createdAt, err := parseTime(created)
	if err != nil {
		return domain.Room{}, err
	}
	updatedAt, err := parseTime(updated)
	if err != nil {
		return domain.Room{}, err
	}
	var archivedAt time.Time
	if archived.Valid {
		archivedAt, err = parseTime(archived.String)
		if err != nil {
			return domain.Room{}, err
		}
	}
	record := domain.RoomRecord{ID: id, Name: name, Description: description, WorkspaceRoot: root, State: domain.RoomState(state), Version: version, CreatedAt: createdAt, UpdatedAt: updatedAt, ArchivedAt: archivedAt, OwnershipKind: domain.RoomOwnershipKind(ownership)}
	if project.Valid {
		record.ProjectID, err = domain.ParseProjectID(project.String)
		if err != nil {
			return domain.Room{}, err
		}
	}
	return domain.RestoreRoom(record)
}

func validRoomLifecycleEvent(before, after domain.Room, event storecontract.RoomLifecycleEvent) bool {
	zero := [32]byte{}
	return event.RoomID == after.ID() && event.FromState == before.State() && event.ToState == after.State() &&
		event.Version == after.Version() && event.OccurredAt.Equal(after.UpdatedAt()) && strings.TrimSpace(event.ActorID) != "" &&
		strings.TrimSpace(event.SessionID) != "" && event.IdempotencyKeyHash != zero && event.RequestDigest != zero
}

func sameRoom(left, right domain.Room) bool {
	return left.ID() == right.ID() && left.Name() == right.Name() && left.Description() == right.Description() &&
		left.ProjectID() == right.ProjectID() && left.OwnershipKind() == right.OwnershipKind() &&
		left.WorkspaceRoot() == right.WorkspaceRoot() && left.State() == right.State() && left.Version() == right.Version() &&
		left.CreatedAt().Equal(right.CreatedAt()) && left.UpdatedAt().Equal(right.UpdatedAt()) && left.ArchivedAt().Equal(right.ArchivedAt())
}
