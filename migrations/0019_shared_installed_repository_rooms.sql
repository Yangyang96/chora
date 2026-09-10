PRAGMA defer_foreign_keys = ON;

-- These triggers live on room_lifecycle_events but reference rooms in their
-- bodies. Remove them while the parent table is rebuilt so every intermediate
-- schema remains valid; their immutable-row siblings do not reference rooms.
DROP TRIGGER room_lifecycle_event_exact;
DROP TRIGGER room_lifecycle_event_apply;

CREATE TABLE rooms_v19 (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  workspace_root TEXT NOT NULL,
  state TEXT NOT NULL DEFAULT 'active' CHECK(state IN ('active','archived')),
  version INTEGER NOT NULL DEFAULT 1 CHECK(version >= 1),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  archived_at TEXT
);

INSERT INTO rooms_v19(
  id,name,description,workspace_root,state,version,created_at,updated_at,archived_at
)
SELECT
  id,name,description,workspace_root,state,version,created_at,updated_at,archived_at
FROM rooms;

DROP TABLE rooms;
ALTER TABLE rooms_v19 RENAME TO rooms;

CREATE INDEX rooms_active_directory_idx
  ON rooms(state, updated_at DESC, id);
CREATE INDEX rooms_archived_directory_idx
  ON rooms(state, archived_at DESC, id);

CREATE TRIGGER rooms_lifecycle_insert_valid
BEFORE INSERT ON rooms
WHEN NEW.state <> 'active' OR NEW.version <> 1 OR NEW.archived_at IS NOT NULL OR NEW.updated_at < NEW.created_at
BEGIN
  SELECT RAISE(ABORT, 'invalid initial room lifecycle');
END;

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
