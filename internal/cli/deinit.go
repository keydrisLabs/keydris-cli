package cli

import (
	"flag"
	"fmt"
	"os"

	"github.com/keydrisLabs/keydris-cli/internal/config"
	"github.com/keydrisLabs/keydris-cli/internal/node/sandbox"
)

// runDeinit implements `keydris deinit claude-code|codex|claude-desktop|codex-desktop`,
// the inverse of `keydris init`. It strips the Keydris configuration for the
// chosen target — the Claude Code sandbox routing, CA env, and hooks, the Codex
// command hooks, or the Claude Desktop or Codex Desktop directory — preserving
// unrelated settings. Shared agent and policy state is cleared only when no
// other integration remains configured.
// The Keydris CA files are left in place so a later `init` reuses them; if you
// installed the CA into the OS trust store with `--trust-store`, remove it
// there manually or use keydris reset.
func runDeinit(args []string) int {
	const usage = "usage: keydris deinit claude-code|codex|claude-desktop|codex-desktop"

	if len(args) == 0 || args[0] == "" || args[0][0] == '-' {
		fmt.Fprintln(os.Stderr, usage)
		return 1
	}
	target := args[0]
	if target == "openai" {
		target = "codex"
	}
	if !knownIntegration(target) {
		fmt.Fprintf(os.Stderr, "keydris deinit: unknown target %q (want claude-code, codex, claude-desktop, or codex-desktop)\n", target)
		return 1
	}

	fs := flag.NewFlagSet("deinit", flag.ContinueOnError)
	if code := parseFlags(fs, args[1:]); code >= 0 {
		return code
	}

	cfg := config.Load()
	if err := cfg.ValidatePaths(); err != nil {
		newUI(os.Stderr).row("error", "Paths", err.Error())
		return 1
	}
	shared, inspectErr := otherIntegrationsRemain(cfg, target)
	if inspectErr != nil {
		newUI(os.Stderr).row("error", "Integration", "Cannot inspect the other integration; shared setup retained")
		return 1
	}

	var changed bool
	var err error
	var configPath string
	switch target {
	case "claude-code":
		configPath = cfg.ClaudeSettingsPath
		changed, err = sandbox.Deconfigure(configPath, sandbox.RemoveOptions{
			HTTPProxyPort:  cfg.HTTPProxyPort,
			CAPath:         cfg.CABundlePath,
			AllowedDomains: cfg.AllowedDomains,
		})
	case "codex":
		configPath, changed, err = deinitCodex(cfg)
	case "claude-desktop":
		configPath, changed, err = deinitClaudeDesktop(cfg)
	default:
		configPath, changed, err = deinitCodexDesktop(cfg)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "keydris deinit: %v\n", err)
		return 1
	}
	// Only the entries Keydris wrote; hand-added servers are left alone.
	switch target {
	case "claude-code":
		if err := sandbox.RemoveManagedMcpServers(
			cfg.ClaudeMcpConfigPath,
		); err != nil {
			fmt.Fprintf(os.Stderr, "keydris deinit: clear MCP servers: %v\n", err)
			return 1
		}
	case "codex", "codex-desktop":
		// The Codex CLI and the desktop app read the same config.toml.
		other := "codex-desktop"
		if target == "codex-desktop" {
			other = "codex"
		}
		if inUse, _ := integrationConfigured(cfg, other); inUse {
			break
		}
		if _, err := sandbox.RemoveManagedCodexMcpServers(
			cfg.CodexConfigPath,
		); err != nil {
			fmt.Fprintf(os.Stderr, "keydris deinit: clear MCP servers: %v\n", err)
			return 1
		}
	}
	if shared {
		cleanupAgentSkill(cfg, target)
		newUI(os.Stdout).row("ok", "Integration", "Removed "+target+" configuration")
		newUI(os.Stdout).row("inactive", "Shared setup", "Agent, identity, certificates and policy scope retained for the other integration")
		return 0
	}
	if err := config.RemovePolicyID(cfg.DataDir); err != nil {
		fmt.Fprintf(os.Stderr, "keydris deinit: clear policy id: %v\n", err)
		return 1
	}
	if err := config.RemoveAgentID(cfg.DataDir); err != nil {
		fmt.Fprintf(os.Stderr, "keydris deinit: clear agent id: %v\n", err)
		return 1
	}
	if err := config.RemoveManagedScope(cfg.DataDir); err != nil {
		fmt.Fprintf(os.Stderr, "keydris deinit: clear proxy scope: %v\n", err)
		return 1
	}

	if changed {
		newUI(os.Stdout).row("ok", "Integration", "Removed Keydris configuration from "+configPath)
	} else {
		fmt.Printf("keydris: no Keydris config in %s (nothing to remove)\n", configPath)
	}
	cleanupAgentSkill(cfg, target)
	fmt.Printf("  cleared agent id, legacy policy id and the detected proxy scope; left the Keydris CA at %s in place\n", cfg.CAPath)
	return 0
}

// integrationNames are the `keydris init` targets.
var integrationNames = []string{"claude-code", "codex", "claude-desktop", "codex-desktop"}

func knownIntegration(name string) bool {
	for _, known := range integrationNames {
		if name == known {
			return true
		}
	}
	return false
}

// integrationConfigured reports whether `keydris init <name>` left its
// configuration in place.
func integrationConfigured(cfg *config.Config, name string) (bool, error) {
	switch name {
	case "claude-code":
		return sandbox.HasKeydrisHooks(cfg.ClaudeSettingsPath)
	case "codex":
		if codexConfigured(cfg) {
			return true, nil
		}
		// Hooks an earlier release wrote to hooks.json, before `keydris
		// init codex` migrates them.
		return legacyCodexHooks(cfg)
	case "claude-desktop":
		return sandbox.HasKeydrisHooks(desktopSettingsPath(cfg))
	case "codex-desktop":
		return codexDesktopConfigured(cfg), nil
	}
	return false, fmt.Errorf("unknown integration %q", name)
}

func otherIntegrationsRemain(cfg *config.Config, target string) (bool, error) {
	for _, name := range integrationNames {
		if name == target {
			continue
		}
		present, err := integrationConfigured(cfg, name)
		if err != nil {
			return false, err
		}
		if present {
			return true, nil
		}
	}
	return false, nil
}
