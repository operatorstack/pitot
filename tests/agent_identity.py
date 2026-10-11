"""Observe the selected CLI entrypoint; this does not attest delegated packages."""

from __future__ import annotations

import hashlib
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import subprocess

VERSION_TIMEOUT = 30
MAX_VERSION_BYTES = 65536
RECEIPT_FIELDS = {
    "schema_version",
    "agent",
    "version",
    "executable",
    "executable_sha256",
    "version_output",
    "installer",
    "runtime",
    "platform",
}


def version_matches(agent: dict[str, object], output: str) -> bool:
    """Recognize a product's complete banner and literal catalog version."""
    banner = output.replace("\r\n", "\n").strip(" \t\n\r\v\f")
    version = str(agent["version"])
    product = agent["id"]
    if product == "claude":
        return banner == f"{version} (Claude Code)"
    if product == "codex":
        return banner == f"codex-cli {version}"
    if product == "copilot":
        return banner == (
            f"GitHub Copilot CLI {version}.\nRun 'copilot update' to check for updates."
        )
    if product == "devin":
        return (
            re.fullmatch(
                r"devin " + re.escape(version) + r" \([0-9a-f]{8,40}\)", banner
            )
            is not None
        )
    if product in {"cursor", "gemini", "kimi", "opencode", "pi", "qwen"}:
        return banner == version
    return False


def npm_executable() -> str:
    name = "npm.cmd" if os.name == "nt" else "npm"
    return shutil.which(name) or name


def owned_entrypoint(agent: dict[str, object], platform: str, runtime: str) -> str:
    """Select installation locations, never an agent discovered on PATH."""
    if runtime == "wsl":
        if platform != "windows" or agent["id"] != "cursor":
            raise RuntimeError("unsupported agent runtime")
        return f"/usr/local/share/pitot-cursor/{agent['version']}/cursor-agent"
    if runtime != "native":
        raise RuntimeError("unsupported agent runtime")
    kind = agent["installer"]["kind"]
    executable = str(agent["executable"])
    if kind == "npm":
        result = subprocess.run(
            [npm_executable(), "prefix", "--global"],
            check=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            timeout=VERSION_TIMEOUT,
            text=True,
            encoding="utf-8",
            errors="strict",
        )
        prefix = Path(result.stdout.strip(" \t\r\n"))
        if not prefix.is_absolute():
            raise RuntimeError("npm global prefix is not absolute")
        return str(
            prefix
            / (f"{executable}.cmd" if platform == "windows" else f"bin/{executable}")
        )
    if kind == "cursor_release":
        return str(
            Path.home()
            / ".local/share/pitot-cursor"
            / str(agent["version"])
            / "cursor-agent"
        )
    if kind == "devin_release":
        return str(
            Path.home()
            / ".local/bin"
            / ("devin.exe" if platform == "windows" else "devin")
        )
    if kind == "kimi_release":
        if platform == "macos":
            return str(Path.home() / ".local/bin/kimi")
        return str(
            Path.home()
            / ".local/share/pitot-kimi"
            / str(agent["version"])
            / "bin"
            / ("kimi.exe" if platform == "windows" else "kimi")
        )
    raise RuntimeError("unsupported agent installer")


def host_controls_wsl(runtime: str) -> bool:
    return runtime == "wsl" and os.name == "nt"


def entrypoint_digest(path: str, runtime: str) -> str:
    if host_controls_wsl(runtime):
        if not PurePosixPath(path).is_absolute():
            raise RuntimeError("WSL entrypoint is not absolute")
        result = subprocess.run(
            ["wsl.exe", "--distribution", "Ubuntu", "--", "sha256sum", "--", path],
            check=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            timeout=VERSION_TIMEOUT,
            text=True,
            encoding="utf-8",
            errors="strict",
        )
        digest = result.stdout.split(" ", 1)[0]
        if not re.fullmatch(r"[0-9a-f]{64}", digest):
            raise RuntimeError("invalid WSL entrypoint digest")
        return digest
    file = Path(path)
    if not file.is_absolute() or not file.is_file():
        raise RuntimeError("agent entrypoint is not an absolute regular file")
    digest = hashlib.sha256()
    with file.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def probe_version(agent: dict[str, object], path: str, runtime: str) -> tuple[str, str]:
    before = entrypoint_digest(path, runtime)
    command = [path, "--version"]
    if host_controls_wsl(runtime):
        command = ["wsl.exe", "--distribution", "Ubuntu", "--", *command]
    result = subprocess.run(
        command,
        check=True,
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        timeout=VERSION_TIMEOUT,
    )
    if len(result.stdout) + len(result.stderr) > MAX_VERSION_BYTES:
        raise RuntimeError("agent version output exceeds limit")
    output = result.stdout.decode("utf-8", errors="strict")
    error = result.stderr.decode("utf-8", errors="strict")
    if error or not version_matches(agent, output):
        raise RuntimeError("agent did not report the exact supervised version")
    if entrypoint_digest(path, runtime) != before:
        raise RuntimeError("agent entrypoint changed during version probe")
    return output, before


def verify_live_entrypoint(receipt: dict[str, object]) -> None:
    if (
        entrypoint_digest(receipt["executable"], receipt["runtime"])
        != receipt["executable_sha256"]
    ):
        raise RuntimeError("agent entrypoint changed since installation receipt")


def inspect_receipt(
    receipt: object, agent: dict[str, object], platform: str
) -> dict[str, object]:
    """Admit the selected cell before the driver creates runtime or fixture effects."""
    if not isinstance(receipt, dict) or set(receipt) != RECEIPT_FIELDS:
        raise RuntimeError("invalid installation receipt fields")
    if type(receipt["schema_version"]) is not int or receipt["schema_version"] != 1:
        raise RuntimeError("unsupported installation receipt schema")
    for field in RECEIPT_FIELDS - {"schema_version"}:
        if (
            not isinstance(receipt[field], str)
            or not receipt[field]
            or "\0" in receipt[field]
        ):
            raise RuntimeError("invalid installation receipt value")
    expected = {
        "agent": agent["id"],
        "version": agent["version"],
        "installer": agent["installer"]["kind"],
        "platform": platform,
        "runtime": agent["runtime"].get(platform),
    }
    if any(receipt[key] != value for key, value in expected.items()):
        raise RuntimeError("installation receipt does not match supervised cell")
    if not re.fullmatch(r"[0-9a-f]{64}", receipt["executable_sha256"]):
        raise RuntimeError("invalid installation entrypoint digest")
    if receipt["executable"] != owned_entrypoint(agent, platform, receipt["runtime"]):
        raise RuntimeError("installation receipt does not select the owned entrypoint")
    if not version_matches(agent, receipt["version_output"]):
        raise RuntimeError("installation receipt has an unexpected version banner")
    verify_live_entrypoint(receipt)
    output, digest = probe_version(agent, receipt["executable"], receipt["runtime"])
    if digest != receipt["executable_sha256"] or output != receipt["version_output"]:
        raise RuntimeError("agent identity changed since installation receipt")
    return receipt
