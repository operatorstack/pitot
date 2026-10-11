import sys
import json
from dataclasses import dataclass
from typing import Callable, Optional
from .types import Event, ControlRequested, ControlResponse
from . import wire

ConsumerHandler = Callable[[Event], None]


def _lines(role):
    stream = getattr(sys.stdin, "buffer", sys.stdin)
    limit = 4 * 1024 * 1024
    while True:
        try:
            line = stream.readline(limit + 2)
        except Exception:
            print(f"Pitot {role} error", file=sys.stderr)
            return
        if not line:
            return
        newline = b"\n" if isinstance(line, bytes) else "\n"
        raw = line[:-1] if line.endswith(newline) else line
        length = (
            len(raw)
            if isinstance(raw, bytes)
            else len(raw.encode("utf-8", errors="surrogatepass"))
        )
        if length > limit:
            try:
                while line and not line.endswith(newline):
                    line = stream.readline(limit + 2)
            except Exception:
                print(f"Pitot {role} error", file=sys.stderr)
                return
            print(f"Pitot {role} error", file=sys.stderr)
            continue
        whitespace = b" \t\r\n" if isinstance(line, bytes) else " \t\r\n"
        if line.strip(whitespace):
            yield line


def run_consumer(handler: ConsumerHandler) -> None:
    for line in _lines("Consumer"):
        try:
            data = wire.parse(line)
            wire.event(data)
            event = Event.from_dict(data)
            handler(event)
        except Exception:
            print("Pitot Consumer error", file=sys.stderr)


@dataclass
class Outcome:
    outcome: str
    message: Optional[str] = None


def allow(message: Optional[str] = None) -> Outcome:
    return Outcome(outcome="allow", message=message)


def deny(message: Optional[str] = None) -> Outcome:
    return Outcome(outcome="deny", message=message)


ControllerHandler = Callable[[ControlRequested], Outcome]


def run_controller(controller_id: str, handler: ControllerHandler) -> None:
    for line in _lines("Controller"):
        try:
            data = wire.parse(line)
            wire.request(data)
            req = ControlRequested.from_dict(data)
            result = handler(req)

            response = ControlResponse(
                pitot_version="1",
                type="control.response",
                controller_id=controller_id,
                action_id=req.action_id,
                outcome=result.outcome,
                message=result.message,
            )

            data = response.to_dict()
            wire.response(data)
            encoded = json.dumps(data, allow_nan=False)
            if len(encoded.encode("utf-8")) > 4 * 1024 * 1024:
                raise ValueError("invalid envelope")
            print(encoded)
            sys.stdout.flush()
        except Exception:
            print("Pitot Controller error", file=sys.stderr)
