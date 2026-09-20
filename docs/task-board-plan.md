# Native Task Board V0 — M2-S6A implementation plan

**English (authoritative)** | [简体中文](task-board-plan.zh-CN.md)

Status: **COMPLETE**. S6A-1, S6A-2 and S6A-3 passed technical acceptance on
2026-09-19. This document retains the scope and acceptance contract. See the
[operating guide](task-board.md) for use.

Baseline inspected: [`b93100d`](https://github.com/Yangyang96/chora/commit/b93100d536c5866456efa9551a4fad92db5d8ee4),
after M2-S6 local application implementation and native interaction acceptance.
Formal signing, notarization and signed-update qualification remain separate.
The planning baseline is historical; the implementation is based on current main.

## 1. Decision and sequence

Build one native view of existing Tasks, not a second issue tracker. Project
owns resources, Room organizes topic/context, Task owns the work, and the board
organizes attention. Keep Provider-owned model/tool loops and existing execution,
review, delivery and authorization contracts.

Schedule **M2-S6 local implementation -> M2-S6A V0 -> M2-S7**. The S6A suffix
inserts a bounded slice without renumbering S7 or reopening S6 acceptance. It
does not wait for formal macOS distribution, all deferred S4-2 profiles, M3 or
M4, and does not become a new first-Alpha release gate.

The value hypothesis is less time searching for work requiring a decision,
review, recovery or delivery, without manually synchronizing task status. This
is not a claim of measured retention, Agent-quality or token-efficiency gains.

## 2. Scope and exclusions

| V0 includes | V0 does not include |
| --- | --- |
| Project-wide aggregation of existing Tasks across Rooms | A second Issue/Card identity or database of board states |
| Board/list switch; Room and repository filters | Backlog drafts, priorities, custom fields, manual ordering |
| A shared Room-scoped view and existing Task deep links | Moving Tasks between Rooms or repositories |
| Needs attention, accurate substates and next-step navigation | Drag-to-run, drag-to-accept, drag-to-merge or bulk writes |
| Read-only summaries, bounded refresh, recovery from stale data | Agent assignment, claims, dependencies, automatic delegation |
| English/Chinese, keyboard and narrow-window operation | A scheduler, another model loop, remote workers or external tracker sync |

Existing New Task, archive/restore, Review, retry, delivery and cleanup workflows
remain reachable with their current safeguards. V0 does not add inline mutation
shortcuts: a card action opens the existing owning screen, which reloads current
state before offering a command. Viewing, filtering and refreshing make no model
calls and do not prepare worktrees or start tasks.

One Task is one card even with multiple repositories, Runs or Attempts. Internal
Agent steps remain inside that card's detail. No mandatory board visit is added
to the current Room -> New Task -> execution journey. This is the initial coding
view, not a universal requirement that every future work type must create a PR.

## 3. Source and integration points at the planning baseline

| Source | Existing responsibility and implication |
| --- | --- |
| [Task domain](../internal/domain/task.go) | `open` / `closed` plus independent archive state; there are no five board states. |
| [Run transitions](../internal/domain/run_transition.go) | Execution, verification, revision, recovery, cancellation and retry; not a one-way pipeline. |
| [Current action](../internal/app/current_action.go) | Plan/Run lineage and action targets; reuse validated facts, not action labels as state. |
| [Resource review](../internal/app/resource_review.go) | Current resource-result review path; do not assume legacy rejection routing is universal. |
| [Result closure](../internal/app/result_closure.go) | Closes eligible undelivered results and retains achievements; closure is not success. |
| [No-change result](../internal/app/resource_terminal.go) | `completed` can mean no changes, subject to the existing result/check rules. |
| [Task summaries](../web/src/types.ts) | Existing TaskSummary lacks the full per-task Gate/blocker summary needed for attention. |
| [Task display](../web/src/taskStatus.ts) and [delivery display](../web/src/deliveryProgress.ts) | Existing labels and repository progress; do not infer stage from a label or tone. |
| [Room home](../web/src/ui/RoomHome.tsx) and [Project home](../web/src/ui/ProjectHome.tsx) | Existing navigation and Task entry surfaces. |
| [Application routing](../web/src/newApp.tsx) | Room polling and Run-route-dependent automatic-verification effect require navigation regression tests. |
| [macOS application](macos-application.md) | S6 service ownership, tab closure, menu reopening and shutdown must remain intact. |

Follow existing service/store/HTTP layering. Locate the actual store queries and
route registration before editing; filenames and endpoint additions below are
proposals, not claims that these components already exist. Prefer one shared
application-layer projection consumed by both list and board. Existing detail
labels may be more granular, but must not contradict the shared facts.

## 4. User journey and layout

Project gets a **Tasks** view containing Board and List modes. Preserve Project
resource management and Room navigation. Room uses the same presentation with
its scope fixed; it must not become a lifecycle column. The default Room remains
the default owner when entering the existing new-task flow from Project.

Use five visible phase groups: **Preparing | Working | Review | Delivery |
Finished**; Chinese: **准备中 | 处理中 | 待审阅 | 交付中 | 已结束**. These are
projections, not editable states. Working does not imply that the Agent process
is currently running; Finished does not imply success. Tasks may move backwards
or skip groups.

Card content: title, Room, repository names/count, phase, precise substate,
attention reason, next action and last activity. Long details, model/provenance,
Attempt history, checks and logs stay in the existing Task/Run screen. Display
only the minimal Gate summary needed to recognize the action, not raw context.

**Needs attention** is a filter across phases, not a sixth lifecycle stage.
Keep finished work compact and filterable. Archived Tasks are excluded by default
and available explicitly; archived Project/Room views remain readable and cannot
authorize new work. A zero-task Project still supports normal project setup.

Persist non-authoritative view preferences only. Put filters in navigation state
or the URL so deep-link/back navigation retains them. Preserve focused card and
scroll position when practical; refresh must not move keyboard focus. Use text
and accessible controls, not color alone. Narrow windows use the same list or
stacked groups without requiring drag interaction.

## 5. Projection contract and precedence

Return stable typed fields, not translated text as logic. The minimum proposed
card contract is:

```text
identity: projectId, roomId, taskId, title, repository summaries
source: latestRunId/version, latestAttemptId when present, observedAt
phase: preparing | working | review | delivery | finished | null
substate: stable reason code + localized display parameters
attention: none | required | unknown, reasons with evidence targets
outcome: null | delivered | applied_locally | no_change | closed |
         partially_delivered_closed | mixed_delivery | cancelled | superseded
nextAction: kind, owning URL, identity/version target, availability reason
visibility: taskArchived, roomArchived, projectArchived, readOnly
health: current | stale | unavailable | inconsistent
```

This is a read model, not a persisted workflow or permission grant. Optional
source fields must reflect real evidence; do not invent an Attempt, reviewer,
owner or delivery identity. Runtime-neutral identities are required. Only emit
an outcome when its applicable conditions below are proved.

Evaluate in this order:

1. Validate scope, ownership and applicable lineage. Use a consistent read of the
   current Task, applicable plan, current Run/Attempt, Gate/review, closure and
   repository delivery facts. Adapt legacy and resource-result paths explicitly.
2. Detect unsupported, unavailable or contradictory facts. Set `phase=null` and
   `attention=unknown`, with a visible **Needs reconciliation / 待核对** group
   outside the five phases. Keep the row and its count; never silently drop it
   or default it to Preparing/Finished. A valid unrelated card must remain usable.
3. Follow current work, not any historical terminal Run. A prior accepted Run
   cannot finish a Task with an authorized successor still active. Resolve an
   explicit successor-plan or related-Task route using its supported contract.
4. For accepted results, aggregate all applicable repository delivery facts.
   Neither `Task.closed`, `Run.accepted`, `terminalAt`, `resultClosed`, cleanup,
   nor the absence of a next-action button proves successful delivery by itself.
5. Apply phase/outcome mapping, then independent attention and archive/read-only
   overlays. Staleness never upgrades a result or authorizes a command.

The projection is total over known and unknown input: every selected Task yields
one card or an identified reconciliation row. It must not attempt to repair the
underlying records as a side effect of a GET.

## 6. Lifecycle mapping

The following rows apply after validity/lineage checks and current-work selection.
They are not an unconditional switch on `Run.status`.

| Current authoritative facts | Phase | Required distinction |
| --- | --- | --- |
| No Run; valid initial plan preparation or initial run readiness | Preparing | Editing plan, plan review, resource preparation and ready-to-start are distinct substates; not Backlog. |
| First Run `draft` / `ready`, no execution history | Preparing | A failed resource preparation is attention, not falsely ready. |
| `ready` for a retry after execution | Working | Waiting to retry/resume; do not label as never started. |
| `running` | Working | Executing, or waiting for a current blocking Gate; only executing shows running activity. |
| `stopping` | Working | Stopping is not stopped; uncertain stop requires reconciliation/recovery. |
| `awaiting_verification` / `verifying` | Working | Waiting for checks versus checking; preserve unavailable/not-applicable distinctions. |
| `recovery_required` / `verification_recovery_required` | Working | Needs recovery; active automatic retry is distinguished from exhausted/blocked retry. |
| `revision_required`, valid implementation retry/resource-review rejection | Working | Changes requested, then retry; not still waiting for result acceptance. |
| Valid `continue_successor_plan` route | Preparing | Replanning within this Task, preserving earlier Run history. |
| Valid `open_related_task` route | Original phase from its own disposition | New Task has its own card and predecessor link; the link alone cannot finish the old Task. Emit superseded only with a proved end disposition. |
| Current result `awaiting_review` | Review | Task result review, not plan review or external PR review. |
| Accepted result, pending Commit/Push/PR/Merge or local Apply | Delivery | Show the actual remaining operation; acceptance is not publication. |
| Accepted result, applicable repository still awaiting review | Review | Preserve repository detail; do not conceal unfinished review inside success. |
| Delivery error, unknown external-operation result or PR closed without merge | Delivery if phase facts are known | Needs attention; unverifiable phase facts instead use reconciliation. PR closure is not merge. |
| Some repositories finished/closed but another has outstanding work | Delivery (or Review as above) | Show per-repository achievements and pending work; never finish from a boolean closure flag. |
| All applicable changed repositories satisfy the delivery rule below | Finished | Delivered, locally applied, or a mixed/closed outcome as appropriate. |
| `completed` from the existing no-change result path | Finished | No changes; do not claim human acceptance, merged code or satisfied requirements beyond the evidence. |
| `cancelled` with an open, resumable Task and no explicit end disposition | Working | Stopped / can retry; do not automatically resume or finish. |
| Proven cancelled/closed end disposition, no successor or unresolved work | Finished | Cancelled/closed, never a green success by default. |
| Archived Task/Room/Project | Underlying phase unchanged | Visibility/read-only overlay, not a completion event. |

### Delivery completion rule

For the normal Task-branch route, **delivery completion means observed integration
into the selected target via the existing supported merge evidence**. Commit,
Push and an open PR are intermediate achievements, not integration. This rule is
a V0 presentation decision, not a new runtime terminal state or a deployment claim.
For a supported legacy local-writeback route, proved Apply is a distinct
`applied_locally` outcome; never call it merged or deployed. When all repositories
finish through a mixture of merge and local Apply, use `mixed_delivery` and
preserve each repository's achieved endpoint.

Evaluate every repository in the bound result/scope. Read-only/no-change entries
do not create delivery work, but unavailable evidence must not be treated as
no-change. A genuinely optional/absent delivery path must be identified by its
contract, not guessed from a missing summary. Historical legacy records without
enough evidence remain explicit reconciliation cases.

Explicit closure can end remaining work only after existing eligibility and
operation-reconciliation facts prove no unresolved action remains. All integrated
repositories plus any abandoned repository yield `partially_delivered_closed`,
not `delivered`; preserve local commits/pushes even when not integrated. A closed
repository plus another open PR remains Delivery. Closure is not inferred from
an external PR simply being closed.

Cleanup is independent maintenance. Uncleaned but fully integrated results may
be Finished with cleanup available in detail. A cleaned worktree does not prove
merge: use retained achievement evidence, including cleanup after abandonment.
Cleanup errors remain visible attention without erasing a proved delivery outcome;
uncertain source/delivery evidence must never be hidden as a mere cleanup issue.

## 7. Attention and action rules

Attention is required for a current necessary blocking Gate, available result
review, actionable recovery/revision, or an available delivery step. Distinguish
plan review from result review and external PR review in the label. Nonblocking
risks/unknowns are informational, not automatic blockers. Active automatic retries
are Working; exhausted/blocked retries require attention. Optional cleanup is
maintenance rather than unfinished delivery; a cleanup failure still needs notice.

A resolved Gate disappears from actionable attention. A reopened/current Gate
appears with its current identity/version; historical Gates cannot replay work.
Use the owning Task/Run page to answer/review/retry. An archived parent suppresses
execution actions without hiding unresolved facts. Unknown/unavailable action
eligibility must not be rendered as "No action needed".

Do not use `currentAction.kind=none`, a success color, Agent-reported checks or
plain status text as acceptance evidence. Existing server authorization,
expected-version checks, idempotency and confirmation remain the final boundary.

## 8. Read API, refresh and lifecycle integration

Implemented route: `GET /api/v2/projects/{projectId}/task-board`, accepting
`roomId`, `repoId`, `phase`, `attention`, `archived`, `cursor`, `limit` and
`optionsCursor`.
Return cards, filtered totals, phase/attention/reconciliation counts, freshness
and a next cursor. Room-scoped display uses the same derivation and API contract.
Filter choices have an independent `optionsSnapshot` and `nextOptionsCursor`.
Each options page advances both Room and repository lists by 100; a shorter list
is exhausted independently. Each list contains at most 100 choices plus its
selected out-of-page choice. The options hash includes Project and all option
IDs/names, not Task progress; changed choices return 409 and restart only choice
pagination. The UI loads further choices explicitly and retains Task pages.

Validate all supplied IDs against the Project. Cross-project Room/repository
filters must not broaden access. Preserve existing local authentication, origin
and read-only rules; no new public listening interface or remote authority.

Use bounded bulk summary reads, not one HTTP request per Room/Task or full Run
logs, diffs and artifacts per card. Do not invoke Pi, models, GitHub reconciliation
commands or filesystem preparation to render the board. Read persisted delivery
observations; expose their freshness and let existing detail workflows reconcile.

Filter/derive before page selection. Counts describe the complete filtered set,
not just fetched cards, from the same consistent snapshot. Use a stable tie-break
such as activity plus Task ID. Refresh invalidates/restarts stale pagination when
necessary; deduplicate by Task ID and never claim snapshot isolation across pages
without implementing it. Start with a bounded page size of 50 and cap requests
at 100; these are design defaults, not measured capacity claims.

Reuse bounded polling (initial target: every two seconds while visible), one
in-flight request per view, cancellation on scope change, and immediate refresh
on focus/reconnect/return from an action. Ignore late responses for a previous
scope. Mark a failed refresh stale immediately; retain the last-known display
with its observation time. Keep detail navigation available, but do not offer
command-ready shortcuts from stale data. With no
valid snapshot, show unavailable, not an empty Project. No polling while hidden;
resuming a view reads first and never silently resumes execution.

**Navigation qualification:** inspect the Run-route-dependent automatic-verification
effect in `newApp.tsx`. Prove that leaving the Run screen for the board cannot
strand already-authorized checks, cause duplicate verification or cancel work.
If a route coupling is demonstrated, make the smallest change to existing
execution/service ownership needed for continuity, with regression tests. A GET
must remain read-only and the board must not become the scheduler. Do not add
queued work, autonomous authorization, crash auto-resume or remote execution.

Preserve S6 tab-close/service ownership, menu reopening, normal shutdown and S5
preview lifetime. Background-tab polling suppression is not permission to stop
an Agent or app. Reopening after restart reads durable truth; it does not restart
previews or change existing recovery rules.

## 9. Implementation slices

All three slices are implemented and technically qualified. The exit conditions
below remain the acceptance contract.

| Slice | Deliverable | Exit condition |
| --- | --- | --- |
| M2-S6A-1 | Pure shared phase/attention/outcome projection; path-specific source adapters; additive read API and bulk summary access | Mapping and API tests cover all rows, invalid scope, incomplete evidence, filters, counts, pagination and GET without side effects. No persisted board state or destructive migration. |
| M2-S6A-2 | Project Tasks entry, shared Room view, Board/List, filters, attention, deep links, translations, accessibility and bounded refresh | Views agree with details, existing creation stays direct, stale/scope-change navigation is safe; route-sensitive continuation is tested and any necessary narrow fix included. |
| M2-S6A-3 | Integrated regression, local macOS exercise, performance observations and final operating documentation | The matrix below is covered; checks report real pass/skip/block results; S6/S5 boundaries hold; documentation status changes only with corresponding evidence. |

Prefer no database migration. Additive summary fields do not require rewriting
historical Tasks. An index or narrowly necessary read support change must retain
backup/restore compatibility and receive its normal migration tests; it does not
justify a parallel task model. Default-presentation rollback must not mutate any
Task, Run, review or delivery data.

## 10. Required acceptance matrix

These retained requirements define acceptance. Implementation evidence and limits
are summarized below; a listed requirement alone is not proof of passing.

| ID | Case | Required result |
| --- | --- | --- |
| B01 | Empty Project and multiple Rooms | Normal empty state; one card per Task; no extra Room ownership. |
| B02 | Initial planning, plan review, first ready Run | Preparing with correct next-step label, no invented Backlog. |
| B03 | Retry `ready` after failed/rejected execution | Working, same Task/card, immutable prior Attempt history. |
| B04 | Execution and verification transitions | Accurate substates, including not-applicable/unavailable checks. |
| B05 | Blocking Gate, resolution and reopening | Attention follows current Gate identity; no stale replay. |
| B06 | Nonblocking risk/unknown | Informational only; does not block execution. |
| B07 | Pending retry versus exhausted/blocked retry | Correct Working/attention distinction and recovery route. |
| B08 | Result review rejection and supported replan/related routes | Correct return phase; no assumption of legacy routing on v2. |
| B09 | Cancel, stop uncertainty and retry | Cancelled Run does not finish an open resumable Task; no auto-resume. |
| B10 | Proved no-change completion | Finished/no-change, no fabricated human acceptance or merge. |
| B11 | Accepted, uncommitted, committed, pushed, PR open | Delivery throughout, with exact next operation. |
| B12 | PR closed without merge | Not delivered; attention/reconciliation as evidence permits. |
| B13 | Two repositories at different milestones | One card, honest partial progress, full-set counts. |
| B14 | One repository closed, another PR open | Still Delivery, retained achievements visible. |
| B15 | All integrated versus partly integrated/rest closed | Delivered versus partially-delivered-closed are distinct. |
| B16 | Legacy Apply, missing legacy evidence, mixed delivery modes | Applied locally distinct from merge; missing facts never imply success. |
| B17 | Cleanup pending/failure or cleanup after abandonment | Delivery outcome and maintenance remain separate. |
| B18 | Historical accepted result and current successor work | Historical success cannot end current work. |
| B19 | Archive/restore, archived parent, unresolved ownership | Visibility/read-only rules preserved; no success inferred. |
| B20 | Missing summary, contradictory counts/lineage, unknown status | Visible reconciliation row/count, safe deep link, no invented action. |
| B21 | Scope filters, pagination and concurrent refresh | No cross-project leakage, duplicate cards, page-only totals or late-scope overwrite. |
| B22 | Network loss, reconnect, restart and stale versions | Honest freshness; detail rereads before command; no stale mutation. |
| B23 | Leave Run before verification; open two views/tabs | Authorized work continues exactly once; GET never starts execution. |
| B24 | Source and S6 local app; tab close/menu reopen/shutdown | Existing service/preview ownership and recovery behavior unchanged. |
| B25 | Keyboard, narrow viewport and English/Chinese | Equivalent actions, retained focus, readable labels, no drag requirement. |
| B26 | Task/repository scale and repeated board reads | Bounded requests/payload; no model, worktree, diff or log fan-out. |
| B27 | Existing direct New Task and detailed delivery journey | No forced board detour; existing confirmation/security checks remain. |

Run focused unit/API/UI tests plus the code gate in [Contributing](../CONTRIBUTING.md):

```sh
make test
make verify
make vet
npm run e2e
git diff --check
```

Include the maintained `npm run e2e:public` journey. Real Pi validation remains
separate via `npm run test:e2e:pi-local-connected`; explicitly report blocked or
skipped real/macOS cases. Exercise relevant Local Connected/Isolated Local paths
without inventing new platform support. Record actual tested source and fixture
scope, not private credentials, machine paths or raw acceptance evidence.

Compare the board with the retained list on a fixed multi-Room fixture: steps to
find the next required action, missed attention items and manual status edits.
Record request count, payload and observed response latency with dataset/host
context. Correctness and zero duplicate status maintenance are required; do not
invent latency targets or claim improved efficiency before measurement.

## 11. Deferred extensions

**V1, demand-gated before/around M3:** lightweight work drafts and ordering only
when recording future work is a repeated need. Draft creation is not execution
authorization; do not create worktrees, call models or freeze a stale execution
base at capture time. Promotion must establish a traceable relation to the real
Task without two independently editable requirement copies.

**M3:** people/Agent assignment, claims, handoff, delegation and dependencies only
with membership, revocation, concurrency and authority rules. An assignee alone
is not authorization. **M4:** queued/background/remote work, budgets and
notifications with real execution ownership. None is hidden work in V0.

## 12. Original implementation handoff

Read this English specification with [Roadmap](../ROADMAP.md),
[Contributing](../CONTRIBUTING.md) and the source references above. The Chinese
translation carries the same scope. Public documents contain requirements and
sanitized status only. Do not add private handoffs/evidence to this PR or change
the repository's publication exclusions.

Retained original scope request; completed work does not need to be restarted:

```text
Implement Chora M2-S6A Native Task Board V0 from ROADMAP.md and
docs/task-board-plan.md, following CONTRIBUTING.md.
Start from current main containing the planning change; inspect divergence from
baseline b93100d536c5866456efa9551a4fad92db5d8ee4 before editing.
Implement S6A-1, S6A-2 and S6A-3 in order, beginning with table-driven projection
and read-API tests. Preserve unrelated changes and all existing Task/Run/Review,
delivery, authorization, S5 preview and S6 lifecycle boundaries.
Do not implement Backlog, drag-to-change-state, assignment or a scheduler.
Cover B01-B27, including Run-to-board verification continuity and uncertain
states. Update the English plan/roadmap and Chinese translations with actual
implementation and verification status. Report changed files, checks and any
blocked real-environment acceptance. Do not mark skipped work PASS or create
releases, tags, installers or distribution claims as part of this task.
```


## 13. Implementation and verification

The implementation adds a pure shared projection, a transactional SQLite summary
reader and the read-only API, with no board-state migration. Project and Room
views share Board/List, URL filters, next-step links, stale-data handling and
bounded polling. Automatic verification remains service-owned; the redundant
Run-page trigger is removed and disconnect/two-view tests cover continuity.

Coverage is retained in the [projection tests](../internal/app/task_board_test.go),
[summary integrity tests](../internal/store/sqlite/task_board_test.go),
[multi-repository scale tests](../internal/store/sqlite/task_board_resource_scale_test.go),
[API tests](../internal/localweb/task_board_test.go),
[verification continuity tests](../internal/app/verification_continuity_test.go),
[UI tests](../web/src/ui/TaskBoard.test.tsx) and
[browser journeys](../e2e/task-board.spec.ts). Existing delivery and explicit
closure integration tests also assert board outcomes, including partial delivery,
closed PRs, cleanup and zero-change results with failed checks.

Observed on Apple Silicon macOS:

- A 1,000-Task/10-Room fixture without Run history used 11 summary queries;
  a 50-card page was 39,607 bytes. The observed summary read was 26.51 ms and
  full first-page derivation 25.72 ms. This is fixture evidence, not a capacity SLA.
- A populated SQLite fixture covered 120 Tasks across six active Rooms, eight
  repositories per Task, 960 results and delivery operations, 360 hosting
  observations, 120 Apply steps and 120 closures. Its directory held 125 Rooms
  and 512 repositories. Three rounds of three reads each used 11 queries per
  read; 50/100-card responses were 131,431–131,533 / 245,532–245,633 bytes.
  Observed normal summary-read times were 0.424–3.449 seconds while other gates
  ran on the host; normal and race checks passed. This tests bounded summary
  payload, pagination and no writes/external calls, not a latency promise.
- A fixed two-Room/three-Task browser fixture found both required plan reviews
  with no missed attention item, manual status edit, Run creation or model call.
  Board navigation used three clicks plus one filter change; retained Room lists
  used four clicks, including Back. Both opened the same owning Plan detail.
  The scripted elapsed times were 474 ms and 349 ms respectively; they do not
  establish a human-efficiency improvement. Across each navigation path, 22/37
  GET responses were observed, with 40,810/48,691 measured bytes respectively;
  one list-response body was unavailable and excluded from its byte total.
- A selected real Local Connected Pi journey exercised review, correction,
  local Apply, service restart and the resulting board outcome. Runtime-profile
  fixtures cover both Local Connected and Isolated Local projection; this slice
  does not claim a new real isolated-Agent qualification.
- A real app-preview browser journey retained the running preview while visiting
  the board and then stopped/cleaned it through its existing owner. A development
  macOS app bundle passed its 12-case packaged-service browser suite, including
  tab closure, reopening and restart. Unchanged native menu/quit interactions
  retain S6 acceptance; formal signing, notarization and distribution remain
  deferred. No release or installer is published by this slice.

Validation passed: `make test`, `make verify`, `make vet`, `npm run e2e`,
`npm run e2e:public`, publication/documentation checks and `git diff --check`.
The final filter-pagination correction additionally passed all Task Board
app/API tests in normal and race modes, the populated SQLite scale test in both
modes, and a rerun of the complete E2E and packaged-service suites. The complete
verification gate includes the final 33-file/289-test Web suite and production
build. Unchanged business tests retain the complete preceding gate evidence.
The standard E2E suite skips its scenario-specific OSS case; that same case
passes separately in the dedicated OSS stage. The selected real-Pi case passes
separately; unselected real scenarios are not claimed as new acceptance.
