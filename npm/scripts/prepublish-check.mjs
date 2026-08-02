// prepublish-check.mjs — fail the release before a broken set reaches the registry.
//
// Verifies the whole npm workspace is internally consistent and publishable:
//   1. every package shares one exact version;
//   2. the launcher pins each platform package at that exact version
//      (exact, not a range — a range desyncs launcher and native binary);
//   3. every platform package's native binary exists and, on POSIX, is
//      executable, so we never publish a launcher that resolves to a missing
//      or non-runnable binary.
//
// Usage:
//   node scripts/prepublish-check.mjs           # full check (binaries required)
//   node scripts/prepublish-check.mjs --no-bin  # skip the binary-presence check
//                                               # (manifest-only, e.g. on PRs)
import { readFileSync, readdirSync, statSync, constants, accessSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const here = dirname(fileURLToPath(import.meta.url));
const packagesDir = join(here, "..", "packages");
const LAUNCHER = "@keydris/cli";
const skipBinaries = process.argv.includes("--no-bin");

const errors = [];
const fail = (msg) => errors.push(msg);

const manifests = readdirSync(packagesDir, { withFileTypes: true })
  .filter((e) => e.isDirectory())
  .map((e) => {
    const dir = join(packagesDir, e.name);
    const path = join(dir, "package.json");
    return { dir, path, json: JSON.parse(readFileSync(path, "utf8")) };
  });

if (manifests.length === 0) fail("no packages found under packages/");

// 1. single shared version
const versions = new Set(manifests.map((m) => m.json.version));
if (versions.size > 1) {
  fail(`packages disagree on version: ${[...versions].join(", ")}. Run scripts/sync-versions.mjs.`);
}
const version = manifests[0]?.json.version;
// The committed in-repo version is the "0.0.0" placeholder; CI stamps a real
// version via sync-versions.mjs at release time. Only reject the placeholder in
// the full (publish) check — PR validation runs with --no-bin and must pass on
// the committed placeholder state.
if (version === "0.0.0" && !skipBinaries) {
  fail('version is still the placeholder "0.0.0". Run scripts/sync-versions.mjs <version> before publishing.');
}

const byName = new Map(manifests.map((m) => [m.json.name, m]));

// 2. launcher pins every platform package at the exact shared version
const launcher = byName.get(LAUNCHER);
if (!launcher) {
  fail(`launcher package ${LAUNCHER} not found`);
} else {
  const optional = launcher.json.optionalDependencies ?? {};
  for (const { json } of manifests) {
    if (json.name === LAUNCHER) continue;
    const pin = optional[json.name];
    if (pin === undefined) {
      fail(`${LAUNCHER} is missing ${json.name} in optionalDependencies`);
    } else if (pin !== version) {
      fail(`${LAUNCHER} pins ${json.name}@${pin}, expected exactly ${version}`);
    }
  }
  // no stale pins pointing at packages that no longer exist
  for (const dep of Object.keys(optional)) {
    if (dep.startsWith("@keydris/cli-") && !byName.has(dep)) {
      fail(`${LAUNCHER} pins ${dep}, which has no package in the workspace`);
    }
  }
}

// 3. each platform package's binary exists and is executable (POSIX)
if (!skipBinaries) {
  for (const { dir, json } of manifests) {
    if (json.name === LAUNCHER) continue;
    const main = json.main;
    if (!main) {
      fail(`${json.name} has no "main" pointing at its native binary`);
      continue;
    }
    const binPath = join(dir, main);
    let st;
    try {
      st = statSync(binPath);
    } catch {
      fail(`${json.name}: native binary missing at ${main} (build + copy it before publish)`);
      continue;
    }
    if (st.size === 0) {
      fail(`${json.name}: native binary at ${main} is empty`);
    }
    const isWindows = (json.os ?? []).includes("win32");
    if (!isWindows) {
      try {
        accessSync(binPath, constants.X_OK);
      } catch {
        fail(`${json.name}: native binary at ${main} is not executable (chmod 0755)`);
      }
    }
  }
}

if (errors.length > 0) {
  process.stderr.write("prepublish-check FAILED:\n");
  for (const e of errors) process.stderr.write(`  - ${e}\n`);
  process.exit(1);
}

process.stdout.write(
  `prepublish-check OK: ${manifests.length} packages @ ${version}` +
    (skipBinaries ? " (manifests only)\n" : " (binaries verified)\n"),
);
