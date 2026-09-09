# @keydris/cli

Authority before action. npm distribution for the native Keydris CLI.

```bash
npm install --global @keydris/cli --foreground-scripts
keydris init
```

The JavaScript package is a launcher. npm installs the matching native Keydris
binary for the current operating system and architecture as an optional
dependency. The complete proxy, identity, Claude Code, and OpenAI Codex
implementation remains in the native binary.

Do not install with `--omit=optional`; that omits the platform binary. A global
installation is recommended when using the long-running `keydris proxy`.

The install writes the channel defaults to `~/.keydris.toml`, backing up an
existing file as `~/.keydris.toml.bak`. Stable releases use the stable config;
prereleases use the dev config. Set `KEYDRIS_NO_CONFIG=1` to leave an existing
config unchanged. npm's `--ignore-scripts` option skips this config step.

The package performs no privileged work during installation. Trust-store changes
happen only when explicitly requested through `keydris init --trust-store`.

Use [`--foreground-scripts`](https://docs.npmjs.com/cli/v11/using-npm/config/#foreground-scripts) to see the native binary and configuration checks. npm owns download progress; Keydris shows real setup stages in `keydris init`. If lifecycle scripts were disabled, `init` creates missing configuration without replacing an existing file. `KEYDRIS_NO_CONFIG=1` also disables this fallback.

The CLI respects `NO_COLOR` and supports `--color auto|always|never`. Use `keydris status` to check readiness and `keydris reset --dry-run` to preview a reset.

Interactive CLI banners show the ASCII logo in `#F8F7F4`. Set
`KEYDRIS_LOGO_COLOR=default` for the terminal's own foreground on light themes.

On Windows with **WSL2**, install using Linux Node/npm inside your distribution
(`node -p 'process.platform'` must print `linux`). Install and run the agent,
Keydris and proxy there too. Keep certificates and state in the Linux home;
do not reuse native Windows state. Claude's Linux sandbox requires `bubblewrap`
and `socat`; `keydris doctor` checks these and namespace availability. WSL1 and
detected mixed Windows/Linux execution are rejected with recovery guidance.
After WSL shuts down, run `keydris proxy up` before reopening agent sessions.

VS Code integrated terminals use the normal agent launch commands: `claude`
or `keydris codex` after onboarding. For WSL, open the folder with VS Code's WSL
extension and run setup in that terminal. Sidebar integration is separate.

`keydris init` installs a bundled `keydris-authority` skill and a short session
briefing explaining delegated authority, approvals and denials. Guidance is
embedded in the native binary; no additional download is needed. Read it with
`keydris skill` or `keydris skill --brief`. Rerun `init` after upgrades to refresh
the installed copy. Existing and user-edited skill files are preserved.
