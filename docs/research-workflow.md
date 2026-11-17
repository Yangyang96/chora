# Research before coding

[简体中文](research-workflow.zh-CN.md)

This optional development feature researches public Chora documentation, presents
a cited proposal, and waits for the task owner to confirm or add coding
instructions. Confirmation creates one Pi Run for the existing Task. Normal
checks, result review and explicit delivery continue through the workbench.
Accepting a research proposal does not accept its code or authorize publication.
The default Pi workflow remains available when research is not selected.

**Coding route is currently held closed.** Research-managed native Pi starts
are refused until a restricted provider transport is composed and independently
validated. A confirmed proposal may create its Run without launching Pi. The
offline lifecycle fixtures do not certify a bounded native Pi session. The
ordinary Pi path is unaffected.

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
Research and local cancellation are attempted independently, with separate
durable outcomes. A failed PG connection or an invalid research receipt cannot
skip local cancellation. The workflow is stopped only when both sides are
confirmed; uncertain outcomes remain unresolved.

A human result rejection can authorize a successor Attempt in the same Run.
Its preparation, workflow transition and authorization receipt commit together.
The original research gate, scope, model and budget remain frozen; automation
cannot use this route. A prepared, unstarted revision can be recovered only with
the same instructions, predecessor and fresh human rejection, with no runtime
session. Stop and expired or unknown provider authority forbid revision starts.

## Authorization and dispatch budgets

A new research ledger can carry an immutable v3 authorization smaller than the
legacy policy. It binds the authorization reference, request and total-token
ceilings, per-request and total-output ceilings, pinned input-envelope identity,
and an absolute deadline starting at authorization. Restart does not renew it.
Legacy ledgers and their continuation records are unchanged. See
[worker setup](../services/workflow/README.md) for the explicit creation input;
neither this feature nor an unused balance grants spending permission.

The workflow's optional `piAuthorization` binds a non-secret explicit model
catalog/selection, runtime version and source identity, profile contract,
transport source digest, request/token/output ceilings and absolute deadline.
Task and Charter bindings must match. Each prepared launch records its actual
runtime fingerprint and final argument digest; managed constants are not used
as observations of a PATH runtime. Missing identity or transport is refused.

The Go dispatch boundary commits a reservation before a physical HTTP POST.
Reservations are shared by all Attempts of the Run and are never refunded.
Replay, pending or unknown outcomes cannot send another request. Only a known,
in-bound response permits the next reservation; malformed usage, excess output,
transport failure or deadline expiry holds the workflow unresolved. The narrow
transport currently supports only explicit text-only, nonthinking DeepSeek
chat-completions requests, with no SDK retries, redirects or model fallback.
Its conservative renderer envelope still needs independent validation. It is a
transport component, **not a connected native Pi budget guarantee**.

Native Pi preserves user configuration, extensions and tool permission policy.
Its current observation hooks cannot establish that every SDK retry or provider
switch passes through this boundary. Installing a provider override alone would
not establish that property. Composing a restricted coding route requires an
explicit runtime/profile contract which preserves the tool authorization
boundary; the default composition supplies no such guard. No native credentials
are copied into the dispatch ledger or sent to Pi by this component.

Trusted-host supervision persists an absolute deadline in its launch contract,
checks it before spawn and gate release, terminates the owned process group at
expiry, and checks expiry before exposing recovered handles. A changed deadline
fails manifest validation. SQLite expiry and local cancellation do not require
PG. Docker currently refuses this absolute-deadline route. While the supervisor
is down, there is no claim of an independently running watchdog. A provider may
still bill an already accepted request after cancellation; its reservation
remains consumed. Unproven process termination stays unresolved.

Spending authorization, confirmation of a specific proposed edit, and acceptance
or delivery of its result are three separate decisions. A live authorization
package needs the actual research and coding identities, admission results,
hard ceilings/deadlines, a price basis or monetary ceiling, existing credential
scope, and exact source/configuration bindings. Synthetic decisions prove none
of those human approvals.

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

## Restricted Pi source (offline boundary work)

`isolated_budgeted` is a separate, currently unavailable profile with source
`public_pi_budgeted_v1`. It does not convert frozen `trusted_local` or
`isolated_local` tasks. Selection is disabled in the UI and the API refuses a
start or model discovery for it. The original profiles retain their contracts.

The added components implement the following boundaries:

- A v2 authorization describes the exact non-secret installed closure, model
  catalog, transport, renderer/billing proof identities and frozen configuration.
  Missing fields, partial v2 records and old bindings cannot supply defaults.
  SQLite migration 51 retains history, adds immutable launch bindings and
  irreversible channel revocation, and rejects opening with an older binary.
- Go owns the launch/channel/epoch and checks the current Run/Attempt and original
  authority in the reservation transaction. One request plus its full input and
  hard output reservation stays consumed, including on crash or unknown result.
  A duplicate logical ID returns an unreplayable original outcome and never
  retransmits. A disconnected or restarted channel cannot recover sending rights.
- A bounded, serial process channel separates `pi_rpc` from `dispatch_request` and
  `dispatch_result`. The provider shim maps text and local functions, submits the
  final `onPayload` replacement for Go admission, invokes `onResponse` before
  decoding, and ends with an error on callback/translation/abort failures. It has
  no built-in provider fallback, retry, arbitrary URL or credential lookup.
  Its numeric Pi cost fields are interface placeholders in fixtures, not billing
  evidence. The wrapper supplies a literal marker with no provider authority.
- The new Docker command plan uses network `none`, private PID/IPC namespaces,
  read-only code/masks, no published ports, credentials, host HOME, proxy or
  Docker socket. A separate read-only Linux PID1 guardian watches control EOF,
  a sequenced lease, wall deadline and `CLOCK_BOOTTIME` including suspend. Pi
  remains UID1000; PID1 uses container UID0 with all capabilities dropped. Host
  inspection must prove exact container death; daemon/VM loss stays unresolved.
  Image recipe, inspect validation and pipe attachment are components for the
  existing Runner, not an independently qualified new execution route.

Production dispatch returns `bound_unproven`. The old
`8192 + 8*ASCII bytes + 256*messages` formula is available only to an explicit
`chora_offline_fixture` test build. The production constructor accepts no injected
network transport or proof function. There is no installed trusted renderer or
billing strategy. Required permission extension semantics, actual image/install
closure, complete Pi execution and Docker guardian behavior are not certified by
interface/pipe/inspect fixtures; the production source stays unavailable.

Before live admission, a concrete authorization package must provide:

1. Exact provider/model/API/endpoint, installed Pi/Node/image/rootfs/helper/guardian
   closure, single-model catalog, final argument/config/mount/tool/required
   permission identities and current Task/Charter/Run/Attempt bindings.
2. An evidenced input bound for **every** allowed final payload, including hidden
   templates, Unicode, tool history/cache/rendering, and failure billing. A hard
   provider context ceiling may be conservatively reserved only if its billing
   contract covers those paths and it fits the existing 100000-token ceiling.
3. Request, total-token, per-call and total-output ceilings within the existing
   maximums (20 / 100000 / 2048 / total output <= total tokens), absolute
   authorization/deadline times spanning research and all Attempts (<=20 minutes),
   revocation and independent runtime qualification. No restart refreshes them.
4. Valid currency/minor-unit prices, worst applicable cached/input/output rates,
   all fixed/tool/error charges and evidence expiry. Round the integer expression
   `Σ(I*input_rate + O*output_rate + fixed_fee)` upward. Unknown coverage or integer
   overflow refuses admission. The proposed cost helper over-reserves both token
   ceilings; its arithmetic does not certify prices or provider behavior.
5. An explicit credential-scope reference authorizing only that endpoint/model,
   held by Go and never placed in Pi, manifests, receipts or public logs. Do not
   record secret values or secret-value hashes. None is supplied by these tests.

The actual renderer, prices/fee coverage, credential scope and runtime closure
are still unknown. Do not infer a spend permission from the proposed limits.
Expense permission, approval of the concrete user plan and final delivery remain
separate decisions.

## Stop intent and research quiescence

A committed Stop closes admission immediately, while local Pi cancellation runs
independently. Research returns `stopping` if its graph session is still active or
any operation has a missing/unknown receipt; saving a tombstone cannot prove
termination. Idle research returns explicit admission/quiescence evidence.

Reconciliation observes the **same** original Stop with `observe_stop`, without
creating a model, dispatching another call, refunding reservations, replacing its
immutable receipt or creating another Run. Go appends the bounded observation to
its event history. A late known result can establish quiescence; permanently
unknown operations remain unresolved across interpreter/application restarts.
This proves local research quiescence only, not cessation of provider generation
or billing after an accepted request.
