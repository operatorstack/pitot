# Pitot

Put your own program at a coding agent's action boundary.

Pitot connects an agent to two kinds of application code:

| Role | What it does |
| --- | --- |
| Consumer | Receives projected events for observation and reporting. |
| Controller | Receives a registered request and returns a correlated allow or deny decision. |

The shipped Codex, Qwen and DeepSeek integrations supervise shell tools. They do
not mediate every file edit or tool call. Your application can also send explicit
requests for its own action kinds. The runtime delivers `action.requested` events;
it does not collect model token usage.

Pitot reports. Your Controller decides.

<!-- pitot-adapter-supervisor:start -->
[Agent checks on main](https://github.com/operatorstack/pitot/actions/workflows/pitot-e2e.yml?query=branch%3Amain)

Every supervised adapter must pass a binary-observed prompt → real action boundary → projected Consumer → Controller allow/deny → tool-result loop on Ubuntu, macOS, and Windows.

Supervised adapters: Claude · Cursor · Codex · Devin · DeepSeek Harness · GitHub Copilot CLI · Gemini · Kimi Code · OpenCode · Pi · Qwen Code

Supervised runtime capabilities: action control · Consumer delivery · explicit request
<!-- pitot-adapter-supervisor:end -->

## Try the supervisory-control example

[The local delivery example](examples/supervisory-control/README.md) connects
Codex to a Qwen or DeepSeek worker through one shared Python supervisor. A worker
can build an artifact, but release is allowed only for its current tested revision.
A failed or invalidated test changes what the worker may do next.

The replay demonstration runs on macOS and Linux. It uses real CLI processes
with a loopback replay provider and fake
credentials. It makes no real model calls. Its README explains the state,
feedback, verification and limits, and gives the pinned tools and run commands.
For application checks without those CLIs or network:

```sh
python3 -m unittest discover -s examples/supervisory-control -v
```

## Install and register a Controller

Use a release binary, or build the CLI from source:

```sh
go install github.com/operatorstack/pitot/cmd/pitot@latest
pitot init --template shell-policy --language go --dir shell-policy
```

This creates a runnable Controller and registers it under `.pitot/conf.d/`.
The sample allows shell commands except those containing `PITOT_DENY_ME`.
It is a demonstration policy, not a general shell-security control.

`pitot init` also writes a repository version pin and launch shims. Commit those
files so clones use the same release. First use can download that release;
cache-only and offline failures are described in [runtime setup](docs/runtime.md).

## Connect Codex, Qwen or DeepSeek

Host setup and a registered Controller are separate steps.

| Host | Setup | What still needs checking |
| --- | --- | --- |
| Codex | `pitot init --host codex` | Review and trust the exact hook in Codex `/hooks`. Writing the file does not activate it. |
| Qwen | Configure the supplied Qwen bridge in the user hook settings. | Use its native `run_shell_command` matcher and inspect conflicting hooks. |
| DeepSeek | `pitot init --host deepseek` | Use the supported official Harness release and a shell Controller. |

Run `pitot doctor --host codex`, `pitot doctor --host qwen`, or
`pitot doctor --host deepseek` to inspect setup. Doctor's static checks do not
prove live enforcement or grant hook trust. Plain doctor does not change settings;
`--fix` performs only supported wiring changes, then checks the result.

[Host setup](docs/hosts.md) contains the commands, platform details and trust
requirements. [DeepSeek setup](docs/deepseek-adapter.md) describes its native
middleware and single-prompt interface.

Once wired, `pitot dev --host HOST -- COMMAND ARGS` starts the declared runtime,
waits for readiness and launches the agent with its runtime descriptor. DeepSeek
also requires `--prompt`. Ordinary agent prompts can contact their configured
model provider; use the replay example above for a no-model-call demonstration.

On Windows, standard npm Node launchers for Codex and Qwen are recognized without
batch evaluation; custom wrappers require an explicit Node entrypoint. The
explicit `-- COMMAND ARGS` form preserves argument boundaries. `--exec` splits
on whitespace. The generic subprocess launcher bounds captured-output waiting; DeepSeek and
Devin use separate ACP lifecycle handling. Pitot does not promise to terminate
arbitrary descendants. See [runtime setup](docs/runtime.md).

## What the boundary guarantees

A Controller response must match the pending action and registered Controller.
Consumers cannot resolve requests. Content is projected before delivery, using
`full`, `sha256` or `omit`. SHA-256 permits comparison against guessed content;
it does not conceal low-entropy secrets.

Only the host's wired boundary is mediated. Codex and Qwen can continue an action
when a hook fails or is skipped. DeepSeek requires an explicit matching live
Controller reply for its native shell tools. These programs run as the current
user: Pitot does not sandbox their filesystem, environment or network access.

Your Controller defines what approval, correctness or completion means. Pitot
validates and transports its answer; it does not establish those meanings.

## Reference

- [Host setup and all supported adapters](docs/hosts.md): Codex, Qwen, DeepSeek,
  Claude, Cursor, Devin, Copilot, Gemini, Kimi, OpenCode and Pi.
- [Runtime and configuration](docs/runtime.md): tenant fragments, readiness,
  manual runtime, explicit requests, fallback, privacy and lifecycle limits.
- [Wire contract](docs/protocol.md): schemas, framing and tested SDK constraints.
- [Python SDK](sdk/python/README.md), [TypeScript SDK](sdk/typescript/README.md)
  and [Rust SDK](sdk/rust/README.md).
- [Contributing](CONTRIBUTING.md): development checks and adapter admission.

Apache-2.0. See [LICENSE](LICENSE).
