# Host setup and boundaries

## Supported hosts

Every host below normalizes its native blocking boundary to a `shell` action and
passes Pitot's language-neutral decoder conformance suite. The E2E column marks
adapters exercised by the [cross-platform checks](https://github.com/operatorstack/pitot/actions/workflows/pitot-e2e.yml);
the Platforms column names the finite test targets, not a universal platform or
host-version guarantee. Kimi
additionally has an in-repo, no-model test that asserts the full allow **and**
deny control path end to end.

| Host               | Blocking boundary                    | Hook wiring                                      | Platforms                                         | Verified in this repo                   |
| ------------------ | ------------------------------------ | ------------------------------------------------ | ------------------------------------------------- | --------------------------------------- |
| Codex              | `PreToolUse`                         | bridge (`integrations/codex`)                    | Ubuntu · macOS · Windows                          | decoder + E2E                           |
| Qwen Code          | `PreToolUse`                         | bridge (`integrations/qwen`)                     | Ubuntu · macOS · Windows                          | decoder + E2E                           |
| DeepSeek Harness   | `tools/pre-execute` / `bash`, `pwsh` | native middleware ([setup](deepseek-adapter.md)) | Ubuntu · macOS · Windows                          | decoder + E2E                           |
| Claude             | `PreToolUse`                         | native settings hook                             | Ubuntu · macOS · Windows                          | decoder + E2E                           |
| Cursor             | `beforeShellExecution`               | bridge (`integrations/cursor`)                   | Ubuntu · macOS · native Windows with PowerShell 7 | decoder + E2E                           |
| Devin              | `session/request_permission` (ACP)   | stateful ACP transport                           | Ubuntu · macOS · Windows                          | decoder + E2E                           |
| GitHub Copilot CLI | `PreToolUse`                         | bridge (`integrations/copilot`)                  | Ubuntu · macOS · Windows                          | decoder + E2E                           |
| Gemini             | `BeforeTool`                         | bridge (`integrations/gemini`)                   | Ubuntu · macOS · Windows                          | decoder + E2E                           |
| Kimi Code          | `PreToolUse` / Bash                  | native `config.toml`                             | Ubuntu · macOS · Windows                          | decoder + E2E + allow/deny control test |
| OpenCode           | `PreToolUse`                         | bridge (`integrations/opencode`)                 | Ubuntu · macOS · Windows                          | decoder + E2E                           |
| Pi                 | `tool_call`                          | extension (`integrations/pi`)                    | Ubuntu · macOS · Windows                          | decoder + E2E                           |

"Decoder" means Pitot correctly normalizes that host's payload into the stable
event envelope. It does not claim Pitot judges whether any command is safe — that
decision belongs to your Controller. Hosts run natively on Windows. Cursor's
Windows hook bridge requires PowerShell 7.

## Shared setup and diagnostics

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

With `PITOT_RUNTIME` unset, command hooks remain observe-only. DeepSeek native
middleware instead requires a live Controller reply. With it set, doctor checks
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

## Codex and Claude

```bash
pitot init --host claude
pitot init --host codex
```

Both init commands wire a `PreToolUse` hook (matcher `Bash`) whose command runs the repo
shim: `"$CLAUDE_PROJECT_DIR"/.pitot/bin/pitot hook claude` (Codex uses the
repo-relative equivalent plus a PowerShell `commandWindows`). Exit `0` allows;
exit `2` blocks with the Controller's reason.

For Codex, the project-local `.codex/` configuration must be trusted. Open
`/hooks` in the Codex CLI to review and trust the exact hook definition. New or
changed non-managed hooks are skipped until trusted; writing the hook file
alone does not activate supervision. See [Codex hook trust](https://learn.chatgpt.com/docs/hooks#review-and-trust-hooks).

## Qwen Code

On macOS or Linux, copy `integrations/qwen/PreToolUse` to a stable absolute
path and make it executable (`chmod +x /path/to/PreToolUse`). This bridge needs
Bash, `python3`, and `pitot` on the Qwen process PATH; an explicit Pitot executable
can also be passed as its single argument. Add this command hook to
`~/.qwen/settings.json`:

```json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "^run_shell_command$",
        "hooks": [{ "type": "command", "command": "/path/to/PreToolUse" }]
      }
    ]
  }
}
```

On Windows, copy `integrations/qwen/PreToolUse.cjs` to a stable path. Use Node
explicitly, with the bridge path and the real `pitot.exe` path as arguments.
For example, replace the placeholders below with your installed paths in
`%USERPROFILE%\.qwen\settings.json`:

```json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "^run_shell_command$",
        "hooks": [
          {
            "type": "command",
            "command": "node.exe \"C:/Tools/Pitot/PreToolUse.cjs\" \"C:/Tools/Pitot/pitot.exe\""
          }
        ]
      }
    ]
  }
}
```

The Qwen process must resolve `node.exe` on PATH. This example uses ordinary
native Windows shell handling; Git Bash/MSYS environments need separate native
inspection. Run `pitot doctor --host qwen` after configuring either bridge, and
review any conflicting or disabled hooks before relying on the setup.

The matcher uses Qwen's runtime shell tool ID. Compatibility aliases such as
`Bash` match exactly; anchoring that alias as `^Bash$` does not select the shell
tool.

Qwen sends the native JSON payload on standard input. The bridge returns its
native structured allow or deny decision and preserves the Controller reason.
See the official
[Qwen Code hooks guide](https://qwenlm.github.io/qwen-code-docs/en/users/features/hooks/).

## DeepSeek Harness

Use the official supported Harness release and the native middleware described
in [DeepSeek setup](deepseek-adapter.md). Register a shell Controller under
`.pitot/conf.d/` separately from host wiring. For example, from the project root:

```sh
pitot init --template shell-policy --language go --dir shell-policy
pitot init --host deepseek
pitot doctor --host deepseek
```

Start a single prompt with `pitot dev --host deepseek --prompt "YOUR PROMPT" -- dsh`.
Ordinary prompts use the configured provider; the [local replay example](../examples/supervisory-control/README.md)
is the no-real-model-call demonstration. DeepSeek's native `bash` and `pwsh`
tools require an explicit matching Controller reply. Other tools are not
mediated by this integration. Existing Harness sandbox constraints still apply.

## Kimi Code

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
Controller denies the action. A normal Kimi prompt can contact its configured model provider. The following
is usage syntax, not an offline enforcement test:

```bash
kimi -p "Show the repository status"
```

See the [Kimi Code documentation](https://www.kimi.com/code/docs/en/) for CLI
authentication, configuration, and hook behavior.

## GitHub Copilot CLI

Copy `integrations/copilot/PreToolUse` to a stable executable path (or use
`PreToolUse.ps1` on Windows), then add the following Claude-compatible hook to
`~/.copilot/settings.json`:

```json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash",
        "hooks": [{ "type": "command", "command": "/path/to/PreToolUse" }]
      }
    ]
  }
}
```

The bridge keeps the blocking payload on Pitot's standard `hook_event_name`
and `tool_input.command` boundary and returns Copilot's structured native deny
reason when a Controller rejects the command. See the official
[Copilot CLI hooks reference](https://docs.github.com/en/copilot/reference/hooks-reference).

## Cursor

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

## Gemini

```bash
pitot init --host gemini
```

Writes the committed bridge `.pitot/bin/hooks/gemini-BeforeTool` and registers
it as a `BeforeTool` command hook for `run_shell_command`. The bridge
translates Pitot rejection into Gemini's structured `decision: "deny"` and
`reason` response so the model receives the blocked tool result. See the
[Gemini CLI hooks reference](https://geminicli.com/docs/hooks/reference/).

## Pi

Copy `integrations/pi/pitot.ts` into `~/.pi/agent/extensions/pitot.ts` (or the
repository-local `.pi/extensions/` directory). The extension converts Pi's
blocking `tool_call` event into Pitot's stable envelope and returns Pi's native
`block` response when Pitot rejects the request. See the official
[Pi extensions documentation](https://pi.dev/docs/latest/extensions).

## Devin

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
[docs/devin-adapter-research.md](devin-adapter-research.md) for why ACP,
not hooks, is the supervised boundary.

## OpenCode

The supplied [OpenCode plugin](../integrations/opencode/server.ts) forwards the
native shell `execute.before` boundary to `pitot hook opencode`. Configure the
plugin in OpenCode's settings; Pitot does not install or edit user-level plugin
settings. The checked harness shows the adapter wiring in
[real_agent_driver.py](../tests/real_agent_driver.py). A plugin error becomes the
native rejected tool result. This is shell supervision, not coverage of every tool.

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

Within an admitted runtime request, Pitot correlates a terminal resolution.
Host hook startup, trust, bypass and transport behavior remain separate boundaries.
A successful allow/deny check does not prove every possible host action is supervised.

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
[docs/devin-adapter-research.md](devin-adapter-research.md).
