# Chora public roadmap

[简体中文](ROADMAP.zh-CN.md) · [README](README.md)

Status, 2026-09-21: Developer Alpha with public source. Real multi-repository delivery
qualification, `M2_S3_PASS` and local DA-1/O5 preparation are complete. Source was
published on 2026-09-10; versioned prereleases are recorded on the
[Releases page](https://github.com/Yangyang96/chora/releases). M2 as a whole is
still active. M2-S4-1 passed technical acceptance on 2026-09-13; M2-S4-2 is withdrawn from required scope, not PASS.
M2-S5-1 passed technical acceptance on 2026-09-14. M2-S5-2 passed Pi model
selection technical acceptance on 2026-09-15. M2-S5-3 optional app preview passed
technical acceptance on 2026-09-16. M2-S6 local application implementation and
native interaction acceptance are complete; formal distribution remains deferred.
M2-S6A Native Task Board V0 passed technical acceptance on 2026-09-19.
M2-S7 Provider-native Skills/MCP setup passed technical acceptance on 2026-09-20
for Local Connected.

The accepted next direction is research, design and documentation within software
Projects, with simpler execution settings. Unrelated general-purpose work is
outside scope. M2-S8-1 execution terminology is implemented;
M2-S8-2 reusable execution settings are complete. M2-S9-1 and M2-S9-2 passed
technical acceptance on 2026-09-21. See the
[product decision](docs/project-workflows.md).

The M2 reliability follow-up adds live Docker observation, bounded Pi progress
capture, qualified in-container browser tooling and explicit acceptance-coverage
limits in Review. A fresh real-Agent sample exercised the browser capability and
required reviewer feedback to repair correctness and check-reporting gaps. The
Agent repaired its own code; this is review-driven repair, not first-pass
autonomous completion. Browser availability and successful selected commands do
not establish full task acceptance or close M2.
See [isolated execution](docs/isolated-local.md) for the supported boundary.

Project is the long-lived owner of repository resources and topic Rooms. A Task
belongs to one Room and selects its repository scope. Empty Projects, multiple
repositories, independent Room history and Task worktrees are current foundations.
Chora owns the work surface, durable Task/Run/Review state and execution integration;
mature Providers supply the model/tool loop and infrastructure. Spec Coding is
the first vertical. The first public target remains source-checkout, Apple Silicon
macOS, existing local Git repositories and user-controlled Pi with explicit
Local Connected / No Sandbox. It is not a stable or installed release.

| Stage | Outcome | State |
| --- | --- | --- |
| M0/M1 | Deterministic foundation and one real isolated local coding journey | Accepted at its historical maintainer boundary; private M1 assets are not publicly distributed. |
| M2-S1 | Local Pi Task worktrees, visible work, Review and Resume | Technical checkpoints retained; final product user acceptance pending. |
| M2-S2-P1/P2/P2A | Request boundaries, immutable Task bases, Project/Room ownership and migration | Qualified in the integrated prepublication candidate; historical evidence retained. |
| P3/P4 redesign | Multi-repository Tasks, text/new-file review, automatic checks, large-repository policy and partial recovery | Real continuous multi-repository technical qualification passed. |
| M2-S2 delivery | Task-branch Commit -> Push -> GitHub PR -> Merge -> safe cleanup, consistent task status/progress | Integrated real multi-repository qualification and repairs passed; legacy Apply retained. |
| M2-S3-0..3 | Close unwanted results, Pi onboarding/model provenance, maintenance/recovery and public journey/CI | `M2_S3_PASS`: S3-0..3 technical acceptance and consolidated independent review complete. |
| M2-S5-1 | Native in-flight instructions and evidence-linked decisions, risks, unknowns, human gates, resolution and reopening | Technical acceptance complete on 2026-09-14; internal evidence retained privately. |
| M2-S5-3 | Optional Task app preview in Local Connected and Isolated Local, logs, stop and recovery cleanup | Technical acceptance complete on 2026-09-16 on Apple Silicon macOS. |
| M2-S6 | macOS application, bundled runtime, owned service lifecycle and local native interactions | Local implementation/development-package validation complete; native interaction acceptance recorded on 2026-09-17. Formal distribution and signed-update qualification remain deferred. |
| M2-S6A | Native Task Board V0: existing Tasks, Project/Room views, attention and evidence-based phase projection | COMPLETE: S6A-1/2/3 passed technical acceptance on 2026-09-19. See the scope and verification summary below. |
| M2-S7 | Project-scoped native Skills/MCP in Local Connected | Technical acceptance complete on 2026-09-20. |
| M2-S8 | Two execution environments and reusable Project settings | S8-1 and S8-2 complete on 2026-09-20. |
| M2-S9 | Research, design and documentation within software Projects | S9-1 and S9-2 passed technical acceptance on 2026-09-21. |
| DA-1 / O5 v2 | Exact local candidate, public checks, licenses/SBOM, secret scans and release materials | Local candidate preparation passed; final user acceptance and publication actions are recorded separately. |

Technical acceptance was frozen on 2026-09-09, followed by identity and publication
repairs. The published verification baseline is [commit `7aaea26`](https://github.com/Yangyang96/chora/commit/7aaea26cbd67c44ce94c70d017c09ca116ab6e6a):
[Linux/macOS tests and browser CI](https://github.com/Yangyang96/chora/actions/runs/34440033315)
and [secret scanning](https://github.com/Yangyang96/chora/actions/runs/34440033375)
passed. The full real-Pi and recovery acceptance records are retained separately
by the maintainer; credential-free CI does not replace them. Skips and historical
N/A cases are not PASS. This is a source-bound milestone record, not a claim that
later HEADs have repeated all acceptance runs or that the user signed acceptance.

## First public Alpha gate

The continuous preparation sequence is:

Existing-work handoff -> real R5/SCM qualification and repairs -> S3-0..3 -> local
DA-1/O5 candidate preparation -> final user acceptance -> user publication.

Once the preparation Goal is started, technical stages advance without separate
human approval at each milestone. `PREPUBLICATION_READY_FOR_USER_ACCEPTANCE`
means all applicable prepublication technical work is done and a runnable
acceptance environment, frozen verified source and release package are ready.
`PUBLIC_DEVELOPER_ALPHA_READY` additionally requires the user's acceptance of
the exact candidate. Neither state means published; actual publication remains
the user's action. Feedback changes invalidate affected candidate evidence.

This candidate includes integrated Commit/Push/PR/Merge/cleanup. The default route
is Task branch/worktree -> development/checks -> Review -> per-repository Commit
-> Push -> GitHub PR -> Merge -> cleanup. Apply remains optional local writeback
and historical compatibility. Do not silently omit SCM to reduce qualification.
Delivery does not implicitly advance original checkouts; later Tasks explicitly
select the current per-repository base while old history stays immutable.

M2-S3-1 requires in-app Pi installation/configuration guidance, reuse of existing
compatible Pi and click-to-install when prerequisites are met. Show source,
qualified version, destination, progress and recovery; use native authentication
and re-detect readiness. Missing Node gets manual guidance. A Chora installer,
automatic system setup and automatic Agent upgrades remain out of scope.

From clean source/manifests and their own Pi configuration, a developer must
complete useful successive tasks across at least two repositories and two Rooms,
including new files, accurate checks, repair, grouped Review and full delivery.
Verify new bases, retained history, restart/Resume, partial delivery recovery,
legacy Apply compatibility, backup/restore/cleanup, schema limits, error recovery
and keyboard/narrow-layout usability. Reuse unchanged real regression evidence.

Credential-free CI and explicitly selected real-Pi/macOS/GitHub evidence remain
separate; skip or UNKNOWN is not PASS. Final clean-candidate journeys, SBOM/license
inventory and dedicated source/history secret scans are required before release.
Verify existing target settings read-only;
identify genuinely release-time checks explicitly instead of claiming them passed.
The public Local Connected route does not require private M1 images or maintainer OAuth.

## Completed first-Alpha usability: M2-S3

S3-0 closes unwanted undelivered results while retaining history and all existing
Commit/Push/PR/Merge/Apply facts. It neither undoes delivered changes nor deletes
worktrees automatically. Partial/uncertain operations require reconciliation;
archive and cleanup retain their own eligibility checks. S3-1 adds qualified Pi
onboarding and actual model provenance/native configuration guidance. S3-2 covers
source updates, compatibility, backup and recovery. S3-3 qualifies the integrated
public journey and maintained CI. All four slices passed technical acceptance.

M2-S8 execution settings are complete. M2-S9 project collaboration passed technical
acceptance on 2026-09-21. S7 native Skills/MCP and S6A Task Board V0 are complete. S6 local
implementation is complete; formal macOS distribution remains a separate release
track. These priorities add no new first-Alpha acceptance gate.

## Completed isolated execution and retired profile qualification

M2-S4-1 provides one [Isolated Local environment](docs/isolated-local.md). Technical
acceptance passed on 2026-09-13, covering public preparation, credentials,
execution, checks, Review -> Commit -> Push -> PR -> Merge -> cleanup,
cancellation, restart and Resume. Later model controls are recorded below.
Immutable multi-repository Task bases, legacy Apply and fail-closed isolation remain.

The 2026-09-20 [product decision](docs/project-workflows.md) withdraws M2-S4-2
Minimal/Standard qualification as a scheduled deliverable and M2 completion
requirement. It is not completed or PASS. Minimal may remain an internal comparison
configuration when useful; Standard's useful tools belong to default Agent
capabilities. Preserve historical IDs, policy bindings and evidence. Additional
presets are demand-gated, not an obligation to recreate the old matrix.

## Next personal workspace slices

Accepted implementation order, without calendar-date commitments. Planning
acceptance does not claim implementation or activate it in this documentation
task. New slice IDs preserve earlier milestone IDs and evidence.

| Order | Slice | Start condition | Completion outcome |
| --- | --- | --- | --- |
| Complete | M2-S8-1: execution terminology | Existing local and isolated Pi paths. | **Local execution · No Sandbox** and **Isolated execution** consistently describe new-Task choices. Focused API/UI tests and browser E2E cover selection, acknowledgement and disclosure. Technical IDs, commands, paths and policy identifiers stay unchanged. |
| Complete | M2-S8-2: reusable Project execution settings | S8-1 terminology is validated. | Project defaults, deliberate Task overrides and an accurate effective-settings summary work through execution and restart. |
| Complete | M2-S9-1: project research and proposal review | S8 execution settings are usable; select one concrete project question. | A repository-optional Task produces a sourced finding/proposal, supports feedback and review, and reopens without fabricated code delivery. |
| Complete | M2-S9-2: document revision and implementation handoff | S9-1 result, review and provenance contract is proved. | Revise and accept a project design/document, then explicitly bind that accepted revision to a later coding Task. |
| In progress | M3 team collaboration and delegation | Start with bounded single-user local Agent delegation after S9; shared identity, membership and concurrency remain gates for multi-human work. | Shared ownership, review, handoff, aggregation and conflict handling. |
| On demonstrated need | M4 background/remote execution | A bounded scenario needs work away from the foreground. | Explicit placement, cancellation, recovery, budgets and notifications. |

S8/S9 do not wait for signed macOS distribution, a second Runtime, Contexere or
generic Work Apps. S6 signing/notarization, clean-machine and signed-update
qualification remain a separate release track. Broader isolated dependencies or
Skills/MCP need their own demand and qualification; they are not included in
Project defaults. Revisit priorities using feedback, without requiring every
future M2 option to finish before any M3 work.

### M2-S8 — Execution settings

Reuse S5-2 model controls, S7 native capability configuration and existing Pi
host/isolated routes. Task overrides take precedence over Project defaults;
show the effective environment, model choice and relevant capability availability
with optional details. Runtime defaults remain distinct from observed models.
No new Provider platform, model router or policy matrix is needed. S8-1 uses the same environment labels in the UI and operating documentation.

Freeze effective settings and their source/version for the Task and its execution
records. Project edits do not rebind existing Tasks, retries or resumed sessions.
Explicit switches retain the supported successor-execution rules; do not broaden
them or mutate old Attempts. Host selection still requires disclosure
acknowledgement. Unavailable isolation cannot choose host execution, another
model or extra credentials automatically.

Completion: focused terminology/configuration tests, relevant browser E2E and
affected real-Pi evidence for both existing execution environments. Prove
inheritance/override, Project isolation, unavailable choices, restart and local
acknowledgement/no-host-fallback. S8-1 does not require migration, reopening or
qualification of old Minimal/Standard history and adds no compatibility adapter.
Internal comparison fixtures may remain without becoming a product promise.

### M2-S9 — Research, design and documentation

Use existing Task/Run/Result mechanisms with workflow-appropriate resources,
outcomes and checks. S9-1 accepts an explicitly supplied software-project question
and material without requiring a Git repository or code change. Use authorized
Provider capabilities; no connector catalog or mandatory task-type form. Show
sources, uncertainties, feedback and reviewable findings. Agent completion is
distinct from human acceptance; neither automatically launches another Task.

S9-2 adds bounded revision/review of a project design or Markdown document, reusing
Artifact, Decision and context mechanisms. Preserve source/revision identity and
review history. Later coding Tasks explicitly select accepted revisions as frozen
input; subsequent document edits cannot change that binding. Materials stay in
their Room unless explicitly referenced or shared. Repository-backed document edits
retain repository checks and delivery; accepting a Room artifact authorizes neither
repository writes nor publication.

The implemented flow accepts one to sixteen explicitly pasted materials whose
combined frozen snapshot remains within the persisted metadata bound. A complete
Agent Markdown result is limited to 64 KiB. Saving, revising and accepting are
separate explicit actions. Acceptance creates an exact Room revision; a later Task
in the same Room must select that accepted revision explicitly. Existing Tasks do
not update when the document changes, and no document action creates SCM delivery,
publication authority or a follow-on implementation Task.

Completion per sub-slice: useful end-to-end work, feedback/retry and restart/reopen
with honest provenance and missing-evidence states. Include API/persistence tests,
relevant browser E2E and a real Pi project journey. Extend Task Board projections
for non-code results without inventing SCM delivery phases. Regress coding
Diff/check/Review/SCM; resource scope, execution authority and accepted history
cannot expand silently. General office work, real-time multi-user editors,
a workflow engine and new context infrastructure remain outside S9.

S9-1 and S9-2 passed technical acceptance on Apple Silicon macOS on 2026-09-21.
API/persistence and browser tests cover revision review, stale-source rejection,
explicit selection and frozen handoff. Real Local Pi completed two sourced-Markdown
executions, including rejection, feedback and a successful retry. A later coding Task produced a real repository diff from the
explicitly selected accepted revision; a later document acceptance and service
restart preserved its exact frozen input. Isolated execution has self-contained automated coverage only;
real-model isolated qualification has not been performed.

M2-S5-1 acceptance covers both completed sub-slices. Agent proposals, system observations
and user entries have distinct provenance. Records persist source evidence,
versions and resolution history; empty/unassessed results are honest states.
Only necessary authorized gates block work, and answers cannot replay stale
execution. No second model loop or Contexere dependency. These retained acceptance
boundaries do not change P3/P4 or first-release acceptance.

## Completed Native Task Board V0 (M2-S6A)

Status: **COMPLETE**, 2026-09-19. The [implementation plan](docs/task-board-plan.md)
retains the scope, lifecycle mapping, API contract, B01-B27 acceptance matrix and
verification summary. See the [operating guide](docs/task-board.md) for use.

Keep Project -> Room -> Task ownership. A board card is the existing Task, not a
new Issue or a copy per Run/repository. Add a Project Tasks view with Board/List
modes, Room/repository filters, a shared Room-scoped view and Needs attention.
Existing New Task remains direct; card actions navigate to the existing owning
screen for fresh-state validation and current authorization/confirmation.

Use **Preparing | Working | Review | Delivery | Finished** as derived phases,
not a second editable state machine. Preserve precise substates and outcomes:
accepted is not delivered; cancelled Run is not necessarily ended Task; no-change
completion is not human acceptance; partial closure and cleanup are not merge.
Unknown or contradictory evidence stays visible in a separate reconciliation
group. Archiving remains an independent visibility/read-only concern.

| Slice | Outcome | State |
| --- | --- | --- |
| M2-S6A-1 | Shared pure phase/attention/outcome projection, path-specific facts, bounded read API and mapping/API tests | COMPLETE |
| M2-S6A-2 | Project/Room Board/List, filters, safe deep links, freshness, accessibility and navigation-continuity tests | COMPLETE |
| M2-S6A-3 | Integrated regression, local macOS qualification, measured usability/performance observations and operating docs | COMPLETE |

V0 excludes Backlog drafts, manual priority/order, drag-to-mutate, inline/bulk
writes, assignment, dependency scheduling, external tracker sync and a new model
loop. Board reads make no model calls and do not create or start work. Prove that
Run-to-board navigation cannot strand already-authorized verification; any needed
repair stays within existing execution/service ownership, not an M4 scheduler.
Preserve S5 preview and S6 service/recovery behavior. Signed distribution is not
required to start V0, and this slice does not authorize a Tag, Release or installer.

V1 lightweight drafts/ordering is demand-gated before or around M3, with capture
separate from execution authorization and explicit promotion into a Task. M3 adds
bounded local delegation first, with team authority/concurrency required for
shared claims and handoff; M4 adds
queued/background/remote execution, budgets and notifications. These extensions
are not implicit V0 work. The S6A suffix preserves existing S6/S7 milestone IDs.

## M3 first local slice: bounded research delegation

The first implementation slice is [local Agent delegation](docs/local-delegation.md):
one person on one Mac explicitly starts a fixed plan of one to four material-only
research/document assignments. Children inherit frozen parent input and execution
settings, execute sequentially, and retain normal Task/Run/result identities.
The parent collects sourced results for human review; no result is automatically
accepted and no SCM delivery is authorized. Stop, restart and duplicate-command
handling are part of this slice. A follow-on source-bound proposal import lets the
parent Agent suggest fixed assignments; explicit start revalidates the current
result and freezes its provenance without accepting it. Dedicated research
planning adds a single explicit start with pre-execution authority, one planning
Attempt and automatic validated import. These three slices passed technical
qualification on Apple Silicon macOS on 2026-09-23: the complete development
gate, browser journeys and real Pi execution covered sourced results, explicit
stop, restart continuity and unchanged human acceptance. Overall M3 remains in
progress.

An optional pre-authorized synthesis uses only frozen child results in one
additional Agent Attempt. Its integrated qualification is in progress.


This is a bounded starting point within M3. Multi-human membership/revocation,
parallel execution and coding integration are not
claimed complete. M4 remains demand-gated.

## Later outcomes and demand gates

- M3: team participation in topic Rooms, membership/revocation, concurrent review
  and handoff, plus reviewed outcome sharing and delegation. Reuse current
  multi-repository Project/Room/Task foundations; do not defer them to M3.
- M4: work away from the foreground, selected from local background or remote
  placement by actual need; cancellation, recovery, budgets and notifications.
- M5: shared SCM/CI and delivery policy, observable rollout/rollback, and proven
  software-project surfaces. Unrelated general-purpose applications are out of scope.
- M6: enterprise-scale governance, reliability and extension ecosystem.

These are priorities, not unconditional serial dependencies. Personal async or
basic PR/CI can be activated earlier by a bounded need. Second Runtime, search,
embedded surfaces and Contexere require repeated user friction or a measured
capability/context bottleneck before implementation; no generic platform is
prebuilt for them. Security, durability and compatibility advance in every slice.
Initial usage feedback can be manual and does not require a telemetry platform.

### Runtime-owned model capability discovery

M2-S5-2 does not require Chora to maintain a hand-authored model allowlist. Each Runtime owns capability discovery and validation. Chora may cache an identity-only catalog for display, but revalidates the selection with the Runtime at launch. A model change applies only to a new Attempt; prior requested and observed provider/model provenance remains immutable. Runtime removal or authentication failure fails closed without substitution.

Local Connected discovers models from the selected Pi executable and configuration.
Isolated Local reads its prepared image's built-in Pi catalog without credentials
or network access; this catalog does not promise authentication for every provider.
The current isolated credential projection remains DeepSeek-only. New tasks can
use the Runtime default or an explicit selection. Retries inherit the preceding
Attempt's selection unless a new model is chosen; requested and observed models
are shown separately. Changing execution mode preserves the model identity and
requires that the target Runtime support it. Ambiguous Pi CLI model references
are rejected rather than resolved to an alias.

Model selection acceptance covers two real DeepSeek models in Local Connected
and Flash/Pro in Isolated Local, Workbench restart, a new isolated Attempt with
a different model, and immutable requested/observed model history. This qualifies
the current Pi integration on Apple Silicon macOS, not another Runtime or all
providers in the discovered catalog.

### Optional application previews

M2-S5-3 adds [app preview](docs/app-preview.md) to the latest Task run as an
optional, collapsed panel. Code, diffs, checks and Review remain the primary
acceptance workflow; running an app is never required for acceptance or delivery.
Users explicitly configure and start one Web app per writable Task repository,
open its loopback URL, inspect bounded logs, stop it and clean up its resources.

Local Connected runs in the proven Task worktree with No Sandbox. Isolated Local
uses a separate app container from the prepared image, a private source snapshot
and one loopback port; Agent container policy stays unchanged and there is no
host fallback. Navigation preserves app lifetime. Successor execution, worktree
cleanup and normal Workbench shutdown stop previews. Recovery checks durable
ownership without automatically restarting apps. Dependencies are not installed
automatically; isolated previews require a restart to reflect newer source.

Preview acceptance covers real browser interaction and a credential-free app in
the prepared isolated image, startup, navigation continuity, logs, stop,
worktree cleanup, normal shutdown, durable recovery and unchanged code-review
state. Full tests, race checks, vet and standard E2E passed on Apple Silicon
macOS. This does not qualify another platform, arbitrary application images or
multi-service orchestration.

### macOS application implementation

M2-S6 provides a menu bar application and a reproducible Apple Silicon package
builder, with bundled Node.js/Pi, native Pi authentication, owned service lifetime,
idle-only backup/restore, source-data adoption and verified manual updates. See
[macOS application](docs/macos-application.md) for the operating contract.

Local implementation and development-package validation are separate from formal
distribution. Developer ID signing, notarization, clean-machine first launch and
signed update/recovery qualification remain release gates. No public installer
or release publication is claimed by this implementation.

Local native interaction acceptance completed on 2026-09-17: fresh setup,
provider selection and credential saving through Pi, authentication cancellation,
browser-tab closure without stopping the service, menu reopening of Workbench,
cancelled quit and confirmed clean shutdown. Repeated authentication labels retain
their provider identities. Formal distribution remains deferred; this does not
qualify a signed installer or a successful signed-update cycle.
