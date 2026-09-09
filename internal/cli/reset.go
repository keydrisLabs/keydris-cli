package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/keydrisLabs/keydris-cli/internal/config"
	"github.com/keydrisLabs/keydris-cli/internal/node/login"
	"github.com/keydrisLabs/keydris-cli/internal/node/sandbox"
	"github.com/keydrisLabs/keydris-cli/internal/node/sessionstate"
	"github.com/keydrisLabs/keydris-cli/internal/runtimecontract"
)

type resetTarget struct {
	path         string
	tree         bool
	members      []string
	skillContent []byte
}

func planReset(cfg *config.Config, all bool) ([]resetTarget, error) {
	if err := cfg.ValidatePaths(); err != nil {
		return nil, err
	}
	if err := validateResetRoot(cfg.DataDir); err != nil {
		return nil, err
	}
	// Validate config edits as well as deleted files. Never follow a junction.
	for _, path := range []string{cfg.ClaudeSettingsPath, cfg.CodexHooksPath, cfg.ClaudeMcpConfigPath} {
		if err := noLinkedPath(path); err != nil {
			return nil, err
		}
		if info, err := os.Stat(path); err == nil && !info.Mode().IsRegular() {
			return nil, fmt.Errorf("not a settings file: %s", path)
		} else if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	var targets []resetTarget
	add := func(path string, tree bool) error {
		path = filepath.Clean(path)
		if pathEqual(path, cfg.DataDir) {
			return fmt.Errorf("a runtime file points at the data directory itself")
		}
		if err := noLinkedPath(path); err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			// Stopping a daemon from an older release can create this lock.
			if pathEqual(path, filepath.Join(cfg.DataDir, "proxy.lock")) {
				targets = append(targets, resetTarget{path: path})
			}
			return nil
		}
		if err != nil {
			return err
		}
		if !tree && info.IsDir() {
			return fmt.Errorf("expected a file, got directory: %s", path)
		}
		if tree {
			if err := validateResetTree(path); err != nil {
				return err
			}
		}
		for _, existing := range targets {
			if pathEqual(existing.path, path) || (existing.tree && withinPath(existing.path, path)) {
				return nil
			}
		}
		target := resetTarget{path: path, tree: tree}
		if tree {
			if err := filepath.WalkDir(path, func(current string, _ os.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				target.members = append(target.members, current)
				return nil
			}); err != nil {
				return err
			}
		}
		targets = append(targets, target)
		return nil
	}
	if all {
		entries, err := os.ReadDir(cfg.DataDir)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		for _, entry := range entries {
			// Keep the user's privacy choice across resets.
			if entry.Name() == "telemetry.json" || entry.Name() == ".reset-in-progress" {
				continue
			}
			if err := add(filepath.Join(cfg.DataDir, entry.Name()), entry.IsDir()); err != nil {
				return nil, err
			}
		}
	}
	for _, path := range []string{
		cfg.CAPath, cfg.CAKeyPath, cfg.CABundlePath, cfg.ClientCAPath, cfg.ClientCAKeyPath,
		cfg.SigningKeyPath, cfg.SessionAuthFile, cfg.SessionSocket,
		filepath.Join(cfg.DataDir, "proxy.pid"), filepath.Join(cfg.DataDir, "agent-id"),
		filepath.Join(cfg.DataDir, "proxy.lock"),
		filepath.Join(cfg.DataDir, "policy-id"), filepath.Join(cfg.DataDir, "managed-destinations.json"),
		filepath.Join(cfg.IdentityDir, login.KeyFile), filepath.Join(cfg.IdentityDir, login.CertFile),
		filepath.Join(cfg.IdentityDir, login.CAFile), filepath.Join(cfg.IdentityDir, login.WhoamiFile),
	} {
		if err := add(path, false); err != nil {
			return nil, err
		}
	}
	if err := add(sessionstate.Dir(cfg.DataDir), true); err != nil {
		return nil, err
	}
	if all && !withinPath(cfg.DataDir, cfg.LedgerPath) {
		if err := add(cfg.LedgerPath, false); err != nil {
			return nil, err
		}
	}
	// Ensure custom runtime-file overrides cannot delete shared configuration.
	var skillTargets []resetTarget
	var skillPaths []string
	for _, integration := range []string{"claude-code", "codex"} {
		path, err := agentSkillPath(cfg, integration)
		if err != nil {
			continue // optional guidance outside this runtime is retained
		}
		skillPaths = append(skillPaths, path)
		raw, err := readAgentSkill(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			continue // never delete guidance whose ownership cannot be inspected
		}
		if isManagedSkill(raw) {
			skillTargets = append(skillTargets, resetTarget{path: path, skillContent: raw})
		}
	}
	for _, target := range targets {
		for _, protected := range append([]string{cfg.ClaudeSettingsPath, cfg.CodexHooksPath, cfg.ClaudeMcpConfigPath}, skillPaths...) {
			if pathEqual(target.path, protected) || target.tree && withinPath(target.path, protected) {
				return nil, fmt.Errorf("reset target overlaps integration settings: %s", protected)
			}
		}
		if home, err := os.UserHomeDir(); err == nil {
			protected := filepath.Join(home, ".keydris.toml")
			if pathEqual(target.path, protected) || target.tree && withinPath(target.path, protected) {
				return nil, fmt.Errorf("reset target overlaps user configuration")
			}
		}
	}
	targets = append(targets, skillTargets...)
	sort.Slice(targets, func(i, j int) bool { return targets[i].path < targets[j].path })
	return targets, nil
}

func resetSessions(cfg *config.Config) ([]sessionstate.State, error) {
	entries, err := os.ReadDir(sessionstate.Dir(cfg.DataDir))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var sessions []sessionstate.State
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		state, err := sessionstate.Load(cfg.DataDir, strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			return nil, fmt.Errorf("cannot read session %s: %w", entry.Name(), err)
		}
		if state.OwnerPID > 0 {
			if _, err := processIdentity(state.OwnerPID); err != nil && !errors.Is(err, errProcessNotRunning) {
				return nil, fmt.Errorf("cannot verify session owner PID %d; close running agents and retry", state.OwnerPID)
			}
			if current, err := processIdentity(state.OwnerPID); err == nil && (state.OwnerIdentity == "" || current == state.OwnerIdentity) {
				return nil, fmt.Errorf("close the agent session owned by PID %d before resetting", state.OwnerPID)
			}
		}
		sessions = append(sessions, state)
	}
	return sessions, nil
}
func revokeResetSessions(cfg *config.Config, sessions []sessionstate.State) error {
	// One bounded budget for the whole operation. Leave credentials intact if
	// the control plane cannot confirm revocation; the user can retry reset.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, state := range sessions {
		expiry, err := time.Parse(time.RFC3339, state.ExpiresAt)
		if err == nil && !time.Now().Before(expiry) {
			continue
		}
		client, err := login.HTTPClient(cfg.IdentityDir, cfg.MTLSServerCA, 5*time.Second)
		if err != nil {
			return err
		}
		err = runtimecontract.RevokeKitSession(ctx, client, cfg.ControlMTLSURL, state.ULID)
		client.CloseIdleConnections()
		if err != nil {
			return fmt.Errorf("could not revoke a saved session; credentials retained: %w", err)
		}
	}
	return nil
}

func runReset(args []string) int {
	fs := flag.NewFlagSet("reset", flag.ContinueOnError)
	all := fs.Bool("all", false, "also delete logs, evidence and other data-directory contents (keep telemetry preference)")
	dry := fs.Bool("dry-run", false, "preview exact paths without changing anything")
	yes := fs.Bool("yes", false, "confirm the displayed reset without an interactive prompt")
	keepTrust := fs.Bool("keep-trust", false, "leave OS trust entries for manual cleanup")
	if code := parseFlags(fs, args); code >= 0 {
		return code
	}
	cfg := config.Load()
	ui := newUI(os.Stdout)
	fail := func(err error) int { ui.row("error", "Reset", err.Error()); return 1 }
	targets, err := planReset(cfg, *all)
	if err != nil {
		return fail(err)
	}
	ui.title("Reset preview")
	ui.row("warning", "Agent sessions", "Close running agents before reset; the Keydris proxy will be stopped")
	ui.row("inactive", "Data directory", cfg.DataDir)
	for _, path := range []string{cfg.ClaudeSettingsPath, cfg.CodexHooksPath, cfg.ClaudeMcpConfigPath} {
		ui.row("inactive", "Edit", path+" (remove Keydris entries only)")
	}
	for _, target := range targets {
		detail := target.path
		if target.tree {
			detail += " (directory contents)"
		}
		ui.row("warning", "Delete", detail)
	}
	for _, integration := range []string{"claude-code", "codex"} {
		if path, err := agentSkillPath(cfg, integration); err == nil {
			if raw, err := readAgentSkill(path); !os.IsNotExist(err) && (err != nil || !isManagedSkill(raw)) {
				ui.row("inactive", "Keep", path+" (existing or user-edited skill)")
			}
		}
	}
	if *keepTrust {
		ui.row("warning", "OS trust", "Kept; remove the old CA manually before starting fresh")
	} else {
		ui.row("inactive", "OS trust", "Remove the exact CA represented by "+cfg.CAPath)
	}
	if !*all {
		ui.row("inactive", "Keep", "Logs, evidence, user configuration and telemetry preference")
	} else {
		ui.row("inactive", "Keep", "CLI executable, user configuration and telemetry preference")
	}
	if *dry {
		ui.row("ok", "Preview", "No changes made")
		return 0
	}
	if !*yes {
		if !terminalFile(os.Stdin) {
			return fail(fmt.Errorf("confirmation required; review --dry-run, then pass --yes"))
		}
		fmt.Fprint(os.Stdout, "Type reset to continue: ")
		answer, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil || strings.TrimSpace(answer) != "reset" {
			ui.row("inactive", "Reset", "Cancelled")
			return 1
		}
	}
	if err := os.MkdirAll(cfg.DataDir, 0700); err != nil {
		return fail(err)
	}
	lockPath := filepath.Join(cfg.DataDir, ".reset-in-progress")
	guard, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fail(fmt.Errorf("another reset may be active; inspect %s before retrying: %w", lockPath, err))
	}
	fmt.Fprintf(guard, "PID %d\n", os.Getpid())
	guard.Close()
	defer os.Remove(lockPath)
	// Detect active owners before any mutation, then reload after stopping the
	// daemon so a renewal cannot race revocation.
	if _, err := resetSessions(cfg); err != nil {
		return fail(err)
	}
	if health := inspectProxy(cfg); health.state == "error" && health.pid == 0 {
		return fail(fmt.Errorf("%s; inspect the process/PID file before resetting", health.detail))
	}
	if code := runProxyDown(); code != 0 {
		return code
	}
	if _, err := planReset(cfg, *all); err != nil {
		return fail(err)
	}
	sessions, err := resetSessions(cfg)
	if err != nil {
		return fail(err)
	}
	finish := ui.progress("Revoking saved sessions")
	err = revokeResetSessions(cfg, sessions)
	finish(err)
	if err != nil {
		return 1
	}
	if !*keepTrust {
		if _, err := os.Stat(cfg.CAPath); os.IsNotExist(err) {
			ui.row("inactive", "OS trust", "No local CA file is available to identify an old trust entry")
		}
		finish = ui.progress("Removing CA trust")
		err = sandbox.RemoveTrustStore(cfg.CAPath)
		finish(err)
		if err != nil {
			ui.row("warning", "Credentials kept", "Resolve trust cleanup, then retry; --keep-trust skips OS trust removal")
			return 1
		}
	}
	if _, err := sandbox.Deconfigure(cfg.ClaudeSettingsPath, sandbox.RemoveOptions{HTTPProxyPort: cfg.HTTPProxyPort, CAPath: cfg.CABundlePath, AllowedDomains: cfg.AllowedDomains}); err != nil {
		return fail(err)
	}
	if _, err := sandbox.DeconfigureCodexHooks(cfg.CodexHooksPath); err != nil {
		return fail(err)
	}
	if err := sandbox.RemoveManagedMcpServers(cfg.ClaudeMcpConfigPath); err != nil {
		return fail(err)
	}
	// Remove from the original reviewed plan only. Revalidate the tree before
	// each operation; do not expand the plan to newly appeared files.
	for _, target := range targets {
		if err := validateResetRoot(cfg.DataDir); err != nil {
			return fail(err)
		}
		if err := noLinkedPath(target.path); err != nil {
			return fail(err)
		}
		err = removeResetTarget(target)
		if err != nil && !os.IsNotExist(err) {
			return fail(err)
		}
	}
	// Empty identity directory is cosmetic; never recursively delete a custom
	// identity directory, which can contain unrelated user files.
	if withinPath(cfg.DataDir, cfg.IdentityDir) {
		_ = os.Remove(cfg.IdentityDir)
	}
	ui.row("ok", "Reset", "Local setup removed")
	ui.next("keydris init")
	return 0
}

// Delete only the entries captured in the reviewed plan. os.Remove on a
// directory refuses newly appeared contents instead of sweeping them up.
func removeResetTarget(target resetTarget) error {
	if err := noLinkedPath(target.path); err != nil {
		return err
	}
	if !target.tree {
		if target.skillContent != nil {
			raw, err := readAgentSkill(target.path)
			if err != nil {
				return err
			}
			if !bytes.Equal(raw, target.skillContent) {
				return fmt.Errorf("skill changed since the reset preview; retained %s", target.path)
			}
		}
		return os.Remove(target.path)
	}
	if err := validateResetTree(target.path); err != nil {
		return err
	}
	planned := make(map[string]bool, len(target.members))
	for _, path := range target.members {
		planned[path] = true
	}
	err := filepath.WalkDir(target.path, func(path string, _ os.DirEntry, walkErr error) error {
		if os.IsNotExist(walkErr) {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		if !planned[path] {
			return fmt.Errorf("new file appeared during reset: %s; close agents and retry", path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for i := len(target.members) - 1; i >= 0; i-- {
		path := target.members[i]
		if !withinPath(target.path, path) {
			return fmt.Errorf("reset path escaped its planned directory")
		}
		if err := noLinkedPath(path); err != nil {
			return err
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
