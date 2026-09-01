import { access, readFile } from "node:fs/promises";
import path from "node:path";

import {
  isSemVer,
  packageDirectory,
  platforms,
  workspaceRoot
} from "./platforms.mjs";

const manifestPath = path.join(process.cwd(), "package.json");
const manifest = JSON.parse(await readFile(manifestPath, "utf8"));
const launcherManifest = JSON.parse(
  await readFile(
    path.join(workspaceRoot, "packages", "cli", "package.json"),
    "utf8"
  )
);

if (!isSemVer(manifest.version)) {
  throw new Error(`${manifest.name}: package version is not valid SemVer`);
}
if (manifest.version === "0.0.0-development") {
  throw new Error(
    `${manifest.name}: refusing to publish the development placeholder version`
  );
}
if (manifest.license === "UNLICENSED") {
  throw new Error(
    `${manifest.name}: choose and record the project license before publishing`
  );
}

if (launcherManifest.version !== manifest.version) {
  throw new Error(
    `${manifest.name}: version does not match @keydris/cli@${launcherManifest.version}`
  );
}
for (const platform of platforms) {
  const nativeManifest = JSON.parse(
    await readFile(
      path.join(packageDirectory(platform), "package.json"),
      "utf8"
    )
  );
  if (nativeManifest.version !== launcherManifest.version) {
    throw new Error(
      `${nativeManifest.name}: version does not match @keydris/cli@${launcherManifest.version}`
    );
  }
  if (
    launcherManifest.optionalDependencies?.[nativeManifest.name] !==
    launcherManifest.version
  ) {
    throw new Error(
      `${nativeManifest.name}: launcher optional dependency is not exact`
    );
  }
}

const entryPoints = new Set([
  ...(manifest.main ? [manifest.main] : []),
  ...Object.values(manifest.bin || {})
]);
for (const entryPoint of entryPoints) {
  await access(path.join(process.cwd(), entryPoint));
}

console.log(`prepublish checks passed for ${manifest.name}@${manifest.version}`);
