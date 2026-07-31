# Pitot Python SDK

The Python binding for Pitot: typed wire types plus a tiny stdio runner for
building a **Controller** (decides `control.requested` → allow/deny) or a passive
**Consumer** (reads the observed action/event stream). It speaks the same
JSON-Lines protocol as every other Pitot SDK — proven identical by the
cross-language conformance suite ([`../tests/test_runners.py`](../tests/test_runners.py)).

## Install

Pitot SDKs install from the project's own registry through the CLI front door,
pinned to the CLI's version — never public PyPI:

```bash
pitot install python        # writes .pitot/registry + operatorstack-pitot==<version>
```

`pitot init --language python` scaffolds a complete, runnable project and registers
it as a tenant fragment; the snippets below are the core of what it writes.

## Build a Controller

A Controller receives a typed `ControlRequested` and returns an `Outcome`. Return
`allow(...)` or `deny(...)`; the runner serializes one `control.response` per line,
in request order.

```python
from pitot.runner import run_controller, allow, deny
from pitot.types import ControlRequested

def handler(req: ControlRequested):
    # decide from req.data, req.kind, etc.
    if req.kind == "shell" and "rm -rf" in str(req.data):
        return deny("blocked a destructive shell command")
    return allow("approved")

if __name__ == "__main__":
    run_controller("my-controller", handler)
```

## Build a Consumer

A Consumer passively reads the event stream. Keep stdout clean — a Consumer must
never write to stdout (the conformance suite enforces this); log to stderr.

```python
import sys
from pitot.runner import run_consumer
from pitot.types import Event

def handler(event: Event):
    print("observed", event.type, file=sys.stderr)

if __name__ == "__main__":
    run_consumer(handler)
```

## Run it behind an agent

```bash
pitot dev --host claude -- claude -p "…"    # discovers your fragment, starts the controller
```

See the repository [README](../../../public-readme-preview/README.md) for the full
runtime, host-hook wiring, and the enforcement boundary.
