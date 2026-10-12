"""Observe the selected CLI entrypoint; this does not attest delegated packages."""

from __future__ import annotations

from contextlib import ExitStack
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import stat
import subprocess
import sys
import tempfile

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
QWEN_BOOTSTRAP_OVERRIDES = {
    "CLI_VERSION",
    "QWEN_CODE_RELAUNCH_ARGS",
    "QWEN_CODE_MANAGED_NPM_PIN",
    "QWEN_CODE_LAUNCHER_PATH",
    "QWEN_CODE_STARTUP_VERSION",
}
KIMI_MODEL_OVERRIDES = {
    "KIMI_MODEL_ADAPTIVE_THINKING",
    "KIMI_MODEL_API_KEY",
    "KIMI_MODEL_BASE_URL",
    "KIMI_MODEL_CAPABILITIES",
    "KIMI_MODEL_DISPLAY_NAME",
    "KIMI_MODEL_MAX_COMPLETION_TOKENS",
    "KIMI_MODEL_MAX_CONTEXT_SIZE",
    "KIMI_MODEL_MAX_OUTPUT_SIZE",
    "KIMI_MODEL_MAX_TOKENS",
    "KIMI_MODEL_NAME",
    "KIMI_MODEL_PROVIDER_TYPE",
    "KIMI_MODEL_REASONING_KEY",
    "KIMI_MODEL_TEMPERATURE",
    "KIMI_MODEL_THINKING_EFFORT",
    "KIMI_MODEL_THINKING_KEEP",
    "KIMI_MODEL_TOP_P",
}
CURSOR_BOOTSTRAP_OVERRIDES = {
    "CURSOR_API_KEY",
    "CURSOR_AGENT_CLI_LOCAL_MODE",
    "NODE_OPTIONS",
}


def agent_environment(agent: str, environment: dict[str, str]) -> dict[str, str]:
    """Remove known verification bootstrap and model overrides."""
    if agent == "devin":
        filtered = {
            key: value
            for key, value in environment.items()
            if not key.startswith(("DEVIN_", "WINDSURF_", "OTEL_"))
            and key.lower()
            not in {"http_proxy", "https_proxy", "all_proxy", "no_proxy"}
        }
        return filtered if len(filtered) != len(environment) else environment
    if agent == "qwen":
        overrides = QWEN_BOOTSTRAP_OVERRIDES
    elif agent == "kimi":
        overrides = KIMI_MODEL_OVERRIDES
    elif agent == "cursor":
        overrides = CURSOR_BOOTSTRAP_OVERRIDES
    else:
        return environment
    return {key: value for key, value in environment.items() if key not in overrides}


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
    if product == "opencode" and version.startswith("2."):
        return banner == f"opencode v{version}"
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
            / ("cursor-agent.cmd" if platform == "windows" else "cursor-agent")
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


def stderr_matches(agent: dict[str, object], error: str) -> bool:
    """Admit only the documented successful Copilot extraction diagnostic."""
    product = agent["id"]
    if product not in {
        "claude",
        "codex",
        "copilot",
        "cursor",
        "devin",
        "gemini",
        "kimi",
        "opencode",
        "pi",
        "qwen",
    }:
        return False
    if not error:
        return True
    if product != "copilot":
        return False
    match = re.fullmatch(r"Package extraction took ([1-9][0-9]*)ms\n", error)
    if match is None:
        return False
    duration = match[1]
    return len(duration) > 4 or (len(duration) == 4 and duration > "5000")


def require_kimi_ready(path: str, runtime: str) -> None:
    """Refuse native updater artifacts before launching the selected Kimi CLI."""
    if runtime != "native" or not Path(path).is_absolute():
        raise RuntimeError("Kimi verification requires an absolute native entrypoint")
    try:
        executable = Path(path).resolve(strict=True)
        try:
            (executable.parent / ".staging").lstat()
        except FileNotFoundError:
            pass
        else:
            raise RuntimeError("Kimi has adjacent native update staging state")
        backup = re.compile(re.escape(executable.name) + r"(?:\.[0-9]+)?\.bak")
        for sibling in executable.parent.iterdir():
            if backup.fullmatch(sibling.name):
                raise RuntimeError("Kimi has adjacent native update backup state")
    except OSError as error:
        raise RuntimeError("cannot inspect Kimi native update state") from error


def require_cursor_ready(path: str, runtime: str) -> None:
    """Reject missing same-directory delegates before a launcher can fall back."""
    if runtime != "native" or not Path(path).is_absolute():
        raise RuntimeError("Cursor verification requires an absolute native entrypoint")
    try:
        selected = Path(path)
        if not stat.S_ISREG(selected.lstat().st_mode):
            raise RuntimeError("Cursor requires a regular selected launcher")
        executable = selected.resolve(strict=True)
        if executable.name == "cursor-agent.cmd":
            names = ("cursor-agent.cmd", "cursor-agent.ps1", "node.exe", "index.js")
        elif executable.name == "cursor-agent":
            names = ("cursor-agent", "node", "index.js")
        else:
            raise RuntimeError("Cursor entrypoint has an unsupported launcher name")
        for name in names:
            if not stat.S_ISREG((executable.parent / name).lstat().st_mode):
                raise RuntimeError(
                    "Cursor requires regular same-directory launch files"
                )
    except OSError as error:
        raise RuntimeError("cannot inspect Cursor owned launch files") from error


def devin_context(home: Path) -> dict[str, str]:
    """Keep release probes and model sessions in explicit, owned configuration."""
    config = {
        "auto_update": False,
        "otel": {"enabled": False},
        "proxy": {"mode": "off"},
        "read_config_from": dict.fromkeys(
            (
                "agents_standard",
                "cursor",
                "windsurf",
                "claude",
                "copilot",
                "opencode",
                "zed",
            ),
            False,
        ),
    }
    app_data = home / "AppData/Roaming"
    for directory in (home / ".config", app_data):
        target = directory / "devin/config.json"
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(json.dumps(config) + "\n", encoding="utf-8")
    return {
        "HOME": str(home),
        "USERPROFILE": str(home),
        "XDG_CONFIG_HOME": str(home / ".config"),
        "XDG_DATA_HOME": str(home / ".local/share"),
        "XDG_CACHE_HOME": str(home / ".cache"),
        "XDG_STATE_HOME": str(home / ".local/state"),
        "APPDATA": str(app_data),
        "LOCALAPPDATA": str(home / "AppData/Local"),
    }


def cursor_context(home: Path, data: Path) -> dict[str, str]:
    """Select owned locations without changing the caller's global environment."""
    return {
        "HOME": str(home),
        "USERPROFILE": str(home),
        "CURSOR_CONFIG_DIR": str(home / ".cursor"),
        "CURSOR_DATA_DIR": str(data),
        "NODE_COMPILE_CACHE": str(home / ".cache" / "cursor-node"),
        "AGENT_CLI_CREDENTIAL_STORE": "memory",
        "DIRENV_DISABLE": "1",
    }


def probe_version(agent: dict[str, object], path: str, runtime: str) -> tuple[str, str]:
    if agent["id"] == "kimi":
        require_kimi_ready(path, runtime)
    elif agent["id"] == "cursor":
        require_cursor_ready(path, runtime)
    before = entrypoint_digest(path, runtime)
    command = [path, "--version"]
    if host_controls_wsl(runtime):
        command = ["wsl.exe", "--distribution", "Ubuntu", "--", *command]
    with ExitStack() as cleanup:
        options = {}
        if agent["id"] == "devin":
            home = Path(
                cleanup.enter_context(
                    tempfile.TemporaryDirectory(prefix="pitot-devin-version-")
                )
            )
            environment = agent_environment("devin", dict(os.environ))
            environment.update(devin_context(home))
            options["env"] = environment
        elif agent["id"] == "cursor":
            home = Path(
                cleanup.enter_context(
                    tempfile.TemporaryDirectory(
                        prefix="pc-", dir="/tmp" if os.name != "nt" else None
                    )
                )
            )
            environment = agent_environment("cursor", dict(os.environ))
            environment.pop("CURSOR_AUTH_TOKEN", None)
            environment.update(cursor_context(home, home / "d"))
            command = [path, "--disable-auto-update", "--version"]
            options["env"] = environment
        elif agent["id"] == "qwen":
            version_home = cleanup.enter_context(
                tempfile.TemporaryDirectory(prefix="pitot-qwen-version-")
            )
            environment = agent_environment("qwen", dict(os.environ))
            environment["QWEN_HOME"] = str(Path(version_home) / ".qwen")
            options["env"] = environment
        elif agent["id"] == "kimi":
            version_home = Path(
                cleanup.enter_context(
                    tempfile.TemporaryDirectory(prefix="pitot-kimi-version-")
                )
            )
            environment = agent_environment("kimi", dict(os.environ))
            environment.update(
                {
                    "HOME": str(version_home),
                    "USERPROFILE": str(version_home),
                    "KIMI_CODE_HOME": str(version_home / ".kimi-code"),
                    "KIMI_CODE_CACHE_DIR": str(version_home / ".cache" / "kimi-code"),
                    "KIMI_CODE_NO_AUTO_UPDATE": "1",
                }
            )
            options["env"] = environment
        result = subprocess.run(
            command,
            check=True,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            timeout=VERSION_TIMEOUT,
            **options,
        )
    if len(result.stdout) + len(result.stderr) > MAX_VERSION_BYTES:
        raise RuntimeError("agent version output exceeds limit")
    output = result.stdout.decode("utf-8", errors="strict")
    error = result.stderr.decode("utf-8", errors="strict")
    if not stderr_matches(agent, error) or not version_matches(agent, output):
        raise RuntimeError(
            "agent did not report the exact supervised version; "
            f"stdout={output[:4096]!r}; stderr={error[:4096]!r}"
        )
    if entrypoint_digest(path, runtime) != before:
        raise RuntimeError("agent entrypoint changed during version probe")
    if result.stderr:
        sys.stderr.buffer.write(result.stderr)
        sys.stderr.buffer.flush()
    return output, before


def verify_live_entrypoint(receipt: dict[str, object]) -> None:
    if receipt.get("agent") == "kimi":
        require_kimi_ready(receipt["executable"], receipt["runtime"])
    elif receipt.get("agent") == "cursor":
        require_cursor_ready(receipt["executable"], receipt["runtime"])
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
