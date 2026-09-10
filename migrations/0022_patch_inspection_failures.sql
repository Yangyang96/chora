CREATE TABLE patch_inspection_failures (
  run_id TEXT PRIMARY KEY REFERENCES runs(id) ON DELETE RESTRICT,
  review_decision_id TEXT NOT NULL UNIQUE REFERENCES verified_review_decisions(id) ON DELETE RESTRICT,
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

CREATE TRIGGER patch_inspection_failures_authority BEFORE INSERT ON patch_inspection_failures
WHEN NOT EXISTS (
  SELECT 1 FROM runs run
  JOIN verified_review_decisions review ON review.run_id=run.id
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
