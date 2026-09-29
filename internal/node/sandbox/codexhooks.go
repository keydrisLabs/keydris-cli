package sandbox

import "fmt"

// Codex command gating runs as Codex hooks. PreToolUse blocks policy denials,
// approval-required commands, and authorization errors. PermissionRequest
// auto-allows policy-allowed commands and denies everything else. The hooks
// are passed per launch as -c session flags together with their trust.
// Earlier releases wrote them to $CODEX_HOME/hooks.json, where Codex skipped
// them until a user trusted them in /hooks.

// CodexHookOptions names the Keydris hook commands.
type CodexHookOptions struct {
	PreToolUseHook        string
	PermissionRequestHook string
	SessionStartHook      string
}

const codexShellMatcher = "^Bash$"
const codexHookTimeoutSeconds = 30
const codexApprovalHookTimeoutSeconds = 660
const codexApprovalStatusMessage = "Keydris policy check — approve or reject in the Keydris console if requested"

// CodexHookOverrides returns the Keydris hooks as Codex -c values. Codex loads
// them with source "sessionFlags" for one launch. Each value is a TOML array
// of matcher groups.
func CodexHookOverrides(opt CodexHookOptions) []string {
	group := func(matcher, command string, timeout int, statusMessage string) string {
		handler := fmt.Sprintf(`{type="command",command=%q,timeout=%d`, command, timeout)
		if statusMessage != "" {
			handler += fmt.Sprintf(`,statusMessage=%q`, statusMessage)
		}
		handler += "}"
		if matcher == "" {
			return fmt.Sprintf(`[{hooks=[%s]}]`, handler)
		}
		return fmt.Sprintf(`[{matcher=%q,hooks=[%s]}]`, matcher, handler)
	}
	overrides := []string{
		"hooks.PreToolUse=" + group(codexShellMatcher, opt.PreToolUseHook, codexApprovalHookTimeoutSeconds, codexApprovalStatusMessage),
		"hooks.PermissionRequest=" + group(codexShellMatcher, opt.PermissionRequestHook, codexApprovalHookTimeoutSeconds, codexApprovalStatusMessage),
	}
	if opt.SessionStartHook != "" {
		overrides = append(overrides, "hooks.SessionStart="+group("", opt.SessionStartHook, codexHookTimeoutSeconds, ""))
	}
	return overrides
}

// DeconfigureCodexHooks removes every Keydris hook entry an earlier release
// wrote to the Codex hooks file, preserving user hooks. It reports whether
// anything changed and never creates a missing file.
func DeconfigureCodexHooks(path string) (bool, error) {
	settings, err := readSettings(path)
	if err != nil {
		return false, err
	}
	if len(settings) == 0 {
		return false, nil
	}
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		return false, nil
	}
	changed := false
	for _, event := range []string{"PreToolUse", "PermissionRequest", "SessionStart"} {
		entries, ok := hooks[event].([]any)
		if !ok {
			continue
		}
		var kept []any
		for _, entry := range entries {
			filtered, removed := stripKeydrisHandlers(entry)
			if removed {
				changed = true
			}
			if !removed || filtered != nil {
				kept = append(kept, filtered)
			}
		}
		if len(kept) == 0 {
			if changed {
				delete(hooks, event)
			}
		} else {
			hooks[event] = kept
		}
	}
	if len(hooks) == 0 {
		delete(settings, "hooks")
	}
	if !changed {
		return false, nil
	}
	return true, writeSettings(path, settings)
}
