package runtimecontract

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// Runtime route discovery — the manifest's minimal replacement.
//
// One flat, advisory response (GET /v1/runtime/routes, KIT bearer) describing
// which origins the session's agent policy governs, which enforcement boundary
// handles each of them, and the stable resource identities needed to address
// provider executions. There is no revision, ETag, or pagination: the CLI
// fetches it once per session and every invariant it reports is re-enforced
// server-side at execution time.

// RoutesEndpointPath mirrors RUNTIME_ENDPOINT_PATHS.routes in
// @keydris/contracts.
const RoutesEndpointPath = "/v1/runtime/routes"

var (
	uuidPattern = regexp.MustCompile(
		`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-8][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)
	hashPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
)

// RuntimeRoutes is the decoded, validated /v1/runtime/routes response.
type RuntimeRoutes struct {
	SchemaVersion  int            `json:"schema_version"`
	OrganizationID string         `json:"organization_id"`
	Agent          RoutesAgent    `json:"agent"`
	Policy         *RoutesPolicy  `json:"policy"`
	Routes         []RuntimeRoute `json:"routes"`
}

type RoutesAgent struct {
	AgentID     string `json:"agent_id"`
	DisplayName string `json:"display_name"`
}

type RoutesPolicy struct {
	PolicyID        string `json:"policy_id"`
	PolicyVersionID string `json:"policy_version_id"`
	PolicyHash      string `json:"policy_hash"`
}

// RuntimeRoute is one governed origin and the enforcement boundary behind it.
// Endpoint-path fields are populated per enforcement mode; resources are
// carried inline (the manifest's separate resource index is gone).
type RuntimeRoute struct {
	RouteID          string          `json:"route_id"`
	DisplayName      string          `json:"display_name"`
	Provider         string          `json:"provider"`
	ConnectionID     string          `json:"connection_id"`
	EnforcementMode  string          `json:"enforcement_mode"`
	Availability     string          `json:"availability"`
	StatusReasonCode *string         `json:"status_reason_code"`
	Matchers         []RouteMatcher  `json:"matchers"`
	Resources        []RouteResource `json:"resources"`

	// provider_executor and mcp_gateway routes.
	RuntimeEndpointPath string `json:"runtime_endpoint_path,omitempty"`
	// mcp_kit_reader routes.
	KitActionTokenEndpointPath string `json:"kit_action_token_endpoint_path,omitempty"`

	origins []OriginMatcher
}

type RouteMatcher struct {
	MatcherType string          `json:"matcher_type"`
	Attributes  json.RawMessage `json:"attributes"`
}

// OriginMatcher is a parsed, normalized http.origin matcher.
type OriginMatcher struct {
	Scheme     string
	Host       string
	Port       int
	PathPrefix string
}

// RouteResource is one selected, stably-identified provider resource.
type RouteResource struct {
	ResourceType     string       `json:"resource_type"`
	ResourceID       string       `json:"resource_id"`
	ExternalID       string       `json:"external_id"`
	DisplayName      string       `json:"display_name"`
	Availability     string       `json:"availability"`
	StatusReasonCode *string      `json:"status_reason_code"`
	RoutingKeys      []RoutingKey `json:"routing_keys"`
}

type RoutingKey struct {
	KeyType string `json:"key_type"`
	Value   string `json:"value"`
}

// FetchRuntimeRoutes retrieves and validates the session's governed routes.
func FetchRuntimeRoutes(
	ctx context.Context,
	client *http.Client,
	baseURL string,
	kitToken string,
) (*RuntimeRoutes, error) {
	endpoint, err := trustedRuntimeURL(baseURL, RoutesEndpointPath)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+kitToken)
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch runtime routes: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("runtime routes returned %s", response.Status)
	}
	raw, err := readBounded(response.Body, maxResponseBytes, "runtime routes")
	if err != nil {
		return nil, err
	}
	return DecodeRuntimeRoutes(raw)
}

// DecodeRuntimeRoutes strictly decodes and validates a routes response.
func DecodeRuntimeRoutes(raw []byte) (*RuntimeRoutes, error) {
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return nil, err
	}
	var routes RuntimeRoutes
	if err := decodeStrict(raw, &routes); err != nil {
		return nil, fmt.Errorf("decode runtime routes: %w", err)
	}
	if err := routes.Validate(); err != nil {
		return nil, err
	}
	return &routes, nil
}

// Validate checks every server-supplied invariant the CLI depends on and
// parses each route's origin matchers.
func (routes *RuntimeRoutes) Validate() error {
	if routes == nil {
		return fmt.Errorf("runtime routes are missing")
	}
	if routes.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported runtime routes schema version %d", routes.SchemaVersion)
	}
	if !uuidPattern.MatchString(routes.OrganizationID) ||
		!uuidPattern.MatchString(routes.Agent.AgentID) ||
		routes.Agent.DisplayName == "" {
		return fmt.Errorf("invalid runtime routes identity")
	}
	if routes.Policy != nil {
		if !uuidPattern.MatchString(routes.Policy.PolicyID) ||
			!uuidPattern.MatchString(routes.Policy.PolicyVersionID) ||
			!hashPattern.MatchString(routes.Policy.PolicyHash) {
			return fmt.Errorf("invalid runtime routes policy")
		}
	}
	seen := make(map[string]bool, len(routes.Routes))
	for index := range routes.Routes {
		route := &routes.Routes[index]
		if err := route.validate(); err != nil {
			return fmt.Errorf("route %d: %w", index, err)
		}
		if seen[route.RouteID] {
			return fmt.Errorf("duplicate route id %s", route.RouteID)
		}
		seen[route.RouteID] = true
	}
	return nil
}

func (route *RuntimeRoute) validate() error {
	if !uuidPattern.MatchString(route.RouteID) ||
		!uuidPattern.MatchString(route.ConnectionID) ||
		route.Provider == "" || route.DisplayName == "" {
		return fmt.Errorf("invalid route identity")
	}
	if route.Availability == "" {
		return fmt.Errorf("route has no availability")
	}
	route.origins = route.origins[:0]
	for _, matcher := range route.Matchers {
		switch matcher.MatcherType {
		case "http.origin":
			origin, err := parseOriginMatcher(matcher.Attributes)
			if err != nil {
				return err
			}
			route.origins = append(route.origins, origin)
		default:
			return fmt.Errorf("unsupported matcher type %q", matcher.MatcherType)
		}
	}
	if len(route.origins) == 0 {
		return fmt.Errorf("route has no HTTP origin matcher")
	}
	switch route.EnforcementMode {
	case "provider_executor", "mcp_gateway":
		if !validRuntimePath(route.RuntimeEndpointPath) {
			return fmt.Errorf("invalid runtime endpoint path")
		}
	case "mcp_kit_reader":
		if !validRuntimePath(route.KitActionTokenEndpointPath) {
			return fmt.Errorf("invalid kit reader route")
		}
	default:
		return fmt.Errorf("unsupported enforcement mode %q", route.EnforcementMode)
	}
	for _, resource := range route.Resources {
		if !uuidPattern.MatchString(resource.ResourceID) ||
			resource.ResourceType == "" || resource.ExternalID == "" ||
			resource.DisplayName == "" || resource.Availability == "" ||
			len(resource.RoutingKeys) == 0 {
			return fmt.Errorf("invalid route resource")
		}
		for _, key := range resource.RoutingKeys {
			if key.KeyType == "" || key.Value == "" {
				return fmt.Errorf("invalid resource routing key")
			}
		}
	}
	return nil
}

func parseOriginMatcher(raw json.RawMessage) (OriginMatcher, error) {
	var attributes struct {
		Scheme     string `json:"scheme"`
		Host       string `json:"host"`
		Port       int    `json:"port"`
		PathPrefix string `json:"path_prefix"`
	}
	if err := decodeStrict(raw, &attributes); err != nil {
		return OriginMatcher{}, fmt.Errorf("invalid http.origin matcher: %w", err)
	}
	attributes.Scheme = strings.ToLower(attributes.Scheme)
	attributes.Host = strings.TrimSuffix(strings.ToLower(attributes.Host), ".")
	if (attributes.Scheme != "http" && attributes.Scheme != "https") ||
		attributes.Host == "" || attributes.Port < 1 || attributes.Port > 65535 ||
		!strings.HasPrefix(attributes.PathPrefix, "/") {
		return OriginMatcher{}, fmt.Errorf("invalid http.origin matcher")
	}
	return OriginMatcher{
		Scheme: attributes.Scheme, Host: attributes.Host, Port: attributes.Port,
		PathPrefix: attributes.PathPrefix,
	}, nil
}

func (matcher OriginMatcher) matchesOrigin(scheme, host string, port int) bool {
	return matcher.Scheme == scheme &&
		matcher.Host == strings.TrimSuffix(strings.ToLower(host), ".") &&
		matcher.Port == port
}

// RoutesFor returns every route whose matcher covers the request origin+path.
func (routes RuntimeRoutes) RoutesFor(
	scheme, host string,
	port int,
	path string,
) []RuntimeRoute {
	var matches []RuntimeRoute
	for _, route := range routes.Routes {
		for _, origin := range route.origins {
			if origin.matchesOrigin(scheme, host, port) &&
				pathHasPrefix(path, origin.PathPrefix) {
				matches = append(matches, route)
				break
			}
		}
	}
	return matches
}

// ManagesOrigin reports whether any route governs the origin, regardless of
// path — the boundary between "reject unrouted paths" and "pass through".
func (routes RuntimeRoutes) ManagesOrigin(scheme, host string, port int) bool {
	for _, route := range routes.Routes {
		for _, origin := range route.origins {
			if origin.matchesOrigin(scheme, host, port) {
				return true
			}
		}
	}
	return false
}

func pathHasPrefix(path, prefix string) bool {
	if prefix == "/" {
		return true
	}
	return path == prefix ||
		strings.HasPrefix(path, strings.TrimSuffix(prefix, "/")+"/")
}

// ResourceByKey returns the route resource carrying an exact routing key.
func (route RuntimeRoute) ResourceByKey(keyType, value string) (*RouteResource, bool) {
	return route.resourceByKey(keyType, value, false)
}

// ResourceByKeyFold matches the routing-key value case-insensitively, for
// providers with case-insensitive identifiers (github.full_name).
func (route RuntimeRoute) ResourceByKeyFold(keyType, value string) (*RouteResource, bool) {
	return route.resourceByKey(keyType, value, true)
}

func (route RuntimeRoute) resourceByKey(
	keyType, value string,
	fold bool,
) (*RouteResource, bool) {
	for index := range route.Resources {
		resource := &route.Resources[index]
		for _, key := range resource.RoutingKeys {
			if key.KeyType != keyType {
				continue
			}
			if key.Value == value || (fold && strings.EqualFold(key.Value, value)) {
				return resource, true
			}
		}
	}
	return nil, false
}
