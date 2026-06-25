package cli

import (
	"flag"
	"fmt"
	"os"

	"github.com/keydrisLabs/keydris-cli/internal/config"
	"github.com/keydrisLabs/keydris-cli/internal/node/sandbox"
)

// runDeinit implements `keydris deinit claude-code`, the inverse of
// `keydris init claude-code`. It strips the Keydris sandbox routing, CA env, and
// any stale Keydris hooks from the Claude Code settings file (preserving
// unrelated settings) and clears the persisted policy id. The Keydris CA files
// are left in place so a later `init` reuses them; if you installed the CA into
// the OS trust store with `--trust-store`, remove it there manually.
func runDeinit(args []string) int {
	const usage = "usage: keydris deinit claude-code"

	if len(args) == 0 || args[0] == "" || args[0][0] == '-' {
		fmt.Fprintln(os.Stderr, usage)
		return 1
	}
	target := args[0]
	if target != "claude-code" {
		fmt.Fprintf(os.Stderr, "keydris deinit: unknown target %q (want claude-code)\n", target)
		return 1
	}

	fs := flag.NewFlagSet("deinit", flag.ContinueOnError)
	if err := fs.Parse(args[1:]); err != nil {
		return 1
	}

	cfg := config.Load()

	changed, err := sandbox.Deconfigure(cfg.ClaudeSettingsPath, sandbox.RemoveOptions{
		HTTPProxyPort:  cfg.HTTPProxyPort,
		CAPath:         cfg.CAPath,
		AllowedDomains: cfg.AllowedDomains,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "keydris deinit: %v\n", err)
		return 1
	}
	if err := config.RemovePolicyID(cfg.DataDir); err != nil {
		fmt.Fprintf(os.Stderr, "keydris deinit: clear policy id: %v\n", err)
		return 1
	}

	if changed {
		fmt.Printf("keydris: removed Keydris sandbox config from %s\n", cfg.ClaudeSettingsPath)
	} else {
		fmt.Printf("keydris: no Keydris sandbox config in %s (nothing to remove)\n", cfg.ClaudeSettingsPath)
	}
	fmt.Printf("  cleared policy id; left the Keydris CA at %s in place\n", cfg.CAPath)
	return 0
}
