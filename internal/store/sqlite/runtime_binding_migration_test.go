package sqlite

const rollbackRuntimeBindingMigration = `DROP TRIGGER run_charters_agent_execution_profile_valid;
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

DROP TRIGGER run_charters_runtime_binding_immutable; DROP TRIGGER attempts_runtime_binding_immutable; ALTER TABLE run_charters DROP COLUMN runtime_binding; ALTER TABLE attempts DROP COLUMN runtime_binding; DELETE FROM schema_migrations WHERE version=50;`

const RollbackRuntimeBindingMigration = rollbackRuntimeBindingMigration
