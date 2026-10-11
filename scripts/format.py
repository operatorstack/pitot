#!/usr/bin/env python3
"""Format tracked and unignored authored files, or check without writing."""

from __future__ import annotations

import argparse
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
# These outputs are checked by their generator or captured-evidence contracts.
GENERATED = {
    ".github/workflows/pitot-e2e.yml",
    "README.md",  # Contains the byte-stable adapter supervisor block.
    "package-lock.json",
    "sdk/python/pitot/types.py",
    "sdk/typescript/src/pitot.ts",
    "sdk/typescript/src/index.ts",
    "sdk/rust/src/types.rs",
    "tests/endpoint-provenance.json",
}
PRETTIER_SUFFIXES = {".js", ".mjs", ".ts", ".json", ".yml", ".yaml", ".md"}


def run(command: list[str], **kwargs) -> subprocess.CompletedProcess:
    return subprocess.run(command, cwd=ROOT, check=True, **kwargs)


def files() -> list[str]:
    result = run(
        ["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard"],
        stdout=subprocess.PIPE,
    )
    return sorted(
        {
            name
            for name in result.stdout.decode().split("\0")
            if name
            and name not in GENERATED
            and not name.startswith("docs/evidence/")
            and (ROOT / name).is_file()
        }
    )


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true", help="fail on formatting drift")
    args = parser.parse_args()
    for tool, expected in (("shfmt", "v3.14.1"), ("rustfmt", "1.9.0")):
        result = run([tool, "--version"], stdout=subprocess.PIPE, text=True)
        observed = result.stdout.strip()
        if tool == "rustfmt":
            observed = observed.split()[1].split("-")[0]
        if observed != expected:
            print(
                f"Expected {expected}; found {result.stdout.strip()}", file=sys.stderr
            )
            return 2
    paths = files()
    groups = {
        suffix: [name for name in paths if Path(name).suffix == suffix]
        for suffix in (".go", ".py", ".rs", ".sh")
    }
    prettier = [name for name in paths if Path(name).suffix in PRETTIER_SUFFIXES]
    failed = False

    if groups[".go"]:
        if args.check:
            result = run(["gofmt", "-l", *groups[".go"]], stdout=subprocess.PIPE)
            if result.stdout:
                sys.stdout.buffer.write(result.stdout)
                failed = True
        else:
            run(["gofmt", "-w", *groups[".go"]])

    if groups[".py"]:
        result = subprocess.run(
            [
                sys.executable,
                "-m",
                "ruff",
                "format",
                *(["--check"] if args.check else []),
                *groups[".py"],
            ],
            cwd=ROOT,
        )
        if result.returncode not in (0, 1):
            return result.returncode
        failed |= result.returncode != 0

    if prettier:
        result = subprocess.run(
            [
                "node",
                str(ROOT / "node_modules/prettier/bin/prettier.cjs"),
                "--check" if args.check else "--write",
                *prettier,
            ],
            cwd=ROOT,
        )
        if result.returncode not in (0, 1):
            return result.returncode
        failed |= result.returncode != 0

    # Stdin formatting prevents module traversal into generated Rust types.
    for name in groups[".rs"]:
        path = ROOT / name
        original = path.read_bytes()
        result = run(
            [
                "rustfmt",
                "--edition",
                "2021",
                "--config",
                "style_edition=2021,reorder_modules=false",
            ],
            input=original,
            stdout=subprocess.PIPE,
        )
        if result.stdout != original:
            if args.check:
                print(name)
                failed = True
            else:
                path.write_bytes(result.stdout)

    if groups[".sh"]:
        result = subprocess.run(
            [
                "shfmt",
                "-i",
                "2",
                "-ci",
                "-sr",
                "-d" if args.check else "-w",
                *groups[".sh"],
            ],
            cwd=ROOT,
        )
        if result.returncode not in (0, 1):
            return result.returncode
        failed |= result.returncode != 0
    return int(failed)


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, subprocess.CalledProcessError) as error:
        print(f"Formatter failed: {error}", file=sys.stderr)
        sys.exit(2)
