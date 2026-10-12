"""Exercise the shipped native plugin with real subprocess output and cancellation."""

from pathlib import Path
import subprocess
import unittest

ROOT = Path(__file__).resolve().parents[1]


class DeepSeekPluginTests(unittest.TestCase):
    def test_native_plugin(self):
        result = subprocess.run(
            ["node", "--test", str(ROOT / "tests/deepseek_plugin_test.mjs")],
            cwd=ROOT,
            capture_output=True,
            text=True,
            timeout=45,
        )
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)


if __name__ == "__main__":
    unittest.main()
