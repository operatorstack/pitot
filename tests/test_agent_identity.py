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
    def test_deepseek_environment_is_owned_and_version_is_exact(self):
        original = {
            "DSH_HOME": "/foreign",
            "NODE_OPTIONS": "--require /foreign",
            "DEEPSEEK_API_KEY": "ambient",
            "HTTPS_PROXY": "http://foreign",
            "KEEP": "value",
        }
        self.assertEqual(
            identity.agent_environment("deepseek", original), {"KEEP": "value"}
        )
        self.assertEqual(original["DSH_HOME"], "/foreign")
        self.assertTrue(identity.version_matches(AGENTS["deepseek"], "0.2.0-rc.2\n"))
        for banner in ("0.2.0", "dsh 0.2.0-rc.2", "0.2.0-rc.2\nextra"):
            self.assertFalse(identity.version_matches(AGENTS["deepseek"], banner))

    def test_devin_environment_removes_inherited_provider_and_model_overrides(self):
        original = {
            "DEVIN_MODEL": "ambient-model",
            "DEVIN_HARNESS": "ambient-agent",
            "DEVIN_REFUSAL_FALLBACK": "ambient-fallback",
            "DEVIN_REMOTE_SESSION_TOKEN": "ambient-token",
            "WINDSURF_API_KEY": "ambient-key",
            "WINDSURF_API_SERVER_URL": "https://unrelated.invalid",
            "OTEL_EXPORTER_OTLP_ENDPOINT": "https://unrelated.invalid",
            "HTTPS_PROXY": "https://unrelated.invalid",
            "KEEP": "unchanged",
        }
        before = dict(original)
        self.assertEqual(
            identity.agent_environment("devin", original), {"KEEP": "unchanged"}
        )
        self.assertEqual(original, before)

    def test_devin_version_probe_owns_configuration_and_cleans_all_exits(self):
        agent = AGENTS["devin"]
        banner = f"devin {agent['version']} (9c803229faa4)\n".encode()
        outcomes = (
            subprocess.CompletedProcess([], 0, banner, b""),
            subprocess.CompletedProcess([], 0, b"unrelated version\n", b""),
            subprocess.TimeoutExpired(["devin"], 30),
        )
        homes = []
        for outcome in outcomes:
            with self.subTest(outcome=type(outcome).__name__):

                def run(command, **kwargs):
                    env = kwargs["env"]
                    home = Path(env["HOME"])
                    homes.append(home)
                    self.assertNotEqual(env["HOME"], "/ambient-home")
                    self.assertEqual(env["USERPROFILE"], str(home))
                    for key in (
                        "DEVIN_MODEL",
                        "WINDSURF_API_KEY",
                        "OTEL_EXPORTER_OTLP_ENDPOINT",
                    ):
                        self.assertNotIn(key, env)
                    config = json.loads(
                        (Path(env["XDG_CONFIG_HOME"]) / "devin/config.json").read_text()
                    )
                    self.assertIs(config["auto_update"], False)
                    self.assertIs(config["otel"]["enabled"], False)
                    self.assertEqual(config["proxy"]["mode"], "off")
                    self.assertEqual(len(config["read_config_from"]), 7)
                    self.assertFalse(any(config["read_config_from"].values()))
                    self.assertEqual(
                        json.loads(
                            (Path(env["APPDATA"]) / "devin/config.json").read_text()
                        ),
                        config,
                    )
                    if isinstance(outcome, Exception):
                        raise outcome
                    return outcome

                with (
                    mock.patch.dict(
                        os.environ,
                        {
                            "HOME": "/ambient-home",
                            "DEVIN_MODEL": "ambient",
                            "WINDSURF_API_KEY": "ambient",
                            "OTEL_EXPORTER_OTLP_ENDPOINT": "ambient",
                        },
                    ),
                    mock.patch.object(
                        identity, "entrypoint_digest", return_value="a" * 64
                    ),
                    mock.patch.object(identity.subprocess, "run", side_effect=run),
                ):
                    if outcome is outcomes[0]:
                        self.assertEqual(
                            identity.probe_version(agent, "/owned/devin", "native"),
                            (banner.decode(), "a" * 64),
                        )
                    else:
                        with self.assertRaises(
                            (RuntimeError, subprocess.TimeoutExpired)
                        ):
                            identity.probe_version(agent, "/owned/devin", "native")
                self.assertFalse(homes[-1].exists())
        self.assertEqual(len(set(homes)), len(outcomes))

    def test_devin_fixture_restores_only_its_authored_credentials(self):
        import real_agent_driver as driver

        with tempfile.TemporaryDirectory() as temporary:
            home = Path(temporary)
            _, authored = driver.configure(
                "devin",
                home,
                home,
                "/owned/witness",
                "http://127.0.0.1:32123",
                pitot_command="/owned/pitot",
                witness_receipt="/owned/receipt",
                nonce="a" * 32,
                runtime_path="/owned/runtime",
            )
            inherited = {
                "WINDSURF_API_KEY": "ambient-key",
                "DEVIN_MODEL": "ambient-model",
            }
            environment = identity.agent_environment("devin", {**inherited, **authored})
            environment.update(authored)
            self.assertEqual(environment["WINDSURF_API_KEY"], "pitot-local-only")
            self.assertEqual(
                environment["WINDSURF_API_SERVER_URL"], "http://127.0.0.1:32123"
            )
            self.assertNotIn("DEVIN_MODEL", environment)
            self.assertEqual(environment["HOME"], str(home))

    def test_qwen_environment_filters_bootstrap_hints_without_mutating_input(self):
        original = {
            "CLI_VERSION": "9.9.9",
            "QWEN_CODE_RELAUNCH_ARGS": '["--version"]',
            "QWEN_CODE_MANAGED_NPM_PIN": '{"updateRoot":"/unrelated"}',
            "QWEN_CODE_LAUNCHER_PATH": "/unrelated/qwen",
            "QWEN_CODE_STARTUP_VERSION": "9.9.9",
            "QWEN_HOME": "/owned/.qwen",
            "KEEP": "unchanged",
        }
        before = dict(original)
        filtered = identity.agent_environment("qwen", original)
        self.assertEqual(filtered, {"QWEN_HOME": "/owned/.qwen", "KEEP": "unchanged"})
        self.assertEqual(original, before)
        self.assertIsNot(filtered, original)
        for product in AGENTS.keys() - {"qwen", "kimi", "cursor", "deepseek"}:
            self.assertIs(identity.agent_environment(product, original), original)
        self.assertEqual(identity.agent_environment("kimi", original), original)

    def test_kimi_environment_keeps_the_authored_model_and_request_controls(self):
        inherited = {
            "KIMI_MODEL_NAME": "ambient-model",
            "KIMI_MODEL_API_KEY": "ambient-fake-key",
            "KIMI_MODEL_BASE_URL": "http://127.0.0.1:1/v1",
            "KIMI_MODEL_PROVIDER_TYPE": "anthropic",
            "KIMI_MODEL_CAPABILITIES": "vision",
            "KIMI_MODEL_DISPLAY_NAME": "ambient-display",
            "KIMI_MODEL_MAX_CONTEXT_SIZE": "1",
            "KIMI_MODEL_MAX_OUTPUT_SIZE": "1",
            "KIMI_MODEL_MAX_COMPLETION_TOKENS": "1",
            "KIMI_MODEL_MAX_TOKENS": "1",
            "KIMI_MODEL_TEMPERATURE": "1.5",
            "KIMI_MODEL_TOP_P": "0.1",
            "KIMI_MODEL_THINKING_KEEP": "all",
            "KIMI_MODEL_REASONING_KEY": "ambient-reasoning",
            "KIMI_MODEL_ADAPTIVE_THINKING": "true",
            "KIMI_MODEL_THINKING_EFFORT": "high",
        }
        preserved = {
            "KEEP": "unchanged",
            "KIMI_MODEL_CUSTOM_EXTENSION": "not-a-known-override",
            "KIMI_CODE_HOME": "/owned/.kimi-code",
            "KIMI_CODE_CACHE_DIR": "/owned/cache",
            "CLI_VERSION": "9.9.9",
        }
        original = {**inherited, **preserved}
        before = dict(original)
        self.assertEqual(identity.agent_environment("kimi", original), preserved)
        self.assertEqual(original, before)
        self.assertEqual(
            identity.agent_environment("qwen", original),
            {key: value for key, value in original.items() if key != "CLI_VERSION"},
        )
        for product in AGENTS.keys() - {"kimi", "qwen", "cursor", "deepseek"}:
            self.assertIs(identity.agent_environment(product, original), original)
        # Sampling overrides also apply when no environment model is selected.
        without_name = {
            key: value for key, value in original.items() if key != "KIMI_MODEL_NAME"
        }
        self.assertEqual(identity.agent_environment("kimi", without_name), preserved)

    def test_kimi_version_probe_cleans_owned_state_and_cache_on_all_exits(self):
        agent = AGENTS["kimi"]
        success = subprocess.CompletedProcess(
            [], 0, f"{agent['version']}\n".encode(), b""
        )
        outcomes = [
            (success, None, ["a" * 64, "a" * 64]),
            (subprocess.TimeoutExpired([], 30), subprocess.TimeoutExpired, ["a" * 64]),
            (
                subprocess.CalledProcessError(1, []),
                subprocess.CalledProcessError,
                ["a" * 64],
            ),
            (
                subprocess.CompletedProcess([], 0, b"x" * 65537, b""),
                RuntimeError,
                ["a" * 64],
            ),
            (
                subprocess.CompletedProcess([], 0, b"0.0.0\n", b""),
                RuntimeError,
                ["a" * 64],
            ),
            (
                subprocess.CompletedProcess([], 0, b"\xff", b""),
                UnicodeDecodeError,
                ["a" * 64],
            ),
            (
                subprocess.CompletedProcess([], 0, success.stdout, b"warning"),
                RuntimeError,
                ["a" * 64],
            ),
            (success, RuntimeError, ["a" * 64, "b" * 64]),
        ]
        seen = []
        for outcome, expected_error, digests in outcomes:
            with self.subTest(
                outcome=type(outcome).__name__, error=expected_error, digests=digests
            ):
                homes = []

                def observe(command, **options):
                    self.assertEqual(command, ["/owned/kimi", "--version"])
                    self.assertEqual(options["timeout"], identity.VERSION_TIMEOUT)
                    environment = options["env"]
                    self.assertNotIn("KIMI_MODEL_NAME", environment)
                    self.assertNotIn("KIMI_MODEL_TEMPERATURE", environment)
                    home = Path(environment["HOME"])
                    self.assertTrue(home.is_absolute() and home.is_dir())
                    self.assertEqual(environment["USERPROFILE"], str(home))
                    self.assertEqual(
                        environment["KIMI_CODE_HOME"], str(home / ".kimi-code")
                    )
                    cache = Path(environment["KIMI_CODE_CACHE_DIR"])
                    self.assertEqual(cache, home / ".cache" / "kimi-code")
                    self.assertEqual(environment["KIMI_CODE_NO_AUTO_UPDATE"], "1")
                    self.assertEqual(
                        environment["XDG_CACHE_HOME"], "/ambient/xdg-cache"
                    )
                    self.assertEqual(
                        environment["LOCALAPPDATA"], "/ambient/local-app-data"
                    )
                    self.assertEqual(environment["KEEP"], "unchanged")
                    # Native startup materializes assets before printing a version.
                    cache.mkdir(parents=True)
                    (cache / "native-asset").write_bytes(b"owned")
                    state = home / ".kimi-code"
                    state.mkdir()
                    (state / "state.json").write_text("{}")
                    homes.append(home)
                    if isinstance(outcome, Exception):
                        raise outcome
                    return outcome

                with (
                    mock.patch.dict(
                        os.environ,
                        {
                            "HOME": "/ambient/home",
                            "USERPROFILE": "/ambient/profile",
                            "KIMI_CODE_HOME": "/ambient/.kimi-code",
                            "KIMI_CODE_CACHE_DIR": "/ambient/cache",
                            "KIMI_CODE_NO_AUTO_UPDATE": "0",
                            "KIMI_MODEL_NAME": "ambient-model",
                            "KIMI_MODEL_TEMPERATURE": "1.5",
                            "XDG_CACHE_HOME": "/ambient/xdg-cache",
                            "LOCALAPPDATA": "/ambient/local-app-data",
                            "KEEP": "unchanged",
                        },
                    ),
                    mock.patch.object(
                        identity, "entrypoint_digest", side_effect=digests
                    ),
                    mock.patch.object(identity, "require_kimi_ready"),
                    mock.patch.object(identity.subprocess, "run", side_effect=observe),
                ):
                    before = dict(os.environ)
                    if expected_error:
                        with self.assertRaises(expected_error):
                            identity.probe_version(agent, "/owned/kimi", "native")
                    else:
                        identity.probe_version(agent, "/owned/kimi", "native")
                    self.assertEqual(dict(os.environ), before)
                self.assertEqual(len(homes), 1)
                self.assertFalse(homes[0].exists())
                seen.extend(homes)
        self.assertEqual(len(set(seen)), len(outcomes))

    def test_qwen_version_probe_owns_and_cleans_its_configuration(self):
        agent = AGENTS["qwen"]
        success = subprocess.CompletedProcess(
            [], 0, f"{agent['version']}\n".encode(), b""
        )
        outcomes = [
            (success, None),
            (subprocess.TimeoutExpired([], 30), subprocess.TimeoutExpired),
            (subprocess.CalledProcessError(1, []), subprocess.CalledProcessError),
            (subprocess.CompletedProcess([], 0, b"x" * 65537, b""), RuntimeError),
            (subprocess.CompletedProcess([], 0, b"0.0.0\n", b""), RuntimeError),
            (subprocess.CompletedProcess([], 0, b"\xff", b""), UnicodeDecodeError),
            (
                subprocess.CompletedProcess([], 0, success.stdout, b"warning"),
                RuntimeError,
            ),
        ]
        seen_homes = []
        for outcome, expected_error in outcomes:
            with self.subTest(outcome=type(outcome).__name__, error=expected_error):
                homes = []

                def observe(command, **options):
                    self.assertEqual(command, ["/owned/qwen", "--version"])
                    environment = options["env"]
                    self.assertFalse(
                        identity.QWEN_BOOTSTRAP_OVERRIDES & environment.keys()
                    )
                    config = Path(environment["QWEN_HOME"])
                    self.assertEqual(config.name, ".qwen")
                    self.assertTrue(config.parent.is_dir())
                    self.assertNotEqual(environment["QWEN_HOME"], "/unrelated/.qwen")
                    self.assertFalse(config.exists())
                    homes.append(config.parent)
                    if isinstance(outcome, Exception):
                        raise outcome
                    return outcome

                with (
                    mock.patch.dict(
                        os.environ,
                        {
                            **dict.fromkeys(
                                identity.QWEN_BOOTSTRAP_OVERRIDES, "unrelated"
                            ),
                            "QWEN_HOME": "/unrelated/.qwen",
                        },
                    ),
                    mock.patch.object(
                        identity, "entrypoint_digest", return_value="a" * 64
                    ),
                    mock.patch.object(identity.subprocess, "run", side_effect=observe),
                ):
                    if expected_error:
                        with self.assertRaises(expected_error):
                            identity.probe_version(agent, "/owned/qwen", "native")
                    else:
                        identity.probe_version(agent, "/owned/qwen", "native")
                self.assertEqual(len(homes), 1)
                self.assertFalse(homes[0].exists())
                seen_homes.extend(homes)
        self.assertEqual(len(set(seen_homes)), len(outcomes))

    def test_other_agent_version_probe_keeps_default_environment(self):
        agent = AGENTS["gemini"]
        with (
            mock.patch.object(identity, "entrypoint_digest", return_value="a" * 64),
            mock.patch.object(
                identity.subprocess,
                "run",
                return_value=subprocess.CompletedProcess(
                    [], 0, f"{agent['version']}\n".encode(), b""
                ),
            ) as run,
        ):
            identity.probe_version(agent, "/owned/gemini", "native")
        self.assertNotIn("env", run.call_args.kwargs)

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
                "opencode": f"opencode v{pin}",
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

    def test_opencode_version_families_keep_distinct_complete_banners(self):
        legacy = {**AGENTS["opencode"], "version": "1.18.4"}
        current = AGENTS["opencode"]
        self.assertTrue(identity.version_matches(legacy, "1.18.4\n"))
        self.assertFalse(identity.version_matches(legacy, "opencode v1.18.4\n"))
        for output in [
            current["version"],
            f"opencode {current['version']}",
            f"opencode v{current['version']}0",
            "opencode v2.0.26",
            f"opencode v{current['version']}\nactual 1.18.4",
        ]:
            with self.subTest(output=output):
                self.assertFalse(identity.version_matches(current, output))
        self.assertFalse(identity.stderr_matches(current, "warning\n"))

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
                return_value=subprocess.CompletedProcess(
                    [], 0, (AGENTS["cursor"]["version"] + "\n").encode(), b""
                ),
            ) as run,
        ):
            with self.assertRaisesRegex(RuntimeError, "absolute native entrypoint"):
                identity.probe_version(AGENTS["cursor"], path, "wsl")
            run.assert_not_called()


class CursorStartupTests(unittest.TestCase):
    def test_missing_or_linked_delegates_refuse_before_both_launch_checks(self):
        names = ("cursor-agent.cmd", "cursor-agent.ps1", "node.exe", "index.js")
        for missing in names:
            with (
                self.subTest(missing=missing),
                tempfile.TemporaryDirectory() as directory,
            ):
                root = Path(directory)
                for name in names:
                    (root / name).write_bytes(b"owned file")
                (root / missing).unlink()
                before = {p.name: p.read_bytes() for p in root.iterdir()}
                with mock.patch.object(identity.subprocess, "run") as run:
                    with self.assertRaises(RuntimeError):
                        identity.probe_version(
                            AGENTS["cursor"], str(root / names[0]), "native"
                        )
                    with self.assertRaises(RuntimeError):
                        identity.verify_live_entrypoint(
                            {
                                "agent": "cursor",
                                "runtime": "native",
                                "executable": str(root / names[0]),
                                "executable_sha256": "a" * 64,
                            }
                        )
                    run.assert_not_called()
                self.assertEqual(
                    before, {p.name: p.read_bytes() for p in root.iterdir()}
                )
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name in names:
                (root / name).write_bytes(b"owned file")
            (root / "index.js").unlink()
            try:
                (root / "index.js").symlink_to(root / "cursor-agent.ps1")
            except OSError:
                return  # Native Windows symlinks may require privileges.
            with self.assertRaisesRegex(RuntimeError, "regular same-directory"):
                identity.require_cursor_ready(str(root / names[0]), "native")

    def test_metadata_uses_owned_context_and_cleans_it_on_success_and_error(self):
        for failure in (False, True):
            with (
                self.subTest(failure=failure),
                tempfile.TemporaryDirectory() as directory,
            ):
                root = Path(directory)
                for name in ("cursor-agent", "node", "index.js"):
                    (root / name).write_bytes(b"selected file")
                original = {
                    "CURSOR_AUTH_TOKEN": "ambient-fake-token",
                    "CURSOR_API_KEY": "ambient-fake-key",
                    "NODE_OPTIONS": "--require /ambient/file",
                    "CURSOR_AGENT_CLI_LOCAL_MODE": "1",
                    "CURSOR_CONFIG_DIR": "/ambient/config",
                    "CURSOR_DATA_DIR": "/ambient/data",
                    "NODE_COMPILE_CACHE": "/ambient/cache",
                    "KEEP": "unchanged",
                }
                homes = []

                def observe(command, **options):
                    self.assertEqual(
                        command,
                        [
                            str(root / "cursor-agent"),
                            "--disable-auto-update",
                            "--version",
                        ],
                    )
                    child = options["env"]
                    self.assertFalse(
                        {
                            "CURSOR_AUTH_TOKEN",
                            "CURSOR_API_KEY",
                            "NODE_OPTIONS",
                            "CURSOR_AGENT_CLI_LOCAL_MODE",
                        }
                        & child.keys()
                    )
                    home = Path(child["HOME"])
                    homes.append(home)
                    self.assertEqual(child["USERPROFILE"], str(home))
                    self.assertEqual(child["CURSOR_CONFIG_DIR"], str(home / ".cursor"))
                    self.assertEqual(child["CURSOR_DATA_DIR"], str(home / "d"))
                    self.assertEqual(
                        child["NODE_COMPILE_CACHE"], str(home / ".cache/cursor-node")
                    )
                    self.assertEqual(child["AGENT_CLI_CREDENTIAL_STORE"], "memory")
                    self.assertEqual(child["DIRENV_DISABLE"], "1")
                    self.assertEqual(child["KEEP"], "unchanged")
                    (home / "marker").write_text("owned cache")
                    if failure:
                        raise subprocess.TimeoutExpired(
                            command, identity.VERSION_TIMEOUT
                        )
                    return subprocess.CompletedProcess(
                        command, 0, (AGENTS["cursor"]["version"] + "\n").encode(), b""
                    )

                with (
                    mock.patch.dict(os.environ, original, clear=True),
                    mock.patch.object(identity.subprocess, "run", side_effect=observe),
                ):
                    if failure:
                        with self.assertRaises(subprocess.TimeoutExpired):
                            identity.probe_version(
                                AGENTS["cursor"], str(root / "cursor-agent"), "native"
                            )
                    else:
                        identity.probe_version(
                            AGENTS["cursor"], str(root / "cursor-agent"), "native"
                        )
                    self.assertEqual(dict(os.environ), original)
                self.assertTrue(homes)
                self.assertTrue(all(not home.exists() for home in homes))

    def test_fixture_filter_preserves_the_authored_token_and_other_agent_inputs(self):
        original = {
            "CURSOR_API_KEY": "ambient-fake-key",
            "NODE_OPTIONS": "--require /ambient",
            "CURSOR_AGENT_CLI_LOCAL_MODE": "1",
            "CURSOR_AUTH_TOKEN": "pitot-local-only",
            "KEEP": "unchanged",
        }
        before = dict(original)
        self.assertEqual(
            identity.agent_environment("cursor", original),
            {"CURSOR_AUTH_TOKEN": "pitot-local-only", "KEEP": "unchanged"},
        )
        self.assertEqual(original, before)
        for agent in AGENTS.keys() - {"cursor", "kimi", "qwen", "deepseek"}:
            self.assertIs(identity.agent_environment(agent, original), original)


class KimiUpdateStateTests(unittest.TestCase):
    def test_updater_artifacts_refuse_both_launch_checks_without_mutation(self):
        for name in (".staging", "kimi.bak", "kimi.123.bak"):
            for directory in (False, True):
                with (
                    self.subTest(name=name, directory=directory),
                    tempfile.TemporaryDirectory() as temporary,
                ):
                    executable = Path(temporary) / "kimi"
                    executable.write_bytes(b"selected entrypoint")
                    artifact = executable.parent / name
                    if directory:
                        artifact.mkdir()
                        (artifact / "sentinel").write_bytes(b"keep")
                    else:
                        artifact.write_bytes(b"keep")
                    before = sorted(
                        (p.name, p.is_dir()) for p in executable.parent.iterdir()
                    )
                    with mock.patch.object(identity.subprocess, "run") as run:
                        with self.assertRaisesRegex(
                            RuntimeError, "adjacent native update"
                        ):
                            identity.probe_version(
                                AGENTS["kimi"], str(executable), "native"
                            )
                        with self.assertRaisesRegex(
                            RuntimeError, "adjacent native update"
                        ):
                            identity.verify_live_entrypoint(
                                {
                                    "agent": "kimi",
                                    "executable": str(executable),
                                    "runtime": "native",
                                    "executable_sha256": "a" * 64,
                                }
                            )
                        run.assert_not_called()
                    self.assertEqual(
                        before,
                        sorted(
                            (p.name, p.is_dir()) for p in executable.parent.iterdir()
                        ),
                    )
                    self.assertEqual(executable.read_bytes(), b"selected entrypoint")
                    self.assertEqual(
                        (artifact / "sentinel" if directory else artifact).read_bytes(),
                        b"keep",
                    )

    def test_user_backups_and_non_ascii_pid_names_remain_allowed(self):
        with tempfile.TemporaryDirectory() as temporary:
            executable = Path(temporary) / "kimi"
            executable.write_bytes(b"selected")
            for name in (
                "kimi.config.bak",
                "kimi.12x.bak",
                "kimi.\u0661.bak",
                "another.bak",
            ):
                (executable.parent / name).write_bytes(b"keep")
            identity.require_kimi_ready(str(executable), "native")
            self.assertTrue(
                all(
                    p.read_bytes() == b"keep"
                    for p in executable.parent.iterdir()
                    if p != executable
                )
            )

    def test_resolved_target_and_dangling_updater_links_are_checked(self):
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            target_dir = base / "target"
            target_dir.mkdir()
            target = target_dir / "kimi"
            target.write_bytes(b"selected")
            alias = base / "alias"
            try:
                alias.symlink_to(target)
            except OSError as error:
                self.skipTest(f"symlinks unavailable: {error}")
            identity.require_kimi_ready(str(alias), "native")
            for name in (".staging", "kimi.bak", "kimi.123.bak"):
                with self.subTest(name=name):
                    artifact = target_dir / name
                    artifact.symlink_to(target_dir / "missing")
                    with self.assertRaisesRegex(RuntimeError, "adjacent native update"):
                        identity.require_kimi_ready(str(alias), "native")
                    self.assertTrue(artifact.is_symlink())
                    artifact.unlink()
            self.assertEqual(target.read_bytes(), b"selected")

    def test_inspection_errors_and_unsupported_runtime_refuse_before_spawn(self):
        with tempfile.TemporaryDirectory() as temporary:
            executable = Path(temporary) / "kimi"
            executable.write_bytes(b"selected")
            for operation in ("resolve", "iterdir"):
                with (
                    self.subTest(operation=operation),
                    mock.patch.object(
                        Path, operation, side_effect=PermissionError("blocked")
                    ),
                    mock.patch.object(identity.subprocess, "run") as run,
                    self.assertRaisesRegex(RuntimeError, "cannot inspect"),
                ):
                    identity.probe_version(AGENTS["kimi"], str(executable), "native")
                run.assert_not_called()
            with (
                mock.patch.object(identity, "host_controls_wsl", return_value=True),
                mock.patch.object(identity.subprocess, "run") as run,
                self.assertRaisesRegex(RuntimeError, "native entrypoint"),
            ):
                identity.probe_version(AGENTS["kimi"], str(executable), "wsl")
            run.assert_not_called()


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
        version = AGENTS["kimi"]["version"]
        with tempfile.TemporaryDirectory(prefix="agent path space ") as directory:
            path = Path(directory) / "kimi"
            path.write_text(f"#!/bin/sh\nprintf '{version}\\n'\n")
            path.chmod(0o755)
            output, digest = identity.probe_version(AGENTS["kimi"], str(path), "native")
            self.assertEqual(output, version + "\n")
            with self.assertRaisesRegex(RuntimeError, "native entrypoint"):
                identity.probe_version(AGENTS["kimi"], str(path), "wsl")
            receipt = self.receipt()
            receipt.update(executable=str(path), executable_sha256=digest)
            with mock.patch.object(
                identity, "owned_entrypoint", return_value=str(path)
            ):
                self.assertEqual(
                    identity.inspect_receipt(receipt, AGENTS["kimi"], "macos"), receipt
                )
                path.write_text(f"#!/bin/sh\nprintf '{version}1\\n'\n")
                with self.assertRaisesRegex(RuntimeError, "changed since"):
                    identity.inspect_receipt(receipt, AGENTS["kimi"], "macos")
            with self.assertRaisesRegex(RuntimeError, "exact supervised version"):
                identity.probe_version(AGENTS["kimi"], str(path), "native")

    @mock.patch.object(identity, "require_kimi_ready")
    def test_probe_failure_timeout_encoding_and_changed_bytes(self, ready):
        agent = AGENTS["kimi"]
        for result in [
            subprocess.CompletedProcess(
                [], 0, (agent["version"] + "\n").encode(), b"warning"
            ),
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
                return_value=subprocess.CompletedProcess(
                    [], 0, (agent["version"] + "\n").encode(), b""
                ),
            ),
            self.assertRaisesRegex(RuntimeError, "during version probe"),
        ):
            identity.probe_version(agent, "/owned", "native")


if __name__ == "__main__":
    unittest.main()
