"""Single-worker product bridge. No HTTP listener, credential discovery or Pi launch.

The operator initializes PG explicitly and installs this package into the configured
interpreter. Commands are JSON stdin. Unknown external outcomes stay unresolved.
"""

import hashlib
import json
import os
import sys
import time

from psycopg.types.json import Jsonb

from .graph import frozen_config
from .model import SDKModel
from .protocol import GRAPH_VERSION, STATE_VERSION, Limits
from .storage import connect
from .tools import PublicCatalog
from .worker import Worker


def describe(worker):
    return {
        "workflow_version": "research-to-pi.v1",
        "graph_version": GRAPH_VERSION,
        "state_schema_version": STATE_VERSION,
        "frozen": frozen_config(worker.model, worker.catalog),
        "sample": worker.execution.sample.model_dump(),
        "code_revision": worker.execution.code_revision,
        "limits": Limits().model_dump(),
    }


def projection(worker, thread, snapshot):
    state = snapshot.values
    status = state.get("status")
    if status not in {"waiting", "accepted", "rejected", "failed", "stopped"}:
        status = "unresolved"
    if state.get("usage_unknown") or "unknown" in (state.get("error") or ""):
        status = "unresolved"
    gate = state.get("gate")
    return {
        "status": status,
        "gate": {**gate, "report": state["report"]} if gate else None,
        "evidence": state.get("evidence", {}),
        "trace_id": state.get("trace_id"),
        "budget": {
            key: state.get(key)
            for key in ("steps", "retries", "spent_tokens", "usage_unknown", "deadline", "limits")
        },
        "error": state.get("error"),
        "events": worker.read_events(thread),
    }


def execute(worker, command):
    thread = command["workflow_id"]
    worker.config(thread)  # Canonical UUID before any write.
    kind = command["kind"]
    if kind not in {"submit", "resume", "stop", "bind_run"}:
        raise ValueError("unsupported product command")
    if command["operation_id"] != thread + ":" + kind:
        raise ValueError("operation identity changed")
    raw = command["payload"]
    if hashlib.sha256(raw.encode()).hexdigest() != command["input_digest"]:
        raise ValueError("command payload changed")
    payload = json.loads(raw)
    # A session lock serializes this M1 worker's commands, not a two-worker lease.
    # FencedSaver still protects every graph checkpoint write. No M6 claim.
    with connect(worker.dsn) as conn:
        lock_key = thread + ":stop" if kind == "stop" else thread
        if kind == "stop":
            # Independent of the graph lock, but repeated Stops serialize and
            # return the first immutable receipt, including its provenance.
            conn.execute("SELECT pg_advisory_lock(hashtextextended(%s, 50))", (lock_key,))
        elif not conn.execute(
            "SELECT pg_try_advisory_lock(hashtextextended(%s, 50)) AS acquired", (lock_key,)
        ).fetchone()["acquired"]:
            raise ValueError("workflow worker is already active")
        try:
            with conn.transaction():
                conn.execute(
                    "INSERT INTO workflow_bridge_commands(operation_id,thread_id,kind,"
                    "input_digest,payload) VALUES (%s,%s,%s,%s,%s) ON CONFLICT DO NOTHING",
                    (command["operation_id"], thread, kind, command["input_digest"], raw),
                )
                row = conn.execute(
                    "SELECT * FROM workflow_bridge_commands WHERE operation_id=%s FOR UPDATE",
                    (command["operation_id"],),
                ).fetchone()
                if (
                    any(row[key] != command[key] for key in ("kind", "input_digest", "payload"))
                    or row["thread_id"] != thread
                ):
                    raise ValueError("conflicting product command")
                delivery = conn.execute(
                    "UPDATE workflow_bridge_commands SET delivery_attempt=delivery_attempt+1 "
                    "WHERE operation_id=%s RETURNING delivery_attempt",
                    (command["operation_id"],),
                ).fetchone()["delivery_attempt"]
                conn.execute(
                    "INSERT INTO workflow_bridge_deliveries(operation_id,delivery_attempt,worker_id,received_at) "
                    "VALUES (%s,%s,%s,%s)",
                    (
                        command["operation_id"],
                        delivery,
                        worker.execution.worker_instance_id,
                        time.time(),
                    ),
                )
                if row["receipt"] is not None:
                    return row["receipt"]
                conn.execute(
                    "INSERT INTO workflow_bridge_inputs(thread_id,input_digest) VALUES (%s,%s) "
                    "ON CONFLICT DO NOTHING",
                    (thread, payload["input_digest"]),
                )
                binding = conn.execute(
                    "SELECT * FROM workflow_bridge_inputs WHERE thread_id=%s FOR UPDATE", (thread,)
                ).fetchone()
                if binding["input_digest"] != payload["input_digest"]:
                    raise ValueError("workflow input changed")
                if kind == "stop":
                    conn.execute(
                        "UPDATE workflow_bridge_inputs SET stopped=true WHERE thread_id=%s",
                        (thread,),
                    )
            if kind == "bind_run":
                operation = payload["go_operation_id"]
                if (
                    operation != thread + ":create_run"
                    or len(payload["go_input_digest"]) != 64
                    or not payload["run_id"].startswith("run_")
                ):
                    raise ValueError("invalid Go operation receipt")
                go_receipt = {
                    "operation_id": operation,
                    "input_digest": payload["go_input_digest"],
                    "run_id": payload["run_id"],
                }
                with conn.transaction():
                    conn.execute(
                        "INSERT INTO workflow_go_operations(operation_id,thread_id,input_digest,run_id,receipt) "
                        "VALUES (%s,%s,%s,%s,%s) ON CONFLICT DO NOTHING",
                        (
                            operation,
                            thread,
                            payload["go_input_digest"],
                            payload["run_id"],
                            Jsonb(go_receipt),
                        ),
                    )
                    existing = conn.execute(
                        "SELECT receipt FROM workflow_go_operations WHERE operation_id=%s",
                        (operation,),
                    ).fetchone()
                    if existing["receipt"] != go_receipt:
                        raise ValueError("conflicting Go Run receipt")
                result = {"status": "coding", "go_receipt": go_receipt, "run_id": payload["run_id"]}
            elif kind == "stop":
                if conn.execute(
                    "SELECT 1 FROM workflow_owners WHERE thread_id=%s", (thread,)
                ).fetchone():
                    worker.stop(thread)
                result = {"status": "stopped"}
            elif binding["stopped"]:
                result = {"status": "stopped"}
            else:
                if payload["configuration"] != describe(worker):
                    raise ValueError(
                        "frozen deployment changed; restore its interpreter/configuration"
                    )
                if kind == "submit":
                    snapshot = worker.submit(
                        thread,
                        payload["question"],
                        Limits.model_validate(payload["configuration"]["limits"]),
                    )
                    if snapshot.next and not snapshot.interrupts:
                        snapshot = worker.resume(thread)
                else:
                    snapshot = worker.resume(thread, payload["decision"])
                result = projection(worker, thread, snapshot)
            receipt = {
                "operation_id": command["operation_id"],
                "workflow_id": thread,
                "input_digest": command["input_digest"],
                "delivery_attempt": delivery,
                "worker_id": worker.execution.worker_instance_id,
                "owner_epoch": (
                    conn.execute(
                        "SELECT owner_epoch FROM workflow_owners WHERE thread_id=%s", (thread,)
                    ).fetchone()
                    or {}
                ).get("owner_epoch"),
                **result,
            }
            with conn.transaction():
                conn.execute(
                    "UPDATE workflow_bridge_commands SET receipt=%s WHERE operation_id=%s",
                    (Jsonb(receipt), command["operation_id"]),
                )
            return receipt
        finally:
            conn.execute("SELECT pg_advisory_unlock(hashtextextended(%s, 50))", (lock_key,))


def main():
    command = json.loads(sys.stdin.read(1_048_577))
    kind = command["kind"]
    model = None if kind in {"stop", "bind_run"} else SDKModel()
    worker = Worker(
        os.environ["CHORA_WORKFLOW_DATABASE_URL"],
        model,
        PublicCatalog(),
        sample_type=None
        if kind in {"stop", "bind_run"}
        else os.environ.get("CHORA_WORKFLOW_SAMPLE_TYPE"),
        code_revision=os.environ.get("CHORA_WORKFLOW_CODE_REVISION"),
    )
    print(json.dumps(describe(worker) if kind == "describe" else execute(worker, command)))


if __name__ == "__main__":
    try:
        main()
    except Exception:
        # Credentials, database details and provider responses never go to Go logs.
        print("research bridge requires reconciliation", file=sys.stderr)
        sys.exit(1)
