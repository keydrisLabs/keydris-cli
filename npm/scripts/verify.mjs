import { spawnSync } from "node:child_process";
import { randomUUID } from "node:crypto";
import {
  cp,
  mkdir,
  readFile,
  rm,
  stat
} from "node:fs/promises";
import path from "node:path";

import {
  binaryPath,
  currentPlatform,
  packageDirectory,
  platforms,
  workspaceRoot
} from "./platforms.mjs";

const cliDirectory = path.join(workspaceRoot, "packages", "cli");
const cliManifest = JSON.parse(
  await readFile(path.join(cliDirectory, "package.json"), "utf8")
);
const expectedVersion = cliManifest.version;

if (
  cliManifest.scripts?.postinstall !== "node scripts/install-config.mjs"
) {
  throw new Error("@keydris/cli: config postinstall script is missing");
}

function effectiveToml(data) {
  return data
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter((line) => line && !line.startsWith("#"))
    .join("\n");
}

for (const channel of ["stable", "dev"]) {
  const bundled = await readFile(
    path.join(cliDirectory, "config", `${channel}.toml`),
    "utf8"
  );
  const deployed = await readFile(
    path.join(workspaceRoot, "..", "deploy", channel, "keydris.toml"),
    "utf8"
  );
  if (effectiveToml(bundled) !== effectiveToml(deployed)) {
    throw new Error(
      `@keydris/cli: bundled ${channel} config differs from deploy/${channel}/keydris.toml`
    );
  }
}

for (const platform of platforms) {
  const directory = packageDirectory(platform);
  const manifest = JSON.parse(
    await readFile(path.join(directory, "package.json"), "utf8")
  );
  if (manifest.name !== platform.packageName) {
    throw new Error(
      `${platform.directory}: package name ${manifest.name} does not match ${platform.packageName}`
    );
  }
  if (
    manifest.version !== expectedVersion ||
    manifest.os?.[0] !== platform.nodeOS ||
    manifest.cpu?.[0] !== platform.nodeCPU
  ) {
    throw new Error(`${manifest.name}: version/os/cpu metadata is inconsistent`);
  }
  const relativeBinary = `bin/${platform.binary}`;
  if (
    manifest.main !== relativeBinary ||
    manifest.bin?.["keydris-native"] !== relativeBinary
  ) {
    throw new Error(
      `${manifest.name}: native entry points do not match ${relativeBinary}`
    );
  }
  if (manifest.scripts?.install || manifest.scripts?.postinstall) {
    throw new Error(`${manifest.name}: install lifecycle scripts are forbidden`);
  }
  if (cliManifest.optionalDependencies[manifest.name] !== expectedVersion) {
    throw new Error(`${manifest.name}: launcher dependency version is not exact`);
  }

  const executable = binaryPath(platform);
  const details = await stat(executable);
  if (!details.isFile() || details.size < 1_000_000) {
    throw new Error(`${executable}: missing or unexpectedly small binary`);
  }
  const file = await readFile(executable);
  if (!file.subarray(0, platform.magic.length).equals(platform.magic)) {
    throw new Error(`${executable}: unexpected executable format`);
  }
  if (!file.includes(Buffer.from(expectedVersion))) {
    throw new Error(
      `${executable}: stamped version does not match ${expectedVersion}`
    );
  }
  console.log(`verified ${manifest.name} (${details.size} bytes)`);
}

const host = currentPlatform();
if (!host) {
  throw new Error(`launcher test unsupported on ${process.platform}/${process.arch}`);
}

const temporaryRoot =
  process.env.KEYDRIS_NPM_TEST_TMP ||
  path.join(workspaceRoot, ".test-tmp");
await mkdir(temporaryRoot, { recursive: true });
const installation = path.join(
  temporaryRoot,
  `keydris-npm-test-${randomUUID()}`
);
await mkdir(installation);
try {
  const scopeDirectory = path.join(installation, "node_modules", "@keydris");
  const installedCLI = path.join(scopeDirectory, "cli");
  const installedNative = path.join(
    scopeDirectory,
    host.packageName.split("/")[1]
  );
  await cp(cliDirectory, installedCLI, { recursive: true });
  await cp(packageDirectory(host), installedNative, { recursive: true });

  const launcher = path.join(installedCLI, "bin", "keydris.js");
  const versionResult = spawnSync(process.execPath, [launcher, "version"], {
    encoding: "utf8",
    shell: false
  });
  if (versionResult.error || versionResult.status !== 0) {
    throw new Error(
      `launcher version test failed: ${versionResult.error || versionResult.stderr}`
    );
  }
  const expectedOutput = `keydris ${expectedVersion}`;
  if (versionResult.stdout.trim() !== expectedOutput) {
    throw new Error(
      `launcher returned ${JSON.stringify(
        versionResult.stdout.trim()
      )}; expected ${JSON.stringify(expectedOutput)}`
    );
  }

  const upgradeResult = spawnSync(process.execPath, [launcher, "upgrade"], {
    encoding: "utf8",
    shell: false
  });
  if (
    upgradeResult.error ||
    upgradeResult.status !== 0 ||
    !upgradeResult.stderr.includes(
      "npm install --global @keydris/cli@latest"
    )
  ) {
    throw new Error(
      `launcher npm-upgrade test failed: ${
        upgradeResult.error || upgradeResult.stderr
      }`
    );
  }
  console.log(`verified launcher on ${host.key}`);
} finally {
  await rm(installation, { recursive: true, force: true });
}
