"""Synthetic acceptance ledgers and real PG; no provider or credential discovery."""

import json
import os
import sqlite3
import subprocess
import sys
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
from uuid import uuid4

import pytest

from chora_workflow import batch
from chora_workflow.batch import BatchLedger, BatchRefused, LEGACY_POLICY, POLICY
from chora_workflow.protocol import digest
from chora_workflow.worker import Worker
from fixtures import FixtureCatalog, FixtureModel
from chora_workflow.model import ModelFailure
from test_model import payload, research_response

pytest_plugins = ["test_model"]

FIRST = 1700000000.0
REVISION = "a" * 40
WORKFLOW = "00000000-0000-4000-8000-000000000001"
SECOND_WORKFLOW = "00000000-0000-4000-8000-000000000002"


def authorization(snapshot):
    return {
        "version": 1,
        "batch_id": snapshot["batch"]["batch_id"],
        "authorization_ref": "synthetic-user-approval",
        "expected_history_digest": digest(snapshot),
        "expected_requests": len(snapshot["requests"]),
        "expected_tokens": sum(
            json.loads(row["usage"])["total_tokens"] for row in snapshot["requests"]
        ),
        "expected_policy": LEGACY_POLICY.copy(),
        "new_policy": POLICY.copy(),
        "candidate_revision": REVISION,
        "previous_workflow_id": str(uuid4()),
        "external_stop": {
            "reason": "input_admission",
            "occurred_at": FIRST + 100,
            "evidence_ref": "synthetic-admission-stop",
        },
        "quality_check_ref": "synthetic-offline-quality-check",
        "review_check_ref": "synthetic-offline-review-check",
    }


@pytest.fixture
def legacy(monkeypatch, tmp_path):
    monkeypatch.setattr(batch.time, "time", lambda: FIRST)
    ledger = BatchLedger.create(tmp_path / "synthetic.sqlite")
    for tokens in (757, 1094):
        request = ledger.reserve(payload())
        ledger.finish(
            request["request_id"],
            {"prompt_tokens": tokens - 10, "completion_tokens": 10, "total_tokens": tokens},
            "response",
        )
    with sqlite3.connect(ledger.path) as conn:
        conn.execute("UPDATE batch SET policy=?", (json.dumps(LEGACY_POLICY),))
    monkeypatch.setattr(batch.time, "time", lambda: FIRST + 2000)
    snapshot = BatchLedger.inspect(ledger.path)
    return ledger.path, snapshot, authorization(snapshot)


def reserve(ledger, approval, request=None):
    return ledger.reserve(
        request or payload(),
        workflow_id=WORKFLOW,
        code_revision=approval["candidate_revision"],
    )


def complete(ledger, request):
    ledger.finish(
        request["request_id"],
        {"prompt_tokens": 5, "completion_tokens": 2, "total_tokens": 7},
        "response",
    )


def test_registration_is_explicit_read_only_until_commit_and_does_not_start_clock(
    legacy, monkeypatch
):
    path, original, approval = legacy
    before = path.read_bytes()
    assert BatchLedger.inspect(path) == original
    with pytest.raises(ValueError, match="policy changed"):
        BatchLedger(path)
    assert path.read_bytes() == before
    registered = BatchLedger.register_continuation(path, approval)
    assert registered["batch"] == original["batch"]
    assert registered["requests"] == original["requests"]
    assert json.loads(registered["continuation"]["original_snapshot"]) == original
    assert registered["continuation"]["activated_at"] is None
    assert registered["continuation"]["deadline"] is None
    monkeypatch.setattr(batch.time, "time", lambda: FIRST + 90000)
    assert BatchLedger(path).snapshot() == registered
    request = reserve(BatchLedger(path), approval)
    active = BatchLedger(path).snapshot()
    assert active["continuation"]["activated_at"] == FIRST + 90000
    assert request["deadline"] == FIRST + 91200
    assert active["batch"]["first_request_at"] == FIRST
    assert active["requests"][:2] == original["requests"]
    assert active["requests"][2]["requests_remaining"] == 17
    assert (
        active["requests"][2]["tokens_remaining"]
        == 100000 - 1851 - batch.input_bound(payload()) - 2048
    )
    assert active["continuation"]["first_request_id"] == request["request_id"]


@pytest.mark.parametrize(
    "field,value",
    [
        ("authorization_ref", ""),
        ("quality_check_ref", ""),
        ("review_check_ref", ""),
        ("batch_id", str(uuid4())),
        ("expected_history_digest", "0" * 64),
        ("expected_requests", 1),
        ("expected_tokens", 0),
        ("expected_policy", POLICY),
        ("new_policy", LEGACY_POLICY),
        ("candidate_revision", "unknown"),
        ("unexpected", "reject"),
    ],
)
def test_bad_authorization_leaves_ledger_bytes_unchanged(legacy, field, value):
    path, original, approval = legacy
    before = path.read_bytes()
    with pytest.raises(ValueError):
        BatchLedger.register_continuation(path, {**approval, field: value})
    assert path.read_bytes() == before
    assert BatchLedger.inspect(path) == original


@pytest.mark.parametrize("field", ["authorization_ref", "external_stop", "expected_history_digest"])
def test_missing_authorization_fields_refuse(legacy, field):
    path, _, approval = legacy
    approval.pop(field)
    with pytest.raises(ValueError):
        BatchLedger.register_continuation(path, approval)


@pytest.mark.parametrize(
    "reason",
    ["usage_unknown", "operator_stopped", "deadline", "provider_budget_violation", "cancelled"],
)
def test_other_halts_cannot_be_released(legacy, reason):
    path, _, approval = legacy
    with sqlite3.connect(path) as conn:
        conn.execute("UPDATE batch SET halt_reason=?", (reason,))
    approval["expected_history_digest"] = digest(BatchLedger.inspect(path))
    with pytest.raises(BatchRefused, match=reason):
        BatchLedger.register_continuation(path, approval)
    assert "continuation" not in BatchLedger.inspect(path)


@pytest.mark.parametrize(
    "change", ["pending", "unknown_usage", "unknown_outcome", "timeout", "exceeded_output", "late"]
)
def test_unsafe_history_cannot_be_laundered_by_approval(legacy, change):
    path, _, approval = legacy
    with sqlite3.connect(path) as conn:
        if change == "pending":
            conn.execute("UPDATE requests SET completed_at=NULL WHERE rowid=1")
        elif change == "unknown_usage":
            conn.execute("UPDATE requests SET usage=NULL WHERE rowid=1")
        elif change == "unknown_outcome":
            conn.execute("UPDATE requests SET outcome=NULL WHERE rowid=1")
        elif change == "timeout":
            conn.execute("UPDATE requests SET outcome='timeout_unknown' WHERE rowid=1")
        elif change == "exceeded_output":
            conn.execute("UPDATE requests SET output_limit=1 WHERE rowid=1")
        else:
            conn.execute("UPDATE requests SET completed_at=deadline WHERE rowid=1")
    snapshot = BatchLedger.inspect(path)
    approval["expected_history_digest"] = digest(snapshot)
    before = path.read_bytes()
    with pytest.raises(BatchRefused):
        BatchLedger.register_continuation(path, approval)
    assert path.read_bytes() == before


def test_same_registration_is_idempotent_before_and_after_activation(legacy):
    path, _, approval = legacy
    registered = BatchLedger.register_continuation(path, approval)
    before = path.read_bytes()
    assert BatchLedger.register_continuation(path, approval) == registered
    assert path.read_bytes() == before
    ledger = BatchLedger(path)
    complete(ledger, reserve(ledger, approval))
    active = ledger.snapshot()
    assert BatchLedger.register_continuation(path, approval) == active
    with pytest.raises(ValueError, match="conflict"):
        BatchLedger.register_continuation(
            path, {**approval, "authorization_ref": "second-approval"}
        )
    assert ledger.snapshot() == active


def test_concurrent_registration_and_first_reservation_commit_one_window(legacy):
    path, original, approval = legacy
    with ThreadPoolExecutor(max_workers=6) as pool:
        snapshots = list(
            pool.map(lambda _: BatchLedger.register_continuation(path, approval), range(6))
        )
    assert all(s == snapshots[0] for s in snapshots)

    def attempt(_):
        try:
            return reserve(BatchLedger(path), approval)
        except BatchRefused as exc:
            return exc.category

    with ThreadPoolExecutor(max_workers=8) as pool:
        results = list(pool.map(attempt, range(8)))
    successful = [r for r in results if isinstance(r, dict)]
    assert len(successful) == 1
    assert results.count("outcome_unknown") == 7
    snapshot = BatchLedger(path).snapshot()
    assert snapshot["requests"][:2] == original["requests"]
    assert len(snapshot["requests"]) == 3
    assert snapshot["continuation"]["first_request_id"] == successful[0]["request_id"]


@pytest.mark.parametrize("crash_after", ["insert", "activation", "commit"])
def test_process_crash_rolls_back_or_preserves_exactly_one_activation(legacy, crash_after):
    path, _, approval = legacy
    BatchLedger.register_continuation(path, approval)
    source = """
import json, os, sqlite3, sys
from chora_workflow.batch import BatchLedger
from test_model import payload

path, approval, boundary = sys.argv[1], json.loads(sys.argv[2]), sys.argv[3]


class Connection(sqlite3.Connection):
    def execute(self, statement, parameters=()):
        cursor = super().execute(statement, parameters)
        if (boundary == "insert" and statement.startswith("INSERT INTO requests")) or (
            boundary == "activation"
            and statement.startswith("UPDATE continuation SET activated_at")
        ):
            os._exit(71)
        return cursor


original = sqlite3.connect
sqlite3.connect = lambda *a, **kw: original(*a, **{**kw, "factory": Connection})
ledger = BatchLedger(path)
ledger.reserve(
    payload(),
    workflow_id="00000000-0000-4000-8000-000000000001",
    code_revision=approval["candidate_revision"],
)
os._exit(72)
"""
    child = subprocess.run(
        [sys.executable, "-c", source, str(path), json.dumps(approval), crash_after],
        env={**os.environ, "PYTHONPATH": str(Path(__file__).parent)},
        capture_output=True,
        text=True,
        timeout=20,
    )
    assert child.returncode == (72 if crash_after == "commit" else 71), child.stderr
    ledger = BatchLedger(path)
    snapshot = ledger.snapshot()
    if crash_after == "commit":
        assert len(snapshot["requests"]) == 3
        assert snapshot["continuation"]["activated_at"] is not None
        with pytest.raises(BatchRefused, match="outcome_unknown"):
            reserve(ledger, approval)
    else:
        assert len(snapshot["requests"]) == 2
        assert snapshot["continuation"]["activated_at"] is None
        reserve(ledger, approval)
        assert len(ledger.snapshot()["requests"]) == 3


def test_expiry_never_renews_again_and_failed_preflight_does_not_activate(legacy, monkeypatch):
    path, _, approval = legacy
    BatchLedger.register_continuation(path, approval)
    ledger = BatchLedger(path)
    with pytest.raises(BatchRefused, match="output_budget"):
        reserve(ledger, approval, {**payload(), "max_tokens": 2049})
    oversized = {**payload(), "messages": [{"role": "user", "content": "x" * 50000}]}
    with pytest.raises(BatchRefused, match="token_budget"):
        reserve(ledger, approval, oversized)
    assert ledger.snapshot()["continuation"]["activated_at"] is None
    request = reserve(ledger, approval)
    complete(ledger, request)
    monkeypatch.setattr(batch.time, "time", lambda: request["deadline"])
    with pytest.raises(BatchRefused, match="deadline"):
        reserve(BatchLedger(path), approval)
    snapshot = ledger.snapshot()
    assert BatchLedger.register_continuation(path, approval) == snapshot
    with pytest.raises(BatchRefused, match="deadline"):
        reserve(BatchLedger(path), approval)


def test_request_cap_remains_cumulative_and_new_unknown_halts(legacy):
    path, _, approval = legacy
    BatchLedger.register_continuation(path, approval)
    ledger = BatchLedger(path)
    for _ in range(18):
        complete(ledger, reserve(ledger, approval))
    with pytest.raises(BatchRefused, match="request_budget"):
        reserve(ledger, approval)
    assert len(ledger.snapshot()["requests"]) == 20
    assert ledger.snapshot()["requests"][-1]["tokens_remaining"] == 100000 - 1851 - 18 * 7


def test_only_original_input_stop_released_and_later_stop_preserved(legacy):
    path, _, approval = legacy
    with sqlite3.connect(path) as conn:
        conn.execute("UPDATE batch SET halt_reason='input_admission'")
    approval["expected_history_digest"] = digest(BatchLedger.inspect(path))
    BatchLedger.register_continuation(path, approval)
    ledger = BatchLedger(path)
    request = reserve(ledger, approval)
    assert ledger.snapshot()["batch"]["halt_reason"] is None
    with sqlite3.connect(path) as conn:
        conn.execute("UPDATE batch SET halt_reason='operator_stopped'")
    complete(ledger, request)
    with pytest.raises(BatchRefused, match="operator_stopped"):
        reserve(ledger, approval)
    assert (
        json.loads(ledger.snapshot()["continuation"]["original_snapshot"])["batch"]["halt_reason"]
        == "input_admission"
    )


def test_workflow_candidate_and_unbound_sdk_refuse_before_reservation(legacy):
    path, _, approval = legacy
    BatchLedger.register_continuation(path, approval)
    ledger = BatchLedger(path)
    for context in (
        {},
        {"workflow_id": approval["previous_workflow_id"], "code_revision": REVISION},
        {"workflow_id": WORKFLOW, "code_revision": "b" * 40},
    ):
        with pytest.raises(ValueError, match="workflow or candidate"):
            ledger.reserve(payload(), **context)
    assert ledger.snapshot()["continuation"]["activated_at"] is None


def test_legacy_inspect_and_registration_cli_needs_no_model_configuration(legacy, tmp_path):
    path, original, approval = legacy
    auth_file = tmp_path / "approval.json"
    auth_file.write_text(json.dumps(approval))
    env = {k: v for k, v in os.environ.items() if not k.startswith("CHORA_WORKFLOW_")}
    for command in ("inspect-batch", "register-continuation", "inspect-batch"):
        arguments = [sys.executable, "-m", "chora_workflow", command, "--batch-file", str(path)]
        if command == "register-continuation":
            arguments += ["--authorization-file", str(auth_file)]
        child = subprocess.run(arguments, env=env, capture_output=True, text=True, timeout=20)
        assert child.returncode == 0, child.stderr
        result = json.loads(child.stdout)
        assert result["history_digest"] == digest(original)
        assert result["requests"] == original["requests"]
        if "continuation" in result:
            assert result["continuation"]["activated_at"] is None


@pytest.mark.parametrize("mutation", ["wrong_workflow", "wrong_revision"])
def test_workflow_link_validation(legacy, mutation):
    path, original, approval = legacy
    BatchLedger.register_continuation(path, approval)
    ledger = BatchLedger(path)
    with pytest.raises(ValueError):
        ledger.validate_workflow(
            approval["previous_workflow_id"] if mutation == "wrong_workflow" else WORKFLOW,
            "b" * 40 if mutation == "wrong_revision" else REVISION,
        )
    assert ledger.snapshot()["continuation"]["activated_at"] is None


def test_independent_processes_share_one_first_reservation(legacy):
    path, original, approval = legacy
    BatchLedger.register_continuation(path, approval)
    source = """
import json, sys
from chora_workflow.batch import BatchLedger, BatchRefused
from test_model import payload

approval = json.loads(sys.argv[2])
try:
    row = BatchLedger(sys.argv[1]).reserve(
        payload(),
        workflow_id="00000000-0000-4000-8000-000000000001",
        code_revision=approval["candidate_revision"],
    )
    print(json.dumps(row))
except BatchRefused as exc:
    print(json.dumps({"refusal": exc.category}))
"""
    children = [
        subprocess.Popen(
            [sys.executable, "-c", source, str(path), json.dumps(approval)],
            env={**os.environ, "PYTHONPATH": str(Path(__file__).parent)},
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
        )
        for _ in range(4)
    ]
    results = []
    try:
        for child in children:
            stdout, stderr = child.communicate(timeout=20)
            assert child.returncode == 0, stderr
            results.append(json.loads(stdout))
    finally:
        for child in children:
            if child.poll() is None:
                child.kill()
                child.communicate()
    assert sum("request_id" in row for row in results) == 1
    assert sum(row.get("refusal") == "outcome_unknown" for row in results) == 3
    snapshot = BatchLedger(path).snapshot()
    assert len(snapshot["requests"]) == 3
    assert snapshot["requests"][:2] == original["requests"]
    assert snapshot["continuation"]["deadline"] == snapshot["continuation"]["activated_at"] + 1200


@pytest.mark.parametrize("fault", ["usage_unknown", "pending", "late", "provider_bound"])
def test_new_continuation_failures_preserve_history_and_prevent_progress(
    legacy, monkeypatch, fault
):
    path, original, approval = legacy
    BatchLedger.register_continuation(path, approval)
    ledger = BatchLedger(path)
    request = reserve(ledger, approval)
    if fault == "pending":
        expected = "outcome_unknown"
    else:
        expected = {
            "usage_unknown": "usage_unknown",
            "late": "deadline",
            "provider_bound": "provider_budget_violation",
        }[fault]
        usage = (
            None
            if fault == "usage_unknown"
            else {"prompt_tokens": 5, "completion_tokens": 2, "total_tokens": 7}
        )
        if fault == "late":
            monkeypatch.setattr(batch.time, "time", lambda: request["deadline"])
        if fault == "provider_bound":
            usage = {
                "prompt_tokens": request["input_bound"] + 1,
                "completion_tokens": 0,
                "total_tokens": request["input_bound"] + 1,
            }
        with pytest.raises(BatchRefused, match=expected):
            ledger.finish(request["request_id"], usage, "response")
    with pytest.raises(BatchRefused, match=expected):
        reserve(BatchLedger(path), approval)
    snapshot = ledger.snapshot()
    assert snapshot["requests"][:2] == original["requests"]
    assert BatchLedger.register_continuation(path, approval) == snapshot


def failed_predecessor(
    dsn, approval, *, error="token_budget", unknown=False, batch_id=None, limits=None
):
    model = FixtureModel()
    model.identity = {
        "adapter": "synthetic-old-sdk",
        "acceptance_batch": {
            "batch_id": batch_id or approval["batch_id"],
            "policy": LEGACY_POLICY.copy(),
        },
    }
    invoke = model.invoke

    def admission_refusal(*args):
        if model.calls == 2:
            raise ModelFailure(error, usage_unknown=unknown)
        return invoke(*args)

    model.invoke = admission_refusal
    worker = Worker(dsn, model, FixtureCatalog())
    state = worker.submit(approval["previous_workflow_id"], "Chora?", limits)
    assert state.values["status"] == "failed"
    return worker, state


@pytest.mark.postgres
@pytest.mark.parametrize("decision", ["accept", "reject"])
def test_linked_new_workflow_real_pg_sdk_mock_gate_new_process_and_old_graph_refusal(
    dsn, legacy, sdk_setup, monkeypatch, decision
):
    from datetime import datetime, timezone
    from chora_workflow.storage import connect

    path, original, approval = legacy
    # Actual wall time permits a fresh interpreter to resume within both deadlines.
    monkeypatch.setattr(batch.time, "time", lambda: datetime.now(timezone.utc).timestamp())
    setup, _, requests = sdk_setup
    old, old_state = failed_predecessor(dsn, approval)
    with connect(dsn) as conn:
        old_epoch = conn.execute(
            "SELECT owner_epoch FROM workflow_owners WHERE thread_id=%s",
            (approval["previous_workflow_id"],),
        ).fetchone()["owner_epoch"]
    BatchLedger.register_continuation(path, approval)
    monkeypatch.setenv("CHORA_WORKFLOW_BATCH_LEDGER", str(path))
    sdk = setup(research_response)
    worker = Worker(dsn, sdk, FixtureCatalog(), code_revision=REVISION)
    with pytest.raises(ValueError, match="workflow or candidate"):
        worker.resume(approval["previous_workflow_id"])
    with pytest.raises(ValueError, match="workflow or candidate"):
        Worker(dsn, setup(research_response), FixtureCatalog()).submit(WORKFLOW, "Chora?")
    with connect(dsn) as conn:
        assert (
            conn.execute(
                "SELECT owner_epoch FROM workflow_owners WHERE thread_id=%s",
                (approval["previous_workflow_id"],),
            ).fetchone()["owner_epoch"]
            == old_epoch
        )
        assert conn.execute("SELECT count(*) AS n FROM workflow_decisions").fetchone()["n"] == 0
    assert worker.inspect(approval["previous_workflow_id"]).values == old_state.values
    assert len(BatchLedger(path).snapshot()["requests"]) == 2
    waiting = worker.submit(WORKFLOW, "What is Chora?")
    assert waiting.values["status"] == "waiting"
    assert waiting.values["report"]["citations"]
    assert len(requests) == 3
    selected = {**waiting.values["gate"], "decision": decision}
    source = """
import json, sys, httpx
from openai import OpenAI
from chora_workflow import model
from chora_workflow.worker import Worker
from fixtures import FixtureCatalog


def refuse(request):
    raise AssertionError("gate resume dispatched a model request")


model.OpenAI = lambda **kw: OpenAI(
    **kw, http_client=httpx.Client(transport=httpx.MockTransport(refuse))
)
sdk = model.SDKModel()
sdk.observation_mode = "mock"
worker = Worker(sys.argv[1], sdk, FixtureCatalog(), code_revision=sys.argv[4])
print(json.dumps(worker.resume(sys.argv[2], json.loads(sys.argv[3])).values))
"""
    child = subprocess.run(
        [
            sys.executable,
            "-c",
            source,
            dsn,
            WORKFLOW,
            json.dumps(selected),
            REVISION,
        ],
        env={**os.environ, "PYTHONPATH": str(Path(__file__).parent)},
        capture_output=True,
        text=True,
        timeout=20,
    )
    assert child.returncode == 0, child.stderr
    state = json.loads(child.stdout)
    assert state["status"] == ("accepted" if decision == "accept" else "rejected")
    assert worker.resume(WORKFLOW, selected).values == state
    with pytest.raises(ValueError, match="conflict"):
        worker.resume(
            WORKFLOW,
            {**selected, "decision": "reject" if decision == "accept" else "accept"},
        )
    with pytest.raises(ValueError, match="stale"):
        worker.resume(WORKFLOW, {**selected, "input_digest": "stale"})
    second = worker.submit(SECOND_WORKFLOW, "What is Chora?")
    opposite = "reject" if decision == "accept" else "accept"
    second_result = worker.resume(SECOND_WORKFLOW, {**second.values["gate"], "decision": opposite})
    assert second_result.values["status"] == ("accepted" if opposite == "accept" else "rejected")
    assert len(requests) == 6
    snapshot = BatchLedger(path).snapshot()
    assert snapshot["requests"][:2] == original["requests"]
    assert len(snapshot["requests"]) == 8
    assert snapshot["requests"][-1]["tokens_remaining"] == 100000 - 1851 - 42
    assert [row["workflow_id"] for row in snapshot["continuation_requests"]] == [WORKFLOW] * 3 + [
        SECOND_WORKFLOW
    ] * 3
    assert worker.inspect(approval["previous_workflow_id"]).values == old_state.values


def test_continuation_reserves_exact_remaining_input_and_output_before_activation(legacy):
    path, _, approval = legacy
    BatchLedger.register_continuation(path, approval)
    ledger = BatchLedger(path)
    request = payload()
    base = batch.input_bound(request)
    output = 2046 if (100000 - 1851 - 2046 - base) % 2 == 0 else 2047
    request["max_tokens"] = output
    characters = (100000 - 1851 - output - batch.input_bound(request)) // 2
    request["messages"][0]["content"] += "x" * characters
    assert batch.input_bound(request) + output + 1851 == 100000
    with pytest.raises(BatchRefused, match="token_budget"):
        reserve(ledger, approval, {**request, "max_tokens": output + 1})
    assert ledger.snapshot()["continuation"]["activated_at"] is None
    reservation = reserve(ledger, approval, request)
    assert ledger.snapshot()["requests"][-1]["tokens_remaining"] == 0
    complete(ledger, reservation)
    assert ledger.snapshot()["requests"][-1]["tokens_remaining"] == 100000 - 1851 - 7


def test_conflicting_concurrent_registration_commits_one_authorization(legacy):
    path, _, approval = legacy

    def attempt(index):
        try:
            BatchLedger.register_continuation(
                path, {**approval, "authorization_ref": f"synthetic-{index}"}
            )
            return "registered"
        except ValueError:
            return "conflict"

    with ThreadPoolExecutor(max_workers=4) as pool:
        results = list(pool.map(attempt, range(4)))
    assert results.count("registered") == 1
    assert results.count("conflict") == 3
    assert BatchLedger.inspect(path)["continuation"]["activated_at"] is None


def test_registration_crash_rolls_back_schema_and_audit_together(legacy):
    path, original, approval = legacy
    source = """
import json, os, sqlite3, sys
from chora_workflow.batch import BatchLedger


class Connection(sqlite3.Connection):
    def execute(self, statement, parameters=()):
        cursor = super().execute(statement, parameters)
        if statement.startswith("INSERT INTO continuation"):
            os._exit(73)
        return cursor


original = sqlite3.connect
sqlite3.connect = lambda *a, **kw: original(*a, **{**kw, "factory": Connection})
BatchLedger.register_continuation(sys.argv[1], json.loads(sys.argv[2]))
"""
    child = subprocess.run(
        [sys.executable, "-c", source, str(path), json.dumps(approval)],
        capture_output=True,
        text=True,
        timeout=20,
    )
    assert child.returncode == 73, child.stderr
    assert BatchLedger.inspect(path) == original
    assert BatchLedger.register_continuation(path, approval)["continuation"]["activated_at"] is None


def test_continued_sdk_rate_limit_has_zero_retries_and_retains_unknown(
    legacy, sdk_setup, monkeypatch
):
    import httpx
    from chora_workflow.model import ModelFailure

    path, original, approval = legacy
    setup, _, requests = sdk_setup
    BatchLedger.register_continuation(path, approval)
    monkeypatch.setenv("CHORA_WORKFLOW_BATCH_LEDGER", str(path))
    sdk = setup(lambda _: httpx.Response(429, json={"error": {"message": "synthetic refusal"}}))
    sdk.bind_workflow(WORKFLOW, REVISION)
    with pytest.raises(ModelFailure, match="rate_limit"):
        sdk.invoke(payload()["messages"], [], 10, 100)
    with pytest.raises(ModelFailure, match="usage_unknown"):
        sdk.invoke(payload()["messages"], [], 10, 100)
    assert sdk.retry_limit == sdk.client.max_retries == 0
    assert len(requests) == 1
    snapshot = BatchLedger(path).snapshot()
    assert snapshot["requests"][:2] == original["requests"]
    assert len(snapshot["requests"]) == 3
    assert snapshot["requests"][-1]["usage"] is None
    assert snapshot["batch"]["halt_reason"] == "usage_unknown"


@pytest.mark.postgres
@pytest.mark.parametrize(
    "fault",
    [
        "cancelled",
        "unknown",
        "pending_operation",
        "pending_write",
        "other_failure",
        "wrong_batch",
        "graph_token_budget",
        "missing",
    ],
)
def test_predecessor_pg_refusals_leave_window_unstarted_and_create_no_new_graph(
    dsn, legacy, sdk_setup, monkeypatch, fault
):
    from chora_workflow.storage import connect
    from langgraph.checkpoint.postgres import PostgresSaver
    from chora_workflow.protocol import Limits

    path, original, approval = legacy
    setup, _, requests = sdk_setup
    if fault != "missing":
        old, _ = failed_predecessor(
            dsn,
            approval,
            error="authentication" if fault == "other_failure" else "token_budget",
            unknown=fault == "unknown",
            batch_id=str(uuid4()) if fault == "wrong_batch" else None,
            limits=Limits(max_tokens=14) if fault == "graph_token_budget" else None,
        )
        if fault == "cancelled":
            old.stop(approval["previous_workflow_id"])
        elif fault == "pending_operation":
            with connect(dsn) as conn:
                conn.execute(
                    "INSERT INTO workflow_operations(thread_id,operation_id,input_digest) VALUES (%s,%s,%s)",
                    (approval["previous_workflow_id"], "synthetic-pending", "synthetic-input"),
                )
        elif fault == "pending_write":
            with connect(dsn) as conn:
                saver = PostgresSaver(conn)
                saved = saver.get_tuple(old.config(approval["previous_workflow_id"]))
                saver.put_writes(
                    saved.config, [("__error__", "synthetic-pending-write")], "synthetic-task"
                )
    BatchLedger.register_continuation(path, approval)
    monkeypatch.setenv("CHORA_WORKFLOW_BATCH_LEDGER", str(path))
    worker = Worker(dsn, setup(research_response), FixtureCatalog(), code_revision=REVISION)
    with pytest.raises(ValueError, match="predecessor"):
        worker.submit(WORKFLOW, "Chora?")
    assert len(BatchLedger(path).snapshot()["requests"]) == 2
    assert BatchLedger(path).snapshot()["continuation"]["activated_at"] is None
    assert requests == []
    with connect(dsn) as conn:
        assert (
            conn.execute("SELECT 1 FROM workflow_owners WHERE thread_id=%s", (WORKFLOW,)).fetchone()
            is None
        )
