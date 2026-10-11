"""Keep shipped hook entrypoints inside the authored syntax-check boundary."""

import importlib.util
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts"))
try:
    spec = importlib.util.spec_from_file_location(
        "pitot_static_checks", ROOT / "scripts/check_static.py"
    )
    checks = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(checks)
finally:
    sys.path.pop(0)


class HookSyntaxTests(unittest.TestCase):
    def check_source(self, name, content, valid):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / name).write_text(content)
            real_run = subprocess.run

            def run(command, **kwargs):
                if command[:2] == ["go", "vet"]:
                    return subprocess.CompletedProcess(command, 0)
                return real_run(
                    command, **kwargs, stdout=subprocess.PIPE, stderr=subprocess.PIPE
                )

            with (
                patch.object(checks, "ROOT", root),
                patch.object(checks, "files", return_value=[name]),
                patch.object(checks.subprocess, "run", side_effect=run),
            ):
                if valid:
                    checks.main()
                else:
                    with self.assertRaises(subprocess.CalledProcessError):
                        checks.main()

    def test_extensionless_shell_hook(self):
        self.check_source("PreToolUse", "#!/usr/bin/env bash\nexit 0\n", True)
        self.check_source("PreToolUse", "#!/usr/bin/env bash\nif then\n", False)

    def test_commonjs_hook(self):
        self.check_source("PreToolUse.cjs", "module.exports = {};\n", True)
        self.check_source("PreToolUse.cjs", "const broken = ;\n", False)


if __name__ == "__main__":
    unittest.main()
