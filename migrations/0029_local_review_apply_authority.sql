-- Preserve M1 records while admitting digest-bound Local Connected reviews.
-- Review rows are immutable; the union triggers replace the single-kind FK.

CREATE TABLE patch_applications_v29 (
  run_id TEXT PRIMARY KEY REFERENCES runs(id) ON DELETE RESTRICT,
  review_decision_id TEXT NOT NULL UNIQUE,
  patch_digest BLOB NOT NULL CHECK(length(patch_digest)=32),
  declared_files_digest BLOB NOT NULL CHECK(length(declared_files_digest)=32),
  target_identity TEXT NOT NULL CHECK(length(trim(target_identity)) > 0),
  base_revision TEXT NOT NULL CHECK(length(trim(base_revision)) > 0),
  affected_paths_json BLOB NOT NULL CHECK(length(affected_paths_json) > 2),
  pre_state_digest BLOB NOT NULL CHECK(length(pre_state_digest)=32),
  post_state_digest BLOB CHECK(post_state_digest IS NULL OR length(post_state_digest)=32),
  state TEXT NOT NULL CHECK(state IN ('applying','applied','conflict','recovery_required')),
  version INTEGER NOT NULL CHECK(version > 0),
  reason TEXT NOT NULL DEFAULT '',
  started_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  applied_at TEXT,
  CHECK(
    (state='applying' AND post_state_digest IS NULL AND reason='' AND applied_at IS NULL) OR
    (state='applied' AND post_state_digest IS NOT NULL AND reason='' AND applied_at IS NOT NULL) OR
    (state IN ('conflict','recovery_required') AND post_state_digest IS NULL AND length(trim(reason)) > 0 AND applied_at IS NULL)
  )
);

INSERT INTO patch_applications_v29 SELECT * FROM patch_applications;
DROP TABLE patch_applications;
ALTER TABLE patch_applications_v29 RENAME TO patch_applications;

CREATE TRIGGER patch_applications_authority BEFORE INSERT ON patch_applications
WHEN NOT EXISTS (
  SELECT 1
  FROM runs run
  JOIN (SELECT id,run_id,kind,patch_digest,declared_files_digest FROM verified_review_decisions
    UNION ALL SELECT id,run_id,kind,patch_digest,declared_files_digest FROM local_review_decisions) review ON review.run_id=run.id
  WHERE run.id=NEW.run_id
    AND run.state='accepted'
    AND review.id=NEW.review_decision_id
    AND review.kind='accept'
    AND review.patch_digest=NEW.patch_digest
    AND review.declared_files_digest=NEW.declared_files_digest
)
BEGIN
  SELECT RAISE(ABORT, 'patch application authority mismatch');
END;

CREATE TRIGGER patch_applications_binding_immutable BEFORE UPDATE ON patch_applications
WHEN OLD.run_id<>NEW.run_id
  OR OLD.review_decision_id<>NEW.review_decision_id
  OR OLD.patch_digest<>NEW.patch_digest
  OR OLD.declared_files_digest<>NEW.declared_files_digest
  OR OLD.affected_paths_json<>NEW.affected_paths_json
BEGIN
  SELECT RAISE(ABORT, 'patch application binding is immutable');
END;

CREATE TRIGGER patch_applications_transition BEFORE UPDATE ON patch_applications
WHEN NEW.version<>OLD.version+1 OR NOT (
  (OLD.state='applying' AND NEW.state IN ('applied','conflict','recovery_required')) OR
  (OLD.state IN ('conflict','recovery_required') AND NEW.state='applying')
)
BEGIN
  SELECT RAISE(ABORT, 'invalid patch application transition');
END;

CREATE TRIGGER patch_applications_immutable_delete BEFORE DELETE ON patch_applications
BEGIN
  SELECT RAISE(ABORT, 'patch application is immutable');
END;

CREATE INDEX patch_applications_state_idx ON patch_applications(state,updated_at);

CREATE TABLE patch_inspection_failures_v29 (
  run_id TEXT PRIMARY KEY REFERENCES runs(id) ON DELETE RESTRICT,
  review_decision_id TEXT NOT NULL UNIQUE,
  patch_digest BLOB NOT NULL CHECK(length(patch_digest)=32),
  declared_files_digest BLOB NOT NULL CHECK(length(declared_files_digest)=32),
  target_identity TEXT NOT NULL CHECK(length(trim(target_identity)) > 0),
  affected_paths_json BLOB NOT NULL CHECK(length(affected_paths_json) > 2),
  state TEXT NOT NULL CHECK(state IN ('conflict','recovery_required')),
  version INTEGER NOT NULL CHECK(version > 0),
  reason TEXT NOT NULL CHECK(length(trim(reason)) > 0),
  started_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

INSERT INTO patch_inspection_failures_v29 SELECT * FROM patch_inspection_failures;
DROP TABLE patch_inspection_failures;
ALTER TABLE patch_inspection_failures_v29 RENAME TO patch_inspection_failures;

CREATE TRIGGER patch_inspection_failures_authority BEFORE INSERT ON patch_inspection_failures
WHEN NOT EXISTS (
  SELECT 1 FROM runs run
  JOIN (SELECT id,run_id,kind,patch_digest,declared_files_digest FROM verified_review_decisions
    UNION ALL SELECT id,run_id,kind,patch_digest,declared_files_digest FROM local_review_decisions) review ON review.run_id=run.id
  WHERE run.id=NEW.run_id AND run.state='accepted' AND review.id=NEW.review_decision_id
    AND review.kind='accept' AND review.patch_digest=NEW.patch_digest
    AND review.declared_files_digest=NEW.declared_files_digest
)
BEGIN
  SELECT RAISE(ABORT, 'patch inspection failure authority mismatch');
END;

CREATE TRIGGER patch_inspection_failures_binding_immutable BEFORE UPDATE ON patch_inspection_failures
WHEN OLD.run_id<>NEW.run_id OR OLD.review_decision_id<>NEW.review_decision_id
  OR OLD.patch_digest<>NEW.patch_digest OR OLD.declared_files_digest<>NEW.declared_files_digest
  OR OLD.target_identity<>NEW.target_identity OR OLD.affected_paths_json<>NEW.affected_paths_json
  OR NEW.version<>OLD.version+1
BEGIN
  SELECT RAISE(ABORT, 'patch inspection failure binding is immutable');
END;

CREATE TRIGGER patch_inspection_failures_immutable_delete BEFORE DELETE ON patch_inspection_failures
BEGIN
  SELECT RAISE(ABORT, 'patch inspection failure is immutable');
END;

CREATE TABLE verified_review_routes_v29 (
  decision_id TEXT PRIMARY KEY,
  source_run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE RESTRICT,
  source_task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE RESTRICT,
  rejection_class TEXT NOT NULL CHECK(rejection_class IN ('planning_gap','contract_change_required')),
  planning_draft_id TEXT REFERENCES technical_plan_drafts(id) ON DELETE RESTRICT,
  related_task_id TEXT REFERENCES tasks(id) ON DELETE RESTRICT,
  created_at TEXT NOT NULL,
  CHECK(
    (rejection_class = 'planning_gap' AND planning_draft_id IS NOT NULL AND related_task_id IS NULL) OR
    (rejection_class = 'contract_change_required' AND planning_draft_id IS NULL AND related_task_id IS NOT NULL)
  )
);

INSERT INTO verified_review_routes_v29 SELECT * FROM verified_review_routes;
DROP TABLE verified_review_routes;
ALTER TABLE verified_review_routes_v29 RENAME TO verified_review_routes;

CREATE TRIGGER verified_review_route_exact BEFORE INSERT ON verified_review_routes
WHEN NOT EXISTS (
  SELECT 1 FROM (SELECT id,run_id,kind,rejection_class FROM verified_review_decisions
    UNION ALL SELECT id,run_id,kind,rejection_class FROM local_review_decisions) decision
  JOIN runs run ON run.id = NEW.source_run_id AND run.task_id = NEW.source_task_id
  WHERE decision.id = NEW.decision_id AND decision.run_id = NEW.source_run_id
    AND decision.kind = 'reject' AND decision.rejection_class = NEW.rejection_class
) OR (
  NEW.rejection_class = 'planning_gap' AND NOT EXISTS (
    SELECT 1 FROM technical_plan_drafts draft
    JOIN technical_plan_run_bindings binding ON binding.run_id = NEW.source_run_id
    WHERE draft.id = NEW.planning_draft_id AND draft.task_id = NEW.source_task_id
      AND draft.predecessor_revision_id = binding.revision_id AND draft.closed_at IS NULL
  )
) OR (
  NEW.rejection_class = 'contract_change_required' AND NOT EXISTS (
    SELECT 1 FROM tasks related
    WHERE related.id = NEW.related_task_id AND related.predecessor_task_id = NEW.source_task_id
  )
)
BEGIN
  SELECT RAISE(ABORT, 'verified review route mismatch');
END;

CREATE TRIGGER verified_review_routes_immutable_update
BEFORE UPDATE ON verified_review_routes BEGIN
  SELECT RAISE(ABORT, 'verified review route is immutable');
END;

CREATE TRIGGER verified_review_routes_immutable_delete
BEFORE DELETE ON verified_review_routes BEGIN
  SELECT RAISE(ABORT, 'verified review route is immutable');
END;

-- Each successor Attempt owns its own Result; a Run may have several.
DROP TRIGGER local_review_decisions_authority;
CREATE TABLE local_review_results_v29 (
  id TEXT PRIMARY KEY,
  run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE RESTRICT,
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
INSERT INTO local_review_results_v29 SELECT * FROM local_review_results;
DROP TABLE local_review_results;
ALTER TABLE local_review_results_v29 RENAME TO local_review_results;
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

