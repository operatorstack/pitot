#!/usr/bin/env python3
"""Supervise Pitot's complete built-in adapter verification boundary."""

from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import re
import subprocess
import sys


ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "tests"))
from installation_identity import validate_identity_evidence

MANIFEST = Path("adapter-verification.json")
ENDPOINT_PROVENANCE = Path("tests/endpoint-provenance.json")
README = Path("README.md")
UNIFIED_WORKFLOW = Path(".github/workflows/pitot-e2e.yml")
REUSABLE_WORKFLOW = Path(".github/workflows/pitot-e2e-agent.yml")
INVENTORY_HELPER = Path("scripts/pitot_adapter_inventory.go")
README_START = "<!-- pitot-adapter-supervisor:start -->"
README_END = "<!-- pitot-adapter-supervisor:end -->"
ID_PATTERN = re.compile(r"^[a-z][a-z0-9-]*$")
LABEL_PATTERN = re.compile(r"^[A-Za-z0-9][A-Za-z0-9 .+_-]{0,39}$")
VERSION_PATTERN = re.compile(r"^(?:[0-9]+\.){2}[0-9]+(?:-[0-9A-Za-z.-]+)?$")
DIALECTS = {
    "anthropic_messages",
    "openai_chat",
    "openai_responses",
    "gemini_generate_content",
    "cursor_connect_proto",
    "devin_connect_proto",
}
INTEGRATIONS = {
    "native_command_hook",
    "pi_extension",
    "opencode_plugin",
    "acp_client",
    "native_pre_execute",
}
INSTALLERS = {"npm", "kimi_release", "cursor_release", "devin_release"}


def canonical_relative_path(value: object) -> bool:
    if (
        not isinstance(value, str)
        or not value
        or value.startswith("/")
        or "\\" in value
        or "\x00" in value
        or any(part in {"", ".", ".."} for part in value.split("/"))
    ):
        return False
    try:
        value.encode("utf-8")
    except UnicodeError:
        return False
    return True


class ContractError(ValueError):
    pass


def load_manifest(root: Path = ROOT) -> dict[str, object]:
    try:
        value = json.loads((root / MANIFEST).read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise ContractError(f"cannot load {MANIFEST}: {error}") from error
    validate_manifest(value)
    validate_endpoint_provenance(root, value)
    return value


def validate_manifest(value: object) -> None:
    if not isinstance(value, dict) or set(value) != {
        "schema_version",
        "capabilities",
        "platforms",
        "agents",
    }:
        raise ContractError(
            "manifest must contain schema_version, capabilities, platforms, and agents"
        )
    if value["schema_version"] != 7:
        raise ContractError("unsupported manifest schema_version")
    expected_capabilities = [
        {"id": "action_control", "matrix": "agent_platform"},
        {"id": "consumer_delivery", "matrix": "agent_platform"},
        {"id": "explicit_request", "matrix": "platform"},
    ]
    if value["capabilities"] != expected_capabilities:
        raise ContractError(
            "capabilities must exactly supervise action control, Consumer delivery, and explicit request"
        )
    platforms = value["platforms"]
    agents = value["agents"]
    if not isinstance(platforms, list) or not isinstance(agents, list) or not agents:
        raise ContractError("platforms and non-empty agents must be lists")
    expected_platforms = [
        {"id": "ubuntu", "runner": "ubuntu-latest"},
        {"id": "macos", "runner": "macos-latest"},
        {"id": "windows", "runner": "windows-latest"},
    ]
    if platforms != expected_platforms:
        raise ContractError(
            "platforms must be the required Ubuntu, macOS, and Windows matrix"
        )
    ids: list[str] = []
    labels: list[str] = []
    for agent in agents:
        required = {
            "id",
            "label",
            "version",
            "executable",
            "installer",
            "integration",
            "artifacts",
            "runtime",
            "driver",
            "required_mode",
        }
        if not isinstance(agent, dict) or set(agent) != required:
            raise ContractError(
                f"each agent must contain exactly {', '.join(sorted(required))}"
            )
        agent_id, label = agent["id"], agent["label"]
        if not isinstance(agent_id, str) or not ID_PATTERN.fullmatch(agent_id):
            raise ContractError(f"invalid agent id: {agent_id!r}")
        if not isinstance(label, str) or not LABEL_PATTERN.fullmatch(label):
            raise ContractError(f"agent {agent_id} has an invalid label")
        version = agent["version"]
        if (
            not isinstance(version, str)
            or not VERSION_PATTERN.fullmatch(version)
            or "latest" in version.lower()
        ):
            raise ContractError(f"agent {agent_id} must pin an immutable CLI version")
        if not isinstance(agent["executable"], str) or not ID_PATTERN.fullmatch(
            agent["executable"]
        ):
            raise ContractError(f"agent {agent_id} has an invalid executable")
        installer = agent["installer"]
        if (
            not isinstance(installer, dict)
            or set(installer) != {"kind", "package"}
            or installer["kind"] not in INSTALLERS
            or not isinstance(installer["package"], str)
            or not installer["package"]
        ):
            raise ContractError(
                f"agent {agent_id} has invalid pinned installer metadata"
            )
        if agent["integration"] not in INTEGRATIONS:
            raise ContractError(
                f"agent {agent_id} has an unsupported integration mechanism"
            )
        artifacts = agent["artifacts"]
        artifact_prefix = f"integrations/{agent_id}/"
        if (
            not isinstance(artifacts, list)
            or any(
                not canonical_relative_path(path)
                or not path.startswith(artifact_prefix)
                for path in artifacts
            )
            or len(artifacts) != len(set(artifacts))
        ):
            raise ContractError(
                f"agent {agent_id} has invalid supervised integration artifacts"
            )
        if agent_id == "cursor" and set(artifacts) != {
            "integrations/cursor/beforeShellExecution",
            "integrations/cursor/beforeShellExecution.ps1",
        }:
            raise ContractError(
                "Cursor must supervise its Unix and native Windows bridges"
            )
        expected_runtime_keys = {platform["id"] for platform in platforms}
        runtime = agent["runtime"]
        if not isinstance(runtime, dict) or set(runtime) != expected_runtime_keys:
            raise ContractError(f"agent {agent_id} must declare every platform runtime")
        if any(item not in {"native", "wsl"} for item in runtime.values()):
            raise ContractError(f"agent {agent_id} has an unsupported platform runtime")
        if "wsl" in runtime.values():
            raise ContractError(f"agent {agent_id} must run natively on every platform")
        if (
            agent["driver"] != "real_agent_driver.py"
            or agent["required_mode"] != "real_cli"
        ):
            raise ContractError(
                f"agent {agent_id} must require the canonical real-CLI driver"
            )
        ids.append(agent_id)
        labels.append(label)
    if len(ids) != len(set(ids)):
        raise ContractError("agent ids must be unique")
    if len(labels) != len(set(labels)):
        raise ContractError("agent labels must be unique")


def validate_endpoint_provenance(root: Path, manifest: dict[str, object]) -> None:
    """Require endpoint claims to be pinned to the real-CLI capture contract."""
    try:
        value = json.loads((root / ENDPOINT_PROVENANCE).read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise ContractError(f"cannot load {ENDPOINT_PROVENANCE}: {error}") from error
    validate_endpoint_value(value, manifest)
    if (
        value["schema_version"] in {3, 4, 5}
        and value["source"]["catalog_sha256"]
        != hashlib.sha256((root / MANIFEST).read_bytes()).hexdigest()
    ):
        raise ContractError(
            "endpoint capture source catalog digest differs from current manifest"
        )
    if value["schema_version"] in {4, 5}:
        from pitot_capture_source import validate_current_producers

        try:
            validate_current_producers(root, value["source"], manifest)
        except (OSError, ValueError, RuntimeError) as error:
            raise ContractError(
                f"invalid current capture producers: {error}"
            ) from error


def validate_endpoint_value(value: object, manifest: dict[str, object]) -> None:
    required = {
        "schema_version",
        "capture_policy",
        "cells",
    }
    if isinstance(value, dict) and value.get("schema_version") in {3, 4, 5}:
        required.add("source")
    if not isinstance(value, dict) or set(value) != required:
        raise ContractError(
            "endpoint provenance must contain schema_version, capture_policy, and cells"
        )
    if (
        type(value["schema_version"]) is not int
        or value["schema_version"] not in {2, 3, 4, 5}
        or value["capture_policy"] != "pinned_real_cli_capture"
    ):
        raise ContractError("endpoint provenance must require pinned real-CLI capture")
    cells = value["cells"]
    if not isinstance(cells, list):
        raise ContractError("endpoint provenance cells must be a list")
    declared = {agent["id"]: agent for agent in manifest["agents"]}
    platforms = [platform["id"] for platform in manifest["platforms"]]
    expected_keys = {
        (agent_id, platform) for agent_id in declared for platform in platforms
    }
    actual_keys = {
        (cell.get("agent"), cell.get("platform"))
        for cell in cells
        if isinstance(cell, dict)
    }
    if len(cells) != len(expected_keys) or actual_keys != expected_keys:
        raise ContractError(
            f"endpoint provenance must contain exactly all {len(expected_keys)} agent/platform cells"
        )
    required = {
        "agent",
        "platform",
        "runtime",
        "version",
        "executable_sha256",
        "dialect",
        "request",
        "response",
        "provenance",
        "capture_sha256",
    }
    if value["schema_version"] == 5:
        required.add("installation")
    for cell in cells:
        agent_id, platform = cell["agent"], cell["platform"]
        if set(cell) != required:
            raise ContractError(
                f"endpoint fixture {agent_id}/{platform} has an invalid schema"
            )
        agent = declared[agent_id]
        if value["schema_version"] == 5:
            try:
                validate_identity_evidence(agent_id, platform, cell["installation"])
            except ValueError as error:
                raise ContractError(str(error)) from error
        if (
            cell["version"] != agent["version"]
            or cell["runtime"] != agent["runtime"][platform]
        ):
            raise ContractError(
                f"endpoint fixture {agent_id}/{platform} is stale for its pinned CLI"
            )
        if (
            cell["dialect"] not in DIALECTS
            or cell["provenance"] != "pinned_real_cli_capture"
        ):
            raise ContractError(
                f"endpoint fixture {agent_id}/{platform} lacks binary-observed provenance"
            )
        if not re.fullmatch(r"[0-9a-f]{64}", str(cell["executable_sha256"])):
            raise ContractError(
                f"endpoint fixture {agent_id}/{platform} lacks an executable digest"
            )
        request, response = cell["request"], cell["response"]
        request_keys = {
            "transport",
            "method",
            "path",
            "media_type",
            "framing",
            "request_shape",
        }
        response_keys = {"encoder", "framing", "tool_call", "acceptance"}
        if (
            not isinstance(request, dict)
            or set(request) != request_keys
            or not isinstance(response, dict)
            or set(response) != response_keys
        ):
            raise ContractError(
                f"endpoint fixture {agent_id}/{platform} has invalid request/response receipts"
            )
        if (
            request["transport"] not in {"http1", "http2"}
            or request["method"] != "POST"
            or not str(request["path"]).startswith("/")
        ):
            raise ContractError(
                f"endpoint fixture {agent_id}/{platform} has an invalid observed endpoint"
            )
        if request["framing"] not in {
            "json",
            "sse",
            "connect_envelope",
        } or not isinstance(request["request_shape"], dict):
            raise ContractError(
                f"endpoint fixture {agent_id}/{platform} has an invalid redacted request shape"
            )
        if cell["dialect"] == "cursor_connect_proto" and (
            request["transport"],
            request["framing"],
        ) != ("http2", "connect_envelope"):
            raise ContractError(
                f"endpoint fixture {agent_id}/{platform} has invalid Cursor wire provenance"
            )
        if cell["dialect"] == "devin_connect_proto" and (
            request["transport"],
            request["framing"],
        ) != ("http1", "connect_envelope"):
            raise ContractError(
                f"endpoint fixture {agent_id}/{platform} has invalid Devin wire provenance"
            )
        if (
            cell["dialect"] in {"cursor_connect_proto", "devin_connect_proto"}
            and request["media_type"] != "application/connect+proto"
        ):
            raise ContractError(
                f"endpoint fixture {agent_id}/{platform} has invalid Connect wire provenance"
            )
        if (
            cell["dialect"] not in {"cursor_connect_proto", "devin_connect_proto"}
            and request["media_type"] != "application/json"
        ):
            raise ContractError(
                f"endpoint fixture {agent_id}/{platform} has invalid JSON wire provenance"
            )
        if response != {
            "encoder": cell["dialect"],
            "framing": request["framing"],
            "tool_call": "native_shell",
            "acceptance": "nonce_tool_result_round_trip",
        }:
            raise ContractError(
                f"endpoint fixture {agent_id}/{platform} lacks accepted response provenance"
            )
        core = {key: value for key, value in cell.items() if key != "capture_sha256"}
        digest = hashlib.sha256(
            json.dumps(core, sort_keys=True, separators=(",", ":")).encode()
        ).hexdigest()
        if cell["capture_sha256"] != digest:
            raise ContractError(
                f"endpoint fixture {agent_id}/{platform} has a fabricated capture digest"
            )
    if value["schema_version"] in {3, 4, 5}:
        from pitot_capture_source import validate_source_index

        try:
            validate_source_index(
                value["source"],
                cells,
                manifest,
                producer_binding=value["schema_version"] in {4, 5},
            )
        except (ValueError, KeyError, TypeError) as error:
            raise ContractError(f"invalid endpoint capture source: {error}") from error


def built_in_inventory(root: Path = ROOT) -> dict[str, list[str]]:
    completed = subprocess.run(
        ["go", "run", str(root / INVENTORY_HELPER)],
        cwd=root,
        check=True,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    value = json.loads(completed.stdout)
    if not isinstance(value, dict) or set(value) != {"adapters", "capabilities"}:
        raise ContractError("Go runtime inventory returned an invalid result")
    for name in ("adapters", "capabilities"):
        if not isinstance(value[name], list) or not all(
            isinstance(item, str) for item in value[name]
        ):
            raise ContractError(f"Go {name} inventory returned an invalid result")
    return value


def built_in_adapters(root: Path = ROOT) -> list[str]:
    return built_in_inventory(root)["adapters"]


def built_in_capabilities(root: Path = ROOT) -> list[str]:
    return built_in_inventory(root)["capabilities"]


def matrix(manifest: dict[str, object]) -> list[dict[str, str]]:
    return [
        {
            "agent": agent["id"],
            "platform": platform["id"],
            "runner": platform["runner"],
            "version": agent["version"],
            "endpoint_fixture": f"tests/endpoint-provenance.json#{agent['id']}/{platform['id']}",
            "runtime": agent["runtime"][platform["id"]],
            "installer": agent["installer"]["kind"],
            "capture": "false",
        }
        for agent in manifest["agents"]
        for platform in manifest["platforms"]
    ]


def runtime_matrix(manifest: dict[str, object]) -> list[dict[str, str]]:
    return [
        {
            "capability": "explicit_request",
            "platform": platform["id"],
            "runner": platform["runner"],
        }
        for platform in manifest["platforms"]
    ]


def readme_block(manifest: dict[str, object]) -> str:
    labels = " · ".join(agent["label"] for agent in manifest["agents"])
    return "\n".join(
        [
            README_START,
            '<p align="center">',
            '  <a href="https://github.com/operatorstack/pitot/actions/workflows/pitot-e2e.yml"><img alt="Pitot agent E2E" src="https://github.com/operatorstack/pitot/actions/workflows/pitot-e2e.yml/badge.svg?branch=main"></a>',
            "</p>",
            "",
            '<p align="center"><sub>Every supervised adapter must pass a binary-observed prompt → real action boundary → projected Consumer → Controller allow/deny → tool-result loop on Ubuntu, macOS, and Windows.</sub></p>',
            "",
            f'<p align="center"><sub>Supervised adapters: {labels}</sub></p>',
            "",
            '<p align="center"><sub>Supervised runtime capabilities: action control · Consumer delivery · explicit request</sub></p>',
            README_END,
        ]
    )


def replace_readme_block(text: str, manifest: dict[str, object]) -> str:
    pattern = re.compile(
        re.escape(README_START) + r".*?" + re.escape(README_END), re.DOTALL
    )
    if len(pattern.findall(text)) != 1:
        raise ContractError("README must contain exactly one supervisor marker block")
    return pattern.sub(readme_block(manifest), text)


def unified_workflow() -> str:
    return """# Generated by pitot_adapter_supervisor.py. Do not edit directly.
name: Pitot E2E

on:
  pull_request:
    paths: &pitot_paths
      - "**"
      - ".github/workflows/pitot-e2e*.yml"
      - ".github/scripts/pitot_e2e_report.py"
  push:
    branches: [main]
    paths: *pitot_paths
  workflow_dispatch:
    inputs:
      capture_provenance:
        description: Capture redacted binary-observed candidates instead of trusting committed fixtures
        required: false
        type: boolean
        default: false

permissions:
  actions: read
  contents: read
  pull-requests: write

jobs:
  inventory:
    runs-on: ubuntu-latest
    outputs:
      agent_matrix: ${{ steps.supervisor.outputs.agent_matrix }}
      runtime_matrix: ${{ steps.supervisor.outputs.runtime_matrix }}
    steps:
      - uses: actions/checkout@v7
      - uses: actions/setup-go@v7
        with:
          go-version-file: go.mod
          cache-dependency-path: go.mod
      - id: supervisor
        shell: bash
        run: |
          operation="matrix"
          runtime_operation="runtime-matrix"
          if [[ "${{ github.event_name }}" == "workflow_dispatch" && "${{ inputs.capture_provenance }}" == "true" ]]; then
            operation="capture"
            runtime_operation="capture-runtime-matrix"
          fi
          agent_matrix="$(python3 scripts/pitot_adapter_supervisor.py "$operation")"
          runtime_matrix="$(python3 scripts/pitot_adapter_supervisor.py "$runtime_operation")"
          echo "agent_matrix=$agent_matrix" >> "$GITHUB_OUTPUT"
          echo "runtime_matrix=$runtime_matrix" >> "$GITHUB_OUTPUT"
      - name: Upload supervised adapter inventory
        uses: actions/upload-artifact@v4
        with:
          name: pitot-e2e-inventory
          path: adapter-verification.json
          if-no-files-found: error
          retention-days: 14

  verify-agents:
    needs: inventory
    strategy:
      fail-fast: false
      matrix: ${{ fromJSON(needs.inventory.outputs.agent_matrix) }}
    uses: ./.github/workflows/pitot-e2e-agent.yml
    with:
      agent: ${{ matrix.agent }}
      platform: ${{ matrix.platform }}
      runner: ${{ matrix.runner }}
      capture: ${{ matrix.capture == 'true' }}

  verify-runtime:
    needs: inventory
    strategy:
      fail-fast: false
      matrix: ${{ fromJSON(needs.inventory.outputs.runtime_matrix) }}
    runs-on: ${{ matrix.runner }}
    steps:
      - uses: actions/checkout@v7
      - uses: actions/setup-go@v7
        with:
          go-version-file: go.mod
          cache-dependency-path: go.mod
      - uses: actions/setup-python@v6
        with:
          python-version: "3.11"
      - name: Resolve Bash runtime
        shell: bash
        run: |
          bash_path="$(command -v bash)"
          if [[ "${{ runner.os }}" == "Windows" ]]; then
            bash_path="$(cygpath -m "$bash_path")"
          fi
          echo "PITOT_BASH=$bash_path" >> "$GITHUB_ENV"
      - name: Run explicit request runtime E2E
        id: e2e
        continue-on-error: true
        timeout-minutes: 5
        shell: bash
        env:
          PITOT_SOURCE_SHA: ${{ github.event.pull_request.head.sha || github.sha }}
          PITOT_E2E_PLATFORM: ${{ matrix.platform }}
          PITOT_E2E_EVIDENCE: ${{ runner.temp }}/pitot-e2e/runtime-evidence.json
        run: >-
          python tests/run_e2e_report.py
          --capability explicit_request
          --platform "${{ matrix.platform }}"
          --output "${{ runner.temp }}/pitot-e2e/runtime-result.json"
          --evidence "${{ runner.temp }}/pitot-e2e/runtime-evidence.json"
          -- "$PITOT_BASH" tests/e2e_runtime_cli_test.sh
          < /dev/null
      - name: Upload structured runtime result
        if: always()
        uses: actions/upload-artifact@v4
        with:
          name: pitot-e2e-runtime-${{ matrix.platform }}
          path: ${{ runner.temp }}/pitot-e2e/runtime-result.json
          if-no-files-found: error
          retention-days: 14
      - name: Enforce runtime E2E result
        if: always() && steps.e2e.outcome != 'success'
        shell: bash
        run: exit 1

  report:
    if: ${{ always() && github.event_name == 'pull_request' && github.event.pull_request.head.repo.full_name == github.repository }}
    needs: [inventory, verify-agents, verify-runtime]
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
      - name: Update sticky PR report
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
        run: >-
          python3 .github/scripts/pitot_e2e_report.py
          --event "$GITHUB_EVENT_PATH"
          --repository "$GITHUB_REPOSITORY"
"""


def contract_errors(root: Path, adapters: list[str] | None = None) -> list[str]:
    try:
        manifest = load_manifest(root)
    except ContractError as error:
        return [str(error)]
    errors: list[str] = []
    go_inventory = None if adapters is not None else built_in_inventory(root)
    inventory = adapters if adapters is not None else go_inventory["adapters"]
    declared = [agent["id"] for agent in manifest["agents"]]
    missing = sorted(set(inventory) - set(declared))
    extra = sorted(set(declared) - set(inventory))
    if missing:
        errors.append(f"built-in adapters missing from manifest: {', '.join(missing)}")
    if extra:
        errors.append(f"manifest agents are not built-in adapters: {', '.join(extra)}")
    declared_capabilities = [item["id"] for item in manifest["capabilities"]]
    capability_inventory = (
        declared_capabilities if adapters is not None else go_inventory["capabilities"]
    )
    if declared_capabilities != capability_inventory:
        errors.append(
            "manifest capabilities do not exactly match the Go runtime inventory"
        )
    for agent_id in declared:
        script = root / f"tests/e2e_{agent_id}_cli_test.sh"
        if not script.is_file():
            errors.append(f"adapter {agent_id} is missing {script.relative_to(root)}")
    for agent in manifest["agents"]:
        for artifact in agent["artifacts"]:
            path = root / artifact
            if not path.is_file():
                errors.append(
                    f"adapter {agent['id']} is missing supervised artifact {artifact}"
                )
    driver = root / "tests/real_agent_driver.py"
    if not driver.is_file():
        errors.append("missing canonical real-agent prompt driver")
    elif any(
        fragment not in driver.read_text(encoding="utf-8")
        for fragment in (
            "consumer_observed",
            "controller_deny_observed",
            "deny_canary_absent",
            "final_output_observed",
        )
    ):
        errors.append(
            "real-agent driver does not require the complete runtime capability receipts"
        )
    runtime_driver = root / "tests/runtime_capability_driver.py"
    runtime_script = root / "tests/e2e_runtime_cli_test.sh"
    if not runtime_driver.is_file() or not runtime_script.is_file():
        errors.append(
            "explicit request capability is missing its canonical runtime driver"
        )
    proxy = root / "tests/model_control_proxy.py"
    if not proxy.is_file():
        errors.append("missing local model-control proxy")
    elif 'add_argument("--protocol"' in proxy.read_text(encoding="utf-8"):
        errors.append("model proxy permits a declared protocol authority")
    cursor_proxy = root / "tests/cursor_control_proxy.mjs"
    if not cursor_proxy.is_file():
        errors.append("missing pinned Cursor endpoint proxy")
    devin_proxy = root / "tests/devin_control_proxy.py"
    if not devin_proxy.is_file():
        errors.append("missing pinned Devin endpoint proxy")
    reporter_path = root / ".github/scripts/pitot_e2e_report.py"
    reporter_text = (
        reporter_path.read_text(encoding="utf-8") if reporter_path.is_file() else ""
    )
    if (
        "artifacts?per_page=100" not in reporter_text
        or "total != len(artifacts)" not in reporter_text
    ):
        errors.append(
            "reporter does not fail closed over the complete supervised artifact page"
        )
    runner = root / "tests/e2e_unified_runner.sh"
    runner_text = runner.read_text(encoding="utf-8") if runner.is_file() else ""
    forbidden = (
        "hook_subprocess",
        "Falling back",
        "active subprocess hook verification",
    )
    if any(fragment in runner_text for fragment in forbidden):
        errors.append("unified runner permits synthetic E2E success")
    if "real_agent_driver.py" not in runner_text or "--test-role" not in runner_text:
        errors.append("unified runner does not invoke the canonical real-agent driver")
    native_runner_fragments = (
        'go build -o "$PITOT_BINARY" "$PITOT_MAIN"',
        'go build -o "$WITNESS_BINARY" "$SCRIPT_DIR/witness/main.go"',
        'go build -o "$TESTROLE_BINARY" "$LAB_DIR/internal/testrole"',
        'if [[ "${RUNNER_OS:-}" == "Windows" ]]; then',
        'PITOT_BINARY="${PITOT_BINARY}.exe"',
        'WITNESS_BINARY="${WITNESS_BINARY}.exe"',
        'TESTROLE_BINARY="${TESTROLE_BINARY}.exe"',
        'DRIVER=(python3 "$SCRIPT_DIR/real_agent_driver.py" "${DRIVER_ARGS[@]}")',
        '--pitot "$PITOT_BINARY"',
        '--witness "$WITNESS_BINARY"',
        '--test-role "$TESTROLE_BINARY"',
        "set -euo pipefail",
        "--response-fault text",
        "--expect-incompatible-response",
    )
    if (
        any(fragment not in runner_text for fragment in native_runner_fragments)
        or runner_text.count("go build -o") != 3
        or runner_text.count('"${DRIVER[@]}"') != 2
        or any(fragment in runner_text for fragment in ("wsl.exe", "GOOS=", "GOARCH="))
    ):
        errors.append("unified runner does not preserve native host verification")
    expected_files = {
        UNIFIED_WORKFLOW: unified_workflow(),
    }
    for relative, expected in expected_files.items():
        path = root / relative
        actual = path.read_text(encoding="utf-8") if path.is_file() else ""
        if actual != expected:
            errors.append(f"generated surface drifted: {relative}")
    legacy_report = root / ".github/workflows/pitot-e2e-report.yml"
    if legacy_report.exists():
        errors.append("legacy Pitot report workflow escaped the unified supervisor")
    reusable_path = root / REUSABLE_WORKFLOW
    reusable = (
        reusable_path.read_text(encoding="utf-8") if reusable_path.is_file() else ""
    )
    required_reusable_contract = (
        "platform:\n        description: Canonical Pitot platform identifier",
        "runner:\n        description: GitHub-hosted runner selected by the supervisor",
        "capture:\n        description: Emit a redacted binary-observed candidate fixture",
        "runs-on: ${{ inputs.runner }}",
        '--agent "${{ inputs.agent }}"',
        '--platform "${{ inputs.platform }}"',
        '--evidence "${{ runner.temp }}/pitot-e2e/evidence.json"',
        "install_real_agent.py",
        "PITOT_CAPTURE_OUTPUT:",
        "name: Verify Cursor native Windows shell",
        "shell: pwsh",
        "$PSVersionTable.PSVersion.Major -lt 7",
        "pitot-endpoint-capture-${{ inputs.agent }}-${{ inputs.platform }}",
        "name: pitot-e2e-${{ inputs.agent }}-${{ inputs.platform }}",
    )
    if (
        any(fragment not in reusable for fragment in required_reusable_contract)
        or "matrix.platform" in reusable
    ):
        errors.append(
            f"reusable workflow escaped supervisor inputs: {REUSABLE_WORKFLOW}"
        )
    readme_path = root / README
    if readme_path.is_file():
        actual = readme_path.read_text(encoding="utf-8")
        try:
            expected = replace_readme_block(actual, manifest)
            if actual != expected:
                errors.append(f"generated surface drifted: {README}")
        except ContractError as error:
            errors.append(str(error))
    else:
        errors.append(f"missing {README}")
    return errors


def render(root: Path = ROOT) -> None:
    manifest = load_manifest(root)
    readme_path = root / README
    readme_path.write_text(
        replace_readme_block(readme_path.read_text(encoding="utf-8"), manifest),
        encoding="utf-8",
    )
    (root / UNIFIED_WORKFLOW).write_text(unified_workflow(), encoding="utf-8")


def merge_captures(root: Path, captures: Path, output: Path) -> None:
    raise ContractError(
        "local capture files cannot authenticate their source; use --repository, --run-id and --source-sha"
    )


def merge_ci_captures(
    root: Path, output: Path, repository: str, run_id: int, source_sha: str, client=None
) -> None:
    from pitot_capture_source import authenticated_client, atomic_json, collect

    try:
        manifest = load_manifest_without_provenance(root)
        client = client if client is not None else authenticated_client(repository)
        value = collect(client, root, manifest, repository, run_id, source_sha)
        atomic_json(output, value)
    except (
        ValueError,
        KeyError,
        TypeError,
        AttributeError,
        OSError,
        subprocess.SubprocessError,
    ) as error:
        raise ContractError(f"capture source acquisition failed: {error}") from error


def load_manifest_without_provenance(root: Path) -> dict[str, object]:
    value = json.loads((root / MANIFEST).read_text(encoding="utf-8"))
    validate_manifest(value)
    return value


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "operation",
        choices=(
            "check",
            "matrix",
            "runtime-matrix",
            "render",
            "capture",
            "capture-runtime-matrix",
            "capture-merge",
        ),
    )
    parser.add_argument("--repo", type=Path, default=ROOT)
    parser.add_argument("--captures", type=Path)
    parser.add_argument("--repository")
    parser.add_argument("--run-id", type=int)
    parser.add_argument("--source-sha")
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    root = args.repo.resolve()
    try:
        if args.operation == "render":
            render(root)
            print("PASS: rendered Pitot adapter verification surfaces")
            return 0
        if args.operation == "capture-merge":
            if args.captures is not None:
                raise ContractError(
                    "local capture files cannot authenticate their source"
                )
            if (
                args.output is None
                or not args.repository
                or args.run_id is None
                or not args.source_sha
            ):
                raise ContractError(
                    "capture-merge requires --repository, --run-id, --source-sha and --output"
                )
            merge_ci_captures(
                root,
                args.output.resolve(),
                args.repository,
                args.run_id,
                args.source_sha,
            )
            manifest = load_manifest_without_provenance(root)
            print(
                f"PASS: merged {len(matrix(manifest))} binary-observed endpoint captures "
                f"into {args.output}"
            )
            return 0
        if args.operation in {"capture", "capture-runtime-matrix"}:
            manifest = load_manifest_without_provenance(root)
            values = (
                runtime_matrix(manifest)
                if args.operation == "capture-runtime-matrix"
                else [{**item, "capture": "true"} for item in matrix(manifest)]
            )
            print(json.dumps({"include": values}, separators=(",", ":")))
            return 0
        errors = contract_errors(root)
        if errors:
            raise ContractError("; ".join(errors))
        if args.operation == "matrix":
            manifest = load_manifest(root)
            values = matrix(manifest)
            print(json.dumps({"include": values}, separators=(",", ":")))
        elif args.operation == "runtime-matrix":
            manifest = load_manifest(root)
            print(
                json.dumps({"include": runtime_matrix(manifest)}, separators=(",", ":"))
            )
        else:
            print(
                f"PASS: supervised {len(load_manifest(root)['agents'])} Pitot adapters"
            )
        return 0
    except (ContractError, subprocess.CalledProcessError) as error:
        print(f"BLOCKED: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
