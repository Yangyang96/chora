ALTER TABLE task_repository_worktrees ADD COLUMN delivery_mode TEXT NOT NULL DEFAULT '' CHECK(delivery_mode IN ('','task_branch'));
ALTER TABLE task_repository_worktrees ADD COLUMN task_branch TEXT NOT NULL DEFAULT '';
ALTER TABLE task_repository_worktrees ADD COLUMN target_ref TEXT NOT NULL DEFAULT '';
DROP TRIGGER task_repository_worktrees_transition;
CREATE TRIGGER task_repository_worktrees_transition BEFORE UPDATE ON task_repository_worktrees
WHEN OLD.task_id<>NEW.task_id OR OLD.repo_id<>NEW.repo_id OR OLD.path<>NEW.path
 OR OLD.root_fingerprint<>NEW.root_fingerprint OR OLD.base_commit<>NEW.base_commit OR OLD.base_tree<>NEW.base_tree
 OR OLD.delivery_mode<>NEW.delivery_mode OR OLD.task_branch<>NEW.task_branch OR OLD.target_ref<>NEW.target_ref
 OR OLD.created_at<>NEW.created_at OR NEW.updated_at<OLD.updated_at OR NEW.version<>OLD.version+1
BEGIN SELECT RAISE(ABORT,'Task worktree authority is immutable'); END;
CREATE TRIGGER task_repository_worktrees_branch_authority_insert BEFORE INSERT ON task_repository_worktrees
WHEN NOT ((NEW.delivery_mode='' AND NEW.task_branch='' AND NEW.target_ref='') OR
 (NEW.delivery_mode='task_branch' AND NEW.task_branch='chora/'||NEW.task_id||'/'||NEW.repo_id AND substr(NEW.target_ref,1,11)='refs/heads/' AND length(NEW.target_ref)>11))
BEGIN SELECT RAISE(ABORT,'invalid Task branch authority'); END;

CREATE TABLE repository_delivery_defaults (
 repo_id TEXT PRIMARY KEY REFERENCES repositories(repo_id) ON DELETE RESTRICT,
 target_ref TEXT NOT NULL CHECK(substr(target_ref,1,11)='refs/heads/' AND length(target_ref)>11),
 version INTEGER NOT NULL CHECK(version>=1), updated_at TEXT NOT NULL
);
CREATE TRIGGER repository_delivery_defaults_transition BEFORE UPDATE ON repository_delivery_defaults
WHEN OLD.repo_id<>NEW.repo_id OR NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at
BEGIN SELECT RAISE(ABORT,'invalid repository delivery default transition'); END;
CREATE TRIGGER repository_delivery_defaults_no_delete BEFORE DELETE ON repository_delivery_defaults
BEGIN SELECT RAISE(ABORT,'repository delivery default history is durable'); END;
