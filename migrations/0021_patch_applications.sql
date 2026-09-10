CREATE TABLE patch_applications (
  run_id TEXT PRIMARY KEY REFERENCES runs(id) ON DELETE RESTRICT,
  review_decision_id TEXT NOT NULL UNIQUE REFERENCES verified_review_decisions(id) ON DELETE RESTRICT,
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

CREATE TRIGGER patch_applications_authority BEFORE INSERT ON patch_applications
WHEN NOT EXISTS (
  SELECT 1
  FROM runs run
  JOIN verified_review_decisions review ON review.run_id=run.id
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

-- Accepted real Tasks from the pre-Apply product remain accepted but become
-- actionable again. Diagnostic/legacy Tasks have no verified review row and
-- preserve their historical closed semantics.
UPDATE tasks SET state='open',updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
WHERE state='closed' AND EXISTS (
  SELECT 1 FROM runs run
  JOIN verified_review_decisions review ON review.run_id=run.id
  WHERE run.task_id=tasks.id AND run.state='accepted' AND review.kind='accept'
);
