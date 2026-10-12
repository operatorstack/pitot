#!/usr/bin/env node
// Pinned Cursor Agent endpoint fixture. This is deliberately separate from the
// JSON dialect handler because Cursor's --endpoint transport upgrades Run to
// cleartext HTTP/2 and protobuf.

import http2 from "node:http2";
import http from "node:http";
import net from "node:net";
import fs from "node:fs";
import path from "node:path";
import { createHash } from "node:crypto";

function parseArgs(argv) {
  const values = {};
  for (let index = 2; index < argv.length; index += 2) {
    values[argv[index].replace(/^--/, "")] = argv[index + 1];
  }
  return values;
}

function varint(value) {
  const bytes = [];
  while (value > 0x7f) {
    bytes.push((value & 0x7f) | 0x80);
    value >>= 7;
  }
  bytes.push(value);
  return Buffer.from(bytes);
}

function fieldBytes(field, value) {
  const payload = Buffer.isBuffer(value) ? value : Buffer.from(value);
  return Buffer.concat([
    varint((field << 3) | 2),
    varint(payload.length),
    payload,
  ]);
}

function fieldVarint(field, value) {
  return Buffer.concat([varint(field << 3), varint(value)]);
}

function connectEnvelope(message, flags = 0) {
  const header = Buffer.alloc(5);
  header[0] = flags;
  header.writeUInt32BE(message.length, 1);
  return Buffer.concat([header, message]);
}

function envelopeShapes(body) {
  const shapes = [];
  let offset = 0;
  while (offset + 5 <= body.length) {
    const flags = body[offset];
    const length = body.readUInt32BE(offset + 1);
    if (offset + 5 + length > body.length) break;
    const message = body.subarray(offset + 5, offset + 5 + length);
    const printable = (message.toString("latin1").match(/[ -~]{4,}/g) || [])
      .map((value) => value.replaceAll(args.nonce, "<nonce>"))
      .filter((value) =>
        /pitot|canary|hook|command|error|fail|denied|not found|nonce/i.test(
          value,
        ),
      )
      .slice(0, 12);
    shapes.push({
      flags,
      length,
      nonce_present: message.includes(Buffer.from(args.nonce)),
      canary_result_present: message.includes(
        Buffer.from(`PITOT_CANARY_RESULT ${args.nonce}`),
      ),
      printable,
      client_message_kind: clientMessageKind(message),
    });
    offset += 5 + length;
  }
  return shapes;
}

function shellExecution(command, phase = "allow", argv = [command]) {
  const [executable, ...arguments_] = argv;
  // ShellCommandParsingResult.ExecutableCommandArg { type, value }
  // ShellCommandParsingResult.ExecutableCommand { name, args, full_text }
  const parsedCommand = Buffer.concat([
    fieldBytes(1, executable),
    ...arguments_.map((argument) =>
      fieldBytes(
        2,
        Buffer.concat([fieldBytes(1, "word"), fieldBytes(2, argument)]),
      ),
    ),
    fieldBytes(3, command),
  ]);
  // ShellCommandParsingResult { executable_commands: 2 }
  const parsingResult = fieldBytes(2, parsedCommand);
  // agent.v1.ShellArgs { command: 1, tool_call_id: 4 }
  const shellArgs = Buffer.concat([
    fieldBytes(1, command),
    fieldBytes(4, `pitot-tool-${phase}`),
    fieldBytes(5, command),
    fieldBytes(8, parsingResult),
    fieldVarint(12, 1),
  ]);
  // agent.v1.ExecServerMessage { id: 1, exec_id: 15, shell_args: 2 }
  const execution = Buffer.concat([
    fieldVarint(1, phase === "allow" ? 1 : 2),
    fieldBytes(15, `pitot-exec-${phase}`),
    fieldBytes(2, shellArgs),
  ]);
  // agent.v1.AgentServerMessage { exec_server_message: 2 }
  return fieldBytes(2, execution);
}

// Decode only the client/result fields needed to prove this fixture's two
// actions. Unknown protobuf fields are skipped; ambiguous known fields refuse.
function protobufFields(message) {
  let offset = 0;
  const fields = [];
  function readVarint() {
    let value = 0n;
    for (let index = 0; index < 10; index += 1) {
      if (offset >= message.length) throw new Error("truncated varint");
      const byte = message[offset++];
      if (index === 9 && byte > 1) throw new Error("oversized varint");
      value |= BigInt(byte & 127) << BigInt(index * 7);
      if (!(byte & 128)) return value;
    }
    throw new Error("unterminated varint");
  }
  while (offset < message.length) {
    if (fields.length >= 4096) throw new Error("too many protobuf fields");
    const key = readVarint();
    const number = Number(key >> 3n);
    const wire = Number(key & 7n);
    if (number < 1 || number > 0x1fffffff) throw new Error("invalid field");
    let value;
    if (wire === 0) value = readVarint();
    else {
      const size =
        wire === 2 ? readVarint() : wire === 1 ? 8n : wire === 5 ? 4n : -1n;
      if (size < 0n || size > BigInt(message.length - offset))
        throw new Error("invalid field length or wire type");
      value = message.subarray(offset, offset + Number(size));
      offset += Number(size);
    }
    fields.push({ number, wire, value });
  }
  return fields;
}

function singleField(fields, number, wire) {
  const values = fields.filter((field) => field.number === number);
  if (values.length > 1 || (values.length === 1 && values[0].wire !== wire))
    throw new Error("ambiguous result field");
  return values[0]?.value;
}

function stringField(fields, number) {
  const value = singleField(fields, number, 2);
  return value === undefined
    ? ""
    : new TextDecoder("utf-8", { fatal: true }).decode(value);
}

function matchesShellResult(message, command, nonce, phase) {
  try {
    const top = protobufFields(message);
    // AgentClientMessage is a oneof. A marker in run_request or heartbeat
    // cannot stand in for an execution result.
    if (
      top.filter((field) => field.number >= 1 && field.number <= 8).length !== 1
    )
      return false;
    const executionBytes = singleField(top, 2, 2);
    if (!executionBytes) return false;
    const execution = protobufFields(executionBytes);
    if (singleField(execution, 1, 0) !== BigInt(phase === "allow" ? 1 : 2))
      return false;
    const executionId = stringField(execution, 15);
    if (executionId && executionId !== `pitot-exec-${phase}`) return false;
    const resultNumbers = new Set([
      2, 3, 4, 5, 7, 8, 9, 10, 11, 14, 16, 17, 18, 20, 21, 22, 23, 27, 28, 29,
      30, 31, 36, 37, 38, 40, 41, 42, 43, 44, 46, 47, 48, 49, 50, 51, 52, 53,
      54, 55, 56,
    ]);
    if (
      execution.filter((field) => resultNumbers.has(field.number)).length !== 1
    )
      return false;
    const shell = singleField(execution, 2, 2);
    if (!shell) return false;
    const results = protobufFields(shell);
    if (
      results.filter((field) => [1, 2, 3, 4, 5, 7].includes(field.number))
        .length !== 1
    )
      return false;
    const result = singleField(results, phase === "allow" ? 1 : 4, 2);
    if (!result) return false;
    const details = protobufFields(result);
    if (stringField(details, 1) !== command) return false;
    if (phase === "allow") {
      return (
        (singleField(details, 3, 0) ?? 0n) === 0n &&
        stringField(details, 5)
          .split(/\r?\n/)
          .includes(`PITOT_CANARY_RESULT PITOT_ALLOW ${nonce}`)
      );
    }
    const reason = stringField(details, 3);
    return (
      reason.includes(`PITOT_CONTROLLER_DENY ${nonce}`) &&
      reason.includes("blocked by a hook")
    );
  } catch {
    return false;
  }
}

function shellObservation(message, commands, nonce) {
  const digest = (value) => createHash("sha256").update(value).digest("hex");
  const frame = { frame_length: message.length, frame_sha256: digest(message) };
  try {
    const top = protobufFields(message);
    const executionBytes = singleField(top, 2, 2);
    if (!executionBytes) return null;
    if (top.filter((f) => f.number >= 1 && f.number <= 8).length !== 1)
      throw new Error("ambiguous execution");
    const execution = protobufFields(executionBytes);
    const shell = singleField(execution, 2, 2);
    if (!shell) return null;
    const resultNumbers = new Set([
      2, 3, 4, 5, 7, 8, 9, 10, 11, 14, 16, 17, 18, 20, 21, 22, 23, 27, 28, 29,
      30, 31, 36, 37, 38, 40, 41, 42, 43, 44, 46, 47, 48, 49, 50, 51, 52, 53,
      54, 55, 56,
    ]);
    if (execution.filter((f) => resultNumbers.has(f.number)).length !== 1)
      throw new Error("ambiguous result");
    const id = singleField(execution, 1, 0) ?? 0n;
    if (id > 0xffffffffn) throw new Error("invalid uint32");
    const echo = stringField(execution, 15);
    const results = protobufFields(shell);
    const variants = {
      1: "success",
      2: "failure",
      3: "timeout",
      4: "rejected",
      5: "spawn_error",
      7: "permission_denied",
    };
    const selected = results.filter((f) => Object.hasOwn(variants, f.number));
    if (selected.length !== 1 || selected[0].wire !== 2)
      throw new Error("ambiguous shell variant");
    const tag = selected[0].number;
    const details = protobufFields(selected[0].value);
    const command = stringField(details, 1);
    stringField(details, 2);
    const phase = id === 1n ? "allow" : id === 2n ? "deny" : null;
    const exactCommand = phase !== null && command === commands[phase];
    const observation = {
      ...frame,
      native_id: Number(id),
      variant: variants[tag],
      issued_phase_id_match: phase !== null,
      command_sha256: digest(Buffer.from(command, "utf8")),
      issued_exact_command_match: exactCommand,
      nonce_bound: exactCommand && command.includes(nonce),
      exec_id_present: echo !== "",
      exec_id_sha256: digest(Buffer.from(echo, "utf8")),
      exec_id_match:
        echo === "" || (phase !== null && echo === `pitot-exec-${phase}`),
    };
    const retainTextShape = (name, field) => {
      const value = stringField(details, field);
      const bytes = Buffer.from(value, "utf8");
      observation[`${name}_bytes`] = bytes.length;
      observation[`${name}_sha256`] = digest(bytes);
      return value;
    };
    let reason = "";
    if (tag === 1 || tag === 2) {
      observation.exit_code = Number(
        BigInt.asIntN(32, singleField(details, 3, 0) ?? 0n),
      );
      stringField(details, 4);
      const stdout = retainTextShape("stdout", 5);
      reason = retainTextShape("stderr", 6);
      observation.allow_canary_marker = stdout
        .split(/\r?\n/)
        .includes(`PITOT_CANARY_RESULT PITOT_ALLOW ${nonce}`);
    } else if (tag === 3) {
      observation.timeout_ms = Number(
        BigInt.asIntN(32, singleField(details, 3, 0) ?? 0n),
      );
    } else {
      reason = retainTextShape("reason", 3);
      if (tag === 4 || tag === 7) {
        const readonly = singleField(details, 4, 0) ?? 0n;
        if (![0n, 1n].includes(readonly)) throw new Error("invalid bool");
        observation.is_readonly = readonly === 1n;
      }
    }
    observation.error_class =
      reason === "Parsing result is required"
        ? "missing_parsing_result"
        : reason === "Command blocked by permissions configuration"
          ? "permissions_configuration_block"
          : reason ===
              "Neither 'pwsh' (PowerShell Core) nor 'powershell' (Windows PowerShell) found in PATH"
            ? "shell_selection_unavailable"
            : "other";
    return observation;
  } catch {
    return {
      ...frame,
      variant: "malformed",
      error_class: "malformed_native_result",
    };
  }
}

function connectReader() {
  let pending = Buffer.alloc(0);
  let refused = false;
  return (chunk) => {
    if (refused) return [];
    pending = Buffer.concat([pending, chunk]);
    const messages = [];
    while (pending.length >= 5) {
      const flags = pending[0];
      const length = pending.readUInt32BE(1);
      if (![0, 2].includes(flags) || length > 16 * 1024 * 1024) {
        refused = true;
        return [];
      }
      if (pending.length < length + 5) break;
      if (flags === 2) {
        refused = true;
        pending = Buffer.alloc(0);
        return messages;
      }
      messages.push(pending.subarray(5, length + 5));
      pending = pending.subarray(length + 5);
    }
    return messages;
  };
}

function clientMessageKind(message) {
  try {
    const kinds = {
      1: "run_request",
      2: "exec_client_message",
      3: "kv_client_message",
      4: "conversation_action",
      5: "exec_client_control_message",
      6: "interaction_response",
      7: "client_heartbeat",
      8: "prewarm_request",
    };
    const known = protobufFields(message).filter((f) =>
      Object.hasOwn(kinds, f.number),
    );
    if (known.length === 0) return "unknown";
    return known.length === 1 && known[0].wire === 2
      ? kinds[known[0].number]
      : "malformed";
  } catch {
    return "malformed";
  }
}

function controlObservation(message, nonce) {
  const digest = (value) => createHash("sha256").update(value).digest("hex");
  const frame = { frame_length: message.length, frame_sha256: digest(message) };
  try {
    const top = protobufFields(message);
    const control = singleField(top, 5, 2);
    if (!control) return null;
    if (clientMessageKind(message) !== "exec_client_control_message")
      throw new Error("ambiguous control");
    const variants = { 1: "stream_close", 2: "throw", 3: "heartbeat" };
    const known = protobufFields(control).filter((f) =>
      Object.hasOwn(variants, f.number),
    );
    if (known.length !== 1 || known[0].wire !== 2)
      throw new Error("ambiguous control variant");
    const fields = protobufFields(known[0].value);
    const id = singleField(fields, 1, 0) ?? 0n;
    if (id > 0xffffffffn) throw new Error("invalid uint32");
    const result = {
      ...frame,
      variant: variants[known[0].number],
      native_id: Number(id),
      issued_phase_id_match: [1n, 2n].includes(id),
      nonce_present: message.includes(Buffer.from(nonce)),
    };
    if (known[0].number === 2) {
      let error = "";
      for (const [name, number] of [
        ["error", 2],
        ["stack_trace", 3],
        ["error_code", 4],
      ]) {
        const value = stringField(fields, number);
        const bytes = Buffer.from(value, "utf8");
        result[`${name}_bytes`] = bytes.length;
        result[`${name}_sha256`] = digest(bytes);
        if (number === 2) error = value;
      }
      result.error_class =
        error ===
        "Neither 'pwsh' (PowerShell Core) nor 'powershell' (Windows PowerShell) found in PATH"
          ? "shell_selection_unavailable"
          : "other";
    }
    return result;
  } catch {
    return {
      ...frame,
      variant: "malformed",
      error_class: "malformed_control_message",
    };
  }
}

function textUpdate(text) {
  // TextDeltaUpdate.text: 1 → InteractionUpdate.text_delta: 1 →
  // AgentServerMessage.interaction_update: 1.
  return fieldBytes(1, fieldBytes(1, fieldBytes(1, text)));
}

function turnEnded() {
  // TurnEndedUpdate is valid when empty. It is field 14 of InteractionUpdate.
  return fieldBytes(1, fieldBytes(14, Buffer.alloc(0)));
}

function modelDetails() {
  return Buffer.concat(
    [1, 3, 4, 5].map((field) => fieldBytes(field, "pitot-control")),
  );
}

function unaryResponse(path) {
  const model = modelDetails();
  if (
    path.endsWith("/GetUsableModels") ||
    path.endsWith("/GetDefaultModelForCli")
  ) {
    return fieldBytes(1, model);
  }
  return Buffer.alloc(0);
}

function outstandingPhase(state, sent) {
  if (!state.allow_tool_result_observed && !sent.allow) return "allow";
  if (
    state.allow_tool_result_observed &&
    !state.denied_result_observed &&
    !sent.deny
  )
    return "deny";
  if (state.denied_result_observed && !sent.final) return "final";
  return null;
}

const args = parseArgs(process.argv);
if (process.argv.includes("--self-test")) {
  const command = "pitot-e2e-canary fixture-nonce";
  const message = shellExecution(command);
  const framed = connectEnvelope(message);
  if (
    !message.includes(Buffer.from(command)) ||
    framed.readUInt32BE(1) !== message.length
  ) {
    throw new Error("Cursor Connect/protobuf fixture is invalid");
  }
  const state = {
    allow_tool_result_observed: false,
    denied_result_observed: false,
  };
  const firstStream = { allow: false, deny: false, final: false };
  if (outstandingPhase(state, firstStream) !== "allow")
    throw new Error("allow phase was not selected");
  firstStream.allow = true;
  if (outstandingPhase(state, firstStream) !== null)
    throw new Error("allow phase duplicated on one stream");
  if (
    outstandingPhase(state, { allow: false, deny: false, final: false }) !==
    "allow"
  ) {
    throw new Error("allow phase was not recovered on a replacement stream");
  }
  state.allow_tool_result_observed = true;
  if (
    outstandingPhase(state, { allow: false, deny: false, final: false }) !==
    "deny"
  ) {
    throw new Error("deny phase was not selected after the allow receipt");
  }
  state.denied_result_observed = true;
  if (
    outstandingPhase(state, { allow: false, deny: false, final: false }) !==
    "final"
  ) {
    throw new Error("final phase was not selected after the denial receipt");
  }
  const nonce = "fixture-nonce";
  const quotedCommand =
    "'/owned path/工具' --role canary --receipt '/owned path/result' PITOT_ALLOW fixture-nonce";
  const argv = [
    "/owned path/工具",
    "--role",
    "canary",
    "--receipt",
    "/owned path/result",
    "PITOT_ALLOW",
    nonce,
  ];
  const encoded = shellExecution(quotedCommand, "allow", argv);
  const serverExecution = protobufFields(
    singleField(protobufFields(encoded), 2, 2),
  );
  const shellArguments = protobufFields(singleField(serverExecution, 2, 2));
  const parsing = protobufFields(singleField(shellArguments, 8, 2));
  const parsedCommand = protobufFields(singleField(parsing, 2, 2));
  if (
    stringField(parsedCommand, 1) !== argv[0] ||
    JSON.stringify(
      parsedCommand
        .filter((field) => field.number === 2)
        .map((field) => stringField(protobufFields(field.value), 2)),
    ) !== JSON.stringify(argv.slice(1))
  )
    throw new Error("authored command arguments were split or lost");
  function clientResult({
    id = 1,
    tag = 1,
    resultCommand = quotedCommand,
    output = `PITOT_CANARY_RESULT PITOT_ALLOW ${nonce}\n`,
    exit = 0,
    reason = `Command execution was blocked by a hook: PITOT_CONTROLLER_DENY ${nonce}`,
    executionId,
    extra = Buffer.alloc(0),
  } = {}) {
    const details = Buffer.concat([
      fieldBytes(1, resultCommand),
      ...(tag === 1
        ? [fieldVarint(3, exit), fieldBytes(5, output)]
        : [fieldBytes(3, reason)]),
    ]);
    return fieldBytes(
      2,
      Buffer.concat([
        fieldVarint(1, id),
        ...(executionId === undefined ? [] : [fieldBytes(15, executionId)]),
        fieldBytes(2, fieldBytes(tag, details)),
        extra,
      ]),
    );
  }
  const valid = clientResult();
  if (!matchesShellResult(valid, quotedCommand, nonce, "allow"))
    throw new Error("typed allowed result refused");
  if (
    !matchesShellResult(
      clientResult({ executionId: "pitot-exec-allow" }),
      quotedCommand,
      nonce,
      "allow",
    )
  )
    throw new Error("matching optional execution ID refused");
  for (const negative of [
    clientResult({ id: 2 }),
    clientResult({ executionId: "pitot-exec-deny" }),
    clientResult({ resultCommand: "different command" }),
    clientResult({ tag: 2 }),
    clientResult({ exit: 1 }),
    clientResult({ output: "PITOT_CANARY_RESULT PITOT_ALLOW wrong-nonce\n" }),
    clientResult({
      output: `prefix PITOT_CANARY_RESULT PITOT_ALLOW ${nonce}\n`,
    }),
    clientResult({ extra: fieldVarint(1, 1) }),
    clientResult({ extra: fieldBytes(3, Buffer.alloc(0)) }),
    fieldBytes(1, valid),
    Buffer.concat([valid, fieldBytes(7, Buffer.alloc(0))]),
    valid.subarray(0, valid.length - 1),
    Buffer.from([0]),
    Buffer.from([
      0x12, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x7f,
    ]),
  ]) {
    if (matchesShellResult(negative, quotedCommand, nonce, "allow"))
      throw new Error("unrelated or malformed allowed result admitted");
  }
  const deniedCommand = quotedCommand.replace("PITOT_ALLOW", "PITOT_DENY");
  const diagnosticCommands = { allow: quotedCommand, deny: deniedCommand };
  const controlThrow = fieldBytes(
    5,
    fieldBytes(
      2,
      Buffer.concat([
        fieldVarint(1, 1),
        fieldBytes(
          2,
          "Neither 'pwsh' (PowerShell Core) nor 'powershell' (Windows PowerShell) found in PATH",
        ),
        fieldBytes(3, "private stack"),
        fieldBytes(4, "private code"),
      ]),
    ),
  );
  const controlSummary = controlObservation(controlThrow, nonce);
  if (
    clientMessageKind(controlThrow) !== "exec_client_control_message" ||
    controlSummary.variant !== "throw" ||
    controlSummary.native_id !== 1 ||
    controlSummary.error_class !== "shell_selection_unavailable" ||
    Object.hasOwn(controlSummary, "issued_exact_command_match") ||
    /private stack|private code/.test(JSON.stringify(controlSummary)) ||
    shellObservation(controlThrow, diagnosticCommands, nonce) !== null
  )
    throw new Error(
      "control diagnostic conflated shell results or exposed content",
    );
  if (
    clientMessageKind(Buffer.concat([controlThrow, controlThrow])) !==
      "malformed" ||
    controlObservation(Buffer.concat([controlThrow, controlThrow]), nonce)
      .variant !== "malformed"
  )
    throw new Error("ambiguous control diagnostic admitted");
  const spawned = shellObservation(
    clientResult({
      tag: 5,
      reason: "Parsing result is required",
    }),
    diagnosticCommands,
    nonce,
  );
  if (
    spawned.variant !== "spawn_error" ||
    spawned.native_id !== 1 ||
    !spawned.issued_exact_command_match ||
    spawned.error_class !== "missing_parsing_result" ||
    JSON.stringify(spawned).includes("C:/private/path")
  )
    throw new Error(
      "native spawn diagnostic lost correlation or exposed content",
    );
  if (shellObservation(valid, diagnosticCommands, nonce).variant !== "success")
    throw new Error("native success diagnostic refused");
  const failure = fieldBytes(
    2,
    Buffer.concat([
      fieldVarint(1, 1),
      fieldBytes(
        2,
        fieldBytes(
          2,
          Buffer.concat([
            fieldBytes(1, quotedCommand),
            Buffer.from([
              0x18, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01,
            ]),
            fieldBytes(6, "private stderr content"),
          ]),
        ),
      ),
    ]),
  );
  const failedObservation = shellObservation(
    failure,
    diagnosticCommands,
    nonce,
  );
  if (
    failedObservation.variant !== "failure" ||
    failedObservation.exit_code !== -1 ||
    JSON.stringify(failedObservation).includes("private stderr content")
  )
    throw new Error(
      "signed failure diagnostic was misdecoded or exposed stderr",
    );
  for (const [tag, expected] of [
    [4, "rejected"],
    [7, "permission_denied"],
  ]) {
    if (
      shellObservation(clientResult({ tag }), diagnosticCommands, nonce)
        .variant !== expected
    )
      throw new Error("native rejection diagnostic refused");
  }
  const timeout = fieldBytes(
    2,
    Buffer.concat([
      fieldVarint(1, 1),
      fieldBytes(
        2,
        fieldBytes(
          3,
          Buffer.concat([fieldBytes(1, quotedCommand), fieldVarint(3, 5000)]),
        ),
      ),
    ]),
  );
  if (shellObservation(timeout, diagnosticCommands, nonce).timeout_ms !== 5000)
    throw new Error("native timeout diagnostic refused");
  if (
    shellObservation(clientResult({ id: 3 }), diagnosticCommands, nonce)
      .issued_phase_id_match
  )
    throw new Error("unrelated diagnostic ID correlated");
  if (
    shellObservation(
      clientResult({ extra: fieldVarint(1, 1) }),
      diagnosticCommands,
      nonce,
    ).variant !== "malformed"
  )
    throw new Error("ambiguous native diagnostic admitted");
  if (
    shellObservation(
      fieldBytes(1, Buffer.from(nonce)),
      diagnosticCommands,
      nonce,
    ) !== null
  )
    throw new Error("non-execution frame diagnosed as a shell result");
  const denied = clientResult({ id: 2, tag: 4, resultCommand: deniedCommand });
  if (!matchesShellResult(denied, deniedCommand, nonce, "deny"))
    throw new Error("typed rejected result refused");
  for (const negative of [
    clientResult({ id: 1, tag: 4, resultCommand: deniedCommand }),
    clientResult({ id: 2, tag: 7, resultCommand: deniedCommand }),
    clientResult({
      id: 2,
      tag: 4,
      resultCommand: deniedCommand,
      reason: "blocked by a hook",
    }),
    clientResult({
      id: 2,
      tag: 4,
      resultCommand: deniedCommand,
      reason: `PITOT_CONTROLLER_DENY ${nonce}`,
    }),
    fieldBytes(7, Buffer.from(`PITOT_DENY ${nonce} blocked by a hook`)),
  ]) {
    if (matchesShellResult(negative, deniedCommand, nonce, "deny"))
      throw new Error("unrelated denied result admitted");
  }
  const resultFrame = connectEnvelope(valid);
  for (let boundary = 1; boundary < resultFrame.length; boundary += 1) {
    const read = connectReader();
    if (
      read(resultFrame.subarray(0, boundary)).length !== 0 ||
      !read(resultFrame.subarray(boundary))[0]?.equals(valid)
    )
      throw new Error("fragmented result frame was lost or admitted early");
  }
  if (
    connectReader()(Buffer.concat([resultFrame, connectEnvelope(denied)]))
      .length !== 2
  )
    throw new Error("coalesced result frames were lost");
  const ended = connectReader();
  if (
    ended(Buffer.concat([connectEnvelope(Buffer.from("{}"), 2), resultFrame]))
      .length ||
    ended(resultFrame).length
  )
    throw new Error("result after end-stream envelope admitted");
  for (const frame of [
    connectEnvelope(valid, 1),
    Buffer.from([0, 1, 0, 0, 1]),
  ]) {
    const read = connectReader();
    if (read(frame).length || read(resultFrame).length)
      throw new Error("unsupported or oversized frame did not refuse stream");
  }
  process.stdout.write("PASS: Cursor Connect/protobuf endpoint fixture\n");
  process.exit(0);
}
const required = [
  "nonce",
  "receipt",
  "ready-file",
  "canary-command",
  "canary-argv",
];
for (const key of required) {
  if (!args[key]) throw new Error(`missing --${key}`);
}
const canaryArgv = JSON.parse(args["canary-argv"]);
if (
  !Array.isArray(canaryArgv) ||
  canaryArgv.length !== 5 ||
  canaryArgv.some(
    (value) => typeof value !== "string" || !value || value.includes("\0"),
  ) ||
  canaryArgv[1] !== "--role" ||
  canaryArgv[2] !== "canary" ||
  canaryArgv[3] !== "--receipt"
)
  throw new Error("invalid authored canary arguments");
function phaseCommand(phase) {
  return `${args["canary-command"]} PITOT_${phase.toUpperCase()} ${args.nonce}`;
}
function phaseArgv(phase) {
  return [...canaryArgv, `PITOT_${phase.toUpperCase()}`, args.nonce];
}

const receipt = {
  schema_version: 1,
  agent: "cursor",
  protocol: "cursor_connect_proto",
  nonce: args.nonce,
  initial_prompt_observed: false,
  tool_call_response_emitted: false,
  tool_result_observed: false,
  allow_tool_call_response_emitted: false,
  allow_tool_result_observed: false,
  deny_tool_call_response_emitted: false,
  denied_result_observed: false,
  final_response_emitted: false,
  endpoint_observed: null,
  auxiliary_requests: 0,
  cursor_requests: [],
  response_attempts: { allow: 0, deny: 0, final: 0 },
  transport_errors: [],
  native_shell_results: [],
  native_control_results: [],
  control_heartbeats: 0,
  front_connections: [],
};

let eventSequence = 0;
let connectionSequence = 0;
let runSequence = 0;
const backendConnections = new Map();
const connectionId = (socket) =>
  backendConnections.get(socket?.remotePort) ?? null;
function sessionConnectionId(session) {
  try {
    return connectionId(session?.socket);
  } catch {
    return null;
  }
}

function save() {
  const temporary = `${args.receipt}.tmp`;
  fs.mkdirSync(path.dirname(args.receipt), {
    recursive: true,
  });
  fs.writeFileSync(temporary, `${JSON.stringify(receipt, null, 2)}\n`);
  fs.renameSync(temporary, args.receipt);
}

const h2Server = http2.createServer();

function recordTransportError(scope, error, context = {}) {
  receipt.transport_errors.push({
    scope,
    code: String(error?.code || "unknown"),
    ...context,
    event_sequence: ++eventSequence,
  });
  receipt.transport_errors = receipt.transport_errors.slice(-20);
  save();
}

h2Server.on("stream", (stream, headers) => {
  const context = {
    connection_id: connectionId(stream.session.socket),
    stream_id: stream.id,
    attempt_id: ++runSequence,
  };
  const path = String(headers[":path"] || "").split("?", 1)[0];
  const contentType = String(headers["content-type"] || "");
  const chunks = [];
  const readMessages = connectReader();
  let responseStarted = false;
  let allowSent = false;
  let denySent = false;
  let finalSent = false;
  stream.on("error", (error) =>
    recordTransportError("run_stream", error, context),
  );
  stream.on("data", (chunk) => {
    const messages = readMessages(chunk);
    for (const message of messages) {
      const observation = shellObservation(
        message,
        {
          allow: phaseCommand("allow"),
          deny: phaseCommand("deny"),
        },
        args.nonce,
      );
      if (observation)
        receipt.native_shell_results.push({
          ...observation,
          ...context,
          source_sha: /^[0-9a-f]{40}$/.test(args["source-sha"] || "")
            ? args["source-sha"]
            : "unbound",
          event_sequence: ++eventSequence,
        });
    }
    for (const message of messages) {
      const control = controlObservation(message, args.nonce);
      if (control?.variant === "heartbeat") receipt.control_heartbeats += 1;
      else if (control)
        receipt.native_control_results.push({
          ...control,
          ...context,
          source_sha: /^[0-9a-f]{40}$/.test(args["source-sha"] || "")
            ? args["source-sha"]
            : "unbound",
          event_sequence: ++eventSequence,
        });
    }
    receipt.native_shell_results = receipt.native_shell_results.slice(-8);
    receipt.native_control_results = receipt.native_control_results.slice(-8);
    chunks.push(chunk);
    const body = Buffer.concat(chunks);
    receipt.cursor_inbound = {
      request_bytes: body.length,
      envelopes: envelopeShapes(body),
    };
    save();
    if (!responseStarted && body.includes(Buffer.from(args.nonce))) {
      responseStarted = true;
      receipt.initial_prompt_observed = true;
      receipt.endpoint_observed = {
        transport: "http2",
        method: "POST",
        path,
        media_type: contentType.split(";", 1)[0].trim().toLowerCase(),
        framing: "connect_envelope",
        request_shape: {
          service: "agent.v1.AgentService",
          method: "Run",
          stream: "bidirectional",
          message: "agent.v1.AgentClientMessage",
        },
      };
      receipt.cursor_run = {
        path,
        content_type: contentType,
        request_bytes: body.length,
        ...context,
      };
      save();
      stream.respond({
        ":status": 200,
        "content-type": "application/connect+proto",
      });
      if (args["response-fault"] === "text") {
        receipt.fault_response_emitted = "text";
        save();
        stream.write(connectEnvelope(textUpdate("No tool call")));
        stream.write(connectEnvelope(turnEnded()));
        stream.end(connectEnvelope(Buffer.from("{}"), 0x02));
      }
    }
    if (
      !responseStarted ||
      args["response-fault"] === "text" ||
      receipt.final_response_emitted
    )
      return;
    // A released Cursor CLI may reconnect its bidirectional Run stream after
    // a transient reset. Delivery is only proven by the matching client tool
    // result, so resend the outstanding phase once on each replacement stream.
    let phase = outstandingPhase(receipt, {
      allow: allowSent,
      deny: denySent,
      final: finalSent,
    });
    if (phase === "allow") {
      allowSent = true;
      receipt.tool_call_response_emitted = true;
      receipt.allow_tool_call_response_emitted = true;
      receipt.response_attempts.allow += 1;
      save();
      stream.write(
        connectEnvelope(
          shellExecution(phaseCommand("allow"), "allow", phaseArgv("allow")),
        ),
      );
      return;
    }
    if (
      !receipt.allow_tool_result_observed &&
      receipt.allow_tool_call_response_emitted &&
      messages.some((message) =>
        matchesShellResult(message, phaseCommand("allow"), args.nonce, "allow"),
      )
    ) {
      receipt.tool_result_observed = true;
      receipt.allow_tool_result_observed = true;
      save();
    }
    phase = outstandingPhase(receipt, {
      allow: allowSent,
      deny: denySent,
      final: finalSent,
    });
    if (phase === "deny") {
      denySent = true;
      receipt.deny_tool_call_response_emitted = true;
      receipt.response_attempts.deny += 1;
      save();
      stream.write(
        connectEnvelope(
          shellExecution(phaseCommand("deny"), "deny", phaseArgv("deny")),
        ),
      );
      return;
    }
    const nativeDenial = messages.some((message) =>
      matchesShellResult(message, phaseCommand("deny"), args.nonce, "deny"),
    );
    if (
      receipt.deny_tool_call_response_emitted &&
      !receipt.denied_result_observed &&
      nativeDenial
    ) {
      receipt.denied_result_observed = true;
      receipt.final_response_emitted = true;
      save();
    }
    phase = outstandingPhase(receipt, {
      allow: allowSent,
      deny: denySent,
      final: finalSent,
    });
    if (phase === "final") {
      finalSent = true;
      receipt.response_attempts.final += 1;
      save();
      stream.write(
        connectEnvelope(textUpdate(`PITOT_E2E_COMPLETE ${args.nonce}`)),
      );
      stream.write(connectEnvelope(turnEnded()));
      // Connect end-stream envelope. Success metadata is an empty JSON object.
      stream.end(connectEnvelope(Buffer.from("{}"), 0x02));
    }
  });
  stream.on("end", () => {
    if (responseStarted) return;
    const body = Buffer.concat(chunks);
    receipt.cursor_requests.push({
      path,
      content_type: contentType,
      request_bytes: body.length,
    });
    receipt.auxiliary_requests = receipt.cursor_requests.length;
    save();
    const payload = unaryResponse(path);
    stream.respond({
      ":status": 200,
      "content-type": contentType.includes("json")
        ? "application/json"
        : "application/proto",
    });
    stream.end(contentType.includes("json") ? Buffer.from("{}") : payload);
  });
});
h2Server.on("sessionError", (error, session) =>
  recordTransportError("h2_session", error, {
    connection_id: sessionConnectionId(session),
  }),
);
h2Server.on("error", (error) => recordTransportError("h2_server", error));

const http1Server = http.createServer((request, response) => {
  const chunks = [];
  request.on("data", (chunk) => chunks.push(chunk));
  request.on("end", () => {
    const path = String(request.url || "").split("?", 1)[0];
    const contentType = String(request.headers["content-type"] || "");
    const body = Buffer.concat(chunks);
    receipt.cursor_requests.push({
      path,
      content_type: contentType,
      request_bytes: body.length,
    });
    receipt.auxiliary_requests = receipt.cursor_requests.length;
    save();
    const payload = unaryResponse(path);
    response.writeHead(200, {
      "content-type": contentType.includes("json")
        ? "application/json"
        : "application/proto",
    });
    response.end(contentType.includes("json") ? Buffer.from("{}") : payload);
  });
});
http1Server.on("clientError", (error, socket) => {
  recordTransportError("http1_client", error, {
    connection_id: connectionId(socket),
  });
  socket.destroy();
});
http1Server.on("error", (error) => recordTransportError("http1_server", error));

const frontServer = net.createServer((client) => {
  const id = ++connectionSequence;
  client.on("error", (error) =>
    recordTransportError("front_client", error, { connection_id: id }),
  );
  client.once("data", (first) => {
    const isHttp2 = first.subarray(0, 14).toString() === "PRI * HTTP/2.0";
    const signature = Buffer.from("PRI * HTTP/2.0");
    receipt.front_connections.push({
      connection_id: id,
      first_data_bytes: first.length,
      signature_partial_match: first
        .subarray(0, Math.min(first.length, 14))
        .equals(signature.subarray(0, Math.min(first.length, 14))),
      routed_protocol: isHttp2 ? "http2" : "http1",
      event_sequence: ++eventSequence,
    });
    receipt.front_connections = receipt.front_connections.slice(-20);
    save();
    const target = isHttp2
      ? h2Server.address().port
      : http1Server.address().port;
    let backendPort;
    const backend = net.connect(target, "127.0.0.1", () => {
      backendPort = backend.localPort;
      backendConnections.set(backendPort, id);
      backend.write(first);
    });
    backend.on("close", () => backendConnections.delete(backendPort));
    backend.on("error", (error) => {
      recordTransportError("front_backend", error, { connection_id: id });
      client.destroy();
    });
    client.pipe(backend).pipe(client);
  });
});
frontServer.on("error", (error) => recordTransportError("front_server", error));

h2Server.listen(0, "127.0.0.1", () => {
  http1Server.listen(0, "127.0.0.1", () => {
    frontServer.listen(0, "127.0.0.1", () => {
      const address = frontServer.address();
      save();
      fs.writeFileSync(
        args["ready-file"],
        `http://127.0.0.1:${address.port}\n`,
      );
    });
  });
});

for (const signal of ["SIGTERM", "SIGINT"]) {
  process.on(signal, () =>
    frontServer.close(() => {
      h2Server.close();
      http1Server.close(() => process.exit(0));
    }),
  );
}
