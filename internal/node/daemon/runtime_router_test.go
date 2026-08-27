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
	"time"

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

func TestRuntimeRouterPassesThroughMCPLifecycle(t *testing.T) {
	routes := testMCPGatewayRoutes(t)
	// `initialize` and `tools/list` are deliberately absent: the upstream server
	// can require auth for both, so the handshake is answered locally and
	// discovery is relayed through the gateway.
	methods := []string{
		"notifications/initialized",
		"ping",
		"prompts/list",
		"resources/list",
	}

	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			dp := &fakeDataPlane{}
			flow := testRoutesFlow(routes)
			flow.MCPMethod = method
			flow.MCPAction = nil
			flow.MCPRequestID = nil

			handled := newRuntimeRouter(
				http.DefaultClient,
				"http://keydris.invalid",
			).handle(context.Background(), dp, flow)

			if !handled || dp.rejected || !dp.passedThrough ||
				dp.providerResponse != nil {
				t.Fatalf(
					"handled=%v rejected=%v passed=%v response=%+v reason=%q",
					handled,
					dp.rejected,
					dp.passedThrough,
					dp.providerResponse,
					dp.rejectReason,
				)
			}
		})
	}
}

// The handshake authorizes nothing, so it is answered locally rather than
// relayed — the upstream 401s it without a credential the client does not hold.
func TestRuntimeRouterAnswersMCPInitializeLocally(t *testing.T) {
	dp := &fakeDataPlane{}
	flow := testRoutesFlow(testMCPGatewayRoutes(t))
	flow.MCPMethod = "initialize"
	flow.MCPRequestID = json.RawMessage("7")

	handled := newRuntimeRouter(
		http.DefaultClient,
		"http://keydris.invalid",
	).handle(context.Background(), dp, flow)

	if !handled || dp.rejected || dp.passedThrough {
		t.Fatalf(
			"handled=%v rejected=%v passed=%v reason=%q",
			handled, dp.rejected, dp.passedThrough, dp.rejectReason,
		)
	}
	if dp.providerResponse == nil || dp.providerResponse.Status != http.StatusOK {
		t.Fatalf("no local response: %+v", dp.providerResponse)
	}
	var payload struct {
		ID     json.RawMessage `json:"id"`
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"result"`
	}
	if err := json.Unmarshal(dp.providerResponse.Body, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(payload.ID) != "7" {
		t.Fatalf("response id = %s, want the request id", payload.ID)
	}
	if payload.Result.ProtocolVersion != mcpProtocolVersion {
		t.Fatalf("protocolVersion = %q", payload.Result.ProtocolVersion)
	}
}

func TestMCPPassthroughReasonKeepsActionsGoverned(t *testing.T) {
	for _, method := range []string{
		"tools/call",
		"resources/read",
		"initialize",
		"tools/list",
		"unknown/method",
	} {
		t.Run(method, func(t *testing.T) {
			flow := dataplane.Flow{MCPMethod: method}
			if reason, ok := mcpPassthroughReason(flow); ok {
				t.Fatalf("method %q passed through as %q", method, reason)
			}
		})
	}
}

func TestRuntimeRouterMintsAndInjectsKitReaderActionToken(t *testing.T) {
	routes := testKitReaderRoutes(t, "/", "ready", nil)
	var captured runtimecontract.MintKitActionTokenRequest
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
			t.Errorf("decode mint request: %v", err)
		}
		_, _ = fmt.Fprintf(
			writer,
			`{"schema_version":1,"kit_action_token":"action-token","expires_at":%q}`,
			time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano),
		)
	}))
	defer server.Close()

	dp := &fakeDataPlane{}
	flow := testRoutesFlow(routes)
	flow.MCPRequestID = json.RawMessage(`7`)
	if !newRuntimeRouter(server.Client(), server.URL).handle(
		context.Background(),
		dp,
		flow,
	) {
		t.Fatal("kit reader flow was not handled")
	}
	if dp.rejected || dp.passedThrough || dp.actionToken != "action-token" {
		t.Fatalf(
			"rejected=%v passed=%v action_token=%q reason=%q",
			dp.rejected,
			dp.passedThrough,
			dp.actionToken,
			dp.rejectReason,
		)
	}
	if captured.Intent.ConnectionID != routes.Routes[0].ConnectionID ||
		captured.Intent.ActionName != "get_stats" ||
		captured.Intent.Resource.ResourceID !=
			"88888888-8888-4888-8888-888888888888" ||
		captured.RequestHash == "" {
		t.Fatalf("captured request = %+v", captured)
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
	if err := routes.Validate(); err != nil {
		t.Fatal(err)
	}
	return routes
}

// The paths Claude Code actually probes, taken from proxy.log. It sends these
// BEFORE any MCP request, so answering 403 made it abandon the server without
// ever reaching initialize — which is why 404 is the fix, not a nicety.
//
// Only the decision is asserted here: dataplane.Flow keeps its parsed request
// private with no test seam, so a flow with a specific path cannot be built from
// this package. The wiring is covered by the end-to-end check in the PR.
func TestIsOAuthDiscoveryPath(t *testing.T) {
	for _, path := range []string{
		"/.well-known/oauth-protected-resource",
		"/.well-known/oauth-protected-resource/mcp",
		"/.well-known/oauth-authorization-server",
		"/.well-known/oauth-authorization-server/mcp",
		"/.well-known/openid-configuration",
		"/.well-known/openid-configuration/mcp",
		"/register",
		"/oauth/register",
	} {
		if !isOAuthDiscoveryPath(path) {
			t.Errorf("isOAuthDiscoveryPath(%q) = false, want true", path)
		}
	}
	// A genuinely ungoverned path must still reject: softening the blanket would
	// lose the signal that Keydris deliberately blocked something.
	for _, path := range []string{
		"/mcp/",
		"/graphql",
		"/.well-known/acme-challenge",
		"/registered-users",
		"/",
	} {
		if isOAuthDiscoveryPath(path) {
			t.Errorf("isOAuthDiscoveryPath(%q) = true, want false", path)
		}
	}
}
