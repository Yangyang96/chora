"""Synthetic SDK configuration controls; every HTTP transport stays in memory."""

import copy
from types import SimpleNamespace

import httpx
import pytest
from openai import OpenAI

from chora_workflow import model
from chora_workflow.batch import BatchLedger
from chora_workflow.graph import Research, initial_state
from fixtures import FixtureCatalog


# Independent cases from the pinned SDK's constructor, not the product's list.
AMBIENT_NAMES = (
    "OPENAI_ADMIN_KEY",
    "OPENAI_ORG_ID",
    "OPENAI_PROJECT_ID",
    "OPENAI_WEBHOOK_SECRET",
    "OPENAI_CUSTOM_HEADERS",
)


class ExplicitEnvironment:
    """Allow name checks, but fail on any attempted ambient value access or mutation."""

    def __init__(self, explicit, ambient):
        self.explicit = explicit
        self.ambient = ambient
        self.value_reads = []

    def __contains__(self, name):
        raise AssertionError("mapping membership can implicitly fetch a value")

    def __getitem__(self, name):
        self.value_reads.append(name)
        assert name not in self.ambient, "ambient value read"
        return self.explicit[name]

    def get(self, name, default=None):
        self.value_reads.append(name)
        assert name not in self.ambient, "ambient value read"
        return self.explicit.get(name, default)

    def __iter__(self):
        return iter(self.explicit.keys() | self.ambient.keys())

    def __setitem__(self, name, value):
        raise AssertionError("environment mutation")

    def __delitem__(self, name):
        raise AssertionError("environment mutation")


@pytest.fixture
def configured(monkeypatch, tmp_path):
    path = tmp_path / "synthetic.sqlite"
    ledger = BatchLedger.create(path)
    explicit = {
        "CHORA_WORKFLOW_BASE_URL": "https://api.deepseek.com",
        "CHORA_WORKFLOW_MODEL": "deepseek-v4-pro",
        "CHORA_WORKFLOW_API_KEY": "synthetic-explicit-key",
        "CHORA_WORKFLOW_BATCH_LEDGER": str(path),
    }
    environment = ExplicitEnvironment(explicit, {})
    monkeypatch.setattr(model, "os", SimpleNamespace(environ=environment))
    effects = []
    requests = []

    def open_ledger(path):
        effects.append("ledger")
        return BatchLedger(path)

    def respond(request):
        requests.append(request)
        return httpx.Response(
            200,
            json={
                "id": "synthetic",
                "object": "chat.completion",
                "created": 1,
                "model": explicit["CHORA_WORKFLOW_MODEL"],
                "choices": [{"index": 0, "message": {"role": "assistant"}}],
                "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
            },
        )

    def open_transport(**kwargs):
        effects.append("transport")
        return httpx.MockTransport(respond)

    def open_client(**kwargs):
        effects.append("client")
        # Bind a memory backend before construction, including historical and
        # generic paths that do not supply an explicit HTTP client.
        if "http_client" not in kwargs:
            kwargs["http_client"] = httpx.Client(
                trust_env=False, transport=httpx.MockTransport(respond)
            )
        return OpenAI(**kwargs)

    monkeypatch.setattr(model, "BatchLedger", open_ledger)
    monkeypatch.setattr(httpx, "HTTPTransport", open_transport)
    monkeypatch.setattr(model, "OpenAI", open_client)
    return environment, ledger, effects, requests


@pytest.mark.parametrize("name", AMBIENT_NAMES)
@pytest.mark.parametrize("value", ["", "synthetic-unsupported-value"])
@pytest.mark.parametrize("existing", [True, False])
def test_ambient_presence_refuses_before_side_effects(configured, name, value, existing):
    environment, ledger, effects, requests = configured
    path = ledger.path if existing else ledger.path.parent / "must-not-be-created.sqlite"
    environment.explicit["CHORA_WORKFLOW_BATCH_LEDGER"] = str(path)
    before = ledger.path.read_bytes()
    environment.ambient[name] = value
    with pytest.raises(ValueError, match="unsupported SDK environment configuration") as error:
        candidate = model.SDKModel()
        candidate.client.close()
    assert name in str(error.value)
    assert "synthetic-unsupported-value" not in str(error.value)
    assert not set(environment.value_reads) & set(AMBIENT_NAMES)
    assert environment.ambient == {name: value}
    assert effects == requests == []
    assert ledger.path.read_bytes() == before
    assert path.exists() == existing


@pytest.mark.parametrize("header", ["Authorization", "authorization", "aUtHoRiZaTiOn"])
def test_custom_authorization_never_reaches_sdk(configured, monkeypatch, header):
    environment, ledger, effects, requests = configured
    # SDK and product both see synthetic configuration. Even arbitrary header
    # casing must be refused before SDK parsing, without inspecting the value.
    value = f"{header}: Bearer synthetic-ambient-key"
    monkeypatch.setenv("OPENAI_CUSTOM_HEADERS", value)
    environment.ambient["OPENAI_CUSTOM_HEADERS"] = value
    before = ledger.snapshot()
    with pytest.raises(ValueError, match="OPENAI_CUSTOM_HEADERS") as error:
        candidate = model.SDKModel()
        candidate.client.close()
    assert "synthetic-ambient-key" not in str(error.value)
    assert effects == requests == []
    assert ledger.snapshot() == before


def test_clean_explicit_configuration_and_frozen_identity(configured):
    _, ledger, effects, requests = configured
    sdk = model.SDKModel()
    try:
        assert sdk.identity["sdk_environment_policy"] == "reject-ambient-config.v1"
        assert effects == ["ledger", "transport", "client"]
        assert sdk.invoke([{"role": "user", "content": "synthetic"}], [], 2, 8).total_tokens == 2
        assert requests[0].headers["authorization"] == "Bearer synthetic-explicit-key"
        assert requests[0].url.host == "api.deepseek.com"
        assert "openai-organization" not in requests[0].headers
        assert "openai-project" not in requests[0].headers
        state = initial_state("synthetic-thread", "Chora?", sdk, FixtureCatalog())
    finally:
        sdk.client.close()
    before = ledger.snapshot()
    restored = model.SDKModel()
    try:
        research = Research(restored, FixtureCatalog(), None)
        research.validate_frozen(state)
        for policy in (None, "different-policy"):
            incompatible = copy.deepcopy(state)
            identity = incompatible["frozen"]["model"]
            if policy is None:
                del identity["sdk_environment_policy"]
            else:
                identity["sdk_environment_policy"] = policy
            with pytest.raises(ValueError, match="frozen workflow configuration changed"):
                research.validate_frozen(incompatible)
        assert len(requests) == 1
        assert ledger.snapshot() == before
        assert len(before["requests"]) == 1
        assert before["requests"][0]["requests_remaining"] == 19
        assert before["requests"][0]["tokens_remaining"] == 99998
    finally:
        restored.client.close()


def test_generic_profile_keeps_existing_identity(configured):
    environment, ledger, _, requests = configured
    environment.explicit["CHORA_WORKFLOW_BASE_URL"] = "https://example.invalid/v1"
    environment.explicit["CHORA_WORKFLOW_MODEL"] = "synthetic-model"
    sdk = model.SDKModel()
    try:
        assert "sdk_environment_policy" not in sdk.identity
        assert sdk.batch is None
        assert sdk.invoke([{"role": "user", "content": "synthetic"}], [], 2, 8).total_tokens == 2
        assert requests[0].headers["authorization"] == "Bearer synthetic-explicit-key"
        assert ledger.snapshot()["requests"] == []
    finally:
        sdk.client.close()
