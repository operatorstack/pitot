"""Keep the real Qwen fixture aligned with the public shell-hook setup."""

from pathlib import Path
import json
import re
import tempfile
import unittest
from unittest.mock import patch

import real_agent_driver as driver


class QwenSetupTests(unittest.TestCase):
    def test_native_fixture_uses_documented_runtime_shell_matcher(self):
        section = (
            (driver.LAB / "docs/hosts.md")
            .read_text(encoding="utf-8")
            .split("## Qwen Code\n", 1)[1]
            .split("\n## ", 1)[0]
        )
        documented = json.loads(section.split("```json\n", 1)[1].split("```", 1)[0])
        expected = documented["hooks"]["PreToolUse"][0]["matcher"]
        self.assertEqual(expected, "^run_shell_command$")
        for windows in (False, True):
            with self.subTest(windows=windows), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                home, project = root / "home", root / "project"
                home.mkdir()
                project.mkdir()
                with patch.object(driver, "windows_host", return_value=windows):
                    _, env = driver.configure(
                        "qwen",
                        home,
                        project,
                        "owned-witness",
                        "http://127.0.0.1:12345",
                        pitot_command="owned-pitot",
                        witness_receipt=str(root / "receipt.json"),
                        nonce="owned-test-nonce",
                        runtime_path=str(root / "runtime.json"),
                    )
                settings = json.loads((home / ".qwen/settings.json").read_text())
                entries = settings["hooks"]["PreToolUse"]
                self.assertEqual(len(entries), 1)
                self.assertEqual(entries[0]["matcher"], expected)
                self.assertEqual(entries[0]["hooks"][0]["type"], "command")
                self.assertEqual(env["QWEN_HOME"], str(home / ".qwen"))
                matcher = re.compile(entries[0]["matcher"])
                self.assertIsNotNone(matcher.fullmatch("run_shell_command"))
                for tool in ("read_file", "monitor", "run_shell_command_extra"):
                    self.assertIsNone(matcher.search(tool), tool)
