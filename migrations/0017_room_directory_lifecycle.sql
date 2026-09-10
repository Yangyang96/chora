ALTER TABLE rooms ADD COLUMN state TEXT NOT NULL DEFAULT 'active'
  CHECK(state IN ('active','archived'));
ALTER TABLE rooms ADD COLUMN version INTEGER NOT NULL DEFAULT 1
  CHECK(version >= 1);
ALTER TABLE rooms ADD COLUMN archived_at TEXT;

CREATE INDEX rooms_active_directory_idx
  ON rooms(state, updated_at DESC, id);
CREATE INDEX rooms_archived_directory_idx
  ON rooms(state, archived_at DESC, id);
CREATE INDEX runs_task_latest_idx
  ON runs(task_id, created_at DESC, id);

CREATE TRIGGER rooms_lifecycle_insert_valid
BEFORE INSERT ON rooms
WHEN NEW.state <> 'active' OR NEW.version <> 1 OR NEW.archived_at IS NOT NULL OR NEW.updated_at < NEW.created_at
BEGIN
  SELECT RAISE(ABORT, 'invalid initial room lifecycle');
END;

CREATE TABLE room_lifecycle_events (
  room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE RESTRICT,
  version INTEGER NOT NULL CHECK(version >= 2),
  from_state TEXT NOT NULL CHECK(from_state IN ('active','archived')),
  to_state TEXT NOT NULL CHECK(to_state IN ('active','archived') AND to_state <> from_state),
  actor_id TEXT NOT NULL CHECK(length(trim(actor_id)) > 0),
  session_id TEXT NOT NULL CHECK(length(trim(session_id)) > 0),
  idempotency_key_hash BLOB NOT NULL CHECK(length(idempotency_key_hash) = 32),
  request_digest BLOB NOT NULL CHECK(length(request_digest) = 32),
  occurred_at TEXT NOT NULL,
  PRIMARY KEY(room_id, version),
  UNIQUE(room_id, idempotency_key_hash)
);

CREATE INDEX room_lifecycle_events_room_time_idx
  ON room_lifecycle_events(room_id, occurred_at, version);

CREATE TRIGGER rooms_lifecycle_update_valid
BEFORE UPDATE ON rooms
WHEN NEW.id <> OLD.id
  OR NEW.name <> OLD.name
  OR NEW.description <> OLD.description
  OR NEW.workspace_root <> OLD.workspace_root
  OR NEW.created_at <> OLD.created_at
  OR NEW.version <> OLD.version + 1
  OR NEW.updated_at < OLD.updated_at
  OR NOT (
    (OLD.state = 'active' AND OLD.archived_at IS NULL
      AND NEW.state = 'active' AND NEW.archived_at IS NULL)
    OR
    (OLD.state = 'active' AND OLD.archived_at IS NULL
      AND NEW.state = 'archived' AND NEW.archived_at = NEW.updated_at
      AND EXISTS (
        SELECT 1 FROM room_lifecycle_events event
        WHERE event.room_id=NEW.id AND event.version=NEW.version
          AND event.from_state=OLD.state AND event.to_state=NEW.state
          AND event.occurred_at=NEW.updated_at
      ))
    OR
    (OLD.state = 'archived' AND OLD.archived_at IS NOT NULL
      AND NEW.state = 'active' AND NEW.archived_at IS NULL
      AND EXISTS (
        SELECT 1 FROM room_lifecycle_events event
        WHERE event.room_id=NEW.id AND event.version=NEW.version
          AND event.from_state=OLD.state AND event.to_state=NEW.state
          AND event.occurred_at=NEW.updated_at
      ))
  )
BEGIN
  SELECT RAISE(ABORT, 'invalid room lifecycle transition');
END;

CREATE TRIGGER room_lifecycle_event_exact
BEFORE INSERT ON room_lifecycle_events
WHEN NOT EXISTS (
  SELECT 1 FROM rooms room
  WHERE room.id = NEW.room_id
    AND room.version + 1 = NEW.version
    AND room.state = NEW.from_state
    AND room.updated_at <= NEW.occurred_at
    AND (
      (NEW.from_state = 'active' AND NEW.to_state = 'archived' AND room.archived_at IS NULL)
      OR
      (NEW.from_state = 'archived' AND NEW.to_state = 'active' AND room.archived_at IS NOT NULL)
    )
)
BEGIN
  SELECT RAISE(ABORT, 'room lifecycle event does not match room');
END;

CREATE TRIGGER room_lifecycle_event_apply
AFTER INSERT ON room_lifecycle_events
BEGIN
  UPDATE rooms
  SET state=NEW.to_state,
      version=NEW.version,
      updated_at=NEW.occurred_at,
      archived_at=CASE WHEN NEW.to_state='archived' THEN NEW.occurred_at ELSE NULL END
  WHERE id=NEW.room_id;
END;

CREATE TRIGGER room_lifecycle_events_no_update
BEFORE UPDATE ON room_lifecycle_events
BEGIN
  SELECT RAISE(ABORT, 'room lifecycle events are immutable');
END;

CREATE TRIGGER room_lifecycle_events_no_delete
BEFORE DELETE ON room_lifecycle_events
BEGIN
  SELECT RAISE(ABORT, 'room lifecycle events are immutable');
END;
