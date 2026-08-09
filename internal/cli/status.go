package cli

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/keydrisLabs/keydris-cli/internal/config"
	"github.com/keydrisLabs/keydris-cli/internal/node/login"
	"github.com/keydrisLabs/keydris-cli/internal/node/sandbox"
)

func runStatus() int {
	cfg := config.Load()

	fmt.Printf("trust domain: %s\n", cfg.TrustDomain)
	fmt.Printf("dataplane:    %s\n", cfg.DataPlane)
	agent := cfg.AgentID
	if agent == "" {
		agent = "(none - run `keydris init`)"
	}
	fmt.Printf("agent id:     %s\n", agent)
	if cfg.PolicyID != "" {
		fmt.Printf("legacy policy: %s\n", cfg.PolicyID)
	}
	fmt.Printf("proxy port:   %d\n", cfg.ProxyPort)
	if cfg.ManagedScopeError != nil {
		fmt.Printf("proxy scope:  INVALID (%v)\n", cfg.ManagedScopeError)
	} else {
		fmt.Printf("proxy scope:  %s", cfg.ManagedMode)
		if cfg.ManagedMode == "selected" {
			fmt.Printf(" (%d destinations)", len(cfg.ManagedDestinations))
		}
		fmt.Println()
		for _, dst := range cfg.ManagedDestinations {
			fmt.Printf("  managed:    %s\n", dst)
		}
	}
	fmt.Printf("backend:      %s (dport %d)\n", cfg.BackendAddr, cfg.BackendPort)
	fmt.Printf("control url:  %s\n", cfg.ControlURL)
	fmt.Printf("session sock: %s\n", cfg.SessionSocket)
	fmt.Printf("ledger:       %s\n", cfg.LedgerPath)

	reportIdentity(cfg)
	reportSandbox(cfg)
	reportCodex(cfg)

	client := &http.Client{Timeout: 2 * time.Second}
	jresp, err := client.Get(cfg.ControlURL + "/agent/jwks")
	if err != nil {
		fmt.Printf("control:      DOWN (%v)\n", err)
		return 1
	}
	defer jresp.Body.Close()
	if jresp.StatusCode < 200 || jresp.StatusCode >= 300 {
		fmt.Printf("control:      UNHEALTHY (jwks %s)\n", jresp.Status)
		return 1
	}
	fmt.Printf("control:      UP (jwks %s)\n", jresp.Status)
	return 0
}

func reportCodex(cfg *config.Config) {
	path, err := exec.LookPath("codex")
	if err != nil {
		fmt.Println("codex:        NOT FOUND (install Codex, then launch with `keydris codex`)")
		return
	}
	fmt.Printf("codex:        READY [%s; launch with `keydris codex`]\n", path)
	hookOptions, err := codexHookOptions()
	if err != nil {
		fmt.Printf("  WARNING: cannot resolve Keydris hook executable: %v\n", err)
		return
	}
	wired, err := sandbox.VerifyCodexHooks(cfg.CodexHooksPath, hookOptions)
	if err != nil {
		fmt.Printf("  WARNING: cannot read %s: %v\n", cfg.CodexHooksPath, err)
		return
	}
	if wired {
		fmt.Printf("  command hooks: wired [%s; trust once via `/hooks` inside Codex]\n", cfg.CodexHooksPath)
	} else {
		fmt.Printf("  WARNING: command hooks not wired: shell commands bypass the policy's command rules (run `keydris init codex <agent-id>`)\n")
	}
}

func reportIdentity(cfg *config.Config) {
	id, err := login.Load(cfg.IdentityDir)
	if err != nil {
		fmt.Printf("identity:     NOT ENROLLED (run `keydris login`)\n")
		return
	}
	state := "valid"
	if id.Expired() {
		state = "EXPIRED (run `keydris login`)"
	}
	fmt.Printf(
		"identity:     %s [%s, device %s, agent %s, cert until %s]\n",
		id.Email,
		state,
		id.DeviceID,
		id.AgentID,
		id.NotAfter,
	)
}

func reportSandbox(cfg *config.Config) {
	st, err := sandbox.Verify(cfg.ClaudeSettingsPath, cfg.HTTPProxyPort)
	if err != nil {
		fmt.Printf("sandbox:      ERROR reading %s: %v\n", cfg.ClaudeSettingsPath, err)
		return
	}
	state := "DISABLED"
	if st.OK() {
		state = "OK (enabled, routed to Keydris)"
	}
	fmt.Printf("sandbox:      %s [%s]\n", state, cfg.ClaudeSettingsPath)
	for _, w := range st.Warnings {
		fmt.Printf("  WARNING: %s\n", w)
	}
	for _, path := range claudeProjectSettings() {
		absConfigured, _ := filepath.Abs(cfg.ClaudeSettingsPath)
		absCandidate, _ := filepath.Abs(path)
		if absConfigured != absCandidate {
			fmt.Printf("  WARNING: higher-priority Claude settings may change the effective sandbox: %s\n", path)
		}
	}
}

func claudeProjectSettings() []string {
	wd, err := os.Getwd()
	if err != nil {
		return nil
	}
	var found []string
	for dir := wd; ; dir = filepath.Dir(dir) {
		for _, name := range []string{"settings.json", "settings.local.json"} {
			path := filepath.Join(dir, ".claude", name)
			if _, err := os.Stat(path); err == nil {
				found = append(found, path)
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	return found
}
