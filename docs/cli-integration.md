# Keydris CLI ↔ Control-Plane API Integration Spec

> **Purpose:** implementation brief for integrating the Keydris CLI with the
> control-plane identity & agent-authorization APIs. This is the authoritative
> contract — implement the CLI against exactly these shapes.

---

## 1. Overview / mental model

The CLI establishes a **mutual-TLS (mTLS) identity** for a logged-in user, then
uses it to mint short-lived, policy-bound **session SVIDs** that a local proxy
presents on every intercepted connection for an allow/deny decision.

End-to-end flow:

1. **Login** → obtain a Cognito **CLI access token** (JWT) for the user.
2. **Enroll** → generate an EC P-256 keypair + CSR, call `POST /identity/sign`
   with the token, receive a **client certificate** (+ CA cert). This is the
   user's mTLS identity: `spiffe://<trust-domain>/user/<cognito-sub>`.
3. **Issue session** → over mTLS, call `POST /agent/authorize/issue` with a
   `policy_name`; receive a **JWT-SVID** bound to that policy + a session `ulid`.
4. **Authorize** → for each connection, call `POST /agent/authorize` with the
   SVID + destination; receive `allow`/`deny`.
5. **Revoke** → `POST /agent/authorize/{ulid}/revoke` ends the session; further
   `authorize` calls for that SVID return `deny`.

Two transport channels:

| Channel | Port | Auth | Endpoints |
| --- | --- | --- | --- |
| Standard HTTPS | `443` | Cognito Bearer token | `POST /identity/sign` |
| mTLS | `8443` | client certificate | `POST /agent/authorize/issue`, `POST /agent/authorize/{ulid}/revoke`, `POST /agent/authorize` |
| Public | `443` | none | `GET /agent/jwks` |

> In production, AWS ALB terminates mTLS on `:8443` and forwards the verified
> client-cert leaf to the API. The CLI just presents `--cert`/`--key` on the TLS
> handshake (standard mTLS client auth); no special header is needed.

---

## 2. Configuration

| Setting | Dev value | Notes |
| --- | --- | --- |
| API base host | `dev.api.keydris.com` | prod: `api.keydris.com` |
| mTLS port | `8443` | for all `/agent/authorize*` calls |
| HTTPS port | `443` | for `/identity/sign`, `/agent/jwks` |
| AWS region | `us-east-1` | |
| Cognito CLI client id | `791bq13e9aijdu8cspi851mv5u` | dev; treat as configurable |
| Trust domain | `keydris.local` | appears in SPIFFE IDs |
| SVID TTL | ~900s (15 min) | sessions are short-lived; re-issue as needed |

The CLI should store these in a config file/profile (e.g. `~/.keydris/config`)
and never hardcode the host.

---

## 3. Authentication — obtaining the CLI token

The user must present a **Cognito access token** minted for the CLI app client
(`client_id` == the configured CLI client id). The token's `sub` is the user's
Cognito subject; a matching user must be provisioned server-side.

- **Production CLI login** should use a browser-based OAuth flow (Authorization
  Code + PKCE via the Cognito Hosted UI) or device-code flow, caching the
  resulting access + refresh tokens locally and refreshing on expiry.
- **Dev shortcut** (username/password, `USER_PASSWORD_AUTH`) — useful for tests:

```bash
aws cognito-idp initiate-auth --region us-east-1 \
  --auth-flow USER_PASSWORD_AUTH \
  --client-id 791bq13e9aijdu8cspi851mv5u \
  --auth-parameters USERNAME=<email>,PASSWORD=<password> \
  --query 'AuthenticationResult.AccessToken' --output text
```

The token is passed as `Authorization: Bearer <token>` **only** to
`/identity/sign`. The `/agent/*` endpoints do not use it (they use the client
cert).

---

## 4. Key & CSR generation (critical requirements)

The client cert **must** use an **ECDSA P-256** key encoded with a **named
curve** (not explicit EC parameters, not Ed25519, not RSA). LibreSSL (default
`openssl` on macOS) emits explicit params by default and the server rejects it.

```bash
openssl ecparam -name prime256v1 -genkey -param_enc named_curve -noout -out client.key
openssl req -new -key client.key -out client.csr -subj "/CN=keydris-cli"
```

If the CLI generates keys in-process (e.g. Go `crypto/ecdsa` with `elliptic.P256()`,
or Node `crypto.generateKeyPair('ec', { namedCurve: 'prime256v1' })`), the SPKI
is emitted with a named curve automatically — this is preferred over shelling
out to `openssl`. The CSR must be **PEM** (`-----BEGIN CERTIFICATE REQUEST-----`).

The server rejects a non-P256 CSR with:

```json
{ "message": "CSR must use an ECDSA P-256 public key", "error": "Forbidden", "statusCode": 403 }
```

---

## 5. API reference

### 5.1 `POST /identity/sign` (HTTPS :443, Bearer token)

Signs the CSR into a client certificate for mTLS.

**Request**
```
POST https://<host>/identity/sign
Authorization: Bearer <cognito-cli-access-token>
Content-Type: application/json
```
```json
{ "csr": "-----BEGIN CERTIFICATE REQUEST-----\n...\n-----END CERTIFICATE REQUEST-----\n" }
```

**Response 200**
```json
{
  "certificate": "-----BEGIN CERTIFICATE-----\n...\n-----END CERTIFICATE-----",
  "ca_cert":     "-----BEGIN CERTIFICATE-----\n...\n-----END CERTIFICATE-----",
  "spiffe_id":   "spiffe://keydris.local/user/<cognito-sub>",
  "subject":     "CN=keydris-cli",
  "email":       "user@example.com",
  "not_after":   "2027-07-03T08:30:10.429Z"
}
```

The CLI should persist `certificate` → `client.crt`, `client.key` (locally
generated), and `ca_cert` → `ca.crt` with `0600` perms (e.g. under
`~/.keydris/`). `not_after` drives re-enrollment.

| Error | Meaning |
| --- | --- |
| `401` | invalid/expired token, or token `client_id` ≠ CLI client id |
| `403 CSR must use an ECDSA P-256 public key` | wrong key type/encoding (see §4) |
| `403 User is not provisioned` | no server-side user for the token `sub` |
| `503 Certificate authority is not configured` | server CA not ready (retry/report) |

### 5.2 `POST /agent/authorize/issue` (mTLS :8443)

Mints a per-session JWT-SVID bound to a policy the caller **owns**.

**Request** (present client cert on TLS handshake)
```
POST https://<host>:8443/agent/authorize/issue
Content-Type: application/json
```
```json
{ "policy_name": "cli-test-policy", "session_handle": "optional-free-form-label" }
```

- `policy_name` (**required**): resolved to the caller's policy UUID by
  `org + owner + name`. The user must own a policy with this name.
- `session_handle` (optional): opaque label the CLI can use to correlate.

**Response 200**
```json
{
  "spiffe_id":  "spiffe://keydris.local/user/<cognito-sub>/<policy-uuid>/<ulid>",
  "svid":       "<JWT-SVID>",
  "ulid":       "01KWKQ2C6J86C1BZR990Y39K95",
  "expires_at": "2026-07-03T10:20:57.331Z"
}
```

Keep `svid` (for authorize) and `ulid` (for revoke) in memory for the session.

| Error | Meaning |
| --- | --- |
| `400` | missing/empty `policy_name` |
| `404 Policy "<name>" not found for this user` | no owned policy by that name |
| `401` | client cert missing/invalid/revoked |
| `503` | SPIFFE issuer not configured server-side |

### 5.3 `POST /agent/authorize/{ulid}/revoke` (mTLS :8443)

Revokes a session. Idempotent.

**Request**
```
POST https://<host>:8443/agent/authorize/<ulid>/revoke
```
**Response:** `204 No Content`. (`404` if the ulid isn't the caller's session.)

After revoke, `authorize` for that SVID returns `deny` even though the JWT is
still cryptographically valid until its `exp`.

### 5.4 `POST /agent/authorize` (mTLS :8443)

Per-connection decision. Call this for each intercepted destination.

**Request**
```
POST https://<host>:8443/agent/authorize
Content-Type: application/json
```
```json
{
  "dst_addr": "example.com:443",
  "dst_host": "example.com",
  "svid": "<JWT-SVID from issue>"
}
```
- `dst_addr` (**required**): full `host:port` of the connection.
- `dst_host` (optional): hostname only.
- `svid` (**required in practice**): the session SVID.
- `session_id`, `policy_id` are accepted but ignored (policy id is derived from
  the SVID) — do not rely on them.

**Response 200 — allow**
```json
{
  "decision": "allow",
  "inject": { "type": "header", "name": "X-Amzn-Mtls-Clientcert-Leaf", "value": "<svid>" }
}
```
**Response 200 — deny**
```json
{ "decision": "deny", "reason": "session revoked" }
```

Decision logic the CLI should assume:
- `deny` if SVID missing/invalid/expired, session unknown, session revoked, no
  matching active policy, or the policy's decision ≠ `allow`.
- `allow` only when the bound policy evaluates to `allow` for the destination.
- The CLI proxy must **fail closed**: treat any non-`allow` (including network
  errors / non-200) as deny.

### 5.5 `GET /agent/jwks` (public :443)

Ed25519 public keys for verifying SVIDs offline (optional for the CLI; the
server verifies on `authorize`).

**Response 200**
```json
{ "keys": [ { "kty": "OKP", "crv": "Ed25519", "x": "...", "kid": "...", "use": "sig", "alg": "EdDSA" } ] }
```

---

## 6. SPIFFE ID formats (for reference / validation)

| Identity | Format |
| --- | --- |
| User (client cert) | `spiffe://keydris.local/user/<cognito-sub>` |
| Session (SVID) | `spiffe://keydris.local/user/<cognito-sub>/<policy-uuid>/<ulid>` |

The SVID JWT payload contains `sub` (the session SPIFFE ID), `jti` (== ulid),
`iss` (control-plane issuer URL), `iat`, `exp`.

---

## 7. Suggested CLI command surface

Implement (names illustrative — match existing CLI conventions):

| Command | Behavior |
| --- | --- |
| `keydris login` | run OAuth/device flow, cache Cognito tokens |
| `keydris enroll` | generate P-256 key+CSR, `POST /identity/sign`, store `client.key`/`client.crt`/`ca.crt`; auto re-enroll when `not_after` is near |
| `keydris session start --policy <name> [--handle <label>]` | `POST /agent/authorize/issue`; cache `svid`+`ulid`+`expires_at` |
| `keydris session revoke [--ulid <ulid>]` | `POST /agent/authorize/{ulid}/revoke` |
| `keydris proxy` (or connection hook) | for each connection call `POST /agent/authorize`, allow/deny; auto re-issue SVID when expired |

State to persist (per profile, `0600`):
- Cognito tokens (access + refresh)
- `client.key`, `client.crt`, `ca.crt`, cert `not_after`
- Active session: `svid`, `ulid`, `expires_at`, `policy_name`

---

## 8. Implementation notes / gotchas

- **mTLS client**: configure the HTTP client with the client cert + key for
  `:8443` calls. Verify the server cert against the system/public CA (the ALB
  presents a normal public TLS cert; `ca.crt` from `/identity/sign` is the
  *client* CA, not the server CA — do not use it to verify the server).
- **Two ports**: `/identity/sign` and `/agent/jwks` are on `443`;
  `/agent/authorize*` are on `8443`. Don't send the client cert to `443` or the
  Bearer token to `8443`.
- **Short-lived SVIDs**: `expires_at` ~15 min. The proxy should re-issue
  proactively (e.g. when <2 min remain) and on any `deny` caused by expiry.
- **Policy ownership**: `policy_name` must resolve to a policy the user owns.
  Surface `404` as a clear "policy not found / not owned" message. Policies are
  created/managed elsewhere (dashboard/API); the CLI only references by name.
- **Fail closed** on all authorize errors.
- **Token client_id** must equal the CLI client id or `/identity/sign` returns
  `401` — do not reuse a browser/console token.

---

## 9. Reference test transcript (dev)

```bash
HOST=dev.api.keydris.com

# 1. token
TOKEN=$(aws cognito-idp initiate-auth --region us-east-1 \
  --auth-flow USER_PASSWORD_AUTH --client-id 791bq13e9aijdu8cspi851mv5u \
  --auth-parameters USERNAME=<email>,PASSWORD=<password> \
  --query 'AuthenticationResult.AccessToken' --output text)

# 2. key + CSR + sign
openssl ecparam -name prime256v1 -genkey -param_enc named_curve -noout -out client.key
openssl req -new -key client.key -out client.csr -subj "/CN=keydris-cli"
curl -sS -X POST "https://$HOST/identity/sign" \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  --data-binary @<(jq -n --rawfile csr client.csr '{csr:$csr}') > sign.json
jq -r .certificate sign.json > client.crt
jq -r .ca_cert     sign.json > ca.crt

# 3. issue session (mTLS)
curl -sS -X POST "https://$HOST:8443/agent/authorize/issue" \
  --cert client.crt --key client.key -H "Content-Type: application/json" \
  -d '{"policy_name":"cli-test-policy","session_handle":"dev-1"}' > issue.json
SVID=$(jq -r .svid issue.json); ULID=$(jq -r .ulid issue.json)

# 4. authorize (allow)
curl -sS -X POST "https://$HOST:8443/agent/authorize" \
  --cert client.crt --key client.key -H "Content-Type: application/json" \
  -d "{\"dst_addr\":\"example.com:443\",\"dst_host\":\"example.com\",\"svid\":\"$SVID\"}"

# 5. revoke, then authorize (deny)
curl -sS -X POST "https://$HOST:8443/agent/authorize/$ULID/revoke" --cert client.crt --key client.key
curl -sS -X POST "https://$HOST:8443/agent/authorize" \
  --cert client.crt --key client.key -H "Content-Type: application/json" \
  -d "{\"dst_addr\":\"example.com:443\",\"dst_host\":\"example.com\",\"svid\":\"$SVID\"}"
```
