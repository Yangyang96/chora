ALTER TABLE runtime_sessions ADD COLUMN runtime_version TEXT NOT NULL DEFAULT '';
ALTER TABLE runtime_sessions ADD COLUMN runtime_fingerprint BLOB CHECK(runtime_fingerprint IS NULL OR length(runtime_fingerprint)=32);

ALTER TABLE artifacts ADD COLUMN description TEXT NOT NULL DEFAULT '';
ALTER TABLE artifacts ADD COLUMN position INTEGER NOT NULL DEFAULT 0 CHECK(position >= 0);
ALTER TABLE artifacts ADD COLUMN role TEXT NOT NULL DEFAULT 'output' CHECK(role IN ('output','candidate'));
ALTER TABLE artifacts ADD COLUMN source_event_id TEXT;

ALTER TABLE observations ADD COLUMN position INTEGER NOT NULL DEFAULT 0 CHECK(position >= 0);
ALTER TABLE observations ADD COLUMN role TEXT NOT NULL DEFAULT '';

CREATE TABLE checks_v6 (
  id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE RESTRICT, attempt_id TEXT,
  criterion_id TEXT NOT NULL, position INTEGER NOT NULL CHECK(position >= 0), name TEXT NOT NULL,
  status TEXT NOT NULL CHECK(status IN ('passed','failed','unknown')), evidence TEXT NOT NULL DEFAULT '',
  source_event_id TEXT, created_at TEXT NOT NULL,
  FOREIGN KEY(run_id, attempt_id) REFERENCES attempts(run_id, id) ON DELETE RESTRICT,
  FOREIGN KEY(run_id, source_event_id) REFERENCES run_events(run_id, id) ON DELETE RESTRICT
);
INSERT INTO checks_v6(id,run_id,attempt_id,criterion_id,position,name,status,evidence,created_at)
SELECT id,run_id,attempt_id,'',0,name,
  CASE status WHEN 'passed' THEN 'passed' WHEN 'failed' THEN 'failed' ELSE 'unknown' END,
  evidence,created_at FROM checks;
DROP TABLE checks;
ALTER TABLE checks_v6 RENAME TO checks;
CREATE INDEX checks_run_idx ON checks(run_id);
CREATE INDEX checks_attempt_idx ON checks(attempt_id);
CREATE INDEX checks_event_idx ON checks(run_id,source_event_id);

ALTER TABLE handoffs ADD COLUMN source_event_id TEXT;
