package cli

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/keydrisLabs/keydris-cli/internal/config"
	"github.com/keydrisLabs/keydris-cli/internal/runtimecontract"
)

const scopeTestAgentID = "e2869415-d26e-4633-a244-4319a4e9bf66"

// decodedTestRoutes goes through decode, the only path that parses origins.
func decodedTestRoutes(t *testing.T, agentID string, hosts ...string) *runtimecontract.RuntimeRoutes {
	t.Helper()
	routes := make([]string, 0, len(hosts))
	for i, host := range hosts {
		routes = append(routes, fmt.Sprintf(`{
			"route_id": "e6273ae3-c222-4406-baac-02280d54231%d",
			"display_name": "route",
			"provider": "mcp",
			"connection_id": "e6273ae3-c222-4406-baac-02280d542315",
			"enforcement_mode": "mcp_gateway",
			"availability": "ready",
			"status_reason_code": null,
			"matchers": [{"matcher_type":"http.origin","attributes":{"scheme":"https","host":%q,"port":443,"path_prefix":"/mcp"}}],
			"resources": [],
			"runtime_endpoint_path": "/v1/runtime/mcp/gateway",
			"session_endpoint_path": "/v1/runtime/mcp/session"
		}`, i, host))
	}
	body := fmt.Sprintf(`{
		"schema_version": 1,
		"organization_id": "c3c7534e-84e4-4b6e-91af-bb7affdec1da",
		"agent": {"agent_id": %q, "display_name": "Test-claude"},
		"policy": null,
		"routes": [%s]
	}`, agentID, strings.Join(routes, ","))

	decoded, err := runtimecontract.DecodeRuntimeRoutes([]byte(body))
	if err != nil {
		t.Fatalf("decode test routes: %v", err)
	}
	return decoded
}

// stubScopeSeams swaps the control-plane seams and counts mints/revokes.
func stubScopeSeams(
	t *testing.T,
	mint func(*config.Config, string, string) (*mintedInstance, error),
	routes func(*config.Config, string) (*runtimecontract.RuntimeRoutes, error),
) (mints, revokes *int) {
	t.Helper()
	mintCount, revokeCount := 0, 0
	oldMint, oldRevoke, oldRoutes := mintSessionInstance, revokeSessionInstance, fetchSessionRoutes
	mintSessionInstance = func(cfg *config.Config, agentID, handle string) (*mintedInstance, error) {
		mintCount++
		return mint(cfg, agentID, handle)
	}
	revokeSessionInstance = func(*config.Config, string) error {
		revokeCount++
		return nil
	}
	fetchSessionRoutes = routes
	t.Cleanup(func() {
		mintSessionInstance, revokeSessionInstance, fetchSessionRoutes = oldMint, oldRevoke, oldRoutes
	})
	return &mintCount, &revokeCount
}

func okMint(*config.Config, string, string) (*mintedInstance, error) {
	return &mintedInstance{
		SPIFFEID:  "spiffe://keydris.test/scope",
		KIT:       "test-kit",
		SessionID: "01KZZDGGTXPBHAZE6EXH02ANMR",
	}, nil
}

func TestDetectPolicyScopePersistsOrigins(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{DataDir: dir}
	mints, revokes := stubScopeSeams(t, okMint,
		func(*config.Config, string) (*runtimecontract.RuntimeRoutes, error) {
			return decodedTestRoutes(t, scopeTestAgentID, "keydris-mcp-demo.fly.dev"), nil
		})

	var out bytes.Buffer
	origins, ok := detectPolicyScope(cfg, scopeTestAgentID, &out)
	if !ok {
		t.Fatalf("detectPolicyScope failed: %s", out.String())
	}
	if len(origins) != 1 || origins[0] != "keydris-mcp-demo.fly.dev:443" {
		t.Fatalf("origins = %#v", origins)
	}
	if *mints != 1 || *revokes != 1 {
		t.Fatalf("mints=%d revokes=%d, want one each", *mints, *revokes)
	}

	state, err := config.ReadManagedScope(dir)
	if err != nil {
		t.Fatal(err)
	}
	if state.Source != config.ManagedScopeSourcePolicy {
		t.Fatalf("Source = %q", state.Source)
	}
	if len(state.Destinations) != 1 || state.Destinations[0] != "keydris-mcp-demo.fly.dev:443" {
		t.Fatalf("Destinations = %#v", state.Destinations)
	}
}

// A failed routes call must still revoke, or init leaks a live session.
func TestDetectPolicyScopeRevokesAfterFetchFailure(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{DataDir: dir}
	mints, revokes := stubScopeSeams(t, okMint,
		func(*config.Config, string) (*runtimecontract.RuntimeRoutes, error) {
			return nil, fmt.Errorf("runtime routes returned 503 Service Unavailable")
		})

	var out bytes.Buffer
	origins, ok := detectPolicyScope(cfg, scopeTestAgentID, &out)
	if ok || origins != nil {
		t.Fatalf("expected failure, got origins=%#v ok=%v", origins, ok)
	}
	if *mints != 1 || *revokes != 1 {
		t.Fatalf("mints=%d revokes=%d, want one each", *mints, *revokes)
	}
	if !strings.Contains(out.String(), "could not detect policy scope") {
		t.Fatalf("missing warning: %q", out.String())
	}
	// Nothing written, so the daemon keeps its previous behavior.
	if state, err := config.ReadManagedScope(dir); err != nil || state.Mode != "all" {
		t.Fatalf("state = %+v, err = %v; want the untouched default", state, err)
	}
}

// A mint failure warns without aborting init or revoking a phantom session.
func TestDetectPolicyScopeMintFailureWarnsOnly(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{DataDir: dir}
	mints, revokes := stubScopeSeams(t,
		func(*config.Config, string, string) (*mintedInstance, error) {
			return nil, fmt.Errorf("control plane unreachable")
		},
		func(*config.Config, string) (*runtimecontract.RuntimeRoutes, error) {
			t.Fatal("routes must not be fetched without a session")
			return nil, nil
		})

	var out bytes.Buffer
	if _, ok := detectPolicyScope(cfg, scopeTestAgentID, &out); ok {
		t.Fatal("expected failure")
	}
	if *mints != 1 || *revokes != 0 {
		t.Fatalf("mints=%d revokes=%d, want 1 and 0", *mints, *revokes)
	}
	if !strings.Contains(out.String(), "control plane unreachable") {
		t.Fatalf("missing cause: %q", out.String())
	}
}

// Routes for a different agent must never become this agent's scope.
func TestDetectPolicyScopeRejectsAgentMismatch(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{DataDir: dir}
	_, revokes := stubScopeSeams(t, okMint,
		func(*config.Config, string) (*runtimecontract.RuntimeRoutes, error) {
			return decodedTestRoutes(t,
				"11111111-1111-4111-8111-111111111111", "other.example.com"), nil
		})

	var out bytes.Buffer
	if _, ok := detectPolicyScope(cfg, scopeTestAgentID, &out); ok {
		t.Fatal("expected an agent mismatch to fail")
	}
	if *revokes != 1 {
		t.Fatalf("revokes = %d, want 1", *revokes)
	}
	if state, err := config.ReadManagedScope(dir); err != nil || len(state.Destinations) != 0 {
		t.Fatalf("state = %+v, err = %v; want nothing persisted", state, err)
	}
}

// An empty policy is a valid answer: persist it as manage-nothing.
func TestDetectPolicyScopeEmptyPolicy(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{DataDir: dir}
	stubScopeSeams(t, okMint,
		func(*config.Config, string) (*runtimecontract.RuntimeRoutes, error) {
			return decodedTestRoutes(t, scopeTestAgentID), nil
		})

	var out bytes.Buffer
	origins, ok := detectPolicyScope(cfg, scopeTestAgentID, &out)
	if !ok {
		t.Fatalf("detectPolicyScope failed: %s", out.String())
	}
	if len(origins) != 0 {
		t.Fatalf("origins = %#v, want none", origins)
	}
	state, err := config.ReadManagedScope(dir)
	if err != nil {
		t.Fatal(err)
	}
	if state.Mode != "selected" || len(state.Destinations) != 0 {
		t.Fatalf("state = %+v, want selected with no destinations", state)
	}
}

// "not detected" and "policy governs nothing" must read differently.
func TestPrintPolicyScopeDistinguishesFailureFromEmpty(t *testing.T) {
	var undetected, empty bytes.Buffer
	printPolicyScope(&undetected, nil, false)
	printPolicyScope(&empty, nil, true)

	if !strings.Contains(undetected.String(), "not detected") {
		t.Fatalf("undetected = %q", undetected.String())
	}
	if !strings.Contains(empty.String(), "no governed origins") {
		t.Fatalf("empty = %q", empty.String())
	}
	if undetected.String() == empty.String() {
		t.Fatal("failure and empty policy render identically")
	}
}

func TestRefreshPolicyScopeWritesCache(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{DataDir: dir}
	var out bytes.Buffer

	refreshPolicyScope(cfg, decodedTestRoutes(t, scopeTestAgentID, "mcp.example.com"), &out)

	state, err := config.ReadManagedScope(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Destinations) != 1 || state.Destinations[0] != "mcp.example.com:443" {
		t.Fatalf("Destinations = %#v", state.Destinations)
	}
	if state.Source != config.ManagedScopeSourcePolicy {
		t.Fatalf("Source = %q", state.Source)
	}
	if out.Len() != 0 {
		t.Fatalf("unexpected output: %q", out.String())
	}
}

// A cache write failure must warn without failing the session.
func TestRefreshPolicyScopeSurvivesWriteFailure(t *testing.T) {
	cfg := &config.Config{DataDir: string([]byte{0})}
	var out bytes.Buffer
	refreshPolicyScope(cfg, decodedTestRoutes(t, scopeTestAgentID, "mcp.example.com"), &out)
	if !strings.Contains(out.String(), "could not refresh the proxy scope cache") {
		t.Fatalf("missing warning: %q", out.String())
	}
}
