import argparse
import json
import os

from .model import SDKModel
from .storage import initialize
from .tools import PublicCatalog
from .worker import Worker


def main():
    parser = argparse.ArgumentParser(description="Chora M0 local workflow harness")
    parser.add_argument(
        "command", choices=["init-db", "submit", "inspect", "resume", "stop", "events"]
    )
    parser.add_argument("--thread")
    parser.add_argument("--question")
    parser.add_argument("--decision-file", help="JSON decision, including gate_id and input_digest")
    args = parser.parse_args()
    dsn = os.environ.get("CHORA_WORKFLOW_DATABASE_URL")
    if not dsn:
        parser.error("CHORA_WORKFLOW_DATABASE_URL is required")
    if args.command == "init-db":
        initialize(dsn)
        print(json.dumps({"initialized": True}))
        return
    if not args.thread:
        parser.error("--thread is required")
    # Inspect/stop/events need no model credentials. Identity is only used on execution.
    model = SDKModel() if args.command in {"submit", "resume"} else None
    worker = Worker(dsn, model, PublicCatalog())
    if args.command == "submit":
        if not args.question:
            parser.error("--question is required")
        result = worker.submit(args.thread, args.question)
    elif args.command == "resume":
        decision = None
        if args.decision_file:
            with open(args.decision_file) as file:
                decision = json.load(file)
        result = worker.resume(args.thread, decision)
    elif args.command == "stop":
        worker.stop(args.thread)
        print(json.dumps({"stop_requested": True}))
        return
    elif args.command == "events":
        print(json.dumps(worker.read_events(args.thread)))
        return
    else:
        result = worker.inspect(args.thread)
    print(
        json.dumps(
            {
                "state": result.values,
                "next": result.next,
                "interrupts": [{"id": i.id, "value": i.value} for i in result.interrupts],
            }
        )
    )


if __name__ == "__main__":
    main()
