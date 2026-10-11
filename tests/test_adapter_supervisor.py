from __future__ import annotations

import copy
import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location(
    "pitot_adapter_supervisor",
    ROOT / "scripts/pitot_adapter_supervisor.py",
)
supervisor = importlib.util.module_from_spec(SPEC)
assert SPEC and SPEC.loader
SPEC.loader.exec_module(supervisor)


def _manifest() -> dict[str, object]:
    return json.loads((ROOT / supervisor.MANIFEST).read_text())


def _provenance(manifest: dict[str, object]) -> dict[str, object]:
    dialects = {"claude": "anthropic_messages", "codex": "openai_responses", "gemini": "gemini_generate_content", "cursor": "cursor_connect_proto", "devin": "devin_connect_proto"}
    paths = {"anthropic_messages": "/v1/messages", "openai_responses": "/v1/responses", "gemini_generate_content": "/v1beta/models/pitot-control:streamGenerateContent", "cursor_connect_proto": "/agent.v1.AgentService/Run", "devin_connect_proto": "/connectrpc.devin.api.ModelService/StreamResponse", "openai_chat": "/v1/chat/completions"}
    cells = []
    for agent in manifest["agents"]:
        for platform in manifest["platforms"]:
            dialect = dialects.get(agent["id"], "openai_chat")
            framing = "connect_envelope" if dialect in {"cursor_connect_proto", "devin_connect_proto"} else "sse"
            request = {"transport": "http2" if dialect == "cursor_connect_proto" else "http1", "method": "POST", "path": paths[dialect], "media_type": "application/connect+proto" if dialect in {"cursor_connect_proto", "devin_connect_proto"} else "application/json", "framing": framing, "request_shape": {"fixture": "redacted"}}
            response = {"encoder": dialect, "framing": framing, "tool_call": "native_shell", "acceptance": "nonce_tool_result_round_trip"}
            core = {"agent": agent["id"], "platform": platform["id"], "runtime": agent["runtime"][platform["id"]], "version": agent["version"], "executable_sha256": hashlib.sha256(f"{agent['id']}/{platform['id']}".encode()).hexdigest(), "dialect": dialect, "request": request, "response": response, "provenance": "pinned_real_cli_capture"}
            cells.append({**core, "capture_sha256": hashlib.sha256(json.dumps(core, sort_keys=True, separators=(",", ":")).encode()).hexdigest()})
    return {"schema_version": 2, "capture_policy": "pinned_real_cli_capture", "cells": cells}


def _fixture(root: Path) -> tuple[Path, list[str]]:
    manifest = copy.deepcopy(_manifest())
    manifest_path = root / supervisor.MANIFEST
    manifest_path.parent.mkdir(parents=True, exist_ok=True)
    manifest_path.write_text(json.dumps(manifest), encoding="utf-8")
    tests = root / "tests"
    tests.mkdir(parents=True, exist_ok=True)
    (tests / "endpoint-provenance.json").write_text(json.dumps(_provenance(manifest)), encoding="utf-8")
    ids = [agent["id"] for agent in manifest["agents"]]
    for agent_id in ids:
        (tests / f"e2e_{agent_id}_cli_test.sh").write_text("#!/usr/bin/env bash\n")
    for agent in manifest["agents"]:
        for artifact in agent["artifacts"]:
            path = root / artifact
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("# supervised integration artifact\n")
    (tests / "real_agent_driver.py").write_text("# canonical driver\n")
    (tests / "model_control_proxy.py").write_text("# proxy\n")
    (tests / "cursor_control_proxy.mjs").write_text("// cursor proxy\n")
    (tests / "devin_control_proxy.py").write_text("# devin proxy\n")
    (tests / "e2e_unified_runner.sh").write_text("python3 real_agent_driver.py\n")
    readme = root / supervisor.README
    readme.parent.mkdir(parents=True, exist_ok=True)
    readme.write_text(
        f"# Pitot\n\n{supervisor.README_START}\nplaceholder\n{supervisor.README_END}\n",
        encoding="utf-8",
    )
    (root / ".github/workflows").mkdir(parents=True, exist_ok=True)
    reusable = root / supervisor.REUSABLE_WORKFLOW
    reusable.write_text(
        "platform:\n        description: Canonical Pitot platform identifier\n"
        "runner:\n        description: GitHub-hosted runner selected by the supervisor\n"
        "capture:\n        description: Emit a redacted binary-observed candidate fixture\n"
        "runs-on: ${{ inputs.runner }}\n"
        '--agent "${{ inputs.agent }}"\n'
        '--platform "${{ inputs.platform }}"\n'
        '--evidence "${{ runner.temp }}/pitot-e2e/evidence.json"\n'
        "install_real_agent.py\n"
        "PITOT_CAPTURE_OUTPUT:\n"
        "pitot-endpoint-capture-${{ inputs.agent }}-${{ inputs.platform }}\n"
        "name: pitot-e2e-${{ inputs.agent }}-${{ inputs.platform }}\n"
    )
    supervisor.render(root)
    return manifest_path, ids


class AdapterSupervisorTests(unittest.TestCase):
    def test_real_repository_contract_is_complete(self):
        self.assertEqual(supervisor.contract_errors(ROOT), [])

    def test_matrix_contains_every_agent_on_every_platform(self):
        manifest = _manifest()
        matrix = supervisor.matrix(manifest)
        self.assertEqual(len(matrix), 30)
        self.assertEqual(
            {(item["agent"], item["platform"]) for item in matrix if item["agent"] == "kimi"},
            {("kimi", "ubuntu"), ("kimi", "macos"), ("kimi", "windows")},
        )
        self.assertEqual(
            {(item["agent"], item["platform"]) for item in matrix if item["agent"] in {"copilot", "pi", "qwen"}},
            {(agent, platform) for agent in {"copilot", "pi", "qwen"} for platform in {"ubuntu", "macos", "windows"}},
        )
        runtime = supervisor.runtime_matrix(manifest)
        self.assertEqual(
            runtime,
            [
                {"capability": "explicit_request", "platform": "ubuntu", "runner": "ubuntu-latest"},
                {"capability": "explicit_request", "platform": "macos", "runner": "macos-latest"},
                {"capability": "explicit_request", "platform": "windows", "runner": "windows-latest"},
            ],
        )

    def test_capability_inventory_is_exact(self):
        manifest = copy.deepcopy(_manifest())
        manifest["capabilities"] = manifest["capabilities"][:-1]
        with self.assertRaisesRegex(supervisor.ContractError, "capabilities must exactly supervise"):
            supervisor.validate_manifest(manifest)
        manifest = copy.deepcopy(_manifest())
        manifest["capabilities"].append({"id": "phantom", "matrix": "platform"})
        with self.assertRaisesRegex(supervisor.ContractError, "capabilities must exactly supervise"):
            supervisor.validate_manifest(manifest)

    def test_missing_integration_artifact_fails(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            _, ids = _fixture(root)
            manifest = json.loads((root / supervisor.MANIFEST).read_text())
            agent = next(item for item in manifest["agents"] if item["artifacts"])
            artifact = agent["artifacts"][0]
            (root / artifact).unlink()
            errors = supervisor.contract_errors(root, ids)
            self.assertIn(f"adapter {agent['id']} is missing supervised artifact {artifact}", errors)

    def test_missing_and_extra_inventory_entries_fail(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            manifest_path, ids = _fixture(root)
            value = json.loads(manifest_path.read_text())
            removed = value["agents"][0]["id"]
            value["agents"] = value["agents"][1:]
            manifest_path.write_text(json.dumps(value))
            provenance_path = root / supervisor.ENDPOINT_PROVENANCE
            provenance = json.loads(provenance_path.read_text())
            provenance["cells"] = [cell for cell in provenance["cells"] if cell["agent"] != removed]
            provenance_path.write_text(json.dumps(provenance))
            errors = supervisor.contract_errors(root, ids)
            self.assertTrue(any("missing from manifest" in error for error in errors))

            phantom = copy.deepcopy(value["agents"][0])
            phantom.update({
                "id": "phantom",
                "label": "Phantom",
                "executable": "phantom",
                "artifacts": [],
                "runtime": {"ubuntu": "native", "macos": "native", "windows": "native"},
            })
            value["agents"].append(phantom)
            manifest_path.write_text(json.dumps(value))
            provenance = _provenance(value)
            provenance_path.write_text(json.dumps(provenance))
            errors = supervisor.contract_errors(root, ids)
            self.assertTrue(any("not built-in adapters" in error for error in errors))

    def test_duplicate_id_and_label_fail(self):
        manifest = copy.deepcopy(_manifest())
        manifest["agents"].append(copy.deepcopy(manifest["agents"][0]))
        with self.assertRaisesRegex(supervisor.ContractError, "ids must be unique"):
            supervisor.validate_manifest(manifest)
        manifest = copy.deepcopy(_manifest())
        manifest["agents"][1]["label"] = manifest["agents"][0]["label"]
        with self.assertRaisesRegex(supervisor.ContractError, "labels must be unique"):
            supervisor.validate_manifest(manifest)

    def test_mutable_or_incomplete_real_cli_metadata_fails(self):
        manifest = copy.deepcopy(_manifest())
        manifest["agents"][0]["version"] = "latest"
        with self.assertRaisesRegex(supervisor.ContractError, "immutable CLI version"):
            supervisor.validate_manifest(manifest)
        manifest = copy.deepcopy(_manifest())
        manifest["agents"][0]["required_mode"] = "hook_subprocess"
        with self.assertRaisesRegex(supervisor.ContractError, "real-CLI driver"):
            supervisor.validate_manifest(manifest)
        manifest = copy.deepcopy(_manifest())
        del manifest["agents"][0]["runtime"]["windows"]
        with self.assertRaisesRegex(supervisor.ContractError, "every platform runtime"):
            supervisor.validate_manifest(manifest)

    def test_matrix_carries_install_protocol_and_runtime_contract(self):
        matrix = supervisor.matrix(_manifest())
        cursor_windows = next(item for item in matrix if item["agent"] == "cursor" and item["platform"] == "windows")
        self.assertEqual(cursor_windows["runtime"], "wsl")
        self.assertEqual(cursor_windows["version"], "2026.07.20-8cc9c0b")
        self.assertNotIn("protocol", cursor_windows)
        self.assertEqual(cursor_windows["endpoint_fixture"], "tests/endpoint-provenance.json#cursor/windows")

    def test_stale_or_missing_endpoint_provenance_fails(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            _, ids = _fixture(root)
            fixture_path = root / supervisor.ENDPOINT_PROVENANCE
            fixture = json.loads(fixture_path.read_text())
            cell = next(item for item in fixture["cells"] if item["agent"] == "cursor" and item["platform"] == "windows")
            cell["version"] = "0.0.0"
            fixture_path.write_text(json.dumps(fixture))
            errors = supervisor.contract_errors(root, ids)
            self.assertTrue(any("stale for its pinned CLI" in error for error in errors))

    def test_platform_swap_and_fabricated_capture_digest_fail(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            _, ids = _fixture(root)
            fixture_path = root / supervisor.ENDPOINT_PROVENANCE
            fixture = json.loads(fixture_path.read_text())
            cell = next(item for item in fixture["cells"] if item["agent"] == "cursor" and item["platform"] == "windows")
            cell["runtime"] = "native"
            fixture_path.write_text(json.dumps(fixture))
            self.assertTrue(any("stale for its pinned CLI" in error for error in supervisor.contract_errors(root, ids)))
            fixture = _provenance(_manifest())
            fixture["cells"][0]["capture_sha256"] = "0" * 64
            fixture_path.write_text(json.dumps(fixture))
            self.assertTrue(any("fabricated capture digest" in error for error in supervisor.contract_errors(root, ids)))

    def test_capture_merge_requires_all_30_accepted_unique_cells(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            _fixture(root)
            manifest = _manifest()
            provenance = _provenance(manifest)
            captures = root / "captures"
            captures.mkdir()
            for cell in provenance["cells"]:
                (captures / f"{cell['agent']}-{cell['platform']}.json").write_text(json.dumps({"schema_version": 1, "accepted": True, "cell": cell}))
            output = root / "merged.json"
            supervisor.merge_captures(root, captures, output)
            self.assertEqual(len(json.loads(output.read_text())["cells"]), 30)
            next(captures.glob("*.json")).unlink()
            with self.assertRaisesRegex(supervisor.ContractError, "exactly all 30"):
                supervisor.merge_captures(root, captures, output)

    def test_capture_merge_rejects_mixed_versions_and_duplicates(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            _fixture(root)
            provenance = _provenance(_manifest())
            captures = root / "captures"
            captures.mkdir()
            for index, cell in enumerate(provenance["cells"]):
                (captures / f"{index}.json").write_text(json.dumps({"schema_version": 1, "accepted": True, "cell": cell}))
            duplicate = provenance["cells"][0]
            (captures / "duplicate.json").write_text(json.dumps({"schema_version": 1, "accepted": True, "cell": duplicate}))
            with self.assertRaisesRegex(supervisor.ContractError, "exactly all 30"):
                supervisor.merge_captures(root, captures, root / "merged.json")
            (captures / "duplicate.json").unlink()
            first = captures / "0.json"
            value = json.loads(first.read_text())
            value["cell"]["version"] = "0.0.0"
            first.write_text(json.dumps(value))
            with self.assertRaisesRegex(supervisor.ContractError, "stale for its pinned CLI"):
                supervisor.merge_captures(root, captures, root / "merged.json")

    def test_missing_script_fails(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            _, ids = _fixture(root)
            (root / "tests/e2e_kimi_cli_test.sh").unlink()
            self.assertTrue(any("adapter kimi is missing" in error for error in supervisor.contract_errors(root, ids)))

    def test_readme_and_workflow_drift_fail(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            _, ids = _fixture(root)
            readme = root / supervisor.README
            readme.write_text(readme.read_text().replace("Every supervised", "Some supervised"))
            self.assertTrue(any("README.md" in error for error in supervisor.contract_errors(root, ids)))

            supervisor.render(root)
            workflow = root / supervisor.UNIFIED_WORKFLOW
            workflow.write_text(workflow.read_text() + "# drift\n")
            self.assertTrue(any("pitot-e2e.yml" in error for error in supervisor.contract_errors(root, ids)))

    def test_reusable_workflow_cannot_select_its_own_platforms(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            _, ids = _fixture(root)
            workflow = root / supervisor.REUSABLE_WORKFLOW
            workflow.write_text(workflow.read_text() + "matrix.platform\n")
            self.assertTrue(any("escaped supervisor" in error for error in supervisor.contract_errors(root, ids)))


if __name__ == "__main__":
    unittest.main()
