-- Additive authority. Historical tables/contracts remain byte-for-byte intact.
CREATE TABLE repositories (
 repo_id TEXT PRIMARY KEY,
 name TEXT NOT NULL CHECK(length(trim(name))>0),
 checkout TEXT NOT NULL UNIQUE,
 common_git_dir TEXT NOT NULL DEFAULT '',
 physical_identity TEXT NOT NULL DEFAULT '',
 identity_source TEXT NOT NULL CHECK(identity_source IN ('legacy_unverified','inspected')),
 legacy_commit TEXT NOT NULL DEFAULT '', legacy_tree TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 CHECK((identity_source='legacy_unverified' AND physical_identity='' AND common_git_dir='') OR
       (identity_source='inspected' AND length(physical_identity)=64 AND length(common_git_dir)>0))
);
CREATE TABLE project_repositories (
 project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
 repo_id TEXT NOT NULL REFERENCES repositories(repo_id) ON DELETE RESTRICT,
 state TEXT NOT NULL CHECK(state IN ('active','removed')),
 version INTEGER NOT NULL CHECK(version>=1),
 added_at TEXT NOT NULL, updated_at TEXT NOT NULL,
 PRIMARY KEY(project_id,repo_id)
);
INSERT INTO repositories(repo_id,name,checkout,identity_source,legacy_commit,legacy_tree,created_at)
SELECT 'repo_'||substr(project_id,9),name,local_locator,'legacy_unverified',admitted_base,
 CASE WHEN instr(base_identity,':tree:')>0 THEN substr(base_identity,instr(base_identity,':tree:')+6) ELSE '' END,created_at
FROM project_repository_resources;
INSERT INTO project_repositories(project_id,repo_id,state,version,added_at,updated_at)
SELECT project_id,'repo_'||substr(project_id,9),state,version,created_at,updated_at FROM project_repository_resources;
CREATE TABLE room_repository_refs (
 room_id TEXT PRIMARY KEY REFERENCES rooms(id) ON DELETE RESTRICT,
 version INTEGER NOT NULL CHECK(version>=1),
 repo_ids_json TEXT NOT NULL CHECK(json_valid(repo_ids_json) AND json_type(repo_ids_json)='array'),
 updated_at TEXT NOT NULL
);
CREATE TRIGGER repositories_identity_immutable BEFORE UPDATE ON repositories
WHEN OLD.repo_id<>NEW.repo_id OR OLD.name<>NEW.name OR OLD.checkout<>NEW.checkout OR OLD.created_at<>NEW.created_at
 OR OLD.legacy_commit<>NEW.legacy_commit OR OLD.legacy_tree<>NEW.legacy_tree
 OR OLD.identity_source<>'legacy_unverified' OR NEW.identity_source<>'inspected'
BEGIN SELECT RAISE(ABORT,'repository identity is immutable'); END;
CREATE TRIGGER repositories_no_delete BEFORE DELETE ON repositories
BEGIN SELECT RAISE(ABORT,'repository history is durable'); END;
CREATE TRIGGER project_repositories_transition BEFORE UPDATE ON project_repositories
WHEN OLD.project_id<>NEW.project_id OR OLD.repo_id<>NEW.repo_id OR OLD.added_at<>NEW.added_at
 OR NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at
BEGIN SELECT RAISE(ABORT,'invalid repository association transition'); END;
CREATE TRIGGER project_repositories_no_delete BEFORE DELETE ON project_repositories
BEGIN SELECT RAISE(ABORT,'repository association history is durable'); END;
CREATE TRIGGER room_repository_refs_transition BEFORE UPDATE ON room_repository_refs
WHEN OLD.room_id<>NEW.room_id OR NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at
BEGIN SELECT RAISE(ABORT,'invalid Room resources transition'); END;

CREATE TABLE task_resource_snapshots (
 task_id TEXT PRIMARY KEY REFERENCES tasks(id) ON DELETE RESTRICT,
 canonical_json BLOB NOT NULL CHECK(length(canonical_json)<=1048576),
 digest BLOB NOT NULL CHECK(length(digest)=32), created_at TEXT NOT NULL
);
CREATE TRIGGER task_resource_snapshots_no_update BEFORE UPDATE ON task_resource_snapshots
BEGIN SELECT RAISE(ABORT,'Task resources are immutable'); END;
CREATE TRIGGER task_resource_snapshots_no_delete BEFORE DELETE ON task_resource_snapshots
BEGIN SELECT RAISE(ABORT,'Task resources are durable'); END;
CREATE TABLE task_repository_worktrees (
 task_id TEXT NOT NULL REFERENCES task_resource_snapshots(task_id) ON DELETE RESTRICT,
 repo_id TEXT NOT NULL REFERENCES repositories(repo_id) ON DELETE RESTRICT,
 path TEXT NOT NULL UNIQUE, root_fingerprint TEXT NOT NULL, base_commit TEXT NOT NULL, base_tree TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('preparing','ready','failed','cancelled','recovery_required')),
 version INTEGER NOT NULL CHECK(version>=1), reason TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,updated_at TEXT NOT NULL, PRIMARY KEY(task_id,repo_id)
);
CREATE TABLE repository_check_definitions (
 repo_id TEXT NOT NULL REFERENCES repositories(repo_id) ON DELETE RESTRICT,
 check_id TEXT NOT NULL,version INTEGER NOT NULL CHECK(version>=1),
 canonical_json BLOB NOT NULL CHECK(length(canonical_json)<=1048576),created_at TEXT NOT NULL,
 PRIMARY KEY(repo_id,check_id,version)
);
CREATE TABLE check_invocations (
 attempt_id TEXT NOT NULL REFERENCES attempts(id) ON DELETE RESTRICT,
 repo_id TEXT NOT NULL REFERENCES repositories(repo_id) ON DELETE RESTRICT,
 tool_call_id TEXT NOT NULL,sequence INTEGER NOT NULL CHECK(sequence>=1),
 canonical_json BLOB NOT NULL CHECK(length(canonical_json)<=1048576),created_at TEXT NOT NULL,
 PRIMARY KEY(attempt_id,repo_id,tool_call_id,sequence)
);
CREATE TABLE resource_result_groups (
 result_id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE RESTRICT,
 task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE RESTRICT,
 attempt_id TEXT NOT NULL UNIQUE REFERENCES attempts(id) ON DELETE RESTRICT,
 canonical_json BLOB NOT NULL CHECK(length(canonical_json)<=1048576),
 digest BLOB NOT NULL CHECK(length(digest)=32),created_at TEXT NOT NULL
);
CREATE TRIGGER resource_result_groups_no_update BEFORE UPDATE ON resource_result_groups
BEGIN SELECT RAISE(ABORT,'Result groups are immutable'); END;
CREATE TRIGGER resource_result_groups_no_delete BEFORE DELETE ON resource_result_groups
BEGIN SELECT RAISE(ABORT,'Result groups are durable'); END;
CREATE TABLE result_repository_changes (
 result_id TEXT NOT NULL REFERENCES resource_result_groups(result_id) ON DELETE RESTRICT,repo_id TEXT NOT NULL REFERENCES repositories(repo_id) ON DELETE RESTRICT,
 canonical_json BLOB NOT NULL CHECK(length(canonical_json)<=1048576),digest BLOB NOT NULL CHECK(length(digest)=32),
 created_at TEXT NOT NULL,PRIMARY KEY(result_id,repo_id)
);
CREATE TABLE apply_operations (
 operation_id TEXT PRIMARY KEY,result_id TEXT NOT NULL UNIQUE,
 canonical_json BLOB NOT NULL CHECK(length(canonical_json)<=1048576),created_at TEXT NOT NULL
);
CREATE TABLE apply_repository_steps (
 operation_id TEXT NOT NULL REFERENCES apply_operations(operation_id) ON DELETE RESTRICT,
 repo_id TEXT NOT NULL REFERENCES repositories(repo_id) ON DELETE RESTRICT,
 sequence INTEGER NOT NULL CHECK(sequence>=1),
 status TEXT NOT NULL CHECK(status IN ('planned','preflight','writing','applied','conflict','uncertain')),
 canonical_json BLOB NOT NULL CHECK(length(canonical_json)<=1048576),created_at TEXT NOT NULL,
 PRIMARY KEY(operation_id,repo_id,sequence)
);
CREATE TRIGGER repository_check_definitions_no_update BEFORE UPDATE ON repository_check_definitions
BEGIN SELECT RAISE(ABORT,'repository_check_definitions is append-only'); END;
CREATE TRIGGER repository_check_definitions_no_delete BEFORE DELETE ON repository_check_definitions
BEGIN SELECT RAISE(ABORT,'repository_check_definitions is append-only'); END;
CREATE TRIGGER check_invocations_no_update BEFORE UPDATE ON check_invocations
BEGIN SELECT RAISE(ABORT,'check_invocations is append-only'); END;
CREATE TRIGGER check_invocations_no_delete BEFORE DELETE ON check_invocations
BEGIN SELECT RAISE(ABORT,'check_invocations is append-only'); END;
CREATE TRIGGER result_repository_changes_no_update BEFORE UPDATE ON result_repository_changes
BEGIN SELECT RAISE(ABORT,'result_repository_changes is append-only'); END;
CREATE TRIGGER result_repository_changes_no_delete BEFORE DELETE ON result_repository_changes
BEGIN SELECT RAISE(ABORT,'result_repository_changes is append-only'); END;
CREATE TRIGGER apply_operations_no_update BEFORE UPDATE ON apply_operations
BEGIN SELECT RAISE(ABORT,'apply_operations is append-only'); END;
CREATE TRIGGER apply_operations_no_delete BEFORE DELETE ON apply_operations
BEGIN SELECT RAISE(ABORT,'apply_operations is append-only'); END;
CREATE TRIGGER apply_repository_steps_no_update BEFORE UPDATE ON apply_repository_steps
BEGIN SELECT RAISE(ABORT,'apply_repository_steps is append-only'); END;
CREATE TRIGGER apply_repository_steps_no_delete BEFORE DELETE ON apply_repository_steps
BEGIN SELECT RAISE(ABORT,'apply_repository_steps is append-only'); END;

CREATE TABLE resource_result_reviews (
 result_id TEXT PRIMARY KEY REFERENCES resource_result_groups(result_id) ON DELETE RESTRICT,
 result_digest BLOB NOT NULL CHECK(length(result_digest)=32),
 review_id TEXT NOT NULL UNIQUE REFERENCES review_decisions(id) ON DELETE RESTRICT
);
CREATE TRIGGER resource_result_reviews_no_update BEFORE UPDATE ON resource_result_reviews
BEGIN SELECT RAISE(ABORT,'Result reviews are immutable'); END;
CREATE TRIGGER resource_result_reviews_no_delete BEFORE DELETE ON resource_result_reviews
BEGIN SELECT RAISE(ABORT,'Result reviews are durable'); END;

CREATE TRIGGER task_repository_worktrees_transition BEFORE UPDATE ON task_repository_worktrees
WHEN OLD.task_id<>NEW.task_id OR OLD.repo_id<>NEW.repo_id OR OLD.path<>NEW.path
 OR OLD.root_fingerprint<>NEW.root_fingerprint OR OLD.base_commit<>NEW.base_commit OR OLD.base_tree<>NEW.base_tree
 OR OLD.created_at<>NEW.created_at OR NEW.updated_at<OLD.updated_at OR NEW.version<>OLD.version+1
BEGIN SELECT RAISE(ABORT,'Task worktree authority is immutable'); END;
CREATE TRIGGER task_repository_worktrees_no_delete BEFORE DELETE ON task_repository_worktrees
BEGIN SELECT RAISE(ABORT,'Task worktree history is durable'); END;
