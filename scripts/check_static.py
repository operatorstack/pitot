#!/usr/bin/env python3
"""Check authored-source correctness without rewriting files."""

from __future__ import annotations

import os
import re
from pathlib import Path
import subprocess
import sys

from format import ROOT, files


def main() -> None:
    paths = files()
    subprocess.run(
        ["go", "vet", "./..."],
        cwd=ROOT,
        env={**os.environ, "GOWORK": "off"},
        check=True,
    )
    python = [name for name in paths if Path(name).suffix == ".py"]
    if python:
        subprocess.run(
            [
                sys.executable,
                "-m",
                "ruff",
                "check",
                "--select",
                "E9,F821,F822,F823",
                *python,
            ],
            cwd=ROOT,
            check=True,
        )
    for name in paths:
        if Path(name).suffix == ".sh" or re.match(
            rb"^#!.*(?:/|\s)(?:ba)?sh(?:\s|$)",
            (ROOT / name).read_bytes().split(b"\n", 1)[0],
        ):
            subprocess.run(["bash", "-n", name], cwd=ROOT, check=True)
        elif Path(name).suffix in {".js", ".mjs", ".cjs"}:
            subprocess.run(["node", "--check", name], cwd=ROOT, check=True)


if __name__ == "__main__":
    main()
