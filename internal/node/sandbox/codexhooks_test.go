package sandbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCodexHooksRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.json")
	// A pre-existing user hook must survive configure and deconfigure.
	seed := map[string]any{
		"hooks": map[string]any{
			"SessionStart": []any{map[string]any{"hooks": []any{map[string]any{
				"type": "command", "command": "custom-context",
			}}}},
			"PreToolUse": []any{
				map[string]any{"hooks": []any{map[string]any{
					"type": "command", "command": "custom-linter",
				}}},
			},
		},
	}
	raw, err := json.Marshal(seed)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	opt := CodexHookOptions{
		PreToolUseHook:        "keydris __pretool-use --codex",
		PermissionRequestHook: "keydris __permission-request",
		SessionStartHook:      "keydris __agent-context",
	}
	if err := ConfigureCodexHooks(path, opt); err != nil {
		t.Fatal(err)
	}
	wired, err := VerifyCodexHooks(path, opt)
	if err != nil || !wired {
		t.Fatalf("hooks not wired after configure: wired=%v err=%v", wired, err)
	}

	// Re-configure must not duplicate the Keydris entries.
	if err := ConfigureCodexHooks(path, opt); err != nil {
		t.Fatal(err)
	}
	settings, err := readSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	hooks := settings["hooks"].(map[string]any)
	starts := hooks["SessionStart"].([]any)
	if len(starts) != 2 {
		t.Fatalf("expected user context and one Keydris briefing, got %d", len(starts))
	}
	if !eventHasMatcherCommand(starts, "", opt.SessionStartHook) {
		t.Fatal("briefing must apply to startup, resume, clear and compact")
	}
	preTool := hooks["PreToolUse"].([]any)
	if len(preTool) != 2 {
		t.Fatalf("PreToolUse should hold the user hook plus one Keydris entry, got %d", len(preTool))
	}
	keydrisPreTool := preTool[1].(map[string]any)
	if keydrisPreTool["matcher"] != "^Bash$" {
		t.Fatalf("PreToolUse matcher = %v, want exact Bash matcher", keydrisPreTool["matcher"])
	}
	permission := hooks["PermissionRequest"].([]any)[0].(map[string]any)
	if permission["matcher"] != "^Bash$" {
		t.Fatalf("PermissionRequest matcher = %v, want exact Bash matcher", permission["matcher"])
	}

	changed, err := DeconfigureCodexHooks(path)
	if err != nil || !changed {
		t.Fatalf("deconfigure: changed=%v err=%v", changed, err)
	}
	wired, err = VerifyCodexHooks(path, opt)
	if err != nil || wired {
		t.Fatalf("hooks still wired after deconfigure: wired=%v err=%v", wired, err)
	}
	settings, err = readSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	hooks = settings["hooks"].(map[string]any)
	if starts := hooks["SessionStart"].([]any); len(starts) != 1 {
		t.Fatal("deinit must preserve only user context")
	}
	if entries := hooks["PreToolUse"].([]any); len(entries) != 1 {
		t.Fatalf("user PreToolUse hook was not preserved: %v", entries)
	}
	if _, present := hooks["PermissionRequest"]; present {
		t.Fatal("empty PermissionRequest event should be removed")
	}
}

func TestDeconfigureCodexHooksNoopWithoutFile(t *testing.T) {
	changed, err := DeconfigureCodexHooks(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil || changed {
		t.Fatalf("missing file should be a no-op: changed=%v err=%v", changed, err)
	}
}

func TestCodexHookVerificationRejectsInertHandlers(t *testing.T) {
	command := "keydris __pretool-use --codex"
	entry := func(handler map[string]any) []any {
		return []any{map[string]any{
			"matcher": codexShellMatcher,
			"hooks":   []any{handler},
		}}
	}
	valid := map[string]any{
		"type": "command", "command": command, "timeout": 30,
	}
	if !eventHasMatcherCommand(entry(valid), codexShellMatcher, command) {
		t.Fatal("valid synchronous command hook was rejected")
	}
	for _, handler := range []map[string]any{
		{"type": "prompt", "command": command, "timeout": 30},
		{"type": "command", "command": command, "timeout": 1},
		{"type": "command", "command": command, "timeout": 30, "async": true},
		{"type": "command", "command": command, "timeout": 30, "commandWindows": "echo bypass"},
		{"type": "command", "command": command, "timeout": 30, "command_windows": "echo bypass"},
	} {
		if eventHasMatcherCommand(entry(handler), codexShellMatcher, command) {
			t.Fatalf("inert hook handler passed verification: %v", handler)
		}
	}
}

func TestKeydrisCommandRecognitionHandlesQuotedAbsolutePath(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "Keydris Agent", "keydris.exe")
	for _, command := range []string{
		`"` + executable + `" __pretool-use --codex`,
		`'` + executable + `' __permission-request`,
		`& "` + executable + `" __pretool-use --codex`,
	} {
		if !isKeydrisCommand(command) {
			t.Fatalf("quoted Keydris command was not recognized: %s", command)
		}
	}
}

func TestCodexHooksMigrateWindowsCallOperatorAndPreserveUserHooks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	legacy := CodexHookOptions{
		PreToolUseHook:        `"C:/Keydris Agent/keydris.exe" __pretool-use --codex`,
		PermissionRequestHook: `"C:/Keydris Agent/keydris.exe" __permission-request`,
		SessionStartHook:      `"C:/Keydris Agent/keydris.exe" __agent-context`,
	}
	if err := ConfigureCodexHooks(path, legacy); err != nil {
		t.Fatal(err)
	}
	settings, err := readSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	hooks := settings["hooks"].(map[string]any)
	entries := hooks["PreToolUse"].([]any)
	group := entries[0].(map[string]any)
	group["hooks"] = append(group["hooks"].([]any), map[string]any{"type": "command", "command": "my-audit-hook"})
	if err := writeSettings(path, settings); err != nil {
		t.Fatal(err)
	}
	current := CodexHookOptions{
		PreToolUseHook:        "& " + legacy.PreToolUseHook,
		PermissionRequestHook: "& " + legacy.PermissionRequestHook,
		SessionStartHook:      "& " + legacy.SessionStartHook,
	}
	for i := 0; i < 2; i++ {
		if err := ConfigureCodexHooks(path, current); err != nil {
			t.Fatal(err)
		}
	}
	if wired, err := VerifyCodexHooks(path, current); err != nil || !wired {
		t.Fatalf("migrated hooks: %v, %v", wired, err)
	}
	if removed, err := DeconfigureCodexHooks(path); err != nil || !removed {
		t.Fatalf("remove migrated hooks: %v, %v", removed, err)
	}
	settings, err = readSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	hooks = settings["hooks"].(map[string]any)
	if len(hooks) != 1 {
		t.Fatalf("stale events remain: %v", hooks)
	}
	entries = hooks["PreToolUse"].([]any)
	if len(entries) != 1 {
		t.Fatalf("stale handlers remain: %v", entries)
	}
	handlers := entries[0].(map[string]any)["hooks"].([]any)
	if len(handlers) != 1 || handlers[0].(map[string]any)["command"] != "my-audit-hook" {
		t.Fatalf("user hook was changed: %v", handlers)
	}
}
