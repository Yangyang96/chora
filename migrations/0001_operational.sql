CREATE TABLE rooms (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, description TEXT NOT NULL DEFAULT '',
  workspace_root TEXT NOT NULL UNIQUE, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE TABLE tasks (
  id TEXT PRIMARY KEY, room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE RESTRICT,
  title TEXT NOT NULL, goal TEXT NOT NULL, state TEXT NOT NULL CHECK(state IN ('open','closed')),
  predecessor_task_id TEXT REFERENCES tasks(id) ON DELETE RESTRICT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE INDEX tasks_room_id_idx ON tasks(room_id);
CREATE INDEX tasks_predecessor_idx ON tasks(predecessor_task_id);
CREATE TABLE task_criteria (
  criterion_id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE RESTRICT,
  position INTEGER NOT NULL CHECK(position >= 0), title TEXT NOT NULL, description TEXT NOT NULL DEFAULT '',
  UNIQUE(task_id, position)
);
CREATE INDEX task_criteria_task_idx ON task_criteria(task_id);
CREATE TABLE run_charters (
  id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE RESTRICT, task_goal TEXT NOT NULL,
  workspace_root TEXT NOT NULL, adapter_id TEXT NOT NULL, sandbox_mode TEXT NOT NULL, expected_output TEXT NOT NULL,
  responsible_human TEXT NOT NULL, initiator TEXT NOT NULL, created_at TEXT NOT NULL
);
CREATE INDEX run_charters_task_idx ON run_charters(task_id);
CREATE TABLE charter_criteria (
  charter_id TEXT NOT NULL REFERENCES run_charters(id) ON DELETE RESTRICT,
  criterion_id TEXT NOT NULL REFERENCES task_criteria(criterion_id) ON DELETE RESTRICT, position INTEGER NOT NULL CHECK(position >= 0), title TEXT NOT NULL, description TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(charter_id, criterion_id), UNIQUE(charter_id, position)
);
CREATE INDEX charter_criteria_charter_idx ON charter_criteria(charter_id);
CREATE TABLE charter_capabilities (
  charter_id TEXT NOT NULL REFERENCES run_charters(id) ON DELETE RESTRICT, name TEXT NOT NULL, allowed INTEGER NOT NULL CHECK(allowed IN (0,1)),
  PRIMARY KEY(charter_id, name)
);
CREATE INDEX charter_capabilities_charter_idx ON charter_capabilities(charter_id);
CREATE TABLE runs (
  id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE RESTRICT,
  charter_id TEXT NOT NULL REFERENCES run_charters(id) ON DELETE RESTRICT,
  state TEXT NOT NULL CHECK(state IN ('draft','ready','running','stopping','awaiting_review','revision_required','recovery_required','accepted','cancelled')),
  version INTEGER NOT NULL CHECK(version >= 0), current_attempt_number INTEGER NOT NULL CHECK(current_attempt_number >= 0),
  last_event_sequence INTEGER NOT NULL DEFAULT 0 CHECK(last_event_sequence >= 0), created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
  started_at TEXT, review_requested_at TEXT, terminal_at TEXT
);
CREATE INDEX runs_task_idx ON runs(task_id);
CREATE INDEX runs_charter_idx ON runs(charter_id);
CREATE UNIQUE INDEX one_active_run_per_task ON runs(task_id) WHERE state NOT IN ('accepted', 'cancelled');
CREATE INDEX runs_recovery_idx ON runs(state, updated_at) WHERE state IN ('running','stopping','recovery_required');
CREATE TABLE attempts (
  id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE RESTRICT, sequence INTEGER NOT NULL CHECK(sequence > 0),
  predecessor_attempt_id TEXT REFERENCES attempts(id) ON DELETE RESTRICT,
  context_snapshot_id TEXT NOT NULL, context_digest BLOB NOT NULL CHECK(length(context_digest)=32), adapter_id TEXT NOT NULL,
  state TEXT NOT NULL CHECK(state IN ('created','starting','running','output_submitted','failed','interrupted','cancelled')),
  retry_reason TEXT NOT NULL DEFAULT '', intervention_reason TEXT NOT NULL DEFAULT '', context_delta TEXT NOT NULL DEFAULT '',
  external_session TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, started_at TEXT, terminal_at TEXT,
  UNIQUE(run_id, sequence), UNIQUE(run_id, id), CHECK((sequence=1 AND predecessor_attempt_id IS NULL) OR (sequence>1 AND predecessor_attempt_id IS NOT NULL)),
  FOREIGN KEY(context_snapshot_id,context_digest) REFERENCES context_snapshots(id,digest) ON DELETE RESTRICT
);
CREATE INDEX attempts_run_idx ON attempts(run_id);
CREATE INDEX attempts_predecessor_idx ON attempts(predecessor_attempt_id);
CREATE INDEX attempts_snapshot_idx ON attempts(context_snapshot_id);
CREATE UNIQUE INDEX one_live_attempt_per_run ON attempts(run_id) WHERE state IN ('starting','running');
CREATE INDEX attempts_recovery_idx ON attempts(state, created_at) WHERE state IN ('starting','running');
CREATE TABLE run_events (
  id TEXT NOT NULL, run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE RESTRICT, sequence INTEGER NOT NULL CHECK(sequence > 0),
  type TEXT NOT NULL, source TEXT NOT NULL, occurred_at TEXT NOT NULL, recorded_at TEXT NOT NULL,
  normalized_json BLOB NOT NULL CHECK(json_valid(CAST(normalized_json AS TEXT))), raw_json BLOB CHECK(raw_json IS NULL OR json_valid(CAST(raw_json AS TEXT))),
  PRIMARY KEY(id), UNIQUE(run_id, sequence), UNIQUE(run_id, id)
);
CREATE INDEX run_events_run_idx ON run_events(run_id);
CREATE TABLE observations (
  id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE RESTRICT, attempt_id TEXT,
  kind TEXT NOT NULL, body TEXT NOT NULL, source_event_id TEXT, created_at TEXT NOT NULL,
  FOREIGN KEY(run_id, source_event_id) REFERENCES run_events(run_id, id) ON DELETE RESTRICT,
  FOREIGN KEY(run_id, attempt_id) REFERENCES attempts(run_id, id) ON DELETE RESTRICT
);
CREATE INDEX observations_run_idx ON observations(run_id); CREATE INDEX observations_attempt_idx ON observations(attempt_id); CREATE INDEX observations_event_idx ON observations(run_id, source_event_id);
CREATE TABLE artifacts (
  id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE RESTRICT, attempt_id TEXT,
  kind TEXT NOT NULL, locator TEXT NOT NULL, digest BLOB CHECK(digest IS NULL OR length(digest)=32), media_type TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL,
  FOREIGN KEY(run_id, attempt_id) REFERENCES attempts(run_id, id) ON DELETE RESTRICT
);
CREATE INDEX artifacts_run_idx ON artifacts(run_id); CREATE INDEX artifacts_attempt_idx ON artifacts(attempt_id);
CREATE TABLE checks (
  id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE RESTRICT, attempt_id TEXT,
  name TEXT NOT NULL, status TEXT NOT NULL CHECK(status IN ('pending','passed','failed','skipped')), evidence TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL,
  FOREIGN KEY(run_id, attempt_id) REFERENCES attempts(run_id, id) ON DELETE RESTRICT
);
CREATE INDEX checks_run_idx ON checks(run_id); CREATE INDEX checks_attempt_idx ON checks(attempt_id);
CREATE TABLE interventions (
  id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE RESTRICT, attempt_id TEXT,
  kind TEXT NOT NULL, reason TEXT NOT NULL, requested_by TEXT NOT NULL, created_at TEXT NOT NULL, resolved_at TEXT,
  FOREIGN KEY(run_id, attempt_id) REFERENCES attempts(run_id, id) ON DELETE RESTRICT
);
CREATE INDEX interventions_run_idx ON interventions(run_id); CREATE INDEX interventions_attempt_idx ON interventions(attempt_id);
CREATE TABLE handoffs (
  id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE RESTRICT, attempt_id TEXT,
  from_actor TEXT NOT NULL, to_actor TEXT NOT NULL, reason TEXT NOT NULL, status TEXT NOT NULL CHECK(status IN ('requested','accepted','declined','completed')),
  created_at TEXT NOT NULL, resolved_at TEXT,
  FOREIGN KEY(run_id, attempt_id) REFERENCES attempts(run_id, id) ON DELETE RESTRICT
);
CREATE INDEX handoffs_run_idx ON handoffs(run_id); CREATE INDEX handoffs_attempt_idx ON handoffs(attempt_id);
CREATE TABLE review_decisions (
  id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE RESTRICT, kind TEXT NOT NULL CHECK(kind IN ('accept','reject')),
  expected_run_version INTEGER NOT NULL CHECK(expected_run_version > 0), reviewer_note TEXT NOT NULL DEFAULT '', decided_at TEXT NOT NULL
);
CREATE INDEX review_decisions_run_idx ON review_decisions(run_id);
CREATE TABLE review_checks (
  review_id TEXT NOT NULL REFERENCES review_decisions(id) ON DELETE RESTRICT, position INTEGER NOT NULL CHECK(position >= 0),
  name TEXT NOT NULL, status TEXT NOT NULL, evidence TEXT NOT NULL DEFAULT '', PRIMARY KEY(review_id, position)
);
CREATE INDEX review_checks_review_idx ON review_checks(review_id);
CREATE TABLE review_artifacts (
  review_id TEXT NOT NULL REFERENCES review_decisions(id) ON DELETE RESTRICT, position INTEGER NOT NULL CHECK(position >= 0),
  artifact_link TEXT NOT NULL, PRIMARY KEY(review_id, position)
);
CREATE INDEX review_artifacts_review_idx ON review_artifacts(review_id);
