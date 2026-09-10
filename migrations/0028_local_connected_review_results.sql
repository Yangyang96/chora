CREATE TABLE local_review_results (
  id TEXT PRIMARY KEY,
  run_id TEXT NOT NULL UNIQUE REFERENCES runs(id) ON DELETE RESTRICT,
  agent_attempt_id TEXT NOT NULL UNIQUE REFERENCES attempts(id) ON DELETE RESTRICT,
  agent_report_id TEXT NOT NULL UNIQUE REFERENCES agent_reports(id) ON DELETE RESTRICT,
  patch_artifact_id TEXT NOT NULL UNIQUE REFERENCES artifacts(id) ON DELETE RESTRICT,
  outcome TEXT NOT NULL CHECK(outcome = 'review_ready'),
  patch_digest BLOB NOT NULL CHECK(length(patch_digest) = 32),
  baseline_digest BLOB NOT NULL CHECK(length(baseline_digest) = 32),
  context_snapshot_digest BLOB NOT NULL CHECK(length(context_snapshot_digest) = 32),
  acceptance_contract_digest BLOB NOT NULL CHECK(length(acceptance_contract_digest) = 32),
  created_at TEXT NOT NULL
);

CREATE TRIGGER local_review_results_authority BEFORE INSERT ON local_review_results
WHEN NOT EXISTS (
  SELECT 1
  FROM runs run
  JOIN attempts attempt ON attempt.id = NEW.agent_attempt_id AND attempt.run_id = run.id
  JOIN agent_reports report ON report.id = NEW.agent_report_id AND report.attempt_id = attempt.id AND report.run_id = run.id
  JOIN artifacts patch ON patch.id = NEW.patch_artifact_id AND patch.attempt_id = attempt.id AND patch.run_id = run.id
  JOIN spec_coding_bindings contract ON contract.task_id = run.task_id
  WHERE run.id = NEW.run_id
    AND run.state = 'awaiting_review'
    AND attempt.state = 'output_submitted'
    AND patch.kind = 'patch' AND patch.role = 'output' AND patch.digest = NEW.patch_digest
    AND contract.status = 'registered'
    AND contract.snapshot_digest = NEW.context_snapshot_digest
    AND contract.active_contract_digest = NEW.acceptance_contract_digest
)
BEGIN
  SELECT RAISE(ABORT, 'local review Result authority mismatch');
END;

CREATE TRIGGER local_review_results_immutable_update BEFORE UPDATE ON local_review_results BEGIN
  SELECT RAISE(ABORT, 'local review Result is immutable');
END;
CREATE TRIGGER local_review_results_immutable_delete BEFORE DELETE ON local_review_results BEGIN
  SELECT RAISE(ABORT, 'local review Result is immutable');
END;

CREATE TABLE local_review_decisions (
  id TEXT PRIMARY KEY,
  run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE RESTRICT,
  result_id TEXT NOT NULL UNIQUE REFERENCES local_review_results(id) ON DELETE RESTRICT,
  agent_attempt_id TEXT NOT NULL REFERENCES attempts(id) ON DELETE RESTRICT,
  agent_report_id TEXT NOT NULL REFERENCES agent_reports(id) ON DELETE RESTRICT,
  patch_artifact_id TEXT NOT NULL REFERENCES artifacts(id) ON DELETE RESTRICT,
  expected_run_version INTEGER NOT NULL CHECK(expected_run_version > 0),
  kind TEXT NOT NULL CHECK(kind IN ('accept','reject')),
  reason TEXT NOT NULL CHECK(length(trim(reason)) > 0),
  rejection_class TEXT NOT NULL DEFAULT '' CHECK(
    (kind = 'accept' AND rejection_class = '') OR
    (kind = 'reject' AND rejection_class IN ('implementation_gap','planning_gap','contract_change_required'))
  ),
  actor_id TEXT NOT NULL CHECK(length(trim(actor_id)) > 0),
  session_id TEXT NOT NULL CHECK(length(trim(session_id)) > 0),
  patch_digest BLOB NOT NULL CHECK(length(patch_digest) = 32),
  baseline_digest BLOB NOT NULL CHECK(length(baseline_digest) = 32),
  declared_files_digest BLOB NOT NULL CHECK(length(declared_files_digest) = 32),
  decided_at TEXT NOT NULL,
  UNIQUE(run_id, expected_run_version)
);

CREATE TRIGGER local_review_decisions_authority BEFORE INSERT ON local_review_decisions
WHEN NOT EXISTS (
  SELECT 1
  FROM runs run
  JOIN local_review_results result ON result.id = NEW.result_id AND result.run_id = run.id
  JOIN agent_reports report ON report.id = NEW.agent_report_id AND report.id = result.agent_report_id
  JOIN attempts attempt ON attempt.id = NEW.agent_attempt_id AND attempt.id = result.agent_attempt_id
  JOIN artifacts patch ON patch.id = NEW.patch_artifact_id AND patch.id = result.patch_artifact_id
  WHERE run.id = NEW.run_id
    AND run.state = 'awaiting_review'
    AND run.version = NEW.expected_run_version
    AND result.outcome = 'review_ready'
    AND result.patch_digest = NEW.patch_digest
    AND result.baseline_digest = NEW.baseline_digest
    AND patch.digest = NEW.patch_digest
)
BEGIN
  SELECT RAISE(ABORT, 'local review decision authority mismatch');
END;

CREATE TRIGGER local_review_decisions_immutable_update BEFORE UPDATE ON local_review_decisions BEGIN
  SELECT RAISE(ABORT, 'local review decision is immutable');
END;
CREATE TRIGGER local_review_decisions_immutable_delete BEFORE DELETE ON local_review_decisions BEGIN
  SELECT RAISE(ABORT, 'local review decision is immutable');
END;

CREATE INDEX local_review_decisions_run_idx ON local_review_decisions(run_id, decided_at);
