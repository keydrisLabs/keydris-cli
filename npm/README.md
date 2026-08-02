# npm distribution (`@keydris/cli`)

An npm-workspaces monorepo that ships the Keydris CLI to Windows, macOS, and
Linux via a single `npm install -g @keydris/cli`. This lives alongside the Go
source (which remains the source of truth for the CLI itself); nothing here
changes the CLI's behavior — it only packages the already-built native binaries
for the npm ecosystem.

## Why this exists

Today the CLI installs with `curl … | bash` (see [`../install.sh`](../install.sh)).
That path is great for Linux/macOS servers but is awkward for Windows and for
teams that manage tooling through `package.json`. npm distribution adds a second,
equivalent install path that works identically on all three OSes and integrates
with the JS toolchain agents already use.

## Layout

```
npm/
  package.json                 private workspace root (not published)
  scripts/
    sync-versions.mjs          stamp one version across all 7 manifests + pins
    prepublish-check.mjs       guard: single version, exact pins, binaries present
  packages/
    cli/                       @keydris/cli — the launcher (published)
    platform-darwin-x64/       @keydris/cli-darwin-x64   (bin/keydris)
    platform-darwin-arm64/     @keydris/cli-darwin-arm64 (bin/keydris)
    platform-linux-x64/        @keydris/cli-linux-x64    (bin/keydris)
    platform-linux-arm64/      @keydris/cli-linux-arm64  (bin/keydris)
    platform-win32-x64/        @keydris/cli-win32-x64    (bin/keydris.exe)
    platform-win32-arm64/      @keydris/cli-win32-arm64  (bin/keydris.exe)
```

## How it works

`@keydris/cli` is a thin Node launcher. Every platform package is listed in its
`optionalDependencies`, each constrained by `os`/`cpu`, so **npm downloads only
the one platform package matching the host** and skips the other five. At
runtime the launcher resolves that package's native binary and execs it,
forwarding argv, stdio, exit code, and signals unchanged. There is no
`postinstall` download step and no network access at install time — the same
pattern esbuild, Biome, and swc use.

## Local development

The launcher is plain Node and needs no build. To exercise it against a real
binary, build the native binaries and stage them into the platform packages:

```bash
# from the repo root
make dist                      # cross-compiles all targets into ../dist/
make npm-stage                 # copies ../dist/keydris-<os>-<arch> into each
                               # packages/platform-*/bin/
cd npm && node packages/cli/bin/keydris.js version
```

> **Note:** a bare `npm install` at the workspace root fails with `EBADPLATFORM`,
> because npm enforces each workspace member's `os`/`cpu` and this set spans all
> three OSes. That constraint is exactly what makes real installs correct (npm
> pulls only the one platform package matching the host). For a local end-to-end
> check, simulate a real install instead:
>
> ```bash
> cd npm
> npm pack packages/cli packages/platform-$(node -p "process.platform==='win32'?'win32':process.platform")-$(node -p "process.arch==='x64'?'x64':process.arch")
> mkdir -p /tmp/keydris-consumer && cd /tmp/keydris-consumer && npm init -y
> npm install /path/to/keydris-cli-*.tgz /path/to/keydris-cli-<os>-<arch>-*.tgz
> ./node_modules/.bin/keydris version
> ```

## Release

The version across all seven packages is kept identical and the launcher's pins
are exact. To cut a release:

```bash
cd npm
node scripts/sync-versions.mjs 0.1.0     # or: v0.1.0 (leading v is stripped)
# (CI stages the built binaries into packages/platform-*/bin/)
node scripts/prepublish-check.mjs        # verifies versions, pins, and binaries
npm publish --workspaces --access public --provenance
```

Publish order matters: **platform packages first, launcher last**, so a
partially-failed publish never leaves an installable launcher pointing at a
binary that isn't on the registry yet. See
[`../.github/workflows/npm-release.yml`](../.github/workflows/npm-release.yml).

## Notes

- **Windows on ARM** (`@keydris/cli-win32-arm64`) is included so ARM Windows
  installs don't "succeed" and then fail at runtime with a missing binary. It
  requires a `windows/arm64` build target (added to the Makefile matrix).
- **musl / Alpine** is not shipped yet. The Linux binaries are built
  `CGO_ENABLED=0` (glibc-independent static); a dedicated `linux-x64-musl`
  variant can be added later with `detect-libc` in the launcher.
- **Do not disable optional dependencies** at install time (`--omit=optional`,
  `--no-optional`, `--ignore-optional`) — the launcher needs its platform
  binary and will exit with a guided error if it's absent.
