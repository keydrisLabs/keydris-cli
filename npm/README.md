# Keydris npm workspace

This directory packages the native Go CLI for npm. It does not reimplement the
proxy or identity runtime in JavaScript.

## Local preparation

No dependency installation is required; every script uses only Node's standard
library.

```bash
cd npm
npm run build:native
npm test
```

Generated executables are ignored by Git. `npm test` verifies all six binary
formats, exercises a production-shaped launcher installation on the host
platform, and checks every package tarball.

Clean all generated binaries, tarballs, caches, and temporary installations
before committing:

```bash
npm run clean
```

The main launcher is the only declared npm workspace. Platform package
directories are deliberately managed by the release scripts instead of npm
workspaces: their mutually exclusive `os` and `cpu` declarations cause npm to
reject incompatible workspace packages during a normal install.

## Prepare a release

Do not publish the checked-in `0.0.0-development` version.

Every manifest is licensed `Apache-2.0`; the full text lives in the repository
root [LICENSE](../LICENSE). Keep the license out of the package directories —
npm always adds a package-root `LICENSE` to the tarball regardless of `files`,
and `pack-check.mjs` asserts an exact file list.

```bash
cd npm
npm run version:packages -- 0.1.0
npm run build:native
npm test
```

The version command updates the workspace root, launcher, all native manifests,
and every exact optional-dependency version.

See [npm distribution](../docs/npm-distribution.md) for the publishing order and
security checklist.
