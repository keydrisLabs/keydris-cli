package runtimecontract

import (
	"fmt"
	"strings"
	"testing"
)

// routesJSON wraps route bodies in a real-shaped routes response.
func routesJSON(routes ...string) []byte {
	return []byte(fmt.Sprintf(`{
		"schema_version": 1,
		"organization_id": "11111111-1111-4111-8111-111111111111",
		"agent": {
			"agent_id": "22222222-2222-4222-8222-222222222222",
			"display_name": "Test-claude"
		},
		"policy": {
			"policy_id": "33333333-3333-4333-8333-333333333333",
			"policy_version_id": "44444444-4444-4444-8444-444444444444",
			"policy_hash": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		},
		"routes": [%s]
	}`, strings.Join(routes, ",")))
}

// mcpRoute is one mcp_gateway route with the given origin matchers.
func mcpRoute(routeID string, matchers ...string) string {
	return fmt.Sprintf(`{
		"route_id": %q,
		"display_name": "github-whoami",
		"provider": "mcp",
		"connection_id": "66666666-6666-4666-8666-666666666666",
		"enforcement_mode": "mcp_gateway",
		"availability": "ready",
		"status_reason_code": null,
		"matchers": [%s],
		"resources": [{
			"resource_type": "mcp.server",
			"resource_id": "77777777-7777-4777-8777-777777777777",
			"external_id": "mcp:github-whoami",
			"display_name": "github-whoami",
			"availability": "ready",
			"status_reason_code": null,
			"routing_keys": [{"key_type": "mcp.server_id", "value": "mcp:github-whoami"}]
		}],
		"runtime_endpoint_path": "/v1/runtime/mcp/gateway",
		"session_endpoint_path": "/v1/runtime/mcp/session"
	}`, routeID, strings.Join(matchers, ","))
}

func originMatcher(scheme, host string, port int, pathPrefix string) string {
	return fmt.Sprintf(
		`{"matcher_type":"http.origin","attributes":{"scheme":%q,"host":%q,"port":%d,"path_prefix":%q}}`,
		scheme, host, port, pathPrefix)
}

// The shape the live control plane returns.
func TestManagedOriginsMatchesLiveShape(t *testing.T) {
	routes, err := DecodeRuntimeRoutes(routesJSON(mcpRoute(
		"55555555-5555-4555-8555-555555555555",
		originMatcher("https", "mcp.example.com", 443, "/mcp"),
	)))
	if err != nil {
		t.Fatal(err)
	}
	got := routes.ManagedOrigins()
	if len(got) != 1 || got[0] != "mcp.example.com:443" {
		t.Fatalf("ManagedOrigins() = %#v, want [mcp.example.com:443]", got)
	}
}

// Origin-only: same origin under different path prefixes collapses to one.
func TestManagedOriginsDedupesAndSorts(t *testing.T) {
	routes, err := DecodeRuntimeRoutes(routesJSON(
		mcpRoute("55555555-5555-4555-8555-555555555555",
			originMatcher("https", "zeta.example.com", 443, "/mcp"),
			originMatcher("https", "alpha.example.com", 8443, "/"),
		),
		mcpRoute("88888888-8888-4888-8888-888888888888",
			originMatcher("https", "zeta.example.com", 443, "/other"),
		),
	))
	if err != nil {
		t.Fatal(err)
	}
	got := routes.ManagedOrigins()
	want := []string{"alpha.example.com:8443", "zeta.example.com:443"}
	if len(got) != len(want) {
		t.Fatalf("ManagedOrigins() = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ManagedOrigins() = %#v, want %#v", got, want)
		}
	}
}

// The result is persisted verbatim, so it must be canonical.
func TestManagedOriginsCanonicalizes(t *testing.T) {
	routes, err := DecodeRuntimeRoutes(routesJSON(mcpRoute(
		"55555555-5555-4555-8555-555555555555",
		originMatcher("https", "API.Example.COM", 8443, "/"),
	)))
	if err != nil {
		t.Fatal(err)
	}
	got := routes.ManagedOrigins()
	if len(got) != 1 || got[0] != "api.example.com:8443" {
		t.Fatalf("ManagedOrigins() = %#v, want [api.example.com:8443]", got)
	}
}

func TestManagedOriginsEmptyWhenNoRoutes(t *testing.T) {
	routes, err := DecodeRuntimeRoutes(routesJSON())
	if err != nil {
		t.Fatal(err)
	}
	if got := routes.ManagedOrigins(); len(got) != 0 {
		t.Fatalf("ManagedOrigins() = %#v, want empty", got)
	}
}

func TestMcpServerEndpointsRebuildsTheDialURL(t *testing.T) {
	routes, err := DecodeRuntimeRoutes(routesJSON(mcpRoute(
		"55555555-5555-4555-8555-555555555555",
		originMatcher("https", "api.githubcopilot.com", 443, "/mcp/"),
	)))
	if err != nil {
		t.Fatal(err)
	}

	got := routes.McpServerEndpoints()
	if len(got) != 1 {
		t.Fatalf("McpServerEndpoints() = %#v", got)
	}
	// The default port is omitted so the URL matches what a user would write.
	if got[0].URL != "https://api.githubcopilot.com/mcp/" {
		t.Fatalf("URL = %q", got[0].URL)
	}
	if got[0].Name != "github-whoami" {
		t.Fatalf("Name = %q, want the slugified display name", got[0].Name)
	}
}

func TestMcpServerEndpointsKeepsNonDefaultPort(t *testing.T) {
	routes, err := DecodeRuntimeRoutes(routesJSON(mcpRoute(
		"55555555-5555-4555-8555-555555555555",
		originMatcher("http", "127.0.0.1", 8931, "/mcp"),
	)))
	if err != nil {
		t.Fatal(err)
	}

	got := routes.McpServerEndpoints()
	if len(got) != 1 || got[0].URL != "http://127.0.0.1:8931/mcp" {
		t.Fatalf("McpServerEndpoints() = %#v", got)
	}
}

// Rejected rather than accepted empty: an auth-requiring route would otherwise
// fall back to passing bare requests upstream.
func TestDecodeRejectsMCPGatewayWithoutASessionPath(t *testing.T) {
	route := strings.Replace(
		mcpRoute("99999999-9999-4999-8999-999999999999",
			originMatcher("https", "api.githubcopilot.com", 443, "/mcp")),
		`,
		"session_endpoint_path": "/v1/runtime/mcp/session"`,
		"",
		1,
	)
	if _, err := DecodeRuntimeRoutes(routesJSON(route)); err == nil {
		t.Fatal("expected a route with no session endpoint path to be rejected")
	}
}

// Defaults to false so a credential-less server keeps passing through.
func TestDecodeCarriesRequiresUpstreamAuth(t *testing.T) {
	route := mcpRoute("99999999-9999-4999-8999-999999999999",
		originMatcher("https", "api.githubcopilot.com", 443, "/mcp"))
	decoded, err := DecodeRuntimeRoutes(routesJSON(route))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Routes[0].RequiresUpstreamAuth {
		t.Fatal("RequiresUpstreamAuth = true, want false when absent")
	}

	flagged := strings.Replace(
		route,
		`"runtime_endpoint_path"`,
		`"requires_upstream_auth": true, "runtime_endpoint_path"`,
		1,
	)
	decoded, err = DecodeRuntimeRoutes(routesJSON(flagged))
	if err != nil {
		t.Fatal(err)
	}
	if !decoded.Routes[0].RequiresUpstreamAuth {
		t.Fatal("RequiresUpstreamAuth = false, want true")
	}
	if decoded.Routes[0].SessionEndpointPath != "/v1/runtime/mcp/session" {
		t.Fatalf("SessionEndpointPath = %q", decoded.Routes[0].SessionEndpointPath)
	}
}
