# Codex Desktop governance

Govern the Codex desktop app (`ChatGPT.app`, bundle `com.openai.codex`) with the same session, routes, and command gate that `keydris codex` uses. The first shipped target is macOS.

## User experience

Setup once:

```bash
keydris init codex-desktop <agent-id>
keydris status
```

`init` signs in when this device is not bound, prepares the CA, installs the agent skill in `~/.agents/skills` (the directory the Codex CLI also reads), caches the policy scope, starts the proxy, and checks that the installed app's codex loads and trusts the Keydris hooks. It writes nothing to `~/.codex`.

Each governed session:

```bash
keydris codex-desktop
```

Quit the app first. Electron hands a second launch to the running instance, and that instance's app-server has no session, so the command stops and says so. Otherwise it mints one session, launches the app, and stays in the foreground until the app quits. Ctrl-C asks the app to quit and forces it after ten seconds or a second Ctrl-C. The session is then revoked.

A Dock launch is ordinary Codex. One exception comes from the CLI integration: when `keydris init codex` wired `~/.codex/hooks.json` and those hooks are trusted, they run in every Codex process, including a Dock launch of the app, and deny each shell command because there is no session.

## How the app is governed

The app runs its agent as `codex app-server`, a child process that inherits the app's environment. When `CODEX_CLI_PATH` names an executable, the app spawns it in place of the bundled codex, with the arguments it would pass to its own binary: `-c features.code_mode_host=true app-server --analytics-default-enabled`, followed by plugin flags such as `-c plugins.codex-app-tools@openai-bundled.mcp_servers.codex_app.enabled=true` when those plugins are enabled. Bundled tools such as `node_repl` receive the same path and run it as their own `app-server` and as `sandbox … -- <command>`.

`keydris codex-desktop` points `CODEX_CLI_PATH` at `~/.keydris-data/codex-desktop/bin/codex`. That shim execs the app's own codex with the Keydris overrides added as `-c` options:

- The settings `keydris codex` passes: hooks enabled, and sandboxed network through Codex's proxy, which honors the Keydris upstream proxy.
- The Keydris `PreToolUse`, `PermissionRequest`, and `SessionStart` hooks, as session-flag hooks with the same commands `keydris init codex` writes to `hooks.json`.
- `hooks.state`, which marks those hooks trusted by their current hashes, and enabled.

Codex keeps only the last group of `-c` options: a `-c` after the subcommand discards every `-c` before it, and the app puts its plugin flags there. The shim therefore adds its options right after the caller's last `-c`, never after `--`, or first when there is none. The app's own options keep the effect they would have without the shim.

The launch environment adds what `keydris codex` gives its child: `HTTP(S)_PROXY` carrying the session handle, the CA bundle variables, `KEYDRIS_SESSION`, and `KEYDRIS_SESSION_OWNER=keydris-run`. `KEYDRIS_CODEX_DESKTOP=1` lets the session briefing name the app.

| Traffic | Route into Keydris | Result |
| --- | --- | --- |
| Shell commands the agent runs | `PreToolUse` and `PermissionRequest` hooks | Allow or deny by policy, as in `keydris codex`. |
| Network from sandboxed commands | Codex's network proxy, upstream to the Keydris proxy | Session routes apply. |
| Model calls from app-server | Session `HTTPS_PROXY` | Metered under the Codex Desktop session when Codex uses an API key (`api.openai.com`). With ChatGPT sign-in they go to `chatgpt.com`, which is not a metered origin, so they pass through the session unmetered. |
| Remote MCP servers app-server dials | Session `HTTPS_PROXY` | Governed when the host is in the session routes. |
| The app's own window traffic | Not proxied | Chromium ignores proxy variables on macOS. Out of scope. |

## Hook trust

Codex skips a hook it does not trust without an error. Trust is recorded per hook, keyed by the hook's source and position, and bound to a hash of its definition. A dotted `-c` path cannot address those keys, because they contain `.`, so `hooks.state` is passed as one inline table.

Codex merges that table over the `hooks.state` in `config.toml`, which is where `/hooks` records a hook the user turned off. The launch sets `enabled = true` along with the hash, so an entry in `config.toml`, or in a repository's `.codex/config.toml`, can neither disable nor untrust a Keydris hook. Trust the user gave their own hooks is kept.

Before each launch, and during `init`, keydris runs the app's codex as `app-server` in a throwaway `CODEX_HOME` with its proxy pointed at a closed port, reads the hashes from `hooks/list`, and lists again with `hooks.state` to confirm that every Keydris hook reports `trusted` and enabled. It then writes the shim and runs it the ways the app does, with and without plugin flags after the subcommand, and requires the same result each time. A missing, untrusted or disabled hook stops the launch before a session is minted, so a Codex update that changes hashing, keys or how it merges `-c` options fails closed instead of running ungoverned. The placement rule does not depend on the app's argument order, but the check covers only the invocations listed in `codexDesktopInvocations`.

## Bundle layout

The app has shipped its codex as `Contents/Resources/codex` (26.915, codex 0.155) and as `Contents/Resources/codex-cli/CodexCLI.app/Contents/MacOS/codex` (26.924, codex 0.158). keydris runs the binary the app itself would, and tries the newer location first.

## Isolation

- keydris writes nothing to `~/.codex` for the launch. Session start still refreshes the Keydris-managed MCP entries in `config.toml`, as every Keydris session does.
- The app itself records the `CODEX_CLI_PATH` it started with in `config.toml`, in the environment of its `node_repl` MCP server, so a governed launch leaves the shim path there. The app writes that entry when it starts, so a normal launch should put its own path back; manual check 7 confirms it.
- `keydris deinit codex-desktop` removes `~/.keydris-data/codex-desktop`, and the Keydris-managed Codex MCP entries unless `keydris codex` is still configured.
- With Codex Desktop configured, MCP inventory also reports `config.toml` under runtime `codex_desktop`, so the console can match those sessions.
- The Chrome plugin copies the app's bundled codex into `~/.codex/plugins` and runs its own app-server outside this launch. The shim never reaches that copy.

## Control plane

Sessions are minted with `agent_runtime=codex_desktop`, and MCP inventory uses the same runtime. The control plane accepts both from keydrisLabs/keydris-backend#183.

## Manual checks

No test launches the app. `TestCodexDesktopShimLive` (`KEYDRIS_CODEX_DESKTOP_LIVE=1`) runs the installed app's codex through the shim with the app's arguments and requires all three hooks to be trusted. These checks remain for a Mac run:

1. `keydris init codex-desktop <agent-id>` completes, including the hook check.
2. With the app quit, `keydris codex-desktop` opens it, and the app-server process runs the bundled codex with the Keydris overrides.
3. A shell command in a Codex thread is allowed or denied by policy; a denial shows the Keydris reason.
4. A command's request to a governed origin is governed, and the proxy log shows it under the session.
5. Model calls leave through the session proxy: `chatgpt.com` tunnels with ChatGPT sign-in, and `api.openai.com` shows `METER` lines with an API key.
6. Quitting the app ends the session and removes the shim; a later Dock launch is ordinary.
7. After that Dock launch, `CODEX_CLI_PATH` in `config.toml` names the app's own codex again, and the browser and computer-use tools still work.
8. The console shows the session as Codex Desktop.

## Out of scope

- Windows and Linux.
- The app's own window and browser traffic.
- Remote SSH and WSL hosts inside the app.
- The Chrome plugin's app-server and cloud tasks.
- The app's thread title, description and summary helpers. The app starts them as ephemeral read-only threads with `approvalPolicy: never` and `features.hooks` off in the thread's own settings. Those settings outrank the launch's `-c` options, so Keydris hooks do not run there.
