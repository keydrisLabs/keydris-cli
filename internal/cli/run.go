package cli

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/keydrisLabs/keydris-cli/internal/config"
)

// runRun implements `keydris run [--blueprint B] -- <command...>`: it opens a
// keydris session (mint + bind + register), runs the wrapped command with the
// session bound (and HTTP(S)_PROXY set in proxy-env mode), then ends the session.
func runRun(args []string) int {
	flagArgs, cmd := splitDashDash(args)

	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	blueprint := fs.String("blueprint", "", "blueprint to bind (overrides config/env)")
	if err := fs.Parse(flagArgs); err != nil {
		return 1
	}
	if len(cmd) == 0 {
		fmt.Fprintln(os.Stderr, "usage: keydris run [--blueprint B] -- <command> [args...]")
		return 1
	}

	cfg := config.Load()
	sid := "run-" + time.Now().UTC().Format("20060102T150405")

	if code := hookSessionStart(cfg, *blueprint, sid); code != 0 {
		return code
	}
	defer hookSessionEnd(cfg, sid)

	// The per-session token registered by hookSessionStart is the session handle;
	// carry it in the proxy URL userinfo so this command's egress is attributed
	// to this session (and isolated from any concurrent session).
	st, _ := loadState(cfg, sid)
	token := st.Handle

	child := exec.Command(cmd[0], cmd[1:]...)
	child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
	child.Env = append(os.Environ(), "KEYDRIS_SESSION="+sid)
	switch cfg.DataPlane {
	case "proxyenv":
		p := fmt.Sprintf("http://127.0.0.1:%d", cfg.ProxyPort)
		child.Env = append(child.Env, "HTTP_PROXY="+p, "HTTPS_PROXY="+p, "http_proxy="+p, "https_proxy="+p)
	case "sandbox", "claude-code":
		// Outside the real Claude Code sandbox, point the command at the Keydris
		// proxy explicitly and trust the CA so the MITM path verifies. Inside a
		// real session the sandbox does this routing itself.
		p := proxyAuthURL(cfg.HTTPProxyPort, token)
		child.Env = append(child.Env,
			"HTTP_PROXY="+p, "HTTPS_PROXY="+p, "http_proxy="+p, "https_proxy="+p,
			"CURL_CA_BUNDLE="+cfg.CAPath, "SSL_CERT_FILE="+cfg.CAPath,
			"NODE_EXTRA_CA_CERTS="+cfg.CAPath, "GIT_SSL_CAINFO="+cfg.CAPath,
			"REQUESTS_CA_BUNDLE="+cfg.CAPath)
	}

	if err := child.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "keydris run: %v\n", err)
		return 1
	}
	// Bind peer verification to the wrapped process tree: the registered session
	// is only honored for connections from this command and its descendants.
	updateSessionOwner(cfg, sid, child.Process.Pid)
	if err := child.Wait(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return exit.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "keydris run: %v\n", err)
		return 1
	}
	return 0
}

// splitDashDash splits args at the first "--": flags before, command after.
func splitDashDash(args []string) (flags, cmd []string) {
	for i, a := range args {
		if a == "--" {
			return args[:i], args[i+1:]
		}
	}
	return args, nil
}
