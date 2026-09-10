# Plan: Task-owned managed worktrees

## Outcome

Replace the single global Apply target with one durable, deterministic linked
Git worktree per real Task, where every Task worktree is itself a direct sibling
of the main repository.

## Milestone W1 — durable binding

- Add a Task/worktree domain lifecycle and SQLite migration.
- Persist repository identity, pinned base revision, safe relative locator,
  configured-root fingerprint, state/version/reason/timestamps.
- Enforce immutable identity, CAS transitions, no deletion, and migration replay.

Acceptance: fresh and upgraded databases pass focused domain/store tests.

## Milestone W2 — product integration

- Add a Task worktree manager port.
- Ensure real Task workspace after the Task transaction and on exact command
  replay.
- Resolve Patch Apply by Task identity.
- Replace `--apply-target` with one explicit Git repository input and derive a
  direct sibling `../<repository-name>-<task-id>-<topic>` path automatically.
- Create/validate linked worktrees using argument-only Git commands, exact common
  Git directory, pinned HEAD, safe paths, and unchanged index.
- Recover provisioning/recovery bindings on restart without redirecting legacy
  attempted work.
- Project a friendly relative workspace identity without exposing host paths.

Acceptance: two Tasks create two distinct worktrees; applying one cannot alter
the other or the main working tree; replay and restart are idempotent.

## Milestone W3 — installed acceptance

- Update installed harnesses and operator documentation.
- Migrate the retained accepted worktree to
  `<repository-parent>/<repository-name>-<task-id>-<topic>/` with
  `git worktree move`.
- Build a fresh candidate and run one public real Task through Accept & Apply.
- Reopen both historical and new applied evidence after restart.
- Run `make test`, `make verify`, `make vet`, `npm run e2e`, and
  `git diff --check`, followed by one consolidated independent review.

## Exclusions

No branch, stage, commit, push, MR, merge, publication, release, deployment,
automatic deletion, M2, or arbitrary-repository expansion.
