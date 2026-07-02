# Keydris Control Plane — API Reference

The control plane is the server side of Keydris. The `keydris` CLI / node daemon
(this repo) is its only client. This document is the contract the client depends
on today, plus notes on what a productionized control plane should add.

> Status legend: **[impl]** = implemented in the POC and relied on by the CLI;
> **[rec]** = recommended for the productionized control plane (not yet built).

## 1. Responsibilities

The control plane is three logical services behind two network listeners:

| Service | Does |
| --- | --- |
| **authn** | Signs a per-user **client certificate** after verifying an OIDC login (mock IdP or AWS Cognito). This cert is what authenticates the node to the mTLS endpoints below. |
| **issuer** | Mints a per-session **SPIFFE JWT-SVID**, serves the **JWKS**, tracks instances, and revokes them. |
| **broker** | Makes the per-connection **authorize** decision: validates the SVID, checks the live **grant/policy**, and on allow mints a short-lived **upstream access token** to inject on the wire. |

The agent never holds an upstream secret; the broker mints one per allow.

## 2. Listeners & authentication

Two listeners, because the `/authorize*` family hands back real credentials and
must be mutually authenticated, while login/JWKS must be reachable before the
client has any identity.

| Listener | Default addr | Auth | Endpoints |
| --- | --- | --- | --- |
| **Public HTTP** | `127.0.0.1:8081` (`KEYDRIS_CONTROL_ADDR`) | none / OIDC bearer | `/healthz`, `/jwks`, `/oauth/authorize`, `/oauth/token`, `/identity/sign` |
| **mTLS** | `127.0.0.1:8443` (`KEYDRIS_CONTROL_MTLS_ADDR`) | client cert signed by the control-plane **Client CA** | `/authorize`, `/authorize/issue`, `/authorize/{ulid}/revoke` |

The mTLS client cert is the one `keydris login` obtains from `/identity/sign`.
The daemon presents it on every mTLS call.

## 3. Endpoints

### 3.1 `GET /healthz` — public **[impl]**

Liveness probe. `200` with body `ok`.

### 3.2 `GET /jwks` — public **[impl]**

The Ed25519 public keys that verify **both** SVIDs and injected access tokens.
Consumed by the broker (to verify SVIDs) and by upstream APIs (to verify the
injected token).

```json
{ "keys": [ { "kty": "OKP", "crv": "Ed25519", "x": "<base64url>", "kid": "<id>", "use": "sig" } ] }
```

### 3.3 Login → client certificate (authn) — public

The browser OAuth 2.0 Authorization-Code + PKCE flow, then a CSR signed into a
client cert. Against the built-in mock IdP these are served by the control plane;
against Cognito, `/oauth/*` are the provider's Hosted-UI endpoints and only
`/identity/sign` is the control plane.

**`GET /oauth/authorize`** **[impl, mock IdP]** — query: `response_type=code`,
`client_id`, `redirect_uri`, `state`, `code_challenge`, `code_challenge_method=S256`,
`scope`, `login_hint`. Redirects back to `redirect_uri?code=…&state=…`.

**`POST /oauth/token`** **[impl, mock IdP]** — form-encoded `grant_type=authorization_code`,
`code`, `code_verifier`, `redirect_uri`, `client_id` (+ HTTP Basic for a
confidential client). Returns:

```json
{ "id_token": "<jwt|optional>", "access_token": "<token>", "email": "<mock only>" }
```

**`POST /identity/sign`** — public, **OIDC bearer** **[impl]**

```
Authorization: Bearer <id_token | access_token from the step above>
Content-Type: application/json

{ "csr": "-----BEGIN CERTIFICATE REQUEST-----\n…" }
```

The control plane **verifies the bearer** (mock IdP, or the OIDC issuer's JWKS for
Cognito), derives the identity from the verified token (not from the CSR), and
signs a client cert.

```json
{
  "certificate": "-----BEGIN CERTIFICATE-----\n…",
  "ca_cert":     "-----BEGIN CERTIFICATE-----\n…",
  "spiffe_id":   "spiffe://keydris.local/user/<sub>",
  "subject":     "<cert subject>",
  "email":       "user@example.com",
  "not_after":   "2026-06-26T00:00:00Z"
}
```

`200` on success; non-`200` (with a short error body) otherwise. The private key
never leaves the client — only the CSR is sent.

### 3.4 `POST /authorize/issue` — mTLS (issuer) **[impl]**

Mint a per-session SPIFFE JWT-SVID. Called by the SessionStart hook / `keydris run`.

```json
// request
{ "blueprint": "my-payment-agent", "session_handle": "<opaque per-session token>" }
```

`blueprint` is the agent identity — it becomes the agent segment of the SPIFFE
ID, and it is what the broker authorizes grants against. (In the CLI this is the
`policy-id` from `keydris init claude-code <policy-id>`.)

```json
// response
{
  "spiffe_id":  "spiffe://keydris.local/agent/my-payment-agent/01KVZ…",
  "svid":       "<JWT-SVID, EdDSA, verifiable via /jwks>",
  "ulid":       "01KVZ…",
  "expires_at": "2026-06-25T09:15:00Z"
}
```

### 3.5 `POST /authorize/{ulid}/revoke` — mTLS (issuer) **[impl]**

Revoke a minted instance by its ULID (called at SessionEnd). `204` on success.
After revocation the broker denies any flow carrying that SVID.

### 3.6 `POST /authorize` — mTLS (broker) **[impl]**

The per-connection decision, called by the proxy for every intercepted flow.

```json
// request
{
  "dst_addr":   "backend.keydris.test:8080",
  "dst_host":   "backend.keydris.test",   // optional
  "session_id": "spiffe://…",             // optional, for logging
  "svid":       "<JWT-SVID>",             // empty => unattributed
  "policy_id":  "my-payment-agent"        // optional; redundant with the SVID's blueprint
}
```

Decision logic: verify the SVID against the JWKS → derive the blueprint from the
SPIFFE ID → reject if the instance is revoked → find an **active grant** for
`(blueprint, dst)` → on allow, mint a short-lived access token scoped to this
session/upstream/destination and return how to inject it.

```json
// allow
{ "decision": "allow", "inject": { "type": "header", "name": "Authorization", "value": "Bearer <access-token>" } }

// deny
{ "decision": "deny", "reason": "no active grant for destination" }
```

HTTP is always `200` with the decision in the body (`400` only on a malformed
request). The allow/deny distinction is **in the payload**, not the status code.

## 4. Data models

### 4.1 JWT-SVID (per session)
SPIFFE JWT-SVID, EdDSA (Ed25519), `sub = spiffe://<trust-domain>/agent/<blueprint>/<ulid>`,
short TTL (`KEYDRIS_SVID_TTL_SECONDS`, default 900s). Verified via `/jwks`.

### 4.2 Injected access token (per allow)
Short-lived EdDSA JWT, **minted by the broker**, never a static secret. Claims:

| Claim | Meaning |
| --- | --- |
| `sub` | the session's SPIFFE ID |
| `iss` | control-plane URL |
| `aud` | the upstream name the token is valid for |
| `dst` | the destination it was minted for |
| `bp` | blueprint |
| `scope` | granted scope (e.g. `egress`) |
| `jti` | unique id (audit / replay scoping) |
| `iat`, `exp` | issued-at / expiry (`KEYDRIS_ACCESS_TTL_SECONDS`, default 60s) |

The **upstream** verifies it against `/jwks`, checks `aud == itself` and `exp`. A
token minted for another upstream or destination is useless.

### 4.3 Grant / policy
A grant authorizes a blueprint to reach destinations using a named upstream.

```json
{ "id": "dg_…", "blueprint": "my-payment-agent", "upstream": "mock-backend",
  "dest": ["backend.keydris.test:8080"], "status": "active" }
```

POC: seeded from JSON at startup, revoked in-memory; no live API. See §6.

## 5. End-to-end sequence

```
keydris login ──► POST /identity/sign (OIDC bearer) ──► client cert (mTLS identity)

SessionStart  ──► POST /authorize/issue (mTLS) ─────► per-session SVID + ULID
                                                       (registered locally; token in Proxy-Authorization)

agent egress  ──► proxy ──► POST /authorize (mTLS) ──► allow + inject{Bearer access-token}
                  proxy injects token, splices to upstream
                  upstream verifies token via GET /jwks ──► 200

SessionEnd    ──► POST /authorize/{ulid}/revoke (mTLS) ► 204
```

Every issuance and allow/deny is appended to a hash-chained **evidence ledger**.

## 6. Productionization notes (for the rebuild)

- **Audit is a hard gate [rec].** Append the decision to a hash-chained,
  **signed** WAL *before* returning `allow`; if the append fails, deny. The POC
  appends best-effort after deciding and does not sign records — so a local
  writer can rewrite the chain. Sign each record (or the rolling tip) with a key
  the agent host does not hold.
- **Fail closed on missing identity [rec].** With no/invalid SVID the POC broker
  degrades to the union of *all* grants (destination-only). Default to deny;
  gate any unattributed mode behind an explicit, narrowly-scoped policy.
- **Grant/policy lifecycle API [rec].** Replace the JSON seed + in-memory revoke
  with real CRUD: `GET/POST/PATCH/DELETE /grants` (or `/policies`), keyed by
  blueprint = policy id, with status transitions and an audit trail. This is what
  lets `keydris init claude-code <policy-id>` map onto a governed policy instead
  of a hand-edited seed file.
- **Token hygiene [rec].** Keep access-token TTL in seconds; consider `jti`
  replay tracking at the upstream for the TTL window.
- **Frozen contracts.** Treat `/authorize`, `/authorize/issue`,
  `/authorize/{ulid}/revoke`, and the `/jwks` shape as stable — the node and
  control plane evolve independently against them.

## 7. Config knobs (server side)

| Env | Default | Purpose |
| --- | --- | --- |
| `KEYDRIS_CONTROL_ADDR` | `127.0.0.1:8081` | public HTTP listener |
| `KEYDRIS_CONTROL_MTLS_ADDR` | `127.0.0.1:8443` | mTLS `/authorize*` listener |
| `KEYDRIS_TRUST_DOMAIN` | `keydris.local` | SPIFFE trust domain |
| `KEYDRIS_SVID_TTL_SECONDS` | `900` | per-session SVID lifetime |
| `KEYDRIS_ACCESS_TTL_SECONDS` | `60` | injected access-token lifetime |
| `KEYDRIS_OIDC_ISSUER` | — | external OIDC issuer (Cognito) for `/identity/sign` verification |
| `KEYDRIS_GRANTS_SEED` | `deploy/seed/grants.seed.json` | grant seed (POC) |

The client-side equivalents (`KEYDRIS_CONTROL_URL`, `KEYDRIS_CONTROL_MTLS_URL`,
the OAuth client vars) are documented in this repo's `.env.example`.
