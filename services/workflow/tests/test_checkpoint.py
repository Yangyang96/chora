import asyncio
from concurrent.futures import ThreadPoolExecutor
from contextlib import contextmanager
from threading import Event
from uuid import uuid4

import psycopg
import pytest
from langgraph.checkpoint.base import empty_checkpoint
from langgraph.checkpoint.postgres import PostgresSaver
from psycopg.rows import dict_row

from chora_workflow.checkpoint import AsyncFencedSaver, FencedSaver, StaleOwner, UnsupportedWrite
from chora_workflow.storage import Journal, connect

pytestmark = pytest.mark.postgres


def owner(dsn):
    thread = str(uuid4())
    Journal.submit(dsn, thread, {})
    return thread, Journal.claim(dsn, thread)


def data(thread):
    config = {"configurable": {"thread_id": thread, "checkpoint_ns": ""}}
    checkpoint = empty_checkpoint()
    checkpoint["channel_values"] = {"evidence": {"quote": "public"}}
    checkpoint["channel_versions"] = {"evidence": "1"}
    return config, checkpoint, {"source": "update", "step": 1}, {"evidence": "1"}


def counts(dsn):
    with connect(dsn) as conn:
        return [
            conn.execute(f"SELECT count(*) AS n FROM {table}").fetchone()["n"]
            for table in ("checkpoints", "checkpoint_blobs", "checkpoint_writes")
        ]


def test_real_mutations_epoch_pending_writes_and_rollback(dsn):
    thread, epoch = owner(dsn)
    with connect(dsn) as conn:
        saver = FencedSaver(conn, thread, epoch)
        config = saver.put(*data(thread))
        saver.put_writes(config, [("answer", "first")], "task")
        saver.put_writes(config, [("__error__", "first")], "error-task")
        assert counts(dsn) == [1, 1, 2]
        with pytest.raises(psycopg.errors.NotNullViolation):
            saver.put_writes(config, [("answer", "ok"), (None, "invalid")], "rollback-task")
        assert counts(dsn) == [1, 1, 2]
        with pytest.raises(ValueError, match="another workflow"):
            saver.put(*data(str(uuid4())))
        Journal.claim(dsn, thread)
        for action in [
            lambda: saver.put(*data(thread)),
            lambda: saver.put_writes(config, [("answer", "late")], "late-task"),
            lambda: saver.put_writes(config, [("__error__", "late")], "error-task"),
        ]:
            with pytest.raises(StaleOwner):
                action()
        assert counts(dsn) == [1, 1, 2]
    with connect(dsn) as conn:
        current = FencedSaver(conn, thread, epoch + 1)
        current.put_writes(config, [("answer", "current")], "current-task")
        assert counts(dsn) == [1, 1, 3]
        broken = list(data(thread))
        broken[1]["id"] = None  # Blob writes occur before checkpoint NOT NULL failure.
        broken[1]["channel_versions"] = {"evidence": "2"}
        broken[3] = {"evidence": "2"}
        with pytest.raises(psycopg.errors.NotNullViolation):
            current.put(*broken)
        assert counts(dsn) == [1, 1, 3]  # New blob was rolled back too.
        for action in [
            current.setup,
            lambda: current.delete_thread(thread),
            lambda: current.prune([thread]),
            lambda: current.prune([thread], strategy="delete"),
        ]:
            with pytest.raises(UnsupportedWrite):
                action()
        assert counts(dsn) == [1, 1, 3]


@pytest.mark.parametrize("pending", [False, True])
def test_check_then_takeover_race_has_negative_control(dsn, pending):
    thread, epoch = owner(dsn)
    # Reproduce the original unsafe design on a real upstream saver:
    # a read-only epoch check followed by takeover does NOT fence actual mutation.
    with connect(dsn) as conn:
        assert (
            conn.execute(
                "SELECT owner_epoch FROM workflow_owners WHERE thread_id=%s", (thread,)
            ).fetchone()["owner_epoch"]
            == epoch
        )
        next_epoch = Journal.claim(dsn, thread)
        config = PostgresSaver(conn).put(*data(thread))
        assert counts(dsn) == [1, 1, 0]  # Unsafe stale write succeeded.
        with pytest.raises(StaleOwner):
            FencedSaver(conn, thread, epoch).put(*data(thread))

    checked, release = Event(), Event()

    class PausedSaver(FencedSaver):
        @contextmanager
        def _cursor(self, *, pipeline=False):
            with super()._cursor(pipeline=pipeline) as cur:
                checked.set()  # Pause precisely AFTER check, BEFORE upstream SQL.
                assert release.wait(5)
                yield cur

    def write():
        with connect(dsn) as conn:
            saver = PausedSaver(conn, thread, next_epoch)
            if pending:
                saver.put_writes(config, [("answer", "ok")], "pending-race")
            else:
                saver.put(*data(thread))

    with ThreadPoolExecutor(max_workers=1) as pool:
        future = pool.submit(write)
        assert checked.wait(5)
        try:
            with connect(dsn) as conn:
                conn.execute("SET lock_timeout='150ms'")
                with pytest.raises(psycopg.errors.LockNotAvailable):
                    conn.execute(
                        "UPDATE workflow_owners SET owner_epoch=owner_epoch+1 WHERE thread_id=%s",
                        (thread,),
                    )
        finally:
            release.set()
        future.result(timeout=5)
    assert Journal.claim(dsn, thread) == next_epoch + 1
    with connect(dsn) as conn:
        with pytest.raises(StaleOwner):
            FencedSaver(conn, thread, next_epoch).put(*data(thread))


@pytest.mark.asyncio
async def test_async_actual_writes_rollbacks_and_stale_owner(dsn):
    thread, epoch = owner(dsn)
    async with await psycopg.AsyncConnection.connect(
        dsn, autocommit=True, row_factory=dict_row
    ) as conn:
        saver = AsyncFencedSaver(conn, thread, epoch)
        config = await saver.aput(*data(thread))
        await saver.aput_writes(config, [("answer", "async")], "task")
        assert counts(dsn) == [1, 1, 1]
        with pytest.raises(psycopg.errors.NotNullViolation):
            await saver.aput_writes(config, [("answer", "ok"), (None, "invalid")], "rollback-task")
        assert counts(dsn) == [1, 1, 1]
        broken = list(data(thread))
        broken[1]["id"] = None
        broken[1]["channel_versions"] = {"evidence": "2"}
        broken[3] = {"evidence": "2"}
        with pytest.raises(psycopg.errors.NotNullViolation):
            await saver.aput(*broken)
        assert counts(dsn) == [1, 1, 1]
        Journal.claim(dsn, thread)
        with pytest.raises(StaleOwner):
            await saver.aput(*data(thread))
        with pytest.raises(StaleOwner):
            await saver.aput_writes(config, [("__error__", "late")], "task")
        for action in [
            saver.setup,
            lambda: saver.adelete_thread(thread),
            lambda: saver.aprune([thread]),
        ]:
            with pytest.raises(UnsupportedWrite):
                await action()
        assert counts(dsn) == [1, 1, 1]


@pytest.mark.asyncio
async def test_async_sync_bridges_are_fenced(dsn):
    thread, epoch = owner(dsn)
    async with await psycopg.AsyncConnection.connect(
        dsn, autocommit=True, row_factory=dict_row
    ) as conn:
        saver = AsyncFencedSaver(conn, thread, epoch)
        config = await asyncio.to_thread(saver.put, *data(thread))
        await asyncio.to_thread(saver.put_writes, config, [("answer", "ok")], "task")
        Journal.claim(dsn, thread)
        with pytest.raises(StaleOwner):
            await asyncio.to_thread(saver.put_writes, config, [("answer", "bad")], "late")
        with pytest.raises(UnsupportedWrite):
            await asyncio.to_thread(saver.delete_thread, thread)
        with pytest.raises(UnsupportedWrite):
            await asyncio.to_thread(saver.prune, [thread])
        assert counts(dsn) == [1, 1, 1]
