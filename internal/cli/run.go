package cli

import (
	"flag"
	"fmt"
	"os"
	"os/exec"

	"github.com/keydrisLabs/keydris-cli/internal/config"
	"github.com/keydrisLabs/keydris-cli/internal/node/sandbox"
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
	sid := "run-" + newProxyToken()

	if code := hookSessionStart(cfg, *blueprint, sid); code != 0 {
		return code
	}

	// The per-session token registered by hookSessionStart is the session handle;
	// carry it in the proxy URL userinfo so this command's egress is attributed
	// to this session (and isolated from any concurrent session).
	st, err := loadState(cfg, sid)
	if err != nil {
		fmt.Fprintf(os.Stderr, "keydris run: load session state: %v\n", err)
		_ = hookSessionEnd(cfg, sid)
		return 1
	}
	token := st.Handle

	child := exec.Command(cmd[0], cmd[1:]...)
	child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
	child.Env = append(os.Environ(),
		"KEYDRIS_SESSION="+sid,
		sessionOwnerEnv+"="+sessionOwnerRun,
	)
	switch cfg.DataPlane {
	case "proxyenv":
		if err := sandbox.BuildCABundle(cfg.CAPath, cfg.CABundlePath); err != nil {
			fmt.Fprintf(os.Stderr, "keydris run: CA bundle: %v\n", err)
			_ = hookSessionEnd(cfg, sid)
			return 1
		}
		p := proxyAuthURL(cfg.ProxyPort, token)
		child.Env = appendProxyEnvironment(child.Env, p, cfg.CABundlePath)
	case "sandbox", "claude-code":
		// Outside the real Claude Code sandbox, point the command at the Keydris
		// proxy explicitly and trust the CA so the MITM path verifies. Inside a
		// real session the sandbox does this routing itself.
		if err := sandbox.BuildCABundle(cfg.CAPath, cfg.CABundlePath); err != nil {
			fmt.Fprintf(os.Stderr, "keydris run: CA bundle: %v\n", err)
			_ = hookSessionEnd(cfg, sid)
			return 1
		}
		p := proxyAuthURL(cfg.HTTPProxyPort, token)
		child.Env = appendProxyEnvironment(child.Env, p, cfg.CABundlePath)
	}

	if err := child.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "keydris run: %v\n", err)
		_ = hookSessionEnd(cfg, sid)
		return 1
	}
	// Bind peer verification to the wrapped process tree: the registered session
	// is only honored for connections from this command and its descendants.
	updateSessionOwner(cfg, sid, child.Process.Pid)
	if err := child.Wait(); err != nil {
		_ = hookSessionEnd(cfg, sid)
		if exit, ok := err.(*exec.ExitError); ok {
			return exit.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "keydris run: %v\n", err)
		return 1
	}
	return hookSessionEnd(cfg, sid)
}

func appendProxyEnvironment(env []string, proxyURL, caPath string) []string {
	return append(env,
		"HTTP_PROXY="+proxyURL, "HTTPS_PROXY="+proxyURL, "http_proxy="+proxyURL, "https_proxy="+proxyURL,
		"CURL_CA_BUNDLE="+caPath, "SSL_CERT_FILE="+caPath,
		"NODE_EXTRA_CA_CERTS="+caPath, "GIT_SSL_CAINFO="+caPath,
		"REQUESTS_CA_BUNDLE="+caPath)
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
