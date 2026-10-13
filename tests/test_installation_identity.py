from __future__ import annotations

import os
import json
import hashlib
import copy
import shutil
from pathlib import Path
import stat
import subprocess
import tempfile
import unittest
from unittest import mock

import agent_identity
import install_real_agent
from installation_identity import (
    tree_digest,
    installation_prefix,
    installation_environment,
)


class InstallationTreeTests(unittest.TestCase):
    @unittest.skipIf(os.name == "nt", "POSIX executable mode initialization")
    def test_qwen_ripgrep_initialized_before_snapshot_without_byte_change(self):
        with tempfile.TemporaryDirectory() as d:
            prefix = Path(d).resolve()
            binary = (
                prefix
                / "node_modules/@qwen-code/qwen-code/vendor/ripgrep/arm64-darwin/rg"
            )
            binary.parent.mkdir(parents=True)
            binary.write_bytes(b"owned bundled executable")
            binary.chmod(0o644)
            before = tree_digest(prefix)
            install_real_agent.initialize_qwen_ripgrep(prefix, "darwin", "arm64")
            self.assertEqual(stat.S_IMODE(binary.stat().st_mode), 0o755)
            self.assertEqual(binary.read_bytes(), b"owned bundled executable")
            initialized = tree_digest(prefix)
            self.assertNotEqual(before, initialized)
            install_real_agent.initialize_qwen_ripgrep(prefix, "darwin", "arm64")
            self.assertEqual(tree_digest(prefix), initialized)
            binary.chmod(0o744)
            install_real_agent.initialize_qwen_ripgrep(prefix, "darwin", "arm64")
            self.assertEqual(stat.S_IMODE(binary.stat().st_mode), 0o744)

    def test_qwen_windows_initialization_has_no_filesystem_effect(self):
        with tempfile.TemporaryDirectory() as d:
            prefix = Path(d).resolve()
            before = tree_digest(prefix)
            install_real_agent.initialize_qwen_ripgrep(prefix, "win32", "x64")
            self.assertEqual(tree_digest(prefix), before)

    def test_executable_extensions_are_stable_regular_files(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            for extension in ("exe", "cmd", "bat", "com", "js"):
                (root / ("program." + extension)).write_bytes(b"stable bytes")
            original = tree_digest(root)
            self.assertEqual(original, tree_digest(root))

    def tree(self, root: Path, reverse: bool = False) -> None:
        root.mkdir()
        names = ["package.json", "cli.js", "payload.js"]
        for name in reversed(names) if reverse else names:
            (root / name).write_bytes(name.encode())
        (root / "empty").mkdir()

    def test_order_location_and_mtime_do_not_replace_content_identity(self):
        with tempfile.TemporaryDirectory() as directory:
            first, second = Path(directory) / "first", Path(directory) / "second"
            self.tree(first)
            self.tree(second, reverse=True)
            original = tree_digest(first)
            self.assertEqual(original, tree_digest(second))
            os.utime(first / "cli.js", (1, 1))
            self.assertEqual(original, tree_digest(first))

    def test_unchanged_launcher_does_not_hide_delegated_tree_changes(self):
        for mutation in ("payload", "add", "remove", "rename", "empty", "mode"):
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as d:
                root = Path(d) / "installed"
                self.tree(root)
                original = tree_digest(root)
                launcher = (root / "cli.js").read_bytes()
                payload = root / "payload.js"
                if mutation == "payload":
                    payload.write_bytes(b"changed delegated behavior")
                elif mutation == "add":
                    (root / "new.js").write_bytes(b"new")
                elif mutation == "remove":
                    payload.unlink()
                elif mutation == "rename":
                    payload.rename(root / "renamed.js")
                elif mutation == "empty":
                    (root / "another-empty").mkdir()
                else:
                    payload.chmod(stat.S_IREAD if os.name == "nt" else 0o700)
                try:
                    self.assertEqual(launcher, (root / "cli.js").read_bytes())
                    self.assertNotEqual(original["sha256"], tree_digest(root)["sha256"])
                finally:
                    if payload.exists():
                        payload.chmod(0o600)

    @unittest.skipIf(os.name == "nt", "Windows profile refuses reparse points")
    def test_internal_npm_file_link_binds_target_spelling_and_bytes(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d) / "installed"
            self.tree(root)
            link = root / "launcher"
            link.symlink_to("cli.js")
            original = tree_digest(root)
            link.unlink()
            link.symlink_to("./cli.js")
            self.assertNotEqual(original, tree_digest(root))
            original = tree_digest(root)
            (root / "cli.js").write_bytes(b"changed")
            self.assertNotEqual(original, tree_digest(root))

    @unittest.skipIf(os.name == "nt", "POSIX link/special-file cases")
    def test_unsupported_links_and_special_files_refuse(self):
        for kind in ("escape", "dangling", "cycle", "directory", "fifo", "root"):
            with self.subTest(kind=kind), tempfile.TemporaryDirectory() as d:
                root = Path(d) / "installed"
                self.tree(root)
                link = root / "link"
                if kind == "escape":
                    (Path(d) / "outside").write_bytes(b"outside")
                    link.symlink_to("../outside")
                elif kind == "dangling":
                    link.symlink_to("missing")
                elif kind == "cycle":
                    link.symlink_to("link")
                elif kind == "directory":
                    link.symlink_to("empty", target_is_directory=True)
                elif kind == "fifo":
                    os.mkfifo(link)
                else:
                    alias = Path(d) / "alias"
                    alias.symlink_to(root, target_is_directory=True)
                    root = alias
                with self.assertRaises(RuntimeError):
                    tree_digest(root)

    @unittest.skipIf(os.name == "nt", "Uses a POSIX root symlink swap")
    def test_persistent_root_replacement_during_resolution_refuses(self):
        with tempfile.TemporaryDirectory() as d:
            root, other = Path(d) / "installed", Path(d) / "replacement"
            self.tree(root)
            self.tree(other)
            resolve = Path.resolve
            replaced = False

            def swap(path, *args, **kwargs):
                nonlocal replaced
                if path == root and not replaced:
                    replaced = True
                    root.rename(Path(d) / "original")
                    root.symlink_to(other, target_is_directory=True)
                return resolve(path, *args, **kwargs)

            with (
                mock.patch.object(Path, "resolve", swap),
                self.assertRaises(RuntimeError),
            ):
                tree_digest(root)

    def test_limits_refuse_incomplete_observations(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d) / "installed"
            self.tree(root)
            for limit in ("MAX_TREE_ENTRIES", "MAX_TREE_BYTES"):
                with (
                    self.subTest(limit=limit),
                    mock.patch("installation_identity." + limit, 1),
                    self.assertRaises(RuntimeError),
                ):
                    tree_digest(root)

    @unittest.skipUnless(os.name == "nt", "Requires native Windows junctions")
    def test_windows_junction_entry_root_and_parent_refuse(self):
        with tempfile.TemporaryDirectory() as d:
            base = Path(d)
            root = base / "installed"
            self.tree(root)
            alias = base / "alias"
            subprocess.run(
                ["cmd", "/d", "/c", "mklink", "/J", str(alias), str(root)],
                check=True,
                capture_output=True,
            )
            try:
                for selected in (alias, alias / "empty"):
                    with (
                        self.subTest(selected=selected),
                        self.assertRaises(RuntimeError),
                    ):
                        tree_digest(selected)
                nested = root / "junction"
                subprocess.run(
                    [
                        "cmd",
                        "/d",
                        "/c",
                        "mklink",
                        "/J",
                        str(nested),
                        str(root / "empty"),
                    ],
                    check=True,
                    capture_output=True,
                )
                try:
                    with self.assertRaises(RuntimeError):
                        tree_digest(root)
                finally:
                    nested.rmdir()
            finally:
                alias.rmdir()


@unittest.skipUnless(
    shutil.which("node"), "Requires Node for the shipped-JS command fixture"
)
class InstalledReceiptTests(unittest.TestCase):
    def fixture(self, home):
        agent = next(
            a for a in install_real_agent.MANIFEST["agents"] if a["id"] == "codex"
        )
        prefix = installation_prefix(agent)
        package = prefix / "node_modules" / agent["installer"]["package"]
        package.mkdir(parents=True)
        (package / "package.json").write_text(
            json.dumps(
                {
                    "name": agent["installer"]["package"],
                    "version": agent["version"],
                    "type": "module",
                    "bin": {"codex": "cli.js"},
                }
            )
        )
        (package / "cli.js").write_text(
            'import "./delegate.js"; console.log("codex-cli ' + agent["version"] + '");'
        )
        (package / "delegate.js").write_text('export const value = "original";')
        launcher = (
            prefix / "node_modules/.bin" / ("codex.cmd" if os.name == "nt" else "codex")
        )
        launcher.parent.mkdir()
        if os.name == "nt":
            launcher.write_text('@echo off\nnode "%~dp0/../@openai/codex/cli.js" %*\n')
        else:
            launcher.symlink_to("../@openai/codex/cli.js")
        platform = (
            "windows"
            if os.name == "nt"
            else "macos"
            if __import__("sys").platform == "darwin"
            else "ubuntu"
        )
        receipt = install_real_agent.executable_receipt(agent, platform, "native")
        return agent, platform, receipt, package

    def test_direct_node_receipt_ignores_path_shadow_and_rejects_delegate_change(self):
        with (
            tempfile.TemporaryDirectory() as d,
            mock.patch.object(Path, "home", return_value=Path(d)),
        ):
            agent, platform, receipt, package = self.fixture(Path(d))
            self.assertEqual(receipt["schema_version"], 2)
            with mock.patch.dict(
                os.environ,
                {
                    "PATH": str(Path(d) / "no-runtime"),
                    "NODE_OPTIONS": "--require missing-module",
                    "NODE_PATH": str(Path(d) / "foreign"),
                },
            ):
                self.assertEqual(
                    agent_identity.inspect_receipt(receipt, agent, platform), receipt
                )
            (package / "delegate.js").write_text('export const value = "changed";')
            with self.assertRaisesRegex(RuntimeError, "package tree changed"):
                agent_identity.inspect_receipt(receipt, agent, platform)

    def test_runtime_change_fails_before_launch(self):
        with (
            tempfile.TemporaryDirectory() as d,
            mock.patch.object(Path, "home", return_value=Path(d)),
        ):
            _, _, receipt, _ = self.fixture(Path(d))
            node = Path(d) / "observed-node"
            node.write_bytes(b"original")
            receipt["installation"]["node"]["path"] = str(node)
            receipt["installation"]["node"]["sha256"] = hashlib.sha256(
                b"original"
            ).hexdigest()
            node.write_bytes(b"changed")
            with (
                mock.patch.object(
                    agent_identity.subprocess,
                    "run",
                    side_effect=AssertionError("must not execute"),
                ),
                self.assertRaisesRegex(RuntimeError, "Node runtime changed"),
            ):
                agent_identity.verify_live_entrypoint(receipt)

    def test_legacy_priority_receipt_cannot_bypass_tree_identity(self):
        with (
            tempfile.TemporaryDirectory() as d,
            mock.patch.object(Path, "home", return_value=Path(d)),
        ):
            agent, platform, receipt, _ = self.fixture(Path(d))
            receipt.pop("installation")
            receipt["schema_version"] = 1
            with self.assertRaisesRegex(RuntimeError, "receipt fields"):
                agent_identity.inspect_receipt(receipt, agent, platform)

    def test_forged_node_version_and_untyped_tree_counts_refuse(self):
        with (
            tempfile.TemporaryDirectory() as d,
            mock.patch.object(Path, "home", return_value=Path(d)),
        ):
            agent, platform, receipt, _ = self.fixture(Path(d))
            bad = copy.deepcopy(receipt)
            bad["installation"]["node"]["version"] = "v0.0.0"
            with self.assertRaisesRegex(RuntimeError, "Node version changed"):
                agent_identity.inspect_receipt(bad, agent, platform)
            for value in (True, 1.0):
                bad = copy.deepcopy(receipt)
                bad["installation"]["tree"]["entries"] = value
                with (
                    mock.patch.object(
                        agent_identity.subprocess,
                        "run",
                        side_effect=AssertionError("must not execute"),
                    ),
                    self.assertRaisesRegex(RuntimeError, "invalid installation tree"),
                ):
                    agent_identity.inspect_receipt(bad, agent, platform)

    @unittest.skipIf(os.name == "nt", "POSIX redirected installer parent")
    def test_installer_rejects_redirected_parent_before_npm(self):
        with (
            tempfile.TemporaryDirectory() as d,
            mock.patch.object(Path, "home", return_value=Path(d)),
        ):
            agent = next(
                a for a in install_real_agent.MANIFEST["agents"] if a["id"] == "codex"
            )
            outside = Path(d) / "foreign"
            outside.mkdir()
            (Path(d) / ".local").symlink_to(outside, target_is_directory=True)
            with (
                mock.patch.object(
                    install_real_agent,
                    "run",
                    side_effect=AssertionError("npm must not execute"),
                ),
                self.assertRaisesRegex(RuntimeError, "parent"),
            ):
                install_real_agent.install(agent, "macos", "native")
            self.assertEqual(list(outside.iterdir()), [])

    @unittest.skipUnless(
        os.name == "nt", "Native Windows pre-install junction boundary"
    )
    def test_installer_rejects_junction_before_creating_foreign_files(self):
        with (
            tempfile.TemporaryDirectory() as d,
            mock.patch.object(Path, "home", return_value=Path(d)),
        ):
            home = Path(d)
            outside = home / "foreign"
            outside.mkdir()
            alias = home / ".local"
            subprocess.run(
                ["cmd", "/d", "/c", "mklink", "/J", str(alias), str(outside)],
                check=True,
                capture_output=True,
            )
            try:
                agent = next(
                    a
                    for a in install_real_agent.MANIFEST["agents"]
                    if a["id"] == "codex"
                )
                with (
                    mock.patch.object(
                        install_real_agent,
                        "run",
                        side_effect=AssertionError("npm must not execute"),
                    ),
                    self.assertRaisesRegex(RuntimeError, "reparse"),
                ):
                    install_real_agent.install(agent, "windows", "native")
                self.assertEqual(list(outside.iterdir()), [])
            finally:
                alias.rmdir()

    def test_failed_probe_retains_streams_without_emitting_raw_secrets(self):
        with (
            tempfile.TemporaryDirectory() as d,
            mock.patch.object(Path, "home", return_value=Path(d)),
        ):
            agent, _, receipt, package = self.fixture(Path(d))
            (package / "cli.js").write_text(
                'console.log("stdout marker"); console.error("private-token-marker"); process.exit(7);'
            )
            value = receipt["installation"]
            value["tree"] = tree_digest(Path(value["prefix"]))
            with self.assertRaises(agent_identity.VersionProbeError) as raised:
                agent_identity.probe_version(
                    agent, receipt["executable"], "native", value
                )
            error = raised.exception
            self.assertEqual(error.exit_code, 7)
            self.assertEqual(error.stdout, b"stdout marker\n")
            self.assertEqual(error.stderr, b"private-token-marker\n")
            self.assertNotIn("private-token-marker", str(error))
            self.assertNotIn("private-token-marker", json.dumps(error.diagnostic))

    def test_timeout_retains_separate_partial_streams(self):
        with (
            tempfile.TemporaryDirectory() as d,
            mock.patch.object(Path, "home", return_value=Path(d)),
        ):
            agent, _, receipt, _ = self.fixture(Path(d))
            run = subprocess.run

            def timeout(command, **kwargs):
                if len(command) == 3 and command[-1] == "--version":
                    raise subprocess.TimeoutExpired(
                        command, 30, output=b"partial stdout", stderr=b"partial stderr"
                    )
                return run(command, **kwargs)

            with (
                mock.patch.object(
                    agent_identity.subprocess, "run", side_effect=timeout
                ),
                self.assertRaises(agent_identity.VersionProbeError) as raised,
            ):
                agent_identity.probe_version(
                    agent, receipt["executable"], "native", receipt["installation"]
                )
            self.assertEqual(raised.exception.reason, "timed-out")
            self.assertEqual(raised.exception.stdout, b"partial stdout")
            self.assertEqual(raised.exception.stderr, b"partial stderr")
            oversized = agent_identity.VersionProbeError(
                "output-limit", b"x" * 100_000, b"y" * 100_000
            )
            self.assertEqual(len(oversized.stdout), agent_identity.MAX_VERSION_BYTES)
            self.assertTrue(oversized.diagnostic["stderr"]["truncated"])

    def test_nonexistent_reported_node_path_is_private_failure(self):
        from installation_identity import observe_installation

        with (
            tempfile.TemporaryDirectory() as d,
            mock.patch.object(Path, "home", return_value=Path(d)),
        ):
            agent, _, _, _ = self.fixture(Path(d))
            missing = str(Path(d) / "private-missing-runtime").encode()
            with (
                mock.patch(
                    "installation_identity.node_probe",
                    return_value=subprocess.CompletedProcess([], 0, missing, b""),
                ),
                self.assertRaises(agent_identity.VersionProbeError) as caught,
            ):
                observe_installation(agent)
            self.assertEqual(caught.exception.reason, "node-invalid-runtime-path")
            self.assertEqual(caught.exception.stdout, missing)
            self.assertNotIn("private-missing-runtime", str(caught.exception))

    def test_malformed_node_output_retains_bounded_diagnostics(self):
        from installation_identity import node_output

        for out, err in (
            (b"\xff", b""),
            (b"v1.2.3", b"private"),
            (b"x" * 100_000, b""),
        ):
            with (
                self.subTest(size=len(out)),
                self.assertRaises(agent_identity.VersionProbeError) as caught,
            ):
                node_output(subprocess.CompletedProcess([], 0, out, err))
            self.assertLessEqual(
                len(caught.exception.stdout), agent_identity.MAX_VERSION_BYTES
            )

    def test_node_probe_failure_keeps_private_streams(self):
        from installation_identity import node_probe, persist_probe_error

        with (
            mock.patch(
                "installation_identity.subprocess.run",
                side_effect=subprocess.CalledProcessError(
                    9, ["node"], output=b"out", stderr=b"secret"
                ),
            ),
            self.assertRaises(agent_identity.VersionProbeError) as caught,
        ):
            node_probe(["node", "--version"])
        with tempfile.TemporaryDirectory() as d:
            output = Path(d) / "receipt.json"
            persist_probe_error(caught.exception, output)
            self.assertEqual(
                output.with_suffix(".probe-private-stderr").read_bytes(), b"secret"
            )
            self.assertNotIn("secret", output.with_suffix(".probe.json").read_text())

    def test_probe_failure_survives_identity_recheck_failure(self):
        with (
            tempfile.TemporaryDirectory() as d,
            mock.patch.object(Path, "home", return_value=Path(d)),
        ):
            agent, _, receipt, _ = self.fixture(Path(d))
            with (
                mock.patch.object(
                    agent_identity,
                    "verify_installation",
                    side_effect=[None, RuntimeError("changed")],
                ),
                mock.patch.object(
                    agent_identity.subprocess,
                    "run",
                    side_effect=subprocess.CalledProcessError(
                        7, [], output=b"out", stderr=b"err"
                    ),
                ),
                self.assertRaises(agent_identity.VersionProbeError) as caught,
            ):
                agent_identity.probe_version(
                    agent, receipt["executable"], "native", receipt["installation"]
                )
            self.assertEqual(caught.exception.stderr, b"err")
            self.assertEqual(caught.exception.diagnostic["identity_recheck"], "failed")

    def test_completed_probe_streams_survive_failed_identity_recheck(self):
        with (
            tempfile.TemporaryDirectory() as d,
            mock.patch.object(Path, "home", return_value=Path(d)),
        ):
            agent, _, receipt, _ = self.fixture(Path(d))
            with (
                mock.patch.object(
                    agent_identity,
                    "verify_installation",
                    side_effect=[None, RuntimeError("changed")],
                ),
                mock.patch.object(
                    agent_identity.subprocess,
                    "run",
                    return_value=subprocess.CompletedProcess(
                        [], 0, b"unexpected banner", b"private output"
                    ),
                ),
                self.assertRaises(agent_identity.VersionProbeError) as caught,
            ):
                agent_identity.probe_version(
                    agent, receipt["executable"], "native", receipt["installation"]
                )
            self.assertEqual(caught.exception.reason, "identity-recheck-failed")
            self.assertEqual(caught.exception.stdout, b"unexpected banner")
            self.assertEqual(caught.exception.stderr, b"private output")
            self.assertNotIn("private output", str(caught.exception))

    def test_qwen_null_pin_is_authored_after_inherited_override_removal(self):
        with tempfile.TemporaryDirectory() as d:
            home = str(Path(d) / "qwen")
            entry = str(Path(d) / "package/cli-entry.js")
            original = {
                "QWEN_HOME": home,
                "QWEN_CODE_MANAGED_NPM_PIN": "foreign",
                "NODE_OPTIONS": "foreign",
            }
            result = installation_environment("qwen", {"entrypoint": entry}, original)
            pin = json.loads(result["QWEN_CODE_MANAGED_NPM_PIN"])
            self.assertEqual(
                pin,
                {
                    "bootstrap": entry,
                    "version": None,
                    "updateRoot": str(Path(home) / "updates/npm"),
                },
            )
            self.assertNotIn("NODE_OPTIONS", result)
            self.assertEqual(original["NODE_OPTIONS"], "foreign")


if __name__ == "__main__":
    unittest.main()
