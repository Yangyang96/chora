"""Synthetic contract and real Cernora CLI acceptance; no Agent execution."""

from __future__ import annotations

from copy import deepcopy
import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

from chora_adapter import AdapterError, adapt_record, canonical, export_record


def record() -> dict:
    return {
        "schemaVersion": "chora.cernora-completed-record/v1", "completed": True,
        "runId": "synthetic-run-attempt-1", "taskId": "synthetic-task", "caseId": "status",
        "conditions": {"agent": "synthetic-v1", "model": "synthetic-v1",
                       "prompt_version": "v1", "budget": None, "toolset_version": "v1"},
        "userTask": "Read the service status.", "finalAnswer": "The status is ready.",
        "events": [
            {"sequence": 1, "timestampMs": 0, "type": "user",
             "payload": {"text": "Read the service status."}},
            {"sequence": 2, "timestampMs": 10, "type": "tool_start",
             "payload": {"invocation_id": "call-1", "tool": "service",
                         "argv": ["service", "status"]}},
            {"sequence": 3, "timestampMs": 20, "type": "tool_end",
             "payload": {"invocation_id": "call-1", "exit_code": 0, "timed_out": False,
                         "evidence_id": "output-1", "stdout": '{"status":"ready"}'}},
            {"sequence": 4, "timestampMs": 30, "type": "final",
             "payload": {"text": "The status is ready."}},
        ],
        "claims": [{"path": "/status", "value": "ready", "evidence_ids": ["output-1"]}],
    }


class AdapterTests(unittest.TestCase):
    def test_adaptation_has_no_process_or_network_authority(self):
        def forbidden(*args, **kwargs):
            raise AssertionError("adapter attempted process or network access")

        with patch("subprocess.Popen", forbidden), patch("os.system", forbidden), \
                patch("socket.socket", forbidden), patch("socket.create_connection", forbidden):
            adapt_record(canonical(record()))

    def test_deterministic_mapping_preserves_unknown_budget_and_hashes_exact_bytes(self):
        source = record()
        source["events"][2]["payload"]["stdout"] = "状态 ready\n"
        outputs = [adapt_record(canonical(source)) for _ in range(3)]
        self.assertEqual(outputs[0], outputs[1])
        self.assertEqual(outputs[1], outputs[2])
        result = json.loads(outputs[0])
        self.assertIsNone(result["conditions"]["budget"])
        self.assertEqual(result["tool_output_digests"]["output-1"],
                         hashlib.sha256("状态 ready\n".encode()).hexdigest())
        self.assertEqual(result["events"][1]["payload"]["argv"], ["service", "status"])

    def test_missing_results_and_stdout_are_not_fabricated(self):
        source = record()
        source["events"].pop(2)
        source["events"][-1]["sequence"] = 3
        source["claims"] = []
        result = json.loads(adapt_record(canonical(source)))
        self.assertEqual(result["tool_outputs"], {})
        self.assertFalse(any(e["kind"] == "tool_result" for e in result["events"]))
        source = record()
        source["claims"] = []
        source["events"][2]["payload"].update(stdout=None, evidence_id=None)
        self.assertEqual(json.loads(adapt_record(canonical(source)))["tool_outputs"], {})

    def test_invalid_records_fail_closed(self):
        mutations = [
            lambda r: r.update(completed=False),
            lambda r: r.update(completed=1),
            lambda r: r.update(schemaVersion="agent-run-export/v1"),
            lambda r: r.update(secret="must-not-be-exported"),
            lambda r: r["events"][1].update(sequence=9),
            lambda r: r["events"][2].update(timestampMs=1),
            lambda r: r["events"][-1]["payload"].update(text="different final"),
            lambda r: r["events"][2]["payload"].update(invocation_id="absent"),
            lambda r: r["events"][2]["payload"].update(exitCode=0),
            lambda r: r["events"][2]["payload"].pop("stdout"),
            lambda r: r["events"][2]["payload"].update(stdout=None),
            lambda r: r["claims"][0].update(evidence_ids=["missing"]),
            lambda r: r["conditions"].pop("budget"),
        ]
        for mutate in mutations:
            with self.subTest(mutation=mutate):
                source = record()
                mutate(source)
                with self.assertRaises(AdapterError):
                    adapt_record(canonical(source))
        for raw in [b'{"completed":true,"completed":true}', b'\xff', b'{}{}',
                    b'{"x":NaN}', canonical(record())[:-1]]:
            with self.subTest(raw=raw[:40]), self.assertRaises(AdapterError):
                adapt_record(raw)

    def test_concurrent_calls_keep_observed_order(self):
        source = record()
        second = deepcopy(source["events"][1])
        second["payload"]["invocation_id"] = "call-2"
        second["timestampMs"] = 15
        source["events"].insert(2, second)
        for index, event in enumerate(source["events"]):
            event["sequence"] = index + 1
        result = json.loads(adapt_record(canonical(source)))
        self.assertEqual([e["kind"] for e in result["events"]],
                         ["user", "tool_call", "tool_call", "tool_result", "final"])

    def test_file_publication_never_overwrites_or_leaves_partial_output(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source, output = root / "record.json", root / "export.json"
            source.write_bytes(canonical(record()))
            export_record(source, output)
            original = output.read_bytes()
            self.assertEqual(output.stat().st_mode & 0o777, 0o600)
            with self.assertRaises(FileExistsError):
                export_record(source, output)
            self.assertEqual(output.read_bytes(), original)
            with self.assertRaises(FileExistsError):
                export_record(source, source)
            linked = root / "linked.json"
            linked.symlink_to(source)
            with self.assertRaises(OSError):
                export_record(linked, root / "rejected.json")
            output_link = root / "output-link.json"
            output_link.symlink_to(root / "absent.json")
            with self.assertRaises(FileExistsError):
                export_record(source, output_link)
            source.write_bytes(b'{"invalid":true}')
            with self.assertRaises(AdapterError):
                export_record(source, root / "rejected.json")
            self.assertFalse((root / "rejected.json").exists())
            self.assertFalse(list(root.glob(".chora-cernora-*")))

    def test_rejected_cli_does_not_echo_private_source_values(self):
        with tempfile.TemporaryDirectory() as directory:
            source, output = Path(directory) / "record.json", Path(directory) / "export.json"
            source.write_bytes(canonical({"private": "private-source-value"}))
            result = subprocess.run([sys.executable, str(Path(__file__).with_name("chora_adapter.py")),
                                     str(source), str(output)], capture_output=True, timeout=30)
            self.assertEqual(result.returncode, 3)
            self.assertNotIn(b"private-source-value", result.stderr)
            self.assertEqual(result.stdout, b"")
            self.assertFalse(output.exists())


class CernoraCLIAcceptance(unittest.TestCase):
    def test_pass_fail_and_missing_evidence_through_real_cli(self):
        adapter = Path(__file__).with_name("chora_adapter.py")
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            reference = root / "reference.json"
            reference.write_bytes(canonical({"/status": "ready"}))
            for path, expected_exit in [("pass", 0), ("fail", 1), ("missing", 3)]:
                with self.subTest(path=path):
                    source = record()
                    if path == "fail":
                        source["claims"][0]["value"] = "busy"
                        source["finalAnswer"] = "The status is busy."
                        source["events"][-1]["payload"]["text"] = source["finalAnswer"]
                        source["events"][2]["payload"]["stdout"] = '{"status":"busy"}'
                    if path == "missing":
                        source["events"].pop(2)
                        source["events"][-1]["sequence"] = 3
                        source["claims"] = []
                    native, exported = root / f"{path}.json", root / f"{path}-export.json"
                    native.write_bytes(canonical(source))
                    completed = subprocess.run([sys.executable, str(adapter), str(native), str(exported)],
                                               capture_output=True, timeout=30)
                    self.assertEqual(completed.returncode, 0, completed.stderr)
                    command = [sys.executable, "-m", "cernora", "agent-run", "inspect",
                               str(exported), "--reference", str(reference)]
                    inspections = [subprocess.run(command, capture_output=True, timeout=30)
                                   for _ in range(3)]
                    for inspected in inspections:
                        self.assertEqual(inspected.returncode, expected_exit, inspected.stderr)
                        self.assertEqual(inspected.stdout, inspections[0].stdout)
                    result = json.loads(inspections[0].stdout)
                    if path == "pass":
                        self.assertEqual(result["claims"]["accuracy"], 1.0)
                        self.assertEqual(result["state"], "evidence_available")
                    elif path == "fail":
                        self.assertEqual(result["claims"]["accuracy"], 0.0)
                    else:
                        self.assertEqual(result["state"], "inconclusive")
                        self.assertEqual(result["missing_result_count"], 1)
                    # Inspection never creates a task gate or human acceptance.
                    self.assertNotIn("case_outcome", result)
                    print(f"{path}: inspector_exit={expected_exit} state={result['state']} "
                          f"claims={result['claims']['status']}", flush=True)


if __name__ == "__main__":
    unittest.main(verbosity=2)
