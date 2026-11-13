# Chora workflow M0

An opt-in Python research harness with a LangGraph model/tool loop, a PostgreSQL
operation journal, and a persisted review interrupt. It is not wired into Chora's
Go service or UI. Existing Pi execution and Run/Attempt gates remain unchanged.
M0 acceptance requires a real authorized model tool round trip in addition to
these deterministic tests; fixture results alone do not satisfy that requirement.

## Setup and checks

Use Python 3.12 or 3.13 and uv. The lock was resolved with uv 0.13.7; local tests
use Python 3.12 and PostgreSQL 17. Keep environments, databases and evidence
outside the repository. From the repository root:

```sh
export UV_PROJECT_ENVIRONMENT="${TMPDIR:-/tmp}/chora-workflow-venv"
export PYTHONDONTWRITEBYTECODE=1
uv sync --project services/workflow --frozen --python 3.12
export CHORA_WORKFLOW_TEST_DSN='postgresql://localhost/chora_workflow_test'
uv run --project services/workflow --frozen pytest -c services/workflow/pyproject.toml services/workflow/tests -q -p no:cacheprovider
uv run --project services/workflow --frozen ruff check --no-cache services/workflow
uv run --project services/workflow --frozen ruff format --check --no-cache services/workflow
```

The explicit pytest config and test path scope collection to this service:
`uv --project` selects the environment but does not change the working directory.

The test database must already exist and be disposable. Tests create and drop
only their own random schemas. Without `CHORA_WORKFLOW_TEST_DSN`, PG tests skip;
a skipped suite is not persistence acceptance. `test_process_restart...` starts
and exits independent Python processes, then resumes the same thread.
`test_receipt_replay...` exits a process after its model receipt is durable but
before the node returns, and proves that recovery uses the receipt.
`test_decision_recovery.py` kills processes after the decision journal commits and
after the real pending resume write commits, and before the terminal checkpoint
after node output commits. It checks accept/reject conflicts in
both directions, same-decision retries, version refusal, database failures and
controls that remain effective during recovery.

No Docker daemon is required when local PostgreSQL binaries are available.
For an isolated cluster, substitute external paths and an unused port:

```sh
initdb -D "$WORKFLOW_TEST_ROOT/data" -A trust --no-locale -E UTF8
postgres -D "$WORKFLOW_TEST_ROOT/data" -k "$WORKFLOW_TEST_ROOT/socket" -h '' -p 55439
# In another terminal, use the Unix socket; stop this test server after testing.
export CHORA_WORKFLOW_TEST_DSN="host=$WORKFLOW_TEST_ROOT/socket port=55439 dbname=postgres"
```

Create `WORKFLOW_TEST_ROOT` and its `socket` directory outside the checkout first.
This trust-authentication example is for an isolated local test socket only.
It is not a deployment configuration.

## Explicit real-model configuration

The harness reads only these configured references. It does not discover tokens
from Pi, Codex, browser sessions, or generic `OPENAI_API_KEY`:

| Variable | Meaning |
| --- | --- |
| `CHORA_WORKFLOW_DATABASE_URL` | Dedicated workflow PostgreSQL database/schema |
| `CHORA_WORKFLOW_BASE_URL` | Explicitly authorized OpenAI-compatible SDK endpoint |
| `CHORA_WORKFLOW_MODEL` | Explicit provider model identifier supporting tool calls |
| `CHORA_WORKFLOW_API_KEY` | Authorized SDK credential, supplied by the operator |
| `CHORA_WORKFLOW_BATCH_LEDGER` | Existing external acceptance ledger, required for the DeepSeek profile |

The adapter uses OpenAI Chat Completions with tool calls and disables SDK retries.
Endpoint compatibility and the chosen model must be tested; the adapter's presence
does not prove access. Do not print or commit credential values. State freezes the
model ID, an endpoint digest, prompt and tool schema digests, graph/schema versions,
and a pinned public Chora corpus revision. Changing them prevents continuation.

### DeepSeek acceptance profile

The official HTTPS `api.deepseek.com` endpoint selects the `deepseek.nonthinking.v1`
profile for `deepseek-v4-pro`. It sends `thinking.type=disabled`, `tool_choice=required`
and `max_tokens` capped at 2048. SDK and graph retries are zero. The default
OpenAI-compatible profile continues to use `max_completion_tokens` and the
workflow's bounded rate-limit retry policy. These request semantics, the output
cap and the acceptance batch identity are frozen in each new workflow. Existing
SDK checkpoints with an older identity require their matching executable or an
explicitly verified migration. Fixture identities retain their existing contract.
The parameter mapping follows the [DeepSeek Chat Completions API](https://api-docs.deepseek.com/api/create-chat-completion/).

Before any authorized batch, create its ledger once in an external directory.
All participating processes and threads must use that same file. Provision the
SDK key through the authorized secure launcher, then run the following commands
with public questions and an independently supplied disposable workflow database.

```sh
export CHORA_WORKFLOW_BASE_URL=https://api.deepseek.com
export CHORA_WORKFLOW_MODEL=deepseek-v4-pro
export CHORA_WORKFLOW_BATCH_LEDGER="$WORKFLOW_ACCEPTANCE_ROOT/batch.sqlite"
uv run --project services/workflow --frozen python -m chora_workflow init-batch \
  --batch-file "$CHORA_WORKFLOW_BATCH_LEDGER"
# Use the same environment and batch file for every submit/resume process.
uv run --project services/workflow --frozen python -m chora_workflow inspect-batch \
  --batch-file "$CHORA_WORKFLOW_BATCH_LEDGER"
```

The fixed batch policy allows 20 model requests, 100000 cumulative tokens, at most
2048 output tokens per request and 1200 seconds from the first durable request
reservation. Initialization leaves the clock unstarted and refuses an existing
file. Each request commits its ID, payload digest, timestamp, input bound and
remaining allowances before network dispatch, then records actual prompt,
completion and total usage. The ledger uses local SQLite transactions and permits
one outstanding request across participating processes. Keep it on a durable
local filesystem with reliable SQLite locking.

Preflight bounds this harness's text/function input by twice its ASCII-escaped
JSON byte length plus 4096 tokens for each request/message/tool framing element.
The bound includes the entire message history and tool definitions. It is based
on the published V4 encoding at revision `0e1a0e5e52aea73055f50fef6f2423db370265b6`
and restricts historical calls to the harness's four-call/three-argument surface.
Unsupported inputs and input-bound-plus-output reservations exceeding remaining
tokens are refused before sending. A provider response exceeding either bound
is recorded with its actual usage and halts the batch for reconciliation.
See the [fixed encoding source](https://huggingface.co/deepseek-ai/DeepSeek-V4-Pro/blob/0e1a0e5e52aea73055f50fef6f2423db370265b6/encoding/encoding_dsv4.py).

Failed requests consume a request slot. Missing or inconsistent usage remains
null and halts the batch. A request left pending after a crash blocks further
requests, including on another thread or process. Retain the ledger for operator
reconciliation using provider evidence. Restarting with a fresh file requires a
new authorized batch. The local ledger and frozen identity enforce continuity
among trusted invocations using that file. Preserve the authorized batch across
all participants, including independent validation processes.

Initialize the dedicated database once while workers are stopped:

```sh
uv run --project services/workflow --frozen python -m chora_workflow init-db
uv run --project services/workflow --frozen python -m chora_workflow submit \
  --thread 00000000-0000-4000-8000-000000000001 \
  --question 'What is Chora and how does its project workflow work?'
uv run --project services/workflow --frozen python -m chora_workflow inspect \
  --thread 00000000-0000-4000-8000-000000000001
```

For an existing M0 database, run `init-db` again with workers stopped to add the
decision journal and nullable collection metadata columns. See [upgrade and recovery semantics](PROTOCOL.md) before resuming
checkpoints created by an older executable.

At `waiting`, the command exits and closes its connections. Copy the returned
`gate_id` and `input_digest` into an external JSON file with `decision: "accept"`
or `"reject"`. A fresh process can resume it:

```sh
uv run --project services/workflow --frozen python -m chora_workflow resume \
  --thread 00000000-0000-4000-8000-000000000001 --decision-file "$DECISION_FILE"
```

`resume` without a decision continues an interrupted non-gate execution. `inspect`,
`events` and `stop` require the database but no model credentials. CLI JSON includes
public evidence, reports and model calls: keep captured output outside the source
tree. Raw CLI failures are local diagnostics, not a public HTTP error contract.
The first durably recorded review decision wins. Retry the same gate/digest/decision
after an interrupted delivery; the opposite decision returns an explicit conflict.
`waiting` is a checkpoint projection and can briefly coexist with a committed
decision after a crash. Cancellation and budget limits still prevent acceptance.

## Execution observations

New events, operation reservations/receipts and review commands carry versioned
worker/epoch, time, sample and optional code-reference metadata. They preserve the
logical workflow/trace/operation identity and cumulative budgets across restart.
Receipt replay retains its original provenance; old records are never relabeled.
See [collection schema and upgrade rules](PROTOCOL.md#collection-schema-1).

For an authorized real-model acceptance run, pass `--sample-type real_model_acceptance`
on both submit and resume. Actual use is `real_usage`;
`deterministic_injection`, `fixture`, `mock` and `load_test` stay separate. With no
explicit purpose, live adapters remain `unknown`. `--code-revision` accepts an
operator-supplied exact commit SHA; omitted references remain null/unknown.
These options classify records; they do not grant model access or establish
acceptance. New submissions freeze the sample declaration. Existing historical
records with missing metadata stay legacy/unknown. Stop all writers for `init-db`;
mixed versions/downgrades are unsupported.

`test_observation.py` covers fresh-process identities, immutable receipt replay,
retry links, sample/field refusal, legacy schema fixtures, transactional collection
failure and an actual PG receipt-write/owner-lock race. Its model and catalog are
deterministic fixtures, including injected faults, not real-model or two-worker
scheduler acceptance.

## Research behavior and limits

The Python graph asks the model to select tools, validates the entire batch (at
most four calls), executes only known tools, validates their results, and routes
back to the model. `query_public` performs bounded lexical search over two pinned
public Chora documents; `read_evidence` reads only their IDs. No model-supplied URL,
path, command or project identity can expand that catalog. `submit_report` validates
its schema and requires citations to previously read evidence (or an explicit
no-answer report). This is not full RAG or proof of semantic citation support.

Step count, absolute deadline, known token subtotal, unknown-usage flag and retry
count survive recovery. The initial deadline/trace are stored in the submission
before the first graph checkpoint. Only rate-limit rejections have bounded retries
(default two, with zero SDK retries and zero graph retries for the DeepSeek profile).
Authentication, malformed parameters/results,
timeout, uncertain outcome and unavailable tools terminate with distinct reasons.
Missing usage stops progression and is not reported as zero consumption. The token
limit stops future calls once reported usage reaches it; a call can exceed the
remaining total through prompt tokens. Output allowance is capped, but this is not
a provider-enforced hard currency or total-token spending cap.

Each read-only model/tool operation is reserved in PG before dispatch and gets a
durable receipt afterward. A completed receipt is replayed; a reservation without
a receipt stops as `outcome_unknown`, without blind replay. This may require human
reconciliation even when a request never reached the provider. An already reserved
call may finish after stop/takeover; late old-owner receipts are rejected. No Go/Pi
side effect is launched. SQLite/PG atomicity and external exactly-once execution
are not claimed. Database errors propagate and prevent further dispatch.

Keep one actively advancing worker per workflow in M0. Explicit invocation claims
a new epoch; there is no lease expiry, polling queue or automatic takeover policy.
The checkpoint adapter proves atomic writes for its supported surface, not complete
multi-worker orchestration. See the [protocol](PROTOCOL.md) and
[write-entry audit](CHECKPOINTER.md) before extending it.
