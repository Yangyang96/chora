"""Real PG/process persistence; model and corpus are deterministic fixtures."""

import json
import subprocess
import sys
import time
from pathlib import Path
from uuid import uuid4

import psycopg
import pytest
from psycopg.types.json import Jsonb

from chora_workflow.checkpoint import StaleOwner
from chora_workflow.observation import Execution
from chora_workflow.protocol import digest, operation_id
from chora_workflow.storage import Journal, OutcomeUnknown, connect, initialize
from chora_workflow.worker import Worker
from fixtures import FixtureCatalog, FixtureModel


def test_process_observations_preserve_history_and_logical_identity(dsn):
    thread = str(uuid4())
    script = str(Path(__file__).with_name("process_worker.py"))

    def run(command):
        result = subprocess.run(
            [sys.executable, script, dsn, command, thread],
            check=True,
            capture_output=True,
            text=True,
        )
        return json.loads(result.stdout)["state"]

    before = run("submit")
    after = run("resume")
    first = before["events"][0]["observation"]
    last = after["events"][-1]["observation"]
    assert first["schema_version"] == last["schema_version"] == 1
    assert first["worker_instance_id"] != last["worker_instance_id"]
    assert first["owner_epoch"] < last["owner_epoch"]
    assert first["sample"]["sample_type"] == last["sample"]["sample_type"] == "fixture"
    assert after["events"][:-1] == before["events"]
    for key in (
        "trace_id",
        "workflow_execution_id",
        "steps",
        "retries",
        "spent_tokens",
        "deadline",
        "gate",
    ):
        assert before[key] == after[key]
    with connect(dsn) as conn:
        rows = conn.execute("SELECT * FROM workflow_operations").fetchall()
    assert len(rows) == 6
    assert all(
        row["reservation_observation"]["worker_instance_id"] == first["worker_instance_id"]
        for row in rows
    )


def rows(dsn, table):
    from psycopg import sql

    with connect(dsn) as conn:
        return conn.execute(
            sql.SQL("SELECT * FROM {} ORDER BY 1,2").format(sql.Identifier(table))
        ).fetchall()


def test_receipt_replay_retains_original_worker_time_and_usage(dsn):
    thread = str(uuid4())
    script = str(Path(__file__).with_name("process_worker.py"))
    child = subprocess.run(
        [sys.executable, script, dsn, "crash-after-receipt", thread], capture_output=True
    )
    assert child.returncode == 77, child.stderr
    original = rows(dsn, "workflow_operations")[0]
    recovered_at = time.time()
    worker = Worker(dsn, FixtureModel(), FixtureCatalog(), code_revision="a" * 40)
    result = worker.resume(thread).values
    recovered = next(
        row
        for row in rows(dsn, "workflow_operations")
        if row["operation_id"] == original["operation_id"]
    )
    assert recovered == original
    event = result["events"][0]
    assert event["operation_id"] == original["operation_id"] == operation_id(thread, "model", 0)
    assert event["input_digest"] == original["input_digest"]
    assert event["receipt_observation"] == original["receipt_observation"]
    assert event["receipt_observation"]["recorded_at"] < recovered_at
    assert event["observation"]["recorded_at"] >= recovered_at
    assert (
        event["receipt_observation"]["worker_instance_id"]
        != event["observation"]["worker_instance_id"]
    )
    assert event["receipt_observation"]["owner_epoch"] < event["observation"]["owner_epoch"]
    assert event["observation"]["code_revision"] == "a" * 40
    assert event["receipt_observation"]["code_revision"] is None
    assert event["receipt_observation"]["budget_before_node"]["spent_tokens"] == 0
    assert result["spent_tokens"] == 21
    assert worker.model.calls == 2


def test_retry_is_explicit_new_logical_step_not_transport_attempt(dsn):
    worker = Worker(dsn, FixtureModel(fail_first=True), FixtureCatalog())
    thread = str(uuid4())
    state = worker.submit(thread, "Chora?").values
    events = [e for e in state["events"] if e["kind"] == "model_receipt"]
    assert events[0]["operation_id"] == operation_id(thread, "model", 0)
    assert events[1]["operation_id"] == operation_id(thread, "model", 1)
    assert events[1]["retry_of_operation_id"] == events[0]["operation_id"]
    assert events[0]["receipt"] == {"error": "rate_limit"}
    assert all(e["logical_attempt"] == 0 for e in events)
    assert state["retries"] == 1
    assert state["steps"] == 4
    decision = {**state["gate"], "decision": "accept"}
    worker.resume(thread, decision)
    command = rows(dsn, "workflow_decisions")[0]
    another = Worker(dsn, FixtureModel(fail_first=True), FixtureCatalog(), code_revision="b" * 40)
    another.resume(thread, decision)
    assert rows(dsn, "workflow_decisions")[0] == command
    binding = {k: v for k, v in command["command"].items() if k != "command_id"}
    assert command["command"]["command_id"] == digest(binding)
    assert command["observation"]["worker_instance_id"] == worker.execution.worker_instance_id
    assert another.model.calls == 0


@pytest.mark.parametrize("label", ["real_usage", "real_model_acceptance", "made_up", "mock"])
def test_wrong_sample_label_rejected_before_submission(dsn, label):
    with pytest.raises(ValueError):
        Worker(dsn, FixtureModel(), FixtureCatalog(), sample_type=label)
    assert rows(dsn, "workflow_owners") == []


def test_sample_label_change_cannot_reclassify_existing_work(dsn):
    worker = Worker(dsn, FixtureModel(), FixtureCatalog())
    thread = str(uuid4())
    state = worker.submit(thread, "Chora?").values
    before = rows(dsn, "checkpoints")
    other = Worker(dsn, FixtureModel(), FixtureCatalog(), sample_type="load_test")
    with pytest.raises(ValueError, match="classification changed"):
        other.resume(thread, {**state["gate"], "decision": "accept"})
    assert rows(dsn, "checkpoints") == before
    assert rows(dsn, "workflow_decisions") == []
    assert other.model.calls == 0


@pytest.mark.parametrize("field", ["worker_instance_id", "schema_version", "sample"])
def test_partial_new_event_is_refused_without_recovery_writes(dsn, field):
    worker = Worker(dsn, FixtureModel(), FixtureCatalog())
    thread = str(uuid4())
    state = worker.submit(thread, "Chora?").values
    state["events"][0]["observation"].pop(field)
    with worker.graph(thread) as graph:
        graph.update_state(worker.config(thread), {"events": state["events"]})
    before = rows(dsn, "checkpoints")
    with pytest.raises(ValueError):
        worker.read_events(thread)
    with pytest.raises(ValueError):
        worker.resume(thread, {**state["gate"], "decision": "accept"})
    assert rows(dsn, "checkpoints") == before
    assert rows(dsn, "workflow_decisions") == []
    assert worker.model.calls == 3


def test_legacy_metadata_is_unknown_not_backfilled(dsn):
    worker = Worker(dsn, FixtureModel(), FixtureCatalog())
    thread = str(uuid4())
    state = worker.submit(thread, "Chora?").values
    for event in state["events"]:
        event.pop("observation")
        event.pop("receipt_observation", None)
    with worker.graph(thread) as graph:
        graph.update_state(worker.config(thread), {"events": state["events"]}, as_node="tools")
        # update_state creates a new checkpoint; re-enter review to persist its interrupt.
        graph.invoke(None, worker.config(thread), durability="sync")
    checkpoint_rows = rows(dsn, "checkpoints")
    with connect(dsn) as conn:
        conn.execute(
            "ALTER TABLE workflow_operations DROP COLUMN reservation_observation, DROP COLUMN receipt_observation"
        )
        conn.execute("ALTER TABLE workflow_decisions DROP COLUMN observation")
        conn.execute("UPDATE workflow_owners SET submission = submission #- '{initial,sample}'")
    initialize(dsn)
    assert rows(dsn, "checkpoints") == checkpoint_rows
    assert all(
        row["reservation_observation"] is None and row["receipt_observation"] is None
        for row in rows(dsn, "workflow_operations")
    )
    assert all(event["observation"] is None for event in worker.read_events(thread))
    result = worker.resume(thread, {**state["gate"], "decision": "accept"}).values
    assert result["events"][:-1] == state["events"]
    assert result["events"][-1]["observation"]["schema_version"] == 1
    assert worker.model.calls == 3


@pytest.mark.parametrize("phase", ["reservation", "receipt"])
def test_collection_failure_is_atomic_and_never_reissues_unknown_call(dsn, phase):
    with connect(dsn) as conn:
        condition = (
            "reservation_observation IS NULL"
            if phase == "reservation"
            else "receipt_observation IS NULL"
        )
        conn.execute(
            f"ALTER TABLE workflow_operations ADD CONSTRAINT fail_collection CHECK ({condition})"
        )
    worker = Worker(dsn, FixtureModel(), FixtureCatalog())
    thread = str(uuid4())
    with pytest.raises(psycopg.errors.CheckViolation):
        worker.submit(thread, "Chora?")
    operations = rows(dsn, "workflow_operations")
    assert worker.model.calls == (0 if phase == "reservation" else 1)
    if phase == "receipt":
        assert len(operations) == 1
        assert operations[0]["receipt"] is None
        assert operations[0]["receipt_observation"] is None
        assert operations[0]["reservation_observation"] is not None
    else:
        assert operations == []
    with connect(dsn) as conn:
        conn.execute("ALTER TABLE workflow_operations DROP CONSTRAINT fail_collection")
    if phase == "receipt":
        recovered_worker = Worker(dsn, FixtureModel(), FixtureCatalog())
        result = recovered_worker.resume(thread).values
        assert result["error"] == "outcome_unknown"
        assert result["usage_unknown"] is True
        assert rows(dsn, "workflow_operations") == operations
        assert recovered_worker.model.calls == 0


def test_takeover_rejects_late_receipt_metadata_and_reuses_operation(dsn):
    thread = str(uuid4())
    Journal.submit(dsn, thread, {})
    journal = Journal(dsn, thread, Journal.claim(dsn, thread))
    op = operation_id(thread, "model", 0)

    def action():
        Journal.claim(dsn, thread)
        return {"reply": "late"}

    with pytest.raises(StaleOwner):
        journal.perform(op, {"a": 1}, action)
    original = rows(dsn, "workflow_operations")
    assert original[0]["receipt"] is None
    assert original[0]["receipt_observation"] is None
    new = Journal(dsn, thread, Journal.claim(dsn, thread))
    with pytest.raises(OutcomeUnknown):
        new.perform(op, {"a": 1}, lambda: pytest.fail("reissued unknown operation"))
    with pytest.raises(ValueError, match="input changed"):
        new.lookup(op, {"a": 2})
    assert rows(dsn, "workflow_operations") == original


def test_invalid_persisted_receipt_label_is_not_silently_replayed(dsn):
    thread = str(uuid4())
    Journal.submit(dsn, thread, {})
    journal = Journal(
        dsn, thread, Journal.claim(dsn, thread), Execution(FixtureModel(), FixtureCatalog())
    )
    op = operation_id(thread, "model", 0)
    journal.perform(op, {}, lambda: {"reply": "fixture"})
    row = rows(dsn, "workflow_operations")[0]
    bad = row["receipt_observation"]
    bad["sample"]["sample_type"] = "real_usage"
    with connect(dsn) as conn:
        conn.execute("UPDATE workflow_operations SET receipt_observation=%s", (Jsonb(bad),))
    before = rows(dsn, "workflow_operations")
    with pytest.raises(ValueError, match="real sample"):
        journal.perform(op, {}, lambda: pytest.fail("reissued"))
    assert rows(dsn, "workflow_operations") == before


@pytest.mark.parametrize("sample_type", ["deterministic_injection", "load_test"])
def test_explicit_test_purpose_survives_resume(dsn, sample_type):
    thread = str(uuid4())
    worker = Worker(dsn, FixtureModel(), FixtureCatalog(), sample_type=sample_type)
    state = worker.submit(thread, "Chora?").values
    fresh = Worker(dsn, FixtureModel(), FixtureCatalog(), sample_type=sample_type)
    final = fresh.resume(thread, {**state["gate"], "decision": "accept"}).values
    assert final["events"][-1]["observation"]["sample"] == {
        "sample_type": sample_type,
        "source": "operator",
        "model_mode": "fixture",
        "catalog_mode": "fixture",
    }
    assert fresh.model.calls == 0


def test_receipt_update_holds_epoch_lock_through_metadata_write(dsn):
    from concurrent.futures import ThreadPoolExecutor
    from threading import Event

    thread = str(uuid4())
    Journal.submit(dsn, thread, {})
    journal = Journal(dsn, thread, Journal.claim(dsn, thread))
    op = operation_id(thread, "model", 0)
    # Hold a second row so a real PG trigger stops the actual receipt UPDATE.
    with connect(dsn) as conn:
        conn.execute("CREATE TABLE receipt_barrier (id int PRIMARY KEY)")
        conn.execute("INSERT INTO receipt_barrier VALUES (1)")
        conn.execute("""CREATE FUNCTION wait_receipt() RETURNS trigger LANGUAGE plpgsql AS $$
            BEGIN PERFORM id FROM receipt_barrier WHERE id=1 FOR UPDATE; RETURN NEW; END $$""")
        conn.execute(
            "CREATE TRIGGER receipt_wait BEFORE UPDATE ON workflow_operations FOR EACH ROW EXECUTE FUNCTION wait_receipt()"
        )
    dispatched = Event()

    def action():
        dispatched.set()
        return {"result": "fixture"}

    with connect(dsn) as blocker, ThreadPoolExecutor(max_workers=1) as pool:
        with blocker.transaction():
            blocker.execute("SELECT * FROM receipt_barrier FOR UPDATE")
            future = pool.submit(journal.perform, op, {}, action)
            assert dispatched.wait(5)
            # Wait for the exact UPDATE to block on the trigger, not an arbitrary delay.
            with connect(dsn) as observer:
                until = time.monotonic() + 5
                while time.monotonic() < until:
                    waiting = observer.execute(
                        "SELECT count(*) AS n FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE 'UPDATE workflow_operations SET receipt=%'"
                    ).fetchone()["n"]
                    if waiting:
                        break
                    time.sleep(0.01)
                assert waiting
                observer.execute("SET lock_timeout='150ms'")
                with pytest.raises(psycopg.errors.LockNotAvailable):
                    observer.execute(
                        "UPDATE workflow_owners SET owner_epoch=owner_epoch+1 WHERE thread_id=%s",
                        (thread,),
                    )
                row = observer.execute("SELECT * FROM workflow_operations").fetchone()
                assert row["receipt"] is row["receipt_observation"] is None
        assert future.result(timeout=5) == {"result": "fixture"}
    row = rows(dsn, "workflow_operations")[0]
    assert row["receipt_observation"]["owner_epoch"] == journal.epoch
    Journal.claim(dsn, thread)
    with pytest.raises(StaleOwner):
        journal.lookup(op, {})


def test_missing_new_receipt_observation_is_not_legacy(dsn):
    thread = str(uuid4())
    Journal.submit(dsn, thread, {})
    journal = Journal(dsn, thread, Journal.claim(dsn, thread))
    op = operation_id(thread, "model", 0)
    journal.perform(op, {}, lambda: {"reply": "fixture"})
    with connect(dsn) as conn:
        conn.execute("UPDATE workflow_operations SET receipt_observation=NULL")
    with pytest.raises(ValueError, match="missing observation"):
        journal.perform(op, {}, lambda: pytest.fail("reissued"))


def test_persisted_sample_change_refused_before_decision_write(dsn):
    worker = Worker(dsn, FixtureModel(), FixtureCatalog())
    thread = str(uuid4())
    state = worker.submit(thread, "Chora?").values
    changed = {**state["sample"], "sample_type": "load_test", "source": "operator"}
    with worker.graph(thread) as graph:
        graph.update_state(worker.config(thread), {"sample": changed})
    before = rows(dsn, "checkpoints")
    with pytest.raises(ValueError, match="classification changed"):
        worker.resume(thread, {**state["gate"], "decision": "accept"})
    assert rows(dsn, "checkpoints") == before
    assert rows(dsn, "workflow_decisions") == []
