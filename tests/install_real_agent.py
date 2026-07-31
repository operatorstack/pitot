#!/usr/bin/env python3
"""Install and attest the exact released CLI declared by Pitot's supervisor."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import platform as host_platform
import shutil
import subprocess
import tarfile
import tempfile
import urllib.request
import zipfile


LAB = Path(__file__).resolve().parent.parent
ROOT = LAB.parents[1] if LAB.name == "15-pitot" else LAB
MANIFEST = json.loads((LAB / "adapter-verification.json").read_text(encoding="utf-8"))


def run(command: list[str], **kwargs: object) -> subprocess.CompletedProcess[str]:
    return subprocess.run(command, check=True, text=True, **kwargs)


def npm_executable() -> str:
    """Resolve npm's platform launcher instead of relying on PATHEXT in Python."""
    name = "npm.cmd" if os.name == "nt" else "npm"
    return shutil.which(name) or name


def install(agent: dict[str, object], platform: str, runtime: str) -> None:
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
        run([*wsl, "/usr/local/bin/agent", "--version"])
        return
    if kind == "npm":
        run([npm_executable(), "install", "--global", "--ignore-scripts=false", f"{package}@{version}"])
    elif kind == "kimi_release":
        if platform == "windows":
            # Use the native Kimi Code installer. The legacy /install.ps1
            # endpoint installs the unrelated Python kimi-cli and ignores the
            # KIMI_VERSION contract used by the released Kimi Code binary.
            command = (
                f"$env:KIMI_VERSION='{version}'; "
                "irm https://code.kimi.com/kimi-code/install.ps1 | iex"
            )
            run(["powershell", "-NoProfile", "-NonInteractive", "-Command", command])
        else:
            with tempfile.TemporaryDirectory() as directory:
                target = Path(directory) / "install.sh"
                run(["curl", "-fsSL", "https://code.kimi.com/kimi-code/install.sh", "-o", str(target)])
                environment = {**os.environ, "KIMI_VERSION": version}
                run(["bash", str(target)], env=environment)
    elif kind == "cursor_release":
        os_name = {"ubuntu": "linux", "macos": "darwin"}[platform]
        machine = host_platform.machine().lower()
        arch = "arm64" if machine in {"arm64", "aarch64"} else "x64"
        url = f"{package}/{os_name}/{arch}/agent-cli-package.tar.gz"
        destination = Path.home() / ".local" / "bin"
        destination.mkdir(parents=True, exist_ok=True)
        with tempfile.TemporaryDirectory() as directory:
            archive = Path(directory) / "cursor.tar.gz"
            run(["curl", "-fsSL", url, "-o", str(archive)])
            run(["tar", "-xzf", str(archive), "-C", directory])
            candidates = [path for path in Path(directory).rglob("cursor-agent") if path.is_file()]
            if len(candidates) != 1:
                raise RuntimeError(f"Cursor archive contained {len(candidates)} agent executables")
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
            raise RuntimeError("Devin release manifest version does not match the supervised pin")
        machine = host_platform.machine().lower()
        arch = "aarch64" if machine in {"arm64", "aarch64"} else "x86_64"
        suffix = {"ubuntu": "unknown-linux", "macos": "apple-darwin", "windows": "pc-windows"}[platform]
        target = f"{arch}-{suffix}"
        release = manifest.get("platforms", {}).get(target)
        if not isinstance(release, dict) or set(release) != {"url", "sha256"}:
            raise RuntimeError(f"Devin release manifest omitted {target}")
        with tempfile.TemporaryDirectory() as directory:
            archive = Path(directory) / ("devin.zip" if platform == "windows" else "devin.tar.gz")
            with urllib.request.urlopen(release["url"], timeout=120) as response:
                archive.write_bytes(response.read())
            if hashlib.sha256(archive.read_bytes()).hexdigest() != release["sha256"]:
                raise RuntimeError("Devin release archive SHA-256 mismatch")
            executable_name = "devin.exe" if platform == "windows" else "devin"
            if platform == "windows":
                with zipfile.ZipFile(archive) as bundle:
                    matches = [name for name in bundle.namelist() if Path(name).name == executable_name]
                    if len(matches) != 1:
                        raise RuntimeError(f"Devin archive contained {len(matches)} executables")
                    executable_bytes = bundle.read(matches[0])
            else:
                with tarfile.open(archive, "r:gz") as bundle:
                    matches = [member for member in bundle.getmembers() if member.isfile() and Path(member.name).name == executable_name]
                    if len(matches) != 1:
                        raise RuntimeError(f"Devin archive contained {len(matches)} executables")
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


def executable_receipt(agent: dict[str, object], platform: str, runtime: str) -> dict[str, object]:
    executable, version = str(agent["executable"]), str(agent["version"])
    if runtime == "wsl":
        resolve = (
            f"if command -v {executable} >/dev/null; then command -v {executable}; "
            f'elif [ -x "$HOME/.local/bin/{executable}" ]; then printf "%s\\n" "$HOME/.local/bin/{executable}"; '
            "else exit 1; fi"
        )
        path = run(["wsl.exe", "--distribution", "Ubuntu", "--", "bash", "-lc", resolve], stdout=subprocess.PIPE).stdout.strip()
        output = run(["wsl.exe", "--distribution", "Ubuntu", "--", "bash", "-lc", f"{path} --version"], stdout=subprocess.PIPE, stderr=subprocess.STDOUT).stdout.strip()
    else:
        path = ""
        if agent["installer"]["kind"] == "npm":
            prefix = Path(run([npm_executable(), "prefix", "--global"], stdout=subprocess.PIPE).stdout.strip())
            candidate = prefix / (f"{executable}.cmd" if os.name == "nt" else f"bin/{executable}")
            if candidate.is_file(): path = str(candidate)
        path = path or shutil.which(executable) or ""
        if not path:
            suffix = f"{executable}.exe" if os.name == "nt" else executable
            home_candidates = (
                Path.home() / ".local" / "bin" / suffix,
                Path.home() / ".kimi-code" / "bin" / suffix,
            )
            path = next((str(candidate) for candidate in home_candidates if candidate.is_file()), "")
        if not path:
            raise RuntimeError(f"installed executable {executable!r} is not on PATH")
        output = run([path, "--version"], stdout=subprocess.PIPE, stderr=subprocess.STDOUT).stdout.strip()
    if version not in output:
        raise RuntimeError(f"{executable} reported {output!r}, expected pinned version {version}")
    if runtime == "wsl":
        digest = run(
            ["wsl.exe", "--distribution", "Ubuntu", "--", "bash", "-lc", f"sha256sum {path} | cut -d' ' -f1"],
            stdout=subprocess.PIPE,
        ).stdout.strip()
    else:
        digest = hashlib.sha256(Path(path).read_bytes()).hexdigest()
    return {
        "schema_version": 1,
        "agent": agent["id"],
        "version": version,
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
    agent = next((item for item in MANIFEST["agents"] if item["id"] == args.agent), None)
    if agent is None:
        raise SystemExit(f"unknown supervised agent: {args.agent}")
    runtime = agent["runtime"].get(args.platform)
    if runtime is None:
        raise SystemExit(f"unsupported platform: {args.platform}")
    install(agent, args.platform, runtime)
    receipt = executable_receipt(agent, args.platform, runtime)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(receipt, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(f"Installed {args.agent} {agent['version']} at {receipt['executable']} ({runtime})")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
