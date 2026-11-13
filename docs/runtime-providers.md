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
| Inspect | Explicit executable/config references + environment → redacted readiness, version, fingerprint, model-selection support | Bounded metadata probes use disposable directories; native bootstrap writes there are cleaned up. Selected-scope authentication probes require separate policy acceptance. No install, login or model call. |
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

Local interface inspection on 2026-10-27 found Codex CLI **0.155.1**. `--version`,
`exec --help`, `exec resume --help`, `login --help` and `login status` succeeded;
status reported an existing ChatGPT login. Help confirmed `--json`, stdin prompt,
`--cd`, `--sandbox`, `--output-schema`, `--output-last-message`,
`--ignore-user-config` and `--ephemeral`. This is an interface baseline, not a
minimum supported version or a successful model execution. The executable was
available outside this run's PATH: discovery must distinguish an explicit path
from missing installation. Chora did not read/copy credential contents, perform
login, request a model or change global configuration. This historical inspection
did not prove unchanged authentication directories: native metadata bootstrap
can write helper files before argument parsing. Current metadata isolation and
selected-scope authentication boundaries are described below.

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

## Protocol compatibility and installation qualification (CHOR-44 / CHOR-62)

`internal/execution/qualification.go` provides the shared evidence reducer and
optional per-provider memory cache. Providers define their own required capability
names and collect observations with a method and a public reference or diagnostic
code. `EvaluateQualification` returns `pending` for missing evidence, `verified`
only when all requirements are verified, and `rejected` if any requirement is
explicitly rejected. Duplicate or malformed observations are errors. A report is
evidence at its stated level; it does not grant execution or human acceptance.

`QualificationIdentity` includes runtime ID, observed version, protocol generation,
source identity, configuration identity and security identity. The provider must
hash the actual executable closure, including canonical/resolved paths and bytes;
launch arguments, selected configuration and adapter policy revisions must also
participate. `QualificationCache.Store` evaluates and copies observations;
`Lookup` requires the same identity. An identity miss evicts the single entry.
There is no persisted proof import, production configuration override or model
request in the shared module. Runtime-specific freshness checks, authentication,
resource loading and process supervision remain the provider's responsibility.
Providers must revalidate live policy and source identity before binding and at
the supervisor's spawn gate. Cache hits never establish that mutable external
policy has remained unchanged.

Codex uses one `codex.exec.jsonl.v1` implementation, with source-reviewed release
anchors and live bounded `--version` / `exec --help` probes. The help probe checks
the actual required flags, then executable identity is rechecked to reject mixed
binary observations. Version strings alone cannot grant qualification. Unknown
versions start pending even with matching help. The host-owned protocol review
service below can update semantic evidence after bounded validation and signed
review, independently of native configuration-source safety. No downgrade or global CLI change is
required by the diagnostic. `codexexec.Inspect` exposes the report in
`Readiness.Qualification`; refused construction/registration exposes the same
report through `codexexec.QualificationError` (`errors.As`). Generic
`execution.Inspect` remains a fingerprint-only readiness helper.

| Codex observation | Protocol / argument evidence | Configuration-source qualification | Product launch |
| --- | --- | --- | --- |
| 0.155.1 | Same JSONL generation; reviewed upstream source and real no-model version/help probes | Rejected: `managed_policy_unverifiable` | Refused |
| 0.167.2 | Same JSONL generation; reviewed upstream source and real no-model version/help probes | Rejected: `managed_policy_unverifiable` | Refused |
| Unreviewed version, matching flags | Pending event/tool/terminal semantic evidence | Pending, not inferred from login or help | Refused |
| Unknown executable after trusted protocol review | Event/tool/terminal evidence verified for that exact identity and validity window | Still independently rejected: `managed_policy_unverifiable` | Refused |
| Reviewed shell fixture | Explicitly labelled signed fixture conformance; never native evidence | Cannot authorize a launch, even with a synthetic policy checker | Refused |
| Required flag absent | Rejected with `missing_flag:<flag>` | Not probed further | Refused |

These are evidence anchors, not a claim that every intermediate or future SemVer
release is supported. Source/fixture evidence covers framing, lifecycle and
completion. Shell fixtures cover EOF, cancellation and descendant death for both
release labels. Native no-model probes cover actual release binaries, argument
availability and policy refusal; they do not cover authenticated model execution
or authenticated cancellation. No current Codex version is production-qualified.
The older release can be obtained in an isolated directory from its official
[release](https://github.com/openai/codex/releases/tag/rust-v0.155.1), checking the
asset digest before execution; never replace the user's global installation.

Every new Codex binding/fingerprint freezes the observed concrete version,
executable identity, descriptor and policy; a later CLI replacement, even with the
same displayed version, cannot reuse that binding. The qualification-policy
revision changed, so old candidate bindings must be prepared again rather than
silently accepted under the new rules. Resume remains unsupported. Pi retains its
existing source/model and frozen-resume contracts; no historical Pi records are
rewritten. OpenCode and Hermes should use the shared types with their own required
capabilities and evidence, without copying Codex's source or policy assumptions.
Unverified resume, skills and MCP must stay unsupported. Claude's existing
candidate is retained without expanding its scope.

### Reviewing an unknown Codex protocol

`codexexec.NewProtocolReviews` creates a process-local registry with an explicit
copy of the embedding host's trusted Ed25519 reviewer public keys. The host and
its reviewer-key provisioning are the trust boundary. Task/agent configuration,
HTTP requests, CLI configuration and returned reports cannot provision keys or
register arbitrary `CapabilityEvidence`. The default `New`/`Inspect` path has no
review registry. An embedding host retains the registry and passes it to
`NewReviewed`, `InspectReviewed`, or
`localweb.NewCodexRuntimeProviderWithProtocolReviews`. These constructors still
apply the independent configuration-source gate; there is no policy override.

The executable review sequence is:

1. Call `reviews.Validate(ctx, config, fixture, validity)` with a validity greater
   than zero and at most 24 hours. It uses the same selected-source preflight,
   disposable version/help probes, exact identity and argument checks as normal
   construction. It runs a fixed public synthetic corpus through the real adapter
   decoder/completion path, covering additive metadata, required fields, unknown
   terminal states, explicit failures and tool pairing. It runs no native task or
   caller-provided transcript. Shell scripts are always marked as fixtures.
2. A trusted reviewer checks the exact executable provenance, protocol generation,
   corpus and immutable public source reference. For a native binary, establishing
   that its source implements these semantics is the reviewer's responsibility;
   the replay corpus alone cannot establish native behavior. Sign the 32 bytes
   returned by `ProtocolReviewMessage(request, reviewerID, sourceReference)` with
   the reviewer's separately held Ed25519 private key. The source reference and
   reviewer identity are part of the signed message.
3. Call `reviews.Register(request, signature)`. Only an outstanding, unexpired,
   unchanged request from that registry and a trusted signature are accepted.
   Registration consumes its random nonce exactly once. It updates event/tool/
   terminal evidence only; it does not establish native configuration safety,
   authentication, model permission or authenticated cancellation.
4. Call `InspectReviewed`/`NewReviewed` with the same registry and configuration.
   An unknown fixture can now report verified protocol evidence, explicitly
   `signed_fixture_conformance:not_native`, while still refusing
   `managed_policy_unverifiable`. A real unknown binary without a source review
   and trusted signature remains pending. No real unknown version is qualified
   by the fixture demonstration.

Approvals bind executable paths and bytes, observed version, protocol generation,
configuration, network/CA identity, policy revision and corpus digest. A changed
identity cannot consume the old approval. The approval digest is also frozen into
the task fingerprint. Expiration, reviewer revocation or replacement invalidates
an existing binding at fingerprint/preparation/spawn checks. `RevokeReviewer`
invalidates that reviewer's approvals. Restarting/recreating the registry loses
approvals and outstanding challenges: fresh validation/review is required, and
old signatures cannot be imported into a new registry. Approvals are intentionally
not persisted or exposed as a browser setting. This trusts the embedding host;
it does not defend against a malicious host, compromised reviewer key or a
same-user process replacing the host itself.

An operator can exercise this complete sequence without modifying product
settings using the review command (all paths below are operator-selected):

```sh
go run ./cmd/chora-codex-review \
  -executable "$CODEX_EXECUTABLE" -home "$EMPTY_HOME" -auth-home "$SELECTED_CODEX_HOME" \
  -reviewer-key "$REVIEWER_PUBLIC_KEY" -reviewer "$REVIEWER_ID" \
  -source-reference "$IMMUTABLE_PUBLIC_SOURCE_REFERENCE"
```

The command loads only a PKIX PEM public key, prints a JSON proposal containing
`Request`, `MessageHex`, `Reviewer`, `SourceReference` and the initial `Before`
readiness. After reviewing them, sign the decoded `MessageHex` bytes externally
(for example, Ed25519 `openssl pkeyutl -sign -rawin` with the reviewer's key).
Supply one stdin JSON object, `{"SignatureHex":"<hex signature>"}`. The same
process registers and consumes the signed request and prints the resulting
qualification/refusal. It exits without starting a native task or installing a
lasting product authorization. Use `-fixture` for synthetic executables; a
shebang executable is labelled fixture even without that flag. Do not sign a
fixture as evidence of native behavior or auto-sign proposals before review.

The reproducible no-key/no-model demonstration is
`go test -race ./cmd/chora-codex-review -run TestReviewCommand -v`; its key is
created solely for the disposable test session. It demonstrates a 0.168.0 shell
fixture moving from pending to signed protocol evidence and then independent
managed-policy refusal, not native 0.168.0 support.

## Codex implementation candidate (CHOR-12)

**Qualification suspended (CHOR-44):** public construction and registration now
fail closed with `configuration_unqualified: managed_policy_unverifiable`.
Reviewed CLI versions 0.155.1 and 0.167.2 load cloud bundles and macOS managed preferences even with
`--ignore-user-config`. A separate configuration snapshot cannot prove that a
later exec uses the same policy. No qualified native snapshot-to-launch interface
is currently integrated, so Chora cannot issue a binding or launch Codex. There
is no configuration or environment escape hatch. Pi and Claude are unchanged.
The implementation details below describe the retained candidate, not an enabled
or currently qualified runtime. The opt-in real test also fails at this gate;
fixture coverage and historical native results do not establish current readiness.

The system rejection list includes `/etc/codex/managed_config.toml`. File absence
checks do not detect cloud or MDM policy. Restoring qualification requires a
native, side-effect-free effective-policy proof covering these sources (including
additional developer instructions), with consistency enforced at exec startup.
See the pinned [exec bootstrap](https://github.com/openai/codex/blob/be2951ea34f0d295ed0becf97079f92fa5f6950e/codex-rs/exec/src/lib.rs)
and [configuration loader](https://github.com/openai/codex/blob/be2951ea34f0d295ed0becf97079f92fa5f6950e/codex-rs/config/src/loader/mod.rs).
The newer [exec bootstrap](https://github.com/openai/codex/blob/ce21072c90d32c40af7a55b60b2fb35c84a43a6a/codex-rs/exec/src/lib.rs)
still loads the cloud bundle and starts an in-process app-server. Its
[configuration loader](https://github.com/openai/codex/blob/ce21072c90d32c40af7a55b60b2fb35c84a43a6a/codex-rs/config/src/loader/mod.rs)
applies managed/cloud sources independently of `ignore_user_config`. A
`config/read` response or a cloud-binding field alone does not prove pre-effect
consumption of the same effective Config by a later `thread/start` or exec.

`internal/agent/codexexec` implements the separate `codex-exec` provider. An
embedding caller explicitly supplies `codexexec.Config` and calls
`localweb.NewCodexRuntimeProvider`, then passes the returned registration in
`Options.RuntimeProviders`. Existing Pi defaults and the disabled historical
`codex` adapter do not change. `Adapter.Binding` derives the versioned binding
from the inspected executable and effective launch policy; that binding can be
referenced by `Registry.ResolveExecution`. This is not the CHOR-13 settings UI
or persisted, independently selectable model configuration.

The candidate uses the protocol/capability qualification described below, an
explicit existing ChatGPT `CODEX_HOME`, a dedicated empty home, and an optional
explicit model. Other authentication mechanisms remain unsupported. Chora
runs bounded version and exec-help probes in disposable HOME/CODEX_HOME/cwd
directories with a controlled environment. Native bootstrap writes in those
directories are permitted and cleaned up on success, failure, cancellation and
timeout. A temporary-directory base within either selected scope is rejected
before allocation. It rejects prohibited selected-scope configuration sources
before starting metadata. Selected-scope login-status probes can perform native bootstrap
writes and require configuration-policy acceptance first; public construction
currently never reaches them. It never reads credential contents,
never logs in/out and never copies credentials. Native default model selection
is labelled `native_default`; an explicit configured model is bound into the
configuration fingerprint, without inventing a model catalog. Independently
persisted model bindings are rejected until the product slice integrates them.

The launch uses JSONL, ephemeral sessions, workspace-write permissions, no
interactive approvals, no user config or execpolicy rules, disabled hooks,
plugins, apps, skill search, multi-agent and shell snapshots, and the inspected
CLI's experimental `skip_host_skill_discovery` feature. Project/ancestor
`.codex` and `.agents` directories, system config/requirements and extra native
config profiles are rejected. A private empty home and explicit minimal child
and shell environments prevent ambient control-plane credentials and user shell
startup files from being inherited. `Config.NetworkEnvironment` accepts explicitly
selected HTTP/HTTPS/ALL/NO proxy variables (upper/lower case), `SSL_CERT_FILE` and
`NODE_EXTRA_CA_CERTS`. Chora snapshots these values, includes them in the binding,
and revalidates selected certificate bytes before launch. It does not discover or
change system proxies. Conflicting upper/lowercase values, credential-bearing
proxy URLs, endpoint overrides, TLS-disable switches, certificate directories and
other environment keys fail closed. Proxy credentials are not supported in v1.
Without explicit proxy values, native system-proxy behavior is unchanged.
Workspace/ancestor and authentication-home `.env` files are rejected so native
dotenv loading cannot silently change routing or credentials. The selected native
authentication scope is
still accessible to the trusted process: this remains **Local Connected, No
Sandbox**, with no adversarial host-read or escaped-process containment claim.
The experimental resource-discovery switch requires further real successful-task
qualification; help/flag acceptance is not that evidence.

Preparation independently re-inspects executable bytes and canonical paths,
validates the frozen binding and returns source, configuration and security
proofs. The supervisor repeats executable/policy validation at launch, requires
exact argv/environment, and closes stdin after delivering the one-shot prompt.
Pi continues to hold stdin until settlement. Resume, Docker, MCP, Pi resource
projection, automatic check evidence and interactive approvals are unsupported;
auto-check resource contracts are rejected before launch. Missing named-check
observations cannot be manufactured into passing evidence.

JSONL projection persists bounded final messages, opaque thread provenance and
sanitized lifecycle/tool identifiers. It discards reasoning, raw tool output and
native diagnostics. Completion requires an ordered session/turn/final/settlement
sequence plus trusted successful exit. Usage accepts the required input/cache-read/
output counters and optional cache-write/reasoning counters emitted by the
reviewed releases. Known counters must be nonnegative integers; unknown usage
metadata and additive non-authoritative frame fields are discarded. Missing
required fields and new terminal authority fields (`status`, `outcome`, `error`,
`is_error`, `stop_reason` on `turn.completed`) fail closed. Command/file-change
states must match their start/update/completion phase; completed commands require
an exit code. Tool outcomes are persisted as statuses, not independent check
evidence; a failed tool can be handled by the agent before successful turn completion.
Unknown tool types or lifecycle/terminal states are rejected. Duplicate/reordered
terminal or final
items, unmatched/duplicate command or file-change results, pending tools at settlement, truncated/oversized frames, missing final output, model/auth errors and
nonzero exits fail closed. Cancellation and timeout remain supervisor/application
owned. Protocol fixtures and persisted application lifecycle tests are synthetic
coverage, not proof of model access.

Run the opt-in real test with explicitly selected existing local values:

```sh
CHORA_CODEX_EXECUTABLE="$selected_codex_executable" \
CHORA_CODEX_AUTH_HOME="$selected_existing_codex_home" \
CHORA_CODEX_MODEL="$selected_codex_model" \
go test ./internal/agent/codexexec -run '^TestRealCodexQualification$' -v -count=1
```

The opt-in test uses disposable workspaces for a bounded document task, an
ignored project-instruction canary, explicit unavailable-model failure and native
process-group cancellation. To select existing network configuration for the test,
prefix the supported variable with `CHORA_CODEX_`, for example
`CHORA_CODEX_HTTPS_PROXY`; the harness does not infer network settings. No raw
native diagnostics or credentials are printed. A separate synthetic shell fixture
checks descendant cleanup and EOF behavior. Login detection does not prove model
access: a model rejection is not by itself a network diagnosis. Native and provider
comparisons must use the same authentication, model, routing and permissions.
Successful qualification of one explicit model does not qualify another model or
all native resource mechanisms. Product enablement and stage acceptance still
require independent review; this registration does not enable a browser default.

## Claude Code print provider (candidate)

`claude-print` is a separate, explicitly registered provider, using the native
Claude Code `2.1.251` executable and `--print --output-format stream-json`.
`localweb.NewClaudeRuntimeProvider` composes it with its own trusted-host
supervisor. It does not change defaults or claim the browser configuration flow
is complete. The reusable execution-configuration resolver can bind it before a
task; persistence and configure-once UX remain the later product slice.

Configuration selects an executable, dedicated private home, existing native
`CLAUDE_CONFIG_DIR` authentication scope, optional model and explicit network
settings using the same allowlist as Codex. Bounded native version and
`auth status` probes require an existing first-party Claude login (`claude.ai`).
API keys, helpers, third-party providers and interactive login are unsupported;
Chora never reads or copies credentials. Installation/login detection is separate
from real inference qualification. Changes to executable bytes, frozen policy,
selected certificate bytes or the bound request fail before launch.

Launch uses safe mode, restricted mode, no setting sources, strict empty MCP
configuration, disabled skills/Chrome, no session persistence and `dontAsk`
permissions. The explicit tool set is Read, Glob, Grep, Edit, Write and Bash;
there is no Agent tool, browser, MCP or fallback model. Explicitly enabling Bash
in restricted mode permits command execution. This is **Local Connected, No
Sandbox**: the native process and shell retain the selected user's host access.
It does not promise adversarial filesystem or escaped-process containment.
Known managed configuration sources are rejected instead of overridden. Native
project/ancestor customization paths and dotenv files are rejected before launch.
Safe mode disables native user customizations; the native init event must report
the exact tool set, version and permissions and empty MCP/skill/plugin/command
lists. Model success and all authenticated policy channels still need independent
qualification; native init alone is not that evidence.

Only bounded assistant text, lifecycle identifiers and tool names are projected.
Thinking, native diagnostics, tool inputs/outputs and account/path metadata are
discarded. Completion requires the same session's ordered init, assistant output,
all started tools settled, a successful native result and trusted exit zero.
`subtype=success` with `is_error=true` is a failure, including the native
missing-login response. Permission denials, turn limits, duplicate/reordered or
cross-session events, truncated frames and unexpected tools/resources fail closed.
Cancellation and deadlines remain supervisor-owned. Resume, Docker, native
resource projection, interactive approvals and automatic check evidence are
unsupported; no missing evidence becomes a passing check.

Run opt-in qualification with explicitly selected existing values:

```sh
CHORA_CLAUDE_EXECUTABLE="$selected_claude_executable" \
CHORA_CLAUDE_AUTH_HOME="$selected_existing_claude_config_dir" \
CHORA_CLAUDE_MODEL="$selected_claude_model" \
go test ./internal/agent/claudeprint -run '^TestRealClaudeQualification$' -v -count=1
```

The harness uses disposable workspaces and canaries. Supported network values
are opt-in via `CHORA_CLAUDE_HTTPS_PROXY` and the analogous allowlisted names.
When the selected login is unavailable, the bounded-task subtest explicitly
skips; the test can still verify native missing-auth failure, resource disabling
and live-process cancellation. A passing overall command with that skip does
**not** qualify successful inference. Synthetic lifecycle and descendant-cleanup
tests are separate from real model access. This candidate stays unqualified for
production enablement until a real task passes using authorized existing login.

Native interfaces: [Claude CLI reference](https://code.claude.com/docs/en/cli-reference)
and [streaming output](https://code.claude.com/docs/en/agent-sdk/streaming-output).
The installed version remains the binding authority; later documentation does
not automatically qualify another version.

## Hermes Agent candidate

The `hermes` candidate implements pure projection of NousResearch Hermes Agent's
`hermes chat -q ... --format stream-json` protocol. **Native launch is unavailable.**
It does not enable a browser runtime option, change Pi defaults, or qualify
Local Connected execution. If enabled in a future slice, it would still be
**Local Connected, No Sandbox**; shell tools retain the user's host access.
Resume, skills, MCP, interactive approvals, automatic check evidence, Docker,
file editing and command execution are not advertised as supported capabilities.

The inspected upstream installation reports `0.23.1` in its install stamp at
commit `3b421578588243f920886e6fe79a6faa87ea026e`. This is **static metadata**, not
a CLI version observation, authenticated inference or a version allowlist.
Other well-formed versions stay pending under the same qualification mechanism.
No synthetic transcript, login file, matching flag or signature grants launch.

This source exposes three concrete integration gaps:

- The installed launcher contains interrupted-update recovery before CLI argument
  handling. Running `--version` or `--help` cannot be assumed free of writes.
  The dotenv loader can normalize files before loading them, and can load external
  secret sources. A replacement empty HOME would not establish safety for the
  selected authentication scope.
- Stream JSON routes through `gateway_chat_startup` and a gateway session. CLI
  termination and its process group do not establish worker termination or that
  an existing daemon consumed the same frozen configuration. Cancellation and
  restart custody therefore remain unverified.
- Disabling SDK retries does not disable native auxiliary retry/fallback policy.
  The inspected source has its own transient retry defaults. A task limit or
  process deadline alone cannot prove the requested no-retry policy.

Relevant upstream source:
[stream emitter](https://github.com/NousResearch/hermes-agent/blob/3b421578588243f920886e6fe79a6faa87ea026e/hermes_cli/stream_json.py),
[CLI routing](https://github.com/NousResearch/hermes-agent/blob/3b421578588243f920886e6fe79a6faa87ea026e/hermes_cli/main.py),
[dotenv loader](https://github.com/NousResearch/hermes-agent/blob/3b421578588243f920886e6fe79a6faa87ea026e/hermes_cli/env_loader.py),
[auxiliary retry policy](https://github.com/NousResearch/hermes-agent/blob/3b421578588243f920886e6fe79a6faa87ea026e/agent/auxiliary_client.py).

`hermes.Inspect` only reads explicitly selected executable identities, bounded
implementation metadata and authentication-file **existence/type**. It never
executes a native process, connects to a daemon, reads credentials, writes the
selected scope or makes a model request. `present_unverified` is not a valid login.
Its identity binds the launcher chain and selected semantic files; it deliberately
does **not** claim the complete Python/runtime/configuration closure is frozen.
Public `New`, `NewReviewed`, fingerprinting and preparation fail closed with
`hermes_safe_start_unverifiable`. Composition creates no supervisor or route.
Unknown installations without the required static metadata are unavailable.

To inspect an explicitly selected existing installation without invoking Hermes:

```sh
go run ./cmd/chora-hermes-review \
  -executable "$HERMES_ENTRY" -native-launcher "$HERMES_NATIVE_LAUNCHER" \
  -interpreter "$HERMES_PYTHON" -source-root "$HERMES_SOURCE" \
  -auth-home "$HERMES_AUTH_HOME"
```

For an unknown version's **semantic** review, add `-reviewer-key` (Ed25519 public
key in PKIX PEM), `-reviewer` and `-source-reference` (immutable public HTTPS
source). The host provisions that trust key; no task or HTTP input can provision
one. The command reads metadata, runs its fixed in-process positive/negative
corpus, and emits an identity/nonce/corpus/expiry challenge and `MessageHex`.
An independent trusted reviewer audits the exact source mapping and signs the
32 decoded message bytes externally. Send `{"SignatureHex":"..."}` on stdin
to that same process. It verifies the signature, registers the outstanding
challenge once and consumes it through `NewReviewed`, printing `Ready: false`.
Only the event/tool/terminal semantic observations change to verified; startup,
retry, worker custody, complete closure and authentication remain pending.
Replays, changed identities, unknown keys and expired requests are rejected;
revocation or process restart loses approval. This uses the existing shared
qualification types and a Hermes-specific host trust service, not Codex's review
registry. It is not native execution acceptance.

Protocol projection retains bounded text, session/call identifiers and tool names;
stderr, native errors, tool inputs/outputs and token/account metadata are discarded.
Completion requires ordered init, paired tools, final assistant text matching the
terminal text, a successful terminal and trusted exit zero. An initially empty
gateway session is allowed; the terminal must supply a valid session. A replay
gap, malformed/truncated frame, unknown event, duplicate terminal or mismatched
tool/session fails closed. Deterministic Run/Attempt tests use a test-only synthetic
launcher; they do not establish native worker cleanup or real-model success.
