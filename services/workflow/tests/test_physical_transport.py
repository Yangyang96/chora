"""Memory-only HTTP controls: no sockets, real credentials or existing ledgers."""

import time

import httpx
import pytest
from openai import OpenAI

from chora_workflow import model
from chora_workflow.batch import BatchLedger, POLICY
from chora_workflow.model import ModelFailure


@pytest.mark.parametrize("reply", ["redirect", "connection_lost", "response"])
def test_deepseek_one_transport_entry_per_reservation(monkeypatch, tmp_path, reply):
    now = time.time()
    path = tmp_path / "synthetic.sqlite"
    policy = {
        **POLICY,
        "version": 3,
        "authorization_ref": "synthetic-offline-only",
        "authorized_at": now,
        "absolute_deadline": now + 300,
        "duration_seconds": 300,
        "max_requests": 1,
        "max_tokens": 100000,
        "max_output_tokens": 8,
        "max_total_output_tokens": 8,
    }
    ledger = BatchLedger.create(path, authorization=policy)
    monkeypatch.setenv("CHORA_WORKFLOW_BASE_URL", "https://api.deepseek.com")
    monkeypatch.setenv("CHORA_WORKFLOW_MODEL", "deepseek-v4-pro")
    monkeypatch.setenv("CHORA_WORKFLOW_API_KEY", "synthetic-not-a-credential")
    monkeypatch.setenv("CHORA_WORKFLOW_BATCH_LEDGER", str(path))
    requests = []
    transport_options = []

    def respond(request):
        requests.append(request)
        if reply == "connection_lost":
            raise httpx.ConnectError("synthetic connection lost", request=request)
        if reply == "redirect" and len(requests) == 1:
            return httpx.Response(307, headers={"location": "https://api.deepseek.com/redirected"})
        return httpx.Response(
            200,
            json={
                "id": "fixture-only",
                "object": "chat.completion",
                "created": 1,
                "model": "deepseek-v4-pro",
                "choices": [{"index": 0, "message": {"role": "assistant", "tool_calls": []}}],
                "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
            },
        )

    def offline_transport(**kwargs):
        transport_options.append(kwargs)
        return httpx.MockTransport(respond)

    def offline_client(**kwargs):
        # Also makes a historical-source replay safe: preserve the SDK's
        # default redirect policy, but bind its transport to memory explicitly.
        if "http_client" not in kwargs:
            kwargs["http_client"] = httpx.Client(
                follow_redirects=True, trust_env=False, transport=httpx.MockTransport(respond)
            )
        return OpenAI(**kwargs)

    monkeypatch.setattr(httpx, "HTTPTransport", offline_transport)
    monkeypatch.setattr(model, "OpenAI", offline_client)
    sdk = model.SDKModel()
    try:
        if reply == "response":
            assert (
                sdk.invoke([{"role": "user", "content": "synthetic"}], [], 2, 8).total_tokens == 2
            )
        else:
            with pytest.raises(ModelFailure, match="outcome_unknown"):
                sdk.invoke([{"role": "user", "content": "synthetic"}], [], 2, 8)
        assert len(requests) == 1
        assert transport_options == [{"retries": 0}]
        assert not sdk.client._client.follow_redirects
        assert not sdk.client._client.trust_env
        assert sdk.identity["physical_transport"] == "httpx.direct.no-redirect.v1"
    finally:
        sdk.client.close()

    before = ledger.snapshot()
    assert len(before["requests"]) == 1
    restarted = model.SDKModel()
    try:
        with pytest.raises(ModelFailure):
            restarted.invoke([{"role": "user", "content": "synthetic"}], [], 2, 8)
    finally:
        restarted.client.close()
    assert len(requests) == 1
    assert BatchLedger(path).snapshot() == before
