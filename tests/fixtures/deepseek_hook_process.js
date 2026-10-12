const fs = require("node:fs");
const { spawn } = require("node:child_process");

const mode = process.argv[2];
const request = JSON.parse(fs.readFileSync(0, "utf8"));
if (mode === "hang" || mode === "descendant") {
  if (mode === "descendant") {
    spawn(process.execPath, ["-e", "setInterval(() => {}, 1000)"], {
      stdio: "inherit",
    });
  }
  setInterval(() => {}, 1000);
} else if (mode === "overflow") {
  process.stdout.write("x".repeat(100000));
} else if (mode === "malformed") {
  process.stdout.write("{}\nextra output\n");
} else {
  const response = {
    version: 1,
    request_id: request.request_id,
    session_id: request.session_id,
    tool_call_id: request.tool_call_id,
    action_id: "act_controller_test",
    outcome: mode === "deny" ? "deny" : "allow",
    reason: "Controller test denied the command",
  };
  if (mode === "mismatch") response.request_id = "another-request";
  if (mode === "foreign-session") response.session_id = "another-session";
  if (mode === "missing-action") delete response.action_id;
  if (mode === "unavailable") response.outcome = "unavailable";
  if (mode === "unknown-field") response.extra = "unexpected";
  if (mode === "duplicate") {
    process.stdout.write(
      JSON.stringify(response).replace(
        '"outcome":"allow"',
        '"outcome":"deny","outcome":"allow"',
      ) + "\n",
    );
    process.exit(0);
  }
  process.stdout.write(JSON.stringify(response) + "\n");
  process.exitCode = mode === "deny" || mode === "unavailable" ? 2 : 0;
}
