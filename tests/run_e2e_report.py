#!/usr/bin/env python3
"""Run one Pitot host E2E script and emit a strict result artifact."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import sys


LAB = Path(__file__).resolve().parent.parent
ROOT = LAB.parents[1] if LAB.name == "15-pitot" else LAB
MANIFEST = json.loads((LAB / "adapter-verification.json").read_text(encoding="utf-8"))
AGENTS = {agent["id"] for agent in MANIFEST["agents"]}
AGENT_RECORDS = {agent["id"]: agent for agent in MANIFEST["agents"]}
PLATFORMS = {platform["id"] for platform in MANIFEST["platforms"]}
RESULT_PATTERN = re.compile(r"^PITOT_E2E_RESULT mode=real_cli evidence=nonce-correlated$", re.MULTILINE)
RUNTIME_RESULT_PATTERN = re.compile(r"^PITOT_RUNTIME_E2E_RESULT capability=explicit_request evidence=nonce-correlated$", re.MULTILINE)


def load_evidence(path: Path | None, *, agent: str) -> dict[str, object] | None:
    if path is None or not path.is_file():
        return None
    value = json.loads(path.read_text(encoding="utf-8"))
    required = {"schema_version", "agent", "cli", "prompt_hash", "protocol", "endpoint", "nonce", "receipts", "runtime", "boundaries", "controller", "consumer", "canary"}
    if not isinstance(value, dict) or set(value) != required or value["schema_version"] != 2 or value["agent"] != agent:
        return None
    receipts = value.get("receipts")
    receipt_fields = {
        "initial_prompt_observed", "allow_tool_call_response_emitted", "allow_tool_result_observed",
        "deny_tool_call_response_emitted", "denied_result_observed", "final_response_emitted",
        "consumer_observed", "controller_allow_observed", "controller_deny_observed",
        "deny_canary_absent", "final_output_observed", "cli_exit_zero",
    }
    if not isinstance(receipts, dict) or set(receipts) != receipt_fields or not all(item is True for item in receipts.values()):
        return None
    if not re.fullmatch(r"[0-9a-f]{64}", str(value.get("prompt_hash", ""))):
        return None
    nonce = value.get("nonce")
    if not isinstance(nonce, str) or not re.fullmatch(r"[0-9a-f]{32}", nonce):
        return None
    boundaries = value.get("boundaries")
    expected_transport = "acp" if AGENT_RECORDS[agent]["integration"] == "acp_client" else "hook"
    if not isinstance(boundaries, list) or len(boundaries) != 2 or [item.get("decision") for item in boundaries] != ["allow", "deny"] or any(item.get("action_kind") != "shell" or item.get("host") != agent or item.get("nonce") != nonce or item.get("transport") != expected_transport for item in boundaries):
        return None
    endpoint = value.get("endpoint", {})
    endpoint_required = {"fixture", "fixture_sha256", "provenance", "dialect", "request", "response", "executable_sha256"}
    if not isinstance(endpoint, dict) or set(endpoint) != endpoint_required:
        return None
    if endpoint.get("provenance") != "pinned_real_cli_capture" or not re.fullmatch(r"[0-9a-f]{64}", str(endpoint.get("fixture_sha256", ""))):
        return None
    return value


def load_runtime_evidence(path: Path | None, *, platform: str) -> dict[str, object] | None:
    if path is None or not path.is_file():
        return None
    value = json.loads(path.read_text(encoding="utf-8"))
    required = {"schema_version", "capability", "platform", "nonce", "runtime", "controller", "receipts", "commit_sha"}
    if not isinstance(value, dict) or set(value) != required or value["schema_version"] != 1 or value["capability"] != "explicit_request" or value["platform"] != platform:
        return None
    if not isinstance(value["nonce"], str) or not re.fullmatch(r"[0-9a-f]{32}", value["nonce"]):
        return None
    receipts = value.get("receipts")
    if not isinstance(receipts, dict) or set(receipts) != {"request_allow_observed", "request_deny_observed", "correlation_observed"} or not all(item is True for item in receipts.values()):
        return None
    controller = value.get("controller")
    if not isinstance(controller, dict) or controller.get("outcomes") != ["allow", "deny"] or len(controller.get("action_ids", [])) != 2:
        return None
    return value


def result_for(agent: str, platform: str, returncode: int, output: str, evidence_path: Path | None = None) -> dict[str, object]:
    if agent not in AGENTS:
        raise ValueError(f"unsupported agent: {agent}")
    if platform not in PLATFORMS:
        raise ValueError(f"unsupported platform: {platform}")

    markers = RESULT_PATTERN.findall(output)
    receipt = load_evidence(evidence_path, agent=agent)
    passed = returncode == 0 and len(markers) == 1 and receipt is not None
    evidence = "binary-observed request, real action control, projected Consumer, allow/deny canary, and final receipts" if passed else "real-agent control evidence contract failed"

    return {
        "schema_version": 2,
        "agent": agent,
        "platform": platform,
        "status": "pass" if passed else "fail",
        "verification_mode": "real_cli" if passed else None,
        "evidence": evidence,
        "cli": receipt["cli"] if passed else None,
        "protocol": receipt["protocol"] if passed else None,
        "endpoint": receipt["endpoint"] if passed else None,
        "prompt_hash": receipt["prompt_hash"] if passed else None,
        "nonce": receipt["nonce"] if passed else None,
        "receipts": receipt["receipts"] if passed else None,
        "runtime": receipt["runtime"] if passed else None,
        "boundaries": receipt["boundaries"] if passed else None,
        "controller": receipt["controller"] if passed else None,
        "consumer": receipt["consumer"] if passed else None,
        "canary": receipt["canary"] if passed else None,
        "commit_sha": os.environ.get("PITOT_SOURCE_SHA", os.environ.get("GITHUB_SHA", "local")),
        "run_url": (
            f"{os.environ['GITHUB_SERVER_URL']}/{os.environ['GITHUB_REPOSITORY']}"
            f"/actions/runs/{os.environ['GITHUB_RUN_ID']}"
            if all(
                key in os.environ
                for key in ("GITHUB_SERVER_URL", "GITHUB_REPOSITORY", "GITHUB_RUN_ID")
            )
            else "local"
        ),
    }


def result_for_runtime(platform: str, returncode: int, output: str, evidence_path: Path | None = None) -> dict[str, object]:
    if platform not in PLATFORMS:
        raise ValueError(f"unsupported platform: {platform}")
    markers = RUNTIME_RESULT_PATTERN.findall(output)
    receipt = load_runtime_evidence(evidence_path, platform=platform)
    passed = returncode == 0 and len(markers) == 1 and receipt is not None
    return {
        "schema_version": 2,
        "capability": "explicit_request",
        "platform": platform,
        "status": "pass" if passed else "fail",
        "verification_mode": "real_runtime" if passed else None,
        "evidence": "real request CLI, authenticated runtime, and correlated allow/deny Controller receipts" if passed else "explicit request evidence contract failed",
        "nonce": receipt["nonce"] if passed else None,
        "runtime": receipt["runtime"] if passed else None,
        "controller": receipt["controller"] if passed else None,
        "receipts": receipt["receipts"] if passed else None,
        "commit_sha": os.environ.get("PITOT_SOURCE_SHA", os.environ.get("GITHUB_SHA", "local")),
        "run_url": (
            f"{os.environ['GITHUB_SERVER_URL']}/{os.environ['GITHUB_REPOSITORY']}"
            f"/actions/runs/{os.environ['GITHUB_RUN_ID']}"
            if all(key in os.environ for key in ("GITHUB_SERVER_URL", "GITHUB_REPOSITORY", "GITHUB_RUN_ID"))
            else "local"
        ),
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    target = parser.add_mutually_exclusive_group(required=True)
    target.add_argument("--agent", choices=sorted(AGENTS))
    target.add_argument("--capability", choices=("explicit_request",))
    parser.add_argument("--platform", required=True, choices=sorted(PLATFORMS))
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--evidence", required=True, type=Path)
    parser.add_argument("command", nargs=argparse.REMAINDER)
    args = parser.parse_args()
    command = args.command[1:] if args.command[:1] == ["--"] else args.command
    if not command:
        parser.error("a command is required after --")

    completed = subprocess.run(
        command,
        text=True,
        encoding="utf-8",
        errors="replace",
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
    )
    sys.stdout.buffer.write(completed.stdout.encode("utf-8", errors="replace"))
    result = (
        result_for(args.agent, args.platform, completed.returncode, completed.stdout, args.evidence)
        if args.agent else result_for_runtime(args.platform, completed.returncode, completed.stdout, args.evidence)
    )
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(result, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    return 0 if result["status"] == "pass" else 1


if __name__ == "__main__":
    raise SystemExit(main())
