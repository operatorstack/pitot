from __future__ import annotations

import importlib.util
import argparse
import copy
import base64
import hashlib
import http.client
import json
import os
from pathlib import Path
import subprocess
import tempfile
import threading
import time
import tomllib
import unittest
from unittest import mock


ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location(
    "model_control_proxy", ROOT / "tests/model_control_proxy.py"
)
proxy = importlib.util.module_from_spec(spec)
assert spec and spec.loader
spec.loader.exec_module(proxy)
driver_spec = importlib.util.spec_from_file_location(
    "real_agent_driver", ROOT / "tests/real_agent_driver.py"
)
driver = importlib.util.module_from_spec(driver_spec)
assert driver_spec and driver_spec.loader
driver_spec.loader.exec_module(driver)
devin_spec = importlib.util.spec_from_file_location(
    "devin_control_proxy", ROOT / "tests/devin_control_proxy.py"
)
devin_proxy = importlib.util.module_from_spec(devin_spec)
assert devin_spec and devin_spec.loader
devin_spec.loader.exec_module(devin_proxy)


class ModelControlProtocolTests(unittest.TestCase):
    def test_deepseek_native_results_read_all_compressed_frames(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            session = (
                root / "home/.dsh/sessions/project/session-id/session.v4.jsonl.zstd"
            )
            session.parent.mkdir(parents=True)
            calls = ["pitot_tool_allow", "pitot_tool_deny"]
            rows = [{"type": "session", "id": "session-id"}]
            for index, call in enumerate(calls):
                rows.append(
                    {
                        "type": "tool/call",
                        "seq": index * 2,
                        "data": {"callId": call, "name": "bash"},
                    }
                )
                rows.append(
                    {
                        "type": "tool/result",
                        "sourceEventSeqs": [index * 2],
                        "data": {
                            "message": {
                                "toolCallId": call,
                                "source": {"callId": call},
                                "isError": index == 1,
                            },
                            "error": {"code": "PITOT_DENIED"} if index else {},
                        },
                    }
                )
            compressed = subprocess.run(
                [
                    "node",
                    "-e",
                    "const z=require('node:zlib');let s='';process.stdin.on('data',b=>s+=b).on('end',()=>{for(const row of JSON.parse(s))process.stdout.write(z.zstdCompressSync(Buffer.from(JSON.stringify(row)+'\\n')))})",
                ],
                input=json.dumps(rows).encode(),
                capture_output=True,
                check=True,
            ).stdout
            session.write_bytes(compressed)
            witnesses = [
                {"tool_call_id": call, "session_id": "session-id"} for call in calls
            ]
            result = driver.validate_deepseek_native_results(root, witnesses)
            self.assertEqual(result["denial_code"], "PITOT_DENIED")
            session.write_bytes(compressed + b"corrupt trailing bytes")
            with self.assertRaises(subprocess.CalledProcessError):
                driver.validate_deepseek_native_results(root, witnesses)

    def test_cursor_failure_diagnostics_preserve_phase_and_transport_failures(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            nonce = "a" * 32
            receipt = root / "proxy.json"
            receipt.write_text(
                json.dumps(
                    {
                        "initial_prompt_observed": True,
                        "allow_tool_call_response_emitted": True,
                        "allow_tool_result_observed": False,
                        "transport_errors": [{"scope": "run_stream", "code": "RESET"}],
                        "unrelated_content": "must not be emitted",
                        "denied_result_observed": "unrelated wrong type",
                        "response_attempts": {"allow": "unrelated wrong type"},
                        "cursor_inbound": {
                            "request_bytes": 100,
                            "envelopes": [
                                {
                                    "flags": 0,
                                    "length": 95,
                                    "printable": ["unrelated secret"],
                                }
                            ],
                        },
                    }
                ),
                encoding="utf-8",
            )
            (root / f"{nonce}-allow.draft.json").write_text("{}", encoding="utf-8")
            (root / f"{nonce}-allow-id.error.txt").write_text(
                "Carrier failure café", encoding="utf-8"
            )
            (root / "unrelated.error.txt").write_text("unrelated", encoding="utf-8")
            text = driver.cursor_failure_diagnostics(receipt, root, nonce)
            state = json.loads(text)
            self.assertNotIn("unrelated", text)
            self.assertTrue(state["carrier"]["allow"]["draft_present"])
            self.assertFalse(state["carrier"]["allow"]["final_present"])
            self.assertEqual(
                state["carrier"]["allow"]["errors"], ["Carrier failure café"]
            )
            self.assertFalse(state["allow_tool_result_observed"])
            self.assertEqual(state["response_attempts"], {})

    def test_cursor_failure_diagnostics_bound_bad_and_large_files(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            nonce = "a" * 32
            receipt = root / "proxy.json"
            receipt.write_bytes(b"x" * 1048577)
            (root / f"{nonce}-deny-id.error.txt").write_bytes(b"x" * 1025)
            state = json.loads(driver.cursor_failure_diagnostics(receipt, root, nonce))
            self.assertEqual(state["receipt"], "missing, malformed or oversized")
            self.assertEqual(state["carrier"]["deny"]["errors"], ["<oversized file>"])
            state = json.loads(driver.cursor_failure_diagnostics(root, root, nonce))
            self.assertEqual(state["receipt"], "missing, malformed or oversized")

    def test_cursor_native_diagnostics_keep_identity_and_omit_content(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            receipt = root / "proxy.json"
            receipt.write_text(
                json.dumps(
                    {
                        "native_shell_results": [
                            {
                                "source_sha": "a" * 40,
                                "connection_id": 7,
                                "stream_id": 11,
                                "attempt_id": 3,
                                "event_sequence": 4,
                                "native_id": 1,
                                "variant": "spawn_error",
                                "command_sha256": "b" * 64,
                                "issued_exact_command_match": True,
                                "nonce_bound": True,
                                "reason_sha256": "c" * 64,
                                "reason_bytes": 100,
                                "error_class": "missing_parsing_result",
                                "reason": "private text",
                                "frame_sha256": "not a digest private text",
                            }
                        ],
                        "transport_errors": [
                            {"scope": "run_stream", "code": "RESET", "connection_id": 7}
                        ],
                    }
                ),
                encoding="utf-8",
            )
            text = driver.cursor_failure_diagnostics(receipt, root, "d" * 32)
            self.assertNotIn("private text", text)
            state = json.loads(text)
            result = state["native_shell_results"][0]
            self.assertEqual(
                result["connection_id"], state["transport_errors"][0]["connection_id"]
            )
            self.assertEqual(result["variant"], "spawn_error")
            self.assertEqual(result["command_sha256"], "b" * 64)
            self.assertTrue(result["issued_exact_command_match"])

    @unittest.skipUnless(hasattr(os, "mkfifo"), "host has no filesystem FIFO")
    def test_cursor_failure_diagnostics_refuse_fifo_before_open(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            receipt = root / "proxy.json"
            os.mkfifo(receipt)
            state = json.loads(
                driver.cursor_failure_diagnostics(receipt, root, "a" * 32)
            )
            self.assertEqual(state["receipt"], "missing, malformed or oversized")

    def test_cursor_carrier_rejects_unrelated_or_corrupt_snapshots(self):
        nonce = "a" * 32
        context = {
            "PITOT_CURSOR_CANDIDATE": "b" * 40,
            "PITOT_CURSOR_CARRIER_TEMP": "C:/owned/ct",
            "PITOT_CURSOR_PARENT_SHELL": "C:/PowerShell/pwsh.exe",
            "PITOT_CURSOR_PARENT_SHELL_SHA256": "c" * 64,
            "PITOT_CURSOR_RELEASE_NODE": "C:/release/node.exe",
            "PITOT_CURSOR_RELEASE_NODE_SHA256": "d" * 64,
            "PITOT_CURSOR_VERIFICATION_HOOK_COMMAND": "& 'C:/PowerShell/pwsh.exe' -File 'C:/owned/bridge.ps1' -VerifyCarrier",
        }
        commands = [
            f"& 'C:/工具 path/canary.exe' PITOT_{phase} {nonce}"
            for phase in ("ALLOW", "DENY")
        ]
        records = []
        for index, phase in enumerate(("allow", "deny")):
            payload_path = f"C:/owned/ct/cursor-hooks-abc{index}/payload.json"
            payload = json.dumps(
                {"command": commands[index]}, ensure_ascii=False
            ).encode("utf-8")
            script = (
                "\ufeff$OutputEncoding = [System.Text.Encoding]::UTF8; Get-Content -LiteralPath '"
                + payload_path
                + "' -Raw | & { $input | "
                + context["PITOT_CURSOR_VERIFICATION_HOOK_COMMAND"]
                + " }"
            ).encode("utf-8")
            records.append(
                {
                    "schema_version": 1,
                    "status": "observed",
                    "nonce": nonce,
                    "source_candidate_sha": context["PITOT_CURSOR_CANDIDATE"],
                    "phase": phase,
                    "action_id": f"act_{index}",
                    "observed_command": commands[index],
                    "expected_command": commands[index],
                    "owned_temp_root": context["PITOT_CURSOR_CARRIER_TEMP"],
                    "elapsed_ms": 100,
                    "input_prefix_bom_removed": True,
                    "inner": {
                        "pid": 10 + index,
                        "parent_pid": 20 + index,
                        "image_path": context["PITOT_CURSOR_PARENT_SHELL"],
                        "image_sha256": "c" * 64,
                        "creation_date": "2026-10-12T12:00:00Z",
                    },
                    "parent": {
                        "pid": 20 + index,
                        "parent_pid": 30,
                        "image_path": context["PITOT_CURSOR_PARENT_SHELL"],
                        "image_sha256": "c" * 64,
                        "creation_date": "2026-10-12T11:59:59Z",
                        "file_major_part": 7,
                        "argv": 'C:/PowerShell/pwsh.exe -ExecutionPolicy Bypass -NonInteractive -File "C:/owned/ct/ps-script-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee.ps1"',
                    },
                    "grandparent": {
                        "pid": 30,
                        "creation_date": "2026-10-12T11:59:58Z",
                        "image_path": context["PITOT_CURSOR_RELEASE_NODE"],
                        "image_sha256": "d" * 64,
                        "version_prefix": "C:/release",
                    },
                    "carrier": {
                        "path": "C:/owned/ct/ps-script-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee.ps1",
                        "bytes_base64": base64.b64encode(script).decode(),
                        "sha256": hashlib.sha256(script).hexdigest(),
                    },
                    "payload": {
                        "path": payload_path,
                        "bytes_base64": base64.b64encode(payload).decode(),
                        "sha256": hashlib.sha256(payload).hexdigest(),
                    },
                }
            )
        driver.validate_cursor_carrier(
            records, context, nonce, commands, ["act_0", "act_1"]
        )
        mutations = [
            lambda r: r[0].pop("input_prefix_bom_removed"),
            lambda r: r[0].update(input_prefix_bom_removed=False),
            lambda r: r[0].update(input_prefix_bom_removed=1),
            lambda r: r[0].update(input_prefix_bom_removed="true"),
            lambda r: r[0].update(status="draft"),
            lambda r: r[0].update(source_candidate_sha="f" * 40),
            lambda r: r[0].update(nonce="0" * 32),
            lambda r: r[0].update(action_id="act_other"),
            lambda r: r[0].update(elapsed_ms=5001),
            lambda r: r[0]["parent"].update(file_major_part=5),
            lambda r: r[0]["parent"].update(image_sha256="0" * 64),
            lambda r: r[0]["inner"].update(parent_pid=999),
            lambda r: r[0]["grandparent"].update(image_path="C:/other/node.exe"),
            lambda r: r[0]["carrier"].update(
                path="C:/foreign/ps-script-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee.ps1"
            ),
            lambda r: r[0]["payload"].update(
                path="C:/owned/ct/../escaped/payload.json"
            ),
            lambda r: r[0]["payload"].update(sha256="0" * 64),
            lambda r: r[0]["carrier"].update(bytes_base64="not base64"),
            lambda r: r[0]["parent"].update(argv="pwsh -File unrelated.ps1"),
            lambda r: r[0]["parent"].update(argv="echo " + r[0]["carrier"]["path"]),
            lambda r: r[0]["parent"].update(creation_date="not a date"),
            lambda r: r[0]["parent"].update(creation_date="2026-10-12T12:00:01Z"),
            lambda r: r[0]["grandparent"].pop("creation_date"),
            lambda r: r.reverse(),
            lambda r: r.pop(),
        ]
        for mutation in mutations:
            broken = copy.deepcopy(records)
            mutation(broken)
            with self.assertRaises(RuntimeError):
                driver.validate_cursor_carrier(
                    broken, context, nonce, commands, ["act_0", "act_1"]
                )

    def test_cursor_windows_requires_modern_powershell_without_legacy_fallback(self):
        with (
            mock.patch.object(driver.shutil, "which", return_value=None),
            mock.patch.object(driver.subprocess, "run") as run,
        ):
            with self.assertRaisesRegex(RuntimeError, "PowerShell 7"):
                driver.cursor_powershell()
            run.assert_not_called()
        for output, error, accepted in (
            (b"7\r\n", b"", True),
            (b"5\r\n", b"", False),
            (b"7\n", b"warning", False),
            (b"7 extra\n", b"", False),
            (b"", b"", False),
        ):
            with (
                self.subTest(output=output, error=error),
                mock.patch.object(driver.shutil, "which", return_value="/owned/pwsh"),
                mock.patch.object(
                    driver.subprocess,
                    "run",
                    return_value=subprocess.CompletedProcess([], 0, output, error),
                ) as run,
            ):
                if accepted:
                    self.assertEqual(driver.cursor_powershell(), "/owned/pwsh")
                else:
                    with self.assertRaisesRegex(RuntimeError, "PowerShell 7"):
                        driver.cursor_powershell()
                self.assertEqual(run.call_args.kwargs["timeout"], 30)
                self.assertIn("-NoProfile", run.call_args.args[0])
                self.assertIn("-NonInteractive", run.call_args.args[0])

    def test_cursor_fixture_preserves_unix_bridge_and_uses_explicit_windows_shell(self):
        for windows in (False, True):
            with (
                self.subTest(windows=windows),
                tempfile.TemporaryDirectory() as directory,
            ):
                home = Path(directory) / "owner's é home"
                project = Path(directory) / "project with spaces"
                with (
                    mock.patch.object(driver, "windows_host", return_value=windows),
                    mock.patch.object(
                        driver,
                        "cursor_powershell",
                        return_value="/owned PowerShell/pwsh",
                    ) as shell,
                ):
                    flags, env = driver.configure(
                        "cursor",
                        home,
                        project,
                        "witness with spaces",
                        "http://127.0.0.1:9123",
                        pitot_command="pitot with spaces",
                        witness_receipt="receipt's file",
                        nonce="nonce",
                        runtime_path="runtime's file",
                    )
                hooks = json.loads((project / ".cursor/hooks.json").read_text())
                hook = hooks["hooks"]["beforeShellExecution"][0]
                self.assertTrue(hook["failClosed"])
                if windows:
                    shell.assert_called_once()
                    self.assertIn("& '/owned PowerShell/pwsh'", hook["command"])
                    self.assertIn("beforeShellExecution.ps1", hook["command"])
                    self.assertIn("-Receipt 'receipt''s file'", hook["command"])
                    self.assertIn("-Runtime 'runtime''s file'", hook["command"])
                    self.assertEqual(env["SHELL"], "/owned PowerShell/pwsh")
                else:
                    shell.assert_not_called()
                    self.assertEqual(
                        (project / ".cursor/hooks/beforeShellExecution").read_bytes(),
                        (
                            ROOT / "integrations/cursor/beforeShellExecution"
                        ).read_bytes(),
                    )
                self.assertEqual(env["CURSOR_CONFIG_DIR"], str(home / ".cursor"))
                self.assertEqual(env["CURSOR_DATA_DIR"], str(home / "d"))
                self.assertEqual(
                    env["NODE_COMPILE_CACHE"], str(home / ".cache/cursor-node")
                )
                self.assertEqual(env["CURSOR_AUTH_TOKEN"], "pitot-local-only")
                self.assertEqual(env["AGENT_CLI_CREDENTIAL_STORE"], "memory")
                self.assertIn("--disable-auto-update", flags)

    def test_kimi_uses_owned_provider_and_native_cache_despite_ambient_settings(self):
        for native_windows in (False, True):
            with (
                self.subTest(native_windows=native_windows),
                tempfile.TemporaryDirectory() as temporary,
                mock.patch.object(driver, "windows_host", return_value=native_windows),
            ):
                home = Path(temporary) / "owned home"
                flags, extra_env = driver.configure(
                    "kimi",
                    home,
                    Path(temporary) / "project",
                    "witness",
                    "http://127.0.0.1:9123",
                    pitot_command="pitot",
                    witness_receipt="witness.jsonl",
                    nonce="nonce",
                    runtime_path="runtime.json",
                )
                environment = driver.agent_environment(
                    "kimi",
                    {
                        "KIMI_CODE_HOME": "/ambient/.kimi-code",
                        "KIMI_CODE_CACHE_DIR": "/ambient/cache",
                        "LOCALAPPDATA": "/ambient/local-app-data",
                        "KIMI_MODEL_NAME": "ambient-model",
                        "KIMI_MODEL_BASE_URL": "http://127.0.0.1:1/v1",
                        "KIMI_MODEL_TEMPERATURE": "1.5",
                        **extra_env,
                    },
                )
                self.assertEqual(
                    environment["KIMI_CODE_HOME"], str(home / ".kimi-code")
                )
                self.assertEqual(
                    environment["KIMI_CODE_CACHE_DIR"],
                    str(home / ".cache" / "kimi-code"),
                )
                self.assertEqual(environment["KIMI_CODE_NO_AUTO_UPDATE"], "1")
                self.assertEqual(environment["LOCALAPPDATA"], "/ambient/local-app-data")
                self.assertNotIn("KIMI_MODEL_NAME", environment)
                self.assertNotIn("KIMI_MODEL_BASE_URL", environment)
                self.assertNotIn("KIMI_MODEL_TEMPERATURE", environment)
                config = tomllib.loads((home / ".kimi-code/config.toml").read_text())
                self.assertEqual(config["default_model"], "pitot-control")
                self.assertEqual(
                    config["providers"]["pitot"],
                    {
                        "type": "openai",
                        "base_url": "http://127.0.0.1:9123/v1",
                        "api_key": "pitot-local-only",
                    },
                )
                self.assertEqual(config["models"]["pitot-control"]["provider"], "pitot")
                self.assertEqual(
                    config["models"]["pitot-control"]["model"], "pitot-control"
                )
                self.assertEqual(
                    config["models"]["pitot-control"]["capabilities"], ["tool_use"]
                )
                self.assertEqual(config["hooks"][0]["event"], "PreToolUse")
                self.assertIn("hook kimi --runtime", config["hooks"][0]["command"])
                self.assertEqual(flags, [])

    def test_qwen_uses_its_owned_settings_and_clean_child_environment(self):
        for native_windows in (False, True):
            with (
                self.subTest(native_windows=native_windows),
                tempfile.TemporaryDirectory() as temporary,
                mock.patch.object(driver, "windows_host", return_value=native_windows),
            ):
                home = Path(temporary) / "owned home"
                flags, extra_env = driver.configure(
                    "qwen",
                    home,
                    Path(temporary) / "project",
                    "witness",
                    "http://127.0.0.1:9123",
                    pitot_command="pitot",
                    witness_receipt="witness.jsonl",
                    nonce="nonce",
                    runtime_path="runtime.json",
                )
                environment = driver.agent_environment(
                    "qwen",
                    {
                        "QWEN_HOME": "/unrelated/.qwen",
                        "CLI_VERSION": "9.9.9",
                        "QWEN_CODE_RELAUNCH_ARGS": '["serve"]',
                        **extra_env,
                    },
                )
                self.assertEqual(environment["QWEN_HOME"], str(home / ".qwen"))
                self.assertNotIn("CLI_VERSION", environment)
                self.assertNotIn("QWEN_CODE_RELAUNCH_ARGS", environment)
                settings = json.loads(
                    (Path(environment["QWEN_HOME"]) / "settings.json").read_text()
                )
                model = settings["modelProviders"]["openai"]["models"][0]
                self.assertEqual(model["baseUrl"], "http://127.0.0.1:9123/v1")
                self.assertEqual(model["envKey"], "OPENAI_API_KEY")
                self.assertEqual(environment["OPENAI_API_KEY"], "pitot-local-only")
                self.assertEqual(settings["security"]["auth"]["selectedType"], "openai")
                self.assertEqual(flags, ["--model", "pitot-control", "-y", "-p"])
                hook = settings["hooks"]["PreToolUse"][0]["hooks"][0]["command"]
                self.assertIn(
                    "PreToolUse.cjs" if native_windows else "PreToolUse", hook
                )

    def test_pi_uses_owned_offline_configuration_despite_ambient_settings(self):
        ambient = {
            "PI_CODING_AGENT_DIR": "/unrelated/pi/agent",
            "PI_OFFLINE": "0",
            "PI_TELEMETRY": "1",
        }
        for native_windows in (False, True):
            with (
                self.subTest(native_windows=native_windows),
                tempfile.TemporaryDirectory() as temporary,
                mock.patch.dict(os.environ, ambient),
                mock.patch.object(driver, "windows_host", return_value=native_windows),
            ):
                home = Path(temporary) / "owned home"
                flags, extra_env = driver.configure(
                    "pi",
                    home,
                    Path(temporary) / "project",
                    "witness",
                    "http://127.0.0.1:9123",
                    pitot_command="pitot",
                    witness_receipt="witness.jsonl",
                    nonce="nonce",
                    runtime_path="runtime.json",
                )
                environment = {**os.environ, **extra_env}
                self.assertEqual(
                    environment["PI_CODING_AGENT_DIR"], str(home / ".pi/agent")
                )
                self.assertEqual(environment["PI_OFFLINE"], "1")
                self.assertEqual(environment["PI_TELEMETRY"], "0")
                config_path = Path(environment["PI_CODING_AGENT_DIR"]) / "models.json"
                provider = json.loads(config_path.read_text())["providers"]["pitot"]
                self.assertEqual(provider["api"], "openai-completions")
                self.assertEqual(provider["apiKey"], "pitot-local-only")
                self.assertEqual(provider["baseUrl"], "http://127.0.0.1:9123/v1")
                self.assertEqual(provider["models"][0]["id"], "pitot-control")
                self.assertEqual(
                    flags,
                    [
                        "--no-session",
                        "--print",
                        "--provider",
                        "pitot",
                        "--model",
                        "pitot-control",
                        "-e",
                        str(ROOT / "integrations/pi/pitot.ts"),
                    ],
                )

    def test_opencode_requires_issued_calls_and_native_typed_denial(self):
        allow = {
            "type": "function_call_output",
            "call_id": "pitot_tool_allow",
            "output": "PITOT_CANARY_RESULT PITOT_ALLOW nonce\n",
        }
        deny = {
            "type": "function_call_output",
            "call_id": "pitot_tool_deny",
            "output": json.dumps(
                {
                    "error": {
                        "type": "tool.execution",
                        "message": "PITOT_CONTROLLER_DENY nonce",
                    },
                    "content": [],
                }
            ),
        }
        body = {"input": [allow, deny]}
        self.assertEqual(
            proxy.opencode_tool_result("openai_responses", body, "allow", "nonce"),
            {"call_id": "pitot_tool_allow"},
        )
        self.assertEqual(
            proxy.opencode_tool_result("openai_responses", body, "deny", "nonce"),
            {"call_id": "pitot_tool_deny", "error_type": "tool.execution"},
        )
        for phase, result in [("allow", allow), ("deny", deny)]:
            wrong = {**result, "call_id": "another_call"}
            with self.subTest(phase=phase):
                self.assertIsNone(
                    proxy.opencode_tool_result(
                        "openai_responses", {"input": [wrong]}, phase, "nonce"
                    )
                )
                self.assertIsNone(
                    proxy.opencode_tool_result(
                        "openai_responses", body, phase, "another_nonce"
                    )
                )
        for output in [
            "PITOT_CONTROLLER_DENY nonce",
            json.dumps(
                {"error": {"type": "unknown", "message": "PITOT_CONTROLLER_DENY nonce"}}
            ),
            json.dumps(
                {
                    "error": {"type": "tool.execution", "message": "another_nonce"},
                    "content": ["PITOT_CONTROLLER_DENY nonce"],
                }
            ),
            "null",
            "[]",
            {},
        ]:
            with self.subTest(output=output):
                self.assertIsNone(
                    proxy.opencode_tool_result(
                        "openai_responses",
                        {"input": [{**deny, "output": output}]},
                        "deny",
                        "nonce",
                    )
                )

    def test_opencode_history_cannot_supply_a_missing_issued_result(self):
        allow = {
            "type": "function_call_output",
            "call_id": "pitot_tool_allow",
            "output": "PITOT_CANARY_RESULT PITOT_ALLOW nonce\n",
        }
        body = {"input": [allow, allow]}
        self.assertIsNotNone(
            proxy.opencode_tool_result("openai_responses", body, "allow", "nonce")
        )
        self.assertIsNone(
            proxy.opencode_tool_result("openai_responses", body, "deny", "nonce")
        )
        self.assertIsNone(
            proxy.opencode_tool_result(
                "openai_responses",
                {"input": [allow, {**allow, "output": "other nonce"}]},
                "allow",
                "nonce",
            )
        )
        self.assertIsNone(
            proxy.opencode_tool_result(
                "openai_responses", {"input": None}, "allow", "nonce"
            )
        )

    def test_opencode_uses_a_portable_directory_and_owned_native_provider(self):
        with tempfile.TemporaryDirectory(prefix="pitot opencode ") as temporary:
            base = Path(temporary)
            flags, environment = driver.configure(
                "opencode",
                base / "home",
                base / "project",
                "witness",
                "http://127.0.0.1:9123",
                pitot_command="pitot",
                witness_receipt="witness.jsonl",
                nonce="nonce",
                runtime_path="runtime.json",
            )
            config = json.loads(Path(environment["OPENCODE_CONFIG"]).read_text())
            self.assertEqual(
                config["plugins"], [(ROOT / "integrations/opencode").resolve().as_uri()]
            )
            self.assertEqual(
                config["providers"]["openai"]["package"],
                "@opencode/ai/providers/openai/responses",
            )
            self.assertEqual(
                config["providers"]["openai"]["settings"]["baseURL"],
                "http://127.0.0.1:9123/v1",
            )
            self.assertIn("--standalone", flags)
            self.assertEqual(flags[flags.index("--log-level") + 1], "debug")

    def test_cursor_keeps_synthetic_credentials_in_memory(self):
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            _, environment = driver.configure(
                "cursor",
                base / "home",
                base / "project",
                "witness",
                "http://127.0.0.1:1",
                pitot_command="pitot",
                witness_receipt="witness.jsonl",
                nonce="nonce",
                runtime_path="runtime.json",
            )
            self.assertEqual(environment["AGENT_CLI_CREDENTIAL_STORE"], "memory")
            self.assertEqual(environment["CURSOR_AUTH_TOKEN"], "pitot-local-only")
            self.assertFalse((base / "home/Library/Keychains").exists())

    def test_windows_codex_uses_blocking_json_bridge(self):
        with (
            tempfile.TemporaryDirectory() as temporary,
            mock.patch.object(driver, "windows_host", return_value=True),
        ):
            base = Path(temporary)
            driver.configure(
                "codex",
                base / "home",
                base / "project",
                "witness.exe",
                "http://127.0.0.1:1",
                pitot_command="pitot.exe",
                witness_receipt="witness.jsonl",
                nonce="nonce",
                runtime_path="runtime.json",
            )
            hooks = json.loads(
                (base / "home/.codex/hooks.json").read_text(encoding="utf-8")
            )
            command = hooks["hooks"]["PreToolUse"][0]["hooks"][0]["command"]
            self.assertIn(
                "integrations/codex/PreToolUse.ps1", command.replace("\\", "/")
            )
            self.assertTrue(command.startswith('& "'))
            self.assertNotIn("powershell -NoProfile", command)

    def test_windows_qwen_uses_node_bridge_without_shell_override(self):
        with (
            tempfile.TemporaryDirectory() as temporary,
            mock.patch.object(driver, "windows_host", return_value=True),
        ):
            base = Path(temporary)
            driver.configure(
                "qwen",
                base / "home",
                base / "project",
                "witness.exe",
                "http://127.0.0.1:1",
                pitot_command="pitot.exe",
                witness_receipt="witness.jsonl",
                nonce="nonce",
                runtime_path="runtime.json",
            )
            settings = json.loads(
                (base / "home/.qwen/settings.json").read_text(encoding="utf-8")
            )
            hook = settings["hooks"]["PreToolUse"][0]["hooks"][0]
            self.assertNotIn("shell", hook)
            self.assertIn("PreToolUse.cjs", hook["command"])
            self.assertTrue(hook["command"].startswith('node "'))

    def test_qwen_uses_cross_platform_yolo_flag(self):
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            flags, _ = driver.configure(
                "qwen",
                base / "home",
                base / "project",
                "witness",
                "http://127.0.0.1:1",
                pitot_command="pitot",
                witness_receipt="witness.jsonl",
                nonce="nonce",
                runtime_path="runtime.json",
            )
            self.assertIn("-y", flags)
            self.assertNotIn("--approval-mode", flags)

    def test_cursor_bridge_is_canonicalized_for_wsl(self):
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            lab = base / "lab"
            source = lab / "integrations/cursor/beforeShellExecution"
            source.parent.mkdir(parents=True)
            source.write_bytes(b"#!/usr/bin/env bash\r\necho hook\r\n")
            with mock.patch.object(driver, "LAB", lab):
                driver.configure(
                    "cursor",
                    base / "home",
                    base / "project",
                    "witness",
                    "http://127.0.0.1:1",
                    pitot_command="pitot",
                    witness_receipt="witness.jsonl",
                    nonce="nonce",
                    runtime_path="runtime.json",
                )
            bridge = (base / "project/.cursor/hooks/beforeShellExecution").read_bytes()
            self.assertEqual(bridge, b"#!/usr/bin/env bash\necho hook\n")

    def test_devin_routes_all_released_client_calls_to_isolated_proxy(self):
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            flags, environment = driver.configure(
                "devin",
                base / "home",
                base / "project",
                "witness",
                "http://127.0.0.1:9123",
                pitot_command="pitot",
                witness_receipt="witness.jsonl",
                nonce="nonce",
                runtime_path="runtime.json",
            )
            self.assertEqual(flags, [])
            self.assertEqual(
                environment["WINDSURF_API_SERVER_URL"], "http://127.0.0.1:9123"
            )
            self.assertEqual(environment["APPDATA"], str(base / "home/AppData/Roaming"))
            self.assertEqual(
                environment["LOCALAPPDATA"], str(base / "home/AppData/Local")
            )
            credentials = (
                base / "home/.local/share/devin/credentials.toml"
            ).read_text()
            self.assertIn('api_server_url = "http://127.0.0.1:9123"', credentials)

    def test_devin_connect_fixture_carries_direct_exec_call(self):
        payload = devin_proxy.tool_response("call-nonce", "echo nonce")
        self.assertEqual(payload[0], 0)
        self.assertIn(b"call-nonce", payload)
        self.assertIn(b"exec", payload)
        self.assertIn(b'{"command":"echo nonce"}', payload)

    def test_cursor_controller_command_proof_rejects_corruption_and_wrong_projection(
        self,
    ):
        expected = [
            "& 'C:/工具 path/canary.exe' PITOT_ALLOW nonce",
            "& 'C:/工具 path/canary.exe' PITOT_DENY nonce",
        ]
        requests = [
            {
                "action_id": f"act_{index}",
                "data": {
                    "type": "action.requested",
                    "host": {"name": "cursor"},
                    "action": {"id": f"act_{index}", "kind": "shell"},
                    "content": {"mode": "full", "full": command},
                },
            }
            for index, command in enumerate(expected)
        ]
        hashes = driver.validate_cursor_commands(requests, expected)
        self.assertEqual(len(hashes), 2)
        self.assertNotEqual(hashes[0], hashes[1])
        for mutation in (
            lambda value: value[0]["data"]["content"].update(full="corrupted 工具"),
            lambda value: value[0]["data"]["content"].update(
                full=expected[0].encode("utf-8").decode("latin1")
            ),
            lambda value: value[0]["data"]["content"].update(mode="sha256"),
            lambda value: value[0]["data"].update(command=expected[0], content={}),
            lambda value: value[0]["data"]["action"].update(id="act_unrelated"),
            lambda value: value[0]["data"].update(host=None),
            lambda value: value[0]["data"].update(host={"name": "copilot"}),
            lambda value: value[0].update(action_id=None),
            lambda value: value.reverse(),
            lambda value: value.pop(),
        ):
            broken = copy.deepcopy(requests)
            mutation(broken)
            with self.assertRaises(RuntimeError):
                driver.validate_cursor_commands(broken, expected)

    def test_cursor_windows_canary_uses_powershell_literal_invocation(self):
        command = driver.render_cursor_canary_command(
            "C:\\owned dir\\工具's $literal\\pitot-testrole.exe",
            "C:\\owned dir\\résult's $literal.json",
        )
        self.assertEqual(
            command,
            "& 'C:/owned dir/工具''s $literal/pitot-testrole.exe' "
            "'--role' 'canary' '--receipt' 'C:/owned dir/résult''s $literal.json'",
        )

    def test_windows_canary_command_is_shell_portable(self):
        command = driver.render_canary_command(
            r"D:\a\temp\pitot-testrole.exe",
            r"D:\a\temp\canary.jsonl",
            True,
        )
        self.assertIn("D:/a/temp/pitot-testrole.exe", command)
        self.assertIn("D:/a/temp/canary.jsonl", command)
        self.assertNotIn("\\", command)

    def test_anthropic_messages_emits_native_shell_tool(self):
        value = proxy.anthropic("Bash", "pitot-e2e-canary nonce", False)
        self.assertEqual(value["content"][0]["type"], "tool_use")
        self.assertEqual(
            value["content"][0]["input"]["command"], "pitot-e2e-canary nonce"
        )

    def test_openai_chat_emits_native_shell_tool(self):
        value = proxy.chat("bash", "pitot-e2e-canary nonce", False)
        call = value["choices"][0]["message"]["tool_calls"][0]["function"]
        self.assertEqual(call["name"], "bash")
        self.assertEqual(
            json.loads(call["arguments"])["command"], "pitot-e2e-canary nonce"
        )

    def test_shell_description_follows_advertised_required_schema(self):
        for required, kind, expected in [
            (["description"], "string", True),
            ([], "string", False),
            (["description"], "number", False),
        ]:
            body = {
                "tools": [
                    {
                        "function": {
                            "name": "bash",
                            "parameters": {
                                "required": required,
                                "properties": {"description": {"type": kind}},
                            },
                        }
                    }
                ]
            }
            arguments = proxy.advertised_tool_arguments(body, "bash", "command")
            self.assertEqual("description" in arguments, expected)
            self.assertEqual(arguments["command"], "command")

    def test_shell_description_ignores_malformed_schema(self):
        for parameters in [
            None,
            {"required": None},
            {"required": ["description"], "properties": []},
            {"required": ["description"], "properties": {"description": None}},
        ]:
            body = {"tools": [{"function": {"name": "bash", "parameters": parameters}}]}
            self.assertEqual(
                proxy.advertised_tool_arguments(body, "bash", "command"),
                {"command": "command"},
            )

    def test_openai_responses_emits_native_shell_tool(self):
        value = proxy.responses("shell", "pitot-e2e-canary nonce", False)
        self.assertEqual(value["output"][0]["type"], "function_call")
        self.assertEqual(
            json.loads(value["output"][0]["arguments"])["command"],
            "pitot-e2e-canary nonce",
        )

    def test_gemini_emits_native_shell_tool(self):
        value = proxy.gemini("run_shell_command", "pitot-e2e-canary nonce", False)
        call = value["candidates"][0]["content"]["parts"][0]["functionCall"]
        self.assertEqual(call["args"]["command"], "pitot-e2e-canary nonce")

    def test_final_responses_do_not_emit_tools(self):
        self.assertEqual(
            proxy.anthropic("Bash", "ignored", True)["content"][0]["type"], "text"
        )
        self.assertNotIn(
            "tool_calls", proxy.chat("bash", "ignored", True)["choices"][0]["message"]
        )
        self.assertEqual(
            proxy.responses("shell", "ignored", True)["output"][0]["type"], "message"
        )
        self.assertIn(
            "text",
            proxy.gemini("run_shell_command", "ignored", True)["candidates"][0][
                "content"
            ]["parts"][0],
        )

    def test_tool_selection_uses_agent_advertised_shell_name(self):
        body = {
            "tools": [{"type": "function", "function": {"name": "run_shell_command"}}]
        }
        self.assertEqual(proxy.tool_name(body), "run_shell_command")

    def test_live_request_classifies_all_json_dialects_without_manifest_hint(self):
        cases = (
            ("/v1/messages", {"messages": []}, "anthropic_messages"),
            ("/v1/chat/completions", {"messages": []}, "openai_chat"),
            ("/v1/responses", {"input": []}, "openai_responses"),
            (
                "/v1beta/models/x:streamGenerateContent",
                {"contents": []},
                "gemini_generate_content",
            ),
        )
        for path, body, expected in cases:
            with self.subTest(expected):
                self.assertEqual(proxy.classify_request(path, body), expected)

    def test_unknown_or_ambiguous_request_fails_closed(self):
        with self.assertRaises(proxy.UnknownProtocol):
            proxy.classify_request("/invented", {"messages": []})
        with self.assertRaises(proxy.UnknownProtocol):
            proxy.classify_request("/v1/messages", {"input": []})

    def test_tool_result_detection_rejects_echoed_tool_call_arguments(self):
        marker = "PITOT_CANARY_RESULT PITOT_ALLOW nonce"
        bodies = {
            "anthropic_messages": {
                "messages": [
                    {
                        "role": "assistant",
                        "content": [{"type": "tool_use", "input": {"command": marker}}],
                    }
                ]
            },
            "openai_chat": {
                "messages": [
                    {
                        "role": "assistant",
                        "tool_calls": [{"function": {"arguments": marker}}],
                    }
                ]
            },
            "openai_responses": {
                "input": [{"type": "function_call", "arguments": marker}]
            },
            "gemini_generate_content": {
                "contents": [
                    {
                        "role": "model",
                        "parts": [{"functionCall": {"args": {"command": marker}}}],
                    }
                ]
            },
        }
        for dialect, body in bodies.items():
            with self.subTest(dialect):
                self.assertFalse(proxy.tool_result_contains(dialect, body, marker))

    def test_tool_result_detection_accepts_only_native_result_fields(self):
        marker = "PITOT_CANARY_RESULT PITOT_ALLOW nonce"
        bodies = {
            "anthropic_messages": {
                "messages": [
                    {
                        "role": "user",
                        "content": [{"type": "tool_result", "content": marker}],
                    }
                ]
            },
            "openai_chat": {"messages": [{"role": "tool", "content": marker}]},
            "openai_responses": {
                "input": [
                    {
                        "type": "function_call_output",
                        "call_id": "pitot_tool_allow",
                        "output": marker,
                    }
                ]
            },
            "gemini_generate_content": {
                "contents": [
                    {
                        "role": "user",
                        "parts": [
                            {"functionResponse": {"response": {"output": marker}}}
                        ],
                    }
                ]
            },
        }
        for dialect, body in bodies.items():
            with self.subTest(dialect):
                self.assertTrue(
                    proxy.tool_result_contains(
                        dialect, body, marker, call_id="pitot_tool_allow"
                    )
                )

    def test_capture_record_binds_detected_dialect_and_executable(self):
        agent = next(
            item for item in driver.MANIFEST["agents"] if item["id"] == "claude"
        )
        installation = {
            "version": agent["version"],
            "runtime": "native",
            "executable_sha256": "a" * 64,
        }
        observed = {
            "transport": "http1",
            "method": "POST",
            "path": "/v1/messages",
            "media_type": "application/json",
            "framing": "sse",
            "request_shape": {},
        }
        record = driver.capture_record(
            agent,
            "macos",
            installation,
            {"protocol": "anthropic_messages", "endpoint_observed": observed},
        )
        self.assertEqual(record["provenance"], "pinned_real_cli_capture")
        self.assertEqual(record["dialect"], "anthropic_messages")
        self.assertRegex(record["capture_sha256"], r"^[0-9a-f]{64}$")

    def test_invented_endpoint_cannot_pass(self):
        agent = next(
            item for item in driver.MANIFEST["agents"] if item["id"] == "cursor"
        )
        installation = {
            "version": agent["version"],
            "runtime": "native",
            "executable_sha256": "a" * 64,
        }
        with self.assertRaisesRegex(RuntimeError, "supported request contract"):
            driver.capture_record(
                agent,
                "macos",
                installation,
                {"protocol": "invented", "endpoint_observed": {}},
            )

    def test_cursor_connect_protobuf_fixture(self):
        completed = subprocess.run(
            ["node", str(ROOT / "tests/cursor_control_proxy.mjs"), "--self-test"],
            check=True,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
        )
        self.assertIn("PASS", completed.stdout)

    def test_cursor_proxy_persists_native_path_before_ready(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            receipt = root / "cursor 工具 café # percent%" / "receipt.json"
            ready = root / "ready.txt"
            process = subprocess.Popen(
                [
                    "node",
                    str(ROOT / "tests/cursor_control_proxy.mjs"),
                    "--nonce",
                    "a" * 32,
                    "--receipt",
                    str(receipt),
                    "--ready-file",
                    str(ready),
                    "--canary-command",
                    "unused",
                    "--canary-argv",
                    json.dumps(["unused", "--role", "canary", "--receipt", "unused"]),
                ],
                stdout=subprocess.PIPE,
                stderr=subprocess.STDOUT,
                text=True,
                encoding="utf-8",
            )
            try:
                deadline = time.monotonic() + 5
                while not ready.is_file() and time.monotonic() < deadline:
                    if process.poll() is not None:
                        self.fail(process.communicate(timeout=5)[0])
                    time.sleep(0.01)
                self.assertTrue(ready.is_file())
                self.assertIsNone(process.poll())
                self.assertEqual(
                    json.loads(receipt.read_text(encoding="utf-8"))["nonce"], "a" * 32
                )
                self.assertRegex(
                    ready.read_text(encoding="utf-8"), r"^http://127\.0\.0\.1:\d+\n$"
                )
            finally:
                process.terminate()
                try:
                    process.communicate(timeout=5)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.communicate(timeout=5)


class ResponsesCallCorrelationTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.args = argparse.Namespace(
            agent="codex",
            nonce="nonce",
            receipt=str(Path(directory.name) / "receipt.json"),
            canary_command="pitot-e2e-canary",
            response_fault="none",
        )
        self.server = proxy.ThreadingHTTPServer(("127.0.0.1", 0), proxy.Handler)
        self.server.state = proxy.State(self.args)
        self.thread = threading.Thread(target=self.server.serve_forever)
        self.thread.start()
        self.addCleanup(self.close_server)

    def close_server(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=5)
        self.assertFalse(self.thread.is_alive())

    def post(self, inputs):
        body = {
            "model": "pitot-control",
            "tools": [{"type": "function", "name": "shell", "parameters": {}}],
            "input": inputs,
        }
        connection = http.client.HTTPConnection(
            "127.0.0.1", self.server.server_port, timeout=5
        )
        try:
            connection.request(
                "POST",
                "/v1/responses",
                json.dumps(body),
                {"Content-Type": "application/json"},
            )
            response = connection.getresponse()
            return response.status, response.read().decode()
        finally:
            connection.close()

    @staticmethod
    def result(phase, call_id):
        marker = (
            "PITOT_CANARY_RESULT PITOT_ALLOW nonce"
            if phase == "allow"
            else "PITOT_CONTROLLER_DENY nonce"
        )
        return {"type": "function_call_output", "call_id": call_id, "output": marker}

    def start(self):
        status, raw = self.post([{"role": "user", "content": "nonce"}])
        self.assertEqual(status, 200)
        completed = [
            json.loads(line[6:])
            for line in raw.splitlines()
            if line.startswith("data: {")
            and json.loads(line[6:]).get("type") == "response.completed"
        ]
        self.assertEqual(len(completed), 1)
        self.assertEqual(
            completed[0]["response"]["output"][0]["call_id"], "pitot_tool_allow"
        )

    def test_result_marker_requires_the_expected_native_call(self):
        marker = "PITOT_CANARY_RESULT PITOT_ALLOW nonce"
        for call_id in (None, "", 42, {}, "unrelated", "pitot_tool_deny"):
            with self.subTest(call_id=call_id):
                item = self.result("allow", call_id)
                self.assertFalse(
                    proxy.tool_result_contains(
                        "openai_responses",
                        {"input": [item]},
                        marker,
                        call_id="pitot_tool_allow",
                    )
                )
        correct = self.result("allow", "pitot_tool_allow")
        self.assertFalse(
            proxy.tool_result_contains("openai_responses", {"input": [correct]}, marker)
        )
        self.assertFalse(
            proxy.tool_result_contains(
                "openai_responses",
                {
                    "input": [
                        self.result("allow", "unrelated"),
                        {**correct, "output": ""},
                    ]
                },
                marker,
                call_id="pitot_tool_allow",
            )
        )

    def test_http_rejects_uncorrelated_results_and_preserves_full_history(self):
        self.start()
        bad_results = [
            self.result("allow", value)
            for value in (None, "", 42, {}, "wrong", "pitot_tool_deny")
        ]
        bad_results.append(
            {
                "type": "function_call_output",
                "output": "PITOT_CANARY_RESULT PITOT_ALLOW nonce",
            }
        )
        bad_results.append(
            {
                "type": "function_call",
                "call_id": "pitot_tool_allow",
                "arguments": "PITOT_CANARY_RESULT PITOT_ALLOW nonce",
            }
        )
        bad_results.append(
            {
                **self.result("allow", "pitot_tool_allow"),
                "output": "PITOT_CANARY_RESULT PITOT_ALLOW another_nonce",
            }
        )
        for item in bad_results:
            with self.subTest(item=item):
                self.assertEqual(self.post([item])[0], 409)
                self.assertFalse(
                    self.server.state.receipt["allow_tool_result_observed"]
                )
                self.assertFalse(
                    self.server.state.receipt["deny_tool_call_response_emitted"]
                )
        allow = self.result("allow", "pitot_tool_allow")
        history = [self.result("allow", "unrelated"), allow]
        self.assertEqual(self.post(history)[0], 200)
        self.assertTrue(self.server.state.receipt["deny_tool_call_response_emitted"])
        self.assertEqual(self.post([allow])[0], 409)
        self.assertEqual(self.post([self.result("deny", "pitot_tool_allow")])[0], 409)
        self.assertFalse(self.server.state.receipt["denied_result_observed"])
        self.assertEqual(
            self.post([*history, self.result("deny", "pitot_tool_deny")])[0], 200
        )
        self.assertTrue(self.server.state.receipt["final_response_emitted"])

    def test_text_only_response_cannot_admit_an_unissued_call_result(self):
        for agent in ("codex", "opencode"):
            with self.subTest(agent=agent):
                self.args.agent = agent
                self.args.response_fault = "text"
                self.server.state = proxy.State(self.args)
                self.assertEqual(
                    self.post([{"role": "user", "content": "nonce"}])[0], 200
                )
                self.assertFalse(
                    self.server.state.receipt["allow_tool_call_response_emitted"]
                )
                self.assertEqual(
                    self.post([self.result("allow", "pitot_tool_allow")])[0], 409
                )
                self.assertFalse(
                    self.server.state.receipt["allow_tool_result_observed"]
                )
                self.assertFalse(
                    self.server.state.receipt["deny_tool_call_response_emitted"]
                )


class CursorFixtureIdentityTests(unittest.TestCase):
    def test_native_windows_resolves_existing_owned_root_once(self):
        original, canonical = mock.Mock(), mock.Mock()
        original.resolve.return_value = canonical
        canonical.samefile.return_value = True
        with mock.patch.object(driver, "windows_host", return_value=True):
            self.assertIs(
                driver.owned_fixture_base(original, "cursor", "native"), canonical
            )
        original.resolve.assert_called_once_with(strict=True)
        canonical.samefile.assert_called_once_with(original)

    def test_other_hosts_and_runtimes_keep_the_original_root(self):
        for agent, runtime, windows in (
            ("cursor", "native", False),
            ("cursor", "wsl", True),
            ("claude", "native", True),
        ):
            with self.subTest(agent=agent, runtime=runtime, windows=windows):
                original = mock.Mock()
                with mock.patch.object(driver, "windows_host", return_value=windows):
                    self.assertIs(
                        driver.owned_fixture_base(original, agent, runtime), original
                    )
                original.resolve.assert_not_called()

    def test_missing_or_unresolvable_root_refuses_without_fallback(self):
        for error in (
            FileNotFoundError(),
            PermissionError(),
            OSError(),
            RuntimeError(),
        ):
            with self.subTest(error=type(error).__name__):
                original = mock.Mock()
                original.resolve.side_effect = error
                with mock.patch.object(driver, "windows_host", return_value=True):
                    with self.assertRaises(type(error)):
                        driver.owned_fixture_base(original, "cursor", "native")
                original.resolve.assert_called_once_with(strict=True)

    def test_changed_or_unverifiable_identity_refuses(self):
        for outcome in (False, PermissionError()):
            original, canonical = mock.Mock(), mock.Mock()
            original.resolve.return_value = canonical
            if isinstance(outcome, Exception):
                canonical.samefile.side_effect = outcome
            else:
                canonical.samefile.return_value = outcome
            with self.subTest(outcome=outcome):
                with mock.patch.object(driver, "windows_host", return_value=True):
                    with self.assertRaises((RuntimeError, PermissionError)):
                        driver.owned_fixture_base(original, "cursor", "native")


class CursorFixtureCleanupTests(unittest.TestCase):
    def test_existing_directory_refuses_and_absent_directory_passes(self):
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            with self.assertRaisesRegex(RuntimeError, "cleanup failed"):
                driver.require_fixture_removed(base)
            self.assertTrue(base.is_dir())
        driver.require_fixture_removed(base)

    def test_any_remaining_entry_refuses_without_deleting(self):
        base = mock.Mock()
        base.lstat.return_value = object()
        with self.assertRaisesRegex(RuntimeError, "cleanup failed"):
            driver.require_fixture_removed(base)
        base.lstat.assert_called_once_with()
        base.unlink.assert_not_called()
        base.rmdir.assert_not_called()

    def test_inspection_errors_are_not_absence(self):
        for error in (PermissionError(), OSError(), ValueError()):
            with self.subTest(error=type(error).__name__):
                base = mock.Mock()
                base.lstat.side_effect = error
                with self.assertRaises(type(error)):
                    driver.require_fixture_removed(base)
                base.lstat.assert_called_once_with()

    def test_only_not_found_is_accepted(self):
        base = mock.Mock()
        base.lstat.side_effect = FileNotFoundError()
        driver.require_fixture_removed(base)
        base.lstat.assert_called_once_with()


if __name__ == "__main__":
    unittest.main()
