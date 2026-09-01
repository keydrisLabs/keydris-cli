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
	} {
		if !isKeydrisCommand(command) {
			t.Fatalf("quoted Keydris command was not recognized: %s", command)
		}
	}
}
