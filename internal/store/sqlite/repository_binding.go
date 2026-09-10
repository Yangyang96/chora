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

const repositoryBindingColumns = `room_id,name,local_locator,source_kind,clone_url,admitted_base,base_identity,target_worktree,dirty_admitted,state,version,created_at,updated_at`
const qualifiedRepositoryBindingColumns = `binding.room_id,binding.name,binding.local_locator,binding.source_kind,binding.clone_url,binding.admitted_base,binding.base_identity,binding.target_worktree,binding.dirty_admitted,binding.state,binding.version,binding.created_at,binding.updated_at`

type repositoryBindingRow struct {
	roomID         domain.RoomID
	name           string
	localLocator   string
	sourceKind     domain.RepositorySourceKind
	cloneURL       string
	admittedBase   string
	baseIdentity   string
	targetWorktree string
	dirtyAdmitted  bool
	state          domain.RepositoryBindingState
	version        uint64
	createdAt      string
	updatedAt      string
}

func (row repositoryBindingRow) restore() (domain.RepositoryBinding, error) {
	createdAt, err := parseTime(row.createdAt)
	if err != nil {
		return domain.RepositoryBinding{}, err
	}
	updatedAt, err := parseTime(row.updatedAt)
	if err != nil {
		return domain.RepositoryBinding{}, err
	}
	return domain.RestoreRepositoryBinding(domain.RepositoryBindingRecord{
		RoomID: row.roomID, Name: row.name, LocalLocator: row.localLocator, SourceKind: row.sourceKind,
		CloneURL: row.cloneURL, AdmittedBase: row.admittedBase, BaseIdentity: row.baseIdentity,
		TargetWorktree: row.targetWorktree, DirtyAdmitted: row.dirtyAdmitted, State: row.state,
		Version: row.version, CreatedAt: createdAt, UpdatedAt: updatedAt,
	})
}

func scanRepositoryBinding(scanner interface{ Scan(...any) error }) (repositoryBindingRow, error) {
	var idText, name, localLocator, sourceKind, cloneURL, admittedBase, baseIdentity, targetWorktree, state, created, updated string
	var dirty, version int
	err := scanner.Scan(&idText, &name, &localLocator, &sourceKind, &cloneURL, &admittedBase, &baseIdentity, &targetWorktree, &dirty, &state, &version, &created, &updated)
	if err != nil {
		return repositoryBindingRow{}, err
	}
	roomID, err := domain.ParseRoomID(idText)
	if err != nil {
		return repositoryBindingRow{}, err
	}
	return repositoryBindingRow{
		roomID: roomID, name: name, localLocator: localLocator, sourceKind: domain.RepositorySourceKind(sourceKind),
		cloneURL: cloneURL, admittedBase: admittedBase, baseIdentity: baseIdentity, targetWorktree: targetWorktree,
		dirtyAdmitted: dirty != 0, state: domain.RepositoryBindingState(state), version: uint64(version), createdAt: created, updatedAt: updated,
	}, nil
}

func (reader *reader) GetRepositoryBinding(ctx context.Context, roomID domain.RoomID) (domain.RepositoryBinding, error) {
	row, err := scanRepositoryBinding(reader.q.QueryRowContext(ctx, `
SELECT room.id,resource.name,resource.local_locator,resource.source_kind,resource.clone_url,resource.admitted_base,resource.base_identity,resource.target_worktree,resource.dirty_admitted,
 CASE WHEN project.state='archived' THEN 'removed' ELSE resource.state END,resource.version,resource.created_at,
 CASE WHEN project.state='archived' THEN project.updated_at ELSE resource.updated_at END
FROM rooms room JOIN projects project ON project.id=room.project_id JOIN project_repository_resources resource ON resource.project_id=room.project_id WHERE room.id=? AND room.ownership_kind='project'
UNION ALL SELECT `+qualifiedRepositoryBindingColumns+` FROM repository_bindings binding JOIN rooms room ON room.id=binding.room_id WHERE binding.room_id=? AND room.ownership_kind='legacy_standalone'`, roomID.String(), roomID.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.RepositoryBinding{}, storecontract.ErrNotFound
	}
	if err != nil {
		return domain.RepositoryBinding{}, err
	}
	return row.restore()
}

func (reader *reader) GetRepositoryBindingByLocator(ctx context.Context, locator string) (domain.RepositoryBinding, error) {
	row, err := scanRepositoryBinding(reader.q.QueryRowContext(ctx, `
SELECT project.default_room_id,resource.name,resource.local_locator,resource.source_kind,resource.clone_url,resource.admitted_base,resource.base_identity,resource.target_worktree,resource.dirty_admitted,CASE WHEN project.state='archived' THEN 'removed' ELSE resource.state END,resource.version,resource.created_at,CASE WHEN project.state='archived' THEN project.updated_at ELSE resource.updated_at END
FROM project_repository_resources resource JOIN projects project ON project.id=resource.project_id WHERE resource.local_locator=?
UNION ALL SELECT `+qualifiedRepositoryBindingColumns+` FROM repository_bindings binding JOIN rooms room ON room.id=binding.room_id WHERE binding.local_locator=? AND room.ownership_kind='legacy_standalone'`, locator, locator))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.RepositoryBinding{}, storecontract.ErrNotFound
	}
	if err != nil {
		return domain.RepositoryBinding{}, err
	}
	return row.restore()
}

func (reader *reader) ListRepositoryBindings(ctx context.Context, state domain.RepositoryBindingState) ([]domain.RepositoryBinding, error) {
	if state != domain.RepositoryBindingStateActive && state != domain.RepositoryBindingStateRemoved {
		return nil, fmt.Errorf("%w: invalid RepositoryBinding state", domain.ErrInvalidArgument)
	}
	rows, err := reader.q.QueryContext(ctx, `
SELECT project.default_room_id,resource.name,resource.local_locator,resource.source_kind,resource.clone_url,resource.admitted_base,resource.base_identity,resource.target_worktree,resource.dirty_admitted,CASE WHEN project.state='archived' THEN 'removed' ELSE resource.state END,resource.version,resource.created_at,CASE WHEN project.state='archived' THEN project.updated_at ELSE resource.updated_at END
FROM project_repository_resources resource JOIN projects project ON project.id=resource.project_id WHERE CASE WHEN project.state='archived' THEN 'removed' ELSE resource.state END=?
UNION ALL SELECT `+qualifiedRepositoryBindingColumns+` FROM repository_bindings binding JOIN rooms room ON room.id=binding.room_id WHERE binding.state=? AND room.ownership_kind='legacy_standalone'
ORDER BY 13 DESC`, string(state), string(state))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.RepositoryBinding
	for rows.Next() {
		row, err := scanRepositoryBinding(rows)
		if err != nil {
			return nil, err
		}
		binding, err := row.restore()
		if err != nil {
			return nil, err
		}
		result = append(result, binding)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (tx *writeTx) InsertRepositoryBinding(ctx context.Context, binding domain.RepositoryBinding) error {
	dirty := 0
	if binding.DirtyAdmitted() {
		dirty = 1
	}
	room, err := tx.GetRoom(ctx, binding.RoomID())
	if err != nil {
		return err
	}
	if room.OwnershipKind() == domain.RoomOwnershipProject {
		_, err = tx.tx.ExecContext(ctx, `INSERT INTO project_repository_resources(project_id,name,local_locator,source_kind,clone_url,admitted_base,base_identity,target_worktree,dirty_admitted,state,version,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, room.ProjectID().String(), binding.Name(), binding.LocalLocator(), string(binding.SourceKind()), binding.CloneURL(), binding.AdmittedBase(), binding.BaseIdentity(), binding.TargetWorktree(), dirty, string(binding.State()), binding.Version(), timeText(binding.CreatedAt()), timeText(binding.UpdatedAt()))
		if err != nil {
			return mapWriteError(err)
		}
		repoID, err := domain.ParseRepositoryID("repo_" + strings.TrimPrefix(room.ProjectID().String(), "project_"))
		if err != nil {
			return err
		}
		tree := ""
		if _, value, ok := strings.Cut(binding.BaseIdentity(), ":tree:"); ok {
			tree = value
		}
		repo := domain.RepositoryRecord{ID: repoID, Name: binding.Name(), Checkout: binding.LocalLocator(), IdentitySource: "legacy_unverified", LegacyCommit: binding.AdmittedBase(), LegacyTree: tree, CreatedAt: binding.CreatedAt()}
		if err = tx.InsertRepository(ctx, repo); err != nil {
			return err
		}
		return tx.SaveProjectRepository(ctx, 0, domain.ProjectRepository{ProjectID: room.ProjectID(), Repository: repo, State: string(binding.State()), Version: 1, AddedAt: binding.CreatedAt(), UpdatedAt: binding.UpdatedAt()})
	}
	if room.OwnershipKind() != domain.RoomOwnershipLegacyStandalone {
		return fmt.Errorf("%w: Room ownership is unclassified", storecontract.ErrRepositoryBindingConflict)
	}
	_, err = tx.tx.ExecContext(ctx, `INSERT INTO repository_bindings(`+repositoryBindingColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		binding.RoomID().String(), binding.Name(), binding.LocalLocator(), string(binding.SourceKind()), binding.CloneURL(),
		binding.AdmittedBase(), binding.BaseIdentity(), binding.TargetWorktree(), dirty, string(binding.State()),
		binding.Version(), timeText(binding.CreatedAt()), timeText(binding.UpdatedAt()))
	return mapWriteError(err)
}

func (tx *writeTx) SaveRepositoryBindingCAS(ctx context.Context, expectedVersion uint64, next domain.RepositoryBinding) error {
	current, err := tx.GetRepositoryBinding(ctx, next.RoomID())
	if err != nil {
		return err
	}
	if current.Version() != expectedVersion {
		return storecontract.ErrVersionConflict
	}
	want, err := current.Remove(expectedVersion, next.UpdatedAt())
	if err != nil || !sameRepositoryBinding(want, next) {
		return fmt.Errorf("%w: invalid RepositoryBinding successor", domain.ErrInvalidArgument)
	}
	room, err := tx.GetRoom(ctx, next.RoomID())
	if err != nil {
		return err
	}
	var result sql.Result
	if room.OwnershipKind() == domain.RoomOwnershipProject {
		result, err = tx.tx.ExecContext(ctx, `UPDATE project_repository_resources SET state=?,version=?,updated_at=? WHERE project_id=? AND version=? AND state='active'`, string(next.State()), next.Version(), timeText(next.UpdatedAt()), room.ProjectID().String(), expectedVersion)
	} else if room.OwnershipKind() == domain.RoomOwnershipLegacyStandalone {
		result, err = tx.tx.ExecContext(ctx, `UPDATE repository_bindings SET state=?,version=?,updated_at=? WHERE room_id=? AND version=? AND state='active'`, string(next.State()), next.Version(), timeText(next.UpdatedAt()), next.RoomID().String(), expectedVersion)
	} else {
		return fmt.Errorf("%w: Room ownership is unclassified", storecontract.ErrRepositoryBindingConflict)
	}
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

func sameRepositoryBinding(left, right domain.RepositoryBinding) bool {
	return left.RoomID() == right.RoomID() && left.Name() == right.Name() && left.LocalLocator() == right.LocalLocator() &&
		left.SourceKind() == right.SourceKind() && left.CloneURL() == right.CloneURL() && left.AdmittedBase() == right.AdmittedBase() &&
		left.BaseIdentity() == right.BaseIdentity() && left.TargetWorktree() == right.TargetWorktree() && left.DirtyAdmitted() == right.DirtyAdmitted() &&
		left.State() == right.State() && left.Version() == right.Version() && left.CreatedAt().Equal(right.CreatedAt()) && left.UpdatedAt().Equal(right.UpdatedAt())
}
