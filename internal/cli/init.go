package cli

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/keydrisLabs/keydris-cli/internal/config"
	"github.com/keydrisLabs/keydris-cli/internal/node/login"
	"github.com/keydrisLabs/keydris-cli/internal/node/proxy"
	"github.com/keydrisLabs/keydris-cli/internal/node/sandbox"
	"github.com/keydrisLabs/keydris-cli/internal/node/sessionsock"
)

// runInit implements one-command onboarding for Claude Code and OpenAI Codex.
// Claude uses its native sandbox and lifecycle hooks; Codex is launched through
// `keydris codex` so Keydris can reliably clean up when the process exits.
func runInit(args []string) int {
	const usage = "usage: keydris init <claude-code|codex> [agent-id] [--strict] [--trust-store]"

	interactive := false
	if len(args) == 0 {
		interactive = true
		var ok bool
		args, ok = promptInit()
		if !ok {
			fmt.Fprintln(os.Stderr, usage)
			return 1
		}
	}
	if args[0] == "" || args[0][0] == '-' {
		fmt.Fprintln(os.Stderr, usage)
		return 1
	}
	target := args[0]
	if target == "openai" {
		target = "codex"
	}
	if target != "claude-code" && target != "codex" {
		fmt.Fprintf(os.Stderr, "keydris init: unknown target %q (want claude-code or codex)\n", target)
		return 1
	}

	cfg := config.Load()

	// The explicit positional value wins; an earlier `keydris init` has already
	// persisted the common case so users need not enter it twice.
	rest := args[1:]
	agentID := cfg.AgentID
	if len(rest) > 0 && rest[0] != "" && rest[0][0] != '-' {
		agentID = rest[0]
		rest = rest[1:]
	}
	if agentID == "" {
		fmt.Fprintf(os.Stderr, "keydris init %s: missing required <agent-id>\n", target)
		fmt.Fprintln(os.Stderr, usage)
		return 1
	}

	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	strict := fs.Bool("strict", true, "lock the sandbox as a hard gate (failIfUnavailable + no unsandboxed escape)")
	trustStore := fs.Bool("trust-store", false, "also install the Keydris CA into the OS trust store (may need sudo)")
	if err := fs.Parse(rest); err != nil {
		return 1
	}

	// Persist only the agent identity. The assigned policy is control-plane
	// state and is never selected or trusted from this local configuration.
	if err := config.SaveAgentID(cfg.DataDir, agentID); err != nil {
		fmt.Fprintf(os.Stderr, "keydris init: save agent id: %v\n", err)
		return 1
	}
	cfg.AgentID = agentID

	// Generate-and-persist the CA so the daemon loads the same root that the
	// sandbox is told to trust below.
	if _, err := proxy.LoadOrCreateCA(cfg.CAPath, cfg.CAKeyPath, "Keydris CA", 825*24*time.Hour); err != nil {
		fmt.Fprintf(os.Stderr, "keydris init: CA: %v\n", err)
		return 1
	}
	if err := sandbox.BuildCABundle(cfg.CAPath, cfg.CABundlePath); err != nil {
		fmt.Fprintf(os.Stderr, "keydris init: CA bundle: %v\n", err)
		return 1
	}

	if target == "claude-code" {
		if err := sandbox.Configure(cfg.ClaudeSettingsPath, sandbox.Options{
			HTTPProxyPort:    cfg.HTTPProxyPort,
			AllowedDomains:   cfg.AllowedDomains,
			CAPath:           cfg.CABundlePath,
			Strict:           *strict,
			SessionStartHook: internalSessionStartCmd,
			SessionEndHook:   internalSessionEndCmd,
			PreToolUseHook:   internalPreToolUseCmd,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "keydris init: configure sandbox: %v\n", err)
			return 1
		}
	}
	if target == "codex" {
		hookOptions, err := codexHookOptions()
		if err != nil {
			fmt.Fprintf(os.Stderr, "keydris init: configure Codex hooks: %v\n", err)
			return 1
		}
		if err := sandbox.ConfigureCodexHooks(cfg.CodexHooksPath, hookOptions); err != nil {
			fmt.Fprintf(os.Stderr, "keydris init: configure Codex hooks: %v\n", err)
			return 1
		}
	}
	if _, err := sessionsock.LoadOrCreateSecret(cfg.SessionAuthFile); err != nil {
		fmt.Fprintf(os.Stderr, "keydris init: session socket auth: %v\n", err)
		return 1
	}

	// The daemon needs a device certificate bound to this agent; the browser
	// sign-in is the enrollment step now that one-time tokens are gone. A valid
	// identity already bound to this agent is reused as-is.
	if id, err := login.Load(cfg.IdentityDir); err != nil || id.Expired() || id.AgentID != agentID {
		fmt.Println("Sign in with your browser to bind this device to the agent…")
		if code := browserLogin(cfg, defaultLoginHint(), false); code != 0 {
			fmt.Fprintln(os.Stderr, "keydris init: sign-in incomplete — run `keydris login` before `keydris proxy up`")
		}
	}

	if !interactive {
		printInitBanner(os.Stdout)
	}
	if target == "claude-code" {
		fmt.Printf("Configured Claude Code sandbox in %s\n", cfg.ClaudeSettingsPath)
	} else {
		fmt.Println("Configured OpenAI Codex launch integration")
	}
	fmt.Printf("  agent id: %s\n", agentID)

	// Derive governed origins from the agent policy
	origins, detected := detectPolicyScope(cfg, agentID, os.Stdout)
	printPolicyScope(os.Stdout, origins, detected)

	fmt.Printf("  CA bundle: %s\n", cfg.CABundlePath)
	if target == "claude-code" {
		fmt.Printf("  sandbox.enabled=true, network.httpProxyPort=%d, per-session SVID hooks wired\n", cfg.HTTPProxyPort)
	}
	if target == "claude-code" && *strict {
		fmt.Printf("  strict: failIfUnavailable=true, allowUnsandboxedCommands=false\n")
	}

	if *trustStore {
		if err := sandbox.InstallTrustStore(cfg.CAPath); err != nil {
			fmt.Fprintf(os.Stderr, "keydris init: OS trust-store install failed (env vars still apply): %v\n", err)
		} else {
			fmt.Printf("  CA installed into the OS trust store\n")
		}
	}

	if target == "claude-code" {
		fmt.Printf("\nNext: keydris proxy up\n      claude\n")
	} else {
		fmt.Printf("  command hooks: %s\n", cfg.CodexHooksPath)
		fmt.Printf("\nNext: keydris proxy up\n      keydris codex\n")
		fmt.Println("Codex is wrapped so the Keydris session is revoked when Codex exits.")
		fmt.Println("Run `/hooks` once inside Codex to trust the Keydris command hooks.")
	}
	return 0
}

func promptInit() ([]string, bool) {
	info, err := os.Stdin.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return nil, false
	}
	printInitBanner(os.Stdout)
	fmt.Fprintln(os.Stdout, "Choose an agent integration:")
	fmt.Fprintln(os.Stdout, "  1) Claude Code")
	fmt.Fprintln(os.Stdout, "  2) OpenAI Codex")
	fmt.Fprintln(os.Stdout)
	fmt.Fprint(os.Stdout, "Selection [1-2]: ")

	reader := bufio.NewReader(os.Stdin)
	choice, err := reader.ReadString('\n')
	if err != nil {
		return nil, false
	}
	var target string
	switch strings.TrimSpace(choice) {
	case "1", "claude", "claude-code":
		target = "claude-code"
	case "2", "codex", "openai":
		target = "codex"
	default:
		fmt.Fprintln(os.Stderr, "keydris init: invalid selection")
		return nil, false
	}

	fmt.Fprint(os.Stdout, "Agent id: ")
	agent, err := reader.ReadString('\n')
	if err != nil {
		return nil, false
	}
	agent = strings.TrimSpace(agent)
	if agent == "" {
		fmt.Fprintln(os.Stderr, "keydris init: agent id is required")
		return nil, false
	}
	return []string{target, agent}, true
}

func printInitBanner(w io.Writer) {
	fmt.Fprint(w, `
 _  __               _      _
| |/ /___ _   _  __| |_ __(_)___
| ' // _ \ | | |/ _' | '__| / __|
| . \  __/ |_| | (_| | |  | \__ \
|_|\_\___|\__, |\__,_|_|  |_|___/
          |___/   governed agent egress
`)
}
