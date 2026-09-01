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
