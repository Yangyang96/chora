# PostgreSQL checkpoint write audit

Audited the installed source of `langgraph-checkpoint-postgres==3.2.0` (full saver,
not shallow), with `langgraph-checkpoint==4.3.1`, `langgraph==1.2.20` and
`psycopg==3.3.9`. These distributions declare Python >=3.10; this package narrows
that to >=3.12,<3.14. `uv.lock` fixes all transitive versions and hashes. Local
execution validates Python 3.12 / PostgreSQL 17, not every advertised platform.

Sources: [package release](https://pypi.org/project/langgraph-checkpoint-postgres/3.2.0/),
[LangGraph release](https://pypi.org/project/langgraph/1.2.20/),
[upstream checkpoint repository](https://github.com/langchain-ai/langgraph/tree/main/libs/checkpoint-postgres),
[psycopg transaction semantics](https://www.psycopg.org/psycopg3/docs/basic/transactions.html).
The installed, locked sources below are the audit authority; the repository's
`main` link is navigation, not a fixed source revision.

## Entry matrix

Paths are relative to the installed `langgraph/checkpoint/postgres` package.

| Entry | Actual write path in locked source | M0 handling |
| --- | --- | --- |
| `PostgresSaver.put` | `__init__.py:268`; `_cursor(pipeline=True)`; blob `executemany`, checkpoint `execute` | Bound-thread check, then epoch row lock + both mutations in one transaction |
| `PostgresSaver.put_writes` | `__init__.py:352`; `_cursor(pipeline=True)`; `INSERT ... DO NOTHING` or special-channel upsert `executemany` | Same transaction; both insert and conflict-update branches tested with stale epoch |
| `AsyncPostgresSaver.aput` | `aio.py:237`; async cursor; blob batch + checkpoint write | Equivalent async transaction + epoch lock |
| `AsyncPostgresSaver.aput_writes` | `aio.py:312`; async cursor and batch | Equivalent async transaction + epoch lock |
| Async saver's synchronous `put` / `put_writes` | `aio.py:674,699`; dispatch to async methods on owner event loop | Reach overrides; invoked from a different thread in real PG tests |
| `delete_thread` / `adelete_thread` | `__init__.py:386`, `aio.py:345`; delete checkpoints, blobs, pending writes | Explicitly rejected on worker savers, including async synchronous bridge |
| `prune` / `aprune`, `delete` strategy | `__init__.py:409`, `aio.py:368`; `PRUNE_DELETE_THREADS_SQL` | Explicitly rejected |
| `prune` / `aprune`, `keep_latest` strategy | Same entries; `_execute` / `_aexecute` with `_keep_latest` in `base.py`; updates/deletes across checkpoint tables | Explicitly rejected; nested isolation assumptions are not compatible with this narrow cursor wrapper |
| `setup` (sync / async) | `__init__.py:90`, `aio.py:95`; DDL, migrations and `checkpoint_migrations` writes | Rejected on worker savers; separate offline `init-db` administrative command uses upstream saver |
| `copy_thread`, `delete_for_runs`, async equivalents | Inherited base methods in checkpoint 4.3.1 raise `NotImplementedError` | No actual write path; unsupported |
| Sync saver's `aput`, `aput_writes`, async deletion/pruning | Inherited base methods raise `NotImplementedError` | Unsupported; use `AsyncFencedSaver` for async calls |
| `get_tuple`, `list`, delta history reads | SELECT paths; `_migrate_pending_sends` adjusts loaded memory, not database | No database mutation; adapter cursor conservatively also checks epoch on its reads |
| Shallow savers | Separate classes in `shallow.py` | Not selected, instantiated or supported by this harness; cannot substitute without a new audit |

No upstream SQL is copied. The subclasses override `_cursor` and the two write
entry methods to bind one thread, then reuse actual upstream serialization and
DML. Administrative writes are excluded explicitly. The serializer uses an empty
additional msgpack module allowlist and does not enable pickle fallback.

## Atomicity and evidence

Dedicated idle autocommit connections are required. External transactions,
pools and pipelines are rejected. Under the saver's lock, `_cursor` opens a PG
transaction and executes `SELECT owner_epoch ... FOR UPDATE`. The row stays locked
through actual upstream DML and commit/rollback. A concurrent claim updates that
same row, so it cannot commit takeover between the check and mutation. After a
successful takeover, the old epoch is rejected before any DML. Network I/O does
not hold this row lock; the operation journal uses separate short transactions.

`tests/test_checkpoint.py` exercises real PostgreSQL:

- Negative control: ordinary epoch SELECT, takeover on another connection, then
  upstream `PostgresSaver.put` succeeds with stale ownership. Check-then-write is
  insufficient.
- Corrected race: pause after the adapter's epoch check, attempt takeover on a
  second connection, observe PostgreSQL lock timeout, release the old transaction,
  then takeover succeeds and subsequent old writes fail. Both checkpoint and
  pending-write mutations run through this schedule.
- Current epoch succeeds; stale epoch fails for checkpoint, pending insert and
  special-channel upsert. Assertions read actual tables, not just wrapper calls.
- A blob insert followed by a checkpoint constraint failure rolls back the blob.
  A pending-write batch whose second row violates a constraint rolls back its
  first row. Sync and async tests cover rollback and old-owner refusal.
- Async synchronous bridges reach the same protected paths; management methods
  reject without changing tables.

This is coordination among trusted code using these adapters, not protection
against arbitrary SQL, malicious workers, database owners or alternate savers.
Application roles and raw SQL access remain separate authorization concerns.
M0 still runs one advancing worker per workflow. Full M6 leases, automatic
scheduling, fencing of external effects and dual-worker end-to-end recovery are
not implemented or certified by these tests.

## Locked-source fingerprints

SHA-256 of installed source files (not generated application artifacts):

| File | SHA-256 |
| --- | --- |
| `__init__.py` | `f7329429ea278c02a3dc5848a08e15f11c2ebf954b142d9b5ef1fc414c22a2b0` |
| `aio.py` | `4fb93dd257ecc7e0d604d438d265cdb369b48149d9e169fbba4e49aa89ff6462` |
| `base.py` | `9b2b96b31cf494358d985eab1fe2f010b7f100b14b33299b6ecf7c4764c46d15` |
| `shallow.py` | `78d8cf4576a2d642aec49719e9089afa19165c53d54bf65f6ba737ec35b5eac8` |

Any upgrade requires checking new mutation entries and re-running the real PG
race and rollback suite. Do not infer compatibility from a successful import.
