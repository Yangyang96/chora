package sqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
	"time"
)

const repositoryColumns = `repo_id,name,checkout,common_git_dir,physical_identity,identity_source,legacy_commit,legacy_tree,created_at`

func scanRepository(row interface{ Scan(...any) error }) (domain.RepositoryRecord, error) {
	var r domain.RepositoryRecord
	var id, created string
	if err := row.Scan(&id, &r.Name, &r.Checkout, &r.CommonGitDir, &r.PhysicalIdentity, &r.IdentitySource, &r.LegacyCommit, &r.LegacyTree, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = storecontract.ErrNotFound
		}
		return r, err
	}
	var err error
	r.ID, err = domain.ParseRepositoryID(id)
	if err != nil {
		return r, err
	}
	r.CreatedAt, err = parseTime(created)
	if err != nil {
		return r, err
	}
	return r, r.Validate()
}
func (r *reader) GetRepository(ctx context.Context, id domain.RepositoryID) (domain.RepositoryRecord, error) {
	return scanRepository(r.q.QueryRowContext(ctx, `SELECT `+repositoryColumns+` FROM repositories WHERE repo_id=?`, id.String()))
}
func (r *reader) GetRepositoryByCheckout(ctx context.Context, path string) (domain.RepositoryRecord, error) {
	return scanRepository(r.q.QueryRowContext(ctx, `SELECT `+repositoryColumns+` FROM repositories WHERE checkout=?`, path))
}
func (tx *writeTx) InsertRepository(ctx context.Context, r domain.RepositoryRecord) error {
	if err := r.Validate(); err != nil {
		return err
	}
	var overlaps int
	if err := tx.tx.QueryRowContext(ctx, `SELECT count(*) FROM repositories WHERE checkout=? OR substr(?,1,length(checkout)+1)=checkout||'/' OR substr(checkout,1,length(?)+1)=?||'/'`, r.Checkout, r.Checkout, r.Checkout, r.Checkout).Scan(&overlaps); err != nil {
		return err
	}
	if overlaps > 0 {
		return storecontract.ErrRepositoryBindingConflict
	}
	_, err := tx.tx.ExecContext(ctx, `INSERT INTO repositories(`+repositoryColumns+`) VALUES(?,?,?,?,?,?,?,?,?)`, r.ID.String(), r.Name, r.Checkout, r.CommonGitDir, r.PhysicalIdentity, r.IdentitySource, r.LegacyCommit, r.LegacyTree, timeText(r.CreatedAt))
	return mapWriteError(err)
}
func (tx *writeTx) VerifyLegacyRepository(ctx context.Context, r domain.RepositoryRecord) error {
	if err := r.Validate(); err != nil {
		return err
	}
	if r.IdentitySource != "inspected" {
		return domain.ErrInvalidArgument
	}
	out, err := tx.tx.ExecContext(ctx, `UPDATE repositories SET common_git_dir=?,physical_identity=?,identity_source='inspected' WHERE repo_id=? AND checkout=? AND identity_source='legacy_unverified'`, r.CommonGitDir, r.PhysicalIdentity, r.ID.String(), r.Checkout)
	if err != nil {
		return mapWriteError(err)
	}
	n, err := out.RowsAffected()
	if err == nil && n != 1 {
		return storecontract.ErrVersionConflict
	}
	return err
}
func scanAssociation(row interface{ Scan(...any) error }) (domain.ProjectRepository, error) {
	var a domain.ProjectRepository
	var project, id, created, added, updated string
	err := row.Scan(&project, &id, &a.Repository.Name, &a.Repository.Checkout, &a.Repository.CommonGitDir, &a.Repository.PhysicalIdentity, &a.Repository.IdentitySource, &a.Repository.LegacyCommit, &a.Repository.LegacyTree, &created, &a.State, &a.Version, &added, &updated)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = storecontract.ErrNotFound
		}
		return a, err
	}
	a.ProjectID, err = domain.ParseProjectID(project)
	if err != nil {
		return a, err
	}
	a.Repository.ID, err = domain.ParseRepositoryID(id)
	if err != nil {
		return a, err
	}
	for _, p := range []struct {
		s string
		t *time.Time
	}{{created, &a.Repository.CreatedAt}, {added, &a.AddedAt}, {updated, &a.UpdatedAt}} {
		*p.t, err = parseTime(p.s)
		if err != nil {
			return a, err
		}
	}
	return a, a.Repository.Validate()
}

const associationColumns = `a.project_id,r.repo_id,r.name,r.checkout,r.common_git_dir,r.physical_identity,r.identity_source,r.legacy_commit,r.legacy_tree,r.created_at,a.state,a.version,a.added_at,a.updated_at`

func (r *reader) GetProjectRepository(ctx context.Context, p domain.ProjectID, id domain.RepositoryID) (domain.ProjectRepository, error) {
	return scanAssociation(r.q.QueryRowContext(ctx, `SELECT `+associationColumns+` FROM project_repositories a JOIN repositories r ON r.repo_id=a.repo_id WHERE a.project_id=? AND a.repo_id=?`, p.String(), id.String()))
}
func (r *reader) ListProjectRepositories(ctx context.Context, p domain.ProjectID, after string, limit int) ([]domain.ProjectRepository, error) {
	if limit < 1 || limit > domain.RepositoryMaxPageSize+1 {
		return nil, domain.ErrInvalidArgument
	}
	rows, err := r.q.QueryContext(ctx, `SELECT `+associationColumns+` FROM project_repositories a JOIN repositories r ON r.repo_id=a.repo_id WHERE a.project_id=? AND a.repo_id>? ORDER BY a.repo_id LIMIT ?`, p.String(), after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.ProjectRepository{}
	for rows.Next() {
		a, err := scanAssociation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
func (tx *writeTx) SaveProjectRepository(ctx context.Context, expected uint64, a domain.ProjectRepository) error {
	if !a.ProjectID.Valid() || !a.Repository.ID.Valid() || a.Version != expected+1 || a.AddedAt.IsZero() || a.UpdatedAt.Before(a.AddedAt) || (a.State != "active" && a.State != "removed") {
		return domain.ErrInvalidArgument
	}
	if expected == 0 {
		_, err := tx.tx.ExecContext(ctx, `INSERT INTO project_repositories(project_id,repo_id,state,version,added_at,updated_at) VALUES(?,?,?,?,?,?)`, a.ProjectID.String(), a.Repository.ID.String(), a.State, a.Version, timeText(a.AddedAt), timeText(a.UpdatedAt))
		return mapWriteError(err)
	}
	out, err := tx.tx.ExecContext(ctx, `UPDATE project_repositories SET state=?,version=?,updated_at=? WHERE project_id=? AND repo_id=? AND version=?`, a.State, a.Version, timeText(a.UpdatedAt), a.ProjectID.String(), a.Repository.ID.String(), expected)
	if err != nil {
		return mapWriteError(err)
	}
	n, err := out.RowsAffected()
	if err == nil && n != 1 {
		return storecontract.ErrVersionConflict
	}
	return err
}
func (r *reader) GetRoomRepositoryReferences(ctx context.Context, id domain.RoomID) (domain.RoomRepositoryReferences, error) {
	out := domain.RoomRepositoryReferences{RoomID: id, RepositoryIDs: []domain.RepositoryID{}}
	var raw, at string
	err := r.q.QueryRowContext(ctx, `SELECT version,repo_ids_json,updated_at FROM room_repository_refs WHERE room_id=?`, id.String()).Scan(&out.Version, &raw, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return out, storecontract.ErrNotFound
	}
	if err != nil {
		return out, err
	}
	var ids []string
	if err = json.Unmarshal([]byte(raw), &ids); err != nil {
		return out, err
	}
	for _, s := range ids {
		id, err := domain.ParseRepositoryID(s)
		if err != nil {
			return out, err
		}
		out.RepositoryIDs = append(out.RepositoryIDs, id)
	}
	out.UpdatedAt, err = parseTime(at)
	return out, err
}
func (tx *writeTx) SaveRoomRepositoryReferences(ctx context.Context, expected uint64, refs domain.RoomRepositoryReferences) error {
	current, lookupErr := tx.GetRoomRepositoryReferences(ctx, refs.RoomID)
	if lookupErr == nil && current.Version != expected {
		return storecontract.ErrVersionConflict
	}
	if errors.Is(lookupErr, storecontract.ErrNotFound) && expected != 0 {
		return storecontract.ErrVersionConflict
	}
	if lookupErr != nil && !errors.Is(lookupErr, storecontract.ErrNotFound) {
		return lookupErr
	}

	if !refs.RoomID.Valid() || refs.Version != expected+1 || refs.UpdatedAt.IsZero() || len(refs.RepositoryIDs) > domain.TaskRepositoryLimit {
		return domain.ErrInvalidArgument
	}
	room, err := tx.GetRoom(ctx, refs.RoomID)
	if err != nil {
		return err
	}
	if room.OwnershipKind() != domain.RoomOwnershipProject {
		return domain.ErrInvalidArgument
	}
	ids := []string{}
	seen := map[string]bool{}
	for _, id := range refs.RepositoryIDs {
		if !id.Valid() || seen[id.String()] {
			return domain.ErrInvalidArgument
		}
		seen[id.String()] = true
		a, err := tx.GetProjectRepository(ctx, room.ProjectID(), id)
		if err != nil {
			return err
		}
		if a.State != "active" {
			return storecontract.ErrRoomStateForbidden
		}
		ids = append(ids, id.String())
	}
	raw, _ := json.Marshal(ids)
	if expected == 0 {
		_, err = tx.tx.ExecContext(ctx, `INSERT INTO room_repository_refs(room_id,version,repo_ids_json,updated_at) VALUES(?,?,?,?)`, refs.RoomID.String(), refs.Version, string(raw), timeText(refs.UpdatedAt))
		return mapWriteError(err)
	}
	result, err := tx.tx.ExecContext(ctx, `UPDATE room_repository_refs SET version=?,repo_ids_json=?,updated_at=? WHERE room_id=? AND version=?`, refs.Version, string(raw), timeText(refs.UpdatedAt), refs.RoomID.String(), expected)
	if err != nil {
		return mapWriteError(err)
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return storecontract.ErrVersionConflict
	}
	return err
}
func (r *reader) GetTaskResourceSnapshot(ctx context.Context, id domain.TaskID) (storecontract.TaskResourceRecord, error) {
	out := storecontract.TaskResourceRecord{TaskID: id}
	var digest []byte
	var at string
	err := r.q.QueryRowContext(ctx, `SELECT canonical_json,digest,created_at FROM task_resource_snapshots WHERE task_id=?`, id.String()).Scan(&out.CanonicalJSON, &digest, &at)
	if errors.Is(err, sql.ErrNoRows) {
		err = storecontract.ErrNotFound
	}
	if err != nil {
		return out, err
	}
	if len(digest) != 32 {
		return out, domain.ErrInvalidArgument
	}
	copy(out.Digest[:], digest)
	out.CreatedAt, err = parseTime(at)
	if err != nil {
		return out, err
	}
	return out, validateResourceRecord(out)
}
func validateResourceRecord(r storecontract.TaskResourceRecord) error {
	if !r.TaskID.Valid() || r.CreatedAt.IsZero() || len(r.CanonicalJSON) > domain.RepositoryMetadataBytes || sha256.Sum256(r.CanonicalJSON) != r.Digest {
		return domain.ErrInvalidArgument
	}
	var snapshot domain.TaskResourceSnapshot
	d := json.NewDecoder(bytes.NewReader(r.CanonicalJSON))
	d.DisallowUnknownFields()
	if err := d.Decode(&snapshot); err != nil {
		return err
	}
	b, digest, err := snapshot.CanonicalJSON()
	if err != nil {
		return err
	}
	if snapshot.TaskID != r.TaskID.String() || digest != r.Digest || !bytes.Equal(b, r.CanonicalJSON) {
		return domain.ErrInvalidArgument
	}
	return nil
}
func (tx *writeTx) InsertTaskResourceSnapshot(ctx context.Context, r storecontract.TaskResourceRecord) error {
	if err := validateResourceRecord(r); err != nil {
		return err
	}
	var s domain.TaskResourceSnapshot
	_ = json.Unmarshal(r.CanonicalJSON, &s)
	task, err := tx.GetTask(ctx, r.TaskID)
	if err != nil {
		return err
	}
	room, err := tx.GetRoom(ctx, task.RoomID())
	if err != nil {
		return err
	}
	if s.RoomID != room.ID().String() || s.ProjectID != room.ProjectID().String() {
		return domain.ErrInvalidArgument
	}
	for _, resource := range s.Resources {
		id, _ := domain.ParseRepositoryID(resource.RepoID)
		a, err := tx.GetProjectRepository(ctx, room.ProjectID(), id)
		if err != nil {
			return err
		}
		if a.State != "active" || a.Version != resource.AssociationVersion || a.Repository.PhysicalIdentity != resource.PhysicalIdentity || a.Repository.Checkout != resource.Checkout || a.Repository.CommonGitDir != resource.CommonGitDir {
			return storecontract.ErrVersionConflict
		}
	}
	_, err = tx.tx.ExecContext(ctx, `INSERT INTO task_resource_snapshots(task_id,canonical_json,digest,created_at) VALUES(?,?,?,?)`, r.TaskID.String(), r.CanonicalJSON, r.Digest[:], timeText(r.CreatedAt))
	return mapWriteError(err)
}
func (r *reader) RepositoryHasPendingWork(ctx context.Context, id domain.RepositoryID) (bool, error) {
	// Legacy tasks remain protected after their Project gains additional resources.
	var count int
	err := r.q.QueryRowContext(ctx, `SELECT count(*) FROM task_repository_worktrees w JOIN tasks t ON t.id=w.task_id WHERE w.repo_id=? AND (
 w.state IN ('preparing','recovery_required')
 OR EXISTS(SELECT 1 FROM runs run WHERE run.task_id=t.id AND run.state NOT IN ('accepted','cancelled','failed','completed') AND (
   run.state IN ('draft','ready','running','stopping','awaiting_verification','verifying','verification_recovery_required')
   OR NOT EXISTS(SELECT 1 FROM result_closures closure, json_each(closure.repository_ids) entry WHERE closure.run_id=run.id AND entry.value=w.repo_id)
 )))
 `, id.String()).Scan(&count)
	if err != nil {
		return false, err
	}
	if count > 0 {
		return true, nil
	}
	err = r.q.QueryRowContext(ctx, `SELECT count(*) FROM tasks t JOIN rooms room ON room.id=t.room_id JOIN project_repository_resources legacy ON legacy.project_id=room.project_id JOIN repositories repo ON repo.checkout=legacy.local_locator WHERE repo.repo_id=? AND NOT EXISTS(SELECT 1 FROM task_resource_snapshots s WHERE s.task_id=t.id) AND (
 EXISTS(SELECT 1 FROM runs run WHERE run.task_id=t.id AND run.state NOT IN ('accepted','cancelled','failed','completed') AND (
   run.state IN ('draft','ready','running','stopping','awaiting_verification','verifying','verification_recovery_required')
   OR NOT EXISTS(SELECT 1 FROM result_closures closure, json_each(closure.repository_ids) entry WHERE closure.run_id=run.id AND entry.value='')
 ))
 OR EXISTS(SELECT 1 FROM patch_applications pa JOIN runs run ON run.id=pa.run_id WHERE run.task_id=t.id AND pa.state<>'applied' AND (
   pa.state IN ('applying','recovery_required')
   OR NOT EXISTS(SELECT 1 FROM result_closures closure, json_each(closure.repository_ids) entry WHERE closure.run_id=run.id AND entry.value='')
 )))`, id.String()).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("inspect pending repository work: %w", err)
	}
	if count > 0 {
		return true, nil
	}
	err = r.q.QueryRowContext(ctx, `SELECT count(*) FROM result_repository_changes c JOIN resource_result_groups g ON g.result_id=c.result_id JOIN runs run ON run.id=g.run_id WHERE c.repo_id=? AND run.state='accepted' AND json_array_length(c.canonical_json,'$.changedPaths')>0
 AND NOT EXISTS(SELECT 1 FROM result_closures closure, json_each(closure.repository_ids) entry WHERE closure.result_id=g.result_id AND entry.value=c.repo_id)
 AND NOT EXISTS(SELECT 1 FROM apply_operations o JOIN apply_repository_steps step ON step.operation_id=o.operation_id WHERE o.result_id=g.result_id AND step.repo_id=c.repo_id AND step.status='applied')`, id.String()).Scan(&count)
	if err != nil {
		return false, err
	}
	if count > 0 {
		return true, nil
	}
	err = r.q.QueryRowContext(ctx, `SELECT count(*) FROM local_review_results result JOIN runs run ON run.id=result.run_id JOIN tasks t ON t.id=run.task_id JOIN rooms room ON room.id=t.room_id JOIN project_repository_resources legacy ON legacy.project_id=room.project_id JOIN repositories repo ON repo.checkout=legacy.local_locator WHERE repo.repo_id=? AND run.state='accepted'
 AND NOT EXISTS(SELECT 1 FROM result_closures closure, json_each(closure.repository_ids) entry WHERE closure.result_id=result.id AND entry.value='')
 AND NOT EXISTS(SELECT 1 FROM patch_applications pa WHERE pa.run_id=run.id AND pa.state='applied')`, id.String()).Scan(&count)
	if err != nil {
		return false, err
	}
	if count > 0 {
		return true, nil
	}
	err = r.q.QueryRowContext(ctx, `SELECT count(*) FROM apply_repository_steps step JOIN apply_operations operation ON operation.operation_id=step.operation_id
 WHERE step.repo_id=? AND step.sequence=(SELECT max(last.sequence) FROM apply_repository_steps last WHERE last.operation_id=step.operation_id AND last.repo_id=step.repo_id)
 AND step.status<>'applied' AND (step.status IN ('writing','uncertain') OR NOT EXISTS(
   SELECT 1 FROM result_closures closure, json_each(closure.repository_ids) entry WHERE closure.result_id=operation.result_id AND entry.value=step.repo_id
 ))`, id.String()).Scan(&count)
	if err != nil || count > 0 {
		return count > 0, err
	}
	err = r.q.QueryRowContext(ctx, `SELECT count(*) FROM task_delivery_operations delivery WHERE delivery.repo_id=? AND (
 delivery.state IN ('writing','recovery_required')
 OR (delivery.state='succeeded' AND delivery.kind IN ('commit','push','pr') AND NOT EXISTS(
   SELECT 1 FROM task_delivery_operations done WHERE done.task_id=delivery.task_id AND done.repo_id=delivery.repo_id AND done.state='succeeded'
   AND (done.kind='cleanup' OR json_extract(done.outcome_json,'$.PR.State')='merged')
 )))`, id.String()).Scan(&count)
	return count > 0, err
}

func (r *reader) ListResourceProjects(ctx context.Context, state domain.ProjectState, after string, limit int) ([]domain.Project, error) {
	if limit < 1 || limit > domain.RepositoryMaxPageSize+1 {
		return nil, domain.ErrInvalidArgument
	}
	rows, err := r.q.QueryContext(ctx, `SELECT `+projectColumns+` FROM projects WHERE state=? AND id>? ORDER BY id LIMIT ?`, string(state), after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.Project{}
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, rows.Err()
}

func (r *reader) ListTaskRepositoryWorktrees(ctx context.Context, taskID domain.TaskID) ([]domain.TaskRepositoryWorktree, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT repo_id,path,root_fingerprint,base_commit,base_tree,delivery_mode,task_branch,target_ref,state,version,reason,created_at,updated_at FROM task_repository_worktrees WHERE task_id=? ORDER BY repo_id`, taskID.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.TaskRepositoryWorktree{}
	for rows.Next() {
		w := domain.TaskRepositoryWorktree{TaskID: taskID}
		var id, created, updated string
		if err = rows.Scan(&id, &w.RelativePath, &w.RootFingerprint, &w.BaseCommit, &w.BaseTree, &w.DeliveryMode, &w.TaskBranch, &w.TargetRef, &w.State, &w.Version, &w.Reason, &created, &updated); err != nil {
			return nil, err
		}
		w.RepositoryID, err = domain.ParseRepositoryID(id)
		if err != nil {
			return nil, err
		}
		w.CreatedAt, err = parseTime(created)
		if err != nil {
			return nil, err
		}
		w.UpdatedAt, err = parseTime(updated)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}
func (tx *writeTx) SaveTaskRepositoryWorktree(ctx context.Context, expected uint64, w domain.TaskRepositoryWorktree) error {
	if !w.TaskID.Valid() || !w.RepositoryID.Valid() || w.Version != expected+1 || w.RelativePath == "" || w.RootFingerprint == "" || w.CreatedAt.IsZero() || w.UpdatedAt.Before(w.CreatedAt) {
		return domain.ErrInvalidArgument
	}
	if expected == 0 {
		_, err := tx.tx.ExecContext(ctx, `INSERT INTO task_repository_worktrees(task_id,repo_id,path,root_fingerprint,base_commit,base_tree,delivery_mode,task_branch,target_ref,state,version,reason,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, w.TaskID.String(), w.RepositoryID.String(), w.RelativePath, w.RootFingerprint, w.BaseCommit, w.BaseTree, w.DeliveryMode, w.TaskBranch, w.TargetRef, w.State, w.Version, w.Reason, timeText(w.CreatedAt), timeText(w.UpdatedAt))
		return mapWriteError(err)
	}
	out, err := tx.tx.ExecContext(ctx, `UPDATE task_repository_worktrees SET state=?,version=?,reason=?,updated_at=? WHERE task_id=? AND repo_id=? AND version=? AND path=? AND root_fingerprint=? AND base_commit=? AND base_tree=? AND delivery_mode=? AND task_branch=? AND target_ref=? AND created_at=?`, w.State, w.Version, w.Reason, timeText(w.UpdatedAt), w.TaskID.String(), w.RepositoryID.String(), expected, w.RelativePath, w.RootFingerprint, w.BaseCommit, w.BaseTree, w.DeliveryMode, w.TaskBranch, w.TargetRef, timeText(w.CreatedAt))
	if err != nil {
		return mapWriteError(err)
	}
	n, err := out.RowsAffected()
	if err == nil && n != 1 {
		return storecontract.ErrVersionConflict
	}
	return err
}

func (r *reader) ListRepositoryChecks(ctx context.Context, id domain.RepositoryID) ([]domain.RepositoryCheckDefinition, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT canonical_json,created_at FROM repository_check_definitions c WHERE repo_id=? AND version=(SELECT max(version) FROM repository_check_definitions current WHERE current.repo_id=c.repo_id AND current.check_id=c.check_id) ORDER BY check_id LIMIT 65`, id.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.RepositoryCheckDefinition{}
	for rows.Next() {
		c := domain.RepositoryCheckDefinition{RepositoryID: id}
		var raw []byte
		var at string
		if err = rows.Scan(&raw, &at); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &c.Command); err != nil {
			return nil, err
		}
		c.CreatedAt, err = parseTime(at)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if len(out) > 64 {
		return nil, domain.ErrInvalidArgument
	}
	return out, rows.Err()
}
func (tx *writeTx) InsertRepositoryCheck(ctx context.Context, expected uint64, c domain.RepositoryCheckDefinition) error {
	if !c.RepositoryID.Valid() || c.Command.ID == "" || c.Command.Name == "" || len(c.Command.Argv) == 0 || c.Command.Version != expected+1 || c.CreatedAt.IsZero() {
		return domain.ErrInvalidArgument
	}
	var current uint64
	if err := tx.tx.QueryRowContext(ctx, `SELECT coalesce(max(version),0) FROM repository_check_definitions WHERE repo_id=? AND check_id=?`, c.RepositoryID.String(), c.Command.ID).Scan(&current); err != nil {
		return err
	}
	if current != expected {
		return storecontract.ErrVersionConflict
	}
	raw, err := json.Marshal(c.Command)
	if err != nil {
		return err
	}
	if len(raw) > domain.RepositoryMetadataBytes {
		return domain.ErrInvalidArgument
	}
	_, err = tx.tx.ExecContext(ctx, `INSERT INTO repository_check_definitions(repo_id,check_id,version,canonical_json,created_at) VALUES(?,?,?,?,?)`, c.RepositoryID.String(), c.Command.ID, c.Command.Version, raw, timeText(c.CreatedAt))
	return mapWriteError(err)
}
