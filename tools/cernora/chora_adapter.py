"""Opt-in, offline Chora completed-record projection for Cernora inspection.

This module has no runtime, store or review authority. The caller supplies a
sanitized completed record; Cernora validates the projected inspection contract.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import stat
import sys
import tempfile
from typing import Any, Literal

from pydantic import BaseModel, ConfigDict, ValidationError

from cernora.core.errors import ContractError
from cernora.evaluation.agent_export import load_agent_run_export


MAX_BYTES = 16 * 1024 * 1024


class AdapterError(ValueError):
    """Unsafe, incomplete or incompatible completed-record input."""


class ClosedModel(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)


class RecordedEvent(ClosedModel):
    sequence: int
    timestampMs: int
    type: Literal["user", "assistant_message", "tool_start", "tool_end", "error", "final"]
    payload: dict[str, Any]


class CompletedRecord(ClosedModel):
    schemaVersion: Literal["chora.cernora-completed-record/v1"]
    completed: Literal[True]
    runId: str
    taskId: str
    caseId: str
    conditions: dict[str, Any]
    userTask: str
    finalAnswer: str
    events: list[RecordedEvent]
    claims: list[dict[str, Any]]


def canonical(value: Any) -> bytes:
    return json.dumps(value, ensure_ascii=False, allow_nan=False,
                      sort_keys=True, separators=(",", ":")).encode("utf-8")


def _object(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
    result: dict[str, Any] = {}
    for key, value in pairs:
        if key in result:
            raise AdapterError("duplicate JSON member")
        result[key] = value
    return result


def _constant(_: str) -> None:
    raise AdapterError("non-finite JSON number")


def adapt_record(raw: bytes) -> bytes:
    """Map recorded facts only; never read references or infer missing results."""
    if len(raw) > MAX_BYTES:
        raise AdapterError("completed record exceeds size limit")
    try:
        decoded = json.loads(raw.decode("utf-8"), object_pairs_hook=_object,
                             parse_constant=_constant)
        # Validate JSON, rather than Python dicts, for strict JSON array handling.
        record = CompletedRecord.model_validate_json(canonical(decoded), strict=True)
        if decoded["completed"] is not True:
            raise AdapterError("record is not terminal")
        events: list[dict[str, Any]] = []
        outputs: dict[str, str] = {}
        kinds = {"assistant_message": "assistant", "tool_start": "tool_call",
                 "tool_end": "tool_result"}
        for index, event in enumerate(record.events):
            if event.sequence != index + 1:
                raise AdapterError("source sequences must be contiguous from one")
            payload = dict(event.payload)
            if event.type == "tool_end":
                # Absence and an observed empty stream are different facts.
                if "stdout" not in payload:
                    raise AdapterError("tool result must explicitly record stdout or null")
                stdout = payload.pop("stdout")
                evidence_id = payload.get("evidence_id")
                if stdout is None:
                    if evidence_id is not None:
                        raise AdapterError("missing stdout cannot own evidence")
                else:
                    if type(stdout) is not str or type(evidence_id) is not str or not evidence_id:
                        raise AdapterError("observed stdout requires its evidence ID")
                    if evidence_id in outputs:
                        raise AdapterError("duplicate stdout evidence ID")
                    outputs[evidence_id] = stdout
            events.append({"seq": index, "timestamp_ms": event.timestampMs,
                           "kind": kinds.get(event.type, event.type), "payload": payload})
        projected = {
            "schema_version": "agent-run-export/v1",
            "run_id": record.runId, "task_id": record.taskId, "case_id": record.caseId,
            "conditions": record.conditions, "user_task": record.userTask,
            "final_answer": record.finalAnswer, "events": events, "claims": record.claims,
            "tool_outputs": outputs,
            "tool_output_digests": {
                key: hashlib.sha256(value.encode("utf-8")).hexdigest()
                for key, value in outputs.items()
            },
        }
        result = canonical(projected)
        # Own the mapping; delegate public wire validation to Cernora itself.
        load_agent_run_export(result)
        return result
    except (ValidationError, ContractError, UnicodeError, ValueError, TypeError,
            KeyError, RecursionError) as error:
        raise AdapterError("invalid completed record or inspection export") from error


def export_record(source: Path, destination: Path) -> None:
    """Read one ordinary file and atomically publish without replacing output."""
    descriptor = os.open(source, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(descriptor, "rb") as stream:
        if not stat.S_ISREG(os.fstat(stream.fileno()).st_mode):
            raise AdapterError("completed record must be an ordinary file")
        raw = stream.read(MAX_BYTES + 1)
    result = adapt_record(raw)
    # Staging and the destination are on the same filesystem. A hard link is
    # atomic and refuses an existing file, directory or symbolic link.
    staging: Path | None = None
    try:
        with tempfile.NamedTemporaryFile(dir=destination.parent, prefix=".chora-cernora-",
                                         delete=False) as stream:
            staging = Path(stream.name)
            stream.write(result)
            stream.flush()
            os.fsync(stream.fileno())
        os.link(staging, destination)
    finally:
        if staging is not None:
            staging.unlink()


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("record", type=Path)
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    try:
        export_record(args.record, args.output)
    except (AdapterError, OSError):
        # Source bytes may contain private data; do not echo validation values.
        print("completed-record export rejected; check contract and file paths", file=sys.stderr)
        return 3
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
