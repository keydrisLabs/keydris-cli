package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/keydrisLabs/keydris-cli/internal/config"
	"github.com/keydrisLabs/keydris-cli/internal/node/sandbox"
)

// The Codex desktop app (ChatGPT.app, bundle com.openai.codex) runs its agent
// as a bundled `codex app-server` child that inherits the app's environment,
// and it spawns the executable CODEX_CLI_PATH names in place of the bundled
// one. `keydris codex-desktop` launches the app inside one Keydris session with
// CODEX_CLI_PATH pointing at a shim that execs the bundled codex with the
// Keydris hooks, their trust, and the settings `keydris codex` passes. Nothing
// is written to ~/.codex, so a Dock launch stays ordinary Codex.

const (
	codexDesktopBundleID = "com.openai.codex"
	// codexDesktopEnv tells the session briefing it runs inside the app.
	codexDesktopEnv = "KEYDRIS_CODEX_DESKTOP"
)

type codexDesktopApp struct {
	Path       string // the .app bundle
	Executable string // Contents/MacOS/<CFBundleExecutable>
	Codex      string // the bundled codex CLI
}

func codexDesktopDir(cfg *config.Config) string {
	return filepath.Join(cfg.DataDir, "codex-desktop")
}

func codexDesktopShimPath(cfg *config.Config) string {
	return filepath.Join(codexDesktopDir(cfg), "bin", "codex")
}

// codexDesktopConfigured reports whether `keydris init codex-desktop` ran.
func codexDesktopConfigured(cfg *config.Config) bool {
	info, err := os.Stat(codexDesktopDir(cfg))
	return err == nil && info.IsDir()
}

// codexDesktopBundleInfo reads CFBundleIdentifier and CFBundleExecutable from
// a bundle's Info.plist.
var codexDesktopBundleInfo = defaultCodexDesktopBundleInfo

func defaultCodexDesktopBundleInfo(app string) (string, string, error) {
	plist := filepath.Join(app, "Contents", "Info.plist")
	read := func(key string) (string, error) {
		out, err := exec.Command("plutil", "-extract", key, "raw", "-o", "-", plist).Output()
		if err != nil {
			return "", fmt.Errorf("read %s from %s: %w", key, plist, err)
		}
		return strings.TrimSpace(string(out)), nil
	}
	id, err := read("CFBundleIdentifier")
	if err != nil {
		return "", "", err
	}
	executable, err := read("CFBundleExecutable")
	if err != nil {
		return "", "", err
	}
	return id, executable, nil
}

// codexDesktopCandidates lists where the app installs. The app has shipped as
// Codex.app and as ChatGPT.app. KEYDRIS_CODEX_DESKTOP_APP points a test or a
// non-standard install at one bundle.
func codexDesktopCandidates() []string {
	if app := os.Getenv("KEYDRIS_CODEX_DESKTOP_APP"); app != "" {
		return []string{app}
	}
	candidates := []string{"/Applications/ChatGPT.app", "/Applications/Codex.app"}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidates = append(candidates,
			filepath.Join(home, "Applications", "ChatGPT.app"),
			filepath.Join(home, "Applications", "Codex.app"))
	}
	return candidates
}

func findCodexDesktopApp() (codexDesktopApp, error) {
	for _, candidate := range codexDesktopCandidates() {
		if info, err := os.Stat(candidate); err != nil || !info.IsDir() {
			continue
		}
		id, executable, err := codexDesktopBundleInfo(candidate)
		if err != nil || id != codexDesktopBundleID || executable == "" || strings.ContainsAny(executable, `/\`) {
			continue
		}
		app := codexDesktopApp{
			Path:       candidate,
			Executable: filepath.Join(candidate, "Contents", "MacOS", executable),
			Codex:      bundledCodex(candidate),
		}
		if !regularFile(app.Executable) || app.Codex == "" {
			continue
		}
		return app, nil
	}
	return codexDesktopApp{}, fmt.Errorf("the Codex desktop app (%s) is not installed in Applications", codexDesktopBundleID)
}

// bundledCodex returns the codex binary the app itself runs as app-server.
// Builds from 26.924 ship it as a CodexCLI.app inside a codex-cli package;
// earlier builds put it directly in Resources.
func bundledCodex(app string) string {
	resources := filepath.Join(app, "Contents", "Resources")
	for _, path := range []string{
		filepath.Join(resources, "codex-cli", "CodexCLI.app", "Contents", "MacOS", "codex"),
		filepath.Join(resources, "codex"),
	} {
		if regularFile(path) {
			return path
		}
	}
	return ""
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// codexDesktopRunning reports whether the app is open. Electron hands a second
// launch to the running instance, whose app-server has no session.
var codexDesktopRunning = defaultCodexDesktopRunning

func defaultCodexDesktopRunning(executable string) bool {
	out, err := exec.Command("ps", "-axo", "comm=").Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == executable {
			return true
		}
	}
	return false
}

// codexListedHook is one hook as `codex app-server` reports it in hooks/list.
type codexListedHook struct {
	Key         string `json:"key"`
	EventName   string `json:"eventName"`
	Source      string `json:"source"`
	CurrentHash string `json:"currentHash"`
	TrustStatus string `json:"trustStatus"`
	Enabled     bool   `json:"enabled"`
}

// codexSessionFlagHooks lists the hooks a codex binary loads with overrides.
// It runs `codex app-server` in a throwaway CODEX_HOME with its proxy pointed
// at a closed port, so it neither reads the user's ~/.codex nor reaches the
// network.
var codexSessionFlagHooks = defaultCodexSessionFlagHooks

func defaultCodexSessionFlagHooks(codexPath string, overrides []string) ([]codexListedHook, error) {
	// Same position as the shim: global options before the subcommand.
	var args []string
	for _, override := range overrides {
		args = append(args, "-c", override)
	}
	return runCodexHookListing(codexPath, append(args, "app-server"))
}

// runCodexHookListing runs an app-server command line and returns its hooks.
func runCodexHookListing(executable string, args []string) ([]codexListedHook, error) {
	home, err := os.MkdirTemp("", "keydris-codex-hooks-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(home)
	return runCodexHookListingIn(home, executable, args)
}

// runCodexHookListingIn lists hooks with home as CODEX_HOME and working
// directory.
func runCodexHookListingIn(home, executable string, args []string) ([]codexListedHook, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir = home
	const closed = "http://127.0.0.1:9"
	cmd.Env = append(os.Environ(), "CODEX_HOME="+home,
		"HTTPS_PROXY="+closed, "HTTP_PROXY="+closed, "ALL_PROXY="+closed,
		"https_proxy="+closed, "http_proxy="+closed, "all_proxy="+closed,
		"NO_PROXY=", "no_proxy=")
	cmd.WaitDelay = time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	hooks, err := listCodexHooks(stdout, stdin, home)
	if ctx.Err() != nil {
		return nil, fmt.Errorf("codex app-server did not list its hooks: %w", ctx.Err())
	}
	return hooks, err
}

// listCodexHooks speaks just enough of the app-server protocol (JSON-RPC over
// JSON lines) to call hooks/list.
func listCodexHooks(r io.Reader, w io.Writer, cwd string) ([]codexListedHook, error) {
	lines := bufio.NewScanner(r)
	lines.Buffer(make([]byte, 64<<10), 8<<20)
	send := func(message any) error {
		raw, err := json.Marshal(message)
		if err != nil {
			return err
		}
		_, err = w.Write(append(raw, '\n'))
		return err
	}
	if err := send(map[string]any{"id": 1, "method": "initialize", "params": map[string]any{
		"clientInfo":   map[string]string{"name": "keydris", "version": Version},
		"capabilities": map[string]bool{"experimentalApi": true},
	}}); err != nil {
		return nil, err
	}
	if _, err := codexResponse(lines, 1); err != nil {
		return nil, fmt.Errorf("initialize: %w", err)
	}
	if err := send(map[string]any{"method": "initialized"}); err != nil {
		return nil, err
	}
	if err := send(map[string]any{"id": 2, "method": "hooks/list", "params": map[string]any{"cwds": []string{cwd}}}); err != nil {
		return nil, err
	}
	result, err := codexResponse(lines, 2)
	if err != nil {
		return nil, fmt.Errorf("hooks/list: %w", err)
	}
	var listed struct {
		Data []struct {
			Hooks  []codexListedHook `json:"hooks"`
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		} `json:"data"`
	}
	if err := json.Unmarshal(result, &listed); err != nil {
		return nil, fmt.Errorf("hooks/list: %w", err)
	}
	var hooks []codexListedHook
	for _, entry := range listed.Data {
		if len(entry.Errors) > 0 {
			return nil, fmt.Errorf("hooks/list: %s", entry.Errors[0].Message)
		}
		hooks = append(hooks, entry.Hooks...)
	}
	return hooks, nil
}

// codexResponse reads messages until the response to id. Notifications and
// requests from the server are skipped.
func codexResponse(lines *bufio.Scanner, id int) (json.RawMessage, error) {
	for lines.Scan() {
		var message struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(lines.Bytes(), &message) != nil || message.Method != "" || message.ID == nil || *message.ID != id {
			continue
		}
		if message.Error != nil {
			return nil, errors.New(message.Error.Message)
		}
		return message.Result, nil
	}
	if err := lines.Err(); err != nil {
		return nil, err
	}
	return nil, io.ErrUnexpectedEOF
}

func sessionFlagHooks(hooks []codexListedHook) []codexListedHook {
	var ours []codexListedHook
	for _, hook := range hooks {
		if hook.Source == "sessionFlags" {
			ours = append(ours, hook)
		}
	}
	return ours
}

// codexHookTrustOverride marks hooks trusted by their current hashes and
// enabled. Codex merges this table over the hooks.state in config.toml, where
// /hooks records a hook the user turned off, so both fields are set. Codex
// keys hook state by source path and position; the keys contain '.', which a
// dotted -c path cannot address, so the value is one inline table.
func codexHookTrustOverride(hooks []codexListedHook) string {
	entries := make([]string, 0, len(hooks))
	for _, hook := range hooks {
		entries = append(entries, fmt.Sprintf("%q={trusted_hash=%q,enabled=true}", hook.Key, hook.CurrentHash))
	}
	sort.Strings(entries)
	return "hooks.state={" + strings.Join(entries, ",") + "}"
}

// codexDesktopOverrides returns the -c values the shim adds: the settings
// `keydris codex` passes, the Keydris hooks, and their trust. Codex silently
// skips a hook that is not trusted, so the bundled codex computes the trust
// hashes, and a second listing must report every Keydris hook as trusted and
// enabled.
func codexDesktopOverrides(codexPath string, opt sandbox.CodexHookOptions) ([]string, error) {
	hookOverrides := sandbox.CodexHookOverrides(opt)
	overrides := append(codexEnforcementOverrides(), hookOverrides...)
	listed, err := codexSessionFlagHooks(codexPath, overrides)
	if err != nil {
		return nil, fmt.Errorf("load the Keydris hooks into Codex: %w", err)
	}
	ours := sessionFlagHooks(listed)
	if len(ours) != len(hookOverrides) {
		return nil, fmt.Errorf("Codex loaded %d of %d Keydris hooks", len(ours), len(hookOverrides))
	}
	overrides = append(overrides, codexHookTrustOverride(ours))
	listed, err = codexSessionFlagHooks(codexPath, overrides)
	if err != nil {
		return nil, fmt.Errorf("trust the Keydris hooks in Codex: %w", err)
	}
	if err := keydrisHooksReady(listed, len(hookOverrides)); err != nil {
		return nil, err
	}
	return overrides, nil
}

// keydrisHooksReady reports why Codex would not run every Keydris hook.
func keydrisHooksReady(listed []codexListedHook, want int) error {
	ours := sessionFlagHooks(listed)
	if len(ours) != want {
		return fmt.Errorf("Codex loaded %d of %d Keydris hooks", len(ours), want)
	}
	for _, hook := range ours {
		if hook.TrustStatus != "trusted" {
			return fmt.Errorf("Codex reports the Keydris %s hook as %s", hook.EventName, hook.TrustStatus)
		}
		if !hook.Enabled {
			return fmt.Errorf("Codex reports the Keydris %s hook as disabled", hook.EventName)
		}
	}
	return nil
}

// prepareCodexDesktopShim writes the shim for the app's codex and confirms
// that codex runs every Keydris hook through it.
func prepareCodexDesktopShim(shim, codexPath string, opt sandbox.CodexHookOptions) error {
	overrides, err := codexDesktopOverrides(codexPath, opt)
	if err != nil {
		return err
	}
	if err := writeCodexDesktopShim(shim, codexPath, overrides); err != nil {
		return fmt.Errorf("write the Codex shim: %w", err)
	}
	return verifyCodexDesktopShim(shim, len(sandbox.CodexHookOverrides(opt)))
}

// codexDesktopShimScript places the Keydris -c values. Codex keeps only the
// last group of -c options: one after the subcommand discards every one
// before it, and the app passes plugin flags there. The values therefore go
// right after the caller's last -c before any "--", or first when there is
// none, so the caller's options and the Keydris ones both stay in effect.
const codexDesktopShimScript = `#!/bin/sh
# Written by keydris codex-desktop for one governed launch.
codex=%s
last=0 i=0 value=0
for arg in "$@"; do
	i=$((i + 1))
	if [ "$value" = 1 ]; then
		last=$i value=0
		continue
	fi
	case $arg in
	--) break ;;
	-c | --config) value=1 ;;
	-c?* | --config=*) last=$i ;;
	esac
done
if [ "$last" = 0 ]; then
	exec "$codex"%s "$@"
fi
i=0 n=$#
while [ "$i" -lt "$n" ]; do
	arg=$1
	shift
	set -- "$@" "$arg"
	i=$((i + 1))
	if [ "$i" = "$last" ]; then
		set -- "$@"%s
	fi
done
exec "$codex" "$@"
`

// codexDesktopInvocations are the ways the app runs CODEX_CLI_PATH as
// app-server: with and without the plugin flags it adds after the subcommand.
var codexDesktopInvocations = [][]string{
	{"-c", "features.code_mode_host=true", "app-server", "--analytics-default-enabled"},
	{"-c", "features.code_mode_host=true", "app-server", "--analytics-default-enabled",
		"-c", "plugins.codex-app-tools@openai-bundled.mcp_servers.codex_app.enabled=true"},
}

// codexShimHooks lists the hooks an app-server command line loads.
var codexShimHooks = runCodexHookListing

// verifyCodexDesktopShim runs the shim the way the app does, so the installed
// codex confirms the placement as well as the values.
func verifyCodexDesktopShim(shim string, want int) error {
	for _, args := range codexDesktopInvocations {
		listed, err := codexShimHooks(shim, args)
		if err != nil {
			return fmt.Errorf("run the Codex shim: %w", err)
		}
		if err := keydrisHooksReady(listed, want); err != nil {
			return fmt.Errorf("%w when the app runs codex %s", err, strings.Join(args, " "))
		}
	}
	return nil
}

// writeCodexDesktopShim writes the executable CODEX_CLI_PATH names. The app
// runs it as app-server, and bundled tools such as node_repl run it for other
// subcommands.
func writeCodexDesktopShim(path, codexPath string, overrides []string) error {
	var flags strings.Builder
	for _, override := range overrides {
		flags.WriteString(" -c " + shellQuote(override))
	}
	script := fmt.Sprintf(codexDesktopShimScript, shellQuote(codexPath), flags.String(), flags.String())
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".codex-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(script); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o700); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func configureCodexDesktop(cfg *config.Config) error {
	if _, err := findCodexDesktopApp(); err != nil {
		return err
	}
	return os.MkdirAll(codexDesktopDir(cfg), 0o700)
}

// verifyCodexDesktop proves the installed app's codex loads and trusts the
// Keydris hooks.
func verifyCodexDesktop(opt sandbox.CodexHookOptions) error {
	app, err := findCodexDesktopApp()
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "keydris-codex-shim-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	return prepareCodexDesktopShim(filepath.Join(dir, "codex"), app.Codex, opt)
}

// deinitCodexDesktop removes the Keydris-owned Codex Desktop directory. This
// integration never writes to ~/.codex, so there is nothing to restore there.
func deinitCodexDesktop(cfg *config.Config) (string, bool, error) {
	dir := codexDesktopDir(cfg)
	removed, err := removeKeydrisDir(dir, "codex-desktop")
	return dir, removed, err
}

func runCodexDesktop(args []string) int {
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "usage: keydris codex-desktop")
		return 2
	}
	if runtime.GOOS != "darwin" {
		fmt.Fprintln(os.Stderr, "keydris codex-desktop: launch is supported on macOS")
		return 1
	}
	app, err := findCodexDesktopApp()
	if err != nil {
		fmt.Fprintf(os.Stderr, "keydris codex-desktop: %v\n", err)
		return 1
	}
	if codexDesktopRunning(app.Executable) {
		fmt.Fprintln(os.Stderr, "keydris codex-desktop: quit the Codex app first; it reads CODEX_CLI_PATH only at startup")
		return 1
	}
	cfg := config.Load()
	if err := cfg.ValidatePaths(); err != nil {
		fmt.Fprintf(os.Stderr, "keydris codex-desktop: %v\n", err)
		return 1
	}
	if !codexDesktopConfigured(cfg) {
		fmt.Fprintln(os.Stderr, "keydris codex-desktop: run `keydris init codex-desktop <agent-id>` first")
		return 1
	}
	hookOptions, err := codexHookOptions()
	if err != nil {
		fmt.Fprintf(os.Stderr, "keydris codex-desktop: %v\n", err)
		return 1
	}
	if err := verifyCodexHookExecution(hookOptions); err != nil {
		fmt.Fprintf(os.Stderr, "keydris codex-desktop: %v\n", err)
		return 1
	}
	// The shim carries no session state, so it is checked before a session
	// is minted.
	shim := codexDesktopShimPath(cfg)
	defer os.Remove(shim)
	if err := prepareCodexDesktopShim(shim, app.Codex, hookOptions); err != nil {
		fmt.Fprintf(os.Stderr, "keydris codex-desktop: %v\n", err)
		return 1
	}
	if inspectProxy(cfg).state != "ok" {
		if code := runProxyUp(); code != 0 {
			return code
		}
	}
	if err := sandbox.BuildCABundle(cfg.CAPath, cfg.CABundlePath); err != nil {
		fmt.Fprintf(os.Stderr, "keydris codex-desktop: CA bundle: %v\n", err)
		return 1
	}

	sid := "codex-desktop-" + newProxyToken()
	if code := hookSessionStart(cfg, "", sid, agentRuntimeCodexDesktop); code != 0 {
		return code
	}
	defer func() { _ = hookSessionEnd(cfg, sid) }()
	st, err := loadState(cfg, sid)
	if err != nil || st.Handle == "" {
		fmt.Fprintf(os.Stderr, "keydris codex-desktop: session has no handle: %v\n", err)
		return 1
	}
	cmd := exec.Command(app.Executable)
	env := append(os.Environ(),
		"CODEX_CLI_PATH="+shim,
		"KEYDRIS_SESSION="+sid,
		sessionOwnerEnv+"="+sessionOwnerRun,
		codexDesktopEnv+"=1",
	)
	cmd.Env = appendProxyEnvironment(env, proxyAuthURL(cfg.HTTPProxyPort, st.Handle), cfg.CABundlePath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "keydris codex-desktop: %v\n", err)
		return 1
	}
	updateSessionOwner(cfg, sid, cmd.Process.Pid, true)
	return waitDesktopApp("codex-desktop", cmd)
}
