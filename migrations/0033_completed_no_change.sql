-- Preserve every Run identity and dependent authority while adding successful no-change completion.
PRAGMA defer_foreign_keys = ON;
DROP TRIGGER technical_plan_run_binding_exact;
DROP TRIGGER verified_review_decisions_authority;
DROP TRIGGER patch_applications_authority;
DROP TRIGGER patch_inspection_failures_authority;
DROP TRIGGER verified_review_route_exact;
DROP TRIGGER local_review_results_authority;
DROP TRIGGER local_review_decisions_authority;

CREATE TABLE runs_v33 (
  id TEXT PRIMARY KEY,
  task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE RESTRICT,
  charter_id TEXT NOT NULL REFERENCES run_charters(id) ON DELETE RESTRICT,
  state TEXT NOT NULL CHECK(state IN (
    'draft','ready','running','stopping','awaiting_verification','verifying',
    'awaiting_review','revision_required','recovery_required',
    'verification_recovery_required','accepted','cancelled','completed'
  )),
  version INTEGER NOT NULL CHECK(version >= 0),
  current_attempt_number INTEGER NOT NULL CHECK(current_attempt_number >= 0),
  last_event_sequence INTEGER NOT NULL DEFAULT 0 CHECK(last_event_sequence >= 0),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  started_at TEXT,
  review_requested_at TEXT,
  terminal_at TEXT
);
INSERT INTO runs_v33 SELECT * FROM runs;
DROP TABLE runs;
ALTER TABLE runs_v33 RENAME TO runs;
CREATE INDEX runs_task_idx ON runs(task_id);
CREATE INDEX runs_charter_idx ON runs(charter_id);
CREATE UNIQUE INDEX one_active_run_per_task ON runs(task_id) WHERE state NOT IN ('accepted', 'cancelled', 'completed');
CREATE INDEX runs_recovery_idx ON runs(state, updated_at) WHERE state IN ('running','stopping','recovery_required','verifying','verification_recovery_required');
CREATE INDEX runs_task_latest_idx
  ON runs(task_id, created_at DESC, id);
CREATE TRIGGER technical_plan_run_binding_exact
BEFORE INSERT ON technical_plan_run_bindings
WHEN NOT EXISTS (
    SELECT 1 FROM runs run
    JOIN run_charters charter ON charter.id = NEW.charter_id
    JOIN technical_plan_acceptance_bindings accepted ON accepted.task_id = NEW.task_id AND accepted.revision_id = NEW.revision_id
    WHERE run.id = NEW.run_id AND run.task_id = NEW.task_id AND run.charter_id = NEW.charter_id
      AND charter.task_id = NEW.task_id AND accepted.snapshot_id = NEW.snapshot_id
      AND accepted.snapshot_digest = NEW.snapshot_digest
)
BEGIN SELECT RAISE(ABORT, 'technical plan run binding mismatch'); END;
CREATE TRIGGER verified_review_decisions_authority BEFORE INSERT ON verified_review_decisions
WHEN NOT EXISTS (
  SELECT 1
  FROM runs r
  JOIN attempts agent_attempt ON agent_attempt.run_id = r.id
  JOIN artifacts patch ON patch.run_id = r.id AND patch.attempt_id = agent_attempt.id
  JOIN verification_runs verification_run ON verification_run.run_id = r.id
  JOIN verification_results result ON result.verification_run_id = verification_run.id
  JOIN verification_attempts verification_attempt ON verification_attempt.id = result.verification_attempt_id
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
