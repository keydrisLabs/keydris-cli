package config

import (
	"path/filepath"
	"testing"
)

func TestEnvDisabled(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"off", true},
		{" OFF ", true},
		{"0", true},
		{"false", true},
		{"FALSE", true},
		{"no", true},
		{"on", false},
		{"1", false},
		{"true", false},
		{"yes", false},
		{"", false},
	} {
		t.Setenv("KEYDRIS_COST_METERING", tc.value)
		if got := envDisabled("KEYDRIS_COST_METERING"); got != tc.want {
			t.Errorf("envDisabled(%q) = %v, want %v", tc.value, got, tc.want)
		}
	}
}

func TestDefaultCodexHooksHonorsCodexHome(t *testing.T) {
	root := filepath.Join(t.TempDir(), "custom-codex")
	t.Setenv("CODEX_HOME", root)
	if got, want := defaultCodexHooks(), filepath.Join(root, "hooks.json"); got != want {
		t.Fatalf("defaultCodexHooks() = %q, want %q", got, want)
	}
}
