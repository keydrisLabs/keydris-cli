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

The launcher has one `postinstall` script. It copies the bundled channel defaults
to `~/.keydris.toml` and backs up an existing file as `~/.keydris.toml.bak`.
Stable package versions select the stable config; prereleases select dev. Set
`KEYDRIS_NO_CONFIG=1` to keep the existing file. The script performs no network
requests and does not start a daemon, modify a trust store, or request
privileges. Installing with npm's `--ignore-scripts` skips the config step.

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
- the bounded config-only install lifecycle script;
- PE, Mach-O, and ELF executable signatures;
- native binary size;
- launcher execution and exit status on the host platform;
- npm-managed `keydris upgrade` behavior; and
- exact files included by `npm pack --dry-run`; and
- an offline installation from freshly packed launcher/native tarballs.

Generated executables and test/cache directories are ignored by Git.

## Version preparation

The checked-in launcher version supplies the base version for development
publishes. The release workflow rewrites all seven manifests before building:

- pushes to `main` publish `<base>-dev.<run-id>.<attempt>` under the `next`
  dist-tag;
- tags such as `v0.2.0` publish `0.2.0` under the `latest` dist-tag.

The prepublish guard rejects `0.0.0-development`, invalid SemVer, version
mismatches, and `UNLICENSED` manifests. All seven packages are `Apache-2.0`; the
text lives in the repository root [LICENSE](../LICENSE) and is deliberately not
copied into the package directories, because npm would then add it to every
tarball and break the exact file-list assertion in `pack-check.mjs`.

```bash
cd npm
npm run version:packages -- 0.1.0
npm run build:native
npm test
```

The version script accepts an optional leading `v`, validates SemVer, and
updates every manifest plus the launcher's exact optional dependencies.

## Publishing order

The `release.yml` GitHub Actions workflow builds and verifies all npm artifacts,
publishes the six native packages, confirms that each native version is visible,
and publishes `@keydris/cli` last. Publishing the launcher last prevents npm
clients from observing a launcher whose exact native dependencies are missing.

Each of the seven existing npm packages must configure this repository and
`.github/workflows/release.yml` as an npm trusted publisher with `npm publish`
enabled. The workflow uses GitHub OIDC and stores no npm token.

Provenance is deliberately **not** enabled. npm generates attestations only for
packages built from a **public** repository, regardless of whether the package
itself is public, so `"provenance": true` would fail a real publish while this
repository stays private. `publishConfig` therefore carries `access` alone. If
the repository is ever opened up, restore `"provenance": true` in all seven
manifests — or simply rely on trusted publishing, which attests by default
without the flag.

Manual publishing remains available for recovery or bootstrap:

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
problem and push a new commit to generate a new development prerelease instead
of attempting to reuse the same version. A failed tagged release requires a new
SemVer tag.

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
- Keep the install lifecycle script limited to copying the bundled config into
  the user's home directory; do not add network, executable, or privileged work.

Official references:

- [npm package metadata](https://docs.npmjs.com/files/package.json/)
- [npm workspaces](https://docs.npmjs.com/cli/using-npm/workspaces/)
- [npm trusted publishing](https://docs.npmjs.com/trusted-publishers/)
- [npm provenance](https://docs.npmjs.com/generating-provenance-statements/)
