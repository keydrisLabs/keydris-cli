package runtimecontract

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

//go:embed bundle/v1/fixtures/mcp-gateway-request.json
var canonicalMCPGatewayRequest string

//go:embed bundle/v1/fixtures/mcp-gateway-response.json
var canonicalMCPGatewayResponse string

//go:embed bundle/v1/schemas/mcp-gateway-request.schema.json
var canonicalMCPGatewayRequestSchema string

//go:embed bundle/v1/schemas/mcp-gateway-response.schema.json
var canonicalMCPGatewayResponseSchema string

func TestVendoredMCPGatewayArtifactsChecksums(t *testing.T) {
	tests := []struct {
		name     string
		contents string
		want     string
	}{
		{
			name:     "request fixture",
			contents: canonicalMCPGatewayRequest,
			want:     "b31cab43a1683b2b56d3c602a45edf5bc298aa8af0a5c03daf7f508d7d61162e",
		},
		{
			name:     "response fixture",
			contents: canonicalMCPGatewayResponse,
			want:     "ca494f356f717a3b9feba232f22e33f2b43fa96a110a326fe83075ae9ecb46be",
		},
		{
			name:     "request schema",
			contents: canonicalMCPGatewayRequestSchema,
			want:     "884aa102945dec4dfa3148fe5ff804332aa16f4b392418700e3637a08ec4f586",
		},
		{
			name:     "response schema",
			contents: canonicalMCPGatewayResponseSchema,
			want:     "fb20e7ca1b6d5f8b644ab4811945b07e7c56a2f637a144ace85566fed7544ef2",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := fmt.Sprintf("%x", sha256.Sum256([]byte(test.contents)))
			if got != test.want {
				t.Fatalf("checksum = %s, want %s", got, test.want)
			}
		})
	}
}

func TestExecuteMCPGatewayUsesSessionIdentityAndPreservesJSONRPCIdentity(
	t *testing.T,
) {
	var captured MCPGatewayRequest
	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		r *http.Request,
	) {
		if r.URL.Path != "/v1/runtime/mcp/gateway" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer runtime-session-token" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("decode MCP gateway request: %v", err)
		}
		_, _ = fmt.Fprintf(
			w,
			`{"schema_version":1,"request_id":%q,"decision":{"schema_version":1,"decision_id":"Keydris-01K1X4Y5Z6A7B8C9D0E1F2G3H4","request_id":%q,"attempt_id":"01K1X4Y5Z6A7B8C9D0E1F2G3H5","correlation_id":"01K1X4Y5Z6A7B8C9D0E1F2G3H6","decided_at":"2026-07-30T12:00:00Z","decision":"allow","reason_code":"keydris_policy_allowed","obligations":[]},"execution_status":"succeeded","replayed":false,"error_code":null,"mcp_response":{"jsonrpc":"2.0","id":"rpc-7","result":{"content":[{"type":"text","text":"ok"}]}}}`,
			captured.RequestID,
			captured.RequestID,
		)
	}))
	defer server.Close()

	input := mcpGatewayTestRequest()
	result, err := ExecuteMCPGateway(
		context.Background(),
		server.Client(),
		server.URL,
		"runtime-session-token",
		"/v1/runtime/mcp/gateway",
		input,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExecutionStatus != "succeeded" ||
		result.MCPResponse == nil ||
		string(result.MCPResponse.ID) != `"rpc-7"` {
		t.Fatalf("MCP gateway result = %+v", result)
	}
	if captured.Message.Method != "tools/call" ||
		captured.Message.Params.Name != "get_stats" ||
		captured.Message.Params.Arguments["campus"] != "north" {
		t.Fatalf("captured MCP message = %+v", captured.Message)
	}
}

func TestExecuteMCPGatewayRejectsInconsistentResponses(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "mismatched JSON-RPC identity",
			body: `{"schema_version":1,"request_id":"mcp-001","decision":{"schema_version":1,"decision_id":"Keydris-01K1X4Y5Z6A7B8C9D0E1F2G3H4","request_id":"mcp-001","attempt_id":"01K1X4Y5Z6A7B8C9D0E1F2G3H5","correlation_id":"01K1X4Y5Z6A7B8C9D0E1F2G3H6","decided_at":"2026-07-30T12:00:00Z","decision":"allow","reason_code":"keydris_policy_allowed","obligations":[]},"execution_status":"succeeded","replayed":false,"error_code":null,"mcp_response":{"jsonrpc":"2.0","id":"different","result":{}}}`,
		},
		{
			name: "unknown outcome with response",
			body: `{"schema_version":1,"request_id":"mcp-001","decision":{"schema_version":1,"decision_id":"Keydris-01K1X4Y5Z6A7B8C9D0E1F2G3H4","request_id":"mcp-001","attempt_id":"01K1X4Y5Z6A7B8C9D0E1F2G3H5","correlation_id":"01K1X4Y5Z6A7B8C9D0E1F2G3H6","decided_at":"2026-07-30T12:00:00Z","decision":"allow","reason_code":"keydris_policy_allowed","obligations":[]},"execution_status":"unknown","replayed":false,"error_code":"keydris_mcp_outcome_unknown","mcp_response":{"jsonrpc":"2.0","id":"rpc-7","result":{}}}`,
		},
		{
			name: "response with result and error",
			body: `{"schema_version":1,"request_id":"mcp-001","decision":{"schema_version":1,"decision_id":"Keydris-01K1X4Y5Z6A7B8C9D0E1F2G3H4","request_id":"mcp-001","attempt_id":"01K1X4Y5Z6A7B8C9D0E1F2G3H5","correlation_id":"01K1X4Y5Z6A7B8C9D0E1F2G3H6","decided_at":"2026-07-30T12:00:00Z","decision":"allow","reason_code":"keydris_policy_allowed","obligations":[]},"execution_status":"failed","replayed":false,"error_code":null,"mcp_response":{"jsonrpc":"2.0","id":"rpc-7","result":{},"error":{"code":-32603,"message":"failed"}}}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(
				w http.ResponseWriter,
				_ *http.Request,
			) {
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()

			_, err := ExecuteMCPGateway(
				context.Background(),
				server.Client(),
				server.URL,
				"runtime-session-token",
				"/v1/runtime/mcp/gateway",
				mcpGatewayTestRequest(),
			)
			if err == nil {
				t.Fatal("invalid MCP gateway response was accepted")
			}
		})
	}

	_, err := ExecuteMCPGateway(
		context.Background(),
		http.DefaultClient,
		"https://keydris.invalid",
		"runtime-session-token",
		"https://attacker.example/gateway",
		mcpGatewayTestRequest(),
	)
	if err == nil || !strings.Contains(err.Error(), "untrusted runtime endpoint") {
		t.Fatalf("absolute endpoint error = %v", err)
	}
}

func mcpGatewayTestRequest() MCPGatewayRequest {
	return MCPGatewayRequest{
		SchemaVersion: SchemaVersion,
		RequestID:     "mcp-001",
		ConnectionID:  "77777777-7777-4777-8777-777777777777",
		ResourceID:    "88888888-8888-4888-8888-888888888888",
		Message: MCPGatewayMessage{
			JSONRPC: "2.0",
			ID:      json.RawMessage(`"rpc-7"`),
			Method:  "tools/call",
			Params: MCPGatewayParams{
				Name:      "get_stats",
				Arguments: map[string]any{"campus": "north"},
			},
		},
	}
}
