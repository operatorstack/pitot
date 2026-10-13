# Wire contract

The canonical contract is the versioned schema plus the raw transport rules and
request state machine. Tested runner agreement is scoped to the conformance
corpus and supported targets; generated types alone do not validate every input.

## Language-neutral by design

Pitot's reference implementation is Go. Its public contract is not.

Compatibility is defined by:

- versioned JSON Schemas;
- newline-delimited JSON framing;
- explicit request/response state machines;
- capability and projection declarations; and
- language-neutral conformance fixtures.

If a program can read JSON Lines from standard input, it can be a Consumer. A Controller must also follow the raw framing, registration and correlation
rules below; a schema-valid JSON value alone is not sufficient.

Official client libraries are optional conveniences—not a prerequisite and
not the source of protocol truth.

## Event envelope

Events identify their schema, host, source, and observation quality.
`session_id` is optional, opaque metadata supplied by the host; Pitot preserves
it without generating a substitute when it is unavailable. Group sessions by
`host.name` and `session_id`, rather than treating IDs from different hosts as
one namespace. The generated per-action ID correlates control replies and is
separate from the host's session identity. Session metadata is retained by every
content projection and does not authenticate a Controller.

This example shows an envelope with session metadata:

```json
{
  "pitot_version": "1",
  "id": "evt_01J...",
  "type": "action.requested",
  "time": "2026-07-19T16:05:00Z",
  "host": {
    "name": "cursor",
    "adapter_version": "1.0.0"
  },
  "session_id": "sess_42",
  "action": {
    "id": "act_7f2",
    "kind": "shell"
  },
  "content": {
    "mode": "sha256",
    "sha256": "9f2c00a18f8164a5f13127c1c38e48e67d97db8a03ccdc0c7a1cb86842f90d85"
  },
  "observation": {
    "source": "host_hook",
    "fidelity": "direct"
  }
}
```

Adapters preserve host capability differences. A normalized field is never
presented as directly observed when the host omitted it or Pitot inferred it.

## Runtime envelope admission

The reference runtime validates event semantics before any Consumer delivery.
Events require version `1`, a nonempty host name, and an observation source of
`host_hook` or `host_event` with fidelity `direct` or `estimated`. Action events
also require a nonempty action ID and kind. Optional event metadata and
Controller-owned request data retain their existing meaning.

Content representations are mutually exclusive:

- `full` carries one JSON string containing the measured bytes, with no digest;
- `sha256` carries a 64-digit hexadecimal digest, with no full content;
- `omit` carries neither representation.

Contradictory or malformed content is rejected before fanout, including events
submitted directly through the Go Manager. Projection validates content again
before using an already-projected representation. Validation errors contain no
payload or envelope identifiers. Valid envelopes retain the existing projection,
Controller correlation, timeout and unavailable-default behavior.

The shared typed-envelope vectors are in
`conformance/fixtures/envelopes.jsonl`. This admission gate does not enable new
event delivery capabilities: runtime ingress still accepts only
`action.requested`. SDK readers apply the wire contract below before invoking handlers.

## SDK wire contract

The canonical v1 value constraints are published in [schema/v1.json](../schema/v1.json).
Generated SDK types are structural conveniences; the Go, Python, TypeScript and
Rust runners validate raw input before constructing handler values. Optional
metadata may be absent, but explicit null is rejected for typed string/object
fields. Controller-owned `data` may contain any JSON value, including null.

The JSON-Lines transport profile adds constraints that JSON Schema cannot check
after decoding:

- UTF-8 and Unicode scalar strings, with unique decoded member names in every
  object, including nested Controller data;
- one object per LF-delimited record, exact-case envelope fields, and no trailing
  JSON or unknown envelope/typed-object fields;
- at most 4 MiB per record, excluding LF (CR counts), and 128 nested containers;
- token counts spelled as JSON integers, from 0 through 9007199254740991. Decimal
  and exponent spellings are rejected even when their mathematical value is
  integral; this preserves typed-reader compatibility and avoids silent rounding.

Blank JSON whitespace lines are ignored. CRLF and a final nonempty record without
LF are accepted. Malformed and oversized lines are skipped with content-safe
diagnostics; oversized records are drained so the next valid record can proceed.
Controller results are validated before stdout, and absent response messages are
omitted. FIFO handler ordering is preserved, including asynchronous TypeScript.
Python admission does not depend on assertions and also runs under `python -O`.

The shared raw corpus is [conformance/fixtures/wire.jsonl](../conformance/fixtures/wire.jsonl).
Tests distinguish decoded JSON Schema constraints from transport constraints and
drive the real runners. Arbitrary Controller data still uses each language's
native JSON representation: numeric precision/range and absent/null distinctions
are not made identical by the SDKs. Generated constructors used directly do not
apply the runner admission gate. The Go API retains platform-sized `int` counters;
verification and shipped targets are 64-bit, while 32-bit callers have a narrower
native integer range. The runtime continues to deliver only
`action.requested` events; accepting `model.usage` in SDKs adds no host capability.
