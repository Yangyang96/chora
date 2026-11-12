"""Test-only subprocess entrypoint. Uses the production graph and PostgreSQL storage."""

import json
import os
import sys

from chora_workflow.worker import Worker
from fixtures import FixtureCatalog, FixtureModel

worker = Worker(sys.argv[1], FixtureModel(fail_first=True), FixtureCatalog())
if sys.argv[2] == "crash-after-receipt":
    from chora_workflow.graph import Research

    original = Research.model_node

    def crash(self, state):
        original(self, state)
        os._exit(77)

    Research.model_node = crash
    worker = Worker(sys.argv[1], FixtureModel(), FixtureCatalog())
    worker.submit(sys.argv[3], "What is Chora?")
elif sys.argv[2] == "submit":
    snapshot = worker.submit(sys.argv[3], "What is Chora?")
else:
    before = worker.inspect(sys.argv[3])
    snapshot = worker.resume(sys.argv[3], {**before.values["gate"], "decision": "accept"})
print(json.dumps({"state": snapshot.values, "interrupts": [i.id for i in snapshot.interrupts]}))
