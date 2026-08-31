package cli

import (
	"fmt"
	"os"

	"github.com/keydrisLabs/keydris-cli/internal/config"
	"github.com/keydrisLabs/keydris-cli/internal/telemetry"
)

// telemetryCommands are the dispatch names allowed to report install/upgrade
// telemetry. The __* hook entrypoints are excluded so the agent's hot paths
// never gain extra egress, and `telemetry` itself is excluded so a user whose
// very first command is `keydris telemetry off` opts out before anything is
// sent.
var telemetryCommands = map[string]bool{
	"proxy": true, "status": true, "login": true, "whoami": true,
	"logout": true, "run": true, "codex": true, "openai": true,
	"init": true, "deinit": true, "logs": true, "upgrade": true,
	"version": true, "--version": true,
}

// recordTelemetry reports at most one anonymous event for this invocation.
// All gating (opt-out, DO_NOT_TRACK, unkeyed builds) lives in the telemetry
// package; this decides only which process may report at all.
func recordTelemetry(command string) {
	if !telemetryCommands[command] {
		return
	}
	// The detached proxy daemon re-execs `keydris proxy up`; the parent
	// command already reported this run.
	if os.Getenv(daemonEnv) == "1" {
		return
	}
	// Load seeds KEYDRIS_* from ~/.keydris.toml so the channel and a
	// fleet-configured `telemetry = "off"` are honored.
	cfg := config.Load()
	telemetry.RecordRun(cfg.DataDir, Version)
}

// runTelemetry shows or changes the anonymous install telemetry setting. The
// persisted flag lives under the data dir, not ~/.keydris.toml, because
// install.sh and `keydris upgrade` replace that file on every config refresh.
func runTelemetry(args []string) int {
	cfg := config.Load()
	action := "status"
	if len(args) > 0 {
		action = args[0]
	}
	switch action {
	case "status":
		printTelemetryStatus(cfg.DataDir)
		return 0
	case "off":
		if err := telemetry.SetOptOut(cfg.DataDir, true); err != nil {
			fmt.Fprintf(os.Stderr, "keydris telemetry off: %v\n", err)
			return 1
		}
		fmt.Println("telemetry: disabled")
		return 0
	case "on":
		if err := telemetry.SetOptOut(cfg.DataDir, false); err != nil {
			fmt.Fprintf(os.Stderr, "keydris telemetry on: %v\n", err)
			return 1
		}
		// Show the effective state: an environment override or an unkeyed
		// build can keep telemetry off even after opting back in.
		printTelemetryStatus(cfg.DataDir)
		return 0
	default:
		fmt.Fprintln(os.Stderr, "usage: keydris telemetry [status|on|off]")
		return 1
	}
}

func printTelemetryStatus(dataDir string) {
	enabled, reason := telemetry.Status(dataDir)
	if !enabled {
		fmt.Printf("telemetry: disabled (%s)\n", reason)
		return
	}
	fmt.Println("telemetry: enabled (anonymous install/upgrade events only)")
	if id := telemetry.AnonymousID(dataDir); id != "" {
		fmt.Printf("anonymous id: %s\n", id)
	}
}
