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
	"sort"
	"strings"
	"time"

	"github.com/keydrisLabs/keydris-cli/internal/node/sandbox"
)

// Codex skips a hook it does not trust, without an error. A governed launch
// therefore passes the Keydris hooks as -c session flags together with their
// trust, and checks with the codex binary it launches that every Keydris hook
// will run. `keydris codex` and `keydris codex-desktop` share this.

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

// governedCodexOverrides returns the -c values a governed Codex launch
// carries: the enforcement settings, the Keydris hooks, and their trust.
// Codex silently skips a hook that is not trusted, so codexPath computes the
// trust hashes, and a second listing must report every Keydris hook as
// trusted and enabled.
func governedCodexOverrides(codexPath string, opt sandbox.CodexHookOptions) ([]string, error) {
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

// withCodexConfig adds -c values to a codex command line. Codex keeps only the
// last group of -c options: a -c after the subcommand discards every -c
// before it. The values therefore go right after the caller's last -c before
// any "--", or first when there is none, so the caller's options and these
// both stay in effect, and these win where they overlap.
func withCodexConfig(args, values []string) []string {
	last := -1
scan:
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "--":
			break scan
		case arg == "-c" || arg == "--config":
			if i+1 < len(args) {
				i++
				last = i
			}
		case strings.HasPrefix(arg, "-c") || strings.HasPrefix(arg, "--config="):
			last = i
		}
	}
	flags := make([]string, 0, 2*len(values))
	for _, value := range values {
		flags = append(flags, "-c", value)
	}
	out := make([]string, 0, len(args)+len(flags))
	out = append(out, args[:last+1]...)
	out = append(out, flags...)
	return append(out, args[last+1:]...)
}
