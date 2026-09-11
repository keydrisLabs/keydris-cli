// Package cli implements the keydris user/agent commands: sign-in, Claude Code
// and OpenAI Codex onboarding, the background egress proxy, session-wrapped
// run, status, and the evidence ledger.
package cli

import (
	"fmt"
	"github.com/keydrisLabs/keydris-cli/internal/config"
	"github.com/keydrisLabs/keydris-cli/internal/node/login"
	"os"
	"strings"
)

// Version is the build version, stamped at link time via
// -ldflags "-X github.com/keydrisLabs/keydris-cli/internal/cli.Version=<v>".
var Version = "dev"

// The login package reports the CLI version to the control plane on
// enrollment and renewal; -X stamps Version before init runs, so this is the
// final value.
func init() {
	login.ClientVersion = Version
}

// Execute dispatches the command line and returns a process exit code.
func Execute() int {
	args := os.Args[1:]
	colorMode = "auto"
	if len(args) > 0 && (args[0] == "--color" || strings.HasPrefix(args[0], "--color=")) {
		if args[0] == "--color" {
			if len(args) < 2 {
				fmt.Fprintln(os.Stderr, "--color requires auto, always or never")
				return 2
			}
			colorMode = args[1]
			args = args[2:]
		} else {
			colorMode = strings.TrimPrefix(args[0], "--color=")
			args = args[1:]
		}
		if !validColorMode() {
			fmt.Fprintln(os.Stderr, "--color must be auto, always or never")
			return 2
		}
	}
	if len(args) == 0 {
		if config.Load().AgentID != "" {
			return runStatus()
		}
		ui := newUI(os.Stdout)
		printInitBanner(os.Stdout)
		ui.row("inactive", "Setup", "Connect an agent from your Keydris dashboard")
		ui.next("keydris init")
		fmt.Fprintln(os.Stdout, "Run keydris help to see all commands.")
		return 0
	}

	// Anonymous install/upgrade telemetry. Reports before dispatch so
	// long-running commands (`run`, `codex`) are still counted; after the
	// first run and outside upgrades this is a no-op.
	readOnly := args[0] == "skill" || args[0] == "__agent-context" || args[0] == "status" || args[0] == "doctor" || args[0] == "reset" || (args[0] == "proxy" && len(args) > 1 && (args[1] == "status" || args[1] == "logs"))
	if !readOnly {
		recordTelemetry(args[0])
	}

	switch args[0] {
	case "proxy":
		return runProxy(args[1:])
	case "status":
		return runStatus(args[1:]...)
	case "doctor":
		return runStatus(append([]string{"--verbose"}, args[1:]...)...)
	case "reset":
		return runReset(args[1:])
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
	case "skill":
		return runSkill(args[1:])
	case "__agent-context":
		return runAgentContext(args[1:])
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
	case "telemetry":
		return runTelemetry(args[1:])
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
	fmt.Fprint(os.Stdout, `keydris - Authority before action

Usage:
  keydris login                      Browser sign-in; stores a local client certificate
                                       [--email you@example.com] [--no-browser]
  keydris whoami                     Show the locally stored identity
  keydris logout                     Remove the locally stored identity
  keydris init                       Interactive agent setup
  keydris init claude-code <agent>   Configure Claude Code sandbox + CA
                                       [--strict=false] [--trust-store] [--no-start] [--no-browser]
  keydris init codex <agent>         Configure OpenAI Codex + CA
                                       [--trust-store] [--no-start] [--no-browser]
  keydris deinit claude-code|codex   Undo init: remove the Keydris config
  keydris proxy up                   Start the brokered egress proxy in the background
  keydris proxy down                 Stop the background proxy
  keydris proxy restart              Stop and start the verified Keydris proxy
  keydris proxy status               Check the local proxy process and health
  keydris proxy logs [--follow]       Read recent proxy output [--lines 50]
  keydris proxy scope list           Show the origins detected from the agent's policy
  keydris run -- <cmd...>            Run a command inside a keydris session
  keydris codex [args...]            Run OpenAI Codex inside a keydris session
  keydris status                     Check identity, proxy, integrations and control plane
                                       [--json] [--verbose] [--offline] [--target codex|claude-code]
  keydris doctor                     Detailed read-only status and recovery guidance
  keydris skill [--brief]            Read the bundled agent skill or session briefing
  keydris reset                      Preview and confirm removal of local setup and certificates
                                       [--dry-run] [--all] [--yes] [--keep-trust]
  keydris logs                       Print and verify the hash-chained evidence ledger
  keydris upgrade                    Download & replace the binary with the latest release
                                       [--channel stable|dev] [--version <v>] [--no-config]
  keydris telemetry [status|on|off]  Show or change anonymous install telemetry
  keydris version                    Print the version
  keydris help                       Show this help

Output: keydris --color auto|always|never <command> (auto respects NO_COLOR).
Banner: KEYDRIS_LOGO_COLOR=default uses the terminal foreground for light themes.
Status exits 0 when its checks pass, 1 when attention is needed, 2 for invalid arguments.
`)
}
