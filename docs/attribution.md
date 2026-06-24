# Cross-OS attribution: the problem and the solution ladder

This document explains why per-session enforcement degrades on macOS/Windows
today, and lays out a tiered plan to fix it — from a portable change we can ship
immediately to the kernel-grade planes that are the long-term product answer.

> **Status:** Tier 1 (per-session proxy tokens) is **implemented** for the
> default `sandbox` plane, so concurrent Claude sessions are attributed
> independently — see [§2](#2-tier-1--authenticated-proxy-sessions-implemented).
> Tiers 2–3 remain future work.

## 1. The problem, precisely

"Attribution" is two distinct sub-problems that are easy to conflate:

1. **Identity binding** — *which session/identity opened this connection?*
   Without it, the broker knows only where traffic is going, not who is sending
   it.
2. **Non-bypassability** — *can the agent escape interception entirely?*
   Only a kernel-level redirect can guarantee the agent cannot opt out.

They fail independently and have different fixes. Today:

| Plane | OS | Identity binding | Non-bypassable |
| --- | --- | --- | --- |
| `transparent` | Linux | yes (`/proc`, eBPF upgrade) | yes (iptables REDIRECT) |
| `proxyenv` | all | **no** | no (`HTTP_PROXY` is opt-in) |

### How attribution works on Linux (the reference path)

The transparent plane resolves every accepted connection before consulting the
broker ([internal/node/dataplane/transparent_linux.go](../internal/node/dataplane/transparent_linux.go)):

```
src IP:port -> /proc/net/tcp (socket inode) -> /proc/*/fd (pid)
            -> /proc/<pid>/cgroup -> session registry -> SVID
```

The cgroup is the **binding point**: `keydris run` places the
agent process into a cgroup and registers `cgroup -> SVID` with the daemon over
the session socket ([internal/node/sessionsock](../internal/node/sessionsock/sessionsock.go)).
The eBPF tracer ([internal/node/ebpf](../internal/node/ebpf/doc.go)) is the race-free
upgrade of the same join: it records `{4-tuple} -> {pid, cgroup_id}` in the
kernel at `connect()` time.

### What breaks on macOS/Windows

The `proxyenv` plane parses the destination out of the absolute-form proxy
request — and that is *all* it knows. There is no kernel hook, no cgroup, and
the resolver is a no-op ([internal/node/attest/attest_other.go](../internal/node/attest/attest_other.go)).
`Flow.SVID` stays empty, so the broker takes its degraded, destination-only
branch (the broker lives in the separate control-plane repo):

```go
if blueprint != "" {
    candidates = p.store.ForBlueprint(blueprint)
} else {
    // Degraded destination-only mode: consider every grant.
    candidates = p.store.All()
}
```

Consequences:

- **Policy collapses to destination-only.** The question changes from "may
  *this session* reach `dst`?" to "may *anyone* reach `dst`?" — effectively the
  union of all grants' destinations.
- **The audit trail loses identity.** Ledger records carry an empty
  `spiffe_id`/`blueprint` for proxied flows.
- **Bypassable.** An agent can unset `HTTP_PROXY` and skip Keydris entirely.

The root cause of the identity-binding failure is *not* the missing kernel — it
is that the connection arrives carrying **no link back to the session** that the
hook minted an SVID for. That observation drives Tier 1.

## 2. Tier 1 — Authenticated proxy sessions (implemented)

**Fixes identity binding on every OS, including concurrent sessions. Does not fix
bypassability.**

HTTP proxies already have a standard channel for caller identity:
`Proxy-Authorization`. Every mainstream HTTP client (curl, Python requests, Node,
Go, git) automatically sends it when the proxy URL carries userinfo:

```
HTTPS_PROXY=http://keydris:<session-token>@127.0.0.1:15001
```

How Keydris wires it:

1. **Token issuance.** `hookSessionStart` mints the SVID and registers the
   session under a random per-session token as its handle (`newProxyToken`,
   [internal/cli/hook.go](../internal/cli/hook.go)); the registry indexes
   sessions by that token
   ([internal/node/attest/attest.go](../internal/node/attest/attest.go)).
2. **Client wiring — two entry points:**
   - `keydris run` sets `HTTP_PROXY`/`HTTPS_PROXY` to the token-bearing URL in the
     wrapped command's environment
     ([internal/cli/run.go](../internal/cli/run.go)).
   - For a real Claude session the internal SessionStart hook
     (`keydris __session-start`) appends the same export to Claude Code's
     `$CLAUDE_ENV_FILE`
     ([internal/cli/session_hook.go](../internal/cli/session_hook.go)), which
     Claude Code sources for every Bash subprocess in that session. Each
     concurrent session runs its own SessionStart hook and so receives a distinct
     token.
3. **Plane lookup.** The sandbox proxy reads `Proxy-Authorization` (Basic, token
   as the password), looks the token up in the registry, and populates
   `Flow.SVID`/`Flow.SessionID` (`resolveSession`,
   [internal/node/dataplane/sandboxproxy.go](../internal/node/dataplane/sandboxproxy.go)).
   A token that is *presented but unknown* resolves to **unattributed** rather
   than guessing — it is never silently downgraded to "the sole session", which
   is what keeps concurrent sessions isolated. Only a request with **no** token
   falls back to the sole registered session (the single-session convenience
   case).
4. **Broker unchanged.** With a real SVID present, the broker's per-session path
   (verify against JWKS, derive blueprint, check grant) applies on every OS.

Properties:

- **Per-session policy + audit on macOS/Windows/Linux**, including multiple
  concurrent sessions through a single proxy.
- The token is a *bearer* credential confined to loopback; it never leaves the
  machine and is unregistered at `session-end`.

Limitations and caveats:

- **Bypassable.** The agent can still unset the env var, and a co-resident
  process that steals the token could impersonate the session. Tier 1 makes the
  fallback *honest* (fully attributed), not *mandatory*.
- **Claude Code coupling.** The Claude path relies on `$CLAUDE_ENV_FILE` and on a
  hook-set `HTTP_PROXY` composing with the sandbox's own `httpProxyPort`
  routing. That composition is not documented and may vary by Claude Code
  version — re-verify on upgrades. `keydris run` does not depend on it.
- **Token in logs.** The session socket currently logs the handle (= token) at
  registration; treat the daemon log as sensitive (see SECURITY.md).
- Variant: **one loopback port per session** also works (the listen port itself
  identifies the session) but consumes ports; the token is the cleaner default.

## 3. Tier 2 — Native userspace pid lookup on macOS (optional middle rung)

**Restores OS-observed (not just claimed) attribution on macOS. Still bypassable.**

macOS has a `/proc` analog for sockets: **`libproc`**
(`proc_listpids` + `proc_pidfdinfo`, the machinery behind `lsof -i`). A
`attest_darwin.go` resolver can map `src IP:port -> pid` the same way
[attest_linux.go](../internal/attest/attest_linux.go) does.

The missing piece is the cgroup analog — the kernel-visible "this process
belongs to session X" marker. The natural substitute is the **process group**:

1. `keydris run` starts the child in its own process group (`Setpgid`).
2. The hook registers `pgid -> SVID` instead of `cgroup -> SVID`.
3. The resolver walks `pid -> pgid -> session registry -> SVID`.

This is stronger than Tier 1 because the *kernel* (not the client) asserts which
process sent the bytes — an agent cannot lie about its token. It is weaker than
Tier 3 because nothing forces traffic through the proxy in the first place.
Worth doing if the macOS demo needs credible attribution; skippable if Tier 1
suffices for the POC.

## 4. Tier 3 — Kernel-grade planes per OS (the product answer)

**Fixes both identity binding and bypassability. Distribution-heavy.**

Each OS has a sanctioned interception point, and notably the macOS/Windows APIs
are *better* than Linux's in one respect: they hand you the originating process
directly, so no 4-tuple join is needed.

| OS | Mechanism | What you get | Cost |
| --- | --- | --- | --- |
| Linux | iptables/eBPF (**built**) | redirect + 4-tuple→{pid, cgroup} join | clang/BTF at build time |
| macOS | `NETransparentProxyProvider` (Network Extension) | every flow arrives with the source process's **audit token, pid, and code-signing identifier** | signed + notarized system extension, network-extension entitlement, user approval flow |
| Windows | WFP ALE-layer callout (or WinDivert) | flow events include the **process id / image path** at connect time | driver signing (or WinDivert dependency), admin install |

Two points worth emphasizing:

- The macOS code-signing identifier is *cryptographic* process identity —
  stronger than a cgroup label. Policy could bind grants to signed binaries,
  not just sessions.
- The `DataPlane` interface ([internal/dataplane/dataplane.go](../internal/dataplane/dataplane.go))
  and the `attest.Resolver` seam were designed for exactly this: a macOS NE
  plane or a Windows WFP plane is a new implementation of the same interface,
  with zero changes to the broker, issuer, or L7 path. The hard work is
  packaging and signing, not architecture.

See [distribution.md](distribution.md) for the platform rollout strategy this
slots into.

## 5. Comparison summary

| | Identity binding | Kernel-asserted | Non-bypassable | Works on | Effort |
| --- | --- | --- | --- | --- | --- |
| Today: proxyenv | none | — | no | all | — |
| Tier 1: proxy session tokens (**built**) | yes (claimed) | no | no | all | done |
| Tier 2: libproc + pgid (macOS) | yes | yes | no | macOS | ~2-3 days |
| Tier 3: NE / WFP planes | yes | yes | yes | macOS / Windows | weeks (mostly signing/packaging) |
| Reference: transparent + eBPF | yes | yes | yes | Linux | built |

## 6. Recommendation

1. **Tier 1 is implemented** for the sandbox plane: per-session tokens eliminate
   the destination-only degradation, and concurrent sessions are attributed
   independently. The broker's degraded branch now only triggers for flows
   outside any keydris session.
2. **Tier 2 only if the macOS demo must show kernel-asserted attribution.**
3. **Keep Tier 3 as the post-POC roadmap item** it already is in
   [plan.md section 7](../plan.md): it is a packaging/signing project more than
   a code project, and the architecture is already shaped for it.
