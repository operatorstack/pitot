from __future__ import annotations

import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

import agent_identity as identity

ROOT = Path(__file__).resolve().parents[1]
CATALOG = json.loads((ROOT / "adapter-verification.json").read_text())
AGENTS = {agent["id"]: agent for agent in CATALOG["agents"]}


class VersionAdmissionTests(unittest.TestCase):
    def test_copilot_extraction_diagnostic_is_retained_without_changing_identity(self):
        agent = AGENTS["copilot"]
        output = (
            f"GitHub Copilot CLI {agent['version']}.\n"
            "Run 'copilot update' to check for updates.\n"
        )
        warning = b"Package extraction took 6148ms\n"
        with (
            mock.patch.object(identity, "entrypoint_digest", return_value="a" * 64),
            mock.patch.object(
                identity.subprocess,
                "run",
                return_value=subprocess.CompletedProcess(
                    [], 0, output.encode(), warning
                ),
            ),
            mock.patch.object(identity.sys, "stderr") as stream,
        ):
            self.assertEqual(
                identity.probe_version(agent, "/owned", "native"),
                (output, "a" * 64),
            )
            stream.buffer.write.assert_called_once_with(warning)
            stream.buffer.flush.assert_called_once_with()

    def test_extraction_diagnostic_does_not_bypass_other_identity_guards(self):
        agent = AGENTS["copilot"]
        warning = b"Package extraction took 6148ms\n"
        for output, digests in [
            (b"GitHub Copilot CLI 0.0.0.\n", ["a" * 64]),
            (
                f"GitHub Copilot CLI {agent['version']}.\n"
                "Run 'copilot update' to check for updates.\n".encode(),
                ["a" * 64, "b" * 64],
            ),
        ]:
            with (
                mock.patch.object(identity, "entrypoint_digest", side_effect=digests),
                mock.patch.object(
                    identity.subprocess,
                    "run",
                    return_value=subprocess.CompletedProcess([], 0, output, warning),
                ),
                mock.patch.object(identity.sys, "stderr") as stream,
                self.assertRaises(RuntimeError),
            ):
                identity.probe_version(agent, "/owned", "native")
            stream.buffer.write.assert_not_called()

    def test_extraction_diagnostic_has_a_closed_agent_and_output_shape(self):
        for product in AGENTS:
            self.assertTrue(identity.stderr_matches(AGENTS[product], ""))
            self.assertEqual(
                identity.stderr_matches(
                    AGENTS[product], "Package extraction took 5001ms\n"
                ),
                product == "copilot",
            )
        self.assertFalse(identity.stderr_matches({"id": "unknown"}, ""))
        for error in [
            "Package extraction took 5000ms\n",
            "Package extraction took 06148ms\n",
            "Package extraction took 6148ms\r\n",
            "Package extraction took 6148ms",
            "Package extraction took 6148ms\nError extracting bundled package: bad\n",
            "Error extracting bundled package: bad\n",
        ]:
            with self.subTest(error=error):
                self.assertFalse(identity.stderr_matches(AGENTS["copilot"], error))

    def test_complete_product_banners(self):
        for product, agent in AGENTS.items():
            pin = agent["version"]
            banner = {
                "claude": f"{pin} (Claude Code)",
                "codex": f"codex-cli {pin}",
                "copilot": f"GitHub Copilot CLI {pin}.\nRun 'copilot update' to check for updates.",
                "devin": f"devin {pin} (d5152ff5)",
            }.get(product, pin)
            with self.subTest(product=product):
                self.assertTrue(identity.version_matches(agent, banner))
                self.assertTrue(
                    identity.version_matches(
                        agent, " \t" + banner.replace("\n", "\r\n") + "\r\n"
                    )
                )
                for wrong in [
                    banner + "0",
                    "expected " + banner,
                    banner + "\nactual 0.0.1",
                    "\u00a0" + banner,
                    banner + "\u00a0",
                ]:
                    self.assertFalse(identity.version_matches(agent, wrong), wrong)

    def test_near_version_is_not_a_pin(self):
        for banner in ["0.29.01", "0.29.00", "expected0.29.0;actual0.0.1"]:
            self.assertFalse(identity.version_matches(AGENTS["kimi"], banner))

    def test_devin_commit_label_is_bounded_and_lowercase(self):
        agent = AGENTS["devin"]
        for label in ["d5152ff", "D5152FF5", "g5152ff5", "a" * 41]:
            self.assertFalse(
                identity.version_matches(agent, f"devin {agent['version']} ({label})")
            )


class OwnedEntrypointTests(unittest.TestCase):
    def test_release_path_ignores_shadowed_path(self):
        with mock.patch.object(
            identity.shutil, "which", side_effect=AssertionError("agent PATH lookup")
        ):
            path = identity.owned_entrypoint(AGENTS["cursor"], "macos", "native")
            self.assertEqual(
                path,
                str(
                    Path.home()
                    / ".local/share/pitot-cursor"
                    / AGENTS["cursor"]["version"]
                    / "cursor-agent"
                ),
            )
            self.assertEqual(
                identity.owned_entrypoint(AGENTS["cursor"], "windows", "wsl"),
                f"/usr/local/share/pitot-cursor/{AGENTS['cursor']['version']}/cursor-agent",
            )

    def test_npm_prefix_selects_launcher_without_path_fallback(self):
        prefix = str(Path.home() / "owned prefix")
        with (
            mock.patch.object(identity, "npm_executable", return_value="npm"),
            mock.patch.object(
                identity.subprocess,
                "run",
                return_value=subprocess.CompletedProcess([], 0, prefix + "\n", ""),
            ),
        ):
            expected = str(Path(prefix) / "bin/claude")
            self.assertEqual(
                identity.owned_entrypoint(AGENTS["claude"], "ubuntu", "native"),
                expected,
            )
            with self.assertRaisesRegex(RuntimeError, "regular file"):
                identity.entrypoint_digest(expected, "native")

    def test_wsl_paths_are_passed_as_one_argument(self):
        path = "/owned path/agent;literal"
        digest = "a" * 64
        with (
            mock.patch.object(identity, "host_controls_wsl", return_value=True),
            mock.patch.object(
                identity.subprocess,
                "run",
                return_value=subprocess.CompletedProcess(
                    [], 0, digest + "  " + path + "\n", ""
                ),
            ) as run,
        ):
            self.assertEqual(identity.entrypoint_digest(path, "wsl"), digest)
            self.assertEqual(
                run.call_args.args[0],
                ["wsl.exe", "--distribution", "Ubuntu", "--", "sha256sum", "--", path],
            )
        with (
            mock.patch.object(identity, "host_controls_wsl", return_value=True),
            mock.patch.object(identity, "entrypoint_digest", return_value=digest),
            mock.patch.object(
                identity.subprocess,
                "run",
                return_value=subprocess.CompletedProcess([], 0, b"0.29.0\n", b""),
            ) as run,
        ):
            self.assertEqual(
                identity.probe_version(AGENTS["kimi"], path, "wsl"),
                ("0.29.0\n", digest),
            )
            self.assertEqual(
                run.call_args.args[0],
                ["wsl.exe", "--distribution", "Ubuntu", "--", path, "--version"],
            )


class ReceiptAdmissionTests(unittest.TestCase):
    def receipt(self):
        agent = AGENTS["kimi"]
        return {
            "schema_version": 1,
            "agent": "kimi",
            "version": agent["version"],
            "executable": str(Path.home() / "owned"),
            "executable_sha256": "a" * 64,
            "version_output": agent["version"] + "\n",
            "installer": "kimi_release",
            "runtime": "native",
            "platform": "macos",
        }

    def test_wrong_cell_and_schema_reject_before_file_or_process(self):
        original = self.receipt()
        variants = [
            None,
            [],
            {**original, "extra": True},
            {key: value for key, value in original.items() if key != "version_output"},
        ]
        for key, value in {
            "schema_version": True,
            "agent": "cursor",
            "version": "0.29.01",
            "installer": "npm",
            "runtime": "wsl",
            "platform": "windows",
            "executable_sha256": "A" * 64,
            "executable": 42,
            "version_output": "0.29.01",
        }.items():
            variants.append({**original, key: value})
        with (
            mock.patch.object(
                identity, "probe_version", side_effect=AssertionError("must not launch")
            ),
            mock.patch.object(
                identity,
                "verify_live_entrypoint",
                side_effect=AssertionError("must not read"),
            ),
            mock.patch.object(
                identity, "owned_entrypoint", return_value=original["executable"]
            ),
        ):
            for value in variants:
                with self.subTest(value=value), self.assertRaises(RuntimeError):
                    identity.inspect_receipt(value, AGENTS["kimi"], "macos")

    def test_unowned_receipt_rejects_even_with_matching_version(self):
        with (
            mock.patch.object(
                identity, "owned_entrypoint", return_value="/actual/owned"
            ),
            self.assertRaisesRegex(RuntimeError, "owned entrypoint"),
        ):
            identity.inspect_receipt(self.receipt(), AGENTS["kimi"], "macos")

    def test_driver_invalid_receipt_fails_before_evidence_effects(self):
        with tempfile.TemporaryDirectory() as directory:
            base = Path(directory)
            receipt = base / "receipt.json"
            receipt.write_text(json.dumps({"schema_version": True}))
            evidence = base / "evidence.json"
            command = [
                sys.executable,
                str(ROOT / "tests/real_agent_driver.py"),
                "--agent",
                "kimi",
                "--platform",
                "macos",
                "--installation",
                str(receipt),
                "--evidence",
                str(evidence),
                "--pitot",
                "missing-pitot",
                "--witness",
                "missing-witness",
                "--test-role",
                "missing-role",
            ]
            result = subprocess.run(command, capture_output=True, timeout=10)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn(b"invalid installation receipt fields", result.stderr)
            self.assertFalse(evidence.exists())

    @unittest.skipIf(os.name == "nt", "portable POSIX controlled executable")
    def test_real_probe_preserves_raw_banner_and_rejects_replacement(self):
        with tempfile.TemporaryDirectory(prefix="agent path space ") as directory:
            path = Path(directory) / "kimi"
            path.write_text("#!/bin/sh\nprintf '0.29.0\\n'\n")
            path.chmod(0o755)
            output, digest = identity.probe_version(AGENTS["kimi"], str(path), "native")
            self.assertEqual(output, "0.29.0\n")
            self.assertEqual(
                identity.probe_version(AGENTS["kimi"], str(path), "wsl"),
                (output, digest),
            )
            receipt = self.receipt()
            receipt.update(executable=str(path), executable_sha256=digest)
            with mock.patch.object(
                identity, "owned_entrypoint", return_value=str(path)
            ):
                self.assertEqual(
                    identity.inspect_receipt(receipt, AGENTS["kimi"], "macos"), receipt
                )
                path.write_text("#!/bin/sh\nprintf '0.29.01\\n'\n")
                with self.assertRaisesRegex(RuntimeError, "changed since"):
                    identity.inspect_receipt(receipt, AGENTS["kimi"], "macos")
            with self.assertRaisesRegex(RuntimeError, "exact supervised version"):
                identity.probe_version(AGENTS["kimi"], str(path), "native")

    def test_probe_failure_timeout_encoding_and_changed_bytes(self):
        agent = AGENTS["kimi"]
        for result in [
            subprocess.CompletedProcess([], 0, b"0.29.0\n", b"warning"),
            subprocess.CompletedProcess([], 0, b"x" * 65537, b""),
        ]:
            with (
                mock.patch.object(identity, "entrypoint_digest", return_value="a" * 64),
                mock.patch.object(identity.subprocess, "run", return_value=result),
                self.assertRaises(RuntimeError),
            ):
                identity.probe_version(agent, "/owned", "native")
        for error in [
            subprocess.TimeoutExpired([], 30),
            subprocess.CalledProcessError(1, []),
        ]:
            with (
                mock.patch.object(identity, "entrypoint_digest", return_value="a" * 64),
                mock.patch.object(identity.subprocess, "run", side_effect=error),
                self.assertRaises(type(error)),
            ):
                identity.probe_version(agent, "/owned", "native")
        with (
            mock.patch.object(identity, "entrypoint_digest", return_value="a" * 64),
            mock.patch.object(
                identity.subprocess,
                "run",
                return_value=subprocess.CompletedProcess([], 0, b"\xff", b""),
            ),
            self.assertRaises(UnicodeDecodeError),
        ):
            identity.probe_version(agent, "/owned", "native")
        with (
            mock.patch.object(
                identity, "entrypoint_digest", side_effect=["a" * 64, "b" * 64]
            ),
            mock.patch.object(
                identity.subprocess,
                "run",
                return_value=subprocess.CompletedProcess([], 0, b"0.29.0\n", b""),
            ),
            self.assertRaisesRegex(RuntimeError, "during version probe"),
        ):
            identity.probe_version(agent, "/owned", "native")


if __name__ == "__main__":
    unittest.main()
