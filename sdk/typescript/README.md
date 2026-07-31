# Pitot TypeScript SDK

The TypeScript binding for Pitot: typed wire types plus a tiny stdio runner for
building a **Controller** (decides `control.requested` → allow/deny) or a passive
**Consumer** (reads the observed action/event stream). It speaks the same
JSON-Lines protocol as every other Pitot SDK — proven identical by the
cross-language conformance suite ([`../tests/test_runners.py`](../tests/test_runners.py)).

## Install

Pitot SDKs install from the project's own registry through the CLI front door,
pinned to the CLI's version — never public npm:

```bash
pitot install typescript    # scoped .npmrc + @operatorstack/pitot@<version>
```

`pitot init --language typescript` scaffolds a complete, runnable project and
registers it as a tenant fragment; the snippets below are the core of what it
writes.

## Build a Controller

A Controller receives a typed `ControlRequested` and returns an `Outcome`. Return
`allow(...)` or `deny(...)`; the runner serializes one `control.response` per line,
in request order.

```typescript
import { runController, allow, deny, ControlRequested } from "@operatorstack/pitot";

runController("my-controller", async (req: ControlRequested) => {
    if (req.kind === "shell" && String(req.data).includes("rm -rf")) {
        return deny("blocked a destructive shell command");
    }
    return allow("approved");
});
```

## Build a Consumer

A Consumer passively reads the event stream. Keep stdout clean — a Consumer must
never write to stdout (the conformance suite enforces this); log to stderr.

```typescript
import { runConsumer, Event } from "@operatorstack/pitot";

runConsumer(async (event: Event) => {
    console.error("observed", event.type);
});
```

## Run it behind an agent

```bash
pitot dev --host claude -- claude -p "…"    # discovers your fragment, starts the controller
```

See the repository [README](../../../public-readme-preview/README.md) for the full
runtime, host-hook wiring, and the enforcement boundary.
