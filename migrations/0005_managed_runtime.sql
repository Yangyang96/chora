ALTER TABLE runtime_sessions ADD COLUMN stop_intent TEXT NOT NULL DEFAULT '';
ALTER TABLE runtime_sessions ADD COLUMN diagnostic TEXT NOT NULL DEFAULT '';
ALTER TABLE runtime_sessions ADD COLUMN finalized_at TEXT;
ALTER TABLE runtime_sessions ADD COLUMN launch_token TEXT NOT NULL DEFAULT '';
