package cli

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/keydrisLabs/keydris-cli/internal/config"
	"github.com/keydrisLabs/keydris-cli/internal/node/daemon"
	"github.com/keydrisLabs/keydris-cli/internal/proxyscope"
)

// daemonEnv marks the re-exec'd child that actually runs the proxy in the
// foreground, so the parent can return after backgrounding it.
const daemonEnv = "KEYDRIS_PROXY_DAEMON"

// runProxy dispatches `keydris proxy <subcommand>`.
func runProxy(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: keydris proxy up|down|scope")
		return 1
	}
	switch args[0] {
	case "up":
		return runProxyUp()
	case "down":
		return runProxyDown()
	case "scope":
		return runProxyScope(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "keydris proxy: unknown subcommand %q (want up|down|scope)\n", args[0])
		return 1
	}
}

func runProxyScope(args []string) int {
	const usage = "usage: keydris proxy scope add <origin>|remove <origin>|list|all"
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return 1
	}
	cfg := config.Load()

	switch args[0] {
	case "list":
		if cfg.ManagedScopeError != nil {
			fmt.Fprintf(os.Stderr, "keydris proxy scope: %v\n", cfg.ManagedScopeError)
			return 1
		}
		scope, err := proxyscope.New(cfg.ManagedMode, cfg.ManagedDestinations)
		if err != nil {
			fmt.Fprintf(os.Stderr, "keydris proxy scope: %v\n", err)
			return 1
		}
		fmt.Printf("mode: %s\n", scope.Mode())
		for _, dst := range scope.Destinations() {
			fmt.Printf("  %s\n", dst)
		}
		return 0
	case "all":
		if len(args) != 1 {
			fmt.Fprintln(os.Stderr, usage)
			return 1
		}
		if err := config.SaveManagedScope(cfg.DataDir, proxyscope.ModeAll, nil); err != nil {
			fmt.Fprintf(os.Stderr, "keydris proxy scope: %v\n", err)
			return 1
		}
		fmt.Println("keydris: all destinations are managed; restart the proxy to apply")
		return 0
	case "add", "remove":
		if len(args) != 2 {
			fmt.Fprintln(os.Stderr, usage)
			return 1
		}
		return updateProxyScope(cfg, args[0], args[1])
	default:
		fmt.Fprintln(os.Stderr, usage)
		return 1
	}
}

func updateProxyScope(cfg *config.Config, action, raw string) int {
	dst, err := proxyscope.Normalize(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "keydris proxy scope: %v\n", err)
		return 1
	}
	state, err := config.ReadManagedScope(cfg.DataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "keydris proxy scope: %v\n", err)
		return 1
	}
	if action == "remove" && state.Mode == proxyscope.ModeAll {
		fmt.Fprintln(os.Stderr, "keydris proxy scope: cannot remove one destination in all mode; add a destination to enter selected mode")
		return 1
	}
	destinations := make(map[string]struct{}, len(state.Destinations)+1)
	if state.Mode == proxyscope.ModeSelected {
		for _, existing := range state.Destinations {
			destinations[existing] = struct{}{}
		}
	}
	if action == "add" {
		destinations[dst] = struct{}{}
	} else {
		delete(destinations, dst)
	}
	list := make([]string, 0, len(destinations))
	for existing := range destinations {
		list = append(list, existing)
	}
	if err := config.SaveManagedScope(cfg.DataDir, proxyscope.ModeSelected, list); err != nil {
		fmt.Fprintf(os.Stderr, "keydris proxy scope: %v\n", err)
		return 1
	}
	fmt.Printf("keydris: proxy scope %s %s; restart the proxy to apply\n", action, dst)
	return 0
}

// runProxyDown stops the backgrounded proxy started by `keydris proxy up`. It
// reads the pidfile under the data dir, sends SIGTERM (the daemon shuts down
// cleanly on it), waits briefly for exit, and removes the pidfile.
func runProxyDown() int {
	cfg := config.Load()
	pidPath := filepath.Join(cfg.DataDir, "proxy.pid")

	data, err := os.ReadFile(pidPath)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("keydris: no proxy pidfile; nothing to stop")
			return 0
		}
		fmt.Fprintf(os.Stderr, "keydris proxy down: %v\n", err)
		return 1
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		fmt.Fprintf(os.Stderr, "keydris proxy down: invalid pidfile %s\n", pidPath)
		return 1
	}

	proc, _ := os.FindProcess(pid) // always succeeds on Unix
	if proc.Signal(syscall.Signal(0)) != nil {
		_ = os.Remove(pidPath)
		fmt.Printf("keydris: proxy not running (removed stale pidfile, pid=%d)\n", pid)
		return 0
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		fmt.Fprintf(os.Stderr, "keydris proxy down: signal pid %d: %v\n", pid, err)
		return 1
	}

	// Wait up to ~3s for a clean exit before removing the pidfile.
	for i := 0; i < 30; i++ {
		if proc.Signal(syscall.Signal(0)) != nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = os.Remove(pidPath)
	fmt.Printf("keydris: proxy stopped (pid=%d)\n", pid)
	return 0
}

// runProxyUp starts the brokered egress proxy. It backgrounds itself — no
// trailing `&` needed — by re-exec'ing detached, writing logs and a pidfile
// under the data dir. The detached child re-enters with KEYDRIS_PROXY_DAEMON=1
// set and runs the daemon in the foreground until interrupted.
func runProxyUp() int {
	cfg := config.Load()

	// Child: run the daemon (blocking) until signaled.
	if os.Getenv(daemonEnv) == "1" {
		if err := daemon.Run(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "keydris proxy: %v\n", err)
			return 1
		}
		return 0
	}

	// Refuse to start a second proxy (and keep the readiness check below
	// meaningful): if the listen port already accepts connections, bail.
	addr := fmt.Sprintf("127.0.0.1:%d", proxyListenPort(cfg))
	if c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
		_ = c.Close()
		fmt.Fprintf(os.Stderr, "keydris proxy up: %s already in use (is the proxy already running?)\n", addr)
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
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // detach from this terminal/session
	if err := child.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "keydris proxy up: start: %v\n", err)
		return 1
	}
	pid := child.Process.Pid
	pidPath := filepath.Join(cfg.DataDir, "proxy.pid")
	_ = os.WriteFile(pidPath, []byte(strconv.Itoa(pid)+"\n"), 0o600)
	_ = os.Chmod(pidPath, 0o600)

	// Detect an early exit via Wait (reliable, unlike signalling a pid that may
	// already be a zombie). If the child outlives the readiness window it keeps
	// running after we return — the kernel reparents it once we exit.
	exited := make(chan error, 1)
	go func() { exited <- child.Wait() }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case werr := <-exited:
			fmt.Fprintf(os.Stderr, "keydris proxy up: daemon exited on startup (%s); see %s\n", exitReason(werr), logPath)
			return 1
		default:
		}
		if c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
			_ = c.Close()
			fmt.Printf("keydris: proxy up (dataplane=%s, pid=%d, port=%d)\n", cfg.DataPlane, pid, proxyListenPort(cfg))
			fmt.Printf("  logs: %s    stop: keydris proxy down\n", logPath)
			return 0
		}
		time.Sleep(100 * time.Millisecond)
	}

	// No listener yet at the deadline: report whether it died or is just slow.
	select {
	case werr := <-exited:
		fmt.Fprintf(os.Stderr, "keydris proxy up: daemon exited on startup (%s); see %s\n", exitReason(werr), logPath)
		return 1
	default:
		fmt.Printf("keydris: proxy starting (pid=%d); see %s\n", pid, logPath)
		return 0
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
