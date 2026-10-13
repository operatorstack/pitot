#!/usr/bin/env python3
"""Codex -> Pitot -> shared supervisor -> Pitot -> Qwen/DeepSeek, locally."""

from __future__ import annotations

import argparse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import secrets
import shlex
import shutil
import signal
import subprocess
import sys
import tempfile
import threading
import time
from urllib.request import Request, urlopen

from replay import Replay, payload, stream_bytes
from supervisor import Supervisor

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = Path(__file__).resolve()


def post(connection: Path, path: str, body: dict, timeout: int = 5) -> dict:
    settings = json.loads(connection.read_text())
    request = Request(
        settings["url"] + path,
        json.dumps(body).encode(),
        {
            "Content-Type": "application/json",
            "Authorization": "Bearer " + settings["token"],
        },
    )
    with urlopen(request, timeout=timeout) as response:
        return json.load(response)


def controller(connection: Path) -> None:
    sys.path.insert(0, str(ROOT / "sdk/python"))
    from pitot import allow, deny, run_controller

    def decide(request):
        try:
            result = post(
                connection,
                "/admit",
                {"action_id": request.action_id, "data": request.data},
                timeout=2,
            )
            return (allow if result["allowed"] else deny)(result["reason"])
        except Exception as error:
            return deny(f"Supervisor unavailable: {error}")

    run_controller("supervisory-control", decide)


def qwen_hook(pitot: str, runtime: str) -> None:
    """Qwen needs a native JSON decision even when the hook transport fails."""
    try:
        result = subprocess.run(
            [pitot, "hook", "qwen", "--runtime", runtime],
            input=sys.stdin.buffer.read(4 * 1024 * 1024 + 1),
            capture_output=True,
            timeout=5,
        )
        allowed = result.returncode == 0
        reason = result.stderr.decode(errors="replace")[:1024] or (
            "Pitot accepted the shell action"
            if allowed
            else "Pitot rejected the shell action"
        )
    except (OSError, subprocess.TimeoutExpired) as error:
        allowed, reason = False, f"Pitot unavailable: {error}"
    print(
        json.dumps(
            {
                "hookSpecificOutput": {
                    "hookEventName": "PreToolUse",
                    "permissionDecision": "allow" if allowed else "deny",
                    "permissionDecisionReason": reason,
                }
            }
        )
    )


def write_json(path: Path, value: dict) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2) + "\n")
    path.chmod(0o600)


class Application:
    def __init__(self, directory: Path, worker: str, executables: dict[str, str]):
        self.directory = directory
        self.project = directory / "project"
        self.project.mkdir()
        self.executables = executables
        self.worker = worker
        self.token = secrets.token_hex(24)
        self.runtime = directory / "runtime.json"
        self.commands = {
            action: shlex.join(
                [
                    sys.executable,
                    str(SCRIPT),
                    "act",
                    action,
                    "--connection",
                    str(
                        directory
                        / (
                            ("codex" if action == "dispatch" else worker)
                            + "-connection.json"
                        )
                    ),
                ]
            )
            for action in ("dispatch", "build", "release")
        }
        self.supervisor = Supervisor(self.project, worker, self.commands)
        self.replays = {
            actor: Replay(actor, self.commands) for actor in ("codex", worker)
        }
        self.processes: list[subprocess.Popen] = []
        self.errors: list[str] = []

    def environment(self, actor: str) -> dict[str, str]:
        home = self.directory / (actor + "-home")
        home.mkdir(exist_ok=True)
        write_json(
            self.directory / (actor + "-connection.json"),
            {
                "url": self.url,
                "token": self.token,
                "actor": actor,
            },
        )
        # Do not carry human credentials, provider settings or hook profiles.
        return {
            "PATH": os.environ["PATH"],
            "HOME": str(home),
            "USERPROFILE": str(home),
            "TMPDIR": str(self.directory),
            "LANG": "en_US.UTF-8",
            "PITOT_RUNTIME": str(self.runtime),
            "PITOT_BIN": self.executables["pitot"],
            "OPENAI_API_KEY": "pitot-local-only",
            "PITOT_DEEPSEEK_FAKE_KEY": "pitot-local-only",
            "DO_NOT_TRACK": "1",
            "DISABLE_TELEMETRY": "1",
        }

    def launch(self, actor: str) -> list[str]:
        env = self.environment(actor)
        home = Path(env["HOME"])
        hook = shlex.join(
            [self.executables["pitot"], "hook", actor, "--runtime", str(self.runtime)]
        )
        prompt = "Complete the delivery task using the available shell tool; respond to denied actions."
        if actor == "codex":
            write_json(
                home / ".codex/hooks.json",
                {
                    "hooks": {
                        "PreToolUse": [
                            {
                                "matcher": "Bash",
                                "hooks": [
                                    {"type": "command", "command": hook + " >/dev/null"}
                                ],
                            }
                        ]
                    }
                },
            )
            (home / ".codex/config.toml").write_text(
                'model="pitot-control"\nmodel_provider="pitot"\n[model_providers.pitot]\n'
                'name="Local replay"\nwire_api="responses"\nenv_key="OPENAI_API_KEY"\n'
                f'base_url="{self.url}/codex/v1"\n'
            )
            env["CODEX_HOME"] = str(home / ".codex")
            command = [
                self.executables[actor],
                "exec",
                "--dangerously-bypass-approvals-and-sandbox",
                "--dangerously-bypass-hook-trust",
                "--skip-git-repo-check",
                "--model",
                "pitot-control",
                prompt,
            ]
        elif actor == "qwen":
            env["QWEN_HOME"] = str(home / ".qwen")
            env["QWEN_CODE_SYSTEM_SETTINGS_PATH"] = str(home / "system-settings.json")
            env["QWEN_CODE_SYSTEM_DEFAULTS_PATH"] = str(home / "system-defaults.json")
            write_json(home / "system-settings.json", {})
            write_json(home / "system-defaults.json", {})
            write_json(
                home / ".qwen/settings.json",
                {
                    "hooks": {
                        "PreToolUse": [
                            {
                                "matcher": "^run_shell_command$",
                                "hooks": [
                                    {
                                        "name": "pitot",
                                        "type": "command",
                                        "command": shlex.join(
                                            [
                                                sys.executable,
                                                str(SCRIPT),
                                                "qwen-hook",
                                                "--pitot",
                                                self.executables["pitot"],
                                                "--runtime",
                                                str(self.runtime),
                                            ]
                                        ),
                                    }
                                ],
                            }
                        ]
                    },
                    "modelProviders": {
                        "openai": {
                            "protocol": "openai",
                            "models": [
                                {
                                    "id": "pitot-control",
                                    "name": "Local replay",
                                    "envKey": "OPENAI_API_KEY",
                                    "baseUrl": self.url + "/qwen/v1",
                                }
                            ],
                        }
                    },
                    "security": {"auth": {"selectedType": "openai"}},
                    "model": {"name": "pitot-control"},
                    "telemetry": {"enabled": False},
                },
            )
            command = [
                self.executables[actor],
                "--model",
                "pitot-control",
                "-y",
                "-p",
                prompt,
            ]
        else:
            command = [
                self.executables["pitot"],
                "acp",
                "deepseek",
                "--runtime",
                str(self.runtime),
                "--prompt",
                prompt,
                "--exec",
                self.executables[actor],
                "--cwd",
                str(self.project),
                "--base-url",
                self.url + "/deepseek/v1",
                "--model",
                "pitot-control",
                "--api-key-env",
                "PITOT_DEEPSEEK_FAKE_KEY",
                "--session-dir",
                str(home / "sessions"),
            ]
        return self.run(command, env, actor, timeout=180)

    def run(
        self, command: list[str], env: dict[str, str], name: str, timeout: int
    ) -> list[str]:
        process = subprocess.Popen(
            command,
            cwd=self.project,
            env=env,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            start_new_session=True,
            stdin=subprocess.DEVNULL,
        )
        self.processes.append(process)
        timed_out = False
        try:
            stdout, stderr = process.communicate(timeout=timeout)
        except subprocess.TimeoutExpired:
            os.killpg(process.pid, signal.SIGKILL)
            stdout, stderr = process.communicate()
            timed_out = True
        (self.directory / f"{name}.stdout").write_text(stdout)
        (self.directory / f"{name}.stderr").write_text(stderr)
        if timed_out:
            raise ValueError(f"{name} timed out; see {self.directory}")
        if process.returncode:
            raise ValueError(f"{name} exited {process.returncode}: {stderr[-1500:]}")
        return [stdout, stderr]

    def admit(self, body: dict) -> dict:
        event = body.get("data")
        if not isinstance(event, dict):
            return {"allowed": False, "reason": "SUPERVISOR_DENY missing native event"}
        content, host, action = (
            event.get(key) for key in ("content", "host", "action")
        )
        if (
            not all(isinstance(value, dict) for value in (content, host, action))
            or event.get("type") != "action.requested"
            or action.get("kind") != "shell"
            or action.get("id") != body.get("action_id")
            or not isinstance(content.get("full"), str)
        ):
            return {
                "allowed": False,
                "reason": "SUPERVISOR_DENY invalid native shell event",
            }
        allowed, reason = self.supervisor.admit(
            host.get("name"), content["full"], body["action_id"]
        )
        return {"allowed": allowed, "reason": reason}

    def act(self, body: dict) -> dict:
        actor, action = body.get("actor"), body.get("action")
        result = self.supervisor.execute(actor, action)
        # No state lock or Controller handler is held across these callbacks.
        if action == "dispatch":
            self.launch(self.worker)
            if self.supervisor.state != "released":
                raise ValueError("Worker finished without reaching released state")
            return {"result": "DISPATCH_OK", "worker": self.worker}
        if action == "build":
            # A separate tester owns pass/invalidation events; agents cannot emit them.
            observed: list[dict] = []
            tester = threading.Thread(
                target=lambda: observed.append(
                    self.supervisor.test_artifact(result["revision"])
                )
            )
            tester.start()
            tester.join()
            if not observed:
                raise ValueError("External tester failed")
            return observed[0]
        return result


def handler(application: Application):
    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass

        def do_POST(self):
            try:
                length = int(self.headers.get("Content-Length", "0"))
                if not 0 < length <= 4 * 1024 * 1024:
                    raise ValueError("Invalid frame size")
                body = json.loads(self.rfile.read(length))
                if not isinstance(body, dict):
                    raise ValueError("Expected an object")
                if self.path in {"/admit", "/act"}:
                    if (
                        self.headers.get("Authorization")
                        != "Bearer " + application.token
                    ):
                        raise ValueError("Invalid application token")
                    result = (
                        application.admit(body)
                        if self.path == "/admit"
                        else application.act(body)
                    )
                    self.reply(json.dumps(result).encode(), "application/json")
                else:
                    actor, endpoint = self.path.strip("/").split("/", 1)
                    expected = (
                        "v1/responses" if actor == "codex" else "v1/chat/completions"
                    )
                    if actor not in application.replays or endpoint != expected:
                        raise ValueError("Unknown local provider route")
                    responses = actor == "codex"
                    call = application.replays[actor].propose(body, responses)
                    result = payload(call, responses)
                    stream = responses or body.get("stream", False)
                    self.reply(
                        stream_bytes(result, responses)
                        if stream
                        else json.dumps(result).encode(),
                        "text/event-stream" if stream else "application/json",
                    )
            except Exception as error:
                application.errors.append(str(error))
                self.reply(
                    json.dumps({"error": str(error)}).encode(), "application/json", 400
                )

        def reply(self, data: bytes, content_type: str, status: int = 200):
            self.send_response(status)
            self.send_header("Content-Type", content_type)
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)

    return Handler


def demo(args) -> None:
    if os.name != "posix":
        raise ValueError("This small example currently requires macOS or Linux")
    executables = {
        name: shutil.which(getattr(args, name))
        for name in ("pitot", "codex", args.worker)
    }
    if not all(executables.values()):
        raise ValueError(f"Install the documented prerequisites first: {executables}")
    with tempfile.TemporaryDirectory(
        prefix="pitot-supervisory-", dir=args.temp_dir
    ) as temporary:
        application = Application(Path(temporary), args.worker, executables)
        server = ThreadingHTTPServer(("127.0.0.1", 0), handler(application))
        application.url = f"http://127.0.0.1:{server.server_port}"
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        runtime = None
        try:
            config = application.directory / "config.json"
            write_json(
                config,
                {
                    "require_controller": True,
                    "controllers": {
                        "shell": {
                            "id": "supervisory-control",
                            "command": [
                                sys.executable,
                                str(SCRIPT),
                                "controller",
                                "--connection",
                                str(application.directory / "runtime-connection.json"),
                            ],
                            "deadline_ms": 3000,
                            "on_timeout": "deny",
                            "on_unavailable": "deny",
                        }
                    },
                },
            )
            runtime_log = (application.directory / "runtime.log").open("w")
            runtime = subprocess.Popen(
                [
                    executables["pitot"],
                    "run",
                    "--config",
                    str(config),
                    "--runtime",
                    str(application.runtime),
                ],
                env=application.environment("runtime"),
                cwd=application.project,
                stdout=runtime_log,
                stderr=runtime_log,
                start_new_session=True,
            )
            deadline = time.monotonic() + 10
            while not application.runtime.exists():
                if runtime.poll() is not None or time.monotonic() > deadline:
                    raise ValueError("Pitot runtime did not become ready")
                time.sleep(0.05)
            application.launch("codex")
            events = application.supervisor.events
            denied = [
                event
                for event in events
                if event["event"] == "admission"
                and event["action"] == "release"
                and not event["allowed"]
            ]
            if (
                len(denied) != 2
                or application.supervisor.state != "released"
                or application.errors
            ):
                raise ValueError(
                    f"Unexpected demo outcome: {events}; {application.errors}"
                )
            for event in events:
                print(json.dumps(event))
            print(
                f"PASS: real Codex supervised real {args.worker}; two denials, recovery, release"
            )
        finally:
            write_json(
                application.directory / "observations.json",
                {
                    "events": application.supervisor.events,
                    "errors": application.errors,
                    "feedback": {
                        actor: replay.feedback
                        for actor, replay in application.replays.items()
                    },
                    "tools": {
                        actor: list(replay.tools)
                        for actor, replay in application.replays.items()
                    },
                },
            )
            for process in application.processes + ([runtime] if runtime else []):
                if process.poll() is None:
                    os.killpg(process.pid, signal.SIGTERM)
                    try:
                        process.wait(timeout=5)
                    except subprocess.TimeoutExpired:
                        os.killpg(process.pid, signal.SIGKILL)
                        process.wait()
            server.shutdown()
            server.server_close()
            thread.join()
            if runtime:
                runtime_log.close()
            if args.keep_logs:
                destination = Path(args.keep_logs).resolve()
                shutil.copytree(application.directory, destination)
                print(f"Demo logs: {destination}")


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="mode", required=True)
    run = sub.add_parser("run")
    run.add_argument("--worker", choices=("qwen", "deepseek"), default="qwen")
    for name, default in (
        ("pitot", "pitot"),
        ("codex", "codex"),
        ("qwen", "qwen"),
        ("deepseek", "dsh"),
    ):
        run.add_argument("--" + name, default=default)
    run.add_argument("--temp-dir")
    run.add_argument(
        "--keep-logs", help="Copy owned demo profiles and logs to a new directory"
    )
    control = sub.add_parser("controller")
    control.add_argument("--connection", type=Path, required=True)
    hook = sub.add_parser("qwen-hook")
    hook.add_argument("--pitot", required=True)
    hook.add_argument("--runtime", required=True)
    action = sub.add_parser("act")
    action.add_argument("action", choices=("dispatch", "build", "release"))
    action.add_argument("--connection", type=Path, required=True)
    args = parser.parse_args()
    if args.mode == "controller":
        controller(args.connection)
    elif args.mode == "qwen-hook":
        qwen_hook(args.pitot, args.runtime)
    elif args.mode == "act":
        settings = json.loads(args.connection.read_text())
        print(
            json.dumps(
                post(
                    args.connection,
                    "/act",
                    {"actor": settings["actor"], "action": args.action},
                    240,
                )
            )
        )
    else:
        demo(args)


if __name__ == "__main__":
    main()
