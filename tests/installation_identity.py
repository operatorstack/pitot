"""Observe an owned installation tree; this is not publisher authentication."""

from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import stat
import subprocess

PRIORITY_AGENTS = {"codex", "qwen", "deepseek"}


TREE_PROFILE = "owned-installation/v1"
MAX_TREE_ENTRIES = 100_000
MAX_TREE_BYTES = 4 * 1024 * 1024 * 1024


MAX_VERSION_BYTES = 65536


class VersionProbeError(RuntimeError):
    """Retain bounded streams while keeping arbitrary CLI output out of logs."""

    def __init__(
        self,
        reason: str,
        stdout: bytes = b"",
        stderr: bytes = b"",
        exit_code: int | None = None,
    ):
        self.stdout = (stdout or b"")[:MAX_VERSION_BYTES]
        self.stderr = (stderr or b"")[:MAX_VERSION_BYTES]
        self.reason = reason
        self.exit_code = exit_code
        self.diagnostic = {
            "reason": reason,
            "exit_code": exit_code,
            "stdout": self._summary(stdout or b""),
            "stderr": self._summary(stderr or b""),
        }
        super().__init__(
            f"agent version probe {reason}; stdout_bytes={len(stdout or b'')}; stderr_bytes={len(stderr or b'')}; exit={exit_code}"
        )

    @staticmethod
    def _summary(data: bytes) -> dict[str, object]:
        captured = data[:MAX_VERSION_BYTES]
        return {
            "bytes": len(data),
            "captured_bytes": len(captured),
            "truncated": len(captured) != len(data),
            "sha256": hashlib.sha256(captured).hexdigest(),
        }


def persist_probe_error(error: VersionProbeError, output: Path) -> None:
    output.parent.mkdir(parents=True, exist_ok=True)
    for name, data in (("stdout", error.stdout), ("stderr", error.stderr)):
        private = output.with_suffix(f".probe-private-{name}")
        descriptor = os.open(private, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(descriptor, "wb") as stream:
            stream.write(data)
    output.with_suffix(".probe.json").write_text(
        json.dumps(error.diagnostic, indent=2) + "\n", encoding="utf-8"
    )


def node_probe(command: list[str]) -> subprocess.CompletedProcess:
    try:
        return subprocess.run(
            command,
            check=True,
            capture_output=True,
            timeout=30,
            env=node_environment(dict(os.environ)),
        )
    except (subprocess.CalledProcessError, subprocess.TimeoutExpired) as error:
        raise VersionProbeError(
            "node-timed-out"
            if isinstance(error, subprocess.TimeoutExpired)
            else "node-nonzero-exit",
            error.stdout,
            error.stderr,
            getattr(error, "returncode", None),
        ) from None


def node_output(result: subprocess.CompletedProcess) -> str:
    if len(result.stdout) + len(result.stderr) > MAX_VERSION_BYTES:
        raise VersionProbeError("node-output-limit", result.stdout, result.stderr)
    try:
        value = result.stdout.decode("utf-8", errors="strict").strip()
    except UnicodeDecodeError:
        raise VersionProbeError(
            "node-invalid-encoding", result.stdout, result.stderr
        ) from None
    if result.stderr:
        raise VersionProbeError("node-unexpected-stderr", result.stdout, result.stderr)
    return value


def node_entrypoint(node: str, entry: str) -> str:
    result = node_probe(
        [
            node,
            "-e",
            "console.log(require('node:fs').realpathSync(process.argv[1]))",
            entry,
        ]
    )
    value = node_output(result)
    if result.stderr or not Path(value).is_absolute() or "\0" in value:
        raise VersionProbeError("node-invalid-entrypoint", result.stdout, result.stderr)
    return value


def installation_prefix(agent: dict[str, object]) -> Path:
    home = Path.home()
    if os.name != "nt":
        home = home.resolve(strict=True)
    return home / ".local/share/pitot-agents" / str(agent["id"]) / str(agent["version"])


def prepare_installation(prefix: Path) -> None:
    """Reject redirected ancestors before npm can create installation effects."""
    for parent in reversed(prefix.parents):
        try:
            info = parent.lstat()
        except FileNotFoundError:
            parent.mkdir()
            info = parent.lstat()
        _reject_reparse(info)
        if not stat.S_ISDIR(info.st_mode):
            raise RuntimeError("installation parent is not a regular directory")
    prefix.mkdir()  # Exclusive: a previous or partial installation is not fresh.


def package_entrypoint(agent: dict[str, object], prefix: Path) -> Path:
    package = prefix / "node_modules" / str(agent["installer"]["package"])
    metadata = json.loads((package / "package.json").read_text(encoding="utf-8"))
    if (
        metadata.get("name") != agent["installer"]["package"]
        or metadata.get("version") != agent["version"]
    ):
        raise RuntimeError("installed package does not match the supervised pin")
    bins = metadata.get("bin")
    entry = bins.get(str(agent["executable"])) if isinstance(bins, dict) else bins
    if not isinstance(entry, str) or not entry or "\0" in entry:
        raise RuntimeError("installed package has no supported JS entrypoint")
    selected = (package / entry).resolve(strict=True)
    if (
        not selected.is_relative_to(prefix.resolve(strict=True))
        or selected.suffix != ".js"
        or not selected.is_file()
    ):
        raise RuntimeError("installed JS entrypoint escapes the owned prefix")
    return selected


def node_environment(environment: dict[str, str]) -> dict[str, str]:
    return {
        key: value
        for key, value in environment.items()
        if key.upper() not in {"NODE_OPTIONS", "NODE_PATH", "NODE_COMPILE_CACHE"}
    }


def file_digest(path: Path) -> str:
    if not path.is_absolute() or not path.is_file():
        raise RuntimeError("runtime path must select an absolute regular file")
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def observe_installation(agent: dict[str, object]) -> dict[str, object]:
    prefix = installation_prefix(agent)
    tree = tree_digest(prefix)
    entry = package_entrypoint(agent, prefix)
    selected = shutil.which("node.exe" if os.name == "nt" else "node")
    if selected is None:
        raise RuntimeError("Node runtime is unavailable")
    # Version managers can expose a symlink to a multi-call launcher. Resolving
    # that symlink and invoking its target would run the manager, not Node.
    located = node_probe([selected, "-p", "process.execPath"])
    runtime_path = node_output(located)
    if (
        located.stderr
        or not runtime_path
        or "\0" in runtime_path
        or not Path(runtime_path).is_absolute()
    ):
        raise VersionProbeError(
            "node-invalid-runtime-path", located.stdout, located.stderr
        )
    try:
        node = Path(runtime_path).resolve(strict=True)
        digest = file_digest(node)
    except (OSError, RuntimeError):
        raise VersionProbeError(
            "node-invalid-runtime-path", located.stdout, located.stderr
        ) from None
    result = node_probe([str(node), "--version"])
    version = node_output(result)
    if result.stderr or not re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+", version):
        raise VersionProbeError(
            "node-invalid-version-banner", result.stdout, result.stderr
        )
    value = {
        "strategy": "absolute-node-js/v1",
        "prefix": str(prefix),
        "entrypoint": node_entrypoint(str(node), str(entry)),
        "tree": tree,
        "node": {"path": str(node), "sha256": digest, "version": version},
    }
    verify_installation(value)
    return value


def verify_installation(value: object) -> None:
    if (
        not isinstance(value, dict)
        or set(value) != {"strategy", "prefix", "entrypoint", "tree", "node"}
        or value["strategy"] != "absolute-node-js/v1"
    ):
        raise RuntimeError("invalid installed-code identity")
    for field in ("prefix", "entrypoint"):
        if (
            not isinstance(value[field], str)
            or "\0" in value[field]
            or not Path(value[field]).is_absolute()
        ):
            raise RuntimeError("invalid installation path")
    node = value["node"]
    if (
        not isinstance(node, dict)
        or set(node) != {"path", "sha256", "version"}
        or not all(isinstance(x, str) and x and "\0" not in x for x in node.values())
    ):
        raise RuntimeError("invalid Node identity")
    if not re.fullmatch(r"[0-9a-f]{64}", node["sha256"]) or not re.fullmatch(
        r"v[0-9]+\.[0-9]+\.[0-9]+", node["version"]
    ):
        raise RuntimeError("invalid Node identity digest or version")
    tree = value["tree"]
    if (
        not isinstance(tree, dict)
        or set(tree) != {"profile", "metadata", "sha256", "entries", "bytes"}
        or tree["profile"] != TREE_PROFILE
        or tree["metadata"] != ("windows-readonly" if os.name == "nt" else "posix-mode")
        or not isinstance(tree["sha256"], str)
        or not re.fullmatch(r"[0-9a-f]{64}", tree["sha256"])
        or type(tree["entries"]) is not int
        or not 1 <= tree["entries"] <= MAX_TREE_ENTRIES
        or type(tree["bytes"]) is not int
        or not 0 <= tree["bytes"] <= MAX_TREE_BYTES
    ):
        raise RuntimeError("invalid installation tree identity")
    if tree_digest(Path(value["prefix"])) != value["tree"]:
        raise RuntimeError("installed package tree changed since installation receipt")
    entry = Path(value["entrypoint"])
    if (
        not entry.resolve(strict=True).is_relative_to(
            Path(value["prefix"]).resolve(strict=True)
        )
        or entry.suffix != ".js"
        or not entry.is_file()
    ):
        raise RuntimeError("invalid installed JS entrypoint")

    if file_digest(Path(node["path"])) != node["sha256"]:
        raise RuntimeError("Node runtime changed since installation receipt")
    result = node_probe([node["path"], "--version"])
    if result.stderr or node_output(result) != node["version"]:
        raise VersionProbeError(
            "Node version changed since installation receipt",
            result.stdout,
            result.stderr,
        )
    if node_entrypoint(node["path"], value["entrypoint"]) != value["entrypoint"]:
        raise RuntimeError("JS entrypoint differs from Node canonical path")
    if file_digest(Path(node["path"])) != node["sha256"]:
        raise RuntimeError("Node runtime changed during version observation")


def installation_command(value: dict[str, object]) -> list[str]:
    return [value["node"]["path"], value["entrypoint"]]


def identity_evidence(receipt: dict[str, object]) -> dict[str, str]:
    """Project observed identity without publishing runner-local paths."""
    if receipt["agent"] not in PRIORITY_AGENTS:
        return {"scope": "entrypoint/v1"}
    value = receipt["installation"]
    return {
        "scope": "owned-prefix-and-node/v1",
        "strategy": value["strategy"],
        "tree_profile": value["tree"]["profile"],
        "metadata": value["tree"]["metadata"],
        "tree_sha256": value["tree"]["sha256"],
        "node_sha256": value["node"]["sha256"],
        "node_version": value["node"]["version"],
        "entrypoint": Path(value["entrypoint"])
        .relative_to(Path(value["prefix"]).resolve())
        .as_posix(),
    }


def validate_identity_evidence(agent: str, platform: str, value: object) -> None:
    """Validate a portable observation, without executing or reading its paths."""
    if agent not in PRIORITY_AGENTS:
        if value != {"scope": "entrypoint/v1"}:
            raise ValueError("invalid entrypoint identity scope")
        return
    if (
        not isinstance(value, dict)
        or set(value)
        != {
            "scope",
            "strategy",
            "tree_profile",
            "metadata",
            "tree_sha256",
            "node_sha256",
            "node_version",
            "entrypoint",
        }
        or not all(
            isinstance(item, str) and item and "\0" not in item
            for item in value.values()
        )
        or value["scope"] != "owned-prefix-and-node/v1"
        or value["strategy"] != "absolute-node-js/v1"
        or value["tree_profile"] != TREE_PROFILE
        or value["metadata"]
        != ("windows-readonly" if platform == "windows" else "posix-mode")
        or not re.fullmatch(r"[0-9a-f]{64}", value["tree_sha256"])
        or not re.fullmatch(r"[0-9a-f]{64}", value["node_sha256"])
        or not re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+", value["node_version"])
    ):
        raise ValueError("invalid installed-code identity evidence")
    entry = PurePosixPath(value["entrypoint"])
    if (
        entry.as_posix() != value["entrypoint"]
        or entry.is_absolute()
        or ".." in entry.parts
        or "\\" in value["entrypoint"]
        or not entry.parts
        or entry.parts[0] != "node_modules"
        or entry.suffix != ".js"
    ):
        raise ValueError("invalid portable package entrypoint")


def installation_environment(
    agent: str, value: dict[str, object], environment: dict[str, str]
) -> dict[str, str]:
    result = node_environment(environment)
    if agent == "qwen":
        home = result.get("QWEN_HOME")
        if not home or not Path(home).is_absolute():
            raise RuntimeError("Qwen execution requires an owned absolute home")
        result["QWEN_CODE_MANAGED_NPM_PIN"] = json.dumps(
            {
                "bootstrap": value["entrypoint"],
                "version": None,
                "updateRoot": str(Path(home) / "updates/npm"),
            }
        )
    return result


def _unchanged(
    before: os.stat_result, after: os.stat_result, *, handle: bool = False
) -> bool:
    fields = ["st_dev", "st_ino", "st_size", "st_mtime_ns", "st_ctime_ns"]
    # Windows path stat adds execute bits for certain filename extensions;
    # descriptor stat cannot apply that rule. Recheck path attributes separately.
    if os.name != "nt" or not handle:
        fields.extend(("st_mode", "st_file_attributes", "st_reparse_tag"))
    return all(
        getattr(before, field, None) == getattr(after, field, None) for field in fields
    )


def _reject_reparse(info: os.stat_result) -> None:
    if getattr(info, "st_file_attributes", 0) & stat.FILE_ATTRIBUTE_REPARSE_POINT:
        raise RuntimeError("installation contains a Windows reparse point")


def _metadata(info: os.stat_result) -> int:
    if os.name == "nt":
        # Windows mode bits are synthesized. Bind readonly, not an invented ACL.
        return info.st_file_attributes & stat.FILE_ATTRIBUTE_READONLY
    return stat.S_IMODE(info.st_mode)


def tree_digest(root: Path) -> dict[str, object]:
    """Bind names, types, metadata and bytes, without following directory links.

    File links used by npm must resolve within the installation. Directory links
    are outside this profile. Before/after observations detect preserved changes;
    they do not provide an atomic snapshot or prevent concurrent replacement.
    """
    if not root.is_absolute():
        raise RuntimeError("installation root must be absolute")
    try:
        requested_root = root
        initial = root.lstat()
        _reject_reparse(initial)
        if not stat.S_ISDIR(initial.st_mode):
            raise RuntimeError("installation root must be a regular directory")
        ancestors: list[tuple[Path, os.stat_result]] = []
        if os.name == "nt":
            for parent in root.parents:
                info = parent.lstat()
                _reject_reparse(info)
                ancestors.append((parent, info))
        root = root.resolve(strict=True)

        def check_root() -> None:
            if not _unchanged(initial, requested_root.lstat()):
                raise RuntimeError("installation root changed during observation")
            for parent, before in ancestors:
                after = parent.lstat()
                _reject_reparse(after)
                # Ancestor directory contents may change independently. Retain
                # its location and type without claiming ownership of siblings.
                if any(
                    getattr(before, field, None) != getattr(after, field, None)
                    for field in ("st_dev", "st_ino", "st_mode", "st_reparse_tag")
                ):
                    raise RuntimeError(
                        "installation ancestor changed during observation"
                    )

        check_root()
        records: list[list[object]] = []
        observations: list[tuple[Path, os.stat_result]] = []
        total_bytes = 0

        def visit(path: Path) -> None:
            nonlocal total_bytes
            if len(records) >= MAX_TREE_ENTRIES:
                raise RuntimeError("installation exceeds entry limit")
            before = path.lstat()
            _reject_reparse(before)
            name = path.relative_to(root).as_posix()
            name.encode("utf-8", errors="strict")
            observations.append((path, before))
            metadata = _metadata(before)
            if stat.S_ISDIR(before.st_mode):
                records.append([name, "directory", metadata])
                for child in sorted(path.iterdir(), key=lambda item: item.name):
                    visit(child)
            elif stat.S_ISLNK(before.st_mode):
                target = os.readlink(path)
                target.encode("utf-8", errors="strict")
                resolved = path.resolve(strict=True)
                if not resolved.is_relative_to(root) or not resolved.is_file():
                    raise RuntimeError("installation link must select an internal file")
                records.append([name, "symlink", metadata, target])
            elif stat.S_ISREG(before.st_mode):
                total_bytes += before.st_size
                if total_bytes > MAX_TREE_BYTES:
                    raise RuntimeError("installation exceeds byte limit")
                digest = hashlib.sha256()
                count = 0
                with path.open("rb") as stream:
                    if not _unchanged(before, os.fstat(stream.fileno()), handle=True):
                        raise RuntimeError("installation changed while opening a file")
                    for block in iter(lambda: stream.read(1024 * 1024), b""):
                        count += len(block)
                        if count > before.st_size:
                            raise RuntimeError(
                                "installation file grew during observation"
                            )
                        digest.update(block)
                    if count != before.st_size or not _unchanged(
                        before, os.fstat(stream.fileno()), handle=True
                    ):
                        raise RuntimeError(
                            "installation changed during file observation"
                        )
                records.append([name, "file", metadata, count, digest.hexdigest()])
            else:
                raise RuntimeError("installation contains an unsupported file type")
            if not _unchanged(before, path.lstat()):
                raise RuntimeError("installation changed during tree observation")

        visit(root)
        for path, before in observations:
            if not _unchanged(before, path.lstat()):
                raise RuntimeError("installation changed during tree observation")
        check_root()
        profile = "windows-readonly" if os.name == "nt" else "posix-mode"
        canonical = json.dumps(
            [TREE_PROFILE, profile, records], ensure_ascii=True, separators=(",", ":")
        ).encode("ascii")
        return {
            "profile": TREE_PROFILE,
            "metadata": profile,
            "sha256": hashlib.sha256(canonical).hexdigest(),
            "entries": len(records),
            "bytes": total_bytes,
        }
    except (OSError, UnicodeError, ValueError) as error:
        raise RuntimeError("cannot observe the complete installation tree") from error
