CREATE TABLE delegation_synthesis (
 parent_task_id TEXT PRIMARY KEY REFERENCES tasks(id) ON DELETE RESTRICT,
 task_id TEXT UNIQUE REFERENCES tasks(id) ON DELETE RESTRICT,
 run_id TEXT UNIQUE REFERENCES runs(id) ON DELETE RESTRICT,
 attempt_id TEXT UNIQUE REFERENCES attempts(id) ON DELETE RESTRICT,
 inputs_json TEXT CHECK(inputs_json IS NULL OR json_valid(inputs_json)),
 max_attempts INTEGER NOT NULL DEFAULT 1 CHECK(max_attempts=1),
 actor_id TEXT NOT NULL,session_id TEXT NOT NULL,created_at TEXT NOT NULL,
 CHECK((task_id IS NULL AND inputs_json IS NULL AND run_id IS NULL AND attempt_id IS NULL) OR (task_id IS NOT NULL AND inputs_json IS NOT NULL)),
 CHECK(attempt_id IS NULL OR run_id IS NOT NULL),CHECK(task_id IS NULL OR task_id<>parent_task_id)
);
CREATE TRIGGER delegation_synthesis_immutable BEFORE UPDATE ON delegation_synthesis
WHEN NEW.parent_task_id<>OLD.parent_task_id OR NEW.actor_id<>OLD.actor_id OR NEW.session_id<>OLD.session_id OR NEW.created_at<>OLD.created_at
 OR (OLD.task_id IS NOT NULL AND (NEW.task_id IS NOT OLD.task_id OR NEW.inputs_json IS NOT OLD.inputs_json))
 OR (OLD.run_id IS NOT NULL AND NEW.run_id IS NOT OLD.run_id) OR (OLD.attempt_id IS NOT NULL AND NEW.attempt_id IS NOT OLD.attempt_id)
BEGIN SELECT RAISE(ABORT,'synthesis authorization and bound sources are immutable'); END;
CREATE TRIGGER delegation_synthesis_no_delete BEFORE DELETE ON delegation_synthesis
BEGIN SELECT RAISE(ABORT,'synthesis authorization is durable'); END;
CREATE TRIGGER delegation_synthesis_run BEFORE UPDATE OF run_id ON delegation_synthesis
WHEN NEW.run_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM runs WHERE id=NEW.run_id AND task_id=NEW.task_id)
BEGIN SELECT RAISE(ABORT,'synthesis Run lineage mismatch'); END;
CREATE TRIGGER delegation_synthesis_attempt BEFORE UPDATE OF attempt_id ON delegation_synthesis
WHEN NEW.attempt_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM attempts WHERE id=NEW.attempt_id AND run_id=NEW.run_id AND sequence=1)
BEGIN SELECT RAISE(ABORT,'synthesis Attempt lineage mismatch'); END;

-- Explicit empty context is reserved for an authorized, bound synthesis Task.
CREATE TABLE synthesis_context_selections (
 task_id TEXT PRIMARY KEY REFERENCES delegation_synthesis(task_id) ON DELETE RESTRICT,
 room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE RESTRICT,
 created_at TEXT NOT NULL
);
CREATE TRIGGER synthesis_context_selection_insert BEFORE INSERT ON synthesis_context_selections
WHEN NOT EXISTS(SELECT 1 FROM tasks WHERE id=NEW.task_id AND room_id=NEW.room_id)
 OR EXISTS(SELECT 1 FROM task_revision_selections WHERE task_id=NEW.task_id)
 OR EXISTS(SELECT 1 FROM task_revision_exclusions WHERE task_id=NEW.task_id)
BEGIN SELECT RAISE(ABORT,'synthesis context must be empty and task-bound'); END;
CREATE TRIGGER synthesis_context_selection_update BEFORE UPDATE ON synthesis_context_selections
BEGIN SELECT RAISE(ABORT,'synthesis context is immutable'); END;
CREATE TRIGGER synthesis_context_selection_delete BEFORE DELETE ON synthesis_context_selections
BEGIN SELECT RAISE(ABORT,'synthesis context is durable'); END;
CREATE TRIGGER synthesis_no_selected_context BEFORE INSERT ON task_revision_selections
WHEN EXISTS(SELECT 1 FROM synthesis_context_selections WHERE task_id=NEW.task_id)
BEGIN SELECT RAISE(ABORT,'synthesis cannot add selected context'); END;
CREATE TRIGGER synthesis_no_excluded_context BEFORE INSERT ON task_revision_exclusions
WHEN EXISTS(SELECT 1 FROM synthesis_context_selections WHERE task_id=NEW.task_id)
BEGIN SELECT RAISE(ABORT,'synthesis cannot add excluded context'); END;
CREATE TABLE synthesis_run_charters (
 charter_id TEXT PRIMARY KEY REFERENCES run_charters(id) ON DELETE RESTRICT,
 task_id TEXT NOT NULL REFERENCES synthesis_context_selections(task_id) ON DELETE RESTRICT
);
CREATE TRIGGER synthesis_charter_insert BEFORE INSERT ON synthesis_run_charters
WHEN NOT EXISTS(SELECT 1 FROM run_charters WHERE id=NEW.charter_id AND task_id=NEW.task_id)
 OR EXISTS(SELECT 1 FROM charter_context_selections WHERE charter_id=NEW.charter_id)
BEGIN SELECT RAISE(ABORT,'synthesis charter must have empty context'); END;
CREATE TRIGGER synthesis_charter_update BEFORE UPDATE ON synthesis_run_charters
BEGIN SELECT RAISE(ABORT,'synthesis charter is immutable'); END;
CREATE TRIGGER synthesis_charter_delete BEFORE DELETE ON synthesis_run_charters
BEGIN SELECT RAISE(ABORT,'synthesis charter is durable'); END;
CREATE TRIGGER synthesis_no_charter_context BEFORE INSERT ON charter_context_selections
WHEN EXISTS(SELECT 1 FROM synthesis_run_charters WHERE charter_id=NEW.charter_id)
BEGIN SELECT RAISE(ABORT,'synthesis charter cannot add context'); END;
