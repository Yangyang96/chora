CREATE TABLE project_settings (
  project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE RESTRICT,
  version INTEGER NOT NULL CHECK(version >= 1),
  writable_files_json TEXT NOT NULL CHECK(json_valid(writable_files_json) AND json_type(writable_files_json)='array'),
  writable_directories_json TEXT NOT NULL CHECK(json_valid(writable_directories_json) AND json_type(writable_directories_json)='array'),
  verification_commands_json TEXT NOT NULL CHECK(json_valid(verification_commands_json) AND json_type(verification_commands_json)='array'),
  no_checks INTEGER NOT NULL CHECK(no_checks IN (0,1)),
  updated_at TEXT NOT NULL,
  CHECK((no_checks=1 AND json_array_length(verification_commands_json)=0)
     OR (no_checks=0 AND json_array_length(verification_commands_json)>0))
);

CREATE TRIGGER project_settings_identity_immutable
BEFORE UPDATE ON project_settings
WHEN OLD.project_id<>NEW.project_id OR NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at
BEGIN SELECT RAISE(ABORT,'invalid Project settings transition'); END;

CREATE TRIGGER project_settings_no_delete
BEFORE DELETE ON project_settings
BEGIN SELECT RAISE(ABORT,'Project settings are durable'); END;
