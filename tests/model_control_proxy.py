#!/usr/bin/env python3
"""Deterministic model server for real prompt-to-hook agent E2E sessions."""

from __future__ import annotations

import argparse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import threading
import time
import uuid


def contains(value: object, needle: str) -> bool:
    return needle in json.dumps(value, separators=(",", ":"), ensure_ascii=True)


def tool_result_contains(protocol: str, body: dict[str, object], needle: str) -> bool:
    """Search only native tool-result fields, never echoed tool-call arguments."""
    if protocol == "gemini_generate_content":
        values = []
        for content in body.get("contents", []):
            if not isinstance(content, dict):
                continue
            for part in content.get("parts", []):
                if isinstance(part, dict) and isinstance(part.get("functionResponse"), dict):
                    values.append(part["functionResponse"].get("response"))
        return any(contains(value, needle) for value in values)
    if protocol in {"anthropic_messages", "openai_chat"}:
        values = []
        for message in body.get("messages", []):
            if not isinstance(message, dict):
                continue
            if protocol == "openai_chat" and message.get("role") == "tool":
                values.append(message.get("content"))
            for item in message.get("content", []) if isinstance(message.get("content"), list) else []:
                if isinstance(item, dict) and item.get("type") == "tool_result":
                    values.append(item.get("content"))
        return any(contains(value, needle) for value in values)
    if protocol == "openai_responses":
        values = [
            item.get("output")
            for item in body.get("input", [])
            if isinstance(item, dict) and item.get("type") == "function_call_output"
        ]
        return any(contains(value, needle) for value in values)
    return False


class UnknownProtocol(ValueError):
    pass


def classify_request(path: str, body: object) -> str:
    """Classify only from the real CLI's observed request, never a manifest hint."""
    clean_path = path.split("?", 1)[0]
    if not isinstance(body, dict):
        raise UnknownProtocol("request body is not a JSON object")
    candidates: list[str] = []
    if clean_path.endswith("/messages") and isinstance(body.get("messages"), list):
        candidates.append("anthropic_messages")
    if clean_path.endswith("/responses") and "input" in body:
        candidates.append("openai_responses")
    if ("generateContent" in clean_path or "streamGenerateContent" in clean_path) and isinstance(body.get("contents"), list):
        candidates.append("gemini_generate_content")
    if clean_path.endswith("/chat/completions") and isinstance(body.get("messages"), list):
        candidates.append("openai_chat")
    if len(candidates) != 1:
        raise UnknownProtocol(f"request matched {len(candidates)} supported dialects")
    return candidates[0]


def request_shape(body: dict[str, object]) -> dict[str, object]:
    """Return a content-redacted structural fingerprint for review fixtures."""
    return {
        "top_level_keys": sorted(body),
        "has_tools": bool(body.get("tools")),
        "stream": bool(body.get("stream")),
        "tool_names": tool_names(body),
    }


def protobuf_varint(value: int) -> bytes:
    encoded = bytearray()
    while value > 0x7F:
        encoded.append((value & 0x7F) | 0x80)
        value >>= 7
    encoded.append(value)
    return bytes(encoded)


def protobuf_bytes(field: int, value: bytes) -> bytes:
    return protobuf_varint((field << 3) | 2) + protobuf_varint(len(value)) + value


def cursor_model_details() -> bytes:
    return b"".join(protobuf_bytes(field, b"pitot-control") for field in (1, 3, 4, 5))


def cursor_auxiliary_response(path: str) -> bytes:
    model = cursor_model_details()
    if path.endswith("/GetUsableModels"):
        return protobuf_bytes(1, model)
    if path.endswith("/GetDefaultModelForCli"):
        return protobuf_bytes(1, model)
    return b""


def tool_name(body: dict[str, object]) -> str:
    names = tool_names(body)
    preferred = ("Bash", "bash", "shell", "run_shell_command", "execute_command", "run_commands")
    return next((name for name in preferred if name in names), names[0] if names else "bash")


def tool_names(body: dict[str, object]) -> list[str]:
    names: list[str] = []
    for item in body.get("tools", []):
        if isinstance(item, dict):
            function = item.get("function")
            name = function.get("name") if isinstance(function, dict) else item.get("name")
            if isinstance(name, str): names.append(name)
            declarations = item.get("functionDeclarations", [])
            if isinstance(declarations, list):
                names.extend(value["name"] for value in declarations if isinstance(value, dict) and isinstance(value.get("name"), str))
    return names


def tool_arguments(tool: str, command: str) -> dict[str, object]:
    if tool == "exec_command": return {"cmd": command}
    if tool == "run_commands": return {"commands": [command]}
    if tool == "run_shell_command": return {"command": command, "is_background": False}
    return {"command": command}


def function_response_shapes(value: object) -> list[dict[str, object]]:
    found: list[dict[str, object]] = []
    if isinstance(value, dict):
        response = value.get("functionResponse")
        if isinstance(response, dict):
            payload = response.get("response")
            found.append({"name": response.get("name"), "response_keys": sorted(payload) if isinstance(payload, dict) else type(payload).__name__})
        for child in value.values(): found.extend(function_response_shapes(child))
    elif isinstance(value, list):
        for child in value: found.extend(function_response_shapes(child))
    return found


def content_shapes(body: dict[str, object]) -> list[dict[str, object]]:
    shapes: list[dict[str, object]] = []
    for content in body.get("contents", []):
        if not isinstance(content, dict): continue
        parts = content.get("parts", [])
        shapes.append({"role": content.get("role"), "parts": [sorted(part) for part in parts if isinstance(part, dict)]})
    return shapes


def message_shapes(body: dict[str, object]) -> list[dict[str, object]]:
    shapes: list[dict[str, object]] = []
    for message in body.get("messages", []):
        if not isinstance(message, dict): continue
        content = message.get("content")
        serialized = json.dumps(content).lower()
        item_shapes = []
        if isinstance(content, list):
            for item in content:
                if isinstance(item, dict):
                    item_shapes.append({"keys": sorted(item), "types": {key: type(value).__name__ for key, value in item.items()}})
                else:
                    item_shapes.append({"type": type(item).__name__})
        markers = (
            "hook", "permission", "denied", "error", "invalid", "background",
            "cancel", "reject", "block", "fail", "not found", "exit code",
            "timed out", "aborted",
        )
        shapes.append({"role": message.get("role"), "keys": sorted(message), "content_type": type(content).__name__, "content_length": len(content) if isinstance(content, (str, list)) else None, "content_items": item_shapes, "markers": [marker for marker in markers if marker in serialized]})
    return shapes


def tool_schema_shapes(body: dict[str, object]) -> list[dict[str, object]]:
    shapes: list[dict[str, object]] = []
    for item in body.get("tools", []):
        if not isinstance(item, dict): continue
        function = item.get("function") if isinstance(item.get("function"), dict) else item
        params = function.get("parameters", {})
        properties = params.get("properties", {}) if isinstance(params, dict) else {}
        property_shapes = {}
        if isinstance(properties, dict):
            for key, value in properties.items():
                if isinstance(value, dict):
                    items = value.get("items", {})
                    property_shapes[key] = {"type": value.get("type"), "item_type": items.get("type") if isinstance(items, dict) else None, "item_properties": sorted(items.get("properties", {})) if isinstance(items, dict) and isinstance(items.get("properties"), dict) else []}
        shapes.append({"name": function.get("name"), "properties": property_shapes})
    return shapes


class State:
    def __init__(self, args: argparse.Namespace) -> None:
        self.args = args
        self.lock = threading.Lock()
        self.receipt = {
            "schema_version": 1,
            "agent": args.agent,
            "protocol": None,
            "nonce": args.nonce,
            "initial_prompt_observed": False,
            "tool_call_response_emitted": False,
            "tool_result_observed": False,
            "allow_tool_call_response_emitted": False,
            "allow_tool_result_observed": False,
            "deny_tool_call_response_emitted": False,
            "denied_result_observed": False,
            "final_response_emitted": False,
            "selected_tool": None,
            "endpoint_observed": None,
            "unexpected_request": None,
            "auxiliary_requests": 0,
        }

    def save(self) -> None:
        target = Path(self.args.receipt)
        target.parent.mkdir(parents=True, exist_ok=True)
        temporary = target.with_suffix(".tmp")
        temporary.write_text(json.dumps(self.receipt, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        temporary.replace(target)


def anthropic(tool: str, command: str, final: bool, nonce: str = "", phase: str = "allow") -> dict[str, object]:
    content = [{"type": "text", "text": f"PITOT_E2E_COMPLETE {nonce}"}] if final else [
        {"type": "tool_use", "id": f"pitot_tool_{phase}", "name": tool, "input": tool_arguments(tool, command)}
    ]
    return {"id": "msg_pitot", "type": "message", "role": "assistant", "model": "pitot-control", "content": content, "stop_reason": "end_turn" if final else "tool_use", "usage": {"input_tokens": 1, "output_tokens": 1}}


def chat(tool: str, command: str, final: bool, nonce: str = "", phase: str = "allow") -> dict[str, object]:
    message: dict[str, object] = {"role": "assistant", "content": f"PITOT_E2E_COMPLETE {nonce}" if final else None}
    finish = "stop" if final else "tool_calls"
    if not final:
        message["tool_calls"] = [{"id": f"pitot_tool_{phase}", "type": "function", "function": {"name": tool, "arguments": json.dumps(tool_arguments(tool, command))}}]
    return {"id": "chatcmpl-pitot", "object": "chat.completion", "created": 1, "model": "pitot-control", "choices": [{"index": 0, "message": message, "finish_reason": finish}], "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}}


def responses(tool: str, command: str, final: bool, nonce: str = "", phase: str = "allow") -> dict[str, object]:
    output = [{"id": "msg_pitot", "type": "message", "role": "assistant", "status": "completed", "content": [{"type": "output_text", "text": f"PITOT_E2E_COMPLETE {nonce}", "annotations": []}]}] if final else [
        {"id": f"fc_pitot_{phase}", "type": "function_call", "call_id": f"pitot_tool_{phase}", "name": tool, "arguments": json.dumps(tool_arguments(tool, command)), "status": "completed"}
    ]
    return {"id": "resp_pitot", "object": "response", "created_at": 1, "status": "completed", "model": "pitot-control", "output": output, "parallel_tool_calls": False, "usage": {"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}


def gemini(tool: str, command: str, final: bool, nonce: str = "", phase: str = "allow") -> dict[str, object]:
    part = {"text": f"PITOT_E2E_COMPLETE {nonce}"} if final else {"functionCall": {"name": tool, "args": tool_arguments(tool, command)}, "thoughtSignature": "cGl0b3Q="}
    return {"candidates": [{"content": {"role": "model", "parts": [part]}, "finishReason": "STOP"}], "usageMetadata": {"promptTokenCount": 1, "candidatesTokenCount": 1, "totalTokenCount": 2}}


class Handler(BaseHTTPRequestHandler):
    server_version = "PitotModelControl/1"

    def log_message(self, format: str, *args: object) -> None:
        return

    def do_GET(self) -> None:
        state: State = self.server.state  # type: ignore[attr-defined]
        if self.path in {"/health", "/v1/models"}:
            self.reply({"object": "list", "data": [{"id": "pitot-control", "object": "model"}]})
        else:
            self.send_error(404)

    def do_POST(self) -> None:
        state: State = self.server.state  # type: ignore[attr-defined]
        try:
            length = int(self.headers.get("content-length", "0"))
            raw_body = self.rfile.read(length)
            body = json.loads(raw_body or b"{}")
        except (ValueError, json.JSONDecodeError):
            self.send_error(400, "invalid JSON")
            return
        nonce = state.args.nonce
        try:
            protocol = classify_request(self.path, body)
        except UnknownProtocol as error:
            with state.lock:
                state.receipt["unexpected_request"] = {
                    "path": self.path.split("?", 1)[0],
                    "top_level_keys": sorted(body) if isinstance(body, dict) else [],
                    "classification_error": str(error),
                }
                state.save()
            self.send_error(422, "unrecognized model request structure")
            return
        with state.lock:
            if state.receipt["protocol"] not in {None, protocol}:
                self.send_error(409, "request dialect changed during session")
                return
            state.receipt["protocol"] = protocol
            if not body.get("tools") and state.receipt["auxiliary_requests"] == 0:
                state.receipt["auxiliary_requests"] = 1
                state.receipt["auxiliary_request"] = {
                    "path": self.path.split("?", 1)[0],
                    "top_level_keys": sorted(body),
                    "nonce_present": contains(body, nonce),
                }
                state.save()
                if protocol == "anthropic_messages":
                    payload = anthropic("", "", True)
                    payload["content"][0]["text"] = '{"title":"Pitot E2E"}'
                elif protocol == "openai_responses": payload = responses("", "", True)
                elif protocol == "gemini_generate_content": payload = gemini("", "", True)
                else: payload = chat("", "", True)
                if isinstance(body.get("model"), str): payload["model"] = body["model"]
                self.reply(payload, stream=bool(body.get("stream")), protocol=protocol)
                return
            allow_result = tool_result_contains(protocol, body, f"PITOT_CANARY_RESULT PITOT_ALLOW {nonce}")
            denied_result = tool_result_contains(protocol, body, f"PITOT_CONTROLLER_DENY {nonce}")
            phase = "allow"
            final = False
            if not state.receipt["initial_prompt_observed"]:
                if not contains(body, nonce):
                    self.send_error(409, "initial request omitted session nonce")
                    return
                state.receipt["initial_prompt_observed"] = True
                state.receipt["endpoint_observed"] = {
                    "transport": "http1",
                    "method": "POST",
                    "path": self.path.split("?", 1)[0],
                    "media_type": self.headers.get("content-type", "").split(";", 1)[0].strip().lower(),
                    "framing": "sse" if bool(body.get("stream")) or "streamGenerateContent" in self.path or protocol == "openai_responses" else "json",
                    "request_shape": request_shape(body),
                }
            elif allow_result and not state.receipt["allow_tool_result_observed"]:
                state.receipt["allow_tool_result_observed"] = True
                state.receipt["tool_result_observed"] = True
                phase = "deny"
            elif denied_result and state.receipt["deny_tool_call_response_emitted"]:
                state.receipt["denied_result_observed"] = True
                phase = "final"
                final = True
            else:
                state.receipt["unexpected_request"] = {
                    "path": self.path.split("?", 1)[0],
                    "top_level_keys": sorted(body),
                    "nonce_present": contains(body, nonce),
                    "allow_result_present": allow_result,
                    "denied_result_present": denied_result,
                    "function_responses": function_response_shapes(body),
                    "contents": content_shapes(body),
                    "messages": message_shapes(body),
                }
                state.save()
                self.send_error(409, "agent request did not advance the supervised allow/deny trajectory")
                return
            tool = tool_name(body)
            state.receipt["selected_tool"] = tool
            state.receipt["advertised_tools"] = tool_names(body)
            state.receipt["tool_structures"] = [sorted(item) if isinstance(item, dict) else type(item).__name__ for item in body.get("tools", [])]
            state.receipt["tool_schemas"] = tool_schema_shapes(body)
            command = f"{state.args.canary_command} PITOT_{phase.upper()} {nonce}"
            if state.args.response_fault == "text" and not final:
                if protocol == "anthropic_messages": payload = anthropic(tool, command, True, nonce, phase)
                elif protocol == "openai_responses": payload = responses(tool, command, True, nonce, phase)
                elif protocol == "gemini_generate_content": payload = gemini(tool, command, True, nonce, phase)
                else: payload = chat(tool, command, True, nonce, phase)
                state.receipt["fault_response_emitted"] = "text"
            elif protocol == "anthropic_messages": payload = anthropic(tool, command, final, nonce, phase)
            elif protocol == "openai_responses": payload = responses(tool, command, final, nonce, phase)
            elif protocol == "gemini_generate_content": payload = gemini(tool, command, final, nonce, phase)
            elif protocol == "openai_chat": payload = chat(tool, command, final, nonce, phase)
            else:
                self.send_error(422, "unsupported protocol")
                return
            if isinstance(body.get("model"), str):
                payload["model"] = body["model"]
            if state.args.response_fault == "text" and not final:
                pass
            elif final:
                state.receipt["final_response_emitted"] = True
            elif phase == "deny":
                state.receipt["deny_tool_call_response_emitted"] = True
            else:
                state.receipt["tool_call_response_emitted"] = True
                state.receipt["allow_tool_call_response_emitted"] = True
            state.save()
        stream = bool(body.get("stream")) or "streamGenerateContent" in self.path or protocol == "openai_responses"
        self.reply(payload, stream=stream, protocol=protocol)

    def reply(self, payload: dict[str, object], *, stream: bool = False, protocol: str = "") -> None:
        if stream:
            if protocol == "openai_responses":
                item = payload["output"][0]
                events = [("response.created", {"type": "response.created", "sequence_number": 0, "response": {**payload, "status": "in_progress", "output": []}})]
                if item["type"] == "function_call":
                    start = {**item, "arguments": "", "status": "in_progress"}
                    events.extend([
                        ("response.output_item.added", {"type": "response.output_item.added", "sequence_number": 1, "output_index": 0, "item": start}),
                        ("response.function_call_arguments.delta", {"type": "response.function_call_arguments.delta", "sequence_number": 2, "item_id": item["id"], "output_index": 0, "delta": item["arguments"]}),
                        ("response.function_call_arguments.done", {"type": "response.function_call_arguments.done", "sequence_number": 3, "item_id": item["id"], "output_index": 0, "arguments": item["arguments"]}),
                        ("response.output_item.done", {"type": "response.output_item.done", "sequence_number": 4, "output_index": 0, "item": item}),
                    ])
                else:
                    text_value = item["content"][0]["text"]
                    start = {**item, "status": "in_progress", "content": []}
                    part = {"type": "output_text", "text": "", "annotations": []}
                    events.extend([
                        ("response.output_item.added", {"type": "response.output_item.added", "sequence_number": 1, "output_index": 0, "item": start}),
                        ("response.content_part.added", {"type": "response.content_part.added", "sequence_number": 2, "item_id": item["id"], "output_index": 0, "content_index": 0, "part": part}),
                        ("response.output_text.delta", {"type": "response.output_text.delta", "sequence_number": 3, "item_id": item["id"], "output_index": 0, "content_index": 0, "delta": text_value}),
                        ("response.output_text.done", {"type": "response.output_text.done", "sequence_number": 4, "item_id": item["id"], "output_index": 0, "content_index": 0, "text": text_value}),
                        ("response.content_part.done", {"type": "response.content_part.done", "sequence_number": 5, "item_id": item["id"], "output_index": 0, "content_index": 0, "part": item["content"][0]}),
                        ("response.output_item.done", {"type": "response.output_item.done", "sequence_number": 6, "output_index": 0, "item": item}),
                    ])
                events.append(("response.completed", {"type": "response.completed", "sequence_number": len(events) + 1, "response": payload}))
            elif protocol == "anthropic_messages":
                block = payload["content"][0]
                if block["type"] == "tool_use":
                    start_block = {"type": "tool_use", "id": block["id"], "name": block["name"], "input": {}}
                    deltas = [("content_block_delta", {"type": "content_block_delta", "index": 0, "delta": {"type": "input_json_delta", "partial_json": json.dumps(block["input"], separators=(',', ':'))}})]
                else:
                    start_block = {"type": "text", "text": ""}
                    deltas = [("content_block_delta", {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": block["text"]}})]
                events = [
                    ("message_start", {"type": "message_start", "message": {**payload, "content": [], "stop_reason": None, "stop_sequence": None}}),
                    ("content_block_start", {"type": "content_block_start", "index": 0, "content_block": start_block}),
                    *deltas,
                    ("content_block_stop", {"type": "content_block_stop", "index": 0}),
                    ("message_delta", {"type": "message_delta", "delta": {"stop_reason": payload["stop_reason"], "stop_sequence": None}, "usage": {"output_tokens": 1}}),
                    ("message_stop", {"type": "message_stop"}),
                ]
            elif protocol == "openai_chat":
                message = payload["choices"][0]["message"]
                if message.get("tool_calls"):
                    call = message["tool_calls"][0]
                    delta = {"role": "assistant", "content": None, "tool_calls": [{"index": 0, **call}]}
                else:
                    delta = {"role": "assistant", "content": message.get("content")}
                base = {"id": payload["id"], "object": "chat.completion.chunk", "created": payload["created"], "model": payload["model"]}
                events = [
                    ("", {**base, "choices": [{"index": 0, "delta": delta, "finish_reason": None}]}),
                    ("", {**base, "choices": [{"index": 0, "delta": {}, "finish_reason": payload["choices"][0]["finish_reason"]}]}),
                ]
            else:
                events = [("", payload)]
            encoded = b"".join(
                ((f"event: {name}\n" if name else "") + f"data: {json.dumps(item, separators=(',', ':'))}\n\n").encode()
                for name, item in events
            ) + (b"data: [DONE]\n\n" if protocol == "openai_chat" else b"")
            self.send_response(200)
            self.send_header("content-type", "text/event-stream")
            self.send_header("content-length", str(len(encoded)))
            self.end_headers()
            self.wfile.write(encoded)
            return
        encoded = json.dumps(payload, separators=(",", ":")).encode()
        self.send_response(200)
        self.send_header("content-type", "application/json")
        self.send_header("content-length", str(len(encoded)))
        self.end_headers()
        self.wfile.write(encoded)

    def reply_bytes(self, payload: bytes, content_type: str) -> None:
        self.send_response(200)
        self.send_header("content-type", content_type)
        self.send_header("content-length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--agent", required=True)
    parser.add_argument("--nonce", required=True)
    parser.add_argument("--receipt", required=True)
    parser.add_argument("--ready-file", required=True)
    parser.add_argument("--canary-command", default="pitot-e2e-canary")
    parser.add_argument("--response-fault", choices=("none", "text"), default="none")
    args = parser.parse_args()
    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    server.state = State(args)  # type: ignore[attr-defined]
    Path(args.ready_file).write_text(f"http://127.0.0.1:{server.server_port}\n", encoding="utf-8")
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
