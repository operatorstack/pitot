#!/usr/bin/env python3
"""Prove the real Pitot request CLI against its authenticated runtime."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import secrets
import subprocess
import tempfile
import time


def lines(path: Path) -> list[dict[str, object]]:
    return [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines() if line.strip()]


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--pitot", type=Path, required=True)
    parser.add_argument("--test-role", type=Path, required=True)
    parser.add_argument("--platform", required=True)
    parser.add_argument("--evidence", type=Path, required=True)
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix="pitot-request-e2e-") as temporary:
        nonce = secrets.token_hex(16)
        base = Path(temporary)
        runtime_path = base / "runtime.json"
        receipt = base / "controller.jsonl"
        config = base / "pitot.json"
        config.write_text(json.dumps({"controllers": {"release.approval": {
            "id": "e2e-release-controller",
            "command": [str(args.test_role.resolve()), "--role", "controller", "--id", "e2e-release-controller", "--receipt", str(receipt), "--nonce", nonce],
            "deadline_ms": 2000,
            "on_timeout": "deny",
            "on_unavailable": "deny",
        }}}) + "\n", encoding="utf-8")
        process = subprocess.Popen(
            [str(args.pitot.resolve()), "run", "--config", str(config), "--runtime", str(runtime_path)],
            stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True,
        )
        try:
            for _ in range(300):
                if runtime_path.is_file(): break
                if process.poll() is not None:
                    raise RuntimeError(f"runtime exited: {process.stdout.read() if process.stdout else ''}")
                time.sleep(0.02)
            else:
                raise RuntimeError("runtime descriptor was not published")
            runtime = json.loads(runtime_path.read_text(encoding="utf-8"))
            allow = subprocess.run(
                [str(args.pitot.resolve()), "request", "release.approval", "--data", json.dumps({"phase": "PITOT_ALLOW", "nonce": nonce}), "--runtime", str(runtime_path)],
                text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
            )
            deny = subprocess.run(
                [str(args.pitot.resolve()), "request", "release.approval", "--data", json.dumps({"phase": "PITOT_DENY", "nonce": nonce}), "--runtime", str(runtime_path)],
                text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
            )
            allow_value = json.loads(allow.stdout)
            deny_value = json.loads(deny.stdout)
            controller = lines(receipt)
            requests = [item["value"] for item in controller if item.get("receipt_type") == "request"]
            responses = [item["value"] for item in controller if item.get("receipt_type") == "response"]
            if allow.returncode != 0 or deny.returncode != 2:
                raise RuntimeError(f"request exits did not encode allow/deny: {allow.returncode}, {deny.returncode}")
            if [allow_value.get("outcome"), deny_value.get("outcome")] != ["allow", "deny"]:
                raise RuntimeError("request output did not carry Controller outcomes")
            action_ids = [item.get("action_id") for item in requests]
            if len(requests) != 2 or len(responses) != 2 or [item.get("action_id") for item in responses] != action_ids:
                raise RuntimeError("request and response correlation receipts disagree")
            if any(nonce not in json.dumps(item.get("data"), sort_keys=True) for item in requests):
                raise RuntimeError("request receipts are not bound to the session nonce")
            public_runtime = {key: runtime[key] for key in ("schema_version", "instance_id", "pid", "endpoint", "config_sha256")}
            evidence = {
                "schema_version": 1,
                "capability": "explicit_request",
                "platform": args.platform,
                "nonce": nonce,
                "runtime": public_runtime,
                "controller": {"id": "e2e-release-controller", "action_ids": action_ids, "outcomes": ["allow", "deny"]},
                "receipts": {"request_allow_observed": True, "request_deny_observed": True, "correlation_observed": True},
                "commit_sha": os.environ.get("PITOT_SOURCE_SHA", os.environ.get("GITHUB_SHA", "local")),
            }
            args.evidence.parent.mkdir(parents=True, exist_ok=True)
            args.evidence.write_text(json.dumps(evidence, indent=2, sort_keys=True) + "\n", encoding="utf-8")
            print("PITOT_RUNTIME_E2E_RESULT capability=explicit_request evidence=nonce-correlated")
        finally:
            process.terminate()
            try: process.wait(timeout=5)
            except subprocess.TimeoutExpired: process.kill()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
