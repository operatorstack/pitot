"""Exercise the installed generator in an isolated source checkout."""

from pathlib import Path
import os
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]


@unittest.skipUnless(
    (ROOT / "node_modules/quicktype/package.json").is_file(),
    "install locked maintenance dependencies with npm ci --ignore-scripts",
)
class GeneratedSDKTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        subprocess.run(["git", "init", "-q", str(self.root)], check=True)
        paths = [
            "go.mod",
            "go.sum",
            "package.json",
            "scripts/generate_types.sh",
            "cmd/generate-schema/main.go",
            "schema/schema.go",
        ]
        paths += subprocess.check_output(
            ["git", "ls-files", "sdk"], cwd=ROOT, text=True
        ).splitlines()
        self.outputs = []
        for name in paths:
            source = ROOT / name
            if not source.is_file():
                continue
            target = self.root / name
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(source, target)
            if name.startswith("sdk/"):
                self.outputs.append(name)
        (self.root / "node_modules").symlink_to(
            ROOT / "node_modules", target_is_directory=True
        )
        self.baseline = self.snapshot()

    def snapshot(self):
        return {name: (self.root / name).read_bytes() for name in self.outputs}

    def generate(self, *args, env=None):
        return subprocess.run(
            ["bash", "scripts/generate_types.sh", *args],
            cwd=self.root,
            env=env,
            capture_output=True,
            text=True,
        )

    def test_repeated_generation_and_check_preserve_bytes(self):
        for args in ((), (), ("--check",)):
            result = self.generate(*args)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(self.snapshot(), self.baseline)

    def test_java_drift_is_rejected_without_writes(self):
        name = "sdk/java/src/main/java/com/operatorstack/pitot/Action.java"
        path = self.root / name
        path.write_bytes(path.read_bytes() + b"\n// drift\n")
        before = self.snapshot()
        result = self.generate("--check")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn(name, result.stderr)
        self.assertEqual(self.snapshot(), before)

    def test_stale_java_source_is_rejected_without_deletion(self):
        path = self.root / "sdk/java/src/main/java/com/operatorstack/pitot/Stale.java"
        path.write_text("// stale\n")
        result = self.generate("--check")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Stale.java", result.stderr)
        self.assertEqual(path.read_text(), "// stale\n")
        self.assertEqual(self.snapshot(), self.baseline)

    def test_missing_tool_does_not_download_or_write(self):
        (self.root / "node_modules").unlink()
        result = self.generate()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("npm ci --ignore-scripts", result.stderr)
        self.assertEqual(self.snapshot(), self.baseline)

    def test_generator_failure_does_not_publish_earlier_outputs(self):
        # Fail the Rust invocation after TypeScript and Python have completed.
        real_node = shutil.which("node")
        tools = self.root / "tools"
        tools.mkdir()
        wrapper = tools / "node"
        counter = tools / "count"
        wrapper.write_text(
            "#!/usr/bin/env python3\n"
            "import os, pathlib, sys\n"
            f"counter = pathlib.Path({str(counter)!r})\n"
            "n = int(counter.read_text()) + 1 if counter.exists() else 1\n"
            "counter.write_text(str(n))\n"
            "if n == 4: sys.exit(23)\n"
            f"os.execv({real_node!r}, [{real_node!r}, *sys.argv[1:]])\n"
        )
        wrapper.chmod(0o700)
        result = self.generate(
            env={**os.environ, "PATH": str(tools) + os.pathsep + os.environ["PATH"]}
        )
        self.assertEqual(result.returncode, 23, result.stderr)
        self.assertEqual(self.snapshot(), self.baseline)


if __name__ == "__main__":
    unittest.main()
