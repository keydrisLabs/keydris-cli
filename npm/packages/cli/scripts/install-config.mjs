import { copyFile, readFile, writeFile } from "node:fs/promises";
import { homedir } from "node:os";
import path from "node:path";

async function installConfig() {
  const home = homedir();
  if (!home) {
    throw new Error("could not resolve the user's home directory");
  }

  const manifest = JSON.parse(
    await readFile(new URL("../package.json", import.meta.url), "utf8")
  );
  const channel = manifest.version.includes("-") ? "dev" : "stable";
  const source = new URL(`../config/${channel}.toml`, import.meta.url);
  const data = await readFile(source);
  const destination = path.join(home, ".keydris.toml");

  if (process.env.KEYDRIS_NO_CONFIG === "1") {
    console.log(
      `keydris: KEYDRIS_NO_CONFIG=1; leaving ${destination} unchanged`
    );
    return;
  }

  let existing;
  try {
    existing = await readFile(destination);
  } catch (error) {
    if (error.code !== "ENOENT") {
      throw error;
    }
  }

  if (existing?.equals(data)) {
    console.log(`keydris: ${channel} config already up to date at ${destination}`);
    return;
  }

  if (existing) {
    const backup = `${destination}.bak`;
    await copyFile(destination, backup);
    console.log(`keydris: backed up existing config to ${backup}`);
  }

  await writeFile(destination, data, { mode: 0o644 });
  console.log(`keydris: wrote ${channel} config to ${destination}`);
}

try {
  await installConfig();
} catch (error) {
  console.warn(`keydris: WARNING: could not install ~/.keydris.toml: ${error.message}`);
}
