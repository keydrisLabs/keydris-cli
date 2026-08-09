import { readFile, rename, writeFile } from "node:fs/promises";
import path from "node:path";

import { isSemVer, platforms, workspaceRoot } from "./platforms.mjs";

const requested = process.argv[2]?.replace(/^v/, "");
if (!requested || !isSemVer(requested)) {
  console.error("usage: npm run version:packages -- <semver>");
  process.exit(1);
}

const manifestPaths = [
  path.join(workspaceRoot, "package.json"),
  path.join(workspaceRoot, "packages", "cli", "package.json"),
  ...platforms.map((platform) =>
    path.join(
      workspaceRoot,
      "packages",
      platform.directory,
      "package.json"
    )
  )
];

for (const manifestPath of manifestPaths) {
  const manifest = JSON.parse(await readFile(manifestPath, "utf8"));
  manifest.version = requested;
  if (manifest.name === "@keydris/cli") {
    for (const dependency of Object.keys(manifest.optionalDependencies)) {
      manifest.optionalDependencies[dependency] = requested;
    }
  }
  const temporaryPath = `${manifestPath}.tmp`;
  await writeFile(temporaryPath, `${JSON.stringify(manifest, null, 2)}\n`);
  await rename(temporaryPath, manifestPath);
  console.log(
    `set ${path.relative(workspaceRoot, manifestPath)} to ${requested}`
  );
}
