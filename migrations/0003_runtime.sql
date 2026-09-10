CREATE TABLE runtime_sessions (
  id TEXT PRIMARY KEY, attempt_id TEXT NOT NULL REFERENCES attempts(id) ON DELETE RESTRICT, predecessor_session_id TEXT REFERENCES runtime_sessions(id) ON DELETE RESTRICT,
  adapter_id TEXT NOT NULL, runtime_kind TEXT NOT NULL, external_reference TEXT NOT NULL DEFAULT '', version INTEGER NOT NULL CHECK(version>=0),
  working_root TEXT NOT NULL, security_fingerprint BLOB NOT NULL CHECK(length(security_fingerprint)=32), process_identity TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL CHECK(state IN ('created','starting','running','stopping','stopped','failed','lost')),
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL, started_at TEXT, terminal_at TEXT
);
CREATE INDEX runtime_sessions_attempt_idx ON runtime_sessions(attempt_id); CREATE INDEX runtime_sessions_predecessor_idx ON runtime_sessions(predecessor_session_id); CREATE INDEX runtime_sessions_state_idx ON runtime_sessions(state);
CREATE TABLE runtime_stream_offsets (
  session_id TEXT NOT NULL REFERENCES runtime_sessions(id) ON DELETE RESTRICT, stream TEXT NOT NULL CHECK(stream IN ('stdout','stderr')),
  offset INTEGER NOT NULL CHECK(offset>=0), eof INTEGER NOT NULL CHECK(eof IN (0,1)), PRIMARY KEY(session_id,stream)
);
CREATE INDEX runtime_stream_offsets_session_idx ON runtime_stream_offsets(session_id);
