import test from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { fileURLToPath } from "node:url";
import { handleToolCall, apply } from "../integrations/deepseek/pitot.mjs";

const fixture = fileURLToPath(
  new URL("./fixtures/deepseek_hook_process.js", import.meta.url),
);
const config = {
  executable: process.execPath,
  runtime: fixture,
  nonce: "owned-ready",
};
const execution = (signal) => ({
  name: "bash",
  callId: "native-call",
  rootCallId: "native-call",
  arguments: { command: "printf controlled" },
  agent: { session: { id: "native-session" } },
  signal,
});
const launch = (mode) => (_program, _args, options) =>
  spawn(process.execPath, [fixture, mode], options);

for (const mode of [
  "allow",
  "deny",
  "malformed",
  "mismatch",
  "foreign-session",
  "missing-action",
  "unavailable",
  "overflow",
  "unknown-field",
  "duplicate",
]) {
  test(`native policy response: ${mode}`, async () => {
    const result = await handleToolCall(
      execution(new AbortController().signal),
      config,
      launch(mode),
    );
    assert.equal(result.kind, mode === "allow" ? "allow" : "deny");
    if (mode !== "allow")
      assert.equal(
        result.info.code,
        mode === "deny" ? "PITOT_DENIED" : "PITOT_CONTROL_UNAVAILABLE",
      );
  });
}

for (const mode of ["hang", "descendant"]) {
  test(`cancellation bounds ${mode} output pipes`, async () => {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), 300);
    const started = Date.now();
    try {
      const result = await handleToolCall(
        execution(controller.signal),
        config,
        launch(mode),
      );
      assert.equal(result.kind, "cancel");
      assert.ok(
        Date.now() - started < 4000,
        "cancelled invocation did not settle within its bound",
      );
    } finally {
      clearTimeout(timer);
    }
  });
}

test("already-cancelled calls do not spawn", async () => {
  const controller = new AbortController();
  controller.abort();
  const result = await handleToolCall(
    execution(controller.signal),
    config,
    () => {
      throw new Error("spawned");
    },
  );
  assert.equal(result.kind, "cancel");
});

test("invalid identity and empty command never spawn", async () => {
  for (const change of [
    { callId: "" },
    { agent: {} },
    { arguments: { command: " " } },
  ]) {
    const result = await handleToolCall(
      { ...execution(new AbortController().signal), ...change },
      config,
      () => {
        throw new Error("spawned");
      },
    );
    assert.equal(result.kind, "deny");
  }
});

test("unrelated tools keep the native middleware chain", async () => {
  let hook;
  let service;
  let ready;
  const ctx = {
    on: (_name, callback) => {
      hook = callback;
    },
    provide: (name, value) => {
      service = { name, value };
    },
    appReady: {
      onReady: (callback) => {
        ready = callback;
      },
    },
  };
  apply(ctx, config);
  assert.equal(service.name, "pitotShellControl");
  assert.equal(typeof ready, "function");
  const sentinel = { kind: "deny", reason: "another policy" };
  assert.equal(
    await hook({ name: "read_file" }, async () => sentinel),
    sentinel,
  );
});
