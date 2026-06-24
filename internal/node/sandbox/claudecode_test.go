package sandbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestConfigureMergesAndPreserves(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")

	// Pre-existing user settings that must survive the merge.
	pre := map[string]any{
		"model": "claude-x",
		"hooks": map[string]any{
			"PreToolUse": []any{map[string]any{"hooks": []any{}}},
		},
		"sandbox": map[string]any{
			"allowedDomains": []any{"existing.example"},
		},
	}
	b, _ := json.Marshal(pre)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}

	err := Configure(path, Options{
		HTTPProxyPort:    15001,
		AllowedDomains:   []string{"api.example"},
		CAPath:           "/tmp/ca.crt",
		Strict:           true,
		SessionStartHook: "keydris __session-start",
		SessionEndHook:   "keydris __session-end",
	})
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}

	got := map[string]any{}
	raw, _ := os.ReadFile(path)
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}

	if got["model"] != "claude-x" {
		t.Errorf("unrelated key clobbered: model=%v", got["model"])
	}
	hooks := got["hooks"].(map[string]any)
	if _, ok := hooks["PreToolUse"]; !ok {
		t.Errorf("existing hook PreToolUse dropped")
	}
	if _, ok := hooks["SessionStart"]; !ok {
		t.Errorf("SessionStart hook not wired")
	}

	sb := got["sandbox"].(map[string]any)
	if sb["enabled"] != true {
		t.Errorf("sandbox.enabled not set")
	}
	if sb["allowUnsandboxedCommands"] != false {
		t.Errorf("strict: allowUnsandboxedCommands should be false, got %v", sb["allowUnsandboxedCommands"])
	}
	net := sb["network"].(map[string]any)
	if int(net["httpProxyPort"].(float64)) != 15001 {
		t.Errorf("httpProxyPort=%v, want 15001", net["httpProxyPort"])
	}
	domains := sb["allowedDomains"].([]any)
	if len(domains) != 2 {
		t.Errorf("allowedDomains should union existing+new, got %v", domains)
	}

	envBlock := got["env"].(map[string]any)
	if envBlock["NODE_EXTRA_CA_CERTS"] != "/tmp/ca.crt" {
		t.Errorf("CA env not set: %v", envBlock["NODE_EXTRA_CA_CERTS"])
	}
}

func TestVerifyReportsDrift(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := Configure(path, Options{HTTPProxyPort: 15001, CAPath: "/tmp/ca.crt", Strict: true, SessionStartHook: "keydris __session-start", SessionEndHook: "keydris __session-end"}); err != nil {
		t.Fatal(err)
	}

	st, err := Verify(path, 15001)
	if err != nil {
		t.Fatal(err)
	}
	if !st.OK() {
		t.Errorf("expected OK after Configure, warnings=%v", st.Warnings)
	}

	// A port mismatch must surface as drift.
	drift, err := Verify(path, 9999)
	if err != nil {
		t.Fatal(err)
	}
	if drift.OK() {
		t.Errorf("expected drift for wrong port")
	}
	if len(drift.Warnings) == 0 {
		t.Errorf("expected a warning for port drift")
	}

	// A missing settings file must report not-enforced, not error.
	none, err := Verify(filepath.Join(dir, "nope.json"), 15001)
	if err != nil {
		t.Fatalf("Verify on missing file should not error: %v", err)
	}
	if none.OK() {
		t.Errorf("missing settings should not be OK")
	}
}
