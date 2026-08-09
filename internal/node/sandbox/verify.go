package sandbox

import (
	"fmt"
	"os"
)

// Status reports whether the Claude Code sandbox is configured the way Keydris
// needs it. Enforcement only holds while the sandbox is enabled and pointed at
// the Keydris proxy (plan_v1.md section 7), so `keydris status` surfaces drift.
type Status struct {
	SettingsExists bool
	Enabled        bool
	ProxyPort      int  // sandbox.network.httpProxyPort found in settings (0 if unset)
	PortMatches    bool // ProxyPort == expected
	Strict         bool // failIfUnavailable=true and allowUnsandboxedCommands=false
	HooksWired     bool // exact Keydris SessionStart + SessionEnd hooks present
	CommandGate    bool // Keydris PreToolUse command-authorization hook present
	Warnings       []string
}

// OK reports whether enforcement is intact: sandbox enabled and routed to the
// Keydris proxy port.
func (s Status) OK() bool {
	return s.Enabled && s.PortMatches && s.Strict && s.HooksWired && s.CommandGate
}

// Verify inspects the settings file and reports the sandbox enforcement state
// against the expected proxy port.
func Verify(path string, expectedPort int) (Status, error) {
	st := Status{}
	settings, err := readSettings(path)
	if err != nil {
		return st, err
	}
	if _, statErr := os.Stat(path); statErr == nil {
		st.SettingsExists = true
	}

	sb, _ := settings["sandbox"].(map[string]any)
	if sb == nil {
		st.Warnings = append(st.Warnings, "no sandbox block: egress is NOT enforced (run `keydris init claude-code`)")
		return st, nil
	}

	st.Enabled, _ = sb["enabled"].(bool)
	if !st.Enabled {
		st.Warnings = append(st.Warnings, "sandbox.enabled is false: the agent can bypass the proxy")
	}
	failClosed, _ := sb["failIfUnavailable"].(bool)
	allowUnsandboxed, allowPresent := sb["allowUnsandboxedCommands"].(bool)
	st.Strict = failClosed && allowPresent && !allowUnsandboxed
	if !st.Strict {
		st.Warnings = append(st.Warnings, "strict sandbox fields are missing or weakened: require failIfUnavailable=true and allowUnsandboxedCommands=false")
	}

	if network, ok := sb["network"].(map[string]any); ok {
		if p, ok := jsonInt(network["httpProxyPort"]); ok {
			st.ProxyPort = p
		}
	}
	st.PortMatches = st.ProxyPort == expectedPort
	if !st.PortMatches {
		st.Warnings = append(st.Warnings, fmt.Sprintf(
			"sandbox.network.httpProxyPort=%d but Keydris proxy is on %d: egress is not routed to Keydris",
			st.ProxyPort, expectedPort))
	}

	if hooks, ok := settings["hooks"].(map[string]any); ok {
		hasStart := eventHasCommand(hooks["SessionStart"], "keydris __session-start")
		hasEnd := eventHasCommand(hooks["SessionEnd"], "keydris __session-end")
		st.HooksWired = hasStart && hasEnd
		st.CommandGate = eventHasCommand(hooks["PreToolUse"], "keydris __pretool-use")
	}
	if !st.HooksWired {
		st.Warnings = append(st.Warnings, "SessionStart/SessionEnd hooks not wired: sessions get no per-session SVID (re-run `keydris init claude-code <agent-id>`)")
	}
	if !st.CommandGate {
		st.Warnings = append(st.Warnings, "PreToolUse hook not wired: shell commands bypass the policy's command rules (re-run `keydris init claude-code <agent-id>`)")
	}

	return st, nil
}

func eventHasCommand(value any, want string) bool {
	entries, _ := value.([]any)
	for _, entry := range entries {
		group, _ := entry.(map[string]any)
		handlers, _ := group["hooks"].([]any)
		for _, handler := range handlers {
			hook, _ := handler.(map[string]any)
			if command, _ := hook["command"].(string); command == want {
				return true
			}
		}
	}
	return false
}

// jsonInt coerces a JSON number (float64) or int into an int.
func jsonInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	}
	return 0, false
}
