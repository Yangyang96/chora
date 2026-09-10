PRAGMA defer_foreign_keys = ON;

DROP INDEX runs_task_idx;
DROP INDEX runs_charter_idx;
DROP INDEX one_active_run_per_task;
DROP INDEX runs_recovery_idx;

CREATE TABLE runs_v11 (
  id TEXT PRIMARY KEY,
  task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE RESTRICT,
  charter_id TEXT NOT NULL REFERENCES run_charters(id) ON DELETE RESTRICT,
  state TEXT NOT NULL CHECK(state IN (
    'draft','ready','running','stopping','awaiting_verification','verifying',
    'awaiting_review','revision_required','recovery_required',
    'verification_recovery_required','accepted','cancelled'
  )),
  version INTEGER NOT NULL CHECK(version >= 0),
  current_attempt_number INTEGER NOT NULL CHECK(current_attempt_number >= 0),
  last_event_sequence INTEGER NOT NULL DEFAULT 0 CHECK(last_event_sequence >= 0),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  started_at TEXT,
  review_requested_at TEXT,
  terminal_at TEXT
);

INSERT INTO runs_v11(
  id,task_id,charter_id,state,version,current_attempt_number,last_event_sequence,
  created_at,updated_at,started_at,review_requested_at,terminal_at
)
SELECT
  id,task_id,charter_id,state,version,current_attempt_number,last_event_sequence,
  created_at,updated_at,started_at,review_requested_at,terminal_at
FROM runs;

DROP TABLE runs;
ALTER TABLE runs_v11 RENAME TO runs;

CREATE INDEX runs_task_idx ON runs(task_id);
CREATE INDEX runs_charter_idx ON runs(charter_id);
CREATE UNIQUE INDEX one_active_run_per_task ON runs(task_id) WHERE state NOT IN ('accepted', 'cancelled');
CREATE INDEX runs_recovery_idx ON runs(state, updated_at) WHERE state IN ('running','stopping','recovery_required','verifying','verification_recovery_required');
