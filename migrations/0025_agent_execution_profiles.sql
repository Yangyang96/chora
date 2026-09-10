ALTER TABLE run_charters ADD COLUMN agent_execution_profile TEXT NOT NULL DEFAULT '';
ALTER TABLE run_charters ADD COLUMN agent_runtime_source TEXT NOT NULL DEFAULT '';
ALTER TABLE run_charters ADD COLUMN agent_execution_provider TEXT NOT NULL DEFAULT '';
ALTER TABLE run_charters ADD COLUMN agent_capability_policy TEXT NOT NULL DEFAULT '';
ALTER TABLE run_charters ADD COLUMN agent_trust_disclosure_policy TEXT NOT NULL DEFAULT '';

ALTER TABLE attempts ADD COLUMN agent_execution_profile TEXT NOT NULL DEFAULT '';
ALTER TABLE attempts ADD COLUMN agent_runtime_source TEXT NOT NULL DEFAULT '';
ALTER TABLE attempts ADD COLUMN agent_execution_provider TEXT NOT NULL DEFAULT '';
ALTER TABLE attempts ADD COLUMN agent_capability_policy TEXT NOT NULL DEFAULT '';
ALTER TABLE attempts ADD COLUMN agent_trust_disclosure_policy TEXT NOT NULL DEFAULT '';

UPDATE run_charters
SET agent_execution_profile='standard',
    agent_runtime_source='managed_pi_image',
    agent_execution_provider='docker',
    agent_capability_policy='chora.standard.v1'
WHERE adapter_id='pi';

UPDATE attempts
SET agent_execution_profile='standard',
    agent_runtime_source='managed_pi_image',
    agent_execution_provider='docker',
    agent_capability_policy='chora.standard.v1'
WHERE adapter_id='pi';

CREATE TRIGGER run_charters_agent_execution_profile_valid
BEFORE INSERT ON run_charters
WHEN NOT (
  (NEW.adapter_id<>'pi'
    AND NEW.agent_execution_profile='' AND NEW.agent_runtime_source=''
    AND NEW.agent_execution_provider='' AND NEW.agent_capability_policy=''
    AND NEW.agent_trust_disclosure_policy='')
  OR
  (NEW.adapter_id='pi' AND (
    (NEW.agent_execution_profile='minimal'
      AND NEW.agent_runtime_source='managed_pi_image'
      AND NEW.agent_execution_provider='docker'
      AND NEW.agent_capability_policy='chora.minimal.v1'
      AND NEW.agent_trust_disclosure_policy='')
    OR
    (NEW.agent_execution_profile='standard'
      AND NEW.agent_runtime_source='managed_pi_image'
      AND NEW.agent_execution_provider='docker'
      AND NEW.agent_capability_policy='chora.standard.v1'
      AND NEW.agent_trust_disclosure_policy='')
    OR
    (NEW.agent_execution_profile='trusted_local'
      AND NEW.agent_runtime_source='local_pi'
      AND NEW.agent_execution_provider='trusted_host'
      AND NEW.agent_capability_policy='pi.native'
      AND NEW.agent_trust_disclosure_policy='chora.trusted-local-disclosure.v1')
  ))
)
BEGIN
  SELECT RAISE(ABORT, 'invalid Run Charter Agent execution profile binding');
END;

CREATE TRIGGER run_charters_agent_execution_profile_immutable
BEFORE UPDATE ON run_charters
WHEN OLD.agent_execution_profile<>NEW.agent_execution_profile
  OR OLD.agent_runtime_source<>NEW.agent_runtime_source
  OR OLD.agent_execution_provider<>NEW.agent_execution_provider
  OR OLD.agent_capability_policy<>NEW.agent_capability_policy
  OR OLD.agent_trust_disclosure_policy<>NEW.agent_trust_disclosure_policy
BEGIN
  SELECT RAISE(ABORT, 'Run Charter Agent execution profile binding is immutable');
END;

CREATE TRIGGER run_charters_agent_execution_profile_update_valid
BEFORE UPDATE ON run_charters
WHEN NOT (
  (NEW.adapter_id<>'pi'
    AND NEW.agent_execution_profile='' AND NEW.agent_runtime_source=''
    AND NEW.agent_execution_provider='' AND NEW.agent_capability_policy=''
    AND NEW.agent_trust_disclosure_policy='')
  OR
  (NEW.adapter_id='pi' AND (
    (NEW.agent_execution_profile='minimal' AND NEW.agent_runtime_source='managed_pi_image' AND NEW.agent_execution_provider='docker' AND NEW.agent_capability_policy='chora.minimal.v1' AND NEW.agent_trust_disclosure_policy='')
    OR (NEW.agent_execution_profile='standard' AND NEW.agent_runtime_source='managed_pi_image' AND NEW.agent_execution_provider='docker' AND NEW.agent_capability_policy='chora.standard.v1' AND NEW.agent_trust_disclosure_policy='')
    OR (NEW.agent_execution_profile='trusted_local' AND NEW.agent_runtime_source='local_pi' AND NEW.agent_execution_provider='trusted_host' AND NEW.agent_capability_policy='pi.native' AND NEW.agent_trust_disclosure_policy='chora.trusted-local-disclosure.v1')
  ))
)
BEGIN
  SELECT RAISE(ABORT, 'invalid Run Charter Agent execution profile binding');
END;

CREATE TRIGGER attempts_agent_execution_profile_valid
BEFORE INSERT ON attempts
WHEN NOT (
  (NEW.adapter_id<>'pi'
    AND NEW.agent_execution_profile='' AND NEW.agent_runtime_source=''
    AND NEW.agent_execution_provider='' AND NEW.agent_capability_policy=''
    AND NEW.agent_trust_disclosure_policy='')
  OR
  (NEW.adapter_id='pi' AND (
    (NEW.agent_execution_profile='minimal' AND NEW.agent_runtime_source='managed_pi_image' AND NEW.agent_execution_provider='docker' AND NEW.agent_capability_policy='chora.minimal.v1' AND NEW.agent_trust_disclosure_policy='')
    OR (NEW.agent_execution_profile='standard' AND NEW.agent_runtime_source='managed_pi_image' AND NEW.agent_execution_provider='docker' AND NEW.agent_capability_policy='chora.standard.v1' AND NEW.agent_trust_disclosure_policy='')
    OR (NEW.agent_execution_profile='trusted_local' AND NEW.agent_runtime_source='local_pi' AND NEW.agent_execution_provider='trusted_host' AND NEW.agent_capability_policy='pi.native' AND NEW.agent_trust_disclosure_policy='chora.trusted-local-disclosure.v1')
  ))
)
BEGIN
  SELECT RAISE(ABORT, 'invalid Attempt Agent execution profile binding');
END;

CREATE TRIGGER attempts_agent_execution_profile_immutable
BEFORE UPDATE ON attempts
WHEN OLD.agent_execution_profile<>NEW.agent_execution_profile
  OR OLD.agent_runtime_source<>NEW.agent_runtime_source
  OR OLD.agent_execution_provider<>NEW.agent_execution_provider
  OR OLD.agent_capability_policy<>NEW.agent_capability_policy
  OR OLD.agent_trust_disclosure_policy<>NEW.agent_trust_disclosure_policy
BEGIN
  SELECT RAISE(ABORT, 'Attempt Agent execution profile binding is immutable');
END;

CREATE TRIGGER attempts_agent_execution_profile_update_valid
BEFORE UPDATE ON attempts
WHEN NOT (
  (NEW.adapter_id<>'pi'
    AND NEW.agent_execution_profile='' AND NEW.agent_runtime_source=''
    AND NEW.agent_execution_provider='' AND NEW.agent_capability_policy=''
    AND NEW.agent_trust_disclosure_policy='')
  OR
  (NEW.adapter_id='pi' AND (
    (NEW.agent_execution_profile='minimal' AND NEW.agent_runtime_source='managed_pi_image' AND NEW.agent_execution_provider='docker' AND NEW.agent_capability_policy='chora.minimal.v1' AND NEW.agent_trust_disclosure_policy='')
    OR (NEW.agent_execution_profile='standard' AND NEW.agent_runtime_source='managed_pi_image' AND NEW.agent_execution_provider='docker' AND NEW.agent_capability_policy='chora.standard.v1' AND NEW.agent_trust_disclosure_policy='')
    OR (NEW.agent_execution_profile='trusted_local' AND NEW.agent_runtime_source='local_pi' AND NEW.agent_execution_provider='trusted_host' AND NEW.agent_capability_policy='pi.native' AND NEW.agent_trust_disclosure_policy='chora.trusted-local-disclosure.v1')
  ))
)
BEGIN
  SELECT RAISE(ABORT, 'invalid Attempt Agent execution profile binding');
END;
