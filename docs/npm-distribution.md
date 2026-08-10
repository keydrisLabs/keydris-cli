# npm distribution

Keydris uses npm as a distribution layer around the complete native Go CLI.
The npm launcher does not implement identity, proxy, certificate, process, or
session behavior itself.

## Packages

| Package | Platform |
| --- | --- |
| `@keydris/cli` | Portable Node launcher |
| `@keydris/cli-win32-x64` | Windows x64 |
| `@keydris/cli-win32-arm64` | Windows ARM64 |
| `@keydris/cli-darwin-x64` | macOS Intel |
| `@keydris/cli-darwin-arm64` | macOS Apple Silicon |
| `@keydris/cli-linux-x64` | Linux x64 |
| `@keydris/cli-linux-arm64` | Linux ARM64 |

The launcher declares the six native packages as exact-version optional
dependencies. npm uses each package's `os` and `cpu` metadata to install the
compatible binary.

Each native package also exposes an internal `keydris-native` bin entry. The
launcher does not call that command; its purpose is to make npm apply executable
permissions to macOS/Linux binaries even if their tarballs were created on
Windows.

There are no `preinstall`, `install`, or `postinstall` scripts. Installing the
package does not download another executable, start a daemon, modify a trust
store, or request privileges.

## User installation

After the packages are published:

```bash
npm install --global @keydris/cli
keydris init
```

For temporary use:

```bash
npx --yes @keydris/cli version
```

A global installation is recommended for `keydris proxy up`, because npm owns
the executable location for the background process. Do not use
`--omit=optional`; it removes the native package.

## Build and verify

The workspace requires Node 20 or newer and Go 1.22 or newer.

```bash
cd npm
npm run build:native
npm test
```

`build:native` produces static Windows, macOS, and Linux binaries for x64 and
ARM64. It stamps the launcher package version into each binary.

`npm test` checks:

- manifest names, versions, platform metadata, and exact dependencies;
- absence of install lifecycle scripts;
- PE, Mach-O, and ELF executable signatures;
- native binary size;
- launcher execution and exit status on the host platform;
- npm-managed `keydris upgrade` behavior; and
- exact files included by `npm pack --dry-run`; and
- an offline installation from freshly packed launcher/native tarballs.

Generated executables and test/cache directories are ignored by Git.

## Version preparation

Checked-in manifests use `0.0.0-development` and must not be published.
The prepublish guard rejects that placeholder and also rejects `UNLICENSED`
manifests. All seven packages are `Apache-2.0`; the text lives in the repository
root [LICENSE](../LICENSE) and is deliberately not copied into the package
directories, because npm would then add it to every tarball and break the exact
file-list assertion in `pack-check.mjs`.

```bash
cd npm
npm run version:packages -- 0.1.0
npm run build:native
npm test
```

The version script accepts an optional leading `v`, validates SemVer, and
updates every manifest plus the launcher's exact optional dependencies.

## Publishing order

Publishing is intentionally not automated yet. When ready:

1. Create or confirm control of the `@keydris` npm organization.
2. Sign Windows executables and sign/notarize macOS executables.
3. Prepare a real version, build, and run all verification.
4. Publish all six native packages with a prerelease dist-tag such as `next`.
5. Confirm every native version is visible in the registry.
6. Publish `@keydris/cli` last, with the same dist-tag.
7. Configure npm trusted publishing for each of the seven packages.
8. Promote to `latest` with `npm dist-tag add` once the prerelease is proven.

Trusted publishing cannot come first. npm only lets a trusted publisher be
attached to a package that already exists in the registry, so the first publish
of each package must authenticate with a granular access token. Trusted
publishing itself works fine from a private repository; only provenance does
not.

Provenance is deliberately **not** enabled. npm generates attestations only for
packages built from a **public** repository, regardless of whether the package
itself is public, so `"provenance": true` would fail a real publish while this
repository stays private. `publishConfig` therefore carries `access` alone. If
the repository is ever opened up, restore `"provenance": true` in all seven
manifests — or simply rely on trusted publishing, which attests by default
without the flag.

Example commands after those prerequisites are complete:

```bash
cd npm

npm publish ./packages/platform-win32-x64 --access public --tag next
npm publish ./packages/platform-win32-arm64 --access public --tag next
npm publish ./packages/platform-darwin-x64 --access public --tag next
npm publish ./packages/platform-darwin-arm64 --access public --tag next
npm publish ./packages/platform-linux-x64 --access public --tag next
npm publish ./packages/platform-linux-arm64 --access public --tag next
npm publish ./packages/cli --access public --tag next
```

npm versions are immutable. If a release is only partially published, fix the
problem and use a new prerelease version rather than attempting to reuse the
same version.

## Upgrades

The launcher sets `KEYDRIS_DISTRIBUTION=npm`. Consequently,
`keydris upgrade` does not overwrite an executable inside `node_modules`; it
prints the appropriate npm commands:

```bash
npm install --global @keydris/cli@latest
npm install --save-dev @keydris/cli@latest
```

The S3/`install.sh` distribution keeps the native self-updater.

## Supply-chain requirements

- Prefer npm trusted publishing through GitHub Actions OIDC over long-lived
  registry tokens.
- Publish provenance for every native package and the launcher once the source
  repository is public; it cannot be generated from a private repository.
- Require two-factor authentication for npm organization maintainers.
- Preserve exact launcher/native versions.
- Review `npm pack --dry-run --json` output before publishing.
- Never add privileged install lifecycle scripts.

Official references:

- [npm package metadata](https://docs.npmjs.com/files/package.json/)
- [npm workspaces](https://docs.npmjs.com/cli/using-npm/workspaces/)
- [npm trusted publishing](https://docs.npmjs.com/trusted-publishers/)
- [npm provenance](https://docs.npmjs.com/generating-provenance-statements/)
