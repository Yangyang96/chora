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
| review `command_id` | SHA-256 of canonical thread/interrupt/gate/digest/decision binding; identical deliveries reuse it |
| `owner_epoch` | PG counter incremented by explicit claim; bound to saver and journal, not reset on resume |

## Commands

| Command | Implemented semantics |
| --- | --- |
| Submit | Persist immutable request and initial state, then claim and invoke. Identical ID/request returns existing state; conflicting reuse rejects. A crash before the first checkpoint retains the original deadline and trace. |
| Inspect | Read latest graph state and interrupt identity without claiming or requiring model credentials. Missing thread returns empty state. |
| Resume | Claim a new epoch and validate graph/schema versions and frozen config before any resume write. Persist the exact gate/interrupt/digest/decision command before graph invocation. The first committed command wins; identical retries reuse it, and conflicting or stale requests explicitly reject even before a terminal checkpoint exists. No new budget is granted. |
| Stop | Persist a stop intent in PG; idempotent. No subsequent operation reservation is accepted. Already reserved work can finish. It does not immediately kill network calls or change a paused checkpoint's displayed status; continuation records `stopped`. |
| ReadEvents | Return checkpointed trace events after a sequence cursor. Events are a state projection; operation receipts may exist before their graph event. No streaming transport/outbox guarantee yet. |

Review nodes have no external effect before `interrupt()`. LangGraph re-enters a
node on resume, so effects belong in journaled operations, not before interrupts.
This follows the upstream [interrupt replay contract](https://docs.langchain.com/oss/python/langgraph/interrupts).
Graph invocation uses synchronous checkpoint durability. The PG journal accounts
for the gap between an external response and its graph checkpoint; uncertain
operations stop for reconciliation.

Review commands live in `workflow_decisions`. Insertion and comparison take the
same owner row lock and epoch check as the operation journal. Command persistence
and LangGraph pending writes are separate transactions: a crash between them leaves
a committed decision that the same request can safely deliver again. A crash after
pending resume persistence is handled the same way. The review node checks its
consumed value against the journal before producing a result; cached resume values
cannot silently substitute another decision. The review trace includes the stable
command ID. The checkpoint is the result projection, not the command authority.
If node output is already durable but the terminal checkpoint is missing, Resume
drains that output into a checkpoint without dispatching another decision or
re-executing the completed node.

Committing a command does not guarantee acceptance: cancellation, deadline and
usage checks still apply when the node resumes. An identical retry returns the
existing stopped/failed result in that case; it does not renew any budget or replace
the decision. A conflicting request fails even if the first command's outcome is
stopped/failed. These are local delivery semantics; Go retains authority over
future business decisions and authorization.

When upgrading an existing M0 database, stop workers and run `init-db` to add the
decision table. Existing waiting checkpoints with no pending resume can continue.
Unjournaled pending resumes from an older executable are explicitly refused for
operator reconciliation, including same-value requests: their command provenance
cannot be proved. Do not delete pending writes or manufacture a journal row to
retry them. Completed legacy checkpoints retain their terminal decision check.

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
