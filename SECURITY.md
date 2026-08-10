# Security — keydris-cli

This is an extracted **proof of concept**. It demonstrates the identity + egress
model; it is **not hardened**. Below are the known issues, tracked from a review
against production agent-governance practice. Items are scoped to *this* repo
(the CLI / agent side); a few belong to the separate **control plane** and are
listed for completeness.

> Threat-model note: the strong guarantee holds only while the OS sandbox
> (Seatbelt/bubblewrap) or iptables redirect is enabled and routed to the proxy.
> The `proxyenv` data plane is intentionally bypassable. See
> [docs/attribution.md](docs/attribution.md).

## In this repo

### Session socket residual trust  (medium)

The registration socket is owner-only and every message authenticates with a
random, owner-only per-install secret. This prevents unrelated local users from
registering or unregistering sessions. Two production hardening steps remain:

- Verify the peer with `SO_PEERCRED` (uid/gid/pid) and, for the transparent
  plane, derive the cgroup handle from the verified peer PID rather than the
  client-claimed handle.
- Verify a submitted SVID against the issuer JWKS at registration time. The
  current secret proves that the caller can access Keydris user state, not that
  every message field was independently issued by the control plane.

### P3 — Proxy egress hardening  (medium)

`internal/node/proxy` dials upstreams on the allow path with no SSRF / infra
guard. Managed-scope matching canonicalizes case, trailing dots, IP literals,
and ports, but remains exact-origin matching. Add:

- DNS resolution/rebinding defenses if policy should treat aliases and resolved
  IP addresses as the same destination.
- An SSRF / control-plane-port denylist for the daemon's outbound dial
  (loopback, RFC1918, link-local `169.254/16`, multicast; Docker `2375/2376`,
  k8s `6443`, kubelet `10250`) — with an explicit allowlist exception for
  intended local backends.
- Fail closed when client process identity is unresolvable (enforce mode).

### P4 — Make injected secrets un-loggable by type  (low)

The proxy injects a Bearer token on the wire. Wrap it in a type whose
`String()`/`MarshalJSON` redacts, so it can never land in the evidence ledger
payload or a future debug log. (Current code already avoids logging it — this
makes the guarantee structural rather than a discipline.)

### P5 — Per-session proxy token is a bearer credential  (medium)

The per-session token (`newProxyToken`) is a bearer credential: presenting it to
the proxy via `Proxy-Authorization` attributes a connection to that session's
SVID. The daemon logs only a short handle prefix and stores proxy logs, PIDs,
session state, and evidence under owner-only paths. One issue remains:

- **Theft = impersonation.** A co-resident process that reads the token (from the
  env or `$CLAUDE_ENV_FILE`) can impersonate the session until `session-end`.
  Mitigations: short token lifetime, tighten `$CLAUDE_ENV_FILE`, and (Tier 2/3
  in [docs/attribution.md](docs/attribution.md))
  move to kernel-asserted attribution so identity is observed, not claimed.

### Verifiable audit on the client

`internal/evidence` is the hash-chained ledger lib used by `keydris logs`. The
chain is only tamper-*evident* to a verifier that knows the true tip. Have the
control plane **sign** each record (or the rolling tip) with its Ed25519 key, and
have `keydris logs` verify the signature — so forging the local ledger requires
the signing key, not just filesystem write access.

Authorization records intentionally include full MCP arguments and JSON request
bodies. These may contain application secrets even though Keydris excludes the
SVID, proxy token, request headers, and injected credential value. Treat
`.keydris-data/evidence.jsonl` and `proxy.log` as sensitive; they are created
with `0600` permissions under a `0700` data directory.

## Control plane (separate repo — listed for completeness)

- **P1 — Fail-closed, signed audit.** The broker should append the decision to
  the ledger *before* returning `allow`, deny if the append fails, and sign
  records (see above). The POC broker appends best-effort and discards the error.
- **P2 — Fail-closed identity fallback.** With no/invalid SVID the POC broker
  falls back to the union of *all* grants (destination-only). Default to deny;
  gate any unattributed mode behind an explicit, narrowly-scoped policy.

## Porting note

`internal/node/login/login_test.go` was dropped during extraction — it exercised
the client login flow against the control plane's in-process mock IdP
(`internal/control/authn`), which does not belong in the CLI repo. The remaining
`exchange_test.go` covers the token-exchange path. Restore equivalent coverage
with a stubbed OIDC server (`httptest`) that has no control-plane dependency.
