CREATE TABLE result_closures (
 result_id TEXT PRIMARY KEY,
 run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE RESTRICT,
 task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE RESTRICT,
 attempt_id TEXT NOT NULL REFERENCES attempts(id) ON DELETE RESTRICT,
 result_digest BLOB NOT NULL CHECK(length(result_digest)=32),
 preview_digest BLOB NOT NULL CHECK(length(preview_digest)=32),
 review_id TEXT NOT NULL,
 repository_ids BLOB NOT NULL CHECK(length(repository_ids)<=4096),
 actor_id TEXT NOT NULL,
 session_id TEXT NOT NULL,
 created_at TEXT NOT NULL
);
CREATE INDEX result_closures_by_run ON result_closures(run_id);
CREATE TRIGGER result_closures_no_update BEFORE UPDATE ON result_closures
BEGIN SELECT RAISE(ABORT,'result closure is immutable'); END;
CREATE TRIGGER result_closures_no_delete BEFORE DELETE ON result_closures
BEGIN SELECT RAISE(ABORT,'result closure history is durable'); END;
