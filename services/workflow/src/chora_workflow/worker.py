"""Local M0 harness. Go integration, authorization and lease scheduling are separate work."""

from contextlib import contextmanager
from uuid import UUID

from langgraph.checkpoint.postgres import PostgresSaver
from langgraph.checkpoint.serde.jsonplus import JsonPlusSerializer
from langgraph.types import Command

from .checkpoint import FencedSaver
from .graph import Research, initial_state, frozen_config
from .protocol import Decision, Limits
from .storage import Journal, connect


class Worker:
    def __init__(self, dsn, model, catalog):
        self.dsn, self.model, self.catalog = dsn, model, catalog

    def config(self, thread):
        UUID(thread)
        return {"configurable": {"thread_id": thread, "checkpoint_ns": ""}, "recursion_limit": 250}

    @contextmanager
    def graph(self, thread):
        epoch = Journal.claim(self.dsn, thread)
        with connect(self.dsn) as conn:
            yield Research(self.model, self.catalog, Journal(self.dsn, thread, epoch)).compile(
                FencedSaver(conn, thread, epoch)
            )

    def submit(self, thread, question, limits=None):
        config = self.config(thread)
        limits = limits or Limits()
        submission = {
            "question": question,
            "limits": limits.model_dump(),
            "frozen": frozen_config(self.model, self.catalog),
        }
        initial = Journal.submit(
            self.dsn,
            thread,
            submission,
            initial_state(thread, question, self.model, self.catalog, limits),
        )
        with self.graph(thread) as graph:
            snapshot = graph.get_state(config)
            if snapshot.values:
                return snapshot  # Submit is idempotent, never restarts a completed graph.
            graph.invoke(
                initial,
                config,
                durability="sync",
            )
            return graph.get_state(config)

    def inspect(self, thread):
        config = self.config(thread)
        with connect(self.dsn) as conn:
            saver = PostgresSaver(conn, serde=JsonPlusSerializer(allowed_msgpack_modules=[]))
            graph = Research(self.model, self.catalog, None).compile(saver)
            return graph.get_state(config)

    def resume(self, thread, decision=None):
        config = self.config(thread)
        with self.graph(thread) as graph:
            snapshot = graph.get_state(config)
            if not snapshot.values:
                raise ValueError("workflow not started")
            if snapshot.values["frozen"] != frozen_config(self.model, self.catalog):
                raise ValueError("frozen workflow configuration changed")
            if not snapshot.next:
                if decision:
                    parsed = Decision.model_validate(decision)
                    gate = snapshot.values.get("gate")
                    expected = "accepted" if parsed.decision == "accept" else "rejected"
                    if (
                        not gate
                        or parsed.gate_id != gate["gate_id"]
                        or parsed.input_digest != gate["input_digest"]
                        or snapshot.values["status"] != expected
                    ):
                        raise ValueError("conflicting or stale decision")
                return snapshot
            if snapshot.interrupts:
                parsed = Decision.model_validate(decision)
                if (
                    parsed.gate_id != snapshot.values["gate"]["gate_id"]
                    or parsed.input_digest != snapshot.values["gate"]["input_digest"]
                ):
                    raise ValueError("stale review decision")
                # Bind to the persisted interrupt ID, not a positional anonymous resume.
                command = Command(resume={snapshot.interrupts[0].id: parsed.model_dump()})
            else:
                if decision is not None:
                    raise ValueError("workflow has no review interrupt")
                command = None
            graph.invoke(command, config, durability="sync")
            return graph.get_state(config)

    def stop(self, thread):
        self.config(thread)
        Journal.stop(self.dsn, thread)
        # Persisted intent stops future dispatch. In-flight calls can still finish.

    def read_events(self, thread, after=0):
        return [e for e in self.inspect(thread).values.get("events", []) if e["sequence"] > after]
