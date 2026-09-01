// Package sandbox configures Claude Code's built-in sandbox to route Bash
// subprocess egress through the Keydris proxy and to trust the Keydris CA. This
// is the genuinely new work in the v2 pivot (plan_v1.md section 4): instead of
// building a kernel data plane, Keydris rides Claude Code's sandbox as the
// custom proxy it documents (sandbox.network.httpProxyPort + a trusted CA).
//
// All keys here match the live Claude Code docs:
//
//	{
//	  "sandbox": {
//	    "enabled": true,
//	    "failIfUnavailable": true,
//	    "allowUnsandboxedCommands": false,
//	    "enableWeakerNetworkIsolation": true,   // macOS, required for MITM+custom CA
//	    "network": {
//	      "httpProxyPort": 15001,
//	      "allowedDomains": [...]
//	    }
//	  },
//	  "hooks": { "SessionStart": [...], "SessionEnd": [...] },
//	  "env": { "NODE_EXTRA_CA_CERTS": "...", "CURL_CA_BUNDLE": "...", ... }
//	}
//
// The merge is non-clobbering: existing user settings (other hooks, env,
// allowedDomains) are preserved.
package sandbox

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
)

// Options drives Configure. CAPath is the Keydris CA written by the proxy; the
// agent's subprocess tools are pointed at it via the env block (see catrust.go).
type Options struct {
	HTTPProxyPort  int
	AllowedDomains []string
	CAPath         string
	// Strict makes the sandbox a hard security gate: refuse to start if the
	// sandbox is unavailable and forbid the unsandboxed escape hatch. This is
	// what makes enforcement non-bypassable (plan_v1.md section 7).
	Strict bool
	// SessionStartHook / SessionEndHook, when both set, are wired into the
	// Claude Code SessionStart/SessionEnd hooks so each session binds a fresh
	// per-session SVID (and revokes it on end). Empty disables hook wiring.
	SessionStartHook string
	SessionEndHook   string
	// PreToolUseHook, when set, gates every shell command through the policy's
	// command rules before Claude Code executes it. Empty disables the gate.
	PreToolUseHook string
}

// Configure merges the Keydris sandbox block, per-session hooks, and CA env into
// the Claude Code settings file at path, preserving unrelated settings.
func Configure(path string, opt Options) error {
	settings, err := readSettings(path)
	if err != nil {
		return err
	}

	mergeSandbox(settings, opt)
	if opt.SessionStartHook != "" && opt.SessionEndHook != "" {
		mergeHooks(settings, opt.SessionStartHook, opt.SessionEndHook)
	}
	if opt.PreToolUseHook != "" {
		mergePreToolUseHook(settings, opt.PreToolUseHook)
	}
	if opt.CAPath != "" {
		mergeCAEnv(settings, opt.CAPath)
	}

	return writeSettings(path, settings)
}

// mergeHooks wires the per-session SessionStart/SessionEnd hook commands into the
// settings, preserving any unrelated user hooks (other events stay untouched).
func mergeHooks(settings map[string]any, startCmd, endCmd string) {
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	entry := func(c string) any {
		return map[string]any{"hooks": []any{map[string]any{"type": "command", "command": c}}}
	}
	mergeEvent := func(event, command string) {
		existing, _ := hooks[event].([]any)
		kept := make([]any, 0, len(existing)+1)
		for _, candidate := range existing {
			if !entryReferencesKeydris(candidate) {
				kept = append(kept, candidate)
			}
		}
		hooks[event] = append(kept, entry(command))
	}
	mergeEvent("SessionStart", startCmd)
	mergeEvent("SessionEnd", endCmd)
	settings["hooks"] = hooks
}

// mergePreToolUseHook gates shell tools through the command-authorize hook.
// The hook fails closed internally, and its harness timeout stays well above
// the hook's own 5s control-plane budget so the harness never kills it first
// (a killed hook fails OPEN in Claude Code).
func mergePreToolUseHook(settings map[string]any, command string) {
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	matcher := "Bash"
	if runtime.GOOS == "windows" {
		matcher = "Bash|PowerShell"
	}
	existing, _ := hooks["PreToolUse"].([]any)
	kept := make([]any, 0, len(existing)+1)
	for _, candidate := range existing {
		if !entryReferencesKeydris(candidate) {
			kept = append(kept, candidate)
		}
	}
	hooks["PreToolUse"] = append(kept, map[string]any{
		"matcher": matcher,
		"hooks": []any{map[string]any{
			"type": "command", "command": command, "timeout": 30,
		}},
	})
	settings["hooks"] = hooks
}

func mergeSandbox(settings map[string]any, opt Options) {
	sb, _ := settings["sandbox"].(map[string]any)
	if sb == nil {
		sb = map[string]any{}
	}
	sb["enabled"] = true
	if opt.Strict {
		sb["failIfUnavailable"] = true
		sb["allowUnsandboxedCommands"] = false
	}
	// macOS Go-based CLIs (curl/git via libcurl, gh, ...) fail TLS verification
	// under Seatbelt when MITM'd; the docs require this flag with a custom CA.
	if runtime.GOOS == "darwin" {
		sb["enableWeakerNetworkIsolation"] = true
	}

	network, _ := sb["network"].(map[string]any)
	if network == nil {
		network = map[string]any{}
	}
	network["httpProxyPort"] = opt.HTTPProxyPort

	// Migrate the old top-level placement written by early Keydris builds even
	// when this run adds no new domains.
	existing := network["allowedDomains"]
	if legacy, ok := sb["allowedDomains"]; ok {
		existing = mergeStringSet(existing, stringsFromJSON(legacy))
		delete(sb, "allowedDomains")
	}
	if len(opt.AllowedDomains) > 0 {
		network["allowedDomains"] = mergeStringSet(existing, opt.AllowedDomains)
	} else if existing != nil {
		network["allowedDomains"] = existing
	}
	sb["network"] = network
	settings["sandbox"] = sb
}

func stringsFromJSON(value any) []string {
	var out []string
	if values, ok := value.([]any); ok {
		for _, candidate := range values {
			if text, ok := candidate.(string); ok {
				out = append(out, text)
			}
		}
	}
	return out
}

// mergeCAEnv points the agent's subprocess tools at the Keydris CA so they trust
// the proxy's terminated TLS. Setting the env in settings.json means the
// sandbox subprocesses inherit it.
func mergeCAEnv(settings map[string]any, caPath string) {
	envBlock, _ := settings["env"].(map[string]any)
	if envBlock == nil {
		envBlock = map[string]any{}
	}
	for _, key := range caEnvKeys() {
		envBlock[key] = caPath
	}
	settings["env"] = envBlock
}

// mergeStringSet unions an existing JSON array (any) with extra entries.
func mergeStringSet(existing any, extra []string) []any {
	seen := map[string]bool{}
	var out []any
	if arr, ok := existing.([]any); ok {
		for _, v := range arr {
			if s, ok := v.(string); ok && !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	add := append([]string(nil), extra...)
	sort.Strings(add)
	for _, s := range add {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func readSettings(path string) (map[string]any, error) {
	settings := map[string]any{}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return settings, nil
		}
		return nil, err
	}
	if len(b) == 0 {
		return settings, nil
	}
	if err := json.Unmarshal(b, &settings); err != nil {
		return nil, fmt.Errorf("parse existing %s: %w", path, err)
	}
	return settings, nil
}

func writeSettings(path string, settings map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o644)
}
