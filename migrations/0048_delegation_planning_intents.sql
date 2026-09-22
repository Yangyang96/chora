CREATE TABLE delegation_planning_intents (
 parent_task_id TEXT PRIMARY KEY REFERENCES tasks(id) ON DELETE RESTRICT,
 run_id TEXT NOT NULL UNIQUE REFERENCES runs(id) ON DELETE RESTRICT,
 attempt_id TEXT UNIQUE REFERENCES attempts(id) ON DELETE RESTRICT,
 proposal_schema TEXT NOT NULL DEFAULT 'chora.delegation-plan.v1' CHECK(proposal_schema='chora.delegation-plan.v1'),
 max_assignments INTEGER NOT NULL DEFAULT 4 CHECK(max_assignments=4),
 max_planning_attempts INTEGER NOT NULL DEFAULT 1 CHECK(max_planning_attempts=1),
 version INTEGER NOT NULL CHECK(version>0),
 state TEXT NOT NULL CHECK(state IN ('planning','blocked','stopping','stopped','imported')),
 authority_digest BLOB NOT NULL CHECK(length(authority_digest)=32),
 actor_id TEXT NOT NULL, session_id TEXT NOT NULL, reason TEXT NOT NULL,
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
 CHECK(state!='imported' OR attempt_id IS NOT NULL)
);
CREATE TRIGGER delegation_planning_identity BEFORE UPDATE ON delegation_planning_intents
WHEN NEW.parent_task_id!=OLD.parent_task_id OR NEW.run_id!=OLD.run_id OR NEW.authority_digest!=OLD.authority_digest OR NEW.actor_id!=OLD.actor_id OR NEW.session_id!=OLD.session_id OR NEW.created_at!=OLD.created_at OR (OLD.attempt_id IS NOT NULL AND NEW.attempt_id IS NOT OLD.attempt_id)
BEGIN SELECT RAISE(ABORT,'planning authorization is immutable'); END;
CREATE TRIGGER delegation_planning_no_delete BEFORE DELETE ON delegation_planning_intents
BEGIN SELECT RAISE(ABORT,'planning authorization is durable'); END;
CREATE TRIGGER delegation_planning_run BEFORE INSERT ON delegation_planning_intents
WHEN NOT EXISTS(SELECT 1 FROM runs WHERE id=NEW.run_id AND task_id=NEW.parent_task_id)
BEGIN SELECT RAISE(ABORT,'planning Run lineage mismatch'); END;
CREATE TRIGGER delegation_planning_attempt BEFORE UPDATE OF attempt_id ON delegation_planning_intents
WHEN NEW.attempt_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM attempts WHERE id=NEW.attempt_id AND run_id=NEW.run_id AND sequence=1)
BEGIN SELECT RAISE(ABORT,'planning Attempt lineage mismatch'); END;
