# Devin CLI adapter research

Research date: 2026-07-31.

## Question

Pitot support requires a synchronous shell-control boundary that carries a
proposed command to a Controller, applies allow or deny before execution, and
lets the agent continue from the resulting tool outcome. The investigation
tested Devin's hook and ACP surfaces separately rather than assuming every host
must use Pitot's existing one-shot hook transport.

## Released binary

The stable manifest at
<https://static.devin.ai/cli/current/manifest.json> selected version
`3000.3.22`. The pinned `aarch64-apple-darwin` archive had the published
SHA-256 `eefe1f3c970c06d58b5ec2612ac5b36cd350635e0a2e59eb4d4f5baf43427662`.
The extracted binary reported `devin 3000.3.22 (d5152ff5)`.

## Surface 1: lifecycle hooks

Devin documents project hooks in `.devin/hooks.v1.json`. `PreToolUse` runs
before a tool executes, shell calls use tool name `exec`, and the command is
available as `tool_input.command`. A command hook can block with exit `2` or a
JSON block decision.

A deterministic Connect/protobuf endpoint drove the pinned binary through a
real `exec` call in noninteractive `--print` mode. Pitot's hook, Consumer, and
Controller all observed the request; denial prevented the canary command from
executing. Devin then ended the turn instead of returning the denied tool
outcome to the model. The documented JSON block response behaved the same way.

`PermissionRequest` was also tested in normal noninteractive mode. Devin
rejected the confirmation-required tool call itself and did not invoke the
configured permission hook. Therefore neither hook variant completes Pitot's
causal loop in `--print` mode.

Hooks remain unsuitable for a Devin adapter. The candidate `.devin` wiring is
not shipped.

## Surface 2: Agent Client Protocol

`devin acp` runs Devin as an ACP server over stdio JSON-RPC. ACP is
bidirectional: the agent sends `session/request_permission`, and the client
selects a typed permission option. Official ACP clients may enforce policy by
automatically selecting an allow or reject option.

The pinned Devin binary was launched through the official Python ACP SDK
(`agent-client-protocol`, protocol version 1) against the same deterministic
model boundary. The model proposed:

```text
touch /tmp/PITOT_ACP_DENY_CANARY
```

The observed sequence was:

1. Devin emitted a `tool_call` update with tool-call ID `pitot_acp_deny`,
   kind `execute`, inference tool `exec`, and direct
   `rawInput.command`.
2. Devin sent `session/request_permission` for the same tool-call ID with
   `allow_once` and `reject_once` options.
3. The client selected `reject_once`.
4. The canary file remained absent.
5. Devin emitted a `tool_call_update` with status `failed`,
   `cognition.ai/rejected: true`, and the text
   `Tool execution was rejected: User rejected this tool call`.
6. Devin made the next main model request. That request contained the original
   tool-call ID and the rejection terms `reject`, `denied`, and `permission`.
7. Devin emitted the model's final response and ended the prompt with
   `stopReason: end_turn`.

The content-safe receipt is
[`evidence/devin-acp-3000.3.22.json`](evidence/devin-acp-3000.3.22.json).

This is the missing causal behavior. The stable changelog independently notes
that skipping a tool call through an ACP client no longer stops the agent and
that the model sees the rejection and can try an alternative.

Primary sources:

- <https://docs.devin.ai/cli/reference/commands>
- <https://docs.devin.ai/cli/changelog/stable>
- <https://agentclientprotocol.com/get-started/architecture>
- <https://agentclientprotocol.com/protocol/tool-calls>
- <https://agentclientprotocol.com/libraries/python>

## SDK and protocol implications

ACP's official SDKs are Rust, Python, TypeScript, Kotlin, and Java. There is no
official Go SDK. Pitot does not need to change its public event or control
envelopes: the direct command still normalizes to `action.requested`, and the
Controller still returns `allow` or `deny`.

The shipped implementation is a stateful Devin transport adapter:

- launch `devin acp` and perform ACP initialization plus session creation;
- correlate each `tool_call` update's `rawInput.command` by `toolCallId`;
- on `session/request_permission`, deliver the normalized shell event to the
  existing Pitot runtime;
- map `allow` to ACP `allow_once` and `deny` to `reject_once`;
- preserve ACP session output and lifecycle until `session/prompt` completes.

The adapter must fail closed when the tool-call ID is unknown, the command was
not directly observed, a required permission option is absent, or the ACP
protocol version is unsupported. It must never select persistent permission
options such as `allow_always` or bypass mode.

## Revised verdict

Devin is supportable through ACP, not through lifecycle hooks. This does not
require weakening Pitot's supervised causal-loop requirement or changing
protocol version 1. It does require broadening the adapter architecture from
only one-shot host hooks to include a stateful host transport.

The two-slice ZCA projection separates:

1. **Control semantics:** Devin ACP satisfies pre-execution allow/deny and
   post-denial continuation.
2. **Integration transport:** Pitot uses a Devin-specific ACP client boundary,
   registered as `acp`, while hook-oriented Go APIs remain compatibility
   aliases.

The immediate value is that Pitot supports Devin without treating its existing
hook shape as the protocol itself. The pinned production adapter is supervised
with real-agent allow/deny, Consumer/Controller, canary, continuation, and
incompatible-response checks.
