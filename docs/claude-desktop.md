# Claude Desktop governance

Govern Claude Desktop with the same session, routes, and command gate the CLI already uses for Claude Code. Desktop is not launched with `keydris run`. It is a long-lived GUI app whose network is split across the Electron app, the embedded Claude Code engine, and the Cowork VM. Those three do not share one proxy setting, and only the engine can carry a proxy URL that contains the session handle.

The first shipped target is macOS. The forwarder and the session lifecycle are written so Windows and Linux can reuse them once their Desktop config path and trust store are confirmed.

## User experience

Setup once:

```bash
keydris init claude-desktop <agent-id>
keydris status
```

`init` signs in when this device is not bound, creates the CA if needed, installs that CA as a full root in the macOS System keychain, and writes the SessionStart, SessionEnd, and PreToolUse hooks. It does not leave a proxy pinned. macOS prompts once for the System keychain change.

Each governed session:

```bash
keydris claude-desktop
```

The command starts the local proxy if it is down, mints one session, starts the forwarder, points Desktop at it, and launches Claude. It stays in the foreground until Claude exits or the command is interrupted, then revokes the session and removes the proxy pin. Quit Claude completely first. If Claude is already running, the command stops and says so.

A Dock launch does not create a session. After this command exits, a Dock launch is ordinary Claude again. Governed tool and fetch calls work only while `keydris claude-desktop` is running.

`keydris deinit claude-desktop` removes the Desktop pin, the Desktop settings directory, and the hooks that belong to this target. `keydris status --target claude-desktop` reports whether the CA is trusted, whether a session is active, and whether a pin was left behind.

## What a session governs

| Traffic | Route into Keydris | Result |
| --- | --- | --- |
| Code-session Web Fetch, remote MCP, and plugin installs made by the embedded engine | `HTTPS_PROXY` with the session handle, in the Desktop settings directory | Session routes apply. Credentials are injected or the call is denied. |
| Chat and Cowork Web Fetch, MCP the app itself dials, marketplace downloads | App proxy, via the forwarder | The forwarder adds the same session handle. Routes then apply. |
| Commands inside the Cowork VM | The same app proxy, via the forwarder | Network calls that honor the proxy are governed. The command itself still passes PreToolUse. |
| Shell commands in the embedded engine | Existing PreToolUse hook | Allow, ask, or deny, same as Claude Code. |
| `POST /v1/messages` to `api.anthropic.com` | The engine proxy | Metered. A governed origin still wins over metering when the policy lists that host. |

A connection with no session handle does not use the session routes. The forwarder exists so the app and the VM can present one.

## Architecture

```text
keydris claude-desktop
  mint one session, register it with the daemon
  start forwarder on 127.0.0.1:<ephemeral>
  write engine proxy + app proxy
  exec Claude and wait

embedded Claude Code engine
  HTTPS_PROXY=http://keydris:<handle>@127.0.0.1:<proxy-port>
  CONNECT straight to the Keydris proxy

Electron app and Cowork VM
  egress proxy = http://127.0.0.1:<forwarder-port>
  CONNECT to the forwarder
  forwarder adds Proxy-Authorization and dials the Keydris proxy

Keydris proxy
  attributed CONNECT on a governed origin -> existing route enforcement
  attributed CONNECT on a metered LLM origin -> existing meter
  anything else -> opaque tunnel
```

The forwarder does not terminate TLS, read bodies, or choose routes. It only stamps `Proxy-Authorization: Basic base64(keydris:<handle>)` and dials `127.0.0.1` on `HTTPProxyPort`. It accepts connections from loopback only. Its handle is fixed for the process lifetime. Sole-session fallback stays off, so a terminal `keydris run -- claude` session and a Desktop session stay distinct.

## Isolation from Claude Code

`~/.claude/settings.json` is the Claude Code file. Writing the Desktop proxy URL there would also redirect a terminal `claude` at the Desktop session.

`keydris claude-desktop` launches the app binary with `CLAUDE_CONFIG_DIR` set to a Keydris-owned directory, default `~/.keydris-data/claude-desktop`. That directory holds the settings file the embedded engine reads: the session proxy env, `NODE_EXTRA_CA_CERTS`, and the same three hooks. Terminal Claude Code keeps `~/.claude`.

This inheritance has to be proven before the launcher is trusted. See the spikes below. If the embedded engine ignores `CLAUDE_CONFIG_DIR`, the fallback is to merge the proxy env into `~/.claude/settings.json` for the session and restore the previous file on every exit. Status then warns that terminal Claude Code shares the Desktop proxy until the session ends.

The app proxy is not `CLAUDE_CONFIG_DIR`. `egressProxyUrl` is Desktop's own setting. The published local store for third-party Desktop is `~/Library/Application Support/Claude-3p/configLibrary/`. A standard install on this machine keeps UI state in `~/Library/Application Support/Claude/config.json`, which is not the policy file and contains the OAuth token cache. The launcher must not write that file.

## Session lifecycle

1. Refuse to start when Claude is already running, or when a Desktop session file says a forwarder is still alive.
2. Mint and register one session. The runtime string sent to `POST /runtime/sessions` is `claude_desktop`.
3. Record the Claude pid as the session owner after the process starts. Peer checks are a no-op on macOS, so the handle is the real boundary.
4. Wait until Claude exits. On interrupt, quit Claude, then revoke.
5. Revoke the session, stop the forwarder, and restore every setting this command wrote.

`__session-start` inside this launch must not mint a second session. The Claude process receives `KEYDRIS_DESKTOP_SESSION=<id>`. When that variable is set, the hook loads the existing session, refreshes the subprocess proxy env, and returns. `__pretool-use` authorizes commands against the same session: the `session_id` in its payload belongs to the embedded engine and has no Keydris state. `__session-end` from the embedded engine does not revoke it. Only the launcher revokes. A `keydris run` nested inside Desktop keeps its own session in every hook. A terminal Claude Code session does not have this variable and keeps today's mint and revoke behavior.

A crash that skips the restore leaves `egressProxyUrl` set. Anthropic does not fall back to a direct connection when that proxy is down, so Claude would fail closed until the pin is removed. Every `keydris` command that loads config checks the Desktop session file. If the forwarder pid is dead, it restores the pin and the engine settings. `keydris status` reports a leftover pin as attention needed.

## Certificate trust

The embedded engine reads the macOS System keychain and ignores a certificate trusted only for SSL. `init claude-desktop` installs the Keydris CA with full root trust in `/Library/Keychains/System.keychain`. The login-keychain install used by `init claude-code --trust-store` is unchanged and is not sufficient for Desktop.

The engine settings also set `NODE_EXTRA_CA_CERTS` to the Keydris CA bundle. The Electron app trusts the OS store, so the System keychain install covers app-made TLS. Cowork guest trust is a separate spike: the VM is not the Mac keychain, and a guest `curl` is not governed until its handshake succeeds against the Keydris leaf.

## App proxy pin

Use `egressProxyUrl=http://127.0.0.1:<forwarder-port>`. It is fail closed: if the forwarder is down, Desktop does not connect directly. Embedded credentials are rejected by Desktop, which is why the forwarder exists.

`claude.ai` then traverses the forwarder as an opaque tunnel. The first manual run confirms that sign-in and an existing chat still load. If the app receives HTTP 403 from Cloudflare on that path, switch the pin to `egressProxyPacUrl` served by the forwarder. The PAC sends `claude.ai`, `platform.claude.com`, and `downloads.claude.ai` direct, and sends every other host to the forwarder. A PAC that fails to download makes Desktop connect directly, so the PAC is the fallback and the startup sweep remains mandatory.

Both keys are read once at launch. The command writes the pin, confirms the forwarder is accepting connections, then starts Claude.

## Plugins

Plugins are used inside the Desktop session. There is no separate command.

Remote HTTP and SSE MCP servers, including servers declared by a plugin or by `managedMcpServers`, are governed when their host is in the session routes. Chat and Cowork enter through the forwarder. Code enters through the engine proxy. An allowed `tools/call` follows `mcp_gateway` or `mcp_kit_reader`. A denied call is a tool error.

Skills, slash commands, and sub-agents in a plugin only change what Claude attempts. PreToolUse and the proxy still decide.

Local plugin programs are out of scope for this pass. A stdio MCP server, a `.mcpb` extension, and a plugin CLI inside the Cowork VM speak to Claude over stdin. HTTPS they open on their own is governed only when that process uses the proxy. Desktop starts those processes itself, and Keydris cannot wrap them. Status and the README say so. Inventory still lists them.

Plugin hooks keep running. They do not replace the Keydris command hook. If Claude Code managed settings on the machine set `disableAllHooks` or `allowManagedHooksOnly` in a way that drops the Keydris PreToolUse hook, `status` reports that shell commands are not gated.

Marketplace downloads are app traffic through the forwarder. They are governed when that host is in the policy. A public marketplace host stays an opaque tunnel.

MCP inventory gains a `claude_desktop` source. It reads configured servers and does not launch them: the user Desktop MCP config, managed MCP entries, and `.mcp.json` files from installed plugins. The report uses the existing inventory API with runtime `claude_desktop`.

## Control plane

`CreateKitSession` already sends `agent_runtime` as a string. The CLI will send `claude_desktop`. Shipping the launcher depends on the control plane accepting that value and showing it as its own runtime. Reuse of `claude_code` hides Desktop sessions in the console and is not the fallback.

## Commands and files

| Surface | Change |
| --- | --- |
| `keydris init claude-desktop <agent-id>` | Accept the target in `runInit`, the interactive menu, help, and `deinit`. |
| `keydris claude-desktop` | New launcher, next to `runCodex`. |
| `keydris deinit claude-desktop` | Remove the Desktop settings directory and a leftover pin. Leave the CA files, matching the other targets. |
| `keydris status` | Add target `claude-desktop`: CA present in the System keychain, hooks present, pin absent unless a session is active, forwarder pid matches the session file. |
| `keydris reset` | Include the Desktop settings directory and the Desktop skill. Do not delete `~/Library/Application Support/Claude`. |
| Agent skill | Install the existing authority skill into the Desktop `CLAUDE_CONFIG_DIR`. |
| README and `keydris help` | Document the two commands and the Dock boundary. |

New code:

| Piece | Role |
| --- | --- |
| `internal/cli/claude_desktop.go` | Launcher, restore, and the stale-pin sweep. |
| `internal/node/desktopproxy` | Loopback forwarder. Unit-tested without Claude. |
| `internal/node/sandbox` | Write and restore the engine settings and the `egressProxyUrl` pin. Verify both. |
| `internal/cli/session_hook.go` | Attach to `KEYDRIS_DESKTOP_SESSION` instead of minting. |
| `internal/cli/agent_runtime.go` | Constant `claude_desktop`. |
| `internal/node/sandbox/mcpinventory.go` | Read Desktop MCP config for inventory. |

`init` still owns identity, the CA, policy scope, and proxy startup. The launcher owns the per-session handle, the forwarder, and the pin.

## Spikes before implementation is trusted

These are manual checks on a Mac with Claude Desktop installed. Each one blocks the slice that depends on it.

1. Launch `/Applications/Claude.app/Contents/MacOS/Claude` with `CLAUDE_CONFIG_DIR` pointed at a temporary directory whose `settings.json` sets `HTTPS_PROXY`. Confirm the embedded engine uses that proxy. The log line is `Resolved system proxy` or the managed-settings override line in `main.log`.
2. Confirm which file a standard Desktop build reads for `egressProxyUrl`. Write a harmless pin, relaunch, and look for `[egress-proxy] pinned to fixed proxy`. Record that path in the sandbox writer. Do not use `config.json`.
3. Install the CA in the System keychain and complete one governed HTTPS call from the Code tab. Then repeat a Cowork shell `curl` to a governed test origin. If the guest fails certificate verification, Cowork shell governance stays listed as unverified.
4. Run one Chat turn and one sign-in through the forwarder. If `claude.ai` returns 403, implement the PAC fallback before calling the pin done.
5. Start a Code session under `keydris claude-desktop` and confirm `KEYDRIS_DESKTOP_SESSION` is visible to `__session-start`. If it is not, the hook will mint a second session and the launcher must refuse Code-tab governance until that is fixed.
6. `POST /runtime/sessions` with `agent_runtime=claude_desktop` against the dev control plane. A rejection blocks the launcher.

## Tests

- Forwarder: a CONNECT to the forwarder arrives at a test proxy with the expected `Proxy-Authorization`. A non-loopback client is refused. The upstream request is otherwise unchanged.
- Settings writer: apply and restore round-trip, including a file that already has user keys. A failed launch restores the previous pin.
- Hook: with `KEYDRIS_DESKTOP_SESSION` set, `__session-start` does not call mint, `__pretool-use` authorizes against the launch session instead of the payload's, and `__session-end` does not revoke. A nested `keydris run` keeps its own session.
- Stale pin: a session file whose pid is dead causes the sweep to restore.
- Inventory: a fixture `.mcp.json` and a fixture Desktop MCP config produce `claude_desktop` entries, including a stdio server marked local.
- `init` and `deinit` accept `claude-desktop` and leave `claude-code` settings untouched.
- No test launches Claude.

## Out of scope

- Governing model calls to `api.anthropic.com`. Those stay on the meter unless the policy lists that origin.
- Wrapping stdio MCP servers, `.mcpb` extensions, or plugin CLIs.
- A Dock or menu-bar session. The session starts only from `keydris claude-desktop`.
- Per-chat sessions. One launch has one handle.
- SSH remote Code sessions. The remote host does not receive the pin.
- OpenTelemetry as a second usage intake.
- Windows and Linux launch and trust, until the macOS path is proven.
