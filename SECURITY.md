# Security Policy

Keydris is security software: it exists to keep credentials off agent machines
and to enforce policy on agent egress. We take vulnerabilities in it seriously
and appreciate coordinated disclosure.

## Reporting a vulnerability

**Please do not report security vulnerabilities through public GitHub issues,
discussions, or pull requests.**

Report privately through either channel:

- **GitHub:** [Report a vulnerability](https://github.com/keydrisLabs/keydris-cli/security/advisories/new)
  (Security tab → *Report a vulnerability*) — preferred.
- **Email:** [security@keydris.com](mailto:security@keydris.com)

Include what you can of the following — it speeds up triage significantly:

- The affected component (CLI command, proxy data plane, daemon, hooks,
  evidence ledger, installer, npm packages) and version (`keydris version`).
- A proof of concept or reproduction steps.
- The impact you believe it has, and any suggested remediation.

### What to expect

- We will acknowledge your report within **3 business days**.
- We will keep you informed as we triage, and work with you on a coordinated
  disclosure timeline (typically within **90 days** of the report).
- With your permission, we will credit you in the release notes for the fix.

We do not currently run a paid bounty program.

## Scope

In scope for this repository: the `keydris` binary and everything it installs
or writes — the CLI, the proxy data planes, the local daemon, the session and
identity handling, command-gating hooks, the evidence ledger, `install.sh`,
and the `@keydris` npm packages.

Vulnerabilities in the **Keydris control plane** or other Keydris services are
handled through the same channels — report them the same way and we will route
them.

Out of scope: vulnerabilities requiring a compromised machine owner account
acting against itself, denial of service against your own local proxy, and
issues in third-party dependencies without a demonstrated impact on Keydris
(please report those upstream too).

## Security model

Read this before relying on Keydris in an adversarial setting — the guarantees
are specific and worth understanding:

- **Enforcement strength depends on the data plane.** The strong
  un-bypassability guarantee holds while the harness sandbox
  (Seatbelt/bubblewrap) or the Linux transparent redirect routes all egress to
  the proxy. The `proxyenv` data plane is **intentionally bypassable** and
  provides attribution, not enforcement. See
  [docs/attribution.md](docs/attribution.md) for the trust-tier ladder.
- **Fail-closed command gating.** Every error path in command authorization —
  no session, unreachable control plane, timeout, malformed payload — produces
  an explicit deny. Client-side decisions are additionally re-enforced
  server-side at execution time.
- **Secrets stay off the agent machine.** Governed calls are executed by the
  control plane; the private key from `keydris login` never leaves your device.
  The per-session proxy token is a short-lived bearer credential scoped to the
  session and delivered only through owner-only files.
- **Local logs can contain application data.** Authorization records in the
  evidence ledger intentionally include full MCP arguments and JSON request
  bodies, which may contain application secrets. `~/.keydris-data/` is created
  `0700` with `0600` files — treat `evidence.jsonl` and `proxy.log` as
  sensitive.
- **The evidence ledger is tamper-evident, not tamper-proof.** `keydris logs`
  verifies the hash chain; a verifier must know the true tip to detect
  truncation.

## Supported versions

Security fixes are released for the latest release on the `stable` channel.
Run `keydris upgrade` (or update the `@keydris/cli` npm package) to get the
current version; older versions are not patched retroactively.
