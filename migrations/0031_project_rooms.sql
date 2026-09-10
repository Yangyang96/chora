CREATE TABLE projects (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL CHECK(length(trim(name)) > 0),
  state TEXT NOT NULL CHECK(state IN ('active','archived')),
  version INTEGER NOT NULL CHECK(version >= 1),
  default_room_id TEXT NOT NULL UNIQUE REFERENCES rooms(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  archived_at TEXT
);

CREATE TABLE project_repository_resources (
  project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE RESTRICT,
  name TEXT NOT NULL CHECK(length(trim(name)) > 0),
  local_locator TEXT NOT NULL UNIQUE,
  source_kind TEXT NOT NULL CHECK(source_kind IN ('open','clone')),
  clone_url TEXT NOT NULL DEFAULT '',
  admitted_base TEXT NOT NULL,
  base_identity TEXT NOT NULL,
  target_worktree TEXT NOT NULL,
  dirty_admitted INTEGER NOT NULL CHECK(dirty_admitted IN (0,1)),
  state TEXT NOT NULL CHECK(state IN ('active','removed')),
  version INTEGER NOT NULL CHECK(version >= 1),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

INSERT INTO projects(id,name,state,version,default_room_id,created_at,updated_at,archived_at)
SELECT 'project_' || substr(binding.room_id,6), binding.name,
       CASE WHEN binding.state='removed' THEN 'archived' ELSE 'active' END,
       1, room.id, room.created_at, binding.updated_at,
       CASE WHEN binding.state='removed' THEN binding.updated_at ELSE NULL END
FROM repository_bindings binding JOIN rooms room ON room.id=binding.room_id;

INSERT INTO project_repository_resources(project_id,name,local_locator,source_kind,clone_url,admitted_base,base_identity,target_worktree,dirty_admitted,state,version,created_at,updated_at)
SELECT 'project_' || substr(room_id,6),name,local_locator,source_kind,clone_url,admitted_base,base_identity,target_worktree,dirty_admitted,state,version,created_at,updated_at
FROM repository_bindings;

ALTER TABLE rooms ADD COLUMN project_id TEXT REFERENCES projects(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE rooms ADD COLUMN ownership_kind TEXT NOT NULL DEFAULT 'unclassified'
  CHECK(ownership_kind IN ('project','legacy_standalone','unclassified'));

DROP TRIGGER rooms_lifecycle_update_valid;
UPDATE rooms SET project_id='project_' || substr(id,6), ownership_kind='project'
WHERE EXISTS(SELECT 1 FROM repository_bindings binding WHERE binding.room_id=rooms.id);

UPDATE rooms SET ownership_kind='legacy_standalone'
WHERE project_id IS NULL AND ownership_kind='unclassified' AND (
  EXISTS(SELECT 1 FROM tasks task JOIN run_charters charter ON charter.task_id=task.id WHERE task.room_id=rooms.id AND charter.adapter_id='fake')
  OR EXISTS(SELECT 1 FROM tasks task JOIN run_charters charter ON charter.task_id=task.id WHERE task.room_id=rooms.id AND charter.adapter_id='pi' AND charter.agent_runtime_source='managed_pi_image' AND charter.agent_execution_provider='docker')
  OR (rooms.workspace_root='/workspace/repository' AND EXISTS(SELECT 1 FROM tasks task JOIN spec_coding_bindings binding ON binding.task_id=task.id WHERE task.room_id=rooms.id AND NOT EXISTS(SELECT 1 FROM user_spec_coding_intents intent WHERE intent.task_id=task.id)))
);

CREATE INDEX rooms_project_idx ON rooms(project_id,state,updated_at DESC);
CREATE INDEX projects_state_idx ON projects(state,updated_at DESC);

CREATE TRIGGER rooms_lifecycle_update_valid
BEFORE UPDATE ON rooms
WHEN NEW.id <> OLD.id
  OR NEW.workspace_root <> OLD.workspace_root
  OR NEW.created_at <> OLD.created_at
  OR NEW.version <> OLD.version + 1
  OR NEW.updated_at < OLD.updated_at
  OR NOT (
    (OLD.state='active' AND NEW.state='active' AND OLD.archived_at IS NULL AND NEW.archived_at IS NULL)
    OR (OLD.state='archived' AND NEW.state='archived' AND OLD.archived_at IS NOT NULL AND NEW.archived_at=NEW.updated_at)
    OR (OLD.state='active' AND NEW.state='archived' AND NEW.archived_at=NEW.updated_at AND EXISTS(SELECT 1 FROM room_lifecycle_events event WHERE event.room_id=NEW.id AND event.version=NEW.version AND event.from_state=OLD.state AND event.to_state=NEW.state AND event.occurred_at=NEW.updated_at))
    OR (OLD.state='archived' AND NEW.state='active' AND NEW.archived_at IS NULL AND EXISTS(SELECT 1 FROM room_lifecycle_events event WHERE event.room_id=NEW.id AND event.version=NEW.version AND event.from_state=OLD.state AND event.to_state=NEW.state AND event.occurred_at=NEW.updated_at))
  )
BEGIN SELECT RAISE(ABORT,'invalid room lifecycle transition'); END;

CREATE TRIGGER rooms_ownership_insert_valid
BEFORE INSERT ON rooms
WHEN (NEW.ownership_kind='project') <> (NEW.project_id IS NOT NULL)
 OR EXISTS(SELECT 1 FROM projects project WHERE project.default_room_id=NEW.id AND project.id<>NEW.project_id)
BEGIN SELECT RAISE(ABORT,'invalid Room ownership'); END;

CREATE TRIGGER rooms_ownership_immutable
BEFORE UPDATE ON rooms
WHEN OLD.project_id IS NOT NEW.project_id OR OLD.ownership_kind <> NEW.ownership_kind
BEGIN SELECT RAISE(ABORT,'Room ownership is immutable'); END;

CREATE TRIGGER projects_identity_immutable
BEFORE UPDATE ON projects
WHEN OLD.id<>NEW.id OR OLD.default_room_id<>NEW.default_room_id OR OLD.created_at<>NEW.created_at
BEGIN SELECT RAISE(ABORT,'Project identity is immutable'); END;

CREATE TRIGGER projects_default_room_insert_valid
BEFORE INSERT ON projects
WHEN EXISTS(SELECT 1 FROM rooms room WHERE room.id=NEW.default_room_id AND (room.ownership_kind<>'project' OR room.project_id<>NEW.id))
BEGIN SELECT RAISE(ABORT,'Project default Room ownership mismatch'); END;

CREATE TRIGGER projects_no_delete BEFORE DELETE ON projects
BEGIN SELECT RAISE(ABORT,'Project is immutable'); END;

CREATE TRIGGER projects_transition_valid
BEFORE UPDATE ON projects
WHEN NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at
  OR NOT ((NEW.state=OLD.state) OR (OLD.state='active' AND NEW.state='archived') OR (OLD.state='archived' AND NEW.state='active'))
  OR (NEW.state='active' AND NEW.archived_at IS NOT NULL)
  OR (NEW.state='archived' AND NEW.archived_at<>NEW.updated_at)
BEGIN SELECT RAISE(ABORT,'invalid Project transition'); END;

CREATE TRIGGER project_repository_resources_immutable
BEFORE UPDATE ON project_repository_resources
WHEN OLD.project_id<>NEW.project_id OR OLD.name<>NEW.name OR OLD.local_locator<>NEW.local_locator
 OR OLD.source_kind<>NEW.source_kind OR OLD.clone_url<>NEW.clone_url OR OLD.admitted_base<>NEW.admitted_base
 OR OLD.base_identity<>NEW.base_identity OR OLD.target_worktree<>NEW.target_worktree
 OR OLD.dirty_admitted<>NEW.dirty_admitted OR OLD.created_at<>NEW.created_at
BEGIN SELECT RAISE(ABORT,'Project repository resource is immutable'); END;

CREATE TRIGGER project_repository_resources_no_delete
BEFORE DELETE ON project_repository_resources
BEGIN SELECT RAISE(ABORT,'Project repository resource is immutable'); END;
