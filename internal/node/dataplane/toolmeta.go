package dataplane

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
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

	f.ToolParams = append(json.RawMessage(nil), body...)
	applyMCPToolMetadata(f, body)
	return nil
}

// applyMCPToolMetadata promotes a Streamable HTTP MCP tools/call request from
// its JSON-RPC envelope into the authorization contract. Non-tool MCP messages
// (initialize, tools/list, notifications) retain the HTTP fallback metadata.
func applyMCPToolMetadata(f *Flow, body []byte) {
	var rpc struct {
		JSONRPC string          `json:"jsonrpc"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(body, &rpc); err != nil || rpc.JSONRPC != "2.0" || rpc.Method != "tools/call" {
		return
	}

	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(rpc.Params, &params); err != nil || params.Name == "" {
		return
	}

	f.ToolCall = params.Name
	if len(params.Arguments) == 0 {
		f.ToolParams = json.RawMessage(`{}`)
		return
	}
	f.ToolParams = append(json.RawMessage(nil), params.Arguments...)
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
