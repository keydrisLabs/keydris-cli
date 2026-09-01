package dataplane

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/keydrisLabs/keydris-cli/internal/runtimecontract"
)

const maxToolParamsBytes = 1 << 20

// applyRequestMetadata captures the HTTP operation and a bounded JSON body for
// the broker's policy evaluation. Reading the body here is safe because it is
// restored before the request is forwarded upstream.
func applyRequestMetadata(f *Flow, req *http.Request) error {
	if f == nil || req == nil {
		return nil
	}

	path := req.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	f.ToolCall = req.Method + " " + path

	if req.Body == nil || req.Body == http.NoBody || !isJSONContentType(req.Header.Get("Content-Type")) {
		return nil
	}

	body, err := io.ReadAll(io.LimitReader(req.Body, maxToolParamsBytes+1))
	closeErr := req.Body.Close()
	req.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("read tool params: %w", err)
	}
	if closeErr != nil {
		return fmt.Errorf("close tool params body: %w", closeErr)
	}
	if len(body) > maxToolParamsBytes {
		return fmt.Errorf("tool params exceed %d bytes", maxToolParamsBytes)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	if !json.Valid(body) {
		return fmt.Errorf("tool params are not valid JSON")
	}
	if err := runtimecontract.RejectDuplicateJSONKeys(body); err != nil {
		return fmt.Errorf("tool params contain ambiguous JSON: %w", err)
	}

	f.ToolParams = append(json.RawMessage(nil), body...)
	applyMCPToolMetadata(f, body)
	return nil
}

// applyMCPToolMetadata promotes supported Streamable HTTP MCP actions from the
// JSON-RPC envelope into an immutable, request-derived routing intent.
func applyMCPToolMetadata(f *Flow, body []byte) {
	var rpc struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&rpc); err != nil || rpc.JSONRPC != "2.0" || rpc.Method == "" {
		return
	}
	f.MCPMethod = rpc.Method

	switch {
	case rpc.Method == "tools/call":
		if applyMCPToolCall(f, rpc.Params) {
			f.MCPRequestID = append(json.RawMessage(nil), rpc.ID...)
		}
	case rpc.Method == "resources/read":
		if applyMCPResourceRead(f, rpc.Params) {
			f.MCPRequestID = append(json.RawMessage(nil), rpc.ID...)
		}
	case runtimecontract.IsMCPSessionMethod(rpc.Method):
		// No MCPAction: the relay addresses the connection, not a resource —
		// discovery cannot name a tool. Only the id and params are forwarded.
		f.MCPRequestID = append(json.RawMessage(nil), rpc.ID...)
		f.MCPParams = append(json.RawMessage(nil), rpc.Params...)
	}
}

func applyMCPToolCall(f *Flow, raw json.RawMessage) bool {
	var params struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&params); err != nil || params.Name == "" {
		return false
	}
	f.ToolCall = params.Name
	if params.Arguments == nil {
		params.Arguments = map[string]any{}
	}
	arguments, err := json.Marshal(params.Arguments)
	if err != nil {
		return false
	}
	f.ToolParams = arguments
	f.MCPAction = &MCPAction{
		ActionType:     "mcp.tool.call",
		ActionName:     params.Name,
		ResourceType:   "mcp.tool",
		RoutingKeyType: "mcp.tool_name",
		RoutingValue:   params.Name,
		Parameters:     params.Arguments,
	}
	return true
}

func applyMCPResourceRead(f *Flow, raw json.RawMessage) bool {
	var params struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(raw, &params); err != nil || params.URI == "" {
		return false
	}
	parameters := map[string]any{"uri": params.URI}
	encoded, err := json.Marshal(parameters)
	if err != nil {
		return false
	}
	f.ToolCall = params.URI
	f.ToolParams = encoded
	f.MCPAction = &MCPAction{
		ActionType:     "mcp.resource.read",
		ActionName:     params.URI,
		ResourceType:   "mcp.resource",
		RoutingKeyType: "mcp.resource_uri",
		RoutingValue:   params.URI,
		Parameters:     parameters,
	}
	return true
}

func injectMCPActionToken(req *http.Request, token string) error {
	if req == nil || token == "" || req.Body == nil || req.Body == http.NoBody {
		return fmt.Errorf("cannot inject KIT action token into this request")
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, maxToolParamsBytes+1))
	if err != nil {
		return fmt.Errorf("read MCP request for token injection: %w", err)
	}
	if len(body) > maxToolParamsBytes {
		return fmt.Errorf("MCP request exceeds %d bytes", maxToolParamsBytes)
	}
	if err := runtimecontract.RejectDuplicateJSONKeys(body); err != nil {
		return fmt.Errorf("MCP request contains ambiguous JSON: %w", err)
	}

	var envelope map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&envelope); err != nil {
		return fmt.Errorf("decode MCP request for token injection: %w", err)
	}
	params, ok := envelope["params"].(map[string]any)
	if !ok {
		return fmt.Errorf("MCP request params must be an object")
	}
	meta, exists := params["_meta"]
	if !exists {
		meta = map[string]any{}
		params["_meta"] = meta
	}
	metaObject, ok := meta.(map[string]any)
	if !ok {
		return fmt.Errorf("MCP request _meta must be an object")
	}
	metaObject["keydris/kit_action_token"] = token

	updated, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("encode MCP request with token: %w", err)
	}
	req.Body = io.NopCloser(bytes.NewReader(updated))
	req.ContentLength = int64(len(updated))
	req.TransferEncoding = nil
	req.Header.Set("Content-Length", fmt.Sprintf("%d", len(updated)))
	req.Header.Del("Transfer-Encoding")
	return nil
}

func isJSONContentType(value string) bool {
	if value == "" {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return false
	}
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}
