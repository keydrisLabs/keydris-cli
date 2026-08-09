package config

import (
	"path/filepath"
	"testing"
)

func TestDefaultCodexHooksHonorsCodexHome(t *testing.T) {
	root := filepath.Join(t.TempDir(), "custom-codex")
	t.Setenv("CODEX_HOME", root)
	if got, want := defaultCodexHooks(), filepath.Join(root, "hooks.json"); got != want {
		t.Fatalf("defaultCodexHooks() = %q, want %q", got, want)
	}
}
