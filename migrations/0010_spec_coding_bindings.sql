CREATE TABLE spec_coding_bindings (
  task_id TEXT PRIMARY KEY REFERENCES tasks(id) ON DELETE RESTRICT,
  snapshot_id TEXT NOT NULL UNIQUE REFERENCES context_snapshots(id) ON DELETE RESTRICT,
  snapshot_digest BLOB NOT NULL CHECK(length(snapshot_digest)=32),
  materialized_contract_digest BLOB NOT NULL CHECK(length(materialized_contract_digest)=32),
  materialized_contract_json BLOB NOT NULL CHECK(length(materialized_contract_json)>0),
  active_contract_digest BLOB,
  active_contract_json BLOB,
  status TEXT NOT NULL CHECK(status IN ('materialized','registered')),
  materialized_at TEXT NOT NULL,
  registered_at TEXT,
  CHECK(
    (status='materialized' AND active_contract_digest IS NULL AND active_contract_json IS NULL AND registered_at IS NULL)
    OR
    (status='registered' AND length(active_contract_digest)=32 AND length(active_contract_json)>0 AND registered_at IS NOT NULL AND registered_at>=materialized_at)
  )
);

CREATE TRIGGER spec_coding_bindings_no_delete
BEFORE DELETE ON spec_coding_bindings
BEGIN
  SELECT RAISE(ABORT,'spec coding binding is immutable');
END;

CREATE TRIGGER spec_coding_bindings_registration_only
BEFORE UPDATE ON spec_coding_bindings
WHEN OLD.status!='materialized' OR NEW.status!='registered'
  OR NEW.task_id IS NOT OLD.task_id
  OR NEW.snapshot_id IS NOT OLD.snapshot_id
  OR NEW.snapshot_digest IS NOT OLD.snapshot_digest
  OR NEW.materialized_contract_digest IS NOT OLD.materialized_contract_digest
  OR NEW.materialized_contract_json IS NOT OLD.materialized_contract_json
  OR NEW.materialized_at IS NOT OLD.materialized_at
BEGIN
  SELECT RAISE(ABORT,'spec coding binding transition rejected');
END;
