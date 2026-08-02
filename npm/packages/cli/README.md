# @keydris/cli

The Keydris CLI, distributed via npm for Windows, macOS, and Linux.

```bash
npm install -g @keydris/cli
keydris version
```

This package is a thin Node launcher. The actual native `keydris` binary is
delivered by a per-platform package (for example `@keydris/cli-linux-x64`),
declared here as an `optionalDependency`. npm downloads only the one platform
package matching your `os`/`cpu` and skips the rest — the same mechanism used by
esbuild, Biome, and swc. There is **no** `postinstall` download step and no
network access at install time.

## Supported platforms

| OS      | Arch          | Package                      |
| ------- | ------------- | ---------------------------- |
| macOS   | x64 (Intel)   | `@keydris/cli-darwin-x64`    |
| macOS   | arm64 (Apple) | `@keydris/cli-darwin-arm64`  |
| Linux   | x64           | `@keydris/cli-linux-x64`     |
| Linux   | arm64         | `@keydris/cli-linux-arm64`   |
| Windows | x64           | `@keydris/cli-win32-x64`     |
| Windows | arm64         | `@keydris/cli-win32-arm64`   |

Linux binaries are built `CGO_ENABLED=0` (static, glibc-independent). A musl /
Alpine variant is not shipped yet.

## Other package managers

pnpm and yarn support the same `optionalDependencies` + `os`/`cpu` scheme.
Do **not** disable optional dependencies during install — the launcher needs its
platform binary:

- npm: avoid `--omit=optional`
- pnpm: avoid `--no-optional`
- yarn: avoid `--ignore-optional`

If the platform binary is missing, `keydris` exits with a message explaining how
to reinstall.

## Not using npm?

The CLI is also available as a standalone static binary installed with a single
`curl … | bash` command — see the repository README. The npm package and the
standalone binary are the same program and are interchangeable.
