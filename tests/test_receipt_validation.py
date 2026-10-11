"""Negative-branch tests for real_agent_driver.validate_receipts — the
causal checks that previously were enforced only by live CI cells. Each test
mutates one receipt and expects the specific rejection; the baseline is
proven sound by reaching the installation-manifest check (the first gate
after every causal branch under test)."""

from __future__ import annotations

import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
LAB = ROOT

driver_spec = importlib.util.spec_from_file_location(
    "real_agent_driver", LAB / "tests/real_agent_driver.py"
)
driver = importlib.util.module_from_spec(driver_spec)
assert driver_spec and driver_spec.loader
driver_spec.loader.exec_module(driver)

MANIFEST = json.loads((LAB / "adapter-verification.json").read_text(encoding="utf-8"))
AGENTS = {agent["id"]: agent for agent in MANIFEST["agents"]}
NONCE = "ab" * 16


class ReceiptFixture:
    """A synthetic, internally-consistent receipt set for one hook run."""

    def __init__(self, base: Path, agent_id: str = "claude"):
        self.base = base
        self.agent = AGENTS[agent_id]
        self.action_ids = ["act_" + "1" * 32, "act_" + "2" * 32]
        self.proxy = base / "proxy.json"
        self.witness = base / "witness.jsonl"
        self.controller = base / "controller.jsonl"
        self.consumer = base / "consumer.jsonl"
        self.canary = base / "canary.txt"
        self.proxy_receipt = {
            "schema_version": 1,
            "agent": agent_id,
            "protocol": "anthropic_messages",
            "nonce": NONCE,
            "initial_prompt_observed": True,
            "allow_tool_call_response_emitted": True,
            "allow_tool_result_observed": True,
            "deny_tool_call_response_emitted": True,
            "denied_result_observed": True,
            "final_response_emitted": True,
            "tool_call_response_emitted": True,
            "tool_result_observed": True,
            "auxiliary_requests": 1,
        }
        self.witness_rows = [
            {
                "nonce": NONCE,
                "host": agent_id,
                "valid": True,
                "pitot_exit": exit_code,
                "action_id": action,
            }
            for exit_code, action in zip((0, 2), self.action_ids)
        ]
        self.controller_rows = [
            {"receipt_type": "request", "value": {"action_id": action}}
            for action in self.action_ids
        ] + [
            {
                "receipt_type": "response",
                "value": {"action_id": self.action_ids[0], "outcome": "allow"},
            },
            {
                "receipt_type": "response",
                "value": {
                    "action_id": self.action_ids[1],
                    "outcome": "deny",
                    "message": f"PITOT_CONTROLLER_DENY {NONCE}",
                },
            },
        ]
        self.consumer_rows = [
            {
                "action": {"id": action},
                "content": {"mode": "sha256", "sha256": "0" * 64},
            }
            for action in self.action_ids
        ]
        self.canary_lines = [f"PITOT_ALLOW {NONCE}"]
        self.runtime_identity = {
            "schema_version": 1,
            "instance_id": "i1",
            "pid": 42,
            "endpoint": "http://127.0.0.1:1/",
            "config_sha256": "c" * 64,
        }
        # Deliberately wrong version: validation must run every causal branch
        # first and terminate at the installation-manifest gate.
        self.installation = {"agent": agent_id, "version": "0.0.0-synthetic"}
        self.output = f"PITOT_E2E_COMPLETE {NONCE}\n"

    def write(self) -> None:
        self.proxy.write_text(json.dumps(self.proxy_receipt), encoding="utf-8")
        self.witness.write_text(
            "\n".join(json.dumps(row) for row in self.witness_rows) + "\n",
            encoding="utf-8",
        )
        self.controller.write_text(
            "\n".join(json.dumps(row) for row in self.controller_rows) + "\n",
            encoding="utf-8",
        )
        self.consumer.write_text(
            "\n".join(json.dumps(row) for row in self.consumer_rows) + "\n",
            encoding="utf-8",
        )
        self.canary.write_text("\n".join(self.canary_lines) + "\n", encoding="utf-8")

    def validate(self):
        self.write()
        return driver.validate_receipts(
            self.agent,
            "ubuntu",
            NONCE,
            self.installation,
            self.proxy,
            self.witness,
            self.controller,
            self.consumer,
            self.canary,
            self.runtime_identity,
            0,
            self.output,
            "prompt",
        )


class ValidateReceiptsNegativeBranches(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.fixture = ReceiptFixture(Path(self.tmp.name))

    def expect(self, fragment: str):
        with self.assertRaises(RuntimeError) as caught:
            self.fixture.validate()
        self.assertIn(fragment, str(caught.exception))

    def test_baseline_reaches_installation_gate(self):
        # Every causal branch passes; the synthetic version stops at the gate.
        self.expect("installation receipt does not match supervised manifest")

    def test_witness_order_must_be_allow_then_deny(self):
        self.fixture.witness_rows[0]["pitot_exit"] = 2
        self.fixture.witness_rows[1]["pitot_exit"] = 0
        self.expect("one allow and one deny")

    def test_duplicated_canary_execution_is_rejected(self):
        self.fixture.canary_lines.append(f"PITOT_ALLOW {NONCE}")
        self.expect("canary execution count")

    def test_denied_canary_execution_is_rejected(self):
        self.fixture.canary_lines.append(f"PITOT_DENY {NONCE}")
        self.expect("canary execution count")

    def test_consumer_projection_leak_is_rejected(self):
        self.fixture.consumer_rows[0]["content"] = {
            "mode": "sha256",
            "sha256": "0" * 64,
            "full": "secret",
        }
        self.expect("projection did not remove full command content")

    def test_deny_message_must_be_nonce_bound(self):
        self.fixture.controller_rows[3]["value"]["message"] = (
            "PITOT_CONTROLLER_DENY wrong"
        )
        self.expect("nonce-bound allow/deny trajectory")

    def test_witness_correlation_must_match_controller(self):
        self.fixture.witness_rows[0]["action_id"] = "act_" + "9" * 32
        self.expect("hook correlation ids do not match")

    def test_nonzero_exit_is_agent_loop_incomplete(self):
        self.fixture.write()
        with self.assertRaises(RuntimeError) as caught:
            driver.validate_receipts(
                self.fixture.agent,
                "ubuntu",
                NONCE,
                self.fixture.installation,
                self.fixture.proxy,
                self.fixture.witness,
                self.fixture.controller,
                self.fixture.consumer,
                self.fixture.canary,
                self.fixture.runtime_identity,
                1,
                self.fixture.output,
                "prompt",
            )
        self.assertIn("agent loop incomplete", str(caught.exception))


class ValidateReceiptsACPBranches(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.fixture = ReceiptFixture(Path(self.tmp.name), agent_id="devin")
        self.fixture.proxy_receipt["protocol"] = "devin_connect_proto"
        self.fixture.witness_rows = []

    def expect(self, fragment: str):
        with self.assertRaises(RuntimeError) as caught:
            self.fixture.validate()
        self.assertIn(fragment, str(caught.exception))

    def test_acp_baseline_reaches_installation_gate(self):
        self.expect("installation receipt does not match supervised manifest")

    def test_acp_with_witness_receipts_is_rejected(self):
        self.fixture.witness_rows = [
            {
                "nonce": NONCE,
                "host": "devin",
                "valid": True,
                "pitot_exit": 0,
                "action_id": self.fixture.action_ids[0],
            }
        ]
        self.expect("unexpectedly entered the lifecycle-hook witness path")

    def test_auxiliary_request_ceiling(self):
        self.fixture.proxy_receipt["auxiliary_requests"] = 101
        self.expect("ceiling is 100")


if __name__ == "__main__":
    unittest.main()
