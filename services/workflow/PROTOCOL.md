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
| `operation_id` | UUIDv5 of workflow, graph version, node, iteration and logical attempt (M0 always `0`); invariant across delivery, restart, claim and receipt replay |
| `trace_id` | Created once with the durable submission, never regenerated on resume |
| `input_digest` | SHA-256 of canonical UTF-8 JSON; binds operation arguments or exact review report |
| `gate_id` | Stable workflow-specific review operation UUID; independent of old Run Gate |
| interrupt ID | LangGraph persisted interrupt identity; Resume explicitly targets that ID |
| review `command_id` | SHA-256 of canonical thread/interrupt/gate/digest/decision binding; identical deliveries reuse it |
| `owner_epoch` | Monotonically increasing PG authorization/write fence; never part of the operation, gate, command or input-digest deduplication key |

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
decision table and nullable collection columns. Existing waiting checkpoints with no pending resume can continue.
Unjournaled pending resumes from an older executable are explicitly refused for
operator reconciliation, including same-value requests: their command provenance
cannot be proved. Do not delete pending writes or manufacture a journal row to
retry them. Completed legacy checkpoints retain their terminal decision check.

## Logical attempts and execution instances

A workflow/thread/trace is a durable logical execution, not a Python process.
`Worker` creates a random `worker_instance_id` once per constructed execution
instance; new instances (including in a new interpreter) get new IDs. A claim
increments `owner_epoch` even when an invalid resume is subsequently refused.
Neither is a model retry counter, a delivery attempt, or a Go Run/Attempt. M0 does
not implement transport attempt IDs or Go Run/Attempt records and does not invent
them in metadata. A process may own several Worker instances; this ID is not a
host ID or a claim of one permanent process per workflow.

`operation_id(thread, node, iteration, attempt=0)` keeps its existing UUID formula.
The final argument is reserved for an explicitly new **logical** attempt; callers
must never put a process restart, epoch or message-delivery counter there. Current
M0 always uses `0`. A known rate-limit rejection permits the bounded graph retry:
it consumes a persisted step and retry, uses the next logical iteration, and the
new model event records `retry_of_operation_id` pointing to the rejected operation.
Other model events set that link to null. Unknown results never enter this retry
path. Any future nonzero logical attempt must persist its reason and parent
operation before dispatch and consume the existing cumulative budget.

Operation journal `input_digest` binds the full canonical operation input: model
messages plus frozen configuration, or tool name/arguments/corpus. New versioned
tool events use that same digest and retain an `arguments_digest` separately.
Legacy unversioned tool events used `input_digest` for arguments alone; interpret
them under the old contract and do not rewrite them. Gates continue to bind the
exact report digest. Review command JSON and its hash are unchanged: observation
metadata is in a separate column and cannot alter the winning decision.

## Collection schema 1

Collection is additive and separately versioned from `research.v1` / state `1`.
It records the execution paths that exist today, not a complete M5 metrics system.

| Record | Stored association |
| --- | --- |
| Submission initial state | Optional `sample`, frozen on new submissions; existing trace, limits and absolute deadline remain authoritative |
| Checkpoint event | Existing workflow/trace/node/iteration/sequence/kind plus `observation`; operation events also link operation ID, input digest and original `receipt_observation` |
| Operation row | Existing thread/operation/input digest/receipt plus `reservation_observation` and `receipt_observation` |
| Review command row | Existing thread/gate/command plus `observation`; retries return the original command and never rewrite this record |

Each non-null observation requires `schema_version: 1`, workflow ID, nullable
trace ID, worker instance ID, epoch, `occurred_at`, `recorded_at`, clock source,
code revision/source, sample and nullable `budget_before_node`. A missing required
field, unsupported version, identity mismatch or invalid sample fails validation
on ReadEvents, journal reads and recovery; Inspect retains raw diagnostic state. The trace comes from the durable submission (null only for a
low-level journal without an initial state). There is no inferred trace or Run ID.

Times are Unix seconds from the worker's wall clock, not a distributed ordering
clock. `occurred_at` means the local reservation intent, return of the action
(including a known error), review binding intent, or creation of an event
projection, depending on the containing record. `recorded_at` is when the metadata
was constructed before its database/checkpoint write, **not** the transaction
commit time. A crash before commit leaves no durable observation of that attempted
write. Unknown provider start, actual remote effect, failure detection and recovery
latencies are not inferred. Clock skew can make cross-worker timestamps unordered;
use sequence/epoch and causal links, not subtraction, for correctness.

Receipt replay leaves the entire operation row unchanged. A newly projected event
may carry the recovering worker/time in `observation`, while its separate
`receipt_observation` retains the original worker/epoch/time (or null for a legacy
receipt). Already checkpointed events are never relabeled with the new instance.
`budget_before_node` is a snapshot of steps, retries, known token subtotal,
unknown-usage flag, absolute deadline and limits before that node; direct journal
and decision calls without graph state use null. It is not a second budget ledger.
Actual reported model usage remains `receipt.reply.total_tokens`, including null
for unknown; tools have no model-token usage claim. Missing usage is not zero and
continues to stop progression under the existing graph controls.

`code_revision` is an optional exact 40-character lowercase Git commit reference
supplied through `Worker(..., code_revision=...)` or CLI `--code-revision`.
`code_revision_source` is `operator` when supplied, otherwise `unknown` with a null
reference. The harness does not guess HEAD, inspect the filesystem for a revision,
or attest that the supplied revision matches the executable. A new executable may
record a different reference without changing logical identity; frozen graph,
model, prompt, tools and corpus compatibility checks still apply.

Sample purposes are `real_usage`, `real_model_acceptance`,
`deterministic_injection`, `fixture`, `mock`, `load_test`, and `unknown`. Set the
purpose with `Worker(..., sample_type=...)` or CLI `--sample-type`. It is distinct
from `model_mode` / `catalog_mode` (`live`, `fixture`, `mock`, `unknown`), which come
from trusted adapter declarations. SDK/public-catalog adapters declare live; the
test fixtures declare fixture. With no explicit purpose, fixture/mock adapters
produce that classification, and all other executions remain unknown. Live
adapters alone never imply real use or acceptance. Real-purpose labels require an
explicit operator declaration and both live adapters; fixture/mock/unknown
adapters cannot be relabeled as real. Injection and load tests retain their
component modes, so an injected fixture cannot be mistaken for a real model.
These are provenance declarations, not independently verified acceptance or a
security boundary against a malicious trusted adapter/operator.

New submissions persist this sample declaration, and continuation must supply the
same declaration before graph/command writes. Changing code reference or worker
identity does not change the sample. Inspect/ReadEvents need no deployment label.
Neither sample labels nor metadata grant authority, change a receipt's dedup key,
reset budget, bypass frozen-version checks, or authorize Go actions.

Stop all writers and run `init-db` offline before upgrading. It adds the three
nullable JSONB columns without backfilling history. Legacy events lacking `observation`
are returned by ReadEvents with null; stored events remain untouched. Legacy
journal columns are null. An absent initial sample is legacy/unknown, not evidence
of real use; new events can describe the current adapter context without certifying
old history. A present but partial observation is malformed, not legacy. Mixed
old/new writers and downgrade are unsupported. The nullable columns allow old
writers to continue writing rows with null metadata. Operators must control
deployed executable versions before restarting writers. Null metadata remains
legacy/unknown and cannot certify real execution. The old unjournaled-resume
refusal still applies.

All new journal metadata is committed in the same owner-lock/epoch-check
transaction as its reservation, receipt or decision; event metadata goes through
the existing fenced checkpoint/pending-write paths. Collection failure propagates:
a failed reservation dispatches nothing; a failed receipt write leaves an unknown
reservation and recovery does not resend. A failure after a durable receipt but
before its event replays that receipt, preserving usage. There is no best-effort
telemetry queue that can silently advance a workflow with unknown budget.

## Future Go integration

M1 must introduce distinct WorkflowExecution/WorkflowGate business records and
project/actor authorization before exposing this harness. An accepted local report
does not authorize execution or automatically create a Run. Existing Run gates
require a running Run/Attempt and cannot represent this workflow's waiting state.

Across SQLite and PG, persist commands and receipts with stable `operation_id`,
bind each operation to its canonical `input_digest`, use at-least-once delivery,
and query Go's durable command/receipt by that same operation after an uncertain
response **before** considering resend. A changed input under an existing ID must
reject; restart, takeover and transport redelivery cannot allocate a new operation
or new Run. A known durable result reuses the original Run/receipt. An unknown
result remains pending reconciliation: lack of a response is not proof of no
effect, and a resend requires the receiving Go endpoint's idempotency and existing
authorization checks. Persist any explicitly authorized new logical attempt and
its parent/reason before sending; never reset cumulative budgets. Checkpoint fencing alone cannot make an
external command exactly once. Go must fence/authorize future effectful requests;
this M0 only dispatches allowlisted public read operations.

M6 must add leases, two-worker scheduling, ownership-aware command dispatch and
late-result reconciliation. An epoch row lock closes checkpoint mutation races;
it does not stop an external request that was reserved before takeover.

M6 should extend versioned records with observed failure/detection time, claim time
and first durable recovery-progress time, each with a defined clock/source and null
when unobserved. It must also bind corpus/index versions and artifact references
to storage accessible to both workers (URI plus content/version digest and access
scope), rather than another worker's private temporary path. M0 only freezes its
public catalog identity and evidence digests; it has no shared index/artifact store,
lease protocol, cross-host clock synchronization or two-worker scheduler. Future
fields need a new validated version/compatibility rule, not fabricated M0 values.
