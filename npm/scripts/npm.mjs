import { spawnSync } from "node:child_process";
import { existsSync } from "node:fs";
import path from "node:path";

import { workspaceRoot } from "./platforms.mjs";

// npmCommand resolves an invocation of the npm CLI that never routes through
// a Windows .cmd shim: Node refuses to spawnSync .cmd files without a shell
// (CVE-2024-27980), and a shell would mangle arguments containing spaces.
// Preference order: the npm that invoked us (npm_execpath, set under
// `npm run`), then npm-cli.js next to the running Node, then the PATH lookup
// that still works everywhere but Windows.
export function npmCommand() {
  const configured = process.env.npm_execpath;
  if (configured?.endsWith(".js")) {
    return { command: process.execPath, prefix: [configured] };
  }
  const bundled = path.join(
    path.dirname(process.execPath),
    "node_modules",
    "npm",
    "bin",
    "npm-cli.js"
  );
  if (existsSync(bundled)) {
    return { command: process.execPath, prefix: [bundled] };
  }
  if (process.platform === "win32") {
    throw new Error(
      "npm entry point not found; run this script via `npm run` so npm_execpath is set"
    );
  }
  return { command: "npm", prefix: [] };
}

// runNPM executes one npm command with the workspace-local cache and throws
// on any failure.
export function runNPM(args, cwd, options = {}) {
  const npm = npmCommand();
  const result = spawnSync(npm.command, [...npm.prefix, ...args], {
    cwd,
    encoding: "utf8",
    env: {
      ...process.env,
      ...options.env,
      npm_config_cache:
        options.env?.npm_config_cache ||
        process.env.npm_config_cache ||
        path.join(workspaceRoot, ".npm-cache")
    },
    shell: false
  });
  if (result.error || result.status !== 0) {
    throw new Error(`npm ${args[0]} failed: ${result.error || result.stderr}`);
  }
  return result;
}
