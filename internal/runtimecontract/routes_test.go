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
		"organization_id": "c3c7534e-84e4-4b6e-91af-bb7affdec1da",
		"agent": {
			"agent_id": "e2869415-d26e-4633-a244-4319a4e9bf66",
			"display_name": "Test-claude"
		},
		"policy": {
			"policy_id": "dbf02187-6e38-44d7-ba0d-ef5c48b5b3fb",
			"policy_version_id": "6556323e-334a-4572-a02b-100abe3bbf00",
			"policy_hash": "sha256:18409e98892c6533e7686c7e04e4cf8926e9b0f50a6c316b14c40ea5565df8cd"
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
		"connection_id": "e6273ae3-c222-4406-baac-02280d542315",
		"enforcement_mode": "mcp_gateway",
		"availability": "ready",
		"status_reason_code": null,
		"matchers": [%s],
		"resources": [{
			"resource_type": "mcp.server",
			"resource_id": "a04f788a-4b91-41b6-9451-e2b0c42f7515",
			"external_id": "mcp:github-whoami",
			"display_name": "github-whoami",
			"availability": "ready",
			"status_reason_code": null,
			"routing_keys": [{"key_type": "mcp.server_id", "value": "mcp:github-whoami"}]
		}],
		"runtime_endpoint_path": "/v1/runtime/mcp/gateway"
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
		"e6273ae3-c222-4406-baac-02280d542315",
		originMatcher("https", "keydris-mcp-demo.fly.dev", 443, "/mcp"),
	)))
	if err != nil {
		t.Fatal(err)
	}
	got := routes.ManagedOrigins()
	if len(got) != 1 || got[0] != "keydris-mcp-demo.fly.dev:443" {
		t.Fatalf("ManagedOrigins() = %#v, want [keydris-mcp-demo.fly.dev:443]", got)
	}
}

// Origin-only: same origin under different path prefixes collapses to one.
func TestManagedOriginsDedupesAndSorts(t *testing.T) {
	routes, err := DecodeRuntimeRoutes(routesJSON(
		mcpRoute("e6273ae3-c222-4406-baac-02280d542315",
			originMatcher("https", "zeta.example.com", 443, "/mcp"),
			originMatcher("https", "alpha.example.com", 8443, "/"),
		),
		mcpRoute("f1273ae3-c222-4406-baac-02280d542316",
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
		"e6273ae3-c222-4406-baac-02280d542315",
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
