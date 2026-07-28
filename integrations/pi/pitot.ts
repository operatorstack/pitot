import { spawnSync } from "node:child_process";

// handleToolCall is exported so the shipped boundary can be tested without a
// live Pi session. Pi itself calls the default extension registration below.
export function handleToolCall(event, run = spawnSync) {
  if (event.toolName !== "bash") return undefined;

  const payload = JSON.stringify({
    hook_event_name: "tool_call",
    tool_name: "bash",
    tool_input: { command: event.input?.command ?? "" },
  });
  const result = run(process.env.PITOT_BIN || "pitot", ["hook", "pi"], {
    input: payload,
    encoding: "utf8",
    maxBuffer: 1024 * 1024,
  });
  if (result.status === 0) return undefined;
  const reason = (result.stderr || "Pitot rejected the shell request").trim();
  return { block: true, reason: reason.slice(0, 1024) };
}

export default function pitotExtension(pi) {
  pi.on("tool_call", async (event) => handleToolCall(event));
}
