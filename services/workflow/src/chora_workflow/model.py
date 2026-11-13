"""Explicitly configured OpenAI-compatible SDK adapter; no credential discovery."""

import json
import os
import time
from urllib.parse import urlsplit

from openai import (
    OpenAI,
    APIConnectionError,
    APIStatusError,
    APITimeoutError,
    AuthenticationError,
    BadRequestError,
    PermissionDeniedError,
    RateLimitError,
)
from pydantic import ValidationError

from .batch import BatchLedger, BatchRefused
from .protocol import Call, Reply, digest


class ModelFailure(Exception):
    def __init__(self, category, *, usage_unknown=False, total_tokens=None):
        self.category = category
        self.usage_unknown = usage_unknown
        self.total_tokens = total_tokens
        super().__init__(category)


class SDKModel:
    observation_mode = "live"

    def __init__(self):
        names = ("CHORA_WORKFLOW_BASE_URL", "CHORA_WORKFLOW_MODEL", "CHORA_WORKFLOW_API_KEY")
        missing = [name for name in names if not os.environ.get(name)]
        if missing:
            raise ValueError("missing explicit model configuration: " + ", ".join(missing))
        self.model = os.environ[names[1]]
        endpoint = urlsplit(os.environ[names[0]])
        self.deepseek = endpoint.hostname == "api.deepseek.com"
        self.batch = None
        if self.deepseek:
            if (
                endpoint.scheme != "https"
                or endpoint.path.rstrip("/") not in {"", "/v1"}
                or endpoint.port not in {None, 443}
                or endpoint.username
                or endpoint.password
                or endpoint.query
                or endpoint.fragment
                or self.model != "deepseek-v4-pro"
            ):
                raise ValueError("unsupported DeepSeek acceptance endpoint or model")
            path = os.environ.get("CHORA_WORKFLOW_BATCH_LEDGER")
            if not path:
                raise ValueError("CHORA_WORKFLOW_BATCH_LEDGER is required for DeepSeek")
            self.batch = BatchLedger(path)
        self.retry_limit = 0 if self.deepseek else 5
        self.identity = {
            "adapter": "openai.chat.v1",
            "provider": "deepseek" if self.deepseek else "openai-compatible",
            "model": self.model,
            "endpoint_digest": digest(os.environ[names[0]]),
            "profile": "deepseek.nonthinking.v1" if self.deepseek else "openai.compatible.v1",
            "thinking": "disabled" if self.deepseek else "provider_default",
            "output_parameter": "max_tokens" if self.deepseek else "max_completion_tokens",
            "max_output_tokens": 2048,
            "tool_choice": "required",
            "stream": False,
            "sdk_retries": 0,
            "graph_retries": self.retry_limit,
            "acceptance_batch": self.batch.identity if self.batch else None,
        }
        self.client = OpenAI(
            base_url=os.environ[names[0]], api_key=os.environ[names[2]], max_retries=0
        )

    def invoke(self, messages, tools, timeout, max_tokens):
        options = {
            "model": self.model,
            "messages": messages,
            "tools": tools,
            "tool_choice": "required",
        }
        reservation = None
        if self.deepseek:
            options.update(
                max_tokens=min(2048, max_tokens), thinking={"type": "disabled"}, stream=False
            )
            try:
                reservation = self.batch.reserve(options)
            except BatchRefused as exc:
                raise ModelFailure(
                    exc.category, usage_unknown=exc.category in {"outcome_unknown", "usage_unknown"}
                ) from None
            timeout = min(timeout, reservation["deadline"] - time.time())
            if timeout <= 0:
                try:
                    self.batch.finish(
                        reservation["request_id"],
                        {"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0},
                        "not_dispatched",
                    )
                except BatchRefused:
                    pass
                raise ModelFailure("deadline")
            options.pop("thinking")
            options["extra_body"] = {"thinking": {"type": "disabled"}}
        else:
            options["max_completion_tokens"] = max_tokens
        try:
            response = self.create(options, timeout)
        except ModelFailure as exc:
            if reservation:
                try:
                    self.batch.finish(reservation["request_id"], None, exc.category)
                except BatchRefused:
                    pass  # The durable halt and null usage have already committed.
                exc.usage_unknown = True
            raise
        known_tokens = None
        if reservation:
            usage = None
            if response.usage:
                usage = {
                    key: getattr(response.usage, key, None)
                    for key in ("prompt_tokens", "completion_tokens", "total_tokens")
                }
            try:
                self.batch.finish(reservation["request_id"], usage, "response")
            except BatchRefused as exc:
                raise ModelFailure(
                    exc.category,
                    usage_unknown=exc.category == "usage_unknown",
                    total_tokens=exc.total_tokens,
                ) from None
            known_tokens = usage["total_tokens"]
        try:
            calls = response.choices[0].message.tool_calls or []
            if any(c.type != "function" for c in calls):
                raise ModelFailure("invalid_result", total_tokens=known_tokens)
            return Reply(
                calls=[
                    Call(id=c.id, name=c.function.name, arguments=json.loads(c.function.arguments))
                    for c in calls
                ],
                total_tokens=response.usage.total_tokens if response.usage else None,
            )
        except (ValueError, IndexError, ValidationError, AttributeError):
            raise ModelFailure("invalid_result", total_tokens=known_tokens) from None

    def create(self, options, timeout):
        try:
            return self.client.chat.completions.create(**options, timeout=timeout)
        except (AuthenticationError, PermissionDeniedError):
            raise ModelFailure("authentication") from None
        except BadRequestError:
            raise ModelFailure("invalid_arguments") from None
        except RateLimitError:
            raise ModelFailure("rate_limit") from None
        except APITimeoutError:
            raise ModelFailure("timeout_unknown") from None
        except APIConnectionError:
            raise ModelFailure("outcome_unknown") from None
        except APIStatusError:
            raise ModelFailure("outcome_unknown") from None
        except (ValueError, ValidationError):
            raise ModelFailure("invalid_result") from None
