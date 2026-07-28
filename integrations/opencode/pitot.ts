import { spawnSync } from "node:child_process";

// OpenCode runs plugins in-process. This is the genuine synchronous
// tool.execute.before boundary; it is not a Claude PreToolUse simulation.
export const PitotPlugin = async () => ({
  "tool.execute.before": async (input, output) => {
    if (input.tool !== "bash") return;
    const command = output.args?.command;
    const payload = JSON.stringify({
      hook_event_name: "PreToolUse",
      tool_name: "Bash",
      tool_input: { command: typeof command === "string" ? command : "" },
    });
    const result = spawnSync(process.env.PITOT_BIN || "pitot", ["hook", "opencode"], {
      input: payload,
      encoding: "utf8",
      maxBuffer: 1024 * 1024,
    });
    if (result.status !== 0) {
      throw new Error((result.stderr || "Pitot rejected the shell request").trim());
    }
  },
});

export default PitotPlugin;
