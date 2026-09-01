# Security Policy

`keydris-cli` exists to take credentials off an agent's machine, so a defect here can put one back on it — or hand one to the wrong session. Reports are taken seriously and handled privately.

> **Status.** This is an extracted **proof of concept** of the identity and egress model. It is not hardened. The [known hardening gaps](#known-hardening-gaps) below are tracked against production agent-governance practice and are **open, not closed**. Read that section before relying on this in an adversarial setting.

---

## Reporting a vulnerability

**Do not open a public issue, and do not post it in Discord.**

Use one of these, in order of preference:

1. **GitHub private vulnerability reporting**, via the *Security* tab on [keydrisLabs/keydris-cli](https://github.com/keydrisLabs/keydris-cli/security/advisories/new). This keeps the report, the discussion, and the eventual advisory in one place.
2. **Email**, to [security@keydris.com](mailto:security@keydris.com). Please put `keydris-cli` in the subject line.

Include whatever you have:

- The version (`keydris version`) or a commit SHA, and the install channel (`stable`, `dev`, or npm)
- Which component: the CLI, the proxy daemon, a data plane, the harness integration, the hooks, or the release pipeline
- The data plane in use (`sandbox`, `transparent`, `proxyenv`) and the operating system
- A request, a routes response, or a hook payload that reproduces it
- What an attacker gains, and what they need in order to get it

Please redact real credentials, KITs, and session tokens from anything you send. A prefix and a length are enough. If you are attaching a `proxy.log` or `evidence.jsonl` excerpt, note that those files deliberately contain full request bodies — scrub them first.

### What to expect

| Stage | Target |
| --- | --- |
| Acknowledgement | 3 business days |
| Initial assessment, with a severity and a plan | 10 business days |
| Fix released for a confirmed high or critical issue | 30 days, sooner where practical |
| Public advisory | With the fix, or coordinated with you |

We will keep you updated while a fix is in progress, credit you in the advisory unless you would rather stay anonymous, and tell you — with the reasoning — if we conclude a report is not a vulnerability. We do not currently run a paid bounty.

### Coordinated disclosure

Please give us 90 days before disclosing publicly, or less if we ship a fix sooner. If a report turns out to affect the Keydris **control plane** rather than this CLI, we will route it internally and tell you that we have.

---

## Supported versions

The CLI is pre-1.0 and ships from two channels out of one codebase. Fixes land on `main` and reach `dev` immediately; `stable` gets them on the next tag. There are no long-term support branches yet.

| Artifact | Version | Supported |
| --- | --- | --- |
| `stable` channel (`get.keydris.com`) | latest tag | Yes |
| `dev` channel (`dev.get.keydris.com`) | latest `main` | Yes |
| `@keydris/cli` and its six native packages | `latest` dist-tag | Yes |
| `@keydris/cli` under the `next` dist-tag | current dev prerelease | Best effort |
| Anything older | n/a | No |

Once 1.0 ships, this table will name a supported range rather than a channel tip.

---

## Scope

### In scope

- The `keydris` binary and everything under [`internal/`](internal/) and [`cmd/`](cmd/)
- The proxy daemon and all three data planes
- The harness integrations: what `keydris init` writes into `~/.claude/settings.json` and `~/.codex/hooks.json`, and the hooks themselves
- The session socket, the session state files, and the evidence ledger
- The runtime contract decoders in [`internal/runtimecontract/`](internal/runtimecontract/)
- The published artifacts — the S3/CloudFront installer, `install.sh`, and the seven `@keydris` npm packages — and [`release.yml`](.github/workflows/release.yml)

Findings we especially want to hear about:

- A path where a KIT, a per-session proxy token, or an injected credential reaches a log, an error message, the evidence ledger, or the agent
- A way for one session's request to be attributed to another session's identity
- A request that is enforced against a resource other than the one it names, or that reaches an upstream the routes do not govern
- A body, path, or header shape that makes the router match the wrong route, or match none and pass through when it should have been rejected
- Any fail-**open** branch in the enforcement path or in the command-gating hooks
- A way to make the CLI send a KIT anywhere other than the configured control plane
- A way to reach the session socket, or to register or unregister a session, without the per-install secret
- Supply-chain issues in the release pipeline: checksum handling, the channel binding, dependency confusion on any `@keydris` package name

### Out of scope

- **The Keydris control plane** — issuer, broker, policy engine, executors, gateway, and vault. Not in this repository; report through [keydris.com](https://keydris.com/) or the email above. The two control-plane items listed below are recorded here only for completeness.
- Vulnerabilities in Claude Code, the OpenAI Codex CLI, Go, npm, or AWS. Report those upstream; tell us if this repository needs a mitigation.
- A user disabling their own sandbox, unsetting `HTTP_PROXY` on the `proxyenv` plane, or removing the hooks from their own settings file. Those are documented, deliberate boundaries — see [what this depends on](#what-this-depends-on-and-cannot-enforce).
- What an upstream does after the control plane's executor calls it.
- Reports generated by a scanner with no demonstrated impact.

---

## The threat model

### What the CLI defends against

- **A credential at rest on the agent's machine.** For governed origins there is nothing to steal between requests. The real credential is either applied by the control plane's executor or injected by the proxy on the wire; an attacker has to be present during an authorized call.
- **A request enforced against the wrong thing.** The routing value is resolved from the request itself — `owner/repo` from a GitHub path, `channel` from a Slack body — never from a header or a claim the agent supplies freely. The resource must be one the session's policy selected, and must be `ready`.
- **Ambiguous authorization.** Two matching routes are refused rather than resolved: choosing one would be a guess about which credential and which mode the operator meant.
- **Ambiguous input.** Duplicate JSON keys are rejected before a body is interpreted or modified, because parsers disagree about which value wins and a decision made on one reading could be executed on another. Bodies are capped at 1 MiB; hook payloads at 10 MiB.
- **Response-shape confusion.** Provider, gateway, and decision responses are decoded with unknown fields rejected, and validated as state machines — `execution_status` must be internally consistent with `decision`, a JSON-RPC id must echo, and a `decision` is valid only with a `reason_code` from its own set. Backend/CLI enum drift cannot silently become a permission.
- **Credentials spent on discovery.** MCP lifecycle and discovery traffic never reaches the control plane, so nothing is authorized for a request that needs no authority.
- **Cross-session borrowing.** A `Proxy-Authorization` token that is presented but unknown resolves to *unattributed*; it is never downgraded to "the sole registered session". A request with no token is anonymous by default.
- **Origin smuggling.** The inner request's authority must match the CONNECT target, and a present TLS SNI must match it too. Origins are canonicalized for case, trailing dots, IP-literal form, and port before any comparison.
- **A token replayed for a different action.** The MCP action intent is canonicalized with RFC 8785 and hashed, so the control plane binds the minted token to the exact action and parameters. A token whose `expires_at` is not in the future is refused before injection.
- **Fail-open hooks.** Both harnesses fail open when a hook crashes, times out, or prints invalid JSON, so every error path in the gating hooks emits an explicit deny and exits 0 — including the verdict-encoding failure path, which has a hard-coded literal.
- **An unrecorded allow.** On the legacy broker path the audit record is appended *before* the decision is acted on, and a failed append rejects the request.
- **A silently edited ledger.** Records are hash-chained; `keydris logs` recomputes the chain and reports the first inconsistency.
- **PID reuse.** `proxy down` and the renewal loop compare an OS process-creation identity, not just a PID, so a recycled PID is never signalled or trusted.
- **A repository redirecting the control plane.** Project-local `.env` and `.keydris.toml` are ignored unless `KEYDRIS_TRUST_PROJECT_CONFIG=1` is set in the process environment.
- **A cross-channel binary.** Each install host serves only its own channel and 403s the other; the rendered installer refuses a mismatched `KEYDRIS_CHANNEL`, and `keydris upgrade` mirrors the same map — pinned to the installer by [`channel_binding_test.go`](internal/cli/channel_binding_test.go). Downloads are checksum-verified before install.

### What this depends on and cannot enforce

These are properties of the environment, not of this code. No change here can create them.

1. **Un-bypassability comes from the sandbox.** The strong guarantee holds only while Claude Code's OS sandbox (bubblewrap on Linux/WSL2, Seatbelt on macOS) is enabled and routed to the Keydris proxy, or while the Linux `transparent` plane's iptables redirect is installed. `keydris init --strict` locks the sandbox as a hard gate and `keydris status` surfaces drift, but a user with write access to their own settings can turn it off and lose enforcement.
2. **The `proxyenv` plane is intentionally bypassable.** `HTTP_PROXY` is opt-in by construction. It exists for environments with no kernel hook and no sandbox; it makes the fallback *honest* (fully attributed), not *mandatory*.
3. **Launching outside the wrapper is not governed.** Starting `codex` directly, rather than through `keydris codex`, creates no Keydris session at all.
4. **Codex hook trust is a user convenience, not an administrator boundary.** For managed environments where users must not be able to disable governance, deploy the same absolute hook paths through Codex `requirements.toml`.
5. **`~/.keydris-data` is as trustworthy as the account that owns it.** Anything running as that user can read the session state, the identity key, and the ledger. Owner-only permissions are enforced (`0700` directory, `0600` files); they do not defend against the user's own processes.
6. **The Claude Code coupling is undocumented surface.** The Claude path relies on `$CLAUDE_ENV_FILE` and on a hook-set `HTTP_PROXY` composing with the sandbox's own `httpProxyPort` routing. That composition is not documented by Anthropic and may vary by Claude Code version — re-verify on upgrades. `keydris run` does not depend on it.
7. **macOS `enableWeakerNetworkIsolation` is required for TLS termination** under Seatbelt, and is itself a documented exfiltration vector; the Claude Code documentation calls this out.
8. **Keydris sees decrypted traffic on governed origins.** It has to, in order to be the enforcement point. TLS termination there is sanctioned through the custom-proxy + CA-in-sandbox path. Everything outside the scope stays an opaque tunnel Keydris cannot read.

### Known hardening gaps

Open items, scoped to this repository. None of these are fixed; they are listed so you can decide what this is safe for.

#### Session socket residual trust — medium

The registration socket is owner-only and every message authenticates against a random, owner-only per-install secret, which prevents unrelated local users from registering or unregistering sessions. Two production steps remain:

- Verify the peer with `SO_PEERCRED` (uid/gid/pid) and, for the transparent plane, derive the cgroup handle from the **verified peer PID** rather than the client-claimed handle.
- Verify a submitted SVID against the issuer JWKS at registration time. The current secret proves the caller can reach Keydris user state — not that every message field was independently issued by the control plane.

#### Proxy egress hardening — medium

[`internal/node/proxy`](internal/node/proxy/) dials upstreams on the allow path with no SSRF or infrastructure guard. Managed-scope matching canonicalizes case, trailing dots, IP literals, and ports, but remains exact-origin matching. Still to add:

- DNS resolution and rebinding defenses, if policy should treat aliases and resolved IP addresses as the same destination.
- An SSRF / control-plane-port denylist for the daemon's outbound dial — loopback, RFC1918, link-local `169.254/16`, multicast; Docker `2375`/`2376`, Kubernetes `6443`, kubelet `10250` — with an explicit allowlist exception for intended local backends.
- Fail closed when client process identity is unresolvable (currently only `KEYDRIS_PEER_VERIFY=enforce` does this, and it is not the default).

#### Per-session proxy token is a bearer credential — medium

Presenting the token to the proxy via `Proxy-Authorization` attributes a connection to that session's SVID. The daemon logs only a short handle prefix and stores proxy logs, PIDs, session state, and evidence under owner-only paths. The remaining issue is structural:

- **Theft equals impersonation.** A co-resident process that reads the token — from the environment or from `$CLAUDE_ENV_FILE` — can impersonate the session until `session-end`. Mitigations include short token lifetimes, tighter `$CLAUDE_ENV_FILE` permissions, and kernel-asserted attribution so identity is *observed* rather than *claimed*.

#### Client-side audit is only tamper-evident — medium

[`internal/evidence`](internal/evidence/) is the hash-chained ledger behind `keydris logs`. The chain is tamper-evident only to a verifier that already knows the true tip. The control plane should **sign** each record (or the rolling tip) with its Ed25519 key and `keydris logs` should verify that signature — so forging the local ledger requires the signing key, not just filesystem write access.

Separately, authorization records intentionally include full MCP arguments and JSON request bodies: a decision is only auditable against the arguments it was made on. Keydris excludes the SVID, the proxy token, request headers, and the injected credential value, and redacts those values recursively from the record. It cannot remove an application secret the agent itself put in a request body. Treat `~/.keydris-data/evidence.jsonl` and `proxy.log` as sensitive; they are created `0600` under a `0700` directory.

#### Injected secrets are not un-loggable by type — low

The proxy injects a bearer token on the wire. It should be wrapped in a type whose `String()` and `MarshalJSON` redact, so it can never land in an evidence payload or a future debug log. The current code already avoids logging it — this would make the guarantee structural rather than a matter of discipline.

#### Test coverage — low

`internal/node/login/login_test.go` was dropped during extraction: it exercised the client login flow against the control plane's in-process mock IdP (`internal/control/authn`), which does not belong in this repository. The remaining `exchange_test.go` covers the token-exchange path. Restoring equivalent coverage means a stubbed OIDC server (`httptest`) with no control-plane dependency.

### Control plane — separate repository, listed for completeness

- **Fail-closed, signed audit.** The broker should append the decision to the ledger *before* returning `allow`, deny if the append fails, and sign records. The POC broker appends best-effort and discards the error.
- **Fail-closed identity fallback.** With no or an invalid SVID, the POC broker falls back to the union of *all* grants (destination-only). It should default to deny, with any unattributed mode gated behind an explicit, narrowly-scoped policy.

### Deliberately out of scope

The CLI governs **which requests reach an upstream, and with what authority**. It does not govern what the control plane's executor does with the org credential once it has authorized a call, and it does not govern origins the agent's policy does not claim — those keep whatever credentials they already had and travel through an opaque tunnel. Extending governance to them would mean routing all of the agent's egress through Keydris, which is a policy decision, not a default.

---

## Hardening checklist for deployers

- **Run `keydris init` with `--strict`** (the default) so the sandbox is `failIfUnavailable` with no unsandboxed escape, and check `keydris status` for drift as part of onboarding.
- **Prefer the `transparent` plane on Linux** where non-bypassability matters more than convenience. Treat `proxyenv` as a fallback, never as an enforcement boundary.
- **Set `KEYDRIS_PEER_VERIFY=enforce`** where the platform can resolve a peer (Linux), so a connection from outside the session's process tree is rejected rather than logged.
- **Leave `KEYDRIS_ALLOW_SOLE_FALLBACK` unset.** Turning it on lets any local process that reaches the proxy without a token borrow the lone session.
- **Point `KEYDRIS_CONTROL_MTLS_URL` at an HTTPS endpoint** and leave `KEYDRIS_MTLS_SERVER_CA` unset in production, so the server certificate verifies against the system roots. Pin an extra CA only for a local or self-signed control plane.
- **Leave `KEYDRIS_MANAGED_MODE` and `KEYDRIS_MANAGED_DESTINATIONS` unset.** Scope is derived from the policy and refreshed every session; setting them by hand overrides that detection and lets local configuration widen what gets terminated.
- **Do not set `KEYDRIS_TRUST_PROJECT_CONFIG=1`** on a machine that opens untrusted repositories.
- **Deploy the Codex hooks through `requirements.toml`** in managed fleets, and verify with `keydris status` that they are wired.
- **Keep the harness's hook timeout well above 5 seconds**, so a slow control plane produces a deny rather than a timeout the harness treats as no answer.
- **Treat `~/.keydris-data` as a secret store.** Exclude it from backups, sync clients, crash reporters, and log shippers, and do not copy `evidence.jsonl` or `proxy.log` into a shared tracker without scrubbing them.
- **Verify what you installed.** The installer checks `SHA256SUMS`; if you mirror artifacts, keep that verification. Do not install with `--omit=optional` from npm — it removes the native package.
- **Do not keep a fallback credential in the agent's environment "just in case".** It defeats the point, and it will be the thing that leaks.

---

## Non-security questions

For usage questions, integration help, or design discussion, use [Discord](https://discord.gg/3JUcXkUTu) or the [issue tracker](https://github.com/keydrisLabs/keydris-cli/issues). Keep vulnerability reports to the private channels above.
