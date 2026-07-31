#!/usr/bin/env python3
"""Deterministic Connect/protobuf model endpoint for released Devin ACP E2E."""

from __future__ import annotations

import argparse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import threading
import time


def varint(value: int) -> bytes:
    output = bytearray()
    while value > 127:
        output.append((value & 127) | 128)
        value >>= 7
    output.append(value)
    return bytes(output)


def field_bytes(field: int, value: bytes) -> bytes:
    return varint((field << 3) | 2) + varint(len(value)) + value


def field_string(field: int, value: str) -> bytes:
    return field_bytes(field, value.encode())


def field_enum(field: int, value: int) -> bytes:
    return varint(field << 3) + varint(value)


def envelope(payload: bytes, flags: int = 0) -> bytes:
    return bytes([flags]) + len(payload).to_bytes(4, "big") + payload


def tool_response(call_id: str, command: str) -> bytes:
    call = field_string(1, call_id) + field_string(2, "exec") + field_string(
        3, json.dumps({"command": command}, separators=(",", ":"))
    )
    message = field_string(1, f"msg-{call_id}") + field_enum(5, 10) + field_bytes(6, call)
    return envelope(message) + envelope(b"{}", 2)


def text_response(text: str) -> bytes:
    message = field_string(1, "pitot-final") + field_string(3, text) + field_enum(5, 2)
    return envelope(message) + envelope(b"{}", 2)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--nonce", required=True)
    parser.add_argument("--receipt", type=Path, required=True)
    parser.add_argument("--ready-file", type=Path, required=True)
    parser.add_argument("--canary-command", required=True)
    parser.add_argument("--response-fault", choices=("none", "text"), default="none")
    parser.add_argument("--request-log", type=Path, default=None)
    args = parser.parse_args()
    log_lock = threading.Lock()

    def rlog(line: str) -> None:
        # Connection/request journal for CI triage: written to a file (never
        # a pipe nobody drains) and printed by the driver after each phase.
        if args.request_log is None:
            return
        with log_lock, open(args.request_log, "a", encoding="utf-8") as fh:
            fh.write(f"{time.time():.6f} {line}\n")
            fh.flush()
    allow_id = f"pitot_tool_allow_{args.nonce}"
    deny_id = f"pitot_tool_deny_{args.nonce}"
    receipt: dict[str, object] = {
        "schema_version": 1,
        "agent": "devin",
        "protocol": "devin_connect_proto",
        "nonce": args.nonce,
        "initial_prompt_observed": False,
        "tool_call_response_emitted": False,
        "tool_result_observed": False,
        "allow_tool_call_response_emitted": False,
        "allow_tool_result_observed": False,
        "deny_tool_call_response_emitted": False,
        "denied_result_observed": False,
        "final_response_emitted": False,
        "endpoint_observed": None,
        "auxiliary_requests": 0,
    }

    receipt_lock = threading.Lock()

    def save() -> None:
        # Handlers run on concurrent threads (one per connection). The receipt
        # mutation and the tmp-file replace must be atomic as a unit: a shared
        # tmp name raced by two handlers throws, killing the connection —
        # which a bursting Devin client observes as "error sending request".
        with receipt_lock:
            args.receipt.parent.mkdir(parents=True, exist_ok=True)
            temporary = args.receipt.with_suffix(f".{threading.get_ident()}.tmp")
            temporary.write_text(json.dumps(receipt, indent=2, sort_keys=True) + "\n", encoding="utf-8")
            temporary.replace(args.receipt)

    class Handler(BaseHTTPRequestHandler):
        # Devin's HTTP client pools connections. Under the default HTTP/1.0
        # the server closes the socket after every response, and a request
        # written to the stale pooled connection can black-hole until the
        # CLI's 10s team-settings deadline expires (observed on Windows).
        # HTTP/1.1 keep-alive keeps pooled connections valid; every response
        # below must therefore carry an exact content-length.
        protocol_version = "HTTP/1.1"

        def log_message(self, *_: object) -> None:
            return

        def setup(self) -> None:
            super().setup()
            rlog(f"CONN open client=:{self.client_address[1]}")

        def finish(self) -> None:
            rlog(f"CONN close client=:{self.client_address[1]}")
            super().finish()

        def do_GET(self) -> None:
            rlog(f"REQ  client=:{self.client_address[1]} GET {self.path}")
            self.send_response(404)
            self.send_header("content-length", "0")
            self.end_headers()
            rlog(f"RES  client=:{self.client_address[1]} 404 GET {self.path}")

        def _read_body(self) -> bytes:
            # Drain the request body exactly, including chunked framing: an
            # unconsumed body on a kept-alive connection desyncs every later
            # request the client sends on it.
            if "chunked" in self.headers.get("transfer-encoding", "").lower():
                chunks = []
                while True:
                    size_line = self.rfile.readline(65537).split(b";", 1)[0].strip()
                    size = int(size_line or b"0", 16)
                    if size == 0:
                        while self.rfile.readline(65537) not in (b"\r\n", b"\n", b""):
                            pass  # trailers
                        break
                    chunks.append(self.rfile.read(size))
                    self.rfile.readline(65537)  # CRLF after each chunk
                return b"".join(chunks)
            return self.rfile.read(int(self.headers.get("content-length", "0")))

        def do_POST(self) -> None:
            started = time.monotonic()
            rlog(
                f"REQ  client=:{self.client_address[1]} POST {self.path} "
                f"cl={self.headers.get('content-length', '-')} "
                f"te={self.headers.get('transfer-encoding', '-')}"
            )
            raw = self._read_body()
            path = self.path.split("?", 1)[0]
            content_type = self.headers.get("content-type", "").split(";", 1)[0].strip().lower()
            if not path.endswith("/GetChatMessage"):
                receipt["auxiliary_requests"] = int(receipt["auxiliary_requests"]) + 1
                save()
                self.send_response(200)
                self.send_header("content-type", "application/proto")
                self.send_header("content-length", "0")
                self.end_headers()
                rlog(
                    f"RES  client=:{self.client_address[1]} 200 {path} "
                    f"aux {int((time.monotonic() - started) * 1000)}ms"
                )
                return

            lowered = raw.lower()
            is_title = b"session title generator" in lowered
            allow_result = f"PITOT_CANARY_RESULT PITOT_ALLOW {args.nonce}".encode() in raw
            denied_result = deny_id.encode() in raw and any(
                marker in lowered for marker in (b"reject", b"denied", b"permission")
            )
            if is_title:
                payload = text_response("Pitot E2E")
                receipt["auxiliary_requests"] = int(receipt["auxiliary_requests"]) + 1
            elif not receipt["initial_prompt_observed"] and args.nonce.encode() in raw:
                receipt["initial_prompt_observed"] = True
                receipt["endpoint_observed"] = {
                    "transport": "http1",
                    "method": "POST",
                    "path": path,
                    "media_type": content_type,
                    "framing": "connect_envelope",
                    "request_shape": {
                        "service": "exa.api_server_pb.ApiServerService",
                        "method": "GetChatMessage",
                        "stream": "server",
                        "message": "GetChatMessageRequest",
                    },
                }
                if args.response_fault == "text":
                    receipt["fault_response_emitted"] = "text"
                    payload = text_response(f"PITOT_E2E_COMPLETE {args.nonce}")
                else:
                    payload = tool_response(
                        allow_id, f"{args.canary_command} PITOT_ALLOW {args.nonce}"
                    )
                    receipt["tool_call_response_emitted"] = True
                    receipt["allow_tool_call_response_emitted"] = True
            elif allow_result and not receipt["allow_tool_result_observed"]:
                receipt["tool_result_observed"] = True
                receipt["allow_tool_result_observed"] = True
                receipt["deny_tool_call_response_emitted"] = True
                payload = tool_response(
                    deny_id, f"{args.canary_command} PITOT_DENY {args.nonce}"
                )
            elif denied_result and receipt["deny_tool_call_response_emitted"]:
                receipt["denied_result_observed"] = True
                receipt["final_response_emitted"] = True
                payload = text_response(f"PITOT_E2E_COMPLETE {args.nonce}")
            else:
                receipt["unexpected_request"] = {
                    "path": path,
                    "bytes": len(raw),
                    "allow_result": allow_result,
                    "deny_call_id": deny_id.encode() in raw,
                    "rejection_marker": any(
                        marker in lowered for marker in (b"reject", b"denied", b"permission")
                    ),
                }
                save()
                self.send_error(409, "request did not advance Devin control trajectory")
                rlog(f"RES  client=:{self.client_address[1]} 409 {path}")
                return
            save()
            self.send_response(200)
            self.send_header("content-type", "application/connect+proto")
            self.send_header("content-length", str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)
            rlog(
                f"RES  client=:{self.client_address[1]} 200 {path} "
                f"chat {int((time.monotonic() - started) * 1000)}ms"
            )

    class BurstTolerantServer(ThreadingHTTPServer):
        # Devin bursts many concurrent connections at startup and session
        # creation. socketserver's default listen backlog of 5 overflows on
        # Windows, where an overflowed SYN is silently dropped and the
        # client's retransmit schedule outlives the CLI's 10s team-settings
        # deadline (or is RST -> instant ConnectionFailed). A deep backlog
        # makes the local fetch path deterministic.
        request_queue_size = 128
        daemon_threads = True

    server = BurstTolerantServer(("127.0.0.1", 0), Handler)
    args.ready_file.write_text(f"http://127.0.0.1:{server.server_port}\n", encoding="utf-8")
    save()
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        return 0
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
