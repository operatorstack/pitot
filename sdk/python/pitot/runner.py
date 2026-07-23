import sys
import json
from dataclasses import dataclass
from typing import Callable, Optional, Union
from .types import Event, ControlRequested, ControlResponse

ConsumerHandler = Callable[[Event], None]

def run_consumer(handler: ConsumerHandler) -> None:
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            data = json.loads(line)
            event = Event.from_dict(data)
            handler(event)
        except Exception as e:
            print(f"Pitot Consumer error: {e}", file=sys.stderr)

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
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            data = json.loads(line)
            req = ControlRequested.from_dict(data)
            result = handler(req)
            
            response = ControlResponse(
                pitot_version="1",
                type="control.response",
                controller_id=controller_id,
                action_id=req.action_id,
                outcome=result.outcome,
                message=result.message
            )
            
            print(json.dumps(response.to_dict()))
            sys.stdout.flush()
        except Exception as e:
            print(f"Pitot Controller error: {e}", file=sys.stderr)
