# OpenAI Agents SDK Sandbox Agents Spike Decision

Date: 2026-08-05  
Status: **REJECT**

## Decision

Do not enter a Chora product Adapter phase for OpenAI Agents SDK Sandbox Agents
`0.18.3`.

The rejection is based on reproducible self-hosted Docker Sandbox failures,
not on the unavailable model credential. A timed-out command continued running
after `ExecTimeoutError`, cancelling an SDK exec coroutine did not stop its
process, native Docker options could not express Chora's isolation/resource
policy, and SDK resume recreated a missing container unless a separate Docker
preflight preserved the dead/orphan distinction. Making these behaviors safe
would require Chora to rebuild the policy, process-supervisor, death-confirmation,
and reconciliation layer the candidate was meant to provide.

The Agent Quality Gate is independently **BLOCKED**: `OPENAI_API_KEY` was absent,
so the frozen task made zero model calls and actual model identity, quality,
usage, and cost remain unknown. This is neither a fabricated model failure nor
the reason for `REJECT`.

No product runtime path, product state, or existing execution interface was
changed. This decision does not authorize restoring the old Codex Runtime or
building another bespoke Runtime.

## Gate result

| Gate | Result | Evidence and consequence |
| --- | --- | --- |
| License and provenance | PASS | MIT source; package `0.18.3`, release commit, package wheel hash, version-pinned Python dependency set, image tag and arm64 digest are recorded. Transitive artifacts are not hash-locked. |
| Normal Sandbox commands | PASS | Command execution and attempt-only file I/O worked. Low-level file modification made the frozen tests pass, but this is not Agent Runtime evidence. |
| Native self-hosted isolation | FAIL | Native options omit required controls. The passing isolation run used a Spike-owned Docker proxy to add network, resource, rootfs, capability, security, label, and tmpfs policy. |
| Declared workspace / host / secret | PASS WITH EXTERNAL POLICY | Only the three manifest files were visible; product paths, sentinel, host secret, and host mounts were absent. |
| Network and resources | PASS WITH EXTERNAL POLICY | No network, 256 MiB, half CPU, 64 PIDs, read-only root, dropped capabilities, and no-new-privileges were confirmed by inspect/probes. |
| Frozen real-Agent task | BLOCKED | No `OPENAI_API_KEY`; zero model calls; requested `gpt-5.6-sol`, actual identity unknown. |
| Normal completion | PASS ONLY AT COMMAND LEVEL | Sandbox command/test completion worked; Agent completion was not observed. |
| Timeout | FAIL | SDK reported timeout but the process later wrote `/workspace/late-timeout`. |
| Cancel | FAIL NATIVELY | Cancelling the awaiting coroutine left the process alive. Deleting and confirming the entire container dead worked only through the Spike supervisor. |
| Abnormal exit / cleanup | PASS | Exit was detected and containers/workspaces were removed. |
| Orphan reconciliation | CONDITIONAL / ADAPTER-OWNED | Alive resume worked; missing-container resume recreated a container. A Docker preflight was required to distinguish dead from new. |
| Stable events | ADAPTER POSSIBLE | 51 raw Sandbox events were captured. SDK `seq` resets after wrapper resume, so the projection had to own a monotonic offset. |
| Unique terminal result | ADAPTER POSSIBLE | Exactly one parseable `chora.agent-result.v1` was emitted; the unrun Agent criterion is `UNKNOWN`. |
| Canonical product state | PASS BY SPIKE DISCIPLINE | SDK RunState/Session/Snapshot were not persisted; Chora would remain canonical. |
| Footprint and cost | PARTIAL PASS / BLOCKED END TO END | Image was 43,681,100 bytes and cached creates were 0.515-0.761 s, much smaller than OpenHands. Pull time, real Agent latency/tokens/dollars, and hosted-container cost are unmeasured. |
| No rebuilt Sandbox/Supervisor | FAIL | Passing the required lifecycle and isolation gates needs external Docker policy, stop confirmation, preflight registry/reaper, and event/result synthesis. |
| Existing Runtime interface unchanged | PASS FOR SPIKE | No Chora interface change was made or requested. The candidate still fails before interface expansion is considered. |

The ADOPT rule requires every gate to pass. Core Sandbox timeout, native cancel,
native policy, and supervisor/reconciliation ownership fail with local evidence;
therefore the correct overall result is **REJECT**, even though Agent quality is
separately blocked.

## Reproducible evidence

The authoritative local-only run identifier is `20260805T083654Z`. Its raw
evidence remains in the maintainer workspace and is intentionally excluded from
the public source candidate.
Its offline verifier parses every JSON/JSONL file, checks all scenario cleanup
records, validates contiguous adapter offsets, enforces exactly one strict
terminal-result shape, accepts credentialed `PASS`/`FAIL` as well as
credential-preflight `BLOCKED`, and confirms the sanitized repository guard and
absence of attempt or container residue.
Authoritative retained-evidence verification uses
`--require-post-validation`, which fails closed unless this `REJECT` decision
and `adapter_phase_authorized: false` are both present. Fresh runner finalization
uses core verification before that retained-run record exists.

The dependency file pins exact versions and the verifier checks the installed
set, but installation does not use `pip --require-hashes`; the environment is
not claimed to be artifact-hash-locked or cryptographically reproducible. The
sanitized repository guard retains no source diff or per-file path, while raw
operational evidence may retain local absolute paths. In summary records,
`probe_completed` and `cleanup_pass` describe evidence collection and cleanup;
they do not override the explicit `native_process_cancel_pass: false` or the
recorded SDK recreation of a missing container.

Key failure evidence:

- timeout: timeout was reported, but the late marker existed;
- cancel: coroutine cancellation succeeded while its process still wrote a
  late marker;
- native policy: all inspected native hardening controls were absent;
- orphan reconciliation: the SDK recreated a missing container and therefore
  required adapter preflight;
- Agent gate: `BLOCKED`, model calls `0`, actual identity `null`.

The corresponding official-source and Beta audit is in the
[research note](../research/2026-08-05-openai-agents-sandbox-spike-sources.md).

## State and result projection

The Spike proved a narrow projection is mechanically possible:

| SDK/runtime fact | Chora representation | Ownership rule |
| --- | --- | --- |
| Session/container ID | external session/runtime handle | Disposable reference, never Attempt identity |
| Instrumentation event | normalized event with raw payload | Adapter redacts and assigns canonical monotonic offset |
| SDK `seq` | `source_seq` diagnostic | Never canonical ordering; resets on resume |
| Agent/test evidence | `checks` | Validate independently; no low-level-command-to-Agent promotion |
| Final projection | `chora.agent-result.v1` | Emit exactly once after lifecycle reconciliation |
| RunState/session/snapshot | no canonical mapping | SDK recovery state remains external and replaceable |

Mapping events and a terminal result is small in isolation. It does not offset
the much larger policy/lifecycle/reconciliation layer required by the failing
Sandbox gates.

## Cost comparison and next step

Compared with the retained OpenHands evidence, this candidate has a much
smaller base image and sub-second cached container creation. That is useful but
insufficient: no real Agent cost was observed, and the required Chora-owned
supervisor work removes the expected integration-cost advantage.

The bounded evaluation is complete. It is **not worth entering the product
Adapter stage** for this Beta release. Keep the Chora runtime disabled and
fail-closed, continue product work that does not depend on a real Agent runtime,
and reconsider only after an official release exposes enforceable policy plus
process-level cancel/timeout/reconciliation semantics that can pass the same
frozen gates without rebuilding the supervisor.
