#!/usr/bin/env node
"use strict";

const { spawnSync } = require("node:child_process");
const fs = require("node:fs");

const supplied = process.argv.slice(2);
const pitot = supplied[0] || process.env.PITOT_BIN || "pitot";
const pitotArgs = supplied.length >= 5
  ? ["--real-bin", supplied[1], "--receipt", supplied[2], "--nonce", supplied[3], "hook", "qwen", "--runtime", supplied[4]]
  : ["hook", "qwen"];
const payload = fs.readFileSync(0);
const result = spawnSync(pitot, pitotArgs, { input: payload, encoding: "utf8", windowsHide: true });
const detail = `${result.stdout || ""}${result.stderr || ""}`.trim();
const allowed = result.status === 0 && !result.error;
const reason = allowed
  ? "Pitot accepted the shell action"
  : (detail || result.error?.message || "Pitot rejected the shell request").slice(0, 1024);

process.stdout.write(JSON.stringify({
  hookSpecificOutput: {
    hookEventName: "PreToolUse",
    permissionDecision: allowed ? "allow" : "deny",
    permissionDecisionReason: reason,
  },
}) + "\n");

