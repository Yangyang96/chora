CREATE TABLE delegation_proposal_sources (
 parent_task_id TEXT PRIMARY KEY REFERENCES task_delegations(parent_task_id) ON DELETE RESTRICT,
 run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE RESTRICT,
 attempt_id TEXT NOT NULL REFERENCES attempts(id) ON DELETE RESTRICT,
 result_id TEXT NOT NULL REFERENCES resource_result_groups(result_id) ON DELETE RESTRICT,
 agent_report_id TEXT NOT NULL REFERENCES agent_reports(id) ON DELETE RESTRICT,
 event_id TEXT NOT NULL REFERENCES run_events(id) ON DELETE RESTRICT,
 event_sequence INTEGER NOT NULL CHECK(event_sequence > 0),
 result_digest BLOB NOT NULL CHECK(length(result_digest)=32),
 source_text_digest BLOB NOT NULL CHECK(length(source_text_digest)=32),
 plan_digest BLOB NOT NULL CHECK(length(plan_digest)=32),
 created_at TEXT NOT NULL
);
CREATE TRIGGER delegation_proposal_sources_no_update BEFORE UPDATE ON delegation_proposal_sources
BEGIN SELECT RAISE(ABORT,'delegation proposal source is immutable'); END;
CREATE TRIGGER delegation_proposal_sources_no_delete BEFORE DELETE ON delegation_proposal_sources
BEGIN SELECT RAISE(ABORT,'delegation proposal source is durable'); END;
CREATE TRIGGER delegation_proposal_sources_lineage BEFORE INSERT ON delegation_proposal_sources
WHEN NOT EXISTS (
 SELECT 1 FROM runs r JOIN attempts a ON a.run_id=r.id
 JOIN resource_result_groups g ON g.attempt_id=a.id
 JOIN agent_reports p ON p.attempt_id=a.id AND p.run_id=r.id
 JOIN run_events e ON e.run_id=r.id
 WHERE r.id=NEW.run_id AND r.task_id=NEW.parent_task_id
 AND a.id=NEW.attempt_id AND g.result_id=NEW.result_id
 AND g.run_id=r.id AND g.task_id=r.task_id AND g.digest=NEW.result_digest
 AND p.id=NEW.agent_report_id AND e.id=NEW.event_id AND e.sequence=NEW.event_sequence
)
BEGIN SELECT RAISE(ABORT,'delegation proposal source lineage mismatch'); END;
