# keydris-cli

The **`keydris` command-line client** — per-session cryptographic identity and
brokered, secretless egress for an **unmodified** agent.

This repo is the **agent/client side** of Keydris, packaged to install on its
own. It is the `keydris` binary extracted from the
[Keydris POC](https://github.com/nocaplabs/keydris): the CLI, the proxy data
plane, session attribution, and Claude Code/OpenAI Codex integrations. The **control plane**
(issuer + broker + grant store) is a separate service the CLI talks to over
mTLS — see [Pointing at a control plane](#pointing-at-a-control-plane).

## Install

`install.sh` downloads a **prebuilt, static binary** (no Go, no checkout) and
installs it to `/usr/local/bin`:

```bash
curl -fsSL https://dev.get.keydris.com/keydris-cli/install.sh | bash                     # stable
curl -fsSL https://dev.get.keydris.com/keydris-cli/install.sh | KEYDRIS_CHANNEL=dev bash # dev (zero-config)
```

- The **dev** channel also drops a `~/.keydris.toml` pointing at the dev control
  plane + IdP, so it works with **no env vars set**.
- Override via env: `PREFIX` (install dir), `KEYDRIS_CHANNEL` (`stable`|`dev`),
  `KEYDRIS_VERSION` (default `latest`), `KEYDRIS_BASE_URL` (download host).
- `keydris version` reports the installed build.

**npm distribution** (after the packages are published):

```bash
npm install --global @keydris/cli
keydris init
```

The npm package selects a prebuilt native binary for Windows, macOS, or Linux;
it does not replace the security-sensitive Go runtime with JavaScript. A global
installation is recommended for the background proxy. See
[docs/npm-distribution.md](docs/npm-distribution.md).

**From source** (developers):

```bash
make install                 # build + install to /usr/local/bin  (PREFIX=$HOME/.local make install)
make build && ./bin/keydris status
go install github.com/keydrisLabs/keydris-cli/cmd/keydris@latest   # once the module is reachable
```

On Windows, build natively with:

```powershell
go build -o bin\keydris.exe .\cmd\keydris
```

**Cutting a release** (`make dist` cross-compiles darwin/linux × amd64/arm64 with
checksums; `make release` publishes to S3). CI does this automatically —
tag push `v*` → `stable`, push to `main` → `dev`. Full runbook (channels, repo
vars, manual publish) in [docs/releasing.md](docs/releasing.md).

## What it does

`keydris` gives a session a fresh cryptographic identity, routes the agent's
egress through a local proxy, and keeps the real upstream credential off the
agent's machine entirely. For origins the agent's policy governs (GitHub,
Slack, MCP), the control plane executes the request itself and relays back the
response — see [Runtime enforcement](#runtime-enforcement). For everything
else, the older **broker** path injects the real upstream credential on the
wire on allow: the agent never holds the secret, and an unmodified client gets
a `200` only because the proxy injected it.

Proxy scope controls which exact origins receive that credential-injecting
treatment, and **you never configure it by hand**. `keydris init` reads the
origins the agent's policy governs and persists them as the effective scope;
every session start refreshes them, so a policy change lands without a
re-init. Only those `host:port` origins are TLS-terminated, authorized, and
credential-injected — all other HTTPS traffic uses an opaque CONNECT tunnel.
Inspect the result with `keydris proxy scope list`.

Scope is deliberately origin-only, even though a policy route can also narrow
a path prefix: the proxy must terminate TLS for the whole origin before it can
see a path at all. Per-path enforcement still happens per request; scope only
decides what gets inspected.

If scope was never detected (a fresh install, or an `init` that could not
reach the control plane), Keydris falls back to `all` mode and manages every
ungoverned destination — the backward-compatible default. Hostname scopes
require the `sandbox`/`proxyenv` planes; Linux transparent mode can safely
scope only exact IP literals.

**Concurrent sessions are isolated.** Each Claude session gets a distinct
per-session token (carried in `Proxy-Authorization` via Claude Code's
`$CLAUDE_ENV_FILE`), so the proxy attributes every connection to the right
session — multiple sessions can share one proxy without cross-attribution. See
[docs/attribution.md](docs/attribution.md).

Three data planes ship in this binary; `sandbox` is the default, so no
configuration is needed. Override with `KEYDRIS_DATAPLANE` only for the others:

- **`sandbox`** (default, cross-platform): the TLS-terminating forward proxy
  Claude Code's sandbox routes all Bash-subprocess egress to. No root, no
  iptables. See [docs/sandbox.md](docs/sandbox.md).
- **`transparent`** (Linux + root): iptables `REDIRECT` + `SO_ORIGINAL_DST`,
  optionally race-free eBPF (`-tags ebpf`). See
  [docs/attribution.md](docs/attribution.md).
- **`proxyenv`**: kernel-free `HTTP_PROXY` fallback (bypassable; token-attributed).
  See [docs/attribution.md](docs/attribution.md).

## Runtime enforcement

At session start, the SessionStart hook mints a **runtime session** over mTLS
(`POST /runtime/sessions`, with an `Idempotency-Key`) and gets back a **KIT** —
a short-lived SPIFFE JWT-SVID bearer token. It then fetches the session's
**routes** (`GET /v1/runtime/routes`, KIT bearer): the origins the agent's
policy governs, each tagged with an enforcement mode and the specific
resources (repos, channels, tools) selected for that agent, addressed by
stable routing keys. The daemon registers the session — KIT and routes
included — over its authenticated local socket, and every proxied request is
matched against those routes before anything reaches the network.

| Enforcement mode | What happens |
| --- | --- |
| `provider_executor` | The request (GitHub, Slack) is relayed to the control plane's executor endpoint (`POST /v1/runtime/providers/<provider>/execute`) with the stable resource id resolved from the request — GitHub: `owner/repo` from the path → `github.full_name` (case-insensitive); Slack: the request body's `channel` → `slack.channel_id`. The control plane authorizes and executes upstream with the org credential; the agent never holds it. |
| `mcp_gateway` | JSON-RPC `tools/call` / `resources/read` calls are relayed through `POST /v1/runtime/mcp/gateway`. |
| `mcp_kit_reader` | Not part of this CLI build — the daemon fails closed rather than pass governed traffic through unenforced. |
| origin not governed by any route | Falls back to the legacy broker path (`/agent/authorize`) and the proxy-scope rules described above. |

Every decision the CLI makes here is re-enforced server-side at execution
time. A repeated SessionStart for the same logical session (Claude
compaction/resume, a Codex retry) revokes the previous instance first;
SessionEnd revokes on exit.

## Quickstart (Claude Code sandbox proxy)

With a control plane reachable (see below) and an **agent id** — a UUID an
operator creates for this integration in the Keydris console, where the
governing policy is assigned; the CLI never selects or overrides a policy:

```bash
# 1. Configure Claude Code's sandbox: generate the Keydris CA and write the
#    sandbox block + CA env + SessionStart/SessionEnd/PreToolUse hooks into
#    ~/.claude/settings.json. If no identity bound to this agent exists yet,
#    init finishes with a browser sign-in that binds this device to it. It
#    then detects the origins this agent's policy governs and prints them —
#    there is no scope to configure by hand.
keydris init claude-code <agent-id>   # add --trust-store to install the CA system-wide

# 2. Start the brokered egress proxy in the background (no `&` needed).
keydris proxy up

# 3. Confirm enforcement state and the detected proxy scope.
keydris status

# 4. Run a real session. Claude Code fires the wired SessionStart hook, which
#    mints a runtime session (KIT) and registers it; the proxy attributes the
#    session's egress to that identity, brokered and secretless, and every
#    Bash command the agent runs is checked against the policy's command rules.
claude
```

Running `keydris init` without arguments opens an interactive setup menu that
prompts for the same choices (Claude Code or OpenAI Codex, then the agent id).
Scripts can continue using the explicit commands shown above. `keydris login`
also works standalone (e.g. to re-authenticate); `init` runs it automatically
when needed.

## Quickstart (OpenAI Codex)

Codex does not currently expose a reliable end-of-session hook. Keydris
therefore owns the lifecycle by wrapping the Codex process:

```bash
keydris init codex <agent-id>        # `init openai` is also accepted; binds via browser sign-in
keydris proxy up
keydris codex                        # pass normal Codex arguments after this
```

The wrapper enables Codex's sandboxed network proxy, chains it through Keydris,
mints and registers one session before Codex starts, and revokes it when Codex
exits. Because scope is detected from the policy, Codex's own model traffic
stays an opaque tunnel unless the policy governs it. Launch Codex through
`keydris codex`, not directly, when Keydris
governance is required. See [docs/codex.md](docs/codex.md).

The sandbox data plane is the default, so you never need to set
`KEYDRIS_DATAPLANE`. Without Claude Code, `keydris run` plays the sandbox's role,
exercising the same proxy/broker path:

```bash
keydris run -- curl -s https://your-api/
```

To cover Claude Code's native remote-HTTP MCP client (not only its sandboxed
Bash children), launch the process itself through the proxy:

```bash
keydris run -- claude
```

## Command gating

A policy can also carry **command rules** — glob patterns over the full shell
command line (`git push*` also matches `git push --force`) — with `allow`,
`require_approval`, or `reject` effects. Each shell command the agent runs is
sent to `POST /v1/runtime/commands/authorize` with the session's KIT, and the
decision maps to the harness's own permission verdict:

- **Claude Code**: `keydris init claude-code` wires a `PreToolUse` hook
  (`keydris __pretool-use`, matcher `Bash` — `Bash|PowerShell` on Windows)
  into `~/.claude/settings.json`, alongside the SessionStart/SessionEnd hooks.
- **Codex**: `keydris init codex` writes `~/.codex/hooks.json` with two hooks —
  `PreToolUse` (`keydris __pretool-use --codex`, deny-only) and
  `PermissionRequest` (`keydris __permission-request`, which auto-allows
  policy-allowed commands and stays silent otherwise so approval-required
  commands land on Codex's interactive prompt). Pair it with
  `approval_policy = "untrusted"`, and run `/hooks` once inside Codex to trust
  the new entries. Details: [docs/codex.md](docs/codex.md).

**Fail-closed**: every error path — no active session, control plane
unreachable, request timeout — produces an explicit deny, never a silent
allow. `keydris status` reports whether the hooks are wired; `keydris deinit
claude-code|codex` removes them.

## Commands

Kept in sync with `usage()` in [internal/cli/cli.go](internal/cli/cli.go):

```text
keydris login                      Browser sign-in; stores a local client certificate
                                     [--email you@example.com] [--no-browser]
keydris whoami                     Show the locally stored identity
keydris logout                     Remove the locally stored identity
keydris init                       Interactive agent setup
keydris init claude-code <agent>   Configure Claude Code sandbox + CA
                                     [--strict] [--trust-store]
keydris init codex <agent>         Configure OpenAI Codex + CA
                                     [--trust-store]
keydris deinit claude-code|codex   Undo init: remove the Keydris config
keydris proxy up                   Start the brokered egress proxy in the background
keydris proxy down                 Stop the background proxy
keydris proxy scope list           Show the origins detected from the agent's policy
keydris run -- <cmd...>            Run a command inside a keydris session
keydris codex [args...]            Run OpenAI Codex inside a keydris session
keydris status                     Show config + sandbox enforcement state
keydris logs                       Print and verify the hash-chained evidence ledger
keydris upgrade                    Download & replace the binary with the latest release
                                     [--channel stable|dev] [--version <v>] [--no-config]
keydris version                    Print the version
keydris help                       Show this help
```

`<agent>` is the agent id (a UUID) created for this integration in the Keydris
console; the policy that governs it is assigned there, not on the command line.

## Pointing at a control plane

The CLI authorizes against a running **keydris control plane**. Configure it via
the process environment or trusted user-level `~/.keydris.toml` (see
[.keydris.toml.example](.keydris.toml.example)). Precedence is process
environment > user config > defaults.

Project-local `.env` and `.keydris.toml` files are ignored by default because
allowing a repository to redirect OAuth, identity, and control-plane endpoints
would cross a trust boundary. To use project-local configuration intentionally,
set `KEYDRIS_TRUST_PROJECT_CONFIG=1` in the process environment before invoking
Keydris; trusted user settings still take precedence.

```bash
export KEYDRIS_CONTROL_URL=https://api.keydris.com                  # /identity/sign, /agent/jwks (:443)
export KEYDRIS_CONTROL_MTLS_URL=https://api.keydris.com:8443        # /agent/authorize* (mTLS)
export KEYDRIS_MANAGED_MODE=selected
export KEYDRIS_MANAGED_DESTINATIONS=docs.mcp.cloudflare.com:443
```

`keydris login` signs in through the browser (OAuth 2.0 Authorization Code +
PKCE) and stores a **client certificate** under `~/.keydris-data/identity/`; the
private key never leaves the machine. It works against the control plane's
built-in mock IdP out of the box, or a real OIDC provider (e.g. AWS Cognito) —
set `KEYDRIS_OIDC_ISSUER` and the OAuth vars (see `.env.example`).

## Security

This is an extracted POC. Known hardening items (peer-derived session
attribution, SVID verification, fail-closed audit, and proxy SSRF protection)
are tracked in [SECURITY.md](SECURITY.md). Read it before relying on this in any
adversarial setting.

`keydris logs` and `proxy.log` include full JSON tool parameters for managed
authorization calls. Owner-only permissions are enforced, but those files may
still contain application secrets and must be handled accordingly.
