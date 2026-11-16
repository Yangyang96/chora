"""Subprocess-only fixture adapter for PG/Go integration, never a live model."""

import json
import os
import sys

from chora_workflow.product import describe, execute
from chora_workflow.worker import Worker
from fixtures import FixtureCatalog, FixtureModel

worker = Worker(os.environ["CHOR50_TEST_DSN"], FixtureModel(), FixtureCatalog())
command = json.load(sys.stdin)
print(json.dumps(describe(worker) if command["kind"] == "describe" else execute(worker, command)))
