# Pitot Rust SDK

The Rust binding for Pitot: typed wire types plus a tiny stdio runner for building a
**Controller** (decides `control.requested` → allow/deny) or a passive **Consumer**
(reads the observed action/event stream). It speaks the same JSON-Lines protocol as
every other Pitot SDK — proven identical by the cross-language conformance suite
([`../tests/test_runners.py`](../tests/test_runners.py)).

## Add the dependency

The crate is `pitot`, distributed from this repository through a Git dependency.
`pitot init --language rust` pins the tag to the CLI release version. Cargo finds
the crate under `sdk/rust`; no separate Cargo registry is required.

```toml
[dependencies]
pitot = { git = "https://github.com/operatorstack/pitot", tag = "v0.1.0" }
```

Use the tag matching `pitot version`. For a development checkout, use
`pitot = { path = "path/to/pitot/sdk/rust" }` instead.

## Build a Controller

A Controller receives a typed `ControlRequested` and returns an `Outcome`. Return
`allow(...)` or `deny(...)`; the runner serializes one `control.response` per line,
in request order.

```rust
use pitot::{run_controller, allow, deny, ControlRequested, Outcome};

fn handler(req: ControlRequested) -> Outcome {
    if req.kind == "shell" {
        return deny(Some("blocked a shell command".to_string()));
    }
    allow(Some("approved".to_string()))
}

fn main() {
    run_controller("my-controller", Box::new(handler));
}
```

## Build a Consumer

A Consumer passively reads the event stream. Keep stdout clean — a Consumer must
never write to stdout (the conformance suite enforces this); log to stderr. Note the
event's protocol `type` field is `event_type` in Rust (it is `#[serde(rename =
"type")]`, since `type` is a reserved word).

```rust
use pitot::{run_consumer, Event};

fn handler(event: Event) {
    eprintln!("observed {}", event.event_type);
}

fn main() {
    run_consumer(Box::new(handler));
}
```

See the repository [README](../../README.md) for the full
runtime, host-hook wiring, and the enforcement boundary.
