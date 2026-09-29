package sandbox

import (
	"path/filepath"
	"testing"
)

// legacyCodexHookGroup is a matcher group as earlier releases wrote it to
// hooks.json.
func legacyCodexHookGroup(matcher, command string) map[string]any {
	group := map[string]any{"hooks": []any{map[string]any{
		"type": "command", "command": command, "timeout": 30,
	}}}
	if matcher != "" {
		group["matcher"] = matcher
	}
	return group
}

func TestDeconfigureCodexHooksRemovesEarlierReleaseEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	// User hooks sit beside the Keydris entries and must survive.
	seed := map[string]any{"hooks": map[string]any{
		"SessionStart": []any{
			legacyCodexHookGroup("", "custom-context"),
			legacyCodexHookGroup("", "keydris __agent-context"),
		},
		"PreToolUse": []any{
			legacyCodexHookGroup("", "custom-linter"),
			legacyCodexHookGroup("^Bash$", "keydris __pretool-use --codex"),
		},
		"PermissionRequest": []any{legacyCodexHookGroup("^Bash$", "keydris __permission-request")},
	}}
	if err := writeSettings(path, seed); err != nil {
		t.Fatal(err)
	}
	if present, err := HasKeydrisHooks(path); err != nil || !present {
		t.Fatalf("earlier release entries not recognized: %v, %v", present, err)
	}

	changed, err := DeconfigureCodexHooks(path)
	if err != nil || !changed {
		t.Fatalf("deconfigure: changed=%v err=%v", changed, err)
	}
	if present, err := HasKeydrisHooks(path); err != nil || present {
		t.Fatalf("Keydris entries remain: %v, %v", present, err)
	}
	settings, err := readSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	hooks := settings["hooks"].(map[string]any)
	if starts := hooks["SessionStart"].([]any); len(starts) != 1 {
		t.Fatalf("user context was not preserved alone: %v", starts)
	}
	if entries := hooks["PreToolUse"].([]any); len(entries) != 1 {
		t.Fatalf("user PreToolUse hook was not preserved alone: %v", entries)
	}
	if _, present := hooks["PermissionRequest"]; present {
		t.Fatal("empty PermissionRequest event should be removed")
	}
	if changed, err := DeconfigureCodexHooks(path); err != nil || changed {
		t.Fatalf("second deconfigure changed the file: %v, %v", changed, err)
	}
}

func TestDeconfigureCodexHooksNoopWithoutFile(t *testing.T) {
	changed, err := DeconfigureCodexHooks(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil || changed {
		t.Fatalf("missing file should be a no-op: changed=%v err=%v", changed, err)
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

func TestDeconfigureCodexHooksRemovesBothWindowsFormsAndKeepsUserHandlers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	const keydris = `"C:/Keydris Agent/keydris.exe"`
	// Earlier releases wrote the plain quoted form, then the PowerShell call
	// operator form. A user handler shares the first group.
	shared := legacyCodexHookGroup("^Bash$", keydris+" __pretool-use --codex")
	shared["hooks"] = append(shared["hooks"].([]any), map[string]any{"type": "command", "command": "my-audit-hook"})
	seed := map[string]any{"hooks": map[string]any{
		"PreToolUse": []any{
			shared,
			legacyCodexHookGroup("^Bash$", "& "+keydris+" __pretool-use --codex"),
		},
		"PermissionRequest": []any{legacyCodexHookGroup("^Bash$", "& "+keydris+" __permission-request")},
		"SessionStart":      []any{legacyCodexHookGroup("", keydris+" __agent-context")},
	}}
	if err := writeSettings(path, seed); err != nil {
		t.Fatal(err)
	}
	if removed, err := DeconfigureCodexHooks(path); err != nil || !removed {
		t.Fatalf("remove earlier hooks: %v, %v", removed, err)
	}
	settings, err := readSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	hooks := settings["hooks"].(map[string]any)
	if len(hooks) != 1 {
		t.Fatalf("stale events remain: %v", hooks)
	}
	entries := hooks["PreToolUse"].([]any)
	if len(entries) != 1 {
		t.Fatalf("stale handlers remain: %v", entries)
	}
	handlers := entries[0].(map[string]any)["hooks"].([]any)
	if len(handlers) != 1 || handlers[0].(map[string]any)["command"] != "my-audit-hook" {
		t.Fatalf("user hook was changed: %v", handlers)
	}
}

func TestCodexHookOverrides(t *testing.T) {
	got := CodexHookOverrides(CodexHookOptions{
		PreToolUseHook:        `'/opt/key dris/keydris' __pretool-use --codex`,
		PermissionRequestHook: `keydris "__permission-request"`,
		SessionStartHook:      "keydris __agent-context",
	})
	want := []string{
		`hooks.PreToolUse=[{matcher="^Bash$",hooks=[{type="command",command="'/opt/key dris/keydris' __pretool-use --codex",timeout=660}]}]`,
		`hooks.PermissionRequest=[{matcher="^Bash$",hooks=[{type="command",command="keydris \"__permission-request\"",timeout=660}]}]`,
		`hooks.SessionStart=[{hooks=[{type="command",command="keydris __agent-context",timeout=30}]}]`,
	}
	if len(got) != len(want) {
		t.Fatalf("overrides = %q", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("override %d = %s, want %s", i, got[i], want[i])
		}
	}
	if withoutBriefing := CodexHookOverrides(CodexHookOptions{PreToolUseHook: "a", PermissionRequestHook: "b"}); len(withoutBriefing) != 2 {
		t.Fatalf("an empty session hook must not be wired: %q", withoutBriefing)
	}
}
