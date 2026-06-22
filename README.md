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

```bash
curl -fsSL https://raw.githubusercontent.com/nocaplabs/keydris-cli/main/install.sh | bash
```

This builds and installs `keydris` to `/usr/local/bin` (override with `PREFIX`).
Requires **Go 1.22+**. No third-party Go modules — stdlib only.

From a checkout instead:

```bash
make install            # -> /usr/local/bin/keydris  (or: PREFIX=$HOME/.local make install)
# or just build locally:
make build && ./bin/keydris status
```

## What it does

`keydris` gives a session a fresh **SPIFFE JWT-SVID**, routes the agent's egress
through a local proxy, and lets the control-plane **broker** inject the real
upstream credential on the wire on allow. The agent never holds the secret; an
unmodified client gets a `200` only because the proxy injected it.

Two data planes ship in this binary:

- **`sandbox`** (recommended, cross-platform): the TLS-terminating forward proxy
  Claude Code's sandbox routes all Bash-subprocess egress to. No root, no
  iptables. See [docs/sandbox.md](docs/sandbox.md).
- **`transparent`** (Linux + root): iptables `REDIRECT` + `SO_ORIGINAL_DST`,
  optionally race-free eBPF (`-tags ebpf`). See
  [docs/attribution.md](docs/attribution.md).
- **`proxyenv`**: kernel-free `HTTP_PROXY` fallback (bypassable; no attribution).
  See [docs/distribution.md](docs/distribution.md).

## Quickstart (Claude Code sandbox proxy)

With a control plane reachable (see below):

```bash
export KEYDRIS_DATAPLANE=sandbox

# 1. Configure Claude Code's sandbox: generate the Keydris CA and write the
#    sandbox block + session hooks + CA env into ~/.claude/settings.json.
keydris init claude-code            # add --trust-store to install the CA system-wide

# 2. Run the proxy data plane.
keydris up &

# 3. Confirm enforcement state (warns if the sandbox is disabled / not routed).
keydris status

# 4. Run a real session — its Bash-subprocess egress is now brokered & secretless.
claude
```

Without Claude Code, `keydris run` plays the sandbox's role, exercising the same
proxy/broker path:

```bash
KEYDRIS_DATAPLANE=sandbox keydris run -- curl -s https://your-api/
```

## Commands

```
keydris login                 Browser sign-in; stores a local client certificate
keydris whoami                Show the locally stored identity
keydris logout                Remove the locally stored identity
keydris up                    Run the proxy daemon (sandbox, or transparent on Linux/root)
keydris run -- <cmd...>       Run a command inside a keydris session
keydris status                Show config + sandbox enforcement state
keydris logs                  Print and verify the hash-chained evidence ledger
keydris hook session-start    Bind a session: mint SVID + register (Claude hook)
keydris hook session-end      End a session: revoke SVID + unregister
keydris init claude-code      Configure the Claude Code sandbox + CA + hooks
                                [--strict] [--trust-store]
keydris iptables-up|down      Install/remove the transparent redirect rules (root)
keydris enroll                Exchange a token for a node credential (legacy, root)
```

## Pointing at a control plane

The CLI authorizes against a running **keydris control plane**. Configure it via
environment, a local `.env` (see [.env.example](.env.example)), or a layered
`.keydris.toml` (see [.keydris.toml.example](.keydris.toml.example)). Precedence:
process env > `.env` > `./.keydris.toml` > `~/.keydris.toml` > defaults.

```bash
export KEYDRIS_CONTROL_ADDR=https://control.example.com:8080        # JWKS, login, health
export KEYDRIS_CONTROL_MTLS_ADDR=control.example.com:8443           # /authorize (mTLS)
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
