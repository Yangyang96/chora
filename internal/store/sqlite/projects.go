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

func scanProject(scanner interface{ Scan(...any) error }) (domain.Project, error) {
	var idText, name, description, state, defaultRoomIDText, created, updated string
	var version uint64
	var archived sql.NullString
	if err := scanner.Scan(&idText, &name, &state, &version, &defaultRoomIDText, &created, &updated, &archived, &description); err != nil {
		return domain.Project{}, err
	}
	id, err := domain.ParseProjectID(idText)
	if err != nil {
		return domain.Project{}, err
	}
	defaultRoomID, err := domain.ParseRoomID(defaultRoomIDText)
	if err != nil {
		return domain.Project{}, err
	}
	createdAt, err := parseTime(created)
	if err != nil {
		return domain.Project{}, err
	}
	updatedAt, err := parseTime(updated)
	if err != nil {
		return domain.Project{}, err
	}
	var archivedAt time.Time
	if archived.Valid {
		archivedAt, err = parseTime(archived.String)
		if err != nil {
			return domain.Project{}, err
		}
	}
	return domain.RestoreProject(domain.ProjectRecord{ID: id, Name: name, Description: description, State: domain.ProjectState(state), Version: version, DefaultRoomID: defaultRoomID, CreatedAt: createdAt, UpdatedAt: updatedAt, ArchivedAt: archivedAt})
}

const projectColumns = `id,name,state,version,default_room_id,created_at,updated_at,archived_at,description`

func (reader *reader) GetProject(ctx context.Context, id domain.ProjectID) (domain.Project, error) {
	project, err := scanProject(reader.q.QueryRowContext(ctx, `SELECT `+projectColumns+` FROM projects WHERE id=?`, id.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Project{}, storecontract.ErrNotFound
	}
	return project, err
}

func (reader *reader) ListProjects(ctx context.Context, state domain.ProjectState) ([]domain.Project, error) {
	if state != domain.ProjectStateActive && state != domain.ProjectStateArchived {
		return nil, fmt.Errorf("%w: invalid Project state", domain.ErrInvalidArgument)
	}
	rows, err := reader.q.QueryContext(ctx, `SELECT `+projectColumns+` FROM projects WHERE state=? ORDER BY updated_at DESC,id`, string(state))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.Project
	for rows.Next() {
		project, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, project)
	}
	return result, rows.Err()
}

func (reader *reader) ListProjectRooms(ctx context.Context, projectID domain.ProjectID) ([]domain.Room, error) {
	rows, err := reader.q.QueryContext(ctx, `SELECT id,name,description,workspace_root,state,version,created_at,updated_at,archived_at,project_id,ownership_kind FROM rooms WHERE project_id=? ORDER BY created_at,id`, projectID.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.Room
	for rows.Next() {
		room, err := scanRoom(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, room)
	}
	return result, rows.Err()
}

func (tx *writeTx) InsertProject(ctx context.Context, project domain.Project) error {
	var archived any
	if !project.ArchivedAt().IsZero() {
		archived = timeText(project.ArchivedAt())
	}
	_, err := tx.tx.ExecContext(ctx, `INSERT INTO projects(id,name,state,version,default_room_id,created_at,updated_at,archived_at,description) VALUES(?,?,?,?,?,?,?,?,?)`, project.ID().String(), project.Name(), string(project.State()), project.Version(), project.DefaultRoomID().String(), timeText(project.CreatedAt()), timeText(project.UpdatedAt()), archived, project.Description())
	return mapWriteError(err)
}

func (tx *writeTx) SaveProjectCAS(ctx context.Context, expectedVersion uint64, next domain.Project) error {
	current, err := tx.GetProject(ctx, next.ID())
	if err != nil {
		return err
	}
	if current.Version() != expectedVersion {
		return storecontract.ErrVersionConflict
	}
	var want domain.Project
	switch {
	case current.State() == next.State():
		want, err = current.Rename(next.Name(), expectedVersion, next.UpdatedAt())
	case current.State() == domain.ProjectStateActive && next.State() == domain.ProjectStateArchived:
		want, err = current.Archive(expectedVersion, next.UpdatedAt())
	case current.State() == domain.ProjectStateArchived && next.State() == domain.ProjectStateActive:
		want, err = current.Restore(expectedVersion, next.UpdatedAt())
	default:
		return fmt.Errorf("%w: unsupported Project transition", domain.ErrInvalidArgument)
	}
	if err != nil || !sameProject(want, next) {
		return fmt.Errorf("%w: invalid Project successor", domain.ErrInvalidArgument)
	}
	if current.State() == domain.ProjectStateActive && next.State() == domain.ProjectStateArchived {
		blocked, checkErr := tx.projectHasExternalActivity(ctx, next.ID())
		if checkErr != nil {
			return checkErr
		}
		if blocked {
			return storecontract.ErrRoomStateForbidden
		}
	}
	var archived any
	if !next.ArchivedAt().IsZero() {
		archived = timeText(next.ArchivedAt())
	}
	result, err := tx.tx.ExecContext(ctx, `UPDATE projects SET name=?,state=?,version=?,updated_at=?,archived_at=? WHERE id=? AND version=?`, next.Name(), string(next.State()), next.Version(), timeText(next.UpdatedAt()), archived, next.ID().String(), expectedVersion)
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

func sameProject(left, right domain.Project) bool {
	return left.ID() == right.ID() && left.Name() == right.Name() && left.Description() == right.Description() && left.State() == right.State() && left.Version() == right.Version() && left.DefaultRoomID() == right.DefaultRoomID() && left.CreatedAt().Equal(right.CreatedAt()) && left.UpdatedAt().Equal(right.UpdatedAt()) && left.ArchivedAt().Equal(right.ArchivedAt())
}

func (tx *writeTx) projectHasExternalActivity(ctx context.Context, projectID domain.ProjectID) (bool, error) {
	var blocked bool
	err := tx.tx.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM runs run JOIN tasks task ON task.id=run.task_id JOIN rooms room ON room.id=task.room_id
 WHERE room.project_id=? AND run.state IN ('running','stopping','awaiting_review','awaiting_verification','verifying','recovery_required','verification_recovery_required')
 UNION ALL
 SELECT 1 FROM runs run JOIN tasks task ON task.id=run.task_id JOIN rooms room ON room.id=task.room_id
 WHERE room.project_id=? AND task.state='open' AND run.state='accepted' AND NOT EXISTS(SELECT 1 FROM patch_applications apply WHERE apply.run_id=run.id AND apply.state='applied')
 UNION ALL
 SELECT 1 FROM patch_applications apply JOIN runs run ON run.id=apply.run_id JOIN tasks task ON task.id=run.task_id JOIN rooms room ON room.id=task.room_id
 WHERE room.project_id=? AND apply.state IN ('applying','recovery_required')
)`, projectID.String(), projectID.String(), projectID.String()).Scan(&blocked)
	return blocked, err
}
