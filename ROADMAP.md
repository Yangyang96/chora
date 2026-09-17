# Chora public roadmap

[简体中文](ROADMAP.zh-CN.md) · [README](README.md)

Status, 2026-09-16: Developer Alpha with public source. Real multi-repository delivery
qualification, `M2_S3_PASS` and local DA-1/O5 preparation are complete. Source was
published on 2026-09-10; versioned prereleases are recorded on the
[Releases page](https://github.com/Yangyang96/chora/releases). M2 as a whole is
still active. M2-S4-1 passed technical acceptance on 2026-09-13; M2-S4-2 is deferred.
M2-S5-1 passed technical acceptance on 2026-09-14. M2-S5-2 passed Pi model
selection technical acceptance on 2026-09-15. M2-S5-3 optional app preview passed
technical acceptance on 2026-09-16.

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

Remaining M2 work includes deferred S4-2 profiles, native Skills/MCP setup
and packaged installation/lifecycle. M3 owns
Agent delegation, aggregation and conflicts; M4 adds background/remote execution.
These keep the priorities below and are not new first-Alpha requirements.

## First feature milestone after public Alpha: M2-S4

M2-S4-1 implementation provides one [Isolated Local mode](docs/isolated-local.md)
using public Pi 0.85.1, DeepSeek/deepseek-v4-pro and local arm64 Docker.
Technical acceptance passed on 2026-09-13, including public preparation, credentials,
execution, checks, Review → Commit → Push → PR → Merge → cleanup, cancellation,
restart and Resume. Immutable multi-repository Task bases and legacy Apply remain.
M2-S4-2 is scheduled after S4-1 is stable and qualifies Minimal/Standard only when
their tools/dependencies, network and resource policies have clear, tested
differences. At the current S4-1 pace, reserve one work unit (about 3–5 hours,
including focused acceptance). It does not block S5, but remains part of the
later M2 completion scope. One qualified mode can ship first. Isolation failure
never falls back to host execution.

Default order: M2-S3 -> public-candidate gates -> first Alpha publication ->
M2-S4. This adds no first-Alpha requirement and does not wait for M3/M4. Reuse
existing isolation evidence and current Project/Room/Task contracts; do not
rebuild the Runtime or Sandbox. This implementation does not create another Tag,
Release or installer. M2 remains incomplete and S4-2 is not implemented.

## Prioritized post-Alpha schedule

Default delivery priority; successors remain PLANNED_NOT_STARTED without calendar dates:

1. M2-S4: S4-1 complete with one usable isolated mode; S4-2 Minimal/Standard deferred.
2. M2-S5-1: S5-1a native in-flight instructions; S5-1b evidence-linked decisions,
   risks and unknowns, necessary human gates, resolution and reopening.
3. M2-S5-2: complete for Pi; explicit supported model controls, separate from execution mode.
4. M2-S5-3: complete; optional local app preview, logs, stop and cleanup.
5. M2-S6: one supported macOS package, compatible updates, recovery and uninstall.
6. M2-S7: deliberate Provider-native Skills/MCP setup and capability inspection.
7. M3: Agent delegation, aggregation and conflicts after basic team authority;
   background/remote continuation belongs to M4.

This ranks the earlier candidates without adding first-Alpha gates or changing
P3/P4. S4-2 may be deferred without blocking S5; M3 does not wait for all M2.
Scope and changes to priority use actual feedback at release checkpoints. A
listed slice is not implemented or automatically authorized to run.

M2-S5-1 completion requires both sub-slices. Agent proposals, system observations
and user entries have distinct provenance. Records persist source evidence,
versions and resolution history; empty/unassessed results are honest states.
Only necessary authorized gates block work, and answers cannot replay stale
execution. No second model loop or Contexere dependency. This is post-Alpha
planning, not current capability or a change to P3/P4/first-release acceptance.

## Later outcomes and demand gates

- M3: team participation in topic Rooms, membership/revocation, concurrent review
  and handoff, plus reviewed outcome sharing and delegation. Reuse current
  multi-repository Project/Room/Task foundations; do not defer them to M3.
- M4: work away from the foreground, selected from local background or remote
  placement by actual need; cancellation, recovery, budgets and notifications.
- M5: shared SCM/CI and delivery policy, observable rollout/rollback, proven apps.
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
