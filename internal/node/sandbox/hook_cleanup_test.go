package sandbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDeconfigurePreservesOtherHandlersInSameGroup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	original := `{"hooks":{"PreToolUse":[{"matcher":"^Bash$","hooks":[{"type":"command","command":"keydris __pretool-use --codex"},{"type":"command","command":"my-audit-hook"}]}]}}`
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	if changed, err := DeconfigureCodexHooks(path); err != nil || !changed {
		t.Fatalf("remove: %v %v", changed, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatal(err)
	}
	entries := settings["hooks"].(map[string]any)["PreToolUse"].([]any)
	handlers := entries[0].(map[string]any)["hooks"].([]any)
	if len(handlers) != 1 || handlers[0].(map[string]any)["command"] != "my-audit-hook" {
		t.Fatal("unrelated handler removed")
	}
}
