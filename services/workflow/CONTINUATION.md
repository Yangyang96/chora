# Explicit acceptance continuation

This single-host compatibility operation supports one authorized continuation
from the frozen v1 acceptance policy to v2. The operator supplies the approval
and completed independent check references. Their authenticity and scope are
operator attestations. All participants must share the original durable SQLite
file and use the reviewed executable. Stop writers before registration.

## Read and prepare

`inspect-batch` reads v1 and v2 ledgers through a read-only connection. It prints
all original fields, requests and a `history_digest` over the logical
`{"batch": ..., "requests": ...}` snapshot using the protocol's canonical JSON
SHA256. A filesystem checksum identifies the evidence file separately. Inspection
and authorization preparation leave the request clock unchanged and need no
model credential or PostgreSQL connection.

```sh
uv run --project services/workflow --frozen python -m chora_workflow inspect-batch \
  --batch-file "$CHORA_WORKFLOW_BATCH_LEDGER" > "$CONTINUATION_ROOT/before.json"
```

Create `authorization.json` outside the checkout using the inspected fields.
The following Python fragment shows the exact JSON shape. Replace the variables
with the existing approval, stop evidence, completed independent checks, candidate
commit and workflow identifiers. Run it with the locked service environment.

```python
import json
from pathlib import Path
from chora_workflow.batch import LEGACY_POLICY, POLICY

snapshot = json.loads(Path(before_file).read_text())
authorization = {
    "version": 1,
    "batch_id": snapshot["batch"]["batch_id"],
    "authorization_ref": user_approval_reference,
    "expected_history_digest": snapshot["history_digest"],
    "expected_requests": len(snapshot["requests"]),
    "expected_tokens": sum(
        json.loads(row["usage"])["total_tokens"] for row in snapshot["requests"]
    ),
    "expected_policy": LEGACY_POLICY,
    "new_policy": POLICY,
    "candidate_revision": reviewed_candidate_commit,
    "previous_workflow_id": original_workflow_uuid,
    "external_stop": {
        "reason": "input_admission",
        "occurred_at": original_external_stop_unix_seconds,
        "evidence_ref": original_stop_evidence_reference,
    },
    "quality_check_ref": independent_quality_reference,
    "review_check_ref": independent_review_reference,
}
Path(authorization_file).write_text(json.dumps(authorization))
```

Every request must have a known response and consistent prompt/completion/total
usage within its original bounds and deadline. Pending, missing, uncertain,
cancelled or late results and other durable stops require reconciliation before
this interface can proceed. The explicit external stop must describe the prior
input-admission refusal and follow the last recorded request observation. Retain
its external evidence. Verify any later external stop before further execution.
The original window must have expired and cumulative allowance must remain.

## Register and execute

After the independently reviewed input proof, continuation behavior and affected
PG checks pass without a stage blocker, the designated acceptance operator runs
registration against the original file.

```sh
uv run --project services/workflow --frozen python -m chora_workflow register-continuation \
  --batch-file "$CHORA_WORKFLOW_BATCH_LEDGER" \
  --authorization-file "$CONTINUATION_ROOT/authorization.json" \
  > "$CONTINUATION_ROOT/registered.json"
```

Registration validates the expected batch, complete history digest, request and
token totals, exact old/new policies, previous workflow, candidate and approval references. A single SQLite transaction appends the authorization,
original snapshot and registration time in a `continuation` table. Existing
`batch.policy`, original first reservation, every request, usage, outcome and
old deadline retain their values. The v2 constructor obtains its effective
policy from this explicit audit record. No direct SQL replacement is needed.
The same authorization is idempotent across restarts and later usage. A changed
authorization or second continuation refuses.

Registration leaves activation time, deadline and first continuation request
null. Offline waiting and inspect commands consume no window. The operator then
uses the authorized secure launcher to supply the existing model configuration,
same ledger, dedicated PG database, exact candidate and linked fresh workflow.

```sh
uv run --project services/workflow --frozen python -m chora_workflow submit \
  --thread "$CONTINUATION_WORKFLOW_ID" --code-revision "$CANDIDATE_REVISION" \
  --sample-type real_model_acceptance --question "$PUBLIC_ACCEPTANCE_QUESTION"
```

Use a fresh UUID and the registered `candidate_revision` on every submit/resume.
Worker validates the candidate and previous graph before writing PG state. The
previous graph must be terminal with a known `token_budget` input-admission
failure below its workflow token cap, the same v1 batch identity, no persisted
cancellation, no unknown usage,
and no unresolved PG operations or pending checkpoint writes. The previous graph
retains its frozen identity and is available through read-only inspect/events.
Executing it through the continuation refuses.

Each fresh workflow runs the normal public catalog query, evidence reading,
cited report and review gate with the v2 model identity. Additional fresh workflows
can exercise separate acceptance/rejection cases in the same cumulative batch
and single window. Each new reservation also records its workflow UUID in
`continuation_requests` in the reservation transaction. The adapter binds workflow
and candidate context on each reservation. An unbound SDK invocation refuses.
Standalone ledger/SDK primitives rely on their caller for the PG predecessor
check. The CLI submit/resume commands perform that check through Worker. Retain
the previous and new workflow IDs and shared batch ID with acceptance evidence.

The first admissible reservation, unique request ID, activation timestamp and
new deadline commit in the same `BEGIN IMMEDIATE` SQLite transaction. The deadline
is that reservation time plus 1200 seconds. A failed preflight leaves activation
null. Concurrent processes serialize. A crash before commit rolls everything
back. A crash after commit retains the reservation and window and blocks further
requests as unknown until reconciled. Inspection, restarts and repeated
registration preserve that deadline. Expiry refuses further calls and this
interface provides no second renewal.

The original first timestamp and request deadlines remain in place. The original
input-admission stop is preserved in the audit snapshot and explicit external
stop record. When necessary its active `input_admission` halt is cleared only
in the atomic first-reservation transaction. Later durable stops stay effective.
All historical and new requests share the original 20-request/100000-token caps,
2048 output cap and zero SDK/graph retry policy. Each reservation charges complete
input bound plus maximum output against remaining allowance. Actual usage replaces
the reservation estimate when known. Unknown usage/outcome, provider bound
violations and pending requests halt progression.

Resume a returned human gate with the existing decision-file interface and the
same code revision and that gate's workflow ID. A fresh process can accept or reject, retry
the same decision, and observe explicit conflict or stale-decision refusal.
Retain the ledger and both PG workflows after the window ends.

## Failure reconciliation

SQLite reservations, PG operation reservations/receipts and external provider or
operator evidence commit independently. Prepare PG and the public input before
submitting. A PG submission failure before a model reservation leaves the window
unstarted. A SQLite activation followed by a crash before dispatch leaves a
pending counted request and a running window. A response with a missing PG receipt
can coexist with known SQLite usage. Preserve both stores and external evidence,
inspect IDs and timestamps, and stop for reconciliation using provider evidence.
PG's operation journal blocks blind replay of an unresolved action.

The interface never resolves pending or unknown requests. An original PG workflow
and its operations retain their original frozen identity. The linked workflow
has new operation identities and shares the cumulative acceptance ledger. A fresh
workflow preserves request allowance and known usage. The read-only predecessor
check and later dispatch span independent PG and SQLite observations. Keep the original graph inactive throughout continuation
and recheck external stop evidence when resuming. Keep only one advancing
acceptance operator and stop on newly observed external refusal,
unknown outcome, expiry or exhausted allowance. There is no cross-store atomic
commit or multi-host budget coordinator.
