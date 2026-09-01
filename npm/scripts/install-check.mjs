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
const home = path.join(root, "home");
await mkdir(tarballs, { recursive: true });
await mkdir(project, { recursive: true });
await mkdir(home, { recursive: true });

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
  const previousConfig = "previous config\n";
  await writeFile(path.join(home, ".keydris.toml"), previousConfig);

  runNPM(
    [
      "install",
      "--offline",
      "--no-audit",
      "--no-fund",
      "--package-lock=false",
      nativeTarball,
      launcherTarball
    ],
    project,
    {
      env: {
        HOME: home,
        USERPROFILE: home
      }
    }
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

  const channel = launcherManifest.version.includes("-") ? "dev" : "stable";
  const expectedConfig = await readFile(
    path.join(launcherDirectory, "config", `${channel}.toml`),
    "utf8"
  );
  const installedConfig = await readFile(
    path.join(home, ".keydris.toml"),
    "utf8"
  );
  const backupConfig = await readFile(
    path.join(home, ".keydris.toml.bak"),
    "utf8"
  );
  if (
    installedConfig !== expectedConfig ||
    backupConfig !== previousConfig
  ) {
    throw new Error("npm postinstall did not install and back up channel config");
  }

  const preservedConfig = "preserve this config\n";
  await writeFile(path.join(home, ".keydris.toml"), preservedConfig);
  const postinstall = path.join(
    project,
    "node_modules",
    "@keydris",
    "cli",
    "scripts",
    "install-config.mjs"
  );
  const noConfigResult = spawnSync(process.execPath, [postinstall], {
    encoding: "utf8",
    env: {
      ...process.env,
      HOME: home,
      USERPROFILE: home,
      KEYDRIS_NO_CONFIG: "1"
    },
    shell: false
  });
  if (
    noConfigResult.error ||
    noConfigResult.status !== 0 ||
    (await readFile(path.join(home, ".keydris.toml"), "utf8")) !==
      preservedConfig
  ) {
    throw new Error(
      `KEYDRIS_NO_CONFIG test failed: ${
        noConfigResult.error || noConfigResult.stderr
      }`
    );
  }

  console.log(`verified packed installation on ${host.key}`);
} finally {
  await rm(root, { recursive: true, force: true });
}
