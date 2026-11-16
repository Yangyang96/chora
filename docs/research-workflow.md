# Research before coding

[简体中文](research-workflow.zh-CN.md)

This optional development feature researches public Chora documentation, presents
a cited proposal, and waits for the task owner to confirm or add coding
instructions. Confirmation creates one Pi Run for the existing Task. Normal
checks, result review and explicit delivery continue through the workbench.
Accepting a research proposal does not accept its code or authorize publication.
The default Pi workflow remains available when research is not selected.

## Setup and use

Install the locked Python package following
[the worker setup](../services/workflow/README.md), using Python 3.12 or 3.13
and a disposable or explicitly provisioned UTF-8 PostgreSQL database. Run the
documented `init-db` administrative command to add checkpoint and bridge tables;
workers never run migrations automatically. SQLite upgrades through migration
0050 when the workbench opens its database. Back up the database before an
upgrade. A database upgraded on another feature branch is not a compatible
replacement for this feature's migration history.

Configure `CHORA_WORKFLOW_PYTHON` with the absolute path of the environment's
Python interpreter, with `chora_workflow` installed. Pass the explicit
`CHORA_WORKFLOW_DATABASE_URL`, model and provider environment described in the
worker setup to the workbench process. Set `CHORA_WORKFLOW_CODE_REVISION` to the
deployed 40-character commit and `CHORA_WORKFLOW_SAMPLE_TYPE` to the actual
collection purpose. Credentials stay in the operator environment, outside
repository files, browser requests and diagnostics. Existing acceptance ledgers
and expired authorizations do not grant new model-call budget.

For a new repository Task, select **Research before coding**. The workbench
activates the task's normal plan, freezes the requirement and its accepted
context, and starts research instead of Pi. An existing Task with an activated
plan and no Run also offers **Start research before coding**. The initial
catalog is limited to versioned public Chora documents; task-private materials
remain frozen in the Go execution snapshot and are not uploaded to research.
Full RAG, private-source tools and concurrent research workers are not enabled.

Review the summary and source links, optionally add instructions, then choose
**Confirm and code** or **Reject proposal**. An unanswered question cannot be
confirmed for coding. Instructions remain within the task's original execution
contract. Use **Open coding result** to continue through checks and human review.
Reloading a waiting page preserves the same gate. Diagnostics contain graph,
sample, budget and operation metadata; normal progress uses business states.

## Recovery and boundaries

Go owns the project and actor authorization, task snapshot, decision, Run,
Attempt, workspace and delivery. Python owns graph progression and checkpoints.
One Task belongs to one scheduler. Legacy delegation and automatic retries do
not advance a research-managed Task. M1 allows one research Worker; it has no
automatic multi-worker leases or high-availability claim.

SQLite commits a command before delivery. Python stores its inbox and receipt
in PG. Stable `operation_id` and payload `input_digest` survive interpreter
changes and delivery attempts. Worker identity, delivery attempt and graph owner
epoch are recorded separately; none changes the operation identity. Graph operations retain their M0 journals and
checkpoint fencing. The bridge checks the frozen model, prompt, tool, catalog,
graph, sample and deployment versions before new graph work. Restore the
original interpreter/configuration to resume an old paused execution; changing
defaults is not a graph migration.

The gate binds the owner, project, content digest and displayed version. An
identical repeated decision returns the same result; conflicting decisions and
stale pages are rejected. The confirmed research and any instructions enter Pi
as a separate, untrusted context field, without rewriting the snapshot or
expanding permissions. Only the fixed public catalog is available to research;
there is no protected-source grant API to reuse for another project or epoch.

Run creation, its operation receipt and workflow mapping commit together in
SQLite. If the response is lost, the same operation queries and reuses that Run.
Before launching Pi, PG records that same Go operation ID, input digest and Run
receipt. If this acknowledgement is lost, reconciliation replays the binding
and preserves the already-created Run.
PG and SQLite have no shared transaction. Neither the bridge nor checkpoints
promise exactly-once external model/tool effects.

**Stop research and coding** commits stop intent before worker or process I/O.
A late research receipt cannot clear it. A Stop arriving before research
submission leaves a persistent PG tombstone. A Run with no runtime session can
be cancelled without launch; a process with an uncertain identity remains
unresolved. A launch already accepted before Stop may still start and must be
cancelled and reconciled through the original supervisor.

Transport failures and uncertain Pi outcomes show **Needs reconciliation** and
hold automatic dispatch. After inspecting the existing operation/Run, choose
**Reconcile existing operation**. This redelivers the same command or queries the
same Run; it does not reset usage, deadline, retries, model version or authority.
An unknown model/tool reservation remains unresolved. Restore original runtime
evidence through the existing Run recovery flow rather than creating a new Run.
The bridge subprocess has a 130-second bound. A timeout can leave graph work
uncertain and requires reconciliation rather than a fresh submission.

Workflow events are durable and monotonically numbered in SQLite. The SSE
endpoint honors `Last-Event-ID` (or `after`) and reads all remaining pages before
waiting. Browser reconnects can recover terminal events; older projections
cannot overwrite a newer visible state.

## Verification

Run the repository's normal checks and the locked Python test suite. Focused
coverage includes `go test ./internal/localweb -run '^TestWorkflow'` and
`python -m pytest services/workflow/tests/test_product.py`. The latter needs
`CHORA_WORKFLOW_TEST_DSN` pointing to a disposable UTF-8 PostgreSQL database.
Each Python case creates and drops its own schema.

The optional cross-language Go journey uses `CHOR50_TEST_PYTHON` and
`CHOR50_TEST_DSN`; initialize a dedicated schema with `init-db` first. Each bridge
call starts a fresh interpreter. With a built web client and Chromium installed,
`CHOR50_BROWSER=1 go test ./internal/localweb -run '^TestWorkflowBrowserJourney$'`
checks the product entry, gate refresh, edited confirmation, review, narrow
viewport and browser history against a disposable Go server.

These journeys use synthetic research responses and a Pi supervisor fixture.
They do not establish live-model quality, actual Pi code changes, human business
acceptance, multi-worker takeover, or production operation. Live integration
requires a separately authorized model budget, existing Pi access and an actual
human delivery decision.
