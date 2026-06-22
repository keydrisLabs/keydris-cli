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
//	    "allowedDomains": [...],
//	    "network": { "httpProxyPort": 15001 }
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
}

// Configure merges the Keydris sandbox block, session hooks, and CA env into the
// Claude Code settings file at path, preserving unrelated settings.
func Configure(path string, opt Options) error {
	settings, err := readSettings(path)
	if err != nil {
		return err
	}

	mergeSandbox(settings, opt)
	mergeHooks(settings)
	if opt.CAPath != "" {
		mergeCAEnv(settings, opt.CAPath)
	}

	return writeSettings(path, settings)
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
	sb["network"] = network

	if len(opt.AllowedDomains) > 0 {
		sb["allowedDomains"] = mergeStringSet(sb["allowedDomains"], opt.AllowedDomains)
	}

	settings["sandbox"] = sb
}

// keydrisHooks is the hook block Keydris owns inside Claude Code settings.
func keydrisHooks() map[string]any {
	cmd := func(c string) map[string]any {
		return map[string]any{"hooks": []any{map[string]any{"type": "command", "command": c}}}
	}
	return map[string]any{
		"SessionStart": []any{cmd("keydris hook session-start")},
		"SessionEnd":   []any{cmd("keydris hook session-end")},
	}
}

func mergeHooks(settings map[string]any) {
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	for event, val := range keydrisHooks() {
		hooks[event] = val
	}
	settings["hooks"] = hooks
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
