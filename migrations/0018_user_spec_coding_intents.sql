CREATE TABLE user_spec_coding_intents (
  task_id TEXT PRIMARY KEY REFERENCES tasks(id) ON DELETE RESTRICT,
  intent_digest BLOB NOT NULL UNIQUE CHECK(length(intent_digest) = 32),
  intent_json BLOB NOT NULL CHECK(length(intent_json) > 0 AND json_valid(CAST(intent_json AS TEXT))),
  created_at TEXT NOT NULL
);

CREATE TRIGGER user_spec_coding_intents_no_update
BEFORE UPDATE ON user_spec_coding_intents
BEGIN
  SELECT RAISE(ABORT, 'user spec coding intent is immutable');
END;

CREATE TRIGGER user_spec_coding_intents_no_delete
BEFORE DELETE ON user_spec_coding_intents
BEGIN
  SELECT RAISE(ABORT, 'user spec coding intent is immutable');
END;
