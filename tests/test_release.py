"""Exercise release admission with an owned repository; never publish."""

import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location(
    "check_release", ROOT / "scripts/check_release.py"
)
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)


class ReleaseAdmission(unittest.TestCase):
    def test_tag_identity_and_source_versions(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for relative, body in {
                "sdk/typescript/package.json": json.dumps({"version": "1.2.3"}),
                "sdk/python/pyproject.toml": 'version = "1.2.3"\n',
                "sdk/rust/Cargo.toml": 'version = "1.2.3"\n',
                "cmd/pitot/workbench.go": 'const pitotPackageVersion = "1.2.3"\n',
            }.items():
                path = root / relative
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text(body)

            def git(*args):
                return subprocess.check_output(
                    [
                        "git",
                        "-c",
                        "user.name=Release Test",
                        "-c",
                        "user.email=release@example.invalid",
                        "-c",
                        "commit.gpgsign=false",
                        *args,
                    ],
                    cwd=root,
                    text=True,
                ).strip()

            git("init", "--quiet")
            git("add", ".")
            git("commit", "--quiet", "-m", "fixture")
            git("tag", "v1.2.3")
            commit = git("rev-parse", "HEAD")
            self.assertEqual(
                release.check_release("refs/tags/v1.2.3", commit, root), "1.2.3"
            )
            for ref in (
                "refs/tags/v2.0.0",
                "refs/heads/main",
                "v1.2.3",
                "refs/tags/vfoo",
                "refs/tags/v1.2.3-rc1",
                "refs/tags/v01.2.3",
                "refs/tags/v1.2.4",
            ):
                with self.subTest(ref=ref), self.assertRaises(ValueError):
                    release.check_release(ref, commit, root)
            with self.assertRaisesRegex(ValueError, "Go module path migration"):
                release.check_release("refs/tags/v2.0.0", commit, root)
            with self.assertRaises(ValueError):
                release.check_release("refs/tags/v1.2.3", "0" * 40, root)
            (root / "untracked").write_text("unexpected")
            with self.assertRaises(ValueError):
                release.check_release("refs/tags/v1.2.3", commit, root)
            (root / "untracked").unlink()
            (root / "sdk/rust/Cargo.toml").write_text('version = "1.2.4"\n')
            with self.assertRaises(ValueError):
                release.check_release("refs/tags/v1.2.3", commit, root)

    def test_repository_versions(self):
        self.assertRegex(release.source_version(), "^" + release.STABLE_VERSION + "$")
