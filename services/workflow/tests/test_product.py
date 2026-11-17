import copy
import hashlib
import json
import os
import subprocess
import sys
from concurrent.futures import ThreadPoolExecutor
from threading import Event
from pathlib import Path
from uuid import uuid4

import pytest

from chora_workflow.product import describe, execute
from chora_workflow.storage import connect
from chora_workflow.worker import Worker
from fixtures import FixtureCatalog, FixtureModel

pytestmark = pytest.mark.postgres


def worker(dsn):
    return Worker(dsn, FixtureModel(), FixtureCatalog())


def command(thread, kind, payload):
    raw = json.dumps(payload, separators=(",", ":"))
    return {
        "workflow_id": thread,
        "operation_id": thread + ":" + kind,
        "kind": kind,
        "input_digest": hashlib.sha256(raw.encode()).hexdigest(),
        "payload": raw,
    }


def submission(w, thread):
    return command(
        thread,
        "submit",
        {"configuration": describe(w), "question": "What is Chora?", "input_digest": "f" * 64},
    )


def child(dsn, cmd):
    result = subprocess.run(
        [sys.executable, str(Path(__file__).with_name("product_worker.py"))],
        input=json.dumps(cmd),
        text=True,
        capture_output=True,
        env={**os.environ, "CHOR50_TEST_DSN": dsn},
        timeout=30,
    )
    assert result.returncode == 0, result.stderr
    return json.loads(result.stdout)


def test_fresh_interpreters_same_gate_budget_and_receipt_after_response_loss(dsn):
    w = worker(dsn)
    thread = str(uuid4())
    cmd = submission(w, thread)
    before = child(dsn, cmd)
    assert before["status"] == "waiting"
    assert child(dsn, cmd) == before  # Response lost to Go: no new model work.
    gate = before["gate"]
    decision = command(
        thread,
        "resume",
        {
            "configuration": describe(w),
            "input_digest": "f" * 64,
            "decision": {
                "gate_id": gate["gate_id"],
                "input_digest": gate["input_digest"],
                "decision": "accept",
            },
        },
    )
    after = child(dsn, decision)
    assert after["status"] == "accepted"
    assert child(dsn, decision) == after
    assert before["budget"] == after["budget"]
    assert before["trace_id"] == after["trace_id"]
    assert before["gate"] == after["gate"]
    assert after["budget"]["spent_tokens"] == 21
    assert w.model.calls == 0


@pytest.mark.parametrize(
    "mutation", ["operation", "digest", "payload", "configuration", "submission"]
)
def test_conflicting_or_changed_commands_refused(dsn, mutation):
    w = worker(dsn)
    thread = str(uuid4())
    cmd = submission(w, thread)
    if mutation in {"payload", "submission"}:
        execute(w, cmd)
    changed = copy.deepcopy(cmd)
    if mutation == "operation":
        changed["operation_id"] += ":attempt2"
    elif mutation == "digest":
        changed["input_digest"] = "0" * 64
    else:
        payload = json.loads(cmd["payload"])
        if mutation == "configuration":
            payload["configuration"]["graph_version"] = "future"
        else:
            payload["question"] = "different submission"
        changed = command(thread, "submit", payload)
    with pytest.raises(ValueError):
        execute(w, changed)


def test_stop_tombstone_before_submit_and_pending_receipt_reconciliation(dsn):
    w = worker(dsn)
    thread = str(uuid4())
    stop = command(thread, "stop", {"input_digest": "f" * 64})
    receipt = execute(w, stop)
    assert receipt["status"] == "stopped"
    assert execute(w, submission(w, thread))["status"] == "stopped"
    assert w.model.calls == 0
    with connect(dsn) as conn:
        assert conn.execute("SELECT count(*) AS n FROM workflow_owners").fetchone()["n"] == 0


def test_pending_command_with_committed_graph_recovers_without_resetting_usage(dsn):
    w = worker(dsn)
    thread = str(uuid4())
    cmd = submission(w, thread)
    before = execute(w, cmd)
    # Fault injection: process died after checkpoint commit but before its bridge receipt.
    with connect(dsn) as conn:
        conn.execute(
            "UPDATE workflow_bridge_commands SET receipt=NULL WHERE operation_id=%s",
            (cmd["operation_id"],),
        )
    recovered = child(dsn, cmd)
    for field in ("status", "gate", "budget", "trace_id", "events", "input_digest", "operation_id"):
        assert recovered[field] == before[field]
    assert recovered["delivery_attempt"] == 2
    assert recovered["worker_id"] != before["worker_id"]
    assert w.model.calls == 3


def test_stale_and_conflicting_decisions_keep_original_gate(dsn):
    w = worker(dsn)
    thread = str(uuid4())
    before = execute(w, submission(w, thread))
    payload = {
        "configuration": describe(w),
        "input_digest": "f" * 64,
        "decision": {
            "gate_id": before["gate"]["gate_id"],
            "input_digest": "bad",
            "decision": "accept",
        },
    }
    with pytest.raises(ValueError, match="stale"):
        execute(w, command(thread, "resume", payload))
    assert worker(dsn).inspect(thread).values["status"] == "waiting"


def test_busy_worker_refused_without_new_reservation(dsn):
    w = worker(dsn)
    thread = str(uuid4())
    with connect(dsn) as conn:
        conn.execute("SELECT pg_advisory_lock(hashtextextended(%s,50))", (thread,))
        with pytest.raises(ValueError, match="already active"):
            execute(w, submission(w, thread))
        assert (
            conn.execute("SELECT count(*) AS n FROM workflow_bridge_commands").fetchone()["n"] == 0
        )


def test_stop_during_inflight_model_call_prevents_the_next_operation(dsn):
    entered, release = Event(), Event()

    class BlockingModel(FixtureModel):
        def invoke(self, *args):
            entered.set()
            assert release.wait(10)
            return super().invoke(*args)

    w = Worker(dsn, BlockingModel(), FixtureCatalog())
    thread = str(uuid4())
    with ThreadPoolExecutor() as pool:
        future = pool.submit(execute, w, submission(w, thread))
        assert entered.wait(10)
        try:
            stop = execute(worker(dsn), command(thread, "stop", {"input_digest": "f" * 64}))
            assert stop["status"] == "stopping"
            assert stop["admission_closed"] and not stop["terminated"]
            assert len(stop["pending_operations"]) == 1
        finally:
            release.set()
        result = future.result(timeout=10)
    assert result["status"] == "stopped"
    original = command(thread, "stop", {"input_digest": "f" * 64})
    assert execute(worker(dsn), original) == stop
    observation = execute(worker(dsn), {**original, "kind": "observe_stop"})
    assert observation["terminated"] and observation["pending_operations"] == []
    assert execute(worker(dsn), original) == stop
    assert w.model.calls == 1
    with connect(dsn) as conn:
        assert conn.execute("SELECT count(*) AS n FROM workflow_operations").fetchone()["n"] == 1


def test_go_run_receipt_replays_across_python_restart_and_refuses_conflict(dsn):
    w = worker(dsn)
    thread = str(uuid4())
    execute(w, submission(w, thread))
    payload = {
        "input_digest": "f" * 64,
        "go_operation_id": thread + ":create_run",
        "go_input_digest": "e" * 64,
        "run_id": "run_" + str(uuid4()),
    }
    cmd = command(thread, "bind_run", payload)
    before = execute(w, cmd)
    assert child(dsn, cmd) == before
    assert before["go_receipt"] == {
        "operation_id": payload["go_operation_id"],
        "input_digest": payload["go_input_digest"],
        "run_id": payload["run_id"],
    }
    changed = {**payload, "run_id": "run_" + str(uuid4())}
    with pytest.raises(ValueError, match="conflicting"):
        execute(w, command(thread, "bind_run", changed))
    with connect(dsn) as conn:
        row = conn.execute("SELECT * FROM workflow_go_operations").fetchone()
        assert row["operation_id"] == thread + ":create_run"
        assert row["run_id"] == payload["run_id"]


def test_concurrent_stops_reuse_the_original_receipt(dsn):
    thread = str(uuid4())
    cmd = command(thread, "stop", {"input_digest": "f" * 64})
    with ThreadPoolExecutor() as pool:
        first = pool.submit(execute, worker(dsn), cmd)
        second = pool.submit(execute, worker(dsn), cmd)
        assert first.result(timeout=10) == second.result(timeout=10)
    with connect(dsn) as conn:
        assert (
            conn.execute("SELECT count(*) AS n FROM workflow_bridge_deliveries").fetchone()["n"]
            == 2
        )


def test_stop_unknown_remains_unresolved_across_fresh_workers(dsn):
    entered, release = Event(), Event()

    class LostReceiptModel(FixtureModel):
        def invoke(self, *args):
            entered.set()
            assert release.wait(10)
            raise RuntimeError("synthetic lost response")

    w = Worker(dsn, LostReceiptModel(), FixtureCatalog())
    thread = str(uuid4())
    original = command(thread, "stop", {"input_digest": "f" * 64})
    with ThreadPoolExecutor() as pool:
        running = pool.submit(execute, w, submission(w, thread))
        assert entered.wait(10)
        try:
            first = execute(worker(dsn), original)
            assert first["status"] == "stopping" and not first["terminated"]
        finally:
            release.set()
        try:
            running.result(timeout=10)
        except RuntimeError:
            pass
    for _ in range(2):
        fresh = worker(dsn)
        assert execute(fresh, original) == first
        observed = execute(fresh, {**original, "kind": "observe_stop"})
        assert observed["graph_idle"] and not observed["terminated"]
        assert observed["status"] == "stopping" and len(observed["pending_operations"]) == 1
        assert fresh.model.calls == 0
    with connect(dsn) as conn:
        assert conn.execute("SELECT count(*) AS n FROM workflow_operations").fetchone()["n"] == 1


def test_idle_stop_has_explicit_quiescence_and_observation_identity(dsn):
    w = worker(dsn)
    thread = str(uuid4())
    execute(w, submission(w, thread))
    original = command(thread, "stop", {"input_digest": "f" * 64})
    first = execute(w, original)
    assert first["status"] == "stopped" and first["admission_closed"]
    assert first["terminated"] and first["graph_idle"] and first["pending_operations"] == []
    observed = execute(worker(dsn), {**original, "kind": "observe_stop"})
    assert observed["terminated"]
    assert execute(worker(dsn), original) == first
    with pytest.raises(ValueError):
        execute(worker(dsn), {**original, "kind": "observe_stop", "input_digest": "0" * 64})
