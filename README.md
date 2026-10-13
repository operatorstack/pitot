<p align="center">
  <img src="assets/pitot-hero.svg" width="1200" alt="Pitot — Pitot reports. Your controller decides.">
</p>

<p align="center">
  <strong>Keep your coding agent. Add the behavior it is missing.</strong>
</p>

<!-- pitot-adapter-supervisor:start -->
<p align="center">
  <a href="https://github.com/operatorstack/pitot/actions/workflows/pitot-e2e.yml"><img alt="Pitot agent E2E" src="https://github.com/operatorstack/pitot/actions/workflows/pitot-e2e.yml/badge.svg?branch=main"></a>
</p>

<p align="center"><sub>Every supervised adapter must pass a binary-observed prompt → real action boundary → projected Consumer → Controller allow/deny → tool-result loop on Ubuntu, macOS, and Windows.</sub></p>

<p align="center"><sub>Supervised adapters: Claude · Cursor · Codex · Devin · DeepSeek Harness · GitHub Copilot CLI · Gemini · Kimi Code · OpenCode · Pi · Qwen Code</sub></p>

<p align="center"><sub>Supervised runtime capabilities: action control · Consumer delivery · explicit request</sub></p>
<!-- pitot-adapter-supervisor:end -->

<p align="center">
  One language-neutral boundary for the coding agents your team already uses.
</p>

Pitot puts your own code at a coding agent's wired action boundary, so it can
allow, deny, or observe a proposed action. The shipped Codex, Qwen, and DeepSeek
integrations supervise shell tools; they do not mediate every file edit or tool
call. Applications can also issue explicit requests for their own action kinds.

Pitot reports what happened. Your code decides what it means.

## See it work with Kimi

The fastest way to understand Pitot is to watch one real command get allowed and
another get denied. This walkthrough is exactly what Pitot's automated test suite
exercises on every commit, so the behavior below is verified, not aspirational.

**1. Scaffold a sample shell policy.** This writes a runnable Controller that
allows shell commands by default and denies any command containing the canary
string `PITOT_DENY_ME`:

```bash
pitot init --template shell-policy --language go --dir ./kimi-policy
cd ./kimi-policy
```

**2. Check your Kimi host wiring** (Pitot does not edit your Kimi config for you):

```bash
pitot doctor --host kimi
```

If the `PreToolUse` hook is missing, add it to `~/.kimi-code/config.toml`:

```toml
[[hooks]]
event = "PreToolUse"
matcher = "Bash"
command = "pitot hook kimi"
```

**3. Run Kimi behind the Controller.** `pitot dev` starts the runtime, launches
the agent you name after `--`, and prints each decision:

```bash
pitot dev --host kimi -- kimi -p "Run: echo hello"
```

An ordinary command is allowed and runs. Now ask for the canary:

```bash
pitot dev --host kimi -- kimi -p "Run: PITOT_DENY_ME=1 echo nope"
```

The Controller denies it. The denied command never executes, and the denial
reason — `Pitot sample policy blocked the PITOT_DENY_ME canary.` — is returned to
Kimi as the blocked tool result. The `shell-policy` sample is a demonstration
tripwire, not a general shell-security control; the point is that *your* code
made the decision.

### Devin uses ACP

Devin exposes the same Pitot control semantics through a stateful ACP boundary,
so it does not need lifecycle-hook files:

```bash
pitot doctor --host devin
pitot dev --host devin -- devin -p "Run: echo hello"
```

To attach to an already-running Pitot runtime, use the explicit single-prompt
surface:

```bash
pitot acp devin --runtime "$PITOT_RUNTIME" --prompt "Run: echo hello"
```

Pitot selects only ACP's one-shot `allow_once` or `reject_once` options.
Interactive, resume, and multi-turn Devin sessions are not supported in this
initial adapter.

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

Pitot does not interpret Consumer output as Controller responses. A Controller is
statically registered for one request kind and returns at most one response for
the pending action.

## Two small programs

<p align="center">
  <img src="assets/pitot-two-roles.svg" width="1200" alt="An action auditor consumes events while an approval controller receives a request and returns a response">
</p>

### 1. Audit shell requests without recording commands

Configure a Consumer with an `omit` content projection:

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
{"pitot_version":"1","type":"action.requested","host":{"name":"claude"},"action":{"id":"act_7f2","kind":"shell"},"content":{"mode":"omit"},"observation":{"source":"host_hook","fidelity":"direct"}}
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

A coding-agent skill can make a synchronous request:

```bash
pitot request release.approval --data '{"release":"v1.4.0"}' --runtime "$PITOT_RUNTIME"
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

**Nobody installs Pitot per machine — the repository pins it.** `pitot init`
writes two substrate files alongside your tenant fragments:

- `.pitot/version` — one committed semver line, the only version authority
- `.pitot/bin/pitot` (+ `pitot.ps1`) — a committed shim that reads the pin,
  hydrates exactly that release into a per-user cache
  (`~/.cache/pitot/<version>/`, sha256-verified against the published
  `checksums.txt`), and execs it

Fresh clones, CI, and cloud agents run `.pitot/bin/pitot` with zero setup —
the first invocation hydrates, every later one is a cache hit. There is no
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

Inspect the effective local boundary, pin, and cache state at any time:

```bash
pitot doctor
```

## Quickstart

**1. Scaffold a Controller.** `pitot init` writes a runnable project — source
and a package manifest — and registers it as one tenant fragment under
`.pitot/conf.d/`. It never overwrites existing files unless you pass `--force`.
Pick a starting template with `--template`:

```bash
pitot init --template shell-policy --language go --dir kimi-policy
```

```
Initialized go controller (shell-policy) in kimi-policy
Files written: .pitot/conf.d/kimi-policy.yaml, kimi-policy/go.mod, kimi-policy/main.go
Next:
  1. Configure a supported host hook (see: pitot doctor --host HOST).
  2. Run: pitot dev --host HOST -- AGENT [ARGS...]
     example: pitot dev --host kimi -- kimi -p "<prompt>"
```

Available templates are `shell-policy` (allow/deny shell commands),
`release-approval` and `blank-controller` (request/response controllers), and
`blank-consumer` (a passive event reader). Without `--template`, `pitot init`
detects the language from the current directory or prompts you to choose. The
four first-class languages (`python`, `typescript`, `go`, `rust`) each generate a
complete project that builds after installing dependencies.

**2. Run your agent behind it.** `pitot dev` discovers every fragment under
`.pitot/conf.d/`, starts the runtime and the declared Controllers, waits until
the runtime is ready, then launches the agent you name after `--` with
`PITOT_RUNTIME` set so its host hook finds the runtime. It prints each decision
as the agent makes it:

```bash
pitot dev --host kimi -- kimi -p "Run: PITOT_DENY_ME=1 echo nope"
```

```
Starting Pitot dev environment for host kimi...
Runtime ready. Starting agent: kimi -p Run: PITOT_DENY_ME=1 echo nope
Decisions:
  [DENY]  shell (act_1a9) — Pitot sample policy blocked the PITOT_DENY_ME canary.
Agent finished. Runtime stopped.
```

`--host` must name a supported agent (`claude`, `codex`, `copilot`, `cursor`,
`devin`, `gemini`, `kimi`, `opencode`, `pi`, `qwen`). Hook-based hosts must
already be wired to `pitot hook HOST` (see **Connect your agent** and
`pitot doctor --host HOST`); Devin connects over ACP and needs no hook file.
The runtime descriptor lives in a per-invocation
temporary path and is removed on exit, so concurrent `pitot dev` sessions never
collide.

On Windows, `pitot dev` recognizes standard npm Node launchers for Codex and
Qwen, preserving their package entrypoint and arguments without invoking a
command shell. Custom `.cmd` wrappers are rejected with guidance to use
`-- node ABSOLUTE_ENTRYPOINT ARGS`. Prefer `-- COMMAND ARGS` for paths or
arguments containing spaces; `--exec` splits its value on whitespace.
When output is captured, an agent descendant that keeps an output pipe open
can cause an incomplete-output error after a two-second drain limit. This
releases the dev runtime; it does not terminate arbitrary descendant processes.

**3. Swap the agent.** The same project — the same Controller and the same
fragment — works with any other supported host whose hook is wired. Change only
`--host` and the agent command:

```bash
pitot dev --host cursor -- cursor-agent -p "Run: PITOT_DENY_ME=1 echo nope"
```

The boundary is language- and agent-neutral: one Controller, every agent.

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
    dir: "kimi-policy"
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
export PITOT_RUNTIME="${XDG_RUNTIME_DIR:-$TMPDIR}/pitot/project.json"
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
that variable or `--runtime PATH`, hooks remain observation-only for backwards
compatibility. Once a runtime is explicitly selected, transport or
authentication failure blocks the controllable action.

On Windows, set the descriptor in the launching PowerShell session:

```powershell
$env:PITOT_RUNTIME = Join-Path $env:LOCALAPPDATA "Pitot\project.json"
pitot run --runtime $env:PITOT_RUNTIME
```

## Supported hosts

Every host below normalizes its native blocking boundary to a `shell` action and
passes Pitot's language-neutral decoder conformance suite. The E2E column marks
adapters exercised by the cross-platform agent supervisor (the badge at the top);
the Platforms column names the operating systems each one is verified on. Kimi
additionally has an in-repo, no-model test that asserts the full allow **and**
deny control path end to end.

| Host | Blocking boundary | Hook wiring | Platforms | Verified in this repo |
|---|---|---|---|---|
| Kimi Code | `PreToolUse` / Bash | native `config.toml` | Ubuntu · macOS · Windows | decoder + E2E + allow/deny control test |
| Claude | `PreToolUse` | native settings hook | Ubuntu · macOS · Windows | decoder + E2E |
| Cursor | `beforeShellExecution` | bridge (`integrations/cursor`) | Ubuntu · macOS · native Windows with PowerShell 7 | decoder + E2E |
| Codex | `PreToolUse` | bridge (`integrations/codex`) | Ubuntu · macOS · Windows | decoder + E2E |
| DeepSeek Harness | `tools/pre-execute` / `bash`, `pwsh` | native middleware ([setup](docs/deepseek-adapter.md)) | Ubuntu · macOS · Windows | decoder + E2E |
| Devin | `session/request_permission` (ACP) | stateful ACP transport | Ubuntu · macOS · Windows | decoder + E2E |
| GitHub Copilot CLI | `PreToolUse` | bridge (`integrations/copilot`) | Ubuntu · macOS · Windows | decoder + E2E |
| Gemini | `BeforeTool` | bridge (`integrations/gemini`) | Ubuntu · macOS · Windows | decoder + E2E |
| OpenCode | `PreToolUse` | bridge (`integrations/opencode`) | Ubuntu · macOS · Windows | decoder + E2E |
| Pi | `tool_call` | extension (`integrations/pi`) | Ubuntu · macOS · Windows | decoder + E2E |
| Qwen Code | `PreToolUse` | bridge (`integrations/qwen`) | Ubuntu · macOS · Windows | decoder + E2E |

"Decoder" means Pitot correctly normalizes that host's payload into the stable
event envelope. It does not claim Pitot judges whether any command is safe — that
decision belongs to your Controller. Hosts run natively on Windows. Cursor's
Windows hook bridge requires PowerShell 7.

## Host admission criteria

A host earns a supervised adapter only when its blocking boundary can complete
Pitot's causal loop: a proposed command must reach a Controller, an allow or
deny decision must apply **before** the command runs, and — critically — a
denied action must return to the model so the agent can continue from the
blocked outcome rather than halting. This last requirement, deny-continuation,
is what distinguishes a supervisable boundary from one that can only abort.

Devin is the worked example. Its lifecycle hooks (`PreToolUse`,
`PermissionRequest`) apply a denial but end the turn instead of handing the
rejected outcome back to the model, so they do not complete the loop in
non-interactive mode. Its Agent Client Protocol surface does: the client
selects `reject_once`, the canary never executes, and Devin makes a follow-up
model request carrying the rejection and continues. Pitot therefore ships Devin
over ACP and does not ship the hook wiring. The full investigation, including
the content-safe evidence receipt, is in
[docs/devin-adapter-research.md](docs/devin-adapter-research.md).

## Connect your agent

The per-host hooks below wire each agent's native blocking boundary to Pitot.
Hosts whose hook config lives **in the repository** are wired by Pitot itself:

```bash
pitot init --host claude    # also: cursor, codex, gemini
```

That writes exactly one marker-owned entry into the host's repo config
(`.claude/settings.json`, `.cursor/hooks.json`, `.codex/hooks.json`,
`.gemini/settings.json`), pointing at the repo shim — foreign entries are
never touched, and the committed fragment `.pitot/hooks/<host>.fragment.json`
witnesses what was installed. `pitot doctor --host HOST` reports the entry as
FOUND, MISSING, or DRIFTED against that witness; `pitot doctor --host HOST
--fix` restores a drifted entry (Pitot's own entries only — that is the single
mutation doctor ever performs, and only on request).

For Codex, Qwen and DeepSeek, doctor also checks the executable and the
integration's static setup on macOS, Linux and Windows. It reports independent
problems together. A `static setup: PASS` is scoped to the inspected files and
dependencies; `live enforcement: UNVERIFIED` means no agent action or Controller
decision was exercised. Codex session trust and normalized hook hashes still
require inspection in `/hooks`; stored trust that doctor cannot compare returns
a nonzero result. Unsupported command forms, conflicting Qwen hook definitions
and additional Codex configuration layers also require native inspection.

With `PITOT_RUNTIME` unset, hooks remain observe-only. With it set, doctor checks
the selected runtime's identity through its local health endpoint. It does not
follow redirects, use an HTTP proxy, contact remote endpoints or submit a test
action. A healthy runtime does not establish that a Controller is available.
Plain doctor does not modify settings or grant trust; `--fix` performs the
supported wiring step, then runs the diagnostic checks separately.

Hosts configured at **user level** (Kimi, Copilot, Qwen) or via **plugin
files** (OpenCode, Pi) keep the one-time manual edit below — Pitot does not
edit files outside the repository; `pitot init --host kimi` prints the exact
snippet. Once wired, both `pitot dev` and the manual runtime flow use the same
hook.

### Claude Code / Codex

```bash
pitot init --host claude
pitot init --host codex
```

Both wire a `PreToolUse` hook (matcher `Bash`) whose command runs the repo
shim: `"$CLAUDE_PROJECT_DIR"/.pitot/bin/pitot hook claude` (Codex uses the
repo-relative equivalent plus a PowerShell `commandWindows`). Exit `0` allows;
exit `2` blocks with the Controller's reason.

For Codex, the project-local `.codex/` configuration must be trusted. Open
`/hooks` in the Codex CLI to review and trust the exact hook definition. New or
changed non-managed hooks are skipped until trusted; writing the hook file
alone does not activate supervision. See [Codex hook trust](https://learn.chatgpt.com/docs/hooks#review-and-trust-hooks).

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
the request is accepted and `2` when input is malformed or the configured
Controller denies the action. Check
the non-interactive Kimi CLI after configuration with:

```bash
kimi -p "Show the repository status"
```

See the [Kimi Code documentation](https://www.kimi.com/code/docs/en/) for CLI
authentication, configuration, and hook behavior.

### GitHub Copilot CLI

Copy `integrations/copilot/PreToolUse` to a stable executable path (or use
`PreToolUse.ps1` on Windows), then add the following Claude-compatible hook to
`~/.copilot/settings.json`:

```json
{
  "hooks": {
    "PreToolUse": [{
      "matcher": "Bash",
      "hooks": [{"type": "command", "command": "/path/to/PreToolUse"}]
    }]
  }
}
```

The bridge keeps the blocking payload on Pitot's standard `hook_event_name`
and `tool_input.command` boundary and returns Copilot's structured native deny
reason when a Controller rejects the command. See the official
[Copilot CLI hooks reference](https://docs.github.com/en/copilot/reference/hooks-reference).

### Cursor

```bash
pitot init --host cursor
```

Writes the committed bridge `.pitot/bin/hooks/cursor-beforeShellExecution`
(which execs the repo shim and returns Cursor's native `permission: "deny"`
envelope, including the Controller message) and points `.cursor/hooks.json` at
it with `failClosed: true`. See Cursor's
[hooks documentation](https://cursor.com/docs/agent/hooks).

The generated Cursor bridge above uses a Unix shell. For native Windows, use
`integrations/cursor/beforeShellExecution.ps1` with PowerShell 7 and keep
`failClosed: true` in the hook configuration. Put the checked PowerShell 7
directory on Cursor's process `PATH` and invoke that same `pwsh.exe` explicitly
in the hook command. Cursor uses PowerShell to read and pass the hook payload;
Windows PowerShell 5.1 can corrupt Unicode before the bridge receives it and is
not supported by this bridge. The verification harness configures both shell
invocations without changing the user's `PATH` or installing WSL.

### Gemini

```bash
pitot init --host gemini
```

Writes the committed bridge `.pitot/bin/hooks/gemini-BeforeTool` and registers
it as a `BeforeTool` command hook for `run_shell_command`. The bridge
translates Pitot rejection into Gemini's structured `decision: "deny"` and
`reason` response so the model receives the blocked tool result. See the
[Gemini CLI hooks reference](https://geminicli.com/docs/hooks/reference/).

### Qwen Code

Copy `integrations/qwen/PreToolUse` to a stable executable path (or use the
Node-based `PreToolUse.cjs` bridge on Windows), then add this command hook to
`~/.qwen/settings.json`:

```json
{
  "hooks": {
    "PreToolUse": [{
      "matcher": "^run_shell_command$",
      "hooks": [{"type": "command", "command": "/path/to/PreToolUse"}]
    }]
  }
}
```

The matcher uses Qwen's runtime shell tool ID. Compatibility aliases such as
`Bash` match exactly; anchoring that alias as `^Bash$` does not select the shell
tool.

Qwen sends the native JSON payload on standard input. The bridge returns its
native structured allow or deny decision and preserves the Controller reason.
See the official
[Qwen Code hooks guide](https://qwenlm.github.io/qwen-code-docs/en/users/features/hooks/).

### Pi

Copy `integrations/pi/pitot.ts` into `~/.pi/agent/extensions/pitot.ts` (or the
repository-local `.pi/extensions/` directory). The extension converts Pi's
blocking `tool_call` event into Pitot's stable envelope and returns Pi's native
`block` response when Pitot rejects the request. See the official
[Pi extensions documentation](https://pi.dev/docs/latest/extensions).

### Devin

Devin needs no hook file. Pitot speaks to it over the Agent Client Protocol,
launching `devin acp` as a stdio JSON-RPC server and correlating each
`tool_call` command by its tool-call ID:

```bash
pitot dev --host devin -- devin -p "Run: echo hello"
```

To attach to an already-running runtime, use the explicit single-prompt
surface:

```bash
pitot acp devin --runtime "$PITOT_RUNTIME" --prompt "Run: echo hello"
```

Pitot maps an allow decision to ACP's one-shot `allow_once` and a deny to
`reject_once`; it never selects a persistent option such as `allow_always` or a
bypass mode. Attestation comes from the Controller receipts and canary rather
than a lifecycle-hook witness. This initial adapter is single-prompt; resume and
multi-turn Devin sessions are not yet supported. See
[docs/devin-adapter-research.md](docs/devin-adapter-research.md) for why ACP,
not hooks, is the supervised boundary.

Pitot uses supervised local processes in v1. It starts declared Consumers and
Controllers itself, applies each projection before bytes enter the child pipe,
and exposes only a loopback endpoint authenticated by the owner-only runtime
descriptor. Pitot does not pass the capability token directly to child processes or include it
in its diagnostics. These are trusted programs running as the launching user:
they may read the owner-only descriptor and access its authenticated endpoint.
Pitot does not sandbox them or restrict their filesystem, environment, or network
access. Descriptor permissions exclude other users, not programs running as the owner.

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

The canonical v1 value constraints are published in [schema/v1.json](schema/v1.json).
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

The shared raw corpus is [conformance/fixtures/wire.jsonl](conformance/fixtures/wire.jsonl).
Tests distinguish decoded JSON Schema constraints from transport constraints and
drive the real runners. Arbitrary Controller data still uses each language's
native JSON representation: numeric precision/range and absent/null distinctions
are not made identical by the SDKs. Generated constructors used directly do not
apply the runner admission gate. The Go API retains platform-sized `int` counters;
verification and shipped targets are 64-bit, while 32-bit callers have a narrower
native integer range. The runtime continues to deliver only
`action.requested` events; accepting `model.usage` in SDKs adds no host capability.

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

## What can you build?

The reference runtime delivers `action.requested` to Consumers and routes
application-issued control requests to registered Controllers. The SDKs can
represent `model.usage`, but the shipped runtime does not collect or deliver
usage events. Token accounting and cross-agent memory need additional data
sources supplied by your application.

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

## Threat model

Pitot mediates the host's wired boundary and nothing else. Understanding where
that boundary ends is part of using it correctly.

- **Only the wired boundary is mediated.** Pitot sees an action when the host
  actually routes it through the configured hook or ACP transport. An action
  the host takes through a path you did not wire is not observed. `pitot doctor
  --host HOST` reports whether the boundary is present, and for repo-owned
  hosts whether the entry has drifted.
- **User-level hook configs are user-editable.** For hosts wired at user level
  (Kimi, Copilot, Qwen) the hook lives in a file the user owns and can change
  or remove. Pitot does not police edits outside the repository; it reports the
  current state through `pitot doctor`.
- **Without a runtime, hooks only observe.** A `pitot hook` invocation with no
  `PITOT_RUNTIME` (or `--runtime`) selected records the action and exits
  allowing it — observation-only, for backwards compatibility. It now prints a
  one-line notice so this mode is never silent, and `pitot doctor` flags a host
  that is wired but has no runtime. Once a runtime is explicitly selected,
  transport or authentication failure blocks the controllable action rather
  than falling open.
- **DeepSeek requires a live Controller reply.** Its native shell middleware
  blocks execution without a selected runtime or an explicit matching reply,
  even if ordinary runtime fallback defaults allow. This differs from the
  observation-only behavior of command hooks with no runtime selected.
- **Hosts retain their own bypass options.** Some hosts expose persistent or
  bypass permissions (for example Devin's ACP `allow_always` and switch-bypass
  modes). Pitot never selects them — it uses only the one-shot `allow_once` /
  `reject_once` options — but a human operating the host directly still can.
- **Kimi executes the action if its hook process crashes or times out.** This
  is Kimi's host semantics, not a Pitot decision, and Pitot cannot override it:
  a supervisory-control analysis of the Kimi lifecycle shows a reachable
  transition from the pending state straight to an unsupervised execution when
  the hook fails open, which no supervisor placed at the boundary can prevent.
  `pitot doctor --host kimi` states this plainly. If your policy cannot
  tolerate fail-open execution, prefer a fail-closed host (such as Cursor,
  wired with `failClosed: true`) or Devin's ACP transport.
- **Codex hook failures can leave an action unsupervised.** A functioning
  synchronous hook can block an action with a Controller's explicit denial or
  Pitot's rejection of an unavailable selected runtime. Codex can continue
  the tool when the hook cannot start, crashes, times out, or returns invalid
  output. That host behavior is separate from a Controller's decision; a
  passing allow/deny test does not guarantee that hook execution failures block
  tools. Untrusted or changed hooks can also be skipped until reviewed.

- **Qwen hook failures can leave an action unsupervised.** The pinned Qwen
  release treats some hook startup errors, timeouts, and invalid output as
  nonblocking. A functioning bridge can return a denial, but that does not
  guarantee an action is blocked when the bridge itself fails. Valid structured
  denial output and the host's blocking exit status are separate from these
  nonblocking failure paths.

None of these change the core contract: within the boundary Pitot mediates,
every pending action receives exactly one terminal resolution. **Pitot reports.
Your controller decides.**

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
├── adapters/        built-in coding-agent hook boundaries
├── sensor/          normalization and observation pipeline
├── bridge/          controller routing and response transport
├── projection/      full, sha256, and omit policies
├── config/          strict Consumer and Controller declarations
├── runtime/         authenticated ingress and local process delivery
├── conformance/     language-neutral fixtures and negative controls
├── examples/        local approval and Consumer examples
└── cmd/pitot/        reference Go executable
```

## Design principles

1. **Report before interpretation.** Preserve what the host actually supplied.
2. **Make capability structural.** Consumer output is observation-only; only the
   configured Controller response pipe participates in decisions.
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
positive and negative fixture suite. It must also meet the host admission
criteria above — a boundary that can only abort on denial, rather than return
the outcome to the model, is not yet supervisable.

See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup, the two adapter
transport classes (one-shot hook and stateful ACP-style transport), and the
cross-platform verification every adapter must pass.

## License

Apache-2.0
