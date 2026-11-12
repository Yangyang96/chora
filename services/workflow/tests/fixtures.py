"""Deterministic fixtures: never presented as real LLM or network acceptance."""

from chora_workflow.model import ModelFailure
from chora_workflow.protocol import Call, Reply, digest


class FixtureModel:
    identity = {"adapter": "deterministic-test", "model": "fixture-v1"}

    def __init__(self, fail_first=False):
        self.fail_first = fail_first
        self.calls = 0

    def invoke(self, messages, tools, timeout, max_tokens):
        self.calls += 1
        if self.fail_first and self.calls == 1:
            raise ModelFailure("rate_limit")
        results = [m for m in messages if m["role"] == "tool"]
        index = len(results)
        name, args = [
            ("query_public", {"query": "Chora"}),
            ("read_evidence", {"evidence_id": "readme"}),
            (
                "submit_report",
                {
                    "summary": "Chora is a local workbench.",
                    "citations": ["readme"],
                    "no_answer": False,
                },
            ),
        ][min(index, 2)]
        return Reply(calls=[Call(id=f"call{index}", name=name, arguments=args)], total_tokens=7)


class FixtureCatalog:
    identity = {"revision": "synthetic-v1"}

    def fetch(self, evidence_id, timeout):
        if evidence_id != "readme":
            raise ValueError("unknown evidence")
        text = "Chora is a local workbench."
        return {
            "evidence_id": evidence_id,
            "url": "https://example.org/chora",
            "content": text,
            "digest": digest(text),
        }

    def query(self, query, timeout):
        return [{"evidence_id": "readme", "url": "https://example.org/chora", "score": 1}]
