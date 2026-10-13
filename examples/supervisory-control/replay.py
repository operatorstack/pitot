"""Tiny local model replay: proposes actions, never grants permission."""

from __future__ import annotations

import json
import re
import uuid


class Replay:
    def __init__(self, actor: str, commands: dict[str, str]):
        self.actor = actor
        self.commands = commands
        self.last_call: str | None = None
        self.last_action: str | None = None
        self.tools: dict[str, dict] = {}
        self.feedback: list[dict] = []

    def propose(self, body: dict, responses: bool) -> tuple[str, dict, str] | None:
        for item in body.get("tools", []):
            function = item.get("function", item)
            if isinstance(function, dict) and isinstance(function.get("name"), str):
                self.tools[function["name"]] = function
        action = "dispatch" if self.actor == "codex" else "release"
        if self.last_call:
            items = body.get("input", []) if responses else body.get("messages", [])
            results = [
                item.get("output") if responses else item.get("content")
                for item in items
                if isinstance(item, dict)
                and (
                    item.get("type") == "function_call_output"
                    and item.get("call_id") == self.last_call
                    if responses
                    else item.get("role") == "tool"
                    and item.get("tool_call_id") == self.last_call
                )
            ]
            if not results:
                raise ValueError(
                    "Replay requires the actual result of its last tool call"
                )
            result = json.dumps(results[-1])
            self.feedback.append(
                {
                    "call_id": self.last_call,
                    "action": self.last_action,
                    "result": results[-1],
                }
            )
            if self.actor == "codex":
                if "DISPATCH_OK" in result:
                    return None
                session = re.search(r"(?:session ID|session_id)[\s\":]+(\d+)", result)
                if session and "write_stdin" in self.tools:
                    return self.issue(
                        "write_stdin",
                        {
                            "session_id": int(session[1]),
                            "chars": "",
                            "yield_time_ms": 30000,
                        },
                        "dispatch",
                    )
                raise ValueError("Codex dispatch did not complete successfully")
            if self.last_action == "release":
                if "RELEASE_OK" in result:
                    return None
                if "SUPERVISOR_DENY" not in result:
                    raise ValueError("Expected native release denial feedback")
                action = "build"
            elif self.last_action == "build":
                if "BUILD_OK" not in result:
                    raise ValueError("Build did not complete; do not advance replay")
                action = "release"
        tool = next(
            (
                name
                for name in (
                    "shell_command",
                    "exec_command",
                    "shell",
                    "run_shell_command",
                    "bash",
                    "pwsh",
                )
                if name in self.tools
            ),
            None,
        )
        if not tool:
            raise ValueError(
                f"No supported native shell tool advertised: {list(self.tools)}"
            )
        command = self.commands[action]
        arguments = {"cmd" if tool == "exec_command" else "command": command}
        if tool == "exec_command":
            arguments["yield_time_ms"] = 30000
        if tool == "shell":
            arguments["command"] = ["/bin/sh", "-c", command]
        if tool == "shell_command":
            arguments["timeout_ms"] = 180000
        if tool == "run_shell_command":
            arguments["is_background"] = False
        parameters = self.tools[tool].get("parameters", {})
        if "description" in parameters.get("required", []):
            arguments["description"] = f"Propose the delivery {action} action"
        return self.issue(tool, arguments, action)

    def issue(self, tool: str, arguments: dict, action: str) -> tuple[str, dict, str]:
        self.last_call = "call_" + uuid.uuid4().hex
        self.last_action = action
        return tool, arguments, self.last_call


def payload(call: tuple[str, dict, str] | None, responses: bool) -> dict:
    identity = uuid.uuid4().hex
    if not responses:
        message = {
            "role": "assistant",
            "content": "Delivery complete." if call is None else None,
        }
        if call:
            tool, arguments, call_id = call
            message["tool_calls"] = [
                {
                    "id": call_id,
                    "type": "function",
                    "function": {"name": tool, "arguments": json.dumps(arguments)},
                }
            ]
        return {
            "id": "chatcmpl_" + identity,
            "object": "chat.completion",
            "created": 1,
            "model": "pitot-control",
            "choices": [
                {
                    "index": 0,
                    "message": message,
                    "finish_reason": "tool_calls" if call else "stop",
                }
            ],
            "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
        }
    if call:
        tool, arguments, call_id = call
        item = {
            "id": "fc_" + identity,
            "type": "function_call",
            "call_id": call_id,
            "name": tool,
            "arguments": json.dumps(arguments),
            "status": "completed",
        }
    else:
        item = {
            "id": "msg_" + identity,
            "type": "message",
            "role": "assistant",
            "status": "completed",
            "content": [
                {"type": "output_text", "text": "Delivery complete.", "annotations": []}
            ],
        }
    return {
        "id": "resp_" + identity,
        "object": "response",
        "created_at": 1,
        "status": "completed",
        "model": "pitot-control",
        "output": [item],
        "parallel_tool_calls": False,
        "usage": {"input_tokens": 1, "output_tokens": 1, "total_tokens": 2},
    }


def stream_bytes(response: dict, responses: bool) -> bytes:
    """The two native provider dialects; all data comes from the local replay."""
    if not responses:
        choice = response["choices"][0]
        delta = dict(choice["message"])
        for index, call in enumerate(delta.get("tool_calls", [])):
            call["index"] = index
        base = {**response, "object": "chat.completion.chunk"}
        events = [
            (
                "",
                {
                    **base,
                    "choices": [{"index": 0, "delta": delta, "finish_reason": None}],
                },
            ),
            (
                "",
                {
                    **base,
                    "choices": [
                        {
                            "index": 0,
                            "delta": {},
                            "finish_reason": choice["finish_reason"],
                        }
                    ],
                },
            ),
        ]
    else:
        item = response["output"][0]
        events = [
            (
                "response.created",
                {"response": {**response, "status": "in_progress", "output": []}},
            )
        ]
        start = {**item, "status": "in_progress"}
        if item["type"] == "function_call":
            start["arguments"] = ""
            events.extend(
                [
                    ("response.output_item.added", {"output_index": 0, "item": start}),
                    (
                        "response.function_call_arguments.delta",
                        {
                            "item_id": item["id"],
                            "output_index": 0,
                            "delta": item["arguments"],
                        },
                    ),
                    (
                        "response.function_call_arguments.done",
                        {
                            "item_id": item["id"],
                            "output_index": 0,
                            "arguments": item["arguments"],
                        },
                    ),
                ]
            )
        else:
            start["content"] = []
            part = item["content"][0]
            fields = {"item_id": item["id"], "output_index": 0, "content_index": 0}
            events.extend(
                [
                    ("response.output_item.added", {"output_index": 0, "item": start}),
                    (
                        "response.content_part.added",
                        {**fields, "part": {**part, "text": ""}},
                    ),
                    ("response.output_text.delta", {**fields, "delta": part["text"]}),
                    ("response.output_text.done", {**fields, "text": part["text"]}),
                    ("response.content_part.done", {**fields, "part": part}),
                ]
            )
        events.extend(
            [
                ("response.output_item.done", {"output_index": 0, "item": item}),
                ("response.completed", {"response": response}),
            ]
        )
        events = [
            (name, {"type": name, "sequence_number": index, **data})
            for index, (name, data) in enumerate(events)
        ]
    return b"".join(
        (
            (f"event: {name}\n" if name else "") + f"data: {json.dumps(data)}\n\n"
        ).encode()
        for name, data in events
    ) + (b"" if responses else b"data: [DONE]\n\n")
