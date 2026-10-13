"""Keep the standalone example's deterministic checks in repository CI."""

from pathlib import Path
import subprocess
import sys
import unittest


class SupervisoryExampleTest(unittest.TestCase):
    def test_application_boundary(self):
        root = Path(__file__).resolve().parents[1]
        result = subprocess.run(
            [
                sys.executable,
                "-m",
                "unittest",
                "discover",
                "-s",
                "examples/supervisory-control",
                "-v",
            ],
            cwd=root,
            capture_output=True,
            text=True,
            timeout=30,
        )
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
