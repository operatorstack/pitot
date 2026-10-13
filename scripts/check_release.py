#!/usr/bin/env python3
"""Validate release identity before acquiring credentials or publishing artifacts."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import re
import subprocess

ROOT = Path(__file__).resolve().parents[1]
STABLE_VERSION = r"(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)"


def source_version(root: Path = ROOT) -> str:
    versions = {
        "npm": json.loads((root / "sdk/typescript/package.json").read_text())["version"]
    }
    for name, path, pattern in (
        ("python", "sdk/python/pyproject.toml", r'^version = "([^"]+)"$'),
        ("rust", "sdk/rust/Cargo.toml", r'^version = "([^"]+)"$'),
        (
            "scaffold",
            "cmd/pitot/workbench.go",
            r'^const pitotPackageVersion = "([^"]+)"$',
        ),
    ):
        match = re.search(pattern, (root / path).read_text(), re.M)
        if not match:
            raise ValueError(f"Missing {name} version")
        versions[name] = match[1]
    value = versions["npm"]
    if not re.fullmatch(STABLE_VERSION, value) or len(set(versions.values())) != 1:
        raise ValueError(
            f"SDK release versions disagree or are unsupported: {versions}"
        )
    return value


def check_release(ref: str, expected_commit: str, root: Path = ROOT) -> str:
    match = re.fullmatch(r"refs/tags/v(" + STABLE_VERSION + r")", ref)
    if not match:
        raise ValueError("Release requires a stable vMAJOR.MINOR.PATCH tag ref")
    value = match[1]
    if int(value.split(".")[0]) >= 2:
        raise ValueError("Release major >=2 requires a Go module path migration first")
    if value != source_version(root):
        raise ValueError(
            "Release tag must match all SDK source versions and scaffold fallback"
        )

    def revision(name):
        return subprocess.check_output(
            ["git", "rev-parse", "--verify", name + "^{commit}"], cwd=root, text=True
        ).strip()

    if (
        not expected_commit
        or revision(ref) != revision("HEAD")
        or revision("HEAD") != expected_commit
    ):
        raise ValueError("Release tag, checkout and event commit must match")
    if subprocess.check_output(
        ["git", "status", "--porcelain"], cwd=root, text=True
    ).strip():
        raise ValueError("Release source must be clean")
    return value


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--ref", default=os.environ.get("GITHUB_REF", ""))
    parser.add_argument("--commit", default=os.environ.get("GITHUB_SHA", ""))
    args = parser.parse_args()
    try:
        value = check_release(args.ref, args.commit)
    except (ValueError, subprocess.CalledProcessError) as error:
        raise SystemExit(str(error)) from error
    print(f"Release source verified: {value}")


if __name__ == "__main__":
    main()
