# Supervisory control

A real Codex CLI delegates a delivery task to a real Qwen or DeepSeek CLI.
Both agents cross Pitot's action boundary. One Python application owns the
supervisor, delivery state and external tester. A loopback LLM replay server
supplies the agents' responses, so no model account or real model call is needed.

```text
Codex <-> Pitot <-> Python supervisor <-> Pitot <-> Qwen or DeepSeek
                         |
                  artifact + tester
```

## System and specification

The toy delivery plant has four states: `draft`, `built`, `verified`, `released`.
`released` is its legitimate objective. The controllable actions are delegation,
build and release. The application tester produces uncontrollable test results
and invalidation events: neither coding agent can approve, reject or forge them
through the action interface.

| Action or event               | Effect                                                                       |
| ----------------------------- | ---------------------------------------------------------------------------- |
| Codex delegates               | Starts the selected worker; available once in `draft`                        |
| Worker builds                 | Writes the toy artifact, advances its revision and enters `built`            |
| Tester passes                 | Enters `verified` for the tested artifact revision                           |
| Environment invalidates tests | Withdraws verification and returns to `built`                                |
| Worker releases               | Allowed only in `verified`; copies the tested artifact and enters `released` |

The specification forbids releasing an unverified or changed artifact. Permission
does not mean completion: the plant records a build only after the write succeeds.
A release consumes a short-lived admission reservation and rechecks the current
state, revision and artifact under the same lock. An intervening invalidation
revokes permission. Unknown commands and wrong agent roles are denied.

This illustrates the controllable/uncontrollable distinction in discrete-event
supervisory control. The supervisor constrains proposed behavior; it does not
choose the agent's next action. The finite toy has a recovery path from each
unfinished state, but makes no guarantee that an arbitrary agent will follow it
or that an external tester will eventually pass.

## Run

Requires macOS or Linux, Python 3.11+, Node 24+, a checkout of this repository,
and the real CLIs. Tested CLI versions: Codex `0.162.1`, Qwen `0.25.1`, and the
official DeepSeek Harness `@deepseek-ai/dsh` `0.2.0-rc.2`.

From the repository root, prepare an optional local tools directory:

```sh
go build -o /tmp/pitot-demo-tools/pitot ./cmd/pitot
npm install --prefix /tmp/pitot-demo-tools --no-audit --no-fund \
  @openai/codex@0.162.1 @qwen-code/qwen-code@0.25.1 @deepseek-ai/dsh@0.2.0-rc.2
export PATH="/tmp/pitot-demo-tools:/tmp/pitot-demo-tools/node_modules/.bin:$PATH"
python3 examples/supervisory-control/demo.py run --worker qwen
python3 examples/supervisory-control/demo.py run --worker deepseek
```

Or use existing installations with `--pitot PATH`, `--codex PATH`, `--qwen PATH`
and `--deepseek PATH`. No CLI is replaced by a stub. Each invocation creates its
own temporary home directories, hook configuration, project and runtime. It uses
fake credentials with local provider endpoints and cleans up its processes and
temporary files. `--keep-logs NEW_DIRECTORY` preserves the owned transcripts,
profiles and observations for inspection. The CLI approval bypass flags apply
only inside this owned toy project; the shared Pitot controller still checks
every proposed shell command.

For fast application boundary checks without any CLIs or network:

```sh
python3 -m unittest discover -s examples/supervisory-control -v
```

## What to observe

1. Codex proposes delegation; Pitot passes the shell action to the supervisor.
2. The worker first proposes release. The supervisor denies it in `draft`.
3. The native tool result carries that denial back to the replay, which proposes
   a build in response.
4. The external tester verifies the completed artifact. On the first build, a
   reproducible environment change immediately invalidates that result.
5. The same release is denied again. The worker responds by rebuilding; this
   revision passes the tester and stays verified.
6. Release is now allowed and completes. Codex receives the worker's completion.

The printed events show the actual admissions, completed operations and tester
events. The final `PASS` requires two release denials and a completed release.
The model replay is deliberately deterministic and checks correlated native tool
results; the supervisor decides independently from application state. This is an
interface/enforcement demonstration, not a measure of model quality.

## Shared machinery

- `supervisor.py`: the application plant, admissibility rules, reservations and
  revision-bound test observations. It knows no provider protocol.
- `demo.py`: a short Python SDK Controller, the loopback application gateway,
  lifecycle management and thin native CLI profiles. The Controller returns
  before the delegated worker starts, avoiding nested-hook deadlock.
- `replay.py`: local Responses for Codex and chat completions for both workers.
  It proposes actions based on actual feedback and has no permission API.

Both workers use the same runtime descriptor, Controller and supervisor rules.
DeepSeek uses Pitot's existing controlled ACP connection. Pitot transports the
native action and enforces the correlated allow/deny reply; the application owns
all supervisory state and completed transitions.

The example runs one delivery session with one worker at a time. It is not an
orchestration framework, a sandbox against hostile local processes, or a
post-release revocation system. Keep the common state owner and completion/event
boundaries when adapting it to a real application.
