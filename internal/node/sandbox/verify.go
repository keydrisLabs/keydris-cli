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
	HooksWired     bool
	Warnings       []string
}

// OK reports whether enforcement is intact: sandbox enabled and routed to the
// Keydris proxy port.
func (s Status) OK() bool {
	return s.Enabled && s.PortMatches
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
		_, hasStart := hooks["SessionStart"]
		_, hasEnd := hooks["SessionEnd"]
		st.HooksWired = hasStart && hasEnd
	}
	if !st.HooksWired {
		st.Warnings = append(st.Warnings, "SessionStart/SessionEnd hooks not wired: sessions get no per-session SVID")
	}

	return st, nil
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
