<p align="center">
  <img src="assets/pitot-hero.svg" width="1200" alt="Pitot — Pitot reports. Your controller decides.">
</p>

<p align="center">
  <strong>The open sensor and control transport for coding-agent tooling.</strong>
</p>

<!-- pitot-adapter-supervisor:start -->
<p align="center">
  <a href="https://github.com/operatorstack/intelligence-flow/actions/workflows/pitot-e2e.yml"><img alt="Pitot agent E2E" src="https://github.com/operatorstack/intelligence-flow/actions/workflows/pitot-e2e.yml/badge.svg?branch=main"></a>
</p>

<p align="center"><sub>Every supervised adapter is required on Ubuntu, macOS, and Windows.</sub></p>

<p align="center"><sub>Supervised adapters: Claude · Cline · Cursor · Codex · GitHub Copilot CLI · Gemini · Kimi Code · OpenCode · Pi · Qwen Code</sub></p>
<!-- pitot-adapter-supervisor:end -->

<p align="center">
  One language-neutral boundary for the coding agents your team already uses.
</p>

Pitot lets you build above coding agents without rebuilding every host
integration or forking an agent runtime. It converts host-specific activity
into a stable local event stream and carries correlated responses from your
controller when a host is waiting synchronously.

Pitot reports what happened. Your code decides what it means.

## Why Pitot?

Building above coding agents usually forces one of two expensive choices:

1. Maintain separate hooks, payload decoders, response formats, version quirks,
   and diagnostics for every host.
2. Own or fork an entire coding-agent runtime just to gain a dependable event
   boundary.

Pitot provides a third option: keep the coding agents your users already chose,
integrate with their host boundary once, and build only the product that
differentiates you.

<p align="center">
  <img src="assets/pitot-boundary.svg" width="1200" alt="Coding-agent hosts connect through Pitot to passive consumers and one controller per action kind">
</p>

Pitot separates two capabilities that are easy to blur:

| Role | Receives | Can reply? | Typical use |
|---|---|---:|---|
| **Consumer** | Projected events | No | Usage, memory, analytics, reports |
| **Controller** | Registered requests | Yes | Approval, verification, policy, workflow |

A passive Consumer cannot reach the response channel. A Controller is
statically registered for one request kind and returns at most one response for
the pending action.

## Use-case gallery

### Operational patterns (grid)

If you want to see concrete integration ideas, start with the **Use-Cases Grid**:

- [04 Use-Cases Editorial Grid](./brand-exploration/design-demos/04-use-cases-editorial-grid.html)

This gallery shows practical ways teams can compose Consumers and Controllers
without forcing each workflow into the host or into a single monolithic runtime.
It includes both engineering patterns (token metering, approvals, audit hooks) and
non-coding workflows (email triage, file movement, and local automation),
so you can quickly evaluate where Pitot helps before building.

## Two small programs

<p align="center">
  <img src="assets/pitot-two-roles.svg" width="1200" alt="A token meter consumes events while an approval controller receives a request and returns a response">
</p>

### 1. Count tokens without recording prompts

Configure a Consumer with an `omit` content projection:

```yaml
consumers:
  - id: token-meter
    command: ["python3", "./examples/token-meter.py"]
    events: ["model.usage"]
    projection:
      content: omit
```

Pitot writes newline-delimited JSON to the program's standard input:

```json
{"pitot_version":"1","type":"model.usage","session_id":"sess_42","model":"gpt-5","usage":{"input_tokens":1240,"output_tokens":380},"observation":{"source":"host_event","fidelity":"direct"}}
```

The Consumer is ordinary Python—no Pitot SDK required:

```python
import json
import sys

total = 0

for line in sys.stdin:
    event = json.loads(line)
    usage = event.get("usage", {})
    total += usage.get("input_tokens", 0)
    total += usage.get("output_tokens", 0)
    print(json.dumps({"session_tokens": total}), file=sys.stderr)
```

Pitot preserves the quality of the measurement. Provider-reported usage is
marked `direct`; tokenizer-derived usage is `estimated`; unavailable usage is
never silently manufactured.

### 2. Let a skill request approval

A coding-agent skill can make a synchronous request:

```bash
pitot request release.approval --data '{"release":"v1.4.0"}'
```

Register one Controller for that request kind:

```yaml
controllers:
  release.approval:
    id: local-approval
    command: ["./examples/local-approval"]
    deadline_ms: 2000
    on_timeout: deny
    on_unavailable: deny
```

The Controller receives:

```json
{"pitot_version":"1","type":"control.requested","kind":"release.approval","action_id":"act_7f2","data":{"release":"v1.4.0"}}
```

It checks its own source of truth and returns one correlated response:

```json
{"pitot_version":"1","type":"control.response","controller_id":"local-approval","action_id":"act_7f2","outcome":"allow","message":"v1.4.0 is approved for publication."}
```

Pitot validates the controller identity, action ID, deadline, schema, and
single-response rule before carrying the answer back. Pitot does not know what
“approved” means; the Controller owns that definition.

## Install

Download a release binary for macOS, Linux, or Windows, or install from source:

```bash
go install github.com/operatorstack/pitot/cmd/pitot@latest
```

Inspect the effective local boundary:

```bash
pitot doctor
```

Start Pitot with repository-owned configuration:

```bash
pitot run --config .pitot.yaml
```

### Kimi Code

Install Kimi Code on macOS or Linux using its official installer:

```bash
curl -fsSL https://code.kimi.com/kimi-code/install.sh | bash
kimi --version
```

On Windows, use the official PowerShell installer:

```powershell
irm https://code.kimi.com/kimi-code/install.ps1 | iex
kimi --version
```

Connect Kimi Code's blocking shell boundary to Pitot in
`~/.kimi-code/config.toml`:

```toml
[[hooks]]
event = "PreToolUse"
matcher = "Bash"
command = "pitot hook kimi"
```

Kimi sends the hook payload to Pitot on standard input. Pitot exits `0` when
the request is accepted and `2` when a malformed request must be blocked. Check
the non-interactive Kimi CLI after configuration with:

```bash
kimi -p "Show the repository status"
```

See the [Kimi Code documentation](https://www.kimi.com/code/docs/en/) for CLI
authentication, configuration, and hook behavior.

### GitHub Copilot CLI

Add the following Claude-compatible hook to `~/.copilot/settings.json`:

```json
{
  "hooks": {
    "PreToolUse": [{
      "matcher": "Bash",
      "hooks": [{"type": "command", "command": "pitot hook copilot"}]
    }]
  }
}
```

The PascalCase event keeps the blocking payload on Pitot's standard
`hook_event_name` and `tool_input.command` boundary. See the official
[Copilot CLI hooks reference](https://docs.github.com/en/copilot/reference/hooks-reference).

### Qwen Code

Add this command hook to `~/.qwen/settings.json`:

```json
{
  "hooks": {
    "PreToolUse": [{
      "matcher": "^Bash$",
      "hooks": [{"type": "command", "command": "pitot hook qwen"}]
    }]
  }
}
```

Qwen sends the native JSON payload on standard input and honors Pitot's
blocking exit status. See the official
[Qwen Code hooks guide](https://qwenlm.github.io/qwen-code-docs/en/users/features/hooks/).

### Pi

Copy `integrations/pi/pitot.ts` into `~/.pi/agent/extensions/pitot.ts` (or the
repository-local `.pi/extensions/` directory). The extension converts Pi's
blocking `tool_call` event into Pitot's stable envelope and returns Pi's native
`block` response when Pitot rejects the request. See the official
[Pi extensions documentation](https://pi.dev/docs/latest/extensions).

### Cline

Copy `integrations/cline/PreToolUse` to `~/Documents/Cline/Hooks/PreToolUse` on
macOS or Linux and make it executable. On Windows, copy `PreToolUse.ps1` into
that directory instead. Enable hooks in Cline's Hooks tab or run
`cline config set hooks-enabled=true`. These
bridges pass Cline's native nested payload to `pitot hook cline` and translate
the result into Cline's `cancel` response. See the official
[Cline hooks documentation](https://docs.cline.bot/customization/hooks).

Pitot uses supervised local processes in v1. It starts declared Consumers and
Controllers itself, applies each projection before bytes enter the child pipe,
and exposes no unauthenticated local socket.

## Language-neutral by design

Pitot's reference implementation is Go. Its public contract is not.

Compatibility is defined by:

- versioned JSON Schemas;
- newline-delimited JSON framing;
- explicit request/response state machines;
- capability and projection declarations; and
- language-neutral conformance fixtures.

If a program can read JSON Lines from standard input, it can be a Consumer. If
it can return one schema-valid response on its dedicated output channel, it can
be a Controller.

Official client libraries are optional conveniences—not a prerequisite and
not the source of protocol truth.

## Event envelope

Every event identifies its schema, source, session, and observation quality:

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
    "sha256": "9f2..."
  },
  "observation": {
    "source": "host_hook",
    "fidelity": "direct"
  }
}
```

Adapters preserve host capability differences. A normalized field is never
presented as directly observed when the host omitted it or Pitot inferred it.

## Controller guarantees

Synchronous hooks are control channels: the host is blocked waiting for an
answer. Pitot makes that privilege explicit.

- Exactly one Controller may register for a request kind.
- Registration is static, auditable configuration.
- The registration declares a deadline and unavailable/timeout defaults.
- Every response is bound to its pending action and Controller identity.
- Late, stale, duplicate, mismatched, and malformed responses are rejected.
- A pending action receives exactly one terminal resolution.
- Registration and its configuration fingerprint appear in diagnostics.

The bridge mechanically enforces the declared default. It contains no approval,
safety, completion, or shipping policy engine.

## Boundary faults

Pitot distinguishes a broken measurement boundary from a judgment about the
work:

```json
{
  "pitot_version": "1",
  "type": "boundary.fault",
  "host": "cursor",
  "action_id": "act_7f2",
  "reason": "empty-command"
}
```

Reason codes never contain prompts, commands, tool inputs, or outputs. A
Controller may choose to deny, retry, report, or escalate the fault according
to its own policy.

## Privacy model

Pitot is local and storage-free by default.

- Raw host payloads terminate inside the adapter.
- Content projection is `full`, `sha256`, or `omit` per Consumer.
- Projection happens before delivery, not inside downstream applications.
- No network exporter is enabled implicitly.
- Pitot does not retain an event history unless a configured Consumer does.
- Passive Consumers receive no Controller capability.
- Diagnostics and fault codes are content-safe.

## What can you build?

- selective cross-agent memory;
- session and decision reports;
- token and cost attribution;
- reliability and host-compatibility diagnostics;
- OpenTelemetry exporters;
- human approval routers;
- security or compliance Controllers;
- delivery verifiers;
- custom agent interfaces over existing runtimes; and
- new Pitot-compatible coding-agent runtimes.

## What Pitot does not decide

Pitot does not define whether:

- work is correct or complete;
- an action is safe;
- a person granted approval;
- evidence satisfies a requirement;
- a claim is valid; or
- something may be shipped.

Those meanings belong to your Controller. **Pitot reports. Your controller
decides.**

## Project layout

```text
pitot/
├── schema/          public event and response schemas
├── protocol/        framing and state-machine specifications
├── adapters/        Claude Code, Cursor, and Codex boundaries
├── sensor/          normalization and observation pipeline
├── bridge/          controller routing and response transport
├── projection/      full, sha256, and omit policies
├── conformance/     language-neutral fixtures and negative controls
├── examples/        token-meter and local-approval
└── cmd/pitot/        reference Go executable
```

## Design principles

1. **Report before interpretation.** Preserve what the host actually supplied.
2. **Make capability structural.** Consumers cannot reply; Controllers can.
3. **Keep host churn together.** Decoders and encoders share one compatibility
   boundary.
4. **Project before delivery.** Privacy is enforced before content crosses the
   process boundary.
5. **Correlate every answer.** A response can resolve only its pending action.
6. **Prefer one maintained implementation.** Use a language-neutral protocol
   instead of rewriting the host boundary in every ecosystem.
7. **Extend standards where they fit.** Map to OpenTelemetry GenAI conventions
   without erasing Pitot-specific provenance or semantic events.

## Name

A pitot probe measures pressure difference so another system can determine
airspeed. It does not fly the aircraft.

Pitot applies the same separation to coding-agent tooling: measurement belongs
at the host boundary; interpretation and control belong downstream.

## Contributing

Start with the protocol and conformance fixtures. A new adapter should declare
its host capabilities, normalize supported events, classify boundary faults
without exposing content, encode Controller responses, and pass the shared
positive and negative fixture suite.

See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup and compatibility
requirements.

## License

Apache-2.0
