from __future__ import annotations

import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import tarfile
import stat
import tempfile
import unittest
import zipfile
from unittest import mock


ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location(
    "install_real_agent",
    ROOT / "tests/install_real_agent.py",
)
installer = importlib.util.module_from_spec(SPEC)
assert SPEC and SPEC.loader
SPEC.loader.exec_module(installer)


def devin_archive(payload: bytes = b"released-devin") -> bytes:
    output = io.BytesIO()
    with tarfile.open(fileobj=output, mode="w:gz") as bundle:
        info = tarfile.TarInfo("bin/devin")
        info.mode = 0o755
        info.size = len(payload)
        bundle.addfile(info, io.BytesIO(payload))
    return output.getvalue()


class DeepSeekLockedInstallationTests(unittest.TestCase):
    def test_ci_uses_owned_locked_graph_and_checks_supervised_pin(self):
        agent = next(
            a
            for a in json.loads((ROOT / "adapter-verification.json").read_text())[
                "agents"
            ]
            if a["id"] == "deepseek"
        )
        with tempfile.TemporaryDirectory() as directory:
            prefix = Path(directory).resolve() / "owned" / agent["version"]
            with (
                mock.patch.object(
                    installer, "installation_prefix", return_value=prefix
                ),
                mock.patch.object(installer, "run") as run,
            ):
                installer.install(agent, "ubuntu", "native")
            command = run.call_args.args[0]
            self.assertEqual(command[1], "ci")
            self.assertIn("--ignore-scripts", command)
            self.assertNotIn("--save-exact", command)
            self.assertNotIn("@deepseek-ai/dsh@" + agent["version"], command)
            for name in ("package.json", "package-lock.json"):
                self.assertEqual(
                    (prefix / name).read_bytes(),
                    (ROOT / "tests/agent-locks/deepseek" / name).read_bytes(),
                )
            changed = {**agent, "version": "0.0.0"}
            with (
                mock.patch.object(
                    installer,
                    "installation_prefix",
                    return_value=Path(directory).resolve() / "wrong",
                ),
                mock.patch.object(installer, "run") as run,
            ):
                with self.assertRaisesRegex(RuntimeError, "lock does not match"):
                    installer.install(changed, "ubuntu", "native")
                run.assert_not_called()

    def test_lock_retains_integrity_and_cross_platform_optional_packages(self):
        lock = json.loads(
            (ROOT / "tests/agent-locks/deepseek/package-lock.json").read_text()
        )
        systems = set()
        cpus = set()
        for name, package in lock["packages"].items():
            if not name:
                continue
            self.assertTrue(
                package["resolved"].startswith("https://registry.npmjs.org/")
            )
            self.assertTrue(package["integrity"].startswith("sha512-"))
            systems.update(package.get("os", []))
            cpus.update(package.get("cpu", []))
        self.assertTrue({"darwin", "linux", "win32"} <= systems)
        self.assertTrue({"arm64", "x64"} <= cpus)


class Response:
    def __init__(self, payload: bytes):
        self.payload = payload

    def __enter__(self) -> "Response":
        return self

    def __exit__(self, *_: object) -> None:
        return None

    def read(self) -> bytes:
        return self.payload


class CursorWindowsInstallerTests(unittest.TestCase):
    def archive(self, extra=(), missing=()):
        output = io.BytesIO()
        with zipfile.ZipFile(output, "w") as bundle:
            for name in (
                "cursor-agent.cmd",
                "cursor-agent.ps1",
                "node.exe",
                "index.js",
            ):
                if name not in missing:
                    bundle.writestr(f"dist-package/{name}", f"released-{name}")
            for name, payload in extra:
                bundle.writestr(name, payload)
        return output.getvalue()

    def test_native_install_preserves_the_complete_version_owned_payload(self):
        payload = self.archive()
        agent = {
            "id": "cursor",
            "version": "2026.10.01-e373342",
            "installer": {
                "kind": "cursor_release",
                "package": "https://downloads.cursor.com/lab/2026.10.01-e373342",
            },
        }
        calls = []

        def download(command, **_):
            calls.append(command)
            Path(command[-1]).write_bytes(payload)

        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory) / "owned home"
            home.mkdir()
            with (
                mock.patch.object(installer.Path, "home", return_value=home),
                mock.patch.object(
                    installer.host_platform, "machine", return_value="AMD64"
                ),
                mock.patch.object(installer, "run", side_effect=download),
            ):
                result = Path(installer.install(agent, "windows", "native"))
            self.assertEqual(
                result,
                home
                / ".local/share/pitot-cursor"
                / agent["version"]
                / "cursor-agent.cmd",
            )
            for name in (
                "cursor-agent.cmd",
                "cursor-agent.ps1",
                "node.exe",
                "index.js",
            ):
                self.assertEqual((result.parent / name).read_text(), f"released-{name}")
            self.assertIn(
                "https://downloads.cursor.com/lab/2026.10.01-e373342/windows/x64/agent-cli-package.zip",
                calls[0],
            )
            self.assertIn("--max-time", calls[0])
            self.assertFalse((home / ".local/bin").exists())

    def test_unsafe_members_refuse_before_extracting_any_file(self):
        link = zipfile.ZipInfo("dist-package/link")
        link.create_system = 3
        link.external_attr = (stat.S_IFLNK | 0o777) << 16
        cases = [
            [("../escape", "bad")],
            [("/absolute", "bad")],
            [("dist-package/../escape", "bad")],
            [("dist-package\\escape", "bad")],
            [("dist-package/file:stream", "bad")],
            [("dist-package/file.", "bad")],
            [("dist-package/NUL.txt", "bad")],
            [("dist-package/INDEX.JS", "duplicate")],
            [("dist-package/block", "file"), ("dist-package/block/child", "bad")],
            [("dist-package/Block", "file"), ("dist-package/block/child", "bad")],
            [("dist-package/Block", "file"), ("dist-package/block/empty/", "")],
            [(link, "../outside")],
        ]
        for extra in cases:
            with self.subTest(extra=extra), tempfile.TemporaryDirectory() as directory:
                archive, destination = (
                    Path(directory) / "package.zip",
                    Path(directory) / "output",
                )
                archive.write_bytes(self.archive(extra))
                with self.assertRaises(RuntimeError):
                    installer.extract_cursor_windows(archive, destination)
                self.assertFalse(destination.exists())

    def test_missing_delegates_refuse_before_extraction(self):
        for name in ("cursor-agent.cmd", "cursor-agent.ps1", "node.exe", "index.js"):
            with self.subTest(missing=name), tempfile.TemporaryDirectory() as directory:
                archive, destination = (
                    Path(directory) / "package.zip",
                    Path(directory) / "output",
                )
                archive.write_bytes(self.archive(missing=(name,)))
                with self.assertRaisesRegex(
                    RuntimeError, "missing its owned launch files"
                ):
                    installer.extract_cursor_windows(archive, destination)
                self.assertFalse(destination.exists())


class DevinInstallerTests(unittest.TestCase):
    def test_installs_attested_version_specific_release(self):
        archive = devin_archive()
        manifest = {
            "version": "3000.3.22",
            "platforms": {
                "aarch64-unknown-linux": {
                    "url": "https://example.invalid/devin.tar.gz",
                    "sha256": hashlib.sha256(archive).hexdigest(),
                }
            },
        }
        responses = [
            Response(json.dumps(manifest).encode()),
            Response(archive),
        ]
        agent = {
            "id": "devin",
            "version": "3000.3.22",
            "executable": "devin",
            "installer": {
                "kind": "devin_release",
                "package": "https://static.devin.ai/cli",
            },
        }
        with (
            tempfile.TemporaryDirectory() as home,
            mock.patch.dict(os.environ, {"HOME": home}),
            mock.patch.object(installer.host_platform, "machine", return_value="arm64"),
            mock.patch.object(
                installer.urllib.request, "urlopen", side_effect=responses
            ) as urlopen,
        ):
            installer.install(agent, "ubuntu", "native")
            installed = Path(home) / ".local/bin/devin"
            self.assertEqual(installed.read_bytes(), b"released-devin")
            self.assertTrue(os.access(installed, os.X_OK))
            self.assertEqual(
                urlopen.call_args_list[0].args[0],
                "https://static.devin.ai/cli/3000.3.22/manifest.json",
            )

    def test_rejects_archive_digest_mismatch(self):
        archive = devin_archive()
        manifest = {
            "version": "3000.3.22",
            "platforms": {
                "x86_64-unknown-linux": {
                    "url": "https://example.invalid/devin.tar.gz",
                    "sha256": "0" * 64,
                }
            },
        }
        agent = {
            "id": "devin",
            "version": "3000.3.22",
            "executable": "devin",
            "installer": {
                "kind": "devin_release",
                "package": "https://static.devin.ai/cli",
            },
        }
        responses = [Response(json.dumps(manifest).encode()), Response(archive)]
        with (
            mock.patch.object(installer.host_platform, "machine", return_value="AMD64"),
            mock.patch.object(
                installer.urllib.request, "urlopen", side_effect=responses
            ),
        ):
            with self.assertRaisesRegex(RuntimeError, "SHA-256 mismatch"):
                installer.install(agent, "ubuntu", "native")


class NpmInstallerTests(unittest.TestCase):
    def test_global_install_pins_version_and_runs_scripts(self):
        agent = {
            "id": "claude",
            "version": "2.1.217",
            "executable": "claude",
            "installer": {"kind": "npm", "package": "@anthropic-ai/claude-code"},
        }
        with (
            mock.patch.object(installer, "run") as run,
            mock.patch.object(installer, "npm_executable", return_value="npm"),
            mock.patch.object(
                installer, "owned_entrypoint", return_value="/owned/claude"
            ),
        ):
            installer.install(agent, "ubuntu", "native")
        run.assert_called_once_with(
            [
                "npm",
                "install",
                "--global",
                "--ignore-scripts=false",
                "@anthropic-ai/claude-code@2.1.217",
            ]
        )


class KimiInstallerTests(unittest.TestCase):
    def test_macos_archive_preserves_the_pinned_binary_identity(self):
        for version, checksum, succeeds in (
            ("0.29.0", hashlib.sha256(b"verified").hexdigest(), True),
            ("0.29.0", "0" * 64, False),
            ("wrong-version", hashlib.sha256(b"verified").hexdigest(), False),
        ):
            with (
                self.subTest(version=version, checksum=checksum),
                tempfile.TemporaryDirectory() as directory,
            ):
                target = Path(directory) / ".local/bin/kimi"
                target.parent.mkdir(parents=True)
                target.write_bytes(b"existing")
                manifest = {
                    "version": version,
                    "platforms": {
                        "darwin-arm64": {
                            "filename": "kimi-code-darwin-arm64",
                            "checksum": checksum,
                        }
                    },
                }

                def download(command, **kwargs):
                    self.assertIn(
                        "https://github.com/MoonshotAI/kimi-code/releases/download/%40moonshot-ai/kimi-code%400.29.0/kimi-code-darwin-arm64.zip",
                        command,
                    )
                    with zipfile.ZipFile(
                        command[command.index("-o") + 1], "w"
                    ) as bundle:
                        bundle.writestr("kimi", b"verified")

                with (
                    mock.patch.object(Path, "home", return_value=Path(directory)),
                    mock.patch.object(
                        installer.host_platform, "machine", return_value="arm64"
                    ),
                    mock.patch.object(
                        installer.urllib.request,
                        "urlopen",
                        return_value=Response(json.dumps(manifest).encode()),
                    ),
                    mock.patch.object(installer, "run", side_effect=download),
                ):
                    if succeeds:
                        installer.install_kimi_macos(
                            "https://code.kimi.com/kimi-code", "0.29.0"
                        )
                    else:
                        with self.assertRaises(RuntimeError):
                            installer.install_kimi_macos(
                                "https://code.kimi.com/kimi-code", "0.29.0"
                            )
                self.assertEqual(
                    target.read_bytes(), b"verified" if succeeds else b"existing"
                )
                if succeeds:
                    self.assertEqual(target.stat().st_mode & 0o777, 0o755)

    def test_posix_install_pins_version_via_env(self):
        agent = {
            "id": "kimi",
            "version": "0.29.0",
            "executable": "kimi",
            "installer": {
                "kind": "kimi_release",
                "package": "https://code.kimi.com/kimi-code",
            },
        }
        with mock.patch.object(installer, "run") as run:
            installer.install(agent, "ubuntu", "native")
        commands = [call.args[0] for call in run.call_args_list]
        self.assertEqual(commands[0][0], "curl")
        self.assertIn("https://code.kimi.com/kimi-code/install.sh", commands[0])
        bash_call = run.call_args_list[1]
        self.assertEqual(bash_call.args[0][0], "bash")
        self.assertEqual(bash_call.kwargs["env"]["KIMI_VERSION"], "0.29.0")

    def test_windows_install_uses_native_installer_with_version(self):
        agent = {
            "id": "kimi",
            "version": "0.29.0",
            "executable": "kimi",
            "installer": {
                "kind": "kimi_release",
                "package": "https://code.kimi.com/kimi-code",
            },
        }
        with mock.patch.object(installer, "run") as run:
            installer.install(agent, "windows", "native")
        command = run.call_args_list[0].args[0]
        self.assertEqual(command[0], "powershell")
        script = command[-1]
        self.assertIn("$env:KIMI_VERSION='0.29.0'", script)
        self.assertIn("kimi-code/install.ps1", script)


class CursorInstallerTests(unittest.TestCase):
    AGENT = {
        "id": "cursor",
        "version": "2026.07.20-8cc9c0b",
        "executable": "agent",
        "installer": {
            "kind": "cursor_release",
            "package": "https://downloads.cursor.com/lab/2026.07.20-8cc9c0b",
        },
    }

    def _run_factory(self, executables: int):
        """Make a fake `run` that materializes the tar extraction so the
        single-executable invariant is exercised on real files."""

        def fake_run(command, **_kwargs):
            if command[0] == "tar" and "-C" in command:
                directory = Path(command[command.index("-C") + 1])
                for index in range(executables):
                    sub = directory / f"pkg{index}"
                    sub.mkdir(parents=True, exist_ok=True)
                    (sub / "cursor-agent").write_bytes(b"cursor")
            return None

        return fake_run

    def test_extracts_single_executable_and_swaps_symlink(self):
        with (
            tempfile.TemporaryDirectory() as home,
            mock.patch.dict(os.environ, {"HOME": home}),
            mock.patch.object(installer.host_platform, "machine", return_value="arm64"),
            mock.patch.object(installer, "run", side_effect=self._run_factory(1)),
        ):
            installer.install(self.AGENT, "ubuntu", "native")
            link = Path(home) / ".local/bin/agent"
            self.assertTrue(link.is_symlink())
            self.assertEqual(link.resolve().read_bytes(), b"cursor")
            self.assertIn("pitot-cursor/2026.07.20-8cc9c0b", str(link.resolve()))

    def test_rejects_archive_with_multiple_executables(self):
        with (
            tempfile.TemporaryDirectory() as home,
            mock.patch.dict(os.environ, {"HOME": home}),
            mock.patch.object(
                installer.host_platform, "machine", return_value="x86_64"
            ),
            mock.patch.object(installer, "run", side_effect=self._run_factory(2)),
        ):
            with self.assertRaisesRegex(RuntimeError, "2 agent executables"):
                installer.install(self.AGENT, "ubuntu", "native")


if __name__ == "__main__":
    unittest.main()
