ALTER TABLE verified_review_decisions RENAME TO verified_review_decisions_v1;

CREATE TABLE verified_review_decisions (
  id TEXT PRIMARY KEY,
  run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE RESTRICT,
  result_id TEXT NOT NULL UNIQUE REFERENCES verification_results(id) ON DELETE RESTRICT,
  agent_attempt_id TEXT NOT NULL REFERENCES attempts(id) ON DELETE RESTRICT,
  verification_attempt_id TEXT NOT NULL REFERENCES verification_attempts(id) ON DELETE RESTRICT,
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

INSERT INTO verified_review_decisions(
  id,run_id,result_id,agent_attempt_id,verification_attempt_id,patch_artifact_id,
  expected_run_version,kind,reason,rejection_class,actor_id,session_id,
  patch_digest,baseline_digest,declared_files_digest,decided_at
)
SELECT id,run_id,result_id,agent_attempt_id,verification_attempt_id,patch_artifact_id,
  expected_run_version,kind,reason,rejection_class,actor_id,session_id,
  patch_digest,baseline_digest,declared_files_digest,decided_at
FROM verified_review_decisions_v1;

DROP TABLE verified_review_decisions_v1;

CREATE INDEX verified_review_decisions_run_idx
  ON verified_review_decisions(run_id, decided_at);

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

CREATE TRIGGER verified_review_decisions_immutable_update
BEFORE UPDATE ON verified_review_decisions BEGIN
  SELECT RAISE(ABORT, 'verified review decision is immutable');
END;

CREATE TRIGGER verified_review_decisions_immutable_delete
BEFORE DELETE ON verified_review_decisions BEGIN
  SELECT RAISE(ABORT, 'verified review decision is immutable');
END;

CREATE TABLE verified_review_routes (
  decision_id TEXT PRIMARY KEY REFERENCES verified_review_decisions(id) ON DELETE RESTRICT,
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

CREATE TRIGGER verified_review_route_exact BEFORE INSERT ON verified_review_routes
WHEN NOT EXISTS (
  SELECT 1 FROM verified_review_decisions decision
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
