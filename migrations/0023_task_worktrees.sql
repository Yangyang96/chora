CREATE TABLE task_worktrees (
  task_id TEXT PRIMARY KEY REFERENCES tasks(id) ON DELETE RESTRICT,
  repository_identity TEXT NOT NULL
    CHECK(length(repository_identity) BETWEEN 1 AND 100)
    CHECK(repository_identity=trim(repository_identity))
    CHECK(substr(repository_identity,1,1) GLOB '[A-Za-z0-9]')
    CHECK(repository_identity NOT GLOB '*[^A-Za-z0-9._-]*'),
  pinned_base_revision TEXT NOT NULL
    CHECK(length(pinned_base_revision) IN (40,64))
    CHECK(pinned_base_revision NOT GLOB '*[^0-9a-f]*'),
  relative_locator TEXT NOT NULL UNIQUE
    CHECK(length(relative_locator) BETWEEN 1 AND 512)
    CHECK(relative_locator=trim(relative_locator))
    CHECK(substr(relative_locator,1,1)<>'/')
    CHECK(instr(relative_locator,char(0))=0)
    CHECK(instr(relative_locator,'\')=0)
    CHECK(instr(relative_locator,'//')=0)
    CHECK(relative_locator<>'.' AND relative_locator<>'..')
    CHECK(relative_locator NOT LIKE './%' AND relative_locator NOT LIKE '../%')
    CHECK(relative_locator NOT LIKE '%/./%' AND relative_locator NOT LIKE '%/../%')
    CHECK(relative_locator NOT LIKE '%/.' AND relative_locator NOT LIKE '%/..'),
  configured_root_fingerprint BLOB NOT NULL CHECK(length(configured_root_fingerprint)=32 AND configured_root_fingerprint<>zeroblob(32)),
  state TEXT NOT NULL CHECK(state IN ('provisioning','ready','recovery_required')),
  version INTEGER NOT NULL CHECK(version>0),
  reason TEXT NOT NULL CHECK(reason=trim(reason)),
  created_at TEXT NOT NULL CHECK(julianday(created_at) IS NOT NULL),
  updated_at TEXT NOT NULL CHECK(julianday(updated_at) IS NOT NULL AND julianday(updated_at)>=julianday(created_at)),
  ready_at TEXT CHECK(ready_at IS NULL OR julianday(ready_at) IS NOT NULL),
  CHECK(
    (state='provisioning' AND reason='' AND ready_at IS NULL) OR
    (state='ready' AND version>=2 AND reason='' AND ready_at=updated_at) OR
    (state='recovery_required' AND version>=2 AND length(reason)>0 AND ready_at IS NULL)
  ),
  CHECK(length(relative_locator)>length(task_id)+11),
  CHECK(substr(relative_locator,1,length(task_id)+11)='worktrees/'||task_id||'-')
);

CREATE INDEX task_worktrees_recovery_candidates
  ON task_worktrees(updated_at,task_id)
  WHERE state IN ('provisioning','recovery_required');

CREATE TRIGGER task_worktrees_initial_state BEFORE INSERT ON task_worktrees
WHEN NEW.state<>'provisioning' OR NEW.version<>1 OR NEW.reason<>'' OR NEW.ready_at IS NOT NULL OR NEW.updated_at<>NEW.created_at
BEGIN
  SELECT RAISE(ABORT, 'Task worktree must start provisioning');
END;

CREATE TRIGGER task_worktrees_immutable_binding BEFORE UPDATE ON task_worktrees
WHEN OLD.task_id<>NEW.task_id OR OLD.repository_identity<>NEW.repository_identity
  OR OLD.pinned_base_revision<>NEW.pinned_base_revision OR OLD.relative_locator<>NEW.relative_locator
  OR OLD.configured_root_fingerprint<>NEW.configured_root_fingerprint OR OLD.created_at<>NEW.created_at
BEGIN
  SELECT RAISE(ABORT, 'Task worktree binding is immutable');
END;

CREATE TRIGGER task_worktrees_state_transition BEFORE UPDATE ON task_worktrees
WHEN NEW.version<>OLD.version+1
  OR julianday(NEW.updated_at)<julianday(OLD.updated_at)
  OR NOT (
    (OLD.state='provisioning' AND NEW.state IN ('ready','recovery_required')) OR
    (OLD.state='ready' AND NEW.state='recovery_required') OR
    (OLD.state='recovery_required' AND NEW.state='provisioning')
  )
BEGIN
  SELECT RAISE(ABORT, 'invalid Task worktree transition');
END;

CREATE TRIGGER task_worktrees_immutable_delete BEFORE DELETE ON task_worktrees
BEGIN
  SELECT RAISE(ABORT, 'Task worktree binding cannot be deleted');
END;
