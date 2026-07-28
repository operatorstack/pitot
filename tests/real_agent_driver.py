#!/usr/bin/env python3
"""Run one released agent through prompt, model, hook, Pitot, and tool result."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import secrets
import re
import shlex
import shutil
import subprocess
import sys
import tempfile
import time


LAB = Path(__file__).resolve().parent.parent
ROOT = LAB.parents[1] if LAB.name == "15-pitot" else LAB
MANIFEST = json.loads((LAB / "adapter-verification.json").read_text(encoding="utf-8"))
ENDPOINT_PROVENANCE = json.loads((LAB / "tests/endpoint-provenance.json").read_text(encoding="utf-8"))


def write_json(path: Path, value: object) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2) + "\n", encoding="utf-8")


def hook_group(event: str, matcher: str, command: str) -> dict[str, object]:
    return {"hooks": {event: [{"matcher": matcher, "hooks": [{"name": "pitot", "type": "command", "command": command}]}]}}


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
        return subprocess.list2cmdline([executable, "--role", "canary", "--receipt", receipt])
    return shlex.join([executable, "--role", "canary", "--receipt", receipt])


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
    witness_invocation = f'& "{witness_command}"' if windows_host() else f'"{witness_command}"'
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
        write_json(home / ".claude/settings.json", hook_group("PreToolUse", "Bash", f'{witnessed_direct} hook claude --runtime "{runtime_path}"'))
        env["ANTHROPIC_BASE_URL"] = proxy
        env["CLAUDE_CODE_MAX_RETRIES"] = "0"
        prompt_flag = ["--print", "--dangerously-skip-permissions", "--tools", "Bash", "--model", "sonnet"]
    elif agent == "codex":
        if windows_host():
            bridge = LAB / "integrations/codex/PreToolUse.ps1"
            hook_command = (
                f'& "{bridge}" '
                f'-Pitot "{witness_command}" -RealBin "{pitot_command}" '
                f'-Receipt "{witness_receipt}" -Nonce "{nonce}" -Runtime "{runtime_path}"'
            )
        else:
            hook_command = f'{witnessed} hook codex --runtime "{runtime_path}" >/dev/null'
        write_json(home / ".codex/hooks.json", hook_group("PreToolUse", "Bash", hook_command))
        (home / ".codex/config.toml").write_text(
            f'model = "pitot-control"\nmodel_provider = "pitot"\n[model_providers.pitot]\nname = "Pitot local control"\nbase_url = "{proxy}/v1"\nenv_key = "OPENAI_API_KEY"\nwire_api = "responses"\n',
            encoding="utf-8",
        )
        env["CODEX_HOME"] = str(home / ".codex")
        prompt_flag = ["exec", "--dangerously-bypass-approvals-and-sandbox", "--dangerously-bypass-hook-trust", "--model", "pitot-control"]
    elif agent == "copilot":
        hooks = home / ".copilot/hooks"
        hooks.mkdir(parents=True, exist_ok=True)
        source = LAB / "integrations/copilot" / ("PreToolUse.ps1" if windows_host() else "PreToolUse")
        target = hooks / source.name
        shutil.copy2(source, target)
        target.chmod(0o755)
        hook_command = f'powershell -NoProfile -NonInteractive -File "{target}"' if windows_host() else str(target)
        write_json(home / ".copilot/settings.json", hook_group("PreToolUse", "Bash", hook_command))
        env["PITOT_BIN"] = witness_command
        env.update({
            "COPILOT_PROVIDER_BASE_URL": f"{proxy}/v1",
            "COPILOT_PROVIDER_API_KEY": "pitot-local-only",
            "COPILOT_PROVIDER_TYPE": "openai",
            "COPILOT_PROVIDER_WIRE_API": "completions",
            "COPILOT_PROVIDER_MODEL_ID": "gpt-4o",
            "COPILOT_PROVIDER_WIRE_MODEL": "pitot-control",
            "COPILOT_MODEL": "gpt-4o",
            "COPILOT_OFFLINE": "true",
            "COPILOT_HOME": str(home / ".copilot"),
        })
        prompt_flag = ["--allow-all-tools", "--model", "gpt-4o", "--no-auto-update", "--no-remote"]
    elif agent == "cursor":
        source = LAB / "integrations/cursor/beforeShellExecution"
        target = project / ".cursor/hooks/beforeShellExecution"
        target.parent.mkdir(parents=True, exist_ok=True)
        # Cursor runs in WSL on the Windows cell. A Windows checkout may expose
        # CRLF bytes through /mnt/<drive>, which turns the bridge shebang into
        # `bash\r` and makes Cursor fail the hook closed before Pitot can run.
        target.write_bytes(source.read_bytes().replace(b"\r\n", b"\n"))
        target.chmod(0o755)
        hook_command = (
            f'"{target}" "{witness_command}" "{pitot_command}" '
            f'"{witness_receipt}" "{nonce}" "{runtime_path}"'
        )
        write_json(project / ".cursor/hooks.json", {"version": 1, "hooks": {"beforeShellExecution": [{"command": hook_command, "failClosed": True}]}})
        # Cursor exposes an authless CLI mode for endpoint compatibility tests;
        # model inference is still supplied only by the pinned local endpoint.
        env["CURSOR_AGENT_CLI_AUTHLESS_MODE"] = "true"
        env["CURSOR_AUTH_TOKEN"] = "pitot-local-only"
        prompt_flag = ["--endpoint", proxy, "--print", "--force"]
    elif agent == "gemini":
        hooks = home / ".gemini/hooks"
        hooks.mkdir(parents=True, exist_ok=True)
        source = LAB / "integrations/gemini" / ("BeforeTool.ps1" if windows_host() else "BeforeTool")
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
        settings["security"] = {"auth": {"selectedType": "gemini-api-key"}, "folderTrust": {"enabled": False}}
        write_json(home / ".gemini/settings.json", settings)
        env.update({"GOOGLE_GEMINI_BASE_URL": proxy, "GEMINI_CLI_HOME": str(home), "GEMINI_CLI_TRUST_WORKSPACE": "true", "PITOT_BIN": witness_command})
        prompt_flag = ["--skip-trust", "--approval-mode", "yolo", "--model", "pitot-control", "-p"]
    elif agent == "kimi":
        config = home / ".kimi-code/config.toml"
        config.parent.mkdir(parents=True, exist_ok=True)
        config.write_text(
            'default_model = "pitot-control"\n'
            '[providers.pitot]\n'
            'type = "openai"\n'
            f'base_url = {json.dumps(proxy + "/v1")}\n'
            'api_key = "pitot-local-only"\n'
            '[models."pitot-control"]\n'
            'provider = "pitot"\n'
            'model = "pitot-control"\n'
            'max_context_size = 32768\n'
            'capabilities = ["tool_use"]\n'
            '[[hooks]]\n'
            'event = "PreToolUse"\n'
            'matcher = ".*"\n'
            f'command = {json.dumps(witnessed_direct + " hook kimi --runtime " + runtime_path)}\n',
            encoding="utf-8",
        )
        env["KIMI_CODE_HOME"] = str(home / ".kimi-code")
        env["KIMI_CODE_NO_AUTO_UPDATE"] = "1"
        prompt_flag = []
    elif agent == "qwen":
        hooks = home / ".qwen/hooks"
        hooks.mkdir(parents=True, exist_ok=True)
        source = LAB / "integrations/qwen" / ("PreToolUse.cjs" if windows_host() else "PreToolUse")
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
        settings = hook_group("PreToolUse", "^(Bash|run_shell_command)$", hook_command)
        settings["modelProviders"] = {"openai": {"protocol": "openai", "models": [{"id": "pitot-control", "name": "Pitot control", "envKey": "OPENAI_API_KEY", "baseUrl": f"{proxy}/v1"}]}}
        settings["security"] = {"auth": {"selectedType": "openai"}}
        settings["model"] = {"name": "pitot-control"}
        write_json(home / ".qwen/settings.json", settings)
        env.update({"OPENAI_API_KEY": "pitot-local-only", "PITOT_BIN": witness_command})
        prompt_flag = ["--model", "pitot-control", "-y", "-p"]
    elif agent == "pi":
        extension = LAB / "integrations/pi/pitot.ts"
        write_json(home / ".pi/agent/models.json", {"providers": {"pitot": {"baseUrl": f"{proxy}/v1", "apiKey": "pitot-local-only", "api": "openai-completions", "models": [{"id": "pitot-control", "name": "Pitot control", "reasoning": False, "input": ["text"], "contextWindow": 32000, "maxTokens": 4096}]}}})
        prompt_flag = ["--no-session", "--print", "--provider", "pitot", "--model", "pitot-control", "-e", str(extension)]
    elif agent == "opencode":
        plugin = LAB / "integrations/opencode/pitot.ts"
        # Use the released binary's bundled OpenAI provider. A custom provider
        # would dynamically install @ai-sdk/openai-compatible and make endpoint
        # verification depend on a second unpinned network package.
        config = home / ".config/opencode/opencode.json"
        write_json(config, {"plugin": [f"file://{plugin}"], "provider": {"openai": {"options": {"baseURL": f"{proxy}/v1", "apiKey": "pitot-local-only"}, "models": {"pitot-control": {"name": "Pitot control"}}}}, "model": "openai/pitot-control", "permission": {"bash": "allow"}})
        env["OPENCODE_CONFIG"] = str(config)
        prompt_flag = ["--print-logs", "--log-level", "DEBUG", "run", "--model", "openai/pitot-control"]
    else:
        raise ValueError(f"unsupported agent {agent}")
    return prompt_flag, env


def prepare_cursor_keychain(home: Path, environment: dict[str, str]) -> None:
    """Provide Cursor an isolated unlocked macOS credential store."""
    if sys.platform != "darwin":
        return
    keychain = home / "Library/Keychains/login.keychain-db"
    keychain.parent.mkdir(parents=True, exist_ok=True)
    password = "pitot-e2e-local-only"
    for command in (
        ["security", "create-keychain", "-p", password, str(keychain)],
        ["security", "set-keychain-settings", "-lut", "900", str(keychain)],
        ["security", "unlock-keychain", "-p", password, str(keychain)],
        ["security", "list-keychains", "-d", "user", "-s", str(keychain)],
        ["security", "default-keychain", "-d", "user", "-s", str(keychain)],
    ):
        subprocess.run(command, env=environment, check=True, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)


def capture_record(agent: dict[str, object], platform: str, installation: dict[str, object], proxy: dict[str, object]) -> dict[str, object]:
    """Normalize the two receipts without trusting a manifest protocol hint."""
    observed = proxy.get("endpoint_observed")
    dialect = proxy.get("protocol")
    if not isinstance(observed, dict) or dialect not in {
        "anthropic_messages", "openai_chat", "openai_responses", "gemini_generate_content", "cursor_connect_proto",
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
    return {**core, "capture_sha256": hashlib.sha256(json.dumps(core, sort_keys=True, separators=(",", ":")).encode()).hexdigest()}


def validate_capture_fixture(record: dict[str, object]) -> dict[str, object]:
    matches = [
        item for item in ENDPOINT_PROVENANCE.get("cells", [])
        if item.get("agent") == record["agent"] and item.get("platform") == record["platform"]
    ]
    if len(matches) != 1 or matches[0] != record:
        raise RuntimeError("binary-observed contract drifted from its supervised platform fixture")
    return {
        "fixture": f"tests/endpoint-provenance.json#{record['agent']}/{record['platform']}",
        "fixture_sha256": record["capture_sha256"],
        "provenance": record["provenance"],
        "dialect": record["dialect"],
        "request": record["request"],
        "response": record["response"],
        "executable_sha256": record["executable_sha256"],
    }


def json_lines(path: Path) -> list[dict[str, object]]:
    if not path.is_file():
        return []
    return [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines() if line.strip()]


def validate_receipts(
    agent: dict[str, object], platform: str, nonce: str, installation: dict[str, object],
    proxy_path: Path, witness_path: Path, controller_path: Path, consumer_path: Path,
    canary_path: Path, runtime_identity: dict[str, object], exit_code: int, output: str,
    prompt: str, capture_output: Path | None = None,
) -> dict[str, object]:
    proxy = json.loads(proxy_path.read_text(encoding="utf-8")) if proxy_path.is_file() else {"missing": True}
    witnesses = json_lines(witness_path)
    controller = json_lines(controller_path)
    consumers = json_lines(consumer_path)
    canary = canary_path.read_text(encoding="utf-8").splitlines() if canary_path.is_file() else []
    proxy_flags = (
        "initial_prompt_observed", "allow_tool_call_response_emitted", "allow_tool_result_observed",
        "deny_tool_call_response_emitted", "denied_result_observed", "final_response_emitted",
    )
    final_marker = f"PITOT_E2E_COMPLETE {nonce}"
    if exit_code != 0 or "hook: PreToolUse Failed" in output or final_marker not in output or not all(proxy.get(flag) is True for flag in proxy_flags):
        raise RuntimeError(f"agent loop incomplete (exit={exit_code}, proxy={proxy}, witnesses={witnesses})\n{output[-4000:]}")
    if proxy.get("nonce") != nonce or len(witnesses) != 2:
        raise RuntimeError("proxy and Pitot witness receipts do not identify exactly two hook actions")
    if any(item.get("nonce") != nonce or item.get("host") != agent["id"] or item.get("valid") is not True for item in witnesses):
        raise RuntimeError("Pitot hook witnesses escaped the nonce-bound real-agent session")
    if [item.get("pitot_exit") for item in witnesses] != [0, 2]:
        raise RuntimeError(f"real hook did not carry one allow and one deny: {witnesses}")
    action_ids = [item.get("action_id") for item in witnesses]
    if len(set(action_ids)) != 2 or not all(isinstance(item, str) and item.startswith("act_") for item in action_ids):
        raise RuntimeError("hook actions lack unique Pitot correlation ids")
    requests = [item.get("value") for item in controller if item.get("receipt_type") == "request"]
    responses = [item.get("value") for item in controller if item.get("receipt_type") == "response"]
    if len(requests) != 2 or len(responses) != 2:
        raise RuntimeError(f"Controller did not receive and resolve both hook actions: {controller}")
    if [item.get("action_id") for item in requests] != action_ids or [item.get("action_id") for item in responses] != action_ids:
        raise RuntimeError("Controller correlation ids do not match the real hook actions")
    if [item.get("outcome") for item in responses] != ["allow", "deny"] or responses[1].get("message") != f"PITOT_CONTROLLER_DENY {nonce}":
        raise RuntimeError("external Controller did not produce the nonce-bound allow/deny trajectory")
    if len(consumers) != 2 or [item.get("action", {}).get("id") for item in consumers] != action_ids:
        raise RuntimeError(f"passive Consumer did not receive both real hook observations: {consumers}")
    if any(item.get("content", {}).get("mode") != "sha256" or "full" in item.get("content", {}) for item in consumers):
        raise RuntimeError("Consumer projection did not remove full command content")
    if canary != [f"PITOT_ALLOW {nonce}"]:
        raise RuntimeError(f"canary execution count violated allow/deny control: {canary}")
    runtime_public = {key: runtime_identity.get(key) for key in ("schema_version", "instance_id", "pid", "endpoint", "config_sha256")}
    if runtime_public["schema_version"] != 1 or not all(runtime_public.get(key) for key in ("instance_id", "endpoint", "config_sha256")):
        raise RuntimeError("authenticated Pitot runtime identity is incomplete")
    if installation.get("agent") != agent["id"] or installation.get("version") != agent["version"]:
        raise RuntimeError("installation receipt does not match supervised manifest")
    captured = capture_record(agent, platform, installation, proxy)
    if capture_output is not None:
        write_json(capture_output, {"schema_version": 1, "accepted": True, "cell": captured})
        endpoint_evidence = {
            "fixture": "candidate",
            "fixture_sha256": captured["capture_sha256"],
            "provenance": captured["provenance"],
            "dialect": captured["dialect"],
            "request": captured["request"],
            "response": captured["response"],
            "executable_sha256": captured["executable_sha256"],
        }
    else:
        endpoint_evidence = validate_capture_fixture(captured)
    return {
        "schema_version": 2,
        "agent": agent["id"],
        "cli": {"version": installation["version"], "executable": installation["executable"], "executable_sha256": installation["executable_sha256"], "installer": installation["installer"], "runtime": installation["runtime"]},
        "prompt_hash": hashlib.sha256(prompt.encode()).hexdigest(),
        "protocol": proxy["protocol"],
        "endpoint": endpoint_evidence,
        "nonce": nonce,
        "receipts": {**{flag: True for flag in proxy_flags}, "consumer_observed": True, "controller_allow_observed": True, "controller_deny_observed": True, "deny_canary_absent": True, "final_output_observed": True, "cli_exit_zero": True},
        "runtime": runtime_public,
        "hooks": [{"host": item["host"], "action_kind": item["action_kind"], "action_id": item["action_id"], "pitot_exit": item["pitot_exit"], "nonce": item["nonce"]} for item in witnesses],
        "controller": {"id": "e2e-shell-controller", "action_ids": action_ids, "outcomes": ["allow", "deny"]},
        "consumer": {"id": "e2e-audit", "action_ids": action_ids, "projection": "sha256"},
        "canary": {"executions": canary, "denied_executions": 0},
    }


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
    agent = next(item for item in MANIFEST["agents"] if item["id"] == args.agent)
    installation = json.loads(args.installation.read_text(encoding="utf-8"))
    nonce = secrets.token_hex(16)
    prompt = f"Pitot E2E session {nonce}: execute the requested verification command"
    with tempfile.TemporaryDirectory(prefix="pitot-real-agent-", ignore_cleanup_errors=True) as temporary:
        base = Path(temporary)
        runtime = installation["runtime"]
        host_controls_wsl = runtime == "wsl" and os.name == "nt"
        home, project, bin_dir = base / "home", base / "project", base / "bin"
        home.mkdir(); project.mkdir(); bin_dir.mkdir()
        canary_receipt = base / "canary.jsonl"
        canary_executable = args.test_role.resolve()
        cursor_system_canary = False
        if args.agent == "cursor" and host_controls_wsl:
            occupied = subprocess.run(
                ["wsl.exe", "--distribution", "Ubuntu", "--", "test", "-e", "/usr/local/bin/pitot-e2e-canary"],
                check=False, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
            )
            if occupied.returncode == 0:
                raise RuntimeError("refusing to replace an existing WSL canary command")
            subprocess.run(
                ["wsl.exe", "--distribution", "Ubuntu", "--", "install", "-m", "0755", wsl_path(canary_executable), "/usr/local/bin/pitot-e2e-canary"],
                check=True, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
            )
            canary_executable = Path("/usr/local/bin/pitot-e2e-canary")
            cursor_system_canary = True
        elif args.agent == "cursor" and os.name != "nt" and os.access("/usr/local/bin", os.W_OK):
            if Path("/usr/local/bin/pitot-e2e-canary").exists():
                raise RuntimeError("refusing to replace an existing system canary command")
            shutil.copy2(canary_executable, "/usr/local/bin/pitot-e2e-canary")
            Path("/usr/local/bin/pitot-e2e-canary").chmod(0o755)
            canary_executable = Path("/usr/local/bin/pitot-e2e-canary")
            cursor_system_canary = True
        receipt_argument = wsl_path(canary_receipt) if host_controls_wsl else str(canary_receipt)
        # Forward slashes keep the same native Windows command valid in
        # PowerShell, cmd.exe, and the Unix-like shells selected by hosts.
        canary_command = render_canary_command(
            str(canary_executable), receipt_argument, windows_host() and not host_controls_wsl,
        )
        ready, proxy_receipt, witness_receipt = base / "proxy.url", base / "proxy.json", base / "witness.jsonl"
        runtime_descriptor = base / "runtime.json"
        runtime_config = base / "pitot.json"
        consumer_receipt = base / "consumer.jsonl"
        controller_receipt = base / "controller.jsonl"
        runtime_config.write_text(json.dumps({
            "consumers": [{
                "id": "e2e-audit",
                "command": [str(args.test_role.resolve()), "--role", "consumer", "--receipt", str(consumer_receipt)],
                "events": ["action.requested"],
                "projection": {"content": "sha256"},
            }],
            "controllers": {
                "shell": {
                    "id": "e2e-shell-controller",
                    "command": [str(args.test_role.resolve()), "--role", "controller", "--id", "e2e-shell-controller", "--receipt", str(controller_receipt), "--nonce", nonce],
                    "deadline_ms": 5000,
                    "on_timeout": "deny",
                    "on_unavailable": "deny",
                }
            },
        }, indent=2) + "\n", encoding="utf-8")
        runtime_process = subprocess.Popen(
            [str(args.pitot.resolve()), "run", "--config", str(runtime_config), "--runtime", str(runtime_descriptor)],
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            text=True,
        )
        for _ in range(600):
            if runtime_descriptor.is_file(): break
            if runtime_process.poll() is not None:
                output = runtime_process.stdout.read() if runtime_process.stdout else ""
                raise RuntimeError(f"Pitot runtime exited before becoming ready: {output.strip()}")
            time.sleep(0.05)
        else:
            runtime_process.terminate()
            raise RuntimeError("Pitot runtime did not publish its authenticated descriptor")
        runtime_identity = json.loads(runtime_descriptor.read_text(encoding="utf-8"))
        if args.agent == "cursor":
            if host_controls_wsl:
                # Keep Cursor and its HTTP/2 Connect control proxy in the same
                # supported WSL network namespace. The workflow installs a
                # pinned Linux Node runtime specifically for this harness.
                proxy_command = [
                    "wsl.exe", "--distribution", "Ubuntu", "--", "node",
                    wsl_path(LAB / "tests/cursor_control_proxy.mjs"),
                    "--nonce", nonce,
                    "--receipt", wsl_path(proxy_receipt),
                    "--ready-file", wsl_path(ready),
                    "--canary-command", canary_command,
                    "--response-fault", args.response_fault,
                ]
            else:
                proxy_command = ["node", str(LAB / "tests/cursor_control_proxy.mjs"), "--nonce", nonce, "--receipt", str(proxy_receipt), "--ready-file", str(ready), "--canary-command", canary_command, "--response-fault", args.response_fault]
        else:
            proxy_command = [sys.executable, str(LAB / "tests/model_control_proxy.py"), "--agent", args.agent, "--nonce", nonce, "--receipt", str(proxy_receipt), "--ready-file", str(ready), "--canary-command", canary_command, "--response-fault", args.response_fault]
        proxy_process = subprocess.Popen(
            proxy_command,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            text=True,
        )
        try:
            for _ in range(600):
                if ready.is_file(): break
                if proxy_process.poll() is not None:
                    output = proxy_process.stdout.read() if proxy_process.stdout else ""
                    raise RuntimeError(f"model-control proxy exited before becoming ready: {output.strip()}")
                time.sleep(0.05)
            else:
                proxy_process.terminate()
                output = proxy_process.communicate(timeout=5)[0] if proxy_process.stdout else ""
                raise RuntimeError(f"model-control proxy did not become ready: {output.strip()}")
            proxy = ready.read_text(encoding="utf-8").strip()
            witness_command = wsl_path(args.witness) if host_controls_wsl else str(args.witness.resolve())
            pitot_command = wsl_path(args.pitot) if host_controls_wsl else str(args.pitot.resolve())
            receipt_command = wsl_path(witness_receipt) if host_controls_wsl else str(witness_receipt)
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
            )
            environment = {**os.environ, **extra_env, "PATH": str(bin_dir) + os.pathsep + os.environ.get("PATH", ""), "PITOT_REAL_BIN": str(args.pitot.resolve()), "PITOT_WITNESS_RECEIPT": str(witness_receipt), "PITOT_E2E_NONCE": nonce, "PITOT_RUNTIME": str(runtime_descriptor)}
            executable = installation["executable"]
            if args.agent == "cursor" and not host_controls_wsl:
                prepare_cursor_keychain(home, environment)
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
                wsl_environment["PATH"] = ":".join((wsl_path(bin_dir), executable_dir, "/usr/local/sbin", "/usr/local/bin", "/usr/sbin", "/usr/bin", "/sbin", "/bin"))
                assignments = [f"{key}={value}" for key, value in wsl_environment.items()]
                # Keep every argument distinct across the Windows/WSL boundary.
                # A reconstructed `bash -lc` string can alter quoting, expand a
                # host PATH, or leave the released agent waiting indefinitely.
                command = [
                    "wsl.exe", "--distribution", "Ubuntu", "--cd", wsl_path(project),
                    "--", "env", *assignments, executable, *flags, prompt,
                ]
            else:
                command = [executable, "-p", prompt, *flags] if args.agent in {"copilot", "kimi"} else [executable, *flags, prompt]
            try:
                completed = subprocess.run(command, cwd=project, env=environment, text=True, encoding="utf-8", errors="replace", stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=420)
            except subprocess.TimeoutExpired as error:
                proxy_state = proxy_receipt.read_text(encoding="utf-8") if proxy_receipt.is_file() else '{"missing":true}'
                witness_state = witness_receipt.read_text(encoding="utf-8") if witness_receipt.is_file() else '{"missing":true}'
                partial_output = error.stdout or ""
                raise RuntimeError(
                    f"released agent timed out; proxy={proxy_state}; witness={witness_state}; "
                    f"output={partial_output[-4000:]}"
                ) from error
            # GitHub's Windows Python console defaults to CP-1252 while several
            # released CLIs emit Unicode status glyphs. Emit UTF-8 bytes so the
            # reporting layer cannot fail after a successful agent session.
            sys.stdout.buffer.write(completed.stdout.encode("utf-8", errors="replace"))
            sys.stdout.buffer.flush()
            if args.expect_incompatible_response:
                observed = json.loads(proxy_receipt.read_text(encoding="utf-8")) if proxy_receipt.is_file() else {}
                if not (
                    observed.get("initial_prompt_observed") is True
                    and observed.get("fault_response_emitted") == "text"
                    and observed.get("tool_call_response_emitted") is False
                    and observed.get("tool_result_observed") is False
                    and not witness_receipt.exists()
                ):
                    raise RuntimeError("incompatible response unexpectedly entered the hook/tool path")
                print("PITOT_INCOMPATIBLE_RESPONSE_REJECTED evidence=binary-observed")
                return 0
            evidence = validate_receipts(
                agent, args.platform, nonce, installation, proxy_receipt, witness_receipt,
                controller_receipt, consumer_receipt, canary_receipt, runtime_identity,
                completed.returncode, completed.stdout, prompt, args.capture_output,
            )
            args.evidence.parent.mkdir(parents=True, exist_ok=True)
            args.evidence.write_text(json.dumps(evidence, indent=2, sort_keys=True) + "\n", encoding="utf-8")
            print("PITOT_E2E_RESULT mode=real_cli evidence=nonce-correlated")
        finally:
            proxy_process.terminate()
            try: proxy_process.wait(timeout=5)
            except subprocess.TimeoutExpired: proxy_process.kill()
            runtime_process.terminate()
            try: runtime_process.wait(timeout=5)
            except subprocess.TimeoutExpired: runtime_process.kill()
            if cursor_system_canary:
                if host_controls_wsl:
                    subprocess.run(
                        ["wsl.exe", "--distribution", "Ubuntu", "--", "rm", "-f", "/usr/local/bin/pitot-e2e-canary"],
                        check=False, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                    )
                else:
                    Path("/usr/local/bin/pitot-e2e-canary").unlink(missing_ok=True)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
