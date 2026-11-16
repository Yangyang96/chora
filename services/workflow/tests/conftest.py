import os
from uuid import uuid4

import pytest
from psycopg import sql

from chora_workflow.storage import connect, initialize


@pytest.fixture
def dsn():
    base = os.environ.get("CHORA_WORKFLOW_TEST_DSN")
    if not base:
        pytest.skip("CHORA_WORKFLOW_TEST_DSN must point to a disposable PostgreSQL database")
    # Each test gets a dedicated schema; never touch existing application tables.
    schema = "test_" + uuid4().hex
    with connect(base) as conn:
        conn.execute(sql.SQL("CREATE SCHEMA {}").format(sql.Identifier(schema)))
    from psycopg.conninfo import make_conninfo

    isolated = make_conninfo(base, options=f"-c search_path={schema}")
    initialize(isolated)
    yield isolated
    with connect(base) as conn:
        conn.execute(sql.SQL("DROP SCHEMA {} CASCADE").format(sql.Identifier(schema)))
