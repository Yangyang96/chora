"""Local M0 harness. Go integration, authorization and lease scheduling are separate work."""

from contextlib import contextmanager
from uuid import UUID

from langgraph.checkpoint.postgres import PostgresSaver
from langgraph.checkpoint.serde.jsonplus import JsonPlusSerializer
from langgraph.types import Command

from .checkpoint import FencedSaver
from .graph import Research, initial_state, frozen_config
from .protocol import Decision, Limits
from .observation import Execution, validate_observation
from .storage import Journal, connect


class Worker:
    def __init__(self, dsn, model, catalog, *, sample_type=None, code_revision=None):
        self.dsn, self.model, self.catalog = dsn, model, catalog
        self.execution = Execution(
            model, catalog, sample_type=sample_type, code_revision=code_revision
        )

    def config(self, thread):
        UUID(thread)
        return {"configurable": {"thread_id": thread, "checkpoint_ns": ""}, "recursion_limit": 250}

    @contextmanager
    def graph(self, thread):
        epoch = Journal.claim(self.dsn, thread)
        journal = Journal(self.dsn, thread, epoch, self.execution)
        journal.validate_sample()
        with connect(self.dsn) as conn:
            yield Research(self.model, self.catalog, journal).compile(
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
            {
                **initial_state(thread, question, self.model, self.catalog, limits),
                "sample": self.execution.sample.model_dump(),
            },
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
            # Must precede Command(resume): LangGraph can commit pending resume
            # writes before the review node gets to validate the executable.
            journal = Journal(self.dsn, thread, graph.checkpointer.epoch, self.execution)
            Research(self.model, self.catalog, journal).validate_frozen(snapshot.values)
            parsed = Decision.model_validate(decision) if decision is not None else None
            gate = snapshot.values.get("gate")
            bound = journal.decision(gate["gate_id"]) if gate else None
            saved = graph.checkpointer.get_tuple(config)
            if not bound and any(
                channel == "__resume__" for _, channel, _ in saved.pending_writes or []
            ):
                # Old, unjournaled decisions cannot safely be attributed to this
                # request. Preserve evidence for operator reconciliation.
                raise ValueError("conflicting unbound pending resume; reconciliation required")
            if parsed:
                if (
                    not gate
                    or parsed.gate_id != gate["gate_id"]
                    or parsed.input_digest != gate["input_digest"]
                ):
                    raise ValueError("stale review decision")
                if bound:
                    journal.verify_decision(parsed.model_dump())
            if not snapshot.next:
                if parsed and not bound:
                    expected = "accepted" if parsed.decision == "accept" else "rejected"
                    if snapshot.values["status"] != expected:
                        raise ValueError("conflicting or stale decision")
                if saved.pending_writes:
                    # get_state projects pending node outputs, which are not yet
                    # a terminal checkpoint. Drain them without another decision.
                    graph.invoke(None, config, durability="sync")
                    return graph.get_state(config)
                return snapshot
            if snapshot.interrupts:
                if parsed is None:
                    raise ValueError("review decision required")
                journal.bind_decision(parsed.model_dump(), snapshot.interrupts[0].id)
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
        state = self.inspect(thread).values
        events = []
        for event in state.get("events", []):
            for key in ("observation", "receipt_observation"):
                if key in event and (key == "observation" or event[key] is not None):
                    validate_observation(event[key], thread, state["trace_id"])
            if event["sequence"] > after:
                events.append({"observation": None, **event})
        return events
