# Optional Cernora completed-record adapter

**English** | [简体中文](cernora.zh-CN.md)

Chora provides an opt-in file adapter at `tools/cernora/chora_adapter.py` for
Cernora's Preview `agent-run-export/v1` inspector. It reads one sanitized,
already terminal record and writes one deterministic inspection export. It has
no access to the Chora database, execution registry, credentials or Review APIs.
Cernora remains an external offline evaluator.

This is the first file integration seam, not automatic capture of existing
Workbench runs. The current UI/event API retains display summaries rather than
complete argv/stdout evidence. Those responses are not accepted as completed
records. An external capture harness must supply the actual facts in the opt-in
record below. Do not reconstruct commands from display text, invent stdout,
extract claims from prose or substitute expected answers for observed claims.

## Environment and three-path acceptance

Use an external Python 3.12 or 3.13 environment with Cernora and its dependencies
installed. The environment must include the Preview inspection API described in
[Cernora's inspection guide](https://github.com/Yangyang96/cernora/blob/main/docs/public/agent-run-inspection.md).
The package version `0.1.4` alone does not guarantee this unreleased API is present.
Chora does not install or activate it automatically. No Python dependency is added
to the Workbench runtime.

From the Chora checkout:

```sh
CHORA_CERNORA_PYTHON=/external/cernora-env/bin/python \
  npm run test:e2e:cernora
```

The test creates only synthetic records in an external temporary directory, runs
the adapter and the actual Cernora CLI in subprocesses, and repeats each
inspection three times to check identical output bytes:

| Demonstration | Recorded facts | Cernora observation | Inspector exit |
| --- | --- | --- | --- |
| pass | Tool exit 0, stdout and structured claim `ready`; independent reference `ready` | `evidence_available`, claim accuracy 1.0 | 0 |
| fail | Tool exit 0, stdout and structured claim `busy`; reference `ready` | Scored claim mismatch, accuracy 0.0 | 1 |
| missing evidence | Tool call without a recorded result; no invented output or claim | `inconclusive`, one missing result | 3 |

Here pass/fail describe **inspection observations**, not a task-quality gate or
human acceptance. Even an inspector exit 0 cannot accept a Chora Task. Full
EvidenceBundle v2 import/evaluation needs an explicitly frozen Profile/Case and
its authority; this adapter does not relabel inspection JSON as a bundle or
implement that separate path. No changes to Run, verification or Review state
are made. These synthetic checks do not qualify real Pi capture or isolation.

## Completed-record contract

`chora.cernora-completed-record/v1` is a new opt-in interchange contract for the
capture harness. It is not the shape returned by `/api/runs/{runID}/events`.
All members below are required. Unknown fields, duplicate JSON keys, non-finite
numbers, invalid UTF-8, nonterminal records and malformed events are rejected.

| Member | Meaning |
| --- | --- |
| `schemaVersion` | Exactly `chora.cernora-completed-record/v1` |
| `completed` | Exactly JSON `true`; capture has finished, without implying success |
| `runId`, `taskId`, `caseId` | Recorded identities; use an attempt-qualified `runId` to distinguish retries and an explicitly selected `caseId` |
| `conditions` | Cernora fields `agent`, `model`, `prompt_version`, `budget`, `toolset_version`; actual recorded effective configuration |
| `userTask`, `finalAnswer` | Recorded task and final public answer; never private reasoning |
| `events` | Observed order, contiguous `sequence` starting at 1, nondecreasing `timestampMs`, `type` and `payload` |
| `claims` | Actual structured final claims: `path`, JSON `value`, `evidence_ids`; use `[]` when absent |

`conditions.budget` is the measured/configured total token budget or explicit
`null` when unrecorded, never a timeout or invented zero. Other condition fields
must be nonempty recorded version identifiers; absent required identity/configuration
must be fixed in capture rather than inferred by the adapter. Event timestamps
are harness-recorded milliseconds; they do not establish billing or total wall time.

Event mapping preserves concurrent interleavings without sorting or pairing by
tool name:

| Source type | Cernora kind | Exact payload members |
| --- | --- | --- |
| `user`, `final` | Same | `text` |
| `assistant_message` | `assistant` | `text` |
| `tool_start` | `tool_call` | `invocation_id`, `tool`, `argv` |
| `tool_end` | `tool_result` | `invocation_id`, `exit_code`, `timed_out`, optional `evidence_id`, required `stdout` |
| `error` | Same | `code` |

Each call ID is unique; each result must follow its call. Missing results are
preserved. `stdout` is an observed UTF-8 string (including an observed empty
string) with a unique `evidence_id`, or explicit `null` with no evidence ID.
Only stdout evidence is projected; stderr, delivery/commit receipts and runtime
isolation are outside this contract. Unknown result exit status remains `null`;
the required `timed_out` flag must be observed, not inferred from missing output.
Exactly one final event must terminate the record and match `finalAnswer`.
Claims must cite existing outputs. Cernora validates the final wire semantics.

## Export and inspect

Keep sanitized records, references and generated output outside this checkout.
Redact before recording stdout and claims; the adapter computes SHA-256 over the
exact supplied UTF-8 bytes. It does not sanitize arbitrary free text, authenticate
the producer or establish the authority of a supplied reference.

```sh
/external/cernora-env/bin/python -B tools/cernora/chora_adapter.py \
  /external/completed-record.json /external/new-inspection.json
/external/cernora-env/bin/python -m cernora agent-run inspect \
  /external/new-inspection.json --reference /external/reference.json
```

The adapter never reads the reference. It rejects nonordinary/symlink source
files and inputs larger than 16 MiB. It publishes an owner-private file atomically
and refuses existing output files, directories and symlinks. Invalid input leaves
no output; diagnostics do not echo source values. Adapter exit 0 means export
succeeded, 2 means argument error, and 3 means invalid input or file publication
failure. Inspector exits are separate, as shown above. No Agent or command in an
event is executed, and no network service is contacted by the adapter.
