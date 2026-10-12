from __future__ import annotations

import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
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
                "input": [{"type": "function_call_output", "output": marker}]
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
                self.assertTrue(proxy.tool_result_contains(dialect, body, marker))

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


if __name__ == "__main__":
    unittest.main()
