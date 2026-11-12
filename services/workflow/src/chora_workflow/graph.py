"""Bounded model -> validated tools -> model loop with a durable review interrupt."""

import json
import time
from uuid import uuid4

import httpx
from langgraph.graph import END, START, StateGraph
from langgraph.types import interrupt
from pydantic import ValidationError

from .model import ModelFailure
from .protocol import (
    Decision,
    GRAPH_VERSION,
    Limits,
    PROMPT,
    PROMPT_VERSION,
    Reply,
    STATE_VERSION,
    State,
    TOOLS_VERSION,
    digest,
    operation_id,
)
from .storage import OutcomeUnknown, Stopped
from .tools import SCHEMAS, TOOL_SCHEMAS, validate_result


def frozen_config(model, catalog):
    return {
        "model": model.identity,
        "prompt_version": PROMPT_VERSION,
        "prompt_digest": digest(PROMPT),
        "tools_version": TOOLS_VERSION,
        "tools_digest": digest(TOOL_SCHEMAS),
        "corpus": catalog.identity,
    }


def initial_state(thread, question, model, catalog, limits=None):
    limits = limits or Limits()
    if not question.strip() or len(question) > 8000:
        raise ValueError("question must contain 1..8000 characters")
    return State(
        workflow_execution_id=thread,
        trace_id=str(uuid4()),
        graph_version=GRAPH_VERSION,
        state_schema_version=STATE_VERSION,
        frozen=frozen_config(model, catalog),
        question=question,
        limits=limits.model_dump(),
        deadline=time.time() + limits.duration_seconds,
        steps=0,
        spent_tokens=0,
        usage_unknown=False,
        retries=0,
        messages=[{"role": "system", "content": PROMPT}, {"role": "user", "content": question}],
        calls=[],
        evidence={},
        report=None,
        status="running",
        error=None,
        gate=None,
        events=[],
    )


def event(state, node, kind, **fields):
    return {
        "sequence": len(state["events"]) + 1,
        "trace_id": state["trace_id"],
        "workflow_execution_id": state["workflow_execution_id"],
        "node_key": node,
        "logical_iteration": state["steps"],
        "kind": kind,
        **fields,
    }


def fail(state, category, **fields):
    events = fields.pop("events", state["events"])
    return {
        "status": "stopped" if category == "cancelled" else "failed",
        "error": category,
        "messages": state["messages"],
        "evidence": state["evidence"],
        "events": events
        + [event({**state, "events": events}, "control", "failed", category=category)],
        **fields,
    }


class Research:
    def __init__(self, model, catalog, journal):
        self.model, self.catalog, self.journal = model, catalog, journal

    def validate_frozen(self, state):
        if (
            state["graph_version"] != GRAPH_VERSION
            or state["state_schema_version"] != STATE_VERSION
            or state["frozen"] != frozen_config(self.model, self.catalog)
        ):
            raise ValueError("frozen workflow configuration changed")

    def check(self, state):
        self.validate_frozen(state)
        self.journal.check()  # A database failure propagates; never proceed with a stale cache.
        if time.time() >= state["deadline"]:
            return "deadline"
        if state["usage_unknown"]:
            return "usage_unknown"
        if state["spent_tokens"] >= state["limits"]["max_tokens"]:
            return "token_budget"
        return None

    def model_node(self, state):
        try:
            self.validate_frozen(state)
            op = operation_id(state["workflow_execution_id"], "model", state["steps"])
            inputs = {"messages": state["messages"], "frozen": state["frozen"]}
            receipt = self.journal.lookup(op, inputs)
            if receipt is None:
                if reason := self.check(state):
                    return fail(state, reason)
                if state["steps"] >= state["limits"]["max_steps"]:
                    return fail(state, "step_budget")

            def call():
                try:
                    reply = self.model.invoke(
                        state["messages"],
                        TOOL_SCHEMAS,
                        min(state["limits"]["timeout_seconds"], state["deadline"] - time.time()),
                        min(2048, state["limits"]["max_tokens"] - state["spent_tokens"]),
                    )
                    return {"reply": Reply.model_validate(reply).model_dump()}
                except ModelFailure as exc:
                    return {"error": exc.category}
                except ValidationError:
                    return {"error": "invalid_result"}

            if receipt is None:
                receipt = self.journal.perform(op, inputs, call)
        except Stopped:
            return fail(state, "cancelled")
        except OutcomeUnknown:
            return fail(state, "outcome_unknown", usage_unknown=True)
        step = state["steps"] + 1
        events = state["events"] + [
            event(state, "model", "model_receipt", operation_id=op, receipt=receipt)
        ]
        if "error" in receipt:
            error = receipt["error"]
            if error == "rate_limit" and state["retries"] < state["limits"]["max_retries"]:
                return {
                    "steps": step,
                    "retries": state["retries"] + 1,
                    "events": events,
                    "status": "retry",
                    "calls": [],
                }
            return fail(
                state,
                error,
                steps=step,
                events=events,
                usage_unknown=error in {"timeout_unknown", "outcome_unknown", "invalid_result"},
            )
        reply = Reply.model_validate(receipt["reply"])
        usage = reply.total_tokens
        updates = {
            "steps": step,
            "spent_tokens": state["spent_tokens"] + (usage or 0),
            "usage_unknown": usage is None,
            "events": events,
        }
        if usage is None:
            return fail(state, "usage_unknown", **updates)
        if not reply.calls:
            return fail(state, "invalid_result", **updates)
        calls = [c.model_dump() for c in reply.calls]
        return {
            **updates,
            "calls": calls,
            "status": "tools",
            "messages": state["messages"]
            + [
                {
                    "role": "assistant",
                    "content": None,
                    "tool_calls": [
                        {
                            "id": c.id,
                            "type": "function",
                            "function": {"name": c.name, "arguments": json.dumps(c.arguments)},
                        }
                        for c in reply.calls
                    ],
                }
            ],
        }

    def tools_node(self, state):
        # Validate the whole batch before dispatching the first call.
        calls = state["calls"]
        if len({c["id"] for c in calls}) != len(calls):
            return fail(state, "duplicate_tool_call")
        try:
            for call in calls:
                if call["name"] not in SCHEMAS:
                    return fail(state, "unknown_tool")
                SCHEMAS[call["name"]].model_validate(call["arguments"])
                if call["name"] == "submit_report" and len(calls) != 1:
                    return fail(state, "report_must_be_alone")
        except ValidationError:
            return fail(state, "invalid_arguments")
        messages, evidence, events = (
            list(state["messages"]),
            dict(state["evidence"]),
            list(state["events"]),
        )
        for index, call in enumerate(calls):
            partial = {**state, "messages": messages, "evidence": evidence, "events": events}
            try:
                self.validate_frozen(state)
                name, args = call["name"], call["arguments"]
                if name == "submit_report":
                    if reason := self.check(state):
                        return fail(partial, reason)
                    if not any(e.get("tool") == "query_public" for e in events):
                        return fail(state, "missing_query")
                    if (
                        any(c not in evidence for c in args["citations"])
                        or (not args["no_answer"] and not args["citations"])
                        or (args["no_answer"] and args["citations"])
                    ):
                        return fail(state, "invalid_citations")
                    report = SCHEMAS[name].model_validate(args).model_dump()
                    gate = {
                        "gate_id": operation_id(state["workflow_execution_id"], "review", 0),
                        "input_digest": digest(report),
                    }
                    return {
                        "report": report,
                        "gate": gate,
                        "status": "waiting",
                        "events": events + [event({**state, "events": events}, "tools", "report")],
                    }
                op = operation_id(state["workflow_execution_id"], f"tool:{index}", state["steps"])
                inputs = {"tool": name, "args": args, "corpus": state["frozen"]["corpus"]}
                receipt = self.journal.lookup(op, inputs)
                if receipt is None:
                    if reason := self.check(state):
                        return fail(partial, reason)

                def execute():
                    try:
                        timeout = min(
                            state["limits"]["timeout_seconds"], state["deadline"] - time.time()
                        )
                        value = (
                            self.catalog.query(args["query"], timeout)
                            if name == "query_public"
                            else self.catalog.fetch(args["evidence_id"], timeout)
                        )
                        return {"result": validate_result(name, value)}
                    except httpx.TimeoutException:
                        return {"error": "tool_timeout"}
                    except httpx.HTTPError:
                        return {"error": "tool_transport"}
                    except (ValueError, UnicodeError):
                        return {"error": "invalid_tool_result"}

                if receipt is None:
                    receipt = self.journal.perform(op, inputs, execute)
                if "error" in receipt:
                    return fail(partial, receipt["error"])
                result = validate_result(name, receipt["result"])
                if name == "read_evidence":
                    if result["evidence_id"] != args["evidence_id"]:
                        return fail(partial, "invalid_tool_result")
                    evidence[result["evidence_id"]] = result
                messages.append(
                    {"role": "tool", "tool_call_id": call["id"], "content": json.dumps(result)}
                )
                events.append(
                    event(
                        {**state, "events": events},
                        "tools",
                        "tool_receipt",
                        operation_id=op,
                        tool=name,
                        input_digest=digest(args),
                        result_digest=digest(result),
                    )
                )
            except Stopped:
                return fail(partial, "cancelled")
            except OutcomeUnknown:
                return fail(partial, "tool_outcome_unknown")
        return {"messages": messages, "evidence": evidence, "events": events, "status": "running"}

    def review_node(self, state):
        self.validate_frozen(state)
        decision = Decision.model_validate(interrupt({**state["gate"], "report": state["report"]}))
        if (
            decision.gate_id != state["gate"]["gate_id"]
            or decision.input_digest != state["gate"]["input_digest"]
        ):
            raise ValueError("stale review decision")
        # Cached pending writes must agree with the durable command, not merely
        # with the gate/digest. Never let an old accept consume a new reject.
        command = self.journal.verify_decision(decision.model_dump())
        try:
            if reason := self.check(state):
                return fail(state, reason)
        except Stopped:
            return fail(state, "cancelled")
        return {
            "status": "accepted" if decision.decision == "accept" else "rejected",
            "events": state["events"]
            + [event(state, "review", decision.decision, command_id=command["command_id"])],
        }

    def compile(self, saver):
        graph = StateGraph(State)
        graph.add_node("model", self.model_node)
        graph.add_node("tools", self.tools_node)
        graph.add_node("review", self.review_node)
        graph.add_edge(START, "model")
        graph.add_conditional_edges(
            "model",
            lambda s: (
                "tools" if s["status"] == "tools" else "model" if s["status"] == "retry" else END
            ),
        )
        graph.add_conditional_edges(
            "tools",
            lambda s: (
                "review"
                if s["status"] == "waiting"
                else "model"
                if s["status"] == "running"
                else END
            ),
        )
        graph.add_edge("review", END)
        return graph.compile(checkpointer=saver)
