"""Single-host acceptance budget shared by all processes using one external ledger."""

import json
import os
import sqlite3
import time
from contextlib import closing, contextmanager
from pathlib import Path
from uuid import UUID, uuid4
from typing import Literal

from pydantic import Field, model_validator

from .protocol import StrictModel, digest
from .tools import TOOL_SCHEMAS

POLICY = {
    "version": 2,
    "max_requests": 20,
    "max_tokens": 100000,
    "max_output_tokens": 2048,
    "duration_seconds": 1200,
    "input_bound": "text-json-utf8-framing.v2",
    "encoder_revision": "0e1a0e5e52aea73055f50fef6f2423db370265b6",
    "encoder_sha256": "bdbd57c132a1b3725042323d02b98b9d1df28e5f388f134399555d041f5055e0",
    "tokenizer_sha256": "8f9f37ca37fdc4f5fd36d5cf4d3b0e8392edb4e894fd10cc0d70b4957c8633cf",
    "tool_schema_digest": digest(TOOL_SCHEMAS),
    "json_byte_multiplier": 2,
    "global_framing_bytes": 4096,
    "message_framing_bytes": 128,
    "call_framing_bytes": 128,
    "argument_framing_bytes": 128,
}


LEGACY_POLICY = {
    "version": 1,
    "max_requests": 20,
    "max_tokens": 100000,
    "max_output_tokens": 2048,
    "duration_seconds": 1200,
    "input_bound": "text-json-utf8-framing.v1",
}


class AdmissionStop(StrictModel):
    reason: Literal["input_admission"]
    occurred_at: float = Field(gt=0, allow_inf_nan=False)
    evidence_ref: str = Field(min_length=1)


class ContinuationAuthorization(StrictModel):
    """Operator attestations checked against an immutable pre-continuation snapshot."""

    version: Literal[1]
    batch_id: str
    authorization_ref: str = Field(min_length=1)
    expected_history_digest: str = Field(pattern=r"^[0-9a-f]{64}$")
    expected_requests: int = Field(ge=1, le=20)
    expected_tokens: int = Field(ge=0, le=100000)
    expected_policy: dict
    new_policy: dict
    candidate_revision: str = Field(pattern=r"^[0-9a-f]{40}$")
    previous_workflow_id: str
    external_stop: AdmissionStop
    quality_check_ref: str = Field(min_length=1)
    review_check_ref: str = Field(min_length=1)

    @model_validator(mode="after")
    def validate_transition(self):
        UUID(self.batch_id)
        UUID(self.previous_workflow_id)
        if self.expected_policy != LEGACY_POLICY or self.new_policy != POLICY:
            raise ValueError("unsupported acceptance policy transition")
        return self


def known_usage(row):
    usage = json.loads(row["usage"]) if row["usage"] is not None else None
    if (
        not isinstance(usage, dict)
        or any(
            type(usage.get(key)) is not int or usage[key] < 0
            for key in ("prompt_tokens", "completion_tokens", "total_tokens")
        )
        or usage["total_tokens"] != usage["prompt_tokens"] + usage["completion_tokens"]
    ):
        raise BatchRefused("usage_unknown")
    return usage


def accounted_tokens(rows):
    if any(
        row["completed_at"] is None
        or row["outcome"] not in {"response", "not_dispatched", "fixture"}
        for row in rows
    ):
        raise BatchRefused("outcome_unknown")
    return sum(known_usage(row)["total_tokens"] for row in rows)


class BatchRefused(RuntimeError):
    def __init__(self, category, *, total_tokens=None):
        self.category = category
        self.total_tokens = total_tokens
        super().__init__(category)


def input_bound(payload):
    """Byte envelope for the pinned non-thinking renderer; see INPUT_BOUND.md."""
    try:
        return _input_bound(payload)
    except (KeyError, TypeError, ValueError, OverflowError, RecursionError, UnicodeError):
        raise BatchRefused("unbounded_input") from None


def _input_bound(payload):
    if (
        not isinstance(payload, dict)
        or set(payload)
        - {"model", "messages", "tools", "tool_choice", "max_tokens", "thinking", "stream"}
        or payload["model"] != "deepseek-v4-pro"
        or payload["thinking"] != {"type": "disabled"}
        or payload.get("stream", False) is not False
        or payload.get("tool_choice", "required") != "required"
        or not isinstance(payload["messages"], list)
        or payload["tools"] not in ([], TOOL_SCHEMAS)
    ):
        raise BatchRefused("unbounded_input")
    call_count = argument_count = 0
    for message in payload["messages"]:
        if (
            not isinstance(message, dict)
            or message.get("role") not in {"system", "user", "assistant", "tool"}
            or not isinstance(message.get("content"), (str, type(None)))
            or set(message) - {"role", "content", "tool_calls", "tool_call_id"}
        ):
            raise BatchRefused("unbounded_input")
        calls = message.get("tool_calls", [])
        if (
            not isinstance(calls, list)
            or len(calls) > 4
            or (calls and message["role"] != "assistant")
        ):
            raise BatchRefused("unbounded_input")
        for call in calls:
            if (
                not isinstance(call, dict)
                or set(call) - {"id", "type", "function"}
                or call["type"] != "function"
                or not isinstance(call["id"], str)
                or not isinstance(call["function"], dict)
                or set(call["function"]) != {"name", "arguments"}
                or not isinstance(call["function"]["name"], str)
                or not isinstance(call["function"]["arguments"], str)
            ):
                raise BatchRefused("unbounded_input")
            arguments = json.loads(call["function"]["arguments"])
            if not isinstance(arguments, dict) or len(arguments) > 3:
                raise BatchRefused("unbounded_input")
            json.dumps(arguments, ensure_ascii=False, allow_nan=False).encode("utf-8")
            call_count += 1
            argument_count += len(arguments)
    encoded = json.dumps(payload, ensure_ascii=True, separators=(",", ":"), allow_nan=False)
    json.dumps(payload, ensure_ascii=False, allow_nan=False).encode("utf-8")
    # Text/schema/argument bytes are covered by 2B. Fixed additions are charged
    # once per actual construct, rather than repeating the whole tools template.
    return (
        POLICY["json_byte_multiplier"] * len(encoded.encode("utf-8"))
        + POLICY["global_framing_bytes"]
        + POLICY["message_framing_bytes"] * len(payload["messages"])
        + POLICY["call_framing_bytes"] * call_count
        + POLICY["argument_framing_bytes"] * argument_count
    )


class BatchLedger:
    def __init__(self, path):
        self.path = Path(path).resolve()
        snapshot = self.inspect(self.path)
        self.identity = self.effective_identity(snapshot)

    @staticmethod
    def effective_identity(snapshot):
        row = snapshot["batch"]
        continuation = snapshot.get("continuation")
        policy = json.loads(row["policy"])
        identity = {"batch_id": row["batch_id"], "policy": POLICY.copy()}
        if continuation:
            authorization = ContinuationAuthorization.model_validate_json(
                continuation["authorization"]
            )
            if (
                policy != authorization.expected_policy
                or row["batch_id"] != authorization.batch_id
                or digest(json.loads(continuation["original_snapshot"]))
                != authorization.expected_history_digest
            ):
                raise ValueError("acceptance continuation identity changed")
            identity["continuation"] = {
                "authorization_digest": digest(authorization.model_dump()),
                "candidate_revision": authorization.candidate_revision,
                "previous_workflow_id": authorization.previous_workflow_id,
            }
        elif policy != POLICY:
            raise ValueError("acceptance batch policy changed")
        return identity

    @staticmethod
    def read_snapshot(conn):
        row = conn.execute("SELECT * FROM batch").fetchall()
        if len(row) != 1:
            raise ValueError("acceptance batch missing or ambiguous")
        snapshot = {
            "batch": dict(row[0]),
            "requests": [
                dict(item)
                for item in conn.execute("SELECT * FROM requests ORDER BY requested_at, rowid")
            ],
        }
        if conn.execute(
            "SELECT 1 FROM sqlite_master WHERE type='table' AND name='continuation'"
        ).fetchone():
            rows = conn.execute("SELECT * FROM continuation").fetchall()
            if len(rows) != 1 or rows[0]["batch_id"] != row[0]["batch_id"]:
                raise ValueError("acceptance continuation missing or ambiguous")
            snapshot["continuation"] = dict(rows[0])
            snapshot["continuation_requests"] = [
                dict(item)
                for item in conn.execute("SELECT * FROM continuation_requests ORDER BY rowid")
            ]
        return snapshot

    @staticmethod
    def inspect(path):
        """Read old and current ledgers without creating schemas or starting a clock."""
        path = Path(path).resolve()
        with closing(sqlite3.connect(path.as_uri() + "?mode=ro", uri=True, timeout=10)) as conn:
            conn.row_factory = sqlite3.Row
            conn.execute("BEGIN")
            return BatchLedger.read_snapshot(conn)

    @classmethod
    def register_continuation(cls, path, authorization):
        authorization = ContinuationAuthorization.model_validate(authorization)
        # Opening the legacy ledger here is explicit. Normal constructors still
        # reject v1 unless this audited compatibility operation has committed.
        ledger = object.__new__(cls)
        ledger.path = Path(path).resolve()
        with ledger.connection() as conn:
            conn.execute("BEGIN IMMEDIATE")
            snapshot = cls.read_snapshot(conn)
            existing = snapshot.get("continuation")
            if existing:
                if json.loads(existing["authorization"]) != authorization.model_dump():
                    raise ValueError("acceptance continuation already registered; conflict")
                return snapshot  # Idempotent even after activation or further usage.
            batch = snapshot["batch"]
            rows = snapshot["requests"]
            spent = accounted_tokens(rows)
            now = time.time()
            if (
                batch["batch_id"] != authorization.batch_id
                or digest(snapshot) != authorization.expected_history_digest
                or len(rows) != authorization.expected_requests
                or spent != authorization.expected_tokens
                or json.loads(batch["policy"]) != authorization.expected_policy
            ):
                raise ValueError("acceptance continuation history mismatch")
            if batch["halt_reason"] not in (None, "input_admission"):
                raise BatchRefused(batch["halt_reason"])
            if any(
                row["outcome"] != "response"
                or known_usage(row)["prompt_tokens"] > row["input_bound"]
                or known_usage(row)["completion_tokens"] > row["output_limit"]
                or row["completed_at"] >= row["deadline"]
                for row in rows
            ):
                raise BatchRefused("continuation_unsafe_history")
            first = batch["first_request_at"]
            if first is None or now < first + POLICY["duration_seconds"]:
                raise BatchRefused("original_window_not_expired")
            if (
                batch["last_observed_at"] is None
                or now < batch["last_observed_at"]
                or authorization.external_stop.occurred_at < batch["last_observed_at"]
                or authorization.external_stop.occurred_at > now
            ):
                raise BatchRefused("batch_clock_regressed")
            if len(rows) >= POLICY["max_requests"] or spent >= POLICY["max_tokens"]:
                raise BatchRefused("continuation_budget_exhausted")
            conn.execute("""
                CREATE TABLE continuation (
                    batch_id TEXT PRIMARY KEY, authorization TEXT NOT NULL,
                    original_snapshot TEXT NOT NULL, registered_at REAL NOT NULL,
                    activated_at REAL, deadline REAL, first_request_id TEXT
                )
            """)
            conn.execute("""
                CREATE TABLE continuation_requests (
                    request_id TEXT PRIMARY KEY REFERENCES requests(request_id),
                    workflow_id TEXT NOT NULL
                )
            """)
            conn.execute(
                "INSERT INTO continuation(batch_id, authorization, original_snapshot, registered_at) "
                "VALUES (?,?,?,?)",
                (batch["batch_id"], authorization.model_dump_json(), json.dumps(snapshot), now),
            )
        return cls.inspect(path)

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
        return self.inspect(self.path)

    def validate_workflow(self, thread, code_revision):
        continuation = self.identity.get("continuation")
        if continuation and (
            thread is None
            or thread == continuation["previous_workflow_id"]
            or code_revision != continuation["candidate_revision"]
        ):
            raise ValueError("acceptance continuation workflow or candidate changed")
        if continuation:
            UUID(thread)

    def reserve(self, payload, *, workflow_id=None, code_revision=None):
        bound = input_bound(payload)
        output = payload["max_tokens"]
        if type(output) is not int or not 1 <= output <= POLICY["max_output_tokens"]:
            raise BatchRefused("output_budget")
        with self.connection() as conn:
            conn.execute("BEGIN IMMEDIATE")
            now = time.time()
            snapshot = self.read_snapshot(conn)
            expected_policy = (
                LEGACY_POLICY if self.identity.get("continuation") else self.identity["policy"]
            )
            if json.loads(snapshot["batch"]["policy"]) != expected_policy or (
                bool(snapshot.get("continuation")) != bool(self.identity.get("continuation"))
            ):
                raise ValueError("acceptance batch identity changed")
            if snapshot.get("continuation") and self.effective_identity(snapshot) != self.identity:
                raise ValueError("acceptance continuation identity changed")
            self.validate_workflow(workflow_id, code_revision)
            batch, rows = snapshot["batch"], snapshot["requests"]
            continuation = snapshot.get("continuation")
            if batch["halt_reason"] and not (
                continuation
                and continuation["activated_at"] is None
                and batch["halt_reason"] == "input_admission"
            ):
                raise BatchRefused(batch["halt_reason"])
            spent = accounted_tokens(rows)
            if continuation and continuation["activated_at"] is None:
                original = json.loads(continuation["original_snapshot"])
                if {"batch": batch, "requests": rows} != original:
                    raise ValueError("acceptance continuation history changed before activation")
            if batch["last_observed_at"] is not None and now < batch["last_observed_at"]:
                raise BatchRefused("batch_clock_regressed")
            first = batch["first_request_at"] if batch["first_request_at"] is not None else now
            deadline = first + POLICY["duration_seconds"]
            if continuation:
                if now < continuation["registered_at"]:
                    raise BatchRefused("batch_clock_regressed")
                deadline = continuation["deadline"] or now + POLICY["duration_seconds"]
            if now >= deadline:
                raise BatchRefused("deadline")
            if len(rows) >= POLICY["max_requests"]:
                raise BatchRefused("request_budget")
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
            if continuation:
                conn.execute(
                    "INSERT INTO continuation_requests(request_id, workflow_id) VALUES (?,?)",
                    (reservation["request_id"], workflow_id),
                )
            if continuation and continuation["activated_at"] is None:
                conn.execute(
                    "UPDATE continuation SET activated_at=?, deadline=?, first_request_id=?",
                    (now, deadline, reservation["request_id"]),
                )
                # The immutable original snapshot retains this narrowly released
                # stop. Any later durable stop remains effective.
                conn.execute("UPDATE batch SET halt_reason=NULL")
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
            halted = conn.execute("SELECT halt_reason FROM batch").fetchone()["halt_reason"]
            conn.execute(
                "UPDATE batch SET halt_reason=?, last_observed_at=?",
                (halted or reason, now),
            )
        if reason:
            raise BatchRefused(reason, total_tokens=usage["total_tokens"] if valid else None)
