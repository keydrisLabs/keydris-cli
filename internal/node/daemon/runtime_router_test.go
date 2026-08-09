package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/keydrisLabs/keydris-cli/internal/node/dataplane"
	"github.com/keydrisLabs/keydris-cli/internal/runtimecontract"
)

func TestRuntimeRouterExecutesSlackAgainstAnExactlySelectedChannel(t *testing.T) {
	routes := testProviderRoutes(t, "slack", testRouteResource(
		"slack.channel", "C12345678", "releases", "slack.channel_id", "C12345678",
	))
	var captured runtimecontract.ProviderExecutionRequest
	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		r *http.Request,
	) {
		switch r.URL.Path {
		case "/v1/runtime/providers/slack/execute":
			if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
				t.Errorf("decode Slack execution request: %v", err)
			}
			_, _ = fmt.Fprintf(
				w,
				`{"schema_version":1,"request_id":%q,"decision":{"schema_version":1,"decision_id":"Keydris-01K1X4Y5Z6A7B8C9D0E1F2G3H4","request_id":%q,"attempt_id":"01K1X4Y5Z6A7B8C9D0E1F2G3H5","correlation_id":"01K1X4Y5Z6A7B8C9D0E1F2G3H6","decided_at":"2026-07-30T12:00:00Z","decision":"allow","reason_code":"keydris_policy_allowed","obligations":[]},"execution_status":"succeeded","replayed":false,"error_code":null,"provider_response":{"status":200,"headers":{"content-type":"application/json","x-slack-req-id":"slack-7"},"body":{"ok":true,"channel":"C12345678","ts":"123.456"}}}`,
				captured.RequestID,
				captured.RequestID,
			)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	dp := &fakeDataPlane{}
	flow := testRoutesFlow(routes)
	flow.MCPMethod = ""
	flow.MCPAction = nil
	flow.ToolParams = json.RawMessage(
		`{"channel":"C12345678","text":"Phase 6 shipped"}`,
	)
	router := newRuntimeRouter(server.Client(), server.URL)
	if handled := router.handle(context.Background(), dp, flow); !handled {
		t.Fatal("Slack routed flow was delegated to the legacy router")
	}
	if dp.rejected || dp.providerResponse == nil ||
		dp.providerResponse.Status != http.StatusOK {
		t.Fatalf(
			"rejected=%v reason=%q response=%+v",
			dp.rejected,
			dp.rejectReason,
			dp.providerResponse,
		)
	}
	if captured.ResourceID != "88888888-8888-4888-8888-888888888888" ||
		captured.Request.Body["channel"] != "C12345678" {
		t.Fatalf("captured Slack request = %+v", captured)
	}
}

func TestRuntimeRouterRejectsSlackChannelOutsideSelection(t *testing.T) {
	routes := testProviderRoutes(t, "slack", testRouteResource(
		"slack.channel", "C12345678", "releases", "slack.channel_id", "C12345678",
	))
	dp := &fakeDataPlane{}
	flow := testRoutesFlow(routes)
	flow.MCPMethod = ""
	flow.MCPAction = nil
	flow.ToolParams = json.RawMessage(`{"channel":"C99999999","text":"hi"}`)
	newRuntimeRouter(http.DefaultClient, "http://keydris.invalid").handle(
		context.Background(),
		dp,
		flow,
	)
	if !dp.rejected || !strings.Contains(dp.rejectReason, "not selected") {
		t.Fatalf("rejected=%v reason=%q", dp.rejected, dp.rejectReason)
	}
}

func TestRuntimeRouterRelaysModernMCPGatewayResponse(t *testing.T) {
	routes := testMCPGatewayRoutes(t)
	var captured runtimecontract.MCPGatewayRequest
	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		r *http.Request,
	) {
		switch r.URL.Path {
		case "/v1/runtime/mcp/gateway":
			if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
				t.Errorf("decode MCP gateway request: %v", err)
			}
			_, _ = fmt.Fprintf(
				w,
				`{"schema_version":1,"request_id":%q,"decision":{"schema_version":1,"decision_id":"Keydris-01K1X4Y5Z6A7B8C9D0E1F2G3H4","request_id":%q,"attempt_id":"01K1X4Y5Z6A7B8C9D0E1F2G3H5","correlation_id":"01K1X4Y5Z6A7B8C9D0E1F2G3H6","decided_at":"2026-07-30T12:00:00Z","decision":"allow","reason_code":"keydris_policy_allowed","obligations":[]},"execution_status":"succeeded","replayed":false,"error_code":null,"mcp_response":{"jsonrpc":"2.0","id":"rpc-11","result":{"content":[{"type":"text","text":"42"}]}}}`,
				captured.RequestID,
				captured.RequestID,
			)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	dp := &fakeDataPlane{}
	flow := testRoutesFlow(routes)
	flow.MCPRequestID = json.RawMessage(`"rpc-11"`)
	router := newRuntimeRouter(server.Client(), server.URL)
	if handled := router.handle(context.Background(), dp, flow); !handled {
		t.Fatal("MCP gateway flow was delegated to the legacy router")
	}
	if dp.rejected || dp.providerResponse == nil ||
		dp.providerResponse.Status != http.StatusOK ||
		!strings.Contains(string(dp.providerResponse.Body), `"text":"42"`) {
		t.Fatalf(
			"rejected=%v reason=%q response=%+v",
			dp.rejected,
			dp.rejectReason,
			dp.providerResponse,
		)
	}
	if captured.Message.Method != "tools/call" ||
		string(captured.Message.ID) != `"rpc-11"` ||
		captured.ResourceID != "88888888-8888-4888-8888-888888888888" {
		t.Fatalf("captured MCP gateway request = %+v", captured)
	}
}

func TestRuntimeRouterFailsClosedOnKitReaderRoute(t *testing.T) {
	routes := testKitReaderRoutes(t, "/", "ready", nil)
	dp := &fakeDataPlane{}
	if !newRuntimeRouter(http.DefaultClient, "http://keydris.invalid").handle(
		context.Background(),
		dp,
		testRoutesFlow(routes),
	) {
		t.Fatal("kit reader flow was not handled")
	}
	if !dp.rejected || !strings.Contains(dp.rejectReason, "Kit Reader") {
		t.Fatalf("rejected=%v reason=%q", dp.rejected, dp.rejectReason)
	}
}

func TestRuntimeRouterFailsClosedOnZeroOrMultipleRouteMatches(t *testing.T) {
	t.Run("zero path matches on a managed origin", func(t *testing.T) {
		routes := testKitReaderRoutes(t, "/mcp", "ready", nil)
		dp := &fakeDataPlane{}
		newRuntimeRouter(http.DefaultClient, "http://keydris.invalid").handle(
			context.Background(),
			dp,
			testRoutesFlow(routes),
		)
		if !dp.rejected || !strings.Contains(dp.rejectReason, "no route") {
			t.Fatalf("rejected=%v reason=%q", dp.rejected, dp.rejectReason)
		}
	})

	t.Run("unmanaged origin passes through", func(t *testing.T) {
		routes := testKitReaderRoutes(t, "/", "ready", nil)
		dp := &fakeDataPlane{}
		flow := testRoutesFlow(routes)
		flow.OrigDst = netip.MustParseAddrPort("198.51.100.7:80")
		newRuntimeRouter(http.DefaultClient, "http://keydris.invalid").handle(
			context.Background(),
			dp,
			flow,
		)
		if dp.rejected || !dp.passedThrough {
			t.Fatalf("rejected=%v passed=%v reason=%q", dp.rejected, dp.passedThrough, dp.rejectReason)
		}
	})

	t.Run("multiple matches", func(t *testing.T) {
		routes := testKitReaderRoutes(t, "/", "ready", nil)
		duplicate := routes.Routes[0]
		duplicate.RouteID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
		duplicate.ConnectionID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
		routes.Routes = append(routes.Routes, duplicate)
		if err := routes.Validate(); err != nil {
			t.Fatal(err)
		}
		dp := &fakeDataPlane{}
		newRuntimeRouter(http.DefaultClient, "http://keydris.invalid").handle(
			context.Background(),
			dp,
			testRoutesFlow(routes),
		)
		if !dp.rejected || !strings.Contains(dp.rejectReason, "ambiguous") {
			t.Fatalf("rejected=%v reason=%q", dp.rejected, dp.rejectReason)
		}
	})
}

func TestRuntimeRouterBlocksUnavailableRouteLocally(t *testing.T) {
	reason := "keydris_action_unsupported"
	routes := testKitReaderRoutes(t, "/", "unavailable", &reason)
	dp := &fakeDataPlane{}
	newRuntimeRouter(http.DefaultClient, "http://keydris.invalid").handle(
		context.Background(),
		dp,
		testRoutesFlow(routes),
	)
	if !dp.rejected || !strings.Contains(dp.rejectReason, reason) {
		t.Fatalf("rejected=%v reason=%q", dp.rejected, dp.rejectReason)
	}
}

func TestGithubRepositoryFromPathUsesOnlyAnUnambiguousRESTIdentity(t *testing.T) {
	tests := []struct {
		path     string
		fullName string
		valid    bool
	}{
		{
			path:     "/repos/Acme/Renamed.Repository/pulls",
			fullName: "Acme/Renamed.Repository",
			valid:    true,
		},
		{
			path:     "/repos/acme/widgets/contents/src/index.go",
			fullName: "acme/widgets",
			valid:    true,
		},
		{path: "/graphql", valid: false},
		{path: "/repos/acme%2Fadmin/widgets/pulls", valid: false},
		{path: "/repos/acme/widgets?ref=main", valid: false},
		{path: "/repos/acme", valid: false},
	}

	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			fullName, err := githubRepositoryFromPath(test.path)
			if test.valid {
				if err != nil || fullName != test.fullName {
					t.Fatalf("full name = %q, err = %v", fullName, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("invalid path resolved to %q", fullName)
			}
		})
	}
}

func testRoutesFlow(routes runtimecontract.RuntimeRoutes) dataplane.Flow {
	return dataplane.Flow{
		OrigDst:   netip.MustParseAddrPort("192.0.2.10:80"),
		SVID:      "session-kit",
		Routes:    &routes,
		MCPMethod: "tools/call",
		MCPAction: &dataplane.MCPAction{
			ActionType:     "mcp.tool.call",
			ActionName:     "get_stats",
			ResourceType:   "mcp.tool",
			RoutingKeyType: "mcp.tool_name",
			RoutingValue:   "get_stats",
			Parameters:     map[string]any{"campus": "north"},
		},
	}
}

func testRouteResource(
	resourceType, externalID, displayName, keyType, keyValue string,
) runtimecontract.RouteResource {
	return runtimecontract.RouteResource{
		ResourceType: resourceType,
		ResourceID:   "88888888-8888-4888-8888-888888888888",
		ExternalID:   externalID,
		DisplayName:  displayName,
		Availability: "ready",
		RoutingKeys: []runtimecontract.RoutingKey{
			{KeyType: keyType, Value: keyValue},
		},
	}
}

func testKitReaderRoutes(
	t *testing.T,
	pathPrefix, availability string,
	reason *string,
) runtimecontract.RuntimeRoutes {
	t.Helper()
	routes := runtimecontract.RuntimeRoutes{
		SchemaVersion:  1,
		OrganizationID: "11111111-1111-4111-8111-111111111111",
		Agent: runtimecontract.RoutesAgent{
			AgentID:     "33333333-3333-4333-8333-333333333333",
			DisplayName: "Test agent",
		},
		Policy: &runtimecontract.RoutesPolicy{
			PolicyID:        "55555555-5555-4555-8555-555555555555",
			PolicyVersionID: "66666666-6666-4666-8666-666666666666",
			PolicyHash:      "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		Routes: []runtimecontract.RuntimeRoute{
			{
				RouteID:          "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
				DisplayName:      "Test MCP",
				Provider:         "mcp",
				ConnectionID:     "77777777-7777-4777-8777-777777777777",
				EnforcementMode:  "mcp_kit_reader",
				Availability:     availability,
				StatusReasonCode: reason,
				Matchers: []runtimecontract.RouteMatcher{
					{
						MatcherType: "http.origin",
						Attributes: json.RawMessage(fmt.Sprintf(
							`{"scheme":"http","host":"192.0.2.10","port":80,"path_prefix":%q}`,
							pathPrefix,
						)),
					},
				},
				Resources: []runtimecontract.RouteResource{testRouteResource(
					"mcp.tool", "get_stats", "Get stats", "mcp.tool_name", "get_stats",
				)},
				KitActionTokenEndpointPath: "/v1/runtime/mcp/kit-action-tokens",
				ServerAudience:             "99999999-9999-4999-8999-999999999999",
			},
		},
	}
	if err := routes.Validate(); err != nil {
		t.Fatal(err)
	}
	return routes
}

func testProviderRoutes(
	t *testing.T,
	provider string,
	resources ...runtimecontract.RouteResource,
) runtimecontract.RuntimeRoutes {
	t.Helper()
	routes := testKitReaderRoutes(t, "/", "ready", nil)
	route := &routes.Routes[0]
	route.DisplayName = "Test " + provider
	route.Provider = provider
	route.EnforcementMode = "provider_executor"
	route.RuntimeEndpointPath = "/v1/runtime/providers/" + provider + "/execute"
	route.KitActionTokenEndpointPath = ""
	route.ServerAudience = ""
	route.Resources = resources
	if err := routes.Validate(); err != nil {
		t.Fatal(err)
	}
	return routes
}

func testMCPGatewayRoutes(t *testing.T) runtimecontract.RuntimeRoutes {
	t.Helper()
	routes := testKitReaderRoutes(t, "/", "ready", nil)
	route := &routes.Routes[0]
	route.EnforcementMode = "mcp_gateway"
	route.RuntimeEndpointPath = "/v1/runtime/mcp/gateway"
	route.KitActionTokenEndpointPath = ""
	route.ServerAudience = ""
	if err := routes.Validate(); err != nil {
		t.Fatal(err)
	}
	return routes
}
