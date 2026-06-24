package cli

import (
	"fmt"
	"net/http"
	"time"

	"github.com/nocaplabs/keydris-cli/internal/config"
	"github.com/nocaplabs/keydris-cli/internal/node/login"
	"github.com/nocaplabs/keydris-cli/internal/node/sandbox"
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
	fmt.Printf("backend:      %s (dport %d)\n", cfg.BackendAddr, cfg.BackendPort)
	fmt.Printf("control url:  %s\n", cfg.ControlURL)
	fmt.Printf("session sock: %s\n", cfg.SessionSocket)
	fmt.Printf("ledger:       %s\n", cfg.LedgerPath)

	reportIdentity(cfg)
	reportSandbox(cfg)

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(cfg.ControlURL + "/healthz")
	if err != nil {
		fmt.Printf("control:      DOWN (%v)\n", err)
		return 1
	}
	defer resp.Body.Close()
	fmt.Printf("control:      UP (%s)\n", resp.Status)

	jresp, err := client.Get(cfg.ControlURL + "/jwks")
	if err == nil {
		defer jresp.Body.Close()
		fmt.Printf("jwks:         %s\n", jresp.Status)
	}
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
