package cli

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/keydrisLabs/keydris-cli/internal/config"
	"github.com/keydrisLabs/keydris-cli/internal/node/proxy"
	"github.com/keydrisLabs/keydris-cli/internal/node/sandbox"
	"github.com/keydrisLabs/keydris-cli/internal/node/sessionsock"
	hostenv "github.com/keydrisLabs/keydris-cli/internal/platform"
)

var agentUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func runInit(args []string) int {
	const usage = "Usage: keydris init [claude-code|codex|claude-desktop|codex-desktop] [agent-id] [--strict=false] [--trust-store] [--no-start] [--no-browser]"
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprintln(os.Stdout, usage)
		return 0
	}
	interactive := len(args) == 0
	if interactive {
		var ok bool
		args, ok = promptInit()
		if !ok {
			fmt.Fprintln(os.Stderr, usage)
			return 1
		}
	}
	target := args[0]
	if target == "openai" {
		target = "codex"
	}
	if !knownIntegration(target) {
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	cfg := config.Load()
	if err := checkResetInProgress(cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	ui := newUI(os.Stdout)
	if err := cfg.ValidatePaths(); err != nil {
		ui.row("error", "Paths", err.Error())
		return 1
	}
	rest := args[1:]
	agentID := cfg.AgentID
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		agentID = rest[0]
		rest = rest[1:]
	}
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	strict := fs.Bool("strict", true, "require the Claude sandbox and disable unsandboxed escape")
	trust := fs.Bool("trust-store", false, "also install the CA in the OS trust store")
	noStart := fs.Bool("no-start", false, "configure without starting the proxy")
	noBrowser := fs.Bool("no-browser", false, "print the sign-in URL without opening a browser")
	if code := parseFlags(fs, rest); code >= 0 {
		return code
	}
	if !agentUUID.MatchString(agentID) {
		ui.row("error", "Agent", "Enter the agent UUID from the Keydris dashboard")
		return 2
	}
	ui = newUI(os.Stdout)
	environmentTarget := target
	if !*strict {
		environmentTarget = ""
	}
	if err := checkInitEnvironment(environmentTarget); err != nil {
		ui.row("error", "Environment", err.Error())
		return 1
	}
	if hostenv.Current().WSL != "" && target == "claude-code" {
		if _, err := hostenv.ResolveCommand("claude"); err != nil {
			ui.row("error", "Agent runtime", err.Error())
			return 1
		}
	}
	// The Codex hook check runs the codex this device launches.
	var codexExecutable string
	if target == "codex" {
		resolved, err := hostenv.ResolveCommand("codex")
		if err != nil {
			ui.row("error", "Agent runtime", err.Error())
			return 1
		}
		codexExecutable = resolved
	}
	// Resolve the executable before writing any configuration.
	claudeOptions, err := claudeHookOptions(cfg, *strict)
	if err != nil {
		ui.row("error", "Hook executable", err.Error())
		return 1
	}
	codexOptions, err := codexHookOptions()
	if err != nil {
		ui.row("error", "Hook executable", err.Error())
		return 1
	}
	if !interactive {
		printInitBanner(os.Stdout)
	}
	if cfg.AgentID != "" && cfg.AgentID != agentID && inspectProxy(cfg).pid != 0 {
		ui.row("error", "Agent", "Stop the proxy before switching agents")
		ui.next("keydris proxy down")
		return 1
	}
	if err := config.SaveAgentID(cfg.DataDir, agentID); err != nil {
		ui.row("error", "Agent", err.Error())
		return 1
	}
	cfg.AgentID = agentID
	ui.row("ok", "Agent", agentID)
	if _, err := identityReady(cfg); err != nil {
		ui.row("working", "Sign in", "Bind this device to your agent in the browser")
		if code := browserLogin(cfg, defaultLoginHint(), *noBrowser); code != 0 {
			ui.row("error", "Setup incomplete", "Sign-in did not complete; rerun keydris init")
			return code
		}
	}
	if _, err := identityReady(cfg); err != nil {
		ui.row("error", "Identity", err.Error())
		return 1
	}
	ui.row("ok", "Identity", "Device is signed in")
	finish := ui.progress("Preparing certificates")
	_, err = proxy.LoadOrCreateCA(cfg.CAPath, cfg.CAKeyPath, "Keydris CA", 825*24*time.Hour)
	if err == nil {
		err = sandbox.BuildCABundle(cfg.CAPath, cfg.CABundlePath)
	}
	if err == nil {
		_, err = sessionsock.LoadOrCreateSecret(cfg.SessionAuthFile)
	}
	finish(err)
	if err != nil {
		return 1
	}
	finish = ui.progress("Configuring " + target)
	var removedLegacyHooks bool
	switch target {
	case "claude-code":
		err = sandbox.Configure(cfg.ClaudeSettingsPath, claudeOptions)
	case "codex":
		removedLegacyHooks, err = configureCodex(cfg)
	case "claude-desktop":
		err = configureClaudeDesktop(cfg, claudeOptions)
	default:
		err = configureCodexDesktop(cfg)
	}
	finish(err)
	if err != nil {
		return 1
	}
	if removedLegacyHooks {
		ui.row("ok", "Codex hooks", "Removed the entries an earlier release wrote to "+cfg.CodexHooksPath+"; keydris codex now passes them at launch")
	}
	if path, pathErr := agentSkillPath(cfg, target); pathErr != nil {
		ui.row("warning", "Agent skill", pathErr.Error())
	} else if err := installAgentSkill(path); err != nil {
		ui.row("warning", "Agent skill", err.Error()+"; keydris skill remains available")
	} else {
		ui.row("ok", "Agent skill", path)
	}
	finish = ui.progress("Reading policy scope")
	var scopeOutput bytes.Buffer
	origins, detected := detectPolicyScope(cfg, agentID, &scopeOutput)
	if !detected {
		finish(fmt.Errorf("could not load the assigned policy scope"))
		ui.row("warning", "Setup incomplete", scopeOutput.String())
		ui.next("keydris init " + target)
		return 1
	}
	finish(nil)
	ui.row("ok", "Policy scope", pluralOrigins(len(origins))+" cached; refreshed at session start")
	if scopeOutput.Len() > 0 {
		ui.row("warning", "Policy scope", scopeOutput.String())
	}
	if target == "claude-desktop" {
		ui.row("working", "System trust", "Enter your administrator password to update the System keychain")
		err = sandbox.InstallSystemTrustStore(cfg.CAPath)
		if err != nil {
			ui.row("error", "System trust", err.Error())
			ui.row("warning", "Setup incomplete", "System keychain trust is required for Claude Desktop")
			return 1
		}
		ui.row("ok", "System trust", "CA installed in the System keychain")
	}
	if *trust {
		finish = ui.progress("Installing OS trust")
		err = sandbox.InstallTrustStore(cfg.CAPath)
		finish(err)
		if err != nil {
			ui.row("warning", "Setup incomplete", "OS trust was requested but could not be installed")
			return 1
		}
	}
	if !*noStart {
		if code := runProxyUp(); code != 0 {
			ui.row("error", "Setup incomplete", "Proxy did not become ready")
			return code
		}
	}
	switch target {
	case "claude-code":
		verified, err := sandbox.Verify(cfg.ClaudeSettingsPath, cfg.HTTPProxyPort, claudeOptions)
		if err != nil || (!verified.OK() && *strict) {
			ui.row("error", "Verification", "Claude configuration needs attention")
			return 1
		}
		if !*strict {
			ui.row("warning", "Sandbox", "Non-strict mode permits unsandboxed commands")
		}
	case "codex":
		finish = ui.progress("Checking the Keydris hooks in Codex")
		_, err := governedCodexOverrides(codexExecutable, codexOptions)
		finish(err)
		if err != nil {
			ui.row("error", "Verification", "Codex hooks need attention: "+err.Error())
			return 1
		}
	case "claude-desktop":
		present, err := sandbox.HasKeydrisHooks(desktopSettingsPath(cfg))
		if err != nil || !present {
			ui.row("error", "Verification", "Claude Desktop hooks need attention")
			return 1
		}
	default:
		finish = ui.progress("Checking the Keydris hooks in the Codex app")
		err := verifyCodexDesktop(codexOptions)
		finish(err)
		if err != nil {
			ui.row("error", "Verification", "Codex Desktop hooks need attention: "+err.Error())
			return 1
		}
	}
	ui.row("ok", "Setup", "Configuration verified")
	if *noStart {
		ui.next("keydris proxy up")
	} else if target == "claude-code" {
		ui.next("keydris run -- claude")
	} else if target == "claude-desktop" {
		ui.next("keydris claude-desktop")
	} else if target == "codex-desktop" {
		ui.next("keydris codex-desktop")
	} else {
		ui.next("keydris codex")
	}
	return 0
}

func promptInit() ([]string, bool) {
	if !terminalFile(os.Stdin) {
		return nil, false
	}
	printInitBanner(os.Stdout)
	reader := bufio.NewReader(os.Stdin)
	fmt.Fprintln(os.Stdout, "Choose an integration:\n  1) Claude Code\n  2) OpenAI Codex\n  3) Claude Desktop\n  4) Codex Desktop")
	target := ""
	for target == "" {
		fmt.Fprint(os.Stdout, "Selection [1-4]: ")
		input, err := reader.ReadString('\n')
		if err != nil {
			return nil, false
		}
		switch strings.TrimSpace(input) {
		case "1", "claude", "claude-code":
			target = "claude-code"
		case "2", "codex", "openai":
			target = "codex"
		case "3", "claude-desktop", "desktop":
			target = "claude-desktop"
		case "4", "codex-desktop":
			target = "codex-desktop"
		default:
			newUI(os.Stdout).row("warning", "Selection", "Enter 1, 2, 3, or 4 (Ctrl+C to cancel)")
		}
	}
	existing := config.Load().AgentID
	for {
		prompt := "Agent UUID from your dashboard"
		if existing != "" {
			prompt += " [" + existing + "]"
		}
		fmt.Fprint(os.Stdout, prompt+": ")
		input, err := reader.ReadString('\n')
		if err != nil {
			return nil, false
		}
		agent := strings.TrimSpace(input)
		if agent == "" {
			agent = existing
		}
		if agentUUID.MatchString(agent) {
			return []string{target, agent}, true
		}
		newUI(os.Stdout).row("warning", "Agent", "Enter a UUID such as 12345678-1234-1234-1234-123456789abc")
	}
}
func printInitBanner(w io.Writer) {
	u := newUI(w)
	// Keep redirected output compact; forced color also enables banner previews.
	if u.terminal || colorMode == "always" {
		code := "38;2;248;247;244"
		if os.Getenv("KEYDRIS_LOGO_COLOR") == "default" || lightTerminalBackground() {
			code = "39"
		}
		for _, line := range strings.Split(strings.Trim(asciiLogo, "\n"), "\n") {
			fmt.Fprintln(w, u.style(code, line))
		}
	}
	u.title("Keydris · Authority before action")
}

const asciiLogo = `
 _  __               _      _
| |/ /___ _   _  __| |_ __(_)___
| ' // _ \ | | |/ _' | '__| / __|
| . \  __/ |_| | (_| | |  | \__ \
|_|\_\___|\__, |\__,_|_|  |_|___/
          |___/
`

func lightTerminalBackground() bool {
	colors := strings.Split(os.Getenv("COLORFGBG"), ";")
	background := colors[len(colors)-1]
	return background == "7" || background == "15"
}
func claudeHookOptions(cfg *config.Config, strict bool) (sandbox.Options, error) {
	executable, err := hookExecutable()
	if err != nil {
		return sandbox.Options{}, err
	}
	return sandbox.Options{
		HTTPProxyPort: cfg.HTTPProxyPort, AllowedDomains: cfg.AllowedDomains, CAPath: cfg.CABundlePath, Strict: strict,
		SessionStartHook: executable + " __session-start", SessionEndHook: executable + " __session-end", PreToolUseHook: executable + " __pretool-use",
	}, nil
}
