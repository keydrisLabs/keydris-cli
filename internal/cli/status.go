package cli

import (
	"fmt"
	"net/http"
	"time"

	"github.com/keydrisLabs/keydris-cli/internal/config"
	"github.com/keydrisLabs/keydris-cli/internal/node/login"
	"github.com/keydrisLabs/keydris-cli/internal/node/sandbox"
)

func runStatus() int {
	cfg := config.Load()

	fmt.Printf("trust domain: %s\n", cfg.TrustDomain)
	fmt.Printf("dataplane:    %s\n", cfg.DataPlane)
	policy := cfg.PolicyID
	if policy == "" {
		policy = "(none — run `keydris init claude-code <policy-id>`)"
	}
	fmt.Printf("policy id:    %s\n", policy)
	fmt.Printf("blueprint:    %s\n", cfg.ResolveBlueprint(""))
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

	// Use the public /agent/jwks endpoint as the reachability/health signal: the
	// new control-plane API does not expose /healthz.
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

// reportIdentity surfaces whether the user has logged in (`keydris login`) and
// whether the stored client certificate is still valid.
func reportIdentity(cfg *config.Config) {
	id, err := login.Load(cfg.IdentityDir)
	if err != nil {
		fmt.Printf("identity:     NOT LOGGED IN (run `keydris login`)\n")
		return
	}
	state := "valid"
	if id.Expired() {
		state = "EXPIRED (run `keydris login`)"
	}
	fmt.Printf("identity:     %s [%s, cert until %s]\n", id.Email, state, id.NotAfter)
}

// reportSandbox surfaces Claude Code sandbox enforcement drift: enforcement only
// holds while the sandbox is enabled and routed to the Keydris proxy port
// (plan_v1.md section 7).
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
}
