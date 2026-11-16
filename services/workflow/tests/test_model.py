"""Offline SDK transport controls; every credential and response is synthetic."""

import json
import sqlite3
import subprocess
import sys
from concurrent.futures import ThreadPoolExecutor
from uuid import uuid4

import httpx
import pytest
from openai import OpenAI

from chora_workflow import model
from chora_workflow import batch
from chora_workflow.batch import BatchLedger, BatchRefused
from chora_workflow.model import ModelFailure
from chora_workflow.protocol import Limits, digest
from chora_workflow.worker import Worker
from fixtures import FixtureCatalog


@pytest.fixture
def sdk_setup(monkeypatch, tmp_path):
    requests = []
    ledger = BatchLedger.create(tmp_path / "synthetic-batch.sqlite")
    monkeypatch.setenv("CHORA_WORKFLOW_BASE_URL", "https://api.deepseek.com")
    monkeypatch.setenv("CHORA_WORKFLOW_MODEL", "deepseek-v4-pro")
    monkeypatch.setenv("CHORA_WORKFLOW_API_KEY", "synthetic-test-credential")
    monkeypatch.setenv("CHORA_WORKFLOW_BATCH_LEDGER", str(ledger.path))

    def setup(handler=None):
        def respond(request):
            payload = json.loads(request.content)
            requests.append(payload)
            if handler:
                return handler(payload)
            return response()

        monkeypatch.setattr(
            model,
            "OpenAI",
            lambda **kwargs: OpenAI(
                **kwargs, http_client=httpx.Client(transport=httpx.MockTransport(respond))
            ),
        )
        sdk = model.SDKModel()
        sdk.observation_mode = "mock"
        return sdk

    return setup, ledger, requests


def response(*, usage=None, calls=None):
    return httpx.Response(
        200,
        json={
            "id": "fixture-response",
            "object": "chat.completion",
            "created": 1,
            "model": "deepseek-v4-pro",
            "choices": [{"index": 0, "message": {"role": "assistant", "tool_calls": calls or []}}],
            "usage": usage or {"prompt_tokens": 5, "completion_tokens": 2, "total_tokens": 7},
        },
    )


def payload():
    return {
        "model": "deepseek-v4-pro",
        "messages": [{"role": "user", "content": "Chora?"}],
        "tools": [],
        "max_tokens": 2048,
        "thinking": {"type": "disabled"},
    }


def test_deepseek_request_parameters(sdk_setup):
    setup, ledger, requests = sdk_setup
    sdk = setup()
    reply = sdk.invoke([{"role": "user", "content": "Chora?"}], [], 10, 5000)
    assert reply.total_tokens == 7
    assert requests[0]["max_tokens"] == 2048
    assert requests[0]["thinking"] == {"type": "disabled"}
    assert requests[0]["tool_choice"] == "required"
    assert "max_completion_tokens" not in requests[0]
    assert sdk.client.max_retries == sdk.retry_limit == 0
    row = ledger.snapshot()["requests"][0]
    assert json.loads(row["usage"])["total_tokens"] == 7
    assert row["requests_remaining"] == 19
    assert row["tokens_remaining"] == 99993
    assert row["request_digest"] == digest({**requests[0]})
    assert "synthetic-test-credential" not in json.dumps(ledger.snapshot())
    assert "synthetic-test-credential" not in json.dumps(sdk.identity)
    assert str(ledger.path) not in json.dumps(sdk.identity)


def test_other_provider_preserves_parameters(sdk_setup, monkeypatch):
    setup, ledger, requests = sdk_setup
    monkeypatch.setenv("CHORA_WORKFLOW_BASE_URL", "https://example.org/v1")
    monkeypatch.delenv("CHORA_WORKFLOW_BATCH_LEDGER")
    sdk = setup()
    sdk.invoke(payload()["messages"], [], 10, 100)
    assert requests[0]["max_completion_tokens"] == 100
    assert "thinking" not in requests[0]
    assert "max_tokens" not in requests[0]
    assert ledger.snapshot()["requests"] == []


def test_deepseek_requires_existing_batch(sdk_setup, monkeypatch, tmp_path):
    setup, _, requests = sdk_setup
    monkeypatch.delenv("CHORA_WORKFLOW_BATCH_LEDGER")
    with pytest.raises(ValueError, match="BATCH_LEDGER"):
        setup()
    missing = tmp_path / "missing.sqlite"
    monkeypatch.setenv("CHORA_WORKFLOW_BATCH_LEDGER", str(missing))
    with pytest.raises(Exception, match="unable to open"):
        setup()
    assert not missing.exists()
    assert requests == []


@pytest.mark.parametrize("field", ["BASE_URL", "MODEL"])
def test_deepseek_rejects_unsupported_profile(sdk_setup, monkeypatch, field):
    setup, _, requests = sdk_setup
    value = "http://api.deepseek.com" if field == "BASE_URL" else "other-model"
    monkeypatch.setenv(f"CHORA_WORKFLOW_{field}", value)
    with pytest.raises(ValueError, match="unsupported DeepSeek"):
        setup()
    assert requests == []


@pytest.mark.parametrize("status", [400, 401, 429, 500])
def test_failed_requests_count_and_halt_without_retry(sdk_setup, status):
    setup, ledger, requests = sdk_setup
    sdk = setup(lambda _: httpx.Response(status, json={"error": {"message": "synthetic failure"}}))
    with pytest.raises(ModelFailure) as error:
        sdk.invoke(payload()["messages"], [], 10, 100)
    assert error.value.usage_unknown
    assert len(requests) == 1
    snapshot = ledger.snapshot()
    assert snapshot["batch"]["halt_reason"] == "usage_unknown"
    assert snapshot["requests"][0]["usage"] is None
    assert snapshot["requests"][0]["tokens_remaining"] is None
    with pytest.raises(ModelFailure, match="usage_unknown"):
        setup().invoke(payload()["messages"], [], 10, 100)
    assert len(requests) == 1


def test_missing_usage_halts(sdk_setup):
    setup, ledger, requests = sdk_setup

    def without_usage(_):
        result = response()
        body = json.loads(result.content)
        body.pop("usage")
        return httpx.Response(200, json=body)

    with pytest.raises(ModelFailure, match="usage_unknown"):
        setup(without_usage).invoke(payload()["messages"], [], 10, 100)
    assert ledger.snapshot()["batch"]["halt_reason"] == "usage_unknown"
    assert len(requests) == 1


def test_timeout_is_one_unknown_request(sdk_setup):
    setup, ledger, requests = sdk_setup

    def timeout(_):
        raise httpx.ReadTimeout("synthetic timeout")

    with pytest.raises(ModelFailure, match="timeout_unknown"):
        setup(timeout).invoke(payload()["messages"], [], 10, 100)
    assert len(requests) == 1
    assert ledger.snapshot()["batch"]["halt_reason"] == "usage_unknown"


def test_expiry_between_reservation_and_send_dispatches_nothing(sdk_setup, monkeypatch):
    setup, ledger, requests = sdk_setup
    sdk = setup()
    clock = iter([1000, 2201, 2201])
    monkeypatch.setattr(batch.time, "time", lambda: next(clock, 2201))
    with pytest.raises(ModelFailure, match="deadline"):
        sdk.invoke(payload()["messages"], [], 10, 100)
    assert requests == []
    row = ledger.snapshot()["requests"][0]
    assert row["outcome"] == "not_dispatched"
    assert json.loads(row["usage"])["total_tokens"] == 0
    assert ledger.snapshot()["batch"]["halt_reason"] == "deadline"


@pytest.mark.parametrize("boundary", ["reserve", "finish"])
def test_batch_database_failure_blocks_further_dispatch(sdk_setup, monkeypatch, boundary):
    setup, ledger, requests = sdk_setup
    sdk = setup()

    def unavailable(*args):
        raise sqlite3.OperationalError("synthetic ledger failure")

    with monkeypatch.context() as patch:
        patch.setattr(sdk.batch, boundary, unavailable)
        with pytest.raises(sqlite3.OperationalError, match="synthetic"):
            sdk.invoke(payload()["messages"], [], 10, 100)
    assert len(requests) == (0 if boundary == "reserve" else 1)
    if boundary == "finish":
        with pytest.raises(ModelFailure, match="outcome_unknown"):
            setup().invoke(payload()["messages"], [], 10, 100)
        assert ledger.snapshot()["requests"][0]["completed_at"] is None
        assert len(requests) == 1


def test_preflight_refusal_dispatches_nothing(sdk_setup):
    setup, ledger, requests = sdk_setup
    sdk = setup()
    with pytest.raises(ModelFailure, match="token_budget"):
        sdk.invoke([{"role": "user", "content": "界" * 20000}], [], 10, 100)
    with pytest.raises(ModelFailure, match="unbounded_input"):
        sdk.invoke([{"role": "user", "content": [{"type": "image_url"}]}], [], 10, 100)
    assert ledger.snapshot()["batch"]["first_request_at"] is None
    assert ledger.snapshot()["requests"] == requests == []


def test_batch_request_cap_survives_new_instances(sdk_setup):
    setup, ledger, requests = sdk_setup
    for _ in range(20):
        setup().invoke(payload()["messages"], [], 10, 100)
    with pytest.raises(ModelFailure, match="request_budget"):
        setup().invoke(payload()["messages"], [], 10, 100)
    rows = ledger.snapshot()["requests"]
    assert len(rows) == len(requests) == 20
    assert rows[-1]["requests_remaining"] == 0
    assert rows[-1]["tokens_remaining"] == 100000 - 20 * 7
    assert len({row["request_id"] for row in rows}) == 20


def test_batch_refuses_reinitialization(sdk_setup):
    _, ledger, _ = sdk_setup
    with pytest.raises(FileExistsError):
        BatchLedger.create(ledger.path)
    assert BatchLedger(ledger.path).identity == ledger.identity


def test_batch_deadline_survives_restart(sdk_setup, monkeypatch):
    setup, ledger, requests = sdk_setup
    monkeypatch.setattr(batch.time, "time", lambda: 1000)
    setup().invoke(payload()["messages"], [], 10, 100)
    monkeypatch.setattr(batch.time, "time", lambda: 2200)
    with pytest.raises(ModelFailure, match="deadline"):
        setup().invoke(payload()["messages"], [], 10, 100)
    assert ledger.snapshot()["batch"]["first_request_at"] == 1000
    assert len(requests) == 1


def test_batch_token_preflight_accounts_for_prior_usage(sdk_setup):
    _, ledger, _ = sdk_setup
    request = payload()
    request["messages"][0]["content"] = "x" * 4000
    for _ in range(9):
        row = ledger.reserve(request)
        ledger.finish(
            row["request_id"],
            {"prompt_tokens": 8000, "completion_tokens": 2000, "total_tokens": 10000},
            "response",
        )
    with pytest.raises(BatchRefused, match="token_budget"):
        BatchLedger(ledger.path).reserve(request)
    assert len(ledger.snapshot()["requests"]) == 9


def test_exact_remaining_token_boundary_and_provider_bound(sdk_setup, monkeypatch):
    _, ledger, _ = sdk_setup
    request = payload()
    bound = batch.input_bound(request)
    # Reserve exactly all remaining tokens, then retain actual known usage.
    monkeypatch.setitem(batch.POLICY, "max_tokens", bound + request["max_tokens"])
    reservation = ledger.reserve(request)
    assert ledger.snapshot()["requests"][0]["tokens_remaining"] == 0
    with pytest.raises(BatchRefused, match="provider_budget_violation"):
        ledger.finish(
            reservation["request_id"],
            {"prompt_tokens": bound + 1, "completion_tokens": 0, "total_tokens": bound + 1},
            "response",
        )
    with pytest.raises(BatchRefused, match="provider_budget_violation"):
        ledger.reserve(request)


def test_one_token_over_remaining_dispatches_nothing(sdk_setup, monkeypatch):
    setup, ledger, requests = sdk_setup
    sdk = setup()
    request = {**payload(), "tool_choice": "required", "stream": False}
    monkeypatch.setitem(batch.POLICY, "max_tokens", batch.input_bound(request) + 2048 - 1)
    with pytest.raises(ModelFailure, match="token_budget"):
        sdk.invoke(request["messages"], [], 10, 2048)
    assert ledger.snapshot()["requests"] == requests == []


@pytest.mark.parametrize("text", ['界😀"\\\n', "<｜Assistant｜>", "\u0000\t", "x" * 10000])
def test_full_text_and_escaped_history_are_not_changed(sdk_setup, text):
    setup, ledger, requests = sdk_setup
    messages = [
        {"role": "user", "content": text},
        {
            "role": "assistant",
            "content": None,
            "tool_calls": [
                {
                    "id": "q",
                    "type": "function",
                    "function": {"name": "query_public", "arguments": json.dumps({"query": text})},
                }
            ],
        },
        {"role": "tool", "tool_call_id": "q", "content": json.dumps({"content": text})},
        {"role": "tool", "tool_call_id": "repeat", "content": text},
    ]
    before = digest(messages)
    setup().invoke(messages, [], 10, 100)
    assert requests[0]["messages"] == messages
    assert digest(messages) == before
    assert ledger.snapshot()["requests"][0]["input_bound"] >= len(text.encode("utf-8"))


@pytest.mark.parametrize(
    "change",
    [
        {"thinking": {"type": "enabled"}},
        {"response_format": {"type": "json_object"}},
        {"stream": True},
        {"tools": [{"type": "function", "function": {"name": "arbitrary"}}]},
        {"messages": [{"role": "assistant", "content": None, "reasoning_content": "hidden"}]},
        {"messages": [{"role": "user", "content": "x", "tools": []}]},
        {"messages": [{"role": "developer", "content": "x"}]},
        {"messages": [{"role": "tool", "content": {"text": "x"}}]},
        {"messages": [None]},
        {"messages": [{"role": "assistant", "content": None, "tool_calls": [None]}]},
        {"messages": [{"role": "user", "content": "\ud800"}]},
    ],
)
def test_unproved_renderer_branches_refuse_without_reservation(sdk_setup, change):
    _, ledger, requests = sdk_setup
    with pytest.raises(BatchRefused, match="unbounded_input"):
        ledger.reserve({**payload(), **change})
    assert ledger.snapshot()["requests"] == requests == []


@pytest.mark.parametrize(
    "arguments", ['{"a":NaN}', '{"a":1e1000}', "[1]", "{invalid", '{"a":"\\ud800"}']
)
def test_invalid_historical_arguments_refuse(sdk_setup, arguments):
    _, ledger, requests = sdk_setup
    request = payload()
    request["messages"] = [
        {
            "role": "assistant",
            "content": None,
            "tool_calls": [
                {
                    "id": "q",
                    "type": "function",
                    "function": {"name": "query_public", "arguments": arguments},
                }
            ],
        }
    ]
    with pytest.raises(BatchRefused, match="unbounded_input"):
        ledger.reserve(request)
    assert ledger.snapshot()["requests"] == requests == []


def test_old_batch_policy_refuses_without_rewriting_history(sdk_setup):
    _, ledger, _ = sdk_setup
    row = ledger.reserve(payload())
    ledger.finish(
        row["request_id"],
        {"prompt_tokens": 5, "completion_tokens": 2, "total_tokens": 7},
        "fixture",
    )
    old_policy = {
        key: value
        for key, value in batch.POLICY.items()
        if key
        in {
            "version",
            "max_requests",
            "max_tokens",
            "max_output_tokens",
            "duration_seconds",
            "input_bound",
        }
    }
    old_policy.update(version=1, input_bound="text-json-utf8-framing.v1")
    with sqlite3.connect(ledger.path) as conn:
        conn.execute("UPDATE batch SET policy=?", (json.dumps(old_policy),))
    before = ledger.path.read_bytes()
    with pytest.raises(ValueError, match="policy changed"):
        BatchLedger(ledger.path)
    assert ledger.path.read_bytes() == before
    assert ledger.snapshot()["requests"][0]["usage"] == json.dumps(
        {"prompt_tokens": 5, "completion_tokens": 2, "total_tokens": 7}
    )


def test_stopped_fixture_batch_cannot_be_reopened(sdk_setup):
    setup, ledger, requests = sdk_setup
    with sqlite3.connect(ledger.path) as conn:
        conn.execute("UPDATE batch SET halt_reason='operator_stopped'")
    before = ledger.snapshot()
    with pytest.raises(ModelFailure, match="operator_stopped"):
        setup().invoke(payload()["messages"], [], 10, 100)
    assert BatchLedger(ledger.path).snapshot() == before
    assert requests == []


@pytest.mark.parametrize(
    "usage", [None, {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 5}]
)
def test_batch_invalid_usage_is_null_and_blocks(sdk_setup, usage):
    _, ledger, _ = sdk_setup
    row = ledger.reserve(payload())
    with pytest.raises(BatchRefused, match="usage_unknown"):
        ledger.finish(row["request_id"], usage, "response")
    assert ledger.snapshot()["requests"][0]["usage"] is None
    with pytest.raises(BatchRefused, match="usage_unknown"):
        BatchLedger(ledger.path).reserve(payload())


def test_batch_pending_request_blocks_other_process(sdk_setup):
    _, ledger, _ = sdk_setup
    script = """import json,os,sys
from chora_workflow.batch import BatchLedger
BatchLedger(sys.argv[1]).reserve(json.loads(sys.argv[2]))
os._exit(77)
"""
    child = subprocess.run(
        [sys.executable, "-c", script, str(ledger.path), json.dumps(payload())],
        capture_output=True,
        text=True,
        timeout=20,
    )
    assert child.returncode == 77, child.stderr
    with pytest.raises(BatchRefused, match="outcome_unknown"):
        BatchLedger(ledger.path).reserve(payload())
    assert len(ledger.snapshot()["requests"]) == 1


def test_batch_usage_continues_in_another_process(sdk_setup):
    setup, ledger, _ = sdk_setup
    setup().invoke(payload()["messages"], [], 10, 100)
    source = """import json,sys
from chora_workflow.batch import BatchLedger
ledger=BatchLedger(sys.argv[1])
row=ledger.reserve(json.loads(sys.argv[2]))
ledger.finish(row['request_id'], {'prompt_tokens':8,'completion_tokens':3,'total_tokens':11}, 'response')
"""
    child = subprocess.run(
        [sys.executable, "-c", source, str(ledger.path), json.dumps(payload())],
        capture_output=True,
        text=True,
        timeout=20,
    )
    assert child.returncode == 0, child.stderr
    rows = BatchLedger(ledger.path).snapshot()["requests"]
    assert len(rows) == 2
    assert rows[-1]["requests_remaining"] == 18
    assert rows[-1]["tokens_remaining"] == 99982


def test_provider_output_violation_is_recorded_and_halts(sdk_setup):
    setup, ledger, _ = sdk_setup
    sdk = setup(
        lambda _: response(
            usage={"prompt_tokens": 5, "completion_tokens": 101, "total_tokens": 106}
        )
    )
    with pytest.raises(ModelFailure, match="provider_budget_violation"):
        sdk.invoke(payload()["messages"], [], 10, 100)
    row = ledger.snapshot()["requests"][0]
    assert json.loads(row["usage"])["total_tokens"] == 106
    with pytest.raises(BatchRefused, match="provider_budget_violation"):
        ledger.reserve(payload())


def test_batch_concurrent_reservations_allow_one(sdk_setup):
    _, ledger, _ = sdk_setup

    def reserve():
        try:
            return BatchLedger(ledger.path).reserve(payload())["request_id"]
        except BatchRefused as exc:
            return exc.category

    with ThreadPoolExecutor(max_workers=2) as pool:
        results = list(pool.map(lambda _: reserve(), range(2)))
    assert results.count("outcome_unknown") == 1
    assert len(ledger.snapshot()["requests"]) == 1


def research_response(payload):
    index = len([message for message in payload["messages"] if message["role"] == "tool"])
    name, arguments = [
        ("query_public", {"query": "Chora"}),
        ("read_evidence", {"evidence_id": "readme"}),
        (
            "submit_report",
            {"summary": "Local workbench", "citations": ["readme"], "no_answer": False},
        ),
    ][min(index, 2)]
    return response(
        calls=[
            {
                "id": f"mock-{index}",
                "type": "function",
                "function": {"name": name, "arguments": json.dumps(arguments)},
            }
        ]
    )


@pytest.mark.postgres
def test_sdk_profile_batch_and_frozen_identity_survive_pg_recovery(
    dsn, sdk_setup, tmp_path, monkeypatch
):
    setup, ledger, requests = sdk_setup
    thread = str(uuid4())
    worker = Worker(dsn, setup(research_response), FixtureCatalog())
    before = worker.submit(thread, "What is Chora?")
    assert before.values["status"] == "waiting"
    assert len(requests) == 3
    decision = {**before.values["gate"], "decision": "accept"}
    source = """import json,sys,httpx
from openai import OpenAI
from chora_workflow import model
from chora_workflow.worker import Worker
from fixtures import FixtureCatalog
def refuse(request):
    raise AssertionError('gate recovery dispatched an SDK request')
model.OpenAI=lambda **kw:OpenAI(**kw,http_client=httpx.Client(transport=httpx.MockTransport(refuse)))
sdk=model.SDKModel()
sdk.observation_mode='mock'
worker=Worker(sys.argv[1],sdk,FixtureCatalog())
print(json.dumps(worker.resume(sys.argv[2],json.loads(sys.argv[3])).values))
"""
    import os
    from pathlib import Path

    child = subprocess.run(
        [sys.executable, "-c", source, dsn, thread, json.dumps(decision)],
        env={**os.environ, "PYTHONPATH": str(Path(__file__).parent)},
        capture_output=True,
        text=True,
        timeout=20,
    )
    assert child.returncode == 0, child.stderr
    recovered = Worker(dsn, setup(research_response), FixtureCatalog())
    after = recovered.inspect(thread)
    assert json.loads(child.stdout) == after.values
    assert after.values["status"] == "accepted"
    assert recovered.resume(thread, decision).values == after.values
    for key in ("spent_tokens", "steps", "retries", "deadline", "trace_id", "frozen"):
        assert before.values[key] == after.values[key]
    other_batch = BatchLedger.create(tmp_path / "different-synthetic-batch.sqlite")
    monkeypatch.setenv("CHORA_WORKFLOW_BATCH_LEDGER", str(other_batch.path))
    with pytest.raises(ValueError, match="frozen"):
        Worker(dsn, setup(research_response), FixtureCatalog()).resume(thread, decision)
    assert len(ledger.snapshot()["requests"]) == len(requests) == 3
    assert other_batch.snapshot()["requests"] == []


@pytest.mark.postgres
@pytest.mark.parametrize(
    "field",
    [
        "provider",
        "profile",
        "thinking",
        "output_parameter",
        "max_output_tokens",
        "tool_choice",
        "stream",
        "graph_retries",
    ],
)
def test_request_semantic_change_refuses_old_pg_gate(dsn, sdk_setup, field):
    setup, ledger, requests = sdk_setup
    sdk = setup(research_response)
    thread = str(uuid4())
    worker = Worker(dsn, sdk, FixtureCatalog())
    paused = worker.submit(thread, "Chora?")
    candidate = setup(research_response)
    candidate.identity[field] = "changed-semantics"
    with pytest.raises(ValueError, match="frozen"):
        Worker(dsn, candidate, FixtureCatalog()).resume(
            thread, {**paused.values["gate"], "decision": "accept"}
        )
    assert len(requests) == len(ledger.snapshot()["requests"]) == 3


@pytest.mark.postgres
@pytest.mark.parametrize(
    "field", ["input_bound", "encoder_sha256", "tool_schema_digest", "message_framing_bytes"]
)
def test_input_policy_change_refuses_before_pg_mutation(dsn, sdk_setup, field):
    setup, ledger, requests = sdk_setup
    thread = str(uuid4())
    paused = Worker(dsn, setup(research_response), FixtureCatalog()).submit(thread, "Chora?")
    candidate = setup(research_response)
    candidate.identity["acceptance_batch"]["policy"][field] = "old-policy-fixture"
    worker = Worker(dsn, candidate, FixtureCatalog())
    before = ledger.snapshot()
    with pytest.raises(ValueError, match="frozen"):
        worker.resume(thread, {**paused.values["gate"], "decision": "accept"})
    assert worker.inspect(thread).values == paused.values
    assert ledger.snapshot() == before
    assert len(requests) == 3
    from chora_workflow.storage import connect

    with connect(dsn) as conn:
        assert conn.execute("SELECT count(*) AS n FROM workflow_decisions").fetchone()["n"] == 0


@pytest.mark.postgres
def test_batch_accumulates_across_threads_and_workflow_stop_survives(dsn, sdk_setup):
    setup, ledger, requests = sdk_setup
    threads = [str(uuid4()), str(uuid4())]
    paused = [
        Worker(dsn, setup(research_response), FixtureCatalog()).submit(thread, "Chora?")
        for thread in threads
    ]
    assert len(requests) == len(ledger.snapshot()["requests"]) == 6
    assert ledger.snapshot()["requests"][-1]["tokens_remaining"] == 100000 - 42
    worker = Worker(dsn, setup(research_response), FixtureCatalog())
    worker.stop(threads[0])
    stopped = worker.resume(threads[0], {**paused[0].values["gate"], "decision": "accept"})
    assert stopped.values["status"] == "stopped"
    assert worker.resume(threads[0]).values == stopped.values
    accepted = worker.resume(threads[1], {**paused[1].values["gate"], "decision": "accept"})
    assert accepted.values["status"] == "accepted"
    assert len(requests) == len(ledger.snapshot()["requests"]) == 6


@pytest.mark.postgres
def test_graph_does_not_retry_guarded_rate_limit(dsn, sdk_setup):
    setup, ledger, requests = sdk_setup
    sdk = setup(lambda _: httpx.Response(429, json={"error": {"message": "synthetic rejection"}}))
    state = (
        Worker(dsn, sdk, FixtureCatalog())
        .submit(str(uuid4()), "Chora?", Limits(max_retries=5))
        .values
    )
    assert state["status"] == "failed"
    assert state["error"] == "rate_limit"
    assert state["usage_unknown"]
    assert state["retries"] == 0
    assert len(requests) == len(ledger.snapshot()["requests"]) == 1


@pytest.mark.postgres
def test_invalid_sdk_result_preserves_known_usage_in_pg(dsn, sdk_setup):
    setup, ledger, requests = sdk_setup
    sdk = setup(
        lambda _: response(
            calls=[
                {
                    "id": "bad",
                    "type": "function",
                    "function": {"name": "query_public", "arguments": "{invalid"},
                }
            ]
        )
    )
    thread = str(uuid4())
    worker = Worker(dsn, sdk, FixtureCatalog())
    before = worker.submit(thread, "Chora?").values
    assert before["error"] == "invalid_result"
    assert before["spent_tokens"] == 7
    assert worker.resume(thread).values["spent_tokens"] == 7
    assert len(requests) == len(ledger.snapshot()["requests"]) == 1
