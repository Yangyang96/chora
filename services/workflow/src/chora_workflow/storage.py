"""Durable read-only operation journal; uncertain calls are never blindly repeated."""

from contextlib import contextmanager

import psycopg
from langgraph.checkpoint.postgres import PostgresSaver
from psycopg.rows import dict_row
from psycopg.types.json import Jsonb

from .checkpoint import OWNER_SQL, verify_owner
from .protocol import digest

SCHEMA = """
CREATE TABLE IF NOT EXISTS workflow_owners (
    thread_id text PRIMARY KEY,
    owner_epoch bigint NOT NULL DEFAULT 0,
    stopped boolean NOT NULL DEFAULT false,
    submission jsonb NOT NULL
);
CREATE TABLE IF NOT EXISTS workflow_operations (
    thread_id text NOT NULL REFERENCES workflow_owners(thread_id),
    operation_id text NOT NULL,
    input_digest text NOT NULL,
    receipt jsonb,
    PRIMARY KEY (thread_id, operation_id)
);
CREATE TABLE IF NOT EXISTS workflow_decisions (
    thread_id text NOT NULL REFERENCES workflow_owners(thread_id),
    gate_id text NOT NULL,
    command jsonb NOT NULL,
    PRIMARY KEY (thread_id, gate_id)
);
"""


class Stopped(RuntimeError):
    pass


class OutcomeUnknown(RuntimeError):
    pass


def connect(dsn):
    return psycopg.connect(dsn, autocommit=True, row_factory=dict_row, connect_timeout=5)


def initialize(dsn):
    # Explicit offline administrative command, never invoked by a worker.
    with connect(dsn) as conn:
        PostgresSaver(conn).setup()
        conn.execute(SCHEMA)


class Journal:
    def __init__(self, dsn, thread, epoch):
        self.dsn, self.thread, self.epoch = dsn, thread, epoch

    @staticmethod
    def submit(dsn, thread, submission, initial=None):
        envelope = {"request": submission, "initial": initial}
        with connect(dsn) as conn, conn.transaction():
            conn.execute(
                "INSERT INTO workflow_owners(thread_id, submission) VALUES (%s,%s) "
                "ON CONFLICT DO NOTHING",
                (thread, Jsonb(envelope)),
            )
            row = conn.execute(
                "SELECT submission FROM workflow_owners WHERE thread_id=%s", (thread,)
            ).fetchone()
            if row["submission"]["request"] != submission:
                raise ValueError("workflow ID already binds a different submission")
            return row["submission"]["initial"]

    @staticmethod
    def claim(dsn, thread):
        # M0 explicit takeover only; automatic leases/scheduling are M6 work.
        with connect(dsn) as conn:
            row = conn.execute(
                "UPDATE workflow_owners SET owner_epoch=owner_epoch+1 "
                "WHERE thread_id=%s RETURNING owner_epoch",
                (thread,),
            ).fetchone()
            if row is None:
                raise ValueError("workflow not found")
            return row["owner_epoch"]

    @contextmanager
    def transaction(self):
        with connect(self.dsn) as conn, conn.transaction():
            row = conn.execute(OWNER_SQL, (self.thread,)).fetchone()
            verify_owner(row, self.epoch)
            yield conn

    def check(self):
        with self.transaction() as conn:
            if conn.execute(
                "SELECT stopped FROM workflow_owners WHERE thread_id=%s", (self.thread,)
            ).fetchone()["stopped"]:
                raise Stopped("stop requested")

    def decision(self, gate_id):
        with self.transaction() as conn:
            row = conn.execute(
                "SELECT command FROM workflow_decisions WHERE thread_id=%s AND gate_id=%s",
                (self.thread, gate_id),
            ).fetchone()
            return row["command"] if row else None

    def bind_decision(self, decision, interrupt_id):
        """First committed command wins, even if no resume/checkpoint follows it."""
        binding = {"thread_id": self.thread, "interrupt_id": interrupt_id, **decision}
        command = {**binding, "command_id": digest(binding)}
        with self.transaction() as conn:
            row = conn.execute(
                "SELECT command FROM workflow_decisions WHERE thread_id=%s AND gate_id=%s",
                (self.thread, decision["gate_id"]),
            ).fetchone()
            if row:
                if row["command"] != command:
                    raise ValueError("conflicting or stale decision")
                return row["command"]
            conn.execute(
                "INSERT INTO workflow_decisions VALUES (%s,%s,%s)",
                (self.thread, decision["gate_id"], Jsonb(command)),
            )
        return command

    def verify_decision(self, decision):
        command = self.decision(decision["gate_id"])
        if command is None or any(command[key] != value for key, value in decision.items()):
            raise ValueError("conflicting or unbound review decision")
        return command

    def lookup(self, op, inputs):
        """Recover an existing receipt even after stop/deadline, without dispatch."""
        with self.transaction() as conn:
            row = conn.execute(
                "SELECT input_digest, receipt FROM workflow_operations "
                "WHERE thread_id=%s AND operation_id=%s",
                (self.thread, op),
            ).fetchone()
            if row is None:
                return None
            if row["input_digest"] != digest(inputs):
                raise ValueError("operation input changed")
            if row["receipt"] is None:
                raise OutcomeUnknown("call reserved without durable receipt")
            return row["receipt"]

    def perform(self, op, inputs, action):
        key = digest(inputs)
        with self.transaction() as conn:
            if conn.execute(
                "SELECT stopped FROM workflow_owners WHERE thread_id=%s", (self.thread,)
            ).fetchone()["stopped"]:
                raise Stopped("stop requested")
            row = conn.execute(
                "SELECT input_digest, receipt FROM workflow_operations "
                "WHERE thread_id=%s AND operation_id=%s",
                (self.thread, op),
            ).fetchone()
            if row:
                if row["input_digest"] != key:
                    raise ValueError("operation input changed")
                if row["receipt"] is None:
                    raise OutcomeUnknown("call reserved without durable receipt")
                return row["receipt"]
            conn.execute(
                "INSERT INTO workflow_operations VALUES (%s,%s,%s,NULL)", (self.thread, op, key)
            )
        # Reservation commits before network I/O. Stop/takeover may happen in flight;
        # already-dispatched read-only calls can complete. No DB lock spans network I/O.
        receipt = action()
        with self.transaction() as conn:
            conn.execute(
                "UPDATE workflow_operations SET receipt=%s WHERE thread_id=%s AND operation_id=%s",
                (Jsonb(receipt), self.thread, op),
            )
        return receipt

    @staticmethod
    def stop(dsn, thread):
        with connect(dsn) as conn:
            if not conn.execute(
                "UPDATE workflow_owners SET stopped=true WHERE thread_id=%s RETURNING thread_id",
                (thread,),
            ).fetchone():
                raise ValueError("workflow not found")
