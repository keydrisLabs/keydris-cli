package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/keydrisLabs/keydris-cli/internal/config"
	"github.com/keydrisLabs/keydris-cli/internal/node/daemon"
	"github.com/keydrisLabs/keydris-cli/internal/proxyscope"
)

// daemonEnv marks the re-exec'd child that actually runs the proxy in the
// foreground, so the parent can return after backgrounding it.
const daemonEnv = "KEYDRIS_PROXY_DAEMON"

var errProcessNotRunning = errors.New("process is not running")

type proxyProcessRecord struct {
	PID      int    `json:"pid"`
	Identity string `json:"identity"`
}

// runProxy dispatches `keydris proxy <subcommand>`.
func runProxy(args []string) int {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(os.Stdout, "Usage: keydris proxy up|down|restart|status|logs|scope list")
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	if args[0] == "scope" {
		return runProxyScope(args[1:])
	}
	if args[0] == "logs" {
		return runProxyLogs(args[1:])
	}
	fs := flag.NewFlagSet("proxy "+args[0], flag.ContinueOnError)
	if code := parseFlags(fs, args[1:]); code >= 0 {
		return code
	}
	cfg := config.Load()
	if err := cfg.ValidatePaths(); err != nil {
		newUI(os.Stderr).row("error", "Paths", err.Error())
		return 1
	}
	switch args[0] {
	case "up":
		return runProxyUp()
	case "down":
		return runProxyDown()
	case "restart":
		if code := runProxyDown(); code != 0 {
			return code
		}
		return runProxyUp()
	case "status":
		health := inspectProxy(cfg)
		newUI(os.Stdout).row(health.state, "Proxy", health.detail)
		if health.state != "ok" {
			return 1
		}
		return 0
	default:
		fmt.Fprintf(os.Stderr, "keydris proxy: unknown subcommand %q\n", args[0])
		return 2
	}
}

type proxyScopeAction int

const (
	proxyScopeUsage proxyScopeAction = iota
	proxyScopeList
	// proxyScopeRetired now explains that scope is policy-derived.
	proxyScopeRetired
)

// classifyProxyScopeArgs routes scope commands and identifies retired subcommands.
func classifyProxyScopeArgs(args []string) (proxyScopeAction, string) {
	if len(args) == 0 {
		return proxyScopeUsage, ""
	}
	switch args[0] {
	case "add", "remove", "all":
		return proxyScopeRetired, args[0]
	case "list":
		if len(args) == 1 {
			return proxyScopeList, "list"
		}
	}
	return proxyScopeUsage, args[0]
}

// runProxyScope shows the policy-derived proxy scope; it is refreshed on each session start.
func runProxyScope(args []string) int {
	switch action, subcommand := classifyProxyScopeArgs(args); action {
	case proxyScopeList:
	case proxyScopeRetired:
		fmt.Fprintf(os.Stderr,
			"keydris proxy scope: %q was removed; scope is detected from the agent's policy\n", subcommand)
		fmt.Fprintln(os.Stderr,
			"  run `keydris init <target> <agent-id>` to re-detect it, or `keydris proxy scope list` to view it")
		return 1
	default:
		fmt.Fprintln(os.Stderr, "usage: keydris proxy scope list")
		return 1
	}

	cfg := config.Load()
	if cfg.ManagedScopeError != nil {
		fmt.Fprintf(os.Stderr, "keydris proxy scope: %v\n", cfg.ManagedScopeError)
		return 1
	}
	scope, err := proxyscope.New(cfg.ManagedMode, cfg.ManagedDestinations)
	if err != nil {
		fmt.Fprintf(os.Stderr, "keydris proxy scope: %v\n", err)
		return 1
	}

	state, err := config.ReadManagedScope(cfg.DataDir)
	if err == nil && state.Source == config.ManagedScopeSourcePolicy {
		fmt.Printf("mode: %s (detected from policy)\n", scope.Mode())
	} else {
		fmt.Printf("mode: %s\n", scope.Mode())
	}
	for _, dst := range scope.Destinations() {
		fmt.Printf("  %s\n", dst)
	}
	if scope.Mode() == proxyscope.ModeAll {
		fmt.Println("  (policy scope not detected yet — run `keydris init <target> <agent-id>`)")
	}
	return 0
}

// runProxyDown stops the backgrounded proxy started by `keydris proxy up`. It
// reads the pidfile under the data dir, verifies that the PID still identifies
// the exact process started by Keydris, asks it to stop, and waits for exit.
func runProxyDown() int {
	cfg := config.Load()
	pidPath := filepath.Join(cfg.DataDir, "proxy.pid")

	data, err := os.ReadFile(pidPath)
	if err != nil {
		if os.IsNotExist(err) {
			newUI(os.Stdout).row("inactive", "Proxy", "Already stopped (no PID file)")
			return 0
		}
		fmt.Fprintf(os.Stderr, "keydris proxy down: %v\n", err)
		return 1
	}
	var record proxyProcessRecord
	unlock, lockErr := lockProxyFile(filepath.Join(cfg.DataDir, "proxy.lock"))
	if lockErr != nil {
		newUI(os.Stderr).row("error", "Proxy", lockErr.Error())
		return 1
	}
	defer unlock()
	// Read again after taking the lifecycle lock.
	data, err = os.ReadFile(pidPath)
	if os.IsNotExist(err) {
		newUI(os.Stdout).row("inactive", "Proxy", "Already stopped")
		return 0
	}
	if err != nil {
		newUI(os.Stderr).row("error", "Proxy", err.Error())
		return 1
	}
	if err := json.Unmarshal(data, &record); err != nil {
		// Older releases wrote a bare PID. Refuse to signal it because a reused
		// PID could now belong to an unrelated process.
		if pid, legacyErr := strconv.Atoi(strings.TrimSpace(string(data))); legacyErr == nil && pid > 0 {
			fmt.Fprintf(os.Stderr, "keydris proxy down: legacy pidfile %s cannot be safely verified; verify pid %d manually, then remove the pidfile\n", pidPath, pid)
			return 1
		}
		fmt.Fprintf(os.Stderr, "keydris proxy down: invalid pidfile %s\n", pidPath)
		return 1
	}
	if record.PID <= 0 || record.Identity == "" {
		fmt.Fprintf(os.Stderr, "keydris proxy down: invalid pidfile %s\n", pidPath)
		return 1
	}

	identity, err := processIdentity(record.PID)
	if err != nil {
		if !errors.Is(err, errProcessNotRunning) {
			newUI(os.Stderr).row("error", "Proxy", "Cannot verify the saved process: "+err.Error())
			return 1
		}
		_ = os.Remove(pidPath)
		newUI(os.Stdout).row("inactive", "Proxy", "Stopped; removed stale PID file")
		return 0
	}
	if identity != record.Identity {
		fmt.Fprintf(os.Stderr, "keydris proxy down: pid %d belongs to a different process; refusing to stop it (remove stale %s)\n", record.PID, pidPath)
		return 1
	}

	proc, err := os.FindProcess(record.PID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "keydris proxy down: find pid %d: %v\n", record.PID, err)
		return 1
	}
	if err := stopProcess(proc); err != nil {
		fmt.Fprintf(os.Stderr, "keydris proxy down: stop pid %d: %v\n", record.PID, err)
		return 1
	}
	_ = proc.Release()

	// Wait up to ~3s for a clean exit. Keep the pidfile when the process does
	// not stop so a subsequent command can retry rather than reporting success.
	for i := 0; i < 30; i++ {
		if current, identityErr := processIdentity(record.PID); errors.Is(identityErr, errProcessNotRunning) || (identityErr == nil && current != record.Identity) {
			_ = os.Remove(pidPath)
			newUI(os.Stdout).row("ok", "Proxy", fmt.Sprintf("Stopped (PID %d)", record.PID))
			return 0
		}
		time.Sleep(100 * time.Millisecond)
	}
	fmt.Fprintf(os.Stderr, "keydris proxy down: timed out waiting for pid %d; pidfile retained\n", record.PID)
	return 1
}

// runProxyUp starts the brokered egress proxy. It backgrounds itself — no
// trailing `&` needed — by re-exec'ing detached, writing logs and a pidfile
// under the data dir. The detached child re-enters with KEYDRIS_PROXY_DAEMON=1
// set and runs the daemon in the foreground until interrupted.
func runProxyUp() int {
	cfg := config.Load()
	if err := checkResetInProgress(cfg); err != nil {
		newUI(os.Stderr).row("error", "Proxy", err.Error())
		return 1
	}

	// Child: run the daemon (blocking) until signaled.
	if os.Getenv(daemonEnv) == "1" {
		if err := daemon.Run(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "keydris proxy: %v\n", err)
			return 1
		}
		return 0
	}

	ui := newUI(os.Stdout)
	if err := cfg.ValidatePaths(); err != nil {
		ui.row("error", "Paths", err.Error())
		return 1
	}
	if err := os.MkdirAll(cfg.DataDir, 0700); err != nil {
		ui.row("error", "Proxy", err.Error())
		return 1
	}
	unlock, lockErr := lockProxyFile(filepath.Join(cfg.DataDir, "proxy.lock"))
	if lockErr != nil {
		ui.row("warning", "Proxy", lockErr.Error())
		return 1
	}
	defer unlock()
	if err := checkResetInProgress(cfg); err != nil {
		ui.row("error", "Proxy", err.Error())
		return 1
	}
	health := inspectProxy(cfg)
	if health.state == "ok" {
		ui.row("ok", "Proxy", "Already running; "+health.detail)
		return 0
	}
	if health.state == "error" {
		ui.row("error", "Proxy", health.detail)
		ui.next("keydris proxy logs")
		return 1
	}

	// Parent: spawn the detached child, then wait briefly for it to come up so
	// we can report success/failure synchronously instead of leaving a `&` job.
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "keydris proxy up: %v\n", err)
		return 1
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "keydris proxy up: %v\n", err)
		return 1
	}
	_ = os.Chmod(cfg.DataDir, 0o700)
	logPath := filepath.Join(cfg.DataDir, "proxy.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "keydris proxy up: open log: %v\n", err)
		return 1
	}
	_ = logFile.Chmod(0o600)
	defer logFile.Close()

	child := exec.Command(exe, "proxy", "up")
	child.Env = append(os.Environ(), daemonEnv+"=1")
	child.Stdout, child.Stderr = logFile, logFile
	configureDetachedProcess(child)
	if err := child.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "keydris proxy up: start: %v\n", err)
		return 1
	}
	pid := child.Process.Pid
	pidPath := filepath.Join(cfg.DataDir, "proxy.pid")
	identity, err := processIdentity(pid)
	if err != nil {
		_ = stopProcess(child.Process)
		_ = child.Wait()
		fmt.Fprintf(os.Stderr, "keydris proxy up: identify child: %v\n", err)
		return 1
	}
	record, err := json.Marshal(proxyProcessRecord{PID: pid, Identity: identity})
	if err != nil {
		_ = stopProcess(child.Process)
		_ = child.Wait()
		fmt.Fprintf(os.Stderr, "keydris proxy up: encode pidfile: %v\n", err)
		return 1
	}
	if err := os.WriteFile(pidPath, append(record, '\n'), 0o600); err != nil {
		_ = stopProcess(child.Process)
		_ = child.Wait()
		fmt.Fprintf(os.Stderr, "keydris proxy up: write pidfile: %v\n", err)
		return 1
	}
	_ = os.Chmod(pidPath, 0o600)

	// Detect an early exit via Wait (reliable, unlike signalling a pid that may
	// already be a zombie). If the child outlives the readiness window it keeps
	// running after we return — the kernel reparents it once we exit.
	exited := make(chan error, 1)
	go func() { exited <- child.Wait() }()

	finishProgress := ui.progress("Starting proxy")
	ready := false
	defer func() {
		if !ready {
			finishProgress(fmt.Errorf("startup did not complete; run keydris proxy logs"))
		}
	}()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case werr := <-exited:
			_ = os.Remove(pidPath)
			fmt.Fprintf(os.Stderr, "keydris proxy up: daemon exited on startup (%s); see %s\n", exitReason(werr), logPath)
			return 1
		default:
		}
		if health := inspectProxy(cfg); health.state == "ok" && health.pid == pid {
			ready = true
			finishProgress(nil)
			ui.row("ok", "Proxy", health.detail)
			ui.next("keydris status")
			return 0
		}
		time.Sleep(100 * time.Millisecond)
	}

	// No listener yet at the deadline: report whether it died or is just slow.
	select {
	case werr := <-exited:
		_ = os.Remove(pidPath)
		fmt.Fprintf(os.Stderr, "keydris proxy up: daemon exited on startup (%s); see %s\n", exitReason(werr), logPath)
		return 1
	default:
		fmt.Fprintf(os.Stderr, "keydris proxy up: readiness timed out (PID %d); process retained for inspection; see %s\n", pid, logPath)
		return 1
	}
}

// proxyListenPort is the local port the daemon's data plane listens on.
func proxyListenPort(cfg *config.Config) int {
	switch cfg.DataPlane {
	case "sandbox", "claude-code":
		return cfg.HTTPProxyPort
	default:
		return cfg.ProxyPort
	}
}

// exitReason renders a child Wait() result for a one-line startup error.
func exitReason(err error) string {
	if err == nil {
		return "exited cleanly"
	}
	return err.Error()
}
