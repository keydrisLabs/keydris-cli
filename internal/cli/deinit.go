package cli

import (
	"flag"
	"fmt"
	"os"

	"github.com/keydrisLabs/keydris-cli/internal/config"
	"github.com/keydrisLabs/keydris-cli/internal/node/sandbox"
)

// runDeinit implements `keydris deinit claude-code|codex|claude-desktop`, the
// inverse of `keydris init`. It strips the Keydris configuration for the chosen
// target — the Claude Code sandbox routing, CA env, and hooks, the Codex
// command hooks, or the Claude Desktop settings directory — preserving
// unrelated settings. Shared agent and policy state is cleared only when no
// other integration retains Keydris hooks.
// The Keydris CA files are left in place so a later `init` reuses them; if you
// installed the CA into the OS trust store with `--trust-store`, remove it
// there manually or use keydris reset.
func runDeinit(args []string) int {
	const usage = "usage: keydris deinit claude-code|codex|claude-desktop"

	if len(args) == 0 || args[0] == "" || args[0][0] == '-' {
		fmt.Fprintln(os.Stderr, usage)
		return 1
	}
	target := args[0]
	if target == "openai" {
		target = "codex"
	}
	if target != "claude-code" && target != "codex" && target != "claude-desktop" {
		fmt.Fprintf(os.Stderr, "keydris deinit: unknown target %q (want claude-code, codex, or claude-desktop)\n", target)
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
		configPath = cfg.CodexHooksPath
		changed, err = sandbox.DeconfigureCodexHooks(configPath)
	default:
		configPath, changed, err = deinitClaudeDesktop(cfg)
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
	case "codex":
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

func otherIntegrationsRemain(cfg *config.Config, target string) (bool, error) {
	checks := []struct{ name, path string }{
		{"claude-code", cfg.ClaudeSettingsPath},
		{"codex", cfg.CodexHooksPath},
		{"claude-desktop", desktopSettingsPath(cfg)},
	}
	for _, check := range checks {
		if check.name == target {
			continue
		}
		present, err := sandbox.HasKeydrisHooks(check.path)
		if err != nil {
			return false, err
		}
		if present {
			return true, nil
		}
	}
	return false, nil
}
