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
elif sys.argv[2] in {
    "crash-after-decision",
    "crash-after-resume",
    "crash-before-terminal",
    "resume-decision",
}:
    from threading import Event

    from chora_workflow.checkpoint import FencedSaver
    from chora_workflow.storage import Journal

    if sys.argv[2] == "crash-after-decision":
        original_bind = Journal.bind_decision

        def crash_after_bind(self, *args):
            original_bind(self, *args)
            os._exit(78)

        Journal.bind_decision = crash_after_bind
    elif sys.argv[2] == "crash-after-resume":
        original_write = FencedSaver.put_writes

        def crash_after_write(self, config, writes, task_id, task_path=""):
            result = original_write(self, config, writes, task_id, task_path)
            if any(channel == "__resume__" for channel, _ in writes):
                os._exit(79)
            return result

        def wait_for_durable_resume(self):
            Event().wait(10)
            raise RuntimeError("resume writer failed to reach crash barrier")

        FencedSaver.put_writes = crash_after_write
        Journal.check = wait_for_durable_resume
    elif sys.argv[2] == "crash-before-terminal":
        original_put = FencedSaver.put
        original_write = FencedSaver.put_writes
        output_committed = Event()

        def record_output(self, config, writes, task_id, task_path=""):
            result = original_write(self, config, writes, task_id, task_path)
            if any(
                channel == "status" and value in {"accepted", "rejected"}
                for channel, value in writes
            ):
                output_committed.set()
            return result

        def crash_before_terminal(self, config, checkpoint, metadata, new_versions):
            if checkpoint["channel_values"].get("status") in {"accepted", "rejected"}:
                if not output_committed.wait(10):
                    raise RuntimeError("output writer failed to reach crash barrier")
                os._exit(80)
            return original_put(self, config, checkpoint, metadata, new_versions)

        FencedSaver.put = crash_before_terminal
        FencedSaver.put_writes = record_output
    before = worker.inspect(sys.argv[3])
    snapshot = worker.resume(sys.argv[3], {**before.values["gate"], "decision": sys.argv[4]})
elif sys.argv[2] == "submit":
    snapshot = worker.submit(sys.argv[3], "What is Chora?")
else:
    before = worker.inspect(sys.argv[3])
    snapshot = worker.resume(sys.argv[3], {**before.values["gate"], "decision": "accept"})
print(json.dumps({"state": snapshot.values, "interrupts": [i.id for i in snapshot.interrupts]}))
