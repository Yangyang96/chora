CREATE TABLE project_execution_settings (
  project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE RESTRICT,
  version INTEGER NOT NULL CHECK(version >= 1),
  agent_execution_profile TEXT NOT NULL CHECK(agent_execution_profile IN ('trusted_local','isolated_local')),
  model_provider TEXT,
  model_id TEXT,
  updated_at TEXT NOT NULL,
  CHECK((model_provider IS NULL AND model_id IS NULL) OR
        (length(trim(model_provider)) > 0 AND length(model_provider) <= 256 AND
         length(trim(model_id)) > 0 AND length(model_id) <= 256))
);

CREATE TRIGGER project_execution_settings_transition
BEFORE UPDATE ON project_execution_settings
WHEN NEW.project_id<>OLD.project_id OR NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at
BEGIN SELECT RAISE(ABORT,'invalid Project execution settings transition'); END;

CREATE TRIGGER project_execution_settings_no_delete
BEFORE DELETE ON project_execution_settings
BEGIN SELECT RAISE(ABORT,'Project execution settings are durable'); END;

CREATE TABLE task_execution_settings (
  task_id TEXT PRIMARY KEY REFERENCES tasks(id) ON DELETE RESTRICT,
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  project_version INTEGER NOT NULL CHECK(project_version >= 0),
  agent_execution_profile TEXT NOT NULL CHECK(agent_execution_profile IN ('trusted_local','isolated_local')),
  environment_source TEXT NOT NULL CHECK(environment_source IN ('project','default','task')),
  model_source TEXT NOT NULL CHECK(model_source IN ('project','default','task')),
  model_binding TEXT,
  native_capabilities_json TEXT NOT NULL CHECK(
    length(CAST(native_capabilities_json AS BLOB)) <= 65536 AND
    json_valid(native_capabilities_json) AND json_type(native_capabilities_json)='object'
  ),
  created_at TEXT NOT NULL
);

CREATE TRIGGER task_execution_settings_ownership
BEFORE INSERT ON task_execution_settings
WHEN NOT EXISTS (
  SELECT 1 FROM tasks task JOIN rooms room ON room.id=task.room_id
  WHERE task.id=NEW.task_id AND room.project_id=NEW.project_id AND room.ownership_kind='project'
)
BEGIN SELECT RAISE(ABORT,'Task execution settings Project ownership mismatch'); END;

CREATE TRIGGER task_execution_settings_no_update
BEFORE UPDATE ON task_execution_settings
BEGIN SELECT RAISE(ABORT,'Task execution settings are immutable'); END;

CREATE TRIGGER task_execution_settings_no_delete
BEFORE DELETE ON task_execution_settings
BEGIN SELECT RAISE(ABORT,'Task execution settings are immutable'); END;
