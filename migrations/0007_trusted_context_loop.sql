CREATE TABLE candidates (
  id TEXT PRIMARY KEY,
  room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE RESTRICT,
  source_run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE RESTRICT,
  source_review_id TEXT NOT NULL REFERENCES review_decisions(id) ON DELETE RESTRICT,
  source_artifact_id TEXT NOT NULL REFERENCES artifacts(id) ON DELETE RESTRICT,
  title TEXT NOT NULL CHECK(chora_valid_trusted_text(CAST(title AS BLOB),200,1)),
  body TEXT NOT NULL CHECK(chora_valid_trusted_text(CAST(body AS BLOB),16000,1)),
  state TEXT NOT NULL CHECK(state IN ('pending','confirmed','dismissed')),
  version INTEGER NOT NULL CHECK(version > 0),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(source_run_id, source_artifact_id),
  UNIQUE(id, room_id)
);
CREATE INDEX candidates_room_idx ON candidates(room_id, created_at, id);
CREATE INDEX candidates_run_idx ON candidates(source_run_id, created_at, id);

CREATE TABLE candidate_edits (
  candidate_id TEXT NOT NULL REFERENCES candidates(id) ON DELETE RESTRICT,
  from_version INTEGER NOT NULL CHECK(from_version > 0),
  to_version INTEGER NOT NULL CHECK(to_version = from_version + 1),
  previous_title TEXT NOT NULL CHECK(chora_valid_trusted_text(CAST(previous_title AS BLOB),200,1)),
  previous_body TEXT NOT NULL CHECK(chora_valid_trusted_text(CAST(previous_body AS BLOB),16000,1)),
  edited_title TEXT NOT NULL CHECK(chora_valid_trusted_text(CAST(edited_title AS BLOB),200,1)),
  edited_body TEXT NOT NULL CHECK(chora_valid_trusted_text(CAST(edited_body AS BLOB),16000,1)),
  edited_at TEXT NOT NULL,
  PRIMARY KEY(candidate_id, to_version)
);

CREATE TABLE candidate_decisions (
  id TEXT PRIMARY KEY,
  candidate_id TEXT NOT NULL UNIQUE REFERENCES candidates(id) ON DELETE RESTRICT,
  kind TEXT NOT NULL CHECK(kind IN ('confirm','dismiss')),
  expected_candidate_version INTEGER NOT NULL CHECK(expected_candidate_version > 0),
  -- Keep the Unicode character limit aligned with domain.MaxCandidateDecisionNoteRunes.
  note TEXT NOT NULL DEFAULT '',
  actor_id TEXT NOT NULL CHECK(chora_valid_trusted_text(CAST(actor_id AS BLOB),200,1)),
  session_id TEXT NOT NULL CHECK(chora_valid_trusted_text(CAST(session_id AS BLOB),200,1)),
  decided_at TEXT NOT NULL,
  CHECK(chora_valid_trusted_text(CAST(note AS BLOB),2000,kind='dismiss'))
);
CREATE INDEX candidate_decisions_candidate_idx ON candidate_decisions(candidate_id);

CREATE TABLE room_revision_records (
  revision_id TEXT PRIMARY KEY REFERENCES context_revisions(id) ON DELETE RESTRICT,
  room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE RESTRICT,
  provenance_kind TEXT NOT NULL CHECK(provenance_kind IN ('human_room','accepted_run_candidate')),
  candidate_id TEXT REFERENCES candidates(id) ON DELETE RESTRICT,
  candidate_decision_id TEXT REFERENCES candidate_decisions(id) ON DELETE RESTRICT,
  source_run_id TEXT REFERENCES runs(id) ON DELETE RESTRICT,
  source_review_id TEXT REFERENCES review_decisions(id) ON DELETE RESTRICT,
  source_artifact_id TEXT REFERENCES artifacts(id) ON DELETE RESTRICT,
  actor TEXT NOT NULL CHECK(chora_valid_trusted_text(CAST(actor AS BLOB),200,1)),
  digest BLOB NOT NULL CHECK(length(digest)=32),
  confirmed_at TEXT NOT NULL,
  UNIQUE(room_id, revision_id),
  UNIQUE(candidate_id),
  UNIQUE(candidate_decision_id),
  CHECK(
    (provenance_kind='human_room' AND candidate_id IS NULL AND candidate_decision_id IS NULL AND source_run_id IS NULL AND source_review_id IS NULL AND source_artifact_id IS NULL)
    OR
    (provenance_kind='accepted_run_candidate' AND candidate_id IS NOT NULL AND candidate_decision_id IS NOT NULL AND source_run_id IS NOT NULL AND source_review_id IS NOT NULL AND source_artifact_id IS NOT NULL)
  )
);
CREATE INDEX room_revision_records_room_idx ON room_revision_records(room_id, confirmed_at, revision_id);
CREATE INDEX room_revision_records_run_idx ON room_revision_records(source_run_id);

CREATE TABLE task_revision_selections (
  task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE RESTRICT,
  room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE RESTRICT,
  revision_id TEXT NOT NULL,
  position INTEGER NOT NULL CHECK(position >= 0),
  digest BLOB NOT NULL CHECK(length(digest)=32),
  provenance_json BLOB NOT NULL CHECK(chora_valid_revision_provenance(CAST(provenance_json AS BLOB))),
  selected_at TEXT NOT NULL,
  PRIMARY KEY(task_id, revision_id),
  UNIQUE(task_id, position),
  FOREIGN KEY(room_id, revision_id) REFERENCES room_revision_records(room_id, revision_id) ON DELETE RESTRICT
);
CREATE INDEX task_revision_selections_revision_idx ON task_revision_selections(revision_id);

CREATE TABLE task_revision_exclusions (
  task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE RESTRICT,
  room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE RESTRICT,
  revision_id TEXT NOT NULL,
  position INTEGER NOT NULL CHECK(position >= 0),
  digest BLOB NOT NULL CHECK(length(digest)=32),
  provenance_json BLOB NOT NULL CHECK(chora_valid_revision_provenance(CAST(provenance_json AS BLOB))),
  reason TEXT NOT NULL CHECK(chora_valid_trusted_text(CAST(reason AS BLOB),2000,1)),
  selected_at TEXT NOT NULL,
  PRIMARY KEY(task_id, revision_id),
  UNIQUE(task_id, position),
  FOREIGN KEY(room_id, revision_id) REFERENCES room_revision_records(room_id, revision_id) ON DELETE RESTRICT
);
CREATE INDEX task_revision_exclusions_revision_idx ON task_revision_exclusions(revision_id);

CREATE TABLE snapshot_revision_manifest (
  snapshot_id TEXT NOT NULL REFERENCES context_snapshots(id) ON DELETE RESTRICT,
  revision_id TEXT NOT NULL REFERENCES room_revision_records(revision_id) ON DELETE RESTRICT,
  disposition TEXT NOT NULL CHECK(disposition IN ('selected','excluded')),
  position INTEGER NOT NULL CHECK(position >= 0),
  digest BLOB NOT NULL CHECK(length(digest)=32),
  provenance_json BLOB NOT NULL CHECK(chora_valid_revision_provenance(CAST(provenance_json AS BLOB))),
  reason TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(snapshot_id, revision_id),
  UNIQUE(snapshot_id, disposition, position),
  CHECK(chora_valid_trusted_text(CAST(reason AS BLOB),2000,disposition='excluded')),
  CHECK((disposition='selected' AND reason='') OR disposition='excluded')
);
CREATE INDEX snapshot_revision_manifest_revision_idx ON snapshot_revision_manifest(revision_id);

-- Rooms created before the trusted-context loop had no selectable Room Revision.
-- Reuse the Room's UUIDv7 payload under the context ID namespaces so the
-- backfill is deterministic and safe to retry as one migration transaction.
INSERT INTO context_entries(id,room_id,kind,created_at)
SELECT 'context_entry_' || substr(id,6),id,'brief',created_at FROM rooms;

INSERT INTO context_revisions(id,entry_id,revision_number,supersedes_revision_id,title,body,locator,sensitive,created_at,updated_at)
SELECT 'context_revision_' || substr(id,6),'context_entry_' || substr(id,6),1,NULL,'Room Brief',description,'room://' || id || '/brief',0,created_at,created_at FROM rooms;

INSERT INTO room_revision_records(revision_id,room_id,provenance_kind,candidate_id,candidate_decision_id,source_run_id,source_review_id,source_artifact_id,actor,digest,confirmed_at)
SELECT 'context_revision_' || substr(id,6),id,'human_room',NULL,NULL,NULL,NULL,NULL,'migration:0007',
  chora_room_revision_digest('context_revision_' || substr(id,6),id,'context_entry_' || substr(id,6),'brief','Room Brief',description,'room://' || id || '/brief',created_at,'migration:0007'),
  created_at
FROM rooms;

-- A V6 Task with no Run has no historical Charter or Snapshot whose context
-- choice could conflict with this deterministic initial selection. Tasks that
-- already have any Run are deliberately left without a selection so later
-- execution fails closed rather than inventing trusted history.
INSERT INTO task_revision_selections(task_id,room_id,revision_id,position,digest,provenance_json,selected_at)
SELECT t.id,t.room_id,rr.revision_id,0,rr.digest,
  json_object('kind','human_room','actor',rr.actor),t.created_at
FROM tasks t
JOIN room_revision_records rr ON rr.room_id=t.room_id AND rr.provenance_kind='human_room'
WHERE t.state='open'
  AND NOT EXISTS(SELECT 1 FROM run_charters c WHERE c.task_id=t.id)
  AND NOT EXISTS(SELECT 1 FROM runs r WHERE r.task_id=t.id);

CREATE TRIGGER room_revision_records_immutable_update BEFORE UPDATE ON room_revision_records BEGIN SELECT RAISE(ABORT,'room revision is immutable'); END;
CREATE TRIGGER room_revision_records_immutable_delete BEFORE DELETE ON room_revision_records BEGIN SELECT RAISE(ABORT,'room revision is immutable'); END;
CREATE TRIGGER confirmed_context_revision_immutable_update BEFORE UPDATE ON context_revisions WHEN EXISTS(SELECT 1 FROM room_revision_records WHERE revision_id=OLD.id) BEGIN SELECT RAISE(ABORT,'confirmed context revision is immutable'); END;
CREATE TRIGGER confirmed_context_revision_immutable_delete BEFORE DELETE ON context_revisions WHEN EXISTS(SELECT 1 FROM room_revision_records WHERE revision_id=OLD.id) BEGIN SELECT RAISE(ABORT,'confirmed context revision is immutable'); END;
CREATE TRIGGER candidate_decisions_immutable_update BEFORE UPDATE ON candidate_decisions BEGIN SELECT RAISE(ABORT,'candidate decision is immutable'); END;
CREATE TRIGGER candidate_decisions_immutable_delete BEFORE DELETE ON candidate_decisions BEGIN SELECT RAISE(ABORT,'candidate decision is immutable'); END;
CREATE TRIGGER candidate_edits_immutable_update BEFORE UPDATE ON candidate_edits BEGIN SELECT RAISE(ABORT,'candidate edit audit is immutable'); END;
CREATE TRIGGER candidate_edits_immutable_delete BEFORE DELETE ON candidate_edits BEGIN SELECT RAISE(ABORT,'candidate edit audit is immutable'); END;
CREATE TRIGGER task_revision_selections_immutable_update BEFORE UPDATE ON task_revision_selections BEGIN SELECT RAISE(ABORT,'task revision selection is immutable'); END;
CREATE TRIGGER task_revision_selections_immutable_delete BEFORE DELETE ON task_revision_selections BEGIN SELECT RAISE(ABORT,'task revision selection is immutable'); END;
CREATE TRIGGER task_revision_exclusions_immutable_update BEFORE UPDATE ON task_revision_exclusions BEGIN SELECT RAISE(ABORT,'task revision exclusion is immutable'); END;
CREATE TRIGGER task_revision_exclusions_immutable_delete BEFORE DELETE ON task_revision_exclusions BEGIN SELECT RAISE(ABORT,'task revision exclusion is immutable'); END;
CREATE TRIGGER snapshot_revision_manifest_immutable_update BEFORE UPDATE ON snapshot_revision_manifest BEGIN SELECT RAISE(ABORT,'snapshot revision manifest is immutable'); END;
CREATE TRIGGER snapshot_revision_manifest_immutable_delete BEFORE DELETE ON snapshot_revision_manifest BEGIN SELECT RAISE(ABORT,'snapshot revision manifest is immutable'); END;
CREATE TRIGGER trusted_context_snapshot_immutable_update BEFORE UPDATE ON context_snapshots WHEN EXISTS(SELECT 1 FROM snapshot_revision_manifest WHERE snapshot_id=OLD.id) BEGIN SELECT RAISE(ABORT,'trusted context snapshot is immutable'); END;
CREATE TRIGGER trusted_context_snapshot_immutable_delete BEFORE DELETE ON context_snapshots WHEN EXISTS(SELECT 1 FROM snapshot_revision_manifest WHERE snapshot_id=OLD.id) BEGIN SELECT RAISE(ABORT,'trusted context snapshot is immutable'); END;
