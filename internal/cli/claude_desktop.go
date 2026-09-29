package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/keydrisLabs/keydris-cli/internal/config"
	"github.com/keydrisLabs/keydris-cli/internal/node/desktopproxy"
	"github.com/keydrisLabs/keydris-cli/internal/node/sandbox"
)

// claudeDesktopRunning reports whether the Claude Desktop GUI is already open.
// A second launch would ignore the proxy pin, which Desktop reads only at startup.
var claudeDesktopRunning = defaultClaudeDesktopRunning

func defaultClaudeDesktopRunning() bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	return exec.Command("pgrep", "-x", "Claude").Run() == nil
}

func desktopSettingsDir(cfg *config.Config) string {
	return filepath.Join(cfg.DataDir, "claude-desktop")
}

func desktopSettingsPath(cfg *config.Config) string {
	return filepath.Join(desktopSettingsDir(cfg), "settings.json")
}

func desktopLaunchRecordPath(cfg *config.Config) string {
	return filepath.Join(cfg.DataDir, "claude-desktop-launch.json")
}

// desktopConfigLibraryDir is the local managed-config directory Claude Desktop
// reads at launch. KEYDRIS_CLAUDE_DESKTOP_CONFIG_LIBRARY points a test or spike
// at a temporary directory instead of the real one.
func desktopConfigLibraryDir() (string, error) {
	if dir := os.Getenv("KEYDRIS_CLAUDE_DESKTOP_CONFIG_LIBRARY"); dir != "" {
		return dir, nil
	}
	if runtime.GOOS != "darwin" {
		return "", fmt.Errorf("Claude Desktop config library is resolved on macOS")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("home directory is unavailable")
	}
	return filepath.Join(home, "Library", "Application Support", "Claude-3p", "configLibrary"), nil
}

type desktopLaunchRecord struct {
	LauncherPID      int                    `json:"launcher_pid"`
	SessionID        string                 `json:"session_id"`
	SettingsPath     string                 `json:"settings_path"`
	SettingsPrevious []byte                 `json:"settings_previous,omitempty"`
	SettingsExisted  bool                   `json:"settings_existed"`
	ConfigLibrary    string                 `json:"config_library"`
	Egress           sandbox.EgressSnapshot `json:"egress"`
}

// desktopSessionID returns the session `keydris claude-desktop` minted for this
// Desktop launch, or "" outside one. Every Code-tab session in the launch
// shares it; hook payloads carry the embedded engine's own session id instead.
// A `keydris run` nested inside Desktop owns a narrower session, which wins.
func desktopSessionID() string {
	if os.Getenv(sessionOwnerEnv) == sessionOwnerRun {
		return ""
	}
	return os.Getenv(desktopSessionEnv)
}

// desktopSessionLive reports whether the keydris process that owns the pin is
// still running. Claude's own pid is stored on the session, not in this record.
func desktopSessionLive(rec *desktopLaunchRecord) bool {
	return rec != nil && processAlive(rec.LauncherPID)
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	_, err := processIdentity(pid)
	return !errors.Is(err, errProcessNotRunning)
}

func readDesktopLaunch(cfg *config.Config) (*desktopLaunchRecord, error) {
	raw, err := os.ReadFile(desktopLaunchRecordPath(cfg))
	if err != nil {
		return nil, err
	}
	var rec desktopLaunchRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

func writeDesktopLaunch(cfg *config.Config, rec desktopLaunchRecord) error {
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	path := desktopLaunchRecordPath(cfg)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

// sweepDesktopLaunch restores a proxy pin left behind when the launcher died.
// A record whose process is still running is left alone.
func sweepDesktopLaunch(cfg *config.Config) error {
	rec, err := readDesktopLaunch(cfg)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if desktopSessionLive(rec) {
		return nil
	}
	if err := finishDesktopLaunch(cfg, rec); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "keydris: restored a Claude Desktop proxy pin left by a previous session")
	if rec.SessionID != "" {
		_ = hookSessionEnd(cfg, rec.SessionID)
	}
	return nil
}

func maybeSweepDesktopLaunch() {
	cfg := config.Load()
	if err := sweepDesktopLaunch(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "keydris: desktop session cleanup: %v\n", err)
	}
}

func finishDesktopLaunch(cfg *config.Config, rec *desktopLaunchRecord) error {
	var restoreErr error
	if rec.SettingsPath != "" {
		if err := sandbox.RestoreDesktopEngineSettings(rec.SettingsPath, rec.SettingsPrevious, rec.SettingsExisted); err != nil {
			restoreErr = err
		}
	}
	if rec.ConfigLibrary != "" && rec.Egress.ConfigName != "" {
		if err := sandbox.RestoreEgressProxyPin(rec.ConfigLibrary, rec.Egress); err != nil && restoreErr == nil {
			restoreErr = err
		}
	}
	if restoreErr != nil {
		return restoreErr
	}
	if err := os.Remove(desktopLaunchRecordPath(cfg)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func configureClaudeDesktop(cfg *config.Config, opt sandbox.Options) error {
	path := desktopSettingsPath(cfg)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	_, _, err := sandbox.ApplyDesktopEngineSettings(path, sandbox.DesktopEngineOptions{
		CABundlePath:     opt.CAPath,
		SessionStartHook: opt.SessionStartHook,
		SessionEndHook:   opt.SessionEndHook,
		PreToolUseHook:   opt.PreToolUseHook,
	})
	return err
}

// deinitClaudeDesktop removes the Keydris-owned Desktop settings directory and
// restores a pin left by a launcher that is no longer running. It does not
// touch Claude Code settings.
func deinitClaudeDesktop(cfg *config.Config) (string, bool, error) {
	dir := desktopSettingsDir(cfg)
	rec, err := readDesktopLaunch(cfg)
	if err != nil && !os.IsNotExist(err) {
		return dir, false, err
	}
	if desktopSessionLive(rec) {
		return dir, false, fmt.Errorf("quit `keydris claude-desktop` before deinit")
	}
	if err := sweepDesktopLaunch(cfg); err != nil {
		return dir, false, err
	}
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return dir, false, nil
	}
	if err != nil {
		return dir, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || !filepath.IsAbs(dir) || filepath.Base(dir) != "claude-desktop" {
		return dir, false, fmt.Errorf("refusing to remove %s", dir)
	}
	if err := os.RemoveAll(dir); err != nil {
		return dir, false, err
	}
	return dir, true, nil
}

func runClaudeDesktop(args []string) int {
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "usage: keydris claude-desktop")
		return 2
	}
	if runtime.GOOS != "darwin" {
		fmt.Fprintln(os.Stderr, "keydris claude-desktop: launch is supported on macOS")
		return 1
	}
	if claudeDesktopRunning() {
		fmt.Fprintln(os.Stderr, "keydris claude-desktop: quit Claude first; it reads the proxy pin only at startup")
		return 1
	}
	cfg := config.Load()
	if err := cfg.ValidatePaths(); err != nil {
		fmt.Fprintf(os.Stderr, "keydris claude-desktop: %v\n", err)
		return 1
	}
	if err := sweepDesktopLaunch(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "keydris claude-desktop: clean up previous session: %v\n", err)
		return 1
	}
	present, err := sandbox.HasKeydrisHooks(desktopSettingsPath(cfg))
	if err != nil || !present {
		fmt.Fprintln(os.Stderr, "keydris claude-desktop: run `keydris init claude-desktop <agent-id>` first")
		return 1
	}
	if inspectProxy(cfg).state != "ok" {
		if code := runProxyUp(); code != 0 {
			return code
		}
	}

	sid := "desktop-" + newProxyToken()
	if code := hookSessionStart(cfg, "", sid, agentRuntimeClaudeDesktop); code != 0 {
		return code
	}
	ended := false
	end := func() {
		if !ended {
			_ = hookSessionEnd(cfg, sid)
			ended = true
		}
	}
	defer end()

	st, err := loadState(cfg, sid)
	if err != nil || st.Handle == "" {
		fmt.Fprintf(os.Stderr, "keydris claude-desktop: session has no handle: %v\n", err)
		return 1
	}
	forwarder, err := desktopproxy.Listen(fmt.Sprintf("127.0.0.1:%d", cfg.HTTPProxyPort), st.Handle)
	if err != nil {
		fmt.Fprintf(os.Stderr, "keydris claude-desktop: forwarder: %v\n", err)
		return 1
	}
	defer forwarder.Close()

	settingsPath := desktopSettingsPath(cfg)
	previous, existed, err := sandbox.ApplyDesktopEngineSettings(settingsPath, sandbox.DesktopEngineOptions{
		ProxyURL: proxyAuthURL(cfg.HTTPProxyPort, st.Handle),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "keydris claude-desktop: engine settings: %v\n", err)
		return 1
	}
	library, err := desktopConfigLibraryDir()
	if err != nil {
		_ = sandbox.RestoreDesktopEngineSettings(settingsPath, previous, existed)
		fmt.Fprintf(os.Stderr, "keydris claude-desktop: %v\n", err)
		return 1
	}
	egress, err := sandbox.ApplyEgressProxyPin(library, "http://"+forwarder.Addr())
	if err != nil {
		_ = sandbox.RestoreDesktopEngineSettings(settingsPath, previous, existed)
		fmt.Fprintf(os.Stderr, "keydris claude-desktop: egress pin: %v\n", err)
		return 1
	}
	rec := &desktopLaunchRecord{
		LauncherPID:      os.Getpid(),
		SessionID:        sid,
		SettingsPath:     settingsPath,
		SettingsPrevious: previous,
		SettingsExisted:  existed,
		ConfigLibrary:    library,
		Egress:           egress,
	}
	if err := writeDesktopLaunch(cfg, *rec); err != nil {
		_ = sandbox.RestoreEgressProxyPin(library, egress)
		_ = sandbox.RestoreDesktopEngineSettings(settingsPath, previous, existed)
		fmt.Fprintf(os.Stderr, "keydris claude-desktop: save launch record: %v\n", err)
		return 1
	}

	cmd, err := startClaudeDesktop(desktopSettingsDir(cfg), sid)
	if err != nil {
		_ = finishDesktopLaunch(cfg, rec)
		fmt.Fprintf(os.Stderr, "keydris claude-desktop: %v\n", err)
		return 1
	}
	updateSessionOwner(cfg, sid, cmd.Process.Pid, true)

	code := waitClaudeDesktop(cmd)
	if err := finishDesktopLaunch(cfg, rec); err != nil {
		fmt.Fprintf(os.Stderr, "keydris claude-desktop: restore: %v\n", err)
		if code == 0 {
			code = 1
		}
	}
	return code
}

func startClaudeDesktop(configDir, sessionID string) (*exec.Cmd, error) {
	const bin = "/Applications/Claude.app/Contents/MacOS/Claude"
	if _, err := os.Stat(bin); err != nil {
		return nil, fmt.Errorf("Claude Desktop is not installed at %s", bin)
	}
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(),
		"CLAUDE_CONFIG_DIR="+configDir,
		desktopSessionEnv+"="+sessionID,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

func waitClaudeDesktop(cmd *exec.Cmd) int {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		var exitErr *exec.ExitError
		if err == nil {
			return 0
		}
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "keydris claude-desktop: %v\n", err)
		return 1
	case <-sig:
		_ = cmd.Process.Kill()
		<-done
		return 1
	}
}
