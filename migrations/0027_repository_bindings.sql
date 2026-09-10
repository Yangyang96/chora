CREATE TABLE repository_bindings (
  room_id TEXT PRIMARY KEY REFERENCES rooms(id) ON DELETE RESTRICT,
  name TEXT NOT NULL CHECK(length(trim(name)) > 0),
  local_locator TEXT NOT NULL,
  source_kind TEXT NOT NULL CHECK(source_kind IN ('open','clone')),
  clone_url TEXT NOT NULL DEFAULT '',
  admitted_base TEXT NOT NULL,
  base_identity TEXT NOT NULL,
  target_worktree TEXT NOT NULL,
  dirty_admitted INTEGER NOT NULL CHECK(dirty_admitted IN (0,1)),
  state TEXT NOT NULL DEFAULT 'active' CHECK(state IN ('active','removed')),
  version INTEGER NOT NULL DEFAULT 1 CHECK(version >= 1),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE INDEX repository_bindings_state_idx
  ON repository_bindings(state, updated_at DESC);

CREATE TRIGGER repository_bindings_insert_valid
BEFORE INSERT ON repository_bindings
WHEN NEW.state <> 'active' OR NEW.version <> 1 OR NEW.updated_at < NEW.created_at
BEGIN
  SELECT RAISE(ABORT, 'invalid initial repository binding');
END;

CREATE TRIGGER repository_bindings_binding_immutable
BEFORE UPDATE ON repository_bindings
WHEN OLD.room_id <> NEW.room_id
  OR OLD.name <> NEW.name
  OR OLD.local_locator <> NEW.local_locator
  OR OLD.source_kind <> NEW.source_kind
  OR OLD.clone_url <> NEW.clone_url
  OR OLD.admitted_base <> NEW.admitted_base
  OR OLD.base_identity <> NEW.base_identity
  OR OLD.target_worktree <> NEW.target_worktree
  OR OLD.dirty_admitted <> NEW.dirty_admitted
  OR OLD.created_at <> NEW.created_at
BEGIN
  SELECT RAISE(ABORT, 'repository binding is immutable');
END;

CREATE TRIGGER repository_bindings_transition
BEFORE UPDATE ON repository_bindings
WHEN NEW.version <> OLD.version + 1
  OR NEW.updated_at < OLD.updated_at
  OR NOT (OLD.state = 'active' AND NEW.state = 'removed')
BEGIN
  SELECT RAISE(ABORT, 'invalid repository binding transition');
END;

CREATE TRIGGER repository_bindings_immutable_delete
BEFORE DELETE ON repository_bindings
BEGIN
  SELECT RAISE(ABORT, 'repository binding is immutable');
END;
