"""Versioned collection metadata; never an authorization or deduplication input."""

import time
from typing import Literal
from uuid import uuid4

from pydantic import Field, model_validator

from .protocol import StrictModel

Mode = Literal["live", "fixture", "mock", "unknown"]
SampleType = Literal[
    "real_usage",
    "real_model_acceptance",
    "deterministic_injection",
    "fixture",
    "mock",
    "load_test",
    "unknown",
]


class Sample(StrictModel):
    sample_type: SampleType
    source: Literal["operator", "adapter_declaration", "unknown"]
    model_mode: Mode
    catalog_mode: Mode

    @model_validator(mode="after")
    def check_label(self):
        if self.sample_type in {"real_usage", "real_model_acceptance"} and (
            self.model_mode != "live" or self.catalog_mode != "live" or self.source != "operator"
        ):
            raise ValueError("real sample requires explicit operator label and live adapters")
        if self.sample_type == "fixture" and "fixture" not in (self.model_mode, self.catalog_mode):
            raise ValueError("fixture sample requires a fixture adapter")
        if self.sample_type == "mock" and "mock" not in (self.model_mode, self.catalog_mode):
            raise ValueError("mock sample requires a mock adapter")
        return self


class Budget(StrictModel):
    steps: int
    retries: int
    spent_tokens: int
    usage_unknown: bool
    deadline: float
    limits: dict


class Observation(StrictModel):
    schema_version: Literal[1]
    workflow_execution_id: str
    trace_id: str | None
    worker_instance_id: str = Field(pattern=r"^[0-9a-f-]{36}$")
    owner_epoch: int = Field(ge=1)
    occurred_at: float | None = Field(allow_inf_nan=False)
    recorded_at: float = Field(allow_inf_nan=False)
    clock_source: Literal["worker_unix_seconds"]
    code_revision: str | None = Field(pattern=r"^[0-9a-f]{40}$")
    code_revision_source: Literal["operator", "unknown"]
    sample: Sample
    budget_before_node: Budget | None

    @model_validator(mode="after")
    def check_revision_source(self):
        if (self.code_revision is None) != (self.code_revision_source == "unknown"):
            raise ValueError("code revision source mismatch")
        return self


class Execution:
    def __init__(self, model=None, catalog=None, *, sample_type=None, code_revision=None):
        self.worker_instance_id = str(uuid4())
        modes = [getattr(adapter, "observation_mode", "unknown") for adapter in (model, catalog)]
        inferred = "mock" if "mock" in modes else "fixture" if "fixture" in modes else "unknown"
        self.sample = Sample(
            sample_type=sample_type if sample_type is not None else inferred,
            source="operator"
            if sample_type is not None
            else ("unknown" if inferred == "unknown" else "adapter_declaration"),
            model_mode=modes[0],
            catalog_mode=modes[1],
        )
        self.code_revision = code_revision
        # Validate deployment input before any database write or provider call.
        self.record("validation", None, 1)

    def record(self, thread, trace, epoch, state=None, *, occurred_at=None):
        return Observation(
            schema_version=1,
            clock_source="worker_unix_seconds",
            workflow_execution_id=thread,
            trace_id=trace,
            worker_instance_id=self.worker_instance_id,
            owner_epoch=epoch,
            occurred_at=occurred_at,
            recorded_at=time.time(),
            code_revision=self.code_revision,
            code_revision_source="operator" if self.code_revision is not None else "unknown",
            sample=self.sample,
            budget_before_node=Budget(**{key: state[key] for key in Budget.model_fields})
            if state
            else None,
        ).model_dump()


def validate_observation(value, thread, trace):
    parsed = Observation.model_validate(value)
    if parsed.workflow_execution_id != thread or parsed.trace_id != trace:
        raise ValueError("observation identity mismatch")
    return parsed
