# Keydris on Claude Code's sandbox (v2)

This is the design in [plan_v1.md](../plan_v1.md): instead of building a per-OS
kernel data plane, Keydris rides Claude Code's sandbox as the **custom proxy** it
documents, plus the per-session identity broker.

## Why

Claude Code's sandbox already forces all Bash-subprocess egress through a local
proxy and blocks bypass at the kernel (bubblewrap on Linux/WSL2, Seatbelt on
macOS). It is explicitly extensible: you register a custom proxy by port and
install a CA it trusts. That is exactly the substrate a credential-injecting
broker needs, maintained by Anthropic and cross-platform. So Keydris stops
re-implementing kernel interception three times (eBPF / Network Extension / WFP)
and becomes that proxy.

## Flow

```
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
  - merges any `allowedDomains`;
- merges the `SessionStart`/`SessionEnd` hooks (the internal
  `keydris __session-start` / `keydris __session-end` entrypoints);
- points the agent's subprocess tools at the CA via the settings `env` block
  (`NODE_EXTRA_CA_CERTS`, `SSL_CERT_FILE`, `CURL_CA_BUNDLE`, `GIT_SSL_CAINFO`,
  `REQUESTS_CA_BUNDLE`). `--trust-store` additionally installs the CA into the OS
  trust store (best-effort, may need privileges).

`SessionStart` mints a per-session SPIFFE JWT-SVID and registers its handle with
the daemon; `SessionEnd` revokes it. `keydris status` reports whether the
sandbox is still enabled and routed to Keydris (enforcement drift).

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

## Coverage and trust model

- **Coverage is Bash-subprocess egress.** The sandbox isolates Bash and its
  children (`curl`, `git`, `npm`, `gh`, build scripts). Claude Code's own MCP,
  WebFetch, and model-API calls run under different boundaries and are not routed
  to the proxy.
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

Needs a running control plane and a prior `keydris login` (the per-session SVID
is minted over mTLS). Then:

```bash
keydris login
keydris init claude-code <policy-id>
keydris proxy up
claude   # each session's Bash egress is brokered, secretless, and per-session attributed
```
