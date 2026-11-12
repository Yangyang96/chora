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

The adapter uses OpenAI Chat Completions with tool calls and disables SDK retries.
Endpoint compatibility and the chosen model must be tested; the adapter's presence
does not prove access. Do not print or commit credential values. State freezes the
model ID, an endpoint digest, prompt and tool schema digests, graph/schema versions,
and a pinned public Chora corpus revision. Changing them prevents continuation.

Initialize the dedicated database once while workers are stopped:

```sh
uv run --project services/workflow --frozen python -m chora_workflow init-db
uv run --project services/workflow --frozen python -m chora_workflow submit \
  --thread 00000000-0000-4000-8000-000000000001 \
  --question 'What is Chora and how does its project workflow work?'
uv run --project services/workflow --frozen python -m chora_workflow inspect \
  --thread 00000000-0000-4000-8000-000000000001
```

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
(default two, no SDK retries). Authentication, malformed parameters/results,
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
