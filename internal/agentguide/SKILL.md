---
name: keydris-authority
description: Work within Keydris delegated authority in a Keydris-configured agent session; interpret action denials, approval requests, and CLI diagnostics. Use when Keydris governs the current task or the user requests Keydris administration.
---

# Keydris: Authority before action

Keydris provides authority infrastructure for the autonomous economy. The CLI
binds an agent session to delegated authority. Its command hooks and local proxy
ask the control plane to authorize governed actions before execution, and
attribute those actions to the session. User intent defines the task; the
applicable authority determines which governed actions may execute.

## Work in the existing session

Use the configured agent tools and inherited Keydris environment. Claude Code
starts its session through hooks; Codex terminal sessions launch with
`keydris codex`. Do not nest wrappers or mint another session for each command.
A running proxy, cached policy scope, or this skill's presence does not prove
that a particular action is authorized or that every tool is governed.

## Handle decisions

- **Allowed:** continue the requested work through the configured execution path.
- **Approval required:** use the approval flow presented by the agent. Do not
  treat pending approval as permission or repeatedly resubmit the request.
- **Denied:** explain the reported reason and continue permitted work. Do not
  reproduce the denied effect through another shell, tool, endpoint, encoding,
  credential, or session, or disable Keydris to make it succeed. A different
  action that is independently permitted can still be useful.
- **Authority unavailable:** report the connection, identity, or session error.
  Failure to obtain a decision is not an allow. Diagnose before retrying; do not
  describe an infrastructure error as a policy rejection.

## Diagnose without changing authority

`keydris status --json` provides structured readiness checks and next steps;
exit 1 means attention is needed. `keydris doctor` includes resolved paths.
Use `keydris status --offline --json` when checking local setup without a
control-plane connectivity request. `keydris help` describes supported commands.
Do not print credentials, private keys, session tokens, or authenticated proxy
URLs. Share only the diagnostic details needed for the user's task.

In WSL2, keep the Linux CLI, agent, proxy, and state in the same distribution and
user account. Use absolute Linux paths and Linux executables. VS Code integrated
terminal sessions use the same launch flow; sidebar integrations are separate.

## Administration

When the user explicitly requests authorized Keydris administration, help with
that task within the existing permissions. Reset, deinit, stopping the proxy,
removing certificate trust, and changing policy are administrative changes;
they are not automatic remedies for denied work. For a requested reset, inspect
`keydris reset --dry-run` before applying it and honor the CLI's confirmation.

This skill is behavioral guidance. Runtime authorization, hooks, proxy routing,
and the agent sandbox provide enforcement; instructions do not replace them.
