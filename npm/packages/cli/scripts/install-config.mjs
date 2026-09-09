import { access, copyFile, readFile, writeFile } from "node:fs/promises";
import { homedir } from "node:os";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";
import path from "node:path";

const require = createRequire(import.meta.url);
const color = Boolean(process.stdout.isTTY) &&
  process.env.TERM !== "dumb" && !Object.hasOwn(process.env, "NO_COLOR");
const clean = (value) => String(value).replace(/[\u0000-\u001f\u007f-\u009f\u202a-\u202e\u2066-\u2069]/g, " ");
function line(state, message) {
  const codes = { ok: 32, warning: 33, inactive: 90 };
  const label = state === "ok" ? "[OK]" : state === "warning" ? "[!]" : "[-]";
  console.log((color ? `\x1b[${codes[state]}m${label}\x1b[0m` : label) + " " + clean(message));
}

// npm downloads the native optional dependency. This script reports only
// checks/configuration it actually performs; it never fabricates download progress.
export async function installConfig({ onlyIfMissing = false } = {}) {
  if (process.platform === "win32" && (process.env.WSL_DISTRO_NAME || process.env.WSL_INTEROP)) {
    throw new Error("Windows Node.js was launched from WSL; install using Linux Node/npm inside WSL2 to keep Windows and Linux configuration separate");
  }
  const home = homedir();
  if (!home || !path.isAbsolute(home)) {
    throw new Error("could not resolve the user's absolute home directory");
  }
  const destination = path.join(home, ".keydris.toml");
  if (process.env.KEYDRIS_NO_CONFIG === "1") {
    if (!onlyIfMissing) line("inactive", `Configuration skipped (KEYDRIS_NO_CONFIG=1): ${destination}`);
    return;
  }
  let existing;
  try { existing = await readFile(destination); }
  catch (error) { if (error.code !== "ENOENT") throw error; }
  if (existing && onlyIfMissing) return;

  const manifest = JSON.parse(await readFile(new URL("../package.json", import.meta.url), "utf8"));
  const channel = manifest.version.includes("-") ? "dev" : "stable";
  const data = await readFile(new URL(`../config/${channel}.toml`, import.meta.url));
  if (existing?.equals(data)) {
    line("ok", `Configuration already current: ${destination}`);
    return;
  }
  if (existing) {
    const backup = `${destination}.bak`;
    await copyFile(destination, backup);
    line("inactive", `Previous configuration backed up: ${backup}`);
  }
  await writeFile(destination, data, { mode: 0o644 });
  line("ok", `${channel} configuration installed: ${destination}`);
}

async function finishInstall() {
  console.log("\nKeydris · Authority before action");
  let binaryReady = false;
  try {
    const packageName = `@keydris/cli-${process.platform}-${process.arch}`;
    const executable = require.resolve(packageName);
    if (!path.isAbsolute(executable)) throw new Error("native binary path is not absolute");
    await access(executable);
    binaryReady = true;
    line("ok", `Native binary available (${process.platform}/${process.arch})`);
  } catch {
    line("warning", "Native binary unavailable. Reinstall @keydris/cli without --omit=optional.");
  }
  let configReady = true;
  try { await installConfig(); }
  catch (error) {
    configReady = false;
    line("warning", `Configuration could not be installed: ${error.message}`);
  }
  if (binaryReady) {
    console.log(configReady ? "\nNext: keydris init" : "\nNext: keydris init (retries missing configuration)");
    console.log("Setup guides you through sign-in, certificates and proxy startup.\n");
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  await finishInstall();
}
