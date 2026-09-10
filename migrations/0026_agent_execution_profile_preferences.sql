CREATE TABLE agent_execution_profile_preferences (
  task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE RESTRICT,
  version INTEGER NOT NULL CHECK(version > 0),
  profile TEXT NOT NULL CHECK(profile IN ('minimal','standard','trusted_local')),
  actor_id TEXT NOT NULL CHECK(length(actor_id) BETWEEN 1 AND 256 AND actor_id=trim(actor_id) AND instr(actor_id,char(0))=0),
  session_id TEXT NOT NULL CHECK(length(session_id) BETWEEN 1 AND 256 AND session_id=trim(session_id) AND instr(session_id,char(0))=0),
  selected_at TEXT NOT NULL,
  PRIMARY KEY(task_id,version)
);

CREATE TRIGGER agent_execution_profile_preferences_append_order
BEFORE INSERT ON agent_execution_profile_preferences
WHEN NEW.version <> COALESCE((
  SELECT max(version)+1 FROM agent_execution_profile_preferences WHERE task_id=NEW.task_id
),1)
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

-- Existing Real Spec Coding Tasks predate Task-level preference selection.
-- Their persisted Pi contract was Standard, so preserve that exact meaning.
INSERT INTO agent_execution_profile_preferences(task_id,version,profile,actor_id,session_id,selected_at)
SELECT task_id,1,'standard','chora-migration','chora-migration',created_at
FROM user_spec_coding_intents;

INSERT INTO agent_execution_profile_preferences(task_id,version,profile,actor_id,session_id,selected_at)
SELECT task_id,1,'standard','chora-migration','chora-migration',min(created_at)
FROM run_charters
WHERE adapter_id='pi'
  AND task_id NOT IN (SELECT task_id FROM agent_execution_profile_preferences)
GROUP BY task_id;

CREATE TABLE trusted_local_acknowledgements (
  actor_id TEXT NOT NULL CHECK(length(actor_id) BETWEEN 1 AND 256 AND actor_id=trim(actor_id) AND instr(actor_id,char(0))=0),
  policy_version TEXT NOT NULL CHECK(
    policy_version GLOB 'chora.trusted-local-disclosure.v[0-9]*'
    AND substr(policy_version,length('chora.trusted-local-disclosure.v')+1) NOT GLOB '*[^0-9]*'
  ),
  session_id TEXT NOT NULL CHECK(length(session_id) BETWEEN 1 AND 256 AND session_id=trim(session_id) AND instr(session_id,char(0))=0),
  acknowledged_at TEXT NOT NULL,
  PRIMARY KEY(actor_id,policy_version)
);

CREATE TRIGGER trusted_local_acknowledgements_immutable_update
BEFORE UPDATE ON trusted_local_acknowledgements
BEGIN
  SELECT RAISE(ABORT, 'Trusted Local acknowledgement is immutable');
END;

CREATE TRIGGER trusted_local_acknowledgements_immutable_delete
BEFORE DELETE ON trusted_local_acknowledgements
BEGIN
  SELECT RAISE(ABORT, 'Trusted Local acknowledgement is immutable');
END;
