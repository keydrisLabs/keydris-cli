# Keydris with OpenAI Codex

Keydris supports the OpenAI Codex CLI through a lifecycle-owning wrapper:

```bash
keydris login
keydris init codex <agent-id>
keydris proxy scope add api.example.com:443
keydris proxy up
keydris codex [normal Codex arguments...]
```

`keydris init openai <agent-id>` and `keydris openai` are accepted aliases.

Codex has a SessionEnd hook, but it is advisory and may run well after an idle
session. Keydris therefore does not depend on it for credential cleanup.
`keydris codex` uses the same
`keydris run` boundary as any wrapped command:

1. Mint and register a per-session SVID.
2. Enable Codex's sandboxed network access and `network_proxy`, which honors
   the authenticated Keydris upstream proxy inherited from the environment.
3. Launch Codex with the additive CA environment.
4. Bind peer verification to the Codex process tree where supported.
5. Revoke and unregister the session when Codex exits.

The proxy daemon renews the session before its KIT expires and atomically
updates both its live registry and the hook state. Normal exit revokes the most
recent renewed session, not only the credential minted at startup. The wrapper
also registers Codex's PID plus its OS process-creation identity; after an
abnormal wrapper/process exit, the daemon withholds renewal and retires the
session after a short grace period without being fooled by PID reuse.

Use selected Keydris proxy scope for the APIs you intend to govern. Unlisted
traffic, including Codex's own model connection, then remains an opaque CONNECT
tunnel and keeps its original credentials.

Always launch through `keydris codex` when Keydris governance is required.
Starting `codex` directly does not create a Keydris session.

## Command gating

`keydris init codex` wires exact `^Bash$` hooks into
`$CODEX_HOME/hooks.json` (default `~/.codex/hooks.json`) that check shell
commands against the policy's command rules
(`POST /v1/runtime/commands/authorize`):

- **PreToolUse** (`keydris __pretool-use --codex`) emits an explicit deny for
  policy-rejected commands — and for every failure path (no session, control
  plane unreachable, timeout), because a crashed or silent hook fails open.
- **PermissionRequest** (`keydris __permission-request`) auto-allows
  policy-allowed commands and stays silent otherwise, so approval-required
  commands fall through to Codex's interactive prompt. Pair it with
  `approval_policy = "untrusted"` (see `examples/codex/config.toml`) —
  Codex's PreToolUse cannot answer "ask", so the prompt is how a human
  resolves `approval_required` decisions.

The hooks resolve the Keydris session from the `KEYDRIS_SESSION` environment
variable that the `keydris codex` wrapper exports.

**One-time trust step:** Codex does not run hooks from a new hooks file until
you confirm them. Run `/hooks` once inside Codex after `keydris init codex`
and accept the Keydris entries. `keydris status` reports whether the hooks are
wired; `keydris deinit codex` removes them.

The installed hook command uses the absolute Keydris executable path. The
wrapper refuses to start when the exact Bash hooks are missing, forces the
`features.hooks` feature on, and rejects command-line arguments that disable or
override it. For managed environments where users must not be able to disable
governance, deploy the same absolute hooks through Codex
`requirements.toml`; user-level hook trust is not an administrator boundary.

Command-policy wildcards stop at shell operators and dynamic syntax, so a rule
such as `git status*` cannot authorize `git status && <another command>`.
Compound syntax must be written explicitly in a matching rule. Bare
interactive shells and interpreters are rejected because later stdin does not
create another hook authorization event.

On Windows, Node receives the Keydris CA through `NODE_EXTRA_CA_CERTS`; native
tools continue using the Windows root store. Use `--trust-store` during init to
add the Keydris CA to the current user's Windows root store for native tools.
macOS uses its login keychain for the optional trust-store installation.
