import path from "node:path";
import { fileURLToPath } from "node:url";

export const workspaceRoot = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  ".."
);
export const repositoryRoot = path.resolve(workspaceRoot, "..");

export const platforms = [
  {
    key: "win32-x64",
    packageName: "@keydris/cli-win32-x64",
    directory: "platform-win32-x64",
    nodeOS: "win32",
    nodeCPU: "x64",
    goos: "windows",
    goarch: "amd64",
    binary: "keydris.exe",
    magic: Buffer.from([0x4d, 0x5a])
  },
  {
    key: "win32-arm64",
    packageName: "@keydris/cli-win32-arm64",
    directory: "platform-win32-arm64",
    nodeOS: "win32",
    nodeCPU: "arm64",
    goos: "windows",
    goarch: "arm64",
    binary: "keydris.exe",
    magic: Buffer.from([0x4d, 0x5a])
  },
  {
    key: "darwin-x64",
    packageName: "@keydris/cli-darwin-x64",
    directory: "platform-darwin-x64",
    nodeOS: "darwin",
    nodeCPU: "x64",
    goos: "darwin",
    goarch: "amd64",
    binary: "keydris",
    magic: Buffer.from([0xcf, 0xfa, 0xed, 0xfe])
  },
  {
    key: "darwin-arm64",
    packageName: "@keydris/cli-darwin-arm64",
    directory: "platform-darwin-arm64",
    nodeOS: "darwin",
    nodeCPU: "arm64",
    goos: "darwin",
    goarch: "arm64",
    binary: "keydris",
    magic: Buffer.from([0xcf, 0xfa, 0xed, 0xfe])
  },
  {
    key: "linux-x64",
    packageName: "@keydris/cli-linux-x64",
    directory: "platform-linux-x64",
    nodeOS: "linux",
    nodeCPU: "x64",
    goos: "linux",
    goarch: "amd64",
    binary: "keydris",
    magic: Buffer.from([0x7f, 0x45, 0x4c, 0x46])
  },
  {
    key: "linux-arm64",
    packageName: "@keydris/cli-linux-arm64",
    directory: "platform-linux-arm64",
    nodeOS: "linux",
    nodeCPU: "arm64",
    goos: "linux",
    goarch: "arm64",
    binary: "keydris",
    magic: Buffer.from([0x7f, 0x45, 0x4c, 0x46])
  }
];

export function packageDirectory(platform) {
  return path.join(workspaceRoot, "packages", platform.directory);
}

export function binaryPath(platform) {
  return path.join(packageDirectory(platform), "bin", platform.binary);
}

export function currentPlatform() {
  return platforms.find(
    (platform) =>
      platform.nodeOS === process.platform &&
      platform.nodeCPU === process.arch
  );
}

export function isSemVer(value) {
  const match =
    /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-([0-9A-Za-z.-]+))?(?:\+([0-9A-Za-z.-]+))?$/.exec(
      value
    );
  if (!match) {
    return false;
  }
  const prerelease = match[4];
  if (
    prerelease &&
    prerelease.split(".").some(
      (identifier) =>
        identifier === "" ||
        !/^[0-9A-Za-z-]+$/.test(identifier) ||
        (/^\d+$/.test(identifier) &&
          identifier.length > 1 &&
          identifier.startsWith("0"))
    )
  ) {
    return false;
  }
  const build = match[5];
  return (
    !build ||
    build
      .split(".")
      .every(
        (identifier) =>
          identifier !== "" && /^[0-9A-Za-z-]+$/.test(identifier)
      )
  );
}
