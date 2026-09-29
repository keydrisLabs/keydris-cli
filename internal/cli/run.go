package cli

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	"github.com/keydrisLabs/keydris-cli/internal/config"
	"github.com/keydrisLabs/keydris-cli/internal/node/sandbox"
	hostenv "github.com/keydrisLabs/keydris-cli/internal/platform"
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
	if err := cfg.ValidatePaths(); err != nil {
		fmt.Fprintf(os.Stderr, "keydris run: %v\n", err)
		return 1
	}
	executable, err := hostenv.ResolveCommand(cmd[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "keydris run: %v\n", err)
		return 1
	}
	sid := "run-" + newProxyToken()

	if code := hookSessionStart(cfg, *blueprint, sid, agentRuntimeForCommand(cmd[0])); code != 0 {
		return code
	}
	ended := false
	endSession := func() int {
		code := hookSessionEnd(cfg, sid)
		if code == 0 {
			ended = true
		}
		return code
	}
	// Cover ordinary errors and panics after minting. OS-level hard kills are
	// handled by the daemon's managed-owner liveness check.
	defer func() {
		if !ended {
			_ = endSession()
		}
	}()

	// The per-session token registered by hookSessionStart is the session handle;
	// carry it in the proxy URL userinfo so this command's egress is attributed
	// to this session (and isolated from any concurrent session).
	st, err := loadState(cfg, sid)
	if err != nil {
		fmt.Fprintf(os.Stderr, "keydris run: load session state: %v\n", err)
		_ = endSession()
		return 1
	}
	token := st.Handle

	child := exec.Command(executable, cmd[1:]...)
	child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
	child.Env = append(os.Environ(),
		"KEYDRIS_SESSION="+sid,
		sessionOwnerEnv+"="+sessionOwnerRun,
	)
	switch cfg.DataPlane {
	case "proxyenv":
		if err := sandbox.BuildCABundle(cfg.CAPath, cfg.CABundlePath); err != nil {
			fmt.Fprintf(os.Stderr, "keydris run: CA bundle: %v\n", err)
			_ = endSession()
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
			_ = endSession()
			return 1
		}
		p := proxyAuthURL(cfg.HTTPProxyPort, token)
		child.Env = appendProxyEnvironment(child.Env, p, cfg.CABundlePath)
	}

	if err := child.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "keydris run: %v\n", err)
		_ = endSession()
		return 1
	}
	// Bind peer verification to the wrapped process tree: the registered session
	// is only honored for connections from this command and its descendants.
	updateSessionOwner(cfg, sid, child.Process.Pid, true)

	// Keep the wrapper alive long enough to revoke on Ctrl-C/SIGTERM. The child
	// normally receives the terminal signal as well; stopProcess is a bounded
	// fallback for detached/platform-specific process behavior.
	signalCh := make(chan os.Signal, 1)
	signal.Notify(signalCh, os.Interrupt, syscall.SIGTERM)
	waitDone := make(chan struct{})
	forwardDone := make(chan struct{})
	go func() {
		defer close(forwardDone)
		select {
		case <-signalCh:
			_ = stopProcess(child.Process)
		case <-waitDone:
		}
	}()
	waitErr := child.Wait()
	signal.Stop(signalCh)
	close(waitDone)
	<-forwardDone

	if waitErr != nil {
		_ = endSession()
		if exit, ok := waitErr.(*exec.ExitError); ok {
			return exit.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "keydris run: %v\n", waitErr)
		return 1
	}
	return endSession()
}

// runCodex launches the OpenAI Codex CLI inside a Keydris-owned session. Codex
// thread lifecycle can outlive client connections, so the wrapper remains the
// lifecycle boundary that guarantees terminal-process-exit revocation.
func runCodex(args []string) int {
	executable, err := hostenv.ResolveCommand("codex")
	if err != nil {
		fmt.Fprintf(os.Stderr, "keydris codex: %v\n", err)
		return 1
	}
	if err := validateCodexHookArgs(args); err != nil {
		fmt.Fprintf(os.Stderr, "keydris codex: %v\n", err)
		return 1
	}
	cfg := config.Load()
	legacy, err := legacyCodexHooks(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "keydris codex: read %s: %v\n", cfg.CodexHooksPath, err)
		return 1
	}
	if legacy {
		fmt.Fprintf(os.Stderr, "keydris codex: %s still holds Keydris hooks from an earlier release; run `keydris init codex <agent-id>` once to remove them\n", cfg.CodexHooksPath)
		return 1
	}
	if !codexConfigured(cfg) {
		fmt.Fprintln(os.Stderr, "keydris codex: run `keydris init codex <agent-id>` first")
		return 1
	}
	hookOptions, err := codexHookOptions()
	if err != nil {
		fmt.Fprintf(os.Stderr, "keydris codex: %v\n", err)
		return 1
	}
	if err := verifyCodexHookExecution(hookOptions); err != nil {
		fmt.Fprintf(os.Stderr, "keydris codex: %v; repair the hooks with `keydris init codex <agent-id>` before retrying\n", err)
		return 1
	}
	// Codex skips an untrusted hook without an error, so the codex about to
	// run must confirm it trusts and enables every Keydris hook.
	overrides, err := governedCodexOverrides(executable, hookOptions)
	if err != nil {
		fmt.Fprintf(os.Stderr, "keydris codex: %v\n", err)
		return 1
	}
	wrapped := append([]string{"--", executable}, codexCommandArgs(args, overrides)...)
	return runRun(wrapped)
}

// codexEnforcementOverrides are the -c values every governed Codex launch
// carries. They enable Codex's own sandboxed-network proxy and let it honor
// the Keydris upstream proxy inherited from the session. The public wildcard
// is constrained again by Keydris; explicit loopback entries permit the local
// upstream endpoint.
func codexEnforcementOverrides() []string {
	return []string{
		"features.hooks=true",
		"sandbox_workspace_write.network_access=true",
		"features.network_proxy.enabled=true",
		`features.network_proxy.domains={"*"="allow","127.0.0.1"="allow","localhost"="allow"}`,
	}
}

// codexCommandArgs places the governed overrides among the user's arguments.
func codexCommandArgs(args, overrides []string) []string {
	if runtime.GOOS == "windows" {
		// Managed networking cannot run in Codex's unelevated backend. Scope
		// the required backend to this launch rather than rewriting user config.
		overrides = append(append([]string{}, overrides...), `windows.sandbox="elevated"`)
	}
	return withCodexConfig(args, overrides)
}

func validateCodexHookArgs(args []string) error {
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if argument == "--disable" {
			if index+1 < len(args) && disablesCodexHooks(args[index+1]) {
				return fmt.Errorf("hooks cannot be disabled in a governed session")
			}
			continue
		}
		if strings.HasPrefix(argument, "--disable=") &&
			disablesCodexHooks(strings.TrimPrefix(argument, "--disable=")) {
			return fmt.Errorf("hooks cannot be disabled in a governed session")
		}
		if argument == "-c" || argument == "--config" {
			if index+1 < len(args) && overridesCodexEnforcement(args[index+1]) {
				return fmt.Errorf("hook and Windows sandbox settings are reserved by `keydris codex`")
			}
			index++
			continue
		}
		if strings.HasPrefix(argument, "-c") && argument != "-c" &&
			overridesCodexEnforcement(strings.TrimPrefix(argument, "-c")) {
			return fmt.Errorf("hook and Windows sandbox settings are reserved by `keydris codex`")
		}
		if strings.HasPrefix(argument, "--config=") &&
			overridesCodexEnforcement(strings.TrimPrefix(argument, "--config=")) {
			return fmt.Errorf("hook and Windows sandbox settings are reserved by `keydris codex`")
		}
	}
	return nil
}

func disablesCodexHooks(value string) bool {
	for _, feature := range strings.Split(strings.ToLower(value), ",") {
		feature = strings.TrimSpace(feature)
		if feature == "hooks" || feature == "codex_hooks" {
			return true
		}
	}
	return false
}

func overridesCodexEnforcement(value string) bool {
	assignment := strings.ToLower(value)
	for _, character := range []string{" ", "\t", "\r", "\n", `"`, `'`} {
		assignment = strings.ReplaceAll(assignment, character, "")
	}
	if runtime.GOOS == "windows" && (strings.HasPrefix(assignment, "windows.sandbox=") || strings.HasPrefix(assignment, "windows=")) {
		return true
	}
	if strings.HasPrefix(assignment, "features.hooks=") ||
		strings.HasPrefix(assignment, "features.codex_hooks=") {
		return true
	}
	// Codex accepts TOML values in -c/--config, including inline tables such as
	// `features = { hooks = false }`. Treat any hooks key inside that table as
	// reserved instead of relying on one spelling of the dotted assignment.
	if strings.HasPrefix(assignment, "features={") {
		body := strings.TrimPrefix(assignment, "features={")
		return strings.HasPrefix(body, "hooks=") ||
			strings.HasPrefix(body, "codex_hooks=") ||
			strings.Contains(body, ",hooks=") ||
			strings.Contains(body, ",codex_hooks=")
	}
	return false
}

func appendProxyEnvironment(env []string, proxyURL, caPath string) []string {
	env = append(env,
		"HTTP_PROXY="+proxyURL, "HTTPS_PROXY="+proxyURL, "http_proxy="+proxyURL, "https_proxy="+proxyURL,
		"NODE_EXTRA_CA_CERTS="+caPath)
	if runtime.GOOS == "windows" {
		// Windows-native curl/git use the certificate store. Pointing their
		// replacement-style variables at a Keydris-only PEM would discard the
		// public roots. `init --trust-store` covers those tools.
		return env
	}
	return append(env,
		"CURL_CA_BUNDLE="+caPath, "SSL_CERT_FILE="+caPath,
		"GIT_SSL_CAINFO="+caPath, "REQUESTS_CA_BUNDLE="+caPath)
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
