# M0 workflow protocol

This is the implemented local Python harness contract and the boundary for a
future Go bridge. There is no new Go API, UI entry or WorkflowGate database table
in this change. Go remains authoritative for identity, project authorization,
business snapshots, human decisions, Run/Attempt, workspace access and delivery.
Python owns the new graph's state. The local CLI is a trusted operator harness,
not a remotely exposed authorization boundary.

| Identity | Rule |
| --- | --- |
| `workflow_execution_id` | Caller-supplied UUID; identical to LangGraph `thread_id`; one immutable submission |
| `graph_version` / `state_schema_version` | `research.v1` / `1`; reject changes on continuation |
| `node_key` / `logical_iteration` | Model/tool/review node and persisted model step; retries consume a step |
| `operation_id` | UUIDv5 of workflow, graph version, node, iteration and attempt; invariant across receipt replay |
| `trace_id` | Created once with the durable submission, never regenerated on resume |
| `input_digest` | SHA-256 of canonical UTF-8 JSON; binds operation arguments or exact review report |
| `gate_id` | Stable workflow-specific review operation UUID; independent of old Run Gate |
| interrupt ID | LangGraph persisted interrupt identity; Resume explicitly targets that ID |
| `owner_epoch` | PG counter incremented by explicit claim; bound to saver and journal, not reset on resume |

## Commands

| Command | Implemented semantics |
| --- | --- |
| Submit | Persist immutable request and initial state, then claim and invoke. Identical ID/request returns existing state; conflicting reuse rejects. A crash before the first checkpoint retains the original deadline and trace. |
| Inspect | Read latest graph state and interrupt identity without claiming or requiring model credentials. Missing thread returns empty state. |
| Resume | Claim a new epoch and continue the same thread using the frozen config. Gate resume validates gate ID, report digest and accept/reject decision. Identical terminal decision is idempotent; conflicting or stale decision rejects. No new budget is granted. |
| Stop | Persist a stop intent in PG; idempotent. No subsequent operation reservation is accepted. Already reserved work can finish. It does not immediately kill network calls or change a paused checkpoint's displayed status; continuation records `stopped`. |
| ReadEvents | Return checkpointed trace events after a sequence cursor. Events are a state projection; operation receipts may exist before their graph event. No streaming transport/outbox guarantee yet. |

Review nodes have no external effect before `interrupt()`. LangGraph re-enters a
node on resume, so effects belong in journaled operations, not before interrupts.
This follows the upstream [interrupt replay contract](https://docs.langchain.com/oss/python/langgraph/interrupts).
Graph invocation uses synchronous checkpoint durability. The PG journal accounts
for the gap between an external response and its graph checkpoint; uncertain
operations stop for reconciliation.

## Future Go integration

M1 must introduce distinct WorkflowExecution/WorkflowGate business records and
project/actor authorization before exposing this harness. An accepted local report
does not authorize execution or automatically create a Run. Existing Run gates
require a running Run/Attempt and cannot represent this workflow's waiting state.

Across SQLite and PG, persist commands and receipts with stable `operation_id`,
use at-least-once delivery, and reconcile an uncertain Go response against the same
operation before creating another Run. Checkpoint fencing alone cannot make an
external command exactly once. Go must fence/authorize future effectful requests;
this M0 only dispatches allowlisted public read operations.

M6 must add leases, two-worker scheduling, ownership-aware command dispatch and
late-result reconciliation. An epoch row lock closes checkpoint mutation races;
it does not stop an external request that was reserved before takeover.
