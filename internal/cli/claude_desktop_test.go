package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/keydrisLabs/keydris-cli/internal/config"
	"github.com/keydrisLabs/keydris-cli/internal/node/sandbox"
)

func TestSweepDesktopLaunchRestoresPin(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{DataDir: dir, HTTPProxyPort: 15001}
	settings := filepath.Join(dir, "claude-desktop", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"env":{"MY_CUSTOM_VAR":"keep"}}` + "\n")
	if err := os.WriteFile(settings, original, 0o600); err != nil {
		t.Fatal(err)
	}
	library := filepath.Join(dir, "configLibrary")
	if err := os.MkdirAll(library, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(library, "_meta.json"), []byte(`{"appliedId":"11111111-1111-4111-8111-111111111111"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	applied := filepath.Join(library, "11111111-1111-4111-8111-111111111111.json")
	if err := os.WriteFile(applied, []byte(`{"keep":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	previous, existed, err := sandbox.ApplyDesktopEngineSettings(settings, sandbox.DesktopEngineOptions{
		ProxyURL: "http://keydris:handle@127.0.0.1:15001",
	})
	if err != nil {
		t.Fatal(err)
	}
	egress, err := sandbox.ApplyEgressProxyPin(library, "http://127.0.0.1:9")
	if err != nil {
		t.Fatal(err)
	}
	rec := desktopLaunchRecord{
		SettingsPath:     settings,
		SettingsPrevious: previous,
		SettingsExisted:  existed,
		ConfigLibrary:    library,
		Egress:           egress,
	}
	if err := writeDesktopLaunch(cfg, rec); err != nil {
		t.Fatal(err)
	}
	if err := sweepDesktopLaunch(cfg); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("settings = %s", got)
	}
	pinned, err := os.ReadFile(applied)
	if err != nil {
		t.Fatal(err)
	}
	if string(pinned) != `{"keep":true}` {
		t.Fatalf("applied config = %s", pinned)
	}
	if _, err := os.Stat(desktopLaunchRecordPath(cfg)); !os.IsNotExist(err) {
		t.Fatalf("launch record still present: %v", err)
	}
}

func TestSweepDesktopLaunchSkipsLiveLauncher(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{DataDir: dir}
	settings := filepath.Join(dir, "claude-desktop", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"env":{"MY_CUSTOM_VAR":"keep"}}` + "\n")
	if err := os.WriteFile(settings, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := sandbox.ApplyDesktopEngineSettings(settings, sandbox.DesktopEngineOptions{
		ProxyURL: "http://keydris:handle@127.0.0.1:15001",
	}); err != nil {
		t.Fatal(err)
	}
	if err := writeDesktopLaunch(cfg, desktopLaunchRecord{
		LauncherPID:  os.Getpid(),
		SettingsPath: settings,
	}); err != nil {
		t.Fatal(err)
	}
	if err := sweepDesktopLaunch(cfg); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) == string(original) {
		t.Fatal("live launcher was swept")
	}
	if _, err := os.Stat(desktopLaunchRecordPath(cfg)); err != nil {
		t.Fatal(err)
	}
}

func TestDeinitClaudeDesktopRestoresPinAndLeavesClaudeCode(t *testing.T) {
	dir := t.TempDir()
	claude := filepath.Join(dir, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(claude), 0o700); err != nil {
		t.Fatal(err)
	}
	claudeBody := []byte("{\"keep\":\"claude-code\"}\n")
	if err := os.WriteFile(claude, claudeBody, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{DataDir: dir, ClaudeSettingsPath: claude}
	settings := filepath.Join(dir, "claude-desktop", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	library := filepath.Join(dir, "configLibrary")
	if err := os.MkdirAll(library, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(library, "_meta.json"), []byte(`{"appliedId":"11111111-1111-4111-8111-111111111111"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	applied := filepath.Join(library, "11111111-1111-4111-8111-111111111111.json")
	if err := os.WriteFile(applied, []byte(`{"keep":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	previous, existed, err := sandbox.ApplyDesktopEngineSettings(settings, sandbox.DesktopEngineOptions{
		ProxyURL: "http://keydris:handle@127.0.0.1:15001",
	})
	if err != nil {
		t.Fatal(err)
	}
	egress, err := sandbox.ApplyEgressProxyPin(library, "http://127.0.0.1:9")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeDesktopLaunch(cfg, desktopLaunchRecord{
		SettingsPath:     settings,
		SettingsPrevious: previous,
		SettingsExisted:  existed,
		ConfigLibrary:    library,
		Egress:           egress,
	}); err != nil {
		t.Fatal(err)
	}
	if _, changed, err := deinitClaudeDesktop(cfg); err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "claude-desktop")); !os.IsNotExist(err) {
		t.Fatalf("desktop settings dir still present: %v", err)
	}
	got, err := os.ReadFile(claude)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(claudeBody) {
		t.Fatalf("claude code settings = %s", got)
	}
	pinned, err := os.ReadFile(applied)
	if err != nil {
		t.Fatal(err)
	}
	if string(pinned) != `{"keep":true}` {
		t.Fatalf("applied config = %s", pinned)
	}
}

func TestDeinitClaudeDesktopRefusesLiveLauncher(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{DataDir: dir}
	settings := filepath.Join(dir, "claude-desktop", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeDesktopLaunch(cfg, desktopLaunchRecord{LauncherPID: os.Getpid(), SettingsPath: settings}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := deinitClaudeDesktop(cfg); err == nil {
		t.Fatal("live launcher should refuse deinit")
	}
	if _, err := os.Stat(settings); err != nil {
		t.Fatal(err)
	}
}

func TestConfigureClaudeDesktopLeavesClaudeCodeSettings(t *testing.T) {
	dir := t.TempDir()
	claude := filepath.Join(dir, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(claude), 0o700); err != nil {
		t.Fatal(err)
	}
	claudeBody := []byte("{\"keep\":\"claude-code\"}\n")
	if err := os.WriteFile(claude, claudeBody, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{DataDir: dir, ClaudeSettingsPath: claude, CABundlePath: filepath.Join(dir, "ca.pem")}
	if err := configureClaudeDesktop(cfg, sandbox.Options{
		CAPath:           cfg.CABundlePath,
		SessionStartHook: "keydris __session-start",
		SessionEndHook:   "keydris __session-end",
		PreToolUseHook:   "keydris __pretool-use",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(claude)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(claudeBody) {
		t.Fatalf("claude code settings = %s", got)
	}
	present, err := sandbox.HasKeydrisHooks(desktopSettingsPath(cfg))
	if err != nil || !present {
		t.Fatalf("desktop hooks present=%v err=%v", present, err)
	}
}

func TestDesktopMCPInventoryPathsHonorOverrides(t *testing.T) {
	wantConfig := filepath.Join(t.TempDir(), "claude_desktop_config.json")
	wantRoot := filepath.Join(t.TempDir(), "plugins")
	t.Setenv("KEYDRIS_CLAUDE_DESKTOP_MCP_CONFIG", wantConfig)
	t.Setenv("KEYDRIS_CLAUDE_DESKTOP_PLUGIN_ROOTS", wantRoot)
	configPath, roots := desktopMCPInventoryPaths()
	if configPath != wantConfig || len(roots) != 1 || roots[0] != wantRoot {
		t.Fatalf("config=%q roots=%v", configPath, roots)
	}
}

func TestRunClaudeDesktopRejectsArguments(t *testing.T) {
	if code := runClaudeDesktop([]string{"extra"}); code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
}

func TestRunClaudeDesktopRefusesWhenAppIsOpen(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the open-app check runs after the macOS gate")
	}
	previous := claudeDesktopRunning
	claudeDesktopRunning = func() bool { return true }
	t.Cleanup(func() { claudeDesktopRunning = previous })
	if code := runClaudeDesktop(nil); code == 0 {
		t.Fatal("open Claude should refuse a new session")
	}
}
