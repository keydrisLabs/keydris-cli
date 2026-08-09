# @keydris/cli

npm distribution for the native Keydris CLI.

```bash
npm install --global @keydris/cli
keydris init
```

The JavaScript package is a launcher. npm installs the matching native Keydris
binary for the current operating system and architecture as an optional
dependency. The complete proxy, identity, Claude Code, and OpenAI Codex
implementation remains in the native binary.

Do not install with `--omit=optional`; that omits the platform binary. A global
installation is recommended when using the long-running `keydris proxy`.

This package performs no privileged work during installation. Trust-store
changes happen only when explicitly requested through
`keydris init --trust-store`.
