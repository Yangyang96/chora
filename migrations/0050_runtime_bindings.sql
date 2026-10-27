ALTER TABLE run_charters ADD COLUMN runtime_binding TEXT NOT NULL DEFAULT '';
ALTER TABLE attempts ADD COLUMN runtime_binding TEXT NOT NULL DEFAULT '';
CREATE TRIGGER run_charters_runtime_binding_immutable BEFORE UPDATE OF runtime_binding ON run_charters WHEN OLD.runtime_binding<>NEW.runtime_binding BEGIN SELECT RAISE(ABORT,'runtime binding is immutable'); END;
CREATE TRIGGER attempts_runtime_binding_immutable BEFORE UPDATE OF runtime_binding ON attempts WHEN OLD.runtime_binding<>NEW.runtime_binding BEGIN SELECT RAISE(ABORT,'runtime binding is immutable'); END;
DROP TRIGGER run_charters_agent_execution_profile_valid;
DROP TRIGGER run_charters_agent_execution_profile_update_valid;
DROP TRIGGER attempts_agent_execution_profile_valid;
DROP TRIGGER attempts_agent_execution_profile_update_valid;

CREATE TRIGGER run_charters_agent_execution_profile_valid
BEFORE INSERT ON run_charters
WHEN NOT COALESCE((
  (NEW.runtime_binding='' AND NEW.adapter_id<>'pi' AND NEW.agent_execution_profile='' AND NEW.agent_runtime_source='' AND NEW.agent_execution_provider='' AND NEW.agent_capability_policy='' AND NEW.agent_trust_disclosure_policy='')
  OR (NEW.runtime_binding='' AND NEW.adapter_id='pi' AND (
    (NEW.agent_execution_profile='minimal' AND NEW.agent_runtime_source='managed_pi_image' AND NEW.agent_execution_provider='docker' AND NEW.agent_capability_policy='chora.minimal.v1' AND NEW.agent_trust_disclosure_policy='')
    OR (NEW.agent_execution_profile='standard' AND NEW.agent_runtime_source='managed_pi_image' AND NEW.agent_execution_provider='docker' AND NEW.agent_capability_policy='chora.standard.v1' AND NEW.agent_trust_disclosure_policy='')
    OR (NEW.agent_execution_profile='isolated_local' AND NEW.agent_runtime_source='public_pi_image' AND NEW.agent_execution_provider='docker' AND NEW.agent_capability_policy='chora.isolated-local.v1' AND NEW.agent_trust_disclosure_policy='')
    OR (NEW.agent_execution_profile='trusted_local' AND NEW.agent_runtime_source='local_pi' AND NEW.agent_execution_provider='trusted_host' AND NEW.agent_capability_policy='pi.native' AND NEW.agent_trust_disclosure_policy='chora.trusted-local-disclosure.v1')
  ))
  OR (NEW.runtime_binding<>'' AND json_valid(NEW.runtime_binding)
    AND NEW.adapter_id NOT IN ('pi','codex','fake')
    AND json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.schemaVersion')='chora.runtime-binding.v1'
    AND json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.runtimeId')=NEW.adapter_id
    AND json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.executionProvider')='trusted_host'
    AND length(json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.sourceIdentity'))=64 AND json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.sourceIdentity') NOT GLOB '*[^0-9a-f]*'
    AND length(json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.configurationFingerprint'))=64 AND json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.configurationFingerprint') NOT GLOB '*[^0-9a-f]*'
    AND length(json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.securityFingerprint'))=64 AND json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.securityFingerprint') NOT GLOB '*[^0-9a-f]*'
    AND length(json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.capabilityDigest'))=64 AND json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.capabilityDigest') NOT GLOB '*[^0-9a-f]*'
    AND length(json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.protocolVersion'))>0
    AND json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.modelProvenance') IN ('native_default','explicit')
    AND NEW.agent_execution_profile='trusted_local' AND NEW.agent_runtime_source=NEW.adapter_id
    AND NEW.agent_execution_provider='trusted_host' AND NEW.agent_capability_policy='chora.runtime-native.v1'
    AND NEW.agent_trust_disclosure_policy='chora.trusted-local-disclosure.v1')
),0)
BEGIN SELECT RAISE(ABORT, 'invalid Run Charter Agent execution profile binding'); END;

CREATE TRIGGER run_charters_agent_execution_profile_update_valid
BEFORE UPDATE ON run_charters
WHEN NOT COALESCE((
  (NEW.runtime_binding='' AND NEW.adapter_id<>'pi' AND NEW.agent_execution_profile='' AND NEW.agent_runtime_source='' AND NEW.agent_execution_provider='' AND NEW.agent_capability_policy='' AND NEW.agent_trust_disclosure_policy='')
  OR (NEW.runtime_binding='' AND NEW.adapter_id='pi' AND (
    (NEW.agent_execution_profile='minimal' AND NEW.agent_runtime_source='managed_pi_image' AND NEW.agent_execution_provider='docker' AND NEW.agent_capability_policy='chora.minimal.v1' AND NEW.agent_trust_disclosure_policy='')
    OR (NEW.agent_execution_profile='standard' AND NEW.agent_runtime_source='managed_pi_image' AND NEW.agent_execution_provider='docker' AND NEW.agent_capability_policy='chora.standard.v1' AND NEW.agent_trust_disclosure_policy='')
    OR (NEW.agent_execution_profile='isolated_local' AND NEW.agent_runtime_source='public_pi_image' AND NEW.agent_execution_provider='docker' AND NEW.agent_capability_policy='chora.isolated-local.v1' AND NEW.agent_trust_disclosure_policy='')
    OR (NEW.agent_execution_profile='trusted_local' AND NEW.agent_runtime_source='local_pi' AND NEW.agent_execution_provider='trusted_host' AND NEW.agent_capability_policy='pi.native' AND NEW.agent_trust_disclosure_policy='chora.trusted-local-disclosure.v1')
  ))
  OR (NEW.runtime_binding<>'' AND json_valid(NEW.runtime_binding)
    AND NEW.adapter_id NOT IN ('pi','codex','fake')
    AND json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.schemaVersion')='chora.runtime-binding.v1'
    AND json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.runtimeId')=NEW.adapter_id
    AND json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.executionProvider')='trusted_host'
    AND length(json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.sourceIdentity'))=64 AND json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.sourceIdentity') NOT GLOB '*[^0-9a-f]*'
    AND length(json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.configurationFingerprint'))=64 AND json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.configurationFingerprint') NOT GLOB '*[^0-9a-f]*'
    AND length(json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.securityFingerprint'))=64 AND json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.securityFingerprint') NOT GLOB '*[^0-9a-f]*'
    AND length(json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.capabilityDigest'))=64 AND json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.capabilityDigest') NOT GLOB '*[^0-9a-f]*'
    AND length(json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.protocolVersion'))>0
    AND json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.modelProvenance') IN ('native_default','explicit')
    AND NEW.agent_execution_profile='trusted_local' AND NEW.agent_runtime_source=NEW.adapter_id
    AND NEW.agent_execution_provider='trusted_host' AND NEW.agent_capability_policy='chora.runtime-native.v1'
    AND NEW.agent_trust_disclosure_policy='chora.trusted-local-disclosure.v1')
),0)
BEGIN SELECT RAISE(ABORT, 'invalid Run Charter Agent execution profile binding'); END;

CREATE TRIGGER attempts_agent_execution_profile_valid
BEFORE INSERT ON attempts
WHEN NOT COALESCE((
  (NEW.runtime_binding='' AND NEW.adapter_id<>'pi' AND NEW.agent_execution_profile='' AND NEW.agent_runtime_source='' AND NEW.agent_execution_provider='' AND NEW.agent_capability_policy='' AND NEW.agent_trust_disclosure_policy='')
  OR (NEW.runtime_binding='' AND NEW.adapter_id='pi' AND (
    (NEW.agent_execution_profile='minimal' AND NEW.agent_runtime_source='managed_pi_image' AND NEW.agent_execution_provider='docker' AND NEW.agent_capability_policy='chora.minimal.v1' AND NEW.agent_trust_disclosure_policy='')
    OR (NEW.agent_execution_profile='standard' AND NEW.agent_runtime_source='managed_pi_image' AND NEW.agent_execution_provider='docker' AND NEW.agent_capability_policy='chora.standard.v1' AND NEW.agent_trust_disclosure_policy='')
    OR (NEW.agent_execution_profile='isolated_local' AND NEW.agent_runtime_source='public_pi_image' AND NEW.agent_execution_provider='docker' AND NEW.agent_capability_policy='chora.isolated-local.v1' AND NEW.agent_trust_disclosure_policy='')
    OR (NEW.agent_execution_profile='trusted_local' AND NEW.agent_runtime_source='local_pi' AND NEW.agent_execution_provider='trusted_host' AND NEW.agent_capability_policy='pi.native' AND NEW.agent_trust_disclosure_policy='chora.trusted-local-disclosure.v1')
  ))
  OR (NEW.runtime_binding<>'' AND json_valid(NEW.runtime_binding)
    AND NEW.adapter_id NOT IN ('pi','codex','fake')
    AND json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.schemaVersion')='chora.runtime-binding.v1'
    AND json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.runtimeId')=NEW.adapter_id
    AND json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.executionProvider')='trusted_host'
    AND length(json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.sourceIdentity'))=64 AND json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.sourceIdentity') NOT GLOB '*[^0-9a-f]*'
    AND length(json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.configurationFingerprint'))=64 AND json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.configurationFingerprint') NOT GLOB '*[^0-9a-f]*'
    AND length(json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.securityFingerprint'))=64 AND json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.securityFingerprint') NOT GLOB '*[^0-9a-f]*'
    AND length(json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.capabilityDigest'))=64 AND json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.capabilityDigest') NOT GLOB '*[^0-9a-f]*'
    AND length(json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.protocolVersion'))>0
    AND json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.modelProvenance') IN ('native_default','explicit')
    AND NEW.agent_execution_profile='trusted_local' AND NEW.agent_runtime_source=NEW.adapter_id
    AND NEW.agent_execution_provider='trusted_host' AND NEW.agent_capability_policy='chora.runtime-native.v1'
    AND NEW.agent_trust_disclosure_policy='chora.trusted-local-disclosure.v1')
),0)
BEGIN SELECT RAISE(ABORT, 'invalid Attempt Agent execution profile binding'); END;

CREATE TRIGGER attempts_agent_execution_profile_update_valid
BEFORE UPDATE ON attempts
WHEN NOT COALESCE((
  (NEW.runtime_binding='' AND NEW.adapter_id<>'pi' AND NEW.agent_execution_profile='' AND NEW.agent_runtime_source='' AND NEW.agent_execution_provider='' AND NEW.agent_capability_policy='' AND NEW.agent_trust_disclosure_policy='')
  OR (NEW.runtime_binding='' AND NEW.adapter_id='pi' AND (
    (NEW.agent_execution_profile='minimal' AND NEW.agent_runtime_source='managed_pi_image' AND NEW.agent_execution_provider='docker' AND NEW.agent_capability_policy='chora.minimal.v1' AND NEW.agent_trust_disclosure_policy='')
    OR (NEW.agent_execution_profile='standard' AND NEW.agent_runtime_source='managed_pi_image' AND NEW.agent_execution_provider='docker' AND NEW.agent_capability_policy='chora.standard.v1' AND NEW.agent_trust_disclosure_policy='')
    OR (NEW.agent_execution_profile='isolated_local' AND NEW.agent_runtime_source='public_pi_image' AND NEW.agent_execution_provider='docker' AND NEW.agent_capability_policy='chora.isolated-local.v1' AND NEW.agent_trust_disclosure_policy='')
    OR (NEW.agent_execution_profile='trusted_local' AND NEW.agent_runtime_source='local_pi' AND NEW.agent_execution_provider='trusted_host' AND NEW.agent_capability_policy='pi.native' AND NEW.agent_trust_disclosure_policy='chora.trusted-local-disclosure.v1')
  ))
  OR (NEW.runtime_binding<>'' AND json_valid(NEW.runtime_binding)
    AND NEW.adapter_id NOT IN ('pi','codex','fake')
    AND json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.schemaVersion')='chora.runtime-binding.v1'
    AND json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.runtimeId')=NEW.adapter_id
    AND json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.executionProvider')='trusted_host'
    AND length(json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.sourceIdentity'))=64 AND json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.sourceIdentity') NOT GLOB '*[^0-9a-f]*'
    AND length(json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.configurationFingerprint'))=64 AND json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.configurationFingerprint') NOT GLOB '*[^0-9a-f]*'
    AND length(json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.securityFingerprint'))=64 AND json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.securityFingerprint') NOT GLOB '*[^0-9a-f]*'
    AND length(json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.capabilityDigest'))=64 AND json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.capabilityDigest') NOT GLOB '*[^0-9a-f]*'
    AND length(json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.protocolVersion'))>0
    AND json_extract(CASE WHEN json_valid(NEW.runtime_binding) THEN NEW.runtime_binding ELSE '{}' END,'$.modelProvenance') IN ('native_default','explicit')
    AND NEW.agent_execution_profile='trusted_local' AND NEW.agent_runtime_source=NEW.adapter_id
    AND NEW.agent_execution_provider='trusted_host' AND NEW.agent_capability_policy='chora.runtime-native.v1'
    AND NEW.agent_trust_disclosure_policy='chora.trusted-local-disclosure.v1')
),0)
BEGIN SELECT RAISE(ABORT, 'invalid Attempt Agent execution profile binding'); END;
