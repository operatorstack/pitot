"""Actual socket regression tests for Cursor's local protocol relay."""

from pathlib import Path
import subprocess
import unittest


ROOT = Path(__file__).resolve().parents[1]


class CursorStreamTransportTests(unittest.TestCase):
    def test_fragmented_streams_and_connection_lifecycle(self):
        result = subprocess.run(
            ["node", "--test", str(ROOT / "tests/cursor_transport_test.mjs")],
            capture_output=True,
            text=True,
            timeout=30,
        )
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)


if __name__ == "__main__":
    unittest.main()
