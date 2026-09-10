CREATE TABLE agent_reports (
  id TEXT PRIMARY KEY,
  run_id TEXT NOT NULL UNIQUE REFERENCES runs(id) ON DELETE RESTRICT,
  attempt_id TEXT NOT NULL UNIQUE REFERENCES attempts(id) ON DELETE RESTRICT,
  summary TEXT NOT NULL,
  final_text TEXT NOT NULL,
  claimed_checks_json BLOB NOT NULL CHECK(json_valid(claimed_checks_json) AND json_type(claimed_checks_json) = 'array'),
  completed_at TEXT NOT NULL
);

CREATE TABLE verification_runs (
  id TEXT PRIMARY KEY,
  run_id TEXT NOT NULL UNIQUE REFERENCES runs(id) ON DELETE RESTRICT,
  baseline_digest BLOB NOT NULL CHECK(length(baseline_digest) = 32),
  patch_digest BLOB NOT NULL CHECK(length(patch_digest) = 32),
  context_snapshot_digest BLOB NOT NULL CHECK(length(context_snapshot_digest) = 32),
  acceptance_contract_digest BLOB NOT NULL CHECK(length(acceptance_contract_digest) = 32),
  verifier_policy_version TEXT NOT NULL CHECK(length(trim(verifier_policy_version)) > 0),
  verifier_policy_digest BLOB NOT NULL CHECK(length(verifier_policy_digest) = 32),
  state TEXT NOT NULL CHECK(state IN ('awaiting','verifying','completed','recovery_required')),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE verification_attempts (
  id TEXT PRIMARY KEY,
  verification_run_id TEXT NOT NULL REFERENCES verification_runs(id) ON DELETE RESTRICT,
  sequence INTEGER NOT NULL CHECK(sequence >= 1),
  predecessor_id TEXT UNIQUE REFERENCES verification_attempts(id) ON DELETE RESTRICT,
  baseline_digest BLOB NOT NULL CHECK(length(baseline_digest) = 32),
  patch_digest BLOB NOT NULL CHECK(length(patch_digest) = 32),
  context_snapshot_digest BLOB NOT NULL CHECK(length(context_snapshot_digest) = 32),
  acceptance_contract_digest BLOB NOT NULL CHECK(length(acceptance_contract_digest) = 32),
  verifier_policy_version TEXT NOT NULL CHECK(length(trim(verifier_policy_version)) > 0),
  verifier_policy_digest BLOB NOT NULL CHECK(length(verifier_policy_digest) = 32),
  state TEXT NOT NULL CHECK(state IN ('pending','running','completed','cancelled','recovery_required')),
  evidence_complete INTEGER NOT NULL DEFAULT 0 CHECK(evidence_complete IN (0,1)),
  cleanup_proven INTEGER NOT NULL DEFAULT 0 CHECK(cleanup_proven IN (0,1)),
  workspace_identity TEXT NOT NULL DEFAULT '' CHECK(workspace_identity = '' OR (length(workspace_identity) = 71 AND substr(workspace_identity,1,7) = 'sha256:')),
  reason TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(verification_run_id, sequence),
  CHECK((sequence = 1 AND predecessor_id IS NULL) OR (sequence > 1 AND predecessor_id IS NOT NULL)),
  CHECK(
    (state IN ('pending','running') AND evidence_complete = 0 AND cleanup_proven = 0 AND workspace_identity = '') OR
    (state = 'completed' AND evidence_complete = 1 AND cleanup_proven = 1 AND workspace_identity <> '') OR
    (state = 'cancelled' AND evidence_complete = 0 AND cleanup_proven = 1 AND workspace_identity <> '') OR
    (state = 'recovery_required' AND evidence_complete = 0 AND cleanup_proven = 0)
  )
);

CREATE TABLE verification_cancel_requests (
  verification_attempt_id TEXT PRIMARY KEY REFERENCES verification_attempts(id) ON DELETE RESTRICT,
  requested_at TEXT NOT NULL
);

CREATE TABLE verification_command_evidence (
  id TEXT PRIMARY KEY,
  verification_attempt_id TEXT NOT NULL REFERENCES verification_attempts(id) ON DELETE RESTRICT,
  command_id TEXT NOT NULL CHECK(length(trim(command_id)) > 0),
  criterion_ids_json BLOB NOT NULL CHECK(json_valid(criterion_ids_json) AND json_type(criterion_ids_json) = 'array'),
  argv_json BLOB NOT NULL CHECK(json_valid(argv_json) AND json_type(argv_json) = 'array'),
  started_at TEXT NOT NULL,
  ended_at TEXT NOT NULL,
  classification TEXT NOT NULL CHECK(classification IN ('exited','timed_out','cancelled','launch_failed','unavailable')),
  exit_code INTEGER,
  verifier_identity TEXT NOT NULL CHECK(length(trim(verifier_identity)) > 0),
  workspace_identity TEXT NOT NULL CHECK(length(workspace_identity) = 71 AND substr(workspace_identity,1,7) = 'sha256:'),
  UNIQUE(verification_attempt_id, command_id),
  UNIQUE(id, verification_attempt_id),
  CHECK((classification = 'exited' AND exit_code IS NOT NULL) OR (classification <> 'exited' AND exit_code IS NULL))
);

CREATE TABLE verification_stream_logs (
  verification_command_evidence_id TEXT NOT NULL REFERENCES verification_command_evidence(id) ON DELETE RESTRICT,
  stream TEXT NOT NULL CHECK(stream IN ('stdout','stderr')),
  full_sha256 BLOB NOT NULL CHECK(length(full_sha256) = 32),
  total_bytes INTEGER NOT NULL CHECK(total_bytes >= 0),
  retained_body TEXT NOT NULL,
  truncated INTEGER NOT NULL CHECK(truncated IN (0,1)),
  truncation_boundary INTEGER NOT NULL CHECK(truncation_boundary >= 0),
  redaction_policy_version TEXT NOT NULL CHECK(length(trim(redaction_policy_version)) > 0),
  PRIMARY KEY(verification_command_evidence_id, stream),
  CHECK(
    (truncated = 0 AND truncation_boundary = 0) OR
    (truncated = 1 AND truncation_boundary > 0 AND total_bytes > truncation_boundary)
  )
);

CREATE TABLE verification_acceptance_checks (
  id TEXT PRIMARY KEY,
  verification_attempt_id TEXT NOT NULL REFERENCES verification_attempts(id) ON DELETE RESTRICT,
  criterion_id TEXT NOT NULL REFERENCES task_criteria(criterion_id) ON DELETE RESTRICT,
  status TEXT NOT NULL CHECK(status IN ('passed','failed','unknown')),
  trust TEXT NOT NULL CHECK(trust = 'trusted'),
  UNIQUE(verification_attempt_id, criterion_id),
  UNIQUE(id, verification_attempt_id)
);

CREATE TABLE verification_check_evidence (
  check_id TEXT NOT NULL,
  verification_attempt_id TEXT NOT NULL,
  position INTEGER NOT NULL CHECK(position >= 0),
  verification_command_evidence_id TEXT NOT NULL,
  PRIMARY KEY(check_id, position),
  UNIQUE(check_id, verification_command_evidence_id),
  FOREIGN KEY(check_id, verification_attempt_id)
    REFERENCES verification_acceptance_checks(id, verification_attempt_id) ON DELETE RESTRICT,
  FOREIGN KEY(verification_command_evidence_id, verification_attempt_id)
    REFERENCES verification_command_evidence(id, verification_attempt_id) ON DELETE RESTRICT
);

CREATE TABLE verification_results (
  id TEXT PRIMARY KEY,
  verification_run_id TEXT NOT NULL UNIQUE REFERENCES verification_runs(id) ON DELETE RESTRICT,
  verification_attempt_id TEXT NOT NULL UNIQUE REFERENCES verification_attempts(id) ON DELETE RESTRICT,
  outcome TEXT NOT NULL CHECK(outcome IN ('review_ready','needs_revision')),
  baseline_digest BLOB NOT NULL CHECK(length(baseline_digest) = 32),
  patch_digest BLOB NOT NULL CHECK(length(patch_digest) = 32),
  context_snapshot_digest BLOB NOT NULL CHECK(length(context_snapshot_digest) = 32),
  acceptance_contract_digest BLOB NOT NULL CHECK(length(acceptance_contract_digest) = 32),
  verifier_policy_version TEXT NOT NULL CHECK(length(trim(verifier_policy_version)) > 0),
  verifier_policy_digest BLOB NOT NULL CHECK(length(verifier_policy_digest) = 32),
  created_at TEXT NOT NULL
);

CREATE INDEX verification_attempts_active_idx
  ON verification_attempts(state, updated_at)
  WHERE state = 'running';
CREATE INDEX verification_evidence_attempt_idx
  ON verification_command_evidence(verification_attempt_id, started_at);

CREATE TRIGGER agent_reports_immutable_update BEFORE UPDATE ON agent_reports BEGIN
  SELECT RAISE(ABORT, 'agent report is immutable');
END;
CREATE TRIGGER agent_reports_immutable_delete BEFORE DELETE ON agent_reports BEGIN
  SELECT RAISE(ABORT, 'agent report is immutable');
END;

CREATE TRIGGER verification_runs_identity_immutable BEFORE UPDATE ON verification_runs
WHEN OLD.id <> NEW.id OR OLD.run_id <> NEW.run_id OR
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

CREATE TRIGGER verification_attempts_identity_immutable BEFORE UPDATE ON verification_attempts
WHEN OLD.id <> NEW.id OR OLD.verification_run_id <> NEW.verification_run_id OR
     OLD.sequence <> NEW.sequence OR COALESCE(OLD.predecessor_id,'') <> COALESCE(NEW.predecessor_id,'') OR
     OLD.baseline_digest <> NEW.baseline_digest OR OLD.patch_digest <> NEW.patch_digest OR
     OLD.context_snapshot_digest <> NEW.context_snapshot_digest OR
     OLD.acceptance_contract_digest <> NEW.acceptance_contract_digest OR
     OLD.verifier_policy_version <> NEW.verifier_policy_version OR
     OLD.verifier_policy_digest <> NEW.verifier_policy_digest OR OLD.created_at <> NEW.created_at
BEGIN
  SELECT RAISE(ABORT, 'verification attempt authority is immutable');
END;
CREATE TRIGGER verification_attempts_state_transition BEFORE UPDATE ON verification_attempts
WHEN NOT (OLD.state = 'pending' AND NEW.state = 'running') AND NOT (
  OLD.state = 'running' AND NEW.state IN ('completed','cancelled','recovery_required')
)
BEGIN
  SELECT RAISE(ABORT, 'invalid verification attempt transition');
END;
CREATE TRIGGER verification_attempts_workspace_identity_transition BEFORE UPDATE ON verification_attempts
WHEN OLD.workspace_identity <> NEW.workspace_identity AND NOT (
  OLD.state = 'running' AND NEW.state IN ('completed','cancelled','recovery_required') AND
  OLD.workspace_identity = '' AND NEW.workspace_identity <> ''
)
BEGIN
  SELECT RAISE(ABORT, 'invalid verification workspace identity transition');
END;
CREATE TRIGGER verification_attempts_immutable_delete BEFORE DELETE ON verification_attempts BEGIN
  SELECT RAISE(ABORT, 'verification attempt is immutable');
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
CREATE TRIGGER verification_cancel_requests_immutable_update BEFORE UPDATE ON verification_cancel_requests BEGIN
  SELECT RAISE(ABORT, 'verification cancel request is immutable');
END;
CREATE TRIGGER verification_cancel_requests_immutable_delete BEFORE DELETE ON verification_cancel_requests BEGIN
  SELECT RAISE(ABORT, 'verification cancel request is immutable');
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

CREATE TRIGGER verification_command_evidence_immutable_update BEFORE UPDATE ON verification_command_evidence BEGIN
  SELECT RAISE(ABORT, 'verification command evidence is immutable');
END;
CREATE TRIGGER verification_command_evidence_immutable_delete BEFORE DELETE ON verification_command_evidence BEGIN
  SELECT RAISE(ABORT, 'verification command evidence is immutable');
END;
CREATE TRIGGER verification_stream_logs_immutable_update BEFORE UPDATE ON verification_stream_logs BEGIN
  SELECT RAISE(ABORT, 'verification stream log is immutable');
END;
CREATE TRIGGER verification_stream_logs_immutable_delete BEFORE DELETE ON verification_stream_logs BEGIN
  SELECT RAISE(ABORT, 'verification stream log is immutable');
END;
CREATE TRIGGER verification_acceptance_checks_immutable_update BEFORE UPDATE ON verification_acceptance_checks BEGIN
  SELECT RAISE(ABORT, 'verification acceptance check is immutable');
END;
CREATE TRIGGER verification_acceptance_checks_immutable_delete BEFORE DELETE ON verification_acceptance_checks BEGIN
  SELECT RAISE(ABORT, 'verification acceptance check is immutable');
END;
CREATE TRIGGER verification_check_evidence_immutable_update BEFORE UPDATE ON verification_check_evidence BEGIN
  SELECT RAISE(ABORT, 'verification check evidence link is immutable');
END;
CREATE TRIGGER verification_check_evidence_immutable_delete BEFORE DELETE ON verification_check_evidence BEGIN
  SELECT RAISE(ABORT, 'verification check evidence link is immutable');
END;
CREATE TRIGGER verification_results_immutable_update BEFORE UPDATE ON verification_results BEGIN
  SELECT RAISE(ABORT, 'verification result is immutable');
END;
CREATE TRIGGER verification_results_immutable_delete BEFORE DELETE ON verification_results BEGIN
  SELECT RAISE(ABORT, 'verification result is immutable');
END;
