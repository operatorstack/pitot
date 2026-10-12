#!/usr/bin/env python3
"""Install and attest the exact released CLI declared by Pitot's supervisor."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import platform as host_platform
import re
import shutil
import stat
import subprocess
import tarfile
import tempfile
import urllib.request
import zipfile

from agent_identity import owned_entrypoint, probe_version


LAB = Path(__file__).resolve().parent.parent
ROOT = LAB
MANIFEST = json.loads((LAB / "adapter-verification.json").read_text(encoding="utf-8"))


def run(command: list[str], **kwargs: object) -> subprocess.CompletedProcess[str]:
    return subprocess.run(command, check=True, text=True, **kwargs)


def npm_executable() -> str:
    """Resolve npm's platform launcher instead of relying on PATHEXT in Python."""
    name = "npm.cmd" if os.name == "nt" else "npm"
    return shutil.which(name) or name


def extract_cursor_windows(archive: Path, destination: Path) -> Path:
    """Validate the complete Windows package before extracting any member."""
    required = {"cursor-agent.cmd", "cursor-agent.ps1", "node.exe", "index.js"}
    reserved = {"CON", "PRN", "AUX", "NUL"} | {
        f"{prefix}{number}" for prefix in ("COM", "LPT") for number in range(1, 10)
    }
    with zipfile.ZipFile(archive) as bundle:
        entries = bundle.infolist()
        if len(entries) > 10000 or sum(item.file_size for item in entries) > 1 << 30:
            raise RuntimeError("Cursor Windows archive exceeds extraction limits")
        names = set()
        files = set()
        for item in entries:
            name = item.filename
            path = PurePosixPath(name)
            mode = stat.S_IFMT(item.external_attr >> 16)
            if (
                not path.parts
                or path.is_absolute()
                or path.parts[0] != "dist-package"
                or "\\" in name
                or name != path.as_posix() + ("/" if item.is_dir() else "")
                or any(
                    part in {".", ".."}
                    or part.endswith((".", " "))
                    or any(ord(char) < 32 or char in ':*?"<>|' for char in part)
                    or part.split(".", 1)[0].upper() in reserved
                    for part in path.parts
                )
                or mode not in {0, stat.S_IFREG, stat.S_IFDIR}
                or (mode == stat.S_IFDIR and not item.is_dir())
                or (mode == stat.S_IFREG and item.is_dir())
            ):
                raise RuntimeError("Cursor Windows archive has an unsafe member")
            key = path.as_posix().casefold()
            if key in names:
                raise RuntimeError("Cursor Windows archive has duplicate members")
            names.add(key)
            if not item.is_dir():
                files.add(path.as_posix())
        if not {f"dist-package/{name}" for name in required}.issubset(files):
            raise RuntimeError(
                "Cursor Windows archive is missing its owned launch files"
            )
        file_keys = {name.casefold() for name in files}
        for name in names:
            path = PurePosixPath(name)
            if any(
                parent.as_posix().casefold() in file_keys for parent in path.parents
            ):
                raise RuntimeError("Cursor Windows archive has conflicting members")
        bundle.extractall(destination)
    return destination / "dist-package"


def install_kimi_macos(package: str, version: str) -> None:
    machine = host_platform.machine().lower()
    arch = "arm64" if machine in {"arm64", "aarch64"} else "x64"
    base = f"{package}/binaries/{version}"
    with urllib.request.urlopen(f"{base}/manifest.json", timeout=30) as response:
        manifest = json.loads(response.read())
    if manifest.get("version") != version:
        raise RuntimeError(
            "Kimi release manifest version does not match the supervised pin"
        )
    release = manifest["platforms"][f"darwin-{arch}"]
    filename = f"kimi-code-darwin-{arch}"
    if release.get("filename") != filename or not re.fullmatch(
        r"[0-9a-f]{64}", release.get("checksum", "")
    ):
        raise RuntimeError("Kimi release manifest has an invalid binary identity")
    destination = Path.home() / ".local/bin"
    destination.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(dir=destination) as directory:
        binary = Path(directory) / "kimi"
        archive = Path(directory) / "kimi.zip"
        url = f"https://github.com/MoonshotAI/kimi-code/releases/download/%40moonshot-ai/kimi-code%40{version}/{filename}.zip"
        run(
            [
                "curl",
                "-fsSL",
                "--connect-timeout",
                "20",
                "--max-time",
                "120",
                url,
                "-o",
                str(archive),
            ]
        )
        with zipfile.ZipFile(archive) as bundle:
            binary.write_bytes(bundle.read("kimi"))
        if hashlib.sha256(binary.read_bytes()).hexdigest() != release["checksum"]:
            raise RuntimeError("Kimi release checksum mismatch")
        binary.chmod(0o755)
        binary.replace(destination / "kimi")


def install(agent: dict[str, object], platform: str, runtime: str) -> str:
    version = str(agent["version"])
    installer = agent["installer"]
    kind, package = installer["kind"], installer["package"]
    if runtime == "wsl":
        if platform != "windows" or agent["id"] != "cursor":
            raise ValueError("WSL is reserved for Cursor on Windows")
        url = f"{package}/linux/x64/agent-cli-package.tar.gz"
        archive = f"/tmp/pitot-cursor-{version}.tar.gz"
        release = f"/usr/local/share/pitot-cursor/{version}"
        wsl = ["wsl.exe", "--distribution", "Ubuntu", "--"]
        run([*wsl, "curl", "-fsSL", url, "-o", archive])
        run([*wsl, "mkdir", "-p", release, "/usr/local/bin"])
        run([*wsl, "tar", "-xzf", archive, "-C", release, "--strip-components=1"])
        run([*wsl, "chmod", "0755", f"{release}/cursor-agent"])
        run([*wsl, "ln", "-sfn", f"{release}/cursor-agent", "/usr/local/bin/agent"])
        return f"{release}/cursor-agent"
    if kind == "npm":
        run(
            [
                npm_executable(),
                "install",
                "--global",
                "--ignore-scripts=false",
                f"{package}@{version}",
            ]
        )
    elif kind == "kimi_release":
        if platform == "macos":
            install_kimi_macos(package, version)
        elif platform == "windows":
            # Use the native Kimi Code installer. The legacy /install.ps1
            # endpoint installs the unrelated Python kimi-cli and ignores the
            # KIMI_VERSION contract used by the released Kimi Code binary.
            owned_directory = str(
                Path.home() / ".local/share/pitot-kimi" / version
            ).replace("'", "''")
            command = (
                f"$env:KIMI_VERSION='{version}'; "
                f"$env:KIMI_INSTALL_DIR='{owned_directory}'; "
                "irm https://code.kimi.com/kimi-code/install.ps1 | iex"
            )
            run(["powershell", "-NoProfile", "-NonInteractive", "-Command", command])
        else:
            with tempfile.TemporaryDirectory() as directory:
                target = Path(directory) / "install.sh"
                run(
                    [
                        "curl",
                        "-fsSL",
                        "https://code.kimi.com/kimi-code/install.sh",
                        "-o",
                        str(target),
                    ]
                )
                environment = {
                    **os.environ,
                    "KIMI_VERSION": version,
                    "KIMI_INSTALL_DIR": str(
                        Path.home() / ".local/share/pitot-kimi" / version
                    ),
                }
                run(["bash", str(target)], env=environment)
    elif kind == "cursor_release":
        os_name = {"ubuntu": "linux", "macos": "darwin", "windows": "windows"}[platform]
        machine = host_platform.machine().lower()
        arch = "arm64" if machine in {"arm64", "aarch64"} else "x64"
        suffix = "zip" if platform == "windows" else "tar.gz"
        url = f"{package}/{os_name}/{arch}/agent-cli-package.{suffix}"
        if platform == "windows":
            release = Path.home() / ".local/share/pitot-cursor" / version
            with tempfile.TemporaryDirectory() as directory:
                temporary = Path(directory)
                archive = temporary / "cursor.zip"
                run(
                    [
                        "curl",
                        "-fsSL",
                        "--connect-timeout",
                        "20",
                        "--max-time",
                        "120",
                        url,
                        "-o",
                        str(archive),
                    ]
                )
                payload = extract_cursor_windows(archive, temporary / "payload")
                release.parent.mkdir(parents=True, exist_ok=True)
                shutil.copytree(payload, release)
            return str(release / "cursor-agent.cmd")
        destination = Path.home() / ".local" / "bin"
        destination.mkdir(parents=True, exist_ok=True)
        with tempfile.TemporaryDirectory() as directory:
            archive = Path(directory) / "cursor.tar.gz"
            run(["curl", "-fsSL", url, "-o", str(archive)])
            run(["tar", "-xzf", str(archive), "-C", directory])
            candidates = [
                path for path in Path(directory).rglob("cursor-agent") if path.is_file()
            ]
            if len(candidates) != 1:
                raise RuntimeError(
                    f"Cursor archive contained {len(candidates)} agent executables"
                )
            release = Path.home() / ".local" / "share" / "pitot-cursor" / version
            shutil.copytree(candidates[0].parent, release, dirs_exist_ok=True)
            installed = release / "cursor-agent"
            installed.chmod(0o755)
            link = destination / "agent"
            temporary_link = destination / f".agent-{version}.tmp"
            if temporary_link.exists() or temporary_link.is_symlink():
                temporary_link.unlink()
            temporary_link.symlink_to(installed)
            temporary_link.replace(link)
    elif kind == "devin_release":
        manifest_url = f"{package}/{version}/manifest.json"
        with urllib.request.urlopen(manifest_url, timeout=30) as response:
            manifest = json.loads(response.read())
        if manifest.get("version") != version:
            raise RuntimeError(
                "Devin release manifest version does not match the supervised pin"
            )
        machine = host_platform.machine().lower()
        arch = "aarch64" if machine in {"arm64", "aarch64"} else "x86_64"
        suffix = {
            "ubuntu": "unknown-linux",
            "macos": "apple-darwin",
            "windows": "pc-windows",
        }[platform]
        target = f"{arch}-{suffix}"
        release = manifest.get("platforms", {}).get(target)
        if not isinstance(release, dict) or set(release) != {"url", "sha256"}:
            raise RuntimeError(f"Devin release manifest omitted {target}")
        with tempfile.TemporaryDirectory() as directory:
            archive = Path(directory) / (
                "devin.zip" if platform == "windows" else "devin.tar.gz"
            )
            with urllib.request.urlopen(release["url"], timeout=120) as response:
                archive.write_bytes(response.read())
            if hashlib.sha256(archive.read_bytes()).hexdigest() != release["sha256"]:
                raise RuntimeError("Devin release archive SHA-256 mismatch")
            executable_name = "devin.exe" if platform == "windows" else "devin"
            if platform == "windows":
                with zipfile.ZipFile(archive) as bundle:
                    matches = [
                        name
                        for name in bundle.namelist()
                        if Path(name).name == executable_name
                    ]
                    if len(matches) != 1:
                        raise RuntimeError(
                            f"Devin archive contained {len(matches)} executables"
                        )
                    executable_bytes = bundle.read(matches[0])
            else:
                with tarfile.open(archive, "r:gz") as bundle:
                    matches = [
                        member
                        for member in bundle.getmembers()
                        if member.isfile() and Path(member.name).name == executable_name
                    ]
                    if len(matches) != 1:
                        raise RuntimeError(
                            f"Devin archive contained {len(matches)} executables"
                        )
                    source = bundle.extractfile(matches[0])
                    if source is None:
                        raise RuntimeError("Devin executable could not be extracted")
                    executable_bytes = source.read()
            installed_dir = Path.home() / ".local" / "share" / "pitot-devin" / version
            installed_dir.mkdir(parents=True, exist_ok=True)
            installed = installed_dir / executable_name
            installed.write_bytes(executable_bytes)
            installed.chmod(0o755)
            destination = Path.home() / ".local" / "bin"
            destination.mkdir(parents=True, exist_ok=True)
            launcher = destination / executable_name
            shutil.copy2(installed, launcher)
            launcher.chmod(0o755)
    else:
        raise ValueError(f"unsupported installer: {kind}")
    return owned_entrypoint(agent, platform, runtime)


def executable_receipt(
    agent: dict[str, object], platform: str, runtime: str, path: str | None = None
) -> dict[str, object]:
    path = path if path is not None else owned_entrypoint(agent, platform, runtime)
    output, digest = probe_version(agent, path, runtime)
    return {
        "schema_version": 1,
        "agent": agent["id"],
        "version": agent["version"],
        "executable": path,
        "executable_sha256": digest,
        "version_output": output,
        "installer": agent["installer"]["kind"],
        "runtime": runtime,
        "platform": platform,
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--agent", required=True)
    parser.add_argument("--platform", required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    agent = next(
        (item for item in MANIFEST["agents"] if item["id"] == args.agent), None
    )
    if agent is None:
        raise SystemExit(f"unknown supervised agent: {args.agent}")
    runtime = agent["runtime"].get(args.platform)
    if runtime is None:
        raise SystemExit(f"unsupported platform: {args.platform}")
    path = install(agent, args.platform, runtime)
    receipt = executable_receipt(agent, args.platform, runtime, path)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(
        json.dumps(receipt, indent=2, sort_keys=True) + "\n", encoding="utf-8"
    )
    print(
        f"Installed {args.agent} {agent['version']} at {receipt['executable']} ({runtime})"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
