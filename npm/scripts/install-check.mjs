import { randomUUID } from "node:crypto";
import { spawnSync } from "node:child_process";
import {
  mkdir,
  readFile,
  rm,
  writeFile
} from "node:fs/promises";
import path from "node:path";

import { runNPM } from "./npm.mjs";
import {
  currentPlatform,
  packageDirectory,
  workspaceRoot
} from "./platforms.mjs";

function pack(directory, destination) {
  const result = runNPM(
    [
      "pack",
      "--json",
      "--ignore-scripts",
      "--pack-destination",
      destination
    ],
    directory
  );
  const report = JSON.parse(result.stdout);
  return path.join(destination, report[0].filename);
}

const host = currentPlatform();
if (!host) {
  throw new Error(`install test unsupported on ${process.platform}/${process.arch}`);
}

const root = path.join(
  workspaceRoot,
  ".install-tmp",
  `keydris-npm-install-${randomUUID()}`
);
const tarballs = path.join(root, "tarballs");
const project = path.join(root, "project");
await mkdir(tarballs, { recursive: true });
await mkdir(project, { recursive: true });

try {
  const nativeTarball = pack(packageDirectory(host), tarballs);
  const launcherDirectory = path.join(workspaceRoot, "packages", "cli");
  const launcherTarball = pack(launcherDirectory, tarballs);
  const launcherManifest = JSON.parse(
    await readFile(path.join(launcherDirectory, "package.json"), "utf8")
  );

  await writeFile(
    path.join(project, "package.json"),
    `${JSON.stringify(
      {
        name: "keydris-install-test",
        version: "0.0.0",
        private: true
      },
      null,
      2
    )}\n`
  );

  runNPM(
    [
      "install",
      "--offline",
      "--ignore-scripts",
      "--no-audit",
      "--no-fund",
      "--package-lock=false",
      nativeTarball,
      launcherTarball
    ],
    project
  );

  const launcher = path.join(
    project,
    "node_modules",
    "@keydris",
    "cli",
    "bin",
    "keydris.js"
  );
  const result = spawnSync(process.execPath, [launcher, "version"], {
    encoding: "utf8",
    shell: false
  });
  const expected = `keydris ${launcherManifest.version}`;
  if (
    result.error ||
    result.status !== 0 ||
    result.stdout.trim() !== expected
  ) {
    throw new Error(
      `installed launcher failed: ${
        result.error || result.stderr || result.stdout
      }`
    );
  }
  console.log(`verified packed installation on ${host.key}`);
} finally {
  await rm(root, { recursive: true, force: true });
}
