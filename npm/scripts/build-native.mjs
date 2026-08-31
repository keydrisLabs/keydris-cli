import { createHash } from "node:crypto";
import { spawnSync } from "node:child_process";
import { chmod, mkdir, readFile, stat } from "node:fs/promises";
import path from "node:path";

import {
  binaryPath,
  isSemVer,
  packageDirectory,
  platforms,
  repositoryRoot,
  workspaceRoot
} from "./platforms.mjs";

const cliManifestPath = path.join(workspaceRoot, "packages", "cli", "package.json");
const cliManifest = JSON.parse(await readFile(cliManifestPath, "utf8"));

let version = cliManifest.version;
let requestedTarget = "";
for (let index = 2; index < process.argv.length; index += 1) {
  const argument = process.argv[index];
  if (argument === "--version" && process.argv[index + 1]) {
    version = process.argv[++index].replace(/^v/, "");
  } else if (argument === "--target" && process.argv[index + 1]) {
    requestedTarget = process.argv[++index];
  } else {
    throw new Error(`unknown or incomplete argument: ${argument}`);
  }
}

if (!isSemVer(version)) {
  throw new Error(`invalid build version: ${version}`);
}

const selected = requestedTarget
  ? platforms.filter((platform) => platform.key === requestedTarget)
  : platforms;
if (selected.length === 0) {
  throw new Error(
    `unknown target ${requestedTarget}; expected one of ${platforms
      .map((platform) => platform.key)
      .join(", ")}`
  );
}

const goExecutable = process.env.GO || "go";
const buildCache = process.env.GOCACHE || path.join(repositoryRoot, ".gobuild");
const versionSymbol =
  "github.com/keydrisLabs/keydris-cli/internal/cli.Version";
const telemetryKeySymbol =
  "github.com/keydrisLabs/keydris-cli/internal/telemetry.PostHogKey";
const telemetryEndpointSymbol =
  "github.com/keydrisLabs/keydris-cli/internal/telemetry.PostHogEndpoint";

// The PostHog key is public/write-only and stamped only when the release
// environment provides it; local builds stay telemetry-free.
let ldflags = `-s -w -X ${versionSymbol}=${version}`;
if (process.env.POSTHOG_API_KEYSL) {
  ldflags += ` -X ${telemetryKeySymbol}=${process.env.POSTHOG_API_KEYSL}`;
}
if (process.env.POSTHOG_ENDPOINT) {
  ldflags += ` -X ${telemetryEndpointSymbol}=${process.env.POSTHOG_ENDPOINT}`;
}

for (const platform of selected) {
  const output = binaryPath(platform);
  await mkdir(path.dirname(output), { recursive: true });

  console.log(
    `building ${platform.packageName} (${platform.goos}/${platform.goarch})`
  );
  const result = spawnSync(
    goExecutable,
    [
      "build",
      "-trimpath",
      "-ldflags",
      ldflags,
      "-o",
      output,
      "./cmd/keydris"
    ],
    {
      cwd: repositoryRoot,
      env: {
        ...process.env,
        CGO_ENABLED: "0",
        GOOS: platform.goos,
        GOARCH: platform.goarch,
        GOCACHE: buildCache
      },
      stdio: "inherit",
      shell: false
    }
  );
  if (result.error) {
    throw result.error;
  }
  if (result.status !== 0) {
    process.exit(result.status ?? 1);
  }
  if (platform.nodeOS !== "win32") {
    await chmod(output, 0o755);
  }

  const body = await readFile(output);
  const details = await stat(output);
  const digest = createHash("sha256").update(body).digest("hex");
  console.log(
    `  ${path.relative(packageDirectory(platform), output)} ` +
      `${details.size} bytes sha256:${digest}`
  );
}
