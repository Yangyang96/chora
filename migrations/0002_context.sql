CREATE TABLE context_entries (
  id TEXT PRIMARY KEY, room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE RESTRICT,
  kind TEXT NOT NULL CHECK(kind IN ('brief','decision','constraint','source_ref','unknown')), created_at TEXT NOT NULL
);
CREATE INDEX context_entries_room_idx ON context_entries(room_id);
CREATE TABLE context_revisions (
  id TEXT PRIMARY KEY, entry_id TEXT NOT NULL REFERENCES context_entries(id) ON DELETE RESTRICT, revision_number INTEGER NOT NULL CHECK(revision_number > 0),
  supersedes_revision_id TEXT REFERENCES context_revisions(id) ON DELETE RESTRICT, title TEXT NOT NULL, body TEXT NOT NULL DEFAULT '', locator TEXT NOT NULL DEFAULT '',
  sensitive INTEGER NOT NULL CHECK(sensitive IN (0,1)), created_at TEXT NOT NULL, updated_at TEXT NOT NULL, UNIQUE(entry_id, revision_number),
  UNIQUE(entry_id, id), CHECK((revision_number=1 AND supersedes_revision_id IS NULL) OR (revision_number>1 AND supersedes_revision_id IS NOT NULL)),
  FOREIGN KEY(entry_id, supersedes_revision_id) REFERENCES context_revisions(entry_id, id) ON DELETE RESTRICT
);
CREATE INDEX context_revisions_entry_idx ON context_revisions(entry_id); CREATE INDEX context_revisions_supersedes_idx ON context_revisions(supersedes_revision_id);
CREATE TABLE charter_context_selections (
  charter_id TEXT NOT NULL REFERENCES run_charters(id) ON DELETE RESTRICT, revision_id TEXT NOT NULL REFERENCES context_revisions(id) ON DELETE RESTRICT,
  position INTEGER NOT NULL CHECK(position >= 0), PRIMARY KEY(charter_id, revision_id), UNIQUE(charter_id, position)
);
CREATE INDEX charter_context_revision_idx ON charter_context_selections(revision_id);
CREATE TABLE charter_sensitive_confirmations (
  charter_id TEXT NOT NULL REFERENCES run_charters(id) ON DELETE RESTRICT, revision_id TEXT NOT NULL REFERENCES context_revisions(id) ON DELETE RESTRICT,
  PRIMARY KEY(charter_id, revision_id)
);
CREATE INDEX charter_confirmations_revision_idx ON charter_sensitive_confirmations(revision_id);
CREATE TABLE charter_sensitive_exclusions (
  charter_id TEXT NOT NULL REFERENCES run_charters(id) ON DELETE RESTRICT, entry_id TEXT NOT NULL REFERENCES context_entries(id) ON DELETE RESTRICT,
  PRIMARY KEY(charter_id, entry_id)
);
CREATE INDEX charter_exclusions_entry_idx ON charter_sensitive_exclusions(entry_id);
CREATE TABLE context_snapshots (
  id TEXT PRIMARY KEY, digest BLOB NOT NULL UNIQUE CHECK(length(digest)=32), canonical_json BLOB NOT NULL CHECK(json_valid(CAST(canonical_json AS TEXT))),
  markdown BLOB NOT NULL, created_at TEXT NOT NULL, UNIQUE(id,digest)
);
CREATE TABLE snapshot_inclusions (
  snapshot_id TEXT NOT NULL REFERENCES context_snapshots(id) ON DELETE RESTRICT, revision_id TEXT NOT NULL REFERENCES context_revisions(id) ON DELETE RESTRICT,
  position INTEGER NOT NULL CHECK(position >= 0), PRIMARY KEY(snapshot_id, revision_id), UNIQUE(snapshot_id, position)
);
CREATE INDEX snapshot_inclusions_revision_idx ON snapshot_inclusions(revision_id);
CREATE TABLE snapshot_exclusions (
  snapshot_id TEXT NOT NULL REFERENCES context_snapshots(id) ON DELETE RESTRICT, entry_id TEXT NOT NULL REFERENCES context_entries(id) ON DELETE RESTRICT,
  revision_id TEXT NOT NULL REFERENCES context_revisions(id) ON DELETE RESTRICT, position INTEGER NOT NULL CHECK(position >= 0), reason TEXT NOT NULL,
  PRIMARY KEY(snapshot_id, revision_id), UNIQUE(snapshot_id, position)
);
CREATE INDEX snapshot_exclusions_entry_idx ON snapshot_exclusions(entry_id); CREATE INDEX snapshot_exclusions_revision_idx ON snapshot_exclusions(revision_id);
CREATE TABLE context_promotions (
  revision_id TEXT PRIMARY KEY REFERENCES context_revisions(id) ON DELETE RESTRICT, command_digest BLOB NOT NULL CHECK(length(command_digest)=32),
  review_decision_id TEXT NOT NULL REFERENCES review_decisions(id) ON DELETE RESTRICT, review_kind TEXT NOT NULL CHECK(review_kind='accept'),
  review_expected_run_version INTEGER NOT NULL CHECK(review_expected_run_version>0), run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE RESTRICT,
  accepted_run_version INTEGER NOT NULL CHECK(accepted_run_version=review_expected_run_version+1), artifact_id TEXT NOT NULL REFERENCES artifacts(id) ON DELETE RESTRICT,
  event_id TEXT NOT NULL, authorization_id TEXT NOT NULL, authorization_binding BLOB NOT NULL CHECK(length(authorization_binding)=32), authorized_at TEXT NOT NULL,
  created_at TEXT NOT NULL, UNIQUE(revision_id, command_digest), FOREIGN KEY(run_id,event_id) REFERENCES run_events(run_id,id) ON DELETE RESTRICT
);
CREATE INDEX context_promotions_review_idx ON context_promotions(review_decision_id); CREATE INDEX context_promotions_run_idx ON context_promotions(run_id); CREATE INDEX context_promotions_artifact_idx ON context_promotions(artifact_id); CREATE INDEX context_promotions_event_idx ON context_promotions(run_id,event_id);
