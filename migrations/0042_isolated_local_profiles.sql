DROP TRIGGER agent_execution_profile_preferences_append_order;
DROP TRIGGER agent_execution_profile_preferences_immutable_update;
DROP TRIGGER agent_execution_profile_preferences_immutable_delete;

ALTER TABLE agent_execution_profile_preferences RENAME TO agent_execution_profile_preferences_v41;

CREATE TABLE agent_execution_profile_preferences (
  task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE RESTRICT,
  version INTEGER NOT NULL CHECK(version > 0),
  profile TEXT NOT NULL CHECK(profile IN ('minimal','standard','isolated_local','trusted_local')),
  actor_id TEXT NOT NULL CHECK(length(actor_id) BETWEEN 1 AND 256 AND actor_id=trim(actor_id) AND instr(actor_id,char(0))=0),
  session_id TEXT NOT NULL CHECK(length(session_id) BETWEEN 1 AND 256 AND session_id=trim(session_id) AND instr(session_id,char(0))=0),
  selected_at TEXT NOT NULL,
  PRIMARY KEY(task_id,version)
);

INSERT INTO agent_execution_profile_preferences(task_id,version,profile,actor_id,session_id,selected_at)
SELECT task_id,version,profile,actor_id,session_id,selected_at FROM agent_execution_profile_preferences_v41;
DROP TABLE agent_execution_profile_preferences_v41;

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
    OR (NEW.agent_execution_profile='trusted_local' AND NEW.agent_runtime_source='local_pi' AND NEW.agent_execution_provider='trusted_host' AND NEW.agent_capability_policy='pi.native' AND NEW.agent_trust_disclosure_policy='chora.trusted-local-disclosure.v1')
  ))
)
BEGIN SELECT RAISE(ABORT, 'invalid Attempt Agent execution profile binding'); END;
