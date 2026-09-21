CREATE UNIQUE INDEX IF NOT EXISTS resource_result_groups_identity_digest_idx ON resource_result_groups(result_id,digest);
CREATE TABLE IF NOT EXISTS project_document_revisions (
  id TEXT PRIMARY KEY,
  task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE RESTRICT,
  room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE RESTRICT,
  revision_number INTEGER NOT NULL CHECK(revision_number > 0),
  kind TEXT NOT NULL CHECK(kind IN ('agent_initial','human_edit')),
  body TEXT NOT NULL CHECK(length(CAST(body AS BLOB)) BETWEEN 1 AND 65536 AND instr(body,char(0))=0),
  body_digest BLOB NOT NULL CHECK(length(body_digest)=32),
  source_run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE RESTRICT,
  source_attempt_id TEXT NOT NULL REFERENCES attempts(id) ON DELETE RESTRICT,
  source_result_id TEXT NOT NULL REFERENCES resource_result_groups(result_id) ON DELETE RESTRICT,
  source_agent_report_id TEXT NOT NULL REFERENCES agent_reports(id) ON DELETE RESTRICT,
  source_event_id TEXT NOT NULL,
  source_event_sequence INTEGER NOT NULL CHECK(source_event_sequence > 0),
  source_result_digest BLOB NOT NULL CHECK(length(source_result_digest)=32),
  source_text_digest BLOB NOT NULL CHECK(length(source_text_digest)=32),
  edit_note TEXT NOT NULL DEFAULT '', actor_id TEXT NOT NULL, created_at TEXT NOT NULL,
  UNIQUE(task_id,revision_number),
  FOREIGN KEY(source_run_id,source_event_id) REFERENCES run_events(run_id,id) ON DELETE RESTRICT,
  FOREIGN KEY(source_result_id,source_result_digest) REFERENCES resource_result_groups(result_id,digest) ON DELETE RESTRICT
);
CREATE INDEX IF NOT EXISTS project_document_revisions_task_idx ON project_document_revisions(task_id,revision_number);
CREATE UNIQUE INDEX IF NOT EXISTS project_document_agent_source_idx ON project_document_revisions(task_id,source_attempt_id) WHERE kind='agent_initial';

CREATE TABLE IF NOT EXISTS project_document_reviews (
  id TEXT PRIMARY KEY,
  revision_id TEXT NOT NULL REFERENCES project_document_revisions(id) ON DELETE RESTRICT,
  task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE RESTRICT,
  expected_version INTEGER NOT NULL CHECK(expected_version > 0),
  kind TEXT NOT NULL CHECK(kind IN ('accept','reject')),
  note TEXT NOT NULL DEFAULT '', actor_id TEXT NOT NULL, session_id TEXT NOT NULL,
  context_revision_id TEXT REFERENCES room_revision_records(revision_id) ON DELETE RESTRICT,
  decided_at TEXT NOT NULL,
  UNIQUE(revision_id), CHECK((kind='accept')=(context_revision_id IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS project_document_reviews_task_idx ON project_document_reviews(task_id,decided_at);
CREATE TRIGGER IF NOT EXISTS project_document_revisions_no_update BEFORE UPDATE ON project_document_revisions BEGIN SELECT RAISE(ABORT,'Project document revisions are immutable'); END;
CREATE TRIGGER IF NOT EXISTS project_document_revisions_no_delete BEFORE DELETE ON project_document_revisions BEGIN SELECT RAISE(ABORT,'Project document revisions are durable'); END;
CREATE TRIGGER IF NOT EXISTS project_document_reviews_no_update BEFORE UPDATE ON project_document_reviews BEGIN SELECT RAISE(ABORT,'Project document reviews are immutable'); END;
CREATE TRIGGER IF NOT EXISTS project_document_reviews_no_delete BEFORE DELETE ON project_document_reviews BEGIN SELECT RAISE(ABORT,'Project document reviews are durable'); END;
