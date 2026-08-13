package daemon

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/keydrisLabs/keydris-cli/internal/node/dataplane"
	"github.com/keydrisLabs/keydris-cli/internal/runtimecontract"
)

const runtimeCallTimeout = 15 * time.Second

var slackChannelIDPattern = regexp.MustCompile(`^C[A-Z0-9]{8,}$`)

// runtimeRouter enforces a session's governed routes (fetched once per session
// from GET /v1/runtime/routes) before the legacy destination broker path.
// Routes carry their selected resources inline, so routing needs no further
// control-plane round trips; every decision it forwards is re-enforced
// server-side at execution time.
type runtimeRouter struct {
	client  *http.Client
	baseURL string
}

func newRuntimeRouter(client *http.Client, baseURL string) *runtimeRouter {
	return &runtimeRouter{client: client, baseURL: baseURL}
}

// handle returns true when the session's routes owned the routing decision. A
// false return delegates to the pre-routes compatibility path.
func (router *runtimeRouter) handle(
	ctx context.Context,
	dp dataplane.DataPlane,
	flow dataplane.Flow,
) bool {
	routes := flow.Routes
	if routes == nil {
		return false
	}
	if router == nil || router.client == nil {
		rejectRuntime(dp, flow, "runtime enforcement unavailable")
		return true
	}

	matches := routes.RoutesFor(
		flow.Scheme(),
		flow.DstHost(),
		flow.DstPort(),
		flow.RequestPath(),
	)
	switch len(matches) {
	case 0:
		if routes.ManagesOrigin(flow.Scheme(), flow.DstHost(), flow.DstPort()) {
			rejectRuntime(dp, flow, "runtime routes have no route for this path")
		} else {
			passThroughRuntime(dp, flow, "unmanaged_origin")
		}
		return true
	case 1:
	default:
		rejectRuntime(dp, flow, "runtime route is ambiguous")
		return true
	}

	route := matches[0]
	log.Printf(
		"RUNTIME_ROUTE dst=%s method=%s path=%s mcp_method=%q route=%s mode=%s",
		flow.DstString(),
		flow.RequestMethod(),
		flow.RequestPath(),
		flow.MCPMethod,
		route.RouteID,
		route.EnforcementMode,
	)
	if route.Availability != "ready" {
		reason := "runtime route unavailable"
		if route.StatusReasonCode != nil {
			reason += ": " + *route.StatusReasonCode
		}
		rejectRuntime(dp, flow, reason)
		return true
	}

	switch route.EnforcementMode {
	case "provider_executor":
		logRuntimeGovern(flow, route)
		router.handleProviderExecutor(ctx, dp, flow, route)
	case "mcp_gateway":
		if reason, ok := mcpPassthroughReason(flow); ok {
			passThroughRuntime(dp, flow, reason)
		} else {
			logRuntimeGovern(flow, route)
			router.handleMCPGateway(ctx, dp, flow, route)
		}
	case "mcp_kit_reader":
		// The Kit Reader bridge is not part of this CLI build; fail closed
		// rather than passing governed traffic through unenforced.
		rejectRuntime(dp, flow, "Kit Reader enforcement is not supported by this CLI")
	default:
		rejectRuntime(dp, flow, "runtime enforcement mode is unsupported")
	}
	return true
}

func logRuntimeGovern(
	flow dataplane.Flow,
	route runtimecontract.RuntimeRoute,
) {
	log.Printf(
		"RUNTIME_GOVERN dst=%s method=%s path=%s mcp_method=%q route=%s mode=%s",
		flow.DstString(),
		flow.RequestMethod(),
		flow.RequestPath(),
		flow.MCPMethod,
		route.RouteID,
		route.EnforcementMode,
	)
}

func mcpPassthroughReason(flow dataplane.Flow) (string, bool) {
	switch flow.RequestMethod() {
	case http.MethodGet, http.MethodHead, http.MethodDelete:
		if flow.MCPMethod == "" {
			return "mcp_transport", true
		}
	}

	switch flow.MCPMethod {
	case "initialize",
		"notifications/initialized",
		"ping",
		"tools/list",
		"prompts/list",
		"prompts/get",
		"resources/list",
		"resources/templates/list",
		"resources/subscribe",
		"resources/unsubscribe",
		"completion/complete",
		"logging/setLevel",
		"notifications/cancelled",
		"notifications/progress",
		"notifications/roots/list_changed":
		return "mcp_lifecycle", true
	default:
		return "", false
	}
}

func passThroughRuntime(
	dp dataplane.DataPlane,
	flow dataplane.Flow,
	reason string,
) {
	log.Printf(
		"RUNTIME_PASSTHROUGH dst=%s method=%s path=%s mcp_method=%q reason=%s",
		flow.DstString(),
		flow.RequestMethod(),
		flow.RequestPath(),
		flow.MCPMethod,
		reason,
	)
	if err := dp.PassThrough(flow); err != nil {
		log.Printf(
			"RUNTIME_ERROR dst=%s method=%s path=%s operation=passthrough error=%q",
			flow.DstString(),
			flow.RequestMethod(),
			flow.RequestPath(),
			err,
		)
	}
}

func rejectRuntime(
	dp dataplane.DataPlane,
	flow dataplane.Flow,
	reason string,
) {
	log.Printf(
		"RUNTIME_DENY dst=%s method=%s path=%s mcp_method=%q reason=%q",
		flow.DstString(),
		flow.RequestMethod(),
		flow.RequestPath(),
		flow.MCPMethod,
		reason,
	)
	_ = dp.Reject(flow, reason)
}

func (router *runtimeRouter) handleProviderExecutor(
	parent context.Context,
	dp dataplane.DataPlane,
	flow dataplane.Flow,
	route runtimecontract.RuntimeRoute,
) {
	if flow.MetadataError != "" {
		rejectRuntime(dp, flow, "invalid provider request metadata")
		return
	}

	target, body, err := resolveProviderExecutionTarget(route.Provider, flow)
	if err != nil {
		rejectRuntime(dp, flow, err.Error())
		return
	}

	resource, found := runtimeResourceForProviderTarget(route, target)
	if !found || resource.ResourceType != target.resourceType {
		rejectRuntime(
			dp, flow,
			target.providerLabel+" resource is not selected for this session",
		)
		return
	}
	if resource.Availability != "ready" {
		rejectRuntime(dp, flow, target.providerLabel+" resource is unavailable")
		return
	}

	ctx, cancel := context.WithTimeout(parent, runtimeCallTimeout)
	defer cancel()
	requestID := newRuntimeRequestID()
	result, err := runtimecontract.ExecuteProvider(
		ctx,
		router.client,
		router.baseURL,
		flow.SVID,
		route.RuntimeEndpointPath,
		runtimecontract.ProviderExecutionRequest{
			SchemaVersion: runtimecontract.SchemaVersion,
			RequestID:     requestID,
			ConnectionID:  route.ConnectionID,
			ResourceID:    resource.ResourceID,
			Request: runtimecontract.ProviderHTTPRequest{
				Method:  flow.RequestMethod(),
				Path:    flow.RequestPath(),
				Query:   flow.RequestQuery(),
				Headers: flow.ProviderRequestHeaders(),
				Body:    body,
			},
		},
	)
	if err != nil {
		log.Printf("runtime provider execution route=%s: %v", route.RouteID, err)
		rejectRuntime(dp, flow, target.providerLabel+" execution unavailable")
		return
	}
	if result.ExecutionStatus == "denied" {
		rejectRuntime(
			dp, flow,
			target.providerLabel+" request denied: "+result.Decision.ReasonCode,
		)
		return
	}
	if result.ProviderResponse == nil {
		reason := target.providerLabel + " execution failed"
		if result.ErrorCode != nil {
			reason += ": " + *result.ErrorCode
		}
		rejectRuntime(dp, flow, reason)
		return
	}
	if err := dp.Respond(flow, *result.ProviderResponse); err != nil {
		log.Printf("relay provider response route=%s: %v", route.RouteID, err)
	}
}

type providerExecutionTarget struct {
	providerLabel  string
	resourceType   string
	routingKeyType string
	routingValue   string
	foldCase       bool
}

func resolveProviderExecutionTarget(
	provider string,
	flow dataplane.Flow,
) (providerExecutionTarget, map[string]any, error) {
	body, err := decodeProviderRequestBody(flow.ToolParams)
	if err != nil {
		return providerExecutionTarget{}, nil, fmt.Errorf(
			"%s request body must be a JSON object",
			providerDisplayName(provider),
		)
	}
	switch provider {
	case "github":
		fullName, pathErr := githubRepositoryFromPath(flow.RequestPath())
		if pathErr != nil {
			return providerExecutionTarget{}, nil, fmt.Errorf(
				"unsupported GitHub request path",
			)
		}
		return providerExecutionTarget{
			providerLabel:  "GitHub",
			resourceType:   "github.repository",
			routingKeyType: "github.full_name",
			routingValue:   fullName,
			foldCase:       true,
		}, body, nil
	case "slack":
		channel, ok := body["channel"].(string)
		if !ok || !slackChannelIDPattern.MatchString(channel) {
			return providerExecutionTarget{}, nil, fmt.Errorf(
				"Slack request must identify a public channel",
			)
		}
		return providerExecutionTarget{
			providerLabel:  "Slack",
			resourceType:   "slack.channel",
			routingKeyType: "slack.channel_id",
			routingValue:   channel,
		}, body, nil
	default:
		return providerExecutionTarget{}, nil, fmt.Errorf(
			"provider executor is not supported by this CLI",
		)
	}
}

func decodeProviderRequestBody(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var body map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&body); err != nil || body == nil {
		return nil, fmt.Errorf("request body is not an object")
	}
	return body, nil
}

func runtimeResourceForProviderTarget(
	route runtimecontract.RuntimeRoute,
	target providerExecutionTarget,
) (*runtimecontract.RouteResource, bool) {
	if target.foldCase {
		return route.ResourceByKeyFold(target.routingKeyType, target.routingValue)
	}
	return route.ResourceByKey(target.routingKeyType, target.routingValue)
}

func providerDisplayName(provider string) string {
	switch provider {
	case "github":
		return "GitHub"
	case "slack":
		return "Slack"
	default:
		return "Provider"
	}
}

func (router *runtimeRouter) handleMCPGateway(
	parent context.Context,
	dp dataplane.DataPlane,
	flow dataplane.Flow,
	route runtimecontract.RuntimeRoute,
) {
	if flow.MetadataError != "" ||
		flow.MCPAction == nil ||
		len(flow.MCPRequestID) == 0 {
		rejectRuntime(dp, flow, "invalid MCP gateway request metadata")
		return
	}

	action := flow.MCPAction
	resource, found := route.ResourceByKey(
		action.RoutingKeyType,
		action.RoutingValue,
	)
	if !found ||
		resource.ResourceType != action.ResourceType ||
		resource.ExternalID != action.RoutingValue {
		rejectRuntime(dp, flow, "MCP resource is not selected for this session")
		return
	}
	if resource.Availability != "ready" {
		rejectRuntime(dp, flow, "MCP resource is unavailable")
		return
	}

	message, err := mcpGatewayMessage(flow)
	if err != nil {
		rejectRuntime(dp, flow, "invalid MCP gateway request metadata")
		return
	}
	ctx, cancel := context.WithTimeout(parent, runtimeCallTimeout)
	defer cancel()
	result, err := runtimecontract.ExecuteMCPGateway(
		ctx,
		router.client,
		router.baseURL,
		flow.SVID,
		route.RuntimeEndpointPath,
		runtimecontract.MCPGatewayRequest{
			SchemaVersion: runtimecontract.SchemaVersion,
			RequestID:     newRuntimeRequestID(),
			ConnectionID:  route.ConnectionID,
			ResourceID:    resource.ResourceID,
			Message:       message,
		},
	)
	if err != nil {
		log.Printf("runtime MCP gateway route=%s: %v", route.RouteID, err)
		rejectRuntime(dp, flow, "MCP gateway unavailable")
		return
	}
	if result.ExecutionStatus == "denied" {
		rejectRuntime(
			dp, flow,
			"MCP request denied: "+result.Decision.ReasonCode,
		)
		return
	}
	if result.MCPResponse == nil {
		reason := "MCP gateway execution failed"
		if result.ErrorCode != nil {
			reason += ": " + *result.ErrorCode
		}
		rejectRuntime(dp, flow, reason)
		return
	}
	body, err := json.Marshal(result.MCPResponse)
	if err != nil {
		rejectRuntime(dp, flow, "MCP gateway response encoding failed")
		return
	}
	if err := dp.Respond(flow, runtimecontract.ProviderHTTPResponse{
		Status: http.StatusOK,
		Headers: map[string]string{
			"content-type": "application/json",
		},
		Body: body,
	}); err != nil {
		log.Printf("relay MCP gateway response route=%s: %v", route.RouteID, err)
	}
}

func mcpGatewayMessage(flow dataplane.Flow) (runtimecontract.MCPGatewayMessage, error) {
	message := runtimecontract.MCPGatewayMessage{
		JSONRPC: "2.0",
		ID:      append(json.RawMessage(nil), flow.MCPRequestID...),
		Method:  flow.MCPMethod,
	}
	switch flow.MCPMethod {
	case "tools/call":
		message.Params = runtimecontract.MCPGatewayParams{
			Name:      flow.MCPAction.ActionName,
			Arguments: flow.MCPAction.Parameters,
		}
	case "resources/read":
		uri, ok := flow.MCPAction.Parameters["uri"].(string)
		if !ok || uri != flow.MCPAction.ActionName {
			return runtimecontract.MCPGatewayMessage{}, fmt.Errorf(
				"invalid MCP resource URI",
			)
		}
		message.Params = runtimecontract.MCPGatewayParams{URI: uri}
	default:
		return runtimecontract.MCPGatewayMessage{}, fmt.Errorf(
			"unsupported MCP method",
		)
	}
	return message, nil
}

func newRuntimeRequestID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err == nil {
		return "cli-" + hex.EncodeToString(value[:])
	}
	return fmt.Sprintf("cli-%d", time.Now().UnixNano())
}

func githubRepositoryFromPath(path string) (string, error) {
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\\?#") {
		return "", fmt.Errorf("invalid GitHub path")
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) < 3 || parts[0] != "repos" {
		return "", fmt.Errorf("GitHub path does not identify a repository")
	}
	owner, err := url.PathUnescape(parts[1])
	if err != nil {
		return "", err
	}
	repository, err := url.PathUnescape(parts[2])
	if err != nil {
		return "", err
	}
	if owner == "" || repository == "" ||
		strings.ContainsAny(owner, "/\\") ||
		strings.ContainsAny(repository, "/\\") {
		return "", fmt.Errorf("invalid GitHub repository")
	}
	return owner + "/" + repository, nil
}
