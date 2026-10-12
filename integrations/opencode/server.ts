import { spawnSync } from "node:child_process";
import { Error as ToolError } from "@opencode/plugin/promise/tool";

export default {
  id: "pitot",
  async setup(ctx) {
    await ctx.tool.hook("execute.before", async (event) => {
      if (event.tool !== "shell") return;
      const command = event.input?.command;
      const result = spawnSync(
        process.env.PITOT_BIN || "pitot",
        ["hook", "opencode"],
        {
          input: JSON.stringify({
            hook_event_name: "PreToolUse",
            tool_name: "Bash",
            tool_input: { command: typeof command === "string" ? command : "" },
          }),
          encoding: "utf8",
          maxBuffer: 1024 * 1024,
        },
      );
      if (result.status !== 0) {
        throw new ToolError({
          message: (result.stderr || "Pitot rejected the shell request").trim(),
        });
      }
    });
  },
};
