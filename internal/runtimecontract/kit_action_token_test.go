package runtimecontract

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func testMCPActionIntent() MCPActionIntent {
	return MCPActionIntent{
		Provider:     "mcp",
		ConnectionID: "77777777-7777-4777-8777-777777777777",
		ActionType:   "mcp.tool.call",
		ActionName:   "get_stats",
		Resource: MCPActionResource{
			ResourceType: "mcp.tool",
			ResourceID:   "88888888-8888-4888-8888-888888888888",
			ExternalID:   "get_stats",
		},
		Parameters: map[string]any{"campus": "north", "limit": 25},
	}
}

func TestNewMintKitActionTokenRequestUsesCanonicalIntentHash(t *testing.T) {
	request, err := NewMintKitActionTokenRequest(
		"request-1",
		testMCPActionIntent(),
	)
	if err != nil {
		t.Fatal(err)
	}
	const expected = "sha256:ee2865dc14c1f4e3d4468304b411d1ec3061a4e8e207ebad5eb02173d3454e65"
	if request.SchemaVersion != SchemaVersion ||
		request.RequestHash != expected {
		t.Fatalf("request = %+v", request)
	}
}

func TestMintKitActionTokenUsesSessionBearer(t *testing.T) {
	var captured MintKitActionTokenRequest
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.URL.Path != "/v1/runtime/mcp/kit-action-tokens" {
			http.NotFound(writer, request)
			return
		}
		if request.Header.Get("Authorization") != "Bearer session-kit" {
			t.Errorf("authorization = %q", request.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(request.Body).Decode(&captured); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_, _ = fmt.Fprintf(
			writer,
			`{"schema_version":1,"kit_action_token":"action-token","expires_at":%q}`,
			time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano),
		)
	}))
	defer server.Close()

	input, err := NewMintKitActionTokenRequest("request-1", testMCPActionIntent())
	if err != nil {
		t.Fatal(err)
	}
	result, err := MintKitActionToken(
		context.Background(),
		server.Client(),
		server.URL,
		"session-kit",
		"/v1/runtime/mcp/kit-action-tokens",
		input,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.KitActionToken != "action-token" ||
		captured.RequestHash != input.RequestHash {
		t.Fatalf("result=%+v captured=%+v", result, captured)
	}
}
