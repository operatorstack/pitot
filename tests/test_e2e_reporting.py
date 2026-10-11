from __future__ import annotations

import importlib.util
import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
import urllib.request


ROOT = Path(__file__).resolve().parents[1]


def _load(name: str, path: Path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(module)
    return module


runner = _load("run_e2e_report", ROOT / "tests/run_e2e_report.py")
reporter = _load("pitot_e2e_report", ROOT / ".github/scripts/pitot_e2e_report.py")
supervisor = _load(
    "pitot_adapter_supervisor",
    ROOT / "scripts/pitot_adapter_supervisor.py",
)


def _receipt(agent="claude", platform="ubuntu"):
    record = next(item for item in runner.MANIFEST["agents"] if item["id"] == agent)
    dialect = {"claude": "anthropic_messages", "codex": "openai_responses", "gemini": "gemini_generate_content", "cursor": "cursor_connect_proto", "devin": "devin_connect_proto"}.get(agent, "openai_chat")
    framing = "connect_envelope" if dialect in {"cursor_connect_proto", "devin_connect_proto"} else "sse"
    request = {"transport": "http2" if dialect == "cursor_connect_proto" else "http1", "method": "POST", "path": "/fixture", "media_type": "application/connect+proto" if dialect in {"cursor_connect_proto", "devin_connect_proto"} else "application/json", "framing": framing, "request_shape": {"fixture": "redacted"}}
    response = {"encoder": dialect, "framing": framing, "tool_call": "native_shell", "acceptance": "nonce_tool_result_round_trip"}
    core = {"agent": agent, "platform": platform, "runtime": record["runtime"][platform], "version": record["version"], "executable_sha256": "d" * 64, "dialect": dialect, "request": request, "response": response, "provenance": "pinned_real_cli_capture"}
    fixture = {**core, "capture_sha256": hashlib.sha256(json.dumps(core, sort_keys=True, separators=(",", ":")).encode()).hexdigest()}
    reporter.ENDPOINT_PROVENANCE["cells"] = [cell for cell in reporter.ENDPOINT_PROVENANCE["cells"] if (cell["agent"], cell["platform"]) != (agent, platform)] + [fixture]
    endpoint = {
        "fixture": f"tests/endpoint-provenance.json#{agent}/{platform}",
        "fixture_sha256": fixture["capture_sha256"],
        "provenance": "pinned_real_cli_capture",
        "dialect": dialect,
        "request": request,
        "response": response,
        "executable_sha256": fixture["executable_sha256"],
    }
    return {
        "schema_version": 2,
        "agent": agent,
        "cli": {"version": record["version"], "executable": f"/bin/{record['executable']}", "executable_sha256": fixture["executable_sha256"], "installer": record["installer"]["kind"], "runtime": record["runtime"][platform]},
        "prompt_hash": "f" * 64,
        "protocol": dialect,
        "endpoint": endpoint,
        "nonce": "e" * 32,
        "receipts": {name: True for name in ("initial_prompt_observed", "allow_tool_call_response_emitted", "allow_tool_result_observed", "deny_tool_call_response_emitted", "denied_result_observed", "final_response_emitted", "consumer_observed", "controller_allow_observed", "controller_deny_observed", "deny_canary_absent", "final_output_observed", "cli_exit_zero")},
        "runtime": {"schema_version": 1, "instance_id": "instance", "pid": 1, "endpoint": "http://127.0.0.1:1", "config_sha256": "c" * 64},
        "boundaries": [
            {"host": agent, "transport": "acp" if agent == "devin" else "hook", "action_kind": "shell", "action_id": "act_allow", "decision": "allow", "nonce": "e" * 32},
            {"host": agent, "transport": "acp" if agent == "devin" else "hook", "action_kind": "shell", "action_id": "act_deny", "decision": "deny", "nonce": "e" * 32},
        ],
        "controller": {"id": "e2e-shell-controller", "action_ids": ["act_allow", "act_deny"], "outcomes": ["allow", "deny"]},
        "consumer": {"id": "e2e-audit", "action_ids": ["act_allow", "act_deny"], "projection": "sha256"},
        "canary": {"executions": ["PITOT_ALLOW " + "e" * 32], "denied_executions": 0},
    }


def _result(agent="claude", platform="ubuntu", status="pass", mode="real_cli"):
    receipt = _receipt(agent, platform) if status == "pass" else None
    return {
        "schema_version": 2,
        "agent": agent,
        "platform": platform,
        "status": status,
        "verification_mode": mode,
        "evidence": "binary-observed request, real action control, projected Consumer, allow/deny canary, and final receipts" if status == "pass" else "real-agent control evidence contract failed",
        "cli": receipt["cli"] if receipt else None,
        "protocol": receipt["protocol"] if receipt else None,
        "endpoint": receipt["endpoint"] if receipt else None,
        "prompt_hash": receipt["prompt_hash"] if receipt else None,
        "nonce": receipt["nonce"] if receipt else None,
        "receipts": receipt["receipts"] if receipt else None,
        "runtime": receipt["runtime"] if receipt else None,
        "boundaries": receipt["boundaries"] if receipt else None,
        "controller": receipt["controller"] if receipt else None,
        "consumer": receipt["consumer"] if receipt else None,
        "canary": receipt["canary"] if receipt else None,
        "commit_sha": "a" * 40,
        "run_url": "https://github.com/operatorstack/pitot/actions/runs/1",
    }


def _runtime_receipt(platform="ubuntu"):
    return {
        "schema_version": 1,
        "capability": "explicit_request",
        "platform": platform,
        "nonce": "e" * 32,
        "runtime": {"schema_version": 1, "instance_id": "instance", "pid": 1, "endpoint": "http://127.0.0.1:1", "config_sha256": "c" * 64},
        "controller": {"id": "e2e-release-controller", "action_ids": ["act_allow", "act_deny"], "outcomes": ["allow", "deny"]},
        "receipts": {"request_allow_observed": True, "request_deny_observed": True, "correlation_observed": True},
        "commit_sha": "a" * 40,
    }


class ResultGenerationTests(unittest.TestCase):
    def test_mock_anthropic_request_classifier(self):
        subprocess.run(
            ["node", "--test", str(ROOT / "tests/mock_anthropic_protocol_test.js")],
            check=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            text=True,
        )

    def test_real_cli_marker_passes(self):
        with tempfile.TemporaryDirectory() as temp:
            evidence = Path(temp) / "evidence.json"
            evidence.write_text(json.dumps(_receipt()))
            result = runner.result_for("claude", "ubuntu", 0, "PITOT_E2E_RESULT mode=real_cli evidence=nonce-correlated\n", evidence)
            self.assertEqual(result["status"], "pass")
            self.assertEqual(result["verification_mode"], "real_cli")

    def test_hook_marker_cannot_pass(self):
        result = runner.result_for("gemini", "macos", 0, "PITOT_E2E_RESULT mode=hook_subprocess\n")
        self.assertEqual(result["status"], "fail")
        self.assertIsNone(result["verification_mode"])

    def test_windows_result_is_supported(self):
        with tempfile.TemporaryDirectory() as temp:
            evidence = Path(temp) / "evidence.json"
            evidence.write_text(json.dumps(_receipt("opencode", "windows")))
            result = runner.result_for("opencode", "windows", 0, "PITOT_E2E_RESULT mode=real_cli evidence=nonce-correlated\n", evidence)
            self.assertEqual(result["platform"], "windows")
            self.assertEqual(result["status"], "pass")

    def test_missing_or_duplicate_marker_fails(self):
        self.assertEqual(runner.result_for("codex", "ubuntu", 0, "ok\n")["status"], "fail")
        duplicate = "PITOT_E2E_RESULT mode=real_cli evidence=nonce-correlated\n" * 2
        self.assertEqual(runner.result_for("codex", "ubuntu", 0, duplicate)["status"], "fail")

    def test_explicit_request_runtime_marker_passes(self):
        with tempfile.TemporaryDirectory() as temp:
            evidence = Path(temp) / "runtime-evidence.json"
            evidence.write_text(json.dumps(_runtime_receipt()))
            result = runner.result_for_runtime(
                "ubuntu", 0,
                "PITOT_RUNTIME_E2E_RESULT capability=explicit_request evidence=nonce-correlated\n",
                evidence,
            )
            self.assertEqual(result["status"], "pass")
            self.assertEqual(result["verification_mode"], "real_runtime")

    def test_nonzero_command_fails_even_with_marker(self):
        result = runner.result_for("cursor", "ubuntu", 1, "PITOT_E2E_RESULT mode=real_cli evidence=nonce-correlated\n")
        self.assertEqual(result["status"], "fail")
        self.assertIsNone(result["verification_mode"])

    def test_runner_replaces_non_utf8_command_output(self):
        with tempfile.TemporaryDirectory() as temp:
            output = Path(temp) / "result.json"
            completed = subprocess.run(
                [
                    sys.executable,
                    str(ROOT / "tests/run_e2e_report.py"),
                    "--agent", "claude",
                    "--platform", "windows",
                    "--output", str(output),
                    "--evidence", str(Path(temp) / "missing-evidence.json"),
                    "--",
                    sys.executable,
                    "-c", "import sys; sys.stdout.buffer.write(b'\\x96')",
                ],
                stdout=subprocess.PIPE,
                stderr=subprocess.STDOUT,
            )
            self.assertNotEqual(completed.returncode, 0)
            self.assertTrue(output.is_file())


class ReporterContractTests(unittest.TestCase):
    def test_artifact_listing_includes_inventory_beyond_default_page(self):
        github = reporter.GitHub("operatorstack/pitot", "test-token")
        calls = []

        def request(method, path, payload=None):
            calls.append((method, path, payload))
            return {"total_count": 31, "artifacts": [{"name": str(index)} for index in range(31)]}

        github.request = request
        self.assertEqual(len(github.list_run_artifacts(7)), 31)
        self.assertEqual(calls, [("GET", "/actions/runs/7/artifacts?per_page=100", None)])

    def test_truncated_artifact_listing_fails_closed(self):
        github = reporter.GitHub("operatorstack/pitot", "test-token")
        github.request = lambda method, path, payload=None: {"total_count": 31, "artifacts": [{}] * 30}
        with self.assertRaisesRegex(ValueError, "truncated or malformed"):
            github.list_run_artifacts(7)

    def test_artifact_redirect_strips_cross_host_credentials(self):
        request = urllib.request.Request(
            "https://api.github.com/repos/operatorstack/pitot/actions/artifacts/1/zip",
            headers={"Authorization": "Bearer secret"},
        )
        redirected = reporter.ArtifactRedirectHandler().redirect_request(
            request,
            None,
            302,
            "Found",
            {},
            "https://artifact.example.test/signed.zip",
        )
        self.assertIsNotNone(redirected)
        self.assertIsNone(redirected.get_header("Authorization"))

    def test_artifact_redirect_rejects_http_downgrade(self):
        request = urllib.request.Request("https://api.github.com/artifact")
        with self.assertRaisesRegex(ValueError, "must use HTTPS"):
            reporter.ArtifactRedirectHandler().redirect_request(
                request,
                None,
                302,
                "Found",
                {},
                "http://artifact.example.test/signed.zip",
            )

    def test_workflow_matrix_covers_every_reported_platform(self):
        workflow = (ROOT / ".github/workflows/pitot-e2e-agent.yml").read_text()
        unified = (ROOT / ".github/workflows/pitot-e2e.yml").read_text()
        runner_script = (ROOT / "tests/e2e_unified_runner.sh").read_text()
        self.assertEqual(set(reporter.PLATFORMS), set(runner.PLATFORMS))
        generated = supervisor.matrix(supervisor.load_manifest_without_provenance(ROOT))
        self.assertEqual(len(generated), len(reporter.AGENTS) * len(reporter.PLATFORMS))
        self.assertEqual(
            {(item["agent"], item["platform"]) for item in generated},
            {(agent, platform) for agent in reporter.AGENTS for platform in reporter.PLATFORMS},
        )
        self.assertIn("runs-on: ${{ inputs.runner }}", workflow)
        self.assertIn('--platform "${{ inputs.platform }}"', workflow)
        self.assertIn('bash_path="$(cygpath -m "$bash_path")"', workflow)
        self.assertIn('-- "$PITOT_BASH"', workflow)
        self.assertIn("node_version=22.23.1", workflow)
        self.assertIn("SHASUMS256.txt", workflow)
        self.assertNotIn('-- bash "tests/e2e_', workflow)
        self.assertIn("fromJSON(needs.inventory.outputs.agent_matrix)", unified)
        self.assertIn("fromJSON(needs.inventory.outputs.runtime_matrix)", unified)
        self.assertIn("pitot-e2e-runtime-${{ matrix.platform }}", unified)
        self.assertIn('-- "$PITOT_BASH" tests/e2e_runtime_cli_test.sh', unified)
        self.assertIn("name: pitot-e2e-inventory", unified)
        self.assertEqual(runner_script.count("GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build"), 3)
        self.assertIn("pitot-linux", runner_script)
        self.assertIn("pitot-witness-linux", runner_script)
        self.assertIn("pitot-testrole-linux", runner_script)
        self.assertIn("wsl.exe --distribution Ubuntu -- env", runner_script)
        self.assertIn('python3 "$(to_wsl_path "$SCRIPT_DIR/real_agent_driver.py")"', runner_script)
        self.assertIn("MSYS2_ARG_CONV_EXCL='*'", runner_script)

    def test_inventory_schema_rejects_identity_injection(self):
        inventory = json.loads((ROOT / "adapter-verification.json").read_text())
        self.assertIs(reporter.validate_inventory(inventory), inventory)
        inventory["agents"][0]["label"] = "Claude | @everyone"
        with self.assertRaisesRegex(ValueError, "label"):
            reporter.validate_inventory(inventory)

    def test_missing_artifacts_fail_every_inventory_cell(self):
        inventory = reporter.validate_inventory(
            json.loads((ROOT / "adapter-verification.json").read_text())
        )
        results = reporter.collect_results(
            None,
            {},
            inventory,
            "a" * 40,
            "https://github.com/operatorstack/pitot/actions/runs/1",
        )
        self.assertEqual(len(results), 10)
        self.assertTrue(
            all(
                result["status"] == "fail"
                for platforms in results.values()
                for result in platforms.values()
            )
        )

    def test_validates_exact_artifact_schema(self):
        value = _result()
        self.assertIs(reporter.validate_result(value, agent="claude", platform="ubuntu"), value)
        value["unexpected"] = True
        with self.assertRaises(ValueError):
            reporter.validate_result(value, agent="claude", platform="ubuntu")

    def test_rejects_identity_and_evidence_injection(self):
        with self.assertRaises(ValueError):
            reporter.validate_result(_result(agent="cursor"), agent="claude", platform="ubuntu")
        value = _result()
        value["evidence"] = "@everyone | injected"
        with self.assertRaises(ValueError):
            reporter.validate_result(value, agent="claude", platform="ubuntu")

    def test_rejects_incomplete_or_mismatched_causal_receipts(self):
        value = _result()
        value["receipts"]["controller_deny_observed"] = False
        with self.assertRaisesRegex(ValueError, "incomplete causal receipts"):
            reporter.validate_result(value, agent="claude", platform="ubuntu")
        value = _result()
        value["boundaries"][0]["host"] = "cursor"
        with self.assertRaisesRegex(ValueError, "boundary receipt"):
            reporter.validate_result(value, agent="claude", platform="ubuntu")
        value = _result()
        value["cli"]["version"] = "latest"
        with self.assertRaisesRegex(ValueError, "supervised manifest"):
            reporter.validate_result(value, agent="claude", platform="ubuntu")
        value = _result()
        value["endpoint"]["fixture_sha256"] = "0" * 64
        with self.assertRaisesRegex(ValueError, "endpoint receipt"):
            reporter.validate_result(value, agent="claude", platform="ubuntu")
        value = _result()
        value["cli"]["executable_sha256"] = "0" * 64
        with self.assertRaisesRegex(ValueError, "executable digest"):
            reporter.validate_result(value, agent="claude", platform="ubuntu")

    def test_rejects_untrusted_commit_and_run_url(self):
        value = _result()
        with self.assertRaises(ValueError):
            reporter.validate_result(
                value,
                agent="claude",
                platform="ubuntu",
                expected_sha="b" * 40,
            )
        value["run_url"] = "https://example.test/injected"
        with self.assertRaises(ValueError):
            reporter.validate_result(value, agent="claude", platform="ubuntu")

    def test_aggregate_requires_all_platforms(self):
        self.assertEqual(reporter.aggregate({"ubuntu": _result(), "macos": None}), "pending")
        self.assertEqual(
            reporter.aggregate(
                {
                    "ubuntu": _result(),
                    "macos": _result(platform="macos"),
                    "windows": None,
                }
            ),
            "pending",
        )
        self.assertEqual(
            reporter.aggregate(
                {
                    "ubuntu": _result(),
                    "macos": _result(platform="macos"),
                    "windows": _result(platform="windows"),
                }
            ),
            "passing",
        )
        self.assertEqual(
            reporter.aggregate(
                {
                    "ubuntu": _result(),
                    "macos": _result(platform="macos", status="fail", mode=None),
                    "windows": _result(platform="windows"),
                }
            ),
            "failing",
        )

    def test_rendered_comment_is_sticky_and_shows_modes(self):
        results = {
            agent: {platform: None for platform in reporter.PLATFORMS}
            for agent in reporter.AGENTS
        }
        results["claude"] = {
            "ubuntu": _result(),
            "macos": _result(platform="macos"),
            "windows": _result(platform="windows"),
        }
        body = reporter.render_comment(results, {"claude": "https://example.test/run"}, "b" * 40)
        self.assertIn(reporter.MARKER, body)
        self.assertIn("Pass · real CLI", body)
        self.assertNotIn("hook verified", body)
        self.assertIn("real CLI 2.1.217 · native", body)
        self.assertIn("Windows", body)
        self.assertIn("All platforms are required", body)
        self.assertIn("Runtime capabilities", body)
        self.assertIn("`pitot request`", body)
        self.assertIn("OpenCode", body)
        self.assertIn("Kimi Code", body)


if __name__ == "__main__":
    unittest.main()
