package cli

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/nocaplabs/keydris-cli/internal/config"
	"github.com/nocaplabs/keydris-cli/internal/node/proxy"
	"github.com/nocaplabs/keydris-cli/internal/node/sandbox"
)

// runInit implements `keydris init claude-code`: the one-command onboarding for
// the v2 sandbox-proxy integration (plan_v1.md section 4/6). It generates (and
// persists) the Keydris CA, then merges the sandbox block, the session hooks,
// and the CA env into ~/.claude/settings.json without clobbering existing
// settings. A reference copy lives at examples/claude-code/settings.json.
func runInit(args []string) int {
	target := "claude-code"
	rest := args
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		target = args[0]
		rest = args[1:]
	}
	if target != "claude-code" {
		fmt.Fprintf(os.Stderr, "keydris init: unknown target %q (want claude-code)\n", target)
		return 1
	}

	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	strict := fs.Bool("strict", true, "lock the sandbox as a hard gate (failIfUnavailable + no unsandboxed escape)")
	trustStore := fs.Bool("trust-store", false, "also install the Keydris CA into the OS trust store (may need sudo)")
	if err := fs.Parse(rest); err != nil {
		return 1
	}

	cfg := config.Load()

	// Generate-and-persist the CA so the daemon loads the same root that the
	// sandbox is told to trust below.
	if _, err := proxy.LoadOrCreateCA(cfg.CAPath, cfg.CAKeyPath, "Keydris CA", 825*24*time.Hour); err != nil {
		fmt.Fprintf(os.Stderr, "keydris init: CA: %v\n", err)
		return 1
	}

	if err := sandbox.Configure(cfg.ClaudeSettingsPath, sandbox.Options{
		HTTPProxyPort:  cfg.HTTPProxyPort,
		AllowedDomains: cfg.AllowedDomains,
		CAPath:         cfg.CAPath,
		Strict:         *strict,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "keydris init: configure sandbox: %v\n", err)
		return 1
	}

	fmt.Printf("keydris: configured Claude Code sandbox in %s\n", cfg.ClaudeSettingsPath)
	fmt.Printf("  sandbox.enabled=true, network.httpProxyPort=%d, hooks wired\n", cfg.HTTPProxyPort)
	fmt.Printf("  CA: %s (trusted by sandboxed tools via NODE_EXTRA_CA_CERTS/CURL_CA_BUNDLE/...)\n", cfg.CAPath)
	if *strict {
		fmt.Printf("  strict: failIfUnavailable=true, allowUnsandboxedCommands=false\n")
	}

	if *trustStore {
		if err := sandbox.InstallTrustStore(cfg.CAPath); err != nil {
			fmt.Fprintf(os.Stderr, "keydris init: OS trust-store install failed (env vars still apply): %v\n", err)
		} else {
			fmt.Printf("  CA installed into the OS trust store\n")
		}
	}

	fmt.Printf("\nNext: start the proxy with `KEYDRIS_DATAPLANE=sandbox keydris up`, then run `claude`.\n")
	return 0
}
