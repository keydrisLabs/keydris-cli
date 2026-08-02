// sync-versions.mjs — stamp a single version across every npm workspace package.
//
// The launcher (@keydris/cli) and all six platform packages must always publish
// the SAME exact version, and the launcher's optionalDependencies must pin that
// exact version (a range there can desync launcher and native binary). This
// script is the one writer of that invariant.
//
// Usage:
//   node scripts/sync-versions.mjs <version>
//   node scripts/sync-versions.mjs            # reads $KEYDRIS_VERSION
//
// Accepts either "0.1.0" or a git tag like "v0.1.0" (the leading "v" is
// stripped so the value is valid npm semver). Rewrites in place:
//   - version in every packages/*/package.json
//   - every pin in @keydris/cli optionalDependencies
import { readFileSync, writeFileSync, readdirSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const here = dirname(fileURLToPath(import.meta.url));
const packagesDir = join(here, "..", "packages");
const LAUNCHER = "@keydris/cli";

function die(msg) {
  process.stderr.write(`sync-versions: ${msg}\n`);
  process.exit(1);
}

function normalizeVersion(raw) {
  if (!raw) die("no version given (pass an argument or set $KEYDRIS_VERSION)");
  const v = raw.trim().replace(/^v/, "");
  // Minimal semver sanity check: MAJOR.MINOR.PATCH with optional -prerelease / +build.
  if (!/^\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)*$/.test(v)) {
    die(`"${raw}" is not a valid semver version`);
  }
  return v;
}

const version = normalizeVersion(process.argv[2] ?? process.env.KEYDRIS_VERSION);

const pkgDirs = readdirSync(packagesDir, { withFileTypes: true })
  .filter((e) => e.isDirectory())
  .map((e) => join(packagesDir, e.name));

const manifests = pkgDirs.map((dir) => {
  const path = join(dir, "package.json");
  const json = JSON.parse(readFileSync(path, "utf8"));
  return { path, json };
});

const names = new Set(manifests.map((m) => m.json.name));

for (const { path, json } of manifests) {
  json.version = version;

  if (json.name === LAUNCHER && json.optionalDependencies) {
    for (const dep of Object.keys(json.optionalDependencies)) {
      // Only re-pin our own platform packages; leave any third-party deps alone.
      if (names.has(dep)) json.optionalDependencies[dep] = version;
    }
  }

  writeFileSync(path, JSON.stringify(json, null, 2) + "\n");
  process.stdout.write(`  ${json.name} -> ${version}\n`);
}

process.stdout.write(`synced ${manifests.length} packages to ${version}\n`);
