# OpenAI Codex with Keydris

Keydris manages Codex through a process wrapper so it can create the session
before Codex starts and revoke it when Codex exits.

## Codex configuration

Codex does not read `settings.json`. Its equivalent is `config.toml`; see the
companion [config.toml](config.toml) in this directory.

Copy it to one of Codex's supported locations:

```text
~/.codex/config.toml          Personal configuration
<project>/.codex/config.toml  Configuration for a trusted project
```

The example keeps Codex in `workspace-write`, retains interactive approvals,
and permits sandboxed commands to reach the local Keydris proxy. A project
configuration is loaded only after Codex trusts that project.

On native Windows, the wrapper selects Codex's `elevated` sandbox because its
managed networking requires that backend. Complete Codex's administrator-approved
sandbox setup first. The selection applies to the wrapped process only.

For usage metering, the wrapper supplies a process-scoped `CODEX_CA_CERTIFICATE`
bundle that preserves existing custom trust. The proxy observes ChatGPT-backed
Codex's HTTP and WebSocket Responses traffic; no API key or login-method change
is required. Deploy backend contracts 1.5.0 and the Codex usage-basis migration,
and restart the upgraded Keydris proxy between active sessions. Check proxy logs
for `usage observed` and `report accepted`; enabled metering alone does not prove
that reports have reached the backend. Subscription cost is an API-equivalent
estimate, not actual billing.

Command rules that require approval are blocked with an explanation: Codex cannot
reliably force a policy approval prompt from `PreToolUse`. Policy-denied commands
and authorization errors also block, regardless of Codex's approval settings.

The config file controls Codex's sandbox behavior, but it cannot own the full
Keydris identity lifecycle. Continue launching Codex through `keydris codex` so
the session is revoked on exit.

## Run

```bash
# Sign in once, binding this device to the agent (an operator assigns the
# governing policy to the agent in the Keydris console).
keydris login
keydris init codex <agent-id>

# Start the local brokered-egress proxy.
keydris proxy up

# Launch Codex inside a Keydris-managed session.
keydris codex
```

Normal Codex arguments are passed through; hook enablement and the native Windows
sandbox selection are reserved by the wrapper:

```bash
keydris codex --help
keydris codex --model <model-name>
```

After upgrading, rerun `keydris init codex <agent-id>` and review the updated hooks
in `/hooks`. The wrapper verifies that both command hooks execute and return an
explicit denial without a session before opening a governed session.

`openai` is also accepted as an alias:

```bash
keydris init openai <agent-id>
keydris openai
```

Use `keydris codex` rather than launching `codex` directly whenever Keydris
governance is required. Codex's SessionEnd notification is advisory and can be
delayed, so the wrapper is the lifecycle boundary. The daemon also stops
renewing and retires the session if that wrapped process exits abnormally.

To stop the proxy after all managed sessions have exited:

```bash
keydris proxy down
```
