CREATE TABLE task_delegations (
 parent_task_id TEXT PRIMARY KEY REFERENCES tasks(id) ON DELETE RESTRICT,
 version INTEGER NOT NULL CHECK(version > 0),
 state TEXT NOT NULL CHECK(state IN ('running','stopping','stopped','blocked','awaiting_review')),
 assignments_json TEXT NOT NULL CHECK(json_valid(assignments_json)),
 actor_id TEXT NOT NULL, session_id TEXT NOT NULL,
 reason TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE TABLE task_delegation_children (
 parent_task_id TEXT NOT NULL REFERENCES task_delegations(parent_task_id) ON DELETE RESTRICT,
 position INTEGER NOT NULL CHECK(position BETWEEN 0 AND 3),
 task_id TEXT NOT NULL UNIQUE REFERENCES tasks(id) ON DELETE RESTRICT,
 run_id TEXT UNIQUE REFERENCES runs(id) ON DELETE RESTRICT,
 created_at TEXT NOT NULL,
 PRIMARY KEY(parent_task_id,position), CHECK(parent_task_id <> task_id)
);
CREATE INDEX task_delegations_state_idx ON task_delegations(state,parent_task_id);
CREATE TRIGGER task_delegations_immutable_authority BEFORE UPDATE ON task_delegations
WHEN NEW.parent_task_id<>OLD.parent_task_id OR NEW.assignments_json<>OLD.assignments_json
 OR NEW.actor_id<>OLD.actor_id OR NEW.session_id<>OLD.session_id OR NEW.created_at<>OLD.created_at
BEGIN SELECT RAISE(ABORT,'delegation authority is immutable'); END;
CREATE TRIGGER task_delegations_no_delete BEFORE DELETE ON task_delegations BEGIN SELECT RAISE(ABORT,'delegation history is durable'); END;
CREATE TRIGGER task_delegation_children_no_update BEFORE UPDATE ON task_delegation_children
WHEN NEW.parent_task_id<>OLD.parent_task_id OR NEW.position<>OLD.position OR NEW.task_id<>OLD.task_id OR NEW.created_at<>OLD.created_at OR OLD.run_id IS NOT NULL OR NEW.run_id IS NULL
BEGIN SELECT RAISE(ABORT,'delegation lineage is immutable'); END;
CREATE TRIGGER task_delegation_children_no_delete BEFORE DELETE ON task_delegation_children BEGIN SELECT RAISE(ABORT,'delegation lineage is durable'); END;
