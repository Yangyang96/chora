# Runtime provider contract and migration

Status: accepted CHOR-10 contract with CHOR-11 provider infrastructure, part of CHOR-9.
The current product still selects Pi; a second production provider is not yet enabled. The legacy Codex Runtime remains disabled in product/Workbench
composition. English is authoritative.

## Decision

Extend the existing adapter and supervisor architecture with a small, explicit
provider registry. Retain the application-owned Run/Attempt lifecycle, stream
transactions, verification, review and delivery. Move provider-specific admission,
completion and recovery policy behind that registry before adding a second runtime.

| Approach | Benefit | Cost / decision |
| --- | --- | --- |
| Provider registration over existing ports | Preserves process identity, launch tokens, stream offsets and human gates; permits incremental Pi regression | Requires removing real Pi assumptions in app/domain/UI, not just adding a CLI wrapper. **Selected.** |
| Replace execution with a conversation-oriented broker / app-server everywhere | Interactive approvals, steering and long-lived conversations fit naturally | Replaces persistence and recovery semantics, expands trust surface and forces Pi migration; unnecessary for one bounded Attempt. Deferred. |

A runtime provider runs an agent (Pi, proposed Codex CLI). A model provider supplies
inference (for example Pi's `openai-codex` model provider). Neither is the execution
provider (`trusted_host` or Docker), and none is Cernora. Cernora remains the
[completed-record evaluation adapter](cernora.md), with no execution authority.

The first new runtime should be `codex-exec`, a new registration/package, never an
alias for the old `codex` adapter. Start with Local Connected only. Native Codex
sandbox settings are defense in depth; they do not qualify Chora Isolated Local.
Unsupported runtime/environment combinations fail before workspace launch, without
switching runtime, model, execution provider or credentials.

## Current-code evidence

Baseline: `dd159bc` (source symbols below remain more useful than line offsets).

| Source | Present contract and migration implication |
| --- | --- |
| [execution/ports.go](../internal/execution/ports.go), [types.go](../internal/execution/types.go), [target.go](../internal/execution/target.go) | `AgentAdapter` already prepares start/resume, decodes events/results and fingerprints. `ProcessSupervisor` owns start/stop/reconcile/read/drain/finalize. `ExecutionTarget` separates adapter from execution provider. `AdapterCapabilities` currently exposes only resume. |
| [Pi adapter](../internal/agent/pi/adapter.go), [path source](../internal/agent/pi/path_source.go), [isolated source](../internal/agent/pi/isolated_source.go) | Pi selects and validates source/model per bound profile. `BoundStartPreparer` binds invocation and fingerprint together. Only PATH Pi advertises explicit-session resume. Managed/isolated paths must retain their exact identity checks. |
| [runs.go](../internal/app/runs.go), [spec_coding.go](../internal/app/spec_coding.go) | Pi-specific profile/retry/session logic and registered SpecCoding admission prevent simply registering another adapter. SpecCoding preparation defaults to Pi; validation permits Pi/Fake only. |
| [streams.go](../internal/app/streams.go), [terminal.go](../internal/app/terminal.go) | Offset/CAS persistence is reusable. `agent_settled` closes held stdin. Policy projection, agent-reported checks and complete-final-message gating contain Pi-specific rules. Exit 0 alone is not sufficient. |
| [recovery.go](../internal/app/recovery.go), [runtime_sink.go](../internal/app/runtime_sink.go) | Startup continuity and trusted launch-bound notifications must survive extraction. PATH Pi has a special missing-route/liveness rule. A missing runtime is not proof that an old process exited. |
| [execution profile](../internal/domain/agent_execution_profile.go), [app profiles](../internal/app/agent_execution_profiles.go), [profile switch](../internal/app/profile_switch.go) | Profile binding contains Pi source/policy identity; non-Pi profiles are rejected. These cannot be relabeled as generic without preserving old values/digests. |
| [trusted host](../internal/trustedhost/supervisor.go), [Pi stream](../internal/trustedhost/pi_stream.go), [routing supervisor](../internal/localweb/supervisor.go) | Process ownership is reusable, but native argv, environment and stream assumptions are Pi-specific. New routing must not turn the supervisor into an arbitrary executable launcher. |
| [server.go](../internal/localweb/server.go), [execution API](../internal/localweb/agent_execution_api.go), [models API](../internal/localweb/models_api.go) | Composition, route restoration, readiness and start/resume handlers special-case Pi/Codex/Fake. Model discovery uses Pi. Runtime selection needs its own setting, separate from model selection. |
| [legacy Codex](../internal/agent/codexcli/adapter.go) | Adapter ID `codex`, pinned CLI 0.146.0, macOS sandbox wrapper and private gate/contract assumptions. `server.go` disables it for product or PATH Pi mode. Do not reuse its gate or unlock it. |
| [native capabilities](native-capabilities.md), [isolated execution](isolated-local.md) | Project Skills/MCP projection is Pi-specific. Docker qualification is Pi-specific. Neither transfers to a new provider by changing a label. |

## Provider contract v1

These are implementation requirements, not new exported Go APIs in this PR.
Use `internal/execution` for neutral value types/ports, `internal/agent` for
provider registration and provider packages for protocol-specific behavior.

The registry is explicit composition, not dynamic executable/plugin discovery.
Each registration owns the following operations:

| Operation | Input → output | Authority / failure rule |
| --- | --- | --- |
| Describe | Stable ID → display name, protocol version, supported environments and task kinds, capability descriptor | Description is static; it cannot claim this installation is ready. Unknown IDs fail. |
| Inspect | Explicit executable/config references + environment → redacted readiness, version, fingerprint, model-selection support | Bounded read-only probes. No install, login, model call, global config mutation or automatic alternative executable. |
| Bind | User selection + current probe + frozen task contract → immutable runtime binding | Validate capability/environment/model compatibility before new Attempt; transactional persistence. Failed binding creates no runnable Attempt. |
| Prepare | Bound Attempt + snapshot/contract + launch token + owned cwd → `StartPreparation` | Revalidate executable/config identity and produce exact argv/env/stdin. No spawn. Existing `BoundStartPreparer` is the seam. |
| Decode | `EventChunk` → bounded normalized events + consumed bytes | Deterministic framing; no DB writes, process signals or review authority. Preserve partial frames and consume only complete input. |
| Complete | Bound Attempt + persisted normalized events + drained terminal files → candidate `TerminalResult` or typed failure | New completion-policy port receives events because existing `DecodeTerminal(TerminalFiles)` alone cannot prove final-message/turn completeness. App still owns termination precedence and artifacts. |
| Recover | Bound capabilities + previous session/fingerprints + observed supervisor reconciliation → resume eligibility or fresh-start requirement | Pure policy, no signaling. App owns stopping, interruption, successor allocation and recovery-required state. |

The provider supplies an `AgentAdapter` and a **validated launch policy** to the
composition layer. That layer binds the policy to an appropriate supervisor and
registers the pair using `ExecutionTarget`. Supervisors remain responsible for
process identity, deadlines, process-tree cancellation, trusted exit, output
bounds and cleanup. Providers cannot assert that a process exited.

A descriptor must enumerate, rather than infer, these dimensions:

- Execution environments and task kinds (code, document, research/planning).
- Protocol and event schema version; streaming granularity (events vs text deltas).
- Resume mode; shutdown mode (`stdin_after_settled` or `exit_after_input`).
- Approval mode (`native_noninteractive`, `unsupported`, future `interactive`).
- Model mode (`native_default`, `explicit`, `catalog`); native resource projection.
- Check evidence mode (`none`, `agent_reported`, content-bound observation).

Capability availability is the intersection of descriptor, inspected installation,
execution environment and task contract. No absent flag means supported. Required
unsupported features return `unsupported_capability` before launch. The first
Codex slice must gate off automatic check evidence until equivalent observation
is implemented; it must not convert missing observations into passing checks.

### Configuration and persistence

Separate reusable execution configuration from the immutable task/Attempt binding.
A project has a default configuration reference; workflow/task templates may map
roles and agent identities to configuration references. These mappings belong to
the template, not to permanent agent records. Configuration references identify an
exact ID and positive revision; editing a default or configuration creates a new
revision. Runtime, model provider/model, and execution environment remain distinct.

Before materializing a task, resolve every declared executor using this precedence:
optional task override > template agent mapping > template role mapping > project
default. An explicit but unavailable reference is an error, not permission to try
a lower-priority choice. Missing mappings/defaults, unregistered runtimes, invalid
bindings and unsupported requirements fail the whole preflight before any runnable
Attempt exists. Ordinary task creation reuses configured defaults without a new
runtime checkbox; a one-task override is an optional advanced control.

Freeze the project revision, template identity/revision, resolved configuration
ID/revision, selection source and each executor's binding into the task contract.
Children inherit their named executor entry; they do not resolve current defaults
again. Unknown child roles require a new authorized contract. Later settings changes
apply only to new tasks, including when the same agent is assigned again. An explicit
successor-Attempt switch remains subject to the existing authorization flow.
Persist configuration changes with CAS and preserve the separately frozen model
selection. A successful configuration preflight does not grant execution authority
or replace installation/readiness, trust acknowledgement and launch identity checks.

The new binding includes runtime ID, protocol version, executable/source identity,
configuration fingerprint, execution provider, security policy fingerprint,
capability descriptor digest and model selection provenance. Use explicit fields
for `runtimeId`, `executionProvider` and `modelProvider`; do not overload current
model `provider`. Config references contain no credentials. Operational fingerprints
must not hash raw token values or expose private paths in public API diagnostics.

Migration is additive and schema-versioned. Existing Pi rows continue to resolve
through their exact legacy binding/digest representation. Do not recompute old
snapshot/charter/profile digests. Missing new fields mean legacy Pi only when the
old adapter is Pi; they never imply Pi for unknown IDs or historical Codex rows.
Preserve Fake test behavior and historical unsupported-runtime diagnostics. Keep
old sessions readable; never route old `codex` sessions to `codex-exec`.

New runtime changes require a fresh binding and Attempt; no cross-runtime resume.
A model change or fingerprint drift likewise invalidates resume. Return a specific
recovery action and require the existing restart authorization, not a hidden retry.

### Events, completion and cancellation

Keep existing stream offsets, launch proofs, CAS and transactional event append.
Define a documented neutral event payload at the extraction step: session-start
(opaque provider session reference), turn-start, progress, final message with an
explicit completeness marker, tool-start/tool-result with correlated call ID,
agent-settled, and sanitized error. Preserve current Pi stored events through a
compatibility decoder; do not rewrite historical events or manufacture Pi events
for other providers. Provider-native metadata is optional, bounded and redacted.

Map Codex `thread.started` to the bound session reference, `turn.started` to
turn-start, `item.*` to supported item/progress projections, final agent-message
items to the final candidate, and `turn.completed`/`turn.failed` to settlement or
failure. Reasoning/raw tool output is not persisted by default. Item order and
malformed/truncated/oversized frames need fixtures. Unknown informational items
may be dropped; unknown terminal shapes fail closed. Do not promise token deltas
from the CLI's event stream.

Success requires a complete current-Attempt final result, a successful terminal
protocol outcome and trusted normal process exit. The application collects scoped
immutable changes and independently evaluates required evidence. Agent tool success
is an agent claim, not independent verification. No response schema or runtime
success can accept a document, approve a patch, Apply, Commit, Push or Merge.

Trusted cancel/timeout, uncertain process identity, stream corruption and provider
failure take precedence over an apparent success. Persist cancellation intent,
stop the bound process group through the supervisor, drain bounded output and
finalize exactly once. If stop/liveness cannot be proved, retain recovery-required
state; do not claim cancelled or start a competing successor. EOF, network loss,
missing final message and exit 0 without completion are not success. Keep errors
such as missing installation, incompatible version, authentication unavailable,
authentication rejected during execution, unsupported capability, identity drift,
protocol invalid, process failure and cancellation distinct in readiness/results.

### Session, environment and authorization boundaries

Chora's runtime-session ID, Run ID, Attempt ID and launch token remain authoritative.
An upstream thread/session ID is opaque metadata scoped to a runtime, source,
working root, policy and owning Attempt lineage. Never use `--last`, names resolved
to a latest session, or a global session search. First Codex implementation has
`ResumeUnsupported`; store its thread ID only for provenance. Fresh retry feeds
frozen context/review feedback into a new execution and does not claim continuity.

Canonical task-owned cwd and allowed writable roots come from the frozen binding,
not provider output. Pass explicit argv without a shell. Build child environment
from an allowlist, excluding service/control-plane/Git credentials. Authentication
stays owned by the native runtime under the selected user-approved credential
scope; do not copy Pi auth to Codex or serialize credentials into SQLite/events.
Probe authentication separately from executable discovery. A local logged-in
status does not prove model access or permission to reuse that auth in another
home/environment.

Native config may load hooks, skills, MCP or other subprocesses. Inspection must
not activate those resources. At launch, a provider must bind the selected config
policy and declare what it loads; it cannot project Pi's project resources into
Codex. Start with explicit per-process restrictions and reject unqualified config
shapes. Do not modify global config or use bypass-sandbox/automatic-approval flags
to hide missing permissions. Local Connected retains its No Sandbox disclosure;
this design supplies no multi-human or adversarial host-read isolation guarantee.
A future interactive approval transport needs a separate human command contract;
it cannot treat an agent message as human authorization.

## Capability matrix

“Planned” below is a first implementation limit, not verified availability.

| Capability | Current PATH/local Pi | Current isolated Pi | Planned Codex exec | Future Claude Code |
| --- | --- | --- | --- | --- |
| Local Connected | Supported | Not this route | Initial scope | Unqualified |
| Docker / managed isolation | Not this route | Existing Pi qualification required | Unsupported | Unqualified |
| Stream transport | Pi RPC | Pi RPC in supervisor | JSONL CLI events | Must verify official protocol |
| Cancellation | Supervisor-owned | Supervisor/container-owned | Supervisor-owned; real tree-stop gate | Same contract required |
| Explicit conversation resume | PATH Pi only, fingerprint-bound | Unsupported; fresh Attempt | Unsupported initially, despite upstream CLI support | Unqualified |
| Model selection | Pi native discovery/binding | Qualified catalog | Explicit/native default with honest provenance; no invented catalog | Unqualified |
| Project Skills/MCP projection | Pi-specific management | Host resources not loaded | Unsupported | Separate native integration needed |
| Check evidence | Pi observations / agent claims with existing distinction | Existing isolated pipeline | Missing evidence remains unknown until qualified | Unqualified |
| Interactive permission requests in Chora | No generic broker | No generic broker | Unsupported | Requires separate design |
| Human acceptance / SCM authority | Chora only | Chora only | Chora only | Chora only |

Claude Code is only an extension point: registration, decoder, launch policy and
capability tests. This phase specifies no command, SDK, minimum version or support
claim for it.

## Provider infrastructure (CHOR-11)

The first infrastructure slice provides `execution.Provider` over the existing
adapter and supervisor ports. `agent.Registry` registers exact runtime IDs,
rejects duplicate/unknown registrations, validates explicit environment/task/feature
requirements and binds versioned selections. Static descriptions and read-only
fingerprint inspection are separate; a descriptor alone never establishes readiness.
`localweb.RuntimeProviderRegistration` pairs a provider with explicitly configured
supervisors. A new registration does not enable the legacy `codex` runtime.

Pi implements provider-owned completion, recovery and agent-check projection.
The application retains immutable contract/snapshot admission, current-Attempt
identity, stream transactions, termination precedence, cancellation, artifact
collection and human authority. Held stdin closes after persisted settlement only
when the provider declares `stdin_after_settled`. The trusted-host composition
explicitly selects Pi progress filtering; the supervisor does not infer it from
a runtime name.

Schema migration 50 adds `runtime_binding` to Charters and Attempts. Empty values
retain the existing legacy representation exactly. New `chora.runtime-binding.v1`
bindings freeze runtime/protocol, source/configuration/security identities,
execution provider, capability digest and model provenance. They are immutable,
reject reserved legacy IDs and currently admit only `trusted_host`. Preparation
checks registration/capabilities. Qualified inspection and atomic preparation
return independently derived source and security identities alongside the aggregate
configuration fingerprint; launch compares all three with the frozen binding and
rejects missing proofs. Providers must not echo requested identity metadata as
proof. Legacy Pi fingerprints retain their existing representation. Identity drift
creates no child process. Missing process ownership at restart keeps
the API readable in recovery_required without routing, signalling, finalizing or
starting a successor for the unknown process. Storage/configuration errors still
fail startup. Installed Pi workspace wrappers retain provider and check policies
while explicitly advertising unsupported resume. This migration does not rewrite
old Pi bindings, snapshots, events or digests.

`agent.Registry.ResolveExecution` implements the reusable configuration preflight
above. It returns a value snapshot with named executor lookup, exact revision
provenance and copied collections; no partial snapshot escapes on failure. Tests
cover consecutive task resolutions without new choices, changed defaults affecting
only new tasks, different runtimes for template roles, agent/override precedence,
child inheritance and invalid-reference/capability/descriptor rejection. This is
a tested configuration and inheritance contract, not a multi-agent scheduler.
CHOR-13 must persist these configuration/snapshot records and connect the product
entry to this resolver; this slice does not claim that new-runtime configuration
management is already available in the browser.

The composition and materialization seams accept an explicitly bound second
provider. Product settings, runtime selection/discovery UI and the concrete Codex
provider remain the subsequent CHOR-12/13 slices. Existing Pi settings and default
selection retain their behavior; no unknown selection falls back to Pi.

Conformance coverage uses a synthetic provider with its own event and completion
policy through persistent prepare/start/terminal, cancellation, startup recovery
and configuration-drift failure. This is infrastructure coverage, not real-model
qualification. The real Pi Local Connected browser gate remains separate.

## Codex interface verification and choice

Official OpenAI documentation describes `exec --json`, typed lifecycle/item events,
structured final output, saved authentication and explicit-session resume:
[non-interactive mode](https://learn.chatgpt.com/docs/non-interactive-mode).
The alternative [App Server](https://learn.chatgpt.com/docs/app-server) exposes
thread/turn methods, interruption and streaming for interactive integrations.
Choose CLI exec for the bounded first slice; reconsider App Server when Chora
requires steering or human approval round trips. A TypeScript SDK adds a process
bridge to this Go service without removing its supervisor requirements.

Read-only local inspection on 2026-10-27 found Codex CLI **0.155.1**. `--version`,
`exec --help`, `exec resume --help`, `login --help` and `login status` succeeded;
status reported an existing ChatGPT login. Help confirmed `--json`, stdin prompt,
`--cd`, `--sandbox`, `--output-schema`, `--output-last-message`,
`--ignore-user-config` and `--ephemeral`. This is an interface baseline, not a
minimum supported version or a successful model execution. The executable was
available outside this run's PATH: discovery must distinguish an explicit path
from missing installation. No credentials were read/copied, login performed,
model request made or global configuration changed.

Candidate launch shape for implementation qualification: resolved executable,
`exec --json --sandbox workspace-write --cd <owned-root> -`, with frozen prompt on
stdin. This is not yet an accepted launch policy. The implementation must verify
noninteractive approval behavior and configuration/resource loading on the exact
installed version, select explicit per-process controls, and fingerprint them.
Help availability alone cannot establish child cleanup, event framing, auth/model
access, sandbox behavior or absence of hooks. Do not use `--full-auto` (absent in
the inspected help), bypass flags or assume resume accepts start's flags unchanged.

## Ordered implementation and acceptance

1. **CHOR-11: neutral registry and Pi migration.** Add descriptors, readiness,
   selection/binding and completion/recovery ports under `internal/execution` and
   registry under `internal/agent`. Extend `internal/domain/agent_execution_profile.go`
   and SQLite migration/record mappings additively. Extract Pi policy from
   `internal/app/{runs,streams,terminal,recovery,spec_coding,profile_switch}.go` and
   `agent_execution_profiles.go`; keep task, snapshot and authority validation
   in app/domain. Adapt `internal/agent/pi` without changing its emitted historical
   schema. Extract exact launch policy from `internal/trustedhost/{supervisor,pi_stream}.go`.
   Replace localweb admission/routing branches with registry lookups. No Codex
   implementation should be needed for this stage's synthetic second-provider
   conformance test.
2. **CHOR-12: Codex provider.** New `internal/agent/codexexec` with discovery,
   bound preparation, decoder, terminal policy and exact supervisor registration.
   Leave `internal/agent/codexcli` disabled and unchanged. Fixtures cover success,
   failed turn, auth failure, duplicate/reordered/unknown items, truncated streams,
   nonzero exit, no final message, cancel race and unsupported resume. Use
   `internal/agent/conformance.go` plus provider-neutral lifecycle tests. Qualify
   a real bounded task, failure and process-tree stop using an explicitly selected
   user credential scope and disposable workspace. No paid resource provisioning.
3. **CHOR-13: product entry.** Add neutral runtime discovery/readiness API in
   `internal/localweb/runtime_providers.go` and composition in `server.go`;
   update `models_api.go`, `agent_execution_api.go`, `web/src/api.ts` and
   `web/src/ui/AgentExecutionProfile.tsx` (new runtime picker alongside environment
   and model) in reusable execution configuration management. Persist project defaults,
   template role/agent mappings and exact configuration revisions; wire preflight
   resolution and the complete task snapshot transaction before materialization.
   Keep optional per-task override in advanced controls, never a required checkbox.
   Show disabled combinations/recovery reasons. Browser acceptance must configure
   once, start consecutive tasks without choosing again, change the default and
   prove only new tasks use it. Verify child mapping inheritance and mixed-runtime
   role resolution without claiming a complete multi-agent scheduler.
   Update English and existing Chinese README, project-workflows, maintenance and
   native-capabilities docs; revise CONTRIBUTING only to distinguish the new
   qualified route from the still-disabled legacy runtime. Keep Pi default for old
   settings. Resume/retry and page refresh must display the frozen actual runtime.
4. **CHOR-14: independent boundary review.** Validate Pi regression and Codex
   actual execution, credential/path/process identity, no fallback, stale resume,
   missing auth, cancel/exit race and unchanged human/SCM authority. Review Docker
   restrictions and reusable-configuration acceptance explicitly: consecutive tasks
   without reselection, immutable old tasks after default changes and mixed-runtime
   role preflight. Codex Local Connected does not depend on reopening
   blocked real-Docker qualification; enabling Codex isolation would.

The CHOR-11 exit gate is structural and behavioral: registering a synthetic second
provider with supported capabilities must run through prepare/start/events/terminal,
stop and startup reconciliation **without editing core app lifecycle code**. Test
registry configuration and provider files may change; new runtime names or imports
must not appear in `internal/app` dispatch. Core policy remains shared and cannot
be supplied as a provider callback that bypasses review or scope validation.

Run focused Go tests for agent/execution/domain/store/app/supervisors/localweb,
then browser/API/persistence E2E for each implementation slice as applicable.
Include old database migration and old Pi session/history fixtures, fingerprint
and selection drift, no fallback, unsupported environments, replayed stream
notifications, model-provider/runtime separation, check evidence freshness and
human authority. Retain Pi's real acceptance gate. Missing real credentials or
model access blocks real Codex acceptance; mock success must be reported separately.
PRs can stack on this design branch before merge, but no stage implies acceptance
of later implementation or authorizes release.

This documentation-only phase checks source references, local CLI surface, links
and publication scope. It does not run or claim real-agent, Docker, browser E2E,
process-cancellation or migration acceptance. Those gates belong to the stages above.
