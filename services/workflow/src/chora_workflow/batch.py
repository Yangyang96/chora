"""Single-host acceptance budget shared by all processes using one external ledger."""

import json
import os
import sqlite3
import time
from contextlib import contextmanager
from pathlib import Path
from uuid import uuid4

from .protocol import digest

POLICY = {
    "version": 1,
    "max_requests": 20,
    "max_tokens": 100000,
    "max_output_tokens": 2048,
    "duration_seconds": 1200,
    "input_bound": "text-json-utf8-framing.v1",
}


class BatchRefused(RuntimeError):
    def __init__(self, category, *, total_tokens=None):
        self.category = category
        self.total_tokens = total_tokens
        super().__init__(category)


def input_bound(payload):
    # Only the harness's text/function subset is bounded. Twice the escaped
    # JSON byte length covers text/schema serialization, with 4096 tokens per
    # framing element for the published V4 non-thinking tool template.
    # Multimodal inputs and server-side tools have no such bound here.
    for message in payload["messages"]:
        if (
            message.get("role") not in {"system", "user", "assistant", "tool"}
            or not isinstance(message.get("content"), (str, type(None)))
            or set(message) - {"role", "content", "tool_calls", "tool_call_id"}
        ):
            raise BatchRefused("unbounded_input")
        calls = message.get("tool_calls", [])
        if len(calls) > 4:
            raise BatchRefused("unbounded_input")
        for call in calls:
            arguments = json.loads(call["function"]["arguments"])
            if not isinstance(arguments, dict) or len(arguments) > 3:
                raise BatchRefused("unbounded_input")
    if any(tool.get("type") != "function" for tool in payload["tools"]):
        raise BatchRefused("unbounded_input")
    encoded = json.dumps(payload, ensure_ascii=True, separators=(",", ":"), allow_nan=False)
    return 2 * len(encoded.encode("utf-8")) + 4096 * (
        1 + len(payload["messages"]) + len(payload["tools"])
    )


class BatchLedger:
    def __init__(self, path):
        self.path = Path(path).resolve()
        with self.connection() as conn:
            row = conn.execute("SELECT * FROM batch").fetchone()
            if row is None or json.loads(row["policy"]) != POLICY:
                raise ValueError("acceptance batch policy changed")
            self.identity = {"batch_id": row["batch_id"], "policy": POLICY.copy()}

    @staticmethod
    def create(path):
        # Exclusive creation deliberately refuses reinitialization of a batch.
        path = Path(path).resolve()
        descriptor = os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
        os.close(descriptor)
        with sqlite3.connect(path) as conn:
            conn.executescript("""
                CREATE TABLE batch (
                    batch_id TEXT PRIMARY KEY, policy TEXT NOT NULL,
                    first_request_at REAL, last_observed_at REAL, halt_reason TEXT
                );
                CREATE TABLE requests (
                    request_id TEXT PRIMARY KEY, request_digest TEXT NOT NULL,
                    requested_at REAL NOT NULL, completed_at REAL,
                    input_bound INTEGER NOT NULL, output_limit INTEGER NOT NULL,
                    usage TEXT, outcome TEXT, requests_remaining INTEGER NOT NULL,
                    tokens_remaining INTEGER, deadline REAL NOT NULL
                );
            """)
            conn.execute(
                "INSERT INTO batch(batch_id, policy) VALUES (?,?)",
                (str(uuid4()), json.dumps(POLICY, sort_keys=True)),
            )
        return BatchLedger(path)

    @contextmanager
    def connection(self):
        # mode=rw prevents a typo from silently creating a fresh budget.
        conn = sqlite3.connect(self.path.as_uri() + "?mode=rw", uri=True, timeout=10)
        conn.row_factory = sqlite3.Row
        try:
            with conn:
                yield conn
        finally:
            conn.close()

    def snapshot(self):
        with self.connection() as conn:
            conn.execute("BEGIN IMMEDIATE")
            return {
                "batch": dict(conn.execute("SELECT * FROM batch").fetchone()),
                "requests": [
                    dict(row)
                    for row in conn.execute("SELECT * FROM requests ORDER BY requested_at, rowid")
                ],
            }

    def reserve(self, payload):
        bound = input_bound(payload)
        output = payload["max_tokens"]
        if type(output) is not int or not 1 <= output <= POLICY["max_output_tokens"]:
            raise BatchRefused("output_budget")
        with self.connection() as conn:
            conn.execute("BEGIN IMMEDIATE")
            now = time.time()
            batch = conn.execute("SELECT * FROM batch").fetchone()
            rows = conn.execute("SELECT usage, completed_at FROM requests").fetchall()
            if batch["halt_reason"]:
                raise BatchRefused(batch["halt_reason"])
            if any(row["completed_at"] is None for row in rows):
                raise BatchRefused("outcome_unknown")
            if batch["last_observed_at"] is not None and now < batch["last_observed_at"]:
                raise BatchRefused("batch_clock_regressed")
            first = batch["first_request_at"] if batch["first_request_at"] is not None else now
            deadline = first + POLICY["duration_seconds"]
            if now >= deadline:
                raise BatchRefused("deadline")
            if len(rows) >= POLICY["max_requests"]:
                raise BatchRefused("request_budget")
            spent = sum(json.loads(row["usage"])["total_tokens"] for row in rows)
            if spent + bound + output > POLICY["max_tokens"]:
                raise BatchRefused("token_budget")
            reservation = {
                "request_id": str(uuid4()),
                "deadline": deadline,
                "input_bound": bound,
            }
            conn.execute(
                "INSERT INTO requests(request_id, request_digest, requested_at, input_bound, "
                "output_limit, requests_remaining, tokens_remaining, deadline) "
                "VALUES (?,?,?,?,?,?,?,?)",
                (
                    reservation["request_id"],
                    digest(payload),
                    now,
                    bound,
                    output,
                    POLICY["max_requests"] - len(rows) - 1,
                    POLICY["max_tokens"] - spent - bound - output,
                    deadline,
                ),
            )
            conn.execute("UPDATE batch SET first_request_at=?, last_observed_at=?", (first, now))
        return reservation

    def finish(self, request_id, usage, outcome):
        valid = (
            isinstance(usage, dict)
            and all(
                type(usage.get(key)) is int and usage[key] >= 0
                for key in ("prompt_tokens", "completion_tokens", "total_tokens")
            )
            and usage["total_tokens"] == usage["prompt_tokens"] + usage["completion_tokens"]
        )
        with self.connection() as conn:
            conn.execute("BEGIN IMMEDIATE")
            now = time.time()
            row = conn.execute(
                "SELECT * FROM requests WHERE request_id=?", (request_id,)
            ).fetchone()
            if row is None or row["completed_at"] is not None:
                raise ValueError("acceptance reservation already completed or missing")
            reason = None
            if not valid:
                reason = "usage_unknown"
            elif (
                usage["prompt_tokens"] > row["input_bound"]
                or usage["completion_tokens"] > row["output_limit"]
            ):
                reason = "provider_budget_violation"
            elif now >= row["deadline"]:
                reason = "deadline"
            elif now < row["requested_at"]:
                reason = "batch_clock_regressed"
            previous = conn.execute(
                "SELECT usage FROM requests WHERE completed_at IS NOT NULL AND usage IS NOT NULL"
            ).fetchall()
            spent = sum(json.loads(item["usage"])["total_tokens"] for item in previous)
            remaining = POLICY["max_tokens"] - spent - usage["total_tokens"] if valid else None
            conn.execute(
                "UPDATE requests SET completed_at=?, usage=?, outcome=?, tokens_remaining=? "
                "WHERE request_id=?",
                (now, json.dumps(usage) if valid else None, outcome, remaining, request_id),
            )
            conn.execute("UPDATE batch SET halt_reason=?, last_observed_at=?", (reason, now))
        if reason:
            raise BatchRefused(reason, total_tokens=usage["total_tokens"] if valid else None)
