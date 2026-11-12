import json
import subprocess
import sys
from pathlib import Path
from uuid import uuid4

import psycopg
import pytest

from chora_workflow.model import ModelFailure, SDKModel
from chora_workflow.protocol import Call, Limits, Reply, operation_id
from chora_workflow.storage import Journal, connect
from chora_workflow.worker import Worker
from fixtures import FixtureCatalog, FixtureModel

pytestmark = pytest.mark.postgres


def test_process_restart_preserves_interrupt_budget_and_retry(dsn):
    thread = str(uuid4())
    script = str(Path(__file__).with_name("process_worker.py"))
    first = subprocess.run(
        [sys.executable, script, dsn, "submit", thread], check=True, capture_output=True, text=True
    )
    before = json.loads(first.stdout)
    assert before["interrupts"]
    assert before["state"]["status"] == "waiting"
    # First process is fully exited; a fresh interpreter resumes the same thread.
    second = subprocess.run(
        [sys.executable, script, dsn, "resume", thread], check=True, capture_output=True, text=True
    )
    after = json.loads(second.stdout)
    assert after["state"]["status"] == "accepted"
    for key in [
        "trace_id",
        "steps",
        "spent_tokens",
        "deadline",
        "retries",
        "report",
        "gate",
        "evidence",
    ]:
        assert before["state"][key] == after["state"][key]
    assert after["state"]["steps"] == 4
    assert after["state"]["spent_tokens"] == 21
    assert after["state"]["retries"] == 1


def test_idempotency_stale_decision_stop_and_frozen_config(dsn):
    thread = str(uuid4())
    model = FixtureModel()
    worker = Worker(dsn, model, FixtureCatalog())
    before = worker.submit(thread, "What is Chora?")
    assert worker.submit(thread, "What is Chora?").values == before.values
    assert model.calls == 3
    with pytest.raises(ValueError, match="different submission"):
        worker.submit(thread, "Different question")
    with pytest.raises(ValueError, match="stale"):
        worker.resume(
            thread, {**before.values["gate"], "input_digest": "old", "decision": "accept"}
        )
    assert worker.inspect(thread).interrupts[0].id == before.interrupts[0].id
    model.identity = {"adapter": "changed"}
    with pytest.raises(ValueError, match="frozen"):
        worker.resume(thread)
    model.identity = FixtureModel.identity
    decision = {**before.values["gate"], "decision": "accept"}
    accepted = worker.resume(thread, decision)
    assert worker.resume(thread, decision).values == accepted.values
    with pytest.raises(ValueError, match="conflicting"):
        worker.resume(thread, {**decision, "decision": "reject"})
    thread2 = str(uuid4())
    paused = worker.submit(thread2, "What is Chora?")
    worker.stop(thread2)
    stopped = worker.resume(thread2, {**paused.values["gate"], "decision": "accept"})
    assert stopped.values["status"] == "stopped"
    assert model.calls == 6


class BadModel(FixtureModel):
    def __init__(self, kind):
        super().__init__()
        self.kind = kind

    def invoke(self, *args):
        self.calls += 1
        if self.kind in {"authentication", "rate_limit", "timeout_unknown", "outcome_unknown"}:
            raise ModelFailure(self.kind)
        if self.kind == "invalid_arguments":
            return Reply(
                calls=[Call(id="x", name="read_evidence", arguments={"path": "/secret"})],
                total_tokens=1,
            )
        return Reply(
            calls=[Call(id="x", name="shell", arguments={})],
            total_tokens=None if self.kind == "usage_unknown" else 1,
        )


@pytest.mark.parametrize(
    "kind",
    [
        "authentication",
        "rate_limit",
        "timeout_unknown",
        "outcome_unknown",
        "invalid_arguments",
        "unknown_tool",
        "usage_unknown",
    ],
)
def test_fail_closed_paths(dsn, kind):
    model = BadModel(kind)
    state = Worker(dsn, model, FixtureCatalog()).submit(str(uuid4()), "Chora?").values
    assert state["status"] == "failed"
    assert state["error"] == kind
    assert state["retries"] == (2 if kind == "rate_limit" else 0)
    assert model.calls == (3 if kind == "rate_limit" else 1)
    assert state["evidence"] == {}


def test_step_budget_is_persistent(dsn):
    model = FixtureModel()
    worker = Worker(dsn, model, FixtureCatalog())
    thread = str(uuid4())
    result = worker.submit(thread, "Chora?", Limits(max_steps=1))
    assert result.values["error"] == "step_budget"
    assert worker.resume(thread).values["steps"] == 1
    assert model.calls == 1


def test_database_unavailable_dispatches_nothing(dsn):
    from psycopg.conninfo import make_conninfo

    model = FixtureModel()
    bad_dsn = make_conninfo(dsn, port="1", connect_timeout="1")
    with pytest.raises(psycopg.OperationalError):
        Worker(bad_dsn, model, FixtureCatalog()).submit(str(uuid4()), "Chora?")
    assert model.calls == 0


def test_reserved_call_without_receipt_is_not_reissued(dsn):
    thread = str(uuid4())
    Journal.submit(dsn, thread, {})
    epoch = Journal.claim(dsn, thread)
    journal = Journal(dsn, thread, epoch)
    op = operation_id(thread, "model", 0)

    def crash():
        raise RuntimeError("process lost")

    with pytest.raises(RuntimeError):
        journal.perform(op, {"a": 1}, crash)
    from chora_workflow.storage import OutcomeUnknown

    with pytest.raises(OutcomeUnknown):
        journal.perform(op, {"a": 1}, lambda: pytest.fail("reissued"))
    with connect(dsn) as conn:
        assert conn.execute("SELECT count(*) AS n FROM workflow_operations").fetchone()["n"] == 1


def test_model_configuration_never_falls_back(monkeypatch):
    for name in ("CHORA_WORKFLOW_BASE_URL", "CHORA_WORKFLOW_MODEL", "CHORA_WORKFLOW_API_KEY"):
        monkeypatch.delenv(name, raising=False)
    monkeypatch.setenv("OPENAI_API_KEY", "unrelated-credential")
    with pytest.raises(ValueError, match="CHORA_WORKFLOW_BASE_URL"):
        SDKModel()


def test_crash_before_first_checkpoint_does_not_renew_deadline(dsn, monkeypatch):
    from contextlib import contextmanager
    import time
    from chora_workflow import graph

    model = FixtureModel()
    worker = Worker(dsn, model, FixtureCatalog())
    thread = str(uuid4())
    original_graph = worker.graph

    @contextmanager
    def crash_before_graph(thread):
        raise RuntimeError("crash after durable submission")
        yield

    monkeypatch.setattr(worker, "graph", crash_before_graph)
    now = time.time()
    with pytest.raises(RuntimeError, match="crash"):
        worker.submit(thread, "Chora?", Limits(duration_seconds=1))
    monkeypatch.setattr(worker, "graph", original_graph)
    monkeypatch.setattr(graph.time, "time", lambda: now + 10)
    result = worker.submit(thread, "Chora?", Limits(duration_seconds=1))
    assert result.values["error"] == "deadline"
    assert model.calls == 0


def test_receipt_replay_after_process_crash(dsn):
    thread = str(uuid4())
    script = str(Path(__file__).with_name("process_worker.py"))
    first = subprocess.run(
        [sys.executable, script, dsn, "crash-after-receipt", thread], capture_output=True, text=True
    )
    assert first.returncode == 77, first.stderr
    worker = Worker(dsn, FixtureModel(), FixtureCatalog())
    result = worker.resume(thread)
    assert result.values["status"] == "waiting"
    assert result.values["spent_tokens"] == 21
    assert worker.model.calls == 2  # First model receipt replayed from PG, not recalled.
    with connect(dsn) as conn:
        assert conn.execute("SELECT count(*) AS n FROM workflow_operations").fetchone()["n"] == 5


def test_deadline_on_gate_preserves_usage(dsn, monkeypatch):
    from chora_workflow import graph

    worker = Worker(dsn, FixtureModel(), FixtureCatalog())
    thread = str(uuid4())
    paused = worker.submit(thread, "Chora?")
    monkeypatch.setattr(graph.time, "time", lambda: paused.values["deadline"] + 1)
    result = worker.resume(thread, {**paused.values["gate"], "decision": "accept"})
    assert result.values["error"] == "deadline"
    assert result.values["spent_tokens"] == 21


def test_invalid_result_and_whole_batch_validation(dsn):
    class BatchModel(FixtureModel):
        def invoke(self, *args):
            return Reply(
                calls=[
                    Call(id="one", name="query_public", arguments={"query": "Chora"}),
                    Call(id="two", name="shell", arguments={}),
                ],
                total_tokens=1,
            )

    class CountingCatalog(FixtureCatalog):
        calls = 0

        def query(self, *args):
            self.calls += 1
            return super().query(*args)

    catalog = CountingCatalog()
    state = Worker(dsn, BatchModel(), catalog).submit(str(uuid4()), "Chora?").values
    assert state["error"] == "unknown_tool"
    assert catalog.calls == 0

    class InvalidCatalog(FixtureCatalog):
        def fetch(self, *args):
            return {"evidence_id": "readme", "content": "wrong", "digest": "wrong"}

    state = Worker(dsn, FixtureModel(), InvalidCatalog()).submit(str(uuid4()), "Chora?").values
    assert state["error"] == "invalid_tool_result"
    assert not state["evidence"]


@pytest.mark.parametrize("terminal", ["deadline", "cancelled"])
def test_crash_receipt_usage_accounted_before_stop_or_deadline(dsn, monkeypatch, terminal):
    from chora_workflow import graph

    thread = str(uuid4())
    script = str(Path(__file__).with_name("process_worker.py"))
    first = subprocess.run(
        [sys.executable, script, dsn, "crash-after-receipt", thread], capture_output=True, text=True
    )
    assert first.returncode == 77, first.stderr
    worker = Worker(dsn, FixtureModel(), FixtureCatalog())
    if terminal == "deadline":
        before = worker.inspect(thread)
        monkeypatch.setattr(graph.time, "time", lambda: before.values["deadline"] + 1)
    else:
        worker.stop(thread)
    result = worker.resume(thread)
    assert result.values["error"] == terminal
    assert result.values["spent_tokens"] == 7
    assert result.values["steps"] == 1
    assert worker.model.calls == 0


def test_stop_between_tools_preserves_completed_result(dsn):
    thread = str(uuid4())

    class BatchModel(FixtureModel):
        def invoke(self, *args):
            return Reply(
                calls=[
                    Call(id="one", name="query_public", arguments={"query": "Chora"}),
                    Call(id="two", name="read_evidence", arguments={"evidence_id": "readme"}),
                ],
                total_tokens=1,
            )

    class StopCatalog(FixtureCatalog):
        def query(self, *args):
            result = super().query(*args)
            Journal.stop(dsn, thread)
            return result

        def fetch(self, *args):
            pytest.fail("dispatched tool after cancellation")

    worker = Worker(dsn, BatchModel(), StopCatalog())
    state = worker.submit(thread, "Chora?").values
    assert state["error"] == "cancelled"
    assert len([m for m in state["messages"] if m["role"] == "tool"]) == 1
    events = worker.read_events(thread)
    assert [e["sequence"] for e in events] == list(range(1, len(events) + 1))
    assert events[-1]["category"] == "cancelled"


def test_database_loss_after_model_response_never_dispatches_tools(dsn):
    thread = str(uuid4())

    class LoseDatabase(FixtureModel):
        def invoke(self, *args):
            reply = super().invoke(*args)
            with connect(dsn) as conn:
                conn.execute("ALTER TABLE workflow_owners RENAME TO workflow_unavailable")
            return reply

    worker = Worker(dsn, LoseDatabase(), FixtureCatalog())
    with pytest.raises(psycopg.errors.UndefinedTable):
        worker.submit(thread, "Chora?")
    with connect(dsn) as conn:
        conn.execute("ALTER TABLE workflow_unavailable RENAME TO workflow_owners")
    result = worker.resume(thread)
    assert result.values["error"] == "outcome_unknown"
    assert result.values["usage_unknown"]
    assert not result.values["evidence"]
    assert worker.model.calls == 1


def test_report_requires_previously_read_evidence(dsn):
    class InvalidCitation(FixtureModel):
        def invoke(self, *args):
            reply = super().invoke(*args)
            if reply.calls[0].name == "submit_report":
                reply.calls[0].arguments["citations"] = ["other-project-private-document"]
            return reply

    state = Worker(dsn, InvalidCitation(), FixtureCatalog()).submit(str(uuid4()), "Chora?").values
    assert state["error"] == "invalid_citations"
    assert state["report"] is None
