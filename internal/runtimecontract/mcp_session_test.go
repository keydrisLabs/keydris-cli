package runtimecontract

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func sessionRequest(method string, id json.RawMessage) MCPSessionRequest {
	return MCPSessionRequest{
		SchemaVersion: SchemaVersion,
		RequestID:     "req-1",
		ConnectionID:  "66666666-6666-4666-8666-666666666666",
		Message: MCPSessionMessage{
			JSONRPC: "2.0",
			ID:      id,
			Method:  method,
			Params:  json.RawMessage(`{}`),
		},
	}
}

func sessionServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.URL.Path != "/v1/runtime/mcp/session" {
			t.Errorf("path = %s", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer session-kit" {
			t.Errorf("missing KIT bearer")
		}
		writer.Header().Set("content-type", "application/json")
		_, _ = writer.Write([]byte(body))
	}))
}

func TestRelayMCPSessionReturnsTheUpstreamResponse(t *testing.T) {
	server := sessionServer(t, `{
		"schema_version": 1,
		"request_id": "req-1",
		"status": "succeeded",
		"error_code": null,
		"mcp_response": {
			"jsonrpc": "2.0",
			"id": 7,
			"result": {"tools": [{"name": "get_me"}]}
		}
	}`)
	defer server.Close()

	result, err := RelayMCPSession(
		context.Background(),
		server.Client(),
		server.URL,
		"session-kit",
		"/v1/runtime/mcp/session",
		sessionRequest("tools/list", json.RawMessage("7")),
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "succeeded" || result.MCPResponse == nil {
		t.Fatalf("result = %+v", result)
	}
	if string(result.MCPResponse.Result) != `{"tools": [{"name": "get_me"}]}` {
		t.Fatalf("result payload = %s", result.MCPResponse.Result)
	}
}

func TestRelayMCPSessionAcceptsANotification(t *testing.T) {
	server := sessionServer(t, `{
		"schema_version": 1,
		"request_id": "req-1",
		"status": "accepted",
		"error_code": null,
		"mcp_response": null
	}`)
	defer server.Close()

	result, err := RelayMCPSession(
		context.Background(),
		server.Client(),
		server.URL,
		"session-kit",
		"/v1/runtime/mcp/session",
		sessionRequest("notifications/initialized", nil),
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "accepted" || result.MCPResponse != nil {
		t.Fatalf("result = %+v", result)
	}
}

// Otherwise a malformed relay would read as a working one.
func TestRelayMCPSessionRejectsInconsistentResponses(t *testing.T) {
	for name, body := range map[string]string{
		"succeeded without a response": `{
			"schema_version": 1, "request_id": "req-1",
			"status": "succeeded", "error_code": null, "mcp_response": null
		}`,
		"failed without an error code": `{
			"schema_version": 1, "request_id": "req-1",
			"status": "failed", "error_code": null, "mcp_response": null
		}`,
		"accepted with a response": `{
			"schema_version": 1, "request_id": "req-1",
			"status": "accepted", "error_code": null,
			"mcp_response": {"jsonrpc": "2.0", "id": 7, "result": {}}
		}`,
		"mismatched request id": `{
			"schema_version": 1, "request_id": "req-other",
			"status": "accepted", "error_code": null, "mcp_response": null
		}`,
		"unknown status": `{
			"schema_version": 1, "request_id": "req-1",
			"status": "queued", "error_code": null, "mcp_response": null
		}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := sessionServer(t, body)
			defer server.Close()

			if _, err := RelayMCPSession(
				context.Background(),
				server.Client(),
				server.URL,
				"session-kit",
				"/v1/runtime/mcp/session",
				sessionRequest("tools/list", json.RawMessage("7")),
			); err == nil {
				t.Fatal("expected the relay to reject the response")
			}
		})
	}
}

// A response carrying a different id would match the wrong client request.
func TestRelayMCPSessionRejectsAMismatchedJSONRPCID(t *testing.T) {
	server := sessionServer(t, `{
		"schema_version": 1,
		"request_id": "req-1",
		"status": "succeeded",
		"error_code": null,
		"mcp_response": {"jsonrpc": "2.0", "id": 99, "result": {}}
	}`)
	defer server.Close()

	if _, err := RelayMCPSession(
		context.Background(),
		server.Client(),
		server.URL,
		"session-kit",
		"/v1/runtime/mcp/session",
		sessionRequest("tools/list", json.RawMessage("7")),
	); err == nil {
		t.Fatal("expected the relay to reject a mismatched JSON-RPC id")
	}
}

func TestIsMCPSessionMethod(t *testing.T) {
	for _, method := range []string{
		"initialize", "server/discover", "notifications/initialized",
		"ping", "tools/list", "resources/list", "prompts/list",
	} {
		if !IsMCPSessionMethod(method) {
			t.Errorf("IsMCPSessionMethod(%q) = false", method)
		}
	}
	// Actions stay governed; anything unlisted must not reach the relay.
	for _, method := range []string{
		"tools/call", "resources/read", "prompts/get", "", "unknown/method",
	} {
		if IsMCPSessionMethod(method) {
			t.Errorf("IsMCPSessionMethod(%q) = true", method)
		}
	}
}
