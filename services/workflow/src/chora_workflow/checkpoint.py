"""Version-pinned PostgreSQL saver; epoch check and writes share one transaction.

This is coordination between trusted workers, not a database authorization boundary.
Use a dedicated connection per saver; pools, external transactions and pipelines are
intentionally excluded. Administrative writes are not worker capabilities.
"""

from contextlib import asynccontextmanager, contextmanager

import psycopg
from langgraph.checkpoint.postgres import PostgresSaver
from langgraph.checkpoint.postgres.aio import AsyncPostgresSaver
from langgraph.checkpoint.serde.jsonplus import JsonPlusSerializer
from psycopg.rows import dict_row
from psycopg.pq import PipelineStatus, TransactionStatus


class StaleOwner(RuntimeError):
    pass


class UnsupportedWrite(RuntimeError):
    pass


OWNER_SQL = "SELECT owner_epoch FROM workflow_owners WHERE thread_id = %s FOR UPDATE"


def verify_owner(row, epoch):
    if row is None or row["owner_epoch"] != epoch:
        raise StaleOwner("owner_epoch does not match")


def verify_connection(conn):
    if (
        not conn.autocommit
        or conn.info.transaction_status != TransactionStatus.IDLE
        or conn.info.pipeline_status != PipelineStatus.OFF
    ):
        raise ValueError("saver requires an idle autocommit connection without a pipeline")


class BoundThread:
    def check_thread(self, config):
        if config["configurable"]["thread_id"] != self.thread_id:
            raise ValueError("saver is bound to another workflow")


class FencedSaver(BoundThread, PostgresSaver):
    def __init__(self, conn, thread_id: str, epoch: int):
        if not isinstance(conn, psycopg.Connection):
            raise TypeError("a dedicated psycopg connection is required")
        verify_connection(conn)
        super().__init__(conn, serde=JsonPlusSerializer(allowed_msgpack_modules=[]))
        self.thread_id, self.epoch = thread_id, epoch

    @contextmanager
    def _cursor(self, *, pipeline=False):
        # Deliberately ignore upstream's pipeline hint: transaction atomicity is required.
        with self.lock:
            verify_connection(self.conn)
            with self.conn.transaction(), self.conn.cursor(row_factory=dict_row) as cur:
                cur.execute(OWNER_SQL, (self.thread_id,))
                verify_owner(cur.fetchone(), self.epoch)
                yield cur

    def put(self, config, checkpoint, metadata, new_versions):
        self.check_thread(config)
        return super().put(config, checkpoint, metadata, new_versions)

    def put_writes(self, config, writes, task_id, task_path=""):
        self.check_thread(config)
        return super().put_writes(config, writes, task_id, task_path)

    def setup(self):
        raise UnsupportedWrite("run the separate database initialization command")

    def delete_thread(self, thread_id):
        raise UnsupportedWrite("worker deletion is disabled")

    def prune(self, thread_ids, *, strategy="keep_latest"):
        raise UnsupportedWrite("worker pruning is disabled")


class AsyncFencedSaver(BoundThread, AsyncPostgresSaver):
    def __init__(self, conn, thread_id: str, epoch: int):
        if not isinstance(conn, psycopg.AsyncConnection):
            raise TypeError("a dedicated async psycopg connection is required")
        verify_connection(conn)
        super().__init__(conn, serde=JsonPlusSerializer(allowed_msgpack_modules=[]))
        self.thread_id, self.epoch = thread_id, epoch

    @asynccontextmanager
    async def _cursor(self, *, pipeline=False):
        async with self.lock:
            verify_connection(self.conn)
            async with self.conn.transaction(), self.conn.cursor(row_factory=dict_row) as cur:
                await cur.execute(OWNER_SQL, (self.thread_id,))
                verify_owner(await cur.fetchone(), self.epoch)
                yield cur

    async def aput(self, config, checkpoint, metadata, new_versions):
        self.check_thread(config)
        return await super().aput(config, checkpoint, metadata, new_versions)

    async def aput_writes(self, config, writes, task_id, task_path=""):
        self.check_thread(config)
        return await super().aput_writes(config, writes, task_id, task_path)

    async def setup(self):
        raise UnsupportedWrite("run the separate database initialization command")

    async def adelete_thread(self, thread_id):
        raise UnsupportedWrite("worker deletion is disabled")

    async def aprune(self, thread_ids, *, strategy="keep_latest"):
        raise UnsupportedWrite("worker pruning is disabled")
