# Workbench maintenance

[简体中文](workbench-maintenance.zh-CN.md)

> Use the exact source version and data layout recorded with your backup.
> Candidate acceptance reports record the executed restore and migration checks;
> a successful doctor check alone does not qualify a backup.

This guide covers the source-checkout Workbench on Apple Silicon macOS. It is
for the first public Alpha boundary: one local user, an explicit absolute Chora
source checkout, and an explicit absolute Chora data root. It does not define an
online backup service, cross-machine migration, or a general disaster-recovery
system.

The current development source uses SQLite schema 42; rc.10 uses schema 41.
The 41 → 42 transition adds Isolated Local authority and creates a consistent
`.chora-schema41-backup-*` snapshot before changing the database. Stop all owners
and make the whole-data-root backup below before upgrading. Rollback restores
the pre-upgrade data with matching code at the same absolute layout.

## Know which state Chora owns

`chora workbench --source /absolute/path/to/chora` uses
`$HOME/.chora/data` by default. If you launch with `--data`, that exact absolute
path is the data root. Record it; Chora does not migrate an existing instance
when a default changes.

A whole-data-root backup includes:

- `chora.db` and any `chora.db-wal` or `chora.db-shm` sidecars;
- `task-workspaces/`, including each Task's repository worktrees;
- `pi-sessions/`;
- `pi/` when Pi 0.85.1 was installed through Workbench (the installation is
  Chora-owned, while Pi's native configuration and credentials remain separate);
- `runtime/`, including retained review/resource patches and runtime records;
- older Chora-owned content under the same data root, including any retained
  `projects/` content.

The backup does **not** include:

- the Chora source checkout or its built Web UI;
- original repositories selected for Projects, their dirt, or their Git object
  databases and branches;
- a user-installed `pi`, its native configuration or credentials;
- `gh` configuration or credentials; or
- files outside the exact data root.

Task worktrees are linked to the original repositories' Git common directories.
The original repositories must remain at their recorded absolute paths with
their Git metadata and objects intact. A data-root archive alone cannot recover
a deleted or relocated original repository.

## Before maintenance

1. In Workbench, stop or finish active Runs and checks. Reconcile interrupted
   Apply, Commit, Push, PR, Merge, or cleanup operations. Do not turn an
   uncertain write into a retry by deleting state.
2. Preserve every dirty original checkout and dirty Task worktree. Maintenance
   must not run `git reset`, `git clean`, `git checkout`, `git worktree remove`,
   or a broad filesystem deletion.
3. Record the exact source revision and relevant prerequisites without copying
   credentials or environment variables:

   ```sh
   CHORA_SOURCE_ROOT="/absolute/path/to/chora"
   CHORA_DATA_ROOT="$HOME/.chora/data" # replace with the exact --data value when used

   git -C "$CHORA_SOURCE_ROOT" rev-parse HEAD
   go version
   node --version
   npm --version
   if command -v pi >/dev/null 2>&1; then pi --version; else echo "pi unavailable"; fi
   ```

4. Stop Workbench with `Ctrl-C` in the terminal that launched it. Wait for the
   command to exit. Stop any Pi process belonging to that Workbench session.
   Do not copy while Workbench or one of its session processes may still write
   the data root.

The Workbench source build requires Go 1.26+, Node.js 22.12+, and npm 11+.
Local Pi use requires Node.js 22.19+ and Pi 0.84.2 or newer. The real validated
Pi baseline was 0.85.1; the minimum does not qualify every later Pi version.

## Make a stopped whole-data-root backup

Choose a backup directory outside the Chora data root. The following macOS
commands archive the data-root directory as one unit, preserving any SQLite WAL
and SHM files that remain after shutdown. They do not modify original
repositories.

```sh
set -eu
umask 077

CHORA_DATA_ROOT="$HOME/.chora/data" # replace with the exact --data value when used
CHORA_BACKUP_DIR="$HOME/chora-backups/$(date -u +%Y%m%dT%H%M%SZ)"

test -d "$CHORA_DATA_ROOT"
mkdir -p "$CHORA_BACKUP_DIR"
CHORA_DATA_PARENT="$(dirname "$CHORA_DATA_ROOT")"
CHORA_DATA_NAME="$(basename "$CHORA_DATA_ROOT")"

/usr/bin/tar -C "$CHORA_DATA_PARENT" -cpf "$CHORA_BACKUP_DIR/data-root.tar" "$CHORA_DATA_NAME"
(
  cd "$CHORA_BACKUP_DIR"
  /usr/bin/shasum -a 256 data-root.tar > data-root.tar.sha256
)
```

Keep the archive, checksum, exact Chora source revision, and the launch
arguments together. Treat the archive as sensitive local data. Do not publish
it or attach it to an issue.

Copying only `chora.db` is not a supported backup. Chora uses WAL mode; copying
the database while omitting a live WAL can lose committed state, and copying
only SQLite omits Task worktrees, Pi sessions, and retained patch material.

## Restore with matching code and the same layout

Restore only while Workbench is stopped. The restored data root must have the
same absolute path as when the backup was made, and every referenced original
repository must still be available at its recorded absolute path. Do not extract
an archive over an existing data root.

First verify the archive:

```sh
CHORA_BACKUP_DIR="/absolute/path/to/the/backup"
(
  cd "$CHORA_BACKUP_DIR"
  /usr/bin/shasum -a 256 -c data-root.tar.sha256
)
```

Then retain the current data root as a reversible hold and extract the backup at
the original location:

```sh
set -eu
umask 077

CHORA_DATA_ROOT="$HOME/.chora/data" # the exact original absolute path
CHORA_BACKUP_DIR="/absolute/path/to/the/backup"
CHORA_RESTORE_HOLD="${CHORA_DATA_ROOT}.before-restore-$(date -u +%Y%m%dT%H%M%SZ)"

test -d "$CHORA_DATA_ROOT"
test ! -e "$CHORA_RESTORE_HOLD"
mv "$CHORA_DATA_ROOT" "$CHORA_RESTORE_HOLD"

CHORA_DATA_PARENT="$(dirname "$CHORA_DATA_ROOT")"
/usr/bin/tar -C "$CHORA_DATA_PARENT" -xpf "$CHORA_BACKUP_DIR/data-root.tar"
test -d "$CHORA_DATA_ROOT"
```

Start the exact matching Chora source revision and point it at the restored
root. The matching source must already have a built `web/dist`:

```sh
CHORA_SOURCE_ROOT="/absolute/path/to/the/matching/chora-source"
CHORA_DATA_ROOT="$HOME/.chora/data" # the exact restored path

cd "$CHORA_SOURCE_ROOT"
go run ./cmd/chora workbench \
  --source "$CHORA_SOURCE_ROOT" \
  --data "$CHORA_DATA_ROOT" \
  --port 8787
```

Before deleting or repurposing the held directory, verify in the UI:

- every Project, Room, Task, Attempt, Result and review history expected from
  the backup is present;
- every selected repository association and frozen base/check configuration is
  unchanged;
- each repository's Commit, Push, PR, Merge, cleanup and legacy Apply history is
  retained;
- retained Task worktrees open at the recorded paths and preserve dirty files;
- interrupted sessions remain visible and are reconciled or resumed through
  their existing UI actions.

If matching code cannot open the restored root, stop it. Preserve the failed
restoration for diagnosis, put the held directory back at the exact data-root
path, and use the code that matches that held data. Never try to repair a schema
mismatch by editing `schema_migrations` or by running an older binary against a
database already migrated by newer code.

## Isolated Local schema migration

Schema 41 → 42 is append-only and tested with a consistent SQLite backup,
foreign-key/integrity checks and restoration of the original BLOB bytes. Chora
creates `.chora-schema41-backup-*` before this transition. This database snapshot
does not replace a stopped whole-data-root backup. Roll back using the complete
pre-upgrade data and matching rc.10 source, never by editing the migration ledger.
For frozen isolated Tasks, retain the exact prepared Docker image as well as the
data; pruning that image stops those Tasks until the exact image is restored.
No online service or existing user data was migrated during development tests.

## Source updates and rollback

The narrowly tested transition above applies to schema 41. Pre-publication
databases from arbitrary source revisions have no general compatibility promise.

For a future update, proceed only when release notes identify all of the
following: source version/revision, source schema, target version/revision,
target schema, supported Pi/Node range, and a qualified migration path. Prepare
the target in a separate clean source checkout; do not reset, overwrite, or
reuse a dirty Chora checkout. Build it with the documented commands:

```sh
npm ci
npm run web:build
```

Before first start of the target code, stop active work and make the whole-root
backup above. A first start may migrate forward. If the update fails after a
migration, rollback means stopping the target, restoring the pre-update backup
at the same path, and starting matching old code. Switching only the executable
or source checkout is not a database downgrade.

Pi remains a separate native tool. Recheck `node --version` and `pi --version`
after an update. Start the selected Pi executable interactively, then use
`/login` and `/model` for native authentication and model selection; Chora does
not collect those credentials. Chora must not overwrite or remove a Pi found on
PATH, its configuration, sessions outside the Chora data root, or its credentials.

## Archive, cleanup, and removal boundaries

Task and Room archive actions hide durable records and can be reversible. They
do not delete files and do not replace a backup.

For delivered Task branches, repository-specific **Cleanup** requires a proven
merged PR and a fully clean Chora-owned Task worktree, including ignored files.
It removes that worktree while retaining its branch, Review and delivery history.

For an unwanted Result, **Close result** closes only the remaining eligible
repository results. It preserves earlier Commit, Push, PR, Merge and legacy Apply
facts. Closing does not delete files. Its separate **Clean up closed result**
action checks the exact closed result and retained content; unexpected changes
or extra files block removal. In a mixed-repository result, retained delivered
repositories remain outside that cleanup. Each removal requires its own preview
and confirmation, and partial cleanup can be reconciled per repository.

Do not manually delete Task worktrees or clean an active or uncertain operation.
Refresh/reconcile `writing` or `recovery_required` delivery first. Archive only
when the UI's current eligibility checks permit it; hiding history does not
resolve pending work or replace result closure.

Workbench can install the frozen Pi 0.85.1 package under `pi/` in the Chora data
root after an explicit user action. That directory is Chora-owned and is included
in a whole-root backup. A Pi already found on PATH, Pi native configuration and
credentials are not Chora-owned. Removing Chora source or Chora-owned data must
never remove that pre-existing Pi, its configuration or credentials, `gh`
configuration, an original repository, a Task branch, or user dirt. Keep the
data root unless you have separately decided to discard all Chora history and
have a verified backup.

## Redacted diagnosis and next actions

Run the bounded Workbench doctor with the same source and data roots used to
launch Workbench:

```sh
go run ./cmd/chora workbench doctor \
  --source "$CHORA_SOURCE_ROOT" \
  --data "$CHORA_DATA_ROOT"
```

Add `--json` for stable machine-readable output. The command opens SQLite
read-only and reports only the Chora build identity, schema/integrity state,
coarse DataRoot component availability, bounded record counts, and normalized
Go/Node/npm/Pi versions. It never emits private absolute paths, database rows,
environment variables, credentials, native transcripts or repository content.
Exit 0 means the inspected schema and prerequisites are ready; exit 1 includes
a stable finding code and a safe next action. The historical `chora doctor`
command remains the installed/M1 preflight and is not Workbench data diagnosis.
Neither doctor proves that a backup or restore is valid.

For a support report, share only:

- the redacted `chora workbench doctor` output (or its `--json` form);
- `uname -sm`, `sw_vers -productVersion`, and the relevant redacted
  `gh --version` when GitHub delivery is involved;
- the UI's stable state/reason, the action attempted, and the first concise,
  redacted error; and
- whether the data root, original repository and Task worktree are present,
  without their private absolute paths.

Never attach the database, data-root archive, OAuth files, Pi/gh configuration,
environment dumps, native transcripts, repository contents, or full private
paths.

Use these recovery directions:

| Symptom | Safe next action |
|---|---|
| Original repository or Task path is missing/stale | Stop; restore the exact recorded path or backup. Do not relink or recreate over retained data. |
| A check failed | Keep its declared argv, working directory and provenance; fix the cause in the Task worktree or request a new Agent attempt. Do not mark it verified manually. |
| A session was interrupted | Restart with the same source and data root; use the visible Resume/Refresh/reconcile action. Keep `pi-sessions/`. |
| Push, PR, Merge or Apply outcome is uncertain | Refresh/reconcile first. Do not replay the write or clean the worktree. |
| Target branch advanced | Refresh the target state and create a newly reviewed delivery authority when required. An old Review does not authorize changed content. |
| Wrong execution profile | Stop the attempt and select the intended disclosed profile before a new attempt. Local Connected remains an explicit no-Sandbox choice. |
| Matching code refuses the schema | Stop. Use matching code with its matching backup; do not edit the migration ledger or attempt an in-place downgrade. |
