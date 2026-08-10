import { lstat, readdir, rm } from "node:fs/promises";
import path from "node:path";

import {
  binaryPath,
  platforms,
  repositoryRoot,
  workspaceRoot
} from "./platforms.mjs";

const generatedPaths = [
  ...platforms.map(binaryPath),
  path.join(workspaceRoot, ".npm-cache"),
  path.join(workspaceRoot, ".test-tmp"),
  path.join(workspaceRoot, ".install-tmp"),
  path.join(workspaceRoot, ".mode-check"),
  path.join(workspaceRoot, ".local-pack"),
  path.join(workspaceRoot, ".local-test"),
  path.join(workspaceRoot, "node_modules"),
  path.join(repositoryRoot, ".gobuild")
];

let removed = 0;
for (const generatedPath of generatedPaths) {
  try {
    await lstat(generatedPath);
    await rm(generatedPath, { recursive: true, force: true });
    removed += 1;
  } catch (error) {
    if (error.code !== "ENOENT") {
      throw error;
    }
  }
}

async function removeGeneratedEntries(directory) {
  let entries;
  try {
    entries = await readdir(directory, { withFileTypes: true });
  } catch (error) {
    if (error.code === "ENOENT") {
      return;
    }
    throw error;
  }

  for (const entry of entries) {
    const entryPath = path.join(directory, entry.name);
    if (entry.isDirectory() && entry.name === "node_modules") {
      await rm(entryPath, { recursive: true, force: true });
      removed += 1;
    } else if (entry.isDirectory()) {
      await removeGeneratedEntries(entryPath);
    } else if (entry.isFile() && entry.name.endsWith(".tgz")) {
      await rm(entryPath, { force: true });
      removed += 1;
    }
  }
}

await removeGeneratedEntries(workspaceRoot);
console.log(`cleared ${removed} generated npm artifact(s)`);
