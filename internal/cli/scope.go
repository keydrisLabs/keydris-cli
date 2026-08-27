package cli

import (
	"fmt"
	"io"

	"github.com/keydrisLabs/keydris-cli/internal/config"
	"github.com/keydrisLabs/keydris-cli/internal/node/sandbox"
	"github.com/keydrisLabs/keydris-cli/internal/runtimecontract"
)

// detectPolicyScope persists the origins the agent's policy governs. Routes
// need a KIT, so it mints a throwaway session and always revokes it; a fresh
// handle keeps it independent of any running session.
//
// Best-effort: failures warn on w and return ok=false so init still finishes.
// ok is separate from len(origins), since an empty policy is a valid answer.
func detectPolicyScope(cfg *config.Config, agentID string, w io.Writer) ([]string, bool) {
	warn := func(err error) ([]string, bool) {
		fmt.Fprintf(w, "  WARNING: could not detect policy scope: %v\n", err)
		fmt.Fprintf(w, "           scope will be detected on the first session\n")
		return nil, false
	}

	inst, err := mintSessionInstance(cfg, agentID, newProxyToken())
	if err != nil {
		return warn(fmt.Errorf("open a runtime session: %w", err))
	}
	defer func() {
		if revokeErr := revokeSessionInstance(cfg, inst.SessionID); revokeErr != nil {
			fmt.Fprintf(w, "  WARNING: could not revoke the scope-detection session %s: %v\n",
				inst.SessionID, revokeErr)
		}
	}()

	routes, err := fetchSessionRoutes(cfg, inst.KIT)
	if err != nil {
		return warn(fmt.Errorf("fetch runtime routes: %w", err))
	}
	if routes.Agent.AgentID != agentID {
		return warn(fmt.Errorf(
			"runtime routes are for agent %s, not %s", routes.Agent.AgentID, agentID))
	}

	origins := routes.ManagedOrigins()
	if err := config.SaveDerivedManagedScope(cfg.DataDir, origins); err != nil {
		return warn(fmt.Errorf("save proxy scope: %w", err))
	}
	return origins, true
}

// printPolicyScope renders the scope in init's summary. detected separates
// "the policy governs nothing" from "we could not ask".
func printPolicyScope(w io.Writer, origins []string, detected bool) {
	if !detected {
		fmt.Fprintf(w, "  policy scope: not detected\n")
		return
	}
	if len(origins) == 0 {
		fmt.Fprintf(w, "  policy scope: no governed origins (this agent's policy grants no integrations)\n")
		return
	}
	fmt.Fprintf(w, "  policy scope: %s\n", pluralOrigins(len(origins)))
	for _, origin := range origins {
		fmt.Fprintf(w, "    %s\n", origin)
	}
}

// refreshPolicyScope updates the scope from already-fetched routes. Never
// fails the session: per-session routes govern it regardless of this cache.
func refreshPolicyScope(cfg *config.Config, routes *runtimecontract.RuntimeRoutes, w io.Writer) {
	if routes == nil {
		return
	}
	if err := config.SaveDerivedManagedScope(cfg.DataDir, routes.ManagedOrigins()); err != nil {
		fmt.Fprintf(w, "keydris session: could not refresh the proxy scope cache: %v\n", err)
	}
}

// refreshMcpServers rewrites Claude Code's MCP server list from the session's
// governed routes, so a policy change is reflected without re-running init.
// Best-effort: a session must still start if the config cannot be written.
func refreshMcpServers(
	cfg *config.Config,
	routes *runtimecontract.RuntimeRoutes,
	w io.Writer,
) {
	if routes == nil {
		return
	}
	endpoints := routes.McpServerEndpoints()
	servers := make([]sandbox.McpServer, 0, len(endpoints))
	for _, endpoint := range endpoints {
		servers = append(servers, sandbox.McpServer{
			Name: endpoint.Name,
			URL:  endpoint.URL,
		})
	}
	if err := sandbox.ConfigureMcpServers(
		cfg.ClaudeMcpConfigPath,
		servers,
	); err != nil {
		fmt.Fprintf(w, "keydris session: could not write MCP servers: %v\n", err)
	}
}

func pluralOrigins(n int) string {
	if n == 1 {
		return "1 governed origin"
	}
	return fmt.Sprintf("%d governed origins", n)
}
