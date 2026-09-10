CREATE TABLE task_delivery_operations (
 operation_id TEXT PRIMARY KEY,
 task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE RESTRICT,
 run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE RESTRICT,
 repo_id TEXT NOT NULL REFERENCES repositories(repo_id) ON DELETE RESTRICT,
 kind TEXT NOT NULL CHECK(kind IN ('commit','push','pr','merge')),
 state TEXT NOT NULL CHECK(state IN ('preview','writing','succeeded','failed','recovery_required')),
 version INTEGER NOT NULL CHECK(version>=1),
 request_digest BLOB NOT NULL CHECK(length(request_digest)=32),
 preview_json BLOB NOT NULL CHECK(length(preview_json)<=146800640),
 outcome_json BLOB NOT NULL CHECK(length(outcome_json)<=1048576),
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE INDEX task_delivery_by_task ON task_delivery_operations(task_id,created_at,operation_id);
CREATE UNIQUE INDEX task_delivery_one_pending ON task_delivery_operations(task_id,repo_id) WHERE state IN ('writing','recovery_required');
CREATE TRIGGER task_delivery_no_delete BEFORE DELETE ON task_delivery_operations
BEGIN SELECT RAISE(ABORT,'delivery history is durable'); END;
CREATE TRIGGER task_delivery_immutable_intent BEFORE UPDATE ON task_delivery_operations
WHEN NEW.operation_id<>OLD.operation_id OR NEW.task_id<>OLD.task_id OR NEW.run_id<>OLD.run_id OR NEW.repo_id<>OLD.repo_id OR NEW.kind<>OLD.kind OR NEW.request_digest<>OLD.request_digest OR NEW.preview_json<>OLD.preview_json OR NEW.created_at<>OLD.created_at
BEGIN SELECT RAISE(ABORT,'delivery intent is immutable'); END;

CREATE TRIGGER task_delivery_terminal_outcome BEFORE UPDATE ON task_delivery_operations
WHEN OLD.state IN ('succeeded','failed')
BEGIN SELECT RAISE(ABORT,'delivery outcome is terminal'); END;
