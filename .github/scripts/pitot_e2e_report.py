#!/usr/bin/env python3
"""Publish a trusted, sticky PR summary for Pitot's unified E2E workflow."""

from __future__ import annotations

import argparse
import io
import json
import os
from pathlib import Path
import re
import sys
import urllib.parse
import urllib.request
import zipfile


MARKER = "<!-- pitot-e2e-report -->"
ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))
from pitot_adapter_supervisor import ContractError, validate_manifest


MANIFEST = json.loads((ROOT / "adapter-verification.json").read_text(encoding="utf-8"))
ENDPOINT_PROVENANCE = json.loads((ROOT / "tests/endpoint-provenance.json").read_text(encoding="utf-8"))
AGENTS = tuple(agent["id"] for agent in MANIFEST["agents"])
AGENT_LABELS = {agent["id"]: agent["label"] for agent in MANIFEST["agents"]}
AGENT_RECORDS = {agent["id"]: agent for agent in MANIFEST["agents"]}
PLATFORMS = tuple(platform["id"] for platform in MANIFEST["platforms"])
MAX_ARTIFACT_BYTES = 1_000_000


class ArtifactRedirectHandler(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, file_pointer, code, message, headers, new_url):
        redirected = super().redirect_request(request, file_pointer, code, message, headers, new_url)
        if redirected is None:
            return None
        source = urllib.parse.urlparse(request.full_url)
        target = urllib.parse.urlparse(new_url)
        if target.scheme != "https":
            raise ValueError("artifact redirect must use HTTPS")
        if source.netloc != target.netloc:
            redirected.remove_header("Authorization")
        return redirected


def validate_result(
    value: object,
    *,
    agent: str,
    platform: str,
    expected_sha: str | None = None,
    expected_run_url: str | None = None,
) -> dict[str, object]:
    if not isinstance(value, dict):
        raise ValueError("result must be an object")
    required = {
        "schema_version",
        "agent",
        "platform",
        "status",
        "verification_mode",
        "evidence",
        "cli",
        "protocol",
        "endpoint",
        "prompt_hash",
        "nonce",
        "receipts",
        "runtime",
        "boundaries",
        "controller",
        "consumer",
        "canary",
        "commit_sha",
        "run_url",
    }
    if set(value) != required:
        raise ValueError("result fields do not match schema")
    if value["schema_version"] != 2 or value["agent"] != agent or value["platform"] != platform:
        raise ValueError("result identity does not match artifact")
    if value["status"] not in {"pass", "fail"}:
        raise ValueError("invalid result status")
    mode = value["verification_mode"]
    if value["status"] == "pass" and mode != "real_cli":
        raise ValueError("passing result requires real_cli verification")
    if value["status"] == "fail" and mode is not None:
        raise ValueError("failed result cannot claim a verification mode")
    allowed_evidence = {"binary-observed request, real action control, projected Consumer, allow/deny canary, and final receipts", "real-agent control evidence contract failed"}
    if value["evidence"] not in allowed_evidence:
        raise ValueError("invalid evidence summary")
    evidence_fields = ("cli", "protocol", "endpoint", "prompt_hash", "nonce", "receipts", "runtime", "boundaries", "controller", "consumer", "canary")
    if value["status"] == "fail":
        if any(value[field] is not None for field in evidence_fields):
            raise ValueError("failed result cannot carry passing evidence")
    else:
        record = AGENT_RECORDS[agent]
        cli = value["cli"]
        if not isinstance(cli, dict) or set(cli) != {"version", "executable", "executable_sha256", "installer", "runtime"}:
            raise ValueError("invalid CLI installation receipt")
        if cli["version"] != record["version"] or cli["installer"] != record["installer"]["kind"] or cli["runtime"] != record["runtime"][platform]:
            raise ValueError("CLI receipt does not match supervised manifest")
        cells = [cell for cell in ENDPOINT_PROVENANCE["cells"] if cell["agent"] == agent and cell["platform"] == platform]
        if len(cells) != 1:
            raise ValueError("missing supervised platform provenance")
        fixture = cells[0]
        if cli["executable_sha256"] != fixture["executable_sha256"]:
            raise ValueError("CLI executable digest does not match binary capture")
        if value["protocol"] != fixture["dialect"] or not re.fullmatch(r"[0-9a-f]{64}", str(value["prompt_hash"])):
            raise ValueError("invalid prompt/protocol receipt")
        nonce = value["nonce"]
        if not isinstance(nonce, str) or not re.fullmatch(r"[0-9a-f]{32}", nonce):
            raise ValueError("invalid session nonce")
        expected_endpoint = {
            "fixture": f"tests/endpoint-provenance.json#{agent}/{platform}",
            "fixture_sha256": fixture["capture_sha256"],
            "provenance": "pinned_real_cli_capture",
            "dialect": fixture["dialect"],
            "request": fixture["request"],
            "response": fixture["response"],
            "executable_sha256": fixture["executable_sha256"],
        }
        if value["endpoint"] != expected_endpoint:
            raise ValueError("endpoint receipt does not match pinned real-CLI provenance")
        receipt_fields = {
            "initial_prompt_observed", "allow_tool_call_response_emitted", "allow_tool_result_observed",
            "deny_tool_call_response_emitted", "denied_result_observed", "final_response_emitted",
            "consumer_observed", "controller_allow_observed", "controller_deny_observed",
            "deny_canary_absent", "final_output_observed", "cli_exit_zero",
        }
        if not isinstance(value["receipts"], dict) or set(value["receipts"]) != receipt_fields or not all(item is True for item in value["receipts"].values()):
            raise ValueError("incomplete causal receipts")
        boundaries = value["boundaries"]
        expected_transport = "acp" if record["integration"] == "acp_client" else "hook"
        if not isinstance(boundaries, list) or len(boundaries) != 2 or [item.get("decision") for item in boundaries] != ["allow", "deny"] or any(item.get("host") != agent or item.get("transport") != expected_transport or item.get("action_kind") != "shell" or item.get("nonce") != nonce for item in boundaries):
            raise ValueError("invalid Pitot allow/deny boundary receipts")
        action_ids = [item.get("action_id") for item in boundaries]
        if len(set(action_ids)) != 2 or value["controller"] != {"id": "e2e-shell-controller", "action_ids": action_ids, "outcomes": ["allow", "deny"]}:
            raise ValueError("invalid Controller receipts")
        if value["consumer"] != {"id": "e2e-audit", "action_ids": action_ids, "projection": "sha256"}:
            raise ValueError("invalid Consumer receipts")
        canary = value["canary"]
        if not isinstance(canary, dict) or canary.get("denied_executions") != 0 or canary.get("executions") != [f"PITOT_ALLOW {nonce}"]:
            raise ValueError("invalid canary execution receipts")
        runtime = value["runtime"]
        if not isinstance(runtime, dict) or set(runtime) != {"schema_version", "instance_id", "pid", "endpoint", "config_sha256"} or runtime["schema_version"] != 1:
            raise ValueError("invalid authenticated runtime receipt")
    if not isinstance(value["commit_sha"], str) or not re.fullmatch(r"[0-9a-f]{40}", value["commit_sha"]):
        raise ValueError("invalid commit_sha")
    if not isinstance(value["run_url"], str) or not re.fullmatch(
        r"https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+/actions/runs/[0-9]+",
        value["run_url"],
    ):
        raise ValueError("invalid run_url")
    if expected_sha is not None and value["commit_sha"] != expected_sha:
        raise ValueError("result commit does not match workflow run")
    if expected_run_url is not None and value["run_url"] != expected_run_url:
        raise ValueError("result URL does not match workflow run")
    return value


def validate_inventory(value: object) -> dict[str, object]:
    try:
        validate_manifest(value)
    except ContractError as error:
        raise ValueError(f"invalid supervised inventory: {error}") from error
    return value


def validate_runtime_result(
    value: object, *, platform: str, expected_sha: str | None = None, expected_run_url: str | None = None,
) -> dict[str, object]:
    required = {"schema_version", "capability", "platform", "status", "verification_mode", "evidence", "nonce", "runtime", "controller", "receipts", "commit_sha", "run_url"}
    if not isinstance(value, dict) or set(value) != required:
        raise ValueError("runtime result fields do not match schema")
    if value["schema_version"] != 2 or value["capability"] != "explicit_request" or value["platform"] != platform:
        raise ValueError("runtime result identity does not match artifact")
    if value["status"] not in {"pass", "fail"}:
        raise ValueError("invalid runtime result status")
    if value["status"] == "pass":
        if value["verification_mode"] != "real_runtime" or value["evidence"] != "real request CLI, authenticated runtime, and correlated allow/deny Controller receipts":
            raise ValueError("passing runtime result lacks real evidence")
        if not isinstance(value["nonce"], str) or not re.fullmatch(r"[0-9a-f]{32}", value["nonce"]):
            raise ValueError("invalid runtime session nonce")
        controller = value["controller"]
        if not isinstance(controller, dict) or controller.get("outcomes") != ["allow", "deny"] or not isinstance(controller.get("action_ids"), list) or len(controller["action_ids"]) != 2:
            raise ValueError("runtime Controller receipt is incomplete")
        if not isinstance(value["receipts"], dict) or not all(item is True for item in value["receipts"].values()):
            raise ValueError("runtime causal receipts are incomplete")
        runtime = value["runtime"]
        if not isinstance(runtime, dict) or set(runtime) != {"schema_version", "instance_id", "pid", "endpoint", "config_sha256"} or runtime["schema_version"] != 1:
            raise ValueError("runtime identity receipt is invalid")
    elif value["verification_mode"] is not None or any(value[field] is not None for field in ("nonce", "runtime", "controller", "receipts")):
        raise ValueError("failed runtime result cannot carry passing evidence")
    if not isinstance(value["commit_sha"], str) or not re.fullmatch(r"[0-9a-f]{40}", value["commit_sha"]):
        raise ValueError("invalid runtime commit_sha")
    if not isinstance(value["run_url"], str) or not re.fullmatch(r"https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+/actions/runs/[0-9]+", value["run_url"]):
        raise ValueError("invalid runtime run_url")
    if expected_sha is not None and value["commit_sha"] != expected_sha:
        raise ValueError("runtime result commit does not match workflow run")
    if expected_run_url is not None and value["run_url"] != expected_run_url:
        raise ValueError("runtime result URL does not match workflow run")
    return value


def aggregate(
    platform_results: dict[str, dict[str, object] | None],
    platforms: tuple[str, ...] = PLATFORMS,
) -> str:
    if any(platform not in platform_results or platform_results[platform] is None for platform in platforms):
        return "pending"
    return "passing" if all(platform_results[p]["status"] == "pass" for p in platforms) else "failing"


def result_cell(result: dict[str, object] | None) -> str:
    if result is None:
        return "⏳ Pending"
    if result["status"] == "fail":
        return "❌ Failed"
    cli = result["cli"]
    runtime = "WSL" if cli["runtime"] == "wsl" else "native"
    dialect = result["endpoint"]["dialect"].replace("_", " ")
    return f"✅ Pass · real CLI {cli['version']} · {runtime} · binary-observed {dialect}"


def failed_result(agent: str, platform: str, head_sha: str, run_url: str) -> dict[str, object]:
    return {
        "schema_version": 2,
        "agent": agent,
        "platform": platform,
        "status": "fail",
        "verification_mode": None,
        "evidence": "real-agent control evidence contract failed",
        "cli": None,
        "protocol": None,
        "endpoint": None,
        "prompt_hash": None,
        "nonce": None,
        "receipts": None,
        "runtime": None,
        "boundaries": None,
        "controller": None,
        "consumer": None,
        "canary": None,
        "commit_sha": head_sha,
        "run_url": run_url,
    }


def render_comment(
    results: dict[str, dict[str, dict[str, object] | None]],
    run_urls: dict[str, str],
    head_sha: str,
    agent_records: tuple[dict[str, str], ...] | None = None,
    platforms: tuple[str, ...] = PLATFORMS,
    runtime_results: dict[str, dict[str, object] | None] | None = None,
) -> str:
    lines = [
        MARKER,
        "## Pitot coding-agent E2E",
        "",
        "Intelligence Flow is the verification source; Pitot's public README carries the latest `main` status.",
        "",
        "| Agent | Ubuntu | macOS | Windows | Result | Evidence |",
        "|---|---|---|---|---|---|",
    ]
    icons = {"passing": "✅ Passing", "failing": "❌ Failing", "pending": "⏳ Pending"}
    records = agent_records or tuple({"id": agent, "label": AGENT_LABELS[agent]} for agent in AGENTS)
    for record in records:
        agent = record["id"]
        platform_results = results.get(agent, {})
        status = aggregate(platform_results, platforms)
        url = run_urls.get(agent)
        label = record["label"]
        linked_label = f"[{label}]({url})" if url else label
        evidence = "All platforms are required" if status != "pending" else "Waiting for all platform artifacts"
        lines.append(
            f"| {linked_label} | {result_cell(platform_results.get('ubuntu'))} | "
            f"{result_cell(platform_results.get('macos'))} | {result_cell(platform_results.get('windows'))} | "
            f"{icons[status]} | {evidence} |"
        )
    lines.extend([
        "",
        "### Runtime capabilities",
        "",
        "| Capability | Ubuntu | macOS | Windows | Result |",
        "|---|---|---|---|---|",
    ])
    capability_results = runtime_results or {}
    runtime_status = aggregate(capability_results, platforms)
    def runtime_cell(result: dict[str, object] | None) -> str:
        if result is None:
            return "⏳ Pending"
        return "✅ Pass · real runtime allow/deny" if result["status"] == "pass" else "❌ Failed"
    lines.append(
        f"| `pitot request` | {runtime_cell(capability_results.get('ubuntu'))} | "
        f"{runtime_cell(capability_results.get('macos'))} | {runtime_cell(capability_results.get('windows'))} | {icons[runtime_status]} |"
    )
    lines.extend(["", f"Source commit: `{head_sha[:12]}`", ""])
    return "\n".join(lines)


class GitHub:
    def __init__(self, repository: str, token: str) -> None:
        self.repository = repository
        self.token = token
        self.base = f"https://api.github.com/repos/{repository}"

    def request(self, method: str, path: str, payload: object | None = None) -> object:
        data = json.dumps(payload).encode() if payload is not None else None
        request = urllib.request.Request(
            self.base + path,
            data=data,
            method=method,
            headers={
                "Accept": "application/vnd.github+json",
                "Authorization": f"Bearer {self.token}",
                "Content-Type": "application/json",
                "X-GitHub-Api-Version": "2022-11-28",
                "User-Agent": "pitot-e2e-reporter",
            },
        )
        with urllib.request.urlopen(request, timeout=30) as response:
            body = response.read(MAX_ARTIFACT_BYTES)
        return json.loads(body) if body else {}

    def download(self, url: str) -> bytes:
        request = urllib.request.Request(
            url,
            headers={"Authorization": f"Bearer {self.token}", "User-Agent": "pitot-e2e-reporter"},
        )
        opener = urllib.request.build_opener(ArtifactRedirectHandler())
        with opener.open(request, timeout=30) as response:
            data = response.read(MAX_ARTIFACT_BYTES + 1)
        if len(data) > MAX_ARTIFACT_BYTES:
            raise ValueError("artifact exceeds size limit")
        return data

    def list_run_artifacts(self, run_id: int) -> list[dict[str, object]]:
        value = self.request("GET", f"/actions/runs/{run_id}/artifacts?per_page=100")
        artifacts = value.get("artifacts") if isinstance(value, dict) else None
        total = value.get("total_count") if isinstance(value, dict) else None
        if not isinstance(artifacts, list) or not isinstance(total, int) or total != len(artifacts):
            raise ValueError("unified E2E artifact listing is truncated or malformed")
        return artifacts


def load_artifact(
    github: GitHub,
    artifact: dict[str, object],
    *,
    agent: str,
    platform: str,
    expected_sha: str,
    expected_run_url: str,
) -> dict[str, object]:
    archive = github.download(str(artifact["archive_download_url"]))
    with zipfile.ZipFile(io.BytesIO(archive)) as bundle:
        names = bundle.namelist()
        if names != ["result.json"] or bundle.getinfo("result.json").file_size > MAX_ARTIFACT_BYTES:
            raise ValueError("artifact must contain one bounded result.json")
        value = json.loads(bundle.read("result.json"))
    return validate_result(
        value,
        agent=agent,
        platform=platform,
        expected_sha=expected_sha,
        expected_run_url=expected_run_url,
    )


def load_inventory(github: GitHub, artifact: dict[str, object]) -> dict[str, object]:
    archive = github.download(str(artifact["archive_download_url"]))
    with zipfile.ZipFile(io.BytesIO(archive)) as bundle:
        names = bundle.namelist()
        if names != ["adapter-verification.json"] or bundle.getinfo(names[0]).file_size > MAX_ARTIFACT_BYTES:
            raise ValueError("inventory artifact must contain one bounded adapter-verification.json")
        return validate_inventory(json.loads(bundle.read(names[0])))


def load_runtime_artifact(
    github: GitHub, artifact: dict[str, object], *, platform: str, expected_sha: str, expected_run_url: str,
) -> dict[str, object]:
    archive = github.download(str(artifact["archive_download_url"]))
    with zipfile.ZipFile(io.BytesIO(archive)) as bundle:
        names = bundle.namelist()
        if names != ["runtime-result.json"] or bundle.getinfo(names[0]).file_size > MAX_ARTIFACT_BYTES:
            raise ValueError("runtime artifact must contain one bounded runtime-result.json")
        value = json.loads(bundle.read(names[0]))
    return validate_runtime_result(value, platform=platform, expected_sha=expected_sha, expected_run_url=expected_run_url)


def collect_results(
    github: GitHub,
    by_name: dict[str, dict[str, object]],
    inventory: dict[str, object],
    head_sha: str,
    run_url: str,
) -> dict[str, dict[str, dict[str, object]]]:
    agent_ids = tuple(agent["id"] for agent in inventory["agents"])
    platforms = tuple(platform["id"] for platform in inventory["platforms"])
    results: dict[str, dict[str, dict[str, object]]] = {agent: {} for agent in agent_ids}
    for agent in agent_ids:
        for platform in platforms:
            artifact = by_name.get(f"pitot-e2e-{agent}-{platform}")
            if artifact is not None:
                try:
                    results[agent][platform] = load_artifact(
                        github,
                        artifact,
                        agent=agent,
                        platform=platform,
                        expected_sha=head_sha,
                        expected_run_url=run_url,
                    )
                    continue
                except (ValueError, json.JSONDecodeError, zipfile.BadZipFile):
                    pass
            results[agent][platform] = failed_result(agent, platform, head_sha, run_url)
    return results


def failed_runtime_result(platform: str, head_sha: str, run_url: str) -> dict[str, object]:
    return {
        "schema_version": 2, "capability": "explicit_request", "platform": platform,
        "status": "fail", "verification_mode": None, "evidence": "explicit request evidence contract failed",
        "nonce": None, "runtime": None, "controller": None, "receipts": None,
        "commit_sha": head_sha, "run_url": run_url,
    }


def collect_runtime_results(
    github: GitHub, by_name: dict[str, dict[str, object]], platforms: tuple[str, ...], head_sha: str, run_url: str,
) -> dict[str, dict[str, object]]:
    results: dict[str, dict[str, object]] = {}
    for platform in platforms:
        artifact = by_name.get(f"pitot-e2e-runtime-{platform}")
        if artifact is not None:
            try:
                results[platform] = load_runtime_artifact(
                    github, artifact, platform=platform, expected_sha=head_sha, expected_run_url=run_url,
                )
                continue
            except (ValueError, json.JSONDecodeError, zipfile.BadZipFile):
                pass
        results[platform] = failed_runtime_result(platform, head_sha, run_url)
    return results


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--event", type=Path, required=True)
    parser.add_argument("--repository", required=True)
    args = parser.parse_args()
    event = json.loads(args.event.read_text(encoding="utf-8"))
    token = os.environ.get("GITHUB_TOKEN")
    if not token:
        raise SystemExit("GITHUB_TOKEN is required")
    github = GitHub(args.repository, token)
    if "pull_request" in event:
        pull_requests = [event["pull_request"]]
        head_sha = event["pull_request"]["head"]["sha"]
        run_id = int(os.environ["GITHUB_RUN_ID"])
        server_url = os.environ.get("GITHUB_SERVER_URL", "https://github.com")
        run_url = f"{server_url}/{args.repository}/actions/runs/{run_id}"
    elif "workflow_run" in event:
        workflow_run = event["workflow_run"]
        pull_requests = workflow_run.get("pull_requests", [])
        if not pull_requests:
            owner = workflow_run.get("head_repository", {}).get("owner", {}).get("login")
            branch = workflow_run.get("head_branch")
            if owner and branch:
                head = urllib.parse.quote(f"{owner}:{branch}", safe=":")
                candidates = github.request("GET", f"/pulls?state=open&head={head}&per_page=20")
                pull_requests = [pr for pr in candidates if pr.get("head", {}).get("sha") == workflow_run["head_sha"]]
            if not pull_requests:
                print("No pull request is associated with this workflow run; nothing to report.")
                return 0
        head_sha = workflow_run["head_sha"]
        run_url = workflow_run["html_url"]
        run_id = int(workflow_run["id"])
    else:
        raise ValueError("reporter requires a pull_request or workflow_run event")

    artifacts = github.list_run_artifacts(run_id)
    by_name = {artifact["name"]: artifact for artifact in artifacts if not artifact.get("expired")}
    inventory_artifact = by_name.get("pitot-e2e-inventory")
    if inventory_artifact is None:
        raise ValueError("unified E2E run is missing its supervised adapter inventory")
    inventory = load_inventory(github, inventory_artifact)
    agent_records = tuple(inventory["agents"])
    agent_ids = tuple(agent["id"] for agent in agent_records)
    platforms = tuple(platform["id"] for platform in inventory["platforms"])
    run_urls = {agent: run_url for agent in agent_ids}
    results = collect_results(github, by_name, inventory, head_sha, run_url)
    runtime_results = collect_runtime_results(github, by_name, platforms, head_sha, run_url)

    body = render_comment(results, run_urls, head_sha, agent_records, platforms, runtime_results)
    issue_number = pull_requests[0]["number"]
    comments = github.request("GET", f"/issues/{issue_number}/comments?per_page=100")
    existing = next(
        (
            comment
            for comment in comments
            if MARKER in comment.get("body", "")
            and comment.get("user", {}).get("login") == "github-actions[bot]"
        ),
        None,
    )
    if existing:
        github.request("PATCH", f"/issues/comments/{existing['id']}", {"body": body})
        print(f"Updated Pitot E2E report on PR #{issue_number}")
    else:
        github.request("POST", f"/issues/{issue_number}/comments", {"body": body})
        print(f"Created Pitot E2E report on PR #{issue_number}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
