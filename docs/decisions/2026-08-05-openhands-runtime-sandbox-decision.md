# OpenHands Runtime/Sandbox Spike Decision

Date: 2026-08-05  
Status: **REJECT**

## Decision

Do not adopt OpenHands SDK, Agent Server, or its Docker workspace as Chora's
Runtime/Sandbox implementation.

The pinned Agent Server can execute a disposable workspace and its Bash API can
complete a real frozen repository task. It also survived the bounded timeout,
cancel, cleanup, and abnormal-exit tests. Those facts are insufficient for
adoption: the real Agent conversation became stuck, enforceable isolation is a
Docker policy owned outside OpenHands, conversation lifecycle semantics do not
match Chora's supervisor contract, and OpenHands exposes no single terminal
result compatible with `chora.agent-result.v1`.

This was a throwaway logic prototype. No product runtime integration or
interface was added.

## Gate result

| Gate | Result | Evidence and reason |
| --- | --- | --- |
| Version and provenance | PASS | SDK/Agent Server `1.40.0`, source commit and arm64 image manifest are pinned. |
| License clarity | FAIL | Source is MIT, but the container's complete third-party license set is not established by that repository license. |
| Normal Runtime completion | PASS | The frozen task failed before execution and passed afterward through the Agent Server Runtime API. |
| Normal Agent completion | FAIL | The conversation emitted 8 actions and 8 observations, repeatedly viewed the same files, and ended `stuck`; no file changed. |
| Workspace isolation | CONDITIONAL | The measured internal Docker network blocked egress and only the attempt was mounted, but OpenHands does not enforce that policy itself. |
| Secret isolation | PASS IN SPIKE | Random server keys were supplied explicitly; the host sentinel and host secret were absent inside the container. Default unauthenticated server operation remains possible if keys are omitted. |
| Timeout / cancel / crash / cleanup | PASS AT CONTAINER LEVEL | Commands did not produce late markers and all labeled containers, networks, and attempt workspaces were removed. Conversation timeout is not equivalent to conversation cancellation. |
| Chora event/result contract | ADAPTER POSSIBLE, NOT NATIVE | Raw events can be normalized, but terminal status, final response, tests, artifacts, and failure reason must be synthesized by Chora. |
| Canonical product state | PASS ONLY WITH CHORA OWNERSHIP | The spike did not deploy OpenHands UI/DB and deleted server-local state with each container. Adoption would require Chora to remain authoritative. |
| Cost and operational fit | FAIL | Warm run: 143.01 s, 134 subprocess calls, six containers; image reports 5.54 GB in Docker CLI (1,545,656,599 inspect bytes), plus a separate control plane and model/runtime dependencies. |

Because adoption requires all gates to pass, the decision is **REJECT**.

## Recorded facts

The retained local-only run identifier is `20260804T211836Z`. Its raw evidence
remains in the maintainer workspace and is intentionally excluded from the
public source candidate.
The offline verifier passes even though the summary's adoption result is false:
it checks that every requested scenario and cleanup record exists, the Runtime
completion path passed, exactly one Chora terminal result was projected for
that path, and the product-repository guard remained unchanged.

The isolation run proves this concrete configuration:

- one bind mount: the disposable attempt at `/workspace`;
- an internal Docker network with outbound DNS/HTTPS blocked;
- no visibility of the Chora product repository;
- no host sentinel file or host-only environment secret;
- a random `OH_SESSION_API_KEYS_0` plus `OH_SECRET_KEY`;
- container user `openhands`, published API bound to `127.0.0.1` when networking
  is enabled.

It does not prove a native OpenHands sandbox policy. OpenHands forwards Docker
network and volume choices; its default workspace does not impose no-egress,
mount allowlists, CPU/memory limits, or a Chora-compatible orphan reaper. On
this Colima setup, an internal network also makes the published host control
port unreachable, so a production design would need an additional trusted
control-plane network or proxy.

The full official-source audit is in
[the research note](../research/2026-08-05-openhands-runtime-sandbox-spike-sources.md).

## Contract projection

No OpenHands object should become canonical Chora state. A hypothetical adapter
would have to apply this mapping:

| OpenHands / container fact | Chora contract | Rule |
| --- | --- | --- |
| Docker container ID | `RuntimeHandle` and `ProcessIdentity` | Opaque runtime identity; Chora owns launch tokens and reconciliation. |
| Conversation ID | `NormalizedEvent.ExternalSession` / attempt external reference | Binding only; never the authoritative attempt ID. |
| Event `kind` | `NormalizedEvent.Type` | Map action, observation, message, state, and error families; unknown kinds remain `runtime.event`. |
| Event timestamp | `NormalizedEvent.OccurredAt` | Parse when present; preserve missing/invalid values as adapter diagnostics. |
| Full event JSON | `RawJSON` | Redact first; treat as untrusted and never rely on it for product invariants. |
| Projection envelope | `NormalizedJSON` | Store stable Chora fields plus source kind; do not make OpenHands schema canonical. |
| Final response + execution status + test evidence | `TerminalResult` | Synthesize exactly once after drain/reconcile; OpenHands has no equivalent single document. |
| Changed workspace files | `Outputs` / `ArtifactCandidates` | Validate relative locators against the attempt workspace before projection. |
| Interrupt / container stop / kill | `ProcessSupervisor.Stop` | Chora must confirm death; HTTP timeout alone is not cancellation. |
| Container inspect and cleanup result | reconciliation / finalize evidence | Chora, not OpenHands conversation deletion, owns orphan cleanup. |

The prototype generated both raw and normalized event streams and one strict
`chora.agent-result.v1` document for each terminal path. This proves mapping is
possible; it also measures the compatibility layer Chora would have to own.

## Canonical-state boundary

Chora must remain authoritative for run, attempt, runtime session, launch token,
workspace identity/digest, security fingerprint, event offsets, terminal proof,
artifacts, and handoff. OpenHands conversation state, its persistence directory,
Agent Server logs, and any OpenHands UI/DB would be disposable diagnostics only.

The spike enforces that boundary operationally: every scenario gets a fresh
attempt, the server is started with `--rm`, its workspace and network are
deleted, no UI is present, and a before/after repository hash guard matches.

## Cost and integration assessment

- Delivery: a 5.54 GB displayed image, pinned per-platform manifests, Python
  SDK/runtime dependencies, a separately managed model endpoint, and Docker
  networking are required before Chora code begins.
- Security: authentication is opt-in, a session key is coarse-grained, the
  secrets endpoint has no product RBAC boundary, and the image contains
  passwordless `sudo`; Chora would still need its own network, mount, secret,
  resource, and cleanup policy.
- Lifecycle: create can race with automatic initial-message execution; API
  timeout does not cancel a conversation; deleting a conversation preserves a
  workspace; crash/orphan recovery remains Chora's responsibility.
- Contract: OpenHands emits useful events but no authoritative Chora terminal
  result. Adapter code must reconstruct completion and protect Chora invariants.
- Agent portability: the successful low-level Runtime task and failed Agent
  task demonstrate that model/tool protocol behavior is an additional support
  matrix, not a sandbox capability.

## Alternative direction

Keep Chora's existing `AgentAdapter` and `ProcessSupervisor` boundaries and
evaluate a smaller provider-neutral sandbox runner that directly implements
launch identity, mount/network policy, stream offsets, stop/reconcile/finalize,
and `chora.agent-result.v1` projection.

If a managed agent-oriented option deserves another spike, evaluate the OpenAI
Agents SDK Sandbox Agents direction against the same frozen task and gates. That
is a recommendation for a separate bounded experiment, not an implementation
or an adoption decision in this change.
