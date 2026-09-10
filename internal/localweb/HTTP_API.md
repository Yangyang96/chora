# Local HTTP access

The actual `Server.Handler()` applies admission to all routes, including reads,
static content, native desktop actions, and future SCM routes. Production launchers
bind to `127.0.0.1`; this API is not a network deployment or authentication system.

Use the displayed local URL. Host must be `localhost` or a literal loopback IP,
with an optional valid port (bracket IPv6). Custom DNS names, forwarding headers,
and reverse-proxy origins do not grant access. An Origin, when present, must be
exactly the request's scheme and Host, including port; aliases are not equivalent.
Malformed, null, multiple, and mismatched origins are rejected. Sec-Fetch-Site,
when present, must be `same-origin` or `none`; `same-site` is insufficient.

Local CLI/API clients may omit Origin and Sec-Fetch-Site. All mutating methods
require `Content-Type: application/json`, even with an empty optional body.
For example, a read-only request and a readiness refresh are:

```sh
curl http://127.0.0.1:8787/api/status
curl -X POST -H 'Content-Type: application/json' \
  http://127.0.0.1:8787/api/readiness/refresh
```

Nonempty bodies must contain one JSON object, at most 1 MiB including whitespace.
Trailing values/garbage, null, arrays, unsupported media types, non-UTF-8 declared
charsets, and Content-Encoding are rejected before endpoint work. Endpoint field
validation and idempotency requirements still apply. Empty bodies are accepted
only by endpoints with optional input. Error responses use the normal JSON error
shape: 403 for authority, 415 for media, 413 for size, 400 for framing.

This boundary prevents browser cross-site access; it does not restrict other
processes already running as the local user. It does not replace native Pi
permissions, explicit Local Connected disclosure, or human Review/Apply.

## Automatic Agent retry

Run detail, Run history, Task summaries, and Room workspace responses may include
`automaticRetry` with `state` (`pending`, `retrying`, `exhausted`, or `blocked`),
`retriesUsed`, fixed `maxRetries: 2`, and `lastFailureReason`. Chora automatically
retries only the classified `runtime_output_limit_exceeded`, `attempt_timeout`,
and `runtime_exit_nonzero` failures. Cancellation, authentication/model access,
policy, integrity, uncertain-cleanup, Review, and Apply failures remain human
recovery boundaries. An explicit Retry resets the budget and enables this policy
for an older Run; historical failures are not started during an upgrade.

A blocked, already prepared Attempt with no runtime can be explicitly continued
through the same Retry endpoint. This starts the saved Attempt fresh, preserves
its immutable instructions and remaining budget, and clears its prior block via
the persisted start event. The UI calls this “Continue prepared retry” rather than
offering editable instructions. Pending retries can be cancelled; recovery with
unproven process cleanup cannot use inactive cancellation.

## Project settings and Local Connected Tasks

`GET /api/projects/{projectID}/settings` returns the persistent Project's settings,
not Room-local settings. A detected draft has `version: 0`. Save with
`PUT /api/projects/{projectID}/settings` and a JSON object containing `version`,
`writableFiles`, `writableDirectories`, `verificationCommands` (each has `argv`
and repository-relative `workingDirectory`), and explicit `noChecks`.
The version is compared atomically; stale writes return conflict. Check working
directories must exist in the selected committed base. Scope excludes `.git`,
path escape, symlink/submodule traversal, binary and LFS content. Ordinary new
text files and bounded directories are supported.

For a real Pi Local Connected Task, `POST /api/rooms/{roomID}/tasks` must include the displayed
`projectSettingsVersion`. Missing/stale versions and caller-supplied
`realSpecCoding` are rejected before Task creation. The server freezes the
selected Project settings and current committed base into the Task contract;
later settings changes affect only new Tasks.

A complete Pi result with no file changes has no Patch or Apply action. With
passing configured checks (or explicit `noChecks`) the Run is `completed` and
exposes an immutable `result` with outcome `completed_no_change`; explicit
no-check remains unverified. Failed or unobserved configured checks retain the
Task for recovery, with `checks_failed` or `checks_incomplete` result outcome.
Pi check results and cost estimates are provider-reported, not independently
verified or billing records.

## Review comments

`comment` on both Review endpoints is persisted in
`review_decisions.comment` and returned through `reviewHistory[].comment` and `review.comment`.
Accept and Reject both retain it, including when a later Apply or Retry fails.
Accept/Apply does not send it to the Agent, create a Git commit message, or publish a
GitHub comment. The UI's “Ask Agent to fix” first records a rejected review, then
submits the explanation as `instructions` to Retry; the selected Agent (Pi for Local Connected) receives the persisted
retry delta when the next attempt starts. Review alone does not start the Agent.

The form is not autosaved. Blank submissions receive the UI's default review
explanation. “Change requirement” currently opens a new Task form and neither
submits this review nor carries its unsaved text into that form. Migration 0036 renames the
previous `reviewer_note` column without changing its contents or review IDs.
Old clients may still submit `note`; conflicting `comment` and `note` values are
rejected. Responses use `comment`. Historical command replay encodings retain
their original keys so existing idempotency records remain valid.

## Task branch development and Review (current stage)

New write resources select a target ref and freeze its commit/tree, then use an
independent Task branch and worktree. Review accepts the immutable Result and
retains uncommitted contents. Result repository metadata includes `deliveryMode`,
`taskBranch`, and optional proven `worktreePath` outside the Result digest.
For a Result containing any `task_branch` repository, `canAcceptAndApply` and
`canApplyPatch` are false; POST `/api/v2/runs/{runID}/apply` returns403. All-legacy
Results retain their existing Apply behavior. Delivery preview/confirm/refresh are
active and use the existing same-origin command authorization. GET delivery is
read-only and does not implicitly refresh remote state.

## Task branch delivery contract

`GET /api/rooms/{roomID}/workspace` includes optional `tasks[].delivery` for
accepted Task-branch runs: `repositories: [{repoId, name, status}]`. It projects
the same durable operations and hosting observations as the delivery panel,
without reading Git/worktree contents or contacting hosting. `unavailable: true`
means evidence could not be loaded; it must not be displayed as completed.
Run/Task lifecycle statuses remain unchanged. Clients exclude `no_change`
repositories from delivery totals and display partial progress explicitly.

New multi-repository Tasks accept `resources[].targetRef`, a full existing local
`refs/heads/*` ref. An omitted target uses only an explicitly saved repository
default; without one, creation requires a selection. The Task freezes the selected
commit/tree/ref and its independent `chora/<task-id>/<repo-id>` branch. Original
checkout HEAD/index/files are not switched. Historical absent-mode snapshots and
detached worktrees keep their existing behavior and immutable digests.

- `GET /api/v2/repositories/{repoID}/branches?after=&limit=50` lists paginated local
  refs and commits (maximum 100 per page).
- `GET /api/v2/repositories/{repoID}/delivery-defaults` returns `targetRef`,
  `version`, `suggestedTargetRef`, and `reason`. A suggestion currently comes only
  from unambiguous cached remote-HEAD metadata and is labeled as unconfirmed
  against the current remote. It is never automatically saved.
- `PUT` on the same path takes `expectedVersion` and `targetRef`; it saves a
  separate repository default. A Task override never writes this setting.
- `GET /api/v2/runs/{runID}/delivery` returns per-repository status/history and
  hosting capabilities. It performs no remote mutations or implicit refresh.
- `POST .../delivery/preview` takes `repoId`, the **Run** `expectedVersion`,
  `resultDigest`, `kind: "commit" | "push" | "pr" | "merge" | "cleanup"`, with `message`, `remote`, or PR `title`/`body` as appropriate.
  Commit selects the entire accepted repository patch; the preview lists exact
  files and message. Push shows the actual single push URL, Task destination ref,
  local commit and observed remote heads. GitHub capabilities use installed/authenticated gh for github.com same-repository PRs. PR previews show repository, head/base branches and SHAs, title/body; merge shows exact PR and merge method. Cleanup previews show exact worktreePath and HEAD and require merged/clean proof.
- `POST .../delivery/confirm` takes `operationId`, Run `expectedVersion` and
  `resultDigest`. The operation's own `version` is a separate CAS version, not
  the Run version. Confirmation revalidates the stored exact preview, records
  intent before side effects, and does not repeat an already attempted operation.
- `POST .../delivery/refresh` takes `repoId`, Run `expectedVersion` and
  `resultDigest`. It reconciles uncertain Git/hosting/cleanup operations and observes recorded PR state;
  it never retries the mutation. Failed or stale operations require a new preview.

Mutations use JSON and the existing `Idempotency-Key` command header. Acceptance
must bind the current immutable Result digest. An unresolved Apply or delivery
operation blocks conflicting work. Content or target drift blocks confirmation;
no force push, implicit rebase, or cross-repository atomic delivery is provided.
New Task-branch Review acceptance records the review without automatic Apply;
legacy local Apply remains separate. Review does not perform delivery automatically. Commit message is separate from Review Comment.

Capabilities include hosting/createPR/merge/cleanup, provider and an unavailable reason. Per-repo status includes pr_open, pr_closed, merged and cleaned; partial delivery never completes all repositories. Cleanup uses non-force Git worktree removal after fresh merged proof and no modified/staged/untracked/ignored files; local/remote branches and review history remain.


### AI delivery drafts

`POST /api/v2/runs/{runID}/delivery/draft-context` reads the accepted repository's
current draft evidence and returns `fingerprint`, a deployment/actor-scoped
`owner` key, available PR `templates`, and context `warnings`. Body: `repoId`,
`expectedVersion`, `resultDigest`, and `kind` (`commit` or `pr`). It does not invoke
the model or persist a delivery operation. PR context includes read-only remote
identity checks and requires a successfully recorded Commit and Push.

`POST /api/v2/runs/{runID}/delivery/message` accepts the same fields plus
`fingerprint`, optional `generationId`, `template`, `feedback`, and
`current: {message, title, body}`. Omitted `kind` retains commit compatibility.
Both kinds default to English, independent of UI locale; legacy `language` input
does not override this default. Explicit revision feedback may request another
language. Commit returns a substantive `message` (subject + blank line + body);
PR returns `title` and Markdown `body`. Responses include provider/model
provenance and the source `fingerprint`.

Automatic identical requests share a bounded, process-local cache and concurrent
model call. A fresh `generationId` requests new text; retries retain the same ID
and inputs. Draft generation never stages, commits, pushes, creates a PR, merges,
or grants any such authority. The existing preview/confirm endpoints retain
exact content and destination checks. Model completion is rechecked against the
current source fingerprint before returning text.

Repository rules and templates are read from regular Git blobs at the frozen
base, never executed or followed through worktree symlinks. PR drafts use the
full merge-base-to-head diff and reject changes outside the accepted Review.
The first slice explicitly refuses automatic generation for diff bodies over
128 KiB or binary patches; it does not silently truncate them. Context limits
and unread conventions are reported. Check records retain provider provenance;
added tests do not imply a passing test run.

The browser stores editable text and its source fingerprint under the returned
owner plus Run/Repo/Result/kind. Reload/reopen restores it locally, without model
regeneration; storage failure is shown. This is browser-local recovery, not
cross-device synchronization. Changed sources mark text stale. Explicit
regeneration/revision proposes a candidate; adopting it is reversible and never
silently overwrites intervening human edits.
