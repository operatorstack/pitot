"""Deterministic application boundary checks; no agent or network required."""

from pathlib import Path
import json
import subprocess
import sys
import tempfile
import unittest

from replay import Replay
from supervisor import Supervisor
from demo import Application


class SupervisorTest(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.project = Path(self.directory.name)
        self.commands = {action: action for action in ("dispatch", "build", "release")}
        self.supervisor = Supervisor(self.project, "qwen", self.commands)

    def build(self):
        self.assertTrue(self.supervisor.admit("qwen", "build", "build-1")[0])
        return self.supervisor.execute("qwen", "build")["revision"]

    def test_permission_is_not_completion(self):
        self.assertTrue(self.supervisor.admit("qwen", "build", "build-1")[0])
        self.assertEqual(self.supervisor.state, "draft")
        self.assertFalse((self.project / "artifact.txt").exists())
        self.assertFalse(self.supervisor.admit("qwen", "release", "release-1")[0])

    def test_external_event_changes_same_action(self):
        revision = self.build()
        self.assertFalse(self.supervisor.admit("qwen", "release", "release-1")[0])
        self.assertTrue(self.supervisor.observe_tests(revision, b"release candidate\n"))
        self.assertTrue(self.supervisor.admit("qwen", "release", "release-2")[0])
        self.supervisor.invalidate("external environment change")
        with self.assertRaises(ValueError):
            self.supervisor.execute("qwen", "release")
        self.assertFalse((self.project / "released.txt").exists())
        self.assertFalse(self.supervisor.admit("qwen", "release", "release-3")[0])
        self.assertTrue(self.supervisor.observe_tests(revision, b"release candidate\n"))
        self.assertTrue(self.supervisor.admit("qwen", "release", "release-4")[0])
        self.supervisor.execute("qwen", "release")
        self.assertEqual(self.supervisor.state, "released")
        with self.assertRaises(ValueError):
            self.supervisor.execute("qwen", "release")

    def test_failed_build_does_not_advance(self):
        self.supervisor.admit("qwen", "build", "build-1")
        (self.project / "artifact.txt").mkdir()
        with self.assertRaises(OSError):
            self.supervisor.execute("qwen", "build")
        self.assertEqual(self.supervisor.state, "draft")
        self.assertEqual(self.supervisor.revision, 0)

    def test_stale_test_and_changed_artifact_do_not_release(self):
        revision = self.build()
        self.build()
        self.assertFalse(
            self.supervisor.observe_tests(revision, b"release candidate\n")
        )
        self.assertEqual(self.supervisor.state, "built")
        self.supervisor.observe_tests(self.supervisor.revision, b"release candidate\n")
        self.supervisor.admit("qwen", "release", "release-1")
        (self.project / "artifact.txt").write_text("changed\n")
        with self.assertRaises(ValueError):
            self.supervisor.execute("qwen", "release")
        self.assertFalse((self.project / "released.txt").exists())

    def test_unknown_command_role_and_forged_event_denied(self):
        for actor, command in (
            ("codex", "build"),
            ("qwen", "dispatch"),
            ("deepseek", "release"),
            ("qwen", "tests_passed"),
            ("qwen", "build; release"),
        ):
            self.assertFalse(self.supervisor.admit(actor, command, "bad-1")[0])
        with self.assertRaises(ValueError):
            self.supervisor.execute("qwen", "tests_passed")

    def test_replay_requires_correlated_native_feedback(self):
        replay = Replay("qwen", self.commands)
        body = {"tools": [{"function": {"name": "run_shell_command"}}], "messages": []}
        tool, arguments, call_id = replay.propose(body, False)
        self.assertEqual(arguments["command"], "release")
        body["messages"] = [
            {"role": "user", "content": "SUPERVISOR_DENY"},
            {"role": "tool", "tool_call_id": "unrelated", "content": "SUPERVISOR_DENY"},
        ]
        with self.assertRaises(ValueError):
            replay.propose(body, False)
        body["messages"].append(
            {
                "role": "tool",
                "tool_call_id": call_id,
                "content": "SUPERVISOR_DENY release state=draft",
            }
        )
        self.assertEqual(replay.propose(body, False)[1]["command"], "build")

    def test_missing_native_event_and_uncorrelated_action_denied(self):
        application = Application(self.project, "qwen", {})
        self.assertFalse(
            application.admit({"action_id": "request-1", "data": None})["allowed"]
        )
        event = {
            "type": "action.requested",
            "host": {"name": "qwen"},
            "action": {"id": "other-request", "kind": "shell"},
            "content": {"full": application.commands["build"]},
        }
        self.assertFalse(
            application.admit({"action_id": "request-1", "data": event})["allowed"]
        )
        event["action"]["id"] = "request-1"
        event["content"] = {"sha256": "opaque"}
        self.assertFalse(
            application.admit({"action_id": "request-1", "data": event})["allowed"]
        )

    def test_qwen_bridge_transport_failure_returns_native_deny(self):
        result = subprocess.run(
            [
                sys.executable,
                str(Path(__file__).with_name("demo.py")),
                "qwen-hook",
                "--pitot",
                str(self.project / "missing-pitot"),
                "--runtime",
                str(self.project / "runtime.json"),
            ],
            input=b"{}",
            capture_output=True,
            timeout=10,
            check=True,
        )
        decision = json.loads(result.stdout)["hookSpecificOutput"]
        self.assertEqual(decision["permissionDecision"], "deny")
        self.assertIn("Pitot unavailable", decision["permissionDecisionReason"])


if __name__ == "__main__":
    unittest.main()
