#!/usr/bin/env node
// Keydris CLI launcher.
//
// This thin Node shim exists only to locate and exec the correct native
// `keydris` binary for the host's platform/arch. The real binary is delivered
// by a per-platform package (e.g. @keydris/cli-linux-x64) declared in this
// package's optionalDependencies, so npm downloads exactly one of them — the
// one matching the host's `os`/`cpu` — and skips the rest. Same pattern as
// esbuild, Biome, and swc.
//
// No network access, no privileged work, and no postinstall script run here:
// installation stays inert, and all argv/stdio/exit-code/signal handling is
// forwarded verbatim to the native binary so `keydris` behaves identically
// whether installed via npm or the curl|bash installer.
"use strict";

const { spawnSync } = require("node:child_process");
const fs = require("node:fs");

// platform+arch -> the package that ships the matching native binary.
// Keep this map in lockstep with optionalDependencies in package.json and with
// the release build matrix (Makefile PLATFORMS + .github/workflows/npm-release.yml).
const PACKAGES = {
  "darwin x64": "@keydris/cli-darwin-x64",
  "darwin arm64": "@keydris/cli-darwin-arm64",
  "linux x64": "@keydris/cli-linux-x64",
  "linux arm64": "@keydris/cli-linux-arm64",
  "win32 x64": "@keydris/cli-win32-x64",
  "win32 arm64": "@keydris/cli-win32-arm64",
};

function fail(message) {
  process.stderr.write(`keydris: ${message}\n`);
  process.exit(1);
}

function resolveBinary() {
  const key = `${process.platform} ${process.arch}`;
  const pkg = PACKAGES[key];

  if (!pkg) {
    fail(
      `unsupported platform: ${key}. Supported: ${Object.keys(PACKAGES).join(", ")}.\n` +
        "If this platform should be supported, please open an issue at " +
        "https://github.com/keydrisLabs/keydris-cli/issues",
    );
  }

  // The platform package's "main" points at its native binary; require.resolve
  // gives us its absolute path without executing it.
  let binary;
  try {
    binary = require.resolve(pkg);
  } catch {
    fail(
      `the native package "${pkg}" for your platform (${key}) is not installed.\n` +
        "This usually means the install skipped optional dependencies. Reinstall with " +
        'optional dependencies enabled (avoid npm "--omit=optional", ' +
        'pnpm "--no-optional", and yarn "--ignore-optional").',
    );
  }

  return binary;
}

function main() {
  const binary = resolveBinary();
  const args = process.argv.slice(2);

  const run = () =>
    spawnSync(binary, args, { stdio: "inherit", windowsHide: false });

  let result = run();

  // On POSIX, a binary restored from a tarball can lose its execute bit
  // depending on the extractor; repair once and retry before giving up.
  if (
    result.error &&
    result.error.code === "EACCES" &&
    process.platform !== "win32"
  ) {
    try {
      fs.chmodSync(binary, 0o755);
      result = run();
    } catch {
      // fall through to the generic error handling below
    }
  }

  if (result.error) {
    fail(`failed to launch native binary at ${binary}: ${result.error.message}`);
  }

  // Propagate a terminating signal (e.g. SIGINT from Ctrl-C) so callers and
  // shells see the CLI die the same way the native binary did.
  if (result.signal) {
    process.kill(process.pid, result.signal);
    return;
  }

  process.exit(result.status === null ? 1 : result.status);
}

main();
