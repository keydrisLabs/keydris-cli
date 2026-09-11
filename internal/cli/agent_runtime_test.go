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
	}
	for command, want := range cases {
		if got := agentRuntimeForCommand(command); got != want {
			t.Errorf("agentRuntimeForCommand(%q) = %q, want %q", command, got, want)
		}
	}
}
