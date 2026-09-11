package cli

import (
	"flag"
	"fmt"
	"os"

	"github.com/keydrisLabs/keydris-cli/internal/config"
	"github.com/keydrisLabs/keydris-cli/internal/node/sandbox"
)

// runDeinit implements `keydris deinit claude-code|codex`, the inverse of
// `keydris init`. It strips the Keydris configuration for the chosen target —
// the Claude Code sandbox routing, CA env, and hooks, or the Codex command
// hooks — preserving unrelated settings. Shared agent and policy state is
// cleared only when neither integration retains Keydris hooks.
// The Keydris CA files are left in place so a later `init` reuses them; if you
// installed the CA into the OS trust store with `--trust-store`, remove it
// there manually or use keydris reset.
func runDeinit(args []string) int {
	const usage = "usage: keydris deinit claude-code|codex"

	if len(args) == 0 || args[0] == "" || args[0][0] == '-' {
		fmt.Fprintln(os.Stderr, usage)
		return 1
	}
	target := args[0]
	if target == "openai" {
		target = "codex"
	}
	if target != "claude-code" && target != "codex" {
		fmt.Fprintf(os.Stderr, "keydris deinit: unknown target %q (want claude-code or codex)\n", target)
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
	otherPath := cfg.CodexHooksPath
	if target == "codex" {
		otherPath = cfg.ClaudeSettingsPath
	}
	shared, inspectErr := sandbox.HasKeydrisHooks(otherPath)
	if inspectErr != nil {
		newUI(os.Stderr).row("error", "Integration", "Cannot inspect the other integration; shared setup retained")
		return 1
	}

	var changed bool
	var err error
	var configPath string
	if target == "claude-code" {
		configPath = cfg.ClaudeSettingsPath
		changed, err = sandbox.Deconfigure(configPath, sandbox.RemoveOptions{
			HTTPProxyPort:  cfg.HTTPProxyPort,
			CAPath:         cfg.CABundlePath,
			AllowedDomains: cfg.AllowedDomains,
		})
	} else {
		configPath = cfg.CodexHooksPath
		changed, err = sandbox.DeconfigureCodexHooks(configPath)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "keydris deinit: %v\n", err)
		return 1
	}
	// Only the entries Keydris wrote; hand-added servers are left alone.
	if target == "claude-code" {
		if err := sandbox.RemoveManagedMcpServers(
			cfg.ClaudeMcpConfigPath,
		); err != nil {
			fmt.Fprintf(os.Stderr, "keydris deinit: clear MCP servers: %v\n", err)
			return 1
		}
	} else {
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
