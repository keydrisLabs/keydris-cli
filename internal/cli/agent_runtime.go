package cli

import (
	"path/filepath"
	"strings"
)

// Agent runtime identifiers reported to the control plane when a session is
// minted, so the console can attribute sessions and decisions to the coding
// tool that produced them. Values mirror the control plane's `agent_runtime`
// enum; an unknown tool is reported as "" (omitted) rather than guessed.
const (
	agentRuntimeClaudeCode = "claude_code"
	agentRuntimeCodex      = "codex"
)

// agentRuntimeForCommand maps the executable `keydris run` wraps to a runtime
// identifier. `keydris codex` funnels through `keydris run -- codex …`, so the
// Codex launcher is covered here too. Windows shims (`claude.cmd`, `codex.exe`)
// resolve by their base name.
func agentRuntimeForCommand(command string) string {
	name := strings.ToLower(filepath.Base(strings.TrimSpace(command)))
	name = strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(name, ".exe"), ".cmd"), ".bat")
	switch name {
	case "claude":
		return agentRuntimeClaudeCode
	case "codex":
		return agentRuntimeCodex
	default:
		return ""
	}
}
