#!/usr/bin/env python3
"""Run one released agent through prompt, model, hook, Pitot, and tool result."""

from __future__ import annotations

import argparse
import base64
from datetime import datetime
from contextlib import ExitStack
import hashlib
from itertools import islice
import json
import os
from pathlib import Path, PureWindowsPath
import secrets
import re
import shlex
import shutil
import subprocess
import stat
import sys
import tempfile
import time
import urllib.parse

from installation_identity import (
    VersionProbeError,
    persist_probe_error,
    installation_command,
    installation_environment,
    identity_evidence,
)

from agent_identity import (
    agent_environment,
    cursor_context,
    devin_context,
    inspect_receipt,
    verify_live_entrypoint,
)


LAB = Path(__file__).resolve().parent.parent
ROOT = LAB
MANIFEST = json.loads((LAB / "adapter-verification.json").read_text(encoding="utf-8"))
ENDPOINT_PROVENANCE = json.loads(
    (LAB / "tests/endpoint-provenance.json").read_text(encoding="utf-8")
)


def write_json(path: Path, value: object) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2) + "\n", encoding="utf-8")


def cursor_failure_diagnostics(
    proxy_receipt: Path, carrier_root: Path, nonce: str
) -> str:
    def bounded_read(path: Path, limit: int) -> str:
        try:
            if not stat.S_ISREG(path.lstat().st_mode):
                return "<nonregular file refused>"
            with path.open("rb") as stream:
                raw = stream.read(limit + 1)
            if len(raw) > limit:
                return "<oversized file>"
            return raw.decode("utf-8", errors="replace")
        except OSError as error:
            return f"<read failed: {type(error).__name__}>"

    def fields(value: object, keys: tuple[str, ...]) -> dict[str, object]:
        if not isinstance(value, dict):
            return {}
        types = {
            "scope": str,
            "code": str,
            "path": str,
            "content_type": str,
            "variant": str,
            "error_class": str,
            "reason_shape": str,
            "validation_kind": str,
            "reason_location": str,
            "approval_origin": str,
            "fail_closed": bool,
            "unsafe_cwd_shape": bool,
            "cwd_slot_empty": bool,
            "routed_protocol": str,
            "source_sha": str,
            "client_message_kind": str,
            "nonce_present": bool,
            "canary_result_present": bool,
            "issued_phase_id_match": bool,
            "issued_exact_command_match": bool,
            "nonce_bound": bool,
            "exec_id_present": bool,
            "exec_id_match": bool,
            "is_readonly": bool,
            "allow_canary_marker": bool,
            "signature_partial_match": bool,
        }
        enums = {
            "reason_shape": {
                "other",
                "malformed",
                "ambiguous",
                "hook_block_shape",
                "hook_output_validation_shape",
                "hook_execution_error_shape",
                "unsafe_cwd_resolution_shape",
                "generic_native_block_ambiguous",
                "native_abort_shape",
            },
            "validation_kind": {"json", "response"},
            "reason_location": {"direct", "hookdetail"},
            "approval_origin": {"unknown_from_reason"},
        }
        return {
            key: value[key][:512] if isinstance(value[key], str) else value[key]
            for key in keys
            if key in value
            and type(value[key])
            is (str if key.endswith("_sha256") else types.get(key, int))
            and (
                not key.endswith("_sha256") or re.fullmatch(r"[0-9a-f]{64}", value[key])
            )
            and (key not in enums or value[key] in enums[key])
        }

    state: dict[str, object] = {}
    try:
        receipt = json.loads(bounded_read(proxy_receipt, 1048576))
        if not isinstance(receipt, dict):
            raise ValueError("invalid receipt")
        for key in (
            "initial_prompt_observed",
            "allow_tool_call_response_emitted",
            "allow_tool_result_observed",
            "deny_tool_call_response_emitted",
            "denied_result_observed",
            "final_response_emitted",
        ):
            if type(receipt.get(key)) is bool:
                state[key] = receipt[key]
        state["response_attempts"] = fields(
            receipt.get("response_attempts"), ("allow", "deny", "final")
        )
        for key, names in (
            (
                "transport_errors",
                (
                    "scope",
                    "code",
                    "connection_id",
                    "stream_id",
                    "attempt_id",
                    "event_sequence",
                ),
            ),
            ("cursor_requests", ("path", "content_type", "request_bytes")),
            (
                "native_shell_results",
                (
                    "source_sha",
                    "frame_length",
                    "frame_sha256",
                    "connection_id",
                    "stream_id",
                    "attempt_id",
                    "event_sequence",
                    "native_id",
                    "variant",
                    "issued_phase_id_match",
                    "command_sha256",
                    "issued_exact_command_match",
                    "nonce_bound",
                    "exec_id_present",
                    "exec_id_sha256",
                    "exec_id_match",
                    "exit_code",
                    "timeout_ms",
                    "is_readonly",
                    "stdout_bytes",
                    "stdout_sha256",
                    "stderr_bytes",
                    "stderr_sha256",
                    "reason_bytes",
                    "reason_sha256",
                    "error_class",
                    "reason_shape",
                    "validation_kind",
                    "reason_location",
                    "approval_origin",
                    "fail_closed",
                    "unsafe_cwd_shape",
                    "cwd_slot_empty",
                    "hook_detail_bytes",
                    "hook_detail_sha256",
                    "allow_canary_marker",
                ),
            ),
            (
                "front_connections",
                (
                    "connection_id",
                    "first_data_bytes",
                    "signature_partial_match",
                    "routed_protocol",
                    "event_sequence",
                ),
            ),
            (
                "native_control_results",
                (
                    "source_sha",
                    "frame_length",
                    "frame_sha256",
                    "connection_id",
                    "stream_id",
                    "attempt_id",
                    "event_sequence",
                    "native_id",
                    "variant",
                    "issued_phase_id_match",
                    "nonce_present",
                    "error_class",
                    "error_bytes",
                    "error_sha256",
                    "stack_trace_bytes",
                    "stack_trace_sha256",
                    "error_code_bytes",
                    "error_code_sha256",
                ),
            ),
        ):
            values = receipt.get(key, [])
            state[key] = (
                [
                    fields(value, names)
                    for value in values[
                        -(
                            8
                            if key in {"native_shell_results", "native_control_results"}
                            else 20
                        ) :
                    ]
                ]
                if isinstance(values, list)
                else []
            )
        state["cursor_run"] = fields(
            receipt.get("cursor_run"),
            (
                "path",
                "content_type",
                "request_bytes",
                "connection_id",
                "stream_id",
                "attempt_id",
            ),
        )
        inbound = receipt.get("cursor_inbound", {})
        state["cursor_inbound"] = fields(inbound, ("request_bytes",))
        envelopes = inbound.get("envelopes", []) if isinstance(inbound, dict) else []
        state["cursor_inbound"]["envelopes"] = (
            [
                fields(
                    value,
                    (
                        "flags",
                        "length",
                        "nonce_present",
                        "canary_result_present",
                        "client_message_kind",
                    ),
                )
                for value in envelopes[-20:]
            ]
            if isinstance(envelopes, list)
            else []
        )
    except (ValueError, TypeError):
        state["receipt"] = "missing, malformed or oversized"
    carrier = {}
    for phase in ("allow", "deny"):
        carrier[phase] = {
            "draft_present": (carrier_root / f"{nonce}-{phase}.draft.json").is_file(),
            "final_present": (carrier_root / f"{nonce}-{phase}.json").is_file(),
            "errors": [
                bounded_read(path, 1024)
                for path in islice(carrier_root.glob(f"{nonce}-{phase}-*.error.txt"), 2)
            ],
        }
    state["carrier"] = carrier
    text = json.dumps(state, ensure_ascii=False, sort_keys=True)
    for key in (
        "cursor_requests",
        "front_connections",
        "transport_errors",
        "native_shell_results",
        "native_control_results",
    ):
        while len(text) > 12000 and len(state.get(key, [])) > 1:
            state[key].pop(0)
            state["diagnostics_trimmed"] = True
            text = json.dumps(state, ensure_ascii=False, sort_keys=True)
    return text[:12000]


def hook_group(event: str, matcher: str, command: str) -> dict[str, object]:
    return {
        "hooks": {
            event: [
                {
                    "matcher": matcher,
                    "hooks": [{"name": "pitot", "type": "command", "command": command}],
                }
            ]
        }
    }


def wsl_path(path: Path) -> str:
    """Map a resolved Windows runner path through WSL's drive automount."""
    native = str(path.resolve()).replace("\\", "/")
    matched = re.fullmatch(r"([A-Za-z]):(/.*)", native)
    if matched is None:
        raise RuntimeError(f"cannot map native path into WSL: {native!r}")
    return f"/mnt/{matched.group(1).lower()}{matched.group(2)}"


def windows_host() -> bool:
    return os.name == "nt"


def render_canary_command(executable: str, receipt: str, native_windows: bool) -> str:
    if native_windows:
        executable = executable.replace("\\", "/")
        receipt = receipt.replace("\\", "/")
        return subprocess.list2cmdline(
            [executable, "--role", "canary", "--receipt", receipt]
        )
    return shlex.join([executable, "--role", "canary", "--receipt", receipt])


def cursor_powershell() -> str:
    """Require the same modern shell selected by Cursor's Windows hook runner."""
    executable = shutil.which("pwsh")
    if not executable or not Path(executable).is_absolute():
        raise RuntimeError("Cursor native Windows verification requires PowerShell 7")
    result = subprocess.run(
        [
            executable,
            "-NoLogo",
            "-NoProfile",
            "-NonInteractive",
            "-Command",
            "$PSVersionTable.PSVersion.Major",
        ],
        check=True,
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        timeout=30,
    )
    if (
        result.stderr
        or not re.fullmatch(rb"[0-9]{1,3}\r?\n?", result.stdout)
        or int(result.stdout) < 7
    ):
        raise RuntimeError("Cursor native Windows verification requires PowerShell 7")
    return executable


def powershell_quote(value: str) -> str:
    return "'" + value.replace("'", "''") + "'"


def render_cursor_canary_command(executable: str, receipt: str) -> str:
    """Invoke a native Windows executable as a PowerShell literal command."""
    arguments = [
        executable.replace("\\", "/"),
        "--role",
        "canary",
        "--receipt",
        receipt.replace("\\", "/"),
    ]
    return "& " + " ".join(powershell_quote(value) for value in arguments)


def configure(
    agent: str,
    home: Path,
    project: Path,
    witness_command: str,
    proxy: str,
    *,
    pitot_command: str,
    witness_receipt: str,
    nonce: str,
    runtime_path: str,
    cursor_carrier: dict[str, str] | None = None,
) -> tuple[list[str], dict[str, str]]:
    env: dict[str, str] = {
        "HOME": str(home),
        "USERPROFILE": str(home),
        "OPENAI_API_KEY": "pitot-local-only",
        "ANTHROPIC_API_KEY": "pitot-local-only",
        "GEMINI_API_KEY": "pitot-local-only",
        "GOOGLE_API_KEY": "pitot-local-only",
        "PITOT_BIN": witness_command,
        "PITOT_RUNTIME": runtime_path,
    }
    # Gemini, Codex, and Copilot execute command hooks through PowerShell on
    # Windows. A quoted executable is only a string there; the call operator is
    # required to invoke it. Explicit receipt arguments also survive the hosts'
    # intentionally reduced hook environments.
    witness_invocation = (
        f'& "{witness_command}"' if windows_host() else f'"{witness_command}"'
    )
    witnessed = (
        f'{witness_invocation} --real-bin "{pitot_command}" '
        f'--receipt "{witness_receipt}" --nonce "{nonce}"'
    )
    witnessed_direct = (
        f'"{witness_command}" --real-bin "{pitot_command}" '
        f'--receipt "{witness_receipt}" --nonce "{nonce}"'
    )
    prompt_flag: list[str]
    if agent == "claude":
        write_json(
            home / ".claude/settings.json",
            hook_group(
                "PreToolUse",
                "Bash",
                f'{witnessed_direct} hook claude --runtime "{runtime_path}"',
            ),
        )
        env["ANTHROPIC_BASE_URL"] = proxy
        env["CLAUDE_CODE_MAX_RETRIES"] = "0"
        prompt_flag = [
            "--print",
            "--dangerously-skip-permissions",
            "--tools",
            "Bash",
            "--model",
            "sonnet",
        ]
    elif agent == "codex":
        if windows_host():
            bridge = LAB / "integrations/codex/PreToolUse.ps1"
            hook_command = (
                f'& "{bridge}" '
                f'-Pitot "{witness_command}" -RealBin "{pitot_command}" '
                f'-Receipt "{witness_receipt}" -Nonce "{nonce}" -Runtime "{runtime_path}"'
            )
        else:
            hook_command = (
                f'{witnessed} hook codex --runtime "{runtime_path}" >/dev/null'
            )
        write_json(
            home / ".codex/hooks.json", hook_group("PreToolUse", "Bash", hook_command)
        )
        (home / ".codex/config.toml").write_text(
            f'model = "pitot-control"\nmodel_provider = "pitot"\n[model_providers.pitot]\nname = "Pitot local control"\nbase_url = "{proxy}/v1"\nenv_key = "OPENAI_API_KEY"\nwire_api = "responses"\n',
            encoding="utf-8",
        )
        env["CODEX_HOME"] = str(home / ".codex")
        prompt_flag = [
            "exec",
            "--dangerously-bypass-approvals-and-sandbox",
            "--dangerously-bypass-hook-trust",
            "--model",
            "pitot-control",
        ]
    elif agent == "devin":
        env.update(devin_context(home))
        endpoint = urllib.parse.urlparse(proxy)
        app_data = home / "AppData/Roaming"
        local_app_data = home / "AppData/Local"
        app_data.mkdir(parents=True, exist_ok=True)
        local_app_data.mkdir(parents=True, exist_ok=True)
        credentials = home / ".local/share/devin/credentials.toml"
        credentials.parent.mkdir(parents=True, exist_ok=True)
        credentials.write_text(
            'windsurf_api_key = "pitot-local-only"\n'
            f"api_server_url = {json.dumps(proxy)}\n"
            f"devin_webapp_host = {json.dumps(endpoint.netloc)}\n"
            f"devin_api_url = {json.dumps(proxy)}\n",
            encoding="utf-8",
        )
        env.update(
            {
                "WINDSURF_API_KEY": "pitot-local-only",
                "WINDSURF_API_SERVER_URL": proxy,
                "XDG_CONFIG_HOME": str(home / ".config"),
                "XDG_DATA_HOME": str(home / ".local/share"),
                "APPDATA": str(app_data),
                "LOCALAPPDATA": str(local_app_data),
            }
        )
        prompt_flag = []
    elif agent == "copilot":
        hooks = home / ".copilot/hooks"
        hooks.mkdir(parents=True, exist_ok=True)
        source = (
            LAB
            / "integrations/copilot"
            / ("PreToolUse.ps1" if windows_host() else "PreToolUse")
        )
        target = hooks / source.name
        shutil.copy2(source, target)
        target.chmod(0o755)
        hook_command = (
            f'powershell -NoProfile -NonInteractive -File "{target}"'
            if windows_host()
            else str(target)
        )
        write_json(
            home / ".copilot/settings.json",
            hook_group("PreToolUse", "Bash", hook_command),
        )
        env["PITOT_BIN"] = witness_command
        env.update(
            {
                "COPILOT_PROVIDER_BASE_URL": f"{proxy}/v1",
                "COPILOT_PROVIDER_API_KEY": "pitot-local-only",
                "COPILOT_PROVIDER_TYPE": "openai",
                "COPILOT_PROVIDER_WIRE_API": "completions",
                "COPILOT_PROVIDER_MODEL_ID": "gpt-4o",
                "COPILOT_PROVIDER_WIRE_MODEL": "pitot-control",
                "COPILOT_MODEL": "gpt-4o",
                "COPILOT_OFFLINE": "true",
                "COPILOT_HOME": str(home / ".copilot"),
            }
        )
        prompt_flag = [
            "--allow-all-tools",
            "--model",
            "gpt-4o",
            "--no-auto-update",
            "--no-remote",
        ]
    elif agent == "cursor":
        name = "beforeShellExecution.ps1" if windows_host() else "beforeShellExecution"
        source = LAB / "integrations/cursor" / name
        target = project / ".cursor/hooks" / name
        target.parent.mkdir(parents=True, exist_ok=True)
        # Canonical bytes keep the Unix shebang valid when the source was
        # checked out on Windows; the native PowerShell bridge uses UTF-8.
        target.write_bytes(source.read_bytes().replace(b"\r\n", b"\n"))
        target.chmod(0o755)
        if windows_host():
            shell = cursor_powershell()
            hook_command = (
                "& "
                + powershell_quote(shell)
                + " -NoLogo -NoProfile -NonInteractive -File "
                + powershell_quote(str(target))
            )
            for key, value in (
                ("Pitot", witness_command),
                ("RealBin", pitot_command),
                ("Receipt", witness_receipt),
                ("Nonce", nonce),
                ("Runtime", runtime_path),
            ):
                hook_command += f" -{key} " + powershell_quote(value)
            env["PATH"] = (
                str(Path(shell).parent) + os.pathsep + os.environ.get("PATH", "")
            )
            env["SHELL"] = shell
            if cursor_carrier is not None:
                hook_command += " -VerifyCarrier"
                env.update(cursor_carrier)
                env["PITOT_CURSOR_PARENT_SHELL"] = shell
                env["PITOT_CURSOR_PARENT_SHELL_SHA256"] = hashlib.sha256(
                    Path(shell).read_bytes()
                ).hexdigest()
                env["PITOT_CURSOR_VERIFICATION_HOOK_COMMAND"] = hook_command
        else:
            hook_command = (
                f'"{target}" "{witness_command}" "{pitot_command}" '
                f'"{witness_receipt}" "{nonce}" "{runtime_path}"'
            )
        write_json(
            project / ".cursor/hooks.json",
            {
                "version": 1,
                "hooks": {
                    "beforeShellExecution": [
                        {"command": hook_command, "failClosed": True}
                    ]
                },
            },
        )
        # Cursor exposes an authless CLI mode for endpoint compatibility tests;
        # model inference is still supplied only by the pinned local endpoint.
        env["CURSOR_AGENT_CLI_AUTHLESS_MODE"] = "true"
        env["CURSOR_AUTH_TOKEN"] = "pitot-local-only"
        # The pinned CLI supports an in-memory store for this synthetic token.
        # Endpoint/hook tests must not depend on or modify an OS login keychain.
        env["AGENT_CLI_CREDENTIAL_STORE"] = "memory"
        env.update(cursor_context(home, home / "d"))
        prompt_flag = [
            "--disable-auto-update",
            "--endpoint",
            proxy,
            "--print",
            "--force",
        ]
    elif agent == "gemini":
        hooks = home / ".gemini/hooks"
        hooks.mkdir(parents=True, exist_ok=True)
        source = (
            LAB
            / "integrations/gemini"
            / ("BeforeTool.ps1" if windows_host() else "BeforeTool")
        )
        target = hooks / source.name
        shutil.copy2(source, target)
        target.chmod(0o755)
        if windows_host():
            hook_command = (
                f'& "{target}" '
                f'-Pitot "{witness_command}" -RealBin "{pitot_command}" '
                f'-Receipt "{witness_receipt}" -Nonce "{nonce}" -Runtime "{runtime_path}"'
            )
        else:
            hook_command = (
                f'"{target}" "{witness_command}" "{pitot_command}" '
                f'"{witness_receipt}" "{nonce}" "{runtime_path}"'
            )
        settings = hook_group("BeforeTool", "run_shell_command", hook_command)
        settings["security"] = {
            "auth": {"selectedType": "gemini-api-key"},
            "folderTrust": {"enabled": False},
        }
        write_json(home / ".gemini/settings.json", settings)
        env.update(
            {
                "GOOGLE_GEMINI_BASE_URL": proxy,
                "GEMINI_CLI_HOME": str(home),
                "GEMINI_CLI_TRUST_WORKSPACE": "true",
                "PITOT_BIN": witness_command,
            }
        )
        prompt_flag = [
            "--skip-trust",
            "--approval-mode",
            "yolo",
            "--model",
            "pitot-control",
            "-p",
        ]
    elif agent == "kimi":
        config = home / ".kimi-code/config.toml"
        config.parent.mkdir(parents=True, exist_ok=True)
        config.write_text(
            'default_model = "pitot-control"\n'
            "[providers.pitot]\n"
            'type = "openai"\n'
            f"base_url = {json.dumps(proxy + '/v1')}\n"
            'api_key = "pitot-local-only"\n'
            '[models."pitot-control"]\n'
            'provider = "pitot"\n'
            'model = "pitot-control"\n'
            "max_context_size = 32768\n"
            'capabilities = ["tool_use"]\n'
            "[[hooks]]\n"
            'event = "PreToolUse"\n'
            'matcher = ".*"\n'
            f"command = {json.dumps(witnessed_direct + ' hook kimi --runtime ' + runtime_path)}\n",
            encoding="utf-8",
        )
        env["KIMI_CODE_HOME"] = str(home / ".kimi-code")
        env["KIMI_CODE_CACHE_DIR"] = str(home / ".cache" / "kimi-code")
        env["KIMI_CODE_NO_AUTO_UPDATE"] = "1"
        prompt_flag = []
    elif agent == "qwen":
        env["QWEN_HOME"] = str(home / ".qwen")
        hooks = home / ".qwen/hooks"
        hooks.mkdir(parents=True, exist_ok=True)
        source = (
            LAB
            / "integrations/qwen"
            / ("PreToolUse.cjs" if windows_host() else "PreToolUse")
        )
        target = hooks / source.name
        shutil.copy2(source, target)
        target.chmod(0o755)
        if windows_host():
            hook_command = (
                f'node "{target}" "{witness_command}" "{pitot_command}" '
                f'"{witness_receipt}" "{nonce}" "{runtime_path}"'
            )
        else:
            hook_command = (
                f'"{target}" "{witness_command}" "{pitot_command}" '
                f'"{witness_receipt}" "{nonce}" "{runtime_path}"'
            )
        settings = hook_group("PreToolUse", "^run_shell_command$", hook_command)
        settings["modelProviders"] = {
            "openai": {
                "protocol": "openai",
                "models": [
                    {
                        "id": "pitot-control",
                        "name": "Pitot control",
                        "envKey": "OPENAI_API_KEY",
                        "baseUrl": f"{proxy}/v1",
                    }
                ],
            }
        }
        settings["security"] = {"auth": {"selectedType": "openai"}}
        settings["model"] = {"name": "pitot-control"}
        write_json(home / ".qwen/settings.json", settings)
        env.update({"OPENAI_API_KEY": "pitot-local-only", "PITOT_BIN": witness_command})
        prompt_flag = ["--model", "pitot-control", "-y", "-p"]
    elif agent == "deepseek":
        env["PITOT_DEEPSEEK_FAKE_KEY"] = "pitot-local-only"
        prompt_flag = []
    elif agent == "pi":
        extension = LAB / "integrations/pi/pitot.ts"
        env.update(
            {
                "PI_CODING_AGENT_DIR": str(home / ".pi/agent"),
                "PI_OFFLINE": "1",
                "PI_TELEMETRY": "0",
            }
        )
        write_json(
            home / ".pi/agent/models.json",
            {
                "providers": {
                    "pitot": {
                        "baseUrl": f"{proxy}/v1",
                        "apiKey": "pitot-local-only",
                        "api": "openai-completions",
                        "models": [
                            {
                                "id": "pitot-control",
                                "name": "Pitot control",
                                "reasoning": False,
                                "input": ["text"],
                                "contextWindow": 32000,
                                "maxTokens": 4096,
                            }
                        ],
                    }
                }
            },
        )
        prompt_flag = [
            "--no-session",
            "--print",
            "--provider",
            "pitot",
            "--model",
            "pitot-control",
            "-e",
            str(extension),
        ]
    elif agent == "opencode":
        plugin = (LAB / "integrations/opencode").resolve()
        # Use the released binary's bundled OpenAI provider. A custom provider
        # would dynamically install @ai-sdk/openai-compatible and make endpoint
        # verification depend on a second unpinned network package.
        config = home / ".config/opencode/opencode.json"
        write_json(
            config,
            {
                "plugins": [plugin.as_uri()],
                "providers": {
                    "openai": {
                        "package": "@opencode/ai/providers/openai/responses",
                        "settings": {
                            "baseURL": f"{proxy}/v1",
                            "apiKey": "pitot-local-only",
                        },
                        "models": {"pitot-control": {"name": "Pitot control"}},
                    }
                },
                "model": "openai/pitot-control",
                "permissions": [
                    {"action": "shell", "resource": "*", "effect": "allow"}
                ],
            },
        )
        env["OPENCODE_CONFIG"] = str(config)
        env["XDG_CACHE_HOME"] = str(home / ".cache")
        env["XDG_DATA_HOME"] = str(home / ".local/share")
        env["XDG_STATE_HOME"] = str(home / ".local/state")
        prompt_flag = [
            "--print-logs",
            "--log-level",
            "debug",
            "run",
            "--standalone",
            "--model",
            "openai/pitot-control",
        ]
    else:
        raise ValueError(f"unsupported agent {agent}")
    return prompt_flag, env


def capture_record(
    agent: dict[str, object],
    platform: str,
    installation: dict[str, object],
    proxy: dict[str, object],
) -> dict[str, object]:
    """Normalize the two receipts without trusting a manifest protocol hint."""
    observed = proxy.get("endpoint_observed")
    dialect = proxy.get("protocol")
    if not isinstance(observed, dict) or dialect not in {
        "anthropic_messages",
        "openai_chat",
        "openai_responses",
        "gemini_generate_content",
        "cursor_connect_proto",
        "devin_connect_proto",
    }:
        raise RuntimeError("proxy did not binary-observe a supported request contract")
    digest = installation.get("executable_sha256")
    if not isinstance(digest, str) or not re.fullmatch(r"[0-9a-f]{64}", digest):
        raise RuntimeError("installation receipt lacks the executable content digest")
    core = {
        "agent": agent["id"],
        "platform": platform,
        "runtime": installation["runtime"],
        "version": installation["version"],
        "executable_sha256": digest,
        "installation": identity_evidence(installation),
        "dialect": dialect,
        "request": observed,
        "response": {
            "encoder": dialect,
            "framing": observed["framing"],
            "tool_call": "native_shell",
            "acceptance": "nonce_tool_result_round_trip",
        },
        "provenance": "pinned_real_cli_capture",
    }
    return {
        **core,
        "capture_sha256": hashlib.sha256(
            json.dumps(core, sort_keys=True, separators=(",", ":")).encode()
        ).hexdigest(),
    }


def validate_capture_fixture(record: dict[str, object]) -> dict[str, object]:
    matches = [
        item
        for item in ENDPOINT_PROVENANCE.get("cells", [])
        if item.get("agent") == record["agent"]
        and item.get("platform") == record["platform"]
    ]
    if len(matches) != 1 or matches[0] != record:
        raise RuntimeError(
            "binary-observed contract drifted from its supervised platform fixture"
        )
    return {
        "fixture": f"tests/endpoint-provenance.json#{record['agent']}/{record['platform']}",
        "fixture_sha256": record["capture_sha256"],
        "provenance": record["provenance"],
        "dialect": record["dialect"],
        "request": record["request"],
        "response": record["response"],
        "executable_sha256": record["executable_sha256"],
        "installation": record["installation"],
    }


def json_lines(path: Path) -> list[dict[str, object]]:
    if not path.is_file():
        return []
    return [
        json.loads(line)
        for line in path.read_text(encoding="utf-8").splitlines()
        if line.strip()
    ]


def validate_session_provenance(
    host: str,
    witnesses: list[dict[str, object]],
    requests: list[dict[str, object]],
    consumers: list[dict[str, object]],
) -> list[dict[str, str]]:
    """Compare native witness identity with both delivered event envelopes."""
    if (
        len(requests) != 2
        or len(consumers) != 2
        or (host != "devin" and len(witnesses) != 2)
    ):
        raise RuntimeError("session provenance requires both boundary actions")
    proofs = []
    action_ids = [
        request.get("action_id") if isinstance(request, dict) else None
        for request in requests
    ]
    if (
        any(
            not isinstance(action_id, str) or not action_id.startswith("act_")
            for action_id in action_ids
        )
        or len(set(action_ids)) != 2
    ):
        raise RuntimeError("session provenance action correlation is malformed")
    for index, (request, consumer) in enumerate(zip(requests, consumers)):
        event = request.get("data")
        if not isinstance(event, dict) or not isinstance(consumer, dict):
            raise RuntimeError("session provenance event is missing")
        action_id = request.get("action_id")
        source = "host_event" if host == "devin" else "host_hook"
        for delivered in (event, consumer):
            if (
                delivered.get("type") != "action.requested"
                or not isinstance(delivered.get("host"), dict)
                or not isinstance(delivered.get("action"), dict)
                or delivered.get("host", {}).get("name") != host
                or delivered.get("action", {}).get("id") != action_id
                or delivered.get("observation")
                != {"source": source, "fidelity": "direct"}
            ):
                raise RuntimeError("session provenance delivery identity changed")
        session = event.get("session_id", "")
        if not isinstance(session, str):
            raise RuntimeError("session provenance has a malformed ID")
        state = "supplied" if session else "unavailable"
        digest = hashlib.sha256(session.encode("utf-8")).hexdigest() if session else ""
        if host == "devin":
            if witnesses or not session:
                raise RuntimeError("ACP session provenance is missing")
        else:
            witness = witnesses[index]
            if not isinstance(witness, dict):
                raise RuntimeError("session provenance witness is malformed")
            if (
                witness.get("valid") is not True
                or witness.get("host") != host
                or witness.get("action_id") != action_id
            ):
                raise RuntimeError("session provenance witness correlation changed")
            if host == "deepseek":
                if not session or witness.get("session_id") != session:
                    raise RuntimeError("native DeepSeek session identity changed")
            elif (
                "session_id" in witness
                or witness.get("session_id_state") != state
                or (state == "supplied" and witness.get("session_id_sha256") != digest)
                or (state == "unavailable" and "session_id_sha256" in witness)
            ):
                raise RuntimeError("native hook session identity changed")
        if host in {"pi", "opencode"} and not session:
            raise RuntimeError("native plugin did not supply its session identity")
        if session:
            if consumer.get("session_id") != session:
                raise RuntimeError("Consumer session identity changed")
        elif "session_id" in event or "session_id" in consumer:
            raise RuntimeError("unavailable session must remain omitted")
        proofs.append({"state": state, "sha256": digest})
    return proofs


def validate_cursor_commands(
    requests: list[dict[str, object]], expected: list[str]
) -> list[str]:
    """Match the measured hook command, including Unicode, at the Controller."""
    if len(requests) != 2 or len(expected) != 2:
        raise RuntimeError("Cursor command proof requires both boundary actions")
    digests = []
    for request, command in zip(requests, expected):
        if not isinstance(request, dict) or not isinstance(command, str) or not command:
            raise RuntimeError("Cursor command proof is malformed")
        event = request.get("data")
        if not isinstance(event, dict):
            raise RuntimeError("Cursor Controller request has no measured event")
        action, content, host = (
            event.get("action"),
            event.get("content"),
            event.get("host"),
        )
        if (
            event.get("type") != "action.requested"
            or not isinstance(host, dict)
            or host.get("name") != "cursor"
            or not isinstance(request.get("action_id"), str)
            or not request["action_id"].startswith("act_")
            or not isinstance(action, dict)
            or action.get("id") != request.get("action_id")
            or action.get("kind") != "shell"
            or not isinstance(content, dict)
            or content.get("mode") != "full"
            or content.get("full") != command
        ):
            raise RuntimeError("Cursor hook command changed before the Controller")
        digests.append(hashlib.sha256(command.encode("utf-8")).hexdigest())
    return digests


def validate_cursor_carrier(
    records: list[dict[str, object]],
    context: dict[str, str],
    nonce: str,
    commands: list[str],
    action_ids: list[str],
) -> None:
    """Check snapshots made by the actual Windows hook's bounded observer."""
    if len(records) != 2 or len(commands) != 2 or len(action_ids) != 2:
        raise RuntimeError("Cursor carrier proof requires both actions")

    def same_path(left, right):
        return isinstance(left, str) and PureWindowsPath(left) == PureWindowsPath(right)

    def owned_path(value):
        if not isinstance(value, str):
            raise RuntimeError("Cursor carrier path is missing")
        path = PureWindowsPath(value)
        root = PureWindowsPath(context["PITOT_CURSOR_CARRIER_TEMP"])
        if not path.is_absolute() or not path.drive.endswith(":") or ".." in path.parts:
            raise RuntimeError("Cursor carrier path is unsafe")
        try:
            path.relative_to(root)
        except ValueError as error:
            raise RuntimeError("Cursor carrier escaped its owned directory") from error
        return path

    for index, (record, command, action_id) in enumerate(
        zip(records, commands, action_ids)
    ):
        try:
            if (
                record["schema_version"] != 1
                or record["status"] != "observed"
                or record["source_candidate_sha"] != context["PITOT_CURSOR_CANDIDATE"]
                or record["nonce"] != nonce
                or record["phase"] != ("allow" if index == 0 else "deny")
                or record["action_id"] != action_id
                or record["observed_command"] != command
                or record["expected_command"] != command
                or record.get("input_prefix_bom_removed") is not True
                or not same_path(
                    record["owned_temp_root"], context["PITOT_CURSOR_CARRIER_TEMP"]
                )
                or type(record["elapsed_ms"]) is not int
                or not 0 <= record["elapsed_ms"] <= 5000
            ):
                raise RuntimeError("Cursor carrier identity mismatch")
            inner, parent, node = (
                record["inner"],
                record["parent"],
                record["grandparent"],
            )
            pids = [item["pid"] for item in (inner, parent, node)]
            if len(set(pids)) != 3 or any(
                type(pid) is not int or pid <= 0 for pid in pids
            ):
                raise RuntimeError("Cursor carrier ancestry is missing")
            if (
                inner["parent_pid"] != parent["pid"]
                or parent["parent_pid"] != node["pid"]
            ):
                raise RuntimeError("Cursor carrier parent relationship is wrong")
            dates = [
                datetime.fromisoformat(item["creation_date"])
                for item in (inner, parent, node)
            ]
            if (
                any(date.tzinfo is None for date in dates)
                or not dates[2] <= dates[1] <= dates[0]
            ):
                raise RuntimeError("Cursor carrier ancestry dates are invalid")
            for shell in (inner, parent):
                if (
                    not same_path(
                        shell["image_path"], context["PITOT_CURSOR_PARENT_SHELL"]
                    )
                    or shell["image_sha256"]
                    != context["PITOT_CURSOR_PARENT_SHELL_SHA256"]
                    or not isinstance(shell["creation_date"], str)
                    or not shell["creation_date"]
                ):
                    raise RuntimeError("Cursor carrier shell image mismatch")
            if (
                type(parent["file_major_part"]) is not int
                or parent["file_major_part"] < 7
            ):
                raise RuntimeError("Cursor carrier did not use PowerShell 7")
            if (
                not same_path(node["image_path"], context["PITOT_CURSOR_RELEASE_NODE"])
                or node["image_sha256"] != context["PITOT_CURSOR_RELEASE_NODE_SHA256"]
                or not same_path(
                    node["version_prefix"],
                    str(PureWindowsPath(context["PITOT_CURSOR_RELEASE_NODE"]).parent),
                )
            ):
                raise RuntimeError("Cursor carrier released node mismatch")
            snapshots = {}
            for kind in ("carrier", "payload"):
                snapshot = record[kind]
                path = owned_path(snapshot["path"])
                raw = base64.b64decode(snapshot["bytes_base64"], validate=True)
                if (
                    len(raw) > 1048576
                    or hashlib.sha256(raw).hexdigest() != snapshot["sha256"]
                ):
                    raise RuntimeError("Cursor carrier snapshot digest mismatch")
                snapshots[kind] = (path, raw)
            script_path, script_bytes = snapshots["carrier"]
            payload_path, payload_bytes = snapshots["payload"]
            if not re.fullmatch(
                r"ps-script-[0-9a-fA-F]{8}(?:-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}\.ps1",
                script_path.name,
            ):
                raise RuntimeError("Cursor carrier script name mismatch")
            if payload_path.name != "payload.json" or not re.fullmatch(
                r"cursor-hooks-[A-Za-z0-9]+", payload_path.parent.name
            ):
                raise RuntimeError("Cursor carrier payload name mismatch")
            if not script_bytes.startswith(b"\xef\xbb\xbf"):
                raise RuntimeError(
                    "Cursor carrier script is missing its UTF8 signature"
                )
            script = script_bytes[3:].decode("utf-8", errors="strict")
            payload = json.loads(payload_bytes.decode("utf-8", errors="strict"))
            if not isinstance(payload, dict) or payload.get("command") != command:
                raise RuntimeError("Cursor vendor payload changed the command")
            wrappers = re.findall(
                r"\$OutputEncoding = \[System.Text.Encoding\]::UTF8; Get-Content -LiteralPath '((?:[^']|'')*)' -Raw \| & \{ \$input \| "
                + re.escape(context["PITOT_CURSOR_VERIFICATION_HOOK_COMMAND"])
                + r" \}",
                script,
            )
            if len(wrappers) != 1 or not same_path(
                wrappers[0].replace("''", "'"), record["payload"]["path"]
            ):
                raise RuntimeError("Cursor vendor wrapper is unrelated")
            executable = re.escape(context["PITOT_CURSOR_PARENT_SHELL"])
            argv = re.fullmatch(
                r'\s*(?:"'
                + executable
                + '"|'
                + executable
                + r')\s+-ExecutionPolicy\s+Bypass\s+(?:-NonInteractive\s+)?-File\s+(?:"(?P<quoted>[^"\r\n]+)"|(?P<bare>[^\s"]+))\s*',
                parent["argv"],
                flags=re.IGNORECASE,
            )
            if argv is None or not same_path(
                argv.group("quoted") or argv.group("bare"), record["carrier"]["path"]
            ):
                raise RuntimeError("Cursor parent did not execute the observed carrier")
        except (KeyError, TypeError, ValueError, UnicodeError) as error:
            raise RuntimeError("Cursor carrier proof is malformed") from error


def validate_deepseek_native_results(
    root: Path | None, witnesses: list[dict[str, object]]
) -> dict[str, object]:
    if root is None:
        raise RuntimeError("DeepSeek native session evidence is missing")
    sessions = list(root.glob("home/.dsh/sessions/*/*/session.v4.jsonl.zstd"))
    if len(sessions) != 1 or sessions[0].stat().st_size > 8 * 1024 * 1024:
        raise RuntimeError("DeepSeek native session evidence is ambiguous or oversized")
    decoded = subprocess.run(
        [
            "node",
            "-e",
            "const fs=require('node:fs'),z=require('node:zlib');"
            "const input=fs.readFileSync(process.argv[1]);let offset=0,total=0;"
            "while(offset<input.length){const result=z.zstdDecompressSync(input.subarray(offset),{info:true,maxOutputLength:8388608-total});"
            "const consumed=result.engine.bytesWritten;if(!Number.isSafeInteger(consumed)||consumed<=0||consumed>input.length-offset)throw Error('invalid frame extent');"
            "total+=result.buffer.length;if(total>8388608)throw Error('session exceeds bound');"
            "process.stdout.write(result.buffer);offset+=consumed;}",
            str(sessions[0]),
        ],
        check=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        timeout=30,
    ).stdout
    rows = [
        json.loads(line)
        for line in decoded.decode("utf-8", errors="strict").splitlines()
    ]
    calls = [row for row in rows if row.get("type") == "tool/call"]
    results = [row for row in rows if row.get("type") == "tool/result"]
    expected = ["pitot_tool_allow", "pitot_tool_deny"]
    if [row.get("data", {}).get("callId") for row in calls] != expected or len(
        results
    ) != 2:
        raise RuntimeError("DeepSeek did not persist exactly the issued native calls")
    if any(row.get("data", {}).get("name") not in {"bash", "pwsh"} for row in calls):
        raise RuntimeError("DeepSeek native calls are not shell tools")
    for index, row in enumerate(results):
        data = row.get("data", {})
        message = data.get("message", {})
        if (
            message.get("toolCallId") != expected[index]
            or message.get("source", {}).get("callId") != expected[index]
            or message.get("isError") is not (index == 1)
        ):
            raise RuntimeError("DeepSeek native result identity or status mismatch")
        if row.get("sourceEventSeqs") != [calls[index].get("seq")]:
            raise RuntimeError("DeepSeek native result lost its call relation")
    if results[1]["data"].get("error", {}).get("code") != "PITOT_DENIED":
        raise RuntimeError("DeepSeek did not preserve the typed Controller denial")
    if (
        len(witnesses) != 2
        or [row.get("tool_call_id") for row in witnesses] != expected
        or any(row.get("session_id") != sessions[0].parent.name for row in witnesses)
    ):
        raise RuntimeError("DeepSeek hook witnesses do not match the native session")
    print(
        "PITOT_NATIVE_TOOL_RESULTS agent=deepseek allow_call_id=pitot_tool_allow deny_call_id=pitot_tool_deny error_type=PITOT_DENIED"
    )
    return {
        "session_id": sessions[0].parent.name,
        "call_ids": expected,
        "is_error": [False, True],
        "denial_code": "PITOT_DENIED",
        "session_sha256": hashlib.sha256(sessions[0].read_bytes()).hexdigest(),
    }


def validate_receipts(
    agent: dict[str, object],
    platform: str,
    nonce: str,
    installation: dict[str, object],
    proxy_path: Path,
    witness_path: Path,
    controller_path: Path,
    consumer_path: Path,
    canary_path: Path,
    runtime_identity: dict[str, object],
    exit_code: int,
    output: str,
    prompt: str,
    capture_output: Path | None = None,
    expected_cursor_commands: list[str] | None = None,
    cursor_carrier_records: list[dict[str, object]] | None = None,
    cursor_carrier_context: dict[str, str] | None = None,
    deepseek_session: Path | None = None,
) -> dict[str, object]:
    proxy = (
        json.loads(proxy_path.read_text(encoding="utf-8"))
        if proxy_path.is_file()
        else {"missing": True}
    )
    witnesses = json_lines(witness_path)
    controller = json_lines(controller_path)
    consumers = json_lines(consumer_path)
    canary = (
        canary_path.read_text(encoding="utf-8").splitlines()
        if canary_path.is_file()
        else []
    )
    proxy_flags = (
        "initial_prompt_observed",
        "allow_tool_call_response_emitted",
        "allow_tool_result_observed",
        "deny_tool_call_response_emitted",
        "denied_result_observed",
        "final_response_emitted",
    )
    final_marker = f"PITOT_E2E_COMPLETE {nonce}"
    if (
        exit_code != 0
        or "hook: PreToolUse Failed" in output
        or final_marker not in output
        or not all(proxy.get(flag) is True for flag in proxy_flags)
    ):
        raise RuntimeError(
            f"agent loop incomplete (exit={exit_code}, proxy={proxy}, witnesses={witnesses})\n{output[-4000:]}"
        )
    if proxy.get("nonce") != nonce:
        raise RuntimeError("proxy receipt escaped the nonce-bound real-agent session")
    if agent["id"] == "opencode":
        if proxy.get("opencode_allow_result") != {
            "call_id": "pitot_tool_allow"
        } or proxy.get("opencode_denied_result") != {
            "call_id": "pitot_tool_deny",
            "error_type": "tool.execution",
        }:
            raise RuntimeError(
                "OpenCode tool results lack issued call IDs or typed denial"
            )
        print(
            "PITOT_NATIVE_TOOL_RESULTS agent=opencode "
            "allow_call_id=pitot_tool_allow deny_call_id=pitot_tool_deny "
            "error_type=tool.execution"
        )
    native_results = None
    if agent["id"] == "deepseek":
        native_results = validate_deepseek_native_results(deepseek_session, witnesses)
    if agent["integration"] == "acp_client":
        if witnesses:
            raise RuntimeError(
                "ACP control unexpectedly entered the lifecycle-hook witness path"
            )
        # Observed baseline is ~10 auxiliary (non-chat) requests per session;
        # an unexplained surge means the proxy is answering traffic the
        # trajectory never accounted for. Test-side ceiling only: the proxy
        # itself stays permissive so a chattier Devin build cannot black-hole.
        auxiliary = int(proxy.get("auxiliary_requests", 0))
        if auxiliary > 100:
            raise RuntimeError(
                f"devin proxy answered {auxiliary} auxiliary requests; ceiling is 100"
            )
    else:
        if len(witnesses) != 2:
            raise RuntimeError(
                "Pitot witness receipts do not identify exactly two hook actions"
            )
        if any(
            item.get("nonce") != nonce
            or item.get("host") != agent["id"]
            or item.get("valid") is not True
            for item in witnesses
        ):
            raise RuntimeError(
                "Pitot hook witnesses escaped the nonce-bound real-agent session"
            )
        if [item.get("pitot_exit") for item in witnesses] != [0, 2]:
            raise RuntimeError(
                f"real hook did not carry one allow and one deny: {witnesses}"
            )
    requests = [
        item.get("value")
        for item in controller
        if item.get("receipt_type") == "request"
    ]
    responses = [
        item.get("value")
        for item in controller
        if item.get("receipt_type") == "response"
    ]
    if len(requests) != 2 or len(responses) != 2:
        raise RuntimeError(
            f"Controller did not receive and resolve both boundary actions: {controller}"
        )
    cursor_command_digests = None
    if agent["id"] == "cursor" and platform == "windows":
        if not expected_cursor_commands or not all(
            any(ord(char) > 127 for char in command)
            for command in expected_cursor_commands
        ):
            raise RuntimeError("Cursor Windows proof requires Unicode commands")
        cursor_command_digests = validate_cursor_commands(
            requests, expected_cursor_commands
        )
        if cursor_carrier_records is None or cursor_carrier_context is None:
            raise RuntimeError("Cursor Windows carrier snapshots are missing")
        validate_cursor_carrier(
            cursor_carrier_records,
            cursor_carrier_context,
            nonce,
            expected_cursor_commands,
            [request.get("action_id") for request in requests],
        )
    action_ids = [item.get("action_id") for item in requests]
    if len(set(action_ids)) != 2 or not all(
        isinstance(item, str) and item.startswith("act_") for item in action_ids
    ):
        raise RuntimeError("boundary actions lack unique Pitot correlation ids")
    if witnesses and [item.get("action_id") for item in witnesses] != action_ids:
        raise RuntimeError("hook correlation ids do not match the Controller actions")
    if [item.get("action_id") for item in requests] != action_ids or [
        item.get("action_id") for item in responses
    ] != action_ids:
        raise RuntimeError(
            "Controller correlation ids do not match the real boundary actions"
        )
    if [item.get("outcome") for item in responses] != ["allow", "deny"] or responses[
        1
    ].get("message") != f"PITOT_CONTROLLER_DENY {nonce}":
        raise RuntimeError(
            "external Controller did not produce the nonce-bound allow/deny trajectory"
        )
    if (
        len(consumers) != 2
        or [item.get("action", {}).get("id") for item in consumers] != action_ids
    ):
        raise RuntimeError(
            f"passive Consumer did not receive both real hook observations: {consumers}"
        )
    if any(
        item.get("content", {}).get("mode") != "sha256"
        or "full" in item.get("content", {})
        for item in consumers
    ):
        raise RuntimeError("Consumer projection did not remove full command content")
    session_proofs = validate_session_provenance(
        agent["id"], witnesses, requests, consumers
    )
    print(
        f"PITOT_SESSION_PROVENANCE agent={agent['id']} "
        f"states={','.join(proof['state'] for proof in session_proofs)} "
        "controller_consumer=matched"
    )
    if canary != [f"PITOT_ALLOW {nonce}"]:
        raise RuntimeError(
            f"canary execution count violated allow/deny control: {canary}"
        )
    runtime_public = {
        key: runtime_identity.get(key)
        for key in ("schema_version", "instance_id", "pid", "endpoint", "config_sha256")
    }
    if runtime_public["schema_version"] != 1 or not all(
        runtime_public.get(key) for key in ("instance_id", "endpoint", "config_sha256")
    ):
        raise RuntimeError("authenticated Pitot runtime identity is incomplete")
    if (
        installation.get("agent") != agent["id"]
        or installation.get("version") != agent["version"]
    ):
        raise RuntimeError("installation receipt does not match supervised manifest")
    captured = capture_record(agent, platform, installation, proxy)
    if capture_output is not None:
        write_json(
            capture_output, {"schema_version": 1, "accepted": True, "cell": captured}
        )
        endpoint_evidence = {
            "fixture": "candidate",
            "fixture_sha256": captured["capture_sha256"],
            "provenance": captured["provenance"],
            "dialect": captured["dialect"],
            "request": captured["request"],
            "response": captured["response"],
            "executable_sha256": captured["executable_sha256"],
            "installation": captured["installation"],
        }
    else:
        endpoint_evidence = validate_capture_fixture(captured)
    return {
        "schema_version": 2,
        "agent": agent["id"],
        "cli": {
            "version": installation["version"],
            "executable": installation["executable"],
            "executable_sha256": installation["executable_sha256"],
            "installation": identity_evidence(installation),
            "installer": installation["installer"],
            "runtime": installation["runtime"],
        },
        "prompt_hash": hashlib.sha256(prompt.encode()).hexdigest(),
        "protocol": proxy["protocol"],
        "endpoint": endpoint_evidence,
        "nonce": nonce,
        "receipts": {
            **{flag: True for flag in proxy_flags},
            "consumer_observed": True,
            "controller_allow_observed": True,
            "controller_deny_observed": True,
            "deny_canary_absent": True,
            "final_output_observed": True,
            "cli_exit_zero": True,
        },
        "runtime": runtime_public,
        "boundaries": [
            {
                "host": agent["id"],
                "transport": "acp" if agent["integration"] == "acp_client" else "hook",
                "action_kind": "shell",
                "action_id": action_id,
                "decision": outcome,
                "nonce": nonce,
            }
            for action_id, outcome in zip(action_ids, ("allow", "deny"), strict=True)
        ],
        "controller": {
            "id": "e2e-shell-controller",
            "action_ids": action_ids,
            "outcomes": ["allow", "deny"],
            **(
                {"native_tool_results": native_results}
                if native_results is not None
                else {}
            ),
            **(
                {
                    "command_sha256": cursor_command_digests,
                    "windows_carrier": cursor_carrier_records,
                }
                if cursor_command_digests is not None
                else {}
            ),
        },
        "consumer": {
            "id": "e2e-audit",
            "action_ids": action_ids,
            "projection": "sha256",
        },
        "canary": {"executions": canary, "denied_executions": 0},
    }


def owned_fixture_base(base: Path, agent: str, runtime: str) -> Path:
    """Use one filesystem identity for native Windows Cursor fixture paths."""
    if agent == "cursor" and windows_host() and runtime == "native":
        canonical = base.resolve(strict=True)
        if not canonical.samefile(base):
            raise RuntimeError("Cursor temporary directory identity changed")
        return canonical
    return base


def require_fixture_removed(base: Path) -> None:
    """Refuse success when the owned fixture remains or cannot be inspected."""
    try:
        base.lstat()
    except FileNotFoundError:
        return
    raise RuntimeError("Cursor temporary fixture cleanup failed")


def qwen_update_canary(
    installation: dict[str, object], environment: dict[str, str], base: Path
) -> Path:
    """Place a valid managed update outside the pinned install as a negative control."""
    value = installation["installation"]
    entry = value["entrypoint"]
    observed = subprocess.run(
        [
            value["node"]["path"],
            "-e",
            "console.log(require('node:fs').statSync(process.argv[1]).ctimeMs)",
            entry,
        ],
        check=True,
        capture_output=True,
        text=True,
        timeout=30,
        env=environment,
    )
    ctime = json.loads(observed.stdout)
    root = (
        Path(environment["QWEN_HOME"])
        / "updates/npm"
        / hashlib.sha256(entry.encode()).hexdigest()[:16]
    )
    package = root / "versions/999.0.0/node_modules/@qwen-code/qwen-code"
    package.mkdir(parents=True)
    marker = base / "unrecorded-qwen-update-selected"
    write_json(
        package / "package.json",
        {"name": "@qwen-code/qwen-code", "version": "999.0.0", "type": "module"},
    )
    (package / "cli.js").write_text(
        "import {writeFileSync} from 'node:fs'; writeFileSync("
        + json.dumps(str(marker))
        + ", 'selected'); process.exit(91);\n",
        encoding="utf-8",
    )
    write_json(
        root / "active.json",
        {
            "version": "999.0.0",
            "bootstrap": entry,
            "baseVersion": installation["version"],
            "bootstrapCtimeMs": ctime,
        },
    )
    unpinned = dict(environment)
    unpinned.pop("QWEN_CODE_MANAGED_NPM_PIN", None)
    control = subprocess.run(
        installation_command(value) + ["--help"],
        env=unpinned,
        capture_output=True,
        timeout=30,
    )
    if control.returncode != 91 or not marker.is_file():
        raise RuntimeError("Qwen managed-update positive control did not execute")
    marker.unlink()
    return marker


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--agent", required=True)
    parser.add_argument("--platform", required=True)
    parser.add_argument("--installation", type=Path, required=True)
    parser.add_argument("--evidence", type=Path, required=True)
    parser.add_argument("--pitot", type=Path, required=True)
    parser.add_argument("--witness", type=Path, required=True)
    parser.add_argument("--test-role", type=Path, required=True)
    parser.add_argument("--capture-output", type=Path)
    parser.add_argument("--response-fault", choices=("none", "text"), default="none")
    parser.add_argument("--expect-incompatible-response", action="store_true")
    args = parser.parse_args()
    try:
        return run_session(args)
    except VersionProbeError as error:
        persist_probe_error(error, args.evidence)
        raise SystemExit(str(error)) from None


def run_session(args: argparse.Namespace) -> int:
    agent = next(item for item in MANIFEST["agents"] if item["id"] == args.agent)
    installation = inspect_receipt(
        json.loads(args.installation.read_text(encoding="utf-8")), agent, args.platform
    )
    nonce = secrets.token_hex(16)
    prompt = f"Pitot E2E session {nonce}: execute the requested verification command"
    with (
        ExitStack() as cleanup,
        tempfile.TemporaryDirectory(
            prefix="pitot-real-agent-", ignore_cleanup_errors=True
        ) as temporary,
    ):
        runtime = installation["runtime"]
        base = owned_fixture_base(Path(temporary), args.agent, runtime)
        if args.agent == "cursor" and windows_host() and runtime == "native":
            cleanup.callback(require_fixture_removed, base)
        host_controls_wsl = runtime == "wsl" and os.name == "nt"
        home, project, bin_dir = base / "home", base / "project", base / "bin"
        home.mkdir()
        project.mkdir()
        bin_dir.mkdir()
        canary_receipt = base / "canary.jsonl"
        if args.agent == "deepseek":
            canary_receipt = project / "canary.jsonl"
        canary_executable = args.test_role.resolve()
        if args.agent == "cursor" and windows_host() and not host_controls_wsl:
            canary_dir = base / "cursor 工具 path"
            canary_dir.mkdir()
            canary_executable = canary_dir / args.test_role.name
            shutil.copy2(args.test_role.resolve(), canary_executable)
            canary_receipt = canary_dir / "résult.jsonl"
        cursor_system_canary = False
        if args.agent == "cursor" and host_controls_wsl:
            occupied = subprocess.run(
                [
                    "wsl.exe",
                    "--distribution",
                    "Ubuntu",
                    "--",
                    "test",
                    "-e",
                    "/usr/local/bin/pitot-e2e-canary",
                ],
                check=False,
                text=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.STDOUT,
            )
            if occupied.returncode == 0:
                raise RuntimeError("refusing to replace an existing WSL canary command")
            subprocess.run(
                [
                    "wsl.exe",
                    "--distribution",
                    "Ubuntu",
                    "--",
                    "install",
                    "-m",
                    "0755",
                    wsl_path(canary_executable),
                    "/usr/local/bin/pitot-e2e-canary",
                ],
                check=True,
                text=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.STDOUT,
            )
            canary_executable = Path("/usr/local/bin/pitot-e2e-canary")
            cursor_system_canary = True
        elif (
            args.agent == "cursor"
            and os.name != "nt"
            and os.access("/usr/local/bin", os.W_OK)
        ):
            if Path("/usr/local/bin/pitot-e2e-canary").exists():
                raise RuntimeError(
                    "refusing to replace an existing system canary command"
                )
            shutil.copy2(canary_executable, "/usr/local/bin/pitot-e2e-canary")
            Path("/usr/local/bin/pitot-e2e-canary").chmod(0o755)
            canary_executable = Path("/usr/local/bin/pitot-e2e-canary")
            cursor_system_canary = True
        receipt_argument = (
            wsl_path(canary_receipt) if host_controls_wsl else str(canary_receipt)
        )
        # Forward slashes keep the same native Windows command valid in
        # PowerShell, cmd.exe, and the Unix-like shells selected by hosts.
        canary_command = render_canary_command(
            str(canary_executable),
            receipt_argument,
            windows_host() and not host_controls_wsl,
        )
        if (
            args.agent in {"cursor", "deepseek"}
            and windows_host()
            and not host_controls_wsl
        ):
            canary_command = render_cursor_canary_command(
                str(canary_executable), receipt_argument
            )
        cursor_canary_argv = [
            str(canary_executable).replace("\\", "/")
            if windows_host() and not host_controls_wsl
            else str(canary_executable),
            "--role",
            "canary",
            "--receipt",
            receipt_argument.replace("\\", "/")
            if windows_host() and not host_controls_wsl
            else receipt_argument,
        ]
        ready, proxy_receipt, witness_receipt = (
            base / "proxy.url",
            base / "proxy.json",
            base / "witness.jsonl",
        )
        proxy_request_log = base / "proxy-requests.log"
        runtime_descriptor = base / "runtime.json"
        runtime_config = base / "pitot.json"
        consumer_receipt = base / "consumer.jsonl"
        controller_receipt = base / "controller.jsonl"
        runtime_config.write_text(
            json.dumps(
                {
                    "consumers": [
                        {
                            "id": "e2e-audit",
                            "command": [
                                str(args.test_role.resolve()),
                                "--role",
                                "consumer",
                                "--receipt",
                                str(consumer_receipt),
                            ],
                            "events": ["action.requested"],
                            "projection": {"content": "sha256"},
                        }
                    ],
                    "controllers": {
                        "shell": {
                            "id": "e2e-shell-controller",
                            "command": [
                                str(args.test_role.resolve()),
                                "--role",
                                "controller",
                                "--id",
                                "e2e-shell-controller",
                                "--receipt",
                                str(controller_receipt),
                                "--nonce",
                                nonce,
                            ],
                            "deadline_ms": 5000,
                            "on_timeout": "deny",
                            "on_unavailable": "deny",
                        }
                    },
                },
                indent=2,
            )
            + "\n",
            encoding="utf-8",
        )
        runtime_process = subprocess.Popen(
            [
                str(args.pitot.resolve()),
                "run",
                "--config",
                str(runtime_config),
                "--runtime",
                str(runtime_descriptor),
            ],
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            text=True,
        )
        for _ in range(600):
            if runtime_descriptor.is_file():
                break
            if runtime_process.poll() is not None:
                output = runtime_process.stdout.read() if runtime_process.stdout else ""
                raise RuntimeError(
                    f"Pitot runtime exited before becoming ready: {output.strip()}"
                )
            time.sleep(0.05)
        else:
            runtime_process.terminate()
            raise RuntimeError(
                "Pitot runtime did not publish its authenticated descriptor"
            )
        runtime_identity = json.loads(runtime_descriptor.read_text(encoding="utf-8"))
        if args.agent == "cursor":
            if host_controls_wsl:
                # Keep Cursor and its HTTP/2 Connect control proxy in the same
                # supported WSL network namespace. The workflow installs a
                # pinned Linux Node runtime specifically for this harness.
                proxy_command = [
                    "wsl.exe",
                    "--distribution",
                    "Ubuntu",
                    "--",
                    "node",
                    wsl_path(LAB / "tests/cursor_control_proxy.mjs"),
                    "--nonce",
                    nonce,
                    "--receipt",
                    wsl_path(proxy_receipt),
                    "--ready-file",
                    wsl_path(ready),
                    "--canary-command",
                    canary_command,
                    "--canary-argv",
                    json.dumps(cursor_canary_argv),
                    "--response-fault",
                    args.response_fault,
                ]
            else:
                proxy_command = [
                    "node",
                    str(LAB / "tests/cursor_control_proxy.mjs"),
                    "--nonce",
                    nonce,
                    "--receipt",
                    str(proxy_receipt),
                    "--ready-file",
                    str(ready),
                    "--canary-command",
                    canary_command,
                    "--canary-argv",
                    json.dumps(cursor_canary_argv),
                    "--response-fault",
                    args.response_fault,
                ]
            proxy_command.extend(
                ["--source-sha", os.environ.get("PITOT_SOURCE_SHA", "")]
            )
        elif args.agent == "devin":
            proxy_command = [
                sys.executable,
                str(LAB / "tests/devin_control_proxy.py"),
                "--nonce",
                nonce,
                "--receipt",
                str(proxy_receipt),
                "--ready-file",
                str(ready),
                "--canary-command",
                canary_command,
                "--response-fault",
                args.response_fault,
                "--request-log",
                str(proxy_request_log),
            ]
        else:
            proxy_command = [
                sys.executable,
                str(LAB / "tests/model_control_proxy.py"),
                "--agent",
                args.agent,
                "--nonce",
                nonce,
                "--receipt",
                str(proxy_receipt),
                "--ready-file",
                str(ready),
                "--canary-command",
                canary_command,
                "--response-fault",
                args.response_fault,
            ]
        proxy_process = subprocess.Popen(
            proxy_command,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            text=True,
        )
        try:
            for _ in range(600):
                if ready.is_file():
                    break
                if proxy_process.poll() is not None:
                    output = proxy_process.stdout.read() if proxy_process.stdout else ""
                    raise RuntimeError(
                        f"model-control proxy exited before becoming ready: {output.strip()}"
                    )
                time.sleep(0.05)
            else:
                proxy_process.terminate()
                output = (
                    proxy_process.communicate(timeout=5)[0]
                    if proxy_process.stdout
                    else ""
                )
                raise RuntimeError(
                    f"model-control proxy did not become ready: {output.strip()}"
                )
            proxy = ready.read_text(encoding="utf-8").strip()
            witness_command = (
                wsl_path(args.witness)
                if host_controls_wsl
                else str(args.witness.resolve())
            )
            pitot_command = (
                wsl_path(args.pitot) if host_controls_wsl else str(args.pitot.resolve())
            )
            receipt_command = (
                wsl_path(witness_receipt) if host_controls_wsl else str(witness_receipt)
            )
            cursor_carrier = None
            if args.agent == "cursor" and windows_host():
                carrier_temp, carrier_root = base / "ct", base / "cp"
                carrier_temp.mkdir()
                carrier_root.mkdir()
                released_node = Path(installation["executable"]).parent / "node.exe"
                cursor_carrier = {
                    "PITOT_CURSOR_CARRIER_TEMP": str(carrier_temp),
                    "PITOT_CURSOR_CARRIER_ROOT": str(carrier_root),
                    "PITOT_CURSOR_RELEASE_NODE": str(released_node),
                    "PITOT_CURSOR_RELEASE_NODE_SHA256": hashlib.sha256(
                        released_node.read_bytes()
                    ).hexdigest(),
                    "PITOT_CURSOR_CANDIDATE": os.environ.get("PITOT_SOURCE_SHA", ""),
                    "PITOT_CURSOR_ISSUED_COMMAND": canary_command,
                }
            flags, extra_env = configure(
                args.agent,
                home,
                project,
                witness_command,
                proxy,
                pitot_command=pitot_command,
                witness_receipt=receipt_command,
                nonce=nonce,
                runtime_path=str(runtime_descriptor),
                cursor_carrier=cursor_carrier,
            )
            if args.agent == "cursor":
                extra_env["CURSOR_DATA_DIR"] = cleanup.enter_context(
                    tempfile.TemporaryDirectory(
                        prefix="pcd-", dir="/tmp" if os.name != "nt" else None
                    )
                )
                if windows_host():
                    extra_env["TMP"] = str(carrier_temp)
                    extra_env["TEMP"] = str(carrier_temp)
            environment = {
                **os.environ,
                **extra_env,
                "PATH": str(bin_dir)
                + os.pathsep
                + extra_env.get("PATH", os.environ.get("PATH", "")),
                "PITOT_REAL_BIN": str(args.pitot.resolve()),
                "PITOT_WITNESS_RECEIPT": str(witness_receipt),
                "PITOT_E2E_NONCE": nonce,
                "PITOT_RUNTIME": str(runtime_descriptor),
            }
            environment = agent_environment(args.agent, environment)
            if args.agent in {"devin", "deepseek"}:
                # Filter inherited vendor overrides, then restore only values
                # authored for this fixture (including its synthetic key).
                environment.update(extra_env)
            if args.agent == "cursor" and windows_host():
                # Cursor's terminal hint precedes SHELL selection. Avoid the
                # verification runner's Git Bash hint overriding checked pwsh.
                environment.pop("MSYSTEM", None)
                environment.pop("EXEPATH", None)
            executable = installation["executable"]
            installed_code = installation.get("installation")
            launch = (
                installation_command(installed_code)
                if installed_code is not None
                else [executable]
            )
            if installed_code is not None:
                environment = installation_environment(
                    args.agent, installed_code, environment
                )
            update_canary = (
                qwen_update_canary(installation, environment, base)
                if args.agent == "qwen"
                else None
            )
            if host_controls_wsl:
                wsl_environment = {
                    **extra_env,
                    "HOME": wsl_path(home),
                    "USERPROFILE": wsl_path(home),
                    "PITOT_BIN": witness_command,
                    "PITOT_REAL_BIN": wsl_path(args.pitot),
                    "PITOT_WITNESS_RECEIPT": wsl_path(witness_receipt),
                    "PITOT_E2E_NONCE": nonce,
                    "PITOT_RUNTIME": wsl_path(runtime_descriptor),
                }
                executable_dir = executable.rsplit("/", 1)[0]
                wsl_environment["PATH"] = ":".join(
                    (
                        wsl_path(bin_dir),
                        executable_dir,
                        "/usr/local/sbin",
                        "/usr/local/bin",
                        "/usr/sbin",
                        "/usr/bin",
                        "/sbin",
                        "/bin",
                    )
                )
                assignments = [
                    f"{key}={value}" for key, value in wsl_environment.items()
                ]
                # Keep every argument distinct across the Windows/WSL boundary.
                # A reconstructed `bash -lc` string can alter quoting, expand a
                # host PATH, or leave the released agent waiting indefinitely.
                command = [
                    "wsl.exe",
                    "--distribution",
                    "Ubuntu",
                    "--cd",
                    wsl_path(project),
                    "--",
                    "env",
                    *assignments,
                    executable,
                    *flags,
                    prompt,
                ]
            elif args.agent == "deepseek":
                command = [
                    str(args.pitot.resolve()),
                    "acp",
                    "deepseek",
                    "--runtime",
                    str(runtime_descriptor),
                    "--prompt",
                    prompt,
                    "--exec",
                    launch[0],
                    "--entrypoint",
                    launch[1],
                    "--cwd",
                    str(project),
                    "--base-url",
                    f"{proxy}/v1",
                    "--model",
                    "pitot-control",
                    "--api-key-env",
                    "PITOT_DEEPSEEK_FAKE_KEY",
                    "--session-dir",
                    str(base / "deepseek-session"),
                ]
            elif args.agent == "devin":
                command = [
                    str(args.pitot.resolve()),
                    "acp",
                    "devin",
                    "--runtime",
                    str(runtime_descriptor),
                    "--prompt",
                    prompt,
                    "--exec",
                    executable,
                    "--cwd",
                    str(project),
                ]
            else:
                command = (
                    [executable, "-p", prompt, *flags]
                    if args.agent in {"copilot", "kimi"}
                    else [*launch, *flags, prompt]
                )
            verify_live_entrypoint(installation)
            try:
                completed = subprocess.run(
                    command,
                    cwd=project,
                    env=environment,
                    text=True,
                    encoding="utf-8",
                    errors="replace",
                    stdout=subprocess.PIPE,
                    stderr=subprocess.STDOUT,
                    timeout=420,
                )
            except subprocess.TimeoutExpired as error:
                proxy_state = (
                    proxy_receipt.read_text(encoding="utf-8")
                    if proxy_receipt.is_file()
                    else '{"missing":true}'
                )
                witness_state = (
                    witness_receipt.read_text(encoding="utf-8")
                    if witness_receipt.is_file()
                    else '{"missing":true}'
                )
                partial_output = error.stdout or ""
                raise RuntimeError(
                    f"released agent timed out; proxy={proxy_state}; witness={witness_state}; "
                    f"output={partial_output[-4000:]}"
                ) from error
            finally:
                verify_live_entrypoint(installation)
                if update_canary is not None and update_canary.exists():
                    raise RuntimeError(
                        "Qwen selected an update outside the recorded installation"
                    )
            # GitHub's Windows Python console defaults to CP-1252 while several
            # released CLIs emit Unicode status glyphs. Emit UTF-8 bytes so the
            # reporting layer cannot fail after a successful agent session.
            sys.stdout.buffer.write(completed.stdout.encode("utf-8", errors="replace"))
            sys.stdout.buffer.flush()
            if proxy_process.poll() is not None:
                proxy_output = proxy_process.communicate(timeout=5)[0] or ""
                raise RuntimeError(
                    f"model-control proxy exited during the released-agent session "
                    f"(exit {proxy_process.returncode}): {proxy_output[-4000:]}"
                )
            if args.agent == "devin" and proxy_request_log.is_file():
                # Connection-level journal from the proxy: shows whether a
                # hanging fetch ever reached the server and on which socket.
                journal = proxy_request_log.read_text(
                    encoding="utf-8", errors="replace"
                )
                sys.stdout.buffer.write(
                    (
                        "PITOT_PROXY_REQUEST_LOG begin\n"
                        + journal[-12000:]
                        + "PITOT_PROXY_REQUEST_LOG end\n"
                    ).encode("utf-8", errors="replace")
                )
                sys.stdout.buffer.flush()
            if args.expect_incompatible_response:
                observed = (
                    json.loads(proxy_receipt.read_text(encoding="utf-8"))
                    if proxy_receipt.is_file()
                    else {}
                )
                if not (
                    observed.get("initial_prompt_observed") is True
                    and observed.get("fault_response_emitted") == "text"
                    and observed.get("tool_call_response_emitted") is False
                    and observed.get("tool_result_observed") is False
                    and not witness_receipt.exists()
                ):
                    raise RuntimeError(
                        "incompatible response unexpectedly entered the hook/tool path"
                    )
                print("PITOT_INCOMPATIBLE_RESPONSE_REJECTED evidence=binary-observed")
                return 0
            carrier_records = None
            if args.agent == "deepseek":
                sessions = list(
                    (base / "deepseek-session").glob(
                        "home/.dsh/sessions/*/*/session.v4.jsonl.zstd"
                    )
                )
                if len(sessions) == 1:
                    args.evidence.parent.mkdir(parents=True, exist_ok=True)
                    shutil.copyfile(
                        sessions[0],
                        args.evidence.parent / "deepseek-native-session.zstd",
                    )
            if cursor_carrier is not None:
                carrier_records = []
                for phase in ("allow", "deny"):
                    proof_path = carrier_root / f"{nonce}-{phase}.json"
                    if (
                        not proof_path.is_file()
                        or proof_path.stat().st_size > 3 * 1048576
                    ):
                        raise RuntimeError(
                            "Cursor carrier snapshot is missing or oversized; "
                            + cursor_failure_diagnostics(
                                proxy_receipt, carrier_root, nonce
                            )
                        )
                    carrier_records.append(
                        json.loads(proof_path.read_text(encoding="utf-8"))
                    )
            evidence = validate_receipts(
                agent,
                args.platform,
                nonce,
                installation,
                proxy_receipt,
                witness_receipt,
                controller_receipt,
                consumer_receipt,
                canary_receipt,
                runtime_identity,
                completed.returncode,
                completed.stdout,
                prompt,
                args.capture_output,
                expected_cursor_commands=[
                    f"{canary_command} PITOT_ALLOW {nonce}",
                    f"{canary_command} PITOT_DENY {nonce}",
                ]
                if args.agent == "cursor" and windows_host()
                else None,
                cursor_carrier_records=carrier_records,
                cursor_carrier_context=extra_env
                if cursor_carrier is not None
                else None,
                deepseek_session=base / "deepseek-session"
                if args.agent == "deepseek"
                else None,
            )
            args.evidence.parent.mkdir(parents=True, exist_ok=True)
            args.evidence.write_text(
                json.dumps(evidence, indent=2, sort_keys=True) + "\n", encoding="utf-8"
            )
            print("PITOT_E2E_RESULT mode=real_cli evidence=nonce-correlated")
        finally:
            proxy_process.terminate()
            try:
                proxy_process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                proxy_process.kill()
            runtime_process.terminate()
            try:
                runtime_process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                runtime_process.kill()
            if cursor_system_canary:
                if host_controls_wsl:
                    subprocess.run(
                        [
                            "wsl.exe",
                            "--distribution",
                            "Ubuntu",
                            "--",
                            "rm",
                            "-f",
                            "/usr/local/bin/pitot-e2e-canary",
                        ],
                        check=False,
                        text=True,
                        stdout=subprocess.PIPE,
                        stderr=subprocess.STDOUT,
                    )
                else:
                    Path("/usr/local/bin/pitot-e2e-canary").unlink(missing_ok=True)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
