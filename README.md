# keydris-cli

The **`keydris` command-line client** — per-session cryptographic identity and
brokered, secretless egress for an **unmodified** agent.

This repo is the **agent/client side** of Keydris, packaged to install on its
own. It is the `keydris` binary extracted from the
[Keydris POC](https://github.com/nocaplabs/keydris): the CLI, the proxy data
plane, session attribution, and Claude Code integration. The **control plane**
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

**From source** (developers):

```bash
make install                 # build + install to /usr/local/bin  (PREFIX=$HOME/.local make install)
make build && ./bin/keydris status
go install github.com/keydrisLabs/keydris-cli/cmd/keydris@latest   # once the module is reachable
```

**Cutting a release** (`make dist` cross-compiles darwin/linux × amd64/arm64 with
checksums; `make release` publishes to S3). CI does this automatically —
tag push `v*` → `stable`, push to `main` → `dev`. Full runbook (channels, repo
vars, manual publish) in [docs/releasing.md](docs/releasing.md).

## What it does

`keydris` gives a session a fresh **SPIFFE JWT-SVID**, routes the agent's egress
through a local proxy, and lets the control-plane **broker** inject the real
upstream credential on the wire on allow. The agent never holds the secret; an
unmodified client gets a `200` only because the proxy injected it.

Proxy scope controls which exact origins receive that treatment. In `all` mode
(the backward-compatible default), every destination is managed. In `selected`
mode, only configured `host:port` origins are TLS-terminated, authorized, and
credential-injected; all other HTTPS traffic uses an opaque CONNECT tunnel.
Selected hostname scopes require the explicit `sandbox`/`proxyenv` planes;
Linux transparent mode can safely scope only exact IP literals.

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
  See [docs/distribution.md](docs/distribution.md).

## Quickstart (Claude Code sandbox proxy)

With a control plane reachable (see below):

```bash
# 1. Sign in. The per-session SVID is minted over mTLS with this identity, so
#    this must happen before any session starts.
keydris login

# 2. Configure Claude Code's sandbox for a governance policy: generate the
#    Keydris CA and write the sandbox block + CA env + per-session SVID hooks
#    into ~/.claude/settings.json.
keydris init claude-code <policy-id>   # add --trust-store to install the CA system-wide

# 3. Optional: govern only selected origins; everything else passes unchanged.
keydris proxy scope add api.example.com:443

# 4. Start the brokered egress proxy in the background (no `&` needed).
keydris proxy up

# 5. Confirm enforcement state and active proxy scope.
keydris status

# 6. Run a real session. Claude Code fires the wired SessionStart hook, which
#    mints a per-session SPIFFE SVID and registers it; the proxy attributes the
#    session's egress to that identity, brokered and secretless.
claude
```

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

## Commands

```text
keydris login                      Browser sign-in; stores a local client certificate
keydris whoami                     Show the locally stored identity
keydris logout                     Remove the locally stored identity
keydris init claude-code <policy>  Configure the Claude Code sandbox + CA for a policy id
                                     [--strict] [--trust-store]
keydris deinit claude-code         Undo init: remove the Keydris sandbox config + policy id
keydris proxy up                   Start the brokered egress proxy in the background
keydris proxy down                 Stop the background proxy
keydris proxy scope add <origin>   Manage only selected host:port origins
keydris proxy scope remove <origin>
keydris proxy scope list|all       Inspect scope or restore all-managed mode
keydris run -- <cmd...>            Run a command inside a keydris session
keydris status                     Show config + sandbox enforcement state
keydris logs                       Print and verify the hash-chained evidence ledger
```

## Pointing at a control plane

The CLI authorizes against a running **keydris control plane**. Configure it via
environment, a local `.env` (see [.env.example](.env.example)), or a layered
`.keydris.toml` (see [.keydris.toml.example](.keydris.toml.example)). Precedence:
process env > `.env` > `./.keydris.toml` > `~/.keydris.toml` > defaults.

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

This is an extracted POC. Known hardening items (session-socket authentication,
fail-closed audit, fail-closed identity fallback, proxy SSRF/canonicalization)
are tracked in [SECURITY.md](SECURITY.md). Read it before relying on this in any
adversarial setting.

`keydris logs` and `proxy.log` include full JSON tool parameters for managed
authorization calls. Owner-only permissions are enforced, but those files may
still contain application secrets and must be handled accordingly.
