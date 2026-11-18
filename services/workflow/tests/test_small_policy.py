"""Offline authorization boundaries. All ledgers and usage are synthetic."""

from concurrent.futures import ThreadPoolExecutor
import json
import sqlite3

import pytest

from chora_workflow import batch
from chora_workflow.batch import BatchLedger, BatchRefused, POLICY, input_bound


def payload(output=8):
    return {
        "model": "deepseek-v4-pro",
        "messages": [{"role": "user", "content": "Chora?"}],
        "tools": [],
        "max_tokens": output,
        "thinking": {"type": "disabled"},
    }


@pytest.fixture
def clock(monkeypatch):
    value = [1800000000.0]
    monkeypatch.setattr(batch.time, "time", lambda: value[0])
    return value


def policy(now, **changes):
    value = {
        **POLICY,
        "version": 3,
        "authorization_ref": "synthetic authorization; not permission to spend",
        "authorized_at": now,
        "absolute_deadline": now + 300,
        "duration_seconds": 300,
        "max_requests": 2,
        "max_tokens": 10000,
        "max_output_tokens": 8,
        "max_total_output_tokens": 16,
    }
    return {**value, **changes}


def finish(ledger, reservation, *, prompt=1, output=1):
    ledger.finish(
        reservation["request_id"],
        {"prompt_tokens": prompt, "completion_tokens": output, "total_tokens": prompt + output},
        "response",
    )


def test_exact_token_and_request_ceiling_survives_restart(tmp_path, clock):
    path = tmp_path / "fixture.sqlite"
    bound = input_bound(payload())
    ledger = BatchLedger.create(
        path, authorization=policy(clock[0], max_requests=1, max_tokens=bound + 8)
    )
    reservation = ledger.reserve(payload())
    finish(ledger, reservation, prompt=bound, output=8)
    restored = BatchLedger(path)
    assert restored.identity == ledger.identity
    with pytest.raises(BatchRefused, match="request_budget"):
        restored.reserve(payload())
    assert len(restored.snapshot()["requests"]) == 1


def test_one_token_over_and_output_caps_reserve_nothing(tmp_path, clock):
    ledger = BatchLedger.create(
        tmp_path / "fixture.sqlite",
        authorization=policy(clock[0], max_tokens=input_bound(payload()) + 7),
    )
    with pytest.raises(BatchRefused, match="token_budget"):
        ledger.reserve(payload())
    with pytest.raises(BatchRefused, match="output_budget"):
        ledger.reserve(payload(9))
    assert ledger.snapshot()["requests"] == []


def test_total_output_exact_then_over(tmp_path, clock):
    path = tmp_path / "fixture.sqlite"
    ledger = BatchLedger.create(path, authorization=policy(clock[0], max_total_output_tokens=8))
    finish(ledger, ledger.reserve(payload()), output=8)
    with pytest.raises(BatchRefused, match="output_budget"):
        BatchLedger(path).reserve(payload(1))


def test_pending_crash_unknown_and_concurrent_reservation(tmp_path, clock):
    path = tmp_path / "fixture.sqlite"
    BatchLedger.create(path, authorization=policy(clock[0]))

    def reserve(_):
        try:
            return BatchLedger(path).reserve(payload())
        except BatchRefused as exc:
            return exc.category

    with ThreadPoolExecutor(max_workers=8) as pool:
        results = list(pool.map(reserve, range(8)))
    accepted = [value for value in results if isinstance(value, dict)]
    assert len(accepted) == 1
    assert results.count("outcome_unknown") == 7
    snapshot = BatchLedger.inspect(path)
    assert snapshot["requests"][0]["completed_at"] is None
    with pytest.raises(BatchRefused, match="outcome_unknown"):
        BatchLedger(path).reserve(payload())
    with pytest.raises(BatchRefused, match="usage_unknown"):
        BatchLedger(path).finish(accepted[0]["request_id"], None, "connection_lost")
    with pytest.raises(BatchRefused, match="usage_unknown"):
        BatchLedger(path).reserve(payload())
    assert len(BatchLedger.inspect(path)["requests"]) == 1


def test_absolute_deadline_starts_before_first_request(tmp_path, clock):
    path = tmp_path / "fixture.sqlite"
    ledger = BatchLedger.create(path, authorization=policy(clock[0]))
    deadline = ledger.identity["policy"]["absolute_deadline"]
    clock[0] = deadline
    with pytest.raises(BatchRefused, match="deadline"):
        BatchLedger(path).reserve(payload())
    assert BatchLedger.inspect(path)["requests"] == []


def test_deadline_and_authorization_are_immutable_after_restart(tmp_path, clock):
    path = tmp_path / "fixture.sqlite"
    ledger = BatchLedger.create(path, authorization=policy(clock[0]))
    reservation = ledger.reserve(payload())
    finish(ledger, reservation)
    clock[0] += 300
    with pytest.raises(BatchRefused, match="deadline"):
        BatchLedger(path).reserve(payload())
    with pytest.raises(FileExistsError):
        BatchLedger.create(path, authorization=policy(clock[0]))
    assert BatchLedger(path).identity == ledger.identity


@pytest.mark.parametrize(
    "change",
    [
        {"max_requests": 21},
        {"max_tokens": 100001},
        {"max_output_tokens": 2049},
        {"duration_seconds": 1201},
        {"absolute_deadline": 1800000301.0},
        {"max_total_output_tokens": 10001},
        {"encoder_sha256": "0" * 64},
        {"max_requests": True},
    ],
)
def test_policy_cannot_raise_or_replace_envelope(tmp_path, clock, change):
    path = tmp_path / "fixture.sqlite"
    with pytest.raises(ValueError):
        BatchLedger.create(path, authorization=policy(clock[0], **change))
    assert not path.exists()


def test_frozen_policy_change_fails_without_reservation(tmp_path, clock):
    path = tmp_path / "fixture.sqlite"
    ledger = BatchLedger.create(path, authorization=policy(clock[0]))
    # Fault injection into this test-owned database; never a historical ledger.
    with ledger.connection() as conn:
        value = ledger.identity["policy"].copy()
        value["max_requests"] += 1
        with pytest.raises(sqlite3.IntegrityError, match="immutable"):
            conn.execute("UPDATE batch SET policy=?", (json.dumps(value),))
    assert BatchLedger(path).identity == ledger.identity
    with ledger.connection() as conn:
        conn.execute("DROP TRIGGER small_policy_immutable")
        conn.execute("UPDATE batch SET policy=?", (json.dumps(value),))
    with pytest.raises(ValueError, match="identity changed"):
        ledger.reserve(payload())
    with pytest.raises(ValueError, match="identity changed"):
        BatchLedger(path)
    assert ledger.snapshot()["requests"] == []
