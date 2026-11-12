"""Explicitly configured OpenAI-compatible SDK adapter; no credential discovery."""

import json
import os

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

from .protocol import Call, Reply, digest


class ModelFailure(Exception):
    def __init__(self, category):
        self.category = category
        super().__init__(category)


class SDKModel:
    observation_mode = "live"

    def __init__(self):
        names = ("CHORA_WORKFLOW_BASE_URL", "CHORA_WORKFLOW_MODEL", "CHORA_WORKFLOW_API_KEY")
        missing = [name for name in names if not os.environ.get(name)]
        if missing:
            raise ValueError("missing explicit model configuration: " + ", ".join(missing))
        self.model = os.environ[names[1]]
        self.identity = {
            "adapter": "openai.chat.v1",
            "model": self.model,
            "endpoint_digest": digest(os.environ[names[0]]),
        }
        self.client = OpenAI(
            base_url=os.environ[names[0]], api_key=os.environ[names[2]], max_retries=0
        )

    def invoke(self, messages, tools, timeout, max_tokens):
        try:
            response = self.client.chat.completions.create(
                model=self.model,
                messages=messages,
                tools=tools,
                tool_choice="required",
                max_completion_tokens=max_tokens,
                timeout=timeout,
            )
            calls = response.choices[0].message.tool_calls or []
            if any(c.type != "function" for c in calls):
                raise ModelFailure("invalid_result")
            return Reply(
                calls=[
                    Call(id=c.id, name=c.function.name, arguments=json.loads(c.function.arguments))
                    for c in calls
                ],
                total_tokens=response.usage.total_tokens if response.usage else None,
            )
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
        except (ValueError, IndexError, ValidationError):
            raise ModelFailure("invalid_result") from None
