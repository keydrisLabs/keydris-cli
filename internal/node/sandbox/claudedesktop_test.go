package sandbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyDesktopEngineSettingsFresh(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	proxy := "http://keydris:handle@127.0.0.1:15001"
	ca := "/tmp/keydris-ca.pem"

	prev, existed, err := ApplyDesktopEngineSettings(path, DesktopEngineOptions{
		ProxyURL:         proxy,
		CABundlePath:     ca,
		SessionStartHook: "keydris __session-start",
		SessionEndHook:   "keydris __session-end",
		PreToolUseHook:   "keydris __pretool-use",
	})
	if err != nil {
		t.Fatalf("ApplyDesktopEngineSettings: %v", err)
	}
	if existed || prev != nil {
		t.Fatalf("fresh file should report !existed and nil previous, got existed=%v prev=%q", existed, prev)
	}

	got := readSettingsFile(t, path)
	if _, ok := got["sandbox"]; ok {
		t.Fatalf("Desktop settings must not enable sandbox: %v", got["sandbox"])
	}
	envBlock := got["env"].(map[string]any)
	for _, key := range desktopProxyEnvKeys {
		if envBlock[key] != proxy {
			t.Errorf("env[%s]=%v, want %s", key, envBlock[key], proxy)
		}
	}
	for _, key := range caEnvKeys() {
		if envBlock[key] != ca {
			t.Errorf("CA env[%s]=%v, want %s", key, envBlock[key], ca)
		}
	}
	hooks := got["hooks"].(map[string]any)
	assertHookCommand(t, hooks["SessionStart"], "keydris __session-start")
	assertHookCommand(t, hooks["SessionEnd"], "keydris __session-end")
	assertHookCommand(t, hooks["PreToolUse"], "keydris __pretool-use")
}

func TestApplyDesktopEngineSettingsPreservesUserHooksAndEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	pre := map[string]any{
		"env": map[string]any{
			"MY_CUSTOM_VAR": "keep-me",
		},
		"hooks": map[string]any{
			"SessionStart": []any{map[string]any{"hooks": []any{
				map[string]any{"type": "command", "command": "echo user-start"},
			}}},
		},
	}
	writeJSON(t, path, pre)

	_, existed, err := ApplyDesktopEngineSettings(path, DesktopEngineOptions{
		ProxyURL:         "http://127.0.0.1:15001",
		CABundlePath:     "/tmp/ca.pem",
		SessionStartHook: "keydris __session-start",
		SessionEndHook:   "keydris __session-end",
		PreToolUseHook:   "keydris __pretool-use",
	})
	if err != nil {
		t.Fatalf("ApplyDesktopEngineSettings: %v", err)
	}
	if !existed {
		t.Fatal("expected existed=true for pre-written settings")
	}

	got := readSettingsFile(t, path)
	envBlock := got["env"].(map[string]any)
	if envBlock["MY_CUSTOM_VAR"] != "keep-me" {
		t.Errorf("unrelated env key lost: %v", envBlock["MY_CUSTOM_VAR"])
	}
	hooks := got["hooks"].(map[string]any)
	start := hooks["SessionStart"].([]any)
	if len(start) != 2 {
		t.Fatalf("user SessionStart hook was clobbered: %v", start)
	}
	assertHookCommand(t, start, "echo user-start")
	assertHookCommand(t, start, "keydris __session-start")
}

func TestRestoreDesktopEngineSettings(t *testing.T) {
	t.Run("created file is deleted", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "settings.json")
		prev, existed, err := ApplyDesktopEngineSettings(path, DesktopEngineOptions{
			ProxyURL: "http://127.0.0.1:15001",
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := RestoreDesktopEngineSettings(path, prev, existed); err != nil {
			t.Fatalf("RestoreDesktopEngineSettings: %v", err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expected created settings to be removed, err=%v", err)
		}
	})

	t.Run("pre-existing file restored", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "settings.json")
		original := []byte("{\n  \"model\": \"keep\"\n}\n")
		if err := os.WriteFile(path, original, 0o644); err != nil {
			t.Fatal(err)
		}
		prev, existed, err := ApplyDesktopEngineSettings(path, DesktopEngineOptions{
			ProxyURL: "http://127.0.0.1:15001",
		})
		if err != nil {
			t.Fatal(err)
		}
		if !existed {
			t.Fatal("expected existed=true")
		}
		if err := RestoreDesktopEngineSettings(path, prev, existed); err != nil {
			t.Fatalf("RestoreDesktopEngineSettings: %v", err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(original) {
			t.Fatalf("restore mismatch:\n got=%q\nwant=%q", got, original)
		}
	})
}

func TestApplyDesktopEngineSettingsRejectsBadPath(t *testing.T) {
	if _, _, err := ApplyDesktopEngineSettings("", DesktopEngineOptions{}); err == nil {
		t.Fatal("expected error for empty path")
	}
	dir := t.TempDir()
	if _, _, err := ApplyDesktopEngineSettings(dir, DesktopEngineOptions{ProxyURL: "http://127.0.0.1:1"}); err == nil {
		t.Fatal("expected error for directory path")
	}
}

func TestApplyEgressProxyPinExistingDocument(t *testing.T) {
	dir := t.TempDir()
	id := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	writeJSON(t, filepath.Join(dir, "_meta.json"), map[string]any{"appliedId": id})
	writeJSON(t, filepath.Join(dir, id+".json"), map[string]any{
		"theme":   "dark",
		"feature": true,
	})

	snap, err := ApplyEgressProxyPin(dir, "http://127.0.0.1:18080")
	if err != nil {
		t.Fatalf("ApplyEgressProxyPin: %v", err)
	}
	if snap.ConfigName != id+".json" {
		t.Fatalf("ConfigName=%q, want %s.json", snap.ConfigName, id)
	}
	if !snap.MetaExisted || !snap.ConfigExisted {
		t.Fatalf("expected both files to have existed: %+v", snap)
	}

	doc := readSettingsFile(t, filepath.Join(dir, id+".json"))
	if doc["egressProxyUrl"] != "http://127.0.0.1:18080" {
		t.Errorf("egressProxyUrl=%v", doc["egressProxyUrl"])
	}
	if doc["theme"] != "dark" || doc["feature"] != true {
		t.Errorf("unrelated keys lost: %v", doc)
	}
}

func TestApplyEgressProxyPinEmptyDirCreateAndRestore(t *testing.T) {
	dir := t.TempDir()
	snap, err := ApplyEgressProxyPin(dir, "http://127.0.0.1:18080")
	if err != nil {
		t.Fatalf("ApplyEgressProxyPin: %v", err)
	}
	if snap.ConfigName == "" || !strings.HasSuffix(snap.ConfigName, ".json") {
		t.Fatalf("unexpected ConfigName %q", snap.ConfigName)
	}
	if snap.MetaExisted || snap.ConfigExisted {
		t.Fatalf("empty dir should report !existed: %+v", snap)
	}

	meta := readSettingsFile(t, filepath.Join(dir, "_meta.json"))
	id, _ := meta["appliedId"].(string)
	if !appliedIDPattern.MatchString(id) {
		t.Fatalf("appliedId %q does not match pattern", id)
	}
	if snap.ConfigName != id+".json" {
		t.Fatalf("ConfigName=%q, want %s.json", snap.ConfigName, id)
	}
	doc := readSettingsFile(t, filepath.Join(dir, snap.ConfigName))
	if doc["egressProxyUrl"] != "http://127.0.0.1:18080" {
		t.Errorf("egressProxyUrl=%v", doc["egressProxyUrl"])
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected only meta+config, got %d entries", len(entries))
	}

	if err := RestoreEgressProxyPin(dir, snap); err != nil {
		t.Fatalf("RestoreEgressProxyPin: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "_meta.json")); !os.IsNotExist(err) {
		t.Fatalf("meta should be removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, snap.ConfigName)); !os.IsNotExist(err) {
		t.Fatalf("config should be removed: %v", err)
	}
}

func TestApplyEgressProxyPinRejectsUserinfo(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "untouched.txt")
	if err := os.WriteFile(marker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ApplyEgressProxyPin(dir, "http://keydris:handle@127.0.0.1:18080")
	if err == nil {
		t.Fatal("expected error for userinfo proxy URL")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "untouched.txt" {
		t.Fatalf("reject must write nothing, got %v", entries)
	}
}

func TestApplyRestoreEgressProxyPinRoundTrip(t *testing.T) {
	dir := t.TempDir()
	id := "11111111-2222-4333-8444-555555555555"
	writeJSON(t, filepath.Join(dir, "_meta.json"), map[string]any{"appliedId": id})
	originalDoc := map[string]any{
		"locale": "en",
		"other":  42,
	}
	configPath := filepath.Join(dir, id+".json")
	writeJSON(t, configPath, originalDoc)
	originalBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}

	snap, err := ApplyEgressProxyPin(dir, "http://127.0.0.1:19090")
	if err != nil {
		t.Fatal(err)
	}
	applied := readSettingsFile(t, configPath)
	if _, ok := applied["egressProxyUrl"]; !ok {
		t.Fatal("expected egressProxyUrl after apply")
	}

	if err := RestoreEgressProxyPin(dir, snap); err != nil {
		t.Fatalf("RestoreEgressProxyPin: %v", err)
	}
	restored, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != string(originalBytes) {
		t.Fatalf("round-trip bytes mismatch:\n got=%s\nwant=%s", restored, originalBytes)
	}
	doc := readSettingsFile(t, configPath)
	if _, ok := doc["egressProxyUrl"]; ok {
		t.Fatalf("egressProxyUrl must not remain after restore: %v", doc)
	}
	if doc["locale"] != "en" {
		t.Errorf("locale lost: %v", doc["locale"])
	}
}

func TestRestoreEgressProxyPinRequiresConfigName(t *testing.T) {
	if err := RestoreEgressProxyPin(t.TempDir(), EgressSnapshot{}); err == nil {
		t.Fatal("expected error for empty ConfigName")
	}
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readSettingsFile(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]any{}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func assertHookCommand(t *testing.T, event any, want string) {
	t.Helper()
	entries, ok := event.([]any)
	if !ok {
		t.Fatalf("hook event is not an array: %T", event)
	}
	found := false
	var walk func(any)
	walk = func(v any) {
		switch typed := v.(type) {
		case map[string]any:
			if cmd, ok := typed["command"].(string); ok && cmd == want {
				found = true
			}
			for _, child := range typed {
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(entries)
	if !found {
		t.Fatalf("hook command %q not found in %v", want, event)
	}
}
