"""Real PG decision delivery/recovery, with deterministic model and corpus only."""

import json
import subprocess
import sys
from pathlib import Path
from uuid import uuid4

import psycopg
import pytest

from chora_workflow import graph as graph_module
from chora_workflow.checkpoint import FencedSaver, StaleOwner
from chora_workflow.protocol import digest
from chora_workflow.storage import Journal, connect
from chora_workflow.worker import Worker
from fixtures import FixtureCatalog, FixtureModel

pytestmark = pytest.mark.postgres


def paused(dsn):
    worker = Worker(dsn, FixtureModel(fail_first=True), FixtureCatalog())
    thread = str(uuid4())
    return worker, thread, worker.submit(thread, "What is Chora?")


def child(dsn, action, thread, decision):
    return subprocess.run(
        [
            sys.executable,
            str(Path(__file__).with_name("process_worker.py")),
            dsn,
            action,
            thread,
            decision,
        ],
        capture_output=True,
        text=True,
        timeout=25,
    )


def commands(dsn):
    with connect(dsn) as conn:
        return conn.execute("SELECT command FROM workflow_decisions").fetchall()


def writes(dsn):
    with connect(dsn) as conn:
        return conn.execute("SELECT * FROM checkpoint_writes ORDER BY 1,2,3,4,5").fetchall()


def assert_preserved(before, after):
    for key in ("trace_id", "steps", "spent_tokens", "deadline", "retries", "report", "gate"):
        assert before[key] == after[key]


@pytest.mark.parametrize(
    "first,opposite,status",
    [
        ("accept", "reject", "accepted"),
        ("reject", "accept", "rejected"),
    ],
)
@pytest.mark.parametrize(
    "boundary,exit_code",
    [
        ("crash-after-decision", 78),
        ("crash-after-resume", 79),
        ("crash-before-terminal", 80),
    ],
)
def test_committed_decision_survives_process_death(
    dsn, first, opposite, status, boundary, exit_code
):
    worker, thread, before = paused(dsn)
    crashed = child(dsn, boundary, thread, first)
    assert crashed.returncode == exit_code, crashed.stderr
    assert worker.inspect(thread).values["status"] == (status if exit_code == 80 else "waiting")
    pending = [row for row in writes(dsn) if row["channel"] == "__resume__"]
    assert bool(pending) == (exit_code in {79, 80})
    rows = commands(dsn)
    assert len(rows) == 1
    command = rows[0]["command"]
    assert command["interrupt_id"] == before.interrupts[0].id
    assert command["thread_id"] == thread
    assert command["decision"] == first
    assert command["command_id"] == digest({k: v for k, v in command.items() if k != "command_id"})
    old_writes = writes(dsn)
    with pytest.raises(ValueError, match="conflicting"):
        worker.resume(thread, {**before.values["gate"], "decision": opposite})
    assert writes(dsn) == old_writes
    assert commands(dsn) == rows
    # A third interpreter uses exactly the original command, with no new budget.
    retried = child(dsn, "resume-decision", thread, first)
    assert retried.returncode == 0, retried.stderr
    result = json.loads(retried.stdout)["state"]
    assert result["status"] == status
    assert_preserved(before.values, result)
    assert result["events"][-1]["command_id"] == command["command_id"]
    assert worker.resume(thread, {**before.values["gate"], "decision": first}).values == result
    assert commands(dsn) == rows
    with connect(dsn) as conn:
        saved = FencedSaver(conn, thread, Journal.claim(dsn, thread)).get_tuple(
            worker.config(thread)
        )
        assert saved.checkpoint["channel_values"]["status"] == status
        assert conn.execute("SELECT count(*) AS n FROM workflow_operations").fetchone()["n"] == 6
    assert worker.model.calls == 4


@pytest.mark.parametrize(
    "constant,replacement",
    [
        ("GRAPH_VERSION", "future"),
        ("STATE_VERSION", 99),
        ("PROMPT_VERSION", "future"),
        ("TOOLS_VERSION", "future"),
    ],
)
def test_version_refusal_precedes_any_decision_write(dsn, monkeypatch, constant, replacement):
    worker, thread, before = paused(dsn)
    original = getattr(graph_module, constant)
    old_writes = writes(dsn)
    monkeypatch.setattr(graph_module, constant, replacement)
    with pytest.raises(ValueError, match="frozen"):
        worker.resume(thread, {**before.values["gate"], "decision": "accept"})
    assert commands(dsn) == []
    assert writes(dsn) == old_writes
    monkeypatch.setattr(graph_module, constant, original)
    result = worker.resume(thread, {**before.values["gate"], "decision": "reject"})
    assert result.values["status"] == "rejected"
    assert_preserved(before.values, result.values)


@pytest.mark.parametrize("field", ["gate_id", "input_digest"])
def test_stale_gate_never_binds_decision(dsn, field):
    worker, thread, before = paused(dsn)
    old_writes = writes(dsn)
    with pytest.raises(ValueError, match="stale"):
        worker.resume(thread, {**before.values["gate"], field: "old", "decision": "accept"})
    assert commands(dsn) == []
    assert writes(dsn) == old_writes


@pytest.mark.parametrize("reason", ["cancelled", "deadline", "token_budget", "usage_unknown"])
def test_recovered_decision_still_obeys_controls(dsn, monkeypatch, reason):
    worker, thread, before = paused(dsn)
    if reason in {"token_budget", "usage_unknown"}:
        with worker.graph(thread) as graph:
            graph.update_state(
                worker.config(thread),
                {"spent_tokens": before.values["limits"]["max_tokens"]}
                if reason == "token_budget"
                else {"usage_unknown": True},
                as_node="tools",
            )
            graph.invoke(None, worker.config(thread), durability="sync")
        before = worker.inspect(thread)
    crashed = child(dsn, "crash-after-resume", thread, "accept")
    assert crashed.returncode == 79, crashed.stderr
    if reason == "cancelled":
        worker.stop(thread)
    elif reason == "deadline":
        monkeypatch.setattr(graph_module.time, "time", lambda: before.values["deadline"] + 1)
    result = worker.resume(thread, {**before.values["gate"], "decision": "accept"})
    assert result.values["error"] == reason
    assert result.values["status"] == ("stopped" if reason == "cancelled" else "failed")
    assert_preserved(before.values, result.values)
    assert (
        worker.resume(thread, {**before.values["gate"], "decision": "accept"}).values
        == result.values
    )
    with pytest.raises(ValueError, match="conflicting"):
        worker.resume(thread, {**before.values["gate"], "decision": "reject"})


def test_database_failure_rolls_back_decision_before_resume(dsn):
    worker, thread, before = paused(dsn)
    old_writes = writes(dsn)
    with connect(dsn) as conn:
        conn.execute("ALTER TABLE workflow_decisions ADD CONSTRAINT fail_decision CHECK (false)")
    with pytest.raises(psycopg.errors.CheckViolation):
        worker.resume(thread, {**before.values["gate"], "decision": "accept"})
    assert commands(dsn) == []
    assert writes(dsn) == old_writes
    with connect(dsn) as conn:
        conn.execute("ALTER TABLE workflow_decisions DROP CONSTRAINT fail_decision")
    assert (
        worker.resume(thread, {**before.values["gate"], "decision": "reject"}).values["status"]
        == "rejected"
    )


def test_database_failure_after_command_commit_reconciles(dsn, monkeypatch):
    worker, thread, before = paused(dsn)
    original = Journal.bind_decision

    def fail_resume_write(self, *args):
        result = original(self, *args)
        with connect(dsn) as conn:
            conn.execute(
                "ALTER TABLE checkpoint_writes ADD CONSTRAINT fail_resume "
                "CHECK (channel != '__resume__')"
            )
        return result

    monkeypatch.setattr(Journal, "bind_decision", fail_resume_write)
    with pytest.raises(psycopg.errors.CheckViolation):
        worker.resume(thread, {**before.values["gate"], "decision": "accept"})
    assert len(commands(dsn)) == 1
    assert not [row for row in writes(dsn) if row["channel"] == "__resume__"]
    monkeypatch.setattr(Journal, "bind_decision", original)
    with connect(dsn) as conn:
        conn.execute("ALTER TABLE checkpoint_writes DROP CONSTRAINT fail_resume")
    with pytest.raises(ValueError, match="conflicting"):
        worker.resume(thread, {**before.values["gate"], "decision": "reject"})
    result = worker.resume(thread, {**before.values["gate"], "decision": "accept"})
    assert result.values["status"] == "accepted"
    assert_preserved(before.values, result.values)


def test_old_unjournaled_pending_resume_requires_reconciliation(dsn):
    worker, thread, before = paused(dsn)
    assert child(dsn, "crash-after-resume", thread, "accept").returncode == 79
    # Simulate a checkpoint produced before the decision journal existed.
    with connect(dsn) as conn:
        conn.execute("DELETE FROM workflow_decisions")
    old_writes = writes(dsn)
    for decision in ("reject", "accept"):
        with pytest.raises(ValueError, match="reconciliation required"):
            worker.resume(thread, {**before.values["gate"], "decision": decision})
    assert commands(dsn) == []
    assert writes(dsn) == old_writes


def test_decision_binding_fences_owner_and_interrupt(dsn):
    worker, thread, before = paused(dsn)
    epoch = Journal.claim(dsn, thread)
    journal = Journal(dsn, thread, epoch)
    decision = {**before.values["gate"], "decision": "reject"}
    journal.bind_decision(decision, before.interrupts[0].id)
    with pytest.raises(ValueError, match="conflicting"):
        journal.bind_decision(decision, "other-interrupt")
    Journal.claim(dsn, thread)
    with pytest.raises(StaleOwner):
        journal.bind_decision(decision, before.interrupts[0].id)
    assert len(commands(dsn)) == 1
