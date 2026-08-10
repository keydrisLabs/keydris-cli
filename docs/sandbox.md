# Keydris on Claude Code's sandbox (v2)

Instead of building a per-OS kernel data plane, Keydris rides Claude Code's
sandbox as the **custom proxy** it documents, plus the per-session identity
broker.

## Why

Claude Code's sandbox already forces all Bash-subprocess egress through a local
proxy and blocks bypass at the kernel (bubblewrap on Linux/WSL2, Seatbelt on
macOS). It is explicitly extensible: you register a custom proxy by port and
install a CA it trusts. That is exactly the substrate a credential-injecting
broker needs, maintained by Anthropic and cross-platform. So Keydris stops
re-implementing kernel interception three times (eBPF / Network Extension / WFP)
and becomes that proxy.

## Flow

```text
Claude session  ── Bash subprocess egress (curl/git/npm) ──►  Keydris proxy
   (sandboxed)         (forced through proxy by the sandbox)     :15001
                                                                   │
                                                  CONNECT host:443 │ terminate TLS with
                                                                   │ a leaf signed by the
                                                                   │ Keydris CA (trusted
                                                                   │ in the sandbox)
                                                                   ▼
                              resolve per-session handle ─► SVID ─► broker /authorize
                                                                   │ allow + real credential
                                                                   ▼
                                       inject credential, re-originate TLS to the real upstream
```

`keydris init claude-code` configures this once:

- generates and persists the Keydris CA (`.keydris-data/ca.crt` + `ca.key`);
- writes the `sandbox` block to `~/.claude/settings.json`:
  - `sandbox.enabled: true`
  - `sandbox.network.httpProxyPort: <proxy port>`
  - `sandbox.failIfUnavailable: true` and `sandbox.allowUnsandboxedCommands: false`
    (with `--strict`, the default) so the sandbox is a hard gate;
  - `sandbox.enableWeakerNetworkIsolation: true` on macOS (required for MITM with
    a custom CA under Seatbelt);
  - merges any `sandbox.network.allowedDomains`;
- merges the `SessionStart`/`SessionEnd` hooks (the internal
  `keydris __session-start` / `keydris __session-end` entrypoints) and a
  `PreToolUse` hook (`keydris __pretool-use`) that gates every shell command
  against the policy's command rules — see [Command gating](#command-gating);
- builds an owner-only bundle containing the platform roots plus the Keydris CA,
  then points the agent's subprocess tools at that bundle via the settings `env` block
  (`NODE_EXTRA_CA_CERTS`, `SSL_CERT_FILE`, `CURL_CA_BUNDLE`, `GIT_SSL_CAINFO`,
  `REQUESTS_CA_BUNDLE`). Keeping the platform roots is required for opaque
  unmanaged CONNECT traffic. `--trust-store` additionally installs the CA into
  the OS trust store (best-effort, may need privileges). Platforms without a
  discoverable PEM root bundle must set `SSL_CERT_FILE` to one before init;
  Keydris fails setup rather than replacing public roots with its private CA.
  Windows is the exception: its public roots live in the certificate store, so
  Keydris sets only Node's additive `NODE_EXTRA_CA_CERTS` by default and
  `--trust-store` installs the CA into the current user's Windows root store.

`SessionStart` mints a per-session runtime session (KIT) and registers its
handle with the daemon; `SessionEnd` revokes it. If Claude emits another start
for the same session after resume, clear, or compaction, Keydris revokes the
old instance before replacing it. `keydris status` reports whether the
sandbox is still enabled and routed to Keydris (enforcement drift).

## Command gating

The same settings file carries a fourth hook alongside SessionStart/SessionEnd:
`hooks.PreToolUse` → `keydris __pretool-use` (matcher `Bash`, or
`Bash|PowerShell` on Windows). Claude Code sends every shell command through it
before running it; the hook forwards the command to
`POST /v1/runtime/commands/authorize` with the session's KIT and returns the
matching `permissionDecision` (`allow` / `ask` / `deny`). It fails closed: any
error — no active session, control plane unreachable, a timeout — is an
explicit `deny`, because a crashed or silent hook would otherwise fail *open*.
`keydris deinit claude-code` removes this hook along with the rest of the
Keydris configuration.

## Per-session attribution

Each session is bound to a random per-session **token** at SessionStart. The
internal `keydris __session-start` hook mints the SVID, registers the session
under its token, and appends a token-bearing proxy URL
(`HTTPS_PROXY=http://keydris:<token>@127.0.0.1:<port>`) to Claude Code's
`$CLAUDE_ENV_FILE`, which Claude Code applies to every Bash subprocess in the
session. The proxy attributes a connection by:

1. the `Proxy-Authorization` token on the request — looked up in the session
   registry; a present-but-unknown token is treated as **unattributed**, never
   downgraded to "the sole session"; else
2. (no token) the sole registered session — the single-session convenience case.

Because each concurrent session runs its own SessionStart hook and gets its own
token, **concurrent sessions through one proxy are attributed independently** —
no per-session port required. Caveat: the Claude path depends on
`$CLAUDE_ENV_FILE` and on a hook-set `HTTP_PROXY` composing with the sandbox's
`httpProxyPort` routing, which is undocumented and may vary by Claude Code
version; re-verify on upgrades. `keydris run` does not depend on it. See
[attribution.md](attribution.md).

The daemon renews each registered session before its KIT expires. Renewal
keeps the same attribution token and process owner, atomically replaces the
live SVID and owner-only hook state, refreshes runtime routes, and makes
SessionEnd revoke the latest replacement ULID. A session that ends during
renewal causes the unused replacement to be revoked. For `keydris run`, the
daemon additionally binds renewal to the wrapped process's PID and creation
identity and retires an abandoned session after a short grace period. Claude's
standalone command-hook parent remains advisory because it can be a short-lived
shell; Claude continues to use its SessionStart/SessionEnd lifecycle unchanged.

## Managed destination scope

By default every destination is managed, matching the original proxy behavior.
For a narrower boundary, configure exact origins:

```bash
keydris proxy scope add docs.mcp.cloudflare.com:443
keydris proxy scope list
keydris proxy down && keydris proxy up
```

The first `scope add` switches to selected mode. Managed HTTPS origins are
terminated with the Keydris CA, sent to `POST /agent/authorize`, and optionally
credential-injected. Unlisted HTTPS origins use an opaque CONNECT tunnel: the
proxy does not terminate TLS, inspect bodies, call the broker, or mutate
headers. `keydris proxy scope all` restores the backward-compatible all-managed
mode.

This scope is separate from Claude's `sandbox.network.allowedDomains`: the
sandbox list controls where Claude may connect, while proxy scope controls
where Keydris governs and injects.

## Coverage and trust model

- **Default coverage is Bash-subprocess egress.** The sandbox isolates Bash and its
  children (`curl`, `git`, `npm`, `gh`, build scripts). Claude Code's own MCP,
  WebFetch, and model-API calls run under different boundaries and are not routed
  to the sandbox proxy. `keydris run -- claude` explicitly supplies the proxy
  environment before Claude starts, so its native remote-HTTP MCP client is
  covered too; wrapper-owned hooks reuse that session instead of minting a
  second identity.
- **Un-bypassability comes from the sandbox**, not from Keydris. It holds only
  while the sandbox is enabled; `keydris init` locks it and `keydris status`
  surfaces drift. A user who disables the sandbox loses enforcement.
- **TLS termination is sanctioned** via the custom-proxy + CA-in-sandbox path, so
  certificate pinning is a non-issue inside the sandbox. Keydris does see
  decrypted traffic (it is the broker).
- **macOS `enableWeakerNetworkIsolation`** is required for MITM and is itself a
  documented exfiltration vector; the Claude Code docs call this out.
- **Secretless still holds.** Real upstream credentials live only in the broker;
  the agent and the proxy logs never contain them; the agent presents only its
  per-session handle/SVID.
- **Config-surface coupling.** The integration depends on Claude Code's
  documented sandbox keys; re-verify on Claude Code upgrades.

## Try it

Needs a running control plane and an agent id (a UUID created in the Keydris
console). `init` runs the browser sign-in itself if this device has no valid
identity bound to that agent yet:

```bash
keydris init claude-code <agent-id>
keydris proxy up
claude   # each session's Bash egress is brokered, secretless, and per-session attributed
```
