// Package cli implements the keydris user/daemon commands. Phase 1 keeps a tiny
// stdlib dispatch (up / status / iptables-up / iptables-down); richer onboarding
// commands arrive in later phases.
package cli

import (
	"fmt"
	"os"

	"github.com/nocaplabs/keydris-cli/internal/config"
	"github.com/nocaplabs/keydris-cli/internal/node/daemon"
	"github.com/nocaplabs/keydris-cli/internal/node/netfilter"
)

// Execute dispatches the command line and returns a process exit code.
func Execute() int {
	args := os.Args[1:]
	if len(args) == 0 {
		usage()
		return 1
	}

	switch args[0] {
	case "up":
		return runUp()
	case "status":
		return runStatus()
	case "login":
		return runLogin(args[1:])
	case "whoami":
		return runWhoami(args[1:])
	case "logout":
		return runLogout(args[1:])
	case "enroll":
		return runEnroll(args[1:])
	case "run":
		return runRun(args[1:])
	case "hook":
		return runHook(args[1:])
	case "init":
		return runInit(args[1:])
	case "logs":
		return runLogs()
	case "iptables-up":
		return runIptables(true)
	case "iptables-down":
		return runIptables(false)
	case "help", "-h", "--help":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "keydris: unknown command %q\n\n", args[0])
		usage()
		return 1
	}
}

func runUp() int {
	cfg := config.Load()
	if err := daemon.Run(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "keydris up: %v\n", err)
		return 1
	}
	return 0
}

func runIptables(up bool) int {
	cfg := config.Load()
	var err error
	if up {
		err = netfilter.Up(cfg.ProxyPort, cfg.BackendPort, cfg.ProxyUID)
	} else {
		err = netfilter.Down(cfg.ProxyPort, cfg.BackendPort, cfg.ProxyUID)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "keydris iptables: %v\n", err)
		return 1
	}
	return 0
}

func usage() {
	fmt.Fprint(os.Stderr, `keydris - per-session agent identity (POC)

Usage:
  keydris login                 Browser sign-in; stores a local client certificate
                                  [--email you@example.com] [--no-browser]
  keydris whoami                Show the locally stored identity
  keydris logout                Remove the locally stored identity
  keydris enroll                Exchange a token for a node credential (legacy, root)
  keydris up                    Run the proxy daemon (sandbox proxy, or transparent on Linux/root)
  keydris run -- <cmd...>       Run a command inside a keydris session
  keydris status                Show config + sandbox enforcement state
  keydris logs                  Print and verify the hash-chained evidence ledger
  keydris hook session-start    Bind a session: mint SVID + register (Claude hook)
  keydris hook session-end      End a session: revoke SVID + unregister
  keydris init claude-code      Configure the Claude Code sandbox + CA + hooks (v2 onboarding)
                                  [--strict] [--trust-store]
  keydris iptables-up           Install the redirect rules only (transparent mode, root)
  keydris iptables-down         Remove the redirect rules (root)
  keydris help                  Show this help
`)
}
