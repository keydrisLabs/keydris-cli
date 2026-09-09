#!/usr/bin/env node

import { spawnSync } from "node:child_process";
import { chmodSync } from "node:fs";
import { createRequire } from "node:module";

const require = createRequire(import.meta.url);

if (process.platform === "win32" && (process.env.WSL_DISTRO_NAME || process.env.WSL_INTEROP)) {
  console.error("keydris: Windows Node.js is running inside WSL. Install Linux Node/npm and @keydris/cli inside your WSL2 distribution, then retry.");
  process.exit(1);
}

const nativePackages = new Map([
  ["win32-x64", "@keydris/cli-win32-x64"],
  ["win32-arm64", "@keydris/cli-win32-arm64"],
  ["darwin-x64", "@keydris/cli-darwin-x64"],
  ["darwin-arm64", "@keydris/cli-darwin-arm64"],
  ["linux-x64", "@keydris/cli-linux-x64"],
  ["linux-arm64", "@keydris/cli-linux-arm64"]
]);

const target = `${process.platform}-${process.arch}`;
const nativePackage = nativePackages.get(target);

if (!nativePackage) {
  console.error(
    `keydris: unsupported platform ${process.platform}/${process.arch}`
  );
  process.exit(1);
}

let executable;
try {
  executable = require.resolve(nativePackage);
} catch {
  console.error(`keydris: native package ${nativePackage} is unavailable.`);
  console.error(
    "Reinstall @keydris/cli without --omit=optional, then try again."
  );
  process.exit(1);
}

function launch() {
  return spawnSync(executable, process.argv.slice(2), {
    stdio: "inherit",
    windowsHide: false,
    env: {
      ...process.env,
      KEYDRIS_DISTRIBUTION: "npm"
    }
  });
}

// Explicit onboarding repairs a missing config when npm lifecycle scripts were
// disabled. Existing user config is never overwritten by this fallback.
const setupArgs = process.argv.slice(2);
if (setupArgs[0] === "--color") setupArgs.splice(0, 2);
else if (setupArgs[0]?.startsWith("--color=")) setupArgs.shift();
if (setupArgs[0] === "init" && !setupArgs.includes("--help") && !setupArgs.includes("-h")) {
  try {
    const { installConfig } = await import("../scripts/install-config.mjs");
    await installConfig({ onlyIfMissing: true });
  } catch (error) {
    console.error("keydris: setup could not prepare configuration:", error.message);
    process.exit(1);
  }
}

let result = launch();
if (process.platform !== "win32" && result.error?.code === "EACCES") {
  try {
    chmodSync(executable, 0o755);
    result = launch();
  } catch (error) {
    console.error(
      `keydris: native binary is not executable and its permissions could not be repaired: ${error.message}`
    );
    process.exit(1);
  }
}

if (result.error) {
  console.error(`keydris: failed to launch native binary: ${result.error.message}`);
  process.exit(1);
}

if (result.signal) {
  try {
    process.kill(process.pid, result.signal);
  } catch {
    process.exit(1);
  }
} else {
  process.exit(result.status ?? 1);
}
