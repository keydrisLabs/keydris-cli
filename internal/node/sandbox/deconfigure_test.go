package sandbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDeconfigureRemovesKeydrisPreservesUser(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")

	if err := Configure(path, Options{
		HTTPProxyPort:  15001,
		CAPath:         "/tmp/ca.crt",
		AllowedDomains: []string{"api.example"},
		Strict:         true,
	}); err != nil {
		t.Fatal(err)
	}

	// Add a user key, a user hook, and a stale keydris hook (as an old init left).
	cur := map[string]any{}
	raw, _ := os.ReadFile(path)
	_ = json.Unmarshal(raw, &cur)
	cur["model"] = "claude-x"
	cur["hooks"] = map[string]any{
		"PreToolUse":   []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "echo hi"}}}},
		"SessionStart": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "keydris hook session-start"}}}},
	}
	b, _ := json.Marshal(cur)
	_ = os.WriteFile(path, b, 0o644)

	changed, err := Deconfigure(path, RemoveOptions{
		HTTPProxyPort:  15001,
		CAPath:         "/tmp/ca.crt",
		AllowedDomains: []string{"api.example"},
	})
	if err != nil {
		t.Fatalf("Deconfigure: %v", err)
	}
	if !changed {
		t.Fatal("expected Deconfigure to report a change")
	}

	got := map[string]any{}
	raw, _ = os.ReadFile(path)
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["model"] != "claude-x" {
		t.Errorf("user key clobbered: model=%v", got["model"])
	}
	if _, ok := got["sandbox"]; ok {
		t.Errorf("sandbox block should be removed, got %v", got["sandbox"])
	}
	if _, ok := got["env"]; ok {
		t.Errorf("CA env block should be removed, got %v", got["env"])
	}
	hooks, _ := got["hooks"].(map[string]any)
	if _, ok := hooks["PreToolUse"]; !ok {
		t.Errorf("user hook PreToolUse dropped")
	}
	if _, ok := hooks["SessionStart"]; ok {
		t.Errorf("stale keydris SessionStart hook not removed")
	}

	// Idempotent: a second deinit is a no-op.
	changed2, err := Deconfigure(path, RemoveOptions{HTTPProxyPort: 15001, CAPath: "/tmp/ca.crt"})
	if err != nil {
		t.Fatal(err)
	}
	if changed2 {
		t.Errorf("second Deconfigure should be a no-op")
	}
}
