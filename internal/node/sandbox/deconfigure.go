package sandbox

import (
	"path/filepath"
	"strings"
)

// RemoveOptions drives Deconfigure. The values identify what Keydris wrote so
// the inverse only strips Keydris's own entries and leaves unrelated user
// settings intact.
type RemoveOptions struct {
	HTTPProxyPort  int      // the proxy port Keydris routed the sandbox to
	CAPath         string   // the Keydris CA the env keys point at
	AllowedDomains []string // domains Keydris added (removed if still present)
}

// Deconfigure is the inverse of Configure: it removes the Keydris sandbox
// routing, CA env, and any stale Keydris hooks from the Claude Code settings
// file, preserving unrelated settings. It reports whether anything changed (so
// the caller can say "nothing to remove") and never creates a missing file.
func Deconfigure(path string, opt RemoveOptions) (bool, error) {
	settings, err := readSettings(path)
	if err != nil {
		return false, err
	}
	if len(settings) == 0 {
		return false, nil // empty or absent: nothing Keydris could have written
	}

	changed := false
	changed = unmergeSandbox(settings, opt) || changed
	changed = unmergeKeydrisHooks(settings) || changed
	changed = unmergeCAEnv(settings, opt.CAPath) || changed
	if !changed {
		return false, nil
	}
	return true, writeSettings(path, settings)
}

// unmergeSandbox strips the keys Configure set, disables enforcement (the proxy
// is going away), and drops the whole block if only a disabled flag remains.
func unmergeSandbox(settings map[string]any, opt RemoveOptions) bool {
	sb, _ := settings["sandbox"].(map[string]any)
	if sb == nil {
		return false
	}
	changed := false

	for _, k := range []string{"failIfUnavailable", "allowUnsandboxedCommands", "enableWeakerNetworkIsolation"} {
		if _, ok := sb[k]; ok {
			delete(sb, k)
			changed = true
		}
	}

	if network, ok := sb["network"].(map[string]any); ok {
		if p, ok := jsonInt(network["httpProxyPort"]); ok && p == opt.HTTPProxyPort {
			delete(network, "httpProxyPort")
			changed = true
		}
		if len(opt.AllowedDomains) > 0 {
			if remaining, did := removeStrings(network["allowedDomains"], opt.AllowedDomains); did {
				changed = true
				if len(remaining) == 0 {
					delete(network, "allowedDomains")
				} else {
					network["allowedDomains"] = remaining
				}
			}
		}
		if len(network) == 0 {
			delete(sb, "network")
		}
	}
	// Also clean the schema used by older Keydris releases.
	if len(opt.AllowedDomains) > 0 {
		if remaining, did := removeStrings(sb["allowedDomains"], opt.AllowedDomains); did {
			changed = true
			if len(remaining) == 0 {
				delete(sb, "allowedDomains")
			} else {
				sb["allowedDomains"] = remaining
			}
		}
	}

	if sb["enabled"] == true {
		sb["enabled"] = false
		changed = true
	}

	// If nothing meaningful is left (empty, or just a disabled flag), drop it so
	// the file returns to the Claude Code default (sandbox off).
	if len(sb) == 0 || (len(sb) == 1 && sb["enabled"] == false) {
		delete(settings, "sandbox")
		changed = true
	}
	return changed
}

// unmergeKeydrisHooks removes SessionStart/SessionEnd/PreToolUse hook entries
// that invoke `keydris ...` (cleaning up stale hooks from an older init),
// preserving any other user hooks.
func unmergeKeydrisHooks(settings map[string]any) bool {
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		return false
	}
	changed := false
	for _, event := range []string{"SessionStart", "SessionEnd", "PreToolUse"} {
		entries, ok := hooks[event].([]any)
		if !ok {
			continue
		}
		var kept []any
		for _, e := range entries {
			if entryReferencesKeydris(e) {
				changed = true
				continue
			}
			kept = append(kept, e)
		}
		if len(kept) == 0 {
			if _, present := hooks[event]; present && changed {
				delete(hooks, event)
			}
		} else {
			hooks[event] = kept
		}
	}
	if len(hooks) == 0 {
		delete(settings, "hooks")
	}
	return changed
}

// entryReferencesKeydris reports whether a Claude Code hook entry contains a
// command that invokes keydris.
func entryReferencesKeydris(entry any) bool {
	m, ok := entry.(map[string]any)
	if !ok {
		return false
	}
	list, _ := m["hooks"].([]any)
	for _, h := range list {
		hm, _ := h.(map[string]any)
		if cmd, ok := hm["command"].(string); ok && isKeydrisCommand(cmd) {
			return true
		}
	}
	return false
}

func isKeydrisCommand(command string) bool {
	command = strings.TrimSpace(command)
	if command == "" {
		return false
	}
	executable := command
	if command[0] == '\'' || command[0] == '"' {
		quote := command[0]
		if end := strings.IndexByte(command[1:], quote); end >= 0 {
			executable = command[1 : end+1]
		}
	} else if end := strings.IndexAny(command, " \t\r\n"); end >= 0 {
		executable = command[:end]
	}
	base := strings.ToLower(filepath.Base(executable))
	return base == "keydris" || base == "keydris.exe"
}

// unmergeCAEnv removes the CA env keys that point at the Keydris CA, dropping
// the env block if it becomes empty.
func unmergeCAEnv(settings map[string]any, caPath string) bool {
	envBlock, _ := settings["env"].(map[string]any)
	if envBlock == nil || caPath == "" {
		return false
	}
	changed := false
	for _, key := range caEnvKeys() {
		if v, ok := envBlock[key].(string); ok && v == caPath {
			delete(envBlock, key)
			changed = true
		}
	}
	if len(envBlock) == 0 {
		delete(settings, "env")
	}
	return changed
}

// removeStrings drops every entry of drop from a JSON string array, reporting
// whether anything was removed.
func removeStrings(existing any, drop []string) ([]any, bool) {
	arr, ok := existing.([]any)
	if !ok {
		return nil, false
	}
	dropSet := map[string]bool{}
	for _, d := range drop {
		dropSet[d] = true
	}
	var out []any
	changed := false
	for _, v := range arr {
		if s, ok := v.(string); ok && dropSet[s] {
			changed = true
			continue
		}
		out = append(out, v)
	}
	return out, changed
}
