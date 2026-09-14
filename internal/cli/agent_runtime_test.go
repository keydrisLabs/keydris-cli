package cli

import "testing"

func TestAgentRuntimeForCommand(t *testing.T) {
	cases := map[string]string{
		"claude":                          agentRuntimeClaudeCode,
		"/usr/local/bin/claude":           agentRuntimeClaudeCode,
		`C:\Users\dev\AppData\claude.CMD`: agentRuntimeClaudeCode,
		"codex":                           agentRuntimeCodex,
		"./node_modules/.bin/codex":       agentRuntimeCodex,
		"codex.exe":                       agentRuntimeCodex,
		"python":                          "",
		"":                                "",

		// Windows shim paths must resolve on every host: WSL interop and
		// pasted commands can hand the wrapper a backslash path on Linux.
		`C:\Program Files\nodejs\claude.cmd`: agentRuntimeClaudeCode,
		`C:\tools\codex.CMD`:                 agentRuntimeCodex,
		"C:/tools/codex.exe":                 agentRuntimeCodex,
		`C:\tools\claude.bat`:                agentRuntimeClaudeCode,
		"  codex  ":                          agentRuntimeCodex,
		"/opt/claude/other":                  "",
		`C:\tools\other.exe`:                 "",
	}
	for command, want := range cases {
		if got := agentRuntimeForCommand(command); got != want {
			t.Errorf("agentRuntimeForCommand(%q) = %q, want %q", command, got, want)
		}
	}
}
