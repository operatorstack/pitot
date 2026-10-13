# Runtime, configuration and privacy

## Install

The repository pins the CLI release. `pitot init`
writes two substrate files alongside your tenant fragments:

- `.pitot/version` — one committed semver line, the only version authority
- `.pitot/bin/pitot` (+ `pitot.ps1`) — a committed shim that reads the pin,
  hydrates exactly that release into a per-user cache
  (SHA-256 verified against the published `checksums.txt` during download),
  and executes it

On macOS and Linux, the default cache slot is
`~/.cache/pitot/<version>/<os>-<arch>/`. `XDG_CACHE_HOME` changes the cache base;
`PITOT_CACHE_DIR` overrides the Pitot cache directory. On Windows, the default
is `%LOCALAPPDATA%\pitot\<version>\windows-<arch>\`; `PITOT_CACHE_DIR` overrides
that directory too.

Fresh clones, CI, and cloud agents can run `.pitot/bin/pitot` (or the PowerShell
shim on Windows). It hydrates when the selected cache slot is absent and reuses
an existing slot. There is no
fallback to whatever binary happens to be on PATH; a missing release with no
network fails closed with a named error, and `PITOT_NO_HYDRATE=1` makes
hydration cache-only.

**Upgrades are a reviewed diff.** `pitot upgrade` verifies the new release,
re-checks every tenant fragment against it, and rewrites the one pin line —
nothing else. Commit that diff and every clone hydrates the new version on
its next invocation; roll back by reverting it.

```bash
pitot upgrade --check   # report pinned vs latest
pitot upgrade           # hydrate, validate tenants, rewrite .pitot/version
```

For a global CLI convenience (running `pitot init` in new repos), grab a
release binary or build from source:

```bash
go install github.com/operatorstack/pitot/cmd/pitot@latest
```

Typed SDKs install from our own registry through the distribution front door
— never public npm/PyPI, pinned to the CLI's version:

```bash
pitot install typescript   # scoped .npmrc + @operatorstack/pitot@<version>
pitot install python       # .pitot/registry + operatorstack-pitot==<version>
```

(`install typescript` creates a minimal private `package.json` when the
directory has none — npm honors a project registry config only inside a
project.)

Inspect the local boundary, pin, and cache state:

```bash
pitot doctor
```

## Start a dev runtime

First register a Controller and wire the host as described in [host setup](hosts.md).
From the project root:

```sh
pitot init --template shell-policy --language go --dir shell-policy
```

Templates are `shell-policy`, `release-approval`, `blank-controller` and
`blank-consumer`. The first four runnable languages are Python, TypeScript, Go
and Rust. The generated project must have its dependencies available before
its configured command can run. The sample shell policy is only a canary tripwire.

`pitot dev --host HOST -- COMMAND ARGS` discovers fragments, starts their
Consumers and Controllers, waits for readiness and launches the agent with
`PITOT_RUNTIME` set. It prints Controller decisions. DeepSeek also requires
`--prompt`; Devin uses ACP. See [host setup](hosts.md) for each launch form.
Readiness establishes a runtime endpoint, not live hook enforcement or trust.

The runtime directory is private to each invocation and removed on exit.
On Windows, standard npm Node launchers for Codex and Qwen are recognized without
batch evaluation. Custom `.cmd` wrappers are rejected with explicit Node
entrypoint guidance. Prefer `-- COMMAND ARGS` for paths and arguments containing
spaces; `--exec` splits its value on whitespace.

The generic subprocess launcher uses a two-second captured-output drain limit.
A descendant retaining an output pipe can produce an incomplete-output error
rather than successful completion. DeepSeek and Devin use separate ACP lifecycle
handling; see their host documentation for cancellation limits. Cleanup releases
the dev runtime but does not promise to kill arbitrary descendant processes.

## Multiple tools, one Pitot

Configuration is tenant-partitioned: every tool or person that registers
processes with Pitot owns exactly one fragment in `.pitot/conf.d/`, and no
tenant ever edits another tenant's file. The effective configuration is the
deterministic merge of all fragments in filename order:

```
.pitot/
  conf.d/
    boatstack.yaml    # a tool's controller, written by its installer
    interlock.yaml    # another tool's controller, different request kind
    my-policy.yaml    # your own, scaffolded by `pitot init`
```

Each fragment is a complete, strictly parsed mini-config declaring
`controllers:` and/or `consumers:`. Merge rules:

- **Consumers always compose.** Any number of tenants can observe
  `action.requested` events.
- **A request kind has one owner.** Two fragments claiming the same kind (for
  example `shell`) fail discovery with an error naming both files — a loud,
  attributable conflict instead of two tools silently fighting over one
  blocking hook. Controller and consumer ids must also be unique across
  fragments.
- **`requires_protocol: "1"`** optionally pins the protocol version a fragment
  was written against; a fragment this binary cannot honor fails discovery.
- **`dir:`** sets a process's working directory (relative to the repository
  root), so each tenant's command stays project-relative:

```yaml
controllers:
  shell:
    id: local-shell-policy
    command: ["go", "run", "main.go"]
    dir: "shell-policy"
    deadline_ms: 2000
    on_timeout: deny
    on_unavailable: deny
```

Installing a second Pitot-based tool is therefore additive by construction: it
drops its own fragment next to yours, `pitot run`/`pitot dev` merge them, and
uninstalling it is deleting its fragment.

## Advanced: manual runtime

`pitot dev` is the recommended path. If you need to manage the runtime yourself
(for example, sharing one runtime across several long-lived agent sessions),
start it with repository-owned configuration and an owner-only runtime
descriptor:

```bash
export PITOT_RUNTIME="${XDG_RUNTIME_DIR:-${TMPDIR:-/tmp}}/pitot/project.json"
pitot run --runtime "$PITOT_RUNTIME"
```

With no `--config`, `pitot run` discovers and merges the repository's
`.pitot/conf.d/` fragments. Pass `--config PATH` to override discovery with one
explicit file (useful for tests and ad-hoc runtimes).

One runtime owns each descriptor path. Pitot holds an operating-system lock on
the owner-only `PATH.lock` companion through startup, serving and cleanup. Leave
that empty file in place; the lock is released automatically on shutdown or a
process crash. Competing starters fail without changing the owner's descriptor.
An uncertain health result (including a timeout) also refuses takeover. A stale
descriptor is recovered only after a direct loopback connection refusal.
Older Pitot versions do not share this lock, so stop them before starting a new
runtime at the same path.

Start coding-agent CLIs from the same environment. Their `pitot hook HOST`
commands discover the authenticated runtime through `PITOT_RUNTIME`. Without
that variable or `--runtime PATH`, command hooks remain observation-only for backwards
compatibility. Once a runtime is explicitly selected, transport or
authentication failure causes Pitot to reject the controllable action; whether
the host honors a failed hook remains a host boundary. DeepSeek native middleware
requires a live explicit Controller response.

On Windows, set the descriptor in the launching PowerShell session:

```powershell
$env:PITOT_RUNTIME = Join-Path $env:LOCALAPPDATA "Pitot\project.json"
pitot run --runtime $env:PITOT_RUNTIME
```

## Two small programs

### 1. Audit shell requests without recording commands

Save the Python program below as `examples/action-audit.py` in your project,
then register this Consumer in `.pitot/conf.d/action-audit.yaml` with an `omit`
content projection:

```yaml
consumers:
  - id: action-audit
    command: ["python3", "./examples/action-audit.py"]
    events: ["action.requested"]
    projection:
      content: omit
```

Pitot writes newline-delimited JSON to the program's standard input:

```json
{
  "pitot_version": "1",
  "type": "action.requested",
  "host": { "name": "claude" },
  "action": { "id": "act_7f2", "kind": "shell" },
  "content": { "mode": "omit" },
  "observation": { "source": "host_hook", "fidelity": "direct" }
}
```

The Consumer is ordinary Python—no Pitot SDK required:

```python
import json
import sys

for line in sys.stdin:
    event = json.loads(line)
    print(json.dumps({
        "host": event["host"]["name"],
        "action_id": event["action"]["id"],
        "kind": event["action"]["kind"],
    }), file=sys.stderr)
```

The command is projected out before bytes enter the Consumer pipe. Consumer
failure cannot allow or deny the waiting host action.

### 2. Let a skill request approval

An application or skill can make a synchronous request to an already-running
runtime. Register its Controller before sending the request:

```bash
pitot request release.approval --data '{"release":"v1.4.0"}' --runtime "$PITOT_RUNTIME"
```

Register one Controller for that request kind:

```yaml
controllers:
  release.approval:
    id: local-approval
    command: ["go", "run", "./examples/local-approval"]
    deadline_ms: 2000
    on_timeout: deny
    on_unavailable: deny
```

The Controller receives:

```json
{
  "pitot_version": "1",
  "type": "control.requested",
  "kind": "release.approval",
  "action_id": "act_7f2",
  "data": { "release": "v1.4.0" }
}
```

It checks its own source of truth and returns one correlated response:

```json
{
  "pitot_version": "1",
  "type": "control.response",
  "controller_id": "local-approval",
  "action_id": "act_7f2",
  "outcome": "allow",
  "message": "v1.4.0 is approved for publication."
}
```

Pitot validates the controller identity, action ID, deadline, schema, and
single-response rule before carrying the answer back. Pitot does not know what
“approved” means; the Controller owns that definition.

The reference local-approval program handles one request and exits; it is a
wire illustration, not a persistent approval service. Its example rule allows
`release.approval` without an external approval check. Use the SDK runner and
your own source of truth for an application Controller.

## Trusted local processes

Pitot uses supervised local processes in v1. It starts declared Consumers and
Controllers itself, applies each projection before bytes enter the child pipe,
and exposes only a loopback endpoint authenticated by the owner-only runtime
descriptor. Pitot does not pass the capability token directly to child processes or include it
in its diagnostics. These are trusted programs running as the launching user:
they may read the owner-only descriptor and access its authenticated endpoint.
Pitot does not sandbox them or restrict their filesystem, environment, or network
access. Descriptor permissions exclude other users, not programs running as the owner.

## Controller guarantees

For Go applications, `runtime.Start` validates the protocol and all process
registrations before launching any child. Empty embedded runtimes remain valid;
file configuration still requires at least one registration. An explicit request
can use any configured nonempty kind, and its unavailable/timeout defaults may
allow or deny. `DeliverControlledEvent` separately requires a live Controller
reply for an action event.

Synchronous hooks are control channels: the host is blocked waiting for an
answer. Pitot makes that privilege explicit.

- Exactly one Controller may register for a request kind.
- Registration is static, auditable configuration.
- The registration declares a deadline and unavailable/timeout defaults.
- The Controller deadline covers waiting for a busy Controller, writing its
  request, and waiting for the response. A caller deadline can shorten that
  budget; canceled requests do not wait indefinitely behind another request.
- If a write is interrupted, Pitot stops that Controller because its input may
  contain a partial frame. Later requests use `on_unavailable` until the runtime
  is restarted. A timeout after a complete write leaves the Controller running;
  late replies cannot resolve a different action.
- Every response is bound to its pending action and Controller identity.
- Late, stale, duplicate, mismatched, and malformed responses are rejected.
- An admitted pending runtime request receives one terminal resolution.
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

Reason codes never contain prompts, commands, tool inputs, or outputs. A malformed
hook payload produces this diagnostic on operator stderr, and the hook returns a
blocked outcome before runtime delivery. The registered Controller does not
receive this fault as a request. Applications that need to react to diagnostics
must arrange a separate input path; host handling of the failed hook still
determines whether execution is prevented.

## Privacy model

Pitot is local and storage-free by default.

- Raw host payloads terminate inside the adapter.
- Content projection is `full`, `sha256`, or `omit` per Consumer.
- Projection happens before delivery, not inside downstream applications.
- No network exporter is enabled implicitly.
- Pitot does not retain an event history unless a configured Consumer does.
- Consumer output is not wired to the Controller response channel.
- Pitot-generated event delivery and response rejection diagnostics omit envelope
  identifiers and content. Configured process labels may appear in diagnostics.
  Controller stderr and both Consumer output streams are forwarded to operator
  stderr; this application-controlled output may contain sensitive information.

Projection controls command content in each delivered event. It preserves other
metadata and does not prevent a trusted program from obtaining content elsewhere.
SHA-256 supports equality checks and comparison against guessed content; it is
not encryption. An existing digest can be narrowed to `omit`; requests that need
unavailable full content or a missing digest are rejected. Absent content stays
absent under every projection policy.

Consumer delivery is bounded and best effort. Queue saturation and unavailable
processes produce diagnostics. An unexpected process exit or pipe-write failure
reports once that observation delivery may be incomplete, even without another
event. Queue acceptance and a successful pipe write are not acknowledgements that
an application processed the event. Pitot does not retry or retain dropped
observations; a Consumer can implement its own retention. Expected runtime
shutdown is quiet. Consumer pipe writes run separately from Controller resolution;
a slow operator diagnostic sink can still block existing delivery diagnostics.

## Applications

Use action events for reports and diagnostics, and registered requests for human
approval, delivery verification or state-dependent policy. The
[supervisory-control example](../examples/supervisory-control/README.md) shows a
shared program checking current tested artifact revisions.

`model.usage` exists in the schema and SDKs, but the runtime does not deliver it.
The [token-meter wire example](../examples/token-meter/main.go) needs records
supplied by your application. Token accounting and cross-agent memory require
additional data sources; Pitot does not supply them automatically.

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
