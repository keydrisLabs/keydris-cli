// Package cli implements the keydris user/agent commands: sign-in, Claude Code
// and OpenAI Codex onboarding, the background egress proxy, session-wrapped
// run, status, and the evidence ledger.
package cli

import (
	"fmt"
	"os"
)

// Version is the build version, stamped at link time via
// -ldflags "-X github.com/keydrisLabs/keydris-cli/internal/cli.Version=<v>".
var Version = "dev"

// Execute dispatches the command line and returns a process exit code.
func Execute() int {
	args := os.Args[1:]
	if len(args) == 0 {
		usage()
		return 1
	}

	switch args[0] {
	case "proxy":
		return runProxy(args[1:])
	case "status":
		return runStatus()
	case "login":
		return runLogin(args[1:])
	case "whoami":
		return runWhoami(args[1:])
	case "logout":
		return runLogout(args[1:])
	case "run":
		return runRun(args[1:])
	case "codex", "openai":
		return runCodex(args[1:])
	case "init":
		return runInit(args[1:])
	case "deinit":
		return runDeinit(args[1:])
	// Internal Claude Code hook entrypoints wired by `init` (not user-facing).
	case "__session-start":
		return runInternalSessionHook("start", args[1:])
	case "__session-end":
		return runInternalSessionHook("end", args[1:])
	case "__pretool-use":
		return runPreToolUse(args[1:])
	case "__permission-request":
		return runPermissionRequest(args[1:])
	case "logs":
		return runLogs()
	case "upgrade":
		return runUpgrade(args[1:])
	case "version", "--version":
		fmt.Printf("keydris %s\n", Version)
		return 0
	case "help", "-h", "--help":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "keydris: unknown command %q\n\n", args[0])
		usage()
		return 1
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `keydris - per-session agent identity (POC)

Usage:
  keydris login                      Browser sign-in; stores a local client certificate
                                       [--email you@example.com] [--no-browser]
  keydris whoami                     Show the locally stored identity
  keydris logout                     Remove the locally stored identity
  keydris init                       Interactive agent setup
  keydris init claude-code <agent>   Configure Claude Code sandbox + CA
                                       [--strict] [--trust-store]
  keydris init codex <agent>         Configure OpenAI Codex + CA
                                       [--trust-store]
  keydris deinit claude-code|codex   Undo init: remove the Keydris config
  keydris proxy up                   Start the brokered egress proxy in the background
  keydris proxy down                 Stop the background proxy
  keydris proxy scope list           Show the origins detected from the agent's policy
  keydris run -- <cmd...>            Run a command inside a keydris session
  keydris codex [args...]            Run OpenAI Codex inside a keydris session
  keydris status                     Show config + sandbox enforcement state
  keydris logs                       Print and verify the hash-chained evidence ledger
  keydris upgrade                    Download & replace the binary with the latest release
                                       [--channel stable|dev] [--version <v>] [--no-config]
  keydris version                    Print the version
  keydris help                       Show this help
`)
}
