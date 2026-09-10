CREATE TABLE command_idempotency (
  key_hash BLOB PRIMARY KEY CHECK(length(key_hash)=32), command TEXT NOT NULL, resource_id TEXT NOT NULL,
  request_digest BLOB NOT NULL CHECK(length(request_digest)=32), response_status INTEGER NOT NULL CHECK(response_status BETWEEN 100 AND 599),
  response_content_type TEXT NOT NULL, response_body BLOB NOT NULL, created_at TEXT NOT NULL, expires_at TEXT
);
CREATE INDEX command_idempotency_resource_idx ON command_idempotency(command,resource_id);
CREATE INDEX command_idempotency_expiry_idx ON command_idempotency(expires_at);
