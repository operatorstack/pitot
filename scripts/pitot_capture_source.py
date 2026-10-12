"""Acquire endpoint captures from an explicitly approved GitHub Actions run."""

from __future__ import annotations

import base64
import hashlib
import importlib.util
import io
import json
import math
import os
from pathlib import Path
import re
import stat
import subprocess
import tempfile
import zipfile

from pitot_adapter_supervisor import validate_endpoint_value


ROOT = Path(__file__).resolve().parents[1]
WORKFLOW = ".github/workflows/pitot-e2e.yml"
PRODUCER_FILES = (
    "adapter-verification.json",
    WORKFLOW,
    ".github/workflows/pitot-e2e-agent.yml",
    "tests/run_e2e_report.py",
    "tests/real_agent_driver.py",
    "tests/e2e_unified_runner.sh",
    "tests/install_real_agent.py",
    "tests/agent_identity.py",
    "scripts/pitot_capture_source.py",
    "scripts/pitot_adapter_supervisor.py",
    ".github/scripts/pitot_e2e_report.py",
)
MAX_BYTES = 1_000_000
MAX_RECORDS = 1000
SOURCE_FIELDS = {
    "repo",
    "run_id",
    "run_attempt",
    "source_sha",
    "checkout_sha",
    "check_run_id",
}
INDEX_FIELDS = {
    "repository",
    "run_id",
    "run_attempt",
    "source_sha",
    "workflow_id",
    "workflow_path",
    "catalog_sha256",
    "artifacts",
    "repository_id",
}
BINDING_FIELDS = {
    "agent",
    "platform",
    "artifact_id",
    "archive_sha256",
    "job_id",
    "check_run_id",
    "capture_sha256",
    "runner",
    "conclusion",
    "runner_id",
    "runner_name",
    "runner_labels",
}


def positive(value: object) -> bool:
    return type(value) is int and value > 0


def digest(value: object, length: int = 64) -> bool:
    return (
        isinstance(value, str)
        and re.fullmatch(r"[0-9a-f]{" + str(length) + "}", value) is not None
    )


def require(condition: bool, message: str) -> None:
    if not condition:
        raise ValueError(message)


def strict_json(data: bytes) -> object:
    def pairs(items):
        result = {}
        for key, value in items:
            require(key not in result, "duplicate JSON key")
            result[key] = value
        return result

    def constant(value):
        raise ValueError("nonstandard JSON number")

    def number(value):
        parsed = float(value)
        require(math.isfinite(parsed), "JSON number exceeds finite representation")
        return parsed

    require(len(data) <= MAX_BYTES, "JSON exceeds size limit")
    return json.loads(
        data.decode("utf-8", errors="strict"),
        object_pairs_hook=pairs,
        parse_constant=constant,
        parse_float=number,
    )


def load_reporter():
    spec = importlib.util.spec_from_file_location(
        "pitot_capture_reporter", ROOT / ".github/scripts/pitot_e2e_report.py"
    )
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def source_context(root: Path, environment: dict[str, str]) -> dict[str, object]:
    """Assign source context in the host wrapper, after the complete session."""
    source = {
        "repo": environment.get("GITHUB_REPOSITORY"),
        "source_sha": environment.get("PITOT_SOURCE_SHA"),
        "checkout_sha": subprocess.check_output(
            ["git", "rev-parse", "HEAD"], cwd=root, text=True
        ).strip(),
    }
    for key, variable in (
        ("run_id", "GITHUB_RUN_ID"),
        ("run_attempt", "GITHUB_RUN_ATTEMPT"),
        ("check_run_id", "PITOT_CAPTURE_CHECK_RUN_ID"),
    ):
        raw = environment.get(variable, "")
        require(
            re.fullmatch(r"[1-9][0-9]*", raw) is not None, "missing capture job context"
        )
        source[key] = int(raw)
    require(
        isinstance(source["repo"], str)
        and re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", source["repo"])
        is not None,
        "invalid capture repository",
    )
    require(
        digest(source["source_sha"], 40)
        and source["checkout_sha"] == source["source_sha"],
        "capture checkout differs from selected source",
    )
    return source


def seal_capture(
    path: Path, result: dict[str, object], root: Path, environment: dict[str, str]
) -> None:
    require(result.get("status") == "pass", "failed session cannot seal capture")
    frame = strict_json(path.read_bytes())
    require(
        isinstance(frame, dict)
        and set(frame) == {"schema_version", "accepted", "cell"},
        "invalid unsealed capture",
    )
    require(
        type(frame["schema_version"]) is int
        and frame["schema_version"] == 1
        and frame["accepted"] is True,
        "invalid unsealed capture version",
    )
    cell = frame["cell"]
    require(isinstance(cell, dict), "invalid capture cell")
    load_reporter().validate_result(
        result, agent=cell["agent"], platform=cell["platform"], capture_fixture=cell
    )
    source = source_context(root, environment)
    require(
        result["commit_sha"] == source["source_sha"]
        and result["run_url"]
        == f"https://github.com/{source['repo']}/actions/runs/{source['run_id']}",
        "capture result differs from job context",
    )
    atomic_json(
        path, {"schema_version": 2, "accepted": True, "cell": cell, "source": source}
    )


def atomic_json(path: Path, value: object) -> None:
    data = (json.dumps(value, indent=2, sort_keys=True, allow_nan=False) + "\n").encode(
        "utf-8"
    )
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = None
    try:
        with tempfile.NamedTemporaryFile(
            dir=path.parent, prefix="." + path.name + ".", delete=False
        ) as stream:
            temporary = Path(stream.name)
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
        temporary = None
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)


def validate_source_index(
    source: object, cells: list[dict[str, object]], manifest: dict[str, object]
) -> None:
    """Check committed consistency; this is not fresh GitHub authentication."""
    require(
        isinstance(source, dict) and set(source) == INDEX_FIELDS,
        "invalid capture source index",
    )
    require(
        isinstance(source["repository"], str)
        and re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", source["repository"])
        is not None,
        "invalid source repository",
    )
    require(
        all(
            positive(source[key])
            for key in ("run_id", "run_attempt", "workflow_id", "repository_id")
        ),
        "invalid source IDs",
    )
    require(
        source["workflow_path"] == WORKFLOW
        and digest(source["source_sha"], 40)
        and digest(source["catalog_sha256"]),
        "invalid source workflow or digest",
    )
    bindings = source["artifacts"]
    require(
        isinstance(bindings, list) and len(bindings) == len(cells),
        "incomplete source bindings",
    )
    by_cell = {(cell["agent"], cell["platform"]): cell for cell in cells}
    runners = {item["id"]: item["runner"] for item in manifest["platforms"]}
    seen = set()
    identities = {key: set() for key in ("artifact_id", "job_id", "check_run_id")}
    for row in bindings:
        require(
            isinstance(row, dict) and set(row) == BINDING_FIELDS,
            "invalid source binding",
        )
        key = (row["agent"], row["platform"])
        require(key in by_cell and key not in seen, "duplicate or unknown source cell")
        seen.add(key)
        require(
            digest(row["archive_sha256"])
            and row["capture_sha256"] == by_cell[key]["capture_sha256"],
            "source binding digest mismatch",
        )
        require(
            row["runner"] == runners[key[1]] and row["conclusion"] == "success",
            "invalid source runner conclusion",
        )
        require(
            positive(row["runner_id"])
            and isinstance(row["runner_name"], str)
            and bool(row["runner_name"])
            and isinstance(row["runner_labels"], list)
            and all(isinstance(label, str) and label for label in row["runner_labels"])
            and row["runner"] in row["runner_labels"],
            "invalid observed runner identity",
        )
        for field in identities:
            require(
                positive(row[field]) and row[field] not in identities[field],
                "duplicate or invalid source ID",
            )
            identities[field].add(row[field])


def listing(client, path: str, key: str) -> list[dict[str, object]]:
    rows = []
    total = None
    for page in range(1, MAX_RECORDS // 100 + 1):
        value = client.request("GET", f"{path}?per_page=100&page={page}")
        require(
            isinstance(value, dict)
            and type(value.get("total_count")) is int
            and 0 <= value["total_count"] <= MAX_RECORDS
            and isinstance(value.get(key), list),
            "invalid API listing",
        )
        require(
            total is None or total == value["total_count"],
            "API listing changed during collection",
        )
        total = value["total_count"]
        batch = value[key]
        require(
            all(isinstance(row, dict) for row in batch) and len(batch) <= 100,
            "invalid API records",
        )
        rows.extend(batch)
        require(len(rows) <= total, "API listing has extra records")
        if len(rows) == total:
            return rows
        require(len(batch) == 100, "truncated API listing")
    raise ValueError("API listing exceeds limit")


def validate_run(
    run: object,
    repository: str,
    repository_id: int,
    run_id: int,
    source_sha: str,
    workflow_id: int,
) -> dict[str, object]:
    require(isinstance(run, dict), "invalid workflow run")
    require(
        run.get("id") == run_id and type(run.get("id")) is int, "wrong workflow run"
    )
    require(
        run.get("repository", {}).get("id") == repository_id
        and run.get("repository", {}).get("full_name") == repository
        and run.get("head_repository", {}).get("id") == repository_id,
        "workflow repository mismatch",
    )
    require(
        run.get("head_sha") == source_sha
        and run.get("workflow_id") == workflow_id
        and run.get("path") == WORKFLOW,
        "workflow source mismatch",
    )
    require(
        run.get("event") == "workflow_dispatch"
        and run.get("status") == "completed"
        and run.get("conclusion") == "success"
        and positive(run.get("run_attempt")),
        "capture run must be completed successful dispatch",
    )
    return run


def source_bytes(client, path: str, source_sha: str) -> bytes:
    value = client.request("GET", f"/contents/{path}?ref={source_sha}")
    require(
        isinstance(value, dict)
        and value.get("type") == "file"
        and value.get("encoding") == "base64"
        and isinstance(value.get("content"), str),
        "source file unavailable",
    )
    require(len(value["content"]) <= MAX_BYTES * 2, "source file exceeds limit")
    data = base64.b64decode(value["content"])
    require(len(data) <= MAX_BYTES, "source file exceeds limit")
    return data


def unpack(archive: bytes) -> tuple[dict[str, object], dict[str, object]]:
    require(len(archive) <= MAX_BYTES, "capture archive exceeds limit")
    with zipfile.ZipFile(io.BytesIO(archive)) as bundle:
        entries = bundle.infolist()
        require(
            len(entries) == 2
            and {entry.filename for entry in entries}
            == {"capture.json", "result.json"},
            "capture archive must contain exactly capture.json and result.json",
        )
        for entry in entries:
            mode = entry.external_attr >> 16
            require(
                not entry.is_dir()
                and stat.S_IFMT(mode) in (0, stat.S_IFREG)
                and entry.file_size <= MAX_BYTES,
                "unsupported capture archive member",
            )
        values = [
            strict_json(bundle.read(name)) for name in ("capture.json", "result.json")
        ]
    require(
        all(isinstance(value, dict) for value in values), "invalid capture JSON objects"
    )
    return values[0], values[1]


def collect(
    client,
    root: Path,
    manifest: dict[str, object],
    repository: str,
    run_id: int,
    source_sha: str,
) -> dict[str, object]:
    require(
        isinstance(repository, str)
        and re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repository) is not None
        and positive(run_id)
        and digest(source_sha, 40),
        "explicit repository/run/source required",
    )
    repo = client.request("GET", "")
    require(
        isinstance(repo, dict)
        and repo.get("full_name") == repository
        and positive(repo.get("id")),
        "repository API identity mismatch",
    )
    workflow = client.request("GET", "/actions/workflows/pitot-e2e.yml")
    require(
        isinstance(workflow, dict)
        and positive(workflow.get("id"))
        and workflow.get("path") == WORKFLOW,
        "workflow API identity mismatch",
    )
    run = validate_run(
        client.request("GET", f"/actions/runs/{run_id}"),
        repository,
        repo["id"],
        run_id,
        source_sha,
        workflow["id"],
    )
    attempt = run["run_attempt"]
    producer_bytes = {}
    for path in PRODUCER_FILES:
        producer_bytes[path] = source_bytes(client, path, source_sha)
        require(
            producer_bytes[path] == (root / path).read_bytes(),
            "approved producer source differs from checkout",
        )
    jobs = listing(client, f"/actions/runs/{run_id}/attempts/{attempt}/jobs", "jobs")
    artifacts = listing(client, f"/actions/runs/{run_id}/artifacts", "artifacts")
    platforms = {item["id"]: item for item in manifest["platforms"]}
    expected = {
        (agent["id"], platform)
        for agent in manifest["agents"]
        for platform in platforms
    }
    selected = {}
    artifact_ids = set()
    for artifact in artifacts:
        name = artifact.get("name", "")
        if not isinstance(name, str) or not name.startswith("pitot-endpoint-capture-"):
            continue
        match = re.fullmatch(
            r"pitot-endpoint-capture-([a-z0-9_]+)-(ubuntu|macos|windows)-([1-9][0-9]*)-([1-9][0-9]*)",
            name,
        )
        require(match is not None, "unbound capture artifact name")
        agent, platform, artifact_attempt, check = match.groups()
        if int(artifact_attempt) != attempt:
            continue
        key = (agent, platform)
        require(
            key in expected
            and key not in selected
            and positive(artifact.get("id"))
            and artifact["id"] not in artifact_ids,
            "duplicate or unknown capture artifact",
        )
        selected[key] = (artifact, int(check))
        artifact_ids.add(artifact["id"])
    require(
        set(selected) == expected, "capture attempt must contain every declared cell"
    )
    reporter = load_reporter()
    cells, bindings, used_jobs = [], [], set()
    for agent, platform in sorted(expected):
        artifact, check = selected[(agent, platform)]
        identity = artifact.get("workflow_run")
        require(
            isinstance(identity, dict)
            and all(
                positive(identity.get(key))
                for key in ("id", "repository_id", "head_repository_id")
            )
            and identity.get("id") == run_id
            and identity.get("repository_id") == repo["id"]
            and identity.get("head_repository_id") == repo["id"]
            and identity.get("head_sha") == source_sha
            and artifact.get("expired") is False,
            "artifact source identity mismatch",
        )
        archive_digest = artifact.get("digest")
        require(
            isinstance(archive_digest, str)
            and re.fullmatch(r"sha256:[0-9a-f]{64}", archive_digest) is not None,
            "missing artifact API digest",
        )
        job_url = f"https://api.github.com/repos/{repository}/check-runs/{check}"
        matching = [job for job in jobs if job.get("check_run_url") == job_url]
        require(len(matching) == 1, "missing or ambiguous capture job")
        job = matching[0]
        require(
            all(positive(job.get(key)) for key in ("id", "run_id", "run_attempt"))
            and job["id"] not in used_jobs
            and job.get("run_id") == run_id
            and job.get("run_attempt") == attempt
            and job.get("head_sha") == source_sha,
            "capture job identity mismatch",
        )
        used_jobs.add(job["id"])
        require(
            isinstance(job.get("name"), str)
            and job["name"].startswith("verify-agents (")
            and job["name"].endswith(f" / {agent} ({platform})")
            and platforms[platform]["runner"] in job.get("labels", [])
            and job.get("status") == "completed"
            and job.get("conclusion") == "success",
            "capture job cell or outcome mismatch",
        )
        require(
            positive(job.get("runner_id"))
            and isinstance(job.get("runner_name"), str)
            and bool(job["runner_name"])
            and isinstance(job.get("labels"), list)
            and all(isinstance(label, str) and label for label in job["labels"]),
            "invalid observed capture runner",
        )
        for name in (f"Run {agent} E2E", "Upload endpoint provenance candidate"):
            steps = [
                step
                for step in job.get("steps", [])
                if isinstance(step, dict) and step.get("name") == name
            ]
            require(
                len(steps) == 1
                and steps[0].get("status") == "completed"
                and steps[0].get("conclusion") == "success",
                "required capture step did not succeed",
            )
        archive = client.download(
            f"https://api.github.com/repos/{repository}/actions/artifacts/{artifact['id']}/zip"
        )
        require(
            hashlib.sha256(archive).hexdigest() == archive_digest[7:],
            "capture archive API digest mismatch",
        )
        frame, result = unpack(archive)
        require(
            set(frame) == {"schema_version", "accepted", "cell", "source"}
            and type(frame["schema_version"]) is int
            and frame["schema_version"] == 2
            and frame["accepted"] is True,
            "invalid capture envelope",
        )
        source = frame["source"]
        expected_source = {
            "repo": repository,
            "run_id": run_id,
            "run_attempt": attempt,
            "source_sha": source_sha,
            "checkout_sha": source_sha,
            "check_run_id": check,
        }
        require(
            isinstance(source, dict)
            and set(source) == SOURCE_FIELDS
            and all(
                positive(source[key])
                for key in ("run_id", "run_attempt", "check_run_id")
            )
            and source == expected_source,
            "capture source context mismatch",
        )
        cell = frame["cell"]
        require(
            isinstance(cell, dict)
            and cell.get("agent") == agent
            and cell.get("platform") == platform
            and result.get("status") == "pass"
            and type(result.get("schema_version")) is int,
            "capture/result identity mismatch",
        )
        reporter.validate_result(
            result,
            agent=agent,
            platform=platform,
            expected_sha=source_sha,
            expected_run_url=f"https://github.com/{repository}/actions/runs/{run_id}",
            capture_fixture=cell,
        )
        cells.append(cell)
        bindings.append(
            {
                "agent": agent,
                "platform": platform,
                "artifact_id": artifact["id"],
                "archive_sha256": archive_digest[7:],
                "job_id": job["id"],
                "check_run_id": check,
                "capture_sha256": cell["capture_sha256"],
                "runner": platforms[platform]["runner"],
                "conclusion": "success",
                "runner_id": job["runner_id"],
                "runner_name": job["runner_name"],
                "runner_labels": job["labels"],
            }
        )
    final_run = validate_run(
        client.request("GET", f"/actions/runs/{run_id}"),
        repository,
        repo["id"],
        run_id,
        source_sha,
        workflow["id"],
    )
    require(
        final_run["run_attempt"] == attempt, "run attempt changed during collection"
    )
    require(
        all(
            (root / path).read_bytes() == data for path, data in producer_bytes.items()
        ),
        "local producer changed during collection",
    )
    value = {
        "schema_version": 3,
        "capture_policy": "pinned_real_cli_capture",
        "cells": cells,
        "source": {
            "repository": repository,
            "repository_id": repo["id"],
            "run_id": run_id,
            "run_attempt": attempt,
            "source_sha": source_sha,
            "workflow_id": workflow["id"],
            "workflow_path": WORKFLOW,
            "catalog_sha256": hashlib.sha256(
                producer_bytes["adapter-verification.json"]
            ).hexdigest(),
            "artifacts": bindings,
        },
    }
    validate_endpoint_value(value, manifest)
    return value


def authenticated_client(repository: str):
    token = os.environ.get("GITHUB_TOKEN")
    if not token:
        token = subprocess.check_output(
            ["gh", "auth", "token", "--hostname", "github.com"], text=True, timeout=30
        ).strip()
    require(bool(token), "GitHub authentication is required")
    return load_reporter().GitHub(repository, token)
