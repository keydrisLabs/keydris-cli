# Architecture

The design and operational reference for `keydris-cli`. It traces one request from the agent's shell to the upstream API and back, names the file that owns each step, and states every branch the code can take.

**Nothing here is aspirational.** Every rule described is enforced by code in this repository, and the file that enforces it is linked. Where a guarantee depends on something outside this codebase, that dependency is named explicitly rather than assumed.

### Who this is for

| If you are… | Start at |
| --- | --- |
| Evaluating Keydris as an enforcement boundary | [§1 The trust model](#1-the-trust-model), then [§11 The complete decision table](#11-the-complete-decision-table) |
| Operating it, and debugging why a request was refused | [§7 The routing decision](#7-the-routing-decision) and [§13 Reading the logs](#13-reading-the-logs) |
| Extending it with a data plane, provider, or action type | [§14 Extension points](#14-extension-points) |
| Reviewing it for security | [§4](#4-interception-what-the-proxy-terminates), [§5](#5-attribution-whose-session-is-this), [§10](#10-command-gating), then [SECURITY.md](SECURITY.md) |
| Looking for one specific thing | [§15 File map](#15-file-map) |

### The system in one paragraph

A coding agent runs unmodified. At session start, a hook exchanges the machine's device certificate for a short-lived **KIT** (a SPIFFE JWT-SVID) and fetches the **routes** the agent's policy governs. A local proxy terminates TLS for exactly those origins — and nothing else — matches each request to one route, and hands it to that route's enforcement mode: the control plane executes it upstream, relays it through an MCP gateway, or mints a single-action token the destination MCP server redeems. Shell commands take a parallel path through the same policy. Every decision made locally is re-enforced server-side, and every error path denies.

### Design principles

These four sentences explain most of the code, and §14 restates them as rules for contributors.

1. **Fail closed, everywhere.** There is no branch in the enforcement path or the gating hooks where an unexpected condition results in the request proceeding.
2. **Decide on what is real.** Scheme, host, and port come from the connection the data plane actually made. A routing value is resolved from the request path or body, never from a header the agent controls freely.
3. **Refuse ambiguity rather than resolve it.** Two matching routes, duplicate JSON keys, a decision whose reason code contradicts it — each is an error, because picking an interpretation would be a guess with a credential attached.
4. **Local checks may only narrow.** They exist to answer cheaply and to fail closed, never to be the only thing standing between the agent and an upstream.

---

**Contents**

1. [The trust model](#1-the-trust-model)
2. [Enrollment: login and init](#2-enrollment-login-and-init)
3. [The session lifecycle](#3-the-session-lifecycle)
4. [Interception: what the proxy terminates](#4-interception-what-the-proxy-terminates)
5. [Attribution: whose session is this](#5-attribution-whose-session-is-this)
6. [Request metadata and MCP promotion](#6-request-metadata-and-mcp-promotion)
7. [The routing decision](#7-the-routing-decision)
8. [The three enforcement modes](#8-the-three-enforcement-modes)
9. [The legacy broker path](#9-the-legacy-broker-path)
10. [Command gating](#10-command-gating)
11. [The complete decision table](#11-the-complete-decision-table)
12. [State on disk](#12-state-on-disk)
13. [Reading the logs](#13-reading-the-logs)
14. [Extension points](#14-extension-points)
15. [File map](#15-file-map)

---

## 1. The trust model

Three parties, with one trust boundary that matters.

| Party | What it is | Where it lives |
| --- | --- | --- |
| **The agent** | Claude Code, OpenAI Codex, or any command run under `keydris run`. Unmodified. It holds no upstream credential | The user's machine |
| **The CLI** | This repository: the `keydris` binary. It is both the short-lived hook process and the long-running proxy daemon | The user's machine |
| **The control plane** | Issuer, broker, policy engine, provider executors, MCP gateway, credential vault | A separate service, reached over mTLS |

The CLI is the *enforcement point*; the control plane is the *decision point* and the *credential custodian*. Every decision the CLI makes locally is re-enforced server-side at execution time — the CLI's local checks exist to answer cheaply and to fail closed, not to be the only thing standing between the agent and an upstream.

Two long-lived facts are configured once and never per session:

- **A device identity.** `keydris login` runs a browser OAuth 2.0 Authorization Code + PKCE flow and stores a client certificate under `~/.keydris-data/identity/`. The private key is generated locally and never leaves the machine. Every mTLS call the CLI makes presents this certificate.
- **An agent id.** A UUID an operator creates in the Keydris console, where the governing policy is assigned. `keydris init <target> <agent-id>` persists it to `~/.keydris-data/agent-id`. **The CLI never selects, names, or overrides a policy** — it only says which agent it is acting as, and the control plane resolves the policy from that.

---

## 2. Enrollment: login and init

### `keydris login`

[`internal/node/login/`](internal/node/login/) — opens the browser, completes PKCE against either the control plane's built-in mock IdP or a real OIDC provider (AWS Cognito is the configured example), and exchanges the resulting token at `/identity/sign` for a client certificate. The certificate, its key, the pinned CA, and whoami metadata land in `identity/`.

`login.EnsureFresh` runs before every mTLS call with a 48-hour freshness window, so a long-running daemon renews its own certificate rather than failing mid-session.

### `keydris init claude-code <agent-id>`

[`internal/cli/init.go`](internal/cli/init.go) does six things, in this order:

1. **Persists the agent id** to `~/.keydris-data/agent-id`.
2. **Generates the Keydris CA** (`ca.crt` + `ca.key`, 825-day validity) if it does not exist, then builds `ca-bundle.crt` = platform roots **plus** the Keydris CA. Keeping the public roots is load-bearing: opaque CONNECT tunnels still need to verify real upstream certificates.
3. **Writes the harness configuration.** For Claude Code, [`internal/node/sandbox/claudecode.go`](internal/node/sandbox/claudecode.go) merges into `~/.claude/settings.json`:
   - `sandbox.enabled: true`
   - `sandbox.network.httpProxyPort: <port>`
   - `sandbox.failIfUnavailable: true` and `sandbox.allowUnsandboxedCommands: false` (with `--strict`, the default), making the sandbox a hard gate
   - `sandbox.enableWeakerNetworkIsolation: true` on macOS, required for TLS termination with a custom CA under Seatbelt
   - the CA bundle into `env` (`NODE_EXTRA_CA_CERTS`, `SSL_CERT_FILE`, `CURL_CA_BUNDLE`, `GIT_SSL_CAINFO`, `REQUESTS_CA_BUNDLE`)
   - the `SessionStart`, `SessionEnd`, and `PreToolUse` hooks

   For Codex, [`internal/node/sandbox/codexhooks.go`](internal/node/sandbox/codexhooks.go) writes `$CODEX_HOME/hooks.json` with exact `^Bash$` matchers, using the **absolute** path of the `keydris` executable.
4. **Creates the session-socket secret** if absent.
5. **Signs in** if no valid, agent-matching identity exists yet.
6. **Detects the proxy scope** (below) and prints it.

On Windows the CA handling differs: public roots live in the certificate store, so Keydris sets only Node's additive `NODE_EXTRA_CA_CERTS` by default, and `--trust-store` installs the CA into the current user's Windows root store for native tools. On platforms with no discoverable PEM root bundle, `init` **fails** rather than pointing `SSL_CERT_FILE` at a Keydris-only bundle and silently discarding the public roots.

### Scope detection

[`internal/cli/scope.go`](internal/cli/scope.go). Routes need a KIT, so scope detection mints a **throwaway** session with a fresh handle, fetches `GET /v1/runtime/routes`, extracts `ManagedOrigins()`, writes them to `managed-destinations.json`, and revokes the session in a `defer` — always, including on the error paths.

It is best-effort: a control plane that cannot be reached warns and lets `init` finish, and scope is detected again on the first real session. Note the deliberate distinction in the return value: `ok=false` means *we could not ask*, while `ok=true, len(origins)==0` means *this agent's policy governs nothing* — a valid and very different answer.

`refreshPolicyScope` re-writes the same file from the routes every session start already fetches, so **a policy change lands without a re-init**.

---

## 3. The session lifecycle

Owned by [`internal/cli/hook.go`](internal/cli/hook.go) (`hookSessionStart` / `hookSessionEnd`), invoked by the internal `keydris __session-start` / `__session-end` entrypoints and by `keydris run`.

### Start

```
validate session id
   └─ existing state for this id?  ──► run hookSessionEnd first (revoke + unregister)
resolve the agent id
bind a handle
   ├─ transparent plane ──► a cgroup path
   └─ otherwise         ──► a random 128-bit per-session token
POST /runtime/sessions   (mTLS, Idempotency-Key: cli-<32 hex>)   ──► KIT
GET  /v1/runtime/routes  (Authorization: Bearer <KIT>)           ──► routes
   └─ routes.agent.agent_id != the agent we asked for? ──► revoke, unbind, fail
refresh managed-destinations.json from the routes
save ~/.keydris-data/sessions/<id>.json   (atomic replace)
register(handle, spiffe_id, KIT, routes) over the local socket
```

Every failure after minting unwinds what came before it: revoke the instance, unbind the handle, remove the state file. The one deliberate exception is a registration failure whose rollback revoke *also* fails — there, the state file is **retained** so the operator can retry `SessionEnd` rather than orphaning a live credential in the control plane.

**Idempotency and replacement.** A repeated SessionStart for the same logical session — Claude compaction, resume, or clear; a Codex retry — is not an error and not a second identity. The existing session is ended first, so its ULID is never orphaned.

### The handle

For every plane except `transparent`, the handle is an unguessable 128-bit hex token from `crypto/rand` ([`newProxyToken`](internal/cli/hook.go)). It is simultaneously:

- the **registry key** the daemon indexes the session by,
- the **bearer credential** the agent's HTTP clients present as the password in `Proxy-Authorization: Basic base64(keydris:<token>)`, and
- what `keydris run` and the SessionStart hook put in the proxy URL: `HTTPS_PROXY=http://keydris:<token>@127.0.0.1:15001`.

For Claude Code, the hook appends that export to `$CLAUDE_ENV_FILE`, which Claude Code sources for every Bash subprocess in that session. Each concurrent session runs its own SessionStart hook and therefore gets its own token — which is the whole mechanism behind concurrent-session isolation.

Because it is a bearer credential, the session socket logs only a 12-character prefix ([`handlePrefix`](internal/node/sessionsock/sessionsock.go)).

### Registration

[`internal/node/sessionsock/`](internal/node/sessionsock/) — a Unix-domain socket under the `0700` data directory. Every message is line-delimited JSON carrying an `auth` field compared in constant time against a 256-bit per-install secret. Four actions: `register`, `unregister`, `update-owner`, `lookup`.

Registration **validates the routes again** on the daemon side (`m.Routes.Validate()`), so a malformed or absent routes blob cannot be registered even by a caller that holds the secret.

`lookup` and `unregister` return a `SessionSnapshot` — that is how a short-lived hook, started long after SessionStart, learns the *currently renewed* KIT instead of the possibly-expired one in its own state file.

### Owner binding

`keydris run` and `keydris codex` call `updateSessionOwner` after starting the child, recording its PID **and** an OS process-creation identity ([`process_unix.go`](internal/cli/process_unix.go) / [`process_windows.go`](internal/cli/process_windows.go)). The creation identity is what makes PID reuse non-exploitable: a recycled PID has a different identity.

Owner binding enables two things: peer verification in the proxy (§5), and liveness-based retirement in the renewal loop.

### Renewal

[`internal/node/daemon/session_renew.go`](internal/node/daemon/session_renew.go) polls every **30 s** and renews any session inside a **5-minute** window before its KIT expires, with a 20 s deadline.

A renewal is a `POST /runtime/sessions` carrying `X-Keydris-Replaces-Kit: <current ULID>`, so the control plane atomically swaps one identity for the next. The renewal **keeps the same handle**, refreshes the routes, and atomically replaces both the registry entry and the on-disk state — so the agent's `Proxy-Authorization` token stays valid across the swap, and SessionEnd revokes the *latest* ULID, not the one minted at startup.

For `OwnerManaged` sessions (those `keydris run` started and can speak for):

- no verifiable owner (`OwnerPID <= 0` or empty identity) → **renewal is withheld**;
- liveness unresolvable → renewal withheld, because an unverifiable owner must not extend a credential's life;
- owner confirmed dead → one polling interval of grace, so the wrapper's own synchronous SessionEnd wins the race, then the session is retired.

Claude's hook parent PID is deliberately treated as advisory: the hook may run through a short-lived shell, so it is not a lifecycle anchor. Claude uses its SessionStart/SessionEnd lifecycle unchanged.

### End

Unregister first (capturing the snapshot), then revoke the ULID the snapshot named, then unbind and remove local state. Taking the snapshot before revoking is what makes SessionEnd correct after a background renewal.

---

## 4. Interception: what the proxy terminates

The default plane is [`internal/node/dataplane/sandboxproxy.go`](internal/node/dataplane/sandboxproxy.go): a forward proxy on `127.0.0.1:15001` that Claude Code's sandbox routes all Bash-subprocess egress to.

```
accept ──► read the first request
             ├─ CONNECT      ──► buildConnect
             └─ absolute-form ──► buildPlain   (http:// upstreams)
```

### buildConnect, step by step

1. **Normalize the target.** `proxyscope.Normalize` canonicalizes case, strips a trailing dot, canonicalizes IP literals, and requires a valid 1–65535 port ([`internal/proxyscope/scope.go`](internal/proxyscope/scope.go)). An unparseable authority is rejected before anything else happens.
2. **Resolve the session** (§5).
3. **Is this origin governed?** `managesSessionOrigin` asks the *session's own routes* when the connection is attributed, and falls back to the static scope file otherwise.
   - **No** → answer `200`, splice bytes, and return without ever creating a `Flow`. The proxy does not terminate TLS, does not read a body, does not call anything, does not mutate a header. This path is logged as `PASSTHROUGH … (opaque CONNECT)`.
   - **Yes** → continue.
4. **Terminate TLS** with a leaf minted on demand by the Keydris CA and cached per host ([`internal/node/proxy/ca.go`](internal/node/proxy/ca.go)). `MinVersion` is TLS 1.2. Clients dialing an IP literal send no SNI, so the CONNECT target host is the authority for which leaf to mint; when SNI *is* present it must match the CONNECT target or the handshake is refused.
5. **Read the decrypted request and check the authority.** The inner request's `Host` / URL authority must normalize to exactly the CONNECT target. A mismatch is rejected — this is what stops a request from smuggling itself onto a different origin behind an approved CONNECT.
6. **Extract request metadata** (§6) and emit a `Flow`.

`buildPlain` is the same shape without the TLS steps, and it extracts metadata only when the origin is governed.

### The Flow

[`internal/node/dataplane/dataplane.go`](internal/node/dataplane/dataplane.go). A `Flow` is one intercepted request plus the live connection state. The portable fields (`OrigDst`, `SrcPID`, `Cgroup`, `SessionID`, `SVID`, `ToolCall`, `ToolParams`, `MCPMethod`, `MCPAction`, `Routes`) are what the daemon reasons about; the unexported ones hold the connection, the buffered reader, and the dial target.

Note `Flow.Scheme()`: it reports the transport the data plane actually selected, never a scheme read out of a JSON body. Routing decisions are made against what the connection really is.

Six terminal operations, and every one of them closes the connection:

| Operation | Effect |
| --- | --- |
| `Inject(flow, credential)` | Add the broker's credential to the request, forward upstream, splice the response |
| `InjectMCPActionToken(flow, token)` | Set `params._meta["keydris/kit_action_token"]`, fix `Content-Length`, forward |
| `PassThrough(flow)` | Forward the request byte-for-byte |
| `Respond(flow, response)` | Synthesize a response from the control plane's relayed result; never dial upstream |
| `Reject(flow, reason)` | Synthesize a refusal carrying the reason; never dial upstream |
| `Close()` | Stop the listener, which closes the flow channel and ends the daemon's loop |

Adding an OS-specific plane means implementing this interface and nothing else. The broker path, the router, and the enforcement modes are all plane-agnostic.

---

## 5. Attribution: whose session is this

`resolveSession` = `matchSession` **and** `verifyPeer`. Either failing yields `nil` — unattributed.

### matchSession

| Request carries | Result |
| --- | --- |
| A `Proxy-Authorization` token that is **registered** | That session |
| A `Proxy-Authorization` token that is **not registered** | **`nil`** — never downgraded to "the sole session" |
| **No** token, and `KEYDRIS_ALLOW_SOLE_FALLBACK` is set, and exactly one session is registered | That session |
| No token, default configuration | `nil` |

The second row is the one that keeps concurrent sessions isolated, and the fourth is the fail-closed default: a request that presents no identity does not borrow one. `AllowSoleFallback` exists only to restore the single-session convenience case, and is off unless you turn it on.

The token is read as the **password** half of `Basic base64(user:token)`; the username is ignored. That is what makes the whole thing work with unmodified clients — curl, git, Node, Python, and Go all send `Proxy-Authorization` automatically when the proxy URL carries userinfo.

### verifyPeer

When the session has a recorded owner PID and the platform can resolve a connection to a PID (Linux today), the proxy checks that the connecting process is a descendant of the session owner:

| `KEYDRIS_PEER_VERIFY` | Peer outside the tree | Peer unresolvable |
| --- | --- | --- |
| `off` | allowed | allowed |
| `warn` (default) | logged, allowed | allowed |
| `enforce` | rejected | rejected |

It is a no-op where the peer cannot be resolved, which is why it defaults to `warn` rather than `enforce`. See [docs/attribution.md](docs/attribution.md) for the tiered plan to make attribution kernel-asserted on macOS and Windows.

---

## 6. Request metadata and MCP promotion

[`internal/node/dataplane/toolmeta.go`](internal/node/dataplane/toolmeta.go), called only for governed origins.

`applyRequestMetadata` sets `ToolCall = "<METHOD> <escaped path>"`, then — only when the `Content-Type` is `application/json` or `*+json` — reads the body under a **1 MiB** cap and restores it for forwarding. The body must be valid JSON with no duplicate keys; anything else sets `MetadataError`, which every downstream path treats as a rejection rather than a warning. Duplicate keys are refused because different parsers disagree about which value wins, and a decision made on one interpretation could be executed on another.

If the body is a JSON-RPC 2.0 envelope, `applyMCPToolMetadata` promotes it:

| Method | `ToolCall` | `MCPAction` |
| --- | --- | --- |
| `tools/call` | `params.name` | `mcp.tool.call` / `mcp.tool` / routing key `mcp.tool_name` = the tool name |
| `resources/read` | `params.uri` | `mcp.resource.read` / `mcp.resource` / routing key `mcp.resource_uri` = the URI |
| anything else | unchanged | `nil` — `MCPMethod` is still recorded |

Only these two produce an `MCPAction`, and only they capture `MCPRequestID`. Everything else is lifecycle or discovery, and §7 passes it through.

Numbers are decoded with `json.Number`, so an integer argument does not become a float and change the canonical hash the control plane will check.

---

## 7. The routing decision

[`internal/node/daemon/runtime_router.go`](internal/node/daemon/runtime_router.go), `handle`. Reading this function top to bottom is the fastest way to understand the whole system.

```
flow.Routes == nil                              ──► return false  ──► §9 legacy broker path
router unusable                                 ──► REJECT  "runtime enforcement unavailable"

matches := routes.RoutesFor(scheme, host, port, path)

len(matches) == 0
   ├─ routes.ManagesOrigin(scheme, host, port)  ──► REJECT  "runtime routes have no route for this path"
   └─ otherwise                                 ──► PASSTHROUGH  reason=unmanaged_origin
len(matches) > 1                                ──► REJECT  "runtime route is ambiguous"

route.Availability != "ready"                   ──► REJECT  "runtime route unavailable[: <status_reason_code>]"

switch route.EnforcementMode
   ├─ provider_executor                         ──► §8.1
   ├─ mcp_gateway    ─ lifecycle? ─► PASSTHROUGH ──► §8.2
   ├─ mcp_kit_reader ─ lifecycle? ─► PASSTHROUGH ──► §8.3
   └─ default                                   ──► REJECT  "runtime enforcement mode is unsupported"
```

Three details are worth pausing on.

**The zero-match split.** A governed origin with no matching path is a *rejection*; an origin no route mentions is a *pass-through*. The proxy only terminated TLS here because the static scope said so, and the routes are the authority — so an origin the routes do not claim is handed back untouched rather than blocked.

**Ambiguity is refused, not resolved.** Two matching routes could imply two different credentials, two different resources, or two different modes. Picking one would be a guess. The request is refused.

**Lifecycle passes before anything else on MCP routes.** `mcpPassthroughReason` returns true for a `GET`/`HEAD`/`DELETE` with no JSON-RPC method (`mcp_transport` — the Streamable HTTP transport's own traffic), and for `initialize`, `notifications/initialized`, `ping`, `tools/list`, `prompts/list`, `prompts/get`, `resources/list`, `resources/templates/list`, `resources/subscribe`, `resources/unsubscribe`, `completion/complete`, `logging/setLevel`, `notifications/cancelled`, `notifications/progress`, and `notifications/roots/list_changed` (`mcp_lifecycle`). An agent can connect and enumerate tools without spending a decision; it discovers it needs authorization only when it calls one.

### Matching rules

- **Origin**: exact `scheme` + lowercased, dot-stripped `host` + numeric `port`. No wildcards, no suffix matching.
- **Path**: `pathHasPrefix` — `/` matches everything; otherwise the path must equal the prefix or start with `prefix + "/"`. `/repos/acme` does **not** match a `/repos/acmecorp` prefix.
- **Resource**: by exact routing-key value, with a case-insensitive variant (`ResourceByKeyFold`) used only for `github.full_name`, because GitHub repository names are case-insensitive.

---

## 8. The three enforcement modes

All three share the same shape: validate metadata → resolve the resource → check availability → call the control plane with a 15-second deadline → relay or reject. All three treat a control-plane failure as a rejection.

### 8.1 `provider_executor`

The request never reaches the upstream from this machine. The control plane executes it.

**Resolving the resource** — this is where "which repo?" is answered from the request itself, not from anything the agent claims:

| Provider | Source | Routing key |
| --- | --- | --- |
| `github` | `owner/repo` parsed from a `/repos/{owner}/{repo}/…` path, percent-decoded, rejecting `\`, `?`, `#`, and embedded slashes | `github.full_name` (case-insensitive) |
| `slack` | the body's `channel`, which must match `^C[A-Z0-9]{8,}$` | `slack.channel_id` |
| anything else | — | rejected: *provider executor is not supported by this CLI* |

The matched resource must exist in `route.resources`, have the expected `resource_type`, and be `ready`. A repo the policy did not select is refused before any call.

**The request**:

```json
{
  "schema_version": 1,
  "request_id": "cli-<32 hex>",
  "connection_id": "<uuid>",
  "resource_id": "<uuid>",
  "request": {
    "method": "GET",
    "path": "/repos/acme/api/pulls",
    "query": { "state": ["open"] },
    "headers": { "accept": "…", "github_api_version": "…" },
    "body": { }
  }
}
```

Only four request headers cross the boundary — `Accept`, `If-Match`, `If-None-Match`, `X-GitHub-Api-Version` ([`Flow.ProviderRequestHeaders`](internal/node/dataplane/dataplane.go)). Everything else, including anything the agent may have set as an `Authorization` header, is dropped on the floor.

**The response** is validated as a state machine before it is trusted ([`provider.go`](internal/runtimecontract/provider.go)): `request_id` must echo, and `execution_status` must be internally consistent with `decision` —

| `execution_status` | Requires |
| --- | --- |
| `denied` | decision is not `allow`, no provider response, no error code |
| `succeeded` | decision is `allow`, a provider response, no error code |
| `failed` | decision is `allow`, and a provider response **or** an error code |
| `unknown` | decision is `allow`, no provider response, an error code |

An inconsistent envelope is an error, not a partial success. On `succeeded`, the upstream status, headers, and body are relayed to the agent through `Respond`.

### 8.2 `mcp_gateway`

`tools/call` and `resources/read` are rebuilt into a clean JSON-RPC message — the original request id, the method, and `params` reconstructed from the promoted `MCPAction` — and posted to the gateway. For `resources/read`, the URI in `params` must equal the promoted action name, or the request is refused.

The response is validated the same way as §8.1, plus JSON-RPC rules: `jsonrpc == "2.0"`, the id must equal the id that was sent, and exactly one of `result` / `error` must be present. `succeeded` additionally forbids a JSON-RPC error — a protocol-level failure cannot be reported as a successful execution. The validated response is relayed as `200 application/json`.

### 8.3 `mcp_kit_reader`

The MCP server is reached directly, but it receives a token instead of the agent's authority.

The daemon builds an **action intent** — provider, connection, action type and name, the resolved resource, and the parameters — canonicalizes it with **JCS ([RFC 8785](https://www.rfc-editor.org/rfc/rfc8785))**, and sends `sha256:<hex>` of that canonical form alongside it ([`kit_action_token.go`](internal/runtimecontract/kit_action_token.go)). Canonicalization is what makes the hash reproducible on the control plane's side regardless of key order or number formatting.

The minted token is injected at `params._meta["keydris/kit_action_token"]` and the request is forwarded to the MCP server, which redeems it for the credential it needs — see the companion [keydris-reader](https://github.com/keydrisLabs/keydris-reader) libraries for the server side.

Injection is not a string substitution ([`injectMCPActionToken`](internal/node/dataplane/toolmeta.go)): the body is re-parsed with duplicate-key rejection, `params` must be an object, an existing `_meta` must be an object, and `Content-Length` is recomputed with `Transfer-Encoding` cleared. A token response whose `expires_at` is not in the future is rejected before it is ever injected.

---

## 9. The legacy broker path

When a flow carries no routes at all — an older control plane, or a session registered before routes existed — [`internal/node/daemon/daemon.go`](internal/node/daemon/daemon.go) `handleFlow` runs the destination-scoped path:

```
scope.Managed(dst) == false ──► PASSTHROUGH
MetadataError != ""         ──► audit, then DENY
POST /agent/authorize (mTLS) with {dst, session, svid, policy, tool_call, tool_params}
   ├─ audit append fails    ──► REJECT "authorization audit unavailable"
   ├─ transport error       ──► REJECT "broker unavailable"
   ├─ decision != allow     ──► REJECT with the broker's reason
   └─ allow                 ──► Inject(credential) and forward
```

The audit append happens **before** the decision is acted on, and a failure to append is itself a rejection: an allow that cannot be recorded is not granted.

[`internal/node/daemon/audit.go`](internal/node/daemon/audit.go) redacts the session SVID and the injected credential value from every string in the record — recursively through the tool parameters, keys included — before it reaches the ledger. The ledger itself ([`internal/evidence/ledger.go`](internal/evidence/ledger.go)) is append-only JSONL where each record's SHA-256 covers the previous record's hash; `keydris logs` recomputes the chain and reports the first inconsistency.

---

## 10. Command gating

[`internal/cli/pretool.go`](internal/cli/pretool.go). Separate from egress: this governs what the agent may *run*, not where it may *connect*.

```
read stdin (≤ 10 MiB)                    ──► oversized / unreadable ──► DENY
reject duplicate JSON keys               ──► DENY
unmarshal                                ──► DENY on failure
tool_input.command == ""                 ──► Claude: DENY (the matcher guarantees a shell tool)
                                             Codex:  silent fall-through
resolve the session id                   ──► empty ──► DENY
load session state                       ──► missing or no KIT ──► DENY
lookup(handle) on the daemon socket
   ├─ succeeded ──► use the daemon's *current* KIT (survives renewal)
   └─ mismatched session id ──► DENY
POST /v1/runtime/commands/authorize      ──► 5 s deadline; any error ──► DENY
map the decision
```

**Why every branch denies.** Both Claude Code and Codex fail **open** when a hook crashes, times out, or prints invalid JSON. So the hook always exits 0 and always prints a verdict; the deny is carried in the payload, never in the exit code. Even the JSON-encoding failure path has a hard-coded literal deny string. Keep the harness's configured hook timeout comfortably above 5 seconds.

**Session resolution** differs by harness ([`resolveHookSessionID`](internal/cli/pretool.go)). Codex's own thread `session_id` is unrelated to the Keydris wrapper session, so for Codex — and for Claude running *inside* `keydris run` — the wrapper's `KEYDRIS_SESSION` environment variable is authoritative. Otherwise Claude's native payload id wins.

**The two-hook split on Codex** is a correctness requirement, not a style choice. Codex's `PreToolUse` rejects an `ask` verdict at runtime, and a rejected verdict fails open — so `PreToolUse --codex` emits **only** denials, and `__permission-request` auto-allows policy-allowed commands and stays silent otherwise, leaving `approval_required` to land on Codex's interactive prompt. That is why the integration is paired with `approval_policy = "untrusted"`.

**Wrapper integrity.** `keydris codex` refuses to start when the exact Bash hooks are not wired, forces `features.hooks=true`, and rejects command-line arguments that would disable or override hooks — including inline-TOML spellings like `features={hooks=false}` ([`validateCodexHookArgs`](internal/cli/run.go)). User-level hook trust is a convenience, not an administrator boundary; for managed fleets, deploy the same absolute hook paths through Codex `requirements.toml`.

---

## 11. The complete decision table

Every outcome the enforcement path can produce, with the message the agent receives.

| # | Situation | Control plane | Outcome |
| --- | --- | --- | --- |
| 1 | Origin outside the session's governed set | No | Opaque CONNECT tunnel; never becomes a `Flow` |
| 2 | Invalid CONNECT authority | No | `invalid CONNECT destination` |
| 3 | Inner request authority ≠ CONNECT target | No | `request authority does not match CONNECT target` |
| 4 | TLS SNI ≠ CONNECT target host | No | Handshake refused |
| 5 | Body not JSON / duplicate keys / > 1 MiB | No | `invalid provider request metadata` (or the MCP equivalent) |
| 6 | Governed origin, no route covers the path | No | `runtime routes have no route for this path` |
| 7 | Origin no route mentions | No | Forwarded unchanged (`unmanaged_origin`) |
| 8 | Two routes match | No | `runtime route is ambiguous` |
| 9 | Route not `ready` | No | `runtime route unavailable[: <status_reason_code>]` |
| 10 | Unknown enforcement mode | No | `runtime enforcement mode is unsupported` |
| 11 | MCP lifecycle / discovery / transport traffic | No | Forwarded unchanged |
| 12 | Unsupported GitHub path, or a Slack body with no valid channel | No | `unsupported GitHub request path` / `Slack request must identify a public channel` |
| 13 | Provider is neither `github` nor `slack` | No | `provider executor is not supported by this CLI` |
| 14 | Resource not selected for this session | No | `… resource is not selected for this session` |
| 15 | Resource not `ready` | No | `… resource is unavailable` |
| 16 | `provider_executor`, allowed | Yes | Upstream status, headers, and body relayed |
| 17 | `mcp_gateway`, allowed | Yes | JSON-RPC response relayed, bound to the original id |
| 18 | `mcp_kit_reader`, allowed | Yes | Token injected at `params._meta`; request forwarded to the MCP server |
| 19 | Control plane denies | Yes | `… denied: <reason_code>` |
| 20 | Control plane returns an inconsistent envelope | Yes | `… execution unavailable` |
| 21 | Control plane unreachable or > 15 s | Attempted | `… unavailable` |
| 22 | Minted action token already expired | Yes | `MCP Kit Reader authorization unavailable` |
| 23 | No routes on the flow at all | Yes (`/agent/authorize`) | Broker decides; credential injected on allow |
| 24 | Legacy path, audit append fails | Yes | `authorization audit unavailable` |

Rows 1–15 are answered locally. That is 15 distinct ways a request is resolved without spending a decision — each one cheap, each one deterministic, and none of them capable of returning "allow" by accident.

---

## 12. State on disk

Everything lives under `~/.keydris-data` (`0700`); files are `0600`.

| Path | Written by | Contents |
| --- | --- | --- |
| `identity/` | `keydris login` | Private key, signed client certificate, pinned CA, whoami metadata |
| `ca.crt`, `ca.key` | `keydris init` | The CA that signs per-host leaves for TLS termination |
| `ca-bundle.crt` | `keydris init`, `keydris run` | Platform roots **plus** the Keydris CA |
| `agent-id` | `keydris init` | The agent this install acts as |
| `policy-id` | legacy `init` | Retained for pre-agent-id installs |
| `managed-destinations.json` | scope detection and every session start | `{mode, source, destinations[]}` |
| `sessions/<id>.json` | SessionStart, renewal | Handle, ULID, SPIFFE ID, KIT, routes, owner PID and identity. Replaced atomically |
| `session.auth` | first use | 256-bit per-install secret every socket message must present |
| `registry.sock` | `keydris proxy up` | The daemon's session socket |
| `evidence.jsonl` | the daemon | The hash-chained ledger |
| `proxy.pid` | `keydris proxy up` | `{pid, identity}` — the identity is what makes `proxy down` refuse to signal a recycled PID |
| `proxy.log` | `keydris proxy up` | The daemon's stdout and stderr |

**`evidence.jsonl` and `proxy.log` are sensitive.** Authorization records deliberately include full MCP arguments and JSON request bodies, because a decision is only auditable against the arguments it was made on. Keydris excludes the SVID, the proxy token, request headers, and the injected credential value — but an application secret an agent put in a request body will be there.

**Configuration precedence** ([`internal/config/`](internal/config/)): process environment > `~/.keydris.toml` > opted-in project files > defaults. Project-local `.env` and `.keydris.toml` are ignored unless `KEYDRIS_TRUST_PROJECT_CONFIG=1` is set in the *process* environment, because a repository that could redirect OAuth, identity, and control-plane endpoints could redirect them somewhere you did not choose. Each TOML key maps to `KEYDRIS_<UPPER(KEY)>`.

---

## 13. Reading the logs

`~/.keydris-data/proxy.log`. Every routing decision emits exactly one of these.

| Prefix | Meaning |
| --- | --- |
| `RUNTIME_ROUTE` | A route matched; names the route id and enforcement mode |
| `RUNTIME_GOVERN` | That route is about to be enforced (the control plane is being called) |
| `RUNTIME_PASSTHROUGH` | Forwarded unchanged; `reason=` is `unmanaged_origin`, `mcp_lifecycle`, or `mcp_transport` |
| `RUNTIME_DENY` | Rejected; `reason=` carries the message the agent received |
| `RUNTIME_ERROR` | A relay or forward failed *after* the decision was made |
| `PASSTHROUGH` | Legacy path, or an out-of-scope opaque CONNECT |
| `ALLOW` / `DENY` | Legacy broker decision, with the tool call and parameters |

A useful first triage: `RUNTIME_PASSTHROUGH … reason=unmanaged_origin` on a call you expected to be governed means the routes do not claim that origin — check `keydris proxy scope list` and the agent's policy, not the CLI.

```bash
keydris status                # config, identity, sandbox drift, detected scope, control-plane health
keydris proxy scope list      # exactly which origins are governed, and whether policy-derived
keydris logs                  # print the evidence ledger and verify the hash chain
```

---

## 14. Extension points

### A new data plane

Implement `dataplane.DataPlane` ([`dataplane.go`](internal/node/dataplane/dataplane.go)) and add a case to `buildDataPlane` in [`daemon.go`](internal/node/daemon/daemon.go). The router, the enforcement modes, the broker path, and the ledger are all plane-agnostic. `inlinePlane` already provides the shared inject/forward/reject implementations for any plane that proxies inline. This is the seam a macOS Network Extension or a Windows WFP plane plugs into — see [docs/attribution.md](docs/attribution.md) §4.

### A new provider executor

Add a case to `resolveProviderExecutionTarget` in [`runtime_router.go`](internal/node/daemon/runtime_router.go) returning the provider label, resource type, routing-key type, the value extracted **from the request**, and whether the value is case-insensitive. The rest — resource lookup, availability, execution, response validation, relaying — is already generic. Resolve the routing value from the request path or body, never from a header or anything else the agent controls freely.

### A new MCP action type

Add a case to `applyMCPToolMetadata` in [`toolmeta.go`](internal/node/dataplane/toolmeta.go) producing an `MCPAction`, and add the method to `mcpGatewayMessage` if it should route through the gateway. If it should *not* consume a decision, add it to `mcpPassthroughReason` instead.

### Rules that must not be broken

- **No fail-open branch.** Every error in the enforcement path and the gating hooks must deny. A test that only asserts the allow path is not a test of this code.
- **Decode strictly.** Unknown fields, duplicate keys, trailing JSON, and out-of-range values are refused. `decodeStrict` and `rejectDuplicateJSONKeys` exist to be used.
- **Never widen a secret's reach.** No new logging of a KIT, a proxy token, or an injected credential. New audit fields go through `sanitizeAuthorizeText`.
- **Trust the transport, not the payload.** Scheme, host, and port come from the connection; a value inside a JSON body is input, not authority.
- **Local checks are advisory.** A local check may only *narrow* what reaches the control plane. It may never be the sole reason something is allowed.

---

## 15. File map

Where to look, by question.

| Question | File |
| --- | --- |
| What commands exist? | [`internal/cli/cli.go`](internal/cli/cli.go) |
| What does `init` write? | [`internal/cli/init.go`](internal/cli/init.go), [`node/sandbox/claudecode.go`](internal/node/sandbox/claudecode.go), [`node/sandbox/codexhooks.go`](internal/node/sandbox/codexhooks.go) |
| How is a session minted and revoked? | [`internal/cli/hook.go`](internal/cli/hook.go), [`runtimecontract/session_client.go`](internal/runtimecontract/session_client.go) |
| What does the routes response look like? | [`internal/runtimecontract/routes.go`](internal/runtimecontract/routes.go) |
| How is a request matched to a route? | `RoutesFor` / `pathHasPrefix` in [`routes.go`](internal/runtimecontract/routes.go) |
| Why was my request rejected? | [`internal/node/daemon/runtime_router.go`](internal/node/daemon/runtime_router.go) |
| What gets TLS-terminated? | `managesSessionOrigin` in [`sandboxproxy.go`](internal/node/dataplane/sandboxproxy.go) |
| How is a connection attributed? | `resolveSession` in [`sandboxproxy.go`](internal/node/dataplane/sandboxproxy.go) |
| How is a shell command gated? | [`internal/cli/pretool.go`](internal/cli/pretool.go) |
| What is a valid decision? | [`internal/runtimecontract/decision.go`](internal/runtimecontract/decision.go) |
| How is an origin canonicalized? | [`internal/proxyscope/scope.go`](internal/proxyscope/scope.go) |
| What is in the ledger, and what is redacted? | [`internal/node/daemon/audit.go`](internal/node/daemon/audit.go), [`internal/evidence/ledger.go`](internal/evidence/ledger.go) |
| Which env var controls X? | [`internal/config/config.go`](internal/config/config.go), [`.env.example`](.env.example) |
| How does a release get published? | [`.github/workflows/release.yml`](.github/workflows/release.yml), [`docs/releasing.md`](docs/releasing.md) |

---

**Found something here that does not match what the code does?** That is a defect in this document, and worth reporting as one. Open an [issue](https://github.com/keydrisLabs/keydris-cli/issues) or ask in [Discord](https://discord.gg/3JUcXkUTu). Vulnerabilities go through [SECURITY.md](SECURITY.md) instead, never a public issue.

**Related:** [README](README.md) · [SECURITY](SECURITY.md) · [CONTRIBUTING](CONTRIBUTING.md) · [docs/sandbox](docs/sandbox.md) · [docs/attribution](docs/attribution.md) · [docs/codex](docs/codex.md)
