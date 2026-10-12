import { spawn } from "node:child_process";
import { randomUUID } from "node:crypto";
import { isAbsolute, join } from "node:path";

export const name = "pitot-shell-control";
export const inject = ["tools", "appReady"];

const LIMIT = 64 * 1024;
const identity = (value) =>
  typeof value === "string" && value.trim().length > 0 && value.length <= 1024;
const deny = (reason, code = "PITOT_CONTROL_UNAVAILABLE") => ({
  kind: "deny",
  reason,
  info: { name: "PitotControlError", code, reason },
});

function parseDecision(text) {
  const value = JSON.parse(text);
  const allowed = new Set([
    "version",
    "request_id",
    "session_id",
    "tool_call_id",
    "action_id",
    "outcome",
    "reason",
  ]);
  if (
    !value ||
    Array.isArray(value) ||
    typeof value !== "object" ||
    Object.keys(value).some((key) => !allowed.has(key)) ||
    value.version !== 1 ||
    !["allow", "deny", "unavailable"].includes(value.outcome) ||
    (value.reason !== undefined &&
      (typeof value.reason !== "string" || value.reason.length > 1024))
  ) {
    throw new Error("Invalid decision envelope");
  }
  // The response is a flat scalar object. Scan complete string tokens rather
  // than relying on JSON.parse's last-key-wins behavior.
  const keys = new Set();
  for (let i = 0; i < text.length; i++) {
    if (text[i] !== '"') continue;
    const start = i++;
    for (; i < text.length; i++) {
      if (text[i] === "\\") i++;
      else if (text[i] === '"') break;
    }
    let following = i + 1;
    while (/\s/.test(text[following] ?? "") && following < text.length)
      following++;
    if (text[following] === ":") {
      const key = JSON.parse(text.slice(start, i + 1));
      if (keys.has(key)) throw new Error("Duplicate decision key");
      keys.add(key);
    }
  }
  return value;
}

function invoke(executable, args, payload, signal, launch) {
  return new Promise((resolve) => {
    let child;
    let stdout = "";
    let bytes = 0;
    let failed = false;
    let cancelled = false;
    let killTimer;
    let drainTimer;
    let timer;
    let settled = false;
    let stopping = false;
    const finish = (code, exitSignal) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      clearTimeout(killTimer);
      clearTimeout(drainTimer);
      signal.removeEventListener("abort", abort);
      resolve({
        code,
        stdout,
        cancelled,
        failed: failed || exitSignal !== null,
      });
    };
    const kill = (signalName) => {
      if (process.platform !== "win32" && child?.pid) {
        try {
          process.kill(-child.pid, signalName);
        } catch {
          /* already exited */
        }
      }
      child?.kill(signalName);
    };
    const stop = () => {
      failed = true;
      if (stopping) return;
      stopping = true;
      if (process.platform === "win32" && child?.pid) {
        // The native hook is normally a single Go process. Also reap children
        // of transparent launch wrappers used by verified installations.
        const cleanup = spawn(
          join(
            process.env.SystemRoot || "C:\\Windows",
            "System32",
            "taskkill.exe",
          ),
          ["/pid", String(child.pid), "/T", "/F"],
          { stdio: "ignore", windowsHide: true },
        );
        cleanup.on("error", () => {});
        const cleanupTimer = setTimeout(() => cleanup.kill(), 1000);
        cleanup.on("close", () => clearTimeout(cleanupTimer));
      } else kill("SIGTERM");
      killTimer = setTimeout(() => kill("SIGKILL"), 500);
      drainTimer = setTimeout(() => {
        child?.stdin.destroy();
        child?.stdout.destroy();
        child?.stderr.destroy();
        finish(null, "SIGKILL");
      }, 1500);
    };
    const abort = () => {
      cancelled = true;
      stop();
    };
    if (signal.aborted) return resolve({ cancelled: true });
    try {
      child = launch(executable, args, {
        stdio: ["pipe", "pipe", "pipe"],
        shell: false,
        detached: process.platform !== "win32",
        windowsHide: true,
      });
    } catch {
      return resolve({ failed: true });
    }
    timer = setTimeout(stop, 35_000);
    signal.addEventListener("abort", abort, { once: true });
    child.on("error", () => {
      failed = true;
    });
    child.stdin.on("error", () => {
      failed = true;
    });
    child.stdout.setEncoding("utf8");
    child.stdout.on("data", (chunk) => {
      bytes += Buffer.byteLength(chunk);
      if (bytes > LIMIT) stop();
      else stdout += chunk;
    });
    child.stderr.on("data", (chunk) => {
      bytes += chunk.length;
      if (bytes > LIMIT) stop();
    });
    child.on("close", finish);
    child.stdin.end(payload);
    // Cancellation can occur between the initial check and listener setup.
    if (signal.aborted) abort();
  });
}

// An explicit Controller grant is required even when the runtime normally
// permits observation-only hooks. Transport failures never become permission.
export async function handleToolCall(exec, config, launch = spawn) {
  if (exec.name !== "bash" && exec.name !== "pwsh") return undefined;
  const sessionId = exec.agent?.session?.id;
  const command = exec.arguments?.command;
  if (
    !identity(sessionId) ||
    !identity(exec.callId) ||
    typeof command !== "string" ||
    !command.trim() ||
    Buffer.byteLength(command) > 1024 * 1024
  ) {
    return deny("Invalid native shell request");
  }
  if (
    !isAbsolute(config.executable ?? "") ||
    !isAbsolute(config.runtime ?? "")
  ) {
    return deny("Pitot executable and runtime must be absolute paths");
  }
  const requestId = randomUUID();
  const payload = JSON.stringify({
    hook_event_name: "tools/pre-execute",
    tool_name: exec.name,
    tool_input: { command },
    session_id: sessionId,
    tool_call_id: exec.callId,
    request_id: requestId,
    ...(identity(exec.rootCallId) ? { root_call_id: exec.rootCallId } : {}),
  });
  const result = await invoke(
    config.executable,
    ["hook", "deepseek", "--runtime", config.runtime],
    payload,
    exec.signal,
    launch,
  );
  if (result.cancelled || exec.signal.aborted) return { kind: "cancel" };
  if (result.failed) return deny("Pitot Controller request failed");
  let response;
  try {
    response = parseDecision(result.stdout);
  } catch {
    return deny("Invalid Pitot Controller response");
  }
  if (
    response?.version !== 1 ||
    response.request_id !== requestId ||
    response.session_id !== sessionId ||
    response.tool_call_id !== exec.callId ||
    !identity(response.action_id)
  ) {
    return deny("Uncorrelated Pitot Controller response");
  }
  if (result.code === 0 && response.outcome === "allow")
    return { kind: "allow" };
  if (result.code === 2 && response.outcome === "deny") {
    return deny(
      typeof response.reason === "string" && response.reason.trim()
        ? response.reason.slice(0, 1024)
        : "Pitot Controller denied the shell request",
      "PITOT_DENIED",
    );
  }
  return deny("Pitot Controller is unavailable");
}

export function apply(ctx, config) {
  if (
    !isAbsolute(config.executable ?? "") ||
    !isAbsolute(config.runtime ?? "") ||
    !identity(config.nonce)
  ) {
    throw new Error(
      "Pitot requires an owned executable, runtime and readiness nonce",
    );
  }
  ctx.on("tools/pre-execute", async (exec, next) => {
    const decision = await handleToolCall(exec, config);
    return decision === undefined || decision.kind === "allow"
      ? next()
      : decision;
  });
  ctx.provide("pitotShellControl", { nonce: config.nonce });
  ctx.appReady.onReady(() =>
    process.stderr.write(
      JSON.stringify({
        event: "pitot-shell-control-ready",
        nonce: config.nonce,
      }) + "\n",
    ),
  );
}
