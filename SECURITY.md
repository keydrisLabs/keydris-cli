# Security — keydris-cli

This is an extracted **proof of concept**. It demonstrates the identity + egress
model; it is **not hardened**. Below are the known issues, tracked from a review
against production agent-governance practice. Items are scoped to *this* repo
(the CLI / agent side); a few belong to the separate **control plane** and are
listed for completeness.

> Threat-model note: the strong guarantee holds only while the OS sandbox
> (Seatbelt/bubblewrap) or iptables redirect is enabled and routed to the proxy.
> The `proxyenv` data plane is intentionally bypassable. See
> [docs/distribution.md](docs/distribution.md).

## In this repo

### P0 — Session socket is unauthenticated and world-writable  (high)

`internal/node/sessionsock/sessionsock.go` creates the registration socket
world-writable (`os.Chmod(path, 0o666)`) and registers a fully client-supplied
`{Handle, SPIFFEID, SVID, Blueprint}` with **no peer authentication and no SVID
verification**. Any local process can bind its own cgroup to a privileged
identity, or unregister another session.

Fix:
- `0o600` perms (or daemon-owned dir), not `0o666`.
- Authenticate the caller with `SO_PEERCRED` (uid/gid/pid).
- Derive the cgroup `Handle` from the verified peer PID — do not trust the
  client-claimed handle. Reuse the `/proc`→cgroup logic in `internal/node/attest`.
- Verify the submitted SVID against the issuer JWKS at registration time.

### P3 — Proxy egress hardening  (medium)

`internal/node/proxy` dials upstreams on the allow path with no SSRF / infra
guard, and destination matching is exact-string. Add:
- Host canonicalization (lowercase, strip trailing dot/brackets, normalize port)
  before any destination comparison, to close match-bypass via `Host.`, case, or
  IP-vs-hostname forms.
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

### Verifiable audit on the client

`internal/evidence` is the hash-chained ledger lib used by `keydris logs`. The
chain is only tamper-*evident* to a verifier that knows the true tip. Have the
control plane **sign** each record (or the rolling tip) with its Ed25519 key, and
have `keydris logs` verify the signature — so forging the local ledger requires
the signing key, not just filesystem write access.

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
