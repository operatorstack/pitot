"""Tests for the two E2E apparatus binaries that previously had no coverage
outside live CI cells: the pitot-witness wrapper (byte/exit transparency +
nonce-bound receipts) and the runtime capability driver (explicit_request
allow/deny against a real runtime)."""

from __future__ import annotations

import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

LAB = Path(__file__).resolve().parents[1]
NONCE = "feed" * 8

FAKE_PITOT = """#!/usr/bin/env python3
import json, os, sys
sys.stdin.read()
event = {
    "pitot_version": "1", "type": "action.requested",
    "host": {"name": "claude", "adapter_version": "0.1.0"},
    "action": {"id": "act_" + "a" * 32, "kind": "shell"},
    "content": {"mode": "full", "full": json.dumps("echo %s")},
    "observation": {"source": "host_hook", "fidelity": "direct"},
}
print(json.dumps(event))
sys.exit(int(os.environ.get("FAKE_PITOT_EXIT", "0")))
"""


def build(binary: str, source: str, cwd: Path, out_dir: Path) -> Path:
    out = out_dir / (binary + (".exe" if os.name == "nt" else ""))
    subprocess.run(
        ["go", "build", "-o", str(out), source],
        cwd=cwd,
        check=True,
        capture_output=True,
    )
    return out


class WitnessTransparencyTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        base = Path(cls.tmp.name)
        cls.witness = build(
            "pitot-witness", "./main.go", LAB / "tests" / "witness", base
        )
        # The fake pitot IS the --real-bin the witness execs, so it must be a
        # self-contained executable (shebang + mode). The witness prepends it
        # and the wrapped argv is exactly the hook invocation (`hook claude`).
        cls.fake_pitot = base / "fake-pitot.py"
        cls.fake_pitot.write_text(FAKE_PITOT % NONCE, encoding="utf-8")
        cls.fake_pitot.chmod(0o755)

    @classmethod
    def tearDownClass(cls):
        cls.tmp.cleanup()

    def run_witness(self, exit_code: int, nonce: str = NONCE):
        # The witness prepends --real-bin, so the wrapped argv is just the
        # hook invocation (`hook claude`) — exactly as the real harness calls
        # it. The fake pitot IS the real-bin.
        receipt = Path(self.tmp.name) / f"receipt-{exit_code}-{nonce[:4]}.jsonl"
        environment = {**os.environ, "FAKE_PITOT_EXIT": str(exit_code)}
        completed = subprocess.run(
            [
                str(self.witness),
                "--real-bin",
                str(self.fake_pitot),
                "--receipt",
                str(receipt),
                "--nonce",
                nonce,
                "--",
                "hook",
                "claude",
            ],
            input="{}",
            text=True,
            capture_output=True,
            env=environment,
        )
        rows = (
            [
                json.loads(line)
                for line in receipt.read_text(encoding="utf-8").splitlines()
            ]
            if receipt.is_file()
            else []
        )
        return completed, rows

    def test_witness_is_exit_and_byte_transparent_and_validates(self):
        completed, rows = self.run_witness(0)
        self.assertEqual(completed.returncode, 0)
        self.assertIn(
            "action.requested", completed.stdout, "witness must not alter pitot stdout"
        )
        self.assertEqual(len(rows), 1)
        row = rows[0]
        self.assertEqual(row["nonce"], NONCE)
        self.assertEqual(row["host"], "claude")
        self.assertEqual(row["pitot_exit"], 0)
        self.assertTrue(row["valid"], f"witness rejected a valid event: {row}")

    def test_witness_records_deny_exit(self):
        completed, rows = self.run_witness(2)
        self.assertEqual(
            completed.returncode, 2, "deny exit must pass through unchanged"
        )
        self.assertEqual(len(rows), 1)
        self.assertEqual(rows[0]["pitot_exit"], 2)

    def test_witness_writes_no_receipt_on_nonce_mismatch(self):
        # The witness only records a receipt for a valid, nonce-bound event;
        # an event without the session nonce leaves no receipt (the driver's
        # negative test relies on this absence).
        _, rows = self.run_witness(0, nonce="00" * 16)
        self.assertEqual(rows, [])


class RuntimeCapabilityDriverTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        base = Path(cls.tmp.name)
        module = LAB
        cls.pitot = build("pitot", "./cmd/pitot", module, base)
        cls.testrole = build("pitot-testrole", "./internal/testrole", module, base)

    @classmethod
    def tearDownClass(cls):
        cls.tmp.cleanup()

    def test_explicit_request_allow_deny_round_trip(self):
        evidence = Path(self.tmp.name) / "evidence.json"
        completed = subprocess.run(
            [
                sys.executable,
                str(LAB / "tests" / "runtime_capability_driver.py"),
                "--pitot",
                str(self.pitot),
                "--test-role",
                str(self.testrole),
                "--platform",
                "local",
                "--evidence",
                str(evidence),
            ],
            text=True,
            capture_output=True,
            timeout=120,
        )
        self.assertEqual(completed.returncode, 0, completed.stdout + completed.stderr)
        self.assertIn(
            "PITOT_RUNTIME_E2E_RESULT capability=explicit_request", completed.stdout
        )
        value = json.loads(evidence.read_text(encoding="utf-8"))
        self.assertEqual(value["controller"]["outcomes"], ["allow", "deny"])
        self.assertTrue(all(value["receipts"].values()))
        self.assertEqual(len(set(value["controller"]["action_ids"])), 2)


if __name__ == "__main__":
    unittest.main()
