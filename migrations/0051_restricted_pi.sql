-- No conversion or balance reset of v1 authority. Older binaries reject the
-- newer schema before execution through the existing migration ledger check.
CREATE TABLE workflow_pi_launches (
 channel TEXT PRIMARY KEY,
 workflow_id TEXT NOT NULL REFERENCES workflow_executions(id) ON DELETE RESTRICT,
 attempt_id TEXT NOT NULL UNIQUE REFERENCES attempts(id) ON DELETE RESTRICT,
 binding TEXT NOT NULL CHECK(json_valid(binding)),
 revoked INTEGER NOT NULL DEFAULT 0 CHECK(revoked IN (0,1))
);
CREATE TRIGGER workflow_pi_launch_immutable BEFORE UPDATE ON workflow_pi_launches
WHEN NEW.channel<>OLD.channel OR NEW.workflow_id<>OLD.workflow_id OR NEW.attempt_id<>OLD.attempt_id
 OR NEW.binding<>OLD.binding OR OLD.revoked=1 OR NEW.revoked<>1
BEGIN SELECT RAISE(ABORT,'restricted launch is immutable except irreversible revocation'); END;
CREATE TRIGGER workflow_pi_launch_no_delete BEFORE DELETE ON workflow_pi_launches
BEGIN SELECT RAISE(ABORT,'restricted launch history is durable'); END;

DROP TRIGGER agent_execution_profile_preferences_append_order;
DROP TRIGGER agent_execution_profile_preferences_immutable_update;
DROP TRIGGER agent_execution_profile_preferences_immutable_delete;

ALTER TABLE agent_execution_profile_preferences RENAME TO agent_execution_profile_preferences_v50;

CREATE TABLE agent_execution_profile_preferences (
  task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE RESTRICT,
  version INTEGER NOT NULL CHECK(version > 0),
  profile TEXT NOT NULL CHECK(profile IN ('minimal','standard','isolated_local','isolated_budgeted','trusted_local')),
  actor_id TEXT NOT NULL CHECK(length(actor_id) BETWEEN 1 AND 256 AND actor_id=trim(actor_id) AND instr(actor_id,char(0))=0),
  session_id TEXT NOT NULL CHECK(length(session_id) BETWEEN 1 AND 256 AND session_id=trim(session_id) AND instr(session_id,char(0))=0),
  selected_at TEXT NOT NULL,
  PRIMARY KEY(task_id,version)
);

INSERT INTO agent_execution_profile_preferences(task_id,version,profile,actor_id,session_id,selected_at)
SELECT task_id,version,profile,actor_id,session_id,selected_at FROM agent_execution_profile_preferences_v50;
DROP TABLE agent_execution_profile_preferences_v50;

CREATE TRIGGER agent_execution_profile_preferences_append_order
BEFORE INSERT ON agent_execution_profile_preferences
WHEN NEW.version <> COALESCE((SELECT max(version)+1 FROM agent_execution_profile_preferences WHERE task_id=NEW.task_id),1)
BEGIN
  SELECT RAISE(ABORT, 'Agent execution profile preference version conflict');
END;

CREATE TRIGGER agent_execution_profile_preferences_immutable_update
BEFORE UPDATE ON agent_execution_profile_preferences
BEGIN
  SELECT RAISE(ABORT, 'Agent execution profile preference is immutable');
END;

CREATE TRIGGER agent_execution_profile_preferences_immutable_delete
BEFORE DELETE ON agent_execution_profile_preferences
BEGIN
  SELECT RAISE(ABORT, 'Agent execution profile preference is immutable');
END;

DROP TRIGGER run_charters_agent_execution_profile_valid;
DROP TRIGGER run_charters_agent_execution_profile_update_valid;
DROP TRIGGER attempts_agent_execution_profile_valid;
DROP TRIGGER attempts_agent_execution_profile_update_valid;

CREATE TRIGGER run_charters_agent_execution_profile_valid
BEFORE INSERT ON run_charters
WHEN NOT (
  (NEW.adapter_id<>'pi' AND NEW.agent_execution_profile='' AND NEW.agent_runtime_source='' AND NEW.agent_execution_provider='' AND NEW.agent_capability_policy='' AND NEW.agent_trust_disclosure_policy='')
  OR (NEW.adapter_id='pi' AND (
    (NEW.agent_execution_profile='minimal' AND NEW.agent_runtime_source='managed_pi_image' AND NEW.agent_execution_provider='docker' AND NEW.agent_capability_policy='chora.minimal.v1' AND NEW.agent_trust_disclosure_policy='')
    OR (NEW.agent_execution_profile='standard' AND NEW.agent_runtime_source='managed_pi_image' AND NEW.agent_execution_provider='docker' AND NEW.agent_capability_policy='chora.standard.v1' AND NEW.agent_trust_disclosure_policy='')
    OR (NEW.agent_execution_profile='isolated_local' AND NEW.agent_runtime_source='public_pi_image' AND NEW.agent_execution_provider='docker' AND NEW.agent_capability_policy='chora.isolated-local.v1' AND NEW.agent_trust_disclosure_policy='')
    OR (NEW.agent_execution_profile='isolated_budgeted' AND NEW.agent_runtime_source='public_pi_budgeted_v1' AND NEW.agent_execution_provider='docker' AND NEW.agent_capability_policy='chora.isolated-budgeted.v1' AND NEW.agent_trust_disclosure_policy='')
    OR (NEW.agent_execution_profile='trusted_local' AND NEW.agent_runtime_source='local_pi' AND NEW.agent_execution_provider='trusted_host' AND NEW.agent_capability_policy='pi.native' AND NEW.agent_trust_disclosure_policy='chora.trusted-local-disclosure.v1')
  ))
)
BEGIN SELECT RAISE(ABORT, 'invalid Run Charter Agent execution profile binding'); END;

CREATE TRIGGER run_charters_agent_execution_profile_update_valid
BEFORE UPDATE ON run_charters
WHEN NOT (
  (NEW.adapter_id<>'pi' AND NEW.agent_execution_profile='' AND NEW.agent_runtime_source='' AND NEW.agent_execution_provider='' AND NEW.agent_capability_policy='' AND NEW.agent_trust_disclosure_policy='')
  OR (NEW.adapter_id='pi' AND (
    (NEW.agent_execution_profile='minimal' AND NEW.agent_runtime_source='managed_pi_image' AND NEW.agent_execution_provider='docker' AND NEW.agent_capability_policy='chora.minimal.v1' AND NEW.agent_trust_disclosure_policy='')
    OR (NEW.agent_execution_profile='standard' AND NEW.agent_runtime_source='managed_pi_image' AND NEW.agent_execution_provider='docker' AND NEW.agent_capability_policy='chora.standard.v1' AND NEW.agent_trust_disclosure_policy='')
    OR (NEW.agent_execution_profile='isolated_local' AND NEW.agent_runtime_source='public_pi_image' AND NEW.agent_execution_provider='docker' AND NEW.agent_capability_policy='chora.isolated-local.v1' AND NEW.agent_trust_disclosure_policy='')
    OR (NEW.agent_execution_profile='isolated_budgeted' AND NEW.agent_runtime_source='public_pi_budgeted_v1' AND NEW.agent_execution_provider='docker' AND NEW.agent_capability_policy='chora.isolated-budgeted.v1' AND NEW.agent_trust_disclosure_policy='')
    OR (NEW.agent_execution_profile='trusted_local' AND NEW.agent_runtime_source='local_pi' AND NEW.agent_execution_provider='trusted_host' AND NEW.agent_capability_policy='pi.native' AND NEW.agent_trust_disclosure_policy='chora.trusted-local-disclosure.v1')
  ))
)
BEGIN SELECT RAISE(ABORT, 'invalid Run Charter Agent execution profile binding'); END;

CREATE TRIGGER attempts_agent_execution_profile_valid
BEFORE INSERT ON attempts
WHEN NOT (
  (NEW.adapter_id<>'pi' AND NEW.agent_execution_profile='' AND NEW.agent_runtime_source='' AND NEW.agent_execution_provider='' AND NEW.agent_capability_policy='' AND NEW.agent_trust_disclosure_policy='')
  OR (NEW.adapter_id='pi' AND (
    (NEW.agent_execution_profile='minimal' AND NEW.agent_runtime_source='managed_pi_image' AND NEW.agent_execution_provider='docker' AND NEW.agent_capability_policy='chora.minimal.v1' AND NEW.agent_trust_disclosure_policy='')
    OR (NEW.agent_execution_profile='standard' AND NEW.agent_runtime_source='managed_pi_image' AND NEW.agent_execution_provider='docker' AND NEW.agent_capability_policy='chora.standard.v1' AND NEW.agent_trust_disclosure_policy='')
    OR (NEW.agent_execution_profile='isolated_local' AND NEW.agent_runtime_source='public_pi_image' AND NEW.agent_execution_provider='docker' AND NEW.agent_capability_policy='chora.isolated-local.v1' AND NEW.agent_trust_disclosure_policy='')
    OR (NEW.agent_execution_profile='isolated_budgeted' AND NEW.agent_runtime_source='public_pi_budgeted_v1' AND NEW.agent_execution_provider='docker' AND NEW.agent_capability_policy='chora.isolated-budgeted.v1' AND NEW.agent_trust_disclosure_policy='')
    OR (NEW.agent_execution_profile='trusted_local' AND NEW.agent_runtime_source='local_pi' AND NEW.agent_execution_provider='trusted_host' AND NEW.agent_capability_policy='pi.native' AND NEW.agent_trust_disclosure_policy='chora.trusted-local-disclosure.v1')
  ))
)
BEGIN SELECT RAISE(ABORT, 'invalid Attempt Agent execution profile binding'); END;

CREATE TRIGGER attempts_agent_execution_profile_update_valid
BEFORE UPDATE ON attempts
WHEN NOT (
  (NEW.adapter_id<>'pi' AND NEW.agent_execution_profile='' AND NEW.agent_runtime_source='' AND NEW.agent_execution_provider='' AND NEW.agent_capability_policy='' AND NEW.agent_trust_disclosure_policy='')
  OR (NEW.adapter_id='pi' AND (
    (NEW.agent_execution_profile='minimal' AND NEW.agent_runtime_source='managed_pi_image' AND NEW.agent_execution_provider='docker' AND NEW.agent_capability_policy='chora.minimal.v1' AND NEW.agent_trust_disclosure_policy='')
    OR (NEW.agent_execution_profile='standard' AND NEW.agent_runtime_source='managed_pi_image' AND NEW.agent_execution_provider='docker' AND NEW.agent_capability_policy='chora.standard.v1' AND NEW.agent_trust_disclosure_policy='')
    OR (NEW.agent_execution_profile='isolated_local' AND NEW.agent_runtime_source='public_pi_image' AND NEW.agent_execution_provider='docker' AND NEW.agent_capability_policy='chora.isolated-local.v1' AND NEW.agent_trust_disclosure_policy='')
    OR (NEW.agent_execution_profile='isolated_budgeted' AND NEW.agent_runtime_source='public_pi_budgeted_v1' AND NEW.agent_execution_provider='docker' AND NEW.agent_capability_policy='chora.isolated-budgeted.v1' AND NEW.agent_trust_disclosure_policy='')
    OR (NEW.agent_execution_profile='trusted_local' AND NEW.agent_runtime_source='local_pi' AND NEW.agent_execution_provider='trusted_host' AND NEW.agent_capability_policy='pi.native' AND NEW.agent_trust_disclosure_policy='chora.trusted-local-disclosure.v1')
  ))
)
BEGIN SELECT RAISE(ABORT, 'invalid Attempt Agent execution profile binding'); END;

DROP TRIGGER project_execution_settings_transition;
DROP TRIGGER project_execution_settings_no_delete;
DROP TRIGGER task_execution_settings_ownership;
DROP TRIGGER task_execution_settings_no_update;
DROP TRIGGER task_execution_settings_no_delete;
ALTER TABLE project_execution_settings RENAME TO project_execution_settings_v50;
ALTER TABLE task_execution_settings RENAME TO task_execution_settings_v50;
CREATE TABLE project_execution_settings (
  project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE RESTRICT,
  version INTEGER NOT NULL CHECK(version >= 1),
  agent_execution_profile TEXT NOT NULL CHECK(agent_execution_profile IN ('trusted_local','isolated_local','isolated_budgeted')),
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
  agent_execution_profile TEXT NOT NULL CHECK(agent_execution_profile IN ('trusted_local','isolated_local','isolated_budgeted')),
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

INSERT INTO project_execution_settings SELECT * FROM project_execution_settings_v50;
INSERT INTO task_execution_settings SELECT * FROM task_execution_settings_v50;
DROP TABLE project_execution_settings_v50;
DROP TABLE task_execution_settings_v50;
