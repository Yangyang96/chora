CREATE TABLE execution_decision_gates (
  id TEXT PRIMARY KEY,
  run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE RESTRICT,
  attempt_id TEXT NOT NULL,
  question TEXT NOT NULL CHECK(chora_valid_trusted_text(CAST(question AS BLOB),500,1)),
  context TEXT NOT NULL CHECK(chora_valid_trusted_text(CAST(context AS BLOB),2000,1)),
  recommendation TEXT NOT NULL CHECK(chora_valid_trusted_text(CAST(recommendation AS BLOB),1000,1)),
  impact TEXT NOT NULL CHECK(chora_valid_trusted_text(CAST(impact AS BLOB),1000,1)),
  status TEXT NOT NULL CHECK(status IN ('open','resolved')),
  selected_option_id TEXT CHECK(selected_option_id IS NULL OR chora_valid_trusted_text(CAST(selected_option_id AS BLOB),100,1)),
  note TEXT CHECK(note IS NULL OR chora_valid_trusted_text(CAST(note AS BLOB),2000,0)),
  actor_id TEXT CHECK(actor_id IS NULL OR chora_valid_trusted_text(CAST(actor_id AS BLOB),200,1)),
  session_id TEXT CHECK(session_id IS NULL OR chora_valid_trusted_text(CAST(session_id AS BLOB),200,1)),
  requested_at TEXT NOT NULL,
  resolved_at TEXT,
  FOREIGN KEY(run_id,attempt_id) REFERENCES attempts(run_id,id) ON DELETE RESTRICT,
  CHECK(
    (status='open' AND selected_option_id IS NULL AND note IS NULL AND actor_id IS NULL AND session_id IS NULL AND resolved_at IS NULL)
    OR
    (status='resolved' AND selected_option_id IS NOT NULL AND note IS NOT NULL AND actor_id IS NOT NULL AND session_id IS NOT NULL AND resolved_at IS NOT NULL AND resolved_at>=requested_at)
  )
);

CREATE TABLE execution_decision_gate_options (
  gate_id TEXT NOT NULL REFERENCES execution_decision_gates(id) ON DELETE RESTRICT,
  option_id TEXT NOT NULL CHECK(chora_valid_trusted_text(CAST(option_id AS BLOB),100,1)),
  position INTEGER NOT NULL CHECK(position>=0),
  label TEXT NOT NULL CHECK(chora_valid_trusted_text(CAST(label AS BLOB),200,1)),
  impact TEXT NOT NULL CHECK(chora_valid_trusted_text(CAST(impact AS BLOB),1000,1)),
  PRIMARY KEY(gate_id,option_id),
  UNIQUE(gate_id,position)
);

CREATE INDEX execution_decision_gates_run_idx ON execution_decision_gates(run_id,requested_at,id);
CREATE INDEX execution_decision_gates_attempt_idx ON execution_decision_gates(attempt_id);
CREATE UNIQUE INDEX one_open_execution_decision_gate_per_run ON execution_decision_gates(run_id) WHERE status='open';

CREATE TRIGGER execution_decision_gate_resolution_only
BEFORE UPDATE ON execution_decision_gates
WHEN OLD.run_id<>NEW.run_id OR OLD.attempt_id<>NEW.attempt_id OR OLD.question<>NEW.question OR OLD.context<>NEW.context
  OR OLD.recommendation<>NEW.recommendation OR OLD.impact<>NEW.impact OR OLD.requested_at<>NEW.requested_at
  OR OLD.status<>'open' OR NEW.status<>'resolved'
BEGIN SELECT RAISE(ABORT,'execution decision gate is immutable except one resolution'); END;

CREATE TRIGGER execution_decision_gate_resolution_option_exists
BEFORE UPDATE ON execution_decision_gates
WHEN NEW.status='resolved' AND NOT EXISTS (
  SELECT 1 FROM execution_decision_gate_options
  WHERE gate_id=NEW.id AND option_id=NEW.selected_option_id
)
BEGIN SELECT RAISE(ABORT,'execution decision gate selected option does not exist'); END;

CREATE TRIGGER execution_decision_gate_no_delete BEFORE DELETE ON execution_decision_gates
BEGIN SELECT RAISE(ABORT,'execution decision gate is immutable'); END;
CREATE TRIGGER execution_decision_gate_options_no_update BEFORE UPDATE ON execution_decision_gate_options
BEGIN SELECT RAISE(ABORT,'execution decision gate options are immutable'); END;
CREATE TRIGGER execution_decision_gate_options_no_delete BEFORE DELETE ON execution_decision_gate_options
BEGIN SELECT RAISE(ABORT,'execution decision gate options are immutable'); END;
