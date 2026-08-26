import { readFile } from "node:fs/promises";
import path from "node:path";

import { runNPM } from "./npm.mjs";
import { packageDirectory, platforms, workspaceRoot } from "./platforms.mjs";

const cliDirectory = path.join(workspaceRoot, "packages", "cli");
const packages = [
  {
    directory: cliDirectory,
    expected: new Set([
      "README.md",
      "bin/keydris.js",
      "config/dev.toml",
      "config/stable.toml",
      "package.json",
      "scripts/install-config.mjs"
    ])
  },
  ...platforms.map((platform) => ({
    directory: packageDirectory(platform),
    expected: new Set([`bin/${platform.binary}`, "package.json"])
  }))
];

for (const entry of packages) {
  const manifest = JSON.parse(
    await readFile(path.join(entry.directory, "package.json"), "utf8")
  );
  let result;
  try {
    result = runNPM(
      ["pack", "--dry-run", "--json", "--ignore-scripts"],
      entry.directory
    );
  } catch (error) {
    throw new Error(`${manifest.name}: ${error.message}`);
  }
  const report = JSON.parse(result.stdout);
  const actual = new Set(report[0].files.map((file) => file.path));
  if (
    actual.size !== entry.expected.size ||
    [...entry.expected].some((file) => !actual.has(file))
  ) {
    throw new Error(
      `${manifest.name}: unexpected tarball files: ${[...actual].join(", ")}`
    );
  }
  console.log(
    `verified npm pack ${manifest.name} (${report[0].size} packed bytes)`
  );
}
