# Decision: Task-owned managed Git worktrees

Date: 2026-08-24
Status: Accepted

## Product decision

The installed product must not use one process-wide working tree as the Apply
target. Every real Task owns a separate linked Git worktree directly beside the
main repository:

```text
<code-directory>/
├── <repository>/
└── <repository-name>-<task-id>-<topic-slug>/
```

For a local installation, the main repository is
`<repository-parent>/<repository-name>/` and its Task worktrees are direct
siblings such as
`<repository-parent>/<repository-name>-<task-id>-<topic-slug>/`.

This follows Git's official worktree model: `git worktree add <path>` accepts an
explicit path, detached worktrees are appropriate when no branch is intended,
and moves must use `git worktree move` rather than ordinary filesystem
relocation. Keeping linked worktrees outside the main working tree prevents them
from appearing as Task content or untracked files in that repository.

Reference: https://git-scm.com/docs/git-worktree.html

## Lifecycle

1. A real Task transaction persists an immutable Task/worktree binding in
   `provisioning` state without storing an absolute host path.
2. After commit, Chora idempotently creates a detached linked worktree at the
   pinned installed Source revision.
3. Successful validation transitions the binding to `ready` by CAS.
4. Agent execution and independent Verification remain isolated. Human
   `Accept & Apply` resolves the target through Run → Task → binding and writes
   only that Task's worktree.
5. Restart reconciles persisted bindings with Git metadata. Identity drift,
   symlinks, foreign paths, wrong base revision, or ambiguous crash state fail
   closed.
6. Task/Room Archive does not delete a worktree. Applied uncommitted code remains
   user-owned until a future explicit promotion or discard decision.

## Naming

- Parent directory: exactly the main repository's parent directory.
- Worktree basename: repository name, immutable full Task ID, and a bounded topic slug.
- No milestone names, runtime paths, dates, or random acceptance-fixture names.
- The basename is branch-compatible, but M1 continues to use detached HEAD.

## Compatibility

Existing applied Patch Application rows and their target identities remain
immutable and readable. Chora must not silently redirect historical applied or
attempted work to a new target.

## Non-goals

This decision does not authorize branch creation, staging, commit, push, MR,
merge, publication, release, deployment, arbitrary repositories, or automatic
worktree deletion.
