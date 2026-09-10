PRAGMA defer_foreign_keys = ON;

DROP TRIGGER agent_reports_immutable_update;
DROP TRIGGER agent_reports_immutable_delete;

CREATE TABLE agent_reports_v14 (
  id TEXT PRIMARY KEY,
  run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE RESTRICT,
  attempt_id TEXT NOT NULL UNIQUE REFERENCES attempts(id) ON DELETE RESTRICT,
  summary TEXT NOT NULL,
  final_text TEXT NOT NULL,
  claimed_checks_json BLOB NOT NULL CHECK(json_valid(claimed_checks_json) AND json_type(claimed_checks_json) = 'array'),
  completed_at TEXT NOT NULL
);

INSERT INTO agent_reports_v14(id,run_id,attempt_id,summary,final_text,claimed_checks_json,completed_at)
SELECT id,run_id,attempt_id,summary,final_text,claimed_checks_json,completed_at
FROM agent_reports;

DROP TABLE agent_reports;
ALTER TABLE agent_reports_v14 RENAME TO agent_reports;
CREATE INDEX agent_reports_run_idx ON agent_reports(run_id, completed_at);

CREATE TRIGGER agent_reports_immutable_update BEFORE UPDATE ON agent_reports BEGIN
  SELECT RAISE(ABORT, 'agent report is immutable');
END;
CREATE TRIGGER agent_reports_immutable_delete BEFORE DELETE ON agent_reports BEGIN
  SELECT RAISE(ABORT, 'agent report is immutable');
END;

DROP TRIGGER verification_runs_identity_immutable;
DROP TRIGGER verification_runs_state_transition;
DROP TRIGGER verification_runs_immutable_delete;
DROP TRIGGER verification_cancel_requests_running_insert;
DROP TRIGGER verification_results_commit_proof;
DROP TRIGGER verified_review_decisions_authority;

CREATE TABLE verification_runs_v14 (
  id TEXT PRIMARY KEY,
  run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE RESTRICT,
  agent_attempt_id TEXT NOT NULL UNIQUE REFERENCES attempts(id) ON DELETE RESTRICT,
  baseline_digest BLOB NOT NULL CHECK(length(baseline_digest) = 32),
  patch_digest BLOB NOT NULL CHECK(length(patch_digest) = 32),
  context_snapshot_digest BLOB NOT NULL CHECK(length(context_snapshot_digest) = 32),
  acceptance_contract_digest BLOB NOT NULL CHECK(length(acceptance_contract_digest) = 32),
  verifier_policy_version TEXT NOT NULL CHECK(length(trim(verifier_policy_version)) > 0),
  verifier_policy_digest BLOB NOT NULL CHECK(length(verifier_policy_digest) = 32),
  state TEXT NOT NULL CHECK(state IN ('awaiting','verifying','completed','recovery_required')),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  FOREIGN KEY(run_id,agent_attempt_id) REFERENCES attempts(run_id,id) ON DELETE RESTRICT
);

INSERT INTO verification_runs_v14(
  id,run_id,agent_attempt_id,baseline_digest,patch_digest,context_snapshot_digest,
  acceptance_contract_digest,verifier_policy_version,verifier_policy_digest,
  state,created_at,updated_at
)
SELECT run.id,run.run_id,report.attempt_id,run.baseline_digest,run.patch_digest,
       run.context_snapshot_digest,run.acceptance_contract_digest,
       run.verifier_policy_version,run.verifier_policy_digest,
       run.state,run.created_at,run.updated_at
FROM verification_runs run
JOIN agent_reports report ON report.run_id=run.run_id;

DROP TABLE verification_runs;
ALTER TABLE verification_runs_v14 RENAME TO verification_runs;
CREATE INDEX verification_runs_run_idx ON verification_runs(run_id, created_at);

CREATE TRIGGER verification_runs_identity_immutable BEFORE UPDATE ON verification_runs
WHEN OLD.id <> NEW.id OR OLD.run_id <> NEW.run_id OR OLD.agent_attempt_id <> NEW.agent_attempt_id OR
     OLD.baseline_digest <> NEW.baseline_digest OR OLD.patch_digest <> NEW.patch_digest OR
     OLD.context_snapshot_digest <> NEW.context_snapshot_digest OR
     OLD.acceptance_contract_digest <> NEW.acceptance_contract_digest OR
     OLD.verifier_policy_version <> NEW.verifier_policy_version OR
     OLD.verifier_policy_digest <> NEW.verifier_policy_digest OR OLD.created_at <> NEW.created_at
BEGIN
  SELECT RAISE(ABORT, 'verification run authority is immutable');
END;
CREATE TRIGGER verification_runs_state_transition BEFORE UPDATE ON verification_runs
WHEN NOT (
  (OLD.state = 'awaiting' AND NEW.state = 'verifying') OR
  (OLD.state = 'verifying' AND NEW.state IN ('awaiting','completed','recovery_required')) OR
  (OLD.state = 'recovery_required' AND NEW.state = 'verifying')
)
BEGIN
  SELECT RAISE(ABORT, 'invalid verification run transition');
END;
CREATE TRIGGER verification_runs_immutable_delete BEFORE DELETE ON verification_runs BEGIN
  SELECT RAISE(ABORT, 'verification run is immutable');
END;

CREATE TRIGGER verification_cancel_requests_running_insert BEFORE INSERT ON verification_cancel_requests
WHEN NOT EXISTS (
  SELECT 1 FROM verification_attempts a
  JOIN verification_runs r ON r.id = a.verification_run_id
  WHERE a.id = NEW.verification_attempt_id AND a.state = 'running' AND r.state = 'verifying'
)
BEGIN
  SELECT RAISE(ABORT, 'verification cancel request is not for a running Attempt');
END;

CREATE TRIGGER verification_results_commit_proof BEFORE INSERT ON verification_results
WHEN NOT EXISTS (
  SELECT 1
  FROM verification_attempts a
  JOIN verification_runs r ON r.id = a.verification_run_id
  WHERE a.id = NEW.verification_attempt_id
    AND r.id = NEW.verification_run_id
    AND a.state = 'completed' AND a.evidence_complete = 1 AND a.cleanup_proven = 1
    AND a.workspace_identity <> ''
    AND a.baseline_digest = NEW.baseline_digest AND r.baseline_digest = NEW.baseline_digest
    AND a.patch_digest = NEW.patch_digest AND r.patch_digest = NEW.patch_digest
    AND a.context_snapshot_digest = NEW.context_snapshot_digest AND r.context_snapshot_digest = NEW.context_snapshot_digest
    AND a.acceptance_contract_digest = NEW.acceptance_contract_digest AND r.acceptance_contract_digest = NEW.acceptance_contract_digest
    AND a.verifier_policy_version = NEW.verifier_policy_version AND r.verifier_policy_version = NEW.verifier_policy_version
    AND a.verifier_policy_digest = NEW.verifier_policy_digest AND r.verifier_policy_digest = NEW.verifier_policy_digest
    AND EXISTS (SELECT 1 FROM verification_command_evidence e WHERE e.verification_attempt_id = a.id)
    AND NOT EXISTS (
      SELECT 1 FROM verification_command_evidence e
      WHERE e.verification_attempt_id = a.id AND e.workspace_identity <> a.workspace_identity
    )
    AND EXISTS (SELECT 1 FROM verification_acceptance_checks c WHERE c.verification_attempt_id = a.id)
    AND (
      (NEW.outcome = 'review_ready' AND NOT EXISTS (
        SELECT 1 FROM verification_acceptance_checks c
        WHERE c.verification_attempt_id = a.id AND c.status <> 'passed'
      )) OR
      (NEW.outcome = 'needs_revision' AND EXISTS (
        SELECT 1 FROM verification_acceptance_checks c
        WHERE c.verification_attempt_id = a.id AND c.status IN ('failed','unknown')
      ))
    )
)
BEGIN
  SELECT RAISE(ABORT, 'verification result lacks complete trusted evidence and cleanup proof');
END;

CREATE TRIGGER verified_review_decisions_authority BEFORE INSERT ON verified_review_decisions
WHEN NOT EXISTS (
  SELECT 1
  FROM runs r
  JOIN attempts agent_attempt ON agent_attempt.run_id = r.id
  JOIN artifacts patch ON patch.run_id = r.id AND patch.attempt_id = agent_attempt.id
  JOIN verification_runs verification_run ON verification_run.run_id = r.id AND verification_run.agent_attempt_id = agent_attempt.id
  JOIN verification_results result ON result.verification_run_id = verification_run.id
  JOIN verification_attempts verification_attempt
    ON verification_attempt.id = result.verification_attempt_id
  WHERE r.id = NEW.run_id
    AND r.state = 'awaiting_review'
    AND r.version = NEW.expected_run_version
    AND agent_attempt.id = NEW.agent_attempt_id
    AND agent_attempt.state = 'output_submitted'
    AND patch.id = NEW.patch_artifact_id
    AND patch.kind = 'patch' AND patch.role = 'output'
    AND patch.digest = NEW.patch_digest
    AND result.id = NEW.result_id
    AND result.outcome = 'review_ready'
    AND result.verification_attempt_id = NEW.verification_attempt_id
    AND result.patch_digest = NEW.patch_digest
    AND result.baseline_digest = NEW.baseline_digest
    AND verification_attempt.state = 'completed'
    AND verification_attempt.evidence_complete = 1
    AND verification_attempt.cleanup_proven = 1
    AND verification_attempt.workspace_identity <> ''
    AND NOT EXISTS (
      SELECT 1 FROM verification_acceptance_checks check_record
      WHERE check_record.verification_attempt_id = verification_attempt.id
        AND (check_record.status <> 'passed' OR check_record.trust <> 'trusted')
    )
)
BEGIN
  SELECT RAISE(ABORT, 'verified review authority mismatch');
END;
