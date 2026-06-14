// Template for the OpenCode tool shim, embedded into the bridge binary. Edit
// this when the shim logic changes. `bridge install` renders it per tool:
// __BRIDGE_TOOL_DEF__ is replaced with a tooldefs/<name>.js module and
// __BRIDGE_BIN__ with the absolute bridge path. The generated shim proxies to
// the bridge CLI, which authenticates and calls the control plane. This header
// is stripped from the output, which gets its own "DO NOT EDIT" banner.
import { tool } from "@opencode-ai/plugin";
import { z } from "zod";
import { spawn } from "node:child_process";

const BRIDGE_BIN = __BRIDGE_BIN__;

function runBridgeTool(name, args) {
  return new Promise((resolve, reject) => {
    const child = spawn(BRIDGE_BIN, ["tool", name], { stdio: ["pipe", "pipe", "inherit"] });
    let stdout = "";
    child.stdout.on("data", (chunk) => {
      stdout += chunk;
    });
    child.on("error", reject);
    child.on("close", (code) => {
      if (code === 0) resolve(stdout.replace(/\n$/, ""));
      else reject(new Error(`bridge tool ${name} exited with code ${code}`));
    });
    child.stdin.write(JSON.stringify(args ?? {}));
    child.stdin.end();
  });
}

__BRIDGE_TOOL_DEF__
