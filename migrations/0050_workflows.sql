CREATE TABLE workflow_executions (
 id TEXT PRIMARY KEY,
 task_id TEXT NOT NULL UNIQUE REFERENCES tasks(id) ON DELETE RESTRICT,
 project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
 actor_id TEXT NOT NULL, session_id TEXT NOT NULL,
 version INTEGER NOT NULL CHECK(version > 0),
 status TEXT NOT NULL CHECK(status IN ('researching','waiting','confirming','approved','coding','awaiting_review','stopping','stopped','rejected','failed','unresolved')),
 input_digest TEXT NOT NULL CHECK(length(input_digest)=64),
 authority_digest TEXT NOT NULL CHECK(length(authority_digest)=64),
 frozen_json TEXT NOT NULL CHECK(json_valid(frozen_json)),
 state_json TEXT NOT NULL CHECK(json_valid(state_json))
);
CREATE INDEX workflow_status_idx ON workflow_executions(status);
CREATE TABLE workflow_commands (
 operation_id TEXT PRIMARY KEY,
 workflow_id TEXT NOT NULL REFERENCES workflow_executions(id) ON DELETE RESTRICT,
 kind TEXT NOT NULL,
 input_digest TEXT NOT NULL CHECK(length(input_digest)=64),
 payload TEXT NOT NULL CHECK(json_valid(payload)),
 receipt TEXT CHECK(receipt IS NULL OR json_valid(receipt))
);
CREATE TABLE workflow_events (
 workflow_id TEXT NOT NULL REFERENCES workflow_executions(id) ON DELETE RESTRICT,
 sequence INTEGER NOT NULL CHECK(sequence > 0),
 kind TEXT NOT NULL, data TEXT NOT NULL CHECK(json_valid(data)),
 PRIMARY KEY(workflow_id, sequence)
);
CREATE TRIGGER workflow_authority_immutable BEFORE UPDATE ON workflow_executions
WHEN NEW.id<>OLD.id OR NEW.task_id<>OLD.task_id OR NEW.project_id<>OLD.project_id
 OR NEW.actor_id<>OLD.actor_id OR NEW.session_id<>OLD.session_id
 OR NEW.input_digest<>OLD.input_digest OR NEW.authority_digest<>OLD.authority_digest
 OR NEW.frozen_json<>OLD.frozen_json OR NEW.version<>OLD.version+1
BEGIN SELECT RAISE(ABORT,'workflow authority is immutable'); END;
CREATE TRIGGER workflow_state_authority BEFORE UPDATE ON workflow_executions
WHEN (json_extract(OLD.state_json,'$.stopRequested')=1 AND json_extract(NEW.state_json,'$.stopRequested')<>1)
 OR (json_extract(OLD.state_json,'$.runId') IS NOT NULL AND json_extract(NEW.state_json,'$.runId') IS NOT json_extract(OLD.state_json,'$.runId'))
 OR (json_extract(OLD.state_json,'$.gate.id') IS NOT NULL AND (
    json_extract(NEW.state_json,'$.gate.id') IS NOT json_extract(OLD.state_json,'$.gate.id')
 OR json_extract(NEW.state_json,'$.gate.inputDigest') IS NOT json_extract(OLD.state_json,'$.gate.inputDigest')
 OR json_extract(NEW.state_json,'$.gate.report') IS NOT json_extract(OLD.state_json,'$.gate.report')
 OR json_extract(NEW.state_json,'$.gate.version') IS NOT json_extract(OLD.state_json,'$.gate.version')))
 OR (json_extract(OLD.state_json,'$.gate.decision') IS NOT NULL AND (
    json_extract(NEW.state_json,'$.gate.decision') IS NOT json_extract(OLD.state_json,'$.gate.decision')
 OR json_extract(NEW.state_json,'$.gate.instructions') IS NOT json_extract(OLD.state_json,'$.gate.instructions')))
BEGIN SELECT RAISE(ABORT,'workflow gate, Run and stop authority is immutable'); END;
CREATE TRIGGER workflow_command_immutable BEFORE UPDATE ON workflow_commands
WHEN NEW.operation_id<>OLD.operation_id OR NEW.workflow_id<>OLD.workflow_id
 OR NEW.kind<>OLD.kind OR NEW.input_digest<>OLD.input_digest OR NEW.payload<>OLD.payload
 OR OLD.receipt IS NOT NULL OR NEW.receipt IS NULL
BEGIN SELECT RAISE(ABORT,'workflow command or receipt is immutable'); END;
CREATE TRIGGER workflow_no_delete BEFORE DELETE ON workflow_executions
BEGIN SELECT RAISE(ABORT,'workflow history is durable'); END;
CREATE TRIGGER workflow_command_no_delete BEFORE DELETE ON workflow_commands
BEGIN SELECT RAISE(ABORT,'workflow command history is durable'); END;
CREATE TRIGGER workflow_events_no_update BEFORE UPDATE ON workflow_events
BEGIN SELECT RAISE(ABORT,'workflow events are immutable'); END;
CREATE TRIGGER workflow_events_no_delete BEFORE DELETE ON workflow_events
BEGIN SELECT RAISE(ABORT,'workflow events are durable'); END;
