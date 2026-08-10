package sandbox

// Codex command gating lives in $CODEX_HOME/hooks.json rather than the Claude
// settings file. Two events cooperate because Codex's PreToolUse cannot answer
// "ask" (see internal/cli/pretool.go): PreToolUse carries explicit denials,
// PermissionRequest resolves policy-allowed commands, and everything else
// falls through to the interactive prompt. Codex requires a one-time `/hooks`
// trust confirmation before it runs commands from this file.

// CodexHookOptions names the hook commands `keydris init codex` wires.
type CodexHookOptions struct {
	PreToolUseHook        string
	PermissionRequestHook string
}

const codexShellMatcher = "^Bash$"
const minCodexHookTimeoutSeconds = 10

// ConfigureCodexHooks merges the Keydris command-gating hooks into the Codex
// hooks file, replacing stale Keydris entries and preserving user hooks.
func ConfigureCodexHooks(path string, opt CodexHookOptions) error {
	settings, err := readSettings(path)
	if err != nil {
		return err
	}
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	merge := func(event, command string) {
		existing, _ := hooks[event].([]any)
		kept := make([]any, 0, len(existing)+1)
		for _, candidate := range existing {
			if !entryReferencesKeydris(candidate) {
				kept = append(kept, candidate)
			}
		}
		hooks[event] = append(kept, map[string]any{
			"matcher": codexShellMatcher,
			"hooks": []any{map[string]any{
				"type": "command", "command": command, "timeout": 30,
			}},
		})
	}
	merge("PreToolUse", opt.PreToolUseHook)
	merge("PermissionRequest", opt.PermissionRequestHook)
	settings["hooks"] = hooks
	return writeSettings(path, settings)
}

// DeconfigureCodexHooks removes every Keydris hook entry from the Codex hooks
// file, preserving user hooks. It reports whether anything changed and never
// creates a missing file.
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
	for _, event := range []string{"PreToolUse", "PermissionRequest"} {
		entries, ok := hooks[event].([]any)
		if !ok {
			continue
		}
		var kept []any
		for _, entry := range entries {
			if entryReferencesKeydris(entry) {
				changed = true
				continue
			}
			kept = append(kept, entry)
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

// VerifyCodexHooks reports whether both Keydris command-gating hooks are wired
// in the Codex hooks file.
func VerifyCodexHooks(path string, opt CodexHookOptions) (bool, error) {
	settings, err := readSettings(path)
	if err != nil {
		return false, err
	}
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		return false, nil
	}
	return eventHasMatcherCommand(hooks["PreToolUse"], codexShellMatcher, opt.PreToolUseHook) &&
		eventHasMatcherCommand(hooks["PermissionRequest"], codexShellMatcher, opt.PermissionRequestHook), nil
}

func eventHasMatcherCommand(value any, matcher, command string) bool {
	entries, _ := value.([]any)
	for _, entry := range entries {
		group, _ := entry.(map[string]any)
		if configuredMatcher, _ := group["matcher"].(string); configuredMatcher != matcher {
			continue
		}
		handlers, _ := group["hooks"].([]any)
		for _, handler := range handlers {
			hook, _ := handler.(map[string]any)
			configuredCommand, _ := hook["command"].(string)
			hookType, _ := hook["type"].(string)
			async, _ := hook["async"].(bool)
			if configuredCommand == command && hookType == "command" && !async &&
				hookTimeoutSeconds(hook["timeout"]) >= minCodexHookTimeoutSeconds {
				return true
			}
		}
	}
	return false
}

func hookTimeoutSeconds(value any) float64 {
	switch timeout := value.(type) {
	case float64:
		return timeout
	case int:
		return float64(timeout)
	default:
		return 0
	}
}
